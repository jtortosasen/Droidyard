package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// GET /v1/screen (plan m2p step 4; ADR-0078 §4): the live view's video, one WebSocket per
// stream, binary messages the server sends and the client never answers.
//
// The adapter holds one thing of the live view: ScreenAttach, which claims a stream id for
// this socket and returns a viewer that reads packets and closes. It has no way to send
// anything to the device (constraint 2 of plan m2p: every input is a capability call), and
// it does not look for one: any frame the client sends after its auth frame closes the
// socket.

// screenPath is the stream socket's route.
const screenPath = "/v1/screen"

// The close codes of the stream socket beside the session's 4401 (ADR-0078 §4). Codes 4000
// to 4999 are the application's.
const (
	closeStreamUnknown  websocket.StatusCode = 4404
	closeStreamAttached websocket.StatusCode = 4409
	closeStreamEnded    websocket.StatusCode = 4410
)

// defaultScreenWriteTimeout bounds each message's write: a socket that stops reading ends
// its own stream instead of holding its writer, and a slow one falls behind only inside
// its own viewer's queue, which drops to the next key frame.
const defaultScreenWriteTimeout = 10 * time.Second

// What a ScreenAttach refuses with, which this adapter turns into close codes.
var (
	// ErrScreenUnknown is a stream id that was never issued or has ended: 4404.
	ErrScreenUnknown = errors.New("httpapi: no such screen stream")
	// ErrScreenAttached is a stream id with a socket already attached: 4409.
	ErrScreenAttached = errors.New("httpapi: the screen stream is already attached")
)

// ScreenPacket is one message of the stream: the device's own Annex B bytes with the key
// frame and config flags, and the presentation time in microseconds.
type ScreenPacket struct {
	Key, Config bool
	PTS         int64
	Data        []byte
}

// ScreenViewer is what a stream socket reads: Next blocks until a packet, the stream's end
// or ctx, and Close ends the stream. It is deliberately all there is.
type ScreenViewer interface {
	Next(ctx context.Context) (ScreenPacket, error)
	Close()
}

// ScreenAttach claims the viewer of a stream id, once. The daemon's wiring supplies it; the
// adapter never imports the domain.
type ScreenAttach func(id string) (ScreenViewer, error)

// UseScreens gives the server the live view's attach function; until it is called
// /v1/screen answers 404.
func (s *Server) UseScreens(attach ScreenAttach) { s.screen.attach.Store(&attach) }

// SetScreenWriteTimeout sets how long one message may take to write on sockets accepted
// from now on; d <= 0 is the default, 10 s. Test-only.
func (s *Server) SetScreenWriteTimeout(d time.Duration) { s.screen.writeTimeout.Store(int64(d)) }

// screenHandler is GET /v1/screen.
type screenHandler struct {
	token  []byte
	attach atomic.Pointer[ScreenAttach]

	authTimeout, writeTimeout atomic.Int64

	// live are the open sockets, closed by closeAll when the daemon stops: a hijacked
	// connection is not the HTTP server's to wait for. ended counts the handlers running.
	mu      sync.Mutex
	live    map[*websocket.Conn]struct{}
	closing bool
	ended   sync.WaitGroup
}

func newScreenHandler(token string) *screenHandler {
	return &screenHandler{token: []byte(token), live: map[*websocket.Conn]struct{}{}}
}

func (h *screenHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	attach := h.attach.Load()
	if attach == nil {
		http.NotFound(w, r)
		return
	}
	// The fence has already refused every Host and Origin that is not one of the two
	// allowed pairs of the bound port and required an Origin; the library's check is
	// relaxed to exactly those two origins, as for /v1/ws.
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
	// 4 KiB for the socket's whole life: the auth frame is a token and an id in a small
	// object, and nothing else the client sends is read.
	c.SetReadLimit(preAuthLimit)
	if !h.add(c) {
		c.Close(websocket.StatusGoingAway, "the daemon is stopping")
		return
	}
	defer h.remove(c)
	h.serve(r.Context(), c, *attach)
}

func (h *screenHandler) add(c *websocket.Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	h.live[c] = struct{}{}
	h.ended.Add(1)
	return true
}

func (h *screenHandler) remove(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.live, c)
	h.mu.Unlock()
	h.ended.Done()
}

// closeAll ends every open socket and waits for the handlers, until ctx is done, when the
// connections are dropped without a close frame.
func (h *screenHandler) closeAll(ctx context.Context) {
	h.mu.Lock()
	h.closing = true
	live := make([]*websocket.Conn, 0, len(h.live))
	for c := range h.live {
		live = append(live, c)
	}
	h.mu.Unlock()
	for _, c := range live {
		go c.Close(websocket.StatusGoingAway, "the daemon is stopping")
	}
	done := make(chan struct{})
	go func() { h.ended.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		for _, c := range live {
			c.CloseNow()
		}
		<-done
	}
}

