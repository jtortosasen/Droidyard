package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The session.state events (ADR-0083 §1): published by Sessions, under the lock that
// changes a session, so the events of one session are in the order of its changes and none
// follows its end.

// sessionRecorder is a StatePublisher that keeps what session.state carried.
type sessionRecorder struct {
	mu  sync.Mutex
	got []SessionState
	raw []string
}

func (r *sessionRecorder) Broadcast(topic, typ, workspace, operation string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if topic != SessionStateTopic || typ != SessionStateType || workspace != "" || operation != "" {
		panic(fmt.Sprintf("session.state published as %q/%q workspace %q operation %q", topic, typ, workspace, operation))
	}
	b, _ := json.Marshal(payload)
	r.got = append(r.got, payload.(SessionState))
	r.raw = append(r.raw, string(b))
}

func (r *sessionRecorder) events() []SessionState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

func newWatchedSessions() (*Sessions, *sessionRecorder) {
	s, r := NewSessions(), &sessionRecorder{}
	s.usePublisher(r)
	return s, r
}

// TestSessionStateFollowsEveryChange holds ADR-0083 §1: a session's events are, in order,
// its connect (the id alone), the name its client gave, each binding (the hello's and every
// move) and its end (the id and ended, nothing else); the state each carries is the whole
// state after the change. A name is bounded as ADR-0048 §1 says, one that is empty once
// bounded or is the one already recorded publishes nothing, and nothing is published for a
// session that is not registered or after its end. The decision it guards: a consumer that
// merges these events by id always holds the session as it is, and never a session that
// ended.
func TestSessionStateFollowsEveryChange(t *testing.T) {
	s, rec := newWatchedSessions()
	s.Connect("a", "chat-secret")
	s.Named("a", "  claude‮-code\x00  ")
	s.Named("a", "claude-code") // the same name once bounded
	s.Named("a", "‮ ")          // nothing once bounded
	s.Opened("a", "/main", "ws-main", "/main")
	s.Bound("a", "ws-wt", "/wt")
	s.End("a")
	// Nothing follows the end, and nothing is published for a session never connected.
	s.Named("a", "late")
	s.Bound("a", "ws-late", "/late")
	s.Opened("a", "/main", "ws-late", "/late")
	s.End("a")
	s.Named("never", "x")
	s.Bound("never", "ws", "/x")
	s.End("never")

	want := []SessionState{
		{ID: "a"},
		{ID: "a", Name: "claude-code"},
		{ID: "a", Name: "claude-code", Workspace: "ws-main"},
		{ID: "a", Name: "claude-code", Workspace: "ws-wt"},
		{ID: "a", Ended: true},
	}
	if got := rec.events(); !slices.Equal(got, want) {
		t.Errorf("session.state carried\n  %+v\nwant\n  %+v", got, want)
	}
	for _, raw := range rec.raw {
		if strings.Contains(raw, "chat-secret") {
			t.Errorf("an event carries the chat id (ADR-0088 §4): %s", raw)
		}
	}
}

// TestSessionListedIsOldestFirstWithoutTheChat holds ADR-0083 §1's snapshot: the sessions
// in progress, the oldest connection first (not by id, and not by the last change), with the
// name and workspace they have now, and no chat id; an ended session is not listed, and an
// empty registry lists an empty (not null) array.
func TestSessionListedIsOldestFirstWithoutTheChat(t *testing.T) {
	s, _ := newWatchedSessions()
	if raw, _ := json.Marshal(SessionWatchResult{Sessions: s.listed()}); !strings.Contains(string(raw), `"sessions":[]`) {
		t.Errorf("an empty registry lists %s; want an empty array", raw)
	}
	for _, id := range []string{"z", "m", "a"} {
		s.Connect(id, "chat-"+id)
	}
	s.Bound("z", "ws-z", "/z") // the oldest changes last: it stays first
	s.Named("m", "agent")
	s.Connect("gone", "")
	s.End("gone")

	list := s.listed()
	var ids []string
	for _, l := range list {
		ids = append(ids, l.ID)
		if l.ConnectedAt.IsZero() {
			t.Errorf("%s has no connectedAt", l.ID)
		}
	}
	if !slices.Equal(ids, []string{"z", "m", "a"}) {
		t.Errorf("listed %v; want z, m, a: the connection order", ids)
	}
	if list[0].Workspace != "ws-z" || list[1].Name != "agent" || list[2].Name != "" || list[2].Workspace != "" {
		t.Errorf("rows %+v do not carry the state each session has", list)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "chat-") {
		t.Errorf("the snapshot carries a chat id: %s", raw)
	}
}

