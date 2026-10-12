package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// watchFx is a registered git.watch over a real repository: the daemon's bus, a poller
// set and a session, and a Starter that records every git it starts, so a test can say
// how many statuses ran and that no git is left.
type watchFx struct {
	*env
	root string
	bus  *event.Bus
	ws   *Watchers
	sess *event.Session
	reg  *capability.Registry

	trusted atomic.Bool // the root's trust, read at every tick
	gone    atomic.Bool // git is no longer found

	statuses atomic.Int32 // gits started whose argument is `status`
	mu       sync.Mutex
	pids     []int
	// before, when set, runs in the Starter before each git starts.
	before func(cmd *exec.Cmd)
}

// newWatchFx builds a repository with the watch registered at the given tick; the root
// is trusted until a test says otherwise.
func newWatchFx(t *testing.T, tick time.Duration) *watchFx {
	t.Helper()
	e := needGit(t)
	f := &watchFx{env: e}
	f.root = platform.Current().Canonical(e.repo())
	f.trusted.Store(true)
	f.bus = event.New(event.Options{})
	f.ws = NewWatchers()
	f.sess = event.NewSession(f.bus, "ses-git")
	f.reg = capability.NewRegistry()
	Register(f.reg, Options{
		Root: func(string) (string, bool) { return f.root, f.root != "" },
		Env:  platform.Current(),
		Trusted: func(string) (bool, *capability.Error) {
			return f.trusted.Load(), nil
		},
		Starter: func(cmd *exec.Cmd) error {
			if f.before != nil {
				f.before(cmd)
			}
			for _, a := range cmd.Args {
				if a == "status" {
					f.statuses.Add(1)
					break
				}
			}
			if err := groupStarter(cmd); err != nil {
				return err
			}
			f.mu.Lock()
			f.pids = append(f.pids, cmd.Process.Pid)
			f.mu.Unlock()
			return nil
		},
		LookPath: func(name string) (string, error) {
			if f.gone.Load() {
				return "", errors.New("gone")
			}
			return exec.LookPath(name)
		},
		Bus: f.bus, Watchers: f.ws, Tick: tick,
	})
	t.Cleanup(func() {
		f.sess.Close()
		f.ws.Stop("ws")
	})
	return f
}

func (f *watchFx) watchCtx(ctx context.Context, p WatchParams) (WatchResult, error) {
	f.t.Helper()
	d, ok := f.reg.Lookup("git.watch")
	if !ok {
		f.t.Fatal("git.watch is not registered")
	}
	res, err := d.Handler(ctx, &capability.Call{Workspace: "ws", OperationID: "op-git", Params: p, Session: f.sess})
	if err != nil {
		return WatchResult{}, err
	}
	return res.(WatchResult), nil
}

// start runs git.watch and fails the test on an error.
func (f *watchFx) start() (WatchResult, *event.Subscription) {
	f.t.Helper()
	res, err := f.watchCtx(context.Background(), WatchParams{})
	if err != nil {
		f.t.Fatalf("git.watch: %v", err)
	}
	sub, ok := f.sess.Subscription(res.SubscriptionID)
	if !ok {
		f.t.Fatal("the result's subscription is not the session's")
	}
	return res, sub
}

// next waits for the subscription's next event.
func next(t *testing.T, sub *event.Subscription) event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("no event within 10 s: %v", err)
	}
	return e
}

func statusEvent(t *testing.T, e event.Event) Status {
	t.Helper()
	if e.Topic != TopicGitChanges || e.Type != TypeStatus {
		t.Fatalf("the event is %s/%s; want git.changes/status", e.Topic, e.Type)
	}
	var st Status
	if err := json.Unmarshal(e.Payload, &st); err != nil {
		t.Fatalf("the payload: %v", err)
	}
	return st
}

