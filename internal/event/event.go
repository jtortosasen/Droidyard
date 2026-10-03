// Package event is the daemon's event bus (docs/architecture/core-runtime.md §Events;
// capability-api.md §Events and subscriptions): one sequence per daemon epoch, a
// bounded replay ring, and a bounded queue per subscription whose losses are reported
// as an events.gap marker, never silently. Delivery is synchronous: once Publish has
// returned, or Position has returned a seq, every matching event up to it is in each
// subscription's queue or recorded as dropped there.
package event

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The gap marker (plan m1d, D4): topic events.gap, type gap, payload {fromSeq, toSeq},
// seq equal to toSeq.
const (
	TopicGap = "events.gap"
	TypeGap  = "gap"
)

// The working bounds of plan m1d, D5.
const (
	DefaultRingEvents = 4096
	DefaultRingBytes  = 4 << 20
	DefaultQueue      = 1024
)

// ErrClosed is Next's answer once the subscription is closed.
var ErrClosed = errors.New("the subscription is closed")

// Event is the event envelope (capability-api.md §Event envelope).
type Event struct {
	Epoch string    `json:"epoch"`
	Seq   int64     `json:"seq"`
	Time  time.Time `json:"time"`
	// Workspace is nil for a global event, encoded as null.
	Workspace *string         `json:"workspace"`
	Operation string          `json:"operation,omitempty"`
	Topic     string          `json:"topic"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`

	size int // the encoded envelope's length, for the ring's byte cap
}

// Gap is the payload of an events.gap marker: the seqs it stands for, both included.
// {1, 0} is empty: a resume from another epoch that lost nothing of this one.
type Gap struct {
	FromSeq int64 `json:"fromSeq"`
	ToSeq   int64 `json:"toSeq"`
}

// Position is a point in the bus's sequence, as `since` names it.
type Position struct {
	Epoch string `json:"epoch"`
	Seq   int64  `json:"seq"`
}

// Options bound a Bus; zero values take the defaults.
type Options struct {
	RingEvents int // events the replay ring holds at most
	RingBytes  int // encoded envelope bytes the replay ring holds at most
	Queue      int // envelopes a subscription's queue holds at most
	Now        func() time.Time
}

// Bus is the daemon's one event bus. It implements capability.Publisher.
type Bus struct {
	opts  Options
	epoch string

	// mu orders everything: the seq, the ring, the subscriptions, and every delivery.
	mu        sync.Mutex
	seq       int64
	ring      []Event
	ringBytes int
	subs      map[*Subscription]struct{}
}

var _ capability.Publisher = (*Bus)(nil)

