package artifact

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

var fixtures = []string{"mediumphonea17", "pixel9a", "pixeltablet"}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "domain", "device", "testdata", name+"-screencap.png"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

var (
	red   = color.RGBA{255, 0, 0, 255}
	green = color.RGBA{0, 255, 0, 255}
	blue  = color.RGBA{0, 0, 255, 255}
	white = color.RGBA{255, 255, 255, 255}
)

// quadrants is a w x h image with four flat colours: red top-left, green top-right,
// blue bottom-left, white bottom-right.
func quadrants(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := red
			switch {
			case x >= w/2 && y < h/2:
				c = green
			case x < w/2 && y >= h/2:
				c = blue
			case x >= w/2 && y >= h/2:
				c = white
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func near(a, b color.Color) bool {
	r1, g1, b1, _ := a.RGBA()
	r2, g2, b2, _ := b.RGBA()
	d := func(x, y uint32) bool {
		if x > y {
			return x-y < 0x0300
		}
		return y-x < 0x0300
	}
	return d(r1, r2) && d(g1, g2) && d(b1, b2)
}

// Decision (plan m1e step 7, R4): with no maxEdge and no region, or a maxEdge at or
// above the long edge, artifact.read returns the stored bytes untouched, never a
// re-encode.
func TestTransformReturnsStoredBytesWhenNothingToDo(t *testing.T) {
	for _, name := range fixtures {
		data := fixture(t, name)
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		long := max(cfg.Width, cfg.Height)
		for _, edge := range []int{0, long, long + 500} {
			out, typ, w, h, err := Transform(data, "image/png", edge, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, data) || typ != "image/png" || w != cfg.Width || h != cfg.Height {
				t.Errorf("%s maxEdge=%d: changed (equal=%v type=%s %dx%d)", name, edge, bytes.Equal(out, data), typ, w, h)
			}
		}
	}
}

// Decision: maxEdge makes the long edge exactly maxEdge, the short edge keeps the
// aspect ratio (within 1 px), for portrait and landscape; a PNG comes back.
func TestTransformMaxEdgeDimensions(t *testing.T) {
	for _, name := range fixtures {
		data := fixture(t, name)
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		out, typ, w, h, err := Transform(data, "image/png", 1024, nil)
		if err != nil {
			t.Fatal(err)
		}
		if typ != "image/png" {
			t.Errorf("%s: type %s", name, typ)
		}
		if max(w, h) != 1024 {
			t.Errorf("%s: long edge %d, want 1024", name, max(w, h))
		}
		wantShort := float64(min(cfg.Width, cfg.Height)) * 1024 / float64(max(cfg.Width, cfg.Height))
		if d := float64(min(w, h)) - wantShort; d > 1 || d < -1 {
			t.Errorf("%s: short edge %d, want %.1f within 1", name, min(w, h), wantShort)
		}
		if (cfg.Width > cfg.Height) != (w > h) {
			t.Errorf("%s: orientation flipped", name)
		}
		got := decode(t, out).Bounds()
		if got.Dx() != w || got.Dy() != h {
			t.Errorf("%s: reported %dx%d, bytes are %dx%d", name, w, h, got.Dx(), got.Dy())
		}
	}
	for _, c := range []struct{ w, h, edge, ww, wh int }{
		{100, 40, 50, 50, 20},  // landscape
		{40, 100, 50, 20, 50},  // portrait
		{100, 100, 10, 10, 10}, // square
		{1000, 3, 10, 10, 1},   // short edge never below 1
	} {
		_, _, w, h, err := Transform(encodePNG(t, quadrants(c.w, c.h)), "image/png", c.edge, nil)
		if err != nil || w != c.ww || h != c.wh {
			t.Errorf("%dx%d at %d: %dx%d err=%v, want %dx%d", c.w, c.h, c.edge, w, h, err, c.ww, c.wh)
		}
	}
}

// Decision: a downscale averages the area (a 2x2 checker at maxEdge 1 is mid-grey) and
// keeps flat colours flat.
func TestTransformDownscaleAverages(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.Black)
	img.Set(1, 1, color.Black)
	img.Set(1, 0, white)
	img.Set(0, 1, white)
	out, _, w, h, err := Transform(encodePNG(t, img), "image/png", 1, nil)
	if err != nil || w != 1 || h != 1 {
		t.Fatalf("%dx%d %v", w, h, err)
	}
	r, _, _, _ := decode(t, out).At(0, 0).RGBA()
	if r>>8 < 126 || r>>8 > 129 {
		t.Errorf("average is %d, want about 127", r>>8)
	}
	out, _, _, _, _ = Transform(encodePNG(t, quadrants(64, 64)), "image/png", 8, nil)
	if c := decode(t, out).At(1, 1); !near(c, red) {
		t.Errorf("flat red quadrant came out %v", c)
	}
}

// Decision (R4): never upscale. A small image asked for a larger maxEdge stays at its
// size; a region smaller than maxEdge is cropped but not enlarged.
func TestTransformNeverUpscales(t *testing.T) {
	small := encodePNG(t, quadrants(20, 10))
	_, _, w, h, err := Transform(small, "image/png", 500, nil)
	if err != nil || w != 20 || h != 10 {
		t.Errorf("%dx%d %v, want 20x10", w, h, err)
	}
	_, _, w, h, err = Transform(small, "image/png", 500, &Region{0, 0, 0.5, 0.5})
	if err != nil || w != 10 || h != 5 {
		t.Errorf("region+large maxEdge: %dx%d %v, want 10x5", w, h, err)
	}
}

// Decision: the region is normalised, applied before scaling, and picks the right
// pixels: each quadrant of the synthetic image is its own colour.
func TestTransformRegionCrop(t *testing.T) {
	data := encodePNG(t, quadrants(100, 60))
	for _, c := range []struct {
		name string
		r    Region
		w, h int
		want color.RGBA
	}{
		{"top-left", Region{0, 0, 0.5, 0.5}, 50, 30, red},
		{"top-right", Region{0.5, 0, 0.5, 0.5}, 50, 30, green},
		{"bottom-left", Region{0, 0.5, 0.5, 0.5}, 50, 30, blue},
		{"bottom-right", Region{0.5, 0.5, 0.5, 0.5}, 50, 30, white},
		{"full", Region{0, 0, 1, 1}, 100, 60, red},
		{"tiny, still one pixel", Region{0.99, 0.99, 0.0001, 0.0001}, 1, 1, white},
	} {
		out, typ, w, h, err := Transform(data, "image/png", 0, &c.r)
		if err != nil || typ != "image/png" || w != c.w || h != c.h {
			t.Errorf("%s: %dx%d %s err=%v, want %dx%d", c.name, w, h, typ, err, c.w, c.h)
			continue
		}
		img := decode(t, out)
		if got := img.At(0, 0); !near(got, c.want) {
			t.Errorf("%s: pixel (0,0) = %v, want %v", c.name, got, c.want)
		}
		if got := img.At(w-1, h-1); !near(got, c.want) && c.name != "full" {
			t.Errorf("%s: pixel (last) = %v, want %v", c.name, got, c.want)
		}
	}
	// Crop first, scale second: the top-right quadrant at maxEdge 10 is all green, which
	// a scale-then-crop of a different area would not be.
	out, _, w, h, _ := Transform(data, "image/png", 10, &Region{0.5, 0, 0.5, 0.5})
	if max(w, h) != 10 {
		t.Fatalf("%dx%d", w, h)
	}
	for _, p := range []image.Point{{0, 0}, {w - 1, h - 1}} {
		if c := decode(t, out).At(p.X, p.Y); !near(c, green) {
			t.Errorf("cropped then scaled pixel %v = %v, want green", p, c)
		}
	}
}

// Decision: a region halves the real fixtures' dimensions, as plan step 7's check says.
func TestTransformRegionOnFixtures(t *testing.T) {
	for _, name := range fixtures {
		data := fixture(t, name)
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		_, _, w, h, err := Transform(data, "image/png", 0, &Region{0, 0, 0.5, 0.5})
		if err != nil || w != (cfg.Width+1)/2 && w != cfg.Width/2 || h != (cfg.Height+1)/2 && h != cfg.Height/2 {
			t.Errorf("%s: %dx%d of %dx%d err=%v", name, w, h, cfg.Width, cfg.Height, err)
		}
	}
}

// Decision: an invalid region or maxEdge is REQUEST_INVALID_PARAMS and nothing is
// decoded; an image Transform cannot decode is not that code.
func TestTransformRejectsInvalidParams(t *testing.T) {
	data := encodePNG(t, quadrants(10, 10))
	nan := func() float64 { var z float64; return z / z }()
	for name, r := range map[string]Region{
		"negative x":     {-0.1, 0, 0.5, 0.5},
		"negative y":     {0, -0.1, 0.5, 0.5},
		"x above 1":      {1.1, 0, 0.1, 0.1},
		"zero width":     {0, 0, 0, 0.5},
		"zero height":    {0, 0, 0.5, 0},
		"negative width": {0.5, 0, -0.1, 0.5},
		"overflow x+w":   {0.6, 0, 0.6, 0.5},
		"overflow y+h":   {0, 0.6, 0.5, 0.6},
		"width above 1":  {0, 0, 1.5, 1},
		"nan":            {nan, 0, 0.5, 0.5},
	} {
		_, _, _, _, err := Transform(data, "image/png", 0, &r)
		if e, ok := err.(*capability.Error); !ok || e.Code != capability.ErrInvalidParams {
			t.Errorf("%s: %v, want REQUEST_INVALID_PARAMS", name, err)
		}
	}
	if _, _, _, _, err := Transform(data, "image/png", -1, nil); code(err) != capability.ErrInvalidParams {
		t.Errorf("negative maxEdge: %v", err)
	}
	if _, _, _, _, err := Transform(data, "image/png", 0, &Region{0.5, 0.5, 0.5, 0.5}); err != nil {
		t.Errorf("a region touching the edge is valid: %v", err)
	}
	if _, _, _, _, err := Transform([]byte("not an image"), "image/png", 5, nil); err == nil || code(err) == capability.ErrInvalidParams {
		t.Errorf("undecodable data: %v", err)
	}
}

// noisy is incompressible, so its PNG is far over 512 KiB.
func noisy(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(rnd.Intn(256)), byte(rnd.Intn(256)), byte(rnd.Intn(256)), 255
	}
	return img
}

