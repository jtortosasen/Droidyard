package peer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/adapters/httpapi"
	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// The peer listener and the link, in one process and over a real TLS socket on loopback:
// the host side is adapters/peer on a registry of its own, the client side is
// hosting.Link. TestPeerLink in cmd/droidyard runs the same between two daemons.

// config is a policy that tests edit while the listener and the links read it, as the
// file is edited by hand.
type config struct {
	mu       sync.Mutex
	hosting  policy.HostingPolicy
	remotes  []policy.Remote
	unusable bool
}

func (c *config) Hosting() (policy.HostingPolicy, *capability.Error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unusable {
		return policy.HostingPolicy{}, capability.NewError(capability.ErrWorkspaceUntrusted, "unusable")
	}
	h := c.hosting
	h.Peers = append([]policy.HostPeer(nil), h.Peers...)
	return h, nil
}

func (c *config) Remotes() ([]policy.Remote, *capability.Error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]policy.Remote(nil), c.remotes...), nil
}

func (c *config) setPeers(p ...policy.HostPeer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hosting.Peers = p
}

// safeBuffer is a log both sides write while the test reads it.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type none struct{}

type tick struct {
	N int `json:"n"`
}

type watching struct {
	SubscriptionID string `json:"subscriptionId" jsonschema:"required"`
}

// side is one daemon of the pair.
type side struct {
	cfg  *config
	host *hosting.Host
	reg  *capability.Registry
	ops  *capability.Operations
	bus  *event.Bus
	log  *safeBuffer
}

func newSide(t *testing.T, api string) *side {
	t.Helper()
	s := &side{cfg: &config{}, log: &safeBuffer{}}
	s.host = hosting.New(s.cfg, t.TempDir(), api, s.log)
	if err := s.host.Init(); err != nil {
		t.Fatal(err)
	}
	reg := capability.NewRegistry()
	s.reg = reg
	s.ops = capability.NewOperations(reg)
	s.bus = event.New(event.Options{})
	s.ops.UsePublisher(s.bus)
	s.ops.UseStatePublisher(s.bus)
	s.ops.RegisterCapabilities()
	event.Register(reg, s.ops.Active)
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	reg.MustRegister(capability.Descriptor{Name: "peertest.echo", Kind: capability.Query, Scope: capability.Global, Lendable: true,
		Description: "Lent.", Params: none{}, Result: tick{},
		Handler: func(context.Context, *capability.Call) (any, error) { return tick{N: 7}, nil }})
	reg.MustRegister(capability.Descriptor{Name: "peertest.secret", Kind: capability.Query, Scope: capability.Global,
		Description: "Not lent.", Params: none{}, Result: tick{},
		Handler: func(context.Context, *capability.Call) (any, error) { return tick{N: 1}, nil }})
	reg.MustRegister(capability.Descriptor{Name: "peertest.hold", Kind: capability.Query, Scope: capability.Global, Lendable: true,
		Description: "Lent, held.", Params: none{}, Result: tick{},
		Handler: func(ctx context.Context, _ *capability.Call) (any, error) {
			select {
			case <-hold:
			case <-ctx.Done():
			}
			return tick{}, ctx.Err()
		}})
	reg.MustRegister(capability.Descriptor{Name: "peertest.watch", Kind: capability.Command, Scope: capability.Global, Lendable: true,
		Statefulness: capability.Streaming, Description: "Lent, streams.", Params: none{}, Result: watching{},
		Handler: func(_ context.Context, call *capability.Call) (any, error) {
			f, ok := call.Session.(capability.Follower)
			if !ok {
				return nil, capability.NewError(capability.ErrSessionRequired, "no session")
			}
			id, err := f.Follow([]string{"peertest.tick"})
			return watching{SubscriptionID: id}, err
		}})
	return s
}

// listen starts the host's listener on an ephemeral loopback port and returns the address
// it bound, once health says so.
func (s *side) listen(t *testing.T, addr string) (bound string, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	srv := peer.New(peer.Options{Host: s.host, Sessions: httpapi.NewPeerSessions(s.reg, s.ops, s.bus), Listen: addr, API: "0.0.0", Log: s.log})
	go func() { defer close(done); srv.Run(ctx) }()
	stop = func() { cancel(); <-done }
	t.Cleanup(stop)
	return waitFor(t, "the listener to report", func() string { return s.host.Health().Hosting.Listen }), stop
}

func waitFor(t *testing.T, what string, f func() string) string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if v := f(); v != "" {
			return v
		}
	}
	t.Fatalf("no %s within 10 s", what)
	return ""
}

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

