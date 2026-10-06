package main

import (
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// tapDoc is the form app's shape as the fake reader returns it: a like button, a
// counter and two nodes that make a selector ambiguous.
const tapDoc = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">` +
	`<node class="android.widget.FrameLayout" package="dev.droidyard.formapp" resource-id="dev.droidyard.formapp:id/root" bounds="[0,0][1080,2340]">` +
	`<node class="android.widget.Button" package="dev.droidyard.formapp" resource-id="dev.droidyard.formapp:id/like" text="Like" bounds="[0,0][200,200]" clickable="true"></node>` +
	`<node class="android.widget.TextView" package="dev.droidyard.formapp" resource-id="dev.droidyard.formapp:id/likes" text="Likes: 1" bounds="[0,200][200,300]"></node>` +
	`<node class="android.widget.Button" package="dev.droidyard.formapp" text="Twin" bounds="[300,300][400,400]"></node>` +
	`<node class="android.widget.Button" package="dev.droidyard.formapp" text="Twin" bounds="[300,300][400,400]"></node>` +
	`</node></hierarchy>`

// tapDaemon is the shipped wiring over the fake adb and a fake persistent reader, so a
// tapElement resolves and taps through the real handlers.
func tapDaemon(t *testing.T) *inProcess {
	t.Helper()
	r := devicetest.StartReader(t, tapDoc)
	t.Cleanup(r.Close)
	return fakeADBDaemon(t, map[string]string{
		"devices.out":                     "List of devices attached\nemulator-5554 device product:sdk_gphone16k_arm64 transport_id:1\n",
		"emulator-5554.bootcompleted.out": "1\n",
		"emulator-5554.push.exit":         "0",
		"emulator-5554.start.out":         "",
		"emulator-5554.forward.out":       r.Port,
	})
}

// Decision (plan m1f step 6, check a; A7, R4, D5): a tapElement result is marked on the
// CLI envelope and over MCP (the fixed line and _meta), and DEVICE_ELEMENT_AMBIGUOUS —
// an error whose details quote device text — carries meta.untrusted on the CLI and
// _meta over MCP, with D5's note on its message. The adapter rendering is step 3's
// generic path; this test wires the shipped capability to it.
func TestTapElementMarkedOnCLIAndMCP(t *testing.T) {
	t.Parallel()
	p := tapDaemon(t)

	env, code := p.call(t, "device.tapElement", "--params", `{"device":"emulator-5554","target":{"id":"like"}}`)
	if code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("exit %d, %+v, error %+v", code, env.Operation, env.Error)
	}
	if env.Meta == nil || !env.Meta.Untrusted {
		t.Errorf("meta = %+v; want untrusted true", env.Meta)
	}

	amb, code := p.call(t, "device.tapElement", "--params", `{"device":"emulator-5554","target":{"text":"Twin"}}`)
	if code == 0 || amb.Error == nil || amb.Error.Code != device.ErrElementAmbiguous {
		t.Fatalf("ambiguous: exit %d, error %+v", code, amb.Error)
	}
	if amb.Meta == nil || !amb.Meta.Untrusted {
		t.Errorf("ambiguous meta = %+v; want untrusted true", amb.Meta)
	}
	if !strings.HasSuffix(amb.Error.Message, capability.UntrustedErrorNote) {
		t.Errorf("ambiguous message %q does not end with D5's note", amb.Error.Message)
	}

	c := startShim(t, p.state, "client", "--wait", "5s")
	c.initialize("tap")
	c.callTool(1, "device_tapElement", `{"device":"emulator-5554","target":{"id":"like"}}`)
	r := decodeTool(t, c.recv(), 1)
	if line, _, ok := strings.Cut(r.text, "\n"); !ok || line != untrustedLine {
		t.Fatalf("text %q does not open with the fixed untrusted line", r.text)
	}
	if m := metaOf(t, r); m["droidyard/untrusted"] != true {
		t.Errorf("_meta = %v; want droidyard/untrusted true", m)
	}
	c.callTool(2, "device_tapElement", `{"device":"emulator-5554","target":{"text":"Twin"}}`)
	e := decodeTool(t, c.recv(), 2)
	if !e.isError {
		t.Fatalf("the ambiguous call is not an MCP error: %v", e.raw)
	}
	if m := metaOf(t, e); m["droidyard/untrusted"] != true {
		t.Errorf("ambiguous _meta = %v; want droidyard/untrusted true", m)
	}
	c.exit()
}
