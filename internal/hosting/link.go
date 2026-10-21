package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
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
	// addrDialTimeout bounds the connection to one address of a remote that has several
	// (ADR-0109 §4): the next is tried after it, so an address that is not reachable from
	// here costs this much and not dialTimeout.
	addrDialTimeout = 3 * time.Second
)

// readLimit bounds a frame a host sends: a response or an event, never a stream's bytes.
const readLimit = 8 << 20

var (
	// ErrNotConnected is a call made while the link is down.
	ErrNotConnected = errors.New("the link to the remote is not connected")
	// ErrLinkLost is a call the link was lost during: whether the host ran it is unknown.
	ErrLinkLost = errors.New("the link to the remote was lost during the call")
)

// Link is this daemon's connection to one host it is paired with (ADR-0098 §4): dialled
// when the remote appears in the configuration, pinned to the host's fingerprint, kept up
// with backoff. Calls go over it as the capability envelopes of the peer protocol. Its name
// and fingerprint are fixed; the addresses it dials are updated in place (ADR-0109 §3).
type Link struct {
	remote policy.Remote // the name and the fingerprint; the addresses are addr and addrs
	id     *Identity
	api    string
	log    io.Writer
	// own, when set, lists the subnets this machine is on, for ordering the addresses.
	own func() []netip.Prefix

	stop func()        // set by the Host: ends the link's run
	done chan struct{} // closed when the link's run has returned

	mu        sync.Mutex
	conn      *websocket.Conn
	wmu       sync.Mutex // one writer at a time
	pending   map[string]chan capability.ResponseEnvelope
	routes    map[string]*route // by request id: where a response's subscription is routed
	events    map[string]func(json.RawMessage)
	next      uint64
	connected bool
	lost      chan struct{} // closed when the connection now up is lost; nil while down
	hostAPI   string
	fast      bool // the remote's mark (ADR-0118 §4), sent in the hello, then in a mark frame when update changes it (ADR-0123 §1)
	// sentFast is the mark the host was last told: the hello's, then each mark frame written
	// (ADR-0123 §1). The mark sender writes a frame when fast differs from it.
	sentFast bool
	// marked is the mark channel (ADR-0123 §1): closed by the mark sender once a mark frame
	// has been written, and replaced at once, so it is never closed twice.
	marked   chan struct{}
	markWake chan struct{} // wakes the mark sender; one pending wake is enough
	lastErr  string
	addr     string                 // the remote's address
	addrs    []policy.RemoteAddress // its tagged others
	answered string                 // the address the last connection was made at; "" before
}

func newLink(r policy.Remote, id *Identity, api string, log io.Writer) *Link {
	return &Link{remote: r, id: id, api: api, log: log, addr: r.Address, addrs: slices.Clone(r.Addresses), fast: r.Fast,
		pending: map[string]chan capability.ResponseEnvelope{}, routes: map[string]*route{},
		events: map[string]func(json.RawMessage){}, marked: make(chan struct{}), markWake: make(chan struct{}, 1)}
}

// update replaces the addresses the link dials, keeping the link and its connection
// (ADR-0109 §3): the next attempt uses them. It reports whether anything changed.
func (l *Link) update(r policy.Remote) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fast != r.Fast {
		l.fast = r.Fast
		l.markChangedLocked()
	}
	if l.addr == r.Address && slices.Equal(l.addrs, r.Addresses) {
		return false
	}
	l.addr, l.addrs = r.Address, slices.Clone(r.Addresses)
	if l.answered != "" && l.answered != l.addr && !slices.ContainsFunc(l.addrs, func(a policy.RemoteAddress) bool { return a.Address == l.answered }) {
		l.answered = "" // the address that answered is no longer the remote's
	}
	return true
}

