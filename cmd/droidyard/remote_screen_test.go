package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The live view of a remote device end to end (plan m2t step 3; ADR-0100 §3, §5; ADR-0078):
// two daemons paired over real TLS on loopback, the host's fake adb behind m2p's fixture
// listener for the screen server (screen_test.go), and the client's /v1/screen and
// /v1/ws as a browser uses them.

// scOffsets is the byte offset just after each packet of a recorded video socket.
func scOffsets(t *testing.T, data []byte) []int {
	t.Helper()
	cr := &countingReader{r: bytes.NewReader(data)}
	var offs []int
	if err := screen.Demux(cr, func(screen.Packet) error { offs = append(offs, cr.n); return nil }); err != nil {
		t.Fatal(err)
	}
	return offs
}

// secureDumpFile is a recorded `dumpsys window`, with the toggle's window focused and
// secure ("on") or not ("off").
func secureDumpFile(t *testing.T, state string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "domain", "device", "screen", "testdata", "dumpsys-window-api31-"+state+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// screenLab is a lab whose host lends phone1 with the fixture listener behind its screen
// server. The stream stops after the packet at index stopAt (a key frame), so the state a
// viewer joins is deterministic: the tail is released with ln.Release().
type screenLab struct {
	*remoteLab
	ln   *scListener
	pkts []screen.Packet
	keys []int // the indexes of the key frames in pkts
}

func newScreenLab(t *testing.T, stopAtKey int, o labOptions) *screenLab {
	t.Helper()
	data, pkts, _ := scRecording(t)
	offs := scOffsets(t, data)
	var keys []int
	for i, p := range pkts {
		if p.Key {
			keys = append(keys, i)
		}
	}
	end := offs[keys[stopAtKey-1]]
	ln := startSCListener(t, data[:offs[1]], data[offs[1]:end])
	if o.hostFiles == nil {
		o.hostFiles = map[string]string{}
	}
	o.hostFiles[phone1+".forwardscrcpy.out"] = ln.Path + "\n"
	o.hostFiles[phone1+".dumpsyswindow.out"] = secureDumpFile(t, "off")
	o.hostProcesses = true
	return &screenLab{remoteLab: newRemoteLabWith(t, o), ln: ln, pkts: pkts, keys: keys}
}

// watchRemote calls device.watchScreen on the remote id and returns its result.
func (w *wsClient) watchRemote(reqID, id string) (sub, stream string, width, height int) {
	w.t.Helper()
	w.send(reqID, "device.watchScreen", "", `{"device":"`+id+`"}`, nil)
	_, f := w.collect(reqID)
	if f.Error != nil {
		w.t.Fatalf("device.watchScreen %s: %+v", id, f.Error)
	}
	var r struct {
		SubscriptionID string `json:"subscriptionId"`
		Device         string `json:"device"`
		StreamID       string `json:"streamId"`
		Width          int    `json:"width"`
		Height         int    `json:"height"`
	}
	if err := json.Unmarshal(f.Result, &r); err != nil || r.StreamID == "" || r.Device != id {
		w.t.Fatalf("device.watchScreen result %s: %v; want device %s", f.Result, err, id)
	}
	if !untrusted(f.ResponseEnvelope) {
		w.t.Errorf("a remote device's watchScreen is not marked untrusted: %+v", f.Meta)
	}
	return r.SubscriptionID, r.StreamID, r.Width, r.Height
}

// stateFrame is the next frame of the session, which must be a device.screen state event
// of one of subs; it returns the subscription and the secure flag.
func (w *wsClient) stateFrame(what string, subs ...string) (sub string, secure bool) {
	w.t.Helper()
	f, ok := w.next(testtime.Bound(30 * time.Second))
	if !ok {
		w.t.Fatalf("%s: no frame (the connection ended: %v)", what, w.endedErr())
	}
	if f.Event == nil || f.Event.Topic != "device.screen" || f.Event.Type != "state" {
		w.t.Fatalf("%s: a frame that is not a state event: %+v", what, f)
	}
	var se struct {
		Device string `json:"device"`
		Secure bool   `json:"secure"`
	}
	if err := json.Unmarshal(f.Event.Payload, &se); err != nil || se.Device != remotePhone {
		w.t.Fatalf("%s: state payload %s: %v; want device %s", what, f.Event.Payload, err, remotePhone)
	}
	for _, s := range subs {
		if s == f.SubscriptionID {
			return s, se.Secure
		}
	}
	w.t.Fatalf("%s: a state event of subscription %s, want one of %v", what, f.SubscriptionID, subs)
	return
}

// stoppedOn waits for the stopped event of sub, naming device, and returns its reason.
func (w *wsClient) stoppedOn(sub, device string) (reason, cause string) {
	w.t.Helper()
	for {
		f, ok := w.next(testtime.Bound(30 * time.Second))
		if !ok {
			w.t.Fatalf("a stopped event: no frame (the connection ended: %v)", w.endedErr())
		}
		if f.Event != nil && f.Event.Type == "state" {
			continue
		}
		if f.Event == nil || f.SubscriptionID != sub || f.Event.Topic != "device.screen" || f.Event.Type != "stopped" {
			w.t.Fatalf("a frame that is not a device.screen stopped event of %s: %+v", sub, f)
		}
		var se struct {
			Device string `json:"device"`
			Reason string `json:"reason"`
			Cause  string `json:"cause"`
		}
		if err := json.Unmarshal(f.Event.Payload, &se); err != nil || se.Device != device {
			w.t.Fatalf("stopped payload %s: %v; want device %s", f.Event.Payload, err, device)
		}
		return se.Reason, se.Cause
	}
}

// messageIs requires the socket's next message to be the packet p, alone, as the device sent
// it (flags key, no config), and returns nothing else.
func messageIs(t *testing.T, s *screenSock, what string, p screen.Packet) {
	t.Helper()
	flags, pts, payload := s.message(what)
	var want byte
	if p.Key {
		want = 1
	}
	if flags != want || pts != uint64(p.PTS) || !bytes.Equal(payload, p.Data) {
		t.Fatalf("%s: flags %d pts %d, %d bytes; want flags %d pts %d, the device's %d bytes", what, flags, pts, len(payload), want, p.PTS, len(p.Data))
	}
}

// quiet requires the socket to send nothing for d.
func quiet(t *testing.T, s *screenSock, what string, d time.Duration) {
	t.Helper()
	select {
	case m := <-s.msgs:
		t.Fatalf("%s: an unexpected message of %d bytes", what, len(m.data))
	case <-s.done:
		t.Fatalf("%s: the socket ended: %v", what, s.err)
	case <-time.After(d):
	}
}

const tapParams = `{"device":"` + remotePhone + `","x":0.5,"y":0.5}`

// TestRelayedScreen: the client's Live view of a remote device (ADR-0100 §3, §5).
//
//   - The bytes: a client /v1/screen viewer's payloads equal the fixture's packets, the
//     first key frame with its config once, byte for byte. Seen failing with the host's
//     adapter re-chunking each packet into two messages (the first message's payload was
//     half the merged key frame, the next was not the device's next packet).
//   - A second client viewer joining after the host's third key frame gets the config once,
//     with that key frame. Seen failing without screen.Split (ParseScreenFrame handing the
//     merged packet to the client's Hub whole): the first viewer's first message was never
//     a key frame with the config and the second viewer waited for ever.
//   - The host's secure state, one state event per change, to each subscription. Seen
//     failing with the relayed source ignoring the host's events (a state from a local
//     check has nothing to read here: this daemon's own adb knows nothing of the host's
//     window): no state event ever arrived.
//   - A client device.tap on the remote id reaches the host's control socket while a view is
//     open; with none open it answers the host's DEVICE_NO_STREAM and sends nothing.
//   - Closing the client's last viewer releases the host's stream, server and forward. No
//     document gives this a number (ADR-0078 §6: the source ends with its last viewer); the 2 s
//     of plan m2t step 3's check was a level-4 reading of 66 ms, and the release runs through
//     the host's fake adb as processes, which a loaded host starts seconds late (1 in 5 runs
//     at a load of 5 took over 2 s). The test holds that the release comes, within a hang
//     guard, and logs how long it took.
func TestRelayedScreen(t *testing.T) {
	t.Parallel()
	lab := newScreenLab(t, 3, labOptions{})
	pkts, keys := lab.pkts, lab.keys
	w := wsConnect(t, lab.client)

	// No view yet: the host answers DEVICE_NO_STREAM and nothing reaches its control socket.
	env := lab.on(t, "device.tap", tapParams)
	if env.Error == nil || env.Error.Code != device.ErrNoStream {
		t.Fatalf("device.tap with no view open: %+v; want DEVICE_NO_STREAM", env.Error)
	}
	if lab.ln.controlBytes() != 0 {
		t.Fatalf("a tap with no view open sent %d bytes to the control socket", lab.ln.controlBytes())
	}

	// 1. The bytes. The first viewer joins at the first key frame, the config in front.
	hostSubs := lab.host.bus.Subscriptions() // the host's own, before any view
	sub1, stream1, width, height := w.watchRemote("r1", remotePhone)
	if width != 590 || height != 1280 {
		t.Errorf("size %dx%d, want the recording's 590x1280", width, height)
	}
	if sub1 == "" {
		t.Fatal("no subscription")
	}
	sock1 := attachScreen(t, lab.client, lab.client.token, stream1)
	flags, pts, payload := sock1.message("the first message")
	wantFirst := append(append([]byte(nil), pkts[0].Data...), pkts[1].Data...)
	if flags != 3 || pts != uint64(pkts[1].PTS) || !bytes.Equal(payload, wantFirst) {
		t.Fatalf("first message: flags %d, pts %d, %d bytes; want key+config, pts %d, the config and key frame in %d bytes",
			flags, pts, len(payload), pkts[1].PTS, len(wantFirst))
	}
	if s, secure := w.stateFrame("the host's first check", sub1); s != sub1 || secure {
		t.Fatalf("the first state: %s secure %v, want %s false", s, secure, sub1)
	}
	lab.ln.Release()
	for i := 2; i <= keys[2]; i++ {
		messageIs(t, sock1, "packet "+strconv.Itoa(i), pkts[i])
	}

	// 2. A second viewer after the host's third key frame: its own subscription and stream,
	// the same relayed source and the same single stream on the host.
	sub2, stream2, _, _ := w.watchRemote("r2", remotePhone)
	if s, secure := w.stateFrame("the second viewer's state", sub2); s != sub2 || secure {
		t.Fatalf("the second viewer's state: %s secure %v", s, secure)
	}
	sock2 := attachScreen(t, lab.client, lab.client.token, stream2)
	flags, pts, payload = sock2.message("the second viewer's first message")
	wantJoin := append(append([]byte(nil), pkts[0].Data...), pkts[keys[2]].Data...)
	if flags != 3 || pts != uint64(pkts[keys[2]].PTS) || !bytes.Equal(payload, wantJoin) {
		t.Fatalf("second viewer's first message: flags %d, pts %d, %d bytes; want key+config, pts %d, the config once and the third key frame in %d bytes",
			flags, pts, len(payload), pkts[keys[2]].PTS, len(wantJoin))
	}
	quiet(t, sock2, "the second viewer after its join", 300*time.Millisecond)
	if n, m := lab.client.adb.LiveRelayedScreens(), lab.host.adb.LiveScreenStreams(); n != 1 || m != 1 {
		t.Errorf("relayed sources on the client %d, streams on the host %d; want 1 and 1 (one relayed source for the device, shared)", n, m)
	}
	if n := lab.host.adb.LiveScreenSources(); n != 1 {
		t.Errorf("screen servers on the host: %d, want 1", n)
	}

	// 3. The host's secure state, one event per change per subscription, nothing on a repeat.
	flip := func(state string, want bool) {
		t.Helper()
		devicetest.Write(t, lab.hostDir, phone1+".dumpsyswindow.out", []byte(secureDumpFile(t, state)))
		seen := map[string]bool{}
		for range 2 {
			s, secure := w.stateFrame("secure "+state, sub1, sub2)
			if secure != want || seen[s] {
				t.Fatalf("secure %s: subscription %s secure %v (seen %v); want one event each, secure %v", state, s, secure, seen, want)
			}
			seen[s] = true
		}
	}
	flip("on", true)
	flip("off", false)
	if f, ok := w.next(2500 * time.Millisecond); ok {
		t.Fatalf("a frame with the host's answer unchanged: %+v", f)
	}

	// 4. A tap on the remote id lands on the host's control socket.
	env = lab.on(t, "device.tap", tapParams)
	var tr device.ScreenInputResult
	if env.Error != nil || json.Unmarshal(env.Result, &tr) != nil || !tr.Delivered {
		t.Fatalf("device.tap with a view open: %+v %s", env.Error, env.Result)
	}
	waitFor(t, "the tap on the host's control socket", func() bool { return lab.ln.controlBytes() > 0 })

	// 5. Closing the last viewer releases the host.
	sock1.c.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "the first client stream to end", func() bool { return lab.client.adb.LiveScreenStreams() == 1 })
	if lab.client.adb.LiveRelayedScreens() != 1 || lab.host.adb.LiveScreenStreams() != 1 {
		t.Fatal("the relayed source ended with a viewer still open")
	}
	if hostSubs >= lab.host.bus.Subscriptions() {
		t.Fatalf("the host holds %d subscriptions with a view open, %d before: the check below has nothing to see", lab.host.bus.Subscriptions(), hostSubs)
	}
	start := time.Now()
	sock2.c.Close(websocket.StatusNormalClosure, "")
	guard := testtime.Bound(10 * time.Second)
	for lab.host.adb.LiveScreenStreams()+lab.host.adb.LiveScreenSources()+lab.client.adb.LiveRelayedScreens()+len(devicetest.Forwards(t, lab.hostDir))+lab.host.bus.Subscriptions()-hostSubs != 0 {
		if time.Since(start) > guard {
			t.Fatalf("after the last viewer closed, still held after %v: host streams %d, servers %d, client relayed sources %d, forwards %d, host subscriptions %d (was %d)",
				guard, lab.host.adb.LiveScreenStreams(), lab.host.adb.LiveScreenSources(), lab.client.adb.LiveRelayedScreens(), len(devicetest.Forwards(t, lab.hostDir)), lab.host.bus.Subscriptions(), hostSubs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("the host's stream, server and forward were released %v after the last viewer closed", time.Since(start))
	before := lab.ln.controlBytes()
	env = lab.on(t, "device.tap", tapParams)
	if env.Error == nil || env.Error.Code != device.ErrNoStream || lab.ln.controlBytes() != before {
		t.Errorf("device.tap after the view closed: %+v (control bytes %d, was %d); want DEVICE_NO_STREAM and nothing sent", env.Error, lab.ln.controlBytes(), before)
	}
}

// TestRelayedScreenLinkCut: the link to the host cut while a view is open ends the client's
// stream with deviceGone (ADR-0100 §3), and its viewer's socket with 4410.
func TestRelayedScreenLinkCut(t *testing.T) {
	t.Parallel()
	lab := newScreenLab(t, 3, labOptions{})
	w := wsConnect(t, lab.client)
	sub, stream, _, _ := w.watchRemote("r1", remotePhone)
	sock := attachScreen(t, lab.client, lab.client.token, stream)
	sock.message("a message before the link is cut")

	lab.stopHost()
	if reason, _ := w.stoppedOn(sub, remotePhone); reason != "deviceGone" {
		t.Errorf("a cut link gave reason %q, want deviceGone", reason)
	}
	if code := sock.closedWith(testtime.Bound(30 * time.Second)); code != 4410 {
		t.Errorf("the viewer's socket of a cut link ended with %d, want 4410", code)
	}
	waitFor(t, "the client's relayed source to be gone", func() bool {
		return lab.client.adb.LiveRelayedScreens() == 0 && lab.client.adb.LiveScreenStreams() == 0
	})
}

// TestRelayedScreenHostEnds: when the host's own stream ends by itself, the client's view
// ends with the same reason (ADR-0100 §3, ADR-0078 §2): the screen server exiting while the
// device stays listed is exited, and the device leaving the host is deviceGone. Seen failing
// with hostEnd answering failed for every reason.
func TestRelayedScreenHostEnds(t *testing.T) {
	t.Parallel()
	lab := newScreenLab(t, 3, labOptions{})
	w := wsConnect(t, lab.client)
	stop := filepath.Join(lab.hostDir, phone1+".scrcpy.stop")

	watch := func(id string) (string, *screenSock) {
		sub, stream, _, _ := w.watchRemote(id, remotePhone)
		sock := attachScreen(t, lab.client, lab.client.token, stream)
		sock.message("the first message of " + id)
		return sub, sock
	}
	ended := func(sub string, sock *screenSock) (reason, cause string) {
		reason, cause = w.stoppedOn(sub, remotePhone)
		if code := sock.closedWith(testtime.Bound(30 * time.Second)); code != 4410 {
			t.Errorf("the viewer's socket ended with %d, want 4410", code)
		}
		waitFor(t, "the client's relayed source and the host's stream to be gone", func() bool {
			return lab.client.adb.LiveRelayedScreens() == 0 && lab.host.adb.LiveScreenSources() == 0
		})
		return
	}

	// 1. The server on the host exits with the device still listed.
	sub, sock := watch("r1")
	devicetest.Write(t, lab.hostDir, phone1+".scrcpy.stop", []byte("3"))
	if reason, cause := ended(sub, sock); reason != "exited" || cause == "" {
		t.Errorf("the host's server exited: reason %q (%q), want exited with a cause", reason, cause)
	}

	// 2. The device leaves the host.
	if err := os.Remove(stop); err != nil {
		t.Fatal(err)
	}
	sub, sock = watch("r2")
	gone := "List of devices attached\n" + hostEmulator + " device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"
	devicetest.Write(t, lab.hostDir, "devices.out", []byte(gone))
	devicetest.Write(t, lab.hostDir, phone1+".scrcpy.stop", []byte("3"))
	if reason, _ := ended(sub, sock); reason != "deviceGone" {
		t.Errorf("the device left the host: reason %q, want deviceGone", reason)
	}
}

// TestRelayedScreenAbandonedStart: the user closes the view while the host is still starting
// the screen server (the fake's push takes 1.5 s). The client's call is cancelled, the
// relayed source is abandoned, and the host's late answer names a subscription that nobody
// holds: the link ends it, so the host's subscription count returns to its baseline and its
// stream and server are released. Seen failing without the link's tombstone: the host kept
// the subscription (and the stream, until its 10 s attach timer) after the cancel.
//
// The test follows the host's own operation, not a clock: the cancel goes once the request is
// on the host, and the release is awaited once the host's start has ended, however long a
// loaded host took to push (a sleep and a poll of 5.8 s were passed by a start of 3.6 to 4.4 s
// plus the cancel's own round trip).
func TestRelayedScreenAbandonedStart(t *testing.T) {
	t.Parallel()
	lab := newScreenLab(t, 3, labOptions{hostFiles: map[string]string{phone1 + ".pushscrcpy.delay": "1500"}})
	w := wsConnect(t, lab.client)
	base := lab.host.bus.Subscriptions()
	hostStarting := func() bool {
		for _, op := range lab.host.ops.Active() {
			if op.Capability == "device.watchScreen" {
				return true
			}
		}
		return false
	}
	zero := int64(0)
	w.send("r1", "device.watchScreen", "", `{"device":"`+remotePhone+`"}`, &zero)
	_, f := w.collect("r1")
	if f.Operation == nil || f.Operation.State.Terminal() {
		t.Fatalf("the start answered at once: %+v", f)
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the request to be on the host, its push under way", hostStarting)
	w.send("c1", "operation.cancel", "", `{"operationId":"`+f.Operation.ID+`"}`, nil)
	w.collect("c1")
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the client's relayed source to be abandoned", func() bool { return lab.client.adb.LiveRelayedScreens() == 0 })

	testtime.Eventually(t, testtime.Bound(30*time.Second), "the host to finish its start and answer", func() bool { return !hostStarting() })
	// Held below screenAttachBound (10 s): a late stream the link's tombstone failed to end
	// would otherwise be released by its attach timer inside the wait, and pass.
	for deadline := time.Now().Add(min(testtime.Bound(3*time.Second), 9*time.Second)); lab.host.bus.Subscriptions()-base+lab.host.adb.LiveScreenStreams()+lab.host.adb.LiveScreenSources() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the host still holds the abandoned view after its start ended: subscriptions %d (was %d), streams %d, servers %d",
				lab.host.bus.Subscriptions(), base, lab.host.adb.LiveScreenStreams(), lab.host.adb.LiveScreenSources())
		}
	}
}

// peerScreenDial opens /peer/v1/screen as the peer whose identity is id and sends stream as
// its first frame.
func peerScreenDial(t *testing.T, id *hosting.Identity, addr, hostFingerprint, stream string) *screenSock {
	t.Helper()
	cfg := hosting.PinnedConfig(id, func(fp string) bool { return fp == hostFingerprint })
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "wss://"+addr+capability.PeerScreenPath, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("dialling %s: %v", capability.PeerScreenPath, err)
	}
	c.SetReadLimit(32 << 20)
	s := &screenSock{t: t, c: c, msgs: make(chan screenMsg, 4096), done: make(chan struct{})}
	t.Cleanup(func() { c.CloseNow() })
	go func() {
		defer close(s.done)
		for {
			typ, data, err := c.Read(context.Background())
			if err != nil {
				s.err = err
				return
			}
			s.msgs <- screenMsg{typ, data}
		}
	}()
	s.write(websocket.MessageText, `{"stream":"`+stream+`"}`)
	return s
}

