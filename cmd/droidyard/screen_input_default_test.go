//go:build !droidyard_inputprobe

package main

import (
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/testtime"
)

// TestScreenEndpointTakesNoInputWithoutTheProbe (plan m2y step 2): in a build without the
// input probe the shipped wiring refuses a well-formed input frame, as a policy violation
// (1008, not the 1003 of a frame that is not the wire), and the source's control socket has
// received no byte: the product has no unrecorded input path until step 3 records the
// gestures. Seen failing with controlOn true by default: the socket stayed open and the
// control listener received the touch.
func TestScreenEndpointTakesNoInputWithoutTheProbe(t *testing.T) {
	t.Parallel()
	data, _, keyEnd := scRecording(t)
	d := startSCDaemon(t, data[:keyEnd], data[keyEnd:])
	w := wsConnect(t, d.inProcess)
	_, stream, _, _ := w.watchScreen("i1")
	s := attachScreen(t, d.inProcess, d.token, stream)
	s.message("the first message of the attached socket")
	s.write(websocket.MessageBinary, "\x01\x00\x00\x10\x00\x20") // a touch down
	if code := s.closedWith(testtime.Bound(30 * time.Second)); code != websocket.StatusPolicyViolation {
		t.Errorf("a well-formed input frame in a build without the probe: closed with %d, want 1008", code)
	}
	time.Sleep(300 * time.Millisecond)
	if n := d.ln.controlBytes(); n != 0 {
		t.Errorf("the control socket received %d bytes", n)
	}
}
