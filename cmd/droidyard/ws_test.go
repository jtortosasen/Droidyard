package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// The WebSocket session, on the shipped wiring in process with real TCP and a
// coder/websocket client (plan m1d, step 7). A test-only fixture.publish publishes
// params.count events on workspace.lifecycle.

type publishParams struct {
	Count int `json:"count"`
}

func startWS(t *testing.T) *inProcess {
	t.Helper()
	var bus atomic.Pointer[event.Bus]
	p := startInProcessAt(t, "127.0.0.1:0", func(reg *capability.Registry) {
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.publish", Kind: capability.Command, Scope: capability.Global,
			Description: "Publishes count events on workspace.lifecycle.", Params: publishParams{}, Result: struct{}{},
			Handler: func(_ context.Context, call *capability.Call) (any, error) {
				b := bus.Load()
				for i := 0; i < call.Params.(publishParams).Count; i++ {
					b.Publish("workspace.lifecycle", "x", "", "", nil)
				}
				return struct{}{}, nil
			},
		})
	})
	bus.Store(p.bus)
	return p
}

// wsFrame is any frame the server sends: a response envelope, or an event frame.
type wsFrame struct {
	capability.ResponseEnvelope
	SubscriptionID string       `json:"subscriptionId"`
	Event          *event.Event `json:"event"`
}

// wsClient reads in a goroutine of its own, as a browser does, so that a test can wait
// for a frame with a timeout without cancelling a read (which would close the
// connection) and the client answers the server's pings.
type wsClient struct {
	t      *testing.T
	c      *websocket.Conn
	frames chan []byte
	done   chan struct{}
	err    error // why the connection ended; valid once done is closed
}

func wsURL(p *inProcess) string { return "ws://" + p.d.HTTPAddr() + "/v1/ws" }

func allowedOrigin(p *inProcess) string { return "http://" + p.d.HTTPAddr() }

// dialWS upgrades with an allowed Origin and the client's pump started, not yet
// authenticated.
func dialWS(t *testing.T, p *inProcess) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, wsURL(p), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {allowedOrigin(p)}}})
	if err != nil {
		t.Fatalf("the upgrade: %v (response %v)", err, resp)
	}
	return pump(t, c)
}

func pump(t *testing.T, c *websocket.Conn) *wsClient {
	c.SetReadLimit(8 << 20)
	w := &wsClient{t: t, c: c, frames: make(chan []byte, 8192), done: make(chan struct{})}
	t.Cleanup(func() { c.CloseNow() })
	go func() {
		defer close(w.done)
		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				w.err = err
				return
			}
			w.frames <- data
		}
	}()
	return w
}

// wsConnect is dialWS and the auth frame.
func wsConnect(t *testing.T, p *inProcess) *wsClient {
	t.Helper()
	w := dialWS(t, p)
	w.writeText(`{"auth":"` + p.token + `"}`)
	return w
}

func (w *wsClient) writeText(s string) { w.t.Helper(); w.writeFrame(websocket.MessageText, s) }

func (w *wsClient) writeFrame(typ websocket.MessageType, s string) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.c.Write(ctx, typ, []byte(s)); err != nil {
		w.t.Fatalf("writing a frame: %v", err)
	}
}

// send writes one request envelope.
func (w *wsClient) send(id, name, workspace, params string, waitMs *int64) {
	w.t.Helper()
	req := capability.RequestEnvelope{ID: id, Capability: name, Workspace: workspace, WaitMs: waitMs}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		w.t.Fatal(err)
	}
	w.writeText(string(raw))
}

// next is the next frame, or ok false when none arrives within d or the connection
// has ended.
func (w *wsClient) next(d time.Duration) (f wsFrame, ok bool) {
	w.t.Helper()
	select {
	case data := <-w.frames:
		if err := json.Unmarshal(data, &f); err != nil {
			w.t.Fatalf("a frame that is not JSON: %v: %.200s", err, data)
		}
		return f, true
	case <-time.After(d):
	case <-w.done:
		select { // frames that arrived before the end are still frames
		case data := <-w.frames:
			json.Unmarshal(data, &f)
			return f, true
		default:
		}
	}
	return wsFrame{}, false
}

