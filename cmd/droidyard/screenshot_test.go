package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/artifacts"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

const shotDevices = "List of devices attached\n" +
	"emulator-5554 device product:sdk_gphone16k_arm64 transport_id:1\n" +
	"R58M12ABCDE device usb:1-1.2 product:raven model:Pixel_6_Pro transport_id:3\n"

func shotDaemon(t *testing.T, extra map[string]string) (*inProcess, string) {
	t.Helper()
	raw := fixtureFile(t, "pixel9a-screencap.png")
	files := map[string]string{
		"devices.out":                     shotDevices,
		"emulator-5554.bootcompleted.out": "1\n",
		"emulator-5554.screencap.out":     raw,
	}
	for k, v := range extra {
		files[k] = v
	}
	return fakeADBDaemon(t, files), raw
}

// Decision (plan step 7, A8): through the real CLI against the fake adb, device.screenshot
// returns an ephemeral droidyard://artifact/ reference with the fixture's dimensions and
// no host path (no string of the envelope holds the state directory or /tmp), marked
// untrusted; artifact.read of it returns the fixture's bytes, marked too, and honours
// maxEdge.
func TestScreenshotThroughCLI(t *testing.T) {
	p, raw := shotDaemon(t, nil)
	env, code := p.call(t, "device.screenshot", "--params", `{"device":"emulator-5554"}`)
	if code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("exit %d, %+v, error %+v", code, env.Operation, env.Error)
	}
	if env.Meta == nil || !env.Meta.Untrusted {
		t.Errorf("meta = %+v; want untrusted", env.Meta)
	}
	var ref artifact.Ref
	if err := json.Unmarshal(env.Result, &ref); err != nil {
		t.Fatal(err)
	}
	cfg, _ := png.DecodeConfig(strings.NewReader(raw))
	if !strings.HasPrefix(ref.Ref, "droidyard://artifact/") || ref.Durability != artifact.DurabilityEphemeral ||
		ref.Width != cfg.Width || ref.Height != cfg.Height || ref.MediaType != "image/png" || !ref.Untrusted {
		t.Errorf("ref %+v, want ephemeral %dx%d image/png untrusted", ref, cfg.Width, cfg.Height)
	}
	whole, _ := json.Marshal(env)
	for _, bad := range []string{p.state, "/tmp", "/var/folders"} {
		if strings.Contains(string(whole), bad) {
			t.Errorf("the envelope contains %q: a host path", bad)
		}
	}
	if bytes.Contains(whole, []byte(base64.StdEncoding.EncodeToString([]byte(raw[:63])))) {
		t.Error("the screenshot's bytes are in the screenshot result")
	}

	read, code := p.call(t, "artifact.read", "--params", `{"ref":"`+ref.Ref+`"}`)
	if code != 0 || read.Operation == nil || read.Operation.State != capability.StateCompleted {
		t.Fatalf("artifact.read exit %d, %+v, error %+v", code, read.Operation, read.Error)
	}
	if read.Meta == nil || !read.Meta.Untrusted {
		t.Errorf("artifact.read meta = %+v; want untrusted", read.Meta)
	}
	var r artifacts.ReadResult
	if err := json.Unmarshal(read.Result, &r); err != nil {
		t.Fatal(err)
	}
	got, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil || string(got) != raw {
		t.Errorf("artifact.read returned %d bytes (%v), want the fixture's %d", len(got), err, len(raw))
	}
	small, _ := p.call(t, "artifact.read", "--params", `{"ref":"`+ref.Ref+`","maxEdge":512}`)
	var rs artifacts.ReadResult
	if err := json.Unmarshal(small.Result, &rs); err != nil || max(rs.Delivered.Width, rs.Delivered.Height) != 512 ||
		rs.Stored.Width != cfg.Width || rs.Stored.Height != cfg.Height {
		t.Errorf("maxEdge 512: %+v (%v, error %+v)", rs, err, small.Error)
	}
}

// Decision (Q3): a screencap that prints text is DEVICE_SCREENSHOT_FAILED over the
// CLI; a physical serial is DEVICE_NOT_ALLOWED and an unknown one DEVICE_NOT_FOUND;
// nothing is stored by any of them.
func TestScreenshotFailuresThroughCLI(t *testing.T) {
	p, _ := shotDaemon(t, map[string]string{"emulator-5554.screencap.out": "Failed to take screenshot\n"})
	for serial, code := range map[string]capability.ErrorCode{
		"emulator-5554": device.ErrScreenshotFailed,
		"R58M12ABCDE":   device.ErrNotAllowed,
		"emulator-5999": device.ErrNotFound,
	} {
		env, _ := p.call(t, "device.screenshot", "--params", `{"device":"`+serial+`"}`)
		if env.Error == nil {
			t.Errorf("%s: no error in %+v", serial, env)
			continue
		}
		if env.Error.Code != code {
			t.Errorf("%s: %s, want %s", serial, env.Error.Code, code)
		}
	}
	if p.store.Len() != 0 {
		t.Errorf("the store holds %d artifacts after failures", p.store.Len())
	}
}