func stoppedEvent(t *testing.T, e event.Event) StoppedPayload {
	t.Helper()
	if e.Topic != TopicGitChanges || e.Type != TypeStopped {
		t.Fatalf("the event is %s/%s; want git.changes/stopped", e.Topic, e.Type)
	}
	var sp StoppedPayload
	if err := json.Unmarshal(e.Payload, &sp); err != nil {
		t.Fatalf("the payload: %v", err)
	}
	return sp
}

// waitStatuses waits until n statuses have been started in all.
func (f *watchFx) waitStatuses(n int32) {
	f.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if f.statuses.Load() >= n {
			return
		}
	}
	f.t.Fatalf("only %d statuses were started; want %d", f.statuses.Load(), n)
}

func waitLive(t testing.TB, ws *Watchers, want int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if ws.Live() == want {
			return
		}
	}
	t.Fatalf("the live poller count is %d; want %d", ws.Live(), want)
}

// liveGits is how many gits the Starter started are still running.
func (f *watchFx) liveGits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, pid := range f.pids {
		if syscall.Kill(pid, 0) == nil {
			n++
		}
	}
	return n
}

// noGitLeft waits for every git the Starter started to be gone.
func (f *watchFx) noGitLeft() {
	f.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if f.liveGits() == 0 {
			return
		}
	}
	f.t.Errorf("%d git processes outlived the watch", f.liveGits())
}

// nothingLeft is what a watch that did not return an id leaves: no poller, no
// subscription, no git.
func (f *watchFx) nothingLeft() {
	f.t.Helper()
	waitLive(f.t, f.ws, 0)
	if f.sess.Len() != 0 || f.bus.Subscriptions() != 0 {
		f.t.Errorf("%d session subscriptions and %d on the bus remain", f.sess.Len(), f.bus.Subscriptions())
	}
	f.noGitLeft()
}

func (f *watchFx) hasEntry(st Status, path string, mark Mark) bool {
	for _, e := range st.Entries {
		if e.Path == path && e.Mark == mark {
			return true
		}
	}
	return false
}