// must is next with a deadline that fails the test.
func (w *wsClient) must(what string) wsFrame {
	w.t.Helper()
	f, ok := w.next(10 * time.Second)
	if !ok {
		w.t.Fatalf("%s: no frame (the connection ended: %v)", what, w.endedErr())
	}
	return f
}

func (w *wsClient) endedErr() error {
	select {
	case <-w.done:
		return w.err
	default:
		return nil
	}
}

// closedWith waits for the connection to end and returns its close status, and when it
// ended.
func (w *wsClient) closedWith(within time.Duration) (websocket.StatusCode, time.Time) {
	w.t.Helper()
	select {
	case <-w.done:
		return websocket.CloseStatus(w.err), time.Now()
	case <-time.After(within):
		w.t.Fatalf("the connection stayed open for %v", within)
		return 0, time.Time{}
	}
}

func subscriptionOf(t *testing.T, f wsFrame) string {
	t.Helper()
	var r struct {
		ID string `json:"subscriptionId"`
	}
	if f.Error != nil || json.Unmarshal(f.Result, &r) != nil || r.ID == "" {
		t.Fatalf("a response that should carry a subscriptionId: %+v (error %+v)", f, f.Error)
	}
	return r.ID
}

const lifecycleSubscribe = `{"topics":["workspace.lifecycle"]}`

// subscribed sends events.subscribe, reads its response and returns the id.
func (w *wsClient) subscribed(id, params string) string {
	w.t.Helper()
	w.send(id, "events.subscribe", "", params, nil)
	f := w.must("the subscribe response")
	if f.ID != id || f.Event != nil {
		w.t.Fatalf("the first frame after subscribing is %+v; want the response %s", f, id)
	}
	return subscriptionOf(w.t, f)
}

// waitSubscriptions polls the bus until it holds n subscriptions.
func waitSubscriptions(t *testing.T, p *inProcess, n int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if p.bus.Subscriptions() == n {
			return
		}
	}
	t.Fatalf("the bus holds %d subscriptions; want %d", p.bus.Subscriptions(), n)
}

