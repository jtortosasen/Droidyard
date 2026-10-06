package artifact

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// This file is the durable class (ADR-0012 §Durability classes; D13). A durable
// artifact is a file under <state>/ws/<wsid>/artifacts/ that a producer writes
// incrementally and that is readable while it grows. Its reference encodes the class,
// the workspace and the owner (the build id), never a path: the id is
// "d.<workspace>.<owner>", so a reference held across a restart still resolves, and a
// reference whose file is gone is ARTIFACT_EXPIRED. Only the build console exists
// today, so an artifact recovered from disk is a ".log" and its media type and mark
// are derived; while the producer still holds the handle, the store keeps what it was
// created with.

// durableExt is the file extension ADR-0021 §Layout gives a build console; the owner
// id is the file's base name.
const durableExt = ".log"

// DurableOptions configure a durable artifact.
type DurableOptions struct {
	// MediaType is the artifact's content type, kept in its reference record.
	MediaType string
	// Untrusted is the producer's mark (ADR-0015 §1). A console is project content
	// and unmarked. The mark is not persisted yet, so CreateDurable refuses true.
	Untrusted bool
	// Cap is the per-artifact byte cap ADR-0012 lets the producer set (D24: 32 MiB
	// for a console). 0 means no cap. Bytes past it are dropped and counted, never an
	// error: a producer writes a marker line and records Dropped.
	Cap int64
}

// Durable is one durable artifact being written. It is safe for concurrent use.
type Durable struct {
	s     *Store
	ref   Ref
	path  string
	owner string

	mu      sync.Mutex
	file    *os.File
	size    int64
	cap     int64
	dropped int64
	closed  bool
}

// CreateDurable starts the durable artifact owned by owner in workspace, truncating
// any bytes a previous artifact with the same owner left (a build id is never reused).
// The caller closes it when the producer is done; until then its reference reads
// `growing`.
func (s *Store) CreateDurable(workspace, owner string, o DurableOptions) (*Durable, error) {
	// The workspace may hold a dot (a project directory may); the owner may not,
	// because the id splits the two on the last dot.
	if !validPart(workspace) {
		return nil, fmt.Errorf("artifact: %q is not a usable workspace id", workspace)
	}
	if !validPart(owner) || strings.Contains(owner, ".") {
		return nil, fmt.Errorf("artifact: %q is not a usable owner id", owner)
	}
	if s.stateRoot == "" {
		return nil, errors.New("artifact: durable artifacts need a state root")
	}
	// The mark lives only on the open handle; a closed or reloaded artifact reads
	// unmarked. Until it is persisted, a marked durable artifact is refused rather than
	// silently unmarked later (ADR-0015 §1).
	if o.Untrusted {
		return nil, errors.New("artifact: a durable artifact cannot be marked untrusted yet: the mark is not persisted")
	}
	dir := s.durableDir(workspace)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, owner+durableExt)
	if filepath.Dir(path) != filepath.Clean(dir) {
		return nil, fmt.Errorf("artifact: %q does not name a file in the workspace's artifacts", owner)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	ref := durableRef(workspace, owner)
	d := &Durable{
		s:     s,
		ref:   Ref{Ref: ref, MediaType: o.MediaType, Durability: DurabilityDurable, Untrusted: o.Untrusted},
		path:  path,
		owner: owner,
		file:  f,
		cap:   o.Cap,
	}
	s.mu.Lock()
	if s.durable == nil {
		s.durable = map[string]*Durable{}
	}
	if old := s.durable[ref]; old != nil && old != d {
		old.mu.Lock()
		old.closed = true
		_ = old.file.Close()
		old.mu.Unlock()
	}
	s.durable[ref] = d
	s.mu.Unlock()
	return d, nil
}

// Ref is the artifact's reference record with its current size. The record's
// ExpiresAt is the zero time: a durable artifact has no expiry, it lives with the
// record that owns it (ADR-0012 §Durability classes).
func (d *Durable) Ref() Ref {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.ref
	r.Bytes = int(d.size)
	return r
}

