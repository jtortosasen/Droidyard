// Package peer is the peer adapter: the listeners a host serves its paired peers on
// (ADR-0098 §3, ADR-0109). They are further listeners beside the UI's loopback one, never
// the UI's API on the network, and the set of them follows the daemon's configuration and
// its interfaces (Server.Reconcile). Each is TLS 1.3 with both certificates pinned, with two
// routes here (/peer/v1/ws, whose sessions are the UI's WebSocket loop started by a hello,
// /peer/v1/screen, a live view's video, /peer/v1/upload, an APK for device.installUpload,
// and /peer/v1/artifacts/{id}, the bytes of an artifact one of the peer's own calls made),
// and nothing else. The one exception is the join (ADR-0107, join.go): a hello offering
// droidyard-join/1 may present a certificate that is not paired while a code is unused, and
// that connection is served one POST /peer/v1/join and closed. It translates;
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
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/adapters/httpapi"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// The listeners' timers. Variables only so this package's tests can shorten them.
var (
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
	// sessionCloseDelay holds each session socket's close back when a listener shuts down. It
	// is zero outside a test, which sets it to put the session's close after a live view's.
	sessionCloseDelay time.Duration
	// uploadBound is how long an upload's body may take to arrive: a little over the 10
	// minutes a client allows itself, so a stalled sender is cut and its file removed.
	uploadBound = 11 * time.Minute
	// artifactBound is how long an artifact's body may take to leave: a peer that reads no
	// more of it is cut and its connection freed.
	artifactBound = 2 * time.Minute
)

// Host is what the listeners need of the daemon's peer state: *hosting.Host. The adapter
// declares it, so it imports nothing of the daemon's (docs/architecture/overview.md
// §Adapter boundary).
type Host interface {
	// Certificate is the identity the listeners present; ok is false when there is none.
	Certificate() (cert tls.Certificate, ok bool)
	// Fingerprint is the identity's, for the log.
	Fingerprint() string
	// Pin is the check of a certificate a peer presents against hosting.peers as the file
	// has it now: the name it is paired under, and its fingerprint whether or not it is.
	Pin(cert *x509.Certificate) (name, fingerprint string, ok bool)
	// SetListening reports the addresses the listeners are bound to, in the order wanted,
	// and why each address that could not be bound is not (keyed by the address wanted).
	SetListening(bound []string, errs map[string]error)
	// PeerOpened and PeerClosed count a peer's open sockets.
	PeerOpened(name string)
	PeerClosed(name string)
	// CodePending reports whether any join code is unused: only then does a handshake that
	// offers the join protocol accept a certificate that is not paired (ADR-0107 §3).
	CodePending() bool
	// ConsumeCode uses up the secret of an unused code, at most once, and names the device
	// it was issued for. It is false too for a code whose use the host could not record on
	// disk (ADR-0108 §2): the code is void then, and the join is refused.
	ConsumeCode(secret [16]byte) (device string, ok bool)
}

// Admit is the host's side of an accepted join (ADR-0107 §3), called once a secret was
// consumed: it pairs the certificate with the fingerprint under a name made from the one
// the joining side proposed (a text from another machine, which the policy cleans), lends
// it the device, and answers the name this host gives itself. An error refuses the join.
type Admit func(fingerprint, proposedName, device string) (peer, host string, err error)

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
	// Screens claims a live view's stream for the peer it was issued to; nil serves no
	// video, and /peer/v1/screen answers 404.
	Screens httpapi.PeerScreenAttach
	// Uploads stores an APK a peer uploads; nil takes none, and /peer/v1/upload answers 404.
	Uploads Uploads
	// Artifacts serves a peer the artifacts of its own calls; nil serves none, and
	// /peer/v1/artifacts/ answers 404.
	Artifacts ArtifactReader
	// Admit pairs a peer that joined with a code; nil serves no join, and a hello that
	// offers the join protocol is refused at the handshake.
	Admit Admit
	// SetFast records whether a peer's hello said its remote is marked fast (ADR-0118 §4);
	// nil records nothing.
	SetFast func(peer string, fast bool)
	// API is this daemon's API version, named in the hello.
	API string
	Log io.Writer
}