// TestWSUpgradeFence: the upgrade runs the fence first, and Origin is mandatory on it
// (http-websocket.md §Security baseline rules 2, 3). The library's own origin check is
// relaxed to the two allowed origins of the bound port, so Origin localhost on Host
// 127.0.0.1 upgrades, and the same with the names swapped. Only a GET to /v1/ws is
// exempt from the bearer token: any other method on it still needs one.
func TestWSUpgradeFence(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	port := portOf(t, p.d.HTTPAddr())
	ps := strconv.Itoa(port)
	for _, c := range []struct {
		name   string
		origin string // "" sends none
		host   string // "" is the URL's
		want   int    // 0: upgraded
	}{
		{"no Origin", "", "", 403},
		{"Origin evil.example", "http://evil.example", "", 403},
		{"Origin null", "null", "", 403},
		{"Origin on another port", "http://127.0.0.1:" + strconv.Itoa(port+1), "", 403},
		{"Host evil.example", "http://127.0.0.1:" + ps, "evil.example:" + ps, 403},
		{"Origin localhost on Host 127.0.0.1", "http://localhost:" + ps, "", 0},
		{"Origin 127.0.0.1 on Host localhost", "http://127.0.0.1:" + ps, "localhost:" + ps, 0},
		{"Origin 127.0.0.1 on Host 127.0.0.1", "http://127.0.0.1:" + ps, "", 0},
	} {
		hdr := http.Header{}
		if c.origin != "" {
			hdr.Set("Origin", c.origin)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, resp, err := websocket.Dial(ctx, wsURL(p), &websocket.DialOptions{HTTPHeader: hdr, Host: c.host})
		cancel()
		switch {
		case c.want == 0 && err != nil:
			t.Errorf("%s: %v (response %v); want an upgrade", c.name, err, resp)
		case c.want == 0:
			conn.CloseNow()
		case err == nil:
			conn.CloseNow()
			t.Errorf("%s: upgraded; want %d", c.name, c.want)
		case resp == nil || resp.StatusCode != c.want:
			t.Errorf("%s: %v; want %d", c.name, resp, c.want)
		}
	}

	// A POST to the route is not the WebSocket: the token is still required.
	post := httpRequest{method: "POST", path: "/v1/ws", proto: "HTTP/1.1", body: "{}", headers: []string{
		"Host: 127.0.0.1:" + ps, "Content-Type: application/json", "Origin: http://127.0.0.1:" + ps}}
	if resp := post.send(t, p.d.HTTPAddr()); resp.StatusCode != 401 {
		t.Errorf("POST /v1/ws without a token: %d; want 401", resp.StatusCode)
	}
	// And an unknown API path without a token is still 401, as before.
	nope := httpRequest{method: "POST", path: "/v1/nope", proto: "HTTP/1.1", body: "{}", headers: []string{
		"Host: 127.0.0.1:" + ps, "Content-Type: application/json"}}
	if resp := nope.send(t, p.d.HTTPAddr()); resp.StatusCode != 401 {
		t.Errorf("POST /v1/nope without a token: %d; want 401", resp.StatusCode)
	}
}

// TestWSAuth: the first frame must be {"auth": "<token>"}; a wrong token, an envelope
// or an empty auth closes with 4401 and answers nothing (rule 4).
func TestWSAuth(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	for _, c := range []struct{ name, first string }{
		{"a wrong token", `{"auth":"` + strings.Repeat("0", 64) + `"}`},
		{"a token one byte short", `{"auth":"` + p.token[:63] + `"}`},
		{"an empty token", `{"auth":""}`},
		{"a request envelope", `{"id":"r1","capability":"system.health"}`},
		{"not JSON", `hello`},
	} {
		w := dialWS(t, p)
		w.writeText(c.first)
		if code, _ := w.closedWith(10 * time.Second); code != 4401 {
			t.Errorf("%s as the first frame: closed with %d; want 4401", c.name, code)
		}
		if f, ok := w.next(0); ok {
			t.Errorf("%s: answered %+v", c.name, f)
		}
	}
}

// TestWSNonTextFrames: the auth frame must be text even when it carries the right
// token, and a binary frame after auth ends the session with 1003.
func TestWSNonTextFrames(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	w := dialWS(t, p)
	w.writeFrame(websocket.MessageBinary, `{"auth":"`+p.token+`"}`)
	if code, _ := w.closedWith(5 * time.Second); code != 4401 {
		t.Errorf("a binary auth frame with the right token: closed with %d; want 4401", code)
	}

	a := wsConnect(t, p)
	a.send("r1", "system.health", "", "", nil)
	a.must("system.health")
	a.writeFrame(websocket.MessageBinary, `{}`)
	if code, _ := a.closedWith(5 * time.Second); code != websocket.StatusUnsupportedData {
		t.Errorf("a binary frame after auth: closed with %d; want %d", code, websocket.StatusUnsupportedData)
	}
}

// TestWSPreAuthLimit: before auth a frame over 4 KiB closes with 1009; after auth the
// limit is 1 MiB (TestWSFrameSize).
func TestWSPreAuthLimit(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	w := dialWS(t, p)
	w.writeText(`{"auth":"` + strings.Repeat("x", 5000) + `"}`)
	if code, _ := w.closedWith(5 * time.Second); code != websocket.StatusMessageTooBig {
		t.Errorf("a 5000-byte first frame: closed with %d; want %d", code, websocket.StatusMessageTooBig)
	}
}

// TestWSAuthDeadline: a connection that sends nothing is closed with 4401 after 5 s,
// not before and not much later (rule 4).
func TestWSAuthDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("waits 5 s")
	}
	// The 5 s is the default's, so the test waits it out; it is independent of every
	// other test, so it waits beside the other parallel ones, not before or after them.
	t.Parallel()
	p := startWS(t)
	// Timed from before the dial: the server's deadline starts when it accepts, which is
	// before dialWS returns, so a start taken after it read 4.99 s under load.
	start := time.Now()
	w := dialWS(t, p)
	code, at := w.closedWith(8 * time.Second)
	if elapsed := at.Sub(start); code != 4401 || elapsed < 5*time.Second || elapsed > 6*time.Second {
		t.Errorf("a silent connection was closed with %d after %v; want 4401 between 5 s and 6 s", code, elapsed)
	}
}

