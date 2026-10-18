package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// The link's timers. Variables only so this package's tests can shorten them.
var (
	// backoffMin and backoffMax bound the wait between two dials: from 1 s, doubling to
	// 30 s (ADR-0098 §4), and back to 1 s once a link has been up.
	backoffMin = time.Second
	backoffMax = 30 * time.Second
	// dialTimeout bounds the TCP and TLS handshakes and the hello exchange.
	dialTimeout = 10 * time.Second
	// pingEvery and pingWait keep a link that went silent from looking up: the host pings
	// too, and either end closes after a missed answer.
	pingEvery = 30 * time.Second
	pingWait  = 10 * time.Second
)

// readLimit bounds a frame a host sends: a response or an event, never a stream's bytes.
const readLimit = 8 << 20

var (
	// ErrNotConnected is a call made while the link is down.
	ErrNotConnected = errors.New("the link to the remote is not connected")
	// ErrLinkLost is a call the link was lost during: whether the host ran it is unknown.
	ErrLinkLost = errors.New("the link to the remote was lost during the call")
)

// Link is this daemon's connection to one host it is paired with (ADR-0098 §4): dialled at
// start, pinned to the host's fingerprint, kept up with backoff. Calls go over it as the
// capability envelopes of the peer protocol.
type Link struct {
	remote policy.Remote
	id     *Identity
	api    string
	log    io.Writer

	mu        sync.Mutex
	conn      *websocket.Conn
	wmu       sync.Mutex // one writer at a time
	pending   map[string]chan capability.ResponseEnvelope
	events    map[string]func(json.RawMessage)
	next      uint64
	connected bool
	hostAPI   string
	lastErr   string
}

func newLink(r policy.Remote, id *Identity, api string, log io.Writer) *Link {
	return &Link{remote: r, id: id, api: api, log: log,
		pending: map[string]chan capability.ResponseEnvelope{}, events: map[string]func(json.RawMessage){}}
}

// Name is the remote's name.
func (l *Link) Name() string { return l.remote.Name }

func (l *Link) health() RemoteHealth {
	l.mu.Lock()
	defer l.mu.Unlock()
	return RemoteHealth{Name: l.remote.Name, Connected: l.connected, API: l.hostAPI, Error: l.lastErr}
}

// Connected reports whether the link is up.
func (l *Link) Connected() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.connected
}

// Call sends one request over the link and returns the host's response. The envelope's id
// is the link's own for the call and restored in the answer. A link that is down is
// ErrNotConnected; one lost before the answer came is ErrLinkLost; ctx ending abandons the
// wait and leaves the host's operation running.
func (l *Link) Call(ctx context.Context, req capability.RequestEnvelope) (capability.ResponseEnvelope, error) {
	l.mu.Lock()
	conn := l.conn
	if !l.connected || conn == nil {
		l.mu.Unlock()
		return capability.ResponseEnvelope{}, ErrNotConnected
	}
	l.next++
	id := "L" + strconv.FormatUint(l.next, 10)
	ch := make(chan capability.ResponseEnvelope, 1)
	l.pending[id] = ch
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.pending, id)
		l.mu.Unlock()
	}()

	original := req.ID
	req.ID = id
	frame, err := json.Marshal(req)
	if err != nil {
		return capability.ResponseEnvelope{}, err
	}
	if err := l.write(ctx, conn, frame); err != nil {
		return capability.ResponseEnvelope{}, fmt.Errorf("%w: %v", ErrLinkLost, err)
	}
	select {
	case env, ok := <-ch:
		if !ok {
			return capability.ResponseEnvelope{}, ErrLinkLost
		}
		env.ID = original
		return env, nil
	case <-ctx.Done():
		return capability.ResponseEnvelope{}, ctx.Err()
	}
}

// OnEvent routes the events of one subscription, by its subscriptionId, to fn until the
// returned stop is called or the link is lost. An event for a subscription nobody routes
// is dropped.
func (l *Link) OnEvent(subscriptionID string, fn func(event json.RawMessage)) (stop func()) {
	l.mu.Lock()
	l.events[subscriptionID] = fn
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		delete(l.events, subscriptionID)
		l.mu.Unlock()
	}
}

func (l *Link) write(ctx context.Context, conn *websocket.Conn, frame []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, frame)
}

// run keeps the link up until ctx ends.
func (l *Link) run(ctx context.Context) {
	wait := backoffMin
	for ctx.Err() == nil {
		up, err := l.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if up {
			wait = backoffMin
		}
		l.mu.Lock()
		l.lastErr = err.Error()
		l.mu.Unlock()
		fmt.Fprintf(l.log, "droidyard: remote %s: %v; trying again in %s\n", l.remote.Name, err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, backoffMax)
	}
}

