package main

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Live peer configuration (ADR-0109 §3): the listeners and the links follow the configuration
// file and the machine's interfaces within seconds, with no restart.

// reconcileBound is the bound the decision names: the daemon checks every 2 s, and the test
// adds a health call or two. It is a product claim and it is unscaled: no level-5 run timed
// the reconcile (m2u steps 2 and 3 did not), so nothing licenses stretching it under -race
// (register: "The 3 s bound live_peer_test.go puts on ADR-0109 §3's reconcile").
const reconcileBound = 3 * time.Second

// updateOn runs policy.update with body on the daemon whose state root is state.
func updateOn(t *testing.T, state, body string) {
	t.Helper()
	if env := callOn(t, state, "policy.update", "--params", body); env.Error != nil {
		t.Fatalf("policy.update %s: %+v", body, env.Error)
	}
}

func hostingOf(t *testing.T, state string) *hosting.HostingHealth {
	t.Helper()
	h := healthOn(t, state).Hosting
	if h == nil {
		t.Fatal("system.health has no hosting")
	}
	return h
}

// portInConfig is hosting.port as policy.get on the daemon says it.
func portInConfig(t *testing.T, state string) int {
	t.Helper()
	env := callOn(t, state, "policy.get")
	var res policy.GetResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("policy.get %s: %v", env.Result, err)
	}
	return res.Hosting.Port
}

// TestLivePeerConfig: two real `serve` processes on loopback follow their configuration with
// no restart. The host binds a hosting.listen written after it started; with a lend and no
// hosting.on it binds nothing (2t's behaviour), then hosting.on binds the private address and
// writes the port, and lending nothing closes the listener. The client dials a remote added
// after it started, closes the link of one removed, and links a remote whose address is a
// closed port through the live one in its addresses. Each was seen failing with the
// configuration read once at start (listen, remotes), or with addresses ignored.
func TestLivePeerConfig(t *testing.T) {
	t.Parallel()
	hostState, clientState := shortDir(t), shortDir(t)
	clientFP, hostFP := identityOf(t, clientState), identityOf(t, hostState)
	daemonAt(t, hostState, `{"hosting":{"peers":[`+peerEntry("anna", clientFP)+`]}}`)
	daemonAt(t, clientState, "")

	if h := hostingOf(t, hostState); h.Listen != "" || len(h.Listening) != 0 {
		t.Fatalf("a host with no listen listens on %+v", h)
	}

	// hosting.listen, written to the running daemon.
	updateOn(t, hostState, `{"hosting":{"listen":"127.0.0.1:0"}}`)
	testtime.Eventually(t, reconcileBound, "the host to bind the hosting.listen written after it started", func() bool { return hostingOf(t, hostState).Listen != "" })
	updateOn(t, hostState, `{"hosting":{"listen":null}}`)
	testtime.Eventually(t, reconcileBound, "the listener to close when hosting.listen is removed", func() bool { return hostingOf(t, hostState).Listen == "" })

	// A lend and no hosting.on is 2t's configuration: nothing is bound.
	updateOn(t, hostState, `{"hosting":{"lend":[{"device":"R58M","to":["anna"]}]}}`)
	time.Sleep(2500 * time.Millisecond) // a reconcile has run
	if h := hostingOf(t, hostState); h.Listen != "" || len(h.Listening) != 0 || len(h.Errors) != 0 {
		t.Fatalf("a lend with no hosting.on binds %+v; want nothing", h)
	}
	// hosting.on names the address; the port the listener took is written.
	updateOn(t, hostState, `{"hosting":{"on":"127.0.0.1"}}`)
	var addr string
	testtime.Eventually(t, reconcileBound, "hosting.on to bind 127.0.0.1", func() bool {
		addr = hostingOf(t, hostState).Listen
		return strings.HasPrefix(addr, "127.0.0.1:")
	})
	port, _ := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
	testtime.Eventually(t, reconcileBound, "the port taken to be written to hosting.port", func() bool { return portInConfig(t, hostState) == port })

	// The client is told the address after it started: remote add is a file edit.
	if code := droidyard(t, clientState, "remote", "add", "qa-lab", addr, hostFP).wait(t); code != 0 {
		t.Fatalf("remote add exited %d", code)
	}
	testtime.Eventually(t, reconcileBound, "the client to link the remote added after it started", func() bool {
		r := healthOn(t, clientState).Remotes
		return len(r) == 1 && r[0].Connected
	})
	testtime.Eventually(t, reconcileBound, "the host to count the peer connected", func() bool {
		p := hostingOf(t, hostState).Peers
		return len(p) == 1 && p[0].Connected
	})
	if code := droidyard(t, clientState, "remote", "remove", "qa-lab").wait(t); code != 0 {
		t.Fatalf("remote remove exited %d", code)
	}
	testtime.Eventually(t, reconcileBound, "the client to close the link of the remote removed", func() bool { return len(healthOn(t, clientState).Remotes) == 0 })
	testtime.Eventually(t, reconcileBound, "the host to see the peer gone", func() bool { return !hostingOf(t, hostState).Peers[0].Connected })

	// A remote whose address is a closed port and whose addresses holds the live one.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	updateOn(t, clientState, `{"remotes":[{"name":"qa-lab","address":"`+closed+`","fingerprint":"`+hostFP+`","addresses":[{"address":"`+addr+`","kind":"lan"}]}]}`)
	testtime.Eventually(t, reconcileBound, "a remote with a closed address to link through its addresses", func() bool {
		r := healthOn(t, clientState).Remotes
		return len(r) == 1 && r[0].Connected
	})

	// Lending nothing closes the listener, and the link with it.
	updateOn(t, hostState, `{"hosting":{"lend":[]}}`)
	testtime.Eventually(t, reconcileBound, "the listener to close when nothing is lent", func() bool { return hostingOf(t, hostState).Listen == "" })
	testtime.Eventually(t, reconcileBound, "the link to close with its listener", func() bool { return !healthOn(t, clientState).Remotes[0].Connected })
	// Lending again binds the same address: the port is remembered.
	updateOn(t, hostState, `{"hosting":{"lend":[{"device":"R58M","to":["anna"]}]}}`)
	testtime.Eventually(t, reconcileBound, "the listener to bind again", func() bool { return hostingOf(t, hostState).Listen == addr })
}

