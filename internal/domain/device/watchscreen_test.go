package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// device.watchScreen and the stream table (plan m2p step 4; ADR-0078 §3, §6) over the
// re-executed fake adb and the fixture listener of screensource_test.go.

// wsFix is the live-view fixture with a bus and one session that carries events.
type wsFix struct {
	*ssFix
	bus  *event.Bus
	sess *event.Session
}

func newWSFix(t *testing.T) *wsFix {
	t.Helper()
	return newWSFixFrom(t, startedFixture(t, nil))
}

// newWSFixFrom is newWSFix over a live-view fixture the test scripted.
func newWSFixFrom(t *testing.T, f *ssFix) *wsFix {
	t.Helper()
	bus := event.New(event.Options{})
	f.adb.SetBus(bus)
	w := &wsFix{ssFix: f, bus: bus, sess: event.NewSession(bus, "s1")}
	t.Cleanup(func() {
		w.sess.Close()
		ctx, cancel := ssCtx()
		defer cancel()
		f.adb.screenViews.StopAll(ctx)
	})
	return w
}

// watchOn starts device.watchScreen on sess and returns its outcome.
func (w *wsFix) watchOn(t *testing.T, sess *event.Session) capability.Outcome {
	t.Helper()
	id, err := w.ops.Start(context.Background(), capability.Request{
		Capability: "device.watchScreen", Params: []byte(`{"device":"` + uiSerial + `"}`),
		Client: capability.ClientUI, Session: sess,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return w.wait(t, id)
}

// watch requires the call to succeed and returns its result and the subscription.
func (w *wsFix) watch(t *testing.T) (WatchScreenResult, *event.Subscription) {
	t.Helper()
	out := w.watchOn(t, w.sess)
	if out.State != capability.StateCompleted {
		t.Fatalf("device.watchScreen %s with error %+v", out.State, out.Error)
	}
	res := out.Result.(WatchScreenResult)
	sub, ok := w.sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatalf("the session does not hold subscription %s", res.SubscriptionID)
	}
	return res, sub
}

// stopped waits for the subscription's next event and requires it to be a stopped event.
func stopped(t *testing.T, sub *event.Subscription) ScreenStoppedEvent {
	t.Helper()
	e := next(t, sub)
	if e.Topic != TopicScreen || e.Type != EventStopped {
		t.Fatalf("event %s/%s, want %s/%s", e.Topic, e.Type, TopicScreen, EventStopped)
	}
	var se ScreenStoppedEvent
	if err := json.Unmarshal(e.Payload, &se); err != nil {
		t.Fatal(err)
	}
	if se.Device != uiSerial {
		t.Errorf("stopped names device %q, want %q", se.Device, uiSerial)
	}
	return se
}

// requireGone says the stream table and the source set are empty and the id is unknown:
// the viewer was released (the last reference ended the server, which requireClean reads
// off the host).
func (w *wsFix) requireGone(t *testing.T, id string) {
	t.Helper()
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the stream to leave the table", func() bool { return w.adb.LiveScreenStreams() == 0 })
	if _, err := w.adb.AttachScreen(id); !errors.Is(err, ErrScreenStreamUnknown) {
		t.Errorf("attach after the end: %v, want ErrScreenStreamUnknown", err)
	}
	// The id is gone from the table, not only refused: a dead stream's entry would stay for
	// the daemon's life.
	ss := &w.adb.screenViews
	ss.mu.Lock()
	n := len(ss.byID)
	ss.mu.Unlock()
	if n != 0 {
		t.Errorf("%d ids are still in the table", n)
	}
	w.requireClean(t)
}

// Decision (ADR-0078 §3, §6): every way a stream ends releases its viewer exactly once,
// makes the id unknown and, but for the subscription's own end, publishes stopped with the
// reason: the socket closing and the 10 s without an attach are detached, the server's
// child exiting with the device gone is deviceGone, and the daemon stopping is exited. The
// source is released with its last viewer, so the fake's child, record and forward are
// gone each time (requireClean). Seen failing with the id kept in the table after the end
// (forget disabled): "N ids are still in the table".
func TestScreenStreamEnds(t *testing.T) {
	t.Run("the subscription ends", func(t *testing.T) {
		w := newWSFix(t)
		res, _ := w.watch(t)
		w.sess.Unsubscribe(res.SubscriptionID)
		w.requireGone(t, res.StreamID)
	})

	t.Run("the socket closes, once", func(t *testing.T) {
		w := newWSFix(t)
		res, sub := w.watch(t)
		h, err := w.adb.AttachScreen(res.StreamID)
		if err != nil {
			t.Fatal(err)
		}
		h.Close()
		h.Close() // a second close, and then the subscription's end, change nothing
		if se := stopped(t, sub); se.Reason != StopDetached {
			t.Errorf("reason %q, want detached", se.Reason)
		}
		w.sess.Unsubscribe(res.SubscriptionID)
		w.requireGone(t, res.StreamID)
		if _, err := h.Next(context.Background()); err == nil {
			t.Error("Next on a closed handle returned a packet")
		}
	})

	t.Run("no socket attaches", func(t *testing.T) {
		old := screenAttachBound
		screenAttachBound = 100 * time.Millisecond
		t.Cleanup(func() { screenAttachBound = old })
		w := newWSFix(t)
		res, sub := w.watch(t)
		se := stopped(t, sub)
		if se.Reason != StopDetached || !strings.Contains(se.Cause, "no stream socket attached") {
			t.Errorf("stopped %+v, want detached naming the missing socket", se)
		}
		w.requireGone(t, res.StreamID)
	})

	t.Run("an attach stops the unattached timer", func(t *testing.T) {
		old := screenAttachBound
		screenAttachBound = 100 * time.Millisecond
		t.Cleanup(func() { screenAttachBound = old })
		w := newWSFix(t)
		res, sub := w.watch(t)
		if _, err := w.adb.AttachScreen(res.StreamID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(400 * time.Millisecond)
		if e, ok := sub.TryNext(); ok {
			t.Fatalf("an attached stream ended with the timer: %+v", e)
		}
		if w.adb.LiveScreenStreams() != 1 {
			t.Errorf("%d streams, want the attached one", w.adb.LiveScreenStreams())
		}
	})

	t.Run("the device goes", func(t *testing.T) {
		w := newWSFix(t)
		res, sub := w.watch(t)
		h, err := w.adb.AttachScreen(res.StreamID)
		if err != nil {
			t.Fatal(err)
		}
		devicetest.Write(t, w.dir, "devices.out", []byte(uiList()))
		devicetest.Write(t, w.dir, uiSerial+".scrcpy.stop", []byte("3"))
		se := stopped(t, sub)
		if se.Reason != StopDeviceGone || se.Cause != "adb shell exited with status 3" {
			t.Errorf("stopped %+v, want deviceGone naming the exit status", se)
		}
		ctx, cancel := ssCtx()
		defer cancel()
		for {
			if _, err := h.Next(ctx); err != nil {
				break // the queued packets first, then the end
			}
		}
		w.requireGone(t, res.StreamID)
	})

	// The HTTP side's order: it reads until Next fails, then closes the handle at once. The
	// close must not win over the source's reason (CI saw "detached" for a device that went).
	// Seen failing with Close ending the stream at once: "reason detached, want deviceGone".
	t.Run("the device goes while the socket drains", func(t *testing.T) {
		w := newWSFix(t)
		res, sub := w.watch(t)
		h, err := w.adb.AttachScreen(res.StreamID)
		if err != nil {
			t.Fatal(err)
		}
		devicetest.Write(t, w.dir, "devices.out", []byte(uiList()))
		devicetest.Write(t, w.dir, uiSerial+".scrcpy.stop", []byte("3"))
		ctx, cancel := ssCtx()
		defer cancel()
		for {
			if _, err := h.Next(ctx); err != nil {
				break
			}
		}
		h.Close()
		if se := stopped(t, sub); se.Reason != StopDeviceGone {
			t.Errorf("reason %s, want deviceGone", se.Reason)
		}
		w.requireGone(t, res.StreamID)
	})

	t.Run("the daemon stops", func(t *testing.T) {
		w := newWSFix(t)
		res, sub := w.watch(t)
		if _, err := w.adb.AttachScreen(res.StreamID); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := ssCtx()
		defer cancel()
		w.adb.Close(ctx)
		se := stopped(t, sub)
		if se.Reason != StopExited || se.Cause != "the daemon is stopping" {
			t.Errorf("stopped %+v, want exited, the daemon is stopping", se)
		}
		w.requireGone(t, res.StreamID)
		// And nothing starts after it.
		out := w.watchOn(t, w.sess)
		if out.State == capability.StateCompleted || out.Error == nil || out.Error.Code != ErrStreamFailed {
			t.Errorf("a watch after the stop: %s %+v, want DEVICE_STREAM_FAILED", out.State, out.Error)
		}
	})
}

// Decision (ADR-0078 §3, §4): AttachScreen claims a stream id once. An id never issued is
// unknown, a second claim is refused, a closed stream's id is unknown again, and what it
// returns is a screen.Viewer whose packets are the source's own bytes (the key frame with
// the config in front, then the device's packets) and which offers one thing that reaches the
// device, Input (ADR-0117 §3: a frame of the wire, parsed and validated), and no Inject. Seen failing with the claim not recorded (attached never set): the
// second attach got a handle on the same viewer.
func TestAttachScreen(t *testing.T) {
	w := newWSFix(t)
	if _, err := w.adb.AttachScreen("nope"); !errors.Is(err, ErrScreenStreamUnknown) {
		t.Errorf("an id never issued: %v, want ErrScreenStreamUnknown", err)
	}
	res, _ := w.watch(t)
	if res.Width != 590 || res.Height != 1280 || res.Device != uiSerial || len(res.StreamID) != 22 {
		t.Errorf("result %+v, want the recording's 590x1280 and a 22-character id (128 bits in base64url)", res)
	}
	h, err := w.adb.AttachScreen(res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.adb.AttachScreen(res.StreamID); !errors.Is(err, ErrScreenStreamAttached) {
		t.Errorf("a second attach: %v, want ErrScreenStreamAttached", err)
	}
	// The handle's whole method set is the Viewer's and Input: a method added to it, Inject
	// included, would be reachable by whoever holds the handle.
	if rt := reflect.TypeOf(h); rt.NumMethod() != 3 {
		var names []string
		for i := 0; i < rt.NumMethod(); i++ {
			names = append(names, rt.Method(i).Name)
		}
		t.Errorf("the handle's methods are %v, want Close, Input and Next only", names)
	}
	_, pkts, _ := ssFixture(t)
	ctx, cancel := ssCtx()
	defer cancel()
	p, err := h.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A join starts at the GOP current when it attaches (ADR-0078 §5), which under load may
	// be a later key frame of the recording than its first: the config once, then one of
	// the recording's key frames, unchanged.
	config := pkts[0].Data
	keyed := false
	if p.Key && p.Config && bytes.HasPrefix(p.Data, config) {
		for _, k := range pkts[1:] {
			if k.Key && bytes.Equal(p.Data[len(config):], k.Data) {
				keyed = true
				break
			}
		}
	}
	if !keyed {
		t.Errorf("first packet key=%v config=%v %d bytes, want the config (%d bytes) then one of the source's key frames", p.Key, p.Config, len(p.Data), len(config))
	}
	h.Close()
	if _, err := w.adb.AttachScreen(res.StreamID); !errors.Is(err, ErrScreenStreamUnknown) {
		t.Errorf("attach after close: %v, want ErrScreenStreamUnknown", err)
	}
}

// Decision (ADR-0078 §5, §6): two streams of one device share one server, each holds one
// reference, and ending one leaves the other playing; the server ends with the last. Seen
// failing with end's once-guard removed and the reference dropped by the source's unref
// (not the once-wrapped release): the first stream's second Close dropped the other's
// reference, and the tests timed out waiting on the streams that never left.
func TestScreenStreamsShareASource(t *testing.T) {
	w := newWSFix(t)
	a, subA := w.watch(t)
	b, _ := w.watch(t)
	w.waitServers(t, 1)
	if n := len(w.callsWith(t, "app_process")); n != 1 {
		t.Fatalf("%d servers started, want 1", n)
	}
	hb, err := w.adb.AttachScreen(b.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	ha, err := w.adb.AttachScreen(a.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	ha.Close()
	ha.Close()
	stopped(t, subA)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the first stream to leave the table", func() bool { return w.adb.LiveScreenStreams() == 1 })
	if w.adb.LiveScreenSources() != 1 {
		t.Fatal("the server ended with a stream still attached")
	}
	ctx, cancel := ssCtx()
	defer cancel()
	if p, err := hb.Next(ctx); err != nil || !p.Key {
		t.Fatalf("the second stream after the first ended: %+v, %v", p, err)
	}
	hb.Close()
	w.requireGone(t, b.StreamID)
}
