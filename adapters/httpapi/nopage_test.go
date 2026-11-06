package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// get runs one request through the handler, as the server would deliver it on port 7317.
func get(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(""))
	req.Host = "127.0.0.1:7317"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if k := req.Header.Get("Host"); k != "" {
		req.Host = k
	}
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7317}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Decision (ADR-0137 §1): the daemon serves no web page. GET / and the old page paths
// answer 404, behind the same fence (nosniff and no-referrer on every answer), while
// POST /v1/call with the token still answers: removing the UI removed the routes, not
// the API.
func TestNoPageIsServed(t *testing.T) {
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	h := New(reg, ops, nil, "tok")
	for _, path := range []string{"/", "/index.html", "/assets/app.js", "/favicon.ico"} {
		rec := get(h, "GET", path, nil)
		if rec.Code != 404 {
			t.Errorf("GET %s: %d %q; want 404", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("GET %s: the fence's headers are missing: %v", path, rec.Header())
		}
	}
	// The fence still guards the rest: a wrong Host is 403 before anything else.
	if rec := get(h, "GET", "/", map[string]string{"Host": "evil.example:7317"}); rec.Code != 403 {
		t.Errorf("GET / with a foreign Host: %d; want 403", rec.Code)
	}

	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://127.0.0.1:7317/v1/call", strings.NewReader(`{"id":"1","capability":"no.such"}`))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7317}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(""); rec.Code != 401 {
		t.Errorf("POST /v1/call without the token: %d; want 401", rec.Code)
	}
	if rec := call("tok"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"1"`) {
		t.Errorf("POST /v1/call with the token: %d %q; want an envelope answering id 1", rec.Code, rec.Body.String())
	}
}
