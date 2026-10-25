package gradle_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// workspaces stands for the registry's GradleRootInUse: the roots an open workspace
// holds, and how often the client asked.
type workspaces struct {
	mu    sync.Mutex
	held  map[string]bool
	asked int
	// flip, when positive, makes every answer after that many asks true: a workspace
	// that took the root while the close waited for its lock.
	flip int
}

func (w *workspaces) inUse(root string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked++
	if w.flip > 0 && w.asked > w.flip {
		return true
	}
	return w.held[root]
}

func (w *workspaces) hold(root string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.held == nil {
		w.held = map[string]bool{}
	}
	w.held[root] = true
}

func (w *workspaces) asks() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.asked
}

func newReleaseClient(t *testing.T, w *workspaces, cfg gradletest.Config) (*gradle.Client, string) {
	t.Helper()
	cfg.Model = gradletest.MustModel(model(t))
	c, _, ev := newClientWith(t, cfg, time.Minute, policy.BelowFloor{}, nil, func(o *gradle.Options) { o.InUse = w.inUse })
	return c, ev
}

// settled waits for the fake's connection lifecycle to be want, then waits a while more
// and checks it stayed: a close that should not come is given time to come.
func settled(t *testing.T, ev string, want ...string) {
	t.Helper()
	waitFor(t, "the connection lifecycle "+strings.Join(want, ", "), func() bool { return sameSeq(verbs(t, ev), want) })
	time.Sleep(300 * time.Millisecond)
	if got := verbs(t, ev); !sameSeq(got, want) {
		t.Fatalf("connection lifecycle = %v, want it to stay %v", got, want)
	}
}

// Decision (ADR-0092 §2, m3e step 5): a call on a root no workspace holds reopens its
// connection, and the connection closes when the call ends, not before and not never. A
// root a workspace holds stays open: the client asks InUse and the answer decides. The
// second Model call is the stale reopen after the forgotten root was closed: it opens
// again and closes again. Seen failing without the release hook (the root stayed open),
// and with InUse ignored (a held root was closed).
func TestStaleCallReopenedRootCloses(t *testing.T) {
	t.Run("a model call on a forgotten root", func(t *testing.T) {
		w := &workspaces{}
		c, ev := newReleaseClient(t, w, gradletest.Config{})
		root := rootDir(t)
		for i := 0; i < 2; i++ {
			if _, _, err := c.Model(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			want := []string{"opened", "closed"}
			if i == 1 {
				want = append(want, "opened", "closed")
			}
			waitFor(t, "the call's connection to close", func() bool { return sameSeq(verbs(t, ev), want) })
		}
	})

	t.Run("a root a workspace holds", func(t *testing.T) {
		w := &workspaces{}
		c, ev := newReleaseClient(t, w, gradletest.Config{})
		root := rootDir(t)
		w.hold(root)
		if _, _, err := c.Model(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		settled(t, ev, "opened")
		if w.asks() == 0 {
			t.Error("the release never asked InUse: the root stayed open because nothing looked, not because a workspace holds it")
		}
	})

	t.Run("a build on a forgotten root", func(t *testing.T) {
		w := &workspaces{}
		c, ev := newReleaseClient(t, w, gradletest.Config{})
		root := rootDir(t)
		run, err := c.BeginBuild(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer run.Close()
		settled(t, ev, "opened") // the build holds the root: nothing closes it meanwhile
		run.Close()
		waitFor(t, "the root to close after the build's end", func() bool { return sameSeq(verbs(t, ev), []string{"opened", "closed"}) })
	})

	// The model call that waited for a build's lock: both releases (the build's and the
	// model call's) ask, whichever takes the lock first, and the root ends closed with
	// every open matched by a close.
	t.Run("a model call that waited for a build", func(t *testing.T) {
		w := &workspaces{}
		c, ev := newReleaseClient(t, w, gradletest.Config{})
		root := rootDir(t)
		run, err := c.BeginBuild(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer run.Close()
		done := make(chan error, 1)
		go func() { _, _, err := c.Model(context.Background(), root); done <- err }()
		time.Sleep(200 * time.Millisecond)
		select {
		case err := <-done:
			t.Fatalf("the model call returned while the build held the root: %v", err)
		default:
		}
		run.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the root to end closed", func() bool {
			v := verbs(t, ev)
			return len(v) >= 2 && v[len(v)-1] == "closed" && count(v, "opened") == count(v, "closed")
		})
	})
}

// Decision (ADR-0092 §2, m3e step 5): InUse is asked again once the close holds the
// root's lock, because a workspace may have taken the root while the close waited. The
// first answer (at the release) is false, the second (under the lock) is true: the root
// stays open. Seen failing with the close run without the second look (the root closed).
func TestStaleReopenCloseAsksAgainUnderTheLock(t *testing.T) {
	w := &workspaces{flip: 1}
	c, ev := newReleaseClient(t, w, gradletest.Config{})
	if _, _, err := c.Model(context.Background(), rootDir(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the close to ask InUse a second time", func() bool { return w.asks() >= 2 })
	settled(t, ev, "opened")
}

// Decision (m3e steps 4 and 5): a drain releases the lease and keeps the root's lock, so
// ReleaseLease is not a release of the root: it neither asks InUse nor closes anything,
// and the root closes only when the run is closed, which is the drain's end. Seen failing
// with ReleaseLease also freeing the lock (the root closed under the running build).
func TestDrainedBuildClosesItsForgottenRootAtTheEnd(t *testing.T) {
	w := &workspaces{}
	c, ev := newReleaseClient(t, w, gradletest.Config{})
	root := rootDir(t)
	run, err := c.BeginBuild(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	run.ReleaseLease()
	settled(t, ev, "opened")
	if n := w.asks(); n != 0 {
		t.Errorf("InUse was asked %d times by ReleaseLease; the lock was not released", n)
	}
	run.Close()
	waitFor(t, "the root to close at the drain's end", func() bool { return sameSeq(verbs(t, ev), []string{"opened", "closed"}) })
}

// Decision (m3e step 3 with step 5): the connection an abandoned open left on the
// sidecar is closed too. A call whose open lost its caller releases the lock with the
// root marked unknown; on a forgotten root that release closes it, the close queued
// behind the open the sidecar still serves, so the lifecycle is open then close. Seen
// failing with the hook asking only for a connection the client saw opened (the open
// the caller abandoned stayed on the sidecar).
func TestAbandonedOpenOnAForgottenRootCloses(t *testing.T) {
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
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			gate := filepath.Join(t.TempDir(), "gate")
			w := &workspaces{}
			c, ev := newReleaseClient(t, w, gradletest.Config{OpenGate: gate})
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
			release(t, gate)
			waitFor(t, "the abandoned open to be closed", func() bool { return sameSeq(verbs(t, ev), []string{"opened", "closed"}) })
		})
	}
}