// Decision (mcp-tool-surface.md §Images): the inline block is PNG while the PNG is at
// most 512 KiB and JPEG once it is over, in both cases at most the max edge, and the
// edge is capped at 2048 whatever is asked for.
func TestEncodeInlineJPEGFallbackThreshold(t *testing.T) {
	small := encodePNG(t, quadrants(300, 200))
	out, typ, w, h, err := EncodeInline(small, "image/png", 0)
	if err != nil || typ != "image/png" || w != 300 || h != 200 || !bytes.Equal(out, small) {
		t.Errorf("small: %s %dx%d err=%v unchanged=%v", typ, w, h, err, bytes.Equal(out, small))
	}

	big := encodePNG(t, noisy(600, 600))
	if len(big) <= InlinePNGLimit {
		t.Fatalf("test image is only %d bytes", len(big))
	}
	out, typ, w, h, err = EncodeInline(big, "image/png", 0)
	if err != nil || typ != "image/jpeg" || w != 600 || h != 600 {
		t.Fatalf("noisy: %s %dx%d err=%v", typ, w, h, err)
	}
	if _, format, err := image.Decode(bytes.NewReader(out)); err != nil || format != "jpeg" {
		t.Errorf("not a JPEG: %s %v", format, err)
	}

	// Just under the limit stays PNG.
	under := encodePNG(t, noisy(400, 400))
	if len(under) > InlinePNGLimit || len(encodePNG(t, noisy(450, 450))) <= InlinePNGLimit {
		t.Fatalf("threshold fixtures drifted: %d bytes at 400, %d at 450", len(under), len(encodePNG(t, noisy(450, 450))))
	}
	if _, typ, _, _, err := EncodeInline(under, "image/png", 0); err != nil || typ != "image/png" {
		t.Errorf("under the limit: %s %v", typ, err)
	}

	// The limit applies to what is delivered: downscaling the noise to 100 px makes it
	// small again, so it is a PNG.
	_, typ, w, h, err = EncodeInline(big, "image/png", 100)
	if err != nil || typ != "image/png" || w != 100 || h != 100 {
		t.Errorf("scaled noise: %s %dx%d %v", typ, w, h, err)
	}
}

