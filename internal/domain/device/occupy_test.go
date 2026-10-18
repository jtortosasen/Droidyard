package device

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// Device occupation at the device capabilities (ADR-0094 §2 to §6), over the fake adb and a
// registry of real sessions: which calls hold, which are refused, and that a refusal comes
// before any adb command that changes the device (the fake's log).

const (
	otherSerial = "emulator-5556"
	// otherLine lists a second online emulator.
	otherLine = otherSerial + " device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:2\n"
)

// occFix is the device capabilities over the fake adb listing two emulators, with the
// registry of sessions wired as the daemon wires it.
type occFix struct {
	dir      string
	adb      *ADB
	ops      *capability.Operations
	sessions *capability.Sessions
	fake     *fakeScreen
}

func newOccFix(t *testing.T) *occFix {
	t.Helper()
	quickLaunch(t)
	base := map[string]string{
		"devices.out": uiList(phoneLine, otherLine),
	}
	for _, s := range []string{uiSerial, otherSerial} {
		base[s+".bootcompleted.out"] = "1\n"
		base[s+".dumptty.out"] = fixture(t, "pixel9a-uidump-devtty.xml")
		base[s+".push.exit"] = "1"
		base[s+".date.out"] = "1696118400.0\n"
		base[s+".monkey.out"] = ""
		base[s+".install.out"] = "Performing Streamed Install\nSuccess\n"
	}
	dir := t.TempDir()
	for name, content := range base {
		devicetest.Write(t, dir, name, []byte(content))
	}
	adb := newFake(t, dir, closedPort(t))
	reg := capability.NewRegistry()
	res := &stubResolver{out: build.ResolvedOutput{ApplicationID: "com.example.app", APK: "app.apk", Path: "/a/app.apk"}}
	Register(reg, adb, testStore(t), res)
	ops := capability.NewOperations(reg)
	ops.UseWorkspaces(admitInstall{})
	ops.RegisterCapabilities()
	adb.SetOccupation(ops.Sessions())
	f := &occFix{dir: dir, adb: adb, ops: ops, sessions: ops.Sessions(), fake: &fakeScreen{}}
	adb.screenLookup = func(serial string) screen.Source {
		if serial == uiSerial {
			return f.fake
		}
		return nil
	}
	return f
}

// connect registers an MCP session of its own chat, named name, bound at the main tree.
func (f *occFix) connect(id, name string) {
	f.sessions.Connect(id, "chat-"+id)
	f.sessions.Named(id, name)
	f.sessions.Opened(id, "/main", "ws-main", "/main")
}

// who is the caller of a request.
type who struct {
	client capability.ClientKind
	id     string
	joined string
}

func mcp(id string) who { return who{client: capability.ClientMCP, id: id} }

var (
	uiCaller  = who{client: capability.ClientUI, id: "ui-1"}
	cliCaller = who{client: capability.ClientCLI, id: "cli-1"}
)

// as runs a capability as w and waits for its outcome.
func (f *occFix) as(t *testing.T, w who, name, params string) capability.Outcome {
	t.Helper()
	ws := ""
	if name == "device.install" {
		ws = installWS
	}
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: name, Workspace: ws, Params: []byte(params), Client: w.client,
		Origin: capability.Origin{ID: w.id, Client: w.client, Joined: w.joined},
	})
	if ce := capability.AsError(err); ce != nil {
		return capability.Outcome{State: capability.StateFailed, Error: ce}
	} else if err != nil {
		t.Fatalf("Start %s: %v", name, err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *occFix) calls(t *testing.T) []string { return devicetest.Calls(t, f.dir) }

// deviceCalls counts the fake's invocations that name a device (-s <serial> ...): the ones
// that read or change it. `devices -l` and `version` are the preamble's.
func (f *occFix) deviceCalls(t *testing.T) int {
	n := 0
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, "-s ") {
			n++
		}
	}
	return n
}

// installs counts the fake's `install` invocations.
func (f *occFix) installs(t *testing.T) int {
	n := 0
	for _, c := range f.calls(t) {
		if strings.Contains(c, " install ") {
			n++
		}
	}
	return n
}

func (f *occFix) holder(serial string) (capability.Holder, bool) {
	return f.sessions.DeviceHolder(serial, "")
}