// Server is the set of peer listeners, one per address wanted (ADR-0109 §3). It binds
// nothing until Reconcile.
type Server struct {
	o Options

	// joinSlots holds one token per join connection being served or handshaking (ADR-0107
	// §4), across every listener; a token is returned when the connection closes.
	joinSlots   chan struct{}
	joinTimeout time.Duration // the package's joinTimeout when the server was made

	// recMu serialises Reconcile and Close, and guards what follows.
	recMu     sync.Mutex
	listeners map[netip.AddrPort]*listener // by the address wanted, which may name port 0
	closed    bool
	told      map[netip.AddrPort]string // the bind error last logged per address, so a retry is silent
}

// listener is one bound address: its own sockets, its own sweep, its own end.
type listener struct {
	s    *Server
	want netip.AddrPort
	addr netip.AddrPort // what it is bound to; the port is the one taken when want names 0
	srv  *http.Server

	stopSweep context.CancelFunc
	sweepDone chan struct{}
	served    chan struct{} // closed when srv.Serve returned

	mu       sync.Mutex
	socks    map[*sock]struct{}
	handlers sync.WaitGroup // the WebSocket handlers (sessions, live views), which the end waits for
	closing  bool           // the listener is stopping: no socket is taken
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

// New returns a server for o. It binds nothing until Reconcile.
func New(o Options) *Server {
	if o.Log == nil {
		o.Log = io.Discard
	}
	return &Server{o: o, joinSlots: make(chan struct{}, maxJoins), joinTimeout: joinTimeout,
		listeners: map[netip.AddrPort]*listener{}, told: map[netip.AddrPort]string{}}
}

// Reconcile makes the listeners the addresses in want: it binds the new, closes the gone
// (closing one ends only the sockets it accepted), and keeps the rest. A bind that fails
// (the VPN down, an address that is not this machine's, a port in use) leaves the other
// listeners as they are and is in errs, keyed by the address wanted; it is tried again at
// the next call. A listener that stopped on its own is replaced the same way. bound are the
// addresses actually bound, in the order wanted; a want naming port 0 is bound to a port the
// system chose. The Host is told the same (ADR-0098 §3, ADR-0109 §3). After Close it does
// nothing.
func (s *Server) Reconcile(want []netip.AddrPort) (bound []netip.AddrPort, errs map[netip.AddrPort]error) {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.closed {
		return nil, nil
	}
	want = unique(want)
	for key, l := range s.listeners {
		dead := false
		select {
		case <-l.served:
			dead = true
		default:
		}
		if !dead && slices.ContainsFunc(want, l.serves) {
			continue
		}
		if dead {
			fmt.Fprintf(s.o.Log, "droidyard: the peer listener on %s stopped; binding it again\n", l.addr)
		} else {
			fmt.Fprintf(s.o.Log, "droidyard: no longer serving peers on %s\n", l.addr)
		}
		l.shutdown()
		delete(s.listeners, key)
	}
	errs = map[netip.AddrPort]error{}
	cert, haveCert := s.o.Host.Certificate()
	for _, w := range want {
		if s.serving(w) != nil {
			delete(s.told, w)
			continue
		}
		var err error
		if !haveCert {
			err = fmt.Errorf("this daemon has no identity (daemon.log says why), so it cannot serve peers")
		} else if l, berr := s.bind(w, cert); berr != nil {
			err = berr
		} else {
			s.listeners[w] = l
			delete(s.told, w)
			continue
		}
		errs[w] = err
		// Logged when it first happens or changes, not at each retry.
		if text := err.Error(); s.told[w] != text {
			s.told[w] = text
			fmt.Fprintf(s.o.Log, "droidyard: the peer listener on %s is down: %v; it is tried again at the next check\n", w, err)
		}
	}
	for w := range s.told {
		if !slices.Contains(want, w) {
			delete(s.told, w)
		}
	}
	// Reported in the order wanted, so the first of each is stable.
	var listening []string
	for _, w := range want {
		if l := s.serving(w); l != nil && !slices.Contains(bound, l.addr) {
			bound = append(bound, l.addr)
			listening = append(listening, l.addr.String())
		}
	}
	reported := make(map[string]error, len(errs))
	for w, err := range errs {
		reported[w.String()] = err
	}
	s.o.Host.SetListening(listening, reported)
	return bound, errs
}

// serves reports whether the listener is what w asks for: the address it was asked to bind,
// or the one it took (a want naming port 0 and the same address with the port taken are one
// listener, so a port learned is not a change).
func (l *listener) serves(w netip.AddrPort) bool { return l.want == w || l.addr == w }

// serving is the listener that serves w, nil if none does.
func (s *Server) serving(w netip.AddrPort) *listener {
	for _, l := range s.listeners {
		if l.serves(w) {
			return l
		}
	}
	return nil
}

// unique is want without repeats, in its order.
func unique(want []netip.AddrPort) []netip.AddrPort {
	out := make([]netip.AddrPort, 0, len(want))
	for _, w := range want {
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// Close closes every listener and every session, and refuses any later Reconcile and any
// later session. It waits for them, and is safe to call more than once.
func (s *Server) Close() {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for w, l := range s.listeners {
		l.shutdown()
		delete(s.listeners, w)
	}
	s.o.Sessions.CloseAll(context.Background())
	s.o.Host.SetListening(nil, nil)
}

// bind opens one listener and starts serving it.
func (s *Server) bind(want netip.AddrPort, cert tls.Certificate) (*listener, error) {
	ln, err := net.Listen("tcp", want.String())
	if err != nil {
		return nil, fmt.Errorf("binding %s: %w", want, err)
	}
	addr, _ := netip.ParseAddrPort(ln.Addr().String())
	l := &listener{s: s, want: want, addr: addr, socks: map[*sock]struct{}{}, sweepDone: make(chan struct{}), served: make(chan struct{})}
	fmt.Fprintf(s.o.Log, "droidyard: serving peers on %s (fingerprint %s)\n", ln.Addr(), s.o.Host.Fingerprint()[:12])

	l.srv = &http.Server{
		Handler:           l.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(s.o.Log, "droidyard: peer listener: ", 0),
		// An empty map keeps HTTP/2 off: a WebSocket needs HTTP/1.1.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	if s.o.Admit != nil {
		// A connection that negotiated the join protocol is served one request here and
		// closed; any other protocol the server has no entry for closes the connection.
		l.srv.TLSNextProto[capability.PeerJoinALPN] = s.serveJoinConn
	}
	l.srv.SetKeepAlivesEnabled(false)

	sweepCtx, stop := context.WithCancel(context.Background())
	l.stopSweep = stop
	go func() { defer close(l.sweepDone); l.sweep(sweepCtx) }()
	go func() {
		defer close(l.served)
		l.srv.Serve(tls.NewListener(&joinListener{Listener: ln, s: s}, s.tlsConfig(cert)))
	}()
	return l, nil
}

// shutdown ends the listener and only the sockets it accepted, and waits for them: the
// sessions are closed with the going-away code, then the live views, an upload or an artifact
// in flight is cancelled, and a socket that does not answer the close within closeGrace is
// dropped. Another listener's sockets are not touched.
func (l *listener) shutdown() {
	l.mu.Lock()
	l.closing = true
	open := make([]*sock, 0, len(l.socks))
	for k := range l.socks {
		open = append(open, k)
	}
	l.mu.Unlock()
	l.stopSweep()
	l.srv.Close() // the listening socket, and the connections that were not hijacked
	var sessions, views []*sock
	for _, k := range open {
		switch {
		case k.conn == nil:
			k.cancel()
		case k.screen:
			views = append(views, k)
		default:
			sessions = append(sessions, k)
		}
	}
	// The sessions first, and the views only once every session's close is out (or dropped):
	// a view's socket closing makes its stream publish stopped{detached} on the peer's
	// subscription, and that event must find the link already closed, or the client reads it
	// as the host ending the stream (failed) and not as the link lost (deviceGone, as
	// internal/domain/device/remotescreen.go says). A close frame written, or a connection
	// dropped, takes the session's writes with it.
	closeAll(sessions, sessionCloseDelay)
	closeAll(views, 0)
	l.handlers.Wait()
	<-l.sweepDone
	<-l.served
}

// closeAll closes every socket of socks with the going-away code, after holding back by
// delay, and returns when each is closed: its close handshake done, or the connection dropped
// after closeGrace.
func closeAll(socks []*sock, delay time.Duration) {
	var wg sync.WaitGroup
	for _, k := range socks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(delay)
			done := make(chan struct{})
			go func() { k.conn.Close(websocket.StatusGoingAway, "the peer listener stopped"); close(done) }()
			select {
			case <-done:
			case <-time.After(closeGrace):
				k.conn.CloseNow()
			}
		}()
	}
	wg.Wait()
}

// track registers a socket the listener accepted; false when the listener is stopping.
func (l *listener) track(k *sock) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return false
	}
	l.socks[k] = struct{}{}
	if k.conn != nil {
		l.handlers.Add(1)
	}
	return true
}

