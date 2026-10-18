package emulator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// Device occupation at the emulator capabilities (ADR-0094 §2, §3): stop and wipe act on an
// emulator and are refused one another session holds, before anything is killed; start
// holds the serial it boots, taking it over, and one it adopts only when free; a stopped
// emulator is released.

// occEmu is the fixture with the registry of sessions wired as the daemon wires it.
type occEmu struct {
	*fixture
	sessions *capability.Sessions
}

func newOccEmu(t *testing.T, adbFiles map[string]string) *occEmu {
	t.Helper()
	f := newFixture(t, adbFiles)
	f.adb.SetOccupation(f.ops.Sessions())
	return &occEmu{fixture: f, sessions: f.ops.Sessions()}
}

func (f *occEmu) connect(id, name string) {
	f.sessions.Connect(id, "chat-"+id)
	f.sessions.Named(id, name)
	f.sessions.Opened(id, "/main", "ws-main", "/main")
}

// as runs a capability as the MCP session id and waits for its outcome.
func (f *occEmu) as(t *testing.T, id, capName, params string) capability.Outcome {
	t.Helper()
	out, err := f.ops.Invoke(context.Background(), capability.Request{
		Capability: capName, Params: []byte(params), Client: capability.ClientMCP,
		Origin: capability.Origin{ID: id, Client: capability.ClientMCP},
	})
	if err != nil {
		t.Fatalf("Invoke %s: %v", capName, err)
	}
	return out
}

func (f *occEmu) holder(serial string) (capability.Holder, bool) {
	return f.sessions.DeviceHolder(serial, "")
}

// Decision (ADR-0094 §2, §5): another session's emulator.stop of a held emulator fails
// DEVICE_OCCUPIED naming the holder, with no `emu kill` in the fake's log; the holder's own
// stop runs, and the serial it stopped is released. Seen failing with the check removed
// from the stop's hook: the other session's stop killed it.
func TestDeviceOccupationStop(t *testing.T) {
	scaleStop(t, time.Second, 5*time.Millisecond)
	killOut, _ := os.ReadFile(filepath.Join("testdata", "emu-kill.txt"))
	avdName, _ := os.ReadFile(filepath.Join("testdata", "emu-avd-name.txt"))
	f := newOccEmu(t, map[string]string{
		"devices.out":             listOf(emuLine),
		serial + ".avd":           string(avdName),
		serial + ".emukill.out":   string(killOut),
		serial + ".emukill.leave": "1",
	})
	f.connect("a", "agent-a")
	f.connect("b", "agent-b")
	f.sessions.OccupyDevice("a", serial)

	if e := refusedOccupied(t, f.as(t, "b", "emulator.stop", `{"device":"`+serial+`"}`)); e != nil && !strings.Contains(e.Message, "agent-a") {
		t.Errorf("message %q does not name the holder", e.Message)
	}
	if n := countCall(f.calls(t), "-s "+serial+" emu kill"); n != 0 {
		t.Errorf("the refused stop ran %d `emu kill`", n)
	}
	if h, _ := f.holder(serial); h.Name != "agent-a" {
		t.Errorf("the refused stop moved the hold to %+v", h)
	}

	mustStop(t, f.as(t, "a", "emulator.stop", `{"device":"`+serial+`"}`))
	if n := countCall(f.calls(t), "-s "+serial+" emu kill"); n != 1 {
		t.Errorf("the holder's own stop ran %d `emu kill`, want one", n)
	}
	if h, ok := f.holder(serial); ok {
		t.Errorf("a stopped emulator is still held by %+v", h)
	}
}

