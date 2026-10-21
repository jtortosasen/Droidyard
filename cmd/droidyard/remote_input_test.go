package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The input of a lent device's live view, the tunnel through the peer pair (plan m2y step 5;
// ADR-0117): two daemons paired over real TLS on loopback, the host's fake adb behind the
// fixture listener for the screen server, whose control socket records what reaches the
// device (screen_test.go, remote_screen_test.go). The client's /v1/screen is what a browser
// uses; the host's own UI is a second /v1/screen on the host.

// ctlKinds reads the messages of scrcpy's control socket as the recorder received them: a
// touch is 32 bytes (the action follows the type), a key 14, a scroll 21.
func ctlKinds(t *testing.T, data []byte) []string {
	t.Helper()
	var out []string
	for len(data) > 0 {
		switch {
		case data[0] == 2 && len(data) >= 32:
			out = append(out, [...]string{"down", "up", "move", "cancel"}[data[1]])
			data = data[32:]
		case data[0] == 0 && len(data) >= 14:
			out = append(out, [...]string{"keydown", "keyup"}[data[1]])
			data = data[14:]
		case data[0] == 3 && len(data) >= 21:
			out = append(out, "scroll")
			data = data[21:]
		default:
			t.Fatalf("the control socket holds % x, which is no message of scrcpy's", data)
		}
	}
	return out
}

// wireFrame is the input wire of ADR-0117 §3, spelled in the test.
func wireTouch(action byte, x, y uint16) string {
	return string([]byte{1, action, byte(x >> 8), byte(x), byte(y >> 8), byte(y)})
}

const touchDown, touchUp, touchMove, touchCancel = 0, 1, 2, 3

var hostSize = screen.Size{Width: 590, Height: 1280}

// scrcpy is the control messages the host writes for the events, against the recording's frame.
func scrcpy(t *testing.T, events ...screen.Event) []byte {
	t.Helper()
	var all []byte
	for _, ev := range events {
		b, err := screen.EncodeEvent(ev, hostSize)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
	}
	return all
}

func touchEv(action screen.TouchAction, x, y uint16) screen.Event {
	return screen.Event{Kind: screen.EventTouch, Action: action, X: x, Y: y}
}

// remoteView is the client's view of the remote phone: its subscription, the host's grant as
// the client's result reports it, and the attached /v1/screen socket.
type remoteView struct {
	sub     string
	control bool
	sock    *screenSock
}

func openRemoteView(t *testing.T, lab *screenLab, w *wsClient, reqID string) remoteView {
	t.Helper()
	w.send(reqID, "device.watchScreen", "", `{"device":"`+remotePhone+`"}`, nil)
	_, f := w.collect(reqID)
	if f.Error != nil {
		t.Fatalf("device.watchScreen %s: %+v", remotePhone, f.Error)
	}
	var r device.WatchScreenResult
	if err := json.Unmarshal(f.Result, &r); err != nil || r.StreamID == "" {
		t.Fatalf("device.watchScreen result %s: %v", f.Result, err)
	}
	sock := attachScreen(t, lab.client, lab.client.token, r.StreamID)
	sock.message("the first message of the view")
	return remoteView{sub: r.SubscriptionID, control: r.Control, sock: sock}
}

// waitControl waits until the host's control socket has received exactly want, and then that
// nothing more arrives for a moment.
func waitControl(t *testing.T, lab *screenLab, what string, want []string) {
	t.Helper()
	var last []string
	testtime.Eventually(t, testtime.Bound(15*time.Second), what, func() bool {
		last = ctlKinds(t, lab.ln.controlData())
		return fmt.Sprint(last) == fmt.Sprint(want)
	})
	time.Sleep(300 * time.Millisecond)
	if got := ctlKinds(t, lab.ln.controlData()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: the host's control socket went on to receive %v after %v", what, got, want)
	}
}