// pair makes a host that lends to a client named anna, and a client linked to it.
func pair(t *testing.T) (host, client *side, link *hosting.Link) {
	t.Helper()
	host, client = newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	addr, _ := host.listen(t, "127.0.0.1:0")
	client.cfg.remotes = []policy.Remote{{Name: "qa-lab", Address: addr, Fingerprint: host.host.Identity().Fingerprint()}}
	client.host.Start()
	t.Cleanup(client.host.Stop)
	eventually(t, "the link to come up", 10*time.Second, func() bool { return client.host.Remote("qa-lab").Connected() })
	return host, client, client.host.Remote("qa-lab")
}

func call(t *testing.T, l *hosting.Link, capName, params string) capability.ResponseEnvelope {
	t.Helper()
	req := capability.RequestEnvelope{ID: "mine", Capability: capName}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	env, err := l.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("%s over the link: %v", capName, err)
	}
	if env.ID != "mine" {
		t.Errorf("%s: the answer's id is %q; want the caller's", capName, env.ID)
	}
	return env
}

// TestPeerSessionRoundTrip: a paired client calls what the host lends and is refused what
// it does not, before anything is recorded; the host's health and the client's say the link
// is up; a stream's events reach the client by their subscription; a peer's operation is
// the host's own, with client peer and the name the host gave it, not one the peer sent.
func TestPeerSessionRoundTrip(t *testing.T) {
	host, client, link := pair(t)

	if env := call(t, link, "peertest.echo", ""); env.Error != nil || !strings.Contains(string(env.Result), `"n":7`) {
		t.Errorf("a lent capability: %+v", env)
	}
	if env := call(t, link, "peertest.secret", ""); env.Error == nil || env.Error.Code != capability.ErrNotAllowedHere {
		t.Errorf("a capability that is not lent: %+v; want REQUEST_NOT_ALLOWED_HERE", env)
	}
	if env := call(t, link, "operation.wait", `{"operationId":"op-0000000000000000"}`); env.Error == nil || env.Error.Code != capability.ErrOperationNotFound {
		t.Errorf("operation.wait of an unknown id: %+v; want OPERATION_NOT_FOUND", env)
	}

	h := host.host.Health()
	if len(h.Hosting.Peers) != 1 || h.Hosting.Peers[0].Name != "anna" || !h.Hosting.Peers[0].Connected {
		t.Errorf("the host's health: %+v; want anna connected", h.Hosting)
	}
	if r := client.host.Health().Remotes; len(r) != 1 || !r[0].Connected || r[0].API != "0.0.0" || r[0].Error != "" {
		t.Errorf("the client's health: %+v; want qa-lab connected at API 0.0.0", r)
	}

	// The host sees a peer's operation as a peer's, named by its configuration.
	go link.Call(context.Background(), capability.RequestEnvelope{ID: "h", Capability: "peertest.hold"})
	var seen []capability.ActiveOperation
	eventually(t, "the host to record the held operation", 5*time.Second, func() bool {
		seen = host.ops.Active()
		for _, a := range seen {
			if a.Capability == "peertest.hold" {
				return true
			}
		}
		return false
	})
	for _, a := range seen {
		if a.Capability == "peertest.hold" && (a.Client != capability.ClientPeer || a.Session == nil || a.Session.Name != "anna") {
			t.Errorf("the peer's operation is %+v; want client peer and session name anna", a)
		}
	}

	// Events: a subscription the peer's call opened delivers to the function routed for it.
	env := call(t, link, "peertest.watch", "")
	var w watching
	if err := json.Unmarshal(env.Result, &w); err != nil || w.SubscriptionID == "" {
		t.Fatalf("peertest.watch: %+v", env)
	}
	got := make(chan json.RawMessage, 4)
	stop := link.OnEvent(w.SubscriptionID, func(e json.RawMessage) { got <- e })
	host.bus.Publish("peertest.tick", "tick", "", "", tick{N: 3})
	select {
	case e := <-got:
		if !strings.Contains(string(e), `"n":3`) {
			t.Errorf("the event is %s; want the tick", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event reached the client")
	}
	stop()
	host.bus.Publish("peertest.tick", "tick", "", "", tick{N: 4})
	select {
	case e := <-got:
		t.Errorf("an event reached a stopped route: %s", e)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRevokedPeerLosesItsLink is ADR-0098 §3 for a socket that is open and quiet: once the
// peer is out of hosting.peers, the sweep closes it within sweepEvery, the host's health
// stops counting it, and the client's redial is refused at the handshake with a message
// that says why. A configuration that becomes unusable revokes every peer the same way.
func TestRevokedPeerLosesItsLink(t *testing.T) {
	host, client, link := pair(t)
	host.cfg.setPeers()
	start := time.Now()
	eventually(t, "the revoked peer's link to close", 5*time.Second, func() bool { return !link.Connected() })
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the link closed %s after the revocation; want within a sweep (2 s) and a close", took)
	}
	if got := host.host.Health().Hosting.Peers; len(got) != 0 {
		t.Errorf("the host still lists %+v", got)
	}
	eventually(t, "a refused redial", 10*time.Second, func() bool {
		r := client.host.Health().Remotes
		return len(r) == 1 && !r[0].Connected && strings.Contains(r[0].Error, "does not admit")
	})
	if !strings.Contains(host.log.String(), "refused a handshake") {
		t.Errorf("the host did not log the refusal:\n%s", host.log.String())
	}
}

// TestBindFailureIsRetried is ADR-0098 §3: an address that cannot be bound leaves no
// listener and an error that names the bind, the daemon runs, and the bind is tried again
// until it works.
func TestBindFailureIsRetried(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	peer.SetRetryEvery(100 * time.Millisecond)
	t.Cleanup(func() { peer.SetRetryEvery(30 * time.Second) })

	s := newSide(t, "0.0.0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	srv := peer.New(peer.Options{Host: s.host, Sessions: httpapi.NewPeerSessions(s.reg, s.ops, s.bus), Listen: taken.Addr().String(), API: "0.0.0", Log: s.log})
	go func() { defer close(done); srv.Run(ctx) }()
	defer func() { cancel(); <-done }()

	msg := waitFor(t, "the bind error", func() string { return s.host.Health().Hosting.Error })
	if !strings.Contains(msg, "hosting.listen "+taken.Addr().String()) || s.host.Health().Hosting.Listen != "" {
		t.Errorf("the bind failed with %q (listen %q); want an error that names the bind and no listener", msg, s.host.Health().Hosting.Listen)
	}
	taken.Close()
	bound := waitFor(t, "the retry to bind", func() string { return s.host.Health().Hosting.Listen })
	if bound == "" || s.host.Health().Hosting.Error != "" {
		t.Errorf("after the address was free: listen %q, error %q; want bound and no error", bound, s.host.Health().Hosting.Error)
	}
}

// TestTheHelloIsRequired: a pinned client that opens the socket must say hello within the
// deadline and speak this peer protocol; each way to fail closes the socket with the code
// that says which, and no request is served on it (ADR-0098 §4).
func TestTheHelloIsRequired(t *testing.T) {
	peer.SetHelloTimeout(300 * time.Millisecond)
	t.Cleanup(func() { peer.SetHelloTimeout(5 * time.Second) })
	host, client := newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	addr, _ := host.listen(t, "127.0.0.1:0")

	dial := func() *websocket.Conn {
		t.Helper()
		cfg := hosting.PinnedConfig(client.host.Identity(), func(fp string) bool { return fp == host.host.Identity().Fingerprint() })
		c, _, err := websocket.Dial(context.Background(), "wss://"+addr+capability.PeerPath, &websocket.DialOptions{
			HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	closeCode := func(c *websocket.Conn) websocket.StatusCode {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, err := c.Read(ctx)
		return websocket.CloseStatus(err)
	}
	send := func(c *websocket.Conn, frame string) {
		t.Helper()
		if err := c.Write(context.Background(), websocket.MessageText, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}

	c := dial()
	send(c, `{"hello":{"peerProtocol":2,"api":"9.9.9"}}`)
	if got := closeCode(c); got != capability.PeerCloseProtocol {
		t.Errorf("a hello of another peerProtocol closed with %d; want %d", got, capability.PeerCloseProtocol)
	}
	c = dial()
	send(c, `{"id":"1","capability":"operation.wait","params":{"operationId":"x"}}`)
	if got := closeCode(c); got != capability.PeerCloseProtocol {
		t.Errorf("a request in place of the hello closed with %d; want %d", got, capability.PeerCloseProtocol)
	}
	c = dial()
	if got := closeCode(c); got != capability.PeerCloseNoHello {
		t.Errorf("a socket that never said hello closed with %d; want %d", got, capability.PeerCloseNoHello)
	}
	if h := host.host.Health().Hosting.Peers; len(h) != 1 || h[0].Connected {
		t.Errorf("a socket that never completed its hello counts as connected: %+v", h)
	}

	// A different API version is only a warning: the session starts and the host logs it.
	c = dial()
	send(c, `{"hello":{"peerProtocol":1,"api":"9.9.9"}}`)
	_, data, err := c.Read(context.Background())
	if err != nil || !strings.Contains(string(data), `"peerProtocol":1`) {
		t.Fatalf("a hello of another API version: %s, %v; want the host's hello", data, err)
	}
	if !strings.Contains(host.log.String(), "runs API 9.9.9") {
		t.Errorf("the host did not log the API version difference:\n%s", host.log.String())
	}
}
