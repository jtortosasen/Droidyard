package hosting

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The join's timers. Variables only so this package's tests can shorten them.
var (
	// joinDialTimeout bounds the TCP connection and the TLS handshake to one address
	// (ADR-0107 §3): the next address is tried after it.
	joinDialTimeout = 3 * time.Second
	// joinExchangeTimeout bounds the request and its answer on a connection that is up; it
	// is the host's own bound too.
	joinExchangeTimeout = 10 * time.Second
)

// Why a join failed, as HOSTING_JOIN_FAILED's details.reason will say (ADR-0107 §6).
const (
	// JoinUnreachable: no address answered, or the ones that did gave no usable answer.
	JoinUnreachable = "unreachable"
	// JoinNotThisHost: every address answered with a certificate other than the code's. A
	// private address can be another machine on the client's own network.
	JoinNotThisHost = "notThisHost"
	// JoinRefused: the host presented the code's certificate and did not admit this daemon:
	// the code is used, void or wrong.
	JoinRefused = "refused"
)

// AddrFailure is why one address of a code did not give an answer.
type AddrFailure struct {
	Addr Addr
	Err  error
}

// JoinError is a join that did not succeed: Reason is one of the Join constants and
// Failures say what happened at each address that was tried.
type JoinError struct {
	Reason   string
	Failures []AddrFailure
}

func (e *JoinError) Error() string {
	var parts []string
	for _, f := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s (%s): %v", f.Addr.IP, f.Addr.Kind, f.Err))
	}
	return "the join failed (" + e.Reason + "): " + strings.Join(parts, "; ")
}

// JoinResult is a join the host admitted: its answer, the address that gave it and why the
// addresses tried before it did not (the ones after it were not tried).
type JoinResult struct {
	Reply    capability.PeerJoinReply
	Addr     Addr
	Failures []AddrFailure
}

// joinDial opens the TCP connection to one address of a code. A variable only so this
// package's tests can send two addresses of a code to two listeners on one machine.
var joinDial = func(ctx context.Context, hostport string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", hostport)
}

// errJoinRefused is the host presenting the pinned certificate and not admitting the join.
var errJoinRefused = errors.New("the host did not admit the join")

// Join presents id to the host a code names and spends the code's secret there
// (ADR-0107 §3). The code's addresses are tried in the order given, each for at most
// joinDialTimeout; TLS 1.3 with ALPN droidyard-join/1, and the host's certificate must be
// the code's fingerprint before a byte of the request is sent, so the secret only ever
// reaches that host. An address that presents another certificate is that address's
// failure and the next is tried. name is the name this daemon proposes for itself; api its
// API version. The answer is the host's text, untrusted: the caller cleans it (ADR-0100 §4).
func Join(ctx context.Context, id *Identity, code Code, name, api string) (JoinResult, error) {
	if err := code.validate(); err != nil {
		return JoinResult{}, &JoinError{Reason: JoinUnreachable, Failures: []AddrFailure{{Err: err}}}
	}
	body, err := json.Marshal(capability.PeerJoinRequest{
		Secret: base64.RawURLEncoding.EncodeToString(code.Secret[:]), Name: name, API: api})
	if err != nil {
		return JoinResult{}, err
	}
	var failures []AddrFailure
	for _, a := range code.Addrs {
		if err := ctx.Err(); err != nil {
			failures = append(failures, AddrFailure{Addr: a, Err: err})
			break
		}
		reply, err := joinAt(ctx, id, code, a, body)
		if err == nil {
			return JoinResult{Reply: reply, Addr: a, Failures: failures}, nil
		}
		failures = append(failures, AddrFailure{Addr: a, Err: err})
		if errors.Is(err, errJoinRefused) {
			// The code's certificate answered: the other addresses are the same host.
			return JoinResult{}, &JoinError{Reason: JoinRefused, Failures: failures}
		}
	}
	reason := JoinNotThisHost
	for _, f := range failures {
		if !errors.Is(f.Err, ErrPinned) {
			reason = JoinUnreachable
		}
	}
	return JoinResult{}, &JoinError{Reason: reason, Failures: failures}
}

// notAdmitted turns the failure of a connection whose host was already pinned into a
// refusal when it is the host's TLS alert for a client certificate it does not accept: with
// TLS 1.3 that alert arrives after the client's handshake has returned, as the host's
// answer to a join that has no code pending.
func notAdmitted(err error) error {
	if strings.Contains(err.Error(), "bad certificate") {
		return fmt.Errorf("%w: it does not accept this daemon's certificate", errJoinRefused)
	}
	return err
}

// joinAt makes one attempt at one address.
func joinAt(ctx context.Context, id *Identity, code Code, a Addr, body []byte) (capability.PeerJoinReply, error) {
	var none capability.PeerJoinReply
	hostport := net.JoinHostPort(a.IP.String(), strconv.Itoa(int(code.Port)))
	dctx, cancel := context.WithTimeout(ctx, joinDialTimeout)
	defer cancel()
	raw, err := joinDial(dctx, hostport)
	if err != nil {
		return none, err
	}
	tc := tls.Client(raw, &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{id.Certificate()},
		NextProtos:         []string{capability.PeerJoinALPN},
		InsecureSkipVerify: true, // the pin below is the verification
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("%w: it presented none", ErrPinned)
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(sum[:], code.Fingerprint[:]) != 1 {
				return ErrPinned
			}
			return nil
		},
	})
	defer tc.Close()
	// The handshake: nothing of the request is sent before the pin has passed.
	if err := tc.HandshakeContext(dctx); err != nil {
		if errors.Is(err, ErrPinned) {
			return none, ErrPinned
		}
		return none, err
	}
	if tc.ConnectionState().NegotiatedProtocol != capability.PeerJoinALPN {
		return none, errors.New("the host does not speak the join protocol")
	}
	tc.SetDeadline(time.Now().Add(joinExchangeTimeout))
	stop := context.AfterFunc(ctx, func() { tc.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	req, err := http.NewRequest(http.MethodPost, "https://"+hostport+capability.PeerJoinPath, bytes.NewReader(body))
	if err != nil {
		return none, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Close = true
	if err := req.Write(tc); err != nil {
		return none, notAdmitted(err)
	}
	// What the host sends back is bounded: headers and a body of at most PeerMaxJoinBody.
	resp, err := http.ReadResponse(bufio.NewReader(io.LimitReader(tc, 4*capability.PeerMaxJoinBody)), req)
	if err != nil {
		return none, notAdmitted(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, capability.PeerMaxJoinBody+1))
	if err != nil {
		return none, err
	}
	if resp.StatusCode != http.StatusOK {
		return none, fmt.Errorf("%w (HTTP %d)", errJoinRefused, resp.StatusCode)
	}
	var reply capability.PeerJoinReply
	if len(data) > capability.PeerMaxJoinBody || json.Unmarshal(data, &reply) != nil {
		return none, fmt.Errorf("%w: its answer is not a join reply", errJoinRefused)
	}
	return reply, nil
}
