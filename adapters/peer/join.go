package peer

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The join exchange (ADR-0107 §3, §4): a TLS hello that offers droidyard-join/1 is the only
// one that may present a certificate that is not paired, and only while a code is unused.
// Its connection is served exactly one request, POST /peer/v1/join, and closed; it is never
// handed to the port's main mux, so what it reaches is this file and nothing else.

// maxJoins is how many join connections are handshaking or being served at once.
const maxJoins = 4

// joinTimeout bounds a join connection from the end of its handshake to its close. A
// variable only so this package's tests can shorten it.
var joinTimeout = 10 * time.Second

// joinWriteGrace is how long the answer to a join may take to be written, after the join's
// own deadline is spent.
const joinWriteGrace = 2 * time.Second

// joinMaxRead is what a join connection may send at all: its headers and the body.
const joinMaxRead = 2*capability.PeerMaxJoinBody + capability.PeerMaxJoinBody

// joinListener wraps what the listener accepts so each connection can carry a join slot.
type joinListener struct {
	net.Listener
	s *Server
}

func (l *joinListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &joinConn{Conn: c, s: l.s}, nil
}

// joinConn is a connection that may hold a join slot, from the hello that offered the join
// protocol to its Close, whatever became of its handshake.
type joinConn struct {
	net.Conn
	s    *Server
	mu   sync.Mutex
	held bool
}

// take holds a slot for this connection: false when all of them are taken.
func (c *joinConn) take() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held {
		return true
	}
	select {
	case c.s.joinSlots <- struct{}{}:
		c.held = true
		return true
	default:
		return false
	}
}

// Close releases the slot, if one is held, once.
func (c *joinConn) Close() error {
	err := c.Conn.Close()
	c.mu.Lock()
	if c.held {
		c.held = false
		<-c.s.joinSlots
	}
	c.mu.Unlock()
	return err
}

// joinConfig is the TLS configuration of a hello that offers the join protocol: it offers
// that protocol alone, and takes a certificate that is paired, or one that is not while a
// code is unused. A hello that finds no slot free fails here.
func (s *Server) joinConfig(c *tls.Config, chi *tls.ClientHelloInfo) (*tls.Config, error) {
	addr := chi.Conn.RemoteAddr().String()
	jc, ok := chi.Conn.(*joinConn)
	if !ok || !jc.take() {
		fmt.Fprintf(s.o.Log, "droidyard: peer listener: refused a join handshake from %s: %d joins are in progress\n", addr, maxJoins)
		return nil, errNoJoinSlot
	}
	c.NextProtos = []string{capability.PeerJoinALPN}
	c.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errNotPinned
		}
		_, fp, pinned := s.o.Host.Pin(cs.PeerCertificates[0])
		if pinned || s.o.Host.CodePending() {
			return nil
		}
		fmt.Fprintf(s.o.Log, "droidyard: peer listener: refused a join handshake from %s: fingerprint %s is not in hosting.peers and no code is unused\n", addr, fp[:12])
		return errNotPinned
	}
	return c, nil
}

// serveJoinConn is TLSNextProto's entry for the join protocol: it reads one request off the
// connection, answers it and closes. The request is read here and not by an inner
// http.Server, which would see the negotiated protocol and close the connection as the
// outer one does.
func (s *Server) serveJoinConn(_ *http.Server, c *tls.Conn, _ http.Handler) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(s.joinTimeout))
	w := &joinWriter{h: http.Header{}}
	req, err := http.ReadRequest(bufio.NewReaderSize(io.LimitReader(c, joinMaxRead), 4096))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
	} else {
		state := c.ConnectionState()
		req.TLS = &state
		req.RemoteAddr = c.RemoteAddr().String()
		s.joinMux().ServeHTTP(w, req)
	}
	// The answer gets a deadline of its own: a write that fails halfway on the join's expired
	// one leaves the TLS record sequence out of step, and the client reads a decryption error
	// instead of the answer.
	c.SetWriteDeadline(time.Now().Add(joinWriteGrace))
	resp := &http.Response{
		StatusCode: w.code(), ProtoMajor: 1, ProtoMinor: 1, Header: w.h, Close: true,
		Body: io.NopCloser(&w.body), ContentLength: int64(w.body.Len()),
	}
	resp.Write(c)
}

// joinMux is the whole surface of a join connection: POST /peer/v1/join, and 404 or 405 for
// anything else, which includes every route of the port's main mux.
func (s *Server) joinMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+capability.PeerJoinPath, s.serveJoin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if len(r.Header.Values("Origin")) > 0 {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// serveJoin is POST /peer/v1/join: the secret of an unused code is consumed, the host's
// Admit pairs the certificate the connection presented, and the answer is the name the host
// gives itself and the device. Every refusal is a 403 with no detail and a log line with the
// address and the fingerprint's prefix; the secret and the proposed name are never logged.
func (s *Server) serveJoin(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_, fp, pinned := s.o.Host.Pin(r.TLS.PeerCertificates[0])
	refuse := func(why string) {
		fmt.Fprintf(s.o.Log, "droidyard: peer listener: refused a join from %s (fingerprint %s): %s\n", r.RemoteAddr, fp[:12], why)
		http.Error(w, "forbidden", http.StatusForbidden)
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, capability.PeerMaxJoinBody+1))
	switch {
	case err != nil:
		refuse("the request was not read")
		return
	case len(data) > capability.PeerMaxJoinBody:
		fmt.Fprintf(s.o.Log, "droidyard: peer listener: refused a join from %s (fingerprint %s): the body is over %d bytes\n", r.RemoteAddr, fp[:12], capability.PeerMaxJoinBody)
		http.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	var jr capability.PeerJoinRequest
	if dec := json.NewDecoder(bytes.NewReader(data)); dec.Decode(&jr) != nil {
		fmt.Fprintf(s.o.Log, "droidyard: peer listener: refused a join from %s (fingerprint %s): the body is not a join request\n", r.RemoteAddr, fp[:12])
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(jr.Secret)
	if err != nil || len(raw) != 16 {
		refuse("the secret is not one")
		return
	}
	var secret [16]byte
	copy(secret[:], raw)
	device, ok := s.o.Host.ConsumeCode(secret)
	if !ok {
		refuse("the code is not an unused one")
		return
	}
	peer, host, err := s.o.Admit(fp, jr.Name, device)
	if err != nil {
		refuse(fmt.Sprintf("the host did not admit it: %v", err))
		return
	}
	state := "paired now"
	if pinned {
		state = "already paired"
	}
	fmt.Fprintf(s.o.Log, "droidyard: peer listener: %s joined from %s (fingerprint %s, %s) and is lent %q\n", peer, r.RemoteAddr, fp[:12], state, device)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(capability.PeerJoinReply{Host: host, Device: device})
}

// joinWriter collects a handler's answer so serveJoinConn can write it on the connection
// without an http.Server.
type joinWriter struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (w *joinWriter) Header() http.Header { return w.h }

func (w *joinWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *joinWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(p)
}

func (w *joinWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// errNoJoinSlot is what a join hello fails with while every slot is taken.
var errNoJoinSlot = errors.New("every join slot is taken")