// session dials once and serves the connection until it ends. up reports whether the hello
// was exchanged, the link having been up; err is why the session ended.
func (l *Link) session(ctx context.Context) (up bool, err error) {
	cfg := PinnedConfig(l.id, func(fp string) bool { return SamePin(fp, l.remote.Fingerprint) })
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true, ForceAttemptHTTP2: false,
		TLSHandshakeTimeout: dialTimeout}}
	defer client.CloseIdleConnections()

	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	conn, _, err := websocket.Dial(dctx, "wss://"+l.remote.Address+capability.PeerPath, &websocket.DialOptions{HTTPClient: client})
	cancel()
	if err != nil {
		return false, dialError(l.remote, err)
	}
	conn.SetReadLimit(readLimit)
	defer conn.CloseNow()

	host, err := l.hello(ctx, conn)
	if err != nil {
		return false, err
	}
	if host.API != l.api {
		fmt.Fprintf(l.log, "droidyard: remote %s runs API %s and this daemon %s; the peer protocol is the same, so the link is kept\n", l.remote.Name, host.API, l.api)
	}

	sctx, stop := context.WithCancel(ctx)
	defer stop()
	l.mu.Lock()
	l.conn, l.connected, l.hostAPI, l.lastErr = conn, true, host.API, ""
	l.mu.Unlock()
	fmt.Fprintf(l.log, "droidyard: remote %s: link up (API %s)\n", l.remote.Name, host.API)
	defer l.down()

	go l.pings(sctx, conn)
	err = l.read(sctx, conn)
	return true, fmt.Errorf("link down: %w", err)
}

// hello sends this daemon's hello and reads the host's, within the dial bound.
func (l *Link) hello(ctx context.Context, conn *websocket.Conn) (capability.PeerHello, error) {
	hctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	out, _ := json.Marshal(capability.PeerHelloFrame{Hello: &capability.PeerHello{PeerProtocol: capability.PeerProtocol, API: l.api}})
	if err := conn.Write(hctx, websocket.MessageText, out); err != nil {
		return capability.PeerHello{}, dialError(l.remote, err)
	}
	typ, data, err := conn.Read(hctx)
	if err != nil {
		return capability.PeerHello{}, dialError(l.remote, err)
	}
	var f capability.PeerHelloFrame
	if typ != websocket.MessageText || json.Unmarshal(data, &f) != nil || f.Hello == nil {
		conn.Close(capability.PeerCloseProtocol, "a hello was expected")
		return capability.PeerHello{}, fmt.Errorf("the host did not answer with a hello")
	}
	if f.Hello.PeerProtocol != capability.PeerProtocol {
		conn.Close(capability.PeerCloseProtocol, "peerProtocol "+strconv.Itoa(capability.PeerProtocol))
		return capability.PeerHello{}, fmt.Errorf("the host speaks peer protocol %d and this daemon %d", f.Hello.PeerProtocol, capability.PeerProtocol)
	}
	return *f.Hello, nil
}

// dialError says what failed in terms of the remote, not of the TLS stack.
func dialError(r policy.Remote, err error) error {
	switch {
	case errors.Is(err, ErrPinned):
		return fmt.Errorf("the certificate at %s is not the fingerprint remotes[%s] pins; nothing was sent", r.Address, r.Name)
	case strings.Contains(err.Error(), "bad certificate"):
		// What a TLS 1.3 host answers a client certificate it does not pin with.
		return fmt.Errorf("the host at %s does not admit this daemon: its hosting.peers does not hold this daemon's fingerprint", r.Address)
	}
	var closed websocket.CloseError
	if errors.As(err, &closed) && closed.Code >= 4000 {
		return fmt.Errorf("the host at %s closed the session: %s", r.Address, closed.Reason)
	}
	return fmt.Errorf("connecting to %s: %w", r.Address, err)
}

// read delivers the host's frames until the connection ends: a response to its caller, an
// event to its subscription.
func (l *Link) read(ctx context.Context, conn *websocket.Conn) error {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			conn.Close(websocket.StatusUnsupportedData, "text frames only")
			return errors.New("the host sent a binary frame")
		}
		var head struct {
			ID             string          `json:"id"`
			SubscriptionID string          `json:"subscriptionId"`
			Event          json.RawMessage `json:"event"`
		}
		if json.Unmarshal(data, &head) != nil {
			continue
		}
		if head.SubscriptionID != "" {
			l.mu.Lock()
			fn := l.events[head.SubscriptionID]
			l.mu.Unlock()
			if fn != nil {
				fn(head.Event)
			}
			continue
		}
		var env capability.ResponseEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		l.mu.Lock()
		ch := l.pending[head.ID]
		l.mu.Unlock()
		if ch != nil {
			select {
			case ch <- env:
			default:
			}
		}
	}
}

// down marks the link lost and fails every call waiting on it.
func (l *Link) down() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.conn, l.connected = nil, false
	for id, ch := range l.pending {
		close(ch)
		delete(l.pending, id)
	}
	l.events = map[string]func(json.RawMessage){}
}

// pings closes a connection that stops answering.
func (l *Link) pings(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, pingWait)
		err := conn.Ping(pctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			conn.CloseNow()
			return
		}
	}
}