// Decision (ADR-0094 §2): a stop of a free emulator holds it for the stopping session only
// until it has left adb devices, so nothing is held after; and a serial that is not an
// emulator console is neither held nor refused by the hook (the handler refuses it).
func TestDeviceOccupationStopLeavesNothingHeld(t *testing.T) {
	scaleStop(t, time.Second, 5*time.Millisecond)
	f := newOccEmu(t, map[string]string{
		"devices.out":             listOf(emuLine, ipLine),
		serial + ".avd":           loadedAVD + "\r\nOK\r\n",
		serial + ".emukill.leave": "1",
	})
	f.connect("a", "agent-a")
	f.connect("b", "agent-b")
	mustStop(t, f.as(t, "a", "emulator.stop", `{"device":"`+serial+`"}`))
	if _, ok := f.holder(serial); ok {
		t.Error("a stop left the emulator held")
	}
	f.sessions.OccupyDevice("b", "192.168.0.2:5555")
	mustFail(t, f.as(t, "a", "emulator.stop", `{"device":"192.168.0.2:5555"}`), ErrNotAnEmulator)
}

// Decision (ADR-0094 §2): emulator.wipe of the AVD running on a serial another session
// holds fails DEVICE_OCCUPIED once the serial is known and before anything is killed or
// launched: no `emu kill` in the fake's log and the launcher is never asked. Seen failing
// with the check removed: the wipe killed the holder's emulator and booted it again.
func TestDeviceOccupationWipe(t *testing.T) {
	scaleBoot(t, 10*time.Second, 20*time.Millisecond)
	scaleStop(t, 5*time.Second, 20*time.Millisecond)
	f := newOccEmu(t, map[string]string{"devices.out": listHead})
	// The AVD runs on emulator-5554; a wiped boot would come up on emulator-5556.
	devicetest.RegisterEmulator(f.adbDir, serial2, loadedAVD, 4242)
	f.emuWrite(t, loadedAVD+".serial", "emulator-5556\n")
	f.launcher(t, loadedAVD+"\n")
	f.connect("a", "agent-a")
	f.connect("b", "agent-b")
	f.sessions.OccupyDevice("a", serial2)

	if e := refusedOccupied(t, f.as(t, "b", "emulator.wipe", `{"avd":"`+loadedAVD+`"}`)); e != nil && (!strings.Contains(e.Message, "agent-a") || !strings.Contains(e.Message, serial2)) {
		t.Errorf("message %q does not name the holder and the device", e.Message)
	}
	if n := countCall(f.calls(t), "-s "+serial2+" emu kill"); n != 0 {
		t.Errorf("the refused wipe ran %d `emu kill`", n)
	}
	if c := f.launchCall(t); c != "" {
		t.Errorf("the refused wipe asked the launcher to boot: %q", c)
	}

	// The holder's own wipe runs; the old serial is released when it leaves and the session
	// holds what it booted.
	r := mustWipe(t, f.as(t, "a", "emulator.wipe", `{"avd":"`+loadedAVD+`"}`))
	if r.Device != "emulator-5556" {
		t.Fatalf("the wipe came up on %s", r.Device)
	}
	if _, ok := f.holder(serial2); ok {
		t.Error("the serial the wipe stopped is still held")
	}
	if h, ok := f.holder("emulator-5556"); !ok || h.Name != "agent-a" {
		t.Errorf("the wiped boot is held by %+v, %v; want agent-a", h, ok)
	}
}

