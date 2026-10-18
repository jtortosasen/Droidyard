package device

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// Decision (plan m2p step 3; ADR-0078 §5): an acquire that arrives just after the last
// release has dropped its reference does not join the source that is about to close: the
// end is claimed with the last decrement, so the acquire waits for that source's teardown
// and starts a new one that streams. A test hook makes the acquire land in that window.
// Seen failing with the end claimed after the unlock (the window open): the acquire got the
// closing source and its viewer's Next returned screen.ErrClosed.
func TestScreenSourceAcquireDuringLastRelease(t *testing.T) {
	f := startedFixture(t, nil)
	_, release := f.mustAcquire(t)
	f.waitServers(t, 1)
	type res struct {
		s   *screenSource
		r   func()
		err error
	}
	got := make(chan res, 1)
	screenAfterLastUnref = func() {
		screenAfterLastUnref = nil
		go func() {
			ctx, cancel := ssCtx()
			defer cancel()
			s, r, err := f.acquire(ctx)
			got <- res{s, r, err}
		}()
		time.Sleep(150 * time.Millisecond) // the acquire is in the window by now
	}
	t.Cleanup(func() { screenAfterLastUnref = nil })
	release()
	r := <-got
	if r.err != nil {
		t.Fatalf("acquire: %v", r.err)
	}
	defer r.r()
	ctx, cancel := ssCtx()
	defer cancel()
	if p, err := r.s.Join().Next(ctx); err != nil || !p.Key {
		t.Fatalf("the new acquire's first packet: key=%v err=%v, want a key frame from a live source", p.Key, err)
	}
	f.waitServers(t, 2)
	if n := len(f.callsWith(t, "app_process")); n != 2 {
		t.Errorf("%d servers started, want a fresh one for the second acquire", n)
	}
}

// Decision (ADR-0078 §6; plan m2p step 3): one server per device at a time. A start that
// follows a release waits for the old source's teardown, so the new push does not begin
// before the old forward is removed. The removal is made slow here. Seen failing with an
// ending source treated as absent (a new start at once): the second push began 400 ms
// before the first removal ended.
func TestScreenSourceNewStartWaitsForTeardown(t *testing.T) {
	f := startedFixture(t, map[string]string{uiSerial + ".forwardremove.delay": "400"})
	_, release := f.mustAcquire(t)
	f.waitServers(t, 1)
	claimed := make(chan struct{})
	screenAfterLastUnref = func() {
		screenAfterLastUnref = nil
		close(claimed)
	}
	t.Cleanup(func() { screenAfterLastUnref = nil })
	go release()
	<-claimed // the old source is ending and not yet gone
	_, r2 := f.mustAcquire(t)
	defer r2()
	f.waitServers(t, 2)
	events := devicetest.Events(t, f.dir)
	index := func(what string, nth int) int {
		for i, e := range events {
			if e == what {
				if nth--; nth == 0 {
					return i
				}
			}
		}
		return -1
	}
	// The push is a start's first device-side step, and the fake writes its begin event
	// before the call returns; the server's own begin event may still be unwritten here.
	removed := index("end "+uiSerial+".forwardremove", 1)
	push2 := index("begin "+uiSerial+".pushscrcpy", 2)
	if removed < 0 || push2 < 0 || push2 < removed {
		t.Errorf("the second start began before the first forward was removed (removal end %d, second push %d): %q",
			removed, push2, events)
	}
}

// Decision (ADR-0078 §1, as the preamble): an adb that cannot be run at all is
// DEVICE_ADB_FAILED, not a stream failure; the same for the shell child that cannot be
// started, which leaves no forward behind.
func TestScreenSourceAdbCannotBeRun(t *testing.T) {
	t.Run("not executable", func(t *testing.T) {
		f := startedFixture(t, nil)
		f.tgt = &target{adb: filepath.Join(t.TempDir(), "no-such-adb"), serial: uiSerial}
		ctx, cancel := ssCtx()
		defer cancel()
		_, _, err := f.acquire(ctx)
		if ce := capability.AsError(err); ce == nil || ce.Code != ErrADBFailed || !strings.HasPrefix(ce.Cause, "push: ") {
			t.Fatalf("error %v, want DEVICE_ADB_FAILED in the push phase", err)
		}
		f.requireClean(t)
	})
	t.Run("the shell cannot be started", func(t *testing.T) {
		f := startedFixture(t, nil)
		f.adb.stream = func(*exec.Cmd, string) (Stream, error) { return nil, errors.New("boom") }
		ctx, cancel := ssCtx()
		defer cancel()
		_, _, err := f.acquire(ctx)
		if ce := capability.AsError(err); ce == nil || ce.Code != ErrADBFailed || !strings.HasPrefix(ce.Cause, "start: ") {
			t.Fatalf("error %v, want DEVICE_ADB_FAILED in the start phase", err)
		}
		if len(f.callsWith(t, "forward --remove tcp:")) != 1 {
			t.Errorf("calls %q, want the forward removed", f.calls(t))
		}
		f.requireClean(t)
	})
}
