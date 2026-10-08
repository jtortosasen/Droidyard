package event

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/invopop/jsonschema"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The capabilities' names.
const (
	CapSubscribe   = "events.subscribe"
	CapUnsubscribe = "events.unsubscribe"
)

// introduced is the API version that introduced events.* (plan m1d, R1).
const introduced = "0.3.0"

// TopicWorkspaceLifecycle is the topic workspace transitions are published on. The
// workspace registry names the same string itself, since the core does not import the
// bus; a test holds the two together.
const TopicWorkspaceLifecycle = "workspace.lifecycle"

// subscribable is the closed set of topics a session may subscribe to (plan m1d, D4):
// an unknown topic is rejected at Start, because a typo that silently delivered
// nothing would make "empty is a fact" a lie. events.gap is not here: it is a marker,
// delivered on every subscription, never subscribed to.
var subscribable = []string{TopicWorkspaceLifecycle, capability.ProgressTopic}

// Topic is a subscribable topic. Decoding rejects any other value.
type Topic string

// UnmarshalJSON accepts only a subscribable topic.
func (t *Topic) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a topic is a string: %w", err)
	}
	if !slices.Contains(subscribable, s) {
		return fmt.Errorf("unknown topic %q: the topics are %s", s, strings.Join(subscribable, ", "))
	}
	*t = Topic(s)
	return nil
}

// JSONSchema publishes the closed set as an enum.
func (Topic) JSONSchema() *jsonschema.Schema {
	enum := make([]any, len(subscribable))
	for i, s := range subscribable {
		enum[i] = s
	}
	return &jsonschema.Schema{Type: "string", Enum: enum}
}

// Topics is a non-empty list of topics: subscribing to nothing is a mistake the
// caller should hear about, not a subscription that delivers nothing.
type Topics []Topic

// UnmarshalJSON rejects null and an empty list, and any element that is not a topic.
func (ts *Topics) UnmarshalJSON(b []byte) error {
	var list []Topic
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		return errors.New("topics must name at least one topic")
	}
	*ts = list
	return nil
}

// JSONSchema publishes the list as an array of the topic enum with minItems 1.
func (Topics) JSONSchema() *jsonschema.Schema {
	one := uint64(1)
	return &jsonschema.Schema{Type: "array", Items: Topic("").JSONSchema(), MinItems: &one}
}

// Since is `since` as a caller writes it: a point in the bus's sequence, both parts
// required.
type Since struct {
	Epoch string `json:"epoch" jsonschema:"required"`
	Seq   int64  `json:"seq" jsonschema:"required"`
}