// Decision (ADR-0066 §1): the result is the baseline, git's own status, the map the
// first event is compared with: so a client builds its view from one call and the events.
func TestWatchBaselineIsStatus(t *testing.T) {
	f := newWatchFx(t, 20*time.Millisecond)
	f.write(f.root, "a.txt", "one\n")
	f.write(f.root, "b.txt", "two\n")
	f.commit(f.root)
	f.write(f.root, "a.txt", "changed\n")
	f.write(f.root, "u.txt", "new\n")

	res, _ := f.start()
	want, err := f.statusOf(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Base != "HEAD" || res.Head.Branch != "main" || res.Head.Oid == "" || res.Head != want.Head {
		t.Errorf("base %q head %+v; want HEAD, main and the commit id %+v", res.Base, res.Head, want.Head)
	}
	if len(res.Entries) != 2 || !equalEntries(res.Entries, want.Entries) || res.Truncated {
		t.Errorf("entries %+v, truncated %v; want git's own %+v", res.Entries, res.Truncated, want.Entries)
	}
	if !f.hasEntry(Status{Entries: res.Entries}, "a.txt", MarkModified) || !f.hasEntry(Status{Entries: res.Entries}, "u.txt", MarkUntracked) {
		t.Errorf("entries %+v; want a.txt modified and u.txt untracked", res.Entries)
	}
}

func equalEntries(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Decision (ADR-0066 §1): an agent's commit reaches the watch without `.git/` being
// watched: an edit is `modified`; a commit leaves no entry and a new head.oid; each is
// seen within three ticks.
func TestWatchPublishesACommit(t *testing.T) {
	f := newWatchFx(t, 20*time.Millisecond)
	f.write(f.root, "a.txt", "one\n")
	f.commit(f.root)
	res, sub := f.start()
	oid0 := res.Head.Oid

	n0 := f.statuses.Load()
	f.write(f.root, "a.txt", "an edit\n")
	st := statusEvent(t, next(t, sub))
	if len(st.Entries) != 1 || !f.hasEntry(st, "a.txt", MarkModified) {
		t.Fatalf("the edit published %+v; want a.txt modified", st.Entries)
	}
	if d := f.statuses.Load() - n0; d > 3 {
		t.Errorf("the edit took %d ticks to be published; want at most three", d)
	}

	f.g(f.root, "commit", "-q", "-am", "second")
	n1 := f.statuses.Load() // counted from the commit's end, not its start
	st = statusEvent(t, next(t, sub))
	if len(st.Entries) != 0 || st.Head.Oid == "" || st.Head.Oid == oid0 {
		t.Fatalf("the commit published head %+v with %+v; want a new oid (was %s) and no entries", st.Head, st.Entries, oid0)
	}
	if d := f.statuses.Load() - n1; d > 3 {
		t.Errorf("the commit took %d ticks to be published; want at most three", d)
	}
}

// Decision (ADR-0066 §1): the status is git's, so the index is covered, which a watch of
// the files would miss: a `git add` of an untracked file turns it from untracked to added
// with no file changing.
func TestWatchPublishesAnIndexChange(t *testing.T) {
	f := newWatchFx(t, 20*time.Millisecond)
	f.write(f.root, "a.txt", "one\n")
	f.commit(f.root)
	_, sub := f.start()

	f.write(f.root, "n.txt", "new\n")
	if st := statusEvent(t, next(t, sub)); !f.hasEntry(st, "n.txt", MarkUntracked) {
		t.Fatalf("the new file published %+v; want n.txt untracked", st.Entries)
	}
	f.g(f.root, "add", "n.txt")
	if st := statusEvent(t, next(t, sub)); !f.hasEntry(st, "n.txt", MarkAdded) || len(st.Entries) != 1 {
		t.Fatalf("the add published %+v; want n.txt added", st.Entries)
	}
}

// Decision (ADR-0066 §1): an event only when the status differs from the last one sent.
// Ten ticks pass with nothing changed, and a stat-dirty file (the daemon never refreshes
// the index, so git hashes it again each tick, finds the same content) publishes
// nothing either.
func TestWatchQuietWhenUnchanged(t *testing.T) {
	f := newWatchFx(t, 20*time.Millisecond)
	f.write(f.root, "a.txt", "one\n")
	f.write(f.root, "b.txt", "two\n")
	f.commit(f.root)
	f.write(f.root, "b.txt", "TWO\n")
	f.touch(f.root, "a.txt")
	_, sub := f.start()

	n := f.statuses.Load()
	f.waitStatuses(n + 10)
	if e, ok := sub.TryNext(); ok {
		t.Errorf("ten unchanged ticks published %s/%s", e.Topic, e.Type)
	}
}

// Decision (files.md §A watch's lifetime): a workspace close ends the poller, which
// publishes stopped {workspaceClosed} as its last act; the subscription stays; and no git
// is left running.
func TestWatchStopsOnWorkspaceClose(t *testing.T) {
	f := newWatchFx(t, 20*time.Millisecond)
	f.write(f.root, "a.txt", "one\n")
	f.commit(f.root)
	_, sub := f.start()
	f.waitStatuses(f.statuses.Load() + 2)

	f.ws.Stop("ws")
	waitLive(t, f.ws, 0)
	if sp := stoppedEvent(t, next(t, sub)); sp.Reason != ReasonWorkspaceClosed {
		t.Errorf("stopped reason %q; want %q", sp.Reason, ReasonWorkspaceClosed)
	}
	f.noGitLeft()
	time.Sleep(100 * time.Millisecond)
	if e, ok := sub.TryNext(); ok {
		t.Errorf("an event followed stopped: %s/%s", e.Topic, e.Type)
	}
	if f.sess.Len() != 1 {
		t.Errorf("the subscription ended with the workspace close (%d left); it stays until the session ends", f.sess.Len())
	}
}

// Decision (files.md §A watch's lifetime): a subscription's end stops the poller
// silently, and a stop never waits for git: a status stuck in a repository filter is
// killed with its group, and the poller ends at once.
func TestWatchStopKillsAStatusInFlight(t *testing.T) {
	f := newWatchFx(t, 20*time.Millisecond)
	pidFile := filepath.Join(t.TempDir(), "pid")
	slow := f.script("slow.sh", "echo $$ > '"+pidFile+"'\nexec sleep 60")
	f.write(f.root, ".gitattributes", "c.txt filter=slow\n")
	f.write(f.root, "c.txt", "one\n")
	f.commit(f.root)
	res, _ := f.start()

	// From now on a status runs the slow filter (same size, so git must run it).
	f.appendConfig(f.root, "[filter \"slow\"]\n\tclean = "+slow+"\n")
	f.write(f.root, "c.txt", "two\n")
	var pid int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && pid == 0; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
	if pid == 0 {
		t.Fatal("the slow filter never started")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)

	f.sess.Unsubscribe(res.SubscriptionID)
	waitLive(t, f.ws, 0) // would take the filter's 60 s if the stop waited for git
	f.noGitLeft()
	for i := 0; i < 250 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Errorf("the filter process %d outlived the stopped watch", pid)
	}
}

// Decision (files.md §A watch's lifetime; ADR-0049's pattern): a cancel that lands while
// the baseline runs, or just after it, leaves nothing behind: no poller, no
// subscription, no git.
func TestWatchCancelledDuringBaseline(t *testing.T) {
	t.Run("while git runs", func(t *testing.T) {
		f := newWatchFx(t, 20*time.Millisecond)
		f.write(f.root, "a.txt", "one\n")
		f.commit(f.root)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		f.before = func(*exec.Cmd) {
			once.Do(func() { close(entered); <-release })
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := f.watchCtx(ctx, WatchParams{})
			done <- err
		}()
		<-entered
		cancel()
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled watch returned %v; want context.Canceled", err)
		}
		f.nothingLeft()
	})
	t.Run("after the poller started", func(t *testing.T) {
		f := newWatchFx(t, 5*time.Millisecond)
		f.write(f.root, "a.txt", "one\n")
		f.commit(f.root)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		watchBaselineHook = cancel
		defer func() { watchBaselineHook = nil }()
		_, err := f.watchCtx(ctx, WatchParams{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a watch cancelled at its last act returned %v; want context.Canceled", err)
		}
		f.nothingLeft()
	})
}

// Decision (ADR-0066 §1): a tick that fails stops the watch with stopped {failed}: it
// never reports "nothing changed" because it cannot see. Git going away, and (step 1,
// ADR-0066 §3) a root no longer trusted whose configuration gained a filter driver that
// cannot be switched off, are two such failures.
func TestWatchFailedTickStops(t *testing.T) {
	t.Run("git is gone", func(t *testing.T) {
		f := newWatchFx(t, 20*time.Millisecond)
		f.write(f.root, "a.txt", "one\n")
		f.commit(f.root)
		_, sub := f.start()
		f.gone.Store(true)
		if sp := stoppedEvent(t, next(t, sub)); sp.Reason != ReasonFailed {
			t.Errorf("stopped reason %q; want %q", sp.Reason, ReasonFailed)
		}
		waitLive(t, f.ws, 0)
		f.noGitLeft()
	})
	t.Run("an unsafe driver on a root no longer trusted", func(t *testing.T) {
		f := newWatchFx(t, 20*time.Millisecond)
		f.write(f.root, "a.txt", "one\n")
		f.commit(f.root)
		_, sub := f.start()
		f.appendConfig(f.root, "[filter \"\"]\n\tclean = cat\n")
		// Still trusted: nothing is overridden, so nothing is refused.
		f.waitStatuses(f.statuses.Load() + 2)
		if e, ok := sub.TryNext(); ok {
			t.Fatalf("a trusted root published %s/%s", e.Topic, e.Type)
		}
		f.trusted.Store(false)
		if sp := stoppedEvent(t, next(t, sub)); sp.Reason != ReasonFailed {
			t.Errorf("stopped reason %q; want %q", sp.Reason, ReasonFailed)
		}
		waitLive(t, f.ws, 0)
		f.noGitLeft()
	})
}

// Decision (ADR-0015 §2): trust is read again on every tick. A watch started on a trusted
// root with a repository filter that marks each run; trust revoked mid-watch; after the
// next tick the filter is not run again. The control: while trusted, the filter runs
// at every tick.
func TestWatchRevokedTrustRunsNoFilter(t *testing.T) {
	f := newWatchFx(t, 20*time.Millisecond)
	mark := filepath.Join(t.TempDir(), "mark")
	clean := f.script("clean.sh", "echo x >> '"+mark+"'\ncat")
	f.appendConfig(f.root, "[filter \"dy\"]\n\tclean = "+clean+"\n")
	f.write(f.root, ".gitattributes", "c.txt filter=dy\n")
	f.write(f.root, "c.txt", "one\n")
	f.commit(f.root)
	f.write(f.root, "c.txt", "two\n") // same size, stat-dirty: git runs the filter on every status
	runs := func() int {
		b, _ := os.ReadFile(mark)
		return strings.Count(string(b), "x")
	}

	_, sub := f.start()
	f.waitStatuses(f.statuses.Load() + 3)
	if runs() < 3 {
		t.Fatalf("while trusted the filter ran %d times; the control cannot fail", runs())
	}

	f.trusted.Store(false)
	// The second status started after the flip belongs to a tick that read the trust after
	// it; the first may belong to a tick already under way.
	f.waitStatuses(f.statuses.Load() + 2)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if f.liveGits() == 0 {
			break
		}
	}
	after := runs()
	f.waitStatuses(f.statuses.Load() + 5)
	if got := runs(); got != after {
		t.Errorf("the filter ran %d more times after trust was revoked", got-after)
	}
	if e, ok := sub.TryNext(); ok {
		t.Errorf("an event %s/%s: the status did not change", e.Topic, e.Type)
	}
}