// TestWSSession: after the auth frame, a request envelope gets its response envelope
// by id; a subscription's events arrive as frames naming it, in seq order, around a
// real workspace.open's own response (the order between them is not asserted).
func TestWSSession(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	w := wsConnect(t, p)

	w.send("r1", "system.health", "", "", nil)
	if f := w.must("system.health"); f.ID != "r1" || f.Event != nil || f.Operation == nil || f.Operation.State != capability.StateCompleted || f.Error != nil {
		t.Fatalf("system.health: %+v; want its response with id r1", f)
	}

	sub := w.subscribed("s1", lifecycleSubscribe)
	root, _ := gradleProject(t)
	wid, _ := platform.Current().WorkspaceID(root)
	w.send("o1", "workspace.open", root, "{}", nil)
	var events []*event.Event
	opened := false
	for len(events) < 2 || !opened {
		f := w.must("workspace.open's frames")
		switch {
		case f.Event != nil:
			if f.SubscriptionID != sub {
				t.Fatalf("an event frame names %q; want %q", f.SubscriptionID, sub)
			}
			events = append(events, f.Event)
		case f.ID == "o1":
			if f.Operation == nil || f.Operation.State != capability.StateCompleted {
				t.Fatalf("workspace.open: %+v", f)
			}
			opened = true
		default:
			t.Fatalf("an unexpected frame: %+v", f)
		}
	}
	if len(events) != 2 || events[0].Type != "opening" || events[1].Type != "ready" || events[0].Seq >= events[1].Seq ||
		events[0].Topic != "workspace.lifecycle" || events[0].Workspace == nil || *events[0].Workspace != wid {
		t.Errorf("events: %+v %+v; want opening then ready, seq increasing, for workspace %s", events[0], events[1], wid)
	}
	if f, ok := w.next(300 * time.Millisecond); ok {
		t.Errorf("an extra frame: %+v", f)
	}

	// events.unsubscribe over the session answers {} and ends the stream.
	w.send("u1", "events.unsubscribe", "", `{"subscriptionId":"`+sub+`"}`, nil)
	if f := w.must("unsubscribe"); f.ID != "u1" || string(f.Result) != "{}" {
		t.Errorf("events.unsubscribe: %+v", f)
	}
	waitSubscriptions(t, p, 0)
}

// TestWSResume: a second connection that subscribes with `since` gets the subscribe's
// response first, on every run, and then the events after that point; with since of
// another epoch the response is followed by an events.gap first (D4, D5).
func TestWSResume(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	a := wsConnect(t, p)
	a.subscribed("s1", lifecycleSubscribe)
	root, _ := gradleProject(t)
	a.send("o1", "workspace.open", root, "{}", nil)
	var opening *event.Event
	for seen := 0; seen < 3; {
		f := a.must("the first connection's frames")
		seen++
		if f.Event != nil && f.Event.Type == "opening" {
			opening = f.Event
		}
	}
	if opening == nil {
		t.Fatal("no opening event")
	}
	since := fmt.Sprintf(`{"topics":["workspace.lifecycle"],"since":{"epoch":%q,"seq":%d}}`, opening.Epoch, opening.Seq)

	for run := 0; run < 20; run++ {
		b := wsConnect(t, p)
		b.send("s2", "events.subscribe", "", since, nil)
		f := b.must("the resumed subscribe")
		if f.ID != "s2" || f.Event != nil {
			t.Fatalf("run %d: the first frame of a resumed subscription is %+v; want the response", run, f)
		}
		id := subscriptionOf(t, f)
		f = b.must("the replay")
		if f.Event == nil || f.SubscriptionID != id || f.Event.Type != "ready" {
			t.Fatalf("run %d: after the response, %+v; want the ready event of %s", run, f, id)
		}
		if g, ok := b.next(20 * time.Millisecond); ok {
			t.Fatalf("run %d: an extra frame after ready: %+v", run, g)
		}
		b.c.CloseNow()
	}

	// The same with the response held back by 150 ms inside the handler, after the
	// subscription and its replay exist: the order does not depend on who wins a race.
	p.bus.SetSubscribeHook(func() { time.Sleep(150 * time.Millisecond) })
	slow := wsConnect(t, p)
	slow.send("s4", "events.subscribe", "", since, nil)
	if f := slow.must("the held, resumed subscribe"); f.ID != "s4" || f.Event != nil {
		t.Fatalf("with the response held, the first frame is %+v; want the response", f)
	}
	if f := slow.must("the held replay"); f.Event == nil || f.Event.Type != "ready" {
		t.Fatalf("after the held response, %+v; want ready", f)
	}
	p.bus.SetSubscribeHook(nil)

	other := wsConnect(t, p)
	other.send("s3", "events.subscribe", "", `{"topics":["workspace.lifecycle"],"since":{"epoch":"d-00000000","seq":5}}`, nil)
	f := other.must("the other-epoch subscribe")
	if f.ID != "s3" || f.Event != nil {
		t.Fatalf("an other-epoch subscribe: first frame %+v; want the response", f)
	}
	id := subscriptionOf(t, f)
	f = other.must("the gap")
	if f.Event == nil || f.SubscriptionID != id || f.Event.Topic != "events.gap" || f.Event.Type != "gap" {
		t.Fatalf("after an other-epoch subscribe, %+v; want an events.gap first", f)
	}
	for _, want := range []string{"opening", "ready"} {
		if f = other.must("the ring's events"); f.Event == nil || f.Event.Type != want {
			t.Errorf("after the gap, %+v; want %s", f, want)
		}
	}
}

