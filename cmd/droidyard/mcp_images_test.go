package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// imageCall is a tools/call answer with its content blocks as they are on the wire.
type imageCall struct {
	toolResult
	blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	}
}

func decodeImageCall(t *testing.T, line []byte, id int) imageCall {
	t.Helper()
	c := imageCall{toolResult: decodeTool(t, line, id)}
	if err := json.Unmarshal(c.raw["content"], &c.blocks); err != nil {
		t.Fatalf("content: %v", err)
	}
	return c
}

// image decodes the image block, failing when it is not exactly the second block.
func (c imageCall) image(t *testing.T) (image.Image, string) {
	t.Helper()
	if len(c.blocks) != 2 || c.blocks[0].Type != "text" || c.blocks[1].Type != "image" {
		t.Fatalf("content %s; want a text block, then an image block", c.raw["content"])
	}
	b, err := base64.StdEncoding.DecodeString(c.blocks[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("the image block does not decode: %v", err)
	}
	if want := "image/" + format; c.blocks[1].MimeType != want {
		t.Errorf("mimeType %q for a %s image", c.blocks[1].MimeType, format)
	}
	return img, c.blocks[1].MimeType
}

func longEdge(img image.Image) int { return max(img.Bounds().Dx(), img.Bounds().Dy()) }

// Decision (plan step 8 (b), A10): over MCP, device_screenshot returns its text, then an
// image block whose longest edge is at most 1024 px (the recorded Pixel_9a screen is
// 1080x2424, so 456x1024), still marked untrusted in _meta and in the fixed first line,
// with the reference, and the stored and delivered sizes in the text; its
// structuredContent is the reference record and conforms to the published outputSchema.
// artifact_read of the reference answers with an image block at its maxEdge (capped at
// 2048) and no `data` in its structured content, the reference's own mark kept.
func TestMCPScreenshotInlinesTheImage(t *testing.T) {
	t.Parallel()
	p, raw := shotDaemon(t, nil)
	c := startShim(t, p.state, "client", "--wait", "5s")
	c.initialize("images")

	c.callTool(1, "device_screenshot", `{"device":"emulator-5554"}`)
	shot := decodeImageCall(t, c.recv(), 1)
	img, mime := shot.image(t)
	if mime != "image/png" || img.Bounds().Dx() != 456 || img.Bounds().Dy() != 1024 {
		t.Errorf("inline image %s %v; want image/png 456x1024", mime, img.Bounds())
	}
	line, rest, _ := strings.Cut(shot.blocks[0].Text, "\n")
	if line != untrustedLine {
		t.Errorf("text %q does not open with the fixed line", shot.blocks[0].Text)
	}
	if m := metaOf(t, shot.toolResult); m["droidyard/untrusted"] != true {
		t.Errorf("_meta = %v", m)
	}
	var ref artifact.Ref
	jsonLine, sizes, _ := strings.Cut(rest, "\n")
	if err := json.Unmarshal([]byte(jsonLine), &ref); err != nil || !strings.HasPrefix(ref.Ref, artifact.RefPrefix) {
		t.Fatalf("the text does not carry the reference record: %q (%v)", jsonLine, err)
	}
	if !strings.Contains(sizes, "stored 1080x2424") || !strings.Contains(sizes, "delivered 456x1024") {
		t.Errorf("the sizes line is %q", sizes)
	}
	if string(shot.raw["structuredContent"]) != jsonLine {
		t.Errorf("structuredContent %s differs from the text's JSON %s", shot.raw["structuredContent"], jsonLine)
	}
	var sc any
	json.Unmarshal(shot.raw["structuredContent"], &sc)
	conforms(t, published(t)["device.screenshot"]["output"].(map[string]any), sc, "structuredContent")

	// artifact_read: the image at its maxEdge, and no bytes in the structured content.
	c.callTool(2, "artifact_read", `{"ref":"`+ref.Ref+`","maxEdge":512}`)
	read := decodeImageCall(t, c.recv(), 2)
	img, mime = read.image(t)
	if mime != "image/png" || longEdge(img) != 512 {
		t.Errorf("artifact_read maxEdge 512: %s %v", mime, img.Bounds())
	}
	var structured map[string]json.RawMessage
	if err := json.Unmarshal(read.raw["structuredContent"], &structured); err != nil {
		t.Fatal(err)
	}
	if _, has := structured["data"]; has {
		t.Errorf("structuredContent holds data: %s", read.raw["structuredContent"])
	}
	if strings.Contains(read.blocks[0].Text, `"data"`) || strings.Contains(read.blocks[0].Text, read.blocks[1].Data[:64]) {
		t.Errorf("the text carries the bytes: %.200s", read.blocks[0].Text)
	}
	if m := metaOf(t, read.toolResult); m["droidyard/untrusted"] != true || !strings.HasPrefix(read.blocks[0].Text, untrustedLine+"\n") {
		t.Errorf("artifact_read lost the untrusted mark: _meta %v, text %.120s", m, read.blocks[0].Text)
	}
	if !strings.Contains(read.blocks[0].Text, "stored 1080x2424, delivered 228x512") {
		t.Errorf("the sizes: %q", read.blocks[0].Text)
	}
	var rsc any
	json.Unmarshal(read.raw["structuredContent"], &rsc)
	conforms(t, published(t)["artifact.read"]["output"].(map[string]any), rsc, "structuredContent")

	// A larger ask is capped at 2048, never upscaled: the stored screen has a long edge
	// of 2424, so 4000 gives 2048.
	c.callTool(3, "artifact_read", `{"ref":"`+ref.Ref+`","maxEdge":4000}`)
	img, _ = decodeImageCall(t, c.recv(), 3).image(t)
	if longEdge(img) != artifact.InlineMaxEdgeCap {
		t.Errorf("maxEdge 4000 gave a long edge of %d; want the cap, %d", longEdge(img), artifact.InlineMaxEdgeCap)
	}

	// A cropped read: the image is the region (its long edge, 1212, is under maxEdge).
	c.callTool(4, "artifact_read", `{"ref":"`+ref.Ref+`","maxEdge":2000,"region":{"x":0,"y":0,"w":0.5,"h":0.5}}`)
	img, _ = decodeImageCall(t, c.recv(), 4).image(t)
	cfg, _ := png.DecodeConfig(strings.NewReader(raw))
	if img.Bounds().Dx() != cfg.Width/2 || img.Bounds().Dy() != cfg.Height/2 {
		t.Errorf("region 0,0,.5,.5: %v of %dx%d", img.Bounds(), cfg.Width, cfg.Height)
	}

	// No maxEdge: the default inline edge, 1024 (not the stored 2424).
	c.callTool(6, "artifact_read", `{"ref":"`+ref.Ref+`"}`)
	if img, _ = decodeImageCall(t, c.recv(), 6).image(t); longEdge(img) != artifact.InlineMaxEdge {
		t.Errorf("artifact_read without maxEdge: long edge %d; want %d", longEdge(img), artifact.InlineMaxEdge)
	}

	// An error stays an error: one text block, no image.
	c.callTool(5, "artifact_read", `{"ref":"droidyard://artifact/e.zzzz.1.aaaaaaaaaaaa"}`)
	bad := decodeImageCall(t, c.recv(), 5)
	var e capability.Error
	if !bad.isError || len(bad.blocks) != 1 || json.Unmarshal([]byte(bad.blocks[0].Text), &e) != nil || e.Code != capability.ErrArtifactExpired {
		t.Errorf("an expired reference: %s", bad.raw["content"])
	}
	c.exit()
}

// Decision (plan step 8 (b)): a screenshot whose downscaled PNG is over 512 KiB comes
// back as image/jpeg, still within 1024 px; the structured reference still says the
// stored artifact is a PNG.
func TestMCPNoisyScreenshotIsJPEG(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	noise := image.NewRGBA(image.Rect(0, 0, 700, 1500))
	for i := range noise.Pix {
		noise.Pix[i] = byte(i*7919>>3) ^ byte(i>>9)
	}
	for i := 3; i < len(noise.Pix); i += 4 {
		noise.Pix[i] = 255
	}
	if err := png.Encode(&buf, noise); err != nil {
		t.Fatal(err)
	}
	p := fakeADBDaemon(t, map[string]string{
		"devices.out":                     shotDevices,
		"emulator-5554.bootcompleted.out": "1\n",
		"emulator-5554.screencap.out":     buf.String(),
	})
	c := startShim(t, p.state, "client", "--wait", "5s")
	c.initialize("jpeg")
	c.callTool(1, "device_screenshot", `{"device":"emulator-5554"}`)
	shot := decodeImageCall(t, c.recv(), 1)
	img, mime := shot.image(t)
	if mime != "image/jpeg" || longEdge(img) != 1024 {
		t.Errorf("noisy screenshot: %s %v; want image/jpeg, long edge 1024", mime, img.Bounds())
	}
	if !strings.Contains(shot.blocks[0].Text, `"mediaType":"image/png"`) || !strings.Contains(shot.blocks[0].Text, "delivered 478x1024, image/jpeg") {
		t.Errorf("the text: %.400s", shot.blocks[0].Text)
	}
	c.exit()
}
