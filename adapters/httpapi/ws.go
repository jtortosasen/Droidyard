package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/event"
)

// wsPath is the WebSocket route (http-websocket.md §GET /v1/ws).
const wsPath = "/v1/ws"

// The defaults of the WebSocket session's timers: the auth deadline of rule 4, and the
// 30 s ping of §GET /v1/ws.
const (
	defaultAuthTimeout  = 5 * time.Second
	defaultPingInterval = 30 * time.Second
)

// preAuthLimit is the largest frame a connection may send before it has authenticated:
// the auth frame is a token in a small object, and 1 MiB per unauthenticated
// connection for 5 s is an allocation nobody asked for.
const preAuthLimit = 4 << 10

// closeUnauthorized is the close code of a connection that did not authenticate with
// its first frame (rule 4). Codes 4000 to 4999 are the application's.
const closeUnauthorized websocket.StatusCode = 4401

// SetAuthTimeout sets how long a new WebSocket may take to send its auth frame; d <= 0
// is the default, 5 s. Test-only.
func (s *Server) SetAuthTimeout(d time.Duration) {
	if s.ws != nil {
		s.ws.authTimeout.Store(int64(d))
	}
	s.screen.authTimeout.Store(int64(d))
}

// SetPingInterval sets the WebSocket ping interval of connections accepted from now
// on; d <= 0 is the default, 30 s. Two missed pongs close a connection. Test-only.
func (s *Server) SetPingInterval(d time.Duration) {
	if s.ws != nil {
		s.ws.pingInterval.Store(int64(d))
	}
}

// SetQueue sets how many envelopes each subscription of a WebSocket accepted from now
// on queues before it drops into a gap; n <= 0 is the bus's. Test-only.
func (s *Server) SetQueue(n int) {
	if s.ws != nil {
		s.ws.queue.Store(int64(n))
	}
}

// SetWriteHook makes every frame write of every WebSocket call f first, holding the
// session's write lock: blocking in f holds that session's writer, so a subscription's
// queue fills deterministically. Test-only. Nil removes it.
func (s *Server) SetWriteHook(f func()) {
	if s.ws == nil {
		return
	}
	if f == nil {
		s.ws.writeHook.Store(nil)
		return
	}
	s.ws.writeHook.Store(&f)
}

// wsHandler is GET /v1/ws: one connection is one session (ADR-0019 §Sessions) of client
// ui, which can carry events (plan m1d, D6).
type wsHandler struct {
	call  *caller
	bus   *event.Bus
	token []byte

	authTimeout, pingInterval, queue atomic.Int64
	writeHook                        atomic.Pointer[func()]

	// live are the open sessions, closed by CloseSessions when the daemon stops: a
	// hijacked connection is not the HTTP server's to wait for. ended is how many are
	// still running their session.
	mu      sync.Mutex
	live    map[*wsSession]struct{}
	closing bool
	ended   sync.WaitGroup
}

func newWSHandler(call *caller, bus *event.Bus, token string) *wsHandler {
	return &wsHandler{call: call, bus: bus, token: []byte(token), live: map[*wsSession]struct{}{}}
}

func (h *wsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The fence has already refused every Host and Origin that is not one of the two
	// allowed pairs of the bound port, and required an Origin. The library's own origin
	// check compares the Origin's host with the Host header and would refuse
	// Origin http://localhost:<p> on Host 127.0.0.1:<p>, which rule 3 allows; it is
	// relaxed to exactly those two origins, from the port this connection arrived on.
	a, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok || a.Port == 0 {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	p := strconv.Itoa(a.Port)
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"http://127.0.0.1:" + p, "http://localhost:" + p},
	})
	if err != nil {
		return // Accept has answered
	}
	c.SetReadLimit(preAuthLimit) // raised to MaxBody once the connection has authenticated
	s := &wsSession{h: h, c: c, started: map[string]bool{}}
	if !h.add(s) {
		c.Close(websocket.StatusGoingAway, "the daemon is stopping")
		return
	}
	defer h.remove(s)
	s.run(r.Context())
}

func (h *wsHandler) add(s *wsSession) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	h.live[s] = struct{}{}
	h.ended.Add(1)
	return true
}

