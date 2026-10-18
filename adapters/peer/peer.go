// Package peer is the peer adapter: the listener a host serves its paired peers on
// (ADR-0098 §3). It is a second listener beside the UI's loopback one, never the UI's API
// on the network: TLS 1.3 with both certificates pinned, one route here (/peer/v1/ws) whose
// sessions are the UI's WebSocket loop started by a hello, and nothing else. It translates;
// what a peer may call is the core's Lendable set, not this package's to widen
// (docs/architecture/overview.md §Adapter boundary).
package peer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/adapters/httpapi"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// The listener's timers. Variables only so this package's tests can shorten them.
var (
	// retryEvery is how long a failed bind waits before it is tried again (ADR-0098 §3):
	// the VPN may come up after the daemon, and an address may appear with it.
	retryEvery = 30 * time.Second
	// sweepEvery is how often the pin is checked on the sockets already open, so a peer
	// removed from hosting.peers loses them within it (ADR-0098 §3).
	sweepEvery = 2 * time.Second
	// helloTimeout is how long a socket may take to send its hello.
	helloTimeout = 5 * time.Second
	// closeGrace is how long a close handshake gets before the connection is dropped.
	closeGrace = time.Second
)

// Host is what the listener needs of the daemon's peer state: *hosting.Host. The adapter
// declares it, so it imports nothing of the daemon's (docs/architecture/overview.md
// §Adapter boundary).
type Host interface {
	// Certificate is the identity the listener presents; ok is false when there is none.
	Certificate() (cert tls.Certificate, ok bool)
	// Fingerprint is the identity's, for the log.
	Fingerprint() string
	// Pin is the check of a certificate a peer presents against hosting.peers as the file
	// has it now: the name it is paired under, and its fingerprint whether or not it is.
	Pin(cert *x509.Certificate) (name, fingerprint string, ok bool)
	// SetListen reports the address the listener is bound to, or why it is not.
	SetListen(addr string, err error)
	// PeerOpened and PeerClosed count a peer's open sockets.
	PeerOpened(name string)
	PeerClosed(name string)
}

// errNotPinned is what a handshake whose client certificate is not paired fails with.
var errNotPinned = errors.New("the client's certificate is not in hosting.peers")

// Options configure a Server.
type Options struct {
	Host     Host
	Sessions *httpapi.PeerSessions
	// Listen is hosting.listen as read at daemon start; "" binds nothing.
	Listen string
	// API is this daemon's API version, named in the hello.
	API string
	Log io.Writer
}

// Server is the peer listener. Run binds it and keeps it bound until its context ends.
type Server struct {
	o Options

	mu    sync.Mutex
	socks map[*sock]struct{}
}

// sock is one open peer session: whose it is, and the connection to close on a revocation.
type sock struct {
	cert *x509.Certificate
	name string
	conn *websocket.Conn
}

// New returns a server for o. It binds nothing until Run.
func New(o Options) *Server {
	if o.Log == nil {
		o.Log = io.Discard
	}
	return &Server{o: o, socks: map[*sock]struct{}{}}
}

// Run binds hosting.listen and serves it until ctx ends, then closes the listener and every
// session and returns. A bind that fails (the VPN down at login, an address that is not
// this machine's) leaves no listener: it is reported to system.health and tried again every
// 30 s, and the daemon runs on (ADR-0098 §3).
func (s *Server) Run(ctx context.Context) {
	defer s.o.Sessions.CloseAll(context.WithoutCancel(ctx))
	if s.o.Listen == "" {
		return
	}
	cert, ok := s.o.Host.Certificate()
	if !ok {
		s.o.Host.SetListen("", fmt.Errorf("this daemon has no identity (daemon.log says why), so it cannot serve peers"))
		return
	}
	for ctx.Err() == nil {
		err := s.serveOnce(ctx, cert)
		if ctx.Err() != nil {
			break
		}
		s.o.Host.SetListen("", err)
		fmt.Fprintf(s.o.Log, "droidyard: the peer listener on %s is down: %v; trying again in %s\n", s.o.Listen, err, retryEvery)
		select {
		case <-ctx.Done():
		case <-time.After(retryEvery):
		}
	}
	s.o.Host.SetListen("", nil)
}

// serveOnce binds and serves until the listener fails or ctx ends.
func (s *Server) serveOnce(ctx context.Context, cert tls.Certificate) error {
	ln, err := net.Listen("tcp", s.o.Listen)
	if err != nil {
		return fmt.Errorf("binding hosting.listen %s: %w", s.o.Listen, err)
	}
	s.o.Host.SetListen(ln.Addr().String(), nil)
	fmt.Fprintf(s.o.Log, "droidyard: serving peers on %s (fingerprint %s)\n", ln.Addr(), s.o.Host.Fingerprint()[:12])

	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(s.o.Log, "droidyard: peer listener: ", 0),
		// An empty map keeps HTTP/2 off: a WebSocket needs HTTP/1.1.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	srv.SetKeepAlivesEnabled(false)

	var wg sync.WaitGroup
	sweepCtx, stopSweep := context.WithCancel(ctx)
	wg.Add(1)
	go func() { defer wg.Done(); s.sweep(sweepCtx) }()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(tls.NewListener(ln, s.tlsConfig(cert))) }()

	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	stopSweep()
	srv.Close()
	s.o.Sessions.EndAll(context.WithoutCancel(ctx)) // Run's deferred CloseAll latches at shutdown
	wg.Wait()
	if err == nil || err == http.ErrServerClosed {
		err = fmt.Errorf("the listener stopped")
	}
	return err
}