// markChangedLocked tells the host the mark changed, with l.mu held and no I/O under it
// (update runs under Host.mu too). A host whose hello names PeerMarkAPI is sent a mark frame
// by the mark sender, which this only wakes (ADR-0123 §1). An older host learns the mark in
// the hello, so the connection is dropped and the run dials again within seconds (ADR-0118
// §4). With the link down there is nothing to do: the next hello carries the mark.
func (l *Link) markChangedLocked() {
	if l.conn == nil {
		return
	}
	if !APIAtLeast(l.hostAPI, capability.PeerMarkAPI) {
		go l.conn.CloseNow()
		return
	}
	select {
	case l.markWake <- struct{}{}:
	default: // a wake is pending already; the sender reads the current mark when it runs
	}
}

// Marked is closed once a mark frame has been written to the host (ADR-0123 §1): a source
// that took it while a write was pending ends after the write and watches again behind the
// frame. It is taken like Down, and a fresh channel replaces it as it closes. While the link
// is down it is open, not closed: a loss is Down's to report.
func (l *Link) Marked() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.marked
}

// markSender writes the mark frames of one connection (ADR-0123 §1). It reads the current
// mark and the connection under l.mu and writes outside every lock, through the link's one
// writer, so a slow write blocks no call, no reconcile and no health. Two quick changes leave
// the host with the last value: the sender writes what the mark is when it runs, and nothing
// when that is what the host already has. A write that fails is the connection's loss.
func (l *Link) markSender(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.markWake:
		}
		l.mu.Lock()
		if l.conn != conn {
			l.mu.Unlock()
			return
		}
		fast, ch := l.fast, l.marked
		if fast == l.sentFast {
			l.mu.Unlock()
			continue
		}
		l.mu.Unlock()
		if markBeforeWrite != nil {
			markBeforeWrite()
		}
		frame, _ := json.Marshal(capability.PeerMarkFrame{Mark: &capability.PeerMark{Fast: &fast}})
		if err := l.write(conn, frame); err != nil {
			conn.CloseNow()
			return
		}
		l.mu.Lock()
		if l.conn != conn {
			// The connection was replaced while the frame was written: the new session's
			// hello told the host its mark, and the channel is the new sender's to close.
			l.mu.Unlock()
			return
		}
		l.sentFast = fast
		if l.marked == ch {
			close(ch)
			l.marked = make(chan struct{})
		}
		l.mu.Unlock()
	}
}

// markBeforeWrite is a test hook, nil in production: it runs in the mark sender after it
// chose the mark to write and before the write, holding no lock.
var markBeforeWrite func()

// afterHello is a test hook, nil in production: it runs in session between the hello and the
// link being marked up.
var afterHello func()

// current is the address a request that goes straight to the host is made at: the one the
// last connection was made at, else the remote's own.
func (l *Link) current() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.answered != "" {
		return l.answered
	}
	return l.addr
}

// remoteAt is the remote as seen at one address, for the messages that name it.
func (l *Link) remoteAt(addr string) policy.Remote {
	r := l.remote
	r.Address, r.Addresses = addr, nil
	return r
}

// candidates are the addresses to dial, in the order to dial them (ADR-0109 §4): the remote's
// address and its tagged ones, a lan address in one of this machine's own subnets first, then
// the vpn ones, then the rest. The remote's address is first among equals; one that the list
// also holds takes the list's tag.
func (l *Link) candidates() []string {
	l.mu.Lock()
	primary, list, own := l.addr, slices.Clone(l.addrs), l.own
	l.mu.Unlock()
	addrs := []string{primary}
	kinds := []Kind{""}
	for _, a := range list {
		if i := slices.Index(addrs, a.Address); i >= 0 {
			kinds[i] = Kind(a.Kind)
			continue
		}
		addrs = append(addrs, a.Address)
		kinds = append(kinds, Kind(a.Kind))
	}
	ips := make([]netip.Addr, len(addrs))
	for i, a := range addrs {
		if ap, err := netip.ParseAddrPort(a); err == nil {
			ips[i] = ap.Addr()
		}
	}
	var prefixes []netip.Prefix
	if own != nil {
		prefixes = own()
	}
	out := make([]string, 0, len(addrs))
	for _, i := range orderIndex(ips, kinds, prefixes) {
		out = append(out, addrs[i])
	}
	return out
}

