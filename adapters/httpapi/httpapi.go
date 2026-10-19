// Package httpapi is the HTTP adapter for the web UI (docs/interfaces/http-websocket.md):
// a loopback listener, the security baseline run on every request before any route,
// the built UI at / and /assets/, POST /v1/call, GET /v1/ws and GET /v1/screen. Requests
// identify their adapter as ui (ADR-0015 §6). It translates; it adds no domain logic.
package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
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

// MaxBody bounds a request body: a larger one is answered 413.
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

// Server is the UI's HTTP side: the baseline, then GET / and GET /assets/ (the built UI,
// ADR-0040), POST /v1/call, GET /v1/artifacts/{id} and, when it has a bus, GET /v1/ws.
// token is the per-launch token API requests must carry (the WebSocket carries it in its
// first frame instead).
type Server struct {
	handler http.Handler
	ws      *wsHandler
	screen  *screenHandler
}

// New builds the UI's HTTP side. A nil bus serves no WebSocket. ui is the built web
// app rooted at its index.html (web.Assets); nil serves the placeholder instead.
func New(reg *capability.Registry, ops *capability.Operations, bus *event.Bus, token string, ui fs.FS) *Server {
	c := &caller{reg: reg, ops: ops}
	mux := http.NewServeMux()
	mux.Handle("/v1/call", c)
	mux.Handle(artifactsPath, &artifactRoute{c})
	s := &Server{screen: newScreenHandler(token)}
	mux.Handle(screenPath, s.screen)
	if bus != nil {
		s.ws = newWSHandler(c, bus, token)
		mux.Handle(wsPath, s.ws)
	}
	mux.Handle("/assets/", &assets{ui})
	mux.Handle("/", &page{ui})
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
	socket := r.URL.Path == wsPath || r.URL.Path == screenPath
	if socket && len(origins) == 0 {
		http.Error(w, "the WebSocket needs an Origin", http.StatusForbidden) // rule 3: mandatory on the upgrade
		return
	}
	if r.Method == http.MethodOptions {
		http.Error(w, "no CORS", http.StatusForbidden) // rule 5
		return
	}
	// The WebSockets authenticate by their first frame (rule 4): a browser's upgrade
	// cannot carry Authorization. Every other method on those paths still needs the token.
	if api && !(socket && r.Method == http.MethodGet) {
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

// placeholder is GET / when the binary carries no UI build (ADR-0040 §2): one static
// page with no script and no data (plan m1d, R6) that names the build command.
const placeholder = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Droidyard</title></head>
<body>
<h1>Droidyard</h1>
<p>The daemon is running, but this binary carries no web UI.</p>
<p>Build it with <code>pnpm -C web install --frozen-lockfile &amp;&amp; pnpm -C web build</code>,
then rebuild and restart the daemon.</p>
</body>
</html>
`

// csp is the UI page's Content-Security-Policy (http-websocket.md rule 7). It is not
// widened for the app: a violation is fixed in the app (plan m2a D6).
const csp = "default-src 'self'; frame-ancestors 'none'"

// page is GET and HEAD /: the app's index.html, or the placeholder without a build. It
// needs no token (the page holds no data); the fence has already checked Host and Origin.
type page struct{ ui fs.FS }

func (p *page) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body := []byte(placeholder)
	if p.ui != nil {
		b, err := fs.ReadFile(p.ui, "index.html")
		if err != nil {
			http.Error(w, "the web UI is unreadable", http.StatusInternalServerError)
			return
		}
		body = b
	}
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodGet {
		w.Write(body)
	}
}

// assetTypes are the files the app's /assets/ serves, by extension: the built app is
// scripts and styles (assetsInlineLimit 0 and no source maps, plan m2a D1), so
// anything else in the tree (a .map, an .svg, a stray file) is not reachable.
var assetTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
}

// assets is GET and HEAD /assets/<name>: one hashed script or style directly under the
// app's assets directory, cached for a year because its name changes with its content
// (plan m2a D6). No subdirectory, no other extension: any other path is 404.
type assets struct{ ui fs.FS }

func (a *assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	typ, ok := assetTypes[path.Ext(name)]
	if a.ui == nil || !ok || name == "" || strings.Contains(name, "/") || !fs.ValidPath("assets/"+name) {
		http.NotFound(w, r)
		return
	}
	body, err := fs.ReadFile(a.ui, "assets/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodGet {
		w.Write(body)
	}
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
	env, ok := c.answer(r.Context(), body, nil, requester{client: capability.ClientUI})
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(env)
}

// run is Start, then Wait for waitMs, or to the end without it; a capability that is
// not recorded (operation.wait, operation.cancel) is Invoked.
func (c *caller) run(ctx context.Context, creq capability.Request, waitMs *int64) (capability.Outcome, error) {
	if d, ok := c.reg.Lookup(creq.Capability); ok && !d.Recorded() {
		// operation.wait and operation.cancel are not operations.
		return c.ops.Invoke(ctx, creq)
	}
	id, err := c.ops.Start(ctx, creq)
	if err != nil {
		return capability.Outcome{}, err
	}
	var deadline time.Time
	if waitMs != nil {
		deadline = time.Now().Add(time.Duration(*waitMs) * time.Millisecond)
	}
	return c.ops.Wait(ctx, id, deadline)
}

// requester is who a request is made as: the client kind the core sees it from, and for a
// peer its configured name, which becomes the session name its operations carry
// (ADR-0098 §5).
type requester struct {
	client capability.ClientKind
	name   string
}

// answer runs one envelope: Start, then Wait for waitMs, or to the end without it. It
// reports false when the client is gone and nothing should be written. session is the
// session the request came in on: nil for a plain HTTP request, a WebSocket's own
// otherwise, and never a nil pointer inside a non-nil interface.
func (c *caller) answer(ctx context.Context, body []byte, session capability.Session, who requester) (capability.ResponseEnvelope, bool) {
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
	creq := capability.Request{Capability: req.Capability, Workspace: req.Workspace, Params: req.Params, Client: who.client, Session: session}
	creq.Origin.Client = who.client
	if session != nil {
		// A WebSocket's calls name their session on operation.state (ADR-0048); a plain
		// HTTP request has none.
		creq.Origin.ID = session.ID()
		creq.Origin.Name = who.name
	}

	out, err := c.run(ctx, creq, req.WaitMs)
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
		// A failed call whose handler marked it carries the mark here too (plan m1f,
		// D5): the error's details may quote device text.
		env.Meta = out.Meta()
	}
	return env, true
}

// artifactsPath is the artifact route's prefix: GET /v1/artifacts/{id}.
const artifactsPath = "/v1/artifacts/"

// artifactRoute is GET /v1/artifacts/{id} (http-websocket.md §GET /v1/artifacts/{id}):
// artifact.read as client ui, with raw bytes for the answer. It sits behind the fence
// like every /v1/ route, and runs the capability through the operation store, so the
// request is recorded as an artifact.read operation; the route carries no operation id.
// The status carries the meaning an envelope cannot: 404 ARTIFACT_NOT_FOUND, 410
// ARTIFACT_EXPIRED, 400 for a malformed query or REQUEST_INVALID_PARAMS, each with the
// typed error as a JSON body.
type artifactRoute struct{ c *caller }

// readParams is artifact.read's wire parameters, spelled here so that the adapter does
// not import the domain package (overview.md §Adapter boundary).
type readParams struct {
	Ref     string      `json:"ref"`
	MaxEdge *int        `json:"maxEdge,omitempty"`
	Region  *readRegion `json:"region,omitempty"`
}

type readRegion struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func (a *artifactRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, artifactsPath)
	if id == "" || strings.Contains(id, "/") {
		writeError(w, capability.NewError(capability.ErrArtifactNotFound, "no artifact has this reference"))
		return
	}
	p := readParams{Ref: "droidyard://artifact/" + id}
	q := r.URL.Query()
	for k := range q {
		if k != "maxEdge" && k != "region" {
			writeError(w, capability.Errorf(capability.ErrInvalidParams, "unknown query parameter %q", k))
			return
		}
	}
	if v, ok := q["maxEdge"]; ok {
		n, err := strconv.Atoi(strings.Join(v, ","))
		if err != nil || len(v) != 1 {
			writeError(w, capability.NewError(capability.ErrInvalidParams, "maxEdge must be a whole number of pixels"))
			return
		}
		p.MaxEdge = &n
	}
	if v, ok := q["region"]; ok {
		parts := strings.Split(strings.Join(v, ","), ",")
		var f [4]float64
		if len(v) != 1 || len(parts) != 4 {
			writeError(w, capability.NewError(capability.ErrInvalidParams, "region must be x,y,w,h"))
			return
		}
		for i, s := range parts {
			var err error
			if f[i], err = strconv.ParseFloat(s, 64); err != nil {
				writeError(w, capability.NewError(capability.ErrInvalidParams, "region must be four numbers, x,y,w,h"))
				return
			}
		}
		p.Region = &readRegion{f[0], f[1], f[2], f[3]}
	}
	params, _ := json.Marshal(p)
	out, err := a.c.run(r.Context(), capability.Request{Capability: "artifact.read", Params: params, Client: capability.ClientUI}, nil)
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		writeError(w, capability.AsError(err))
		return
	}
	if out.State != capability.StateCompleted {
		writeError(w, out.Error)
		return
	}
	raw, err := json.Marshal(out.Result)
	var res struct {
		MediaType string `json:"mediaType"`
		Data      string `json:"data"`
	}
	var data []byte
	if err == nil {
		if err = json.Unmarshal(raw, &res); err == nil {
			data, err = base64.StdEncoding.DecodeString(res.Data)
		}
	}
	// A completed read always carries its bytes in `data`: the result is slimmed only
	// after its first delivery (ADR-0020, 2026-10-04), so an empty one means this
	// delivery was lost. That is the daemon's fault, not the request's: 500.
	if err == nil && res.Data == "" {
		writeStatus(w, http.StatusInternalServerError, capability.NewError(capability.ErrInvalidParams, "the artifact cannot be served").
			WithCause("the read result holds no bytes: its first delivery was already made"))
		return
	}
	// Rule 8: artifacts are served only as these types. A type the route cannot serve
	// is the daemon's own state, not a bad request: 500. No internal-error code is
	// published, so the body keeps the generic fallback code AsError uses.
	if err != nil || (res.MediaType != "image/png" && res.MediaType != "image/jpeg") {
		writeStatus(w, http.StatusInternalServerError, capability.NewError(capability.ErrInvalidParams, "the artifact cannot be served").WithCause("its media type or bytes are not servable"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", res.MediaType)
	h.Set("Cache-Control", "no-store")
	if out.Untrusted {
		h.Set("X-Droidyard-Untrusted", "true") // R3: the route has no envelope for meta
	}
	h.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodGet {
		w.Write(data)
	}
}

// writeError answers with the typed error as the JSON body: the status is the meaning
// (404, 410, 400); any other code is a 500, since a read has no other expected failure.
func writeError(w http.ResponseWriter, e *capability.Error) {
	status := http.StatusInternalServerError
	switch e.Code {
	case capability.ErrArtifactNotFound:
		status = http.StatusNotFound
	case capability.ErrArtifactExpired:
		status = http.StatusGone
	case capability.ErrInvalidParams:
		status = http.StatusBadRequest
	}
	writeStatus(w, status, e)
}

// writeStatus answers with the typed error as the JSON body under the given status.
func writeStatus(w http.ResponseWriter, status int, e *capability.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(e)
}
