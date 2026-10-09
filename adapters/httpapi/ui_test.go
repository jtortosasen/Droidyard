package httpapi

import (
	"context"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

// countFS counts the files opened through it, to show that a refused request reads none.
type countFS struct {
	fs.FS
	opens atomic.Int64
}

func (c *countFS) Open(name string) (fs.File, error) {
	c.opens.Add(1)
	return c.FS.Open(name)
}

func uiFS() *countFS {
	return &countFS{FS: fstest.MapFS{
		"index.html":               {Data: []byte("<!doctype html><title>app</title>")},
		"assets/app-abc123.js":     {Data: []byte("console.log(1)")},
		"assets/app-abc123.css":    {Data: []byte("body{}")},
		"assets/app-abc123.js.map": {Data: []byte("{}")},
		"assets/logo.svg":          {Data: []byte("<svg/>")},
		"assets/sub/x.js":          {Data: []byte("x")},
		"secret.txt":               {Data: []byte("s")},
	}}
}

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

// Decision (plan m2a D6; ADR-0006): the built app is served behind the unchanged fence.
// `/` is index.html with the CSP, nosniff and no-cache and needs no token; a hashed
// script or style under /assets/ is cached as immutable with its type; anything else
// (an unknown path, a source map, an svg, a nested file, a raw dot-dot path) is 404, so
// nothing in the tree but the app's scripts and styles is reachable; a method other
// than GET and HEAD is 405; and a wrong Host or Origin is 403 before any file is read.
func TestUIIsServedBehindTheFence(t *testing.T) {
	ui := uiFS()
	h := New(nil, nil, nil, "tok", ui)

	root := get(h, "GET", "/", nil) // no Authorization: the page holds no data
	if root.Code != 200 || !strings.Contains(root.Body.String(), "<title>app</title>") {
		t.Fatalf("GET /: %d %q", root.Code, root.Body.String())
	}
	for k, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-cache",
		"Content-Type":            "text/html; charset=utf-8",
	} {
		if got := root.Header().Get(k); got != want {
			t.Errorf("GET / %s = %q; want %q", k, got, want)
		}
	}
	if head := get(h, "HEAD", "/", nil); head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD /: %d, %d body bytes, Content-Length %q", head.Code, head.Body.Len(), head.Header().Get("Content-Length"))
	}

	for path, typ := range map[string]string{
		"/assets/app-abc123.js":  "text/javascript; charset=utf-8",
		"/assets/app-abc123.css": "text/css; charset=utf-8",
	} {
		rec := get(h, "GET", path, nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != typ ||
			rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
			rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: %d, %v", path, rec.Code, rec.Header())
		}
	}
	if rec := get(h, "HEAD", "/assets/app-abc123.js", nil); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD asset: %d, %d body bytes", rec.Code, rec.Body.Len())
	}

	for _, path := range []string{
		"/x", "/index.html", "/secret.txt", "/assets/", "/assets/missing.js",
		"/assets/app-abc123.js.map", "/assets/logo.svg", "/assets/sub/x.js", "/assets/sub%2fx.js", "/v1/nothing",
	} {
		if rec := get(h, "GET", path, map[string]string{"Authorization": "Bearer tok"}); rec.Code != 404 {
			t.Errorf("GET %s: %d; want 404", path, rec.Code)
		}
	}

	// A path the mux redirects (the subtree root without its slash, a raw dot-dot path)
	// never reaches a file: it answers a redirect to the cleaned path, and that path is a
	// 404 (index.html is served at / only).
	for _, raw := range []string{"/assets", "/assets/../index.html", "/assets/%2e%2e/index.html", "/assets/../../secret.txt"} {
		rec := get(h, "GET", raw, nil)
		switch rec.Code {
		case http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			if loc := rec.Header().Get("Location"); get(h, "GET", loc, nil).Code != 404 {
				t.Errorf("GET %s redirects to %s, which is not a 404", raw, loc)
			}
		case 404:
		default:
			t.Errorf("GET %s: %d; want 404 or a redirect to one", raw, rec.Code)
		}
	}

	for _, c := range []struct{ method, path string }{{"POST", "/"}, {"PUT", "/"}, {"DELETE", "/"}, {"POST", "/assets/app-abc123.js"}} {
		if rec := get(h, c.method, c.path, map[string]string{"Content-Type": "application/json"}); rec.Code != 405 {
			t.Errorf("%s %s: %d; want 405", c.method, c.path, rec.Code)
		}
	}

	// The fence answers before the static handlers: no file is opened for a request it
	// refuses.
	before := ui.opens.Load()
	for _, c := range []struct {
		path string
		hdr  map[string]string
	}{
		{"/", map[string]string{"Host": "evil.example:7317"}},
		{"/assets/app-abc123.js", map[string]string{"Host": "evil.example:7317"}},
		{"/", map[string]string{"Host": "127.0.0.1:9999"}},
		{"/", map[string]string{"Origin": "http://evil.example:7317"}},
		{"/assets/app-abc123.js", map[string]string{"Origin": "http://evil.example"}},
	} {
		if rec := get(h, "GET", c.path, c.hdr); rec.Code != 403 {
			t.Errorf("GET %s with %v: %d; want 403", c.path, c.hdr, rec.Code)
		}
	}
	if n := ui.opens.Load() - before; n != 0 {
		t.Errorf("%d files were opened for refused requests", n)
	}
}

// Decision (ADR-0040 §2): a daemon built without the UI answers / with a placeholder
// that names the build command and the rebuild, under the same CSP and with no script;
// no asset is reachable.
func TestUIPlaceholderWhenNotBuilt(t *testing.T) {
	h := New(nil, nil, nil, "tok", nil)
	rec := get(h, "GET", "/", nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "pnpm -C web build") || !strings.Contains(body, "rebuild") {
		t.Errorf("GET /: %d %q; want the placeholder naming `pnpm -C web build` and a rebuild", rec.Code, body)
	}
	if strings.Contains(body, "<script") {
		t.Errorf("the placeholder has a script: %q", body)
	}
	if rec.Header().Get("Content-Security-Policy") != "default-src 'self'; frame-ancestors 'none'" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("headers: %v", rec.Header())
	}
	if rec := get(h, "GET", "/assets/app.js", nil); rec.Code != 404 {
		t.Errorf("GET /assets/app.js without a build: %d; want 404", rec.Code)
	}
}
