package peer_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// The join exchange of ADR-0107 §3, §4 over real TLS on loopback: the host side is
// adapters/peer, the client side hosting.Join or, where a test must send what Join never
// would, a raw TLS connection. Admit, which step 3 backs with a policy write, is a recorder.

type admitCall struct{ fingerprint, name, device string }

// admitLog is the host's Admit: it records each call and answers.
type admitLog struct {
	mu    sync.Mutex
	calls []admitCall
	err   error
}

func (a *admitLog) admit(fingerprint, name, device string) (string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, admitCall{fingerprint, name, device})
	if a.err != nil {
		return "", "", a.err
	}
	return "peer-" + name, "the-host", nil
}

func (a *admitLog) all() []admitCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]admitCall(nil), a.calls...)
}

// joinSetup is a host that takes joins and an unpaired client.
func joinSetup(t *testing.T) (host, client *side, addr string, adm *admitLog) {
	t.Helper()
	host, client = newSide(t, "0.0.0"), newSide(t, "0.0.0")
	adm = &admitLog{}
	host.admit = adm.admit
	addr, _ = host.listen(t, "127.0.0.1:0")
	return host, client, addr, adm
}

// codeFor is the code of a secret the host issued, naming loopback and the host's port.
func codeFor(t *testing.T, host *side, addr string, secret [16]byte) hosting.Code {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(port)
	fp, err := hex.DecodeString(host.host.Identity().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	c := hosting.Code{Addrs: []hosting.Addr{{IP: netip.MustParseAddr("127.0.0.1"), Kind: hosting.KindLAN}}, Port: uint16(n), Secret: secret}
	copy(c.Fingerprint[:], fp)
	return c
}

// joinConn is a raw TLS connection offering the given protocols.
type joinConn struct {
	c  *tls.Conn
	br *bufio.Reader
}

// dialJoin connects and finishes the client's side of the handshake. With TLS 1.3 a host that
// refuses the certificate is only heard from on the next read, so a nil error is not an
// acceptance: round does that.
func dialJoin(t *testing.T, addr string, id *hosting.Identity, protos ...string) (*joinConn, error) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{id.Certificate()},
		NextProtos: protos, InsecureSkipVerify: true})
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := c.Handshake(); err != nil {
		return nil, err
	}
	return &joinConn{c: c, br: bufio.NewReader(c)}, nil
}