// Write appends p. Bytes past the cap are dropped and counted rather than refused, so a
// producer's write loop is never interrupted; the count is Dropped. Like every
// io.Writer it reports the bytes consumed from p, dropped ones included.
func (d *Durable) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return 0, fs.ErrClosed
	}
	total := int64(len(p))
	if d.cap > 0 {
		room := d.cap - d.size
		if room > total {
			room = total
		}
		if room < total {
			if room < 0 {
				room = 0
			}
			d.dropped += total - room
			p = p[:room]
		}
	}
	n, err := d.file.Write(p)
	d.size += int64(n)
	if err != nil {
		return n, err
	}
	return int(total), nil
}

// WriteMarker appends one line past the cap, for the producer's note that the cap was
// reached (D24). It is bounded to 1 KiB so the cap still bounds the file.
func (d *Durable) WriteMarker(line string) error {
	if len(line) > 1<<10 {
		line = line[:1<<10]
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return fs.ErrClosed
	}
	n, err := d.file.Write([]byte(line + "\n"))
	d.size += int64(n)
	return err
}

// Size is the bytes written so far; Dropped the bytes the cap refused.
func (d *Durable) Size() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.size
}

// Dropped is how many bytes the cap has dropped.
func (d *Durable) Dropped() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

// Close stops the artifact growing: its reference reads `growing: false` from now on
// and it is read back from disk like an artifact of an earlier run.
func (d *Durable) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	err := d.file.Close()
	// Released before the store's lock: CreateDurable takes the store's, then an old
	// handle's.
	d.mu.Unlock()
	d.s.mu.Lock()
	if d.s.durable[d.ref.Ref] == d {
		delete(d.s.durable, d.ref.Ref)
	}
	d.s.mu.Unlock()
	return err
}

// DurableFile is one file under a workspace's artifacts/, for the sweep that deletes
// the ones no record owns (D13).
type DurableFile struct {
	Owner   string    `json:"owner"`
	Ref     string    `json:"ref"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"modTime"`
}

// ListDurable lists a workspace's durable artifact files. It does not need the
// workspace open.
func (s *Store) ListDurable(workspace string) ([]DurableFile, error) {
	if !validPart(workspace) {
		return nil, fmt.Errorf("artifact: %q is not a usable workspace id", workspace)
	}
	if s.stateRoot == "" {
		return nil, nil
	}
	ents, err := os.ReadDir(s.durableDir(workspace))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []DurableFile
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), durableExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		owner := strings.TrimSuffix(e.Name(), durableExt)
		out = append(out, DurableFile{
			Owner: owner, Ref: durableRef(workspace, owner), Bytes: info.Size(), ModTime: info.ModTime(),
		})
	}
	return out, nil
}