// TestRemoteInputReachesTheHostsControlSocket (ADR-0117 §1, §3, §6; plan m2y step 5): the
// host's grant reaches the client's result (control true), and the frames a browser sends on
// the client's /v1/screen arrive at the host's control socket as scrcpy's messages for the
// frame, byte for byte, through the client's daemon, the peer socket and the host's: a tap,
// a key, a wheel and a drag. Each gesture is recorded on both sides as an operation of the
// capability that replays it: on the host as the peer anna's (`client=peer peer=anna` in the
// trace), on the client as the UI's against the remote id (operation.state). Seen failing
// with the relayed source's Control refused as before this step: the result said control false
// and the host's socket received nothing.
func TestRemoteInputReachesTheHostsControlSocket(t *testing.T) {
	t.Parallel()
	lab := newScreenLab(t, 3, labOptions{})
	hostTrace, clientTrace := &lockedBuffer{}, &lockedBuffer{}
	lab.host.ops.UseTrace(func(format string, v ...any) { fmt.Fprintf(hostTrace, format+"\n", v...) })
	lab.client.ops.UseTrace(func(format string, v ...any) { fmt.Fprintf(clientTrace, format+"\n", v...) })
	activity := wsConnect(t, lab.client)
	activity.send("watch", "operation.watch", "", `{}`, nil)
	var watch struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	if f := activity.must("the operation.watch response"); json.Unmarshal(f.Result, &watch) != nil || watch.SubscriptionID == "" {
		t.Fatalf("operation.watch answered %s (error %+v)", f.Result, f.Error)
	}

	w := wsConnect(t, lab.client)
	v := openRemoteView(t, lab, w, "r1")
	if !v.control {
		t.Fatal("the client's result for a lent device says control false: the host's grant did not reach it")
	}
	frames := []string{
		wireTouch(touchDown, 0x1000, 0x2000), wireTouch(touchUp, 0x1000, 0x2000), // a tap
		"\x02\x02", // home
		"\x03\x00\x64\x00\xc8\x00\x00\x01\x00",
		wireTouch(touchDown, 0x4000, 0x4000), wireTouch(touchMove, 0x6000, 0x6000), wireTouch(touchUp, 0x6000, 0x6000), // a drag
	}
	for _, f := range frames {
		v.sock.write(websocket.MessageBinary, f)
	}
	want := scrcpy(t,
		touchEv(screen.TouchDown, 0x1000, 0x2000), touchEv(screen.TouchUp, 0x1000, 0x2000),
		screen.Event{Kind: screen.EventKey, Key: "home"},
		screen.Event{Kind: screen.EventScroll, X: 100, Y: 200, V: 256},
		touchEv(screen.TouchDown, 0x4000, 0x4000), touchEv(screen.TouchMove, 0x6000, 0x6000), touchEv(screen.TouchUp, 0x6000, 0x6000))
	testtime.Eventually(t, testtime.Bound(15*time.Second), "the seven messages on the host's control socket", func() bool {
		return len(lab.ln.controlData()) >= len(want)
	})
	if got := lab.ln.controlData(); !bytes.Equal(got, want) {
		t.Errorf("the host's control socket received % x\nwant % x (%v)", got, want, ctlKinds(t, want))
	}

	// The records: the host's trace names the peer, one line for each gesture.
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host's records of the four gestures", func() bool {
		s := hostTrace.String()
		for _, c := range []string{"device.tap", "device.pressKey", "device.scroll", "device.swipe"} {
			if !strings.Contains(s, "client=peer capability="+c+" ") {
				return false
			}
		}
		return true
	})
	for _, line := range strings.Split(hostTrace.String(), "\n") {
		for _, c := range []string{"device.tap", "device.pressKey", "device.scroll", "device.swipe"} {
			if strings.Contains(line, "capability="+c+" ") && (!strings.Contains(line, "client=peer") || !strings.Contains(line, " peer=anna")) {
				t.Errorf("the host's record of a gesture of the peer: %q; want client=peer and peer=anna", line)
			}
		}
	}
	// The client's: the UI's own, against the remote id.
	var states []capability.OperationState
	for len(states) < 8 {
		fr := activity.must("the client's recorded gestures")
		if fr.Event == nil || fr.SubscriptionID != watch.SubscriptionID {
			continue
		}
		var pl capability.OperationState
		if err := json.Unmarshal(fr.Event.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(pl.Capability, "device.") && pl.Capability != "device.watchScreen" {
			states = append(states, pl)
		}
	}
	for _, s := range states {
		if s.Client != capability.ClientUI || s.Target == nil || s.Target.Device != remotePhone {
			t.Errorf("the client recorded %+v; want a gesture of the UI on %s", s, remotePhone)
		}
	}
	for _, c := range []string{"device.tap", "device.pressKey", "device.scroll", "device.swipe"} {
		if !strings.Contains(clientTrace.String(), "client=ui capability="+c+" ") {
			t.Errorf("the client's trace has no record of %s as the UI's:\n%s", c, clientTrace.String())
		}
	}
	if strings.Contains(clientTrace.String(), "client=peer") {
		t.Errorf("the client's trace names a peer:\n%s", clientTrace.String())
	}
}

