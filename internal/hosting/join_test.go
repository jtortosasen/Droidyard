package hosting

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/httpapi"
	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// fixedConfig is a policy that pairs nobody.
type fixedConfig struct{}

func (fixedConfig) Hosting() (policy.HostingPolicy, *capability.Error) {
	return policy.HostingPolicy{}, nil
}
func (fixedConfig) Remotes() ([]policy.Remote, *capability.Error) { return nil, nil }

// joinHost is a daemon's peer listener on loopback, with a join handler that records what it
// admits.
type joinHost struct {
	host   *Host
	addr   string
	log    *lockedLog
	mu     sync.Mutex
	admits []string
}

func startJoinHost(t *testing.T) *joinHost {
	t.Helper()
	jh := &joinHost{log: &lockedLog{}}
	jh.host = New(fixedConfig{}, t.TempDir(), "0.0.0", jh.log)
	t.Cleanup(jh.host.Uploads().Close)
	if err := jh.host.Init(); err != nil {
		t.Fatal(err)
	}
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	bus := event.New(event.Options{})
	srv := peer.New(peer.Options{Host: jh.host, Sessions: httpapi.NewPeerSessions(reg, ops, bus), Listen: "127.0.0.1:0", API: "0.0.0", Log: jh.log,
		Admit: func(fingerprint, name, device string) (string, string, error) {
			jh.mu.Lock()
			defer jh.mu.Unlock()
			jh.admits = append(jh.admits, fingerprint)
			return "peer", "the-host", nil
		}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); srv.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(10 * time.Second); jh.addr == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		jh.addr = jh.host.Health().Hosting.Listen
	}
	if jh.addr == "" {
		t.Fatal("the listener did not bind")
	}
	return jh
}

func (j *joinHost) admitted() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.admits)
}

