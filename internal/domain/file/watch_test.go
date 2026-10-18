package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// newWatchFixture is the read/write fixture with the daemon's bus, a poller set and a
// session, and a short poll tick so a watch the handler starts actually ticks (plan m1j
// step 4). The manual-tick tests do not need it; the poller tests use a long tick.
func newWatchFixture(t *testing.T) *fixture { return newWatchFixtureTick(t, 5*time.Millisecond) }

func newWatchFixtureTick(t *testing.T, tick time.Duration) *fixture {
	t.Helper()
	bus := event.New(event.Options{})
	ws := NewWatchers()
	f := newFixtureWith(t, func(o *Options) { o.Bus = bus; o.Watchers = ws; o.Tick = tick })
	f.bus, f.watchers = bus, ws
	f.sess = event.NewSession(bus, "ses-watch")
	return f
}

func (f *fixture) mkdir(rel string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.root, filepath.FromSlash(rel)), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) remove(rel string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.root, filepath.FromSlash(rel))); err != nil {
		f.t.Fatal(err)
	}
}

// watchCall runs file.watch's handler with the given session (nil for no session) and
// workspace ws.
func (f *fixture) watchCall(sess *event.Session, params WatchParams) (WatchResult, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup("file.watch")
	if !ok {
		f.t.Fatal("file.watch is not registered")
	}
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", OperationID: "op-watch", Params: params, Session: sess})
	if err != nil {
		return WatchResult{}, capability.AsError(err)
	}
	return res.(WatchResult), nil
}

// startManualWatch walks rel as the baseline and returns the watcher, its subscription
// and the session, so a test drives the poll with tickOnce rather than a ticker.
func (f *fixture) startManualWatch(rel string) (*watcher, *event.Subscription, *event.Session) {
	f.t.Helper()
	sess := event.NewSession(f.bus, "ses-manual")
	root, err := os.OpenRoot(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	w := &watcher{
		watchers: f.watchers, bus: f.bus, root: root, rel: rel,
		workspace: "ws", operation: "op-manual", tick: time.Hour,
		stop: make(chan string, 1),
	}
	id, sub, err := sess.Open([]string{TopicFileChanges}, func() { w.requestStop("") })
	if err != nil {
		f.t.Fatal(err)
	}
	w.sub = sub
	base, _, cerr := w.walk(root.FS(), rel)
	if cerr != nil {
		f.t.Fatal(cerr)
	}
	w.baseline = base
	f.watchers.add(w)
	f.t.Cleanup(func() {
		w.requestStop("")
		sess.Unsubscribe(id)
		f.watchers.remove(w)
	})
	return w, sub, sess
}

// drainEvents takes everything the subscription holds now.
func drainEvents(s *event.Subscription) []event.Event {
	var out []event.Event
	for {
		e, ok := s.TryNext()
		if !ok {
			return out
		}
		out = append(out, e)
	}
}

// oneChanges is the single changes event a tick published, decoded.
func oneChanges(t *testing.T, sub *event.Subscription) ChangesPayload {
	t.Helper()
	got := drainEvents(sub)
	if len(got) != 1 {
		t.Fatalf("the tick published %d events; want one changes event", len(got))
	}
	if got[0].Topic != TopicFileChanges || got[0].Type != TypeChanges {
		t.Fatalf("the event is %s/%s; want file.changes/changes", got[0].Topic, got[0].Type)
	}
	var p ChangesPayload
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("the payload: %v", err)
	}
	return p
}

func kinds(changes []FileChange) map[string]string {
	m := map[string]string{}
	for _, c := range changes {
		m[c.Path] = c.Kind
	}
	return m
}