// TestWSSubscriptionIDFirst: a subscription's first frame follows a response that
// carries its id, even when the subscribe's own response did not (its waitMs ran out
// with the operation running). A hook inside the events.subscribe handler holds it
// after the subscription exists; workspace.open publishes meanwhile; the hook is
// released and nothing arrives for 500 ms although the subscribe has completed; then
// operation.wait answers with the id and the held events follow that response, never
// before it (plan m1d, D5).
func TestWSSubscriptionIDFirst(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	created, hold := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	p.bus.SetSubscribeHook(func() {
		created <- struct{}{}
		<-hold
	})
	t.Cleanup(func() { p.bus.SetSubscribeHook(nil) })

	w := wsConnect(t, p)
	zero := int64(0)
	w.send("s1", "events.subscribe", "", lifecycleSubscribe, &zero)
	f := w.must("the running subscribe")
	if f.ID != "s1" || f.Operation == nil || f.Operation.State != capability.StateRunning || f.Result != nil {
		t.Fatalf("the held subscribe answered %+v; want running with no result", f)
	}
	opID := f.Operation.ID
	<-created // the subscription exists

	root, _ := gradleProject(t)
	w.send("o1", "workspace.open", root, "{}", nil)
	if f = w.must("workspace.open"); f.ID != "o1" || f.Event != nil {
		t.Fatalf("with the subscription held, %+v arrived; want only workspace.open's response", f)
	}
	release()
	// The subscribe completes; its response had no id to give, so nothing is released.
	if out, err := p.ops.Wait(context.Background(), opID, time.Now().Add(10*time.Second)); err != nil || out.State != capability.StateCompleted {
		t.Fatalf("the subscribe operation: %+v, %v; want completed", out, err)
	}
	if f, ok := w.next(500 * time.Millisecond); ok {
		t.Fatalf("a frame %+v arrived although no response had named the subscription", f)
	}

	w.send("w1", "operation.wait", "", `{"operationId":"`+opID+`"}`, nil)
	f = w.must("operation.wait")
	if f.ID != "w1" || f.Event != nil || f.Operation == nil || f.Operation.State != capability.StateCompleted {
		t.Fatalf("after operation.wait, the first frame is %+v; want its response", f)
	}
	id := subscriptionOf(t, f)
	for _, want := range []string{"opening", "ready"} {
		if f = w.must("the held events"); f.Event == nil || f.SubscriptionID != id || f.Event.Type != want {
			t.Errorf("after the response, %+v; want %s on %s", f, want, id)
		}
	}
}

