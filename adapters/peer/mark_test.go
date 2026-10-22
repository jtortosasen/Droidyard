package peer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// markLog is the fast marks a host was told for anna, in order: one per hello and one per
// mark frame.
type markLog struct {
	mu   sync.Mutex
	seen []bool
}

func (m *markLog) set(_ string, fast bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, fast)
}

func (m *markLog) get() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]bool(nil), m.seen...)
}

func sameMarks(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// markPair is a host of the given API version that lends to a client named anna, and the
// client linked to it with its remote marked fast (the hello says true). The host records the
// marks in order.
func markPair(t *testing.T, hostAPI string) (host, client *side, log *markLog, remote *policy.Remote, link *hosting.Link) {
	t.Helper()
	host, client = newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.api = hostAPI
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	log = &markLog{}
	host.setFast = func(peer string, fast bool) {
		log.set(peer, fast)
		host.host.SetPeerFast(peer, fast)
	}
	addr, _ := host.listen(t, "127.0.0.1:0")
	remote = &policy.Remote{Name: "qa-lab", Address: addr, Fingerprint: host.host.Identity().Fingerprint(), Fast: true}
	client.cfg.remotes = []policy.Remote{*remote}
	client.host.Start()
	t.Cleanup(client.host.Stop)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the link to come up with the mark", func() bool {
		return client.host.Remote("qa-lab").Connected() && sameMarks(log.get(), []bool{true})
	})
	return host, client, log, remote, client.host.Remote("qa-lab")
}

// setMark changes the client's configured mark and reconciles, as `droidyard remote fast` and
// the 2 s pass do.
func (c *side) setMark(remote *policy.Remote, fast bool) {
	remote.Fast = fast
	c.cfg.mu.Lock()
	c.cfg.remotes = []policy.Remote{*remote}
	c.cfg.mu.Unlock()
	c.host.Reconcile()
}

func linkUps(s *side) int { return strings.Count(s.log.String(), "peer anna: link up") }

// Decision (ADR-0123 §1): a changed mark travels in the open session to a host whose hello
// names API 0.36.0: the host applies it, and the session is the same one (the link was never
// down, the host saw one hello), so the lease, a recording and the other streams are kept.
// Seen failing with the change dropping the connection as before (update closing it): the
// link's Down channel closed and the host counted a second hello.
func TestAMarkChangeTravelsInTheSession(t *testing.T) {
	host, client, log, remote, link := markPair(t, "0.36.0")
	down := link.Down()

	client.setMark(remote, false)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to learn the mark was lifted", func() bool {
		return sameMarks(log.get(), []bool{true, false})
	})
	select {
	case <-down:
		t.Fatal("the link was lost: the mark change closed the session instead of travelling in it")
	default:
	}
	if n := linkUps(host); n != 1 {
		t.Errorf("the host counted %d hellos, want 1", n)
	}
	if host.host.PeerFast("anna") {
		t.Error("the host still reads the peer as fast")
	}
}

// Decision (ADR-0123 §4, ADR-0118 §3): a host whose hello names a version older than 0.36.0
// gets no mark frame, which it would not know: the change reconnects as it did, and the second
// hello carries the mark. Seen failing with the version gate removed (a frame to every host):
// the 0.35.0 host never reconnected and the second hello never came.
func TestAnOlderHostIsReconnectedAsToday(t *testing.T) {
	host, client, log, remote, link := markPair(t, "0.35.0")
	down := link.Down()

	client.setMark(remote, false)
	testtime.Eventually(t, testtime.Bound(15*time.Second), "the second hello, with the lifted mark", func() bool {
		return sameMarks(log.get(), []bool{true, false}) && linkUps(host) == 2
	})
	select {
	case <-down:
	default:
		t.Error("the first connection was not dropped")
	}
}