// Decision (ADR-0066 §1, §3): the errors of the baseline are the contract's codes, and
// a watch that fails to start leaves no subscription, no poller and no git.
func TestWatchBaselineErrors(t *testing.T) {
	t.Run("base other than HEAD", func(t *testing.T) {
		f := newWatchFx(t, 20*time.Millisecond)
		_, err := f.watchCtx(context.Background(), WatchParams{Base: "main"})
		wantCode(t, capability.AsError(err), capability.ErrInvalidParams)
		f.nothingLeft()
	})
	t.Run("not a repository", func(t *testing.T) {
		f := newWatchFx(t, 20*time.Millisecond)
		f.root = platform.Current().Canonical(t.TempDir())
		_, err := f.watchCtx(context.Background(), WatchParams{})
		wantCode(t, capability.AsError(err), CodeNotARepository)
		f.nothingLeft()
	})
	t.Run("git not found", func(t *testing.T) {
		f := newWatchFx(t, 20*time.Millisecond)
		f.gone.Store(true)
		_, err := f.watchCtx(context.Background(), WatchParams{})
		wantCode(t, capability.AsError(err), CodeNotFound)
		f.nothingLeft()
	})
	t.Run("workspace not open", func(t *testing.T) {
		f := newWatchFx(t, 20*time.Millisecond)
		f.root = ""
		_, err := f.watchCtx(context.Background(), WatchParams{})
		wantCode(t, capability.AsError(err), capability.ErrWorkspaceNotOpen)
		f.nothingLeft()
	})
	t.Run("a driver that cannot be switched off on an untrusted root", func(t *testing.T) {
		f := newWatchFx(t, 20*time.Millisecond)
		f.write(f.root, "a.txt", "one\n")
		f.commit(f.root)
		f.appendConfig(f.root, "[filter \"\"]\n\tclean = cat\n")
		f.trusted.Store(false)
		_, err := f.watchCtx(context.Background(), WatchParams{})
		wantCode(t, capability.AsError(err), capability.ErrWorkspaceUntrusted)
		f.nothingLeft()
	})
	t.Run("no session", func(t *testing.T) {
		f := newWatchFx(t, 20*time.Millisecond)
		d, _ := f.reg.Lookup("git.watch")
		_, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: WatchParams{}})
		wantCode(t, capability.AsError(err), capability.ErrSessionRequired)
		f.nothingLeft()
	})
	t.Run("no bus", func(t *testing.T) {
		// The golden registry's descriptors carry no bus: a call with a session still
		// hears SESSION_REQUIRED, not a nil dereference.
		reg := capability.NewRegistry()
		Register(reg, Options{})
		d, _ := reg.Lookup("git.watch")
		sess := event.NewSession(event.New(event.Options{}), "ses")
		_, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: WatchParams{}, Session: sess})
		wantCode(t, capability.AsError(err), capability.ErrSessionRequired)
	})
}

