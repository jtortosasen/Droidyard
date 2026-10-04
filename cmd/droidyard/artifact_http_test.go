package main

import (
	"bytes"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

func artifactRequest(port int, token, path string) httpRequest {
	return httpRequest{method: "GET", path: path, proto: "HTTP/1.1", headers: []string{
		"Host: 127.0.0.1:" + strconv.Itoa(port),
		"Authorization: Bearer " + token,
	}}
}

// screenshotRef takes a screenshot through the CLI and returns its reference.
func screenshotRef(t *testing.T, p *inProcess) artifact.Ref {
	t.Helper()
	env, code := p.call(t, "device.screenshot", "--params", `{"device":"emulator-5554"}`)
	if code != 0 || env.Error != nil {
		t.Fatalf("device.screenshot: exit %d, %+v", code, env.Error)
	}
	var ref artifact.Ref
	if err := json.Unmarshal(env.Result, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func bodyOf(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Decision (plan step 8 (a), A9): GET /v1/artifacts/{id} is behind the fence (the token,
// the Host and the Origin rules apply to it as to every /v1/ route) and serves a stored
// screenshot as raw bytes, image/png, marked X-Droidyard-Untrusted: true (R3), never
// cached and never sniffed; ?maxEdge and ?region shape it as artifact.read does.
func TestArtifactRouteServesTheScreenshot(t *testing.T) {
	p, raw := shotDaemon(t, nil)
	ref := screenshotRef(t, p)
	id := strings.TrimPrefix(ref.Ref, artifact.RefPrefix)
	addr := p.d.HTTPAddr()
	port := portOf(t, addr)
	get := artifactRequest(port, p.token, "/v1/artifacts/"+id)

	resp := get.send(t, addr)
	body := bodyOf(t, resp)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, []byte(raw)) {
		t.Fatalf("status %d, %s, %d bytes; want 200 image/png and the fixture's %d bytes", resp.StatusCode, resp.Header.Get("Content-Type"), len(body), len(raw))
	}
	if resp.Header.Get("X-Droidyard-Untrusted") != "true" {
		t.Errorf("X-Droidyard-Untrusted %q; want true", resp.Header.Get("X-Droidyard-Untrusted"))
	}
	checkHeaders(t, "the artifact", resp, true)

	small := get
	small.path += "?maxEdge=100"
	resp = small.send(t, addr)
	img, err := png.Decode(bytes.NewReader(bodyOf(t, resp)))
	if err != nil || resp.StatusCode != 200 || max(img.Bounds().Dx(), img.Bounds().Dy()) != 100 {
		t.Errorf("maxEdge=100: %d, %v, %v", resp.StatusCode, img.Bounds(), err)
	}
	half := get
	half.path += "?region=0,0,0.5,0.5"
	resp = half.send(t, addr)
	img, err = png.Decode(bytes.NewReader(bodyOf(t, resp)))
	if err != nil || resp.StatusCode != 200 || img.Bounds().Dx() != ref.Width/2 || img.Bounds().Dy() != ref.Height/2 {
		t.Errorf("region 0,0,0.5,0.5: %d, %v (stored %dx%d), %v", resp.StatusCode, img.Bounds(), ref.Width, ref.Height, err)
	}

	// The fence: no token, a foreign Host and a foreign Origin never reach the store.
	for name, c := range map[string]struct {
		req  httpRequest
		want int
	}{
		"no token":         {get.without("Authorization"), 401},
		"a wrong token":    {get.with("Authorization: Bearer " + strings.Repeat("0", 64)), 401},
		"a foreign Host":   {get.with("Host: evil.example:" + strconv.Itoa(port)), 403},
		"a foreign Origin": {get.with("Origin: http://evil.example"), 403},
	} {
		resp := c.req.send(t, addr)
		if body := bodyOf(t, resp); resp.StatusCode != c.want || bytes.Contains(body, []byte("PNG")) {
			t.Errorf("%s: %d; want %d and no image", name, resp.StatusCode, c.want)
		}
	}
}

// Decision (plan step 8 (a), http-websocket.md §GET /v1/artifacts/{id}): the status
// carries the meaning and the body is the typed error as JSON: 404 ARTIFACT_NOT_FOUND
// for an id that never was (above the highest counter, or malformed), 410
// ARTIFACT_EXPIRED for one that once could have been (another daemon epoch), 400
// REQUEST_INVALID_PARAMS for a malformed query or a region outside the image.
func TestArtifactRouteErrorsAreTyped(t *testing.T) {
	p, _ := shotDaemon(t, nil)
	ref := screenshotRef(t, p)
	parts := strings.Split(strings.TrimPrefix(ref.Ref, artifact.RefPrefix), ".") // e.<epoch>.<n>.<random>
	addr := p.d.HTTPAddr()
	port := portOf(t, addr)
	for _, c := range []struct {
		name, path string
		status     int
		code       capability.ErrorCode
	}{
		{"above the highest counter", "/v1/artifacts/e." + parts[1] + ".9999." + parts[3], 404, capability.ErrArtifactNotFound},
		{"malformed", "/v1/artifacts/nope", 404, capability.ErrArtifactNotFound},
		{"another epoch", "/v1/artifacts/e.zzzz." + parts[2] + "." + parts[3], 410, capability.ErrArtifactExpired},
		{"wrong random bits", "/v1/artifacts/e." + parts[1] + "." + parts[2] + ".000000000000", 410, capability.ErrArtifactExpired},
		{"a region outside the image", "/v1/artifacts/" + strings.Join(parts, ".") + "?region=0.5,0.5,1,1", 400, capability.ErrInvalidParams},
		{"a malformed region", "/v1/artifacts/" + strings.Join(parts, ".") + "?region=1,2", 400, capability.ErrInvalidParams},
		{"a malformed maxEdge", "/v1/artifacts/" + strings.Join(parts, ".") + "?maxEdge=big", 400, capability.ErrInvalidParams},
		{"maxEdge 0", "/v1/artifacts/" + strings.Join(parts, ".") + "?maxEdge=0", 400, capability.ErrInvalidParams},
		{"an unknown parameter", "/v1/artifacts/" + strings.Join(parts, ".") + "?offset=1", 400, capability.ErrInvalidParams},
	} {
		resp := artifactRequest(port, p.token, c.path).send(t, addr)
		var e capability.Error
		body := bodyOf(t, resp)
		if resp.StatusCode != c.status || resp.Header.Get("Content-Type") != "application/json" ||
			json.Unmarshal(body, &e) != nil || e.Code != c.code {
			t.Errorf("%s: %d %s %s; want %d %s", c.name, resp.StatusCode, resp.Header.Get("Content-Type"), body, c.status, c.code)
		}
		if resp.Header.Get("X-Droidyard-Untrusted") != "" {
			t.Errorf("%s: an error carries the untrusted header", c.name)
		}
	}
}