// UnmarshalJSON requires both fields and refuses any other.
func (s *Since) UnmarshalJSON(b []byte) error {
	var raw struct {
		Epoch *string `json:"epoch"`
		Seq   *int64  `json:"seq"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	if raw.Epoch == nil || raw.Seq == nil {
		return errors.New("since needs both epoch and seq")
	}
	*s = Since{Epoch: *raw.Epoch, Seq: *raw.Seq}
	return nil
}

// SubscribeParams are events.subscribe's parameters (plan m1d, D7). `filter` is not
// here: it arrives with its first consumer, as an optional parameter.
type SubscribeParams struct {
	Topics Topics `json:"topics" jsonschema:"required"`
	// Since resumes from a point the caller saw: the ring's events after it, then
	// live. Omitted: from now on.
	Since *Since `json:"since,omitempty"`
}

// SubscribeResult is events.subscribe's result.
type SubscribeResult struct {
	SubscriptionID string `json:"subscriptionId" jsonschema:"required"`
}

// UnsubscribeParams are events.unsubscribe's parameters.
type UnsubscribeParams struct {
	SubscriptionID string `json:"subscriptionId" jsonschema:"required"`
}

// UnsubscribeResult is events.unsubscribe's result: nothing.
type UnsubscribeResult struct{}

// Session is a session that can carry events: what a WebSocket connection sets on its
// requests. It owns the subscriptions its calls make, so that its end closes them
// (capability-api.md §Subscriptions: "Subscriptions belong to the session"). It
// implements capability.Session.
type Session struct {
	bus *Bus
	id  string

	mu     sync.Mutex
	subs   map[string]*Subscription
	closed bool
	queue  int // envelopes a new subscription's queue holds; 0 is the bus's
}

var _ capability.Session = (*Session)(nil)

// NewSession returns a session on bus, named id.
func NewSession(bus *Bus, id string) *Session {
	return &Session{bus: bus, id: id, subs: map[string]*Subscription{}}
}

// ID implements capability.Session.
func (s *Session) ID() string { return s.id }

// Subscription returns one of the session's own subscriptions by id.
func (s *Session) Subscription(id string) (*Subscription, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[id]
	return sub, ok
}

// Len is how many subscriptions the session holds.
func (s *Session) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// SetQueue sets how many envelopes the queue of each subscription this session opens
// from now on holds; n <= 0 is the bus's size. Test-only: it is how a test makes a
// subscription overflow with a few events instead of a thousand.
func (s *Session) SetQueue(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = n
}

// Close ends the session's subscriptions, and refuses new ones. It is idempotent.
func (s *Session) Close() {
	s.mu.Lock()
	subs := s.subs
	s.subs, s.closed = map[string]*Subscription{}, true
	s.mu.Unlock()
	for _, sub := range subs {
		sub.Close()
	}
}

// Subscribe opens a subscription that belongs to the session, as events.subscribe
// does. The registration with the bus happens under the session's lock, so a Close
// cannot slip between the check and the registration and leave a subscription nobody
// owns.
func (s *Session) subscribe(topics []string, since *Position) (string, error) {
	id, _, err := s.open(topics, since, nil)
	return id, err
}

// Open opens a subscription that belongs to the session for a capability that carries
// its own stream (plan m1j, file.watch): it returns the id and the subscription the
// stream is delivered to. onClose, when not nil, runs exactly once when the
// subscription ends, with no lock held, so a poller's stop hook never waits under
// bus.mu (D9). The subscription registers like one events.subscribe makes, so it ends
// with the session and events.unsubscribe can end it.
func (s *Session) Open(topics []string, onClose func()) (string, *Subscription, error) {
	return s.open(topics, nil, onClose)
}

// open is subscribe with the close hook and the subscription itself exposed.
func (s *Session) open(topics []string, since *Position, onClose func()) (string, *Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", nil, capability.Errorf(capability.ErrSessionRequired, "the session has ended")
	}
	var b [8]byte
	rand.Read(b[:]) // never returns an error (crypto/rand, Go 1.24)
	id := "sub-" + hex.EncodeToString(b[:])
	sub := s.bus.subscribe(topics, since, s.queue)
	// The hook is set before the id is in the map, so a Close that collects this
	// subscription also sees the hook (both happen under s.mu).
	sub.onClose = onClose
	s.subs[id] = sub
	return id, sub, nil
}

// Unsubscribe ends one of the session's own subscriptions, as events.unsubscribe does.
// A file.watch handler calls it to clean up a watch it will not return an id for.
func (s *Session) Unsubscribe(id string) { s.unsubscribe(id) }

// unsubscribe ends one of the session's own subscriptions. An id it does not own,
// another session's included, is not found here, and nothing happens: there is no
// way to probe another session's ids (plan m1d, D7).
func (s *Session) unsubscribe(id string) {
	s.mu.Lock()
	sub, ok := s.subs[id]
	delete(s.subs, id)
	s.mu.Unlock()
	if ok {
		sub.Close()
	}
}

// SessionOf is the session a call carries, when it can carry events: what a handler
// needs to open a subscription of its own (plan m1j, file.watch does). Start rejects a
// call to a stateful or streaming capability without one, before the handler runs; a
// handler reached through some other path, or with a session of another kind, still
// gets SESSION_REQUIRED here rather than a panic.
func SessionOf(call *capability.Call) (*Session, error) { return sessionOf(call) }

// sessionOf is the session a call carries, when it can carry events.
func sessionOf(call *capability.Call) (*Session, error) {
	s, ok := call.Session.(*Session)
	if !ok || s == nil {
		return nil, capability.NewError(capability.ErrSessionRequired, "events need a session that can carry them").
			WithNextStep("subscribe over the WebSocket session, GET /v1/ws")
	}
	return s, nil
}

// Register adds events.subscribe and events.unsubscribe. Neither is an MCP tool: the
// adapter lists stateless capabilities only (mcp.md §What is exposed).
func Register(reg *capability.Registry) {
	// A subscription is state the call creates, so this is a command, not a query
	// (mcp.md §Annotations: "a query that writes is a Kind bug").
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:         CapSubscribe,
		Kind:         capability.Command,
		Scope:        capability.Global,
		Statefulness: capability.Streaming,
		Since:        introduced,
		Description:  "Subscribes the calling session to events on one or more topics (workspace.lifecycle, operation.progress) and returns the subscription's id. The events then arrive on the session, each naming the subscription, in seq order. With since {epoch, seq}, events after that point are replayed first; if they are no longer held, or the epoch is another daemon's, delivery starts with an events.gap marker and the caller must resync through queries. Only a WebSocket session can subscribe.",
		Params:       SubscribeParams{},
		Result:       SubscribeResult{},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "adds a subscription to the calling session; ends nothing",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			s, err := sessionOf(call)
			if err != nil {
				return nil, err
			}
			p := call.Params.(SubscribeParams)
			topics := make([]string, len(p.Topics))
			for i, t := range p.Topics {
				topics[i] = string(t)
			}
			var from *Position
			if p.Since != nil {
				from = &Position{Epoch: p.Since.Epoch, Seq: p.Since.Seq}
			}
			id, err := s.subscribe(topics, from)
			if err != nil {
				return nil, err
			}
			if hook := s.bus.subscribeHook.Load(); hook != nil {
				(*hook)()
			}
			return SubscribeResult{SubscriptionID: id}, nil
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:         CapUnsubscribe,
		Kind:         capability.Command,
		Scope:        capability.Global,
		Statefulness: capability.Stateful,
		Idempotent:   true,
		Since:        introduced,
		Description:  "Ends one of the calling session's subscriptions. An id that is unknown, already ended or another session's is not an error: nothing happens.",
		Params:       UnsubscribeParams{},
		Result:       UnsubscribeResult{},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "ends only the calling session's own subscription",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			s, err := sessionOf(call)
			if err != nil {
				return nil, err
			}
			s.unsubscribe(call.Params.(UnsubscribeParams).SubscriptionID)
			return UnsubscribeResult{}, nil
		},
	})
}
