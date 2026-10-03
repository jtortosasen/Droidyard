package event

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// sessionFixture is a bus, the events.* capabilities on a store, and two sessions.
type sessionFixture struct {
	bus  *Bus
	ops  *capability.Operations
	a, b *Session
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	reg := capability.NewRegistry()
	Register(reg)
	f := &sessionFixture{bus: New(Options{}), ops: capability.NewOperations(reg)}
	f.a, f.b = NewSession(f.bus, "ses-a"), NewSession(f.bus, "ses-b")
	return f
}

// call runs one events.* call as the session s, through the store's real Start and
// Wait, and returns the result decoded as JSON text, or the Start rejection.
func (f *sessionFixture) call(t *testing.T, s capability.Session, name, params string) (string, string, error) {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Params: json.RawMessage(params), Client: capability.ClientUI, Session: s})
	if err != nil {
		if id != "" {
			t.Errorf("%s %s: rejected with an operation id %q", name, params, id)
		}
		return "", "", err
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != capability.StateCompleted {
		return "", string(out.State), out.Error
	}
	raw, _ := json.Marshal(out.Result)
	return string(raw), string(out.State), nil
}

func (f *sessionFixture) subscribe(t *testing.T, s capability.Session, params string) string {
	t.Helper()
	res, state, err := f.call(t, s, CapSubscribe, params)
	if err != nil || state != "completed" {
		t.Fatalf("events.subscribe %s: (%s, %s, %v); want completed", params, res, state, err)
	}
	var r SubscribeResult
	if err := json.Unmarshal([]byte(res), &r); err != nil || !strings.HasPrefix(r.SubscriptionID, "sub-") {
		t.Fatalf("events.subscribe %s answered %s; want a subscriptionId", params, res)
	}
	return r.SubscriptionID
}

func typesOf(es []Event) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Topic+"/"+e.Type)
	}
	return out
}

// TestSubscribeRejectsWhatItCannotDeliver: an unknown topic, no topic, a malformed
// `since` is REQUEST_INVALID_PARAMS at Start, with no operation and no subscription
// (plan m1d, D4 and D7: a typo that silently delivered nothing would make "empty is a
// fact" a lie). The enum and minItems the schema publishes are enforced, not merely
// described.
func TestSubscribeRejectsWhatItCannotDeliver(t *testing.T) {
	f := newSessionFixture(t)
	for _, params := range []string{
		`{"topics":["nope.nope"]}`,
		`{"topics":[]}`,
		`{"topics":null}`,
		`{}`,
		`{"topics":"workspace.lifecycle"}`,
		`{"topics":["events.gap"]}`, // a marker every subscription receives, not a topic
		`{"topics":["workspace.lifecycle","nope.nope"]}`,
		`{"topics":["workspace.lifecycle"],"since":{"epoch":"d-1"}}`,
		`{"topics":["workspace.lifecycle"],"since":{"seq":1}}`,
		`{"topics":["workspace.lifecycle"],"since":{"epoch":"d-1","seq":1,"extra":true}}`,
		`{"topics":["workspace.lifecycle"],"filter":{}}`, // filter is not in 1d
	} {
		_, _, err := f.call(t, f.a, CapSubscribe, params)
		var e *capability.Error
		if !errors.As(err, &e) || e.Code != capability.ErrInvalidParams {
			t.Errorf("events.subscribe %s: %v; want REQUEST_INVALID_PARAMS", params, err)
		}
	}
	if f.bus.Subscriptions() != 0 || f.a.Len() != 0 {
		t.Errorf("rejected calls left %d subscriptions on the bus and %d on the session", f.bus.Subscriptions(), f.a.Len())
	}
	f.subscribe(t, f.a, `{"topics":["workspace.lifecycle","operation.progress"]}`)
	f.subscribe(t, f.a, `{"topics":["workspace.lifecycle"],"since":{"epoch":"d-x","seq":0}}`)
}

