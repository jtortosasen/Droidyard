package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/store"
)

// watchHarness is the shipped workspace registry over a real event bus, as the daemon
// wires it: lifecycle on Publish, workspace.state on Broadcast. Its clock moves one
// minute per reading, so "newest" is a fact of the order of the opens.
type watchHarness struct {
	ws    *Registry
	ops   *capability.Operations
	bus   *event.Bus
	st    *store.Store
	clock atomic.Int64
}

// trustRoots is a Policy that trusts the roots it holds.
type trustRoots map[string]bool

func (p trustRoots) Trusted(root string) (bool, *capability.Error) { return p[root], nil }

func newWatchHarness(t *testing.T, policy Policy) *watchHarness {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &watchHarness{st: st, bus: event.New(event.Options{})}
	reg := capability.NewRegistry()
	h.ops = capability.NewOperations(reg)
	h.ops.RegisterCapabilities()
	h.ws = New(Options{Store: st, Ops: h.ops, Policy: policy, Publisher: h.bus, States: h.bus})
	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	h.ws.now = func() time.Time { return base.Add(time.Duration(h.clock.Add(1)) * time.Minute) }
	Register(reg, h.ws)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.ops.Shutdown(ctx)
	})
	return h
}

func (h *watchHarness) open(t *testing.T, root string) Info {
	t.Helper()
	out, err := h.ops.Invoke(context.Background(), capability.Request{Capability: CapOpen, Workspace: root})
	if err != nil || out.State != capability.StateCompleted {
		t.Fatalf("open %s: %v %+v", root, err, out)
	}
	return out.Result.(Info)
}

func (h *watchHarness) closeWS(t *testing.T, root string) {
	t.Helper()
	out, err := h.ops.Invoke(context.Background(), capability.Request{Capability: CapClose, Workspace: root})
	if err != nil || out.State != capability.StateCompleted {
		t.Fatalf("close %s: %v %+v", root, err, out)
	}
}

// watch calls workspace.watch on a new session.
func (h *watchHarness) watch(t *testing.T) (WatchResult, *event.Subscription, *event.Session) {
	t.Helper()
	sess := event.NewSession(h.bus, "watcher")
	t.Cleanup(sess.Close)
	out, err := h.ops.Invoke(context.Background(), capability.Request{Capability: CapWatch, Session: sess})
	if err != nil || out.State != capability.StateCompleted {
		t.Fatalf("workspace.watch: %v %+v", err, out)
	}
	res := out.Result.(WatchResult)
	sub, ok := sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatalf("the session holds no subscription %s", res.SubscriptionID)
	}
	return res, sub, sess
}

// drain returns what the subscription has queued.
func drain(sub *event.Subscription) []event.Event {
	var out []event.Event
	for {
		e, ok := sub.TryNext()
		if !ok {
			return out
		}
		out = append(out, e)
	}
}