// TestWSFrameSize: a request envelope of exactly 1 MiB is answered; one byte more
// closes the connection with "message too big" (the library's default would close at
// 32 KiB).
func TestWSFrameSize(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	envelope := func(size int) string {
		base := `{"capability":"system.health","id":""}`
		return `{"capability":"system.health","id":"` + strings.Repeat("x", size-len(base)) + `"}`
	}
	w := wsConnect(t, p)
	big := envelope(1 << 20)
	if len(big) != 1<<20 {
		t.Fatalf("the envelope is %d bytes", len(big))
	}
	w.writeText(big)
	if f := w.must("the 1 MiB envelope"); len(f.ID) != 1<<20-len(`{"capability":"system.health","id":""}`) || f.Operation == nil || f.Operation.State != capability.StateCompleted {
		t.Fatalf("the 1 MiB envelope was answered with an id of %d bytes: %+v", len(f.ID), f.Operation)
	}
	w.writeText(envelope(1<<20 + 1))
	if code, _ := w.closedWith(10 * time.Second); code != websocket.StatusMessageTooBig {
		t.Errorf("an envelope of 1 MiB + 1 closed the connection with %d; want %d", code, websocket.StatusMessageTooBig)
	}
}

// TestWSOverflow: with a 16-frame queue and the session's writer held, 2000 published
// events overflow the subscription; once the writer is released, the delivered seqs
// and the gap ranges together cover every one of them, with no seq twice, in order,
// and at least one gap. The hold, not socket buffering, decides that the queue fills.
func TestWSOverflow(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	p.http.SetQueue(16)
	w := wsConnect(t, p)
	w.subscribed("s1", lifecycleSubscribe)

	gate := make(chan struct{})
	var once sync.Once
	release := func() { p.http.SetWriteHook(nil); once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	p.http.SetWriteHook(func() { <-gate })

	first := p.bus.Position().Seq
	const n = 2000
	w.send("pub", "fixture.publish", "", `{"count":2000}`, nil)
	for deadline := time.Now().Add(10 * time.Second); p.bus.Position().Seq < first+n; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the bus is at %d; want %d", p.bus.Position().Seq, first+n)
		}
	}
	release()

	covered := map[int64]bool{}
	last, delivered, gaps, answered := first, 0, 0, false
	for last < first+n || !answered {
		f := w.must(fmt.Sprintf("frames after seq %d", last))
		if f.ID == "pub" {
			answered = true
			continue
		}
		if f.Event == nil {
			t.Fatalf("an unexpected frame: %+v", f)
		}
		from, to := f.Event.Seq, f.Event.Seq
		if f.Event.Topic == "events.gap" {
			var g event.Gap
			if err := json.Unmarshal(f.Event.Payload, &g); err != nil || g.ToSeq != f.Event.Seq {
				t.Fatalf("a gap frame: %+v (%v)", f.Event, err)
			}
			from, to = g.FromSeq, g.ToSeq
			gaps++
		} else {
			delivered++
		}
		if from <= last {
			t.Fatalf("frame covering %d..%d after %d: out of order or repeated", from, to, last)
		}
		for s := from; s <= to; s++ {
			covered[s] = true
		}
		last = to
	}
	for s := first + 1; s <= first+n; s++ {
		if !covered[s] {
			t.Fatalf("seq %d was neither delivered nor covered by a gap", s)
		}
	}
	// 16 queued plus the one the writer was holding; the bus's own queue is 1024.
	if gaps == 0 || delivered > 17 {
		t.Errorf("%d events delivered and %d gaps for %d published into a queue of 16", delivered, gaps, n)
	}
}

// TestWSTwoSubscriptions: an event that matches two subscriptions of one connection is
// sent once under each id.
func TestWSTwoSubscriptions(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	w := wsConnect(t, p)
	a := w.subscribed("s1", lifecycleSubscribe)
	b := w.subscribed("s2", lifecycleSubscribe)
	if a == b {
		t.Fatalf("two subscriptions share the id %s", a)
	}
	root, _ := gradleProject(t)
	w.send("o1", "workspace.open", root, "{}", nil)
	got := map[string][]string{}
	for opened := false; !opened || len(got[a]) < 2 || len(got[b]) < 2; {
		f := w.must("the frames of two subscriptions")
		if f.Event == nil {
			opened = opened || f.ID == "o1"
			continue
		}
		got[f.SubscriptionID] = append(got[f.SubscriptionID], f.Event.Type)
	}
	for _, id := range []string{a, b} {
		if strings.Join(got[id], ",") != "opening,ready" {
			t.Errorf("subscription %s received %v; want opening, ready", id, got[id])
		}
	}
	if f, ok := w.next(200 * time.Millisecond); ok {
		t.Errorf("an extra frame: %+v", f)
	}
}

