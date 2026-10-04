// Package httpapi is the HTTP adapter for the web UI (docs/interfaces/http-websocket.md):
// a loopback listener, the security baseline run on every request before any route,
// POST /v1/call and GET /v1/ws. Requests identify their adapter as ui (ADR-0015 §6). It
// translates; it adds no domain logic.
package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// DefaultPort is the fixed loopback port the UI is served on, until the configuration
// file can change it (plan m1d, D3).
const DefaultPort = 7317

// MaxBody bounds a request body (http-websocket.md §POST /v1/call: 413 over 1 MiB).
const MaxBody = 1 << 20

// CheckLoopback refuses a bind address whose host is not a loopback IP: LAN exposure
// needs its own ADR (http-websocket.md §Security baseline rule 1; plan m1d, R5).
func CheckLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("the HTTP address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("the HTTP address %q is not loopback; droidyard serves the UI on 127.0.0.1 only", addr)
	}
	return nil
}

// Listen binds addr, a loopback address, and falls back to an ephemeral port on the
// same host when its port is taken (rule 1). The listener's Addr is the address
// actually bound.
func Listen(addr string) (net.Listener, error) {
	if err := CheckLoopback(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if errors.Is(err, syscall.EADDRINUSE) {
		host, _, _ := net.SplitHostPort(addr)
		ln, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	return ln, err
}

// Server is the UI's HTTP side: the baseline, then GET /, POST /v1/call and, when it
// has a bus, GET /v1/ws. token is the per-launch token API requests must carry (the
// WebSocket carries it in its first frame instead).
type Server struct {
	handler http.Handler
	ws      *wsHandler
}

// New builds the UI's HTTP side. A nil bus serves no WebSocket.
func New(reg *capability.Registry, ops *capability.Operations, bus *event.Bus, token string) *Server {
	c := &caller{reg: reg, ops: ops}
	mux := http.NewServeMux()
	mux.Handle("/v1/call", c)
	s := &Server{}
	if bus != nil {
		s.ws = newWSHandler(c, bus, token)
		mux.Handle(wsPath, s.ws)
	}
	mux.HandleFunc("/", page)
	s.handler = &fence{next: mux, token: []byte(token)}
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// fence runs the security baseline (http-websocket.md §Security baseline, rules 2 to
// 7) on every request before any route, in the order of plan m1d, R2: the cheapest
// rejection first. A request that fails a rule gets a bare HTTP error and reaches no
// capability.
type fence struct {
	next  http.Handler
	token []byte
}

func (f *fence) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	api := strings.HasPrefix(r.URL.Path, "/v1/")
	if api {
		h.Set("Cache-Control", "no-store")
	}

	// The allowlists follow the port this connection was accepted on, the ephemeral
	// fallback included.
	port := 0
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok {
		port = a.Port
	}
	p := strconv.Itoa(port)
	if port == 0 || (r.Host != "127.0.0.1:"+p && r.Host != "localhost:"+p) {
		http.Error(w, "forbidden host", http.StatusForbidden) // rule 2
		return
	}
	origins := r.Header.Values("Origin")
	if len(origins) > 0 && (len(origins) != 1 || (origins[0] != "http://127.0.0.1:"+p && origins[0] != "http://localhost:"+p)) {
		http.Error(w, "forbidden origin", http.StatusForbidden) // rule 3
		return
	}
	if r.URL.Path == wsPath && len(origins) == 0 {
		http.Error(w, "the WebSocket needs an Origin", http.StatusForbidden) // rule 3: mandatory on the upgrade
		return
	}
	if r.Method == http.MethodOptions {
		http.Error(w, "no CORS", http.StatusForbidden) // rule 5
		return
	}
	// The WebSocket authenticates by its first frame (rule 4): a browser's upgrade
	// cannot carry Authorization. Every other method on that path still needs the token.
	if api && !(r.URL.Path == wsPath && r.Method == http.MethodGet) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// An empty token refuses everything: "" equals "" in a constant-time compare.
		if !ok || len(f.token) == 0 || subtle.ConstantTimeCompare([]byte(got), f.token) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized) // rule 4
			return
		}
	}
	if r.Method == http.MethodPost {
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			http.Error(w, "JSON only", http.StatusUnsupportedMediaType) // rule 6
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	f.next.ServeHTTP(w, r)
}

// placeholder is GET /: one static page with no script and no data (plan m1d, R6).
const placeholder = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Droidyard</title></head>
<body>
<h1>Droidyard</h1>
<p>The daemon is running. The web UI arrives in Phase 2.</p>
</body>
</html>
`

func page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
	h.Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, placeholder)
}

// caller is POST /v1/call: one request envelope in, one response envelope out, as
// client ui. Every answer is 200 with an envelope, rejections and failed operations
// included; only a body that is not JSON at all is 400 (plan m1d, R7). The request's
// context ending abandons the wait, never the operation (ADR-0019 §Sessions).
type caller struct {
	reg *capability.Registry
	ops *capability.Operations
}

func (c *caller) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		http.Error(w, "the body is over 1 MiB", http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		return // the client went away
	case !json.Valid(body):
		http.Error(w, "malformed JSON", http.StatusBadRequest)
		return
	}
	env, ok := c.answer(r.Context(), body, nil)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(env)
}

// answer runs one envelope: Start, then Wait for waitMs, or to the end without it. It
// reports false when the client is gone and nothing should be written. session is the
// session the request came in on: nil for a plain HTTP request, a WebSocket's own
// otherwise, and never a nil pointer inside a non-nil interface.
func (c *caller) answer(ctx context.Context, body []byte, session capability.Session) (capability.ResponseEnvelope, bool) {
	var req capability.RequestEnvelope
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var id struct {
			ID string `json:"id"`
		}
		json.Unmarshal(body, &id)
		return capability.ResponseEnvelope{ID: id.ID, Error: capability.NewError(capability.ErrInvalidParams,
			"the request is not a valid envelope").WithCause(err.Error())}, true
	}
	env := capability.ResponseEnvelope{ID: req.ID}
	creq := capability.Request{Capability: req.Capability, Workspace: req.Workspace, Params: req.Params, Client: capability.ClientUI, Session: session}

	var out capability.Outcome
	var err error
	if d, ok := c.reg.Lookup(req.Capability); ok && !d.Recorded() {
		// operation.wait and operation.cancel are not operations.
		out, err = c.ops.Invoke(ctx, creq)
	} else {
		var id string
		if id, err = c.ops.Start(ctx, creq); err == nil {
			var deadline time.Time
			if req.WaitMs != nil {
				deadline = time.Now().Add(time.Duration(*req.WaitMs) * time.Millisecond)
			}
			out, err = c.ops.Wait(ctx, id, deadline)
		}
	}
	if ctx.Err() != nil {
		return env, false
	}
	if err != nil {
		env.Error = capability.AsError(err)
		return env, true
	}
	if out.OperationID != "" {
		env.Operation = &capability.OperationStatus{ID: out.OperationID, State: out.State, Progress: out.Progress, BlockedBy: out.BlockedBy}
	}
	switch out.State {
	case capability.StateCompleted:
		raw, merr := json.Marshal(out.Result)
		if merr != nil {
			env.Error = capability.AsError(merr)
			break
		}
		env.Result = raw
		env.Meta = out.Meta()
	case capability.StateFailed, capability.StateCancelled:
		env.Error = out.Error
	}
	return env, true
}
