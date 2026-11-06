package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/testtime"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// idleClock is the file a re-executed daemon's workspace clock reads: the seconds added to
// the real time. Writing it moves every stamp and judgement of the registry at once.
type idleClock string

func (c idleClock) set(t *testing.T, d time.Duration) {
	t.Helper()
	if err := os.WriteFile(string(c), []byte(strconv.Itoa(int(d.Seconds()))), 0o600); err != nil {
		t.Fatal(err)
	}
}

// idleEnv is the environment that gives a daemon the workspace idle-clock seam and a sweep
// every 50 ms, with the clock file path.
func idleEnv(t *testing.T) (idleClock, []string) {
	t.Helper()
	clock := idleClock(filepath.Join(t.TempDir(), "clock"))
	return clock, []string{workspaceClockEnv + "=" + string(clock), workspaceSweepEnv + "=50ms"}
}

// TestIdleReleaseStopsTheSidecar holds the wiring of ADR-0137 §3 and ADR-0027 in the shipped
// daemon: a root that has had no call and no running operation for longer than
// workspace.IdleAfter is released, and its release stops the workspace's sidecars (the
// dummy and the helper it forked) and removes their pid records. The clock is moved
// to +31 minutes through the daemon's test seam; nothing is waited for. Seen failing with
// `StopSidecars` left out of workspace.New in main.go: the dummy and its helper survive
// the release and stay recorded.
func TestIdleReleaseStopsTheSidecar(t *testing.T) {
	t.Parallel()
	clock := idleClock(filepath.Join(t.TempDir(), "clock"))
	c := newSidecarCase(t, map[string]string{workspaceClockEnv: string(clock), workspaceSweepEnv: "50ms"})
	if env, code := callIn(t, c.state, c.sub, "fixture.touch"); code != 0 {
		t.Fatalf("fixture.touch: %+v", env.Error)
	}
	pid := c.sidecar(sidecarParams{Options: []string{"fork-helper"}, IdleMs: 60_000, ShutdownMs: 300})
	before := snap(t, pid, c.helper())
	if len(c.records()) != 1 || len(before.survivors()) != 2 {
		t.Fatalf("before the release: records %v, survivors %v; want the dummy recorded and both alive", c.records(), before.survivors())
	}

	clock.set(t, workspace.IdleAfter+time.Minute)
	testtime.Eventually(t, testtime.Bound(15*time.Second), "the release to stop the dummy and its helper", func() bool {
		return len(before.survivors()) == 0 && len(c.records()) == 0
	})
	c.stop()
}

// TestIdleReleaseClosesTheGradleRootOnce holds ADR-0075 §8 through the idle release on a real
// daemon over the fake Gradle sidecar: releasing the top's workspace, whose calls resolved
// <top>/dev, closes the connection to dev/ once (`closed <dev>` once in the fake's log); and
// while another open workspace, the one at <top>/dev, has that Gradle root as its own, the
// top's release leaves the connection open and the last release closes it. Seen failing
// with `CloseRoot` left out of workspace.New in main.go: no `closed` line.
func TestIdleReleaseClosesTheGradleRootOnce(t *testing.T) {
	t.Parallel()
	start := func(t *testing.T) (state, top, dev, events string, clock idleClock, stop func()) {
		t.Helper()
		clock, env := idleEnv(t)
		state = shortDir(t)
		cfg, events := fakeSidecar(t, gradleRootModel)
		top = topWith(t, ".cursor/", "dev/settings.gradle.kts")
		dev = filepath.Join(top, "dev")
		daemonEnv := append(env, gradleFakeEnv+"="+cfg, gradleJVMEnv+"="+fakeJDK(t))
		srv := droidyardEnv(t, state, "", daemonEnv, "serve")
		srv.expect(t, "listening")
		for _, root := range []string{top, dev} {
			if code := droidyard(t, state, "trust", root).wait(t); code != 0 {
				t.Fatalf("droidyard trust %s exited %d", root, code)
			}
		}
		return state, top, dev, events, clock, func() {
			if code := droidyard(t, state, "stop").wait(t); code != 0 {
				t.Errorf("stop exited %d", code)
			}
			srv.wait(t)
		}
	}
	call := func(t *testing.T, state, dir string) {
		t.Helper()
		if env, code := callIn(t, state, dir, "build.tasks"); code != 0 {
			t.Fatalf("build.tasks in %s exited %d: %+v", dir, code, env.Error)
		}
	}
	// quiet is how long the log is watched for a `closed` line that should not come: the
	// release is a goroutine of its own, so a close, were it due, would follow within it.
	quiet := func() { time.Sleep(testtime.Bound(1500 * time.Millisecond)) }

	t.Run("the top alone: its release closes dev/ once", func(t *testing.T) {
		t.Parallel()
		state, top, dev, events, clock, stop := start(t)
		call(t, state, top)
		if got := linesWithPrefix(t, events, "closed "); len(got) != 0 {
			t.Fatalf("before the release the sidecar closed %q", got)
		}
		clock.set(t, workspace.IdleAfter+time.Minute)
		waitLines(t, events, "closed ", 1)
		quiet()
		if got, want := linesWithPrefix(t, events, "closed "), []string{"closed " + dev}; !reflect.DeepEqual(got, want) {
			t.Errorf("after the release the sidecar closed %q; want %q, once", got, want)
		}
		stop()
	})

	t.Run("two workspaces on one Gradle root: the first release keeps it, the last closes it", func(t *testing.T) {
		t.Parallel()
		state, top, dev, events, clock, stop := start(t)
		call(t, state, top)
		call(t, state, dev)
		// dev/ is used again 20 minutes on, so at +31 minutes the top is idle and dev/ is not.
		clock.set(t, 20*time.Minute)
		call(t, state, dev)
		clock.set(t, 31*time.Minute)
		quiet()
		if got := linesWithPrefix(t, events, "closed "); len(got) != 0 {
			t.Fatalf("the top's release closed %q while the workspace at dev/ is open and uses it", got)
		}
		clock.set(t, 52*time.Minute)
		waitLines(t, events, "closed ", 1)
		quiet()
		if got, want := linesWithPrefix(t, events, "closed "), []string{"closed " + dev}; !reflect.DeepEqual(got, want) {
			t.Errorf("after both releases the sidecar closed %q; want %q, once", got, want)
		}
		stop()
	})
}
