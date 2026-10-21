package device

import (
	"errors"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Decision (plan m2y step 3; ADR-0117 §1, §6): the live view of a local device takes input in
// every build, since each gesture it carries is recorded, and without any test switch: the
// result says control true, a well-formed frame reaches the server's control socket as
// scrcpy's touch message, and a frame that is not the wire is still the client's error. (Step
// 2 had this off outside the input probe's build, so the product had no input path whose
// gestures were not recorded.) Seen failing with controlOn following the probe build: the
// result said control false and the socket received nothing.
func TestControlTrueForALocalDevice(t *testing.T) {
	if !controlOn {
		t.Fatal("controlOn is off by default")
	}
	w := newWSFix(t)
	res, _ := w.watch(t)
	if !res.Control {
		t.Error("the result says control false for a local device")
	}
	h, err := w.adb.AttachScreen(res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Input(frameOf(screen.TouchDown, 100, 100)); err != nil {
		t.Errorf("a well-formed frame: %v", err)
	}
	if err := h.Input([]byte{9, 9}); !errors.Is(err, screen.ErrBadControl) {
		t.Errorf("a malformed frame: %v, want ErrBadControl", err)
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the touch on the control socket", func() bool { return len(w.ln.controlBytes()) >= 32 })
	h.Close()
	w.requireGone(t, res.StreamID)
}
