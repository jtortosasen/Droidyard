package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	h := Handler(nil, nil, "")
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
