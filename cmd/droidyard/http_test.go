package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

type countResult struct {
	Count int32 `json:"count"`
}

// startHTTP is the shipped wiring in process with two more test-only capabilities:
// fixture.count counts its invocations, and fixture.forhumans is for humans only.
func startHTTP(t *testing.T, httpAddr string) (*inProcess, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	p := startInProcessAt(t, httpAddr, func(reg *capability.Registry) {
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.count", Kind: capability.Command, Scope: capability.Global,
			Description: "Counts its invocations.", Params: struct{}{}, Result: countResult{},
			Handler: func(context.Context, *capability.Call) (any, error) {
				return countResult{Count: count.Add(1)}, nil
			},
		})
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.forhumans", Kind: capability.Command, Scope: capability.Global, Audience: capability.AudienceHuman,
			Description: "For humans only.", Params: struct{}{}, Result: struct{}{},
			Handler: func(context.Context, *capability.Call) (any, error) { return struct{}{}, nil },
		})
	})
	return p, &count
}

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(port)
	return n
}

// httpRequest is one request written byte for byte, so nothing normalises the header
// under test.
type httpRequest struct {
	method, path, proto string
	headers             []string // "Name: value"
	body                string
}

// callRequest is a well-formed POST /v1/call on port with the token and body; a test
// changes the one thing it is about.
func callRequest(port int, token, body string) httpRequest {
	return httpRequest{method: "POST", path: "/v1/call", proto: "HTTP/1.1", body: body, headers: []string{
		"Host: 127.0.0.1:" + strconv.Itoa(port),
		"Authorization: Bearer " + token,
		"Content-Type: application/json",
	}}
}

// without drops the headers named name; with adds one.
func (r httpRequest) without(name string) httpRequest {
	var hs []string
	for _, h := range r.headers {
		if !strings.HasPrefix(strings.ToLower(h), strings.ToLower(name)+":") {
			hs = append(hs, h)
		}
	}
	r.headers = hs
	return r
}

func (r httpRequest) with(header string) httpRequest {
	name, _, _ := strings.Cut(header, ":")
	r = r.without(name)
	r.headers = append(append([]string(nil), r.headers...), header)
	return r
}

// send writes r on a new connection to addr and reads the response.
func (r httpRequest) send(t *testing.T, addr string) *http.Response {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	var b strings.Builder
	b.WriteString(r.method + " " + r.path + " " + r.proto + "\r\n")
	for _, h := range r.headers {
		b.WriteString(h + "\r\n")
	}
	if r.body != "" {
		b.WriteString("Content-Length: " + strconv.Itoa(len(r.body)) + "\r\n")
	}
	b.WriteString("Connection: close\r\n\r\n")
	b.WriteString(r.body)
	go conn.Write([]byte(b.String()))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("%s %s: %v", r.method, r.path, err)
	}
	return resp
}

func envelopeOf(t *testing.T, resp *http.Response) capability.ResponseEnvelope {
	t.Helper()
	defer resp.Body.Close()
	var env capability.ResponseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("not an envelope: %v", err)
	}
	return env
}

// checkHeaders holds a response of ours to rule 5 and rule 7: nosniff and no-referrer
// everywhere, no-store on the API, and never a CORS grant or a cookie.
func checkHeaders(t *testing.T, what string, resp *http.Response, api bool) {
	t.Helper()
	h := resp.Header
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("%s: nosniff %q, Referrer-Policy %q", what, h.Get("X-Content-Type-Options"), h.Get("Referrer-Policy"))
	}
	if api && h.Get("Cache-Control") != "no-store" {
		t.Errorf("%s: Cache-Control %q; want no-store", what, h.Get("Cache-Control"))
	}
	for k := range h {
		if strings.HasPrefix(k, "Access-Control-Allow-") || k == "Set-Cookie" {
			t.Errorf("%s: carries %s", what, k)
		}
	}
}

