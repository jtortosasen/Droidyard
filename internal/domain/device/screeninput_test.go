package device

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// device.tap, device.swipe and device.pressKey (plan m2p step 6; ADR-0079) over the fake adb
// of the policy fixture and a fake screen.Source that records Inject.

// fakeScreen is a screen.Source that records what Inject was given and answers with err.
type fakeScreen struct {
	mu      sync.Mutex
	inputs  []screen.Input
	holders []screen.Holder // who each Inject was made as, in the same order
	err     error
	// block, when set, holds Inject until it is closed or ctx ends (a gesture in progress).
	block   chan struct{}
	entered chan struct{} // receives once per Inject that began, when set
}

func (f *fakeScreen) Join() screen.Viewer { return nil }
func (f *fakeScreen) Size() screen.Size   { return screen.Size{Width: 1080, Height: 2340} }
func (f *fakeScreen) Done() <-chan struct{} {
	return nil
}
func (f *fakeScreen) Reason() error { return nil }
func (f *fakeScreen) WatchSecure(func(bool)) func() {
	return func() {}
}
func (f *fakeScreen) Control(screen.Holder, screen.Event) error { return nil }
func (f *fakeScreen) Inject(ctx context.Context, h screen.Holder, in screen.Input) error {
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	f.holders = append(f.holders, h)
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			// What the real Inject returns for a call cancelled after the finger went down.
			return &screen.PartialError{Err: ctx.Err()}
		}
	}
	return f.err
}

func (f *fakeScreen) got() []screen.Input {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]screen.Input(nil), f.inputs...)
}

// siFix is the policy fixture with a lookup that serves fake for the serials in live and
// counts every lookup.
type siFix struct {
	*devicePolicyFixture
	fake  *fakeScreen
	mu    sync.Mutex
	asked []string
}

func newSIFix(t *testing.T, config string, live ...string) *siFix {
	t.Helper()
	f := &siFix{devicePolicyFixture: newDevicePolicyFixture(t, nil, config), fake: &fakeScreen{}}
	f.adb.screenLookup = func(serial string) screen.Source {
		f.mu.Lock()
		f.asked = append(f.asked, serial)
		f.mu.Unlock()
		for _, s := range live {
			if s == serial {
				return f.fake
			}
		}
		return nil
	}
	return f
}