// TestRemoteFingerCancelledWhenTheClientViewEnds (ADR-0117 §5): a drag in progress when the
// client's view ends reaches the device as a cancel and never an up: the host's control
// socket receives the down and an ACTION_CANCEL, and no lift of any other kind follows. Seen
// failing with the cancel written as an up: the host's device was told the drag was dropped.
func TestRemoteFingerCancelledWhenTheClientViewEnds(t *testing.T) {
	t.Parallel()
	lab := newScreenLab(t, 3, labOptions{})
	w := wsConnect(t, lab.client)
	v := openRemoteView(t, lab, w, "r1")
	if !v.control {
		t.Fatal("no control for the lent device's view")
	}
	v.sock.write(websocket.MessageBinary, wireTouch(touchDown, 0x2000, 0x3000))
	waitControl(t, lab, "the down", []string{"down"})
	v.sock.c.Close(websocket.StatusNormalClosure, "")
	waitControl(t, lab, "the cancel after the view ended", []string{"down", "cancel"})
	msg := lab.ln.controlData()[32:]
	if want := scrcpy(t, touchEv(screen.TouchCancel, 0x2000, 0x3000)); !bytes.Equal(msg, want) {
		t.Errorf("the cancel is % x, want % x (the finger's last point)", msg, want)
	}
}

// setHostLend sets who the host lends phone1 to (the lab's lend list, its first row) and
// rewrites its configuration.
func (l *screenLab) setHostLend(t *testing.T, peers ...string) {
	t.Helper()
	lend := l.hostConf["hosting"].(map[string]any)["lend"].([]map[string]any)
	lend[0] = map[string]any{"device": phone1, "to": peers}
	configAt(t, l.host.state, jsonObject(t, l.hostConf))
}

