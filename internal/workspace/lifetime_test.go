package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	builddomain "github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// fakeClock is the registry's idle clock (Registry.now), moved by the test. The operation
// store keeps the wall clock, which nothing here depends on.
type fakeClock struct{ ns atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *fakeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// lifeHarness is the harness with the idle clock fake and the three things the lifetime
// is judged by counted: roots opened (OnOpen), Gradle roots released (CloseRoot), and
// the ends the registry heard of (Ended), delivered on a channel so a test can wait for
// the stamp an operation's end makes instead of sleeping.
type lifeHarness struct {
	*harness
	clock  *fakeClock
	opened atomic.Int32
	closed atomic.Int32
	ended  chan string
}

func newLifeHarness(t *testing.T) *lifeHarness {
	t.Helper()
	l := &lifeHarness{harness: newHarness(t, 200*time.Millisecond), clock: newFakeClock(), ended: make(chan string, 4096)}
	// An index of a newer version is neither read nor written
	// (TestNewerIndexIsNeitherReadNorWritten), so a reopen writes nothing and costs no
	// fsync: what these tests judge is the ordering of the locks, which a reopen's disk
	// time would only slow.
	if err := os.WriteFile(filepath.Join(l.st.Root(), indexFile), []byte(`{"schemaVersion":2,"workspaces":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l.ws.now = l.clock.now
	l.ws.opts.OnOpen = func(id, root string) { l.opened.Add(1) }
	l.ws.opts.CloseRoot = func(root string) { l.closed.Add(1) }
	hook := func(id string) { l.ended <- id }
	l.ws.endedHook.Store(&hook)
	return l
}

// lifeProject is gradleProject in a folder of its own under /tmp. Canonicalising a root
// lists every directory above it (platform.Env.Canonical), and the system temporary
// directory of a development machine holds thousands of entries: the thousand rounds of
// TestReleaseNeverCancelsAnAdmittedCall would spend their time listing it.
func lifeProject(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "droidyard-life")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	root := platform.Current().Canonical(base)
	if err := os.WriteFile(filepath.Join(root, "settings.gradle"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// sweep is one round of RunIdle.
func (l *lifeHarness) sweep() { l.ws.releaseIdle(context.Background()) }

// gradleRootOf records a Gradle root on the workspace, as a call that resolved one does
// (UseGradleRoot), so that its release has one to close.
func (l *lifeHarness) gradleRootOf(t *testing.T, id string) {
	t.Helper()
	if _, err := l.ws.UseGradleRoot(id, "/gradle/"+id); err != nil {
		t.Fatalf("UseGradleRoot %s: %v", id, err)
	}
}

// endOf waits for the registry to hear of one operation's end.
func (l *lifeHarness) endOf(t *testing.T) string { return recv(t, l.ended, "the end of an operation") }

// TestEnterOpensOnceUnderConcurrency: twenty calls at once, naming one project by three
// spellings (a trailing slash, a symlink, a folder inside it), open it once: one OnOpen,
// one id, twenty admissions. The open takes the workspace's transition lock, which is
// what makes the second caller find the root open instead of opening it again
// (ADR-0137 §3, ADR-0027). Seen failing with Open not taking w.tx.
func TestEnterOpensOnceUnderConcurrency(t *testing.T) {
	l := newLifeHarness(t)
	root := lifeProject(t)
	inside := filepath.Join(root, "app", "src")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(project(t), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	spellings := []string{root + "/", link, inside}
	id, _ := platform.Current().WorkspaceID(root)

	const calls = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	outs := make([]capability.Outcome, calls)
	errs := make([]error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outs[i], errs[i] = l.ops.Invoke(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(spellings[i%len(spellings)])})
		}()
	}
	close(start)
	wg.Wait()

	for i := range outs {
		if errs[i] != nil || outs[i].State != capability.StateCompleted {
			t.Errorf("call %d (%s): %v %+v; want completed", i, spellings[i%len(spellings)], errs[i], outs[i])
		}
	}
	if n := l.opened.Load(); n != 1 {
		t.Errorf("OnOpen ran %d times; want once", n)
	}
	if n := l.ran.Load(); n != calls || l.sawWorkspace() != id {
		t.Errorf("%d admissions, the handler saw workspace %v; want %d on %s", n, l.sawWorkspace(), calls, id)
	}
	l.ws.allMu.RLock()
	entries := len(l.ws.all)
	l.ws.allMu.RUnlock()
	if entries != 1 {
		t.Errorf("%d workspaces registered; want one", entries)
	}
	opening := 0
	for _, e := range l.events.take() {
		if e.topic == LifecycleTopic && e.typ == string(Opening) {
			opening++
		}
	}
	if opening != 1 {
		t.Errorf("%d opening events on workspace.lifecycle; want one", opening)
	}
}

// TestIdleNeverReleasesARunningOperation: an operation of the workspace holds the root
// open past IdleAfter, however long it runs; once it ends the idle clock starts from its
// end, not from its admission, so a long build is not followed by an immediate release.
// Seen failing with ActiveIn dropped from the decision (the first sweep released it), and
// with lastUsed stamped at admission only (the sweep after the end released it).
func TestIdleNeverReleasesARunningOperation(t *testing.T) {
	l := newLifeHarness(t)
	root := lifeProject(t)
	op := l.start(t, "fixture.ignores", root)
	id, _ := platform.Current().WorkspaceID(root)
	l.gradleRootOf(t, id)

	l.clock.advance(31 * time.Minute)
	l.sweep()
	if s := l.ws.State(id); s != Ready || l.closed.Load() != 0 {
		t.Fatalf("31 minutes into a running operation: state %s, %d roots closed; want ready, none", s, l.closed.Load())
	}

	close(l.release)
	l.waitEnd(t, op)
	l.endOf(t)

	l.clock.advance(29 * time.Minute)
	l.sweep()
	if s := l.ws.State(id); s != Ready {
		t.Fatalf("29 minutes after the operation ended: state %s; want ready", s)
	}
	l.clock.advance(time.Minute) // exactly IdleAfter: older than it, not as old
	l.sweep()
	if s := l.ws.State(id); s != Ready {
		t.Fatalf("exactly %s after the end: state %s; want ready", IdleAfter, s)
	}
	l.clock.advance(time.Minute)
	l.sweep()
	if s := l.ws.State(id); s != Closed || l.closed.Load() != 1 {
		t.Fatalf("31 minutes after the end: state %s, %d roots closed; want closed, once", s, l.closed.Load())
	}
	l.sweep()
	if n := l.closed.Load(); n != 1 {
		t.Errorf("a sweep of a closed workspace closed a root again (%d)", n)
	}
}

// TestIdleRootIsReleasedAndReopenedByTheNextCall: a root with nothing running and no use
// for longer than IdleAfter is released by a sweep, and the next call that names it opens
// it again with no other step (ADR-0137 §3).
func TestIdleRootIsReleasedAndReopenedByTheNextCall(t *testing.T) {
	l := newLifeHarness(t)
	root := lifeProject(t)
	l.open(t, root)
	id, _ := platform.Current().WorkspaceID(root)

	l.clock.advance(IdleAfter + time.Minute)
	l.sweep()
	if s := l.ws.State(id); s != Closed {
		t.Fatalf("an idle root: state %s; want closed", s)
	}
	if out := l.invoke(t, "fixture.scoped", root); out.State != capability.StateCompleted || l.ws.State(id) != Ready {
		t.Fatalf("a call after the release: %+v, state %s; want completed on a reopened root", out, l.ws.State(id))
	}
	if n := l.opened.Load(); n != 2 {
		t.Errorf("OnOpen ran %d times; want two (the open and the reopen)", n)
	}
}

// TestReleaseNeverCancelsAnAdmittedCall: Enter and the sweep meet at the edge of IdleAfter,
// a thousand times, at the five places they can meet: the sweep first; the sweep while the
// Enter is about to ask for w.tx; the sweep between Enter's Open and its admission; the
// two free to race; and the Enter arriving while the release, having decided, is held. Every call is admitted and completes, none ends OPERATION_CANCELLED,
// and every release was followed by exactly one reopen. The release decides and enters
// closing while it holds w.tx, which every Enter takes through Open, so an Enter either
// stamps the clock before the decision (no release) or waits for closed and reopens; and
// the stamp Open makes on an open root is what keeps a sweep that lands after the Open from
// releasing a root the call has just asked for: that round must release nothing.
// Seen failing with the release taking w.tx after its decision, and with Open's stamp
// removed.
func TestReleaseNeverCancelsAnAdmittedCall(t *testing.T) {
	l := newLifeHarness(t)
	root := lifeProject(t)
	id, _ := platform.Current().WorkspaceID(root)
	l.open(t, root)
	l.gradleRootOf(t, id)

	call := func() (capability.Outcome, error) {
		return l.ops.Invoke(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(root)})
	}
	// sweepIn runs one sweep, once, from inside hook.
	sweepIn := func(hook *atomic.Pointer[func(string)]) {
		var once sync.Once
		f := func(string) { once.Do(l.sweep) }
		hook.Store(&f)
	}

	const rounds = 1000
	for i := 0; i < rounds; i++ {
		l.clock.advance(IdleAfter + time.Minute)
		var out capability.Outcome
		var err error
		closedBefore := l.closed.Load()
		variant := i % 5
		switch variant {
		case 0: // the sweep first: the call reopens what it released
			l.sweep()
			out, err = call()
		case 1: // the sweep while the call is about to ask for w.tx
			sweepIn(&l.ws.openHook)
			out, err = call()
			l.ws.openHook.Store(nil)
		case 2: // the sweep between the call's Open and its admission
			sweepIn(&l.ws.admitHook)
			out, err = call()
			l.ws.admitHook.Store(nil)
			if n := l.closed.Load(); n != closedBefore {
				t.Fatalf("round %d: a sweep after the call's Open released the root (%d roots closed); Open's stamp must keep it", i, n-closedBefore)
			}
		case 3: // free to race
			var wg sync.WaitGroup
			start := make(chan struct{})
			wg.Add(2)
			go func() { defer wg.Done(); <-start; out, err = call() }()
			go func() { defer wg.Done(); <-start; l.sweep() }()
			close(start)
			wg.Wait()
		case 4: // the call arrives while the release, having decided, is held
			decided, resume, reached := make(chan struct{}), make(chan struct{}), make(chan struct{})
			hold := func(string) { close(decided); <-resume }
			arrive := func(string) { close(reached) }
			l.ws.releaseHook.Store(&hold)
			l.ws.openHook.Store(&arrive)
			swept := make(chan struct{})
			go func() { defer close(swept); l.sweep() }()
			recv(t, decided, "the release at its hook")
			done := make(chan struct{})
			go func() { defer close(done); out, err = call() }()
			recv(t, reached, "the call's Open")
			select {
			case <-done:
				t.Fatalf("round %d: the call completed while the release was held after its decision; it must wait for w.tx", i)
			case <-time.After(5 * time.Millisecond):
			}
			close(resume)
			recv(t, swept, "the release")
			recv(t, done, "the call")
			l.ws.releaseHook.Store(nil)
			l.ws.openHook.Store(nil)
		}
		l.endOf(t)

		if err != nil || out.State != capability.StateCompleted {
			t.Fatalf("round %d (variant %d): the call was %v %+v; want completed (a release cancelled or refused it)", i, variant, err, out)
		}
		if s := l.ws.State(id); s != Ready {
			t.Fatalf("round %d (variant %d): state %s after the call; want ready", i, variant, s)
		}
		l.gradleRootOf(t, id)
	}
	opens, closes := int(l.opened.Load()), int(l.closed.Load())
	if releases := opens - 1; closes != releases || releases < rounds/2 {
		t.Errorf("%d opens and %d roots closed over %d rounds; want one close per reopen, and the sweep to have released in at least half the rounds", opens, closes, rounds)
	}
}

// TestEnterDuringRelease: the release holds w.tx from its decision to closed. A hook holds
// it with the workspace in closing; a call that arrives then waits on w.tx, and is admitted
// on the reopened root, after closed was published (ADR-0027: an open during a close waits
// for it). Seen failing with the release taking w.tx after its decision: the call
// reopened the root and completed while the release was still held.
func TestEnterDuringRelease(t *testing.T) {
	l := newLifeHarness(t)
	root := lifeProject(t)
	id, _ := platform.Current().WorkspaceID(root)
	l.open(t, root)
	l.gradleRootOf(t, id)
	l.events.take()
	ranBefore := l.ran.Load() // the call that opened the root
	l.clock.advance(IdleAfter + time.Minute)

	held, resume := make(chan string), make(chan struct{})
	hook := func(id string) { held <- id; <-resume }
	l.ws.releaseHook.Store(&hook)
	swept := make(chan struct{})
	go func() { defer close(swept); l.sweep() }()
	recv(t, held, "the release at its hook")
	if s := l.ws.State(id); s != Closing {
		t.Fatalf("at the hook the state is %s; want closing", s)
	}

	reached := make(chan string, 1)
	enter := func(id string) { reached <- id }
	l.ws.openHook.Store(&enter)
	done := make(chan capability.Outcome, 1)
	go func() {
		out, _ := l.ops.Invoke(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(root)})
		done <- out
	}()
	recv(t, reached, "the call's Open")
	select {
	case out := <-done:
		t.Fatalf("the call completed while the release was held in closing: %+v", out)
	case <-time.After(100 * time.Millisecond):
	}
	if n := l.ran.Load() - ranBefore; n != 0 {
		t.Fatalf("the handler ran %d times during the release; want none", n)
	}

	close(resume)
	<-swept
	out := recv(t, done, "the call")
	if out.State != capability.StateCompleted || l.ran.Load()-ranBefore != 1 || l.ws.State(id) != Ready {
		t.Fatalf("after the release: %+v, ran %d, state %s; want one admission on a reopened root", out, l.ran.Load()-ranBefore, l.ws.State(id))
	}
	var states []string
	for _, e := range l.events.take() {
		if e.topic == LifecycleTopic {
			states = append(states, e.typ)
		}
	}
	if got := strings.Join(states, ","); got != "closing,closed,opening,ready" {
		t.Errorf("lifecycle %s; want closing,closed,opening,ready: the call opened before the release finished", got)
	}
	if n := l.closed.Load(); n != 1 {
		t.Errorf("%d roots closed; want the release's one", n)
	}
}

// TestRootRemovedWhileOpen: the project's folder is deleted while its workspace is open. A
// call naming it is refused at Start and opens and records nothing; the sweep releases the
// old entry like any idle one (ADR-0137 §3).
func TestRootRemovedWhileOpen(t *testing.T) {
	l := newLifeHarness(t)
	root := lifeProject(t)
	id, _ := platform.Current().WorkspaceID(root)
	if out := l.invoke(t, "fixture.scoped", root); out.State != capability.StateCompleted {
		t.Fatalf("first call: %+v", out)
	}
	l.endOf(t)
	l.gradleRootOf(t, id)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	opID, err := l.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(root)})
	if opID != "" || codeOf(err) != capability.ErrInvalidParams {
		t.Errorf("a call naming the removed project: %q, %v; want REQUEST_INVALID_PARAMS", opID, err)
	}
	if l.ran.Load() != 1 || l.ops.Running() != 0 || l.opened.Load() != 1 {
		t.Errorf("handler ran %d times, %d operations running, %d opens; want the first call's only", l.ran.Load(), l.ops.Running(), l.opened.Load())
	}

	l.sweep()
	if s := l.ws.State(id); s != Ready {
		t.Fatalf("a sweep right after the removal: state %s; want ready (it was used a moment ago)", s)
	}
	l.clock.advance(IdleAfter + time.Minute)
	l.sweep()
	if s := l.ws.State(id); s != Closed || l.closed.Load() != 1 {
		t.Errorf("the idle sweep: state %s, %d roots closed; want the old entry released", s, l.closed.Load())
	}
}

// TestFolderGoneBetweenResolveAndOpen: the project's folder disappears after ResolveProject
// and before the open. The refusal is REQUEST_INVALID_PARAMS, as for a folder that was
// never there; no operation is recorded and nothing is registered.
func TestFolderGoneBetweenResolveAndOpen(t *testing.T) {
	l := newLifeHarness(t)
	root := lifeProject(t)
	gone := func(r string) { os.RemoveAll(r) }
	l.ws.resolvedHook.Store(&gone)

	opID, err := l.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(root)})
	if opID != "" || codeOf(err) != capability.ErrInvalidParams {
		t.Fatalf("a call whose folder went before the open: %q, %v; want REQUEST_INVALID_PARAMS", opID, err)
	}
	l.ws.allMu.RLock()
	entries := len(l.ws.all)
	l.ws.allMu.RUnlock()
	if l.ran.Load() != 0 || l.ops.Running() != 0 || entries != 0 || l.opened.Load() != 0 {
		t.Errorf("handler ran %d times, %d operations, %d registered, %d opens; want none", l.ran.Load(), l.ops.Running(), entries, l.opened.Load())
	}
}

// TestLastResultSurvivesRelease: a release stops what the root holds in memory and nothing
// under ws/<wsid>/, where the build history is read from at every call. build.lastResult,
// the shipped capability over the shipped build registry, answers the same build before the
// release and after the call that reopens the root. The record is written where a finished
// build writes it (ws/<wsid>/builds/); running a build needs a Gradle sidecar, which
// cmd/droidyard's tests have. Seen failing with a release that removed ws/<wsid>/builds
// (BUILD_NOT_FOUND).
func TestLastResultSurvivesRelease(t *testing.T) {
	l := newLifeHarness(t)
	arts, err := artifact.New(artifact.Options{Epoch: event.New(event.Options{}).Position().Epoch, StateRoot: l.st.Root()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { arts.Close() })
	builddomain.Register(l.reg, builddomain.Options{
		History: builddomain.NewRegistry(builddomain.RegistryOptions{Store: l.st, Artifacts: arts}),
	})
	root := lifeProject(t)
	id, _ := platform.Current().WorkspaceID(root)
	l.open(t, root)

	const buildID = "op-0123456789abcdef"
	finished := time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC)
	if err := l.st.Write("ws/"+id+"/builds/"+buildID+".json", map[string]any{
		"buildId": buildID, "root": root, "outcome": "success", "finishedAt": finished}); err != nil {
		t.Fatal(err)
	}
	if err := l.st.Write("ws/"+id+"/builds/index.json", map[string]any{
		"builds": []map[string]any{{"buildId": buildID, "outcome": "success", "finishedAt": finished}}}); err != nil {
		t.Fatal(err)
	}
	last := func(when string) string {
		t.Helper()
		out, err := l.ops.Invoke(context.Background(), capability.Request{Capability: "build.lastResult", Params: projectParams(root)})
		if err != nil || out.State != capability.StateCompleted {
			t.Fatalf("build.lastResult %s: %v %+v %+v", when, err, out, out.Error)
		}
		return out.Result.(builddomain.Result).BuildID
	}
	if got := last("before the release"); got != buildID {
		t.Fatalf("build.lastResult before the release = %s; want %s", got, buildID)
	}

	l.clock.advance(IdleAfter + time.Minute)
	l.sweep()
	if s := l.ws.State(id); s != Closed {
		t.Fatalf("state %s after the sweep; want closed", s)
	}
	if got := last("after the release"); got != buildID || l.ws.State(id) != Ready || l.opened.Load() != 2 {
		t.Errorf("build.lastResult after the release = %s, state %s, %d opens; want %s on a root the call reopened", got, l.ws.State(id), l.opened.Load(), buildID)
	}
}

// TestRunIdleSweepsUntilItsContextEnds: RunIdle looks at every interval, releases what is
// idle, and returns when its context ends.
func TestRunIdleSweepsUntilItsContextEnds(t *testing.T) {
	l := newLifeHarness(t)
	l.ws.idleEvery = time.Millisecond
	root := lifeProject(t)
	info := l.open(t, root)
	l.clock.advance(IdleAfter + time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); l.ws.RunIdle(ctx) }()
	for until := time.Now().Add(5 * time.Second); l.ws.State(info.ID) != Closed; time.Sleep(time.Millisecond) {
		if time.Now().After(until) {
			cancel()
			t.Fatalf("state %s after 5 s of sweeps; want closed", l.ws.State(info.ID))
		}
	}
	cancel()
	recv(t, stopped, "RunIdle after its context ended")
}
