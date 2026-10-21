package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// Decision (plan m2z D7; plan m2z step 4): a derived command prints for a person by default,
// through the descriptor's own Text renderer where it has one and the generic renderer
// where it has none; --json and `call` print the envelope.
func TestHumanUsesTheDescriptorText(t *testing.T) {
	t.Parallel()
	t.Run("system capabilities prints the descriptor's text", func(t *testing.T) {
		p := startInProcess(t)
		env, code := p.call(t, "system.capabilities")
		if code != 0 {
			t.Fatalf("system.capabilities exited %d: %+v", code, env.Error)
		}
		ct, err := newCmdTree()
		if err != nil {
			t.Fatal(err)
		}
		d, ok := ct.reg.Lookup("system.capabilities")
		if !ok || d.Text == nil {
			t.Fatal("system.capabilities has no Text renderer")
		}
		var caps system.Capabilities
		if err := json.Unmarshal(env.Result, &caps); err != nil {
			t.Fatal(err)
		}
		want := d.Text(&caps)
		if want == "" {
			t.Fatal("the in-memory rendering is empty")
		}
		got := droidyard(t, p.state, "system", "capabilities", "--no-autostart")
		if c := got.wait(t); c != 0 {
			t.Fatalf("system capabilities exited %d: %s", c, got.stderr.String())
		}
		if strings.TrimRight(got.stdout.String(), "\n") != strings.TrimRight(want, "\n") {
			t.Errorf("printed:\n%s\nwant what the descriptor renders:\n%s", got.stdout.String(), want)
		}
	})

	t.Run("device list prints the generic table", func(t *testing.T) {
		p := fakeADBDaemon(t, map[string]string{
			"devices.out":           "List of devices attached\nemulator-5554 device product:sdk_gphone16k_arm64 transport_id:1\n",
			"emulator-5554.getprop": fixtureFile(t, "pixel9a-getprop.txt"),
			"emulator-5554.avd":     fixtureFile(t, "pixel9a-emu-avd-name.txt"),
		})
		got := droidyard(t, p.state, "device", "list", "--no-autostart")
		if c := got.wait(t); c != 0 {
			t.Fatalf("device list exited %d: %s", c, got.stderr.String())
		}
		lines := strings.Split(strings.TrimRight(got.stdout.String(), "\n"), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "device") || !strings.HasPrefix(lines[1], "emulator-5554") {
			t.Errorf("printed %q; want a header line and one row for the emulator", got.stdout.String())
		}
		if strings.Contains(got.stdout.String(), `"operation"`) || strings.Contains(got.stdout.String(), "{") {
			t.Errorf("the human output looks like the envelope: %q", got.stdout.String())
		}
	})
}