// tlsConfig is the listener's TLS: 1.3 only, a client certificate required, and the pin
// checked on each handshake against hosting.peers as the file has it now. A handshake that
// fails the pin is logged with the fingerprint's prefix and the address (ADR-0098 §3).
func (s *Server) tlsConfig(cert tls.Certificate) *tls.Config {
	base := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{"http/1.1"},
	}
	base.GetConfigForClient = func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
		c := base.Clone()
		c.GetConfigForClient = nil
		addr := chi.Conn.RemoteAddr().String()
		c.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errNotPinned
			}
			if _, fp, ok := s.o.Host.Pin(cs.PeerCertificates[0]); !ok {
				fmt.Fprintf(s.o.Log, "droidyard: peer listener: refused a handshake from %s: fingerprint %s is not in hosting.peers\n", addr, fp[:12])
				return errNotPinned
			}
			return nil
		}
		return c, nil
	}
	return base
}

// handler is the peer port's whole surface: a request carrying an Origin is refused (a
// browser has no business here), the pin is checked again, and only /peer/v1/ws exists.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(capability.PeerPath, s.serveWS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if len(r.Header.Values("Origin")) > 0 {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if _, _, ok := s.requestPeer(r); !ok {
			http.Error(w, "not paired", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// requestPeer is the peer a request's certificate pins to now.
func (s *Server) requestPeer(r *http.Request) (name string, cert *x509.Certificate, ok bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", nil, false
	}
	cert = r.TLS.PeerCertificates[0]
	name, _, ok = s.o.Host.Pin(cert)
	return name, cert, ok
}

// serveWS is GET /peer/v1/ws.
func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	name, cert, ok := s.requestPeer(r)
	if !ok {
		http.Error(w, "not paired", http.StatusForbidden)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has answered
	}
	k := &sock{cert: cert, name: name, conn: c}
	s.mu.Lock()
	s.socks[k] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.socks, k)
		s.mu.Unlock()
	}()

	opened := false
	defer func() {
		if opened {
			s.o.Host.PeerClosed(name)
			fmt.Fprintf(s.o.Log, "droidyard: peer %s: link down (%s)\n", name, r.RemoteAddr)
		}
	}()
	s.o.Sessions.Serve(r.Context(), c, httpapi.Peer{
		Name: name,
		Allowed: func() bool {
			if now, _, ok := s.o.Host.Pin(k.cert); ok && now == k.name {
				return true
			}
			go revoke(c)
			return false
		},
	}, func(ctx context.Context) bool {
		if !s.hello(ctx, c, name) {
			return false
		}
		opened = true
		s.o.Host.PeerOpened(name)
		fmt.Fprintf(s.o.Log, "droidyard: peer %s: link up (%s)\n", name, r.RemoteAddr)
		return true
	})
}

// hello reads the peer's hello and answers it with this daemon's. A peerProtocol that is not
// this one is refused with the close code that says so; a different API version is logged
// and the session goes on (ADR-0098 §4).
func (s *Server) hello(ctx context.Context, c *websocket.Conn, name string) bool {
	timer := time.AfterFunc(helloTimeout, func() { c.Close(capability.PeerCloseNoHello, "no hello in time") })
	typ, data, err := c.Read(ctx)
	if !timer.Stop() {
		return false // the deadline passed: the timer is closing the connection
	}
	if err != nil {
		c.CloseNow()
		return false
	}
	var f capability.PeerHelloFrame
	if typ != websocket.MessageText || json.Unmarshal(data, &f) != nil || f.Hello == nil {
		c.Close(capability.PeerCloseProtocol, "a hello was expected")
		return false
	}
	if f.Hello.PeerProtocol != capability.PeerProtocol {
		fmt.Fprintf(s.o.Log, "droidyard: peer %s speaks peer protocol %d and this host %d; refused\n", name, f.Hello.PeerProtocol, capability.PeerProtocol)
		c.Close(capability.PeerCloseProtocol, fmt.Sprintf("peerProtocol %d is not supported; this host speaks %d", f.Hello.PeerProtocol, capability.PeerProtocol))
		return false
	}
	if f.Hello.API != s.o.API {
		fmt.Fprintf(s.o.Log, "droidyard: peer %s runs API %s and this host %s; the peer protocol is the same, so the link is kept\n", name, f.Hello.API, s.o.API)
	}
	out, _ := json.Marshal(capability.PeerHelloFrame{Hello: &capability.PeerHello{PeerProtocol: capability.PeerProtocol, API: s.o.API}})
	wctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	if c.Write(wctx, websocket.MessageText, out) != nil {
		c.CloseNow()
		return false
	}
	return true
}

// sweep closes the sockets of a peer that is no longer paired, every sweepEvery, so a
// revocation reaches a socket that sends nothing (ADR-0098 §3).
func (s *Server) sweep(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		open := make([]*sock, 0, len(s.socks))
		for k := range s.socks {
			open = append(open, k)
		}
		s.mu.Unlock()
		for _, k := range open {
			if now, _, ok := s.o.Host.Pin(k.cert); !ok || now != k.name {
				fmt.Fprintf(s.o.Log, "droidyard: peer %s is no longer paired; its link is closed\n", k.name)
				go revoke(k.conn)
			}
		}
	}
}

// revoke closes a peer's connection with the reason, and drops it if the peer does not
// answer the close within closeGrace.
func revoke(c *websocket.Conn) {
	done := make(chan struct{})
	go func() {
		c.Close(capability.PeerCloseRevoked, "no longer paired")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeGrace):
		c.CloseNow()
	}
}