// routeDials sends each fake private address of a code to a listener on this machine, so
// one code can name two machines although every test binds 127.0.0.1 only.
func routeDials(t *testing.T, routes map[string]string) {
	t.Helper()
	old := joinDial
	t.Cleanup(func() { joinDial = old })
	joinDial = func(ctx context.Context, hostport string) (net.Conn, error) {
		ip, _, _ := net.SplitHostPort(hostport)
		to, ok := routes[ip]
		if !ok {
			return nil, errors.New("no route to " + ip)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", to)
	}
}

func codeAt(host *joinHost, secret [16]byte, ips ...string) Code {
	c := Code{Port: 4444, Secret: secret}
	fp, _ := fingerprintBytes(host.host.Fingerprint())
	c.Fingerprint = fp
	for _, ip := range ips {
		c.Addrs = append(c.Addrs, Addr{IP: netip.MustParseAddr(ip), Kind: KindLAN})
	}
	return c
}

// fingerprintBytes is a fingerprint in hex as the 32 bytes a code carries.
func fingerprintBytes(hex string) (fp [32]byte, err error) {
	if len(hex) != 64 {
		return fp, errors.New("not a fingerprint")
	}
	for i := range fp {
		var b byte
		for _, c := range hex[2*i : 2*i+2] {
			b <<= 4
			switch {
			case c >= '0' && c <= '9':
				b |= byte(c - '0')
			case c >= 'a' && c <= 'f':
				b |= byte(c-'a') + 10
			default:
				return fp, errors.New("not hex")
			}
		}
		fp[i] = b
	}
	return fp, nil
}

// TestJoinPinsTheHost is ADR-0107 §3: the client checks the host's certificate against the
// code's fingerprint in the handshake, before a byte of the request, so the secret reaches
// only that host. An address that presents another certificate (a private address can be
// another machine on the client's own network) is that address's failure and the next is
// tried; the join is notThisHost only when no address presented the pinned certificate and
// not one of them was unreachable.
func TestJoinPinsTheHost(t *testing.T) {
	client := startJoinHost(t) // only its identity is used
	host, other := startJoinHost(t), startJoinHost(t)
	secret := host.host.Pending().Issue("SER1")
	other.host.Pending().Issue("SER9") // so that, unpinned, the other machine would take the join
	routeDials(t, map[string]string{"10.9.9.1": other.addr, "10.9.9.2": host.addr})
	id := client.host.Identity()

	// Only the other machine answers: nothing is sent to it.
	_, err := Join(context.Background(), id, codeAt(host, secret, "10.9.9.1"), "anna", "0.0.0")
	var je *JoinError
	if !errors.As(err, &je) || je.Reason != JoinNotThisHost || len(je.Failures) != 1 || !errors.Is(je.Failures[0].Err, ErrPinned) {
		t.Fatalf("a code naming a host that is not at its address: %v; want notThisHost with a pin failure", err)
	}
	if other.admitted() != 0 || host.admitted() != 0 || strings.Contains(other.log.String(), "join from") || strings.Contains(other.log.String(), "joined from") {
		t.Errorf("the join handler was reached on a machine that is not the code's host:\n%s", other.log.String())
	}

	// The first address is the other machine, the second the host: the join goes on.
	res, err := Join(context.Background(), id, codeAt(host, secret, "10.9.9.1", "10.9.9.2"), "anna", "0.0.0")
	if err != nil {
		t.Fatalf("the join with the host at the second address: %v", err)
	}
	if res.Addr.IP.String() != "10.9.9.2" || len(res.Failures) != 1 || res.Failures[0].Addr.IP.String() != "10.9.9.1" || !errors.Is(res.Failures[0].Err, ErrPinned) {
		t.Errorf("the join answered at %v with failures %+v; want the second address and the first's pin failure", res.Addr, res.Failures)
	}
	if host.admitted() != 1 || other.admitted() != 0 {
		t.Errorf("admits: host %d, other %d; want 1 and 0", host.admitted(), other.admitted())
	}

	// An address that answered with another certificate and one that did not answer at all:
	// the host may be at the second, so this is unreachable, and the failures say both.
	_, err = Join(context.Background(), id, codeAt(host, secret, "10.9.9.1", "10.9.9.3"), "anna", "0.0.0")
	if !errors.As(err, &je) || je.Reason != JoinUnreachable || len(je.Failures) != 2 {
		t.Errorf("a mismatch and an unreachable address: %v; want unreachable with two failures", err)
	}
}

// TestJoinTriesAddressesInOrderWithinTheBound: addresses are tried in the order the code
// gives, each for at most the dial bound, and a host that refuses stops the attempts: the
// other addresses are the same machine.
func TestJoinTriesAddressesInOrderWithinTheBound(t *testing.T) {
	client := startJoinHost(t)
	host := startJoinHost(t)
	secret := host.host.Pending().Issue("SER1")
	var mu sync.Mutex
	var order []string
	old := joinDial
	t.Cleanup(func() { joinDial = old })
	joinDial = func(ctx context.Context, hostport string) (net.Conn, error) {
		ip, _, _ := net.SplitHostPort(hostport)
		mu.Lock()
		order = append(order, ip)
		mu.Unlock()
		if ip == "10.9.9.1" { // a black hole: the dial ends when its bound does
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", host.addr)
	}
	oldBound := joinDialTimeout
	joinDialTimeout = 200 * time.Millisecond
	t.Cleanup(func() { joinDialTimeout = oldBound })

	start := time.Now()
	res, err := Join(context.Background(), client.host.Identity(), codeAt(host, secret, "10.9.9.1", "10.9.9.2"), "anna", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 150*time.Millisecond || took > 3*time.Second {
		t.Errorf("the join took %s; want the first address's bound (200 ms) and then the second", took)
	}
	if res.Addr.IP.String() != "10.9.9.2" || len(res.Failures) != 1 || !errors.Is(res.Failures[0].Err, context.DeadlineExceeded) {
		t.Errorf("answered at %v, failures %+v; want the second address after the first timed out", res.Addr, res.Failures)
	}

	// A refusal ends it: the second address is not tried.
	mu.Lock()
	order = nil
	mu.Unlock()
	_, err = Join(context.Background(), client.host.Identity(), codeAt(host, secret, "10.9.9.2", "10.9.9.4"), "anna", "0.0.0") // the secret is spent
	if !errors.As(err, new(*JoinError)) {
		t.Fatalf("a spent code: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 1 || order[0] != "10.9.9.2" {
		t.Errorf("addresses dialled after a refusal: %v; want only the first", order)
	}
}