func (l *listener) untrack(k *sock) {
	l.mu.Lock()
	delete(l.socks, k)
	l.mu.Unlock()
	if k.conn != nil {
		l.handlers.Done()
	}
}

// tlsConfig is the listeners' TLS: 1.3 only, a client certificate required, and the pin
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
		if s.o.Admit != nil && slices.Contains(chi.SupportedProtos, capability.PeerJoinALPN) {
			return s.joinConfig(c, chi)
		}
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
func (l *listener) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(capability.PeerPath, l.serveWS)
	mux.HandleFunc(capability.PeerScreenPath, l.serveScreen)
	mux.HandleFunc("PUT "+capability.PeerUploadPath, l.serveUpload)
	mux.HandleFunc("GET "+capability.PeerArtifactsPath+"{id}", l.serveArtifact)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if len(r.Header.Values("Origin")) > 0 {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if _, _, ok := l.s.requestPeer(r); !ok {
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
func (l *listener) serveWS(w http.ResponseWriter, r *http.Request) {
	s := l.s
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
	if !l.track(k) { // the listener is stopping: shutdown has already counted the open ones
		c.Close(websocket.StatusGoingAway, "the peer listener stopped")
		return
	}
	defer l.untrack(k)

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
func (l *listener) serveScreen(w http.ResponseWriter, r *http.Request) {
	s := l.s
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
	if !l.track(k) { // the listener is stopping: shutdown has already counted the open ones
		c.Close(websocket.StatusGoingAway, "the peer listener stopped")
		return
	}
	defer l.untrack(k)
	httpapi.ServePeerScreen(r.Context(), c, name, streamFrameTimeout, s.o.Screens)
}

// serveUpload is PUT /peer/v1/upload: an APK for the peer's next device.installUpload
// (ADR-0099 §3). The length must be declared and at most capability.PeerMaxUpload, which is
// answered before a byte of the body is read; the body is stored by Uploads, which keeps
// nothing of one that fails. The request is in the revocation sweep like a socket, so a peer
// removed from hosting.peers loses an upload in progress within sweepEvery, and its body is
// cut off after uploadBound whatever it does.
func (l *listener) serveUpload(w http.ResponseWriter, r *http.Request) {
	s := l.s
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
	if !l.track(k) {
		http.Error(w, "the peer listener is stopping", http.StatusServiceUnavailable)
		return
	}
	defer l.untrack(k)
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
func (l *listener) serveArtifact(w http.ResponseWriter, r *http.Request) {
	s := l.s
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
	if !l.track(k) {
		http.Error(w, "the peer listener is stopping", http.StatusServiceUnavailable)
		return
	}
	defer l.untrack(k)
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
	if s.o.SetFast != nil {
		s.o.SetFast(name, f.Hello.Fast) // read when the peer starts a live view (ADR-0118 §4)
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
func (l *listener) sweep(ctx context.Context) {
	s := l.s
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		l.mu.Lock()
		open := make([]*sock, 0, len(l.socks))
		for k := range l.socks {
			open = append(open, k)
		}
		l.mu.Unlock()
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
