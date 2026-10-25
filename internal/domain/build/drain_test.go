package build

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// A cancel that outlives cancelWait (m3e step 4; gradle.md §Lifecycle): the operation
// ends cancelled, and the build is drained, not forgotten. The fake's IgnoreCancelUntil is
// a build that ignores build.cancel and finishes when a file appears.

// shortCancelWait shortens cancelWait for the test, so the drain starts in a fraction of
// a second.
func shortCancelWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := cancelWait
	cancelWait = d
	t.Cleanup(func() { cancelWait = old })
}

func readFile(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

// waitFor polls until cond holds, or fails the test after limit.
func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (f *buildFixture) waitLog(t *testing.T, limit time.Duration, sub string) {
	t.Helper()
	waitFor(t, limit, "the daemon log to say "+sub, func() bool { return strings.Contains(f.log.String(), sub) })
}

// ignoringBuild is the fake's script for a build that ignores its cancel until gate
// exists, with one progress step first and a late progress and output after it.
func ignoringBuild(gate string) *gradletest.Build {
	return &gradletest.Build{
		IgnoreCancelUntil: gate,
		Steps: []gradletest.Step{
			{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:busy"}},
			{Kind: "progress", Counts: &gradle.TaskSummary{}, ActiveTask: ":app:busy"},
		},
		LateSteps: []gradletest.Step{
			{Kind: "output", Stream: "stdout", Lines: []string{"late console line"}},
			{Kind: "progress", Counts: &gradle.TaskSummary{Executed: 7}, ActiveTask: ":app:late"},
		},
	}
}

// fixtureWithBuildScript is a fixture over a project root that holds a build.gradle.kts,
// so editing it changes the model's fingerprint.
func fixtureWithBuildScript(t *testing.T, cfg gradletest.Config) (*buildFixture, string) {
	t.Helper()
	root := t.TempDir()
	script := filepath.Join(root, "build.gradle.kts")
	if err := os.WriteFile(script, []byte("// v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return newFixtureTop(t, t.TempDir(), cfg, policy.BelowFloor{}, root, ""), script
}

func createGate(t *testing.T, gate string) {
	t.Helper()
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Decision (m3e step 4, A5): a build that ignores its cancel past cancelWait keeps the
// root's Tooling API lock until its terminal event. The operation ends cancelled at
// once, a model call meanwhile answers from the cache (no second Gradle daemon), a new
// build waits for the lock and starts only after the first finished, and a build waiting
// for the lock can be cancelled. A progress notification of the drained build, after its
// operation ended, changes neither the operation nor its published progress nor its
// console. Seen failing with the old Close (the second build started while the first
// still ran) and without the sink's detach (the late progress moved the ended operation).
func TestIgnoredCancelKeepsTheRootLocked(t *testing.T) {
	shortCancelWait(t, 300*time.Millisecond)
	gate := filepath.Join(t.TempDir(), "gate")
	evPath := filepath.Join(t.TempDir(), "events")
	f, script := fixtureWithBuildScript(t, gradletest.Config{Events: evPath, Build: ignoringBuild(gate)})

	b1 := f.start(t, "build.run", `{"tasks":[":app:busy"]}`)
	waitForProgress(t, f)
	begun := time.Now()
	if err := f.ops.Cancel(b1); err != nil {
		t.Fatal(err)
	}
	out1 := f.wait(t, b1)
	if out1.State != capability.StateCancelled {
		t.Fatalf("operation ended %s (%+v), want cancelled", out1.State, out1.Error)
	}
	if took := time.Since(begun); took > cancelWait+5*time.Second {
		t.Errorf("the cancelled operation took %v to end, want about cancelWait (%v)", took, cancelWait)
	}
	f.waitLog(t, 5*time.Second, "drain began")
	if strings.Contains(readFile(evPath), "finished "+b1) {
		t.Fatal("the fake finished b1 already: the drain was not exercised")
	}
	progressAtEnd, publishedAtEnd := out1.Progress, f.pub.count(b1)

	// A model call while the drain holds the root answers from the cache, flagged stale
	// after a build script edit, and never reaches the sidecar (a second project.model
	// would start a second Gradle daemon).
	if n := strings.Count(readFile(evPath), "served project.model"); n != 1 {
		t.Fatalf("project.model was served %d times before the check, want the one that cached the model", n)
	}
	tasks := f.call(t, "build.tasks", `{}`)
	if res, ok := tasks.Result.(TasksResult); !ok || res.Stale {
		t.Fatalf("build.tasks during the drain: %+v (%T), want a fresh cached answer", tasks.Result, tasks.Result)
	}
	if err := os.WriteFile(script, []byte("// v2, an edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks = f.call(t, "build.tasks", `{}`)
	if res, ok := tasks.Result.(TasksResult); !ok || !res.Stale {
		t.Fatalf("build.tasks after an edit during the drain: %+v, want stale", tasks.Result)
	}
	if n := strings.Count(readFile(evPath), "served project.model"); n != 1 {
		t.Errorf("project.model was served %d times during the drain, want none beyond the first", n)
	}

	// A build waiting for the lock is cancelled at once, and never reached the sidecar.
	b3 := f.start(t, "build.run", `{"tasks":[":app:busy"]}`)
	time.Sleep(200 * time.Millisecond)
	if n := strings.Count(readFile(evPath), "accepted "); n != 1 {
		t.Fatalf("a second build was accepted during the drain (%d accepted)", n)
	}
	begun = time.Now()
	if err := f.ops.Cancel(b3); err != nil {
		t.Fatal(err)
	}
	if out := f.wait(t, b3); out.State != capability.StateCancelled {
		t.Fatalf("the waiting build ended %s, want cancelled", out.State)
	}
	if took := time.Since(begun); took > 2*time.Second {
		t.Errorf("cancelling a build waiting for the lock took %v, want it at once", took)
	}

	// A build that waits starts only after the drained one finished.
	b2 := f.start(t, "build.run", `{"tasks":[":app:busy"]}`)
	time.Sleep(200 * time.Millisecond)
	if n := strings.Count(readFile(evPath), "accepted "); n != 1 {
		t.Fatalf("the second build was accepted while the first still ran (%d accepted)", n)
	}
	createGate(t, gate)
	if res := mustResult(t, f.wait(t, b2)); res.Outcome != "success" {
		t.Errorf("the second build ended %q, want success", res.Outcome)
	}
	events := readFile(evPath)
	fin, acc := strings.Index(events, "finished "+b1), strings.Index(events, "accepted "+b2)
	if fin < 0 || acc < 0 || fin > acc {
		t.Errorf("the sidecar's log orders finished b1 at %d and accepted b2 at %d, want b1 first:\n%s", fin, acc, events)
	}
	f.waitLog(t, 5*time.Second, "drain ended, the build finished")

	// The late progress and output of b1 reached a sink whose operation was over.
	again, err := f.ops.Wait(t.Context(), b1, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if again.State != capability.StateCancelled || !reflect.DeepEqual(again.Progress, progressAtEnd) {
		t.Errorf("after its late progress the cancelled operation is %s with %+v, want cancelled with %+v", again.State, again.Progress, progressAtEnd)
	}
	if n := f.pub.count(b1); n != publishedAtEnd {
		t.Errorf("the ended operation published %d progress events, want the %d from before it ended", n, publishedAtEnd)
	}
	entry, err := f.arts.Get(f.mustRecord(t, b1).ConsoleRef)
	if err != nil {
		t.Fatal(err)
	}
	if body := readFile(entry.File); strings.Contains(body, "late console line") {
		t.Errorf("the ended build's console took a late line: %q", body)
	}
}

// startedPids reads the pids of the sidecar processes the fake logged, oldest first.
func startedPids(t *testing.T, evPath string) []int {
	t.Helper()
	var pids []int
	for _, line := range strings.Split(readFile(evPath), "\n") {
		if rest, ok := strings.CutPrefix(line, "started "); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				t.Fatal(err)
			}
			pids = append(pids, pid)
		}
	}
	return pids
}

// Decision (m3e step 4): the sidecar's exit ends a drain, so a root cannot stay locked
// for a build that has no sidecar any more; the next call starts a new sidecar. Seen
// failing when the drain waited for the terminal event alone (the second build waited
// out the fake's minute).
func TestDrainEndsWhenTheSidecarExits(t *testing.T) {
	shortCancelWait(t, 300*time.Millisecond)
	gate := filepath.Join(t.TempDir(), "gate")
	evPath := filepath.Join(t.TempDir(), "events")
	f := newFixture(t, gradletest.Config{Events: evPath, Build: ignoringBuild(gate)})

	b1 := f.start(t, "build.run", `{"tasks":[":app:busy"]}`)
	waitForProgress(t, f)
	if err := f.ops.Cancel(b1); err != nil {
		t.Fatal(err)
	}
	if out := f.wait(t, b1); out.State != capability.StateCancelled {
		t.Fatalf("operation ended %s, want cancelled", out.State)
	}
	f.waitLog(t, 5*time.Second, "drain began")

	b2 := f.start(t, "build.run", `{"tasks":[":app:busy"]}`)
	time.Sleep(200 * time.Millisecond)
	if n := strings.Count(readFile(evPath), "accepted "); n != 1 {
		t.Fatalf("the second build was accepted during the drain (%d accepted)", n)
	}
	pids := startedPids(t, evPath)
	if len(pids) != 1 {
		t.Fatalf("sidecars started: %v, want one", pids)
	}
	// The fake sidecar is a process this test started: killed by its pid.
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	f.waitLog(t, 10*time.Second, "drain ended, the sidecar exited")

	createGate(t, gate)
	if res := mustResult(t, f.wait(t, b2)); res.Outcome != "success" {
		t.Errorf("the second build ended %q, want success", res.Outcome)
	}
	if after := startedPids(t, evPath); len(after) != 2 || after[1] == pids[0] {
		t.Errorf("sidecars started: %v, want a second one for the next call", after)
	}
}

// Decision (m3e step 4): a drained build holds no lease, so the idle stop still ends a
// sidecar whose build never finishes; that exit ends the drain. Seen failing when the
// drain kept the lease (the sidecar was never stopped).
func TestDrainHoldsNoLease(t *testing.T) {
	shortCancelWait(t, 300*time.Millisecond)
	old := fixtureIdleTimeout
	fixtureIdleTimeout = 2 * time.Second
	t.Cleanup(func() { fixtureIdleTimeout = old })
	gate := filepath.Join(t.TempDir(), "gate")
	evPath := filepath.Join(t.TempDir(), "events")
	f := newFixture(t, gradletest.Config{Events: evPath, Build: ignoringBuild(gate)})

	b1 := f.start(t, "build.run", `{"tasks":[":app:busy"]}`)
	waitForProgress(t, f)
	if err := f.ops.Cancel(b1); err != nil {
		t.Fatal(err)
	}
	if out := f.wait(t, b1); out.State != capability.StateCancelled {
		t.Fatalf("operation ended %s, want cancelled", out.State)
	}
	f.waitLog(t, 5*time.Second, "drain began")
	if strings.Contains(readFile(evPath), "eof") {
		t.Fatal("the sidecar was stopped before the idle timeout")
	}
	waitFor(t, 15*time.Second, "the idle stop to close the sidecar's stdin", func() bool {
		return strings.Contains(readFile(evPath), "eof")
	})
	f.waitLog(t, 10*time.Second, "drain ended, the sidecar exited")
}

// Decision (ADR-0092 §2 with m3e steps 4 and 5): a drained build keeps its root locked,
// so a forgotten root is not closed under it; it closes when the drain ends, after the
// build's terminal event. A root a workspace holds is never closed. The fake logs
// "closed <root>" for each connection.close it serves. Seen failing with the release
// hook removed (the forgotten root never closed) and with the close not waiting for the
// drain (a close logged before "finished").
func TestDrainedBuildOnAForgottenRootClosesAtTheDrainsEnd(t *testing.T) {
	for _, tc := range []struct {
		name  string
		inUse bool
	}{{"a forgotten root", false}, {"a root a workspace holds", true}} {
		t.Run(tc.name, func(t *testing.T) {
			shortCancelWait(t, 300*time.Millisecond)
			fixtureInUse = func(string) bool { return tc.inUse }
			t.Cleanup(func() { fixtureInUse = nil })
			gate := filepath.Join(t.TempDir(), "gate")
			evPath := filepath.Join(t.TempDir(), "events")
			f := newFixture(t, gradletest.Config{Events: evPath, Build: ignoringBuild(gate)})

			b1 := f.start(t, "build.run", `{"tasks":[":app:busy"]}`)
			waitForProgress(t, f)
			if err := f.ops.Cancel(b1); err != nil {
				t.Fatal(err)
			}
			if out := f.wait(t, b1); out.State != capability.StateCancelled {
				t.Fatalf("operation ended %s, want cancelled", out.State)
			}
			f.waitLog(t, 5*time.Second, "drain began")
			// The model fetch that precedes a build on a forgotten root closes it once, and
			// the build reopens it: closes are counted from the drain's start.
			before := strings.Count(readFile(evPath), "closed ")
			time.Sleep(500 * time.Millisecond)
			if n := strings.Count(readFile(evPath), "closed "); n != before {
				t.Fatalf("the root was closed while the drained build still ran:\n%s", readFile(evPath))
			}

			createGate(t, gate)
			f.waitLog(t, 5*time.Second, "drain ended, the build finished")
			if tc.inUse {
				time.Sleep(500 * time.Millisecond)
				if strings.Count(readFile(evPath), "closed ") != before {
					t.Fatalf("a root a workspace holds was closed at the drain's end:\n%s", readFile(evPath))
				}
				return
			}
			waitFor(t, 5*time.Second, "the forgotten root to close at the drain's end", func() bool {
				return strings.Count(readFile(evPath), "closed ") == before+1
			})
			events := readFile(evPath)
			if fin, closed := strings.Index(events, "finished "+b1), strings.LastIndex(events, "closed "); fin < 0 || closed < fin {
				t.Errorf("the sidecar's log orders finished at %d and closed at %d, want the build's end first:\n%s", fin, closed, events)
			}
		})
	}
}