// DeleteDurable removes the durable artifact owner owns in workspace, if any: it is
// how an artifact is deleted with its owning record. Its reference then reads
// ARTIFACT_EXPIRED.
func (s *Store) DeleteDurable(workspace, owner string) error {
	if !validPart(workspace) || !validPart(owner) {
		return fmt.Errorf("artifact: %q / %q do not name a durable artifact", workspace, owner)
	}
	if s.stateRoot == "" {
		return nil
	}
	if d := s.openDurable(durableRef(workspace, owner)); d != nil {
		_ = d.Close()
	}
	err := os.Remove(filepath.Join(s.durableDir(workspace), owner+durableExt))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// getDurable resolves a durable id "<workspace>.<owner>". The workspace is everything
// before the last dot, so a workspace id that itself holds dots still resolves. Both
// parts must be single path elements: anything that could step out of the workspace's
// artifacts/ directory ("..", a slash, an absolute path) is not a reference this store
// ever issued, so it is ARTIFACT_NOT_FOUND and nothing is opened. A reference whose
// workspace this state root has never held is NOT_FOUND for the same reason; only a
// workspace the state root does hold, with its file gone, is ARTIFACT_EXPIRED.
func (s *Store) getDurable(rest string) (Entry, error) {
	cut := strings.LastIndex(rest, ".")
	if cut < 0 {
		return Entry{}, notAnArtifact()
	}
	workspace, owner := rest[:cut], rest[cut+1:]
	if !validPart(workspace) || !validPart(owner) {
		return Entry{}, notAnArtifact()
	}
	ref := durableRef(workspace, owner)
	if s.stateRoot == "" {
		return Entry{}, notAnArtifact()
	}
	if d := s.openDurable(ref); d != nil {
		d.mu.Lock()
		e := Entry{Ref: d.ref, File: d.path, Size: d.size, Growing: !d.closed}
		e.Ref.Bytes = int(d.size)
		d.mu.Unlock()
		return e, nil
	}
	// A workspace the state root has never held (no ws/<wsid>/ directory) means the
	// reference was not one this store issued: NOT_FOUND, never the EXPIRED a deleted
	// artifact of a known workspace reads (plan m1g D13).
	if _, err := os.Stat(filepath.Join(s.stateRoot, "ws", workspace)); errors.Is(err, fs.ErrNotExist) {
		return Entry{}, notAnArtifact()
	}
	path := filepath.Join(s.durableDir(workspace), owner+durableExt)
	if filepath.Dir(path) != filepath.Clean(s.durableDir(workspace)) {
		return Entry{}, notAnArtifact()
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Entry{}, durableExpired()
		}
		return Entry{}, capability.AsError(err)
	}
	// Only a regular file the store wrote: a symlink, FIFO or directory planted under
	// artifacts/ is not followed (defence in depth; the state root is 0700).
	if !fi.Mode().IsRegular() {
		return Entry{}, notAnArtifact()
	}
	return Entry{
		Ref: Ref{
			Ref: ref, MediaType: durableMediaType(owner + durableExt), Bytes: int(fi.Size()), Durability: DurabilityDurable,
		},
		File: path, Size: fi.Size(),
	}, nil
}

func (s *Store) openDurable(ref string) *Durable {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.durable[ref]
}

// durableDir is ws/<wsid>/artifacts/ under the state root (ADR-0021 §Layout).
func (s *Store) durableDir(workspace string) string {
	return filepath.Join(s.stateRoot, "ws", workspace, "artifacts")
}

func durableRef(workspace, owner string) string {
	return RefPrefix + classDurable + "." + workspace + "." + owner
}

// durableMediaType recovers a durable artifact's content type from disk. Only the
// build console exists today, so a ".log" is text/plain (ADR-0012; D13).
func durableMediaType(name string) string {
	if strings.HasSuffix(name, durableExt) {
		return "text/plain"
	}
	return "application/octet-stream"
}

func durableExpired() *capability.Error {
	return capability.NewError(capability.ErrArtifactExpired, "the artifact is gone: it was deleted with the record that owned it").
		WithNextStep("build again")
}

func notAnArtifact() *capability.Error {
	return capability.NewError(capability.ErrArtifactNotFound, "the reference is not an artifact reference")
}

// validPart reports whether s can name one directory or file without leaving the
// directory it is joined into: it must be exactly one path element of this platform,
// so filepath.Base leaves it unchanged and filepath.IsLocal accepts it (non-empty, not
// "." or "..", not absolute, no separator). It is the guard that keeps a durable
// reference from resolving to a file outside <state>/ws/<wsid>/artifacts/.
//
// It is a single-element check, not a character denylist, so a workspace id is usable
// exactly when the workspace package can produce it: a workspace id is
// <basename>-<12 hex> (platform.WorkspaceID), and a directory basename may hold a
// percent sign, a backslash on a platform where that is an ordinary character, or a
// dot. A percent is never decoded here — this file joins the part into a path as it
// stands — so allowing one opens nothing; a separator, which would, is still refused,
// on the platform that treats it as one.
func validPart(s string) bool {
	return s != "" && filepath.IsLocal(s) && filepath.Base(s) == s
}