// serve authenticates the socket, then sends the stream's packets until it ends. The
// viewer is closed on every way out, which ends the stream (as detached when the socket
// was the reason).
func (h *screenHandler) serve(parent context.Context, c *websocket.Conn, attach ScreenAttach) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	timeout := time.Duration(h.authTimeout.Load())
	if timeout <= 0 {
		timeout = defaultAuthTimeout
	}
	v, ok := h.authenticate(ctx, c, attach, timeout)
	if !ok {
		return
	}
	wt := time.Duration(h.writeTimeout.Load())
	if wt <= 0 {
		wt = defaultScreenWriteTimeout
	}
	sendScreen(ctx, cancel, c, v, wt)
}

// sendScreen sends the viewer's packets on c until the stream or the socket ends, then
// closes the viewer, which ends the stream (as detached when the socket was the reason). It
// is the part of /v1/screen and /peer/v1/screen that comes after the first frame. cancel
// ends ctx.
func sendScreen(ctx context.Context, cancel context.CancelFunc, c *websocket.Conn, v ScreenViewer, wt time.Duration) {
	// Reads: the only thing the socket may still say is that it is gone. Any frame, text
	// or binary, closes it; nothing it carries is looked at.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		if _, _, err := c.Read(ctx); err == nil {
			c.Close(websocket.StatusUnsupportedData, "this socket only receives")
		}
		cancel()
	}()
	defer func() {
		cancel()
		v.Close()
		c.CloseNow()
		<-readerDone
	}()

	var buf []byte
	for {
		p, err := v.Next(ctx)
		if err != nil {
			if ctx.Err() == nil {
				c.Close(closeStreamEnded, "the stream ended")
			}
			return
		}
		buf = screenMessage(buf, p)
		wctx, wcancel := context.WithTimeout(ctx, wt)
		err = c.Write(wctx, websocket.MessageBinary, buf)
		wcancel()
		if err != nil {
			return // the library closed a connection whose write failed or timed out
		}
		if cap(buf) > 1<<20 {
			buf = nil // do not keep a key frame's buffer for the delta frames behind it
		}
	}
}

// authenticate reads the first frame, {"auth": <token>, "stream": <id>}, within the
// deadline, and claims the stream. A bad token is 4401 before the id is looked at, so an
// unauthenticated client learns nothing about ids.
func (h *screenHandler) authenticate(ctx context.Context, c *websocket.Conn, attach ScreenAttach, timeout time.Duration) (ScreenViewer, bool) {
	timer := time.AfterFunc(timeout, func() { c.Close(closeUnauthorized, "no auth frame in time") })
	typ, data, err := c.Read(ctx)
	if !timer.Stop() {
		return nil, false // the deadline passed: the timer is closing the connection
	}
	if err != nil {
		c.CloseNow()
		return nil, false
	}
	var f struct {
		Auth   string `json:"auth"`
		Stream string `json:"stream"`
	}
	// An empty token refuses everything, as the fence does.
	if typ != websocket.MessageText || json.Unmarshal(data, &f) != nil || len(h.token) == 0 ||
		subtle.ConstantTimeCompare([]byte(f.Auth), h.token) != 1 {
		c.Close(closeUnauthorized, "unauthorized")
		return nil, false
	}
	v, err := attach(f.Stream)
	switch {
	case errors.Is(err, ErrScreenUnknown):
		c.Close(closeStreamUnknown, "unknown stream")
	case errors.Is(err, ErrScreenAttached):
		c.Close(closeStreamAttached, "the stream is already attached")
	case err != nil || v == nil:
		c.Close(websocket.StatusInternalError, "the stream could not be attached")
	default:
		return v, true
	}
	return nil, false
}

// The flags byte of a message (ADR-0078 §4).
const (
	screenFlagKey    = 1
	screenFlagConfig = 2
)

// screenMessage lays one packet out as [flags u8][pts u64 BE][Annex B], reusing buf. The
// Annex B bytes are the packet's, unchanged.
func screenMessage(buf []byte, p ScreenPacket) []byte {
	var flags byte
	if p.Key {
		flags |= screenFlagKey
	}
	if p.Config {
		flags |= screenFlagConfig
	}
	buf = append(buf[:0], flags)
	buf = binary.BigEndian.AppendUint64(buf, uint64(p.PTS))
	return append(buf, p.Data...)
}
