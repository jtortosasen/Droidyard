package hosting

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/httpapi"
	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// The live configuration of ADR-0109 §3 as the host sees it: a Host over a real policy file
// (so the file can be edited under it and the port written to it), a real peer server on
// loopback, and the links of a client Host that follow the client's file.

type echo struct {
	N int `json:"n"`
}

type none struct{}

// daemonAt is the peer state of a daemon whose state root is state, reading the configuration
// file there, the way `serve` builds it.
func daemonAt(t *testing.T, state string) (*Host, *policy.Policy, *lockedLog) {
	t.Helper()
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	pol := policy.New(env)
	log := &lockedLog{}
	h := New(pol, state, "0.0.0", log)
	t.Cleanup(h.Uploads().Close)
	if err := h.Init(); err != nil {
		t.Fatal(err)
	}
	return h, pol, log
}

func writeConfig(t *testing.T, state, body string) {
	t.Helper()
	dir := filepath.Join(state, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// lendingHost is a host whose peer server answers peertest.echo and peertest.hold (held until
// release is closed; started gets a value when a call reaches it), and which pairs the
// client with the given fingerprint as anna.
type lendingHost struct {
	host    *Host
	srv     *peer.Server
	started chan struct{}
	release chan struct{}
}

func startLendingHost(t *testing.T, state, clientFP string) *lendingHost {
	t.Helper()
	h, _, log := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"peers":[{"name":"anna","fingerprint":"`+clientFP+`"}]}}`)
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	bus := event.New(event.Options{})
	ops.UsePublisher(bus)
	ops.UseStatePublisher(bus)
	ops.RegisterCapabilities()
	event.Register(reg, ops.Active)
	lh := &lendingHost{host: h, started: make(chan struct{}, 4), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-lh.release:
		default:
			close(lh.release)
		}
	})
	reg.MustRegister(capability.Descriptor{Name: "peertest.echo", Kind: capability.Query, Scope: capability.Global, Lendable: true,
		Description: "Lent.", Params: none{}, Result: echo{},
		Handler: func(context.Context, *capability.Call) (any, error) { return echo{N: 7}, nil }})
	reg.MustRegister(capability.Descriptor{Name: "peertest.hold", Kind: capability.Query, Scope: capability.Global, Lendable: true,
		Description: "Lent, held.", Params: none{}, Result: echo{},
		Handler: func(ctx context.Context, _ *capability.Call) (any, error) {
			lh.started <- struct{}{}
			select {
			case <-lh.release:
			case <-ctx.Done():
			}
			return echo{N: 9}, nil
		}})
	lh.srv = peer.New(peer.Options{Host: h, Sessions: httpapi.NewPeerSessions(reg, ops, bus), API: "0.0.0", Log: log})
	t.Cleanup(lh.srv.Close)
	return lh
}

// clientAt is a client Host on its own state root with the remotes of body, started.
func clientAt(t *testing.T, state, remotes string) *Host {
	t.Helper()
	h, _, _ := daemonAt(t, state)
	writeConfig(t, state, remotes)
	return h
}

func remoteJSON(addr, fp, addresses string) string {
	extra := ""
	if addresses != "" {
		extra = `,"addresses":` + addresses
	}
	return `{"remotes":[{"name":"qa-lab","address":"` + addr + `","fingerprint":"` + fp + `"` + extra + `}]}`
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

// closedAddr is a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// TestRemoteAddressesUpdatedInPlace is ADR-0109 §3: a remote whose entry gains addresses keeps
// its link, its connection and the call in flight on it; only the next dial uses the new
// list. Seen failing with a changed entry closed and started again (the shape for a changed
// fingerprint, applied to any change): the link object was replaced and the held call
// failed with the link lost.
func TestRemoteAddressesUpdatedInPlace(t *testing.T) {
	clientState := t.TempDir()
	cid, _, err := LoadIdentity(clientState)
	if err != nil {
		t.Fatal(err)
	}
	lh := startLendingHost(t, t.TempDir(), cid.Fingerprint())
	lh.srv.Reconcile([]netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")})
	addr := lh.host.Health().Hosting.Listen
	fp := lh.host.Identity().Fingerprint()

	c := clientAt(t, clientState, remoteJSON(addr, fp, ""))
	c.Start()
	t.Cleanup(c.Stop)
	waitFor(t, "the link to come up", 10*time.Second, func() bool { return c.Remote("qa-lab").Connected() })
	link := c.Remote("qa-lab")

	type answer struct {
		env capability.ResponseEnvelope
		err error
	}
	got := make(chan answer, 1)
	go func() {
		env, err := link.Call(context.Background(), capability.RequestEnvelope{ID: "h", Capability: "peertest.hold"})
		got <- answer{env, err}
	}()
	select {
	case <-lh.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the held call never reached the host")
	}

	writeConfig(t, clientState, remoteJSON(addr, fp, `[{"address":"`+addr+`","kind":"lan"},{"address":"100.64.0.18:7318","kind":"vpn"}]`))
	c.Reconcile()
	if now := c.Remote("qa-lab"); now != link {
		t.Fatal("the link was replaced by a change of addresses; want it updated in place")
	}
	if !link.Connected() {
		t.Error("the link went down when its addresses changed")
	}
	if cands := link.candidates(); len(cands) != 2 || cands[0] != "100.64.0.18:7318" && cands[0] != addr {
		t.Errorf("candidates after the update = %v; want the remote's address and the tagged one", cands)
	}
	close(lh.release)
	select {
	case a := <-got:
		if a.err != nil || a.env.Error != nil || !strings.Contains(string(a.env.Result), `"n":9`) {
			t.Errorf("the call in flight across the update: %+v, %v; want its answer", a.env, a.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call in flight never answered")
	}
}

// TestRemoteAddressesAreDialledInOrder is ADR-0109 §4: a remote whose own address is dead
// links through the live one in its list, and among live ones a lan address inside the
// client's own subnets is tried before a vpn one, the client's subnets being what the
// interface lister says. Seen failing with the list ignored (only address dialled): the
// first link never came up; with the tags ignored (the list's order kept): the vpn address
// answered where the lan one in the client's subnet was wanted.
func TestRemoteAddressesAreDialledInOrder(t *testing.T) {
	both := func(t *testing.T, state string) (lh *lendingHost, a1, a2 string) {
		t.Helper()
		id, _, err := LoadIdentity(state)
		if err != nil {
			t.Fatal(err)
		}
		lh = startLendingHost(t, t.TempDir(), id.Fingerprint())
		a1, a2 = closedAddr(t), closedAddr(t)
		lh.srv.Reconcile([]netip.AddrPort{netip.MustParseAddrPort(a1), netip.MustParseAddrPort(a2)})
		return lh, a1, a2
	}

	t.Run("a dead address and a live one in the list", func(t *testing.T) {
		state := t.TempDir()
		lh, live, _ := both(t, state)
		c := clientAt(t, state, remoteJSON(closedAddr(t), lh.host.Identity().Fingerprint(), `[{"address":"`+live+`","kind":"lan"}]`))
		c.Start()
		t.Cleanup(c.Stop)
		waitFor(t, "a remote with a dead address to link through its list", 10*time.Second, func() bool { return c.Remote("qa-lab").Connected() })
		if got := c.Remote("qa-lab").current(); got != live {
			t.Errorf("the link answered at %s; want the listed %s", got, live)
		}
	})

	for _, row := range []struct {
		name    string
		own     []netip.Prefix
		wantLAN bool
	}{
		{"a lan address in the client's subnet goes first", []netip.Prefix{pfx("127.0.0.1/8")}, true},
		{"with no subnet in common the vpn address goes first", nil, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			state := t.TempDir()
			lh, vpn, lan := both(t, state)
			cl := clientAt(t, state, remoteJSON(vpn, lh.host.Identity().Fingerprint(),
				`[{"address":"`+vpn+`","kind":"vpn"},{"address":"`+lan+`","kind":"lan"}]`))
			cl.SetInterfaces(func() ([]Iface, error) {
				var out []Iface
				for _, p := range row.own {
					out = append(out, Iface{Name: "en0", Up: true, Addrs: []netip.Prefix{p}})
				}
				return out, nil
			})
			cl.Start()
			t.Cleanup(cl.Stop)
			waitFor(t, "the link", 10*time.Second, func() bool { return cl.Remote("qa-lab").Connected() })
			want := vpn
			if row.wantLAN {
				want = lan
			}
			if got := cl.Remote("qa-lab").current(); got != want {
				t.Errorf("answered at %s; want %s", got, want)
			}
		})
	}
}

// TestReconcileLinks: a remote added is dialled, one removed is closed, and one whose
// fingerprint changed is a different link (a link is to one certificate), all without a
// restart (ADR-0109 §3).
func TestReconcileLinks(t *testing.T) {
	clientState := t.TempDir()
	cid, _, err := LoadIdentity(clientState)
	if err != nil {
		t.Fatal(err)
	}
	lh := startLendingHost(t, t.TempDir(), cid.Fingerprint())
	lh.srv.Reconcile([]netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")})
	addr := lh.host.Health().Hosting.Listen
	fp := lh.host.Identity().Fingerprint()

	c := clientAt(t, clientState, `{}`)
	c.Start()
	t.Cleanup(c.Stop)
	if c.Remote("qa-lab") != nil {
		t.Fatal("a link exists with no remote configured")
	}
	writeConfig(t, clientState, remoteJSON(addr, fp, ""))
	c.Reconcile()
	waitFor(t, "the added remote to link", 10*time.Second, func() bool { l := c.Remote("qa-lab"); return l != nil && l.Connected() })
	first := c.Remote("qa-lab")

	// The same fingerprint: the same link. Another: a new link, which cannot be admitted.
	c.Reconcile()
	if c.Remote("qa-lab") != first {
		t.Error("a reconcile with nothing changed replaced the link")
	}
	writeConfig(t, clientState, remoteJSON(addr, strings.Repeat("d", 64), ""))
	c.Reconcile()
	if now := c.Remote("qa-lab"); now == first || first.Connected() {
		t.Errorf("a changed fingerprint kept the link (replaced %v, old connected %v); want it closed and a new one", now != first, first.Connected())
	}
	writeConfig(t, clientState, `{"remotes":[]}`)
	c.Reconcile()
	if c.Remote("qa-lab") != nil || len(c.Health().Remotes) != 0 {
		t.Errorf("a removed remote is still linked: %+v", c.Health().Remotes)
	}
}

// loopbackOnly is the machine of TestPortBusyIsRemembered: one interface, 127.0.0.1.
func loopbackOnly() ([]Iface, error) {
	return []Iface{{Name: "lo0", Up: true, Addrs: []netip.Prefix{pfx("127.0.0.1/8")}}}, nil
}

func portInConfig(t *testing.T, pol *policy.Policy) int {
	t.Helper()
	return pol.Get().Hosting.Port
}

// TestPortBusyIsRemembered is ADR-0109 §2. With hosting.port absent the first bind tries the
// default (here a port another socket holds), takes a free one instead and writes it to
// hosting.port, once. Once written it is never replaced: the port held by another socket
// afterwards is an error in health, retried, and hosting.port is what it was, so the
// clients paired with this host keep finding it. Seen failing without the write (hosting.port
// stayed absent) and with the replacement kept (the busy port was changed for a free one).
func TestPortBusyIsRemembered(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	heldPort := held.Addr().(*net.TCPAddr).Port

	state := t.TempDir()
	h, pol, log := daemonAt(t, state)
	lend := `"lend":[{"device":"R1","to":["anna"]}]`
	writeConfig(t, state, `{"hosting":{"on":"127.0.0.1",`+lend+`}}`)
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	srv := peer.New(peer.Options{Host: h, Sessions: httpapi.NewPeerSessions(reg, ops, event.New(event.Options{})), API: "0.0.0", Log: log})
	t.Cleanup(srv.Close)
	h.UseListeners(srv)
	h.SetInterfaces(loopbackOnly)
	h.SetDefaultPort(heldPort)

	h.Reconcile()
	listening := h.Health().Hosting.Listening
	if len(listening) != 1 {
		t.Fatalf("listening %v, errors %+v; want one address", listening, h.Health().Hosting.Errors)
	}
	ap := netip.MustParseAddrPort(listening[0])
	port := int(ap.Port())
	if port == heldPort || ap.Addr().String() != "127.0.0.1" {
		t.Fatalf("bound %s while %d is held; want another port on 127.0.0.1", listening[0], heldPort)
	}
	if got := portInConfig(t, pol); got != port {
		t.Fatalf("hosting.port = %d after the first bind; want the port taken, %d", got, port)
	}
	rev := pol.Get().Revision
	h.Reconcile()
	h.Reconcile()
	if pol.Get().Revision != rev {
		t.Error("the port was written again by a reconcile that had nothing to learn")
	}
	if n := strings.Count(log.String(), "wrote it to hosting.port"); n != 1 {
		t.Errorf("the log says the port was written %d times; want once", n)
	}

	// The lending stops: the listener closes. Another socket takes the port. Lending again
	// finds it busy: an error in health, hosting.port the same, nothing bound.
	writeConfig(t, state, `{"hosting":{"on":"127.0.0.1","port":`+strconv.Itoa(port)+`,"lend":[]}}`)
	h.Reconcile()
	if l := h.Health().Hosting.Listening; len(l) != 0 {
		t.Fatalf("still listening on %v with nothing lent", l)
	}
	other, err := net.Listen("tcp", ap.String())
	if err != nil {
		t.Fatalf("the port %s was taken by someone else in between: %v", ap, err)
	}
	defer other.Close()
	writeConfig(t, state, `{"hosting":{"on":"127.0.0.1","port":`+strconv.Itoa(port)+`,`+lend+`}}`)
	h.Reconcile()
	hh := h.Health().Hosting
	if hh.Listen != "" || len(hh.Errors) != 1 || hh.Errors[0].Address != ap.String() || !strings.Contains(hh.Error, ap.String()) {
		t.Errorf("health after the port was held: %+v; want no listener and an error for %s", hh, ap)
	}
	if got := portInConfig(t, pol); got != port {
		t.Errorf("hosting.port = %d after a busy port; want %d, never replaced automatically", got, port)
	}
	other.Close()
	h.Reconcile()
	if hh := h.Health().Hosting; hh.Listen != ap.String() || hh.Error != "" {
		t.Errorf("health after the port was free again: %+v; want it bound again with no error", hh)
	}
}

// TestPrivateListenersFollowLending: with hosting.on set the listeners exist only while a
// device is lent (a 2t configuration with a lend and no on binds nothing), and hosting.listen
// wins over hosting.on (ADR-0109 §1).
func TestPrivateListenersFollowLending(t *testing.T) {
	state := t.TempDir()
	h, _, log := daemonAt(t, state)
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	srv := peer.New(peer.Options{Host: h, Sessions: httpapi.NewPeerSessions(reg, ops, event.New(event.Options{})), API: "0.0.0", Log: log})
	t.Cleanup(srv.Close)
	h.UseListeners(srv)
	h.SetInterfaces(loopbackOnly)
	h.SetDefaultPort(0)
	lend := `"lend":[{"device":"R1","to":["anna"]}]`
	step := func(body string) []string {
		t.Helper()
		writeConfig(t, state, body)
		h.Reconcile()
		return h.Health().Hosting.Listening
	}
	if got := step(`{"hosting":{` + lend + `}}`); len(got) != 0 {
		t.Errorf("a lend and no hosting.on bound %v; want nothing (2t's configuration)", got)
	}
	if got := step(`{"hosting":{"on":"127.0.0.1",` + lend + `}}`); len(got) != 1 || !strings.HasPrefix(got[0], "127.0.0.1:") {
		t.Errorf("hosting.on and a lend bound %v; want 127.0.0.1", got)
	}
	if got := step(`{"hosting":{"on":"127.0.0.1","lend":[]}}`); len(got) != 0 {
		t.Errorf("nothing lent but still bound %v", got)
	}
	if got := step(`{"hosting":{"on":"127.0.0.1","listen":"127.0.0.1:0"}}`); len(got) != 1 {
		t.Errorf("hosting.listen set with nothing lent bound %v; want its one address (its meaning is unchanged)", got)
	}
	// The unusable file leaves the listeners as they are.
	writeConfig(t, state, `{"hosting":{"listen":"0.0.0.0:7318"}}`)
	h.Reconcile()
	if got := h.Health().Hosting.Listening; len(got) != 1 {
		t.Errorf("an unusable configuration changed the listeners to %v; want them left as they were", got)
	}
}

// TestLearntPortNeverReplacesAWrittenOne is the write's own check (ADR-0109 §2): a port
// written by someone else between a pass's read and its write stands.
func TestLearntPortNeverReplacesAWrittenOne(t *testing.T) {
	state := t.TempDir()
	h, pol, _ := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"on":"127.0.0.1","port":47999}}`)
	if err := h.writePort(48000); err != nil {
		t.Fatal(err)
	}
	if got := portInConfig(t, pol); got != 47999 {
		t.Fatalf("hosting.port = %d after a learnt write; want 47999, the one written before", got)
	}
}