// TestHTTPFence: every rule of the security baseline runs before any capability is
// resolved (http-websocket.md §Security baseline, rules 2 to 7): each rejected
// request gets its bare status and fixture.count never runs; then POST /v1/call
// answers 200 with an envelope as client ui, and a client that disconnects cancels
// nothing (ADR-0019 §Sessions). Real TCP, the real net/http server, the shipped
// wiring.
func TestHTTPFence(t *testing.T) {
	p, count := startHTTP(t, "127.0.0.1:0")
	addr := p.d.HTTPAddr()
	if host, _, _ := net.SplitHostPort(addr); host != "127.0.0.1" {
		t.Fatalf("the UI listens on %s; want 127.0.0.1", addr)
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("::1", strconv.Itoa(portOf(t, addr))), time.Second); err == nil {
		c.Close()
		t.Errorf("a connection to [::1] on the UI's port was accepted")
	}
	port := portOf(t, addr)
	ps, next := strconv.Itoa(port), strconv.Itoa(port+1)
	counting := callRequest(port, p.token, `{"id":"r","capability":"fixture.count"}`)
	big := callRequest(port, p.token, `{"capability":"fixture.count","id":"`+strings.Repeat("x", 1<<20+1-len(`{"capability":"fixture.count","id":""}`))+`"}`)
	if len(big.body) != 1<<20+1 {
		t.Fatalf("the oversized body is %d bytes", len(big.body))
	}
	short := p.token[:len(p.token)-1]
	root := httpRequest{method: "GET", path: "/", proto: "HTTP/1.1", headers: []string{"Host: 127.0.0.1:" + ps}}

	for _, c := range []struct {
		name string
		req  httpRequest
		want int
		ours bool // answered by our handler, not by net/http
	}{
		{"Host evil.example", counting.with("Host: evil.example:" + ps), 403, true},
		{"Host on another port", counting.with("Host: 127.0.0.1:" + next), 403, true},
		{"HTTP/1.0 without Host", func() httpRequest { r := counting.without("Host"); r.proto = "HTTP/1.0"; return r }(), 403, true},
		{"HTTP/1.1 without Host (net/http)", counting.without("Host"), 400, false},
		{"Origin evil.example", counting.with("Origin: http://evil.example"), 403, true},
		{"Origin null", counting.with("Origin: null"), 403, true},
		{"Origin on another port", counting.with("Origin: http://127.0.0.1:" + next), 403, true},
		{"GET / with Host evil.example", root.with("Host: evil.example:" + ps), 403, true},
		{"GET / with Origin evil.example", root.with("Origin: http://evil.example"), 403, true},
		{"an unknown API path without Authorization", func() httpRequest { r := counting.without("Authorization"); r.path = "/v1/nope"; return r }(), 401, true},
		{"a preflight", func() httpRequest {
			// A browser preflight carries no Authorization: the token check must not be what stops it.
			r := counting.without("Authorization").with("Access-Control-Request-Method: POST").with("Origin: http://localhost:" + ps)
			r.method = "OPTIONS"
			return r
		}(), 403, true},
		{"no Authorization", counting.without("Authorization"), 401, true},
		{"a wrong token", counting.with("Authorization: Bearer " + strings.Repeat("0", 64)), 401, true},
		{"a token one byte short", counting.with("Authorization: Bearer " + short), 401, true},
		{"Content-Type text/plain", counting.with("Content-Type: text/plain"), 415, true},
		{"a body of 1 MiB + 1", big, 413, true},
		{"malformed JSON", callRequest(port, p.token, `{`), 400, true},
	} {
		resp := c.req.send(t, addr)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d; want %d", c.name, resp.StatusCode, c.want)
		}
		if c.ours {
			checkHeaders(t, c.name, resp, strings.HasPrefix(c.req.path, "/v1/"))
		}
	}
	if n := count.Load(); n != 0 {
		t.Fatalf("fixture.count ran %d times behind rejected requests", n)
	}

	call := func(what string, req httpRequest) capability.ResponseEnvelope {
		t.Helper()
		resp := req.send(t, addr)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("%s: %d %s; want 200 with JSON", what, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		checkHeaders(t, what, resp, true)
		return envelopeOf(t, resp)
	}
	health := callRequest(port, p.token, `{"id":"h","capability":"system.health"}`)
	if env := call("an allowed Origin of the other name", health.with("Origin: http://localhost:"+ps)); env.Operation == nil || env.Operation.State != capability.StateCompleted || env.ID != "h" {
		t.Errorf("system.health with Origin localhost: %+v", env)
	}
	// Rule 6 parses the media type: parameters do not make it another one.
	if env := call("Content-Type with a charset", health.with("Content-Type: application/json; charset=utf-8")); env.Error != nil || env.Operation == nil {
		t.Errorf("a charset on the Content-Type: %+v", env)
	}
	if env := call("system.health", health); env.Operation == nil || env.Operation.State != capability.StateCompleted || env.Error != nil {
		t.Errorf("system.health: %+v", env)
	}
	if env := call("nope.nope", callRequest(port, p.token, `{"id":"n","capability":"nope.nope"}`)); env.Error == nil || env.Error.Code != capability.ErrCapabilityNotFound || env.Operation != nil {
		t.Errorf("nope.nope: %+v; want REQUEST_CAPABILITY_NOT_FOUND and no operation", env)
	}
	if env := call("an envelope that does not decode", callRequest(port, p.token, `{"id":"x","capability":7}`)); env.Error == nil || env.Error.Code != capability.ErrInvalidParams {
		t.Errorf("a bad envelope: %+v; want REQUEST_INVALID_PARAMS", env)
	}
	if env := call("fixture.count", counting); string(env.Result) != `{"count":1}` {
		t.Errorf("fixture.count: %+v; want count 1", env)
	}
	if env := call("the human-audience fixture", callRequest(port, p.token, `{"capability":"fixture.forhumans"}`)); env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Errorf("fixture.forhumans as client ui: %+v; want completed", env)
	}
	running := call("fixture.block with waitMs 0", callRequest(port, p.token, `{"capability":"fixture.block","waitMs":0}`))
	if running.Operation == nil || running.Operation.State != capability.StateRunning {
		t.Errorf("fixture.block with waitMs 0: %+v; want running", running)
	}
	<-p.started

	// A client that closes its connection mid-wait leaves the operation running.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"capability":"fixture.block"}`
	conn.Write([]byte("POST /v1/call HTTP/1.1\r\nHost: 127.0.0.1:" + ps + "\r\nAuthorization: Bearer " + p.token +
		"\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body))
	var id string
	select {
	case id = <-p.started:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture.block never started")
	}
	conn.Close()
	time.Sleep(300 * time.Millisecond)
	if out, err := p.ops.Wait(context.Background(), id, time.Now()); err != nil || out.State != capability.StateRunning {
		t.Fatalf("after its client closed the connection, the operation is %+v (%v); want running", out, err)
	}
	close(p.release)
	if out, err := p.ops.Wait(context.Background(), id, time.Now().Add(10*time.Second)); err != nil || out.State != capability.StateCompleted {
		t.Errorf("after the release the operation is %+v (%v); want completed", out, err)
	}

	// GET / is the placeholder, with the CSP and without the token; other paths and
	// methods are 404 and 405.
	page := httpRequest{method: "GET", path: "/", proto: "HTTP/1.1", headers: []string{"Host: localhost:" + ps}}.send(t, addr)
	page.Body.Close()
	if page.StatusCode != 200 || page.Header.Get("Content-Security-Policy") != "default-src 'self'; frame-ancestors 'none'" {
		t.Errorf("GET /: %d, CSP %q", page.StatusCode, page.Header.Get("Content-Security-Policy"))
	}
	checkHeaders(t, "GET /", page, false)
	for _, c := range []struct {
		req  httpRequest
		want int
	}{
		{httpRequest{method: "GET", path: "/assets/app.js", proto: "HTTP/1.1", headers: []string{"Host: 127.0.0.1:" + ps}}, 404},
		{httpRequest{method: "GET", path: "/v1/call", proto: "HTTP/1.1", headers: []string{"Host: 127.0.0.1:" + ps, "Authorization: Bearer " + p.token}}, 405},
		{httpRequest{method: "GET", path: "/v1/nope", proto: "HTTP/1.1", headers: []string{"Host: 127.0.0.1:" + ps, "Authorization: Bearer " + p.token}}, 404},
	} {
		resp := c.req.send(t, addr)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s: %d; want %d", c.req.method, c.req.path, resp.StatusCode, c.want)
		}
		checkHeaders(t, c.req.path, resp, strings.HasPrefix(c.req.path, "/v1/"))
	}
}

