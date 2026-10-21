//go:build droidyard_inputprobe

package main

import (
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/testtime"
)

// TestScreenEndpointInputReachesTheDevice (plan m2y step 2, the probe build): through the
// shipped wiring a binary touch frame after the auth frame reaches the server's control
// socket as scrcpy's 32-byte touch message, the socket stays open, and a frame that is not
// the wire closes it with 1003 without writing a byte more.
func TestScreenEndpointInputReachesTheDevice(t *testing.T) {
	t.Parallel()
	data, _, keyEnd := scRecording(t)
	d := startSCDaemon(t, data[:keyEnd], data[keyEnd:])
	w := wsConnect(t, d.inProcess)
	_, stream, _, _ := w.watchScreen("i1")
	s := attachScreen(t, d.inProcess, d.token, stream)
	s.message("the first message of the attached socket")
	s.write(websocket.MessageBinary, "\x01\x00\x00\x10\x00\x20") // a touch down
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the touch on the control socket", func() bool { return d.ln.controlBytes() == 32 })
	s.write(websocket.MessageBinary, "\x09")
	if code := s.closedWith(testtime.Bound(30 * time.Second)); code != websocket.StatusUnsupportedData {
		t.Errorf("a frame that is not the wire: closed with %d, want 1003", code)
	}
	// The socket's end lifts the finger with a cancel, 32 bytes more, and nothing else.
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the cancel on the control socket", func() bool { return d.ln.controlBytes() == 64 })
}
