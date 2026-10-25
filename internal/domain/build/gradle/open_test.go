package gradle_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/process"
)

// The chains below are what the pinned Tooling API sends for a distribution install
// that failed, as recorded by m3e step 1 (the plan's step 1 evidence): the outer text is
// the same for every cause, the inner texts differ.
const (
	stallChain = "Could not install Gradle distribution from 'https://services.gradle.org/distributions/gradle-8.0.2-bin.zip'.\n" +
		"Downloading https://services.gradle.org/distributions/gradle-8.0.2-bin.zip failed: timeout (10000ms)\n" +
		"Read timed out"
	lockChain = "Could not install Gradle distribution from 'https://services.gradle.org/distributions/gradle-8.0.2-bin.zip'.\n" +
		"Timeout of 120000 reached waiting for exclusive access to file: /home/u/.gradle/wrapper/dists/gradle-8.0.2-bin/x/gradle-8.0.2-bin.zip"
)

// openFailure runs a Model call against a fake whose connection.open fails with chain, on
// a root with or without the wrapper's properties, and returns the client's error.
func openFailure(t *testing.T, chain string, wrapper bool) *capability.Error {
	t.Helper()
	cfg := gradletest.Config{Model: gradletest.MustModel(model(t)), OpenFailure: chain}
	c, _, _ := newClient(t, cfg, time.Minute)
	root := rootDir(t)
	if wrapper {
		props := filepath.Join(root, "gradle", "wrapper", "gradle-wrapper.properties")
		if err := os.MkdirAll(filepath.Dir(props), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(props, []byte("distributionUrl=https\\://services.gradle.org/distributions/gradle-8.0.2-bin.zip\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := c.Model(context.Background(), root)
	if err == nil {
		t.Fatal("a failed connection.open succeeded")
	}
	if got := codeOf(t, err); got != capability.ErrProjectModelFailed {
		t.Fatalf("code = %s, want PROJECT_MODEL_FAILED: %v", got, err)
	}
	return err.(*capability.Error)
}

// Decision (m3e step 3; plan review B3): a failed distribution install names its way
// out. A stalled or refused download says to run `./gradlew --version` once; Gradle's
// install lock held by another process says so and to retry when it finishes; any other
// open failure gets neither, and neither names a path (ADR-0075). The lock text alone,
// without the install failure around it, is not an install failure. A root with no
// wrapper has no ./gradlew: its step names the network instead (verifier finding 1).
// Seen failing with the mapping removed (no nextStep), and the wrapperless case with
// the wrapper check removed.
func TestDistributionFailureNamesTheWayOut(t *testing.T) {
	cases := []struct {
		name    string
		chain   string
		wrapper bool
		want    string // a substring of the nextStep; "" means no nextStep at all
		not     string // a substring the nextStep must not hold
	}{
		{"a stalled download", stallChain, true, "./gradlew --version", "another Gradle process"},
		{"a stalled download, no wrapper", stallChain, false, "no Gradle wrapper", "./gradlew"},
		{"the install lock held by another process", lockChain, true, "another Gradle process", "./gradlew"},
		{"an unrelated open failure", "Could not read the settings of the project", true, "", ""},
		{"the lock text outside an install failure", "Timeout of 120000 reached waiting for exclusive access to file: /x.lck", true, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := openFailure(t, tc.chain, tc.wrapper)
			if tc.want == "" {
				if e.NextStep != "" {
					t.Fatalf("nextStep = %q, want none", e.NextStep)
				}
				return
			}
			if !strings.Contains(e.NextStep, tc.want) {
				t.Fatalf("nextStep = %q, want it to hold %q", e.NextStep, tc.want)
			}
			if strings.Contains(e.NextStep, tc.not) {
				t.Fatalf("nextStep = %q must not hold %q", e.NextStep, tc.not)
			}
			if strings.Contains(e.NextStep, "/") && !strings.Contains(e.NextStep, "./gradlew") {
				t.Fatalf("nextStep names a path: %q", e.NextStep)
			}
			if !strings.Contains(e.Cause, "Could not install Gradle distribution") {
				t.Fatalf("the cause lost Gradle's chain: %q", e.Cause)
			}
		})
	}
}

// verbs reduces the fake's event log to its connection lifecycle: "opened" and "closed",
// in order.
func verbs(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, e := range events(t, path) {
		if v, _, ok := strings.Cut(e, " "); ok && (v == "opened" || v == "closed") {
			out = append(out, v)
		}
	}
	return out
}

func sameSeq(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// abandonedOpen starts a call that reaches the sidecar's connection.open, held at the
// gate, and cancels its caller before the reply. It returns the client, the supervisor,
// the events file, the root and the gate's path.
func abandonedOpen(t *testing.T, call func(ctx context.Context, c *gradle.Client, root string) error, holdOn ...string) (*gradle.Client, *process.Supervisor, string, string, string) {
	t.Helper()
	gate := filepath.Join(t.TempDir(), "gate")
	cfg := gradletest.Config{Model: gradletest.MustModel(model(t)), OpenGate: gate}
	if len(holdOn) > 0 {
		cfg.HoldOn = holdOn[0]
	}
	c, sup, ev := newClient(t, cfg, time.Minute)
	root := rootDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := make(chan error, 1)
	go func() { failed <- call(ctx, c, root) }()
	waitFor(t, "the open to reach the sidecar", func() bool { return count(events(t, ev), "served connection.open") >= 1 })
	cancel()
	if err := <-failed; !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled call returned %v, want context.Canceled", err)
	}
	return c, sup, ev, root, gate
}

func release(t *testing.T, gate string) {
	t.Helper()
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Decision (m3e step 3; plan m1g's ignored cancel, gradle.md §Concurrency): a
// connection.open whose caller went away is still served by the sidecar, which then
// holds the root open and refuses a second open ("root is already open"). The client
// marks the root, and the next call on that sidecar closes it before it opens, so the
// root stays usable; the fake's refusal of a double open stays in force, so a missing
// mark is seen. The second call is made while the sidecar is still held in the first
// open, as in the real sidecar, where its close waits behind the install. Seen failing
// with the mark removed ("root is already open").
func TestAbandonedOpenLeavesTheRootUsable(t *testing.T) {
	calls := map[string]func(ctx context.Context, c *gradle.Client, root string) error{
		"a model call": func(ctx context.Context, c *gradle.Client, root string) error {
			_, _, err := c.Model(ctx, root)
			return err
		},
		"a build": func(ctx context.Context, c *gradle.Client, root string) error {
			run, err := c.BeginBuild(ctx, root)
			if err == nil {
				run.Close()
			}
			return err
		},
	}
	for name, abandon := range calls {
		for next, again := range calls {
			t.Run(name+" then "+next, func(t *testing.T) {
				c, _, ev, root, gate := abandonedOpen(t, abandon)
				done := make(chan error, 1)
				go func() { done <- again(context.Background(), c, root) }()
				time.Sleep(100 * time.Millisecond) // the second call's close is queued behind the open
				release(t, gate)
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("the call after an abandoned open failed: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("the call after an abandoned open never answered")
				}
				if got, want := verbs(t, ev), []string{"opened", "closed", "opened"}; !sameSeq(got, want) {
					t.Fatalf("connection lifecycle = %v, want %v (the abandoned open, a close, the new open)", got, want)
				}
			})
		}
	}
}

// Decision (m3e step 3): an abandoned open counts as open for a workspace's close, so
// the close reaches the sidecar and the root does not stay open there until the sidecar
// exits. Seen failing with the mark not counted by the close's guard (no close sent).
func TestAbandonedOpenIsClosedByAWorkspaceClose(t *testing.T) {
	c, _, ev, root, gate := abandonedOpen(t, func(ctx context.Context, c *gradle.Client, root string) error {
		_, _, err := c.Model(ctx, root)
		return err
	})
	release(t, gate)
	waitFor(t, "the abandoned open to complete", func() bool { return len(verbs(t, ev)) == 1 })
	c.Close(context.Background(), root)
	if got, want := verbs(t, ev), []string{"opened", "closed"}; !sameSeq(got, want) {
		t.Fatalf("connection lifecycle = %v, want %v: the workspace's close did not reach the abandoned open", got, want)
	}
}

// Decision (m3e step 3): the mark belongs to one sidecar process. A restarted sidecar
// has no connection for the root, so the next call opens it without a close first.
func TestAbandonedOpenMarkDoesNotSurviveASidecarRestart(t *testing.T) {
	c, sup, ev, root, gate := abandonedOpen(t, func(ctx context.Context, c *gradle.Client, root string) error {
		_, _, err := c.Model(ctx, root)
		return err
	})
	release(t, gate)
	waitFor(t, "the abandoned open to complete", func() bool { return len(verbs(t, ev)) == 1 })
	pid := sup.Pid(gradle.ID, process.Global)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sidecar to be gone", func() bool { return gone(sup.State(gradle.ID, process.Global)) })
	if _, _, err := c.Model(context.Background(), root); err != nil {
		t.Fatalf("the call after a restart failed: %v", err)
	}
	if n := started(t, ev); n != 2 {
		t.Fatalf("the sidecar started %d times, want 2", n)
	}
	if got, want := verbs(t, ev), []string{"opened", "opened"}; !sameSeq(got, want) {
		t.Fatalf("connection lifecycle = %v, want %v: a close was sent to a sidecar that never held the root", got, want)
	}
}

// Decision (m3e step 3): a root whose open was abandoned counts as having a connection
// when the sidecar crashes, so it is marked degraded with the others (plan m1g D20) and
// the next successful handshake clears it.
func TestAbandonedOpenRootIsDegradedByACrash(t *testing.T) {
	a, sup, ev, rootA, gate := abandonedOpen(t, func(ctx context.Context, c *gradle.Client, root string) error {
		_, _, err := c.Model(ctx, root)
		return err
	}, "project.model")
	release(t, gate)
	waitFor(t, "the abandoned open to complete", func() bool { return len(verbs(t, ev)) == 1 })
	failed := make(chan error, 1)
	go func() {
		_, _, err := a.Model(context.Background(), rootDir(t))
		failed <- err
	}()
	waitFor(t, "the second root's model call to reach the fake", func() bool { return count(events(t, ev), "served project.model") >= 1 })
	if err := syscall.Kill(sup.Pid(gradle.ID, process.Global), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if got := codeOf(t, <-failed); got != capability.ErrBuildSidecarCrashed {
		t.Fatalf("code = %s, want BUILD_SIDECAR_CRASHED", got)
	}
	if d := a.Degraded(rootA); d == nil {
		t.Fatal("the root whose open was abandoned is not degraded by the crash")
	}
}
