package peer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/adapters/httpapi"
	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// TestRoutedEventsOutlastTheResponse: a capability that has events waiting when it answers
// (device.watchScreen's first state) loses none of them on the client, because CallRouted
// installs the route when the response is read and not after the caller has it. Seen
// failing with the route installed after the call returned (Call, then OnEvent): the first
// events were dropped as belonging to a subscription nobody routed.
func TestRoutedEventsOutlastTheResponse(t *testing.T) {
	_, _, link := pair(t)
	for round := range 25 {
		got := make(chan int, 64)
		env, stop, err := link.CallRouted(context.Background(), capability.RequestEnvelope{ID: "b", Capability: "peertest.burst"}, func(e json.RawMessage) {
			var f struct {
				Payload tick `json:"payload"`
			}
			if json.Unmarshal(e, &f) == nil {
				got <- f.Payload.N
			}
		})
		if err != nil || env.Error != nil {
			t.Fatalf("peertest.burst: %v %+v", err, env.Error)
		}
		for want := range 20 {
			select {
			case n := <-got:
				if n != want {
					t.Fatalf("round %d: event %d arrived where %d was expected", round, n, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("round %d: event %d never arrived", round, want)
			}
		}
		stop()
	}
}

// packetViewer is a stream of fixed messages that then stays open until its socket goes.
type packetViewer struct {
	packets []httpapi.ScreenPacket
	closed  chan struct{}

	mu     sync.Mutex
	frames [][]byte // the input frames Input took
}

func (v *packetViewer) Next(ctx context.Context) (httpapi.ScreenPacket, error) {
	if len(v.packets) == 0 {
		select {
		case <-ctx.Done():
		case <-v.closed:
		}
		return httpapi.ScreenPacket{}, ctx.Err()
	}
	p := v.packets[0]
	v.packets = v.packets[1:]
	return p, nil
}

func (v *packetViewer) Close() { close(v.closed) }

// Input takes what the peer's socket sends after its stream id (ADR-0117): a frame of one
// byte 9 is not the wire (the adapter's ErrInputMalformed), one byte 8 is refused, and any
// other is kept.
func (v *packetViewer) Input(frame []byte) error {
	switch {
	case bytes.Equal(frame, []byte{9}):
		return httpapi.ErrInputMalformed
	case bytes.Equal(frame, []byte{8}):
		return errors.New("input refused")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.frames = append(v.frames, append([]byte(nil), frame...))
	return nil
}

func (v *packetViewer) got() [][]byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([][]byte(nil), v.frames...)
}

// TestPeerScreenSocket is ADR-0078 §4 on the peer port: the first text frame, within the
// deadline, is {"stream": <id>}; the attach is made for the peer the certificate pins to, by
// the name the host gave it; the messages are the device's packets with the header of the
// format; an id the attach does not know is 4404 and one already attached 4409; a frame
// after the first closes the socket with 1003 and ends the stream; and a first frame that is
// not a text frame is 1003 too.
func TestPeerScreenSocket(t *testing.T) {
	peer.SetStreamFrameTimeout(300 * time.Millisecond)
	t.Cleanup(func() { peer.SetStreamFrameTimeout(5 * time.Second) })
	host, client := newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	var mu sync.Mutex
	var attachedBy []string
	viewer := &packetViewer{closed: make(chan struct{}), packets: []httpapi.ScreenPacket{
		{Key: true, Config: true, PTS: 1234, Data: []byte{0, 0, 0, 1, 0x67, 1}},
		{PTS: 1300, Data: []byte{0, 0, 0, 1, 0x41, 2}},
	}}
	host.screens = func(name, id string) (httpapi.ScreenViewer, error) {
		mu.Lock()
		attachedBy = append(attachedBy, name)
		mu.Unlock()
		switch id {
		case "good":
			return viewer, nil
		case "taken":
			return nil, httpapi.ErrScreenAttached
		}
		return nil, httpapi.ErrScreenUnknown
	}
	addr, _ := host.listen(t, "127.0.0.1:0")
	dial := func() *websocket.Conn {
		t.Helper()
		cfg := hosting.PinnedConfig(client.host.Identity(), func(fp string) bool { return fp == host.host.Identity().Fingerprint() })
		c, _, err := websocket.Dial(context.Background(), "wss://"+addr+capability.PeerScreenPath, &websocket.DialOptions{
			HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	code := func(c *websocket.Conn) websocket.StatusCode {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return websocket.CloseStatus(err)
			}
		}
	}
	write := func(c *websocket.Conn, typ websocket.MessageType, s string) {
		t.Helper()
		if err := c.Write(context.Background(), typ, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}

	for id, want := range map[string]websocket.StatusCode{"nope": 4404, "taken": 4409} {
		c := dial()
		write(c, websocket.MessageText, `{"stream":"`+id+`"}`)
		if got := code(c); got != want {
			t.Errorf("stream %q closed with %d, want %d", id, got, want)
		}
	}
	if got := code(dial()); got != 4401 {
		t.Errorf("a socket that never sent an id closed with %d, want 4401", got)
	}
	c := dial()
	write(c, websocket.MessageBinary, `{"stream":"good"}`)
	if got := code(c); got != websocket.StatusUnsupportedData {
		t.Errorf("a binary first frame closed with %d, want 1003", got)
	}

	c = dial()
	write(c, websocket.MessageText, `{"stream":"good"}`)
	for i, want := range [][]byte{
		{3, 0, 0, 0, 0, 0, 0, 0x04, 0xd2, 0, 0, 0, 1, 0x67, 1},
		{0, 0, 0, 0, 0, 0, 0, 0x05, 0x14, 0, 0, 0, 1, 0x41, 2},
	} {
		typ, data, err := c.Read(context.Background())
		if err != nil || typ != websocket.MessageBinary || !bytes.Equal(data, want) {
			t.Fatalf("message %d: %x (%v, %v); want %x", i, data, typ, err, want)
		}
	}
	write(c, websocket.MessageText, "hello")
	if got := code(c); got != websocket.StatusUnsupportedData {
		t.Errorf("a frame after the id closed with %d, want 1003", got)
	}
	select {
	case <-viewer.closed:
	case <-time.After(5 * time.Second):
		t.Error("the stream's viewer was not closed with its socket")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, n := range attachedBy {
		if n != "anna" {
			t.Errorf("an attach was made for %q; want the name the host gave the peer, anna", n)
		}
	}
	if len(attachedBy) != 3 {
		t.Errorf("attaches = %v; want three (nope, taken, good)", attachedBy)
	}
}

// TestPeerScreenSocketTakesInputFrames is ADR-0117 §3 on the peer port: after the stream id,
// the peer's binary frames reach the stream's viewer one by one, in order (the host's wiring
// parses and validates them; the adapter reads nothing of the wire), a frame the viewer calls
// malformed closes the socket with 1003 and one it refuses closes it with 1008, in each case
// only that socket and ending its stream, and a text frame is 1003. Seen failing with the
// peer endpoint dropping the frames after the id (the viewer saw none).
func TestPeerScreenSocketTakesInputFrames(t *testing.T) {
	host, client := newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	viewers := map[string]*packetViewer{}
	for _, id := range []string{"ok", "malformed", "refused", "text"} {
		viewers[id] = &packetViewer{closed: make(chan struct{})}
	}
	host.screens = func(_, id string) (httpapi.ScreenViewer, error) { return viewers[id], nil }
	addr, _ := host.listen(t, "127.0.0.1:0")
	open := func(id string) *websocket.Conn {
		t.Helper()
		cfg := hosting.PinnedConfig(client.host.Identity(), func(fp string) bool { return fp == host.host.Identity().Fingerprint() })
		c, _, err := websocket.Dial(context.Background(), "wss://"+addr+capability.PeerScreenPath, &websocket.DialOptions{
			HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		if err := c.Write(context.Background(), websocket.MessageText, []byte(`{"stream":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
		return c
	}
	send := func(c *websocket.Conn, typ websocket.MessageType, b []byte) {
		t.Helper()
		if err := c.Write(context.Background(), typ, b); err != nil {
			t.Fatal(err)
		}
	}
	code := func(c *websocket.Conn) websocket.StatusCode {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return websocket.CloseStatus(err)
			}
		}
	}
	ended := func(id string) {
		t.Helper()
		select {
		case <-viewers[id].closed:
		case <-time.After(5 * time.Second):
			t.Errorf("the viewer of %q was not closed with its socket", id)
		}
	}

	c := open("ok")
	frames := [][]byte{{1, 0, 0, 1, 0, 2}, {2, 2}, {3, 0, 1, 0, 2, 0, 0, 1, 0}}
	for _, f := range frames {
		send(c, websocket.MessageBinary, f)
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the three frames at the viewer", func() bool { return len(viewers["ok"].got()) == 3 })
	for i, f := range frames {
		if got := viewers["ok"].got()[i]; !bytes.Equal(got, f) {
			t.Errorf("frame %d reached the viewer as % x, want % x", i, got, f)
		}
	}
	c.CloseNow()
	ended("ok")

	for id, want := range map[string]websocket.StatusCode{"malformed": websocket.StatusUnsupportedData, "refused": websocket.StatusPolicyViolation} {
		c := open(id)
		send(c, websocket.MessageBinary, map[string][]byte{"malformed": {9}, "refused": {8}}[id])
		if got := code(c); got != want {
			t.Errorf("a %s frame closed the socket with %d, want %d", id, got, want)
		}
		ended(id)
	}
	c = open("text")
	send(c, websocket.MessageText, []byte("hello"))
	if got := code(c); got != websocket.StatusUnsupportedData {
		t.Errorf("a text frame closed the socket with %d, want 1003", got)
	}
	ended("text")
}

// TestAbandonedRoutedCallLeavesNoSubscription: a caller that gives up on a routed call
// while the host is still starting leaves a tombstone, and the late response that names a
// subscription is answered with events.unsubscribe by the link itself, so the host's
// subscription count returns to what it was. Seen failing without the tombstone: the
// subscription (and its drain goroutine) stayed on the host until the link closed.
func TestAbandonedRoutedCallLeavesNoSubscription(t *testing.T) {
	host, _, link := pair(t)
	base := host.bus.Subscriptions()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := link.CallRouted(ctx, capability.RequestEnvelope{ID: "s", Capability: "peertest.slowwatch"}, func(json.RawMessage) {}); err == nil {
		t.Fatal("the call answered within 50 ms; the capability is meant to take 300 ms")
	}
	time.Sleep(600 * time.Millisecond) // the host opened it at 300 ms and answered at once
	// A product claim (the relay reaping an abandoned subscription), unscaled: no level-5 run timed it.
	testtime.Eventually(t, 2*time.Second, "the host's abandoned subscription to be ended", func() bool {
		return host.bus.Subscriptions() == base
	})
}

// A listener with no video wiring answers the screen route 404, not a socket.
func TestPeerScreenWithoutWiringIs404(t *testing.T) {
	host, client := newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	addr, _ := host.listen(t, "127.0.0.1:0")
	cfg := hosting.PinnedConfig(client.host.Identity(), func(fp string) bool { return fp == host.host.Identity().Fingerprint() })
	_, resp, err := websocket.Dial(context.Background(), "wss://"+addr+capability.PeerScreenPath, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}})
	if err == nil || resp == nil || resp.StatusCode != 404 {
		t.Errorf("the screen route without wiring: %v, %v; want 404", resp, err)
	}
}