// atError is a failure at one address of a remote that may have several.
type atError struct {
	addr string
	err  error
}

func (e *atError) Error() string { return e.err.Error() }
func (e *atError) Unwrap() error { return e.err }

// closedChan is what Down returns for a link that is down.
var closedChan = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// Down is closed when the connection that is up now is lost; it is already closed while the
// link is down. A stream that lives on one connection (its subscription, its video socket)
// takes it before the call that starts it, so a connection lost in between counts as lost.
func (l *Link) Down() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.connected || l.lost == nil {
		return closedChan
	}
	return l.lost
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
	return l.call(ctx, req, nil)
}

// CallRouted is Call for a request whose result names a subscription (a subscriptionId, as
// device.watchScreen's does): when the response arrives, that subscription's events are
// routed to fn before the next frame is read, so none can fall between the response and a
// registration made after it. stop ends the route; it is a no-op when the response named
// none, and the route also ends with the connection.
func (l *Link) CallRouted(ctx context.Context, req capability.RequestEnvelope, fn func(event json.RawMessage)) (resp capability.ResponseEnvelope, stop func(), err error) {
	resp, err = l.call(ctx, req, fn)
	if err != nil {
		return resp, func() {}, err
	}
	id := subscriptionOf(resp.Result)
	if id == "" {
		return resp, func() {}, nil
	}
	return resp, func() {
		l.mu.Lock()
		delete(l.events, id)
		l.mu.Unlock()
	}, nil
}

// subscriptionOf is the subscriptionId a result names, "" if it names none.
func subscriptionOf(result json.RawMessage) string {
	var r struct {
		ID string `json:"subscriptionId"`
	}
	if json.Unmarshal(result, &r) != nil {
		return ""
	}
	return r.ID
}

// route is what a CallRouted request leaves for read: where its response's subscription
// goes, and which one it was once routed. A caller that gives up before the response leaves
// the route as a tombstone (abandoned): the host may still open the subscription, and the
// response that names it is answered with events.unsubscribe, so nothing is held on the host
// for a stream nobody has.
type route struct {
	fn        func(json.RawMessage)
	sub       string
	abandoned bool
}

// maxSubscriptionID bounds a subscription id a host names that this link then sends back.
const maxSubscriptionID = 128

// endSubscription asks the host to end a subscription this link has no use for.
func (l *Link) endSubscription(sub string) {
	if sub == "" || len(sub) > maxSubscriptionID {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	p, _ := json.Marshal(event.UnsubscribeParams{SubscriptionID: sub})
	l.Call(ctx, capability.RequestEnvelope{Capability: event.CapUnsubscribe, Params: p})
}

func (l *Link) call(ctx context.Context, req capability.RequestEnvelope, fn func(json.RawMessage)) (resp capability.ResponseEnvelope, err error) {
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
	var rt *route
	if fn != nil {
		rt = &route{fn: fn}
		l.routes[id] = rt
	}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.pending, id)
		drop := ""
		switch {
		case rt == nil || err == nil:
			delete(l.routes, id)
		case rt.sub != "":
			// Routed, then abandoned as it arrived: no route is left for a stream nobody
			// holds, and the host is told to end its subscription.
			delete(l.routes, id)
			delete(l.events, rt.sub)
			drop = rt.sub
		case ctx.Err() != nil:
			// The request is on its way and the answer may name a subscription: read ends it.
			rt.abandoned = true
		default:
			delete(l.routes, id)
		}
		l.mu.Unlock()
		if drop != "" {
			go l.endSubscription(drop)
		}
	}()

	original := req.ID
	req.ID = id
	frame, err := json.Marshal(req)
	if err != nil {
		return capability.ResponseEnvelope{}, err
	}
	// The frame is written on a context of its own: coder/websocket closes the whole
	// connection when the context of a Write ends before the frame is out, so one caller
	// giving up must not be able to cut every other call on the link. The caller stops
	// waiting; the write finishes, or fails on its own bound.
	written := make(chan error, 1)
	go func() { written <- l.write(conn, frame) }()
	select {
	case err := <-written:
		if err != nil {
			return capability.ResponseEnvelope{}, fmt.Errorf("%w: %v", ErrLinkLost, err)
		}
	case <-ctx.Done():
		return capability.ResponseEnvelope{}, ctx.Err()
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

// write sends one frame, bounded by dialTimeout and by nothing a caller can cancel.
func (l *Link) write(conn *websocket.Conn, frame []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	wctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
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
		at := l.current()
		var ae *atError
		if errors.As(err, &ae) {
			at = ae.addr
		}
		l.mu.Lock()
		l.lastErr = healthError(l.remoteAt(at), err)
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

// pinnedClient is the HTTP client every connection to the remote is made with: TLS 1.3, this
// daemon's certificate, and the remote's accepted by its pinned fingerprint alone.
func (l *Link) pinnedClient() *http.Client {
	cfg := PinnedConfig(l.id, func(fp string) bool { return SamePin(fp, l.remote.Fingerprint) })
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true, ForceAttemptHTTP2: false,
		TLSHandshakeTimeout: dialTimeout}}
}