const (
	installParams   = `{"device":"emulator-5554","build":{"module":":app","variant":"debug"}}`
	uninstallParams = `{"device":"emulator-5554","applicationId":"com.example.app"}`
	launchParams    = `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp"}`
	tapParams       = `{"device":"emulator-5554","target":{"id":"like"}}`
	inputParams     = `{"device":"emulator-5554","text":"hi"}`
)

func mustOK(t *testing.T, what string, out capability.Outcome) {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("%s: %s, %+v", what, out.State, out.Error)
	}
}

// TestDeviceOccupation holds ADR-0094 §2 to §6 for the device capabilities: an MCP
// session's acting call holds a free device, another session's acting call on it fails
// DEVICE_OCCUPIED naming the holder before any adb command changes the device, and reads,
// the UI and the CLI (a joined one included) neither hold nor are refused.
func TestDeviceOccupation(t *testing.T) {
	t.Run("a launch holds, another session's acting calls are refused before adb", func(t *testing.T) {
		f := newOccFix(t)
		f.connect("a", "agent-a")
		f.connect("b", "agent-b")

		mustOK(t, "A's launch", f.as(t, mcp("a"), "device.launch", launchParams))
		if h, ok := f.holder(uiSerial); !ok || h.Name != "agent-a" || h.Since.IsZero() {
			t.Fatalf("after A's launch the holder is %+v, %v; want agent-a with a time", h, ok)
		}
		// device.list shows the holder to B, the UI and the CLI, and not to A itself.
		for name, w := range map[string]who{"B": mcp("b"), "UI": uiCaller, "CLI": cliCaller} {
			out := f.as(t, w, "device.list", `{}`)
			mustOK(t, "device.list as "+name, out)
			if row := rowOf(out, uiSerial); row.OccupiedBy == nil || row.OccupiedBy.Name != "agent-a" || row.OccupiedBy.Since.IsZero() {
				t.Errorf("device.list as %s: occupiedBy on %s = %+v; want agent-a", name, uiSerial, row.OccupiedBy)
			}
			if row := rowOf(out, otherSerial); row.OccupiedBy != nil {
				t.Errorf("device.list as %s: the free %s says it is occupied by %+v", name, otherSerial, row.OccupiedBy)
			}
		}
		if row := rowOf(f.as(t, mcp("a"), "device.list", `{}`), uiSerial); row.OccupiedBy != nil {
			t.Errorf("the holder is told it is occupied by %+v", row.OccupiedBy)
		}

		before := f.deviceCalls(t)
		installsBefore := f.installs(t)
		for _, c := range []struct{ name, params string }{
			{"device.install", installParams},
			{"device.uninstall", uninstallParams},
			{"device.launch", launchParams},
			{"device.tapElement", tapParams},
			{"device.inputText", inputParams},
		} {
			out := f.as(t, mcp("b"), c.name, c.params)
			if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != ErrOccupied {
				t.Errorf("%s as B: %s, %+v; want failed with %s", c.name, out.State, out.Error, ErrOccupied)
				continue
			}
			e := out.Error
			if !strings.Contains(e.Message, "agent-a") || !strings.Contains(e.Message, uiSerial) {
				t.Errorf("%s: message %q does not name the holder and the device", c.name, e.Message)
			}
			if !strings.Contains(e.NextStep, "emulator_start") || !strings.Contains(e.NextStep, "devices panel") || !strings.Contains(e.NextStep, uiSerial) {
				t.Errorf("%s: nextStep %q does not give the way out", c.name, e.NextStep)
			}
		}
		// Decision: the check runs before any adb command that changes the device. The
		// fake saw only the preamble's listing, no `-s <serial>` command, and no install.
		if after := f.deviceCalls(t); after != before {
			t.Errorf("B's refused calls ran %d device commands: %v", after-before, f.calls(t))
		}
		if n := f.installs(t); n != installsBefore {
			t.Errorf("B's refused calls ran %d installs", n-installsBefore)
		}
		if _, ok := f.sessions.DeviceHolder(uiSerial, "a"); ok {
			t.Error("the holder is told it is occupied")
		}
		if h, _ := f.holder(uiSerial); h.Name != "agent-a" {
			t.Errorf("B's refused calls moved the hold to %+v", h)
		}

		// Reads are never refused and never hold.
		mustOK(t, "B's uiTree", f.as(t, mcp("b"), "device.uiTree", `{"device":"emulator-5554"}`))
		if h, _ := f.holder(uiSerial); h.Name != "agent-a" {
			t.Errorf("B's read moved the hold to %+v", h)
		}
		mustOK(t, "B's uiTree on a free serial", f.as(t, mcp("b"), "device.uiTree", `{"device":"emulator-5556"}`))
		if _, ok := f.holder(otherSerial); ok {
			t.Error("a read held the free serial")
		}
	})

	t.Run("the UI and the CLI are never refused and hold nothing", func(t *testing.T) {
		f := newOccFix(t)
		f.connect("a", "agent-a")
		f.connect("b", "agent-b")
		mustOK(t, "A's launch", f.as(t, mcp("a"), "device.launch", launchParams))

		// The UI's live-view tap, on the held serial.
		mustOK(t, "UI device.tap", f.as(t, uiCaller, "device.tap", `{"device":"emulator-5554","x":0.5,"y":0.5}`))
		if got := f.fake.got(); len(got) != 1 {
			t.Errorf("the UI's tap injected %d inputs; want 1", len(got))
		}
		// The CLI's acting calls, on the held serial and on a free one.
		before := f.installs(t)
		mustOK(t, "CLI install on a held serial", f.as(t, cliCaller, "device.install", installParams))
		mustOK(t, "CLI launch on a free serial", f.as(t, cliCaller, "device.launch", strings.ReplaceAll(launchParams, uiSerial, otherSerial)))
		if f.installs(t) != before+1 {
			t.Error("the CLI's install did not reach the device")
		}
		if _, ok := f.holder(otherSerial); ok {
			t.Error("the CLI's launch held the free serial")
		}

		// Only the connection that is the MCP session acts for it: a call of another kind that
		// carries a session's id (the daemon's ids are not secret from a same-uid process, but
		// they grant nothing, ADR-0048 §2) is refused nothing and holds nothing.
		impostor := who{client: capability.ClientCLI, id: "b"}
		mustOK(t, "a CLI call carrying B's id, on a held serial", f.as(t, impostor, "device.install", installParams))
		mustOK(t, "a CLI call carrying B's id, on a free serial", f.as(t, impostor, "device.launch", strings.ReplaceAll(launchParams, uiSerial, otherSerial)))
		if h, ok := f.holder(otherSerial); ok {
			t.Errorf("a CLI call carrying B's id held %s for %+v", otherSerial, h)
		}

		// A CLI call joined to B by its chat is the CLI (ADR-0095 §1; ADR-0094 §5): not
		// refused on A's serial, and it holds nothing for B on a free one.
		joined := who{client: capability.ClientCLI, id: "cli-2", joined: "b"}
		mustOK(t, "joined CLI launch on a free serial", f.as(t, joined, "device.launch", strings.ReplaceAll(launchParams, uiSerial, otherSerial)))
		if h, ok := f.holder(otherSerial); ok {
			t.Errorf("a CLI call joined to B held %s for %+v", otherSerial, h)
		}
		mustOK(t, "joined CLI install on a held serial", f.as(t, joined, "device.install", installParams))
		if h, _ := f.holder(uiSerial); h.Name != "agent-a" {
			t.Errorf("a joined CLI call moved the hold to %+v", h)
		}
	})

	t.Run("entering the main tree releases, the end releases, a vanished serial releases", func(t *testing.T) {
		f := newOccFix(t)
		f.connect("a", "agent-a")
		f.connect("b", "agent-b")
		mustOK(t, "A's launch", f.as(t, mcp("a"), "device.launch", launchParams))
		mustFail(t, f.as(t, mcp("b"), "device.install", installParams), ErrOccupied)

		// A's "task done": it enters the main tree it is already at.
		f.sessions.Occupy("a", "ws-main", "/main", false)
		if _, ok := f.holder(uiSerial); ok {
			t.Fatal("A entering the main tree kept the device")
		}
		mustOK(t, "B's install", f.as(t, mcp("b"), "device.install", installParams))
		if h, ok := f.holder(uiSerial); !ok || h.Name != "agent-b" {
			t.Fatalf("after B's install the holder is %+v, %v; want agent-b", h, ok)
		}

		// B ends.
		f.sessions.End("b")
		if _, ok := f.holder(uiSerial); ok {
			t.Error("the ended session still holds the device")
		}

		// A holds the second serial; it then leaves adb devices, and the next preamble sees
		// it gone.
		mustOK(t, "A's launch", f.as(t, mcp("a"), "device.launch", strings.ReplaceAll(launchParams, uiSerial, otherSerial)))
		if _, ok := f.holder(otherSerial); !ok {
			t.Fatal("A did not hold the second serial")
		}
		devicetest.Write(t, f.dir, "devices.out", []byte(uiList(phoneLine)))
		mustOK(t, "any preamble", f.as(t, uiCaller, "device.uiTree", `{"device":"emulator-5554"}`))
		if h, ok := f.holder(otherSerial); ok {
			t.Errorf("a serial gone from the listing is still held by %+v", h)
		}
	})

	t.Run("an idle holder is taken over and allow lets the other proceed", func(t *testing.T) {
		f := newOccFix(t)
		clk := &occClock{t: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
		f.sessions.UseClock(clk.now)
		conflict := capability.OccupationRefuse
		f.sessions.UseOccupation(func() (string, time.Duration) { return conflict, time.Minute })
		f.connect("a", "agent-a")
		f.connect("b", "agent-b")
		mustOK(t, "A's launch", f.as(t, mcp("a"), "device.launch", launchParams))

		// allow: B's call goes on and A stays the holder.
		conflict = capability.OccupationAllow
		installs := f.installs(t)
		mustOK(t, "B's install under allow", f.as(t, mcp("b"), "device.install", installParams))
		if f.installs(t) != installs+1 {
			t.Error("allow did not let B's install through")
		}
		if h, _ := f.holder(uiSerial); h.Name != "agent-a" {
			t.Errorf("allow moved the hold to %+v; want A to stay", h)
		}

		// ask is refuse for a device (ADR-0094 §6).
		conflict = capability.OccupationAsk
		mustFail(t, f.as(t, mcp("b"), "device.install", installParams), ErrOccupied)

		// Idle: A makes no request for longer than the threshold; B takes the device over.
		conflict = capability.OccupationRefuse
		clk.advance(5 * time.Minute)
		f.sessions.Sweep()
		f.sessions.Seen("b") // B's own request ended its idleness; A's did not
		if h, _ := f.sessions.DeviceHolder(uiSerial, "b"); !h.Stale {
			t.Fatalf("the idle holder is reported %+v; want stale", h)
		}
		mustOK(t, "B's install over an idle holder", f.as(t, mcp("b"), "device.install", installParams))
		if h, _ := f.holder(uiSerial); h.Name != "agent-b" {
			t.Errorf("after the takeover the holder is %+v; want agent-b", h)
		}
	})

	// Decision (ADR-0094 §1): two sessions acting on one free serial at once, one holds and
	// one is refused. Run under -race.
	t.Run("two sessions acting at once, one holds", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			f := newOccFix(t)
			f.connect("a", "agent-a")
			f.connect("b", "agent-b")
			var wg sync.WaitGroup
			outs := make([]capability.Outcome, 2)
			start := make(chan struct{})
			for i, id := range []string{"a", "b"} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					outs[i] = f.as(t, mcp(id), "device.launch", launchParams)
				}()
			}
			close(start)
			wg.Wait()
			ok, refused := 0, 0
			for _, o := range outs {
				switch {
				case o.State == capability.StateCompleted:
					ok++
				case o.Error != nil && o.Error.Code == ErrOccupied:
					refused++
				default:
					t.Fatalf("run %d: %s, %+v", i, o.State, o.Error)
				}
			}
			if ok != 1 || refused != 1 {
				t.Fatalf("run %d: %d calls went on and %d were refused; want one each", i, ok, refused)
			}
		}
	})
}

// occClock is a clock a test moves.
type occClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *occClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *occClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// rowOf is the row of serial in a device.list outcome.
func rowOf(out capability.Outcome, serial string) Device {
	if r, ok := out.Result.(ListResult); ok {
		for _, d := range r.Devices {
			if d.Device == serial {
				return d
			}
		}
	}
	return Device{}
}