// bobIdentity is the fingerprint of a second peer the host pairs, so a test can take anna out
// of what it lends or pairs without leaving the host with none.
func bobIdentity(t *testing.T) labPeer {
	t.Helper()
	id, _, err := hosting.LoadIdentity(shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return labPeer{"bob", id.Fingerprint()}
}

// TestRemoteViewEndsWhenTheDeviceIsNoLongerLent (ADR-0117 §2): the host's lease sweep ends
// the peer's stream when the phone is no longer lent to it, within the 2 s check and a close:
// the client's view stops `deviceGone`, the finger the view had down is cancelled and never
// lifted, and nothing the client sends afterwards reaches the device. Seen failing with the
// host's endLease doing nothing: no stopped event came for the 30 s the test waits. (The
// stream that ended accepts nothing is TestInputAfterTheStreamEndedSendsNothing's decision;
// here the sockets are closed by the end, so the late frames have nowhere to go.)
func TestRemoteViewEndsWhenTheDeviceIsNoLongerLent(t *testing.T) {
	t.Parallel()
	lab := newScreenLab(t, 3, labOptions{extraPeers: []labPeer{bobIdentity(t)}})
	w := wsConnect(t, lab.client)
	v := openRemoteView(t, lab, w, "r1")
	if !v.control {
		t.Fatal("no control for the lent device's view")
	}
	v.sock.write(websocket.MessageBinary, wireTouch(touchDown, 0x2000, 0x3000))
	waitControl(t, lab, "the down", []string{"down"})

	start := time.Now()
	lab.setHostLend(t, "bob")
	reason, _ := w.stoppedOn(v.sub, remotePhone)
	took := time.Since(start)
	t.Logf("the client's view ended %v after the phone was un-lent", took)
	if reason != "deviceGone" || took > device.LeaseSweepEvery+2*time.Second {
		t.Errorf("the view stopped as %q after %v; want deviceGone within the 2 s check (and a close)", reason, took)
	}
	waitControl(t, lab, "the cancel of the finger", []string{"down", "cancel"})
	// Whatever the client's socket still takes reaches no one.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	v.sock.c.Write(ctx, websocket.MessageBinary, []byte(wireTouch(touchUp, 0x2000, 0x3000)))
	v.sock.c.Write(ctx, websocket.MessageBinary, []byte(wireTouch(touchDown, 0x1000, 0x1000)))
	time.Sleep(500 * time.Millisecond)
	if got := ctlKinds(t, lab.ln.controlData()); fmt.Sprint(got) != "[down cancel]" {
		t.Errorf("the host's control socket received %v after the lease ended; want only the down and its cancel", got)
	}
	waitFor(t, "the host's stream to be gone", func() bool { return lab.host.adb.LiveScreenStreams() == 0 })
}

// TestHostUIPreemptsTheRemoteFinger (ADR-0117 §4, ADR-0094 §5): the host's own user touching
// the lent device while the peer's drag is down takes the finger, with an ACTION_CANCEL for
// the peer's gesture written first; the rest of the peer's gesture is dropped, and the host's
// own gesture completes. Seen failing with the peer's stream classed as a local holder: the
// host's down was dropped behind the peer's finger.
func TestHostUIPreemptsTheRemoteFinger(t *testing.T) {
	t.Parallel()
	lab := newScreenLab(t, 3, labOptions{})
	w := wsConnect(t, lab.client)
	v := openRemoteView(t, lab, w, "r1")
	hw := wsConnect(t, lab.host)
	hw.send("h1", "device.watchScreen", "", `{"device":"`+phone1+`"}`, nil)
	_, f := hw.collect("h1")
	var r device.WatchScreenResult
	if f.Error != nil || json.Unmarshal(f.Result, &r) != nil || r.StreamID == "" || !r.Control {
		t.Fatalf("the host's own device.watchScreen: %+v %s", f.Error, f.Result)
	}
	ui := attachScreen(t, lab.host, lab.host.token, r.StreamID)
	ui.message("the first message of the host's view")

	v.sock.write(websocket.MessageBinary, wireTouch(touchDown, 0x1000, 0x2000))
	waitControl(t, lab, "the peer's down", []string{"down"})
	ui.write(websocket.MessageBinary, wireTouch(touchDown, 0x8000, 0x8000))
	waitControl(t, lab, "the preemption", []string{"down", "cancel", "down"})
	v.sock.write(websocket.MessageBinary, wireTouch(touchMove, 0x1100, 0x2100))
	v.sock.write(websocket.MessageBinary, wireTouch(touchUp, 0x1100, 0x2100))
	ui.write(websocket.MessageBinary, wireTouch(touchUp, 0x8000, 0x8000))
	waitControl(t, lab, "the host's gesture, the peer's rest dropped", []string{"down", "cancel", "down", "up"})
	want := scrcpy(t, touchEv(screen.TouchDown, 0x1000, 0x2000), touchEv(screen.TouchCancel, 0x1000, 0x2000),
		touchEv(screen.TouchDown, 0x8000, 0x8000), touchEv(screen.TouchUp, 0x8000, 0x8000))
	if got := lab.ln.controlData(); !bytes.Equal(got, want) {
		t.Errorf("the host's control socket received % x\nwant % x", got, want)
	}
}

// TestUnpairedPeerViewEndsDeviceGone (R6, ADR-0098 §3): the host unpairing the client closes
// the client's session and its video socket with the revocation code; the client's view ends
// `stopped deviceGone` within the sweep, not `failed`. (The unit-level reading of the code,
// at once and before the grace, is TestRelayedViewReadsARevokedPairingAsDeviceGone in
// internal/domain/device, which is the one seen failing with the code not read; here both
// daemons run, and the link's own loss, which the same sweep causes, also ends the view
// deviceGone, so this test passes without the code: it holds the end to end promise, not R6's
// line.)
func TestUnpairedPeerViewEndsDeviceGone(t *testing.T) {
	t.Parallel()
	bob := bobIdentity(t)
	lab := newScreenLab(t, 3, labOptions{extraPeers: []labPeer{bob}})
	w := wsConnect(t, lab.client)
	v := openRemoteView(t, lab, w, "r1")
	start := time.Now()
	conf := lab.hostConf["hosting"].(map[string]any)
	conf["peers"] = []map[string]string{{"name": bob.name, "fingerprint": bob.fingerprint}}
	configAt(t, lab.host.state, jsonObject(t, lab.hostConf))
	reason, cause := w.stoppedOn(v.sub, remotePhone)
	t.Logf("the client's view ended %v after the host unpaired it: %s (%s)", time.Since(start), reason, cause)
	if reason != "deviceGone" {
		t.Errorf("the view of an unpaired peer stopped as %q (%q); want deviceGone", reason, cause)
	}
	waitFor(t, "both daemons' streams to be gone", func() bool {
		return lab.client.adb.LiveScreenStreams() == 0 && lab.host.adb.LiveScreenStreams() == 0
	})
}
