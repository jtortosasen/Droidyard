package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestOnlyLoopbackIsBound: the guard refuses every non-loopback bind address at
// startup, the unspecified ones included, and accepts the two loopback ones
// (http-websocket.md §Security baseline rule 1; plan m1d, R5).
func TestOnlyLoopbackIsBound(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.168.1.10:0", "[::]:0", "localhost:0", "example.com:7317", "7317"} {
		if err := CheckLoopback(addr); err == nil {
			t.Errorf("%s was accepted", addr)
		}
		if ln, err := Listen(addr); err == nil {
			ln.Close()
			t.Errorf("Listen bound %s", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		if err := CheckLoopback(addr); err != nil {
			t.Errorf("%s was refused: %v", addr, err)
		}
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); host != "127.0.0.1" {
		t.Errorf("bound %s", ln.Addr())
	}
}

// TestEmptyTokenRefusesEverything: a handler built with an empty token answers every API
// request 401, "Authorization: Bearer " included, instead of matching "" with "". The
// header is set on the request directly: a real server trims its trailing space, so
// only the handler itself can be held to this.
func TestEmptyTokenRefusesEverything(t *testing.T) {
	h := New(nil, nil, nil, "", nil)
	req := httptest.NewRequest("POST", "http://127.0.0.1:7317/v1/call", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer ")
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7317}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("an empty token with an empty bearer: %d; want 401", rec.Code)
	}
}

type spyRead struct {
	Ref     string                        `json:"ref"`
	MaxEdge *int                          `json:"maxEdge"`
	Region  *struct{ X, Y, W, H float64 } `json:"region"`
}

type spyResult struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

// Decision (plan step 8 (a)): the artifact route runs as an artifact.read operation of
// client ui, and carries no operation id. The registry's artifact.read here is a spy
// reserved to humans: the core rejects such a call from client mcp, so the call
// completing proves the route is client ui; the operation store then holds one completed
// artifact.read operation, the one the spy saw by id; the query becomes its parameters;
// and no header names the operation.
func TestArtifactRouteIsAnArtifactReadOperationOfClientUI(t *testing.T) {
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	var ids []string
	var got spyRead
	reg.MustRegister(capability.Descriptor{
		Name: "artifact.read", Kind: capability.Query, Scope: capability.Global, Audience: capability.AudienceHuman,
		Description: "spy", Params: spyRead{}, Result: spyResult{},
		Handler: func(_ context.Context, call *capability.Call) (any, error) {
			ids = append(ids, call.OperationID)
			got = call.Params.(spyRead)
			return spyResult{MediaType: "image/png", Data: "iVBO"}, nil
		},
	})
	ops.RegisterCapabilities()
	traces := make(chan string, 4)
	ops.UseTrace(func(format string, v ...any) { traces <- fmt.Sprintf(format, v...) })
	h := New(reg, ops, nil, "tok", nil)
	req := httptest.NewRequest("GET", "http://127.0.0.1:7317/v1/artifacts/e.x.1.aaaaaaaaaaaa?maxEdge=100&region=0,0,0.5,0.25", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7317}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.Len() != 3 {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("the spy ran %d times with operation ids %v; want one recorded operation", len(ids), ids)
	}
	out, err := ops.Wait(context.Background(), ids[0], time.Now().Add(-time.Second))
	if err != nil || out.Capability != "artifact.read" || out.State != capability.StateCompleted {
		t.Errorf("the operation store: %+v, %v; want a completed artifact.read", out, err)
	}
	// The capability trace (plan m2a, Q3): the route's operation is traced as client ui,
	// and the line carries neither the ref nor the query.
	select {
	case line := <-traces:
		want := regexp.MustCompile(`^droidyard: op ` + regexp.QuoteMeta(ids[0]) + ` client=ui capability=artifact\.read workspace=- state=completed ms=\d+$`)
		if !want.MatchString(line) {
			t.Errorf("trace line %q; want client=ui capability=artifact.read for %s", line, ids[0])
		}
	case <-time.After(5 * time.Second):
		t.Error("no trace line for the artifact.read operation")
	}
	if got.Ref != "droidyard://artifact/e.x.1.aaaaaaaaaaaa" || got.MaxEdge == nil || *got.MaxEdge != 100 || got.Region == nil || got.Region.H != 0.25 {
		t.Errorf("params %+v", got)
	}
	for k := range rec.Header() {
		if strings.Contains(strings.ToLower(k), "operation") {
			t.Errorf("the response carries %s", k)
		}
	}
}

// Decision (N3, N5): a completed artifact.read the route cannot serve is the daemon's
// fault, not the request's: 500 with the typed error as the JSON body, never a 200
// with an empty body (a lost first delivery: no `data`) and never a 400 (a media type
// the route does not serve). No internal-error code is published, so the body keeps
// REQUEST_INVALID_PARAMS, the code AsError falls back to.
func TestArtifactRouteAnswers500ForAResultItCannotServe(t *testing.T) {
	for name, res := range map[string]spyResult{
		"no bytes":         {MediaType: "image/png", Data: ""},
		"unservable type":  {MediaType: "text/html", Data: "iVBO"},
		"bytes not base64": {MediaType: "image/png", Data: "%%%"},
	} {
		reg := capability.NewRegistry()
		ops := capability.NewOperations(reg)
		reg.MustRegister(capability.Descriptor{
			Name: "artifact.read", Kind: capability.Query, Scope: capability.Global, Audience: capability.AudienceHuman,
			Description: "spy", Params: spyRead{}, Result: spyResult{},
			Handler: func(context.Context, *capability.Call) (any, error) { return res, nil },
		})
		ops.RegisterCapabilities()
		h := New(reg, ops, nil, "tok", nil)
		req := httptest.NewRequest("GET", "http://127.0.0.1:7317/v1/artifacts/e.x.1.aaaaaaaaaaaa", nil)
		req.Header.Set("Authorization", "Bearer tok")
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7317}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var e capability.Error
		if rec.Code != 500 || rec.Header().Get("Content-Type") != "application/json" || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Code == "" || e.Cause == "" {
			t.Errorf("%s: %d %q %q; want 500 with a typed JSON error naming why", name, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}