// Decision (ADR-0094 §2): emulator.start holds the serial it boots, taking it over from
// whoever held it, and is never refused; an emulator it adopts is held only when nobody
// holds it. Seen failing with the boot's hold removed: the new serial was held by no one.
func TestDeviceOccupationStart(t *testing.T) {
	t.Run("a fresh boot takes the serial", func(t *testing.T) {
		scaleBoot(t, 10*time.Second, 10*time.Millisecond)
		f := newOccEmu(t, map[string]string{"devices.out": listHead})
		f.launcher(t, loadedAVD+"\n")
		f.connect("a", "agent-a")
		f.connect("b", "agent-b")
		f.sessions.OccupyDevice("b", "emulator-5556") // a serial B held before it left

		r := mustStart(t, f.as(t, "a", "emulator.start", `{"avd":"`+loadedAVD+`"}`))
		if r.Adopted || r.Device != "emulator-5556" {
			t.Fatalf("result = %+v; want a fresh boot on emulator-5556", r)
		}
		if h, ok := f.holder("emulator-5556"); !ok || h.Name != "agent-a" {
			t.Errorf("the booted serial is held by %+v, %v; want agent-a", h, ok)
		}
	})

	t.Run("a boot by the CLI holds nothing", func(t *testing.T) {
		scaleBoot(t, 10*time.Second, 10*time.Millisecond)
		f := newOccEmu(t, map[string]string{"devices.out": listHead})
		f.launcher(t, loadedAVD+"\n")
		f.connect("a", "agent-a")
		out, err := f.ops.Invoke(context.Background(), capability.Request{
			Capability: "emulator.start", Params: []byte(`{"avd":"` + loadedAVD + `"}`), Client: capability.ClientCLI,
			Origin: capability.Origin{ID: "a", Client: capability.ClientCLI, Joined: "a"},
		})
		if err != nil {
			t.Fatal(err)
		}
		mustStart(t, out)
		if h, ok := f.holder("emulator-5556"); ok {
			t.Errorf("a CLI boot (even one joined to A) left %s held by %+v", "emulator-5556", h)
		}
	})

	t.Run("a boot by the CLI frees a hold left on its serial", func(t *testing.T) {
		scaleBoot(t, 10*time.Second, 10*time.Millisecond)
		f := newOccEmu(t, map[string]string{"devices.out": listHead})
		f.launcher(t, loadedAVD+"\n")
		f.connect("b", "agent-b")
		f.sessions.OccupyDevice("b", "emulator-5556") // B's emulator vanished unseen
		out, err := f.ops.Invoke(context.Background(), capability.Request{
			Capability: "emulator.start", Params: []byte(`{"avd":"` + loadedAVD + `"}`), Client: capability.ClientCLI,
			Origin: capability.Origin{Client: capability.ClientCLI},
		})
		if err != nil {
			t.Fatal(err)
		}
		mustStart(t, out)
		if h, ok := f.holder("emulator-5556"); ok {
			t.Errorf("a CLI boot onto %s kept the old hold of %+v on a new emulator", "emulator-5556", h)
		}
	})

	t.Run("an adopted emulator is held only when free", func(t *testing.T) {
		scaleBoot(t, 10*time.Second, 10*time.Millisecond)
		f := newOccEmu(t, map[string]string{
			"devices.out":                 listOf(emuLine),
			serial + ".avd":               loadedAVD + "\r\nOK\r\n",
			serial + ".bootcompleted.out": "1\n",
		})
		f.launcher(t, loadedAVD+"\n")
		f.connect("a", "agent-a")
		f.connect("b", "agent-b")

		r := mustStart(t, f.as(t, "a", "emulator.start", `{"avd":"`+loadedAVD+`"}`))
		if !r.Adopted {
			t.Fatalf("result = %+v; want an adoption", r)
		}
		if h, ok := f.holder(serial); !ok || h.Name != "agent-a" {
			t.Fatalf("a free adopted emulator is held by %+v, %v; want agent-a", h, ok)
		}
		// B adopts the same emulator: A keeps it, and B is not refused.
		r = mustStart(t, f.as(t, "b", "emulator.start", `{"avd":"`+loadedAVD+`"}`))
		if !r.Adopted {
			t.Fatalf("result = %+v; want an adoption", r)
		}
		if h, _ := f.holder(serial); h.Name != "agent-a" {
			t.Errorf("an adoption took the emulator from its holder: now %+v", h)
		}
	})
}

// refusedOccupied is the error of an outcome that failed DEVICE_OCCUPIED; any other outcome
// is reported without ending the test, so the assertions about the fake's log still run.
func refusedOccupied(t *testing.T, out capability.Outcome) *capability.Error {
	t.Helper()
	if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != device.ErrOccupied {
		t.Errorf("state %s, error %+v; want failed with %s", out.State, out.Error, device.ErrOccupied)
		return nil
	}
	return out.Error
}
