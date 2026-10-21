package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

type textRead struct {
	Ref   string `json:"ref"`
	Limit *int   `json:"limit"`
}

type textResult struct {
	MediaType string `json:"mediaType"`
	Growing   bool   `json:"growing"`
	Text      string `json:"text"`
}

// textServer is a server whose artifact.read is a spy answering as a text artifact of the
// given media type and growing flag, with TextArtifacts serving body. reads counts the
// artifact.read operations and limits records the limit each asked for.
func textServer(t *testing.T, mediaType string, growing, untrusted bool, body string, reads *int, limits *[]int) *Server {
	t.Helper()
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	reg.MustRegister(capability.Descriptor{
		Name: "artifact.read", Kind: capability.Query, Scope: capability.Global, Audience: capability.AudienceHuman,
		Description: "spy", Params: textRead{}, Result: textResult{},
		Handler: func(_ context.Context, call *capability.Call) (any, error) {
			*reads++
			if l := call.Params.(textRead).Limit; l != nil {
				*limits = append(*limits, *l)
			}
			call.Untrusted = &untrusted
			return textResult{MediaType: mediaType, Growing: growing, Text: "x"}, nil
		},
	})
	ops.RegisterCapabilities()
	h := New(reg, ops, nil, "tok", nil)
	h.UseTextArtifacts(func(id string) (TextFile, error) {
		// The bytes served must be the artifact the read authorized: the path's id.
		if id != "r.20261021T100000Z-0123abcd" && id != "e.x.1.aaaaaaaaaaaa" && id != "b.1" {
			return TextFile{}, capability.NewError(capability.ErrArtifactNotFound, "the reader was asked for "+id)
		}
		return TextFile{Body: nopSeekCloser{strings.NewReader(body)}}, nil
	})
	return h
}

type nopSeekCloser struct{ *strings.Reader }

func (nopSeekCloser) Close() error { return nil }

func getArtifact(h *Server, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "http://127.0.0.1:7317"+path, nil)
	req.Header.Set("Authorization", "Bearer tok")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7317}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Decision (rule 8, ADR-0012 §Reading artifacts): a finished recording timeline is
// served whole as text/plain; charset=utf-8, uncached and marked untrusted, after one
// artifact.read operation of the smallest limit (the authorization, existence and mark);
// the bytes come from the injected reader, not from that probe. An unmarked artifact
// carries no untrusted header.
func TestArtifactRouteServesATimelineAsText(t *testing.T) {
	var reads int
	var limits []int
	body := "{\"t\":1}\n{\"t\":2}\n"
	h := textServer(t, "application/x-ndjson", false, true, body, &reads, &limits)
	rec := getArtifact(h, "/v1/artifacts/r.20261021T100000Z-0123abcd", nil)
	if rec.Code != 200 || rec.Body.String() != body {
		t.Fatalf("%d %q; want 200 and the whole timeline", rec.Code, rec.Body.String())
	}
	hd := rec.Header()
	if hd.Get("Content-Type") != "text/plain; charset=utf-8" || hd.Get("Cache-Control") != "no-store" ||
		hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("X-Droidyard-Untrusted") != "true" {
		t.Errorf("headers %v", hd)
	}
	if reads != 1 || len(limits) != 1 || limits[0] != 4 {
		t.Errorf("artifact.read ran %d times with limits %v; want once with limit 4", reads, limits)
	}
	var r2 int
	var l2 []int
	plain := textServer(t, "text/plain", false, false, "ok\n", &r2, &l2)
	if rec := getArtifact(plain, "/v1/artifacts/b.1", nil); rec.Code != 200 || rec.Header().Get("X-Droidyard-Untrusted") != "" {
		t.Errorf("an unmarked artifact: %d %v", rec.Code, rec.Header())
	}
}

// Decision (Q8): a running recording is not served as if it were whole; it is the
// documented 400 REQUEST_INVALID_PARAMS pointing at artifact.read, and no bytes leave.
func TestArtifactRouteRefusesGrowingText(t *testing.T) {
	var reads int
	var limits []int
	h := textServer(t, "application/x-ndjson", true, true, "partial", &reads, &limits)
	rec := getArtifact(h, "/v1/artifacts/r.20261021T100000Z-0123abcd", nil)
	var e capability.Error
	if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Code != capability.ErrInvalidParams ||
		!strings.Contains(e.Message, "still being written") || strings.Contains(rec.Body.String(), "partial") {
		t.Fatalf("%d %q; want 400 REQUEST_INVALID_PARAMS 'still being written' without bytes", rec.Code, rec.Body.String())
	}
}

// Decision: a text artifact honours Range (http.ServeContent), so a long timeline can
// be fetched in parts; the type stays text/plain on a 206.
func TestArtifactRouteTextRange(t *testing.T) {
	var reads int
	var limits []int
	h := textServer(t, "application/x-ndjson", false, true, "0123456789", &reads, &limits)
	rec := getArtifact(h, "/v1/artifacts/r.20261021T100000Z-0123abcd", map[string]string{"Range": "bytes=2-5"})
	if rec.Code != 206 || rec.Body.String() != "2345" || rec.Header().Get("Content-Range") != "bytes 2-5/10" ||
		rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

// Decision (rule 8): several ranges would be multipart/byteranges, a type outside rule 8's three,
// so such a request is answered with the whole artifact as text/plain. Seen failing without the
// rule (206 multipart/byteranges).
func TestArtifactRouteMultiRangeIsWholeText(t *testing.T) {
	var reads int
	var limits []int
	h := textServer(t, "application/x-ndjson", false, true, "0123456789", &reads, &limits)
	rec := getArtifact(h, "/v1/artifacts/r.20261021T100000Z-0123abcd", map[string]string{"Range": "bytes=0-1,4-5"})
	if rec.Code != 200 || rec.Body.String() != "0123456789" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

// Decision (rule 8): whatever the stored text type, the wire type is text/plain, never
// the stored one, so XML or HTML-looking content cannot be rendered by the browser.
func TestArtifactRouteXMLIsServedAsPlainText(t *testing.T) {
	for _, mt := range []string{"application/xml", "text/xml", "text/html", "image/svg+xml"} {
		var reads int
		var limits []int
		h := textServer(t, mt, false, true, "<hierarchy/>", &reads, &limits)
		rec := getArtifact(h, "/v1/artifacts/e.x.1.aaaaaaaaaaaa", nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %d %v", mt, rec.Code, rec.Header())
		}
	}
}
