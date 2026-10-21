//go:build !droidyard_inputprobe

package device

import (
	"errors"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// Decision (plan m2y step 2): in a build without the input probe the live view takes no
// input, so the product never has an input path whose gestures are not recorded (step 3
// records them and turns it on). The result says control false, a well-formed frame is
// refused (not read as malformed), and the server's control socket receives not one byte.
// Seen failing with controlOn true by default: the result said control true and the
// socket received the down.
func TestControlFalseWithoutTheProbeBuild(t *testing.T) {
	w := newWSFix(t)
	res, _ := w.watch(t)
	if res.Control {
		t.Error("the result says control true in a build without the probe")
	}
	h, err := w.adb.AttachScreen(res.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Input(frameOf(screen.TouchDown, 100, 100)); !errors.Is(err, errInputOff) {
		t.Errorf("a well-formed frame: %v, want errInputOff", err)
	}
	if err := h.Input([]byte{9, 9}); !errors.Is(err, screen.ErrBadControl) {
		t.Errorf("a malformed frame: %v, want ErrBadControl", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := w.ln.controlBytes(); len(got) != 0 {
		t.Errorf("the control socket received % x", got)
	}
	h.Close()
	w.requireGone(t, res.StreamID)
}
