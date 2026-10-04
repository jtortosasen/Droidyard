package device

import (
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

const screencapCall = "-s emulator-5554 exec-out screencap -p"

func (f *uiFixture) shoot(t *testing.T, device string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.screenshot", Params: []byte(`{"device":"` + device + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return f.wait(t, id)
}

func shotFixture(t *testing.T, files map[string]string) *uiFixture {
	t.Helper()
	return newUIFixture(t, files)
}

// Decision (check, device.screenshot): the PNG screencap prints goes only to the
// store, byte for byte, and the result is the reference record of the stored artifact:
// ephemeral, the PNG's own dimensions, marked untrusted, produced by device.screenshot.
// Run on the three recorded screens, so the dimensions are the devices' own.
func TestScreenshotStoresTheRecordedPNGs(t *testing.T) {
	for _, img := range uiImages {
		raw := fixture(t, img+"-screencap.png")
		f := shotFixture(t, map[string]string{uiSerial + ".screencap.out": raw})
		out := f.shoot(t, uiSerial)
		if out.State != capability.StateCompleted {
			t.Fatalf("%s: %s, %+v", img, out.State, out.Error)
		}
		res := out.Result.(ScreenshotResult)
		cfg, err := png.DecodeConfig(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if res.Width != cfg.Width || res.Height != cfg.Height || res.Bytes != len(raw) {
			t.Errorf("%s: record %+v, want %dx%d and %d bytes", img, res.Ref, cfg.Width, cfg.Height, len(raw))
		}
		if res.Durability != artifact.DurabilityEphemeral || !res.Untrusted || res.MediaType != "image/png" ||
			!strings.HasPrefix(res.Ref.Ref, artifact.RefPrefix) {
			t.Errorf("%s: record %+v", img, res.Ref)
		}
		e, err := f.store.Get(res.Ref.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if string(e.Data) != raw || e.Producer != "device.screenshot" || !e.Ref.Untrusted {
			t.Errorf("%s: stored entry differs from what screencap printed (producer %q)", img, e.Producer)
		}
		if n := count(f.calls(t), screencapCall); n != 1 {
			t.Errorf("%s: %d screencap runs, want 1", img, n)
		}
	}
}

// Decision (Q3): a screencap that prints text on a ready device is
// DEVICE_SCREENSHOT_FAILED with the text's last line as the cause, and nothing is
// stored. A stream of non-PNG bytes is the same code and is never quoted; a PNG that is
// cut (no closing chunk) is refused; a PNG with a non-zero exit status is refused.
func TestScreenshotFailureIsTheQ3Code(t *testing.T) {
	good := fixture(t, "pixel9a-screencap.png")
	// A PNG whose IDAT bytes are corrupted keeps its signature, header and IEND.
	bad := []byte(good)
	idat := strings.Index(good, "IDAT")
	for i := idat + 8; i < idat+40; i++ {
		bad[i] ^= 0xff
	}
	cases := []struct {
		name  string
		files map[string]string
		cause string // must appear
		not   string // must not appear
	}{
		{"text", map[string]string{uiSerial + ".screencap.out": "Failed to take screenshot: display off\n"}, "display off", ""},
		{"text on stderr, exit 1", map[string]string{uiSerial + ".screencap.err": "screencap: no display\n", uiSerial + ".screencap.exit": "1"}, "no display", ""},
		{"binary garbage, not UTF-8", map[string]string{uiSerial + ".screencap.out": "\xff\xfe garbage \x89"}, "no PNG", "garbage"},
		{"binary garbage with NULs", map[string]string{uiSerial + ".screencap.out": "\x00\x01\x02 garbage \x1a"}, "no PNG", "garbage"},
		{"cut PNG", map[string]string{uiSerial + ".screencap.out": good[:len(good)/2]}, "cut", ""},
		{"corrupt IDAT", map[string]string{uiSerial + ".screencap.out": string(bad)}, "corrupt", ""},
		{"empty", map[string]string{uiSerial + ".screencap.out": ""}, "no PNG", ""},
		{"PNG and exit 1", map[string]string{uiSerial + ".screencap.out": good, uiSerial + ".screencap.exit": "1"}, "status 1", ""},
	}
	for _, c := range cases {
		f := shotFixture(t, c.files)
		ce := mustFail(t, f.shoot(t, uiSerial), ErrScreenshotFailed)
		if !strings.Contains(ce.Cause, c.cause) || (c.not != "" && strings.Contains(ce.Cause, c.not)) || ce.NextStep == "" {
			t.Errorf("%s: cause %q, next step %q; want %q in the cause, %q out of it", c.name, ce.Cause, ce.NextStep, c.cause, c.not)
		}
		if f.store.Len() != 0 {
			t.Errorf("%s: the store holds %d artifacts after a failed screenshot", c.name, f.store.Len())
		}
	}
}

// Decision (plan step 7, as step 6): a serial adb says is gone is DEVICE_DISCONNECTED,
// read from screencap's own stderr, not DEVICE_SCREENSHOT_FAILED.
func TestScreenshotDisconnectedFromItsOwnStderr(t *testing.T) {
	f := shotFixture(t, map[string]string{
		uiSerial + ".screencap.err":  "error: device 'emulator-5554' not found\n",
		uiSerial + ".screencap.exit": "1",
	})
	ce := mustFail(t, f.shoot(t, uiSerial), ErrDisconnected)
	if !strings.Contains(ce.Cause, "not found") {
		t.Errorf("cause %q", ce.Cause)
	}
	if n := count(f.calls(t), "devices -l"); n != 1 {
		t.Errorf("%d `devices -l`; the disconnect must come from screencap's own stderr", n)
	}
}

// Decision (Q2, step 6's preamble through the same hook): a physical serial is
// DEVICE_NOT_ALLOWED and an unknown one DEVICE_NOT_FOUND with details.online; screencap
// never runs and nothing is stored.
func TestScreenshotPreamble(t *testing.T) {
	f := shotFixture(t, map[string]string{
		"devices.out":               uiList(phoneLine, "R58M12ABCDE device usb:1-1.2 product:raven model:Pixel_6_Pro transport_id:3\n"),
		uiSerial + ".screencap.out": fixture(t, "pixel9a-screencap.png"),
	})
	mustFail(t, f.shoot(t, "R58M12ABCDE"), ErrNotAllowed)
	ce := mustFail(t, f.shoot(t, "emulator-5999"), ErrNotFound)
	if online, _ := ce.Details["online"].([]string); len(online) != 1 || online[0] != uiSerial {
		t.Errorf("details %v, want online [%s]", ce.Details, uiSerial)
	}
	for _, c := range f.calls(t) {
		if strings.Contains(c, "screencap") {
			t.Errorf("screencap ran: %q", c)
		}
	}
	if f.store.Len() != 0 {
		t.Error("something was stored")
	}
}

// Decision (Q3, step 7's 10 s bound): a screencap that never answers ends at its
// timeout as DEVICE_SCREENSHOT_FAILED naming the timeout.
func TestScreenshotTimeout(t *testing.T) {
	old := screencapTimeout
	screencapTimeout = 300 * time.Millisecond
	t.Cleanup(func() { screencapTimeout = old })
	f := shotFixture(t, map[string]string{uiSerial + ".screencap.hang": ""})
	start := time.Now()
	ce := mustFail(t, f.shoot(t, uiSerial), ErrScreenshotFailed)
	if !strings.Contains(ce.Cause, "timed out") || time.Since(start) > 5*time.Second {
		t.Errorf("cause %q after %v", ce.Cause, time.Since(start))
	}
}

// Decision (check, no host path): the result names no file: its JSON holds no path
// and none of the daemon's directories.

// Decision (plan m1e step 8, decode once): the capture is decoded once, for validation,
// and that decode makes the inline variant the MCP adapter delivers; the store holds it
// with the artifact. This test checks that the held variant is the recorded screen
// downscaled to at most 1024 px with the bytes EncodeInline gives; that the capture is
// not decoded a second time is TestScreenshotDecodesTheCaptureOnce.
func TestScreenshotHoldsItsInlineVariant(t *testing.T) {
	for _, img := range uiImages {
		raw := fixture(t, img+"-screencap.png")
		f := shotFixture(t, map[string]string{uiSerial + ".screencap.out": raw})
		res := f.shoot(t, uiSerial).Result.(ScreenshotResult)
		e, err := f.store.Get(res.Ref.Ref)
		if err != nil || e.Inline == nil {
			t.Fatalf("%s: %v, inline %v", img, err, e.Inline)
		}
		want, mt, w, h, err := artifact.EncodeInline([]byte(raw), "image/png", artifact.InlineMaxEdge)
		if err != nil || e.Inline.MediaType != mt || e.Inline.Width != w || e.Inline.Height != h || !bytes.Equal(e.Inline.Data, want) {
			t.Errorf("%s: inline %s %dx%d (%d bytes), want %s %dx%d (%d bytes)", img, e.Inline.MediaType, e.Inline.Width, e.Inline.Height, len(e.Inline.Data), mt, w, h, len(want))
		}
		if max(e.Inline.Width, e.Inline.Height) > artifact.InlineMaxEdge {
			t.Errorf("%s: inline edge %dx%d is over %d", img, e.Inline.Width, e.Inline.Height, artifact.InlineMaxEdge)
		}
	}
}

// Decision (plan m1e step 8, decode once): the handler hands its one decode to the
// inline encoder. artifact.Decodes counts the decodes the artifact package makes
// itself; the handler's own decode is the validation one, so the count must not move
// (a handler calling EncodeInline instead of EncodeInlineDecoded decodes again).
func TestScreenshotDecodesTheCaptureOnce(t *testing.T) {
	for _, img := range uiImages {
		raw := fixture(t, img+"-screencap.png")
		f := shotFixture(t, map[string]string{uiSerial + ".screencap.out": raw})
		before := artifact.Decodes()
		f.shoot(t, uiSerial)
		if n := artifact.Decodes() - before; n != 0 {
			t.Errorf("%s: the artifact package decoded the capture %d more times; want none", img, n)
		}
	}
}
