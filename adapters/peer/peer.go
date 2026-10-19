// Package peer is the peer adapter: the listener a host serves its paired peers on
// (ADR-0098 §3). It is a second listener beside the UI's loopback one, never the UI's API
// on the network: TLS 1.3 with both certificates pinned, two routes here (/peer/v1/ws,
// whose sessions are the UI's WebSocket loop started by a hello, /peer/v1/screen, a
// live view's video, /peer/v1/upload, an APK for device.installUpload, and
// /peer/v1/artifacts/{id}, the bytes of an artifact one of the peer's own calls made), and
// nothing else. It translates;
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
	"strconv"
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
	// streamFrameTimeout is how long a screen socket may take to send its stream id
	// (ADR-0078 §4).
	streamFrameTimeout = 5 * time.Second
	// closeGrace is how long a close handshake gets before the connection is dropped.
	closeGrace = time.Second
	// uploadBound is how long an upload's body may take to arrive: a little over the 10
	// minutes a client allows itself, so a stalled sender is cut and its file removed.
	uploadBound = 11 * time.Minute
	// artifactBound is how long an artifact's body may take to leave: a peer that reads no
	// more of it is cut and its connection freed.
	artifactBound = 2 * time.Minute
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

// Uploads stores the APKs a peer uploads (ADR-0099 §3): *hosting.Uploads. Put reads exactly
// size bytes of body, which is never larger than capability.PeerMaxUpload, as the named
// peer's only pending upload, and returns the id it is known by and its SHA-256.
type Uploads interface {
	Put(peer string, size int64, body io.Reader) (id, sha256 string, err error)
	// Forget drops the peer's pending upload: it was unpaired.
	Forget(peer string)
}

// Artifact is what a peer may fetch of the host's store: the bytes of an artifact it owns.
type Artifact struct {
	MediaType string
	Untrusted bool
	Data      []byte
}

// ArtifactReader resolves an artifact id for the named peer (ADR-0099 §2): the artifact only
// if one of that peer's own calls made it. Every other id, a durable one, another peer's or
// the host's own included, is a capability.ErrArtifactNotFound error, the same as an id that
// never was; one that has expired is capability.ErrArtifactExpired. The daemon's wiring
// supplies it over the artifact store.
type ArtifactReader func(peer, id string) (Artifact, error)

// errNotPinned is what a handshake whose client certificate is not paired fails with.
var errNotPinned = errors.New("the client's certificate is not in hosting.peers")

// Options configure a Server.
type Options struct {
	Host     Host
	Sessions *httpapi.PeerSessions
	// Listen is hosting.listen as read at daemon start; "" binds nothing.
	Listen string
	// Screens claims a live view's stream for the peer it was issued to; nil serves no
	// video, and /peer/v1/screen answers 404.
	Screens httpapi.PeerScreenAttach
	// Uploads stores an APK a peer uploads; nil takes none, and /peer/v1/upload answers 404.
	Uploads Uploads
	// Artifacts serves a peer the artifacts of its own calls; nil serves none, and
	// /peer/v1/artifacts/ answers 404.
	Artifacts ArtifactReader
	// API is this daemon's API version, named in the hello.
	API string
	Log io.Writer
}

// Server is the peer listener. Run binds it and keeps it bound until its context ends.
type Server struct {
	o Options

	mu      sync.Mutex
	socks   map[*sock]struct{}
	screens sync.WaitGroup // the screen sockets' handlers, which the listener's end waits for
	closing bool           // the listener is stopping: no screen socket is taken
}

