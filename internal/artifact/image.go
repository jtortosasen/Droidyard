package artifact

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"sync/atomic"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// decodes counts the full image decodes this package makes itself, so a test can show
// that a producer which hands in its decoded image is not decoded a second time.
var decodes atomic.Int64

// Decodes reports how many full image decodes this package has made (a test seam).
func Decodes() int64 { return decodes.Load() }

// Region is a crop in normalised 0..1 coordinates of the stored image: origin X, Y
// and size W, H (artifact.read's `region`, ADR-0012 §Reading artifacts).
type Region struct{ X, Y, W, H float64 }

// Inline delivery defaults for the MCP adapter (mcp.md §Images and
// artifacts): the longest edge, its hard cap, the PNG size above which JPEG is used,
// and the JPEG quality.
const (
	InlineMaxEdge     = 1024
	InlineMaxEdgeCap  = 2048
	InlinePNGLimit    = 512 << 10
	InlineJPEGQuality = 85
)

const regionEpsilon = 1e-9

func invalid(msg string) error {
	return capability.NewError(capability.ErrInvalidParams, msg)
}

// Validate reports whether the region lies inside 0..1 and is not empty.
func (r Region) Validate() error {
	for _, v := range []float64{r.X, r.Y, r.W, r.H} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return invalid("region values must be finite numbers")
		}
	}
	if r.X < 0 || r.Y < 0 || r.X > 1 || r.Y > 1 {
		return invalid("region x and y must be within 0..1")
	}
	if r.W <= 0 || r.H <= 0 {
		return invalid("region width and height must be greater than 0")
	}
	if r.X+r.W > 1+regionEpsilon || r.Y+r.H > 1+regionEpsilon {
		return invalid("region must lie inside the image: x+w and y+h are at most 1")
	}
	return nil
}

// Transform is artifact.read's image path. With no region, and no maxEdge or one at or
// above the image's long edge, it returns data untouched (same bytes, same media
// type). Otherwise it decodes, crops to region (applied before scaling), downscales so
// the long edge is at most maxEdge (never upscaling) and re-encodes as PNG. maxEdge 0
// means no limit. The returned width and height are the delivered ones. A bad region
// or a negative maxEdge is REQUEST_INVALID_PARAMS.
func Transform(data []byte, mediaType string, maxEdge int, region *Region) (out []byte, outType string, w, h int, err error) {
	out, outType, w, h, _, err = transform(data, mediaType, maxEdge, region, nil)
	return
}

// transform is Transform with the image already decoded when the caller has it (nil
// otherwise), so a producer that had to decode it to validate it does not decode twice.
// pix is the delivered pixels when they are at hand (decoded, cropped or scaled), else
// nil.
func transform(data []byte, mediaType string, maxEdge int, region *Region, decoded image.Image) (out []byte, outType string, w, h int, pix image.Image, err error) {
	if maxEdge < 0 {
		return nil, "", 0, 0, nil, invalid("maxEdge must be positive")
	}
	if region != nil {
		if err := region.Validate(); err != nil {
			return nil, "", 0, 0, nil, err
		}
	}
	var cw, ch int
	if decoded != nil {
		b := decoded.Bounds()
		cw, ch = b.Dx(), b.Dy()
	} else {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, "", 0, 0, nil, fmt.Errorf("artifact: not a decodable image: %w", err)
		}
		cw, ch = cfg.Width, cfg.Height
	}
	if region == nil && (maxEdge == 0 || maxEdge >= max(cw, ch)) {
		return data, mediaType, cw, ch, decoded, nil
	}
	src := decoded
	if src == nil {
		var err error
		decodes.Add(1)
		if src, _, err = image.Decode(bytes.NewReader(data)); err != nil {
			return nil, "", 0, 0, nil, fmt.Errorf("artifact: not a decodable image: %w", err)
		}
	}
	if region != nil {
		src = crop(src, *region)
	}
	if b := src.Bounds(); maxEdge > 0 && maxEdge < max(b.Dx(), b.Dy()) {
		src = downscale(src, maxEdge)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		return nil, "", 0, 0, nil, err
	}
	b := src.Bounds()
	return buf.Bytes(), "image/png", b.Dx(), b.Dy(), src, nil
}

// EncodeInline is the MCP adapter's rule for the image block: the longest edge at
// most maxEdge (0 means InlineMaxEdge; above InlineMaxEdgeCap it is capped), PNG, or
// JPEG at quality 85 when the PNG exceeds 512 KiB. A source that already fits is not
// re-scaled, but is still turned into JPEG if its PNG is over the limit.
func EncodeInline(data []byte, mediaType string, maxEdge int) (out []byte, outType string, w, h int, err error) {
	return encodeInline(data, mediaType, maxEdge, nil)
}

