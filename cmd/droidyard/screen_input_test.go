package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// TestScreenEndpointInputReachesTheDevice (plan m2y steps 2 and 3, the default build): through
// the shipped wiring a binary touch frame after the auth frame reaches the server's control
// socket as scrcpy's 32-byte touch message, the socket stays open, the tap it makes is
// recorded as a device.tap operation of the UI's client that a watcher of operation.state
// sees run and complete, and a frame that is not the wire closes the socket with 1003
// without writing a byte more. Seen failing with the product's input off unless the probe
// build: the socket was closed 1008 and the control socket received nothing.
func TestScreenEndpointInputReachesTheDevice(t *testing.T) {
	t.Parallel()
	data, _, keyEnd := scRecording(t)
	d := startSCDaemon(t, data[:keyEnd], data[keyEnd:])
	w := wsConnect(t, d.inProcess)
	activity := wsConnect(t, d.inProcess)
	activity.send("watch", "operation.watch", "", `{}`, nil)
	var watch struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	if f := activity.must("the operation.watch response"); json.Unmarshal(f.Result, &watch) != nil || watch.SubscriptionID == "" {
		t.Fatalf("operation.watch answered %s (error %+v)", f.Result, f.Error)
	}
	_, stream, _, _ := w.watchScreen("i1")
	s := attachScreen(t, d.inProcess, d.token, stream)
	s.message("the first message of the attached socket")
	s.write(websocket.MessageBinary, "\x01\x00\x00\x10\x00\x20") // a touch down
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the touch on the control socket", func() bool { return d.ln.controlBytes() == 32 })
	s.write(websocket.MessageBinary, "\x01\x01\x00\x10\x00\x20") // and its up
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the up on the control socket", func() bool { return d.ln.controlBytes() == 64 })

	var states []capability.OperationState
	for len(states) < 2 {
		fr := activity.must("the events of the recorded tap")
		if fr.Event == nil || fr.SubscriptionID != watch.SubscriptionID {
			continue
		}
		var pl capability.OperationState
		if err := json.Unmarshal(fr.Event.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		if pl.Capability == "device.tap" {
			states = append(states, pl)
		}
	}
	if states[0].State != capability.StateRunning || states[1].State != capability.StateCompleted ||
		states[1].Client != capability.ClientUI || states[1].Target == nil || states[1].Target.Device != scSerial {
		t.Errorf("operation.watch saw %+v then %+v; want a device.tap of the UI on %s, running then completed", states[0], states[1], scSerial)
	}

	s.write(websocket.MessageBinary, "\x09")
	if code := s.closedWith(testtime.Bound(30 * time.Second)); code != websocket.StatusUnsupportedData {
		t.Errorf("a frame that is not the wire: closed with %d, want 1003", code)
	}
	time.Sleep(300 * time.Millisecond)
	if n := d.ln.controlBytes(); n != 64 {
		t.Errorf("the control socket received %d bytes after a finished tap and a bad frame, want the tap's 64", n)
	}
}