// answersAsThisDaemon reports whether addr takes a TLS handshake and presents the certificate
// whose fingerprint is fp: this daemon's peer listener. A bare dial would also take any other
// socket that holds the port once the daemon's is closed, on a machine whose test processes
// bind and release ports all the time (a free port is anyone's), and that is not the claim.
func answersAsThisDaemon(addr, fp string) bool {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return false
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	return len(certs) > 0 && hosting.SamePin(hosting.FingerprintOf(certs[0]), fp)
}

// TestReconcileFollowsInterfaces: the daemon reads this machine's interfaces at every
// reconcile, not once. An in-process daemon with an injected lister that holds 127.0.0.1 as
// the only address, under hosting.on 127.0.0.1: when the lister drops it the listener closes
// within 3 s and the port no longer answers as this daemon; when it returns the listener is bound again, at the
// same port. Seen failing with the interfaces read once (the first answer kept).
func TestReconcileFollowsInterfaces(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	p := startInProcessConfig(t, "127.0.0.1:0", nil, nil, 0,
		`{"hosting":{"on":"127.0.0.1","port":`+strconv.Itoa(port)+`,"lend":[{"device":"R58M","to":["anna"]}]}}`)
	var present atomic.Bool
	present.Store(true)
	p.host.SetInterfaces(func() ([]hosting.Iface, error) {
		if !present.Load() {
			return []hosting.Iface{{Name: "lo0", Up: true}}, nil
		}
		return []hosting.Iface{{Name: "lo0", Up: true, Addrs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}}}, nil
	})
	stop := startPeers(p.host, p.reg, p.ops, p.bus, p.store, p.adb, os.Stderr)
	t.Cleanup(stop)
	want := "127.0.0.1:" + strconv.Itoa(port)
	listening := func() []string { return p.host.Health().Hosting.Listening }

	testtime.Eventually(t, reconcileBound, "the listener to bind the address the lister holds", func() bool { return len(listening()) == 1 && listening()[0] == want })
	// The probe below is only worth something if it sees the listener while it is up.
	if !answersAsThisDaemon(want, p.host.Fingerprint()) {
		t.Fatalf("%s is listed as listening, and does not answer as this daemon (%s)", want, p.host.Fingerprint())
	}
	present.Store(false)
	testtime.Eventually(t, reconcileBound, "the listener to close when the lister drops the address", func() bool { return len(listening()) == 0 })
	if answersAsThisDaemon(want, p.host.Fingerprint()) {
		t.Error("the port still answers as this daemon after the address left the interfaces")
	}
	present.Store(true)
	testtime.Eventually(t, reconcileBound, "the listener to bind again when the address returns", func() bool { return len(listening()) == 1 && listening()[0] == want })
}
