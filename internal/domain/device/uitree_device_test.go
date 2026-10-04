package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/process"
)

var updateGolden = flag.Bool("update", false, "rewrite the uiTree golden files under testdata")

const (
	uiSerial   = "emulator-5554"
	phoneLine  = "emulator-5554 device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"
	tabletLine = "emulator-5556 device product:sdk_gtablet_arm64 model:Pixel_Tablet transport_id:2\n"
	listHead   = "List of devices attached\n"
	ttyCall    = "-s emulator-5554 exec-out uiautomator dump /dev/tty"
	sdDumpCall = "-s emulator-5554 exec-out uiautomator dump /sdcard/window_dump.xml"
	sdCatCall  = "-s emulator-5554 exec-out cat /sdcard/window_dump.xml"
	sdRmCall   = "-s emulator-5554 shell rm -f /sdcard/window_dump.xml"
	idleLine   = "ERROR: could not get idle state.\n"
)

// uiFixture is a daemon-less device capability over the fake adb: one online emulator
// whose /dev/tty dump is the recorded Pixel_9a document, unless files say otherwise.
type uiFixture struct {
	dir string
	adb *ADB
	ops *capability.Operations
	// store is the artifact store the fixture's device.screenshot writes to.
	store *artifact.Store
}

func newUIFixture(t *testing.T, files map[string]string) *uiFixture {
	t.Helper()
	return newUIFixturePort(t, files, closedPort(t))
}

// newUIFixturePort is newUIFixture with the adb server port the clients are given.
func newUIFixturePort(t *testing.T, files map[string]string, port string) *uiFixture {
	t.Helper()
	base := map[string]string{
		"devices.out":                   uiList(phoneLine),
		uiSerial + ".bootcompleted.out": "1\n",
		uiSerial + ".dumptty.out":       fixture(t, "pixel9a-uidump-devtty.xml"),
		// The reader cannot run unless a test says it can (withReader): the tests that
		// predate ADR-0029 exercise the uiautomator dump path, now the fallback.
		uiSerial + ".push.exit": "1",
	}
	for k, v := range files {
		base[k] = v
	}
	dir := t.TempDir()
	for name, content := range base {
		devicetest.Write(t, dir, name, []byte(content))
	}
	sup := process.New(process.Options{})
	t.Cleanup(sup.Shutdown)
	adb := NewADB(devicetest.Env(t, dir, port), sup.Spawn)
	reg := capability.NewRegistry()
	store := testStore(t)
	Register(reg, adb, store)
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	return &uiFixture{dir: dir, adb: adb, ops: ops, store: store}
}

// withReader makes the fake's UI reader print doc, merged into files.
func withReader(files map[string]string, doc string) map[string]string {
	out := map[string]string{uiSerial + ".reader.out": doc, uiSerial + ".push.exit": "0"}
	for k, v := range files {
		out[k] = v
	}
	return out
}

func uiList(lines ...string) string { return listHead + strings.Join(lines, "") }

func (f *uiFixture) start(t *testing.T, device string) (string, error) {
	t.Helper()
	return f.ops.Start(context.Background(), capability.Request{Capability: "device.uiTree", Params: []byte(`{"device":"` + device + `"}`)})
}

func (f *uiFixture) wait(t *testing.T, id string) capability.Outcome {
	t.Helper()
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *uiFixture) tree(t *testing.T, device string) capability.Outcome {
	t.Helper()
	id, err := f.start(t, device)
	if err != nil {
		t.Fatal(err)
	}
	return f.wait(t, id)
}

func (f *uiFixture) calls(t *testing.T) []string { return devicetest.Calls(t, f.dir) }