// New returns a bus with a new epoch: "d-" and 8 random hex digits (plan m1d, D4).
func New(opts Options) *Bus {
	if opts.RingEvents <= 0 {
		opts.RingEvents = DefaultRingEvents
	}
	if opts.RingBytes <= 0 {
		opts.RingBytes = DefaultRingBytes
	}
	if opts.Queue <= 0 {
		opts.Queue = DefaultQueue
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	var b [4]byte
	rand.Read(b[:]) // never returns an error (crypto/rand, Go 1.24)
	return &Bus{opts: opts, epoch: "d-" + hex.EncodeToString(b[:]), subs: map[*Subscription]struct{}{}}
}

// Position is the bus's epoch and the last seq it assigned; 0 before the first event.
// Every matching event up to it is already queued, or recorded as dropped, in every
// subscription.
func (b *Bus) Position() Position {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Position{Epoch: b.epoch, Seq: b.seq}
}

// Subscriptions is how many subscriptions the bus holds: what a session's end must
// bring back to zero.
func (b *Bus) Subscriptions() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Publish implements capability.Publisher.
func (b *Bus) Publish(topic, typ, workspace, operation string, payload any) {
	b.publish(topic, typ, workspace, operation, payload)
}

// publish stamps the event, appends it to the ring and delivers it to every
// subscription to its topic, all under mu, and returns it. A payload that does not
// encode is published as null.
func (b *Bus) publish(topic, typ, workspace, operation string, payload any) Event {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = json.RawMessage("null")
	}
	e := Event{Operation: operation, Topic: topic, Type: typ, Payload: raw}
	if workspace != "" {
		e.Workspace = &workspace
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.Epoch, e.Seq, e.Time = b.epoch, b.seq, b.now()
	if enc, err := json.Marshal(e); err == nil {
		e.size = len(enc)
	}
	b.ring = append(b.ring, e)
	b.ringBytes += e.size
	for len(b.ring) > 0 && (len(b.ring) > b.opts.RingEvents || b.ringBytes > b.opts.RingBytes) {
		b.ringBytes -= b.ring[0].size
		b.ring[0] = Event{}
		b.ring = b.ring[1:]
	}
	for s := range b.subs {
		if s.topics[topic] {
			s.offer(e)
		}
	}
	return e
}

func (b *Bus) now() time.Time { return b.opts.Now().UTC().Truncate(time.Millisecond) }

func (b *Bus) gap(g Gap) Event {
	raw, _ := json.Marshal(g)
	return Event{Epoch: b.epoch, Seq: g.ToSeq, Time: b.now(), Topic: TopicGap, Type: TypeGap, Payload: raw}
}

// Subscribe returns a subscription to topics. With since nil it receives what is
// published from now on. With since in this epoch, it first receives the matching
// events after since.Seq that the ring holds, preceded by events.gap {since.Seq+1,
// oldest-1} if the ring no longer reaches back that far. With since of another epoch
// it first receives events.gap {1, oldest-1}, then every matching event in the ring
// (plan m1d, D4). The replay and the registration happen under the lock Publish
// takes, so between them nothing is missed and nothing repeated.
func (b *Bus) Subscribe(topics []string, since *Position) *Subscription {
	s := &Subscription{bus: b, topics: map[string]bool{}, max: b.opts.Queue, ready: make(chan struct{}, 1)}
	for _, t := range topics {
		s.topics[t] = true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if since != nil {
		oldest := b.seq + 1
		if len(b.ring) > 0 {
			oldest = b.ring[0].Seq
		}
		from := since.Seq + 1 // the first seq the subscriber has not seen
		if since.Epoch != b.epoch || from < 1 {
			from = 1
		}
		if since.Epoch != b.epoch || from < oldest {
			s.offer(b.gap(Gap{FromSeq: from, ToSeq: oldest - 1}))
		}
		for _, e := range b.ring {
			if e.Seq >= from && s.topics[e.Topic] {
				s.offer(e)
			}
		}
	}
	b.subs[s] = struct{}{}
	return s
}

// Subscription is one subscriber's view of the bus: the events of its topics, in seq
// order, through a queue that holds at most Options.Queue envelopes. A full queue
// never blocks Publish: what does not fit is dropped, and one events.gap marker
// takes its place once there is room.
type Subscription struct {
	bus    *Bus
	topics map[string]bool
	max    int
	ready  chan struct{} // signalled when something is queued, or on Close

	mu      sync.Mutex
	queue   []Event
	pending *Gap // dropped seqs not yet reported
	closed  bool
}

// offer queues e, or records it as dropped. Called with bus.mu held.
func (s *Subscription) offer(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.pending != nil && len(s.queue) < s.max {
		s.queue = append(s.queue, s.bus.gap(*s.pending))
		s.pending = nil
	}
	switch {
	case s.pending == nil && len(s.queue) < s.max:
		s.queue = append(s.queue, e)
	case s.pending == nil:
		s.pending = &Gap{FromSeq: e.Seq, ToSeq: e.Seq}
	default:
		s.pending.ToSeq = e.Seq
	}
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// TryNext returns the next envelope without blocking: the oldest queued, else the gap
// for what was dropped after it; false when there is neither, or once closed.
func (s *Subscription) TryNext() (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) > 0 {
		e := s.queue[0]
		s.queue[0] = Event{}
		s.queue = s.queue[1:]
		return e, true
	}
	if s.pending != nil {
		g := *s.pending
		s.pending = nil
		return s.bus.gap(g), true
	}
	return Event{}, false
}

// Next returns the next envelope, waiting for one until ctx ends or the subscription
// is closed (ErrClosed).
func (s *Subscription) Next(ctx context.Context) (Event, error) {
	for {
		if e, ok := s.TryNext(); ok {
			return e, nil
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return Event{}, ErrClosed
		}
		select {
		case <-s.ready:
		case <-ctx.Done():
			return Event{}, ctx.Err()
		}
	}
}

// Close ends the subscription: nothing more is delivered, and its queue is freed.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	delete(s.bus.subs, s)
	s.bus.mu.Unlock()
	s.mu.Lock()
	s.closed, s.queue, s.pending = true, nil, nil
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}