// session dials once and serves the connection until it ends. up reports whether the hello
// was exchanged, the link having been up; err is why the session ended.
func (l *Link) session(ctx context.Context) (up bool, err error) {
	client := l.pinnedClient()
	defer client.CloseIdleConnections()

	conn, addr, err := l.dialAny(ctx, client)
	if err != nil {
		return false, err
	}
	conn.SetReadLimit(readLimit)
	defer conn.CloseNow()

	host, err := l.hello(ctx, conn, l.remoteAt(addr))
	if err != nil {
		return false, &atError{addr, err}
	}
	if host.API != l.api {
		fmt.Fprintf(l.log, "droidyard: remote %s runs API %s and this daemon %s; the peer protocol is the same, so the link is kept\n", l.remote.Name, host.API, l.api)
	}

	if afterHello != nil {
		afterHello()
	}
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	l.mu.Lock()
	l.conn, l.connected, l.hostAPI, l.lastErr, l.answered = conn, true, host.API, "", addr
	l.lost = make(chan struct{})
	if l.fast != l.sentFast {
		// The mark changed since the hello, while update found no connection to tell it on:
		// it is sent now, as a change of an open link is (ADR-0123 §1).
		l.markChangedLocked()
	}
	l.mu.Unlock()
	fmt.Fprintf(l.log, "droidyard: remote %s: link up (API %s)\n", l.remote.Name, host.API)
	defer l.down()

	go l.pings(sctx, conn)
	go l.markSender(sctx, conn)
	err = l.read(sctx, conn)
	return true, &atError{addr, fmt.Errorf("link down: %w", err)}
}

// dialAny opens the WebSocket at the first address that answers, trying them in order
// (ADR-0109 §4), and returns it with the address. When none does, the error is the most
// telling one: a remote that answered and was refused or was not the host (a failure in
// this daemon's own words) before one that did not answer, the first of each kind.
func (l *Link) dialAny(ctx context.Context, client *http.Client) (*websocket.Conn, string, error) {
	cands := l.candidates()
	per := dialTimeout
	if len(cands) > 1 {
		per = min(dialTimeout, addrDialTimeout)
	}
	var best error
	for _, addr := range cands {
		dctx, cancel := context.WithTimeout(ctx, per)
		conn, _, err := websocket.Dial(dctx, "wss://"+addr+capability.PeerPath, &websocket.DialOptions{HTTPClient: client})
		cancel()
		if err == nil {
			return conn, addr, nil
		}
		derr := dialError(l.remoteAt(addr), err)
		if ctx.Err() != nil {
			return nil, "", &atError{addr, derr}
		}
		var own, bestOwn *ownError
		if best == nil || (!errors.As(best, &bestOwn) && errors.As(derr, &own)) {
			best = &atError{addr, derr}
		}
	}
	return nil, "", best
}