// TestWSDisconnect: closing the connection leaves the bus with no subscription of that
// session, and an operation started on it runs on to completed: a disconnect ends
// waits, never operations (ADR-0019 §Sessions).
func TestWSDisconnect(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	w := wsConnect(t, p)
	w.subscribed("s1", lifecycleSubscribe)
	waitSubscriptions(t, p, 1)

	zero := int64(0)
	w.send("b1", "fixture.block", "", "", &zero)
	f := w.must("fixture.block")
	if f.Operation == nil || f.Operation.State != capability.StateRunning {
		t.Fatalf("fixture.block: %+v; want running", f)
	}
	opID := <-p.started
	w.c.Close(websocket.StatusNormalClosure, "")
	waitSubscriptions(t, p, 0)

	if out, err := p.ops.Wait(context.Background(), opID, time.Now()); err != nil || out.State != capability.StateRunning {
		t.Fatalf("after the session ended the operation is %+v (%v); want running", out, err)
	}
	close(p.release)
	if out, err := p.ops.Wait(context.Background(), opID, time.Now().Add(10*time.Second)); err != nil || out.State != capability.StateCompleted {
		t.Errorf("after the release the operation is %+v (%v); want completed", out, err)
	}

	// A connection that drops without a close frame ends the same way.
	d := wsConnect(t, p)
	d.subscribed("s2", lifecycleSubscribe)
	waitSubscriptions(t, p, 1)
	d.c.CloseNow()
	waitSubscriptions(t, p, 0)
}

// rawClient speaks the little of RFC 6455 a test needs by hand so that it can stay
// silent: it never reads, so it never answers a ping.
type rawClient struct {
	conn net.Conn
}

func dialRaw(t *testing.T, p *inProcess) *rawClient {
	t.Helper()
	addr := p.d.HTTPAddr()
	ps := strconv.Itoa(portOf(t, addr))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "GET /v1/ws HTTP/1.1\r\nHost: 127.0.0.1:%s\r\nOrigin: http://127.0.0.1:%s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", ps, ps)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("the raw upgrade: %v %v", resp, err)
	}
	return &rawClient{conn: conn}
}

// text writes one masked text frame (the mask key is zero, which RFC 6455 allows).
func (r *rawClient) text(t *testing.T, s string) {
	t.Helper()
	var b bytes.Buffer
	b.WriteByte(0x81)
	switch n := len(s); {
	case n < 126:
		b.WriteByte(0x80 | byte(n))
	default:
		b.WriteByte(0x80 | 126)
		b.WriteByte(byte(n >> 8))
		b.WriteByte(byte(n))
	}
	b.Write([]byte{0, 0, 0, 0})
	b.WriteString(s)
	if _, err := r.conn.Write(b.Bytes()); err != nil {
		t.Fatal(err)
	}
}