// TestHTTPFallsBackToAnEphemeralPort: with the asked-for port taken, the UI binds
// another loopback port and its allowlists follow it: Host and Origin name the port
// actually bound, never the one asked for (rule 1, rules 2 and 3); with the port free,
// it binds that one.
func TestHTTPFallsBackToAnEphemeralPort(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	asked := held.Addr().String()
	P := portOf(t, asked)
	p, _ := startHTTP(t, asked)
	E := portOf(t, p.d.HTTPAddr())
	if E == P || E == 0 {
		t.Fatalf("with port %d taken the UI bound %s", P, p.d.HTTPAddr())
	}
	health := callRequest(E, p.token, `{"capability":"system.health"}`)
	for _, c := range []struct {
		name string
		req  httpRequest
		want int
	}{
		{"Host on the bound port", health, 200},
		{"Host on the port asked for", health.with("Host: 127.0.0.1:" + strconv.Itoa(P)), 403},
		{"Origin on the port asked for", health.with("Origin: http://127.0.0.1:" + strconv.Itoa(P)), 403},
	} {
		resp := c.req.send(t, p.d.HTTPAddr())
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d; want %d", c.name, resp.StatusCode, c.want)
		}
	}

	held.Close()
	q, _ := startHTTP(t, asked)
	if got := q.d.HTTPAddr(); got != asked {
		t.Errorf("with port %d free the UI bound %s", P, got)
	}
}