// hello sends this daemon's hello and reads the host's, within the dial bound.
func (l *Link) hello(ctx context.Context, conn *websocket.Conn, rem policy.Remote) (capability.PeerHello, error) {
	hctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	l.mu.Lock()
	fast := l.fast
	l.sentFast = fast // what this hello tells the host
	l.mu.Unlock()
	out, _ := json.Marshal(capability.PeerHelloFrame{Hello: &capability.PeerHello{PeerProtocol: capability.PeerProtocol, API: l.api, Fast: fast}})
	if err := conn.Write(hctx, websocket.MessageText, out); err != nil {
		return capability.PeerHello{}, dialError(rem, err)
	}
	typ, data, err := conn.Read(hctx)
	if err != nil {
		return capability.PeerHello{}, dialError(rem, err)
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
	if !apiVersion.MatchString(f.Hello.API) {
		// Another machine wrote it, and system.health shows it to agents: only a version
		// number is kept (ADR-0100 §4).
		fmt.Fprintf(l.log, "droidyard: remote %s sent an API version that is not one; it is not shown\n", l.remote.Name)
		f.Hello.API = ""
	}
	return *f.Hello, nil
}

// apiVersion is the shape of an API version, the one text of the host's hello this daemon
// shows.
var apiVersion = regexp.MustCompile(`^\d{1,4}\.\d{1,5}\.\d{1,6}$`)

// healthError is why the link is down as system.health shows it: this daemon's own words
// only, since a transport error can quote what the host sent (a malformed status line, a
// close reason) and agents read the health (ADR-0100 §4). The daemon's log keeps the full
// error.
func healthError(r policy.Remote, err error) string {
	var own *ownError
	var closed websocket.CloseError
	switch {
	case errors.As(err, &own):
		return own.msg
	case errors.As(err, &closed):
		return fmt.Sprintf("the host at %s closed the session (code %d)", r.Address, closed.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("the host at %s did not answer in time", r.Address)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Sprintf("nothing listens at %s", r.Address)
	}
	return fmt.Sprintf("the link to %s failed; this daemon's log says why", r.Address)
}

// ownError is a failure told in this daemon's own words, with nothing the host sent.
type ownError struct{ msg string }

func (e *ownError) Error() string { return e.msg }

// dialError says what failed in terms of the remote, not of the TLS stack.
func dialError(r policy.Remote, err error) error {
	switch {
	case errors.Is(err, ErrPinned):
		return &ownError{fmt.Sprintf("the certificate at %s is not the fingerprint remotes[%s] pins; nothing was sent", r.Address, r.Name)}
	case strings.Contains(err.Error(), "bad certificate"):
		// What a TLS 1.3 host answers a client certificate it does not pin with.
		return &ownError{fmt.Sprintf("the host at %s does not admit this daemon: its hosting.peers does not hold this daemon's fingerprint", r.Address)}
	}
	var closed websocket.CloseError
	if errors.As(err, &closed) && closed.Code >= 4000 {
		// The reason is the host's text: system.health would show it to agents (ADR-0100 §4).
		return &ownError{fmt.Sprintf("the host at %s closed the session (code %d)", r.Address, closed.Code)}
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
		orphan := ""
		if rt := l.routes[head.ID]; rt != nil && rt.abandoned {
			// The caller is gone: whatever subscription this answer opened is ended.
			delete(l.routes, head.ID)
			orphan = subscriptionOf(env.Result)
		} else if rt != nil && ch != nil {
			// Before the response is handed on, so the events behind it find their route.
			if sub := subscriptionOf(env.Result); sub != "" {
				l.events[sub], rt.sub = rt.fn, sub
			}
		}
		l.mu.Unlock()
		if orphan != "" {
			go l.endSubscription(orphan)
			continue
		}
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
	if l.lost != nil {
		close(l.lost)
		l.lost = nil
	}
	for id, ch := range l.pending {
		close(ch)
		delete(l.pending, id)
	}
	l.events = map[string]func(json.RawMessage){}
	for id, rt := range l.routes {
		if rt.abandoned { // its answer can no longer come; a live call removes its own
			delete(l.routes, id)
		}
	}
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