// EncodeInlineDecoded is EncodeInline for a producer that has already decoded data
// (img is data's pixels): the image is not decoded a second time.
func EncodeInlineDecoded(data []byte, mediaType string, img image.Image, maxEdge int) (out []byte, outType string, w, h int, err error) {
	return encodeInline(data, mediaType, maxEdge, img)
}

func encodeInline(data []byte, mediaType string, maxEdge int, decoded image.Image) (out []byte, outType string, w, h int, err error) {
	if maxEdge <= 0 {
		maxEdge = InlineMaxEdge
	}
	maxEdge = min(maxEdge, InlineMaxEdgeCap)
	out, outType, w, h, img, err := transform(data, mediaType, maxEdge, nil, decoded)
	if err != nil {
		return nil, "", 0, 0, err
	}
	if outType == "image/jpeg" || len(out) <= InlinePNGLimit {
		return out, outType, w, h, nil
	}
	// The JPEG is made from the pixels transform had, not from a decode of its PNG.
	if img == nil {
		decodes.Add(1)
		if img, _, err = image.Decode(bytes.NewReader(out)); err != nil {
			return nil, "", 0, 0, fmt.Errorf("artifact: not a decodable image: %w", err)
		}
	}
	// JPEG has no alpha: flatten onto white.
	b := img.Bounds()
	flat := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), img, b.Min, draw.Over)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: InlineJPEGQuality}); err != nil {
		return nil, "", 0, 0, err
	}
	return buf.Bytes(), "image/jpeg", w, h, nil
}

// crop returns the region of src as a new image with origin 0,0. The edges are
// rounded to whole pixels and the result is at least 1x1.
func crop(src image.Image, r Region) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	x0 := clamp(int(math.Round(r.X*float64(sw))), 0, sw-1)
	y0 := clamp(int(math.Round(r.Y*float64(sh))), 0, sh-1)
	x1 := clamp(int(math.Round((r.X+r.W)*float64(sw))), x0+1, sw)
	y1 := clamp(int(math.Round((r.Y+r.H)*float64(sh))), y0+1, sh)
	dst := image.NewNRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	draw.Draw(dst, dst.Bounds(), src, image.Pt(b.Min.X+x0, b.Min.Y+y0), draw.Src)
	return dst
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }

// downscale shrinks src so its long edge is exactly edge, keeping the aspect ratio
// (the other edge rounded, at least 1), by area averaging in premultiplied colour.
func downscale(src image.Image, edge int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := edge, edge
	if sw >= sh {
		dh = max(1, int(math.Round(float64(sh)*float64(edge)/float64(sw))))
	} else {
		dw = max(1, int(math.Round(float64(sw)*float64(edge)/float64(sh))))
	}
	rgba := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)

	// Horizontal pass: sw x sh -> dw x sh, four float32 channels per pixel.
	tmp := make([]float32, dw*sh*4)
	for y := 0; y < sh; y++ {
		row := rgba.Pix[y*rgba.Stride : y*rgba.Stride+sw*4]
		for dx := 0; dx < dw; dx++ {
			lo, hi := float64(dx)*float64(sw)/float64(dw), float64(dx+1)*float64(sw)/float64(dw)
			var acc [4]float64
			for sx := int(lo); sx < sw && float64(sx) < hi; sx++ {
				wgt := math.Min(float64(sx+1), hi) - math.Max(float64(sx), lo)
				for c := 0; c < 4; c++ {
					acc[c] += float64(row[sx*4+c]) * wgt
				}
			}
			span := hi - lo
			for c := 0; c < 4; c++ {
				tmp[(y*dw+dx)*4+c] = float32(acc[c] / span)
			}
		}
	}
	// Vertical pass: dw x sh -> dw x dh.
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		lo, hi := float64(dy)*float64(sh)/float64(dh), float64(dy+1)*float64(sh)/float64(dh)
		span := hi - lo
		for dx := 0; dx < dw; dx++ {
			var acc [4]float64
			for sy := int(lo); sy < sh && float64(sy) < hi; sy++ {
				wgt := math.Min(float64(sy+1), hi) - math.Max(float64(sy), lo)
				for c := 0; c < 4; c++ {
					acc[c] += float64(tmp[(sy*dw+dx)*4+c]) * wgt
				}
			}
			for c := 0; c < 4; c++ {
				v := int(math.Round(acc[c] / span))
				dst.Pix[dy*dst.Stride+dx*4+c] = uint8(clamp(v, 0, 255))
			}
		}
	}
	return dst
}