func (f *siFix) lookups() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// call starts the capability as client and waits for its outcome.
func (f *siFix) call(t *testing.T, client capability.ClientKind, name, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Params: []byte(params), Client: client})
	if ce := capability.AsError(err); ce != nil {
		// Refused before it became an operation (a missing required parameter).
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

func devicesCalls(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	for _, c := range devicetest.Calls(t, dir) {
		if strings.Contains(c, "devices") {
			n++
		}
	}
	return n
}

func ptr[T any](v T) *T { return &v }

// Decision (ADR-0079 §1, §3): each capability turns its parameters into the one screen.Input
// the live view's source takes, normalised and unconverted, and a completed call says it was
// delivered. A durationMs becomes the tap's Hold, a swipe's t the point's offset, a key its
// name. Seen failing with device.pressKey building Key{"home"} for every key.
func TestScreenInputCapabilitiesPassTheInput(t *testing.T) {
	cases := []struct {
		name, params string
		want         screen.Input
	}{
		{"device.tap", `{"device":"` + uiSerial + `","x":0.25,"y":0.75}`, screen.Tap{X: 0.25, Y: 0.75}},
		{"device.tap", `{"device":"` + uiSerial + `","x":0,"y":1,"durationMs":2000}`, screen.Tap{X: 0, Y: 1, Hold: 2 * time.Second}},
		{"device.swipe", `{"device":"` + uiSerial + `","path":[{"x":0.5,"y":0.8,"t":0},{"x":0.5,"y":0.5,"t":120},{"x":0.5,"y":0.2,"t":5000}]}`,
			screen.Swipe{Points: []screen.Point{{X: 0.5, Y: 0.8}, {X: 0.5, Y: 0.5, T: 120 * time.Millisecond}, {X: 0.5, Y: 0.2, T: 5 * time.Second}}}},
		{"device.pressKey", `{"device":"` + uiSerial + `","key":"back"}`, screen.Key{Name: "back"}},
		{"device.scroll", `{"device":"` + uiSerial + `","x":0.25,"y":0.75,"dx":-2.5,"dy":16}`, screen.Scroll{X: 0.25, Y: 0.75, DX: -2.5, DY: 16}},
		{"device.scroll", `{"device":"` + uiSerial + `","x":1,"y":0,"dx":16,"dy":0}`, screen.Scroll{X: 1, Y: 0, DX: 16, DY: 0}},
		{"device.pressKey", `{"device":"` + uiSerial + `","key":"appSwitch"}`, screen.Key{Name: "appSwitch"}},
		{"device.pressKey", `{"device":"` + uiSerial + `","key":"delete"}`, screen.Key{Name: "delete"}},
	}
	for _, c := range cases {
		t.Run(c.name+" "+c.params, func(t *testing.T) {
			f := newSIFix(t, "", uiSerial)
			out := f.call(t, capability.ClientUI, c.name, c.params)
			if out.State != capability.StateCompleted {
				t.Fatalf("%s: %s, %+v", c.name, out.State, out.Error)
			}
			res, ok := out.Result.(ScreenInputResult)
			if !ok || !res.Delivered || len(res.Warnings) != 0 {
				t.Errorf("result %#v, want delivered with no warnings", out.Result)
			}
			if got := f.fake.got(); len(got) != 1 || !reflect.DeepEqual(got[0], c.want) {
				t.Errorf("Inject got %#v, want exactly [%#v]", got, c.want)
			}
		})
	}
}

// Decision (ADR-0079 §1): a parameter outside the bounds is REQUEST_INVALID_PARAMS before
// anything else: no adb command runs (not even the device listing), no source is looked up
// and nothing is injected. The bounds are the ADR's, tighter than the screen package's:
// coordinates 0..1, durationMs 0..2000, 2..64 points, t strictly increasing from 0 to at
// most 5000, five key names. Seen failing with the point count limit at 1024 (the screen
// package's): the 65-point row reached Inject.
func TestScreenInputRefusesWhatTheBoundsRefuse(t *testing.T) {
	path := func(pts ...string) string {
		return `{"device":"` + uiSerial + `","path":[` + strings.Join(pts, ",") + `]}`
	}
	pt := func(x, y float64, tt int) string { return fmt.Sprintf(`{"x":%v,"y":%v,"t":%d}`, x, y, tt) }
	many := func(n int) string {
		pts := make([]string, n)
		for i := range pts {
			pts[i] = pt(0.5, 0.5, i)
		}
		return path(pts...)
	}
	dev := `"device":"` + uiSerial + `"`
	cases := []struct{ name, cap, params string }{
		{"x above 1", "device.tap", `{` + dev + `,"x":1.01,"y":0.5}`},
		{"y below 0", "device.tap", `{` + dev + `,"x":0.5,"y":-0.01}`},
		{"x null", "device.tap", `{` + dev + `,"x":null,"y":0.5}`},
		{"x and y null", "device.tap", `{` + dev + `,"x":null,"y":null}`},
		{"a point with a null t", "device.swipe", path(pt(0.5, 0.5, 0), `{"x":0.5,"y":0.5,"t":null}`)},
		{"x missing", "device.tap", `{` + dev + `,"y":0.5}`},
		{"durationMs above 2000", "device.tap", `{` + dev + `,"x":0.5,"y":0.5,"durationMs":2001}`},
		{"durationMs negative", "device.tap", `{` + dev + `,"x":0.5,"y":0.5,"durationMs":-1}`},
		{"one point", "device.swipe", path(pt(0.5, 0.5, 0))},
		{"65 points", "device.swipe", many(65)},
		{"x outside 0..1", "device.swipe", path(pt(0.5, 0.5, 0), pt(1.5, 0.5, 100))},
		{"t equal to the one before", "device.swipe", path(pt(0.5, 0.5, 0), pt(0.5, 0.6, 100), pt(0.5, 0.7, 100))},
		{"t going back", "device.swipe", path(pt(0.5, 0.5, 0), pt(0.5, 0.6, 100), pt(0.5, 0.7, 50))},
		{"t above 5000", "device.swipe", path(pt(0.5, 0.5, 0), pt(0.5, 0.6, 5001))},
		{"first t not 0", "device.swipe", path(pt(0.5, 0.5, 10), pt(0.5, 0.6, 100))},
		{"a point without y", "device.swipe", path(pt(0.5, 0.5, 0), `{"x":0.5,"t":100}`)},
		{"an unknown key", "device.pressKey", `{` + dev + `,"key":"power"}`},
		// device.scroll's bounds (plan m2y step 3, S1): each axis within 16 notches, not both
		// zero, every number present.
		{"dy above 16", "device.scroll", `{` + dev + `,"x":0.5,"y":0.5,"dx":0,"dy":16.01}`},
		{"dx below -16", "device.scroll", `{` + dev + `,"x":0.5,"y":0.5,"dx":-16.01,"dy":1}`},
		{"dx and dy both zero", "device.scroll", `{` + dev + `,"x":0.5,"y":0.5,"dx":0,"dy":0}`},
		{"scroll x above 1", "device.scroll", `{` + dev + `,"x":1.01,"y":0.5,"dx":0,"dy":1}`},
		{"scroll dx null", "device.scroll", `{` + dev + `,"x":0.5,"y":0.5,"dx":null,"dy":1}`},
		{"scroll dy missing", "device.scroll", `{` + dev + `,"x":0.5,"y":0.5,"dx":1}`},
		{"a key in the wrong case", "device.pressKey", `{` + dev + `,"key":"Back"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSIFix(t, "", uiSerial)
			out := f.call(t, capability.ClientUI, c.cap, c.params)
			mustFail(t, out, capability.ErrInvalidParams)
			if n := len(f.fake.got()); n != 0 {
				t.Errorf("%d Inject calls for a refused input", n)
			}
			if l := f.lookups(); len(l) != 0 {
				t.Errorf("the source was looked up (%v) for a refused input", l)
			}
			if n := devicesCalls(t, f.dir); n != 0 {
				t.Errorf("%d adb device listings ran for a refused input", n)
			}
		})
	}
}

// Decision (ADR-0079 §3): with no live view open the call is DEVICE_NO_STREAM with the
// next step "open the device's live view", and nothing is sent. The capabilities never
// start a source: the fake adb saw no push, no forward and no server start, and the source
// set is empty. Seen failing with the handler acquiring the source (a.screens.acquire):
// the server was pushed.
func TestScreenInputWithNoLiveViewSendsNothing(t *testing.T) {
	for _, c := range []struct{ name, params string }{
		{"device.tap", `{"device":"` + uiSerial + `","x":0.5,"y":0.5}`},
		{"device.swipe", `{"device":"` + uiSerial + `","path":[{"x":0.5,"y":0.5,"t":0},{"x":0.5,"y":0.1,"t":100}]}`},
		{"device.pressKey", `{"device":"` + uiSerial + `","key":"home"}`},
		{"device.scroll", `{"device":"` + uiSerial + `","x":0.5,"y":0.5,"dx":0,"dy":1}`},
	} {
		t.Run(c.name+" with the lookup finding none", func(t *testing.T) {
			f := newSIFix(t, "") // no live serial
			ce := mustFail(t, f.call(t, capability.ClientUI, c.name, c.params), ErrNoStream)
			if !strings.Contains(ce.NextStep, "live view") {
				t.Errorf("nextStep %q, want it to say to open the device's live view", ce.NextStep)
			}
			if n := len(f.fake.got()); n != 0 {
				t.Errorf("%d Inject calls", n)
			}
		})
		t.Run(c.name+" through the real source set", func(t *testing.T) {
			f := newDevicePolicyFixture(t, nil, "")
			ce := mustFail(t, f.run(t, c.name, c.params), ErrNoStream)
			if ce.Code != ErrNoStream {
				t.Errorf("code %s", ce.Code)
			}
			for _, call := range devicetest.Calls(t, f.dir) {
				for _, started := range []string{" push ", " forward ", "app_process"} {
					if strings.Contains(call, started) {
						t.Errorf("a source was started: %q", call)
					}
				}
			}
			if n := f.adb.LiveScreenSources(); n != 0 {
				t.Errorf("%d sources are live, want none", n)
			}
		})
	}
}

// Decision (ADR-0015 §2, §4; plan m2p step 6 "Rests on"): no input reaches a device without
// passing authorize, and authorize runs before the source is looked up. A physical device
// the policy does not admit is DEVICE_NOT_ALLOWED with no lookup and no Inject, for each
// capability, even when a live view is open on it; an admitted one is looked up and gets the
// input. Seen failing with the authorize hook returning a bare target (policy skipped): each
// call for the unadmitted device completed instead of failing "operation completed, error
// <nil>; want failed with DEVICE_NOT_ALLOWED".
func TestScreenInputIsAuthorizedBeforeTheSourceIsLooked(t *testing.T) {
	params := map[string]string{
		"device.tap":      `{"device":"` + physSerial + `","x":0.5,"y":0.5}`,
		"device.swipe":    `{"device":"` + physSerial + `","path":[{"x":0.5,"y":0.5,"t":0},{"x":0.5,"y":0.1,"t":100}]}`,
		"device.pressKey": `{"device":"` + physSerial + `","key":"home"}`,
		"device.scroll":   `{"device":"` + physSerial + `","x":0.5,"y":0.5,"dx":0,"dy":1}`,
	}
	for name, p := range params {
		t.Run(name+" not admitted", func(t *testing.T) {
			f := newSIFix(t, "", physSerial, uiSerial) // a live view is open, the policy still says no
			mustFail(t, f.call(t, capability.ClientUI, name, p), ErrNotAllowed)
			if l := f.lookups(); len(l) != 0 {
				t.Errorf("the source was looked up (%v) before the policy refused", l)
			}
			if n := len(f.fake.got()); n != 0 {
				t.Errorf("%d Inject calls for a refused device", n)
			}
		})
		t.Run(name+" admitted", func(t *testing.T) {
			f := newSIFix(t, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["`+physSerial+`"]}}`, physSerial)
			if out := f.call(t, capability.ClientUI, name, p); out.State != capability.StateCompleted {
				t.Fatalf("admitted: %s, %+v", out.State, out.Error)
			}
			if l := f.lookups(); len(l) != 1 || l[0] != physSerial {
				t.Errorf("lookups %v, want [%s]", l, physSerial)
			}
			if n := len(f.fake.got()); n != 1 {
				t.Errorf("%d Inject calls, want 1", n)
			}
		})
	}
}