// TestSessionStatesNeverFollowAnEnd is the claim session.watch rests on: whatever the
// interleaving, the events of one session are its connect, then changes, then one end, and
// nothing after it, because the change and its event are made under one lock. Many sessions
// are driven at once, each by a connect, two goroutines that bind and name it, and an
// end; the recorder keeps the order the bus would have seen (Broadcast is called under the
// registry's lock). Run with -race.
func TestSessionStatesNeverFollowAnEnd(t *testing.T) {
	s, rec := newWatchedSessions()
	var wg sync.WaitGroup
	const n = 200
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("s%d", i)
		s.Connect(id, "")
		wg.Add(3)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				s.Bound(id, "ws", "/r")
			}
		}()
		go func() {
			defer wg.Done()
			s.Named(id, "name")
			s.Opened(id, "/p", "ws2", "/r2")
		}()
		go func() {
			defer wg.Done()
			s.End(id)
		}()
	}
	wg.Wait()

	state := map[string]string{} // "" none, "open", "ended"
	for _, e := range rec.events() {
		switch state[e.ID] {
		case "":
			if e.Name != "" || e.Workspace != "" || e.Ended {
				t.Fatalf("%s: the first event is %+v; want the connect, the id alone", e.ID, e)
			}
			state[e.ID] = "open"
		case "open":
			if e.Ended {
				state[e.ID] = "ended"
			}
		case "ended":
			t.Fatalf("%s: %+v was published after its end", e.ID, e)
		}
	}
	for i := 0; i < n; i++ {
		if got := state[fmt.Sprintf("s%d", i)]; got != "ended" {
			t.Fatalf("s%d: its events end in %q; want ended", i, got)
		}
	}
}

// TestSessionWatchNeedsAFollower: a session that cannot carry a subscription is refused
// SESSION_REQUIRED, naming the WebSocket, and no subscription is left behind; one that can
// is given its subscription before the snapshot is taken (the seam shows a connect made
// between the two is in the snapshot) and its subscription is closed when the call is
// cancelled.
func TestSessionWatchNeedsAFollower(t *testing.T) {
	ops := NewOperations(NewRegistry())
	ops.RegisterCapabilities()
	d, ok := ops.reg.Lookup(SessionWatch)
	if !ok {
		t.Fatal("session.watch is not registered")
	}
	if _, err := d.Handler(context.Background(), &Call{Session: nil}); err == nil {
		t.Fatal("session.watch answered a call with no session")
	} else if e, ok := err.(*Error); !ok || e.Code != ErrSessionRequired || !strings.Contains(e.NextStep, "GET /v1/ws") {
		t.Errorf("err = %v; want SESSION_REQUIRED naming GET /v1/ws", err)
	}

	f := &fakeFollower{}
	ops.Sessions().SetWatchHook(func() { ops.Sessions().Connect("mid", "") })
	got, err := d.Handler(context.Background(), &Call{Session: f})
	if err != nil {
		t.Fatal(err)
	}
	res := got.(SessionWatchResult)
	if f.topics == nil || len(f.topics) != 1 || f.topics[0] != SessionStateTopic || res.SubscriptionID != "sub-1" {
		t.Errorf("followed %v, answered %+v; want session.state and sub-1", f.topics, res)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].ID != "mid" {
		t.Errorf("the snapshot is %+v; want the session that connected after the subscription opened", res.Sessions)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.unsubscribed = nil
	if _, err := d.Handler(ctx, &Call{Session: f}); err == nil || len(f.unsubscribed) != 1 {
		t.Errorf("a cancelled call: err %v, unsubscribed %v; want an error and its subscription closed", err, f.unsubscribed)
	}
}

type fakeFollower struct {
	topics       []string
	unsubscribed []string
}

func (f *fakeFollower) ID() string { return "ws-1" }
func (f *fakeFollower) Follow(topics []string) (string, error) {
	f.topics = topics
	return "sub-1", nil
}
func (f *fakeFollower) Unsubscribe(id string) { f.unsubscribed = append(f.unsubscribed, id) }
