package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// ErrScreenshotFailed is `screencap` failing, or printing no PNG, on a ready device
// (plan m1e, Q3). adb itself that cannot be run is DEVICE_ADB_FAILED, as in the
// preamble.
const ErrScreenshotFailed capability.ErrorCode = "DEVICE_SCREENSHOT_FAILED"

// ErrStreamFailed is the live view's source failing to start: the push of the server, the
// forward, the server's start, the connect to its sockets, or the first bytes of its
// stream not being the expected protocol (ADR-0077, ADR-0078 §1; plan m2p step 3). Its
// cause names the phase and the exit status, never device output. adb itself that cannot
// be run is DEVICE_ADB_FAILED, as in the preamble.
const ErrStreamFailed capability.ErrorCode = "DEVICE_STREAM_FAILED"

// screencapTimeout bounds `exec-out screencap -p`; a variable so a test is quick.
var screencapTimeout = 10 * time.Second

// screencapCap is the largest PNG read (plan m1e, step 7). Output past it is not a
// screenshot.
const screencapCap = 32 << 20

// ScreenshotParams is device.screenshot's parameters.
type ScreenshotParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
}

// ScreenshotResult is device.screenshot's result: the artifact's reference record,
// its fields flat, and adb's warnings. Never a path, never the bytes.
type ScreenshotResult struct {
	artifact.Ref
	// Warnings are adb's (client/server version mismatch) and the daemon's own secure-window
	// notice (ADR-0080), none of them device text: untagged.
	Warnings []string `json:"warnings,omitempty"`
}

func registerScreenshot(reg *capability.Registry, adb *ADB, store *artifact.Store) {
	reg.MustRegister(adb.plainThen(capability.Descriptor{
		Name:         "device.screenshot",
		Kind:         capability.Query,
		Scope:        capability.Global,
		Since:        "0.4.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Captures the device's screen as a PNG and returns an ephemeral artifact reference with its size, not the bytes. Read it with artifact.read, which can downscale or crop it; prefer device.uiTree when text is enough.",
		Params:       ScreenshotParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(ScreenshotParams).Device) },
		Result:       ScreenshotResult{},
		Errors:       append(append([]capability.ErrorCode{}, LentErrors...), ErrScreenshotFailed),
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			return adb.authorize(ctx, call.Origin, call.Params.(ScreenshotParams).Device, "device.screenshot")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			t := call.Authorized.(*target)
			if err := adb.ready(ctx, t); err != nil {
				return nil, err
			}
			return adb.screenshot(ctx, t, store, artifact.OwnerOf(call.Origin))
		},
	}, func(ctx context.Context, rt *remoteTarget, res any) (any, error) {
		return adb.screenshotFetched(ctx, rt, store, res.(ScreenshotResult))
	}))
}

func (a *ADB) screenshot(ctx context.Context, t *target, store *artifact.Store, owner string) (ScreenshotResult, error) {
	const cmd = "exec-out screencap -p"
	out, err := a.Run(ctx, t.adb, screencapTimeout, screencapCap, "-s", t.serial, "exec-out", "screencap", "-p")
	if cerr := ctx.Err(); cerr != nil {
		return ScreenshotResult{}, cerr
	}
	if err != nil && !errors.Is(err, ErrTimeout) {
		return ScreenshotResult{}, capability.NewError(ErrADBFailed, "adb could not be run").
			WithCause("adb " + cmd + ": " + err.Error()).
			WithNextStep("Check that the adb binary is executable, then retry.")
	}
	img, perr := checkPNG(out.Stdout)
	if err != nil || out.Truncated || out.ExitCode != 0 || perr != nil {
		// A PNG that decodes wins over any text in it; otherwise the command's own
		// output says whether the serial is gone.
		if line := disconnectedLine(t.serial, out); line != "" && perr != nil {
			return ScreenshotResult{}, disconnected(t.serial, line)
		}
		fail := screenshotFailed(cmd, out, err, perr)
		if secure, cerr := a.secureFocus(ctx, t); cerr == nil && secure {
			fail.Cause += "; " + secureCause
		}
		return ScreenshotResult{}, fail
	}
	// The one decode of the capture serves validation and the inline image the MCP
	// adapter delivers (plan m1e, step 8): the adapter never decodes it again. An image
	// that cannot be re-encoded is not one the device produced.
	d, mt, iw, ih, ierr := artifact.EncodeInlineDecoded(out.Stdout, "image/png", img, artifact.InlineMaxEdge)
	if ierr != nil {
		return ScreenshotResult{}, screenshotFailed(cmd, out, nil, fmt.Errorf("the PNG cannot be re-encoded: %w", ierr))
	}
	inline := &artifact.Inline{Data: d, MediaType: mt, Width: iw, Height: ih}
	ref, err := store.Put(artifact.Item{
		Data: out.Stdout, MediaType: "image/png", Width: img.Bounds().Dx(), Height: img.Bounds().Dy(),
		Untrusted: true, Producer: "device.screenshot", Owner: owner, Inline: inline,
	})
	if err != nil {
		return ScreenshotResult{}, err
	}
	warnings := t.warnings
	if secure, cerr := a.secureFocus(ctx, t); cerr == nil && secure {
		warnings = append(append([]string(nil), warnings...), secureWarning)
	}
	return ScreenshotResult{Ref: ref, Warnings: warnings}, nil
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")
var pngEnd = []byte("\x00\x00\x00\x00IEND\xaeB`\x82")

// checkPNG accepts what screencap -p must print: a PNG signature, a header with a
// positive size and the closing IEND chunk, so a cut stream is refused, and pixels
// that decode, so a corrupt image is not stored and blamed on a later reader.
func checkPNG(b []byte) (image.Image, error) {
	if !bytes.HasPrefix(b, pngSignature) {
		return nil, errors.New("no PNG signature")
	}
	if !bytes.HasSuffix(b, pngEnd) {
		return nil, errors.New("the PNG is cut: no closing chunk")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("the PNG header is unreadable: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, errors.New("the PNG has no size")
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("the PNG pixels are corrupt: %w", err)
	}
	return img, nil
}

func screenshotFailed(cmd string, out Output, runErr, pngErr error) *capability.Error {
	var what string
	switch {
	case errors.Is(runErr, ErrTimeout):
		what = "timed out after " + screencapTimeout.String()
	case out.Truncated:
		what = fmt.Sprintf("printed more than the %d MiB limit", screencapCap>>20)
	case out.ExitCode != 0:
		what = fmt.Sprintf("exited with status %d", out.ExitCode)
	default:
		what = "printed no PNG (" + pngErr.Error() + ")"
	}
	// Device text is quoted only when it is text: a stream of image bytes is not.
	if l := textLine(out); l != "" {
		what += ": " + l
	}
	return capability.NewError(ErrScreenshotFailed, "the device did not produce a screenshot").
		WithCause("adb " + cmd + " " + what).
		WithNextStep("Retry device.screenshot. If it keeps failing, the screen may be off or secured (a screen with secure content captures black or not at all): wake the device.")
}

// textLine is the last line of the command's stderr, else stdout, when that output is
// short printable text, and "" when it is binary.
func textLine(out Output) string {
	for _, b := range [][]byte{out.Stderr, out.Stdout} {
		if len(b) == 0 || len(b) > 4<<10 || !utf8.Valid(b) || strings.IndexFunc(string(b), isControl) >= 0 {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
			return truncate(l, 200)
		}
	}
	return ""
}

// isControl is a control character other than the line breaks and tab text has.
func isControl(r rune) bool { return r < 0x20 && r != '\n' && r != '\r' && r != '\t' }