func (h *wsHandler) remove(s *wsSession) {
	h.mu.Lock()
	delete(h.live, s)
	h.mu.Unlock()
	h.ended.Done()
}

// Sessions is how many WebSocket sessions are open. Test-only.
func (s *Server) Sessions() int {
	if s.ws == nil {
		return 0
	}
	s.ws.mu.Lock()
	defer s.ws.mu.Unlock()
	return len(s.ws.live)
}

// CloseSessions ends every open WebSocket session and refuses new ones, and returns
// when they have ended or ctx is done, in which case their connections are dropped
// without a close frame. The daemon calls it when it shuts down: the WebSocket sessions
// close with the others (plan m1d, R10). It is called from the start of the shutdown,
// not registered on first use, so a connection upgrading during it is refused too.
func (s *Server) CloseSessions(ctx context.Context) {
	var wg sync.WaitGroup
	if s.ws != nil {
		wg.Add(1)
		go func() { defer wg.Done(); s.ws.closeAll(ctx) }()
	}
	s.screen.closeAll(ctx)
	wg.Wait()
}

// closeAll ends every open session and waits for them.
func (h *wsHandler) closeAll(ctx context.Context) {
	h.mu.Lock()
	h.closing = true
	live := make([]*wsSession, 0, len(h.live))
	for s := range h.live {
		live = append(live, s)
	}
	h.mu.Unlock()
	for _, s := range live {
		go s.c.Close(websocket.StatusGoingAway, "the daemon is stopping")
	}
	done := make(chan struct{})
	go func() { h.ended.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		for _, s := range live {
			s.c.CloseNow()
		}
		<-done
	}
}

// wsSession is one WebSocket connection after the upgrade.
type wsSession struct {
	h    *wsHandler
	c    *websocket.Conn
	sess *event.Session // set once the connection has authenticated
	wg   sync.WaitGroup

	// wmu is the one writer: every frame goes out under it, one at a time.
	wmu sync.Mutex

	mu      sync.Mutex
	started map[string]bool // subscriptions whose events are being sent; pruned when one ends
	// writeTimeout bounds every frame write: a client that stopped reading ends the
	// session instead of holding its writer forever.
	writeTimeout time.Duration
}

// run serves the connection until it ends: authenticate, then answer requests while
// the subscriptions' events go out, then release everything. Operations the session
// started are not its to cancel (ADR-0019 §Sessions): only its waits end.
func (s *wsSession) run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	timeout := time.Duration(s.h.authTimeout.Load())
	if timeout <= 0 {
		timeout = defaultAuthTimeout
	}
	if !s.authenticate(ctx, timeout) {
		return
	}
	s.sess = event.NewSession(s.h.bus, newSessionID())
	if q := int(s.h.queue.Load()); q > 0 {
		s.sess.SetQueue(q)
	}
	s.c.SetReadLimit(MaxBody) // the library's default is 32 KiB; /v1/call's bound is 1 MiB
	interval := time.Duration(s.h.pingInterval.Load())
	if interval <= 0 {
		interval = defaultPingInterval
	}
	s.writeTimeout = 2 * interval
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.pings(ctx, interval)
	}()

	s.readLoop(ctx)

	cancel()
	s.sess.Close() // the session's subscriptions end with it
	s.c.CloseNow()
	s.wg.Wait()
}

// authenticate reads the first frame, which must be {"auth": "<token>"}, within the
// deadline (rule 4). Anything else, or nothing, closes the connection with 4401. The
// deadline is a timer that closes with 4401 itself: a context deadline on the read
// would drop the connection with no close code at all.
func (s *wsSession) authenticate(ctx context.Context, timeout time.Duration) bool {
	timer := time.AfterFunc(timeout, func() { s.c.Close(closeUnauthorized, "no auth frame in time") })
	typ, data, err := s.c.Read(ctx)
	if !timer.Stop() {
		return false // the deadline passed: the timer is closing the connection
	}
	if err != nil {
		s.c.CloseNow()
		return false
	}
	var f struct {
		Auth string `json:"auth"`
	}
	// An empty token refuses everything, as the fence does.
	if typ != websocket.MessageText || json.Unmarshal(data, &f) != nil || len(s.h.token) == 0 ||
		subtle.ConstantTimeCompare([]byte(f.Auth), s.h.token) != 1 {
		s.c.Close(closeUnauthorized, "unauthorized")
		return false
	}
	return true
}