// Decision (ADR-0060 §2): the three are for humans, so the core refuses them from an MCP
// session with REQUEST_NOT_ALLOWED_HERE before the call is an operation: no operation, no
// listing, no lookup, no Inject. The UI and the CLI are let through. Seen failing with the
// descriptors' Audience left at all: the MCP call completed.
func TestScreenInputIsRefusedFromMCP(t *testing.T) {
	for name, p := range map[string]string{
		"device.tap":      `{"device":"` + uiSerial + `","x":0.5,"y":0.5}`,
		"device.swipe":    `{"device":"` + uiSerial + `","path":[{"x":0.5,"y":0.5,"t":0},{"x":0.5,"y":0.1,"t":100}]}`,
		"device.pressKey": `{"device":"` + uiSerial + `","key":"home"}`,
		"device.scroll":   `{"device":"` + uiSerial + `","x":0.5,"y":0.5,"dx":0,"dy":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newSIFix(t, "", uiSerial)
			_, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Params: []byte(p), Client: capability.ClientMCP})
			if ce := capability.AsError(err); ce == nil || ce.Code != capability.ErrNotAllowedHere {
				t.Fatalf("Start over MCP: %v, want REQUEST_NOT_ALLOWED_HERE", err)
			}
			if len(f.lookups()) != 0 || len(f.fake.got()) != 0 || devicesCalls(t, f.dir) != 0 {
				t.Errorf("the refused call reached the device path: lookups %v, injects %d, listings %d", f.lookups(), len(f.fake.got()), devicesCalls(t, f.dir))
			}
			for _, client := range []capability.ClientKind{capability.ClientUI, capability.ClientCLI} {
				if out := f.call(t, client, name, p); out.State != capability.StateCompleted {
					t.Errorf("client %s: %s, %+v", client, out.State, out.Error)
				}
			}
		})
	}
}

// Decision (ADR-0079 §Consequences; ADR-0020): every input is an operation with its client
// kind and one trace line, which names the capability and the client and nothing of the
// coordinates; and it takes the device's write key, so an input queues behind another write
// to the same device (device.tapElement, device.inputText) and runs after it. Seen failing
// with Resource removed from the descriptors: the second call's Inject began while the first
// was still held.
func TestScreenInputIsAnOperationOnTheWriteKey(t *testing.T) {
	f := newSIFix(t, "", uiSerial)
	var mu sync.Mutex
	var lines []string
	f.ops.UseTrace(func(format string, v ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, v...))
	})

	hold := make(chan struct{})
	f.fake.block = hold
	f.fake.entered = make(chan struct{}, 4)
	start := func(name, params string) string {
		id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Params: []byte(params), Client: capability.ClientUI})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := start("device.tap", `{"device":"`+uiSerial+`","x":0.123456,"y":0.5}`)
	<-f.fake.entered
	second := start("device.pressKey", `{"device":"`+uiSerial+`","key":"home"}`)
	select {
	case <-f.fake.entered:
		t.Fatal("the second input began while the first held the device's write key")
	case <-time.After(150 * time.Millisecond):
	}
	close(hold)
	for _, id := range []string{first, second} {
		out, err := f.ops.Wait(context.Background(), id, time.Now().Add(10*time.Second))
		if err != nil || out.State != capability.StateCompleted {
			t.Fatalf("operation %s: %+v, %v", id, out, err)
		}
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "two trace lines", func() bool { mu.Lock(); defer mu.Unlock(); return len(lines) >= 2 })
	mu.Lock()
	defer mu.Unlock()
	for i, c := range []struct{ id, name string }{{first, "device.tap"}, {second, "device.pressKey"}} {
		re := regexp.MustCompile(fmt.Sprintf(`^droidyard: op %s client=ui capability=%s workspace=- state=completed ms=\d+$`, c.id, regexp.QuoteMeta(c.name)))
		var found bool
		for _, l := range lines {
			found = found || re.MatchString(l)
			if strings.Contains(l, "0.123456") {
				t.Errorf("a trace line carries the coordinates: %q", l)
			}
		}
		if !found {
			t.Errorf("no trace line for call %d matches %s: %q", i, re, lines)
		}
	}
}

// Decision (ADR-0079 §3; ui-automation.md §Design rules): a failure of the live view before
// the first byte is DEVICE_STREAM_FAILED with nothing sent; one after it completes the call
// with a warning, because part of the gesture is on the device and an error invites a repeat;
// a call cancelled even after the first byte (the fake returns what the real Inject does: a
// PartialError wrapping the context's error) is cancelled, not a warning or a stream
// failure. The cause is the daemon's words (the
// source's end reason), never device text. Seen failing with the PartialError treated as any
// other error: the cut gesture failed.
func TestScreenInputFailureBeforeAndAfterTheFirstByte(t *testing.T) {
	const tap = `{"device":"` + uiSerial + `","x":0.5,"y":0.5}`
	t.Run("before the first byte", func(t *testing.T) {
		f := newSIFix(t, "", uiSerial)
		f.fake.err = &ScreenEnd{Reason: StopDeviceGone, Cause: "adb shell exited with status 3"}
		ce := mustFail(t, f.call(t, capability.ClientUI, "device.tap", tap), ErrStreamFailed)
		if !strings.Contains(ce.Cause, StopDeviceGone) || !strings.Contains(ce.Cause, "adb shell exited with status 3") {
			t.Errorf("cause %q, want the source's end reason", ce.Cause)
		}
	})
	t.Run("the control socket failing before the first byte", func(t *testing.T) {
		f := newSIFix(t, "", uiSerial)
		f.fake.err = errors.New("screen: the control socket: write tcp 127.0.0.1:1: broken pipe")
		ce := mustFail(t, f.call(t, capability.ClientUI, "device.tap", tap), ErrStreamFailed)
		if strings.Contains(ce.Cause, "127.0.0.1") || strings.Contains(ce.Cause, "broken pipe") {
			t.Errorf("cause %q carries the socket error", ce.Cause)
		}
	})
	t.Run("after the first byte", func(t *testing.T) {
		f := newSIFix(t, "", uiSerial)
		f.fake.err = &screen.PartialError{Err: &ScreenEnd{Reason: StopExited, Cause: "adb shell terminated"}}
		out := f.call(t, capability.ClientUI, "device.swipe", `{"device":"`+uiSerial+`","path":[{"x":0.5,"y":0.5,"t":0},{"x":0.5,"y":0.1,"t":100}]}`)
		if out.State != capability.StateCompleted {
			t.Fatalf("a cut gesture is %s with %+v, want completed with a warning", out.State, out.Error)
		}
		res := out.Result.(ScreenInputResult)
		if !res.Delivered || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "part of it") || !strings.Contains(res.Warnings[0], "adb shell terminated") {
			t.Errorf("result %+v, want delivered with one warning naming the cause", res)
		}
	})
	// Decision (ADR-0117 §4): a gesture whose finger the live view's user took is not a failed
	// live view. Before its first byte the call fails (nothing sent) with a cause and next step
	// that say so, not "open the live view again"; after it the warning says the finger was
	// taken. Seen failing with the generic stream-failed text for both.
	t.Run("the finger taken before the first byte", func(t *testing.T) {
		f := newSIFix(t, "", uiSerial)
		f.fake.err = errFingerLost
		ce := mustFail(t, f.call(t, capability.ClientUI, "device.tap", tap), ErrStreamFailed)
		if !strings.Contains(ce.Message, "took the finger") || !strings.Contains(ce.NextStep, "Retry") || strings.Contains(ce.NextStep, "Open the device's live view again") {
			t.Errorf("error %+v, want it to say the live view's user took the finger and the call can be retried", ce)
		}
	})
	t.Run("the finger taken after the first byte", func(t *testing.T) {
		f := newSIFix(t, "", uiSerial)
		f.fake.err = &screen.PartialError{Err: errFingerLost}
		out := f.call(t, capability.ClientUI, "device.tap", tap)
		res, _ := out.Result.(ScreenInputResult)
		if out.State != capability.StateCompleted || !res.Delivered || len(res.Warnings) != 1 ||
			!strings.Contains(res.Warnings[0], "took the finger") || strings.Contains(res.Warnings[0], "failed") {
			t.Errorf("outcome %s %+v, want delivered with a warning that the live view took the finger", out.State, res)
		}
	})
	t.Run("a cancelled call", func(t *testing.T) {
		f := newSIFix(t, "", uiSerial)
		f.fake.block = make(chan struct{})
		f.fake.entered = make(chan struct{}, 1)
		id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.tap", Params: []byte(tap), Client: capability.ClientUI})
		if err != nil {
			t.Fatal(err)
		}
		<-f.fake.entered
		if err := f.ops.Cancel(id); err != nil {
			t.Fatal(err)
		}
		out, err := f.ops.Wait(context.Background(), id, time.Now().Add(10*time.Second))
		if err != nil || out.State != capability.StateCancelled {
			t.Fatalf("outcome %+v, %v; want cancelled", out, err)
		}
	})
}

// Decision (ADR-0079 §3): the lookup finds a source that is started and not ending, and
// nothing else: none for a device nobody watches, the source while a viewer holds it, and
// none while the source is still starting (its control socket is not open), and none in the
// moment after the last viewer let go and before the source is torn down. It holds no
// reference of its own, so it never keeps a source alive or starts one. Seen failing twice:
// with the ready check removed the starting source was found, and with `|| s.ending`
// removed the one just released was found.
func TestScreenSourcesRunningFindsOnlyAStartedSource(t *testing.T) {
	t.Run("nothing started", func(t *testing.T) {
		f := startedFixture(t, nil)
		if s := f.adb.screens.running(uiSerial); s != nil {
			t.Fatalf("found %p with nothing started", s)
		}
		if n := f.adb.LiveScreenSources(); n != 0 {
			t.Fatalf("looking up started %d sources", n)
		}
	})

	t.Run("still starting", func(t *testing.T) {
		data, _, _ := ssFixture(t)
		f := newSSFixture(t, screenListenerOpts{head: data, gateHead: true}, nil)
		got := make(chan func(), 1)
		go func() {
			_, release, err := f.acquire(context.Background())
			if err != nil {
				t.Error(err)
				release = func() {}
			}
			got <- release
		}()
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the source in the set", func() bool { return f.adb.screens.Live() == 1 })
		if s := f.adb.screens.running(uiSerial); s != nil {
			t.Errorf("running = %p while the source is still starting, want none", s)
		}
		f.ln.Release()
		release := <-got
		if s := f.adb.screens.running(uiSerial); s == nil {
			t.Error("running found none once the source started")
		}
		release()
	})

	t.Run("held, then released", func(t *testing.T) {
		f := startedFixture(t, nil)
		src, release := f.mustAcquire(t)
		if got := f.adb.screens.running(uiSerial); got != src {
			t.Errorf("running = %p, want the acquired source %p", got, src)
		}
		if got := f.adb.screens.running("emulator-5556"); got != nil {
			t.Errorf("running found %p for another device", got)
		}
		// The window after the last reference was dropped and before the source left the set.
		var inWindow *screenSource
		screenAfterLastUnref = func() { inWindow = f.adb.screens.running(uiSerial) }
		t.Cleanup(func() { screenAfterLastUnref = nil })
		release()
		if inWindow != nil {
			t.Errorf("running = %p while the last release was ending the source, want none", inWindow)
		}
		if got := f.adb.screens.running(uiSerial); got != nil {
			t.Errorf("running = %p after the last release, want none", got)
		}
		f.requireClean(t)
	})
}

// Decision (ADR-0079 §3, §1): through the real source set and a real source, a device.tap
// reaches the control socket as EncodeControl's messages against the stream's size, and
// reaches nothing once the last viewer is gone (DEVICE_NO_STREAM, no more bytes). Seen
// failing with the coordinates passed through as pixels: the bytes differed.
func TestScreenInputReachesTheControlSocket(t *testing.T) {
	f := startedFixture(t, nil)
	_, release := f.mustAcquire(t)
	out := runAs(t, f.ops, capability.ClientUI, "device.tap", `{"device":"`+uiSerial+`","x":0.5,"y":0.25}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("device.tap: %s, %+v", out.State, out.Error)
	}
	cs, err := screen.EncodeControl(screen.Tap{X: 0.5, Y: 0.25}, screen.Size{Width: 590, Height: 1280})
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	for _, c := range cs {
		want = append(want, c.Data...)
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the tap's bytes", func() bool { return len(f.ln.controlBytes()) >= len(want) })
	if got := f.ln.controlBytes(); !reflect.DeepEqual(got, want) {
		t.Errorf("control bytes % x, want % x", got, want)
	}
	release()
	n := len(f.ln.controlBytes())
	mustFail(t, runAs(t, f.ops, capability.ClientUI, "device.tap", `{"device":"`+uiSerial+`","x":0.5,"y":0.25}`), ErrNoStream)
	if m := len(f.ln.controlBytes()); m != n {
		t.Errorf("%d control bytes after the view closed, want %d", m, n)
	}
}

func runAs(t *testing.T, ops *capability.Operations, client capability.ClientKind, name, params string) capability.Outcome {
	t.Helper()
	id, err := ops.Start(context.Background(), capability.Request{Capability: name, Params: []byte(params), Client: client})
	if err != nil {
		t.Fatalf("Start %s: %v", name, err)
	}
	out, err := ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Decision (ADR-0079 §3): the real source tells a failure before the first byte from one
// after it. An Inject on a source that already ended returns the end reason as it is;
// an Inject cut by its context after the finger went down, and one cut by the source ending
// mid-gesture, are *screen.PartialError wrapping the cause. Seen failing with fail() not
// wrapping: the mid-gesture end came back as a plain *ScreenEnd.
func TestScreenSourceInjectSaysWhetherAnythingWasSent(t *testing.T) {
	size := screen.Size{Width: 590, Height: 1280}
	swipe := screen.Swipe{Points: []screen.Point{{X: 0.5, Y: 0.8}, {X: 0.5, Y: 0.2, T: 5 * time.Second}}}
	down, _ := screen.EncodeControl(swipe, size)

	t.Run("cut by its context after the first byte", func(t *testing.T) {
		f := startedFixture(t, nil)
		src, release := f.mustAcquire(t)
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		err := src.Inject(ctx, screen.NewHolder(false), swipe)
		var partial *screen.PartialError
		if !errors.As(err, &partial) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Inject = %v, want a PartialError wrapping the deadline", err)
		}
	})

	t.Run("the source ending mid-gesture", func(t *testing.T) {
		f := startedFixture(t, nil)
		src, release := f.mustAcquire(t)
		defer release()
		done := make(chan error, 1)
		go func() { done <- src.Inject(context.Background(), screen.NewHolder(false), swipe) }()
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the finger down", func() bool { return len(f.ln.controlBytes()) >= len(down[0].Data) })
		src.end(&ScreenEnd{Reason: StopExited, Cause: "adb shell terminated"})
		select {
		case err := <-done:
			var partial *screen.PartialError
			var end *ScreenEnd
			if !errors.As(err, &partial) || !errors.As(err, &end) || end.Reason != StopExited {
				t.Fatalf("Inject = %v, want a PartialError wrapping the source's end", err)
			}
		case <-time.After(ssWait):
			t.Fatal("Inject did not return when the source ended")
		}
	})

	t.Run("the source already ended", func(t *testing.T) {
		f := startedFixture(t, nil)
		src, release := f.mustAcquire(t)
		defer release()
		src.end(&ScreenEnd{Reason: StopFailed, Cause: "x"})
		<-src.Done()
		err := src.Inject(context.Background(), screen.NewHolder(false), screen.Tap{X: 0.5, Y: 0.5})
		var partial *screen.PartialError
		var end *ScreenEnd
		if errors.As(err, &partial) || !errors.As(err, &end) {
			t.Fatalf("Inject = %v, want the source's end reason, not a PartialError", err)
		}
		if n := len(f.ln.controlBytes()); n != 0 {
			t.Errorf("%d control bytes written to an ended source", n)
		}
	})
}

// Decision (ADR-0079 §3): a control write that fails before anything of the input was
// written is a plain error wrapping the socket's, not a PartialError, and nothing reaches
// the device; and any control write error ends the source as failed, because a write cut
// midway leaves half a message and the next input would be read out of frame. The viewers
// see failed, and the next input finds no live view (DEVICE_NO_STREAM, nothing sent). A write
// that fails after the finger went down is a PartialError, and ends the source the same way.
// Seen failing: with `sent := true` from the start, the first case came back as a
// PartialError; with endOnWriteError removed, the source stayed running and the next tap
// was written.
func TestScreenSourceControlWriteErrorEndsTheSource(t *testing.T) {
	tap := `{"device":"` + uiSerial + `","x":0.5,"y":0.5}`
	requireEndedFailed := func(t *testing.T, f *ssFix, src *screenSource) {
		t.Helper()
		select {
		case <-src.Done():
		case <-time.After(ssWait):
			t.Fatal("the source did not end after a control write error")
		}
		var end *ScreenEnd
		if !errors.As(src.Reason(), &end) || end.Reason != StopFailed || end.Cause != "the control socket could not be written" {
			t.Errorf("reason %v, want failed because the control socket could not be written", src.Reason())
		}
		n := len(f.ln.controlBytes())
		mustFail(t, runAs(t, f.ops, capability.ClientUI, "device.tap", tap), ErrNoStream)
		if m := len(f.ln.controlBytes()); m != n {
			t.Errorf("%d control bytes after the failure, want %d: the next input was written", m, n)
		}
	}

	t.Run("before the first byte", func(t *testing.T) {
		f := startedFixture(t, nil)
		src, release := f.mustAcquire(t)
		defer release()
		src.control.Close()
		err := src.Inject(context.Background(), screen.NewHolder(false), screen.Tap{X: 0.5, Y: 0.5})
		var partial *screen.PartialError
		if err == nil || errors.As(err, &partial) || !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Inject = %v, want a plain error wrapping the closed socket's", err)
		}
		if n := len(f.ln.controlBytes()); n != 0 {
			t.Errorf("%d control bytes written, want none", n)
		}
		requireEndedFailed(t, f, src)
	})

	t.Run("after the first byte", func(t *testing.T) {
		f := startedFixture(t, nil)
		src, release := f.mustAcquire(t)
		defer release()
		swipe := screen.Swipe{Points: []screen.Point{{X: 0.5, Y: 0.8}, {X: 0.5, Y: 0.2, T: 400 * time.Millisecond}}}
		done := make(chan error, 1)
		go func() { done <- src.Inject(context.Background(), screen.NewHolder(false), swipe) }()
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the finger down", func() bool { return len(f.ln.controlBytes()) > 0 })
		src.control.Close()
		select {
		case err := <-done:
			var partial *screen.PartialError
			if !errors.As(err, &partial) || !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Inject = %v, want a PartialError wrapping the closed socket's", err)
			}
		case <-time.After(ssWait):
			t.Fatal("Inject did not return")
		}
		requireEndedFailed(t, f, src)
	})
}