func count(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

func mustComplete(t *testing.T, out capability.Outcome) Snapshot {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	s, ok := out.Result.(Snapshot)
	if !ok {
		t.Fatalf("result is %T, want Snapshot", out.Result)
	}
	return s
}

func mustFail(t *testing.T, out capability.Outcome, code capability.ErrorCode) *capability.Error {
	t.Helper()
	if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != code {
		t.Fatalf("operation %s, error %+v; want failed with %s", out.State, out.Error, code)
	}
	return out.Error
}

// Decision (check a): the whole path, fake adb to published result, gives for each
// recorded document the snapshot committed beside it. The goldens were read by hand
// once (their node counts, foreground, display and a few refs against the XML); a
// change to them is a change to what agents see, so -update is deliberate.
func TestUITreeGoldenFromRecordedDumps(t *testing.T) {
	for _, img := range uiImages {
		doc := fixture(t, img+"-uidump-devtty.xml")
		// Both paths give the one snapshot: the reader (no warning) and the dump
		// fallback (the fallback's warning only).
		for _, viaReader := range []bool{true, false} {
			files := map[string]string{uiSerial + ".dumptty.out": doc}
			if viaReader {
				files = withReader(files, doc)
			}
			f := newUIFixture(t, files)
			s := mustComplete(t, f.tree(t, uiSerial))
			if !strings.HasPrefix(s.ID, "s-") {
				t.Errorf("%s: snapshot id %q", img, s.ID)
			}
			if viaReader != (len(s.Warnings) == 0) {
				t.Errorf("%s reader=%v: warnings %q", img, viaReader, s.Warnings)
			}
			s.ID, s.Warnings = "s-golden", nil
			got, _ := json.MarshalIndent(s, "", "  ")
			got = append(got, '\n')
			path := filepath.Join("testdata", img+"-uitree.golden.json")
			if *updateGolden && viaReader {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: %v (run with -update to create it)", img, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s reader=%v: snapshot differs from %s", img, viaReader, path)
			}
			refsExist(t, s)
		}
	}
}

// Decision (check d, retry): one "could not get idle state" is retried once and the
// caller gets one result; the fake received exactly two dumps, and the /sdcard path
// never ran (the retry is of the same path).
func TestUITreeRetriesIdleFailureOnce(t *testing.T) {
	f := newUIFixture(t, map[string]string{
		uiSerial + ".dumptty#1.out":  idleLine,
		uiSerial + ".dumptty#1.exit": "1",
	})
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) == 0 {
		t.Error("no nodes after the retry")
	}
	if n := count(f.calls(t), ttyCall); n != 2 {
		t.Errorf("%d /dev/tty dumps, want 2 (the failure and its retry)", n)
	}
	if n := count(f.calls(t), sdDumpCall); n != 0 {
		t.Errorf("%d /sdcard dumps after an idle failure, want none", n)
	}
}

// Decision (check d, always failing): after the one retry the call is
// DEVICE_UI_DUMP_FAILED with adb's line as the cause, within the 22 s dump deadline,
// and never a third dump.
func TestUITreeIdleFailureTwiceIsDumpFailed(t *testing.T) {
	f := newUIFixture(t, map[string]string{
		uiSerial + ".dumptty.out":  idleLine,
		uiSerial + ".dumptty.exit": "1",
	})
	start := time.Now()
	ce := mustFail(t, f.tree(t, uiSerial), ErrUIDumpFailed)
	if d := time.Since(start); d > 22*time.Second {
		t.Errorf("took %v, over the 22 s dump deadline", d)
	}
	if !strings.Contains(ce.Cause, "could not get idle state") {
		t.Errorf("cause %q does not carry the line", ce.Cause)
	}
	if n := count(f.calls(t), ttyCall); n != 2 {
		t.Errorf("%d dumps, want 2", n)
	}
}

// Decision (check d, fallback): /dev/tty output without a <hierarchy runs the /sdcard
// path (dump, read) and removes the file; the result is the same snapshot. The
// removal is issued after the read, and also when the read fails.
func TestUITreeFallsBackToSDCardAndRemovesTheFile(t *testing.T) {
	f := newUIFixture(t, map[string]string{
		uiSerial + ".dumptty.out":    "something that is not a hierarchy\n",
		uiSerial + ".dumpsdcard.out": fixture(t, "pixel9a-uidump-sdcard-out.txt"),
		uiSerial + ".catsdcard.out":  fixture(t, "pixel9a-uidump-sdcard.xml"),
	})
	s := mustComplete(t, f.tree(t, uiSerial))
	if s.Foreground != "com.android.settings" || len(s.Nodes) == 0 {
		t.Errorf("snapshot %+v", s)
	}
	calls := f.calls(t)
	i, j, k := slices.Index(calls, sdDumpCall), slices.Index(calls, sdCatCall), slices.Index(calls, sdRmCall)
	if i < 0 || !(i < j && j < k) {
		t.Errorf("calls %q: want the /sdcard dump, its read, then its removal", calls)
	}

	f = newUIFixture(t, map[string]string{
		uiSerial + ".dumptty.out":    "nope\n",
		uiSerial + ".dumpsdcard.out": "UI hierchary dumped to: /sdcard/window_dump.xml\n",
		uiSerial + ".catsdcard.out":  "cat: no such file\n",
		uiSerial + ".catsdcard.exit": "1",
	})
	mustFail(t, f.tree(t, uiSerial), ErrUIDumpFailed)
	if count(f.calls(t), sdRmCall) != 1 {
		t.Errorf("calls %q: the file is removed even when reading it failed", f.calls(t))
	}
}

