// Package event is the daemon's event bus (docs/architecture/overview.md §Events;
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
	"sync/atomic"
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
	// evicted is the highest seq the ring has dropped: a resume from before it has lost
	// events. A seq PublishTo assigned never entered the ring, so it is not a loss to any
	// resume (m1j §Changes, step 4 N5).
	evicted int64
	subs    map[*Subscription]struct{}

	// subscribeHook is a test seam: see SetSubscribeHook.
	subscribeHook atomic.Pointer[func()]
	// watchHook is a test seam: see SetWatchHook.
	watchHook atomic.Pointer[func(sessionID string)]
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

// PublishTo delivers one event to one subscription: it takes a seq like Publish, but
// appends nothing to the replay ring and offers the event to sub alone. A capability's
// own stream uses it to keep a busy stream out of the ring other subscribers resume
// from, whose 4096 events and 4 MiB a watch would otherwise flush (plan m1j, D8). A nil
// sub takes a seq and drops the event, so a stream whose subscription ended still
// advances the daemon's sequence by nothing observable.
func (b *Bus) PublishTo(sub *Subscription, topic, typ, workspace, operation string, payload any) {
	e := stamp(topic, typ, workspace, operation, payload)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.assign(&e)
	if sub != nil {
		sub.offer(e)
	}
}

// Broadcast delivers one event to every subscription that holds its topic: it takes a seq
// like Publish but appends nothing to the replay ring, so a busy stream of a capability's
// own (operation.state, ADR-0043) never flushes what other subscribers resume from, and
// the topic is not one events.subscribe can name. It returns how many subscriptions it
// offered the event to (workspace.show's tabs, ADR-0084 §1), those whose queue dropped it
// included. It implements capability.StatePublisher.
func (b *Bus) Broadcast(topic, typ, workspace, operation string, payload any) int {
	e := stamp(topic, typ, workspace, operation, payload)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.assign(&e)
	n := 0
	for s := range b.subs {
		if s.topics[topic] {
			s.offer(e)
			n++
		}
	}
	return n
}

var _ capability.StatePublisher = (*Bus)(nil)

// stamp builds an event envelope from its parts, encoding the payload; one that does
// not encode becomes null. The epoch, seq, time and encoded size are assigned under mu
// by assign.
func stamp(topic, typ, workspace, operation string, payload any) Event {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = json.RawMessage("null")
	}
	e := Event{Operation: operation, Topic: topic, Type: typ, Payload: raw}
	if workspace != "" {
		e.Workspace = &workspace
	}
	return e
}

// assign stamps the epoch, the next seq and the time on e and records its encoded
// length for the ring's byte cap. Called with mu held.
func (b *Bus) assign(e *Event) {
	b.seq++
	e.Epoch, e.Seq, e.Time = b.epoch, b.seq, b.now()
	if enc, err := json.Marshal(*e); err == nil {
		e.size = len(enc)
	}
}

// publish stamps the event, appends it to the ring and delivers it to every
// subscription to its topic, all under mu, and returns it.
func (b *Bus) publish(topic, typ, workspace, operation string, payload any) Event {
	e := stamp(topic, typ, workspace, operation, payload)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.assign(&e)
	b.ring = append(b.ring, e)
	b.ringBytes += e.size
	for len(b.ring) > 0 && (len(b.ring) > b.opts.RingEvents || b.ringBytes > b.opts.RingBytes) {
		b.ringBytes -= b.ring[0].size
		b.evicted = b.ring[0].Seq
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
	return b.subscribe(topics, since, 0)
}

// subscribe is Subscribe with a queue of queue envelopes; zero is the bus's.
func (b *Bus) subscribe(topics []string, since *Position, queue int) *Subscription {
	if queue <= 0 {
		queue = b.opts.Queue
	}
	s := &Subscription{bus: b, topics: map[string]bool{}, max: queue, ready: make(chan struct{}, 1)}
	for _, t := range topics {
		s.topics[t] = true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if since != nil {
		oldest := b.evicted + 1 // the first seq a resume can still be given, or was never in the ring
		from := since.Seq + 1   // the first seq the subscriber has not seen
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

// SetSubscribeHook makes every events.subscribe call run f after it has created its
// subscription and before it returns. Test-only: holding f leaves the operation
// running while the subscription already exists and receives. Nil removes it.
func (b *Bus) SetSubscribeHook(f func()) {
	if f == nil {
		b.subscribeHook.Store(nil)
		return
	}
	b.subscribeHook.Store(&f)
}

// SetWatchHook makes every operation.watch call run f, given its session's id, after it
// has opened its subscription and before it takes the snapshot. Test-only: it is where an
// operation can be made to start or end inside the window ADR-0043 §1 closes. Nil removes it.
func (b *Bus) SetWatchHook(f func(sessionID string)) {
	if f == nil {
		b.watchHook.Store(nil)
		return
	}
	b.watchHook.Store(&f)
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

	// onClose, when set, runs exactly once when the subscription ends, after its queue
	// is freed and with no lock held: a domain hook that signals a poller must never
	// wait under bus.mu, which both Close and publishing take (plan m1j, D9). It is set
	// at creation, under the session's lock, so a Close that finds the subscription also
	// sees it.
	onClose   func()
	closeOnce sync.Once

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

// Close ends the subscription: nothing more is delivered, and its queue is freed. It
// is idempotent, and its onClose hook (if any) runs exactly once, after the queue is
// freed and with no lock held, so the hook may publish.
func (s *Subscription) Close() {
	s.closeOnce.Do(func() {
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
		if s.onClose != nil {
			s.onClose()
		}
	})
}