// TestSubscriptionsBelongToTheirSession: each subscription delivers its topics to its
// own session; unsubscribe ends only the calling session's own subscription, and an
// unknown id, an ended one or another session's is a no-op that answers {} (plan m1d,
// D7: no new error code, no way to probe another session's ids).
func TestSubscriptionsBelongToTheirSession(t *testing.T) {
	f := newSessionFixture(t)
	idA := f.subscribe(t, f.a, `{"topics":["workspace.lifecycle"]}`)
	idB := f.subscribe(t, f.b, `{"topics":["workspace.lifecycle","operation.progress"]}`)
	if idA == idB {
		t.Fatalf("two subscriptions share the id %s", idA)
	}
	subA, _ := f.a.Subscription(idA)
	subB, _ := f.b.Subscription(idB)
	if subA == nil || subB == nil {
		t.Fatal("a session does not hold the subscription it made")
	}
	if _, ok := f.a.Subscription(idB); ok {
		t.Error("session a can see session b's subscription")
	}

	f.bus.Publish("workspace.lifecycle", "opening", "ws-1", "", map[string]string{"state": "opening"})
	f.bus.Publish("operation.progress", "progress", "", "op-1", map[string]int{"done": 1})
	if got := typesOf(drain(subA)); len(got) != 1 || got[0] != "workspace.lifecycle/opening" {
		t.Errorf("session a received %v; want only the lifecycle event", got)
	}
	if got := typesOf(drain(subB)); len(got) != 2 {
		t.Errorf("session b received %v; want both events", got)
	}

	// Another session's id, an unknown one, and an ended one: all {}, nothing ends.
	for _, id := range []string{idA, "sub-0000000000000000", "", "nope"} {
		res, state, err := f.call(t, f.b, CapUnsubscribe, `{"subscriptionId":"`+id+`"}`)
		if err != nil || state != "completed" || res != `{}` {
			t.Errorf("b unsubscribing %q: (%s, %s, %v); want completed {}", id, res, state, err)
		}
	}
	if f.a.Len() != 1 || f.bus.Subscriptions() != 2 {
		t.Fatalf("after b's no-op unsubscribes the bus holds %d subscriptions, a holds %d; want 2 and 1", f.bus.Subscriptions(), f.a.Len())
	}
	f.bus.Publish("workspace.lifecycle", "ready", "ws-1", "", map[string]string{"state": "ready"})
	if got := typesOf(drain(subA)); len(got) != 1 {
		t.Errorf("a's subscription received %v after b tried to end it; want the event", got)
	}
	drain(subB)

	// a ends its own, twice: the second finds nothing.
	for i := 0; i < 2; i++ {
		res, state, err := f.call(t, f.a, CapUnsubscribe, `{"subscriptionId":"`+idA+`"}`)
		if err != nil || state != "completed" || res != `{}` {
			t.Fatalf("a unsubscribing %s (%d): (%s, %s, %v); want completed {}", idA, i, res, state, err)
		}
	}
	f.bus.Publish("workspace.lifecycle", "closing", "ws-1", "", map[string]string{"state": "closing"})
	if got := drain(subA); len(got) != 0 {
		t.Errorf("an ended subscription received %v", typesOf(got))
	}
	if got := typesOf(drain(subB)); len(got) != 1 {
		t.Errorf("b's subscription received %v; want the event a's unsubscribe did not touch", got)
	}
	if f.a.Len() != 0 || f.bus.Subscriptions() != 1 {
		t.Errorf("the bus holds %d subscriptions, a holds %d; want 1 and 0", f.bus.Subscriptions(), f.a.Len())
	}
}

// TestSubscribeResumesFromSince: `since` reaches the bus, so a resume replays the ring
// after it, and another epoch starts with an events.gap (capability-api.md
// §Subscriptions).
func TestSubscribeResumesFromSince(t *testing.T) {
	f := newSessionFixture(t)
	for i := 0; i < 3; i++ {
		f.bus.Publish("workspace.lifecycle", "opening", "ws-1", "", map[string]string{"state": "opening"})
	}
	pos := f.bus.Position()

	id := f.subscribe(t, f.a, `{"topics":["workspace.lifecycle"],"since":{"epoch":"`+pos.Epoch+`","seq":1}}`)
	sub, _ := f.a.Subscription(id)
	if got := seqs(drain(sub)); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("a resume from seq 1 delivered %v; want 2, 3", got)
	}
	id = f.subscribe(t, f.a, `{"topics":["workspace.lifecycle"],"since":{"epoch":"d-other","seq":3}}`)
	sub, _ = f.a.Subscription(id)
	if got := drain(sub); len(got) == 0 || got[0].Topic != TopicGap {
		t.Errorf("a resume from another epoch delivered %v; want an events.gap first", typesOf(got))
	}
}