// Decision (check d, deadline): every attempt, the fallback and its cleanup are inside
// one deadline: with a /dev/tty that yields no document, a hanging /sdcard dump and the budget injected short, the call fails
// DEVICE_UI_DUMP_FAILED inside it (the attempt limit is longer than the deadline, so only
// the deadline can cut it), with the removal issued.
func TestUITreeDeadlineBoundsAllAttempts(t *testing.T) {
	old1, old2, old3 := dumpDeadline, dumpAttempt, cleanupReserve
	dumpDeadline, dumpAttempt, cleanupReserve = 2*time.Second, 8*time.Second, 500*time.Millisecond
	defer func() { dumpDeadline, dumpAttempt, cleanupReserve = old1, old2, old3 }()
	f := newUIFixture(t, map[string]string{
		uiSerial + ".dumptty.out":     "not a hierarchy\n",
		uiSerial + ".dumpsdcard.hang": "",
	})
	// The dump itself, without the preamble's commands (slow to start under -race).
	start := time.Now()
	_, err := f.adb.dump(context.Background(), &target{adb: f.adb.Resolve().Path, serial: uiSerial})
	mustFail(t, capability.Outcome{State: capability.StateFailed, Error: capability.AsError(err)}, ErrUIDumpFailed)
	if d := time.Since(start); d > dumpDeadline+time.Second {
		t.Errorf("took %v with a %v dump deadline", d, dumpDeadline)
	}
	if count(f.calls(t), sdRmCall) != 1 {
		t.Errorf("calls %q: want the removal issued after the hung /sdcard dump", f.calls(t))
	}
}

// maxOverlap is the most invocations of one prefix that were in flight at once, from
// events.log.
func maxOverlap(events []string, prefix string) int {
	n, m := 0, 0
	for _, e := range events {
		switch e {
		case "begin " + prefix:
			n++
			m = max(m, n)
		case "end " + prefix:
			n--
		}
	}
	return m
}

// Decision (check e; the delay is long because under -race each fake adb call takes seconds): the daemon never issues two dumps to one device at once. Two
// calls on one device: the second is queued behind the first (the lock is the
// scheduler's, taken in Start) and the fake sees its dumps one after the other; on two
// devices the dumps overlap. Without the dump key declared the first half fails.
func TestUITreeDumpsAreSerialisedPerDevice(t *testing.T) {
	two := withReader(map[string]string{
		"devices.out":                     uiList(phoneLine, tabletLine),
		uiSerial + ".reader.delay":        "2500",
		"emulator-5556.bootcompleted.out": "1\n",
		"emulator-5556.reader.out":        fixture(t, "pixeltablet-uidump-devtty.xml"),
		"emulator-5556.reader.delay":      "2500",
	}, fixture(t, "pixel9a-uidump-devtty.xml"))
	f := newUIFixture(t, two)
	a, err := f.start(t, uiSerial)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.start(t, uiSerial)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := f.ops.Wait(context.Background(), b, time.Now().Add(-time.Second))
	if second.State != capability.StateQueued || second.BlockedBy == nil || second.BlockedBy.OperationID != a {
		t.Errorf("second call is %s blockedBy %+v, want queued behind %s", second.State, second.BlockedBy, a)
	}
	mustComplete(t, f.wait(t, a))
	mustComplete(t, f.wait(t, b))
	if got := maxOverlap(devicetest.Events(t, f.dir), uiSerial+".reader"); got != 1 {
		t.Errorf("%d dumps to one device overlapped, want 1: %q", got, devicetest.Events(t, f.dir))
	}

	f = newUIFixture(t, two)
	a, _ = f.start(t, uiSerial)
	b, _ = f.start(t, "emulator-5556")
	mustComplete(t, f.wait(t, a))
	mustComplete(t, f.wait(t, b))
	var ev []string
	for _, e := range devicetest.Events(t, f.dir) {
		if strings.HasSuffix(e, ".reader") {
			ev = append(ev, e)
		}
	}
	first := slices.IndexFunc(ev, func(e string) bool { return strings.HasPrefix(e, "end ") })
	if first < 0 || !slices.Contains(ev[:first], "begin "+uiSerial+".reader") || !slices.Contains(ev[:first], "begin emulator-5556.reader") {
		t.Errorf("events %q: dumps to two devices should be in flight together", ev)
	}
}