// TestWatchTickReportsCreatedModifiedDeleted: a tick publishes only what changed since
// the last walk — an entry that appeared is created, one whose size or modification time
// differs is modified, one that vanished is deleted — and a tick with no change publishes
// nothing at all (plan m1j, D7: "empty is a fact").
func TestWatchTickReportsCreatedModifiedDeleted(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/a.txt", "one")
	w, sub, _ := f.startManualWatch("w")

	w.tickOnce()
	if got := drainEvents(sub); len(got) != 0 {
		t.Fatalf("a tick with no change published %d events", len(got))
	}

	f.write("w/b.txt", "two")
	w.tickOnce()
	switch p := oneChanges(t, sub); {
	case p.Path != "w" || p.Truncated:
		t.Errorf("the changes payload is %+v; want path \"w\" and no truncation", p)
	case len(p.Changes) != 1 || kinds(p.Changes)["w/b.txt"] != "created":
		t.Errorf("a new file reported %+v; want w/b.txt created", p.Changes)
	}

	// Grow a.txt, so the size differs: the mtime is not relied on.
	f.write("w/a.txt", "one-and-a-bit")
	w.tickOnce()
	switch p := oneChanges(t, sub); {
	case len(p.Changes) != 1 || kinds(p.Changes)["w/a.txt"] != "modified":
		t.Errorf("a grown file reported %+v; want w/a.txt modified", p.Changes)
	}

	f.remove("w/b.txt")
	w.tickOnce()
	switch p := oneChanges(t, sub); {
	case len(p.Changes) != 1 || kinds(p.Changes)["w/b.txt"] != "deleted":
		t.Errorf("a removed file reported %+v; want w/b.txt deleted", p.Changes)
	}
}

// TestWatchTickSeesANewDirectory: a new directory holding two files is three created
// entries in one event, the directory first by name (plan m1j step 4 (a)).
func TestWatchTickSeesANewDirectory(t *testing.T) {
	f := newWatchFixture(t)
	f.mkdir("d")
	w, sub, _ := f.startManualWatch("d")

	f.write("d/sub/f1", "1")
	f.write("d/sub/f2", "2")
	w.tickOnce()
	p := oneChanges(t, sub)
	got := kinds(p.Changes)
	want := map[string]string{"d/sub": "created", "d/sub/f1": "created", "d/sub/f2": "created"}
	if len(got) != len(want) {
		t.Fatalf("the new directory reported %+v; want %+v", got, want)
	}
	for path, kind := range want {
		if got[path] != kind {
			t.Errorf("%s is %q; want %q", path, got[path], kind)
		}
	}
}

// TestWatchTickSeesARenameAsDeleteAndCreate: a rename is a deletion and a creation, not
// a move: the walk records paths, not inodes (plan m1j step 4 (a)).
func TestWatchTickSeesARenameAsDeleteAndCreate(t *testing.T) {
	f := newWatchFixture(t)
	f.write("r/old.txt", "x")
	w, sub, _ := f.startManualWatch("r")

	if err := os.Rename(filepath.Join(f.root, "r", "old.txt"), filepath.Join(f.root, "r", "new.txt")); err != nil {
		t.Fatal(err)
	}
	w.tickOnce()
	got := kinds(oneChanges(t, sub).Changes)
	if got["r/old.txt"] != "deleted" || got["r/new.txt"] != "created" || len(got) != 2 {
		t.Errorf("a rename reported %+v; want r/old.txt deleted and r/new.txt created", got)
	}
}

// TestWatchExclusions: the walk records nothing under .git, .gradle, a build directory
// beside a build.gradle.kts, or a file.write temporary name, but a source package named
// build is kept (plan m1j, D7 and review B4's sibling: exclusion is by rule, not by a
// name anywhere). The baseline a test reads is the walk's own record.
func TestWatchExclusions(t *testing.T) {
	f := newWatchFixture(t)
	f.write("x/real.txt", "keep")
	f.write("x/.git/objects/o", "no")
	f.write("x/.gradle/cache/c", "no")
	f.mkdir("x/.idea")
	f.write("x/app/build.gradle.kts", "plugins {}")
	f.write("x/app/build/output/t", "no")
	f.write("x/app/src/main/java/a/build/X.java", "keep")
	f.write("x/.notes.droidyard-01234567.tmp", "no")

	w := &watcher{root: mustOpenRoot(t, f.root)}
	defer w.root.Close()
	base, truncated, cerr := w.walk(w.root.FS(), "x")
	if cerr != nil {
		t.Fatal(cerr)
	}
	if truncated {
		t.Fatal("the walk was truncated")
	}
	for _, keep := range []string{"x/real.txt", "x/app/build.gradle.kts", "x/app/src/main/java/a/build/X.java"} {
		if _, ok := base[keep]; !ok {
			t.Errorf("the walk recorded nothing for %s; want it kept", keep)
		}
	}
	for _, gone := range []string{"x/.git", "x/.git/objects", "x/.git/objects/o", "x/.gradle", "x/.gradle/cache/c", "x/.idea", "x/app/build", "x/app/build/output/t", "x/.notes.droidyard-01234567.tmp"} {
		if _, ok := base[gone]; ok {
			t.Errorf("the walk recorded %s; want it excluded", gone)
		}
	}
}

func mustOpenRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestWatchRecordsASymlinkWithoutWalkingIt: a symlink is one entry of type symlink and
// the walk never descends it, so esc -> /etc adds nothing under esc (plan m1j, D7).
func TestWatchRecordsASymlinkWithoutWalkingIt(t *testing.T) {
	f := newWatchFixture(t)
	_, _, _ = f.startManualWatch(".")
	w := &watcher{root: mustOpenRoot(t, f.root)}
	defer w.root.Close()
	base, _, cerr := w.walk(w.root.FS(), ".")
	if cerr != nil {
		t.Fatal(cerr)
	}
	if got, ok := base["esc"]; !ok || got.typ != "symlink" {
		t.Fatalf("esc is %+v; want one symlink entry", got)
	}
	for p := range base {
		if strings.HasPrefix(p, "esc/") {
			t.Errorf("the walk descended esc: it recorded %s", p)
		}
		if strings.HasPrefix(p, "in/") {
			t.Errorf("the walk descended the in-root symlink in: it recorded %s", p)
		}
	}
}

// TestWatchWalkIsCapped: a directory of 20001 entries is watched as 20000 with truncated
// true, and the walk is timed for the plan's evidence (plan m1j, D7 and the Unverified
// row "a 1 s walk of 20 000 entries is cheap").
func TestWatchWalkIsCapped(t *testing.T) {
	f := newWatchFixtureTick(t, time.Hour) // no ticker: only the baseline walks
	f.mkdir("big")
	for i := 0; i < 20001; i++ {
		name := filepath.Join(f.root, "big", "f"+pad(i)+".txt")
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.watchCall(f.sess, WatchParams{Path: "big"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Watched != 20000 || !res.Truncated {
		t.Fatalf("a 20001-entry directory watched %d entries (truncated %v); want 20000 and truncated", res.Watched, res.Truncated)
	}
	// ADR-0049 §1: the result's entries are capped with the baseline, not beyond it.
	if len(res.Entries) != 20000 {
		t.Fatalf("a 20001-entry directory returned %d entries; want 20000", len(res.Entries))
	}

	w := &watcher{root: mustOpenRoot(t, f.root)}
	defer w.root.Close()
	start := time.Now()
	base, truncated, cerr := w.walk(w.root.FS(), "big")
	elapsed := time.Since(start)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if len(base) != 20000 || !truncated {
		t.Fatalf("the walk recorded %d entries (truncated %v); want 20000 and truncated", len(base), truncated)
	}
	t.Logf("a 20000-entry walk took %v (plan m1j, the Unverified timing row)", elapsed)
}

func pad(i int) string {
	s := "00000"
	d := []byte(s)
	for j := len(d) - 1; j >= 0 && i > 0; j-- {
		d[j] = byte('0' + i%10)
		i /= 10
	}
	return string(d)
}

// TestWatchCappedTickReportsNoDeletion: when a tick's walk is truncated, the entries the
// cap pushed out are not deletions — the event is truncated and names no deletion (plan
// m1j, D7 and review B4). The mutation that reports them fails here.
func TestWatchCappedTickReportsNoDeletion(t *testing.T) {
	f := newWatchFixture(t)
	f.mkdir("big")
	for i := 0; i < 20000; i++ {
		name := filepath.Join(f.root, "big", "f"+pad(i)+".txt")
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w, sub, _ := f.startManualWatch("big")

	// A file that sorts before every fNNNNN.txt, so it enters the walk early and pushes
	// the last entry past the cap.
	if err := os.WriteFile(filepath.Join(f.root, "big", "f-0000.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w.tickOnce()
	p := oneChanges(t, sub)
	if !p.Truncated {
		t.Errorf("a capped walk's event is not truncated: %+v", p)
	}
	for _, c := range p.Changes {
		if c.Kind == "deleted" {
			t.Fatalf("a capped walk reported %s deleted; a cap is not a deletion", c.Path)
		}
	}
	if kinds(p.Changes)["big/f-0000.txt"] != "created" {
		t.Errorf("the new early file was not reported created: %+v", p.Changes)
	}
}

// TestWatchChangeListIsCapped: more than 1000 changes in one tick are reported as 1000
// with truncated true (plan m1j, D8).
func TestWatchChangeListIsCapped(t *testing.T) {
	f := newWatchFixture(t)
	f.mkdir("c")
	w, sub, _ := f.startManualWatch("c")

	for i := 0; i < 1001; i++ {
		if err := os.WriteFile(filepath.Join(f.root, "c", "f"+pad(i)+".txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.tickOnce()
	p := oneChanges(t, sub)
	if len(p.Changes) != 1000 || !p.Truncated {
		t.Fatalf("1001 changes reported as %d (truncated %v); want 1000 and truncated", len(p.Changes), p.Truncated)
	}
	if !sort.SliceIsSorted(p.Changes, func(i, j int) bool { return p.Changes[i].Path < p.Changes[j].Path }) {
		t.Error("the changes are not sorted by path")
	}
}

// TestWatchHandlerResultAndErrors: file.watch returns a subscriptionId at once, and its
// path errors are the ones its descriptor lists: FILE_NOT_FOUND for a missing directory,
// FILE_WRONG_TYPE for a file (plan m1j, D7).
func TestWatchHandlerResultAndErrors(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/a.txt", "x")

	res, err := f.watchCall(f.sess, WatchParams{Path: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.SubscriptionID, "sub-") || res.Watched != 1 || res.Truncated {
		t.Fatalf("a watch of a one-file directory: %+v; want a sub id watching 1", res)
	}
	if len(res.Excluded) == 0 {
		t.Error("the result names no exclusions")
	}
	f.sess.Unsubscribe(res.SubscriptionID)
	waitLive(t, f.watchers, 0)

	_, err = f.watchCall(f.sess, WatchParams{Path: "missing"})
	wantCode(t, err, capability.ErrFileNotFound)
	_, err = f.watchCall(f.sess, WatchParams{Path: "w/a.txt"})
	wantCode(t, err, capability.ErrFileWrongType)
	_, err = f.watchCall(f.sess, WatchParams{Path: "../outside"})
	wantCode(t, err, capability.ErrFileOutsideRoot)
	_, err = f.watchCall(f.sess, WatchParams{Path: "a\x00b"})
	wantCode(t, err, capability.ErrInvalidParams)
	// D9: a call that returns no id leaves no subscription behind.
	if n := f.sess.Len(); n != 0 {
		t.Errorf("after the refused watches the session holds %d subscriptions; want 0", n)
	}
	if n := f.bus.Subscriptions(); n != 0 {
		t.Errorf("after the refused watches the bus holds %d subscriptions; want 0", n)
	}
}

// TestWatchNeedsASession: file.watch is streaming, so a call without an event session is
// SESSION_REQUIRED, and a registry without the daemon's bus answers the same rather than
// panicking (plan m1j, D9; ADR-0010).
func TestWatchNeedsASession(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/a.txt", "x")
	_, err := f.watchCall(nil, WatchParams{Path: "w"})
	wantCode(t, err, capability.ErrSessionRequired)

	// The golden registry's descriptors have no bus: a watch still hears SESSION_REQUIRED
	// even with a session, because it cannot publish.
	g := newFixture(t)
	g.sess = event.NewSession(event.New(event.Options{}), "ses-golden")
	_, err = g.watchCall(g.sess, WatchParams{Path: "src"})
	wantCode(t, err, capability.ErrSessionRequired)
}

// TestWatchCancelDuringTheBaselineWalk: a cancel that lands while the baseline walks
// ends the subscription and starts no poller, so nothing outlives a cancelled operation
// (plan m1j, D9). watchWalkHook holds the walk open until the test releases it.
func TestWatchCancelDuringTheBaselineWalk(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/a.txt", "x")

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	watchWalkHook = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	defer func() { watchWalkHook = nil }()

	d, _ := f.reg.Lookup("file.watch")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.Handler(ctx, &capability.Call{Workspace: "ws", OperationID: "op-cancel", Params: WatchParams{Path: "w"}, Session: f.sess})
		done <- err
	}()
	<-entered
	cancel()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("a cancelled watch returned no error")
	}
	waitLive(t, f.watchers, 0)
	if f.sess.Len() != 0 {
		t.Errorf("a cancelled watch left %d subscriptions on the session", f.sess.Len())
	}
	if f.bus.Subscriptions() != 0 {
		t.Errorf("a cancelled watch left %d subscriptions on the bus", f.bus.Subscriptions())
	}
}

// waitLive waits for the poller set's live count to reach want.
func waitLive(t *testing.T, ws *Watchers, want int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if ws.Live() == want {
			return
		}
	}
	t.Fatalf("the live poller count is %d; want %d", ws.Live(), want)
}

// TestWatchEndsWithItsSubscription: a watch started by the handler ends when its
// subscription ends — events.unsubscribe of its id, or the session's end — so no poller
// is left running (plan m1j, D9). The subscription's close hook is what stops it; the
// mutation that leaves the poller running fails the live-poller assertion.
func TestWatchEndsWithItsSubscription(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/a.txt", "x")
	res, err := f.watchCall(f.sess, WatchParams{Path: "w"})
	if err != nil {
		t.Fatal(err)
	}
	waitLive(t, f.watchers, 1)

	f.sess.Unsubscribe(res.SubscriptionID)
	waitLive(t, f.watchers, 0)
	if f.sess.Len() != 0 || f.bus.Subscriptions() != 0 {
		t.Errorf("after the unsubscribe: %d session subscriptions, %d on the bus", f.sess.Len(), f.bus.Subscriptions())
	}

	// A session's end ends its watches the same way.
	res, err = f.watchCall(f.sess, WatchParams{Path: "w"})
	if err != nil {
		t.Fatal(err)
	}
	waitLive(t, f.watchers, 1)
	f.sess.Close()
	waitLive(t, f.watchers, 0)
	if f.sess.Len() != 0 || f.bus.Subscriptions() != 0 {
		t.Errorf("after the session end: %d session subscriptions, %d on the bus", f.sess.Len(), f.bus.Subscriptions())
	}
}

// TestWatchWorkspaceCloseStopsPollers: a workspace close stops every poller of that
// workspace and each publishes stopped as its own last act, after which nothing follows
// it; the subscription stays open until the session ends (plan m1j, D8, D9).
func TestWatchWorkspaceCloseStopsPollers(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/a.txt", "x")
	res, err := f.watchCall(f.sess, WatchParams{Path: "w"})
	if err != nil {
		t.Fatal(err)
	}
	waitLive(t, f.watchers, 1)

	f.watchers.Stop("ws")
	waitLive(t, f.watchers, 0)

	sub, ok := f.sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatal("the subscription ended with the workspace close; it must stay until the session ends")
	}
	got := drainEvents(sub)
	if len(got) != 1 || got[0].Type != TypeStopped {
		t.Fatalf("the workspace close delivered %+v; want one stopped event", got)
	}
	var sp StoppedPayload
	if err := json.Unmarshal(got[0].Payload, &sp); err != nil {
		t.Fatal(err)
	}
	if sp.Reason != ReasonWorkspaceClosed || sp.Path != "w" {
		t.Errorf("the stopped payload is %+v; want reason %q and path \"w\"", sp, ReasonWorkspaceClosed)
	}
	if e, ok := sub.TryNext(); ok {
		t.Errorf("a frame followed stopped: %+v", e)
	}

	f.sess.Close()
	if f.sess.Len() != 0 || f.bus.Subscriptions() != 0 {
		t.Errorf("after the session end: %d session subscriptions, %d on the bus", f.sess.Len(), f.bus.Subscriptions())
	}
}

// TestWatchEventCarriesTheCallIdentity: a change event names the workspace and, as
// operation, the file.watch call's id (plan m1j, D8).
func TestWatchEventCarriesTheCallIdentity(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/a.txt", "x")
	res, err := f.watchCall(f.sess, WatchParams{Path: "w"})
	if err != nil {
		t.Fatal(err)
	}
	sub, ok := f.sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatal("the result named an unknown subscription")
	}
	f.write("w/b.txt", "y")
	var e event.Event
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if e, ok = sub.TryNext(); ok {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if e.Topic != TopicFileChanges {
		t.Fatalf("no file.changes event arrived (got %+v)", e)
	}
	if e.Operation != "op-watch" || e.Workspace == nil || *e.Workspace != "ws" {
		t.Errorf("the event carries operation %q and workspace %v; want op-watch and ws", e.Operation, e.Workspace)
	}
	f.sess.Close()
}

// Decision (m1j §Changes, step 4 B1; capability-api.md §Empty is a fact): a watched
// directory that disappears has every entry reported deleted, then the watch stops with
// reason pathGone; a tick never takes a failed walk for "nothing changed".
func TestWatchTickReportsAGoneDirectory(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/a.txt", "a")
	f.write("w/b.txt", "b")
	w, sub, _ := f.startManualWatch("w")
	if err := os.RemoveAll(filepath.Join(f.root, "w")); err != nil {
		t.Fatal(err)
	}
	w.tickOnce()
	got := oneChanges(t, sub)
	if len(got.Changes) != 2 || got.Changes[0].Kind != "deleted" || got.Changes[1].Kind != "deleted" {
		t.Fatalf("a removed watch path reported %+v; want a.txt and b.txt deleted", got.Changes)
	}
	select {
	case reason := <-w.stop:
		if reason != ReasonPathGone {
			t.Fatalf("stop reason %q; want %q", reason, ReasonPathGone)
		}
	default:
		t.Fatal("the watch was not stopped after its path disappeared")
	}
}

// Decision (step 4 B1): a subdirectory a tick cannot read keeps its last known entries,
// unchanged, and the rest of the tree is still reported.
func TestWatchTickKeepsAnUnreadableSubdirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	f := newWatchFixture(t)
	f.write("w/sub/x.txt", "x")
	w, sub, _ := f.startManualWatch("w")
	locked := filepath.Join(f.root, "w", "sub")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	f.write("w/c.txt", "c")
	w.tickOnce()
	got := oneChanges(t, sub)
	if len(got.Changes) != 1 || got.Changes[0].Path != "w/c.txt" || got.Changes[0].Kind != "created" {
		t.Fatalf("with w/sub unreadable the tick reported %+v; want only w/c.txt created", got.Changes)
	}
}

// TestWatchEntriesAreTheBaseline pins ADR-0049 §1: the result's entries are the baseline
// walk the first tick diffs against, sorted by path, with the exclusions applied, and
// never a second walk. The file the hook creates after the walk has read the root is not
// in entries and arrives as a created event; a result built from a second walk would
// list it in entries as well, and the client's tree would count it twice.
func TestWatchEntriesAreTheBaseline(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/b.txt", "b")
	f.write("w/a/x.txt", "x")
	f.write("w/z/y.txt", "y")
	f.write("w/.git/config", "no")
	f.write("w/app/build.gradle.kts", "plugins {}")
	f.write("w/app/build/out", "no")
	f.write("w/.notes.droidyard-01234567.tmp", "no")
	f.symlink("b.txt", "w/link")

	// The hook runs once per directory, before it is read, in the walk's order: the
	// second call is the first subdirectory, entered after w itself was read. The file it
	// creates in w is one the baseline has missed. It stays set until the poller is gone,
	// since the poller reads it too.
	var calls atomic.Int32
	watchWalkHook = func() {
		if calls.Add(1) == 2 {
			f.write("w/late.txt", "late")
		}
	}
	defer func() { watchWalkHook = nil }()
	res, err := f.watchCall(f.sess, WatchParams{Path: "w"})
	if err != nil {
		t.Fatal(err)
	}

	want := []WatchedEntry{
		{"w/a", "dir"}, {"w/a/x.txt", "file"}, {"w/app", "dir"}, {"w/app/build.gradle.kts", "file"},
		{"w/b.txt", "file"}, {"w/link", "symlink"}, {"w/z", "dir"}, {"w/z/y.txt", "file"},
	}
	if len(res.Entries) != len(want) {
		t.Fatalf("entries %+v; want %+v", res.Entries, want)
	}
	for i := range want {
		if res.Entries[i] != want[i] {
			t.Fatalf("entries[%d] is %+v; want %+v (all: %+v)", i, res.Entries[i], want[i], res.Entries)
		}
	}
	if res.Watched != len(want) {
		t.Errorf("watched %d; want %d, the length of entries", res.Watched, len(want))
	}

	// The late file is the first event's, not the baseline's.
	sub, ok := f.sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatal("the result named an unknown subscription")
	}
	var created bool
	for deadline := time.Now().Add(5 * time.Second); !created && time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		for _, e := range drainEvents(sub) {
			var p ChangesPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			created = created || kinds(p.Changes)["w/late.txt"] == "created"
		}
	}
	if !created {
		t.Error("the file created after the baseline never arrived as a created event")
	}
	f.sess.Close()
	waitLive(t, f.watchers, 0)
}

// TestWatchEntriesAreSlimmed pins ADR-0020 §Retention for file.watch (ADR-0049 §1): the
// first delivery of the result carries the entries; an operation.wait after it returns
// the same result with none (an empty array, which the schema requires), the count and
// the flags kept. A descriptor without the Slim keeps 20000 entries for the retention
// period, and the second wait would list them again.
func TestWatchEntriesAreSlimmed(t *testing.T) {
	f := newWatchFixture(t)
	f.write("w/a.txt", "a")
	f.write("w/b.txt", "b")
	ops := capability.NewOperations(f.reg)
	ops.UseWorkspaces(admit{})
	ops.RegisterCapabilities()

	params, _ := json.Marshal(WatchParams{Path: "w"})
	id, err := ops.Start(context.Background(), capability.Request{Capability: "file.watch", Workspace: "ws", Params: params, Session: f.sess})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ops.Wait(context.Background(), id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	first := out.Result.(WatchResult)
	if len(first.Entries) != 2 {
		t.Fatalf("the first delivery carried %+v; want the two entries", first.Entries)
	}
	out, err = ops.Wait(context.Background(), id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	second := out.Result.(WatchResult)
	if second.Entries == nil || len(second.Entries) != 0 {
		t.Errorf("a retained watch still carries entries %+v; want an empty, non-nil array", second.Entries)
	}
	if second.Watched != 2 || second.SubscriptionID != first.SubscriptionID {
		t.Errorf("Slim dropped more than the entries: %+v", second)
	}
	f.sess.Close()
	waitLive(t, f.watchers, 0)
}

// buildNestedTree lays out the benchmark's tree under dir: directories branching by four
// down to depth 6 (5460 of them) and files spread over every directory until the walk
// has 20000 entries, the cap. A real project's tree is nested, not one big directory, and
// the cost of a walk is dominated by the directories it opens (ADR-0049 §Consequences).
func buildNestedTree(tb testing.TB, dir string) {
	tb.Helper()
	var dirs []string
	level := []string{dir}
	for depth := 1; depth <= 6; depth++ {
		var next []string
		for _, parent := range level {
			for i := 0; i < 4; i++ {
				d := filepath.Join(parent, "d"+strconv.Itoa(i))
				if err := os.Mkdir(d, 0o755); err != nil {
					tb.Fatal(err)
				}
				next = append(next, d)
			}
		}
		dirs = append(dirs, next...)
		level = next
	}
	files := watchCap - len(dirs)
	all := append([]string{dir}, dirs...)
	for i := 0; i < files; i++ {
		name := filepath.Join(all[i%len(all)], "f"+strconv.Itoa(i)+".kt")
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			tb.Fatal(err)
		}
	}
}

// BenchmarkWatchWalkNested measures one walk of the watch's cap, 20000 entries in a tree
// 6 directories deep, the cost the poller pays every second per watch (ADR-0049; plan
// m2d step 4's gate). "walk" is the baseline; "tick" adds the diff against the last walk
// that a tick does. The entries are checked once, so a benchmark of a smaller tree cannot
// pass for this one.
func BenchmarkWatchWalkNested(b *testing.B) {
	dir := b.TempDir()
	buildNestedTree(b, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer root.Close()
	w := &watcher{root: root}
	base, truncated, cerr := w.walk(root.FS(), ".")
	if cerr != nil {
		b.Fatal(cerr)
	}
	if len(base) != watchCap || truncated {
		b.Fatalf("the tree has %d entries (truncated %v); want exactly %d", len(base), truncated, watchCap)
	}

	b.Run("walk", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, cerr := w.walk(root.FS(), "."); cerr != nil {
				b.Fatal(cerr)
			}
		}
	})
	b.Run("tick", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			next, trunc, cerr := w.walkFrom(root.FS(), ".", base)
			if cerr != nil {
				b.Fatal(cerr)
			}
			if changes, _ := diff(base, next, trunc); len(changes) != 0 {
				b.Fatalf("an unchanged tree reported %d changes", len(changes))
			}
		}
	})
}

// Decision (m2r step 4, row 160): a watch ends with pathGone when the project directory
// itself is deleted, moved away or replaced, though its descriptor still reads the old
// directory; a project left in place never stops it.
func TestWatchStopsWhenTheProjectGoes(t *testing.T) {
	run := func(t *testing.T, watchRel string, gone func(f *fixture)) (string, []FileChange) {
		f := newWatchFixture(t)
		f.write("app/a.txt", "a")
		w, sub, _ := f.startManualWatch(watchRel)
		if gone != nil {
			gone(f)
		}
		var changes []FileChange
		for i := 0; i < 20; i++ {
			w.tickOnce()
			for _, e := range drainEvents(sub) {
				switch e.Type {
				case TypeStopped:
					var p StoppedPayload
					_ = json.Unmarshal(e.Payload, &p)
					return p.Reason, changes
				case TypeChanges:
					var p ChangesPayload
					_ = json.Unmarshal(e.Payload, &p)
					changes = append(changes, p.Changes...)
				}
			}
			select {
			case r := <-w.stop:
				return r, changes
			default:
			}
		}
		return "", changes
	}
	moved := func(f *fixture) {
		if err := os.Rename(f.root, f.root+"-moved"); err != nil {
			f.t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, rel string
		gone      func(f *fixture)
		deleted   bool
	}{
		{"root removed", ".", func(f *fixture) {
			if err := os.RemoveAll(f.root); err != nil {
				f.t.Fatal(err)
			}
		}, true},
		{"root renamed", ".", moved, true},
		{"subdirectory, project renamed", "app", moved, true},
		// A new directory of the same name with the same contents: only the identity check
		// tells it from the one the descriptor holds.
		{"root replaced", ".", func(f *fixture) {
			moved(f)
			if err := os.MkdirAll(filepath.Join(f.root, "app"), 0o755); err != nil {
				f.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.root, "app", "a.txt"), []byte("a"), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			reason, changes := run(t, c.rel, c.gone)
			if reason != ReasonPathGone {
				t.Fatalf("stop reason %q; want %q", reason, ReasonPathGone)
			}
			if c.deleted && len(changes) == 0 {
				t.Fatal("no entry was reported deleted before the stop")
			}
			for _, ch := range changes {
				if ch.Kind != "deleted" {
					t.Fatalf("change %+v; want only deleted", ch)
				}
			}
		})
	}
	t.Run("project in place", func(t *testing.T) {
		if reason, changes := run(t, ".", nil); reason != "" || len(changes) != 0 {
			t.Fatalf("a project left in place gave stop %q and changes %+v; want neither", reason, changes)
		}
	})
}