func TestEncodeInlineEdgeDefaultsAndCap(t *testing.T) {
	data := encodePNG(t, quadrants(3000, 100))
	if _, _, w, _, err := EncodeInline(data, "image/png", 0); err != nil || w != 1024 {
		t.Errorf("default edge: %d %v, want 1024", w, err)
	}
	if _, _, w, _, err := EncodeInline(data, "image/png", 5000); err != nil || w != 2048 {
		t.Errorf("capped edge: %d %v, want 2048", w, err)
	}
	if _, _, w, _, err := EncodeInline(data, "image/png", 500); err != nil || w != 500 {
		t.Errorf("explicit edge: %d %v, want 500", w, err)
	}
}

// Reports the real sizes on the recorded screenshots; a regression guard that the
// inline block stays a PNG within 1024 or a JPEG within 1024, whichever it is.
func TestEncodeInlineOnFixtures(t *testing.T) {
	for _, name := range fixtures {
		data := fixture(t, name)
		out, typ, w, h, err := EncodeInline(data, "image/png", 1024)
		if err != nil {
			t.Fatal(err)
		}
		if max(w, h) > 1024 || (typ != "image/png" && typ != "image/jpeg") {
			t.Errorf("%s: %s %dx%d", name, typ, w, h)
		}
		if typ == "image/png" && len(out) > InlinePNGLimit {
			t.Errorf("%s: PNG of %d bytes over the limit", name, len(out))
		}
		t.Logf("%s: stored %d bytes -> inline %s %dx%d %d bytes", name, len(data), typ, w, h, len(out))
		tr, _, tw, th, err := Transform(data, "image/png", 1024, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: maxEdge 1024 PNG %dx%d %d bytes", name, tw, th, len(tr))
	}
}