// Decision (check f, Q2): the preamble. Every outcome carries its own code, the
// operation exists (the denial is decided at the re-check), and a call makes one
// `devices -l`.
func TestUITreePreamble(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		device string
		code   capability.ErrorCode
		check  func(t *testing.T, ce *capability.Error, calls []string)
	}{
		{"unknown serial", nil, "emulator-9999", ErrNotFound, func(t *testing.T, ce *capability.Error, _ []string) {
			if online, _ := ce.Details["online"].([]string); !slices.Equal(online, []string{uiSerial}) {
				t.Errorf("details %+v, want online [%s]", ce.Details, uiSerial)
			}
		}},
		{"no emulator at all", map[string]string{"devices.out": listHead}, uiSerial, ErrNotFound, func(t *testing.T, ce *capability.Error, _ []string) {
			if online, ok := ce.Details["online"].([]string); !ok || len(online) != 0 {
				t.Errorf("details %+v, want an empty online list", ce.Details)
			}
		}},
		{"unauthorized", map[string]string{"devices.out": uiList("emulator-5554 unauthorized transport_id:1\n")}, uiSerial, ErrNotReady, func(t *testing.T, ce *capability.Error, _ []string) {
			if !strings.Contains(ce.Message, "unauthorized") || ce.Details["state"] != "unauthorized" {
				t.Errorf("error %+v does not name the state", ce)
			}
		}},
		{"physical", map[string]string{"devices.out": uiList(phoneLine, "R58M12ABCDE device usb:1-1.2 product:raven model:Pixel_6_Pro transport_id:3\n")}, "R58M12ABCDE", ErrNotAllowed, func(t *testing.T, _ *capability.Error, calls []string) {
			for _, c := range calls {
				if strings.HasPrefix(c, "-s ") {
					t.Errorf("a command was sent to the denied device: %q", calls)
				}
			}
		}},
		{"still booting", map[string]string{uiSerial + ".bootcompleted.out": "0\n"}, uiSerial, ErrNotReady, func(t *testing.T, ce *capability.Error, calls []string) {
			if !strings.Contains(ce.Message, "still booting") {
				t.Errorf("message %q", ce.Message)
			}
			if count(calls, ttyCall) != 0 {
				t.Error("dumped a device that is still booting")
			}
		}},
		{"boot property empty", map[string]string{uiSerial + ".bootcompleted.out": ""}, uiSerial, ErrNotReady, nil},
		{"disconnected in the dump", map[string]string{uiSerial + ".dumptty.out": "", uiSerial + ".dumptty.err": "adb: device 'emulator-5554' not found\n", uiSerial + ".dumptty.exit": "1"}, uiSerial, ErrDisconnected, func(t *testing.T, ce *capability.Error, calls []string) {
			if !strings.Contains(ce.Cause, "device 'emulator-5554' not found") {
				t.Errorf("cause %q is not the command's own stderr", ce.Cause)
			}
			if n := count(calls, "devices -l"); n != 1 {
				t.Errorf("%d listings, want the one (no extra listing to learn the device is gone)", n)
			}
			if count(calls, sdDumpCall) != 0 {
				t.Error("fell back to /sdcard on a device that is gone")
			}
		}},
		{"offline in the dump", map[string]string{uiSerial + ".dumptty.out": "", uiSerial + ".dumptty.err": "error: device offline\n", uiSerial + ".dumptty.exit": "1"}, uiSerial, ErrDisconnected, nil},
		{"adb fails on the host", map[string]string{"devices.exit": "1", "devices.err": "error: cannot connect to daemon\n"}, uiSerial, ErrADBFailed, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newUIFixture(t, c.files)
			id, err := f.start(t, c.device)
			if err != nil {
				t.Fatalf("Start: %v (the denial is decided with an operation, not at Start)", err)
			}
			ce := mustFail(t, f.wait(t, id), c.code)
			if n := count(f.calls(t), "devices -l"); n != 1 {
				t.Errorf("%d `devices -l`, want one per call", n)
			}
			if c.check != nil {
				c.check(t, ce, f.calls(t))
			}
		})
	}
}

// Decision (check d, document wins): node text that reads like an adb error is not an
// adb error. A valid document that holds "device offline" is a result.
func TestUITreeDocumentTextIsNeverReadAsAnError(t *testing.T) {
	doc := string(synthDump(0, synthNode("android.widget.TextView", `text="device 'emulator-5554' not found; device offline; could not get idle state"`, "")))
	f := newUIFixture(t, map[string]string{uiSerial + ".dumptty.out": doc})
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Nodes) != 1 {
		t.Errorf("nodes %+v", s.Nodes)
	}
}