// sock is one open peer connection, a session, a live view's video or an upload in progress:
// whose it is, and what ends it on a revocation (the WebSocket is closed with the reason;
// an upload is cancelled).
type sock struct {
	cert   *x509.Certificate
	name   string
	conn   *websocket.Conn    // nil for an upload
	cancel context.CancelFunc // set for an upload
	screen bool
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
	s.mu.Lock()
	s.closing = false
	s.mu.Unlock()
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
	s.closeScreens()
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
// browser has no business here), the pin is checked again, and only /peer/v1/ws,
// /peer/v1/screen, /peer/v1/upload and /peer/v1/artifacts/{id} exist.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(capability.PeerPath, s.serveWS)
	mux.HandleFunc(capability.PeerScreenPath, s.serveScreen)
	mux.HandleFunc("PUT "+capability.PeerUploadPath, s.serveUpload)
	mux.HandleFunc("GET "+capability.PeerArtifactsPath+"{id}", s.serveArtifact)
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
			s.forget(k.name)
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

// serveScreen is GET /peer/v1/screen: one live view's video for the peer whose certificate
// is pinned now. The socket is in the revocation sweep like a session, so a peer removed
// from hosting.peers loses it within sweepEvery (ADR-0098 §3).
func (s *Server) serveScreen(w http.ResponseWriter, r *http.Request) {
	name, cert, ok := s.requestPeer(r)
	if !ok {
		http.Error(w, "not paired", http.StatusForbidden)
		return
	}
	if s.o.Screens == nil {
		http.NotFound(w, r)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has answered
	}
	k := &sock{cert: cert, name: name, conn: c, screen: true}
	s.mu.Lock()
	if s.closing { // the listener is stopping: closeScreens has already counted the open ones
		s.mu.Unlock()
		c.Close(websocket.StatusGoingAway, "the peer listener stopped")
		return
	}
	s.socks[k] = struct{}{}
	s.screens.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.socks, k)
		s.mu.Unlock()
		s.screens.Done()
	}()
	httpapi.ServePeerScreen(r.Context(), c, name, streamFrameTimeout, s.o.Screens)
}