// rawPeer is a paired client's socket to the host with the hello exchanged, driven by hand.
func rawPeer(t *testing.T, host, client *side, addr string, fast bool) *websocket.Conn {
	t.Helper()
	cfg := hosting.PinnedConfig(client.host.Identity(), func(fp string) bool { return fp == host.host.Identity().Fingerprint() })
	c, _, err := websocket.Dial(context.Background(), "wss://"+addr+capability.PeerPath, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	hello, _ := json.Marshal(capability.PeerHelloFrame{Hello: &capability.PeerHello{PeerProtocol: capability.PeerProtocol, API: "0.36.0", Fast: fast}})
	rawSend(t, c, string(hello))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err != nil {
		t.Fatalf("the host's hello: %v", err)
	}
	return c
}

func rawSend(t *testing.T, c *websocket.Conn, frame string) {
	t.Helper()
	if err := c.Write(context.Background(), websocket.MessageText, []byte(frame)); err != nil {
		t.Fatal(err)
	}
}

// rawHost is a host of API 0.36.0 with a client paired and its address, and the marks it
// was told; applying a mark to true takes 200 ms.
func rawHost(t *testing.T) (host, client *side, addr string, log *markLog) {
	t.Helper()
	host, client = newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.api = "0.36.0"
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	log = &markLog{}
	host.setFast = func(peer string, fast bool) {
		if fast {
			time.Sleep(200 * time.Millisecond) // a slow application, which the next frame must not overtake
		}
		log.set(peer, fast)
		host.host.SetPeerFast(peer, fast)
	}
	addr, _ = host.listen(t, "127.0.0.1:0")
	return host, client, addr, log
}

// Decision (ADR-0123 §1): the host applies a mark frame in its read loop, before it reads the
// next frame, so a request the client sends right behind the frame, on the same socket, sees
// the new mark (a live view it starts is started with the preset the mark says). Applying the
// mark takes 200 ms here. Seen failing with the frame served on a goroutine of its own, as a
// request is: the request read the old mark.
func TestAMarkFrameIsAppliedBeforeTheNextFrame(t *testing.T) {
	host, client, addr, _ := rawHost(t)
	c := rawPeer(t, host, client, addr, false)

	rawSend(t, c, `{"mark":{"fast":true}}`)
	rawSend(t, c, `{"id":"q1","capability":"peertest.mark"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for the answer: %v", err)
		}
		var env capability.ResponseEnvelope
		if json.Unmarshal(data, &env) != nil || env.ID != "q1" {
			t.Fatalf("a frame that is not the answer (a mark gets none): %s", data)
		}
		if env.Error != nil || !strings.Contains(string(env.Result), `"n":1`) {
			t.Fatalf("the request behind the mark frame read %s %+v; want the new mark (n 1)", env.Result, env.Error)
		}
		return
	}
}

// Decision (ADR-0123 §1): changes that follow each other quickly leave the host with the last
// value, whichever frames were written, and none for a value it already has (the strict
// version, with the first write held, is hosting's). Three flips in a row from true end on
// false.
func TestTwoQuickChangesLeaveTheLast(t *testing.T) {
	host, client, log, remote, _ := markPair(t, "0.36.0")
	client.setMark(remote, false)
	client.setMark(remote, true)
	client.setMark(remote, false)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to end on the last mark", func() bool {
		seen := log.get()
		return !seen[len(seen)-1] && !host.host.PeerFast("anna")
	})
	time.Sleep(300 * time.Millisecond)
	if seen := log.get(); seen[len(seen)-1] || host.host.PeerFast("anna") {
		t.Errorf("the host ended on %v (reads fast %v); want false last", seen, host.host.PeerFast("anna"))
	}
}

// Decision (ADR-0123 §1): a mark frame whose mark is not an object with a boolean fast is a
// protocol error: the session closes 1003, as a binary frame does, and nothing is applied.
// Seen failing with a missing fast read as false: {"mark":{}} and {"mark":null} were applied
// and the session stayed open.
func TestAMalformedMarkClosesTheSession(t *testing.T) {
	for _, frame := range []string{`{"mark":{"fast":"yes"}}`, `{"mark":{}}`, `{"mark":null}`, `{"mark":5}`} {
		host, client, addr, log := rawHost(t)
		c := rawPeer(t, host, client, addr, false)
		before := len(log.get())
		rawSend(t, c, frame)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _, err := c.Read(ctx)
		cancel()
		if got := websocket.CloseStatus(err); got != websocket.StatusUnsupportedData {
			t.Errorf("%s closed with %d (%v); want 1003", frame, got, err)
		}
		if n := len(log.get()); n != before {
			t.Errorf("%s was applied", frame)
		}
	}
}

// Decision (ADR-0123 §1, ADR-0098 §3): the mark frame passes the check a request passes: a
// peer that is no longer paired changes nothing. Seen failing with the check left out: the
// unpaired peer's frame was applied.
func TestAMarkFrameOfAnUnpairedPeerIsNotApplied(t *testing.T) {
	host, client, addr, log := rawHost(t)
	c := rawPeer(t, host, client, addr, false)
	before := len(log.get())
	host.cfg.setPeers()
	rawSend(t, c, `{"mark":{"fast":true}}`)
	time.Sleep(500 * time.Millisecond) // longer than the 200 ms an applied mark would take
	if n := len(log.get()); n != before || host.host.PeerFast("anna") {
		t.Errorf("the mark of a peer no longer paired was applied: %v", log.get())
	}
}
