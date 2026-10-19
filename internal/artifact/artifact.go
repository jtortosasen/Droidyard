// Package artifact is the core's artifact store: opaque, URI-shaped references to
// bytes a capability produced (ADR-0012). The ephemeral class is in memory: a
// 15-minute lifetime from creation (the same as operation retention, ADR-0020), LRU
// eviction under a byte budget, and a timer that releases expired bytes without a
// read. Spill to the cache directory is deliberately not implemented (ADR-0012,
// Amended 2026-10-04). The durable class (durable.go) writes a file under
// <state>/ws/<wsid>/artifacts/ that survives a restart and is owned by a record.
//
// A reference is "droidyard://artifact/<id>" and consumers never parse it. An
// ephemeral id is "e.<epoch>.<counter>.<random>": the class, the daemon epoch the
// event bus mints, a per-epoch counter from 1, and 48 random bits. The counter is what
// lets a miss be classified without tombstones: a counter at or below the highest
// issued was once valid (ARTIFACT_EXPIRED), one above it never was
// (ARTIFACT_NOT_FOUND). A durable id is "d.<workspace>.<owner>" and names the file
// under that workspace's artifacts/ rather than an in-memory slot (durable.go).
package artifact

import (
	"container/list"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

const (
	// RefPrefix is the scheme and host of every reference.
	RefPrefix = "droidyard://artifact/"
	// DefaultTTL is the ephemeral lifetime, counted from creation (ADR-0012).
	DefaultTTL = 15 * time.Minute
	// DefaultBudget is ADR-0012's working byte budget, kept by plan m1e Q5.
	DefaultBudget int64 = 64 << 20

	classEphemeral = "e"
	classDurable   = "d"
	randomHexLen   = 12
)

// ErrTooLarge is returned by Put for an artifact bigger than the whole budget: it
// could only be stored by evicting everything, and even then it would not fit.
var ErrTooLarge = errors.New("artifact larger than the store's byte budget")

// Durability is the artifact's class.
type Durability string

// DurabilityEphemeral lives in memory for the TTL.
const DurabilityEphemeral Durability = "ephemeral"

// DurabilityDurable is written under the workspace's artifacts/ and survives a restart;
// it lives and dies with the record that owns it (ADR-0012 §Durability classes).
const DurabilityDurable Durability = "durable"

// Ref is ADR-0012's ArtifactRef record. Width and Height are set for images only. In
// this milestone every artifact is device-derived, so a result that carries a Ref is
// untrusted through its reference field (plan m1e R9; ADR-0015 §1): the tag is here so
// that every capability returning the record inherits it.
type Ref struct {
	Ref        string     `json:"ref" jsonschema:"required" untrusted:"true"`
	MediaType  string     `json:"mediaType" jsonschema:"required"`
	Bytes      int        `json:"bytes" jsonschema:"required"`
	Durability Durability `json:"durability" jsonschema:"required"`
	ExpiresAt  time.Time  `json:"expiresAt" jsonschema:"required"`
	Width      int        `json:"width,omitempty"`
	Height     int        `json:"height,omitempty"`
	Untrusted  bool       `json:"untrusted" jsonschema:"required"`
}

// Inline is an image's ready-made inline delivery (EncodeInline's output at the default
// edge). A producer that decoded the image anyway hands it in, so the MCP adapter
// reads it instead of decoding the image again. It is held, and counted against the
// budget, with the artifact.
type Inline struct {
	Data      []byte
	MediaType string
	Width     int
	Height    int
}

// Item is what a producer hands to Put. Producer is the canonical name of the
// capability that made it; it stays in the store's entry and is not part of Ref.
type Item struct {
	Data      []byte
	MediaType string
	Width     int
	Height    int
	Untrusted bool
	Producer  string
	// Owner is who the artifact belongs to, "" for the daemon's own clients: OwnerOf of the
	// producing call's origin. Only the owner reads it through GetOwned (ADR-0099 §2).
	Owner string
	// Inline, when set, is the image's inline variant (see Inline).
	Inline *Inline
}

// Entry is a held artifact. For an ephemeral artifact Data holds the bytes, shared
// with the store: a reader must not modify it. For a durable artifact Data is nil and
// File and Size name the file on disk, so a read of a page does not pull a whole
// console into memory.
type Entry struct {
	Ref      Ref
	Data     []byte
	File     string // durable only: the file holding the bytes
	Size     int64  // durable only: the file's size in bytes
	Producer string
	Owner    string  // see Item.Owner; a durable artifact has none
	Growing  bool    // durable only: still being written
	Inline   *Inline // nil when the producer made none
}

// size is what the entry counts against the budget.
func (e Entry) size() int64 {
	n := int64(len(e.Data))
	if e.Inline != nil {
		n += int64(len(e.Inline.Data))
	}
	return n
}

// Timer is the stoppable half of a scheduled function.
type Timer interface{ Stop() bool }

// Clock is the store's seam for time: tests drive it by hand, so no test waits minutes.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

// Options configures a Store; zero values take the documented defaults.
type Options struct {
	// Epoch is the daemon epoch, the bus's (internal/event). Required: it may not
	// contain '.', '/' or whitespace, because the id is split on '.'.
	Epoch string
	// StateRoot is the state root durable artifacts live under (ADR-0021 §Layout).
	// Without one the store serves ephemeral references only.
	StateRoot string
	TTL       time.Duration
	Budget    int64
	Clock     Clock
}

type held struct {
	entry   Entry
	random  string
	counter uint64
	expires time.Time
	elem    *list.Element // in lru; front is the most recently used
}

// Store is safe for concurrent use.
type Store struct {
	epoch     string
	stateRoot string
	ttl       time.Duration
	budget    int64
	clock     Clock

	mu      sync.Mutex
	entries map[uint64]*held
	lru     *list.List // of *held
	used    int64
	highest uint64 // the highest counter issued
	timer   Timer
	durable map[string]*Durable // open durable artifacts, by reference
}

// New returns an empty store for one daemon epoch.
func New(o Options) (*Store, error) {
	if o.Epoch == "" || strings.ContainsAny(o.Epoch, "./ \t\r\n") {
		return nil, fmt.Errorf("artifact: invalid epoch %q", o.Epoch)
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.Budget <= 0 {
		o.Budget = DefaultBudget
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	return &Store{
		epoch: o.Epoch, stateRoot: o.StateRoot, ttl: o.TTL, budget: o.Budget, clock: o.Clock,
		entries: map[uint64]*held{}, lru: list.New(), durable: map[string]*Durable{},
	}, nil
}

// Put stores a copy of it.Data and returns its reference record. It evicts the least
// recently used entries until the new one fits the budget.
func (s *Store) Put(it Item) (Ref, error) {
	size := int64(len(it.Data))
	if it.Inline != nil {
		size += int64(len(it.Inline.Data))
	}
	if size > s.budget {
		return Ref{}, ErrTooLarge
	}
	var rb [randomHexLen / 2]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return Ref{}, err
	}
	data := append([]byte(nil), it.Data...)
	var inline *Inline
	if it.Inline != nil {
		c := *it.Inline
		c.Data = append([]byte(nil), c.Data...)
		inline = &c
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	for s.used+size > s.budget {
		s.removeLocked(s.lru.Back().Value.(*held))
	}
	s.highest++
	h := &held{random: hex.EncodeToString(rb[:]), counter: s.highest, expires: s.clock.Now().Add(s.ttl)}
	h.entry = Entry{
		Ref: Ref{
			Ref:        RefPrefix + classEphemeral + "." + s.epoch + "." + strconv.FormatUint(h.counter, 10) + "." + h.random,
			MediaType:  it.MediaType,
			Bytes:      len(data),
			Durability: DurabilityEphemeral,
			ExpiresAt:  h.expires,
			Width:      it.Width,
			Height:     it.Height,
			Untrusted:  it.Untrusted,
		},
		Data:     data,
		Producer: it.Producer,
		Owner:    it.Owner,
		Inline:   inline,
	}
	h.elem = s.lru.PushFront(h)
	s.entries[h.counter] = h
	s.used += size
	s.armLocked()
	return h.entry.Ref, nil
}

// Get resolves a reference (or a bare id, as the HTTP route has it): an ephemeral one
// from memory, a durable one from the workspace's artifacts/ directory, so a durable
// reference resolves after a restart without the workspace being open (D13). A miss is
// a capability error: ARTIFACT_EXPIRED when the id was, or could have been, valid
// (evicted, past its lifetime, another epoch, or a durable artifact deleted with its
// owner); ARTIFACT_NOT_FOUND when it never could have been, a malformed durable
// reference included.
func (s *Store) Get(ref string) (Entry, error) {
	id := strings.TrimPrefix(ref, RefPrefix)
	if rest, ok := strings.CutPrefix(id, classDurable+"."); ok {
		return s.getDurable(rest)
	}
	return s.getEphemeral(id)
}

// PeerOwner is the owner of the artifacts a paired peer's calls produced (ADR-0099 §2).
func PeerOwner(name string) string { return "peer:" + name }

// OwnerOf is the owner of an artifact a call from o produces: the peer's configured name for
// a paired peer, "" for every other client, whose artifacts no peer may read.
func OwnerOf(o capability.Origin) string {
	if o.Client != capability.ClientPeer || o.Name == "" {
		return ""
	}
	return PeerOwner(o.Name)
}

// GetOwned is Get for a reader that may see only what it owns (a paired peer, ADR-0099 §2): a
// live ephemeral artifact whose owner is exactly owner and whose reference is exact. Every
// other reference is ARTIFACT_NOT_FOUND with nothing else said: a durable one, another
// owner's or the host's own, one with forged random bits, one that expired, one of another
// daemon run or one that never was. The owner is checked under the store's lock before the
// random bits, so nothing tells a live artifact of someone else (nor its producer) from an id
// that never was. An empty owner reads nothing.
func (s *Store) GetOwned(ref, owner string) (Entry, error) {
	notFound := capability.NewError(capability.ErrArtifactNotFound, "no artifact has this reference")
	id := strings.TrimPrefix(ref, RefPrefix)
	if owner == "" || strings.HasPrefix(id, classDurable+".") {
		return Entry{}, notFound
	}
	epoch, counter, random, ok := parseID(id)
	if !ok || epoch != s.epoch {
		return Entry{}, notFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	h := s.entries[counter]
	if h == nil || h.entry.Owner != owner || subtle.ConstantTimeCompare([]byte(h.random), []byte(random)) != 1 {
		return Entry{}, notFound
	}
	s.lru.MoveToFront(h.elem)
	return h.entry, nil
}

// getEphemeral resolves an ephemeral id and marks the entry most recently used;
// reading does not extend its lifetime.
func (s *Store) getEphemeral(id string) (Entry, error) {
	epoch, counter, random, ok := parseID(id)
	if !ok {
		return Entry{}, capability.NewError(capability.ErrArtifactNotFound,
			"the reference is not an artifact reference")
	}
	if epoch != s.epoch {
		return Entry{}, expired("it was issued by an earlier daemon run", "")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if counter > s.highest {
		return Entry{}, capability.NewError(capability.ErrArtifactNotFound,
			"no artifact has this reference")
	}
	h := s.entries[counter]
	if h == nil || subtle.ConstantTimeCompare([]byte(h.random), []byte(random)) != 1 {
		producer := ""
		if h != nil {
			producer = h.entry.Producer // known only while the counter is still held
		}
		return Entry{}, expired("it was evicted or is past its lifetime", producer)
	}
	s.lru.MoveToFront(h.elem)
	return h.entry, nil
}

// Bytes is the total size of the artifacts the store holds in memory right now. It
// does not sweep: it is what the timer must bring down on its own.
func (s *Store) Bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

// Len is the number of artifacts held in memory right now, like Bytes.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Close stops the sweep timer. The store stays usable; the next Put re-arms it.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// expired names the producing capability in nextStep when it is known: only the
// wrong-random-bits miss still holds the entry; an evicted one took its producer along.
func expired(why, producer string) *capability.Error {
	next := "run the capability that produced the artifact again"
	if producer != "" {
		next = "run " + producer + " again"
	}
	return capability.NewError(capability.ErrArtifactExpired, "the artifact is gone: "+why).WithNextStep(next)
}

// parseID is strict: anything that is not exactly what Put emits is malformed.
func parseID(id string) (epoch string, counter uint64, random string, ok bool) {
	parts := strings.Split(id, ".")
	if len(parts) != 4 || parts[0] != classEphemeral || parts[1] == "" {
		return
	}
	c, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || c == 0 || strconv.FormatUint(c, 10) != parts[2] {
		return
	}
	if len(parts[3]) != randomHexLen {
		return
	}
	for _, r := range parts[3] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return
		}
	}
	return parts[1], c, parts[3], true
}

func (s *Store) removeLocked(h *held) {
	s.lru.Remove(h.elem)
	delete(s.entries, h.counter)
	s.used -= h.entry.size()
}

// sweepLocked drops every expired entry. Expiry is by creation time, so a read never
// extends it.
func (s *Store) sweepLocked() {
	now := s.clock.Now()
	for _, h := range s.entries {
		if !now.Before(h.expires) {
			s.removeLocked(h)
		}
	}
}

// armLocked makes sure a timer will fire at the earliest expiry. A timer that fires
// with nothing to drop (its entry was evicted first) just re-arms.
func (s *Store) armLocked() {
	if s.timer != nil || len(s.entries) == 0 {
		return
	}
	var first time.Time
	for _, h := range s.entries {
		if first.IsZero() || h.expires.Before(first) {
			first = h.expires
		}
	}
	s.timer = s.clock.AfterFunc(max(first.Sub(s.clock.Now()), 0), s.fire)
}

func (s *Store) fire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	s.sweepLocked()
	s.armLocked()
}