// serveUpload is PUT /peer/v1/upload: an APK for the peer's next device.installUpload
// (ADR-0099 §3). The length must be declared and at most capability.PeerMaxUpload, which is
// answered before a byte of the body is read; the body is stored by Uploads, which keeps
// nothing of one that fails. The request is in the revocation sweep like a socket, so a peer
// removed from hosting.peers loses an upload in progress within sweepEvery, and its body is
// cut off after uploadBound whatever it does.
func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request) {
	name, cert, ok := s.requestPeer(r)
	if !ok {
		http.Error(w, "not paired", http.StatusForbidden)
		return
	}
	if s.o.Uploads == nil {
		http.NotFound(w, r)
		return
	}
	// In the sweep before any work, so a peer unpaired in between is caught.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	k := &sock{cert: cert, name: name, cancel: cancel}
	s.mu.Lock()
	s.socks[k] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.socks, k)
		s.mu.Unlock()
	}()
	switch {
	case r.ContentLength < 0:
		http.Error(w, "a Content-Length is required", http.StatusLengthRequired)
		return
	case r.ContentLength == 0:
		http.Error(w, "an empty upload", http.StatusBadRequest)
		return
	case r.ContentLength > capability.PeerMaxUpload:
		fmt.Fprintf(s.o.Log, "droidyard: peer %s: an upload of %d bytes was refused; the limit is %d\n", name, r.ContentLength, int64(capability.PeerMaxUpload))
		http.Error(w, "the upload is over the limit", http.StatusRequestEntityTooLarge)
		return
	}
	// A body read blocks on the connection, which a context does not reach: the deadline does.
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(uploadBound))
	stop := context.AfterFunc(ctx, func() { rc.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()

	id, sum, err := s.o.Uploads.Put(name, r.ContentLength, r.Body)
	if err != nil {
		fmt.Fprintf(s.o.Log, "droidyard: peer %s: an upload of %d bytes was not stored: %v\n", name, r.ContentLength, err)
		if errors.Is(err, capability.ErrPeerUploadBusy) {
			http.Error(w, "an earlier upload is still arriving", http.StatusConflict)
			return
		}
		http.Error(w, "the upload was not stored", http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(s.o.Log, "droidyard: peer %s: stored an upload of %d bytes, sha256 %s\n", name, r.ContentLength, sum[:12])
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(capability.PeerUploadReply{Upload: id, SHA256: sum})
}

// serveArtifact is GET /peer/v1/artifacts/{id}: the stored bytes of an artifact one of the
// calling peer's own operations produced (ADR-0099 §2), with the media type and, for
// untrusted content, X-Droidyard-Untrusted: true as GET /v1/artifacts/{id} marks it. The
// reader decides ownership; this route only translates its answer, and an artifact the peer
// does not own is told apart from one that never was by nobody. The request is in the
// revocation sweep like an upload, and its body is cut off after artifactBound.
func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request) {
	name, cert, ok := s.requestPeer(r)
	if !ok {
		http.Error(w, "not paired", http.StatusForbidden)
		return
	}
	if s.o.Artifacts == nil {
		http.NotFound(w, r)
		return
	}
	// In the sweep before any work, so a peer unpaired in between is caught.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	k := &sock{cert: cert, name: name, cancel: cancel}
	s.mu.Lock()
	s.socks[k] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.socks, k)
		s.mu.Unlock()
	}()
	a, err := s.o.Artifacts(name, r.PathValue("id"))
	if err != nil {
		var ce *capability.Error
		switch {
		case !errors.As(err, &ce):
			http.Error(w, "the artifact cannot be served", http.StatusInternalServerError)
		case ce.Code == capability.ErrArtifactNotFound:
			http.Error(w, "no such artifact", http.StatusNotFound)
		case ce.Code == capability.ErrArtifactExpired:
			http.Error(w, "the artifact is gone", http.StatusGone)
		default:
			http.Error(w, "the artifact cannot be served", http.StatusInternalServerError)
		}
		return
	}
	// The types a store holds for the peer-lendable producers; anything else is not served.
	switch a.MediaType {
	case "image/png", "image/jpeg", "text/plain":
	default:
		http.Error(w, "the artifact cannot be served", http.StatusInternalServerError)
		return
	}
	if ctx.Err() != nil {
		http.Error(w, "not paired", http.StatusForbidden) // unpaired before the answer
		return
	}
	// A body write blocks on the connection, which a context does not reach: the deadline does.
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Now().Add(artifactBound))
	stop := context.AfterFunc(ctx, func() { rc.SetWriteDeadline(time.Unix(1, 0)) })
	defer stop()

	h := w.Header()
	if a.MediaType == "text/plain" {
		h.Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		h.Set("Content-Type", a.MediaType)
	}
	h.Set("Cache-Control", "no-store")
	if a.Untrusted {
		h.Set("X-Droidyard-Untrusted", "true")
	}
	h.Set("Content-Length", strconv.Itoa(len(a.Data)))
	w.Write(a.Data)
}

// closeScreens ends every screen socket and waits for their handlers: the listener stopped,
// and a hijacked connection is not the HTTP server's to close. A socket that does not answer
// the close within closeGrace is dropped.
func (s *Server) closeScreens() {
	s.mu.Lock()
	s.closing = true
	var open []*websocket.Conn
	for k := range s.socks {
		if k.screen {
			open = append(open, k.conn)
		}
	}
	s.mu.Unlock()
	for _, c := range open {
		go func() {
			done := make(chan struct{})
			go func() { c.Close(websocket.StatusGoingAway, "the peer listener stopped"); close(done) }()
			select {
			case <-done:
			case <-time.After(closeGrace):
				c.CloseNow()
			}
		}()
	}
	s.screens.Wait()
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
				s.forget(k.name)
				if k.conn == nil {
					k.cancel()
					continue
				}
				go revoke(k.conn)
			}
		}
	}
}

// forget drops what an unpaired peer left on this host: its pending upload, which a name
// paired again with another certificate must not find (ADR-0099 §3).
func (s *Server) forget(name string) {
	if s.o.Uploads != nil {
		s.o.Uploads.Forget(name)
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