// readLoop serves each text frame as /v1/call serves a body, concurrently, until the
// connection ends.
func (s *wsSession) readLoop(ctx context.Context) {
	for {
		typ, data, err := s.c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			s.c.Close(websocket.StatusUnsupportedData, "text frames only")
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(ctx, data)
		}()
	}
}

// serve answers one request envelope with its response envelope.
func (s *wsSession) serve(ctx context.Context, body []byte) {
	env, ok := s.h.call.answer(ctx, body, s.sess)
	if !ok {
		return
	}
	data, err := json.Marshal(env)
	if err != nil {
		return
	}
	if s.write(ctx, data) != nil {
		return
	}
	// A response that names a subscription of this session releases its events
	// (plan m1d, D5): the client sees the id before any frame that carries it.
	if id := subscriptionIDOf(env.Result); id != "" {
		if sub, ok := s.sess.Subscription(id); ok {
			s.startDrain(ctx, id, sub)
		}
	}
}

// subscriptionIDOf is the subscriptionId a result carries, "" if it carries none.
func subscriptionIDOf(result json.RawMessage) string {
	if !bytes.Contains(result, []byte(`"subscriptionId"`)) {
		return ""
	}
	var r struct {
		ID string `json:"subscriptionId"`
	}
	if json.Unmarshal(result, &r) != nil {
		return ""
	}
	return r.ID
}

// startDrain begins sending a subscription's events, once. Until it does, they wait in
// the subscription's bounded queue.
func (s *wsSession) startDrain(ctx context.Context, id string, sub *event.Subscription) {
	s.mu.Lock()
	if s.started[id] {
		s.mu.Unlock()
		return
	}
	s.started[id] = true
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.drain(ctx, id, sub)
	}()
}

// eventFrame is what the server sends for a subscription's event (plan m1d, D5).
type eventFrame struct {
	SubscriptionID string      `json:"subscriptionId"`
	Event          event.Event `json:"event"`
}

// drain sends a subscription's envelopes, gaps included, in the order the bus
// delivered them, until the subscription or the connection ends.
func (s *wsSession) drain(ctx context.Context, id string, sub *event.Subscription) {
	defer func() {
		s.mu.Lock()
		delete(s.started, id)
		s.mu.Unlock()
	}()
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			return
		}
		data, err := json.Marshal(eventFrame{SubscriptionID: id, Event: e})
		if err != nil {
			// Skipping the event would lose it silently: end the session, so the
			// client reconnects and resumes with since. Nothing of the token is logged.
			log.Printf("droidyard: ending a WebSocket session: an event does not encode: %v", err)
			s.c.Close(websocket.StatusInternalError, "an event could not be encoded")
			return
		}
		if s.write(ctx, data) != nil {
			return
		}
	}
}

// write sends one text frame. Frames go out one at a time.
func (s *wsSession) write(ctx context.Context, data []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if hook := s.h.writeHook.Load(); hook != nil {
		(*hook)()
	}
	// A write that outlives its deadline closes the connection, which ends the session.
	wctx, cancel := context.WithTimeout(ctx, s.writeTimeout)
	defer cancel()
	return s.c.Write(wctx, websocket.MessageText, data)
}

// pings pings every interval and closes the connection when two pings in a row get
// no pong. The read loop is what receives the pongs.
func (s *wsSession) pings(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var ponged atomic.Bool
	missed, sent := 0, false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		switch {
		case ponged.Swap(false):
			missed = 0
		case sent:
			missed++
		}
		if missed >= 2 {
			s.c.CloseNow() // nobody is listening for a close frame
			return
		}
		sent = true
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if s.c.Ping(ctx) == nil {
				ponged.Store(true)
			}
		}()
	}
}

// newSessionID names a WebSocket session, for the event layer.
func newSessionID() string {
	var b [8]byte
	rand.Read(b[:]) // never returns an error (crypto/rand, Go 1.24)
	return "ws-" + hex.EncodeToString(b[:])
}
