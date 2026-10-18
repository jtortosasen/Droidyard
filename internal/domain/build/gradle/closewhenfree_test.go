package gradle_test

import (
	"context"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/process"
)

// lockFree reports whether root's lock can be taken at once: Close takes it under its
// context, and on a root whose connection is gone it then does nothing else, so a quick
// return means the lock was free and one at the context's end means it was not.
func lockFree(c *gradle.Client, root string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	c.Close(ctx, root)
	return time.Since(start) < 100*time.Millisecond
}

// Decision (ADR-0092 §2): CloseWhenFree waits for the root's lock without a deadline,
// but the call itself is bounded like a workspace close's (5 s), so a sidecar that never
// answers connection.close does not leave a holder of the root's lock that nothing can
// end. The fake never answers connection.close; the root's lock is free again within
// about 5 s of the call. Seen failing with the call run under the lock-wait's context
// (no deadline): the lock was never released.
func TestCloseWhenFreeBoundsTheCall(t *testing.T) {
	c, _, ev := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t)), HoldOn: "connection.close"}, time.Minute)
	root := rootDir(t)
	if _, _, err := c.Model(context.Background(), root); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	done := make(chan struct{})
	go func() { c.CloseWhenFree(root); close(done) }()
	waitFor(t, "the fake to receive connection.close", func() bool { return count(events(t, ev), "served connection.close") == 1 })
	if lockFree(c, root) {
		t.Fatal("the root's lock was free while connection.close was unanswered; the test does not hold it")
	}
	select {
	case <-done:
	case <-time.After(9 * time.Second):
		t.Fatal("CloseWhenFree did not return within 9 s of an unanswered connection.close; want about 5 s")
	}
	if took := time.Since(start); took < 4*time.Second {
		t.Errorf("CloseWhenFree returned after %v; the 5 s bound of the call was not what ended it", took)
	}
	if !lockFree(c, root) {
		t.Error("the root's lock is still held after CloseWhenFree returned")
	}
}

// Decision (ADR-0092 §2 and Close's guard, plan m1p step 2): CloseWhenFree never starts
// a sidecar to close a connection that is gone. With the sidecar stopped, the call
// returns and the fake has started once, not twice. Seen failing without the
// not-running guard: the close restarted the sidecar.
func TestCloseWhenFreeNeverStartsASidecar(t *testing.T) {
	c, sup, ev := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t))}, time.Minute)
	root := rootDir(t)
	if _, _, err := c.Model(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	sup.StopProcess(gradle.ID, process.Global)
	if st := sup.State(gradle.ID, process.Global); !gone(st) {
		t.Fatalf("the sidecar is %q after StopProcess; this test needs it stopped", st)
	}

	c.CloseWhenFree(root)
	if n := started(t, ev); n != 1 {
		t.Errorf("the sidecar started %d times; want the one start of the model call, none for the close", n)
	}
	if st := sup.State(gradle.ID, process.Global); !gone(st) {
		t.Errorf("the sidecar is %q after CloseWhenFree; want it still stopped", st)
	}
}

// Decision (ADR-0092 §2): a build in flight on the root finishes first; CloseWhenFree
// reaches the sidecar only after the build's run is closed. The bound of five seconds is
// not the wait (cmd/droidyard's TestSwitchedGradleRootCloses holds the build longer).
func TestCloseWhenFreeWaitsForTheRootsOperation(t *testing.T) {
	c, _, ev := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t))}, time.Minute)
	root := rootDir(t)
	run, err := c.BeginBuild(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			run.Close()
		}
	}()

	done := make(chan struct{})
	go func() { c.CloseWhenFree(root); close(done) }()
	time.Sleep(700 * time.Millisecond)
	if n := count(events(t, ev), "served connection.close"); n != 0 {
		t.Fatalf("connection.close reached the sidecar while a build held the root (%d times)", n)
	}
	released = true
	run.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseWhenFree did not return after the build's run was closed")
	}
	if n := count(events(t, ev), "served connection.close"); n != 1 {
		t.Errorf("connection.close reached the sidecar %d times after the build; want once", n)
	}
	if got := count(events(t, ev), "closed "+root); got != 1 {
		t.Errorf("the sidecar closed %s %d times; want once", root, got)
	}
}