// TestWSPing: with a 400 ms ping interval, a client that answers pings stays
// connected through many intervals; one that never answers is closed after two missed
// pongs, which is neither at the first miss nor much after the second (§GET /v1/ws).
func TestWSPing(t *testing.T) {
	// Waits out several 400 ms intervals, so it waits beside the other parallel tests;
	// its own timing window (200 ms of slack) is between its connection and the daemon.
	t.Parallel()
	p := startWS(t)
	p.http.SetPingInterval(400 * time.Millisecond)

	live := wsConnect(t, p)
	live.subscribed("s1", lifecycleSubscribe)
	time.Sleep(2 * time.Second) // five intervals, each pinged and answered
	if p.bus.Subscriptions() != 1 || live.endedErr() != nil {
		t.Fatalf("a client that answers pings was closed: %d subscriptions, %v", p.bus.Subscriptions(), live.endedErr())
	}
	live.c.CloseNow()
	waitSubscriptions(t, p, 0)

	silent := dialRaw(t, p)
	start := time.Now()
	silent.text(t, `{"auth":"`+p.token+`"}`)
	silent.text(t, `{"id":"s1","capability":"events.subscribe","params":`+lifecycleSubscribe+`}`)
	waitSubscriptions(t, p, 1)
	waitSubscriptions(t, p, 0)
	elapsed := time.Since(start)
	// The session starts at the auth frame: pings go out at 400 and 800 ms, and the
	// second miss is counted at 1200 ms; a third would close at 1600 ms. The window
	// 1000..1400 ms is 200 ms of scheduling slack, and excludes both one and three.
	if elapsed < 1000*time.Millisecond || elapsed > 1400*time.Millisecond {
		t.Errorf("a client that never answers was closed after %v; want about 1200 ms (two missed pongs of 400 ms)", elapsed)
	}
}

// TestWSStuckWriter: a client that subscribes and then stops reading while events flood
// in fills the socket buffers and blocks the session's writer. The session must still
// end, within a bound, and take its subscription with it: a stuck writer must not
// hide the missed pongs nor outlive them.
func TestWSStuckWriter(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	p.http.SetPingInterval(200 * time.Millisecond)
	stuck := dialRaw(t, p)
	stuck.text(t, `{"auth":"`+p.token+`"}`)
	stuck.text(t, `{"id":"s1","capability":"events.subscribe","params":`+lifecycleSubscribe+`}`)
	waitSubscriptions(t, p, 1)
	start := time.Now()

	pub := wsConnect(t, p)
	pub.send("pub", "fixture.publish", "", `{"count":400000}`, nil)
	for deadline := time.Now().Add(5 * time.Second); p.bus.Subscriptions() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the session of a client that stopped reading is still alive after %v", time.Since(start))
		}
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("the session ended after %v; want within about 1 s of a 200 ms ping", elapsed)
	}
}

// TestWSShutdownWaits: daemon shutdown returns only when the WebSocket sessions have
// ended, not when they were told to. A session whose writer is held cannot end; with
// it held, Shutdown must still be waiting, and returns once the hold is released.
func TestWSShutdownWaits(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	w := wsConnect(t, p)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { p.http.SetWriteHook(nil); once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	held := make(chan struct{})
	var heldOnce sync.Once
	p.http.SetWriteHook(func() { heldOnce.Do(func() { close(held) }); <-gate })
	w.send("r1", "system.health", "", "", nil) // its response is held in the writer
	// Shutdown only once the response is in the writer: sent at once, a loaded machine
	// could close the session before the request reached the writer, and the session
	// would end with nothing to wait for.
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the response never reached the writer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	returned := make(chan struct{})
	go func() { p.d.Shutdown(ctx); close(returned) }()
	select {
	case <-returned:
		t.Fatal("Shutdown returned while a WebSocket session was still running")
	case <-time.After(500 * time.Millisecond):
	}
	release()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return after the session could end")
	}
	if n := p.http.Sessions(); n != 0 {
		t.Errorf("%d WebSocket sessions after Shutdown returned", n)
	}
}

// TestWSEndsWithTheDaemon: stopping the daemon closes its WebSocket sessions with the
// others, and their subscriptions with them (plan m1d, R10).
func TestWSEndsWithTheDaemon(t *testing.T) {
	t.Parallel()
	p := startWS(t)
	w := wsConnect(t, p)
	w.subscribed("s1", lifecycleSubscribe)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.d.Shutdown(ctx); err != nil {
		t.Logf("shutdown: %v", err)
	}
	if code, _ := w.closedWith(10 * time.Second); code != websocket.StatusGoingAway {
		t.Errorf("the session was closed with %d; want %d", code, websocket.StatusGoingAway)
	}
	waitSubscriptions(t, p, 0)
	// Shutdown waited for the sessions to end, not only told them to.
	if n := p.http.Sessions(); n != 0 {
		t.Errorf("%d WebSocket sessions after Shutdown returned", n)
	}
}