// TestASessionsEndClosesItsSubscriptions: ending a session leaves the bus with none of
// its subscriptions, ending it twice is harmless, and once it has ended it takes no
// new one, even from a subscribe racing the end (capability-api.md §Subscriptions:
// "the core also cleans up on disconnect").
func TestASessionsEndClosesItsSubscriptions(t *testing.T) {
	f := newSessionFixture(t)
	keep := f.subscribe(t, f.b, `{"topics":["workspace.lifecycle"]}`)
	f.subscribe(t, f.a, `{"topics":["workspace.lifecycle"]}`)
	f.subscribe(t, f.a, `{"topics":["operation.progress"]}`)
	if f.bus.Subscriptions() != 3 {
		t.Fatalf("the bus holds %d subscriptions; want 3", f.bus.Subscriptions())
	}
	f.a.Close()
	f.a.Close()
	if f.bus.Subscriptions() != 1 || f.a.Len() != 0 {
		t.Fatalf("after a's end the bus holds %d subscriptions and a %d; want 1 and 0", f.bus.Subscriptions(), f.a.Len())
	}
	if _, ok := f.b.Subscription(keep); !ok {
		t.Error("a's end closed b's subscription")
	}

	_, state, err := f.call(t, f.a, CapSubscribe, `{"topics":["workspace.lifecycle"]}`)
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrSessionRequired {
		t.Errorf("subscribing on an ended session: (%s, %v); want SESSION_REQUIRED", state, err)
	}
	if f.bus.Subscriptions() != 1 {
		t.Errorf("an ended session took a subscription: the bus holds %d", f.bus.Subscriptions())
	}

	// A subscribe racing the end never leaves one behind.
	for i := 0; i < 50; i++ {
		s := NewSession(f.bus, "ses-race")
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.subscribe([]string{"workspace.lifecycle"}, nil)
			}()
		}
		s.Close()
		wg.Wait()
		if f.bus.Subscriptions() != 1 {
			t.Fatalf("round %d: %d subscriptions on the bus after the session ended; want only b's", i, f.bus.Subscriptions())
		}
	}
}

// otherSession is a capability.Session that cannot carry events.
type otherSession struct{}

func (otherSession) ID() string { return "ses-other" }

// TestOnlyAnEventSessionCanSubscribe: a handler reached with a session of another kind
// still refuses, with SESSION_REQUIRED, rather than assuming (D6: "a session that can
// carry the capability's state and stream").
func TestOnlyAnEventSessionCanSubscribe(t *testing.T) {
	f := newSessionFixture(t)
	for _, name := range []string{CapSubscribe, CapUnsubscribe} {
		params := `{"topics":["workspace.lifecycle"]}`
		if name == CapUnsubscribe {
			params = `{"subscriptionId":"x"}`
		}
		_, state, err := f.call(t, otherSession{}, name, params)
		var e *capability.Error
		if !errors.As(err, &e) || e.Code != capability.ErrSessionRequired || state != "failed" {
			t.Errorf("%s with a session that cannot carry events: (%s, %v); want a failed operation with SESSION_REQUIRED", name, state, err)
		}
	}
	if f.bus.Subscriptions() != 0 {
		t.Errorf("the bus holds %d subscriptions", f.bus.Subscriptions())
	}
}

// TestTheSubscribableTopicsAreThoseThatArePublished: the set the enum publishes is the
// two topics of plan m1d, D4, spelled as the publishers spell them. The core cannot
// import this package, so the two strings exist twice, and this holds them together.
func TestTheSubscribableTopicsAreThoseThatArePublished(t *testing.T) {
	want := []string{workspace.LifecycleTopic, capability.ProgressTopic}
	if len(subscribable) != 2 || subscribable[0] != want[0] || subscribable[1] != want[1] {
		t.Errorf("the subscribable topics are %v; want %v", subscribable, want)
	}
	if TopicGap == subscribable[0] || TopicGap == subscribable[1] {
		t.Error("the gap marker is subscribable")
	}
}