// round sends one request on the connection and returns the status of the answer.
func (j *joinConn) round(method, path, body string) (int, error) {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, "https://host"+path, r)
	if err != nil {
		return 0, err
	}
	if err := req.Write(j.c); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(j.br, req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func joinBody(secret [16]byte, name string) string {
	b, _ := json.Marshal(capability.PeerJoinRequest{Secret: base64.RawURLEncoding.EncodeToString(secret[:]), Name: name, API: "0.0.0"})
	return string(b)
}

func joinErrReason(t *testing.T, err error) string {
	t.Helper()
	var je *hosting.JoinError
	if !errors.As(err, &je) {
		t.Fatalf("the join's error is %v; want a *hosting.JoinError", err)
	}
	return je.Reason
}

// TestJoinOnlyWithAPendingCode is ADR-0107 §3: an unpaired certificate gets through the
// handshake only while a code is unused and only with the join protocol; a secret that is
// not an unused one is a 403 and never reaches Admit; the right one reaches it once, with
// the fingerprint of the certificate that was presented, and is spent; and two joins with
// one secret admit one.
func TestJoinOnlyWithAPendingCode(t *testing.T) {
	host, client, addr, adm := joinSetup(t)
	cid := client.host.Identity()

	// No code is pending: the handshake fails, whatever the client then asks.
	j, err := dialJoin(t, addr, cid, capability.PeerJoinALPN)
	if err == nil {
		_, err = j.round("POST", capability.PeerJoinPath, joinBody([16]byte{1}, "x"))
	}
	if err == nil {
		t.Fatal("an unpaired certificate was served with no code pending")
	}
	if !strings.Contains(host.log.String(), "no code is unused") {
		t.Errorf("the host did not log why it refused the handshake:\n%s", host.log.String())
	}
	_, jerr := hosting.Join(context.Background(), cid, codeFor(t, host, addr, [16]byte{1}), "x", "0.0.0")
	if got := joinErrReason(t, jerr); got != hosting.JoinRefused {
		t.Errorf("Join with no code pending: %s; want refused", got)
	}

	secret := host.host.Pending().Issue("SER1")
	wrong := secret
	wrong[0] ^= 0xFF
	_, jerr = hosting.Join(context.Background(), cid, codeFor(t, host, addr, wrong), "anna", "0.0.0")
	if got := joinErrReason(t, jerr); got != hosting.JoinRefused {
		t.Errorf("Join with a wrong secret: %s; want refused", got)
	}
	if n := len(adm.all()); n != 0 {
		t.Fatalf("Admit was called %d times for a secret that was never issued", n)
	}
	if !strings.Contains(host.log.String(), "refused a join from") {
		t.Errorf("the host did not log the refused join:\n%s", host.log.String())
	}
	if strings.Contains(host.log.String(), base64.RawURLEncoding.EncodeToString(secret[:])) {
		t.Error("the host logged a secret")
	}

	res, err := hosting.Join(context.Background(), cid, codeFor(t, host, addr, secret), "anna", "0.0.0")
	if err != nil {
		t.Fatalf("Join with the right secret: %v", err)
	}
	calls := adm.all()
	if len(calls) != 1 || calls[0].fingerprint != cid.Fingerprint() || calls[0].device != "SER1" || calls[0].name != "anna" {
		t.Fatalf("Admit was called as %+v; want once with the presented certificate's fingerprint, the device and the proposed name", calls)
	}
	if res.Reply.Host != "the-host" || res.Reply.Device != "SER1" || res.Addr.IP.String() != "127.0.0.1" {
		t.Errorf("the join answered %+v from %v; want what Admit returned and the device", res.Reply, res.Addr)
	}

	// The same secret again is spent.
	_, jerr = hosting.Join(context.Background(), cid, codeFor(t, host, addr, secret), "anna", "0.0.0")
	if got := joinErrReason(t, jerr); got != hosting.JoinRefused {
		t.Errorf("Join with a spent secret: %s; want refused", got)
	}
	if n := len(adm.all()); n != 1 {
		t.Errorf("Admit was called %d times; want 1", n)
	}

	// Two (three) joins with one secret admit one, whoever presents it.
	before := len(adm.all())
	shared := host.host.Pending().Issue("SER2")
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 3 {
		c := newSide(t, "0.0.0")
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := hosting.Join(context.Background(), c.host.Identity(), codeFor(t, host, addr, shared), "c", "0.0.0"); err == nil {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || len(adm.all())-before != 1 {
		t.Errorf("%d concurrent joins with one secret succeeded and Admit ran %d times; want 1 and 1", won.Load(), len(adm.all())-before)
	}
}

// TestJoinBodyBoundAndAdmitFailure: a body over 4 KiB is refused before the secret is read
// (so the code is not spent), and an Admit that fails is a refused join.
func TestJoinBodyBoundAndAdmitFailure(t *testing.T) {
	host, client, addr, adm := joinSetup(t)
	secret := host.host.Pending().Issue("SER1")
	j, err := dialJoin(t, addr, client.host.Identity(), capability.PeerJoinALPN)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := j.round("POST", capability.PeerJoinPath, `{"secret":"`+strings.Repeat("A", 5000)+`"}`); err != nil || st != http.StatusRequestEntityTooLarge {
		t.Errorf("a 5000-byte body: %d, %v; want 413", st, err)
	}
	adm.err = errors.New("the device is no longer lent")
	_, jerr := hosting.Join(context.Background(), client.host.Identity(), codeFor(t, host, addr, secret), "anna", "0.0.0")
	if got := joinErrReason(t, jerr); got != hosting.JoinRefused {
		t.Errorf("Join whose Admit failed: %s; want refused", got)
	}
	if n := len(adm.all()); n != 1 {
		t.Errorf("the oversized body spent the code or Admit ran %d times; want 1", n)
	}
}

// TestUnpairedReachesOnlyJoin is ADR-0107 §3, §4: a connection that joined with the join
// protocol is served the join route and nothing else of the port, one request, and then
// closed; an unpaired certificate without the protocol is refused at the handshake while a
// code is pending; at most four join connections at once, counted from the hello and freed
// by the connection's close whatever became of its handshake.
func TestUnpairedReachesOnlyJoin(t *testing.T) {
	host, client, addr, adm := joinSetup(t)
	cid := client.host.Identity()
	host.host.Pending().Issue("SER1")

	// The main mux's routes are not reachable on a join connection: its handler answers 404,
	// and no session, upload or stream is started.
	for _, rq := range []struct{ method, path string }{
		{"GET", capability.PeerPath}, {"GET", capability.PeerScreenPath},
		{"PUT", capability.PeerUploadPath}, {"GET", capability.PeerArtifactsPath + "x"},
	} {
		j, err := dialJoin(t, addr, cid, capability.PeerJoinALPN)
		if err != nil {
			t.Fatal(err)
		}
		body := ""
		if rq.method == "PUT" {
			body = "not an apk"
		}
		if st, err := j.round(rq.method, rq.path, body); err != nil || st != http.StatusNotFound {
			t.Errorf("%s %s on a join connection: %d, %v; want 404 from the join handler", rq.method, rq.path, st, err)
		}
	}
	if j, err := dialJoin(t, addr, cid, capability.PeerJoinALPN); err != nil {
		t.Fatal(err)
	} else if st, err := j.round("GET", capability.PeerJoinPath, ""); err != nil || st != http.StatusMethodNotAllowed {
		t.Errorf("GET on the join route: %d, %v; want 405: it holds POST alone", st, err)
	}
	if ops := host.ops.Active(); len(ops) != 0 {
		t.Errorf("a join connection started operations: %+v", ops)
	}
	if h := host.host.Health().Hosting.Peers; len(h) != 0 {
		t.Errorf("a join connection became a peer: %+v", h)
	}

	// One request per connection: a second one on it is not served.
	j, err := dialJoin(t, addr, cid, capability.PeerJoinALPN)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := j.round("POST", capability.PeerJoinPath, joinBody([16]byte{9}, "x")); err != nil || st != http.StatusForbidden {
		t.Fatalf("a join with a wrong secret: %d, %v; want 403", st, err)
	}
	if st, err := j.round("POST", capability.PeerJoinPath, joinBody([16]byte{9}, "x")); err == nil {
		t.Errorf("a second request on a join connection was served: %d", st)
	}

	// An unpaired certificate that does not offer the join protocol is refused at the
	// handshake although a code is pending, whether it offers http/1.1 or nothing.
	for _, protos := range [][]string{{"http/1.1"}, nil} {
		j, err := dialJoin(t, addr, cid, protos...)
		if err == nil {
			_, err = j.round("GET", capability.PeerPath, "")
		}
		if err == nil {
			t.Errorf("an unpaired certificate offering %v was served while a code was pending", protos)
		}
	}
	if !strings.Contains(host.log.String(), "is not in hosting.peers\n") {
		t.Errorf("the host did not log the handshake it refused:\n%s", host.log.String())
	}
	if n := len(adm.all()); n != 0 {
		t.Errorf("Admit ran %d times", n)
	}
}

// TestJoinSlots is ADR-0107 §4: four join connections at once, counted from the hello; a
// fifth hello fails at the handshake; a slot is free again when its connection closes,
// including a connection whose handshake failed (the host refused the certificate, or the
// client aborted on its own pin), which never reaches the join handler.
func TestJoinSlots(t *testing.T) {
	host, client, addr, adm := joinSetup(t)
	cid := client.host.Identity()
	first := host.host.Pending().Issue("SER1")
	second := host.host.Pending().Issue("SER1")

	var held []*joinConn
	for i := range 4 {
		j, err := dialJoin(t, addr, cid, capability.PeerJoinALPN)
		if err != nil {
			t.Fatalf("join connection %d: %v", i+1, err)
		}
		held = append(held, j)
	}
	if _, err := dialJoin(t, addr, cid, capability.PeerJoinALPN); err == nil {
		t.Fatal("a fifth join hello was accepted")
	}
	if !strings.Contains(host.log.String(), "joins are in progress") {
		t.Errorf("the host did not log the refused hello:\n%s", host.log.String())
	}

	// A join that returns frees its slot.
	if st, err := held[0].round("POST", capability.PeerJoinPath, joinBody(first, "anna")); err != nil || st != http.StatusOK {
		t.Fatalf("a join on a held connection: %d, %v; want 200", st, err)
	}
	eventually(t, "a slot to be free after a join returned", 5*time.Second, func() bool {
		j, err := dialJoin(t, addr, cid, capability.PeerJoinALPN)
		if err != nil {
			return false
		}
		st, err := j.round("POST", capability.PeerJoinPath, joinBody(second, "anna"))
		return err == nil && st == http.StatusOK
	})
	if n := len(adm.all()); n != 2 {
		t.Errorf("Admit ran %d times; want 2", n)
	}
	for _, j := range held {
		j.c.Close()
	}
	eventually(t, "the held connections to free their slots", 5*time.Second, func() bool {
		var opened []*joinConn
		defer func() {
			for _, j := range opened {
				j.c.Close()
			}
		}()
		for range 4 {
			j, err := dialJoin(t, addr, cid, capability.PeerJoinALPN)
			if err != nil {
				return false
			}
			opened = append(opened, j)
		}
		return true
	})
}

// TestFailedHandshakesFreeTheirSlots: four join hellos whose handshakes fail, two refused
// by the host (no code is pending) and two abandoned by the client on its own pin, leave no
// slot held: a valid join is then admitted. A slot freed only when the join handler returns
// would stay held by all four, since none reaches the handler.
func TestFailedHandshakesFreeTheirSlots(t *testing.T) {
	host, client, addr, adm := joinSetup(t)
	cid := client.host.Identity()

	for range 2 {
		j, err := dialJoin(t, addr, cid, capability.PeerJoinALPN)
		if err == nil {
			_, err = j.round("POST", capability.PeerJoinPath, joinBody([16]byte{1}, "x"))
		}
		if err == nil {
			t.Fatal("a join was served with no code pending")
		}
	}
	secret := host.host.Pending().Issue("SER1")
	for range 2 {
		other := codeFor(t, host, addr, secret)
		other.Fingerprint[0] ^= 0xFF // not the host's: the client aborts on its own pin
		if _, err := hosting.Join(context.Background(), cid, other, "anna", "0.0.0"); joinErrReason(t, err) != hosting.JoinNotThisHost {
			t.Fatalf("a join with another fingerprint: %v; want notThisHost", err)
		}
	}
	var res hosting.JoinResult
	var err error
	eventually(t, "a valid join to be admitted", 5*time.Second, func() bool {
		res, err = hosting.Join(context.Background(), cid, codeFor(t, host, addr, secret), "anna", "0.0.0")
		return err == nil
	})
	if res.Reply.Device != "SER1" || len(adm.all()) != 1 {
		t.Errorf("the valid join answered %+v and Admit ran %d times; want SER1 and 1", res.Reply, len(adm.all()))
	}
}

// TestJoinConnectionIsBounded: a join connection that sends nothing is cut at the join
// timeout and frees its slot.
func TestJoinConnectionIsBounded(t *testing.T) {
	peer.SetJoinTimeout(300 * time.Millisecond)
	t.Cleanup(func() { peer.SetJoinTimeout(10 * time.Second) })
	host, client, addr, _ := joinSetup(t)
	host.host.Pending().Issue("SER1")
	var held []*joinConn
	for range 4 {
		j, err := dialJoin(t, addr, client.host.Identity(), capability.PeerJoinALPN)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, j)
	}
	held[0].c.SetDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	if _, err := io.ReadAll(held[0].br); err != nil {
		t.Fatalf("the idle join connection was not closed by the host: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the idle join connection lived %s; want about the join timeout", took)
	}
	eventually(t, "the cut connection's slot", 5*time.Second, func() bool {
		j, err := dialJoin(t, addr, client.host.Identity(), capability.PeerJoinALPN)
		if err != nil {
			return false
		}
		j.c.Close()
		return true
	})
}

// TestPairedPeerJoins is ADR-0107 §3: a paired certificate may join too (a new share):
// with a valid code it reaches Admit with its fingerprint, and with none it is served and
// refused rather than failed at the handshake.
func TestPairedPeerJoins(t *testing.T) {
	host, client, addr, adm := joinSetup(t)
	cid := client.host.Identity()
	host.cfg.setPeers(policy.HostPeer{Name: "anna", Fingerprint: cid.Fingerprint()})

	_, err := hosting.Join(context.Background(), cid, codeFor(t, host, addr, [16]byte{4}), "anna", "0.0.0")
	if got := joinErrReason(t, err); got != hosting.JoinRefused {
		t.Errorf("a paired peer joining with no code pending: %s; want refused (served, then 403)", got)
	}
	if n := len(adm.all()); n != 0 {
		t.Fatalf("Admit ran %d times with no code pending", n)
	}

	secret := host.host.Pending().Issue("SER1")
	if _, err := hosting.Join(context.Background(), cid, codeFor(t, host, addr, secret), "anna", "0.0.0"); err != nil {
		t.Fatalf("a paired peer joining a new share: %v", err)
	}
	calls := adm.all()
	if len(calls) != 1 || calls[0].fingerprint != cid.Fingerprint() || calls[0].device != "SER1" {
		t.Errorf("Admit was called as %+v; want once with the paired certificate's fingerprint", calls)
	}
	if !strings.Contains(host.log.String(), "already paired") {
		t.Errorf("the host's log does not say the peer was already paired:\n%s", host.log.String())
	}
}

// TestHelloOfferingOnlyJoinFailsWithoutAJoinHandler: on a listener with no Admit, a hello
// that offers only the join protocol fails at the handshake, a paired certificate's too:
// the default configuration offers http/1.1 alone.
func TestHelloOfferingOnlyJoinFailsWithoutAJoinHandler(t *testing.T) {
	host, client := newSide(t, "0.0.0"), newSide(t, "0.0.0")
	addr, _ := host.listen(t, "127.0.0.1:0")
	host.cfg.setPeers(policy.HostPeer{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()})
	host.host.Pending().Issue("SER1")
	if _, err := dialJoin(t, addr, client.host.Identity(), capability.PeerJoinALPN); err == nil {
		t.Error("a hello offering only the join protocol was accepted by a listener that takes no joins")
	}
	if _, err := dialJoin(t, addr, client.host.Identity(), "http/1.1"); err != nil {
		t.Errorf("a paired peer's ordinary hello failed: %v", err)
	}
}