// Decision (A5): `device` is required and a call without it is rejected at Start, so
// no operation exists; and without adb the call is DEVICE_ADB_NOT_FOUND.
func TestUITreeRequiresDeviceAndAdb(t *testing.T) {
	f := newUIFixture(t, nil)
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.uiTree", Params: []byte(`{}`)})
	if ce := capability.AsError(err); err == nil || id != "" || ce == nil || ce.Code != capability.ErrInvalidParams {
		t.Errorf("Start without device = %q, %v; want a rejection with %s", id, err, capability.ErrInvalidParams)
	}
	if n := len(f.calls(t)); n != 0 {
		t.Errorf("adb ran %d times for a rejected call", n)
	}

	sup := process.New(process.Options{})
	t.Cleanup(sup.Shutdown)
	reg := capability.NewRegistry()
	Register(reg, NewADB([]string{"HOME=" + t.TempDir(), "PATH="}, sup.Spawn), testStore(t))
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	id, err = ops.Start(context.Background(), capability.Request{Capability: "device.uiTree", Params: []byte(`{"device":"emulator-5554"}`)})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := ops.Wait(context.Background(), id, time.Now().Add(10*time.Second))
	mustFail(t, out, ErrADBNotFound)
}

// Decision: the published contract of device.uiTree: since 0.4.0, needs adb, its
// errors are every code the handler or preamble returns plus the queue bound the
// scheduler may return, and its result is untrusted (derived from the tags) while
// device.list's is not.
func TestUITreeDescriptorAndMark(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, NewADB(nil, nil), testStore(t))
	var d capability.Descriptor
	for _, x := range reg.All() {
		if x.Name == "device.uiTree" {
			d = x
		}
	}
	if d.Name == "" {
		t.Fatal("device.uiTree is not registered")
	}
	var codes []string
	for _, c := range d.Errors {
		codes = append(codes, string(c))
	}
	slices.Sort(codes)
	want := []string{"DEVICE_ADB_FAILED", "DEVICE_ADB_NOT_FOUND", "DEVICE_DISCONNECTED", "DEVICE_NOT_ALLOWED", "DEVICE_NOT_FOUND", "DEVICE_NOT_READY", "DEVICE_UI_DUMP_FAILED", "OPERATION_QUEUE_TIMEOUT"}
	if !slices.Equal(codes, want) {
		t.Errorf("errors = %v, want %v", codes, want)
	}
	if d.Since != "0.4.0" || len(d.Availability) != 1 || d.Availability[0] != capability.RequiresADB || d.Resource == nil || d.Authorize == nil || d.LongRunning {
		t.Errorf("descriptor = %+v", d)
	}
	if k := d.Resource(UITreeParams{Device: "emulator-5554"}); k == "" || k == d.Resource(UITreeParams{Device: "emulator-5556"}) {
		t.Errorf("dump key %q is not one per device", k)
	}

	f := newUIFixture(t, nil)
	if out := f.tree(t, uiSerial); !out.Untrusted {
		t.Error("a uiTree result is not marked untrusted")
	}
	id, _ := f.ops.Start(context.Background(), capability.Request{Capability: "device.list"})
	if out := f.wait(t, id); out.State != capability.StateCompleted || out.Untrusted {
		t.Errorf("device.list is %s, untrusted=%v; want completed and not untrusted", out.State, out.Untrusted)
	}
}

// Decision (B1): adb's client/server version-mismatch warning is on the device call,
// as device.list carries it (adb-device.md §Failure modes). The fake client is 41; the
// loopback server says 42. The warning is adb's, so it is not under the untrusted mark.
func TestUITreeCarriesTheVersionMismatchWarning(t *testing.T) {
	port, _ := fakeServer(t, []byte("OKAY0004002a"))
	doc := fixture(t, "pixel9a-uidump-devtty.xml")
	f := newUIFixturePort(t, withReader(nil, doc), port)
	s := mustComplete(t, f.tree(t, uiSerial))
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "version 42") || !strings.Contains(s.Warnings[0], "1.0.41") {
		t.Errorf("warnings = %q, want the mismatch between client 1.0.41 and server 42", s.Warnings)
	}
	if s := mustComplete(t, newUIFixture(t, withReader(nil, doc)).tree(t, uiSerial)); len(s.Warnings) != 0 {
		t.Errorf("warnings = %q with no server running", s.Warnings)
	}
}