// Decision (ADR-0020 §Retention): a retained result drops the entries once delivered and
// keeps the rest, so operation.wait still answers with the head.
func TestWatchResultIsSlimmedAfterDelivery(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, Options{})
	d, _ := reg.Lookup("git.watch")
	full := WatchResult{
		SubscriptionID: "sub-1", Base: "HEAD", Head: Head{Oid: "abc", Branch: "main"},
		Entries: []Entry{{Path: "a.txt", Mark: MarkModified}}, Truncated: true,
	}
	got, ok := d.Slim(full).(WatchResult)
	if !ok {
		t.Fatalf("Slim returned %T", d.Slim(full))
	}
	if got.Entries == nil || len(got.Entries) != 0 {
		t.Errorf("slimmed entries %#v; want an empty, non-nil list (the schema requires it)", got.Entries)
	}
	if got.SubscriptionID != "sub-1" || got.Head != full.Head || !got.Truncated || got.Base != "HEAD" {
		t.Errorf("Slim changed more than the entries: %+v", got)
	}
}

// Decision (ADR-0066 §2): the daemon's reads never take .git/index.lock, so a commit an
// agent makes beside a fast watch never fails. 200 add-and-commit cycles beside a 5 ms
// watch: every one exits 0 and none mentions index.lock. The count with the lock
// switches removed (an -overlay of run.go) is recorded in the plan step's report, not
// asserted here.
func TestCommitsNeverFailBesideTheWatch(t *testing.T) {
	if testing.Short() {
		t.Skip("200 commits beside a watch")
	}
	f := newWatchFx(t, 5*time.Millisecond)
	f.write(f.root, "seed.txt", "seed\n")
	f.commit(f.root)
	f.start()

	failed, locked := 0, 0
	for i := 0; i < 200; i++ {
		f.write(f.root, fmt.Sprintf("f%03d.txt", i), fmt.Sprintf("content %d\n", i))
		for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", fmt.Sprintf("c%d", i)}} {
			out, err := f.gErr(f.root, args...)
			if err != nil {
				failed++
				if strings.Contains(out, "index.lock") {
					locked++
				}
				t.Errorf("cycle %d: git %v: %v\n%s", i, args, err, out)
			} else if strings.Contains(out, "index.lock") {
				locked++
			}
		}
	}
	t.Logf("200 cycles beside the watch: %d failed commands, %d mentioned index.lock, %d statuses ran", failed, locked, f.statuses.Load())
	if locked != 0 {
		t.Errorf("%d commands mentioned index.lock", locked)
	}
	if f.statuses.Load() < 20 {
		t.Errorf("only %d statuses ran beside the commits; the test proves nothing", f.statuses.Load())
	}
}