func stateOf(t *testing.T, e event.Event) StateEvent {
	t.Helper()
	if e.Topic != StateTopic || e.Type != EventState {
		t.Fatalf("event %s/%s; want %s/%s", e.Topic, e.Type, StateTopic, EventState)
	}
	var s StateEvent
	if err := json.Unmarshal(e.Payload, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func find(list []Listed, id string) (Listed, bool) {
	for _, l := range list {
		if l.ID == id {
			return l, true
		}
	}
	return Listed{}, false
}

// TestWorkspaceWatchMissesNothing: the decision of ADR-0045 §3: the subscription opens
// first and the snapshot second, so a workspace opened between the two reaches the
// watcher in the snapshot and as an event. A snapshot taken first would miss it twice.
func TestWorkspaceWatchMissesNothing(t *testing.T) {
	h := newWatchHarness(t, nil)
	var opened Info
	root := project(t)
	h.ws.SetWatchHook(func() { opened = h.open(t, root) })
	res, sub, _ := h.watch(t)
	h.ws.SetWatchHook(nil)

	if opened.ID == "" {
		t.Fatal("the hook did not run: the window was not exercised")
	}
	got, ok := find(res.Workspaces, opened.ID)
	if !ok || got.State != Ready || got.Root != root {
		t.Errorf("the snapshot lists %+v; want the workspace opened after the subscription, ready", res.Workspaces)
	}
	var states []State
	for _, e := range drain(sub) {
		if e.Workspace != nil && *e.Workspace == opened.ID {
			states = append(states, stateOf(t, e).State)
		}
	}
	if len(states) != 2 || states[0] != Opening || states[1] != Ready {
		t.Errorf("the watcher saw the states %v of the workspace; want opening, ready", states)
	}
}

// TestWorkspaceWatchListsTheNewestFiftyWithTheirLiveState: sixty known workspaces are
// cut to the fifty most recently opened with truncated set (plan m2c D18), and one that
// is open now carries its live state and is first, because it was opened last.
func TestWorkspaceWatchListsTheNewestFiftyWithTheirLiveState(t *testing.T) {
	h := newWatchHarness(t, nil)
	idx := index{Workspaces: map[string]indexEntry{}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("p%02d-0123456789ab", i)
		idx.Workspaces[id] = indexEntry{Root: "/projects/" + id, Name: id, LastOpenedAt: base.Add(time.Duration(i) * time.Hour)}
	}
	if err := h.st.Write(indexFile, idx); err != nil {
		t.Fatal(err)
	}
	res, _, _ := h.watch(t)
	if len(res.Workspaces) != MaxListed || !res.Truncated {
		t.Fatalf("%d listed, truncated %v; want %d and truncated", len(res.Workspaces), res.Truncated, MaxListed)
	}
	if first, last := res.Workspaces[0], res.Workspaces[MaxListed-1]; first.ID != "p59-0123456789ab" || last.ID != "p10-0123456789ab" {
		t.Errorf("the list runs from %s to %s; want the newest, p59, down to p10", first.ID, last.ID)
	}
	for _, l := range res.Workspaces {
		if l.State != Closed {
			t.Errorf("%s is %s; a workspace this daemon never opened is closed", l.ID, l.State)
		}
	}

	root := project(t)
	info := h.open(t, root)
	res, _, _ = h.watch(t)
	if got := res.Workspaces[0]; got.ID != info.ID || got.State != Ready || got.Name != info.Name || got.Root != root {
		t.Errorf("the first entry is %+v; want the workspace opened last, ready", got)
	}
	if !res.Truncated || len(res.Workspaces) != MaxListed {
		t.Errorf("61 known: %d listed, truncated %v", len(res.Workspaces), res.Truncated)
	}
	h.closeWS(t, root)
	res, _, _ = h.watch(t)
	if got, _ := find(res.Workspaces, info.ID); got.State != Closed {
		t.Errorf("after the close the entry is %s; want closed", got.State)
	}
}

// TestReopenOfAnOpenWorkspaceRefreshesLastOpened: ADR-0045 §3 refreshes lastOpenedAt on
// every open, one of an open workspace included: opening A, then B, then A again puts A
// first. Before it, only a closed workspace's open wrote the index.
func TestReopenOfAnOpenWorkspaceRefreshesLastOpened(t *testing.T) {
	h := newWatchHarness(t, nil)
	a, b := project(t), project(t)
	infoA := h.open(t, a)
	infoB := h.open(t, b)
	res, _, _ := h.watch(t)
	if res.Workspaces[0].ID != infoB.ID {
		t.Fatalf("after opening a then b the first is %s; want b", res.Workspaces[0].ID)
	}
	before := res.Workspaces[1].LastOpenedAt
	h.open(t, a) // a is open: nothing but its recency changes
	res, _, _ = h.watch(t)
	if res.Workspaces[0].ID != infoA.ID || !res.Workspaces[0].LastOpenedAt.After(before) {
		t.Errorf("after opening a again the list is %+v; want a first, with a later lastOpenedAt than %v", res.Workspaces, before)
	}
	if got := res.Workspaces[0].State; got != Ready {
		t.Errorf("a reopened workspace is %s; want ready", got)
	}
}

// TestStateEventsCarryNameAndRoot: every transition is on workspace.state with the
// workspace's name, root and trust, the first of an open included, which happens before
// the workspace has loaded (ADR-0045 §3).
func TestStateEventsCarryNameAndRoot(t *testing.T) {
	root := project(t)
	h := newWatchHarness(t, trustRoots{root: true})
	_, sub, _ := h.watch(t)
	info := h.open(t, root)
	h.closeWS(t, root)

	var got []StateEvent
	for _, e := range drain(sub) {
		if *e.Workspace != info.ID {
			t.Errorf("event for %s; want only %s", *e.Workspace, info.ID)
		}
		got = append(got, stateOf(t, e))
	}
	want := []State{Opening, Ready, Closing, Closed}
	if len(got) != len(want) {
		t.Fatalf("%d events %+v; want %v", len(got), got, want)
	}
	for i, s := range got {
		if s.State != want[i] || s.Name != filepath.Base(root) || s.Root != root || !s.Trusted {
			t.Errorf("event %d is %+v; want %s, name %s, root %s, trusted", i, s, want[i], filepath.Base(root), root)
		}
	}
}

// TestLifecycleUnchanged: workspace.lifecycle keeps its payload, {state, degradedBy?},
// for events.subscribe (ADR-0045 §3); name, root and trust are the new topic's alone,
// and workspace.state is not a topic events.subscribe serves, so it stays out of the ring.
func TestLifecycleUnchanged(t *testing.T) {
	h := newWatchHarness(t, nil)
	life := h.bus.Subscribe([]string{LifecycleTopic}, nil)
	defer life.Close()
	root := project(t)
	h.open(t, root)
	h.closeWS(t, root)

	var types []string
	for _, e := range drain(life) {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(e.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 1 || raw["state"] == nil {
			t.Errorf("a %s payload is %s; want {state} only", e.Type, e.Payload)
		}
		types = append(types, e.Type)
	}
	if fmt.Sprint(types) != "[opening ready closing closed]" {
		t.Errorf("lifecycle types %v; want opening ready closing closed", types)
	}
	// A late subscriber resuming from the start finds no workspace.state in the ring.
	late := h.bus.Subscribe([]string{LifecycleTopic, StateTopic}, &event.Position{Epoch: h.bus.Position().Epoch, Seq: 0})
	defer late.Close()
	for _, e := range drain(late) {
		if e.Topic == StateTopic {
			t.Errorf("workspace.state was replayed from the ring: %+v", e)
		}
	}
}

// TestWatchNeedsASessionThatCarriesEvents: a call without one is refused at Start.
func TestWatchNeedsASessionThatCarriesEvents(t *testing.T) {
	h := newWatchHarness(t, nil)
	_, err := h.ops.Invoke(context.Background(), capability.Request{Capability: CapWatch})
	if codeOf(err) != capability.ErrSessionRequired {
		t.Errorf("workspace.watch without a session: %v; want SESSION_REQUIRED", err)
	}
}

// TestTheWatchListsWhatANewerIndexHidesOnceOpened: workspaces.json of a newer version is
// neither read nor written, but the workspaces this daemon opened still list.
func TestTheWatchListsWhatANewerIndexHidesOnceOpened(t *testing.T) {
	h := newWatchHarness(t, nil)
	newer := index{Workspaces: map[string]indexEntry{"x": {Root: "/x", Name: "x"}}}
	if err := h.st.WriteVersion(indexFile, store.Version+1, newer); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.st.Root(), indexFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	root := project(t)
	info := h.open(t, root)
	res, _, _ := h.watch(t)
	if len(res.Workspaces) != 1 || res.Workspaces[0].ID != info.ID {
		t.Errorf("the list is %+v; want only the workspace opened by this daemon", res.Workspaces)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("the newer index was rewritten: %s", after)
	}
}