// Decision (B2): the handler's own 40 KB cap. A ~5000-node tree served through the
// fake adb and run through the real handler comes out within 40960 encoded bytes,
// truncated, with the hint naming the cap; every other truncation test passes its cap.
func TestUITreeHandlerCapsAtDefault(t *testing.T) {
	var body string
	for d := 0; d < 50; d++ {
		var leaves strings.Builder
		for i := 0; i < 100; i++ {
			leaves.WriteString(synthNode("android.widget.TextView", fmt.Sprintf(`text="item %d-%d"`, d, i), ""))
		}
		body = synthNode("android.view.ViewGroup", fmt.Sprintf(`text="level %d"`, d), leaves.String()+body)
	}
	f := newUIFixture(t, map[string]string{uiSerial + ".dumptty.out": string(synthDump(0, body))})
	s := mustComplete(t, f.tree(t, uiSerial))
	enc, _ := json.Marshal(s)
	if len(enc) > 40960 {
		t.Errorf("encoded result is %d bytes, over 40960", len(enc))
	}
	if !s.Truncated || !strings.Contains(s.Hint, "40960") {
		t.Errorf("truncated=%v hint=%q, want truncated and a hint naming 40960", s.Truncated, s.Hint)
	}
	if len(s.Nodes) == 0 {
		t.Error("no nodes kept")
	}
}

// Decision (N1): an incomplete hierarchy is device text. It is never scanned for adb's
// disconnect or idle lines and never quoted: node text "device offline" in a cut
// document with exit 1 is DEVICE_UI_DUMP_FAILED naming only the size.
func TestUITreeIncompleteHierarchyIsNeverQuoted(t *testing.T) {
	cut := `<?xml version='1.0' ?><hierarchy rotation="0"><node class="a" text="device offline; could not get idle state" bounds="[0,0][1,1]">`
	f := newUIFixture(t, map[string]string{
		uiSerial + ".dumptty.out":    cut,
		uiSerial + ".dumptty.exit":   "1",
		uiSerial + ".catsdcard.out":  cut,
		uiSerial + ".catsdcard.exit": "1",
	})
	ce := mustFail(t, f.tree(t, uiSerial), ErrUIDumpFailed)
	if strings.Contains(ce.Cause, "offline") || strings.Contains(ce.Cause, "idle") {
		t.Errorf("cause %q quotes device text", ce.Cause)
	}
	if want := fmt.Sprintf("incomplete hierarchy (%d bytes)", len(cut)); !strings.Contains(ce.Cause, want) {
		t.Errorf("cause %q, want %q", ce.Cause, want)
	}
	if n := count(f.calls(t), ttyCall); n != 1 {
		t.Errorf("%d /dev/tty dumps, want 1 (no idle retry on device text)", n)
	}
}

// Decision (N2): the /sdcard file is removed even when the caller cancels during the
// read; the removal does not inherit the caller's cancellation.
func TestUITreeRemovesTheSDCardFileWhenCancelled(t *testing.T) {
	f := newUIFixture(t, map[string]string{
		uiSerial + ".dumptty.out":     "not a hierarchy\n",
		uiSerial + ".dumpsdcard.out":  "UI hierchary dumped to: /sdcard/window_dump.xml\n",
		uiSerial + ".catsdcard.delay": "5000",
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(1500 * time.Millisecond); cancel() }()
	_, err := f.adb.dump(ctx, &target{adb: f.adb.Resolve().Path, serial: uiSerial})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
	if count(f.calls(t), sdRmCall) != 1 {
		t.Errorf("calls %q: want the removal issued after the cancelled read", f.calls(t))
	}
}

// Decision (Q3): adb that cannot be started is a host failure, DEVICE_ADB_FAILED, not
// a device that is not answering.
func TestReadyStartFailureIsAdbFailed(t *testing.T) {
	sup := process.New(process.Options{})
	t.Cleanup(sup.Shutdown)
	a := NewADB([]string{"HOME=" + t.TempDir(), "PATH="}, sup.Spawn)
	err := a.ready(context.Background(), &target{adb: filepath.Join(t.TempDir(), "no-such-adb"), serial: uiSerial,
		dev: &Device{Device: uiSerial, State: StateDevice}})
	if ce := capability.AsError(err); ce == nil || ce.Code != ErrADBFailed {
		t.Errorf("err = %v, want %s", err, ErrADBFailed)
	}
}