// TestPeerScreenOwnership: a stream id belongs to the peer whose device.watchScreen issued
// it (ADR-0100 §3). Another peer attaching it is 4404 as for an id never issued, though
// the id is valid and unattached; the owner attaches it. A peer removed from hosting.peers
// loses its screen socket within the sweep. Seen failing with the id looked up alone, as
// AttachScreen does (the attach by the other peer succeeded and streamed the owner's view).
func TestPeerScreenOwnership(t *testing.T) {
	t.Parallel()
	bobState := shortDir(t)
	bobID, _, err := hosting.LoadIdentity(bobState)
	if err != nil {
		t.Fatal(err)
	}
	lab := newScreenLab(t, 3, labOptions{extraPeers: []labPeer{{"bob", bobID.Fingerprint()}}})
	addr := lab.host.host.Health().Hosting.Listen
	hostFP := lab.host.host.Identity().Fingerprint()

	// bob's own link, calling the host as a peer would.
	configAt(t, bobState, `{"remotes":[{"name":"qa","address":"`+addr+`","fingerprint":"`+hostFP+`"}]}`)
	bob := clientHost(t, bobState)
	bob.Start()
	t.Cleanup(bob.Stop)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "bob's link to come up", func() bool { return bob.Remote("qa").Connected() })

	watch := func() (stream string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, stop, err := bob.Remote("qa").CallRouted(ctx, capability.RequestEnvelope{
			Capability: "device.watchScreen", Params: json.RawMessage(`{"device":"` + phone1 + `"}`)}, func(json.RawMessage) {})
		if err != nil || resp.Error != nil {
			t.Fatalf("bob's device.watchScreen: %v %+v", err, resp.Error)
		}
		t.Cleanup(stop)
		var r device.WatchScreenResult
		if err := json.Unmarshal(resp.Result, &r); err != nil || r.StreamID == "" {
			t.Fatalf("bob's device.watchScreen result %s: %v", resp.Result, err)
		}
		return r.StreamID
	}

	// anna holds bob's stream id: unknown to her, though it is valid and unattached.
	annaID := lab.client.host.Identity()
	stream := watch()
	if code := peerScreenDial(t, annaID, addr, hostFP, stream).closedWith(testtime.Bound(30 * time.Second)); code != 4404 {
		t.Errorf("anna attaching bob's stream closed with %d, want 4404", code)
	}
	// ... which bob still attaches, and which streams the device's bytes.
	bobSock := peerScreenDial(t, bobID, addr, hostFP, stream)
	flags, _, payload := bobSock.message("bob's first message")
	if flags != 3 || !bytes.Equal(payload, append(append([]byte(nil), lab.pkts[0].Data...), lab.pkts[1].Data...)) {
		t.Errorf("bob's first message: flags %d, %d bytes; want key+config of the first key frame", flags, len(payload))
	}
	// A second attach of the same id is 4409, from the owner.
	if code := peerScreenDial(t, bobID, addr, hostFP, stream).closedWith(testtime.Bound(30 * time.Second)); code != 4409 {
		t.Errorf("a second attach by the owner closed with %d, want 4409", code)
	}
	// The browser's endpoint never serves a peer's stream: its own client and token do not own it.
	if code := attachScreen(t, lab.host, lab.host.token, stream).closedWith(testtime.Bound(30 * time.Second)); code != 4404 {
		t.Errorf("the host's UI endpoint attaching bob's stream closed with %d, want 4404", code)
	}

	// Revoking bob closes his screen socket within the sweep (2 s) and a little.
	peers := lab.hostConf["hosting"].(map[string]any)
	peers["peers"] = []map[string]string{{"name": "anna", "fingerprint": annaID.Fingerprint()}}
	start := time.Now()
	configAt(t, lab.host.state, jsonObject(t, lab.hostConf))
	code := bobSock.closedWith(5 * time.Second)
	if code != capability.PeerCloseRevoked {
		t.Errorf("the revoked peer's screen socket closed with %d, want %d", code, capability.PeerCloseRevoked)
	}
	t.Logf("the revoked peer's screen socket closed %v after the configuration changed", time.Since(start))
	waitFor(t, "the host's stream of the revoked peer to end", func() bool { return lab.host.adb.LiveScreenStreams() == 0 })
}
