package device

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// How device.tap, device.swipe and device.pressKey reach a device with no live view (plan
// m3g step 4; ADR-0133 §1, §2): the open view's stream, else the reader, else `adb shell
// input`, over the fake adb, the fake reader and a fake screen source. The facts about a real
// device are the spike's and step 2's real test's; these hold the daemon to them.

const (
	// header1080 and header2400 are what `screencap | head -c 8 | od -An -tu4` prints for a
	// 1080x2400 portrait screen and the same screen rotated, as od pads its columns.
	header1080 = "        1080        2400\n"
	header2400 = "        2400        1080\n"
	// headerCall is the fake's recorded form of the size read.
	headerCall = "screencap | head -c 8 | od -An -tu4"
)

// agentCall runs a capability as an MCP session, which is what an agent is.
func (f *uiFixture) agentCall(t *testing.T, name, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Params: []byte(params),
		Client: capability.ClientMCP, Origin: capability.Origin{Client: capability.ClientMCP}})
	if ce := capability.AsError(err); ce != nil {
		return capability.Outcome{State: capability.StateFailed, Error: ce}
	} else if err != nil {
		t.Fatalf("Start %s: %v", name, err)
	}
	return f.wait(t, id)
}

func mustDeliver(t *testing.T, out capability.Outcome) ScreenInputResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(ScreenInputResult)
	if !ok {
		t.Fatalf("result is %T, want ScreenInputResult", out.Result)
	}
	return r
}

// inputCommands are the `adb shell input …` commands the fake recorded, without the prefix.
func (f *uiFixture) inputCommands(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range f.shellCalls(t) {
		if strings.HasPrefix(c, "input ") {
			out = append(out, c)
		}
	}
	return out
}

// Decision (ADR-0133 §1): an agent's tap with no live view open is delivered by the reader as
// a two-point gesture at one place, held 50 ms, in the view wire's units (0.5 is 32768): the
// result says `reader`, the reader got exactly that line, and no `input` command ran. A long
// press holds for its durationMs, a swipe sends its points as they are, and a key is
// `press <code>`. Seen failing on step 3's code: the tap was DEVICE_NO_STREAM.
func TestAgentTapWithoutAView(t *testing.T) {
	cases := []struct {
		name, capName, params, want string
	}{
		{"a tap", "device.tap", `{` + devParam + `,"x":0.5,"y":0.5}`, "gesture 2 32768 32768 0 32768 32768 50"},
		{"a long press", "device.tap", `{` + devParam + `,"x":0,"y":1,"durationMs":800}`, "gesture 2 0 65535 0 0 65535 800"},
		{"a swipe", "device.swipe", `{` + devParam + `,"path":[{"x":0.5,"y":0,"t":0},{"x":0.5,"y":0.3,"t":150},{"x":0.5,"y":0.6,"t":300}]}`,
			"gesture 3 32768 0 0 32768 19661 150 32768 39321 300"},
		{"a key", "device.pressKey", `{` + devParam + `,"key":"power"}`, "press 26"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files, r := withPersistentReaderR(t, nil, pixelDoc(t))
			f := newUIFixture(t, files)
			res := mustDeliver(t, f.agentCall(t, c.capName, c.params))
			if !res.Delivered || res.Delivery != DeliveryReader || len(res.Warnings) != 0 {
				t.Errorf("result %+v, want delivered by the reader with no warnings", res)
			}
			if got := r.Acts(); !slices.Equal(got, []string{c.want}) {
				t.Errorf("the reader received %q, want exactly %q", got, c.want)
			}
			if got := f.inputCommands(t); len(got) != 0 {
				t.Errorf("`input` ran too: %q", got)
			}
		})
	}
}

// Decision (ADR-0133 §1): an open live view takes the input before the reader does: the fake
// source gets the three inputs as they were asked for, the result says `stream`, and the
// reader is never connected to (no ping, no act, no dump) nor started (no push, no forward).
// Seen failing with the reader tried first: the reader got the gesture and the source none.
func TestAgentInputPrefersTheView(t *testing.T) {
	files, r := withPersistentReaderR(t, nil, pixelDoc(t))
	f := newUIFixture(t, files)
	src := &fakeScreen{}
	f.adb.screenLookup = func(serial string) screen.Source {
		if serial == uiSerial {
			return src
		}
		return nil
	}
	for _, c := range []struct {
		name, params string
		want         screen.Input
	}{
		{"device.tap", `{` + devParam + `,"x":0.25,"y":0.75}`, screen.Tap{X: 0.25, Y: 0.75}},
		{"device.swipe", `{` + devParam + `,"path":[{"x":0.5,"y":0.8,"t":0},{"x":0.5,"y":0.2,"t":200}]}`,
			screen.Swipe{Points: []screen.Point{{X: 0.5, Y: 0.8}, {X: 0.5, Y: 0.2, T: 200 * time.Millisecond}}}},
		{"device.pressKey", `{` + devParam + `,"key":"volumeUp"}`, screen.Key{Name: "volumeUp"}},
	} {
		res := mustDeliver(t, f.agentCall(t, c.name, c.params))
		if res.Delivery != DeliveryStream || !res.Delivered {
			t.Errorf("%s: result %+v, want delivered by the stream", c.name, res)
		}
		got := src.got()
		if len(got) == 0 || !reflect.DeepEqual(got[len(got)-1], c.want) {
			t.Errorf("%s: the source got %#v, want it to end with %#v", c.name, got, c.want)
		}
	}
	if n := len(src.got()); n != 3 {
		t.Errorf("the source got %d inputs, want 3", n)
	}
	if r.Pings() != 0 || r.Dumps() != 0 || len(r.Acts()) != 0 {
		t.Errorf("the reader was used: %d pings, %d dumps, acts %q", r.Pings(), r.Dumps(), r.Acts())
	}
	for _, c := range f.calls(t) {
		if strings.Contains(c, " push ") || strings.Contains(c, " forward ") || strings.Contains(c, "app_process") || strings.Contains(c, " shell input ") {
			t.Errorf("a command reached past the view: %q", c)
		}
	}
}

// Decision (plan m3g Changes, step 4): a swipe replayed down an open view's control socket
// moves along straight segments, a move at least every 16 ms (a sparse path left the shade
// closed on API 36). The bytes on a real source's control socket for an agent's 100 ms swipe
// are the encoder's whole path, down, six moves between the points, the move onto the last
// point and the up, not the two the caller gave. Seen failing with the encoder writing one
// move per given point: 3 messages reached the socket.
func TestAgentSwipeThroughTheViewMoves(t *testing.T) {
	f := startedFixture(t, nil)
	_, release := f.mustAcquire(t)
	defer release()
	out := runAs(t, f.ops, capability.ClientMCP, "device.swipe", `{"device":"`+uiSerial+`","path":[{"x":0.5,"y":0.8,"t":0},{"x":0.5,"y":0.2,"t":100}]}`)
	if res := mustDeliver(t, out); res.Delivery != DeliveryStream {
		t.Fatalf("result %+v, want the stream", res)
	}
	cs, err := screen.EncodeControl(screen.Swipe{Points: []screen.Point{{X: 0.5, Y: 0.8}, {X: 0.5, Y: 0.2, T: 100 * time.Millisecond}}}, screen.Size{Width: 590, Height: 1280})
	if err != nil {
		t.Fatal(err)
	}
	// 100 ms in 16 ms gaps is 7 gaps: down, 6 moves between, the move onto the last point, up.
	if len(cs) != 9 {
		t.Fatalf("the encoder wrote %d messages for a 100 ms swipe, want 9", len(cs))
	}
	var want []byte
	for _, c := range cs {
		want = append(want, c.Data...)
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the swipe's bytes", func() bool { return len(f.ln.controlBytes()) >= len(want) })
	if got := f.ln.controlBytes(); string(got) != string(want) {
		t.Errorf("control bytes % x, want the moving path's % x", got, want)
	}
}

// Decision (ADR-0133 §1, ADR-0035 §3): with no view, the reader is tried and everything it did
// not execute falls to `adb shell input`, once, with the pixels of the on-device screencap
// header (which follows the rotation): a swipe from the top edge down 60% is `input swipe 540 0
// 540 1440 300` on 1080x2400 and `input swipe 1200 0 1200 648 300` on the rotated 2400x1080;
// a tap is `input tap`, a long press `input swipe x y x y <ms>`, a key `input keyevent`; a
// reader that denies the request is no reason to refuse it; a path of more than two points
// has no `input` form and is DEVICE_NOT_READY before anything is read or sent; a reader that
// is busy or cannot size the screen executed nothing and fails the call, with no `input`; and
// a request the reader took and answered with a timeout may be on the device, so the call
// completes with a warning and `input` is not run behind it. An `input` command whose
// outcome is unknown completes with a warning, as inputTap does. Seen failing with the
// fallback taken after the reader's timeout: a second swipe ran.
func TestAgentInputFallback(t *testing.T) {
	const swipe = `{` + devParam + `,"path":[{"x":0.5,"y":0,"t":0},{"x":0.5,"y":0.6,"t":300}]}`

	// down is a reader that cannot run: the dex push fails (the fixture's default).
	t.Run("reader unreachable", func(t *testing.T) {
		for _, c := range []struct {
			name, header, capName, params string
			want                          []string
		}{
			{"a swipe, portrait", header1080, "device.swipe", swipe, []string{"input swipe 540 0 540 1440 300"}},
			{"a swipe, landscape", header2400, "device.swipe", swipe, []string{"input swipe 1200 0 1200 648 300"}},
			{"a tap", header1080, "device.tap", `{` + devParam + `,"x":0.5,"y":0.5}`, []string{"input tap 540 1200"}},
			{"a long press", header1080, "device.tap", `{` + devParam + `,"x":0.5,"y":0.5,"durationMs":800}`, []string{"input swipe 540 1200 540 1200 800"}},
			{"the last pixel", header1080, "device.tap", `{` + devParam + `,"x":1,"y":1}`, []string{"input tap 1079 2399"}},
			{"a key", header1080, "device.pressKey", `{` + devParam + `,"key":"power"}`, []string{"input keyevent 26"}},
		} {
			t.Run(c.name, func(t *testing.T) {
				f := newUIFixture(t, map[string]string{uiSerial + ".screenheader.out": c.header})
				res := mustDeliver(t, f.agentCall(t, c.capName, c.params))
				if !res.Delivered || res.Delivery != DeliveryInput || len(res.Warnings) != 0 {
					t.Errorf("result %+v, want delivered by `input` with no warnings", res)
				}
				if got := f.inputCommands(t); !slices.Equal(got, c.want) {
					t.Errorf("`input` commands %q, want %q", got, c.want)
				}
			})
		}
	})

	t.Run("three points have no input form", func(t *testing.T) {
		f := newUIFixture(t, map[string]string{uiSerial + ".screenheader.out": header1080})
		ce := mustFail(t, f.agentCall(t, "device.swipe", `{`+devParam+`,"path":[{"x":0.5,"y":0.9,"t":0},{"x":0.5,"y":0.5,"t":100},{"x":0.5,"y":0.1,"t":200}]}`), ErrNotReady)
		if !strings.Contains(ce.Cause, "3 points") || !strings.Contains(ce.NextStep, "Nothing was sent") {
			t.Errorf("error %+v, want the cause to name the points and the next step to say nothing was sent", ce)
		}
		// (The reader's own start-up probes may run: it is tried first.)
		if got := f.inputCommands(t); len(got) != 0 || slices.Contains(f.shellCalls(t), headerCall) {
			t.Errorf("`input` or a size read ran for a path that cannot be sent: %q", f.shellCalls(t))
		}
	})

	t.Run("the screen cannot be sized", func(t *testing.T) {
		f := newUIFixture(t, nil) // no header scripted: od prints nothing
		ce := mustFail(t, f.agentCall(t, "device.tap", `{`+devParam+`,"x":0.5,"y":0.5}`), ErrNotReady)
		if !strings.Contains(ce.Message, "nothing was sent") {
			t.Errorf("message %q, want it to say nothing was sent", ce.Message)
		}
		if got := f.inputCommands(t); len(got) != 0 {
			t.Errorf("`input` ran without a size: %q", got)
		}
		// A key needs no size.
		if res := mustDeliver(t, f.agentCall(t, "device.pressKey", `{`+devParam+`,"key":"home"}`)); res.Delivery != DeliveryInput {
			t.Errorf("a key without a screen size: %+v", res)
		}
	})

	t.Run("the input command's outcome is unknown", func(t *testing.T) {
		f := newUIFixture(t, map[string]string{uiSerial + ".screenheader.out": header1080, uiSerial + ".inputswipe.exit": "1"})
		res := mustDeliver(t, f.agentCall(t, "device.swipe", swipe))
		if !res.Delivered || res.Delivery != DeliveryInput || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "may have reached the device") {
			t.Errorf("result %+v, want delivered with one warning that it may have reached the device", res)
		}
		if got := f.inputCommands(t); len(got) != 1 {
			t.Errorf("`input` ran %d times (%q), want once", len(got), got)
		}
	})

	// readerWith starts a fixture whose reader runs and answers the acting request with answer.
	readerWith := func(t *testing.T, answer string) (*uiFixture, *devicetest.FakeReader) {
		files, r := withPersistentReaderR(t, map[string]string{uiSerial + ".screenheader.out": header1080}, pixelDoc(t))
		r.SetActAnswers(answer)
		return newUIFixture(t, files), r
	}

	t.Run("the reader denies the request", func(t *testing.T) {
		f, r := readerWith(t, "droidyard-reader: denied 10123")
		res := mustDeliver(t, f.agentCall(t, "device.swipe", swipe))
		if res.Delivery != DeliveryInput || len(res.Warnings) != 0 {
			t.Errorf("result %+v, want `input` after a denial, with no warning", res)
		}
		if len(r.Acts()) != 1 || !slices.Equal(f.inputCommands(t), []string{"input swipe 540 0 540 1440 300"}) {
			t.Errorf("the reader got %q and `input` ran %q; want one request to each", r.Acts(), f.inputCommands(t))
		}
	})

	for _, c := range []struct{ name, answer, cause string }{
		{"the reader is busy", "droidyard-reader: busy", "acting thread was busy"},
		{"the reader cannot size the screen", "droidyard-reader: refused nosize", "screen's size"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, r := readerWith(t, c.answer)
			ce := mustFail(t, f.agentCall(t, "device.swipe", swipe), ErrNotReady)
			if !strings.Contains(ce.Message+ce.Cause, c.cause) {
				t.Errorf("error %+v, want it to say %q", ce, c.cause)
			}
			if len(r.Acts()) != 1 || len(f.inputCommands(t)) != 0 {
				t.Errorf("the reader got %q and `input` ran %q; want one request and no `input`", r.Acts(), f.inputCommands(t))
			}
		})
	}

	t.Run("the reader times out", func(t *testing.T) {
		f, r := readerWith(t, "droidyard-reader: timeout 3000")
		res := mustDeliver(t, f.agentCall(t, "device.swipe", swipe))
		if !res.Delivered || res.Delivery != DeliveryReader || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "never answered") {
			t.Errorf("result %+v, want delivered by the reader with one warning", res)
		}
		if len(r.Acts()) != 1 || len(f.inputCommands(t)) != 0 {
			t.Errorf("the reader got %q and `input` ran %q; want the one request and no `input` behind it", r.Acts(), f.inputCommands(t))
		}
	})

	t.Run("the reader takes the request and never answers", func(t *testing.T) {
		oldBase, oldMargin := readerActBoundBase, readerActMargin
		readerActBoundBase, readerActMargin = 0, 300*time.Millisecond
		t.Cleanup(func() { readerActBoundBase, readerActMargin = oldBase, oldMargin })
		f, r := readerWith(t, devicetest.ReaderGone)
		res := mustDeliver(t, f.agentCall(t, "device.tap", `{`+devParam+`,"x":0.5,"y":0.5}`))
		if res.Delivery != DeliveryReader || len(res.Warnings) != 1 {
			t.Errorf("result %+v, want delivered by the reader with one warning", res)
		}
		if len(r.Acts()) != 1 || len(f.inputCommands(t)) != 0 {
			t.Errorf("the reader got %q and `input` ran %q; want one request and no `input`", r.Acts(), f.inputCommands(t))
		}
	})
}

// the key names with their Android codes, literals of KeyEvent and not the table read back.
var keyCodes = map[string]int{
	"back": 4, "home": 3, "appSwitch": 187, "enter": 66, "delete": 67,
	"power": 26, "wakeUp": 224, "sleep": 223, "volumeUp": 24, "volumeDown": 25, "volumeMute": 164,
	"menu": 82, "search": 84, "tab": 61, "escape": 111,
	"dpadUp": 19, "dpadDown": 20, "dpadLeft": 21, "dpadRight": 22, "dpadCenter": 23,
	"notification": 83, "assist": 219,
}

// Decision (plan m3g Q2; ADR-0133 §3): every key name is the same key on the three deliveries:
// the stream's encoder writes its code, the reader is asked to `press` it and `adb shell input
// keyevent` is given it. A name outside the list is REQUEST_INVALID_PARAMS before anything
// runs, and the schema's enum is the list. Seen failing with `sleep` and `wakeUp` swapped in
// the table: the three paths agreed with each other and with nothing else, so the literals
// above are the check.
func TestPressKeyEveryPath(t *testing.T) {
	if got := screen.KeyNames(); len(got) != len(keyCodes) {
		t.Fatalf("%d key names, want %d: %v", len(got), len(keyCodes), got)
	}
	for name, code := range keyCodes {
		params := `{` + devParam + `,"key":"` + name + `"}`

		// The stream encoder.
		cs, err := screen.EncodeControl(screen.Key{Name: name}, screen.Size{Width: 720, Height: 1280})
		if err != nil || len(cs) != 2 || int(cs[0].Data[5]) != code&0xff || int(cs[0].Data[4]) != code>>8&0xff {
			t.Errorf("%s: the stream encodes %v, %v; want key %d down and up", name, cs, err, code)
		}

		// The reader.
		files, r := withPersistentReaderR(t, nil, pixelDoc(t))
		f := newUIFixture(t, files)
		if res := mustDeliver(t, f.agentCall(t, "device.pressKey", params)); res.Delivery != DeliveryReader {
			t.Errorf("%s: %+v, want the reader", name, res)
		}
		if got, want := r.Acts(), []string{fmt.Sprintf("press %d", code)}; !slices.Equal(got, want) {
			t.Errorf("%s: the reader received %q, want %q", name, got, want)
		}

		// `input keyevent`.
		f = newUIFixture(t, nil)
		if res := mustDeliver(t, f.agentCall(t, "device.pressKey", params)); res.Delivery != DeliveryInput {
			t.Errorf("%s: %+v, want `input`", name, res)
		}
		if got, want := f.inputCommands(t), []string{fmt.Sprintf("input keyevent %d", code)}; !slices.Equal(got, want) {
			t.Errorf("%s: `input` ran %q, want %q", name, got, want)
		}
	}

	f := newUIFixture(t, nil)
	before := len(f.calls(t))
	for _, bad := range []string{"reboot", "26", "Power", "", "back; reboot"} {
		mustFail(t, f.agentCall(t, "device.pressKey", `{`+devParam+`,"key":"`+bad+`"}`), capability.ErrInvalidParams)
	}
	if after := len(f.calls(t)); after != before {
		t.Errorf("a refused key ran %d adb commands", after-before)
	}
}

// Decision (ADR-0133 §3): the schema's enum for the key is exactly screen.KeyNames, so the
// descriptor cannot offer a key the encoder does not know or hide one it does.
func TestPressKeyEnumIsTheKeyTable(t *testing.T) {
	tag, ok := reflect.TypeOf(ScreenKeyParams{}).FieldByName("Key")
	if !ok {
		t.Fatal("no Key field")
	}
	var enum []string
	for _, part := range strings.Split(tag.Tag.Get("jsonschema"), ",") {
		if v, ok := strings.CutPrefix(part, "enum="); ok {
			enum = append(enum, v)
		}
	}
	slices.Sort(enum)
	if want := screen.KeyNames(); !slices.Equal(enum, want) {
		t.Errorf("the enum is %v, want the key table %v", enum, want)
	}
}

// dialogHierarchy is a reader dump whose root is a dialog (948x1600 of a 1080x2400 screen),
// with attrs on <hierarchy>.
func dialogHierarchy(attrs string) string {
	return `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0"` + attrs + `>` +
		`<node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="android" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[132,800][948,1600]">` +
		`<node index="0" text="Close app" resource-id="android:id/aerr_close" class="android.widget.Button" package="android" content-desc="" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[200,1400][500,1500]"></node>` +
		`</node></hierarchy>`
}

// Decision (ADR-0133 §2): device.uiTree publishes the real screen's size, which agents take
// their fractions of, beside the active window's extent. With a dialog on top `display` is the
// dialog's 948x1600 and `screen` is the screen's 1080x2400, in the result and in the text
// rendering's header; when no reader can run, the screen is the on-device screencap header,
// not the dump's root extent; and when that cannot be read either the tree is returned
// without one. Seen failing with `screen` taken from `display`: the dialog's extent.
func TestUITreeReportsTheScreen(t *testing.T) {
	t.Run("from the reader, under a dialog", func(t *testing.T) {
		files, _ := withPersistentReaderR(t, nil, dialogHierarchy(` screenWidth="1080" screenHeight="2400"`))
		f := newUIFixture(t, files)
		s := mustComplete(t, f.tree(t, uiSerial))
		if s.Display != (UIDisplay{Width: 948, Height: 1600}) {
			t.Errorf("display %+v, want the dialog's extent", s.Display)
		}
		if s.Screen == nil || *s.Screen != (UIScreen{Width: 1080, Height: 2400}) {
			t.Errorf("screen %v, want 1080x2400", s.Screen)
		}
		if text := uiTreeText(s); !strings.HasPrefix(text, "snapshot ") || !strings.Contains(strings.SplitN(text, "\n", 2)[0], "948x1600") || !strings.Contains(strings.SplitN(text, "\n", 2)[0], "screen 1080x2400") {
			t.Errorf("the rendering's header is %q, want the display and `screen 1080x2400`", strings.SplitN(text, "\n", 2)[0])
		}
		if got := f.shellCalls(t); slices.Contains(got, headerCall) {
			t.Errorf("a reader that wrote the size was asked again: %q", got)
		}
	})

	t.Run("on the uiautomator path", func(t *testing.T) {
		f := newUIFixture(t, map[string]string{
			uiSerial + ".dumptty.out":      dialogHierarchy(""),
			uiSerial + ".screenheader.out": header1080,
		})
		s := mustComplete(t, f.tree(t, uiSerial))
		if s.Display != (UIDisplay{Width: 948, Height: 1600}) {
			t.Errorf("display %+v, want the dialog's extent", s.Display)
		}
		if s.Screen == nil || *s.Screen != (UIScreen{Width: 1080, Height: 2400}) {
			t.Errorf("screen %v, want the header's 1080x2400, not the dump's root extent", s.Screen)
		}
		if !strings.Contains(strings.SplitN(uiTreeText(s), "\n", 2)[0], "screen 1080x2400") {
			t.Errorf("the rendering's header lacks the screen: %q", strings.SplitN(uiTreeText(s), "\n", 2)[0])
		}
	})

	t.Run("rotated, on the uiautomator path", func(t *testing.T) {
		f := newUIFixture(t, map[string]string{uiSerial + ".screenheader.out": header2400})
		if s := mustComplete(t, f.tree(t, uiSerial)); s.Screen == nil || *s.Screen != (UIScreen{Width: 2400, Height: 1080}) {
			t.Errorf("screen %v, want the rotated 2400x1080", s.Screen)
		}
	})

	t.Run("nothing can say", func(t *testing.T) {
		f := newUIFixture(t, nil)
		s := mustComplete(t, f.tree(t, uiSerial))
		if s.Screen != nil {
			t.Errorf("screen %v, want none: the header was unreadable", *s.Screen)
		}
		if strings.Contains(uiTreeText(s), "screen ") && strings.Contains(strings.SplitN(uiTreeText(s), "\n", 2)[0], "screen ") {
			t.Errorf("the rendering names a screen it does not have")
		}
	})
}

// Decision (ADR-0133 §2): a tree an action returns carries the screen too, so an agent that
// asked tapElement for returnTree can aim its next coordinate action at it. The tree comes
// from the reader, which wrote the size.
func TestTapElementTreeReportsTheScreen(t *testing.T) {
	quickAct(t)
	doc := strings.Replace(pixelDoc(t), `<hierarchy rotation="0"`, `<hierarchy rotation="0" screenWidth="1080" screenHeight="2400"`, 1)
	if !strings.Contains(doc, "screenWidth") {
		t.Skip("the recorded document's <hierarchy> has another shape")
	}
	files, _ := withPersistentReaderR(t, nil, doc)
	f := newUIFixture(t, files)
	res := mustTap(t, f.tapCall(t, `{`+devParam+`,"target":{"desc":"Navigate up"},"returnTree":true}`))
	if res.Tree == nil || res.Tree.Screen == nil || *res.Tree.Screen != (UIScreen{Width: 1080, Height: 2400}) {
		t.Errorf("tree %+v, want it to carry the screen 1080x2400", res.Tree)
	}
}

// Decision (ADR-0133 §1; ADR-0060 §2): device.tap, device.swipe and device.pressKey are for
// agents too, so the core lets an MCP session call them; device.scroll, which only the view's
// stream carries, is still refused from MCP with REQUEST_NOT_ALLOWED_HERE. Seen failing with
// the three left at audience human: the MCP calls were refused.
func TestInputAudience(t *testing.T) {
	f := newSIFix(t, "", uiSerial)
	calls := map[string]string{
		"device.tap":      `{"device":"` + uiSerial + `","x":0.5,"y":0.5}`,
		"device.swipe":    `{"device":"` + uiSerial + `","path":[{"x":0.5,"y":0.5,"t":0},{"x":0.5,"y":0.1,"t":100}]}`,
		"device.pressKey": `{"device":"` + uiSerial + `","key":"home"}`,
	}
	for name, p := range calls {
		if out := f.call(t, capability.ClientMCP, name, p); out.State != capability.StateCompleted {
			t.Errorf("%s over MCP: %s, %+v; want it completed", name, out.State, out.Error)
		}
	}
	if n := len(f.fake.got()); n != len(calls) {
		t.Errorf("the source got %d inputs, want %d", n, len(calls))
	}
	_, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.scroll", Client: capability.ClientMCP,
		Params: []byte(`{"device":"` + uiSerial + `","x":0.5,"y":0.5,"dx":0,"dy":1}`)})
	if ce := capability.AsError(err); ce == nil || ce.Code != capability.ErrNotAllowedHere {
		t.Errorf("device.scroll over MCP: %v, want REQUEST_NOT_ALLOWED_HERE", err)
	}
}

// Decision (ADR-0094 §2; ADR-0133 §1): the three are acting calls. While another chat holds
// the device each fails DEVICE_OCCUPIED naming the holder, and nothing reaches the device: the
// view's source got no input and the fake adb no command; the first call on a free device
// holds it. Seen failing with the three registered without OccupiesAct: B's calls went through.
func TestAgentInputOccupies(t *testing.T) {
	f := newOccFix(t)
	f.connect("a", "agent-a")
	f.connect("b", "agent-b")
	calls := []struct{ name, params string }{
		{"device.tap", `{` + devParam + `,"x":0.5,"y":0.5}`},
		{"device.swipe", `{` + devParam + `,"path":[{"x":0.5,"y":0.8,"t":0},{"x":0.5,"y":0.2,"t":200}]}`},
		{"device.pressKey", `{` + devParam + `,"key":"home"}`},
	}
	mustOK(t, "A's tap", f.as(t, mcp("a"), calls[0].name, calls[0].params))
	if h, ok := f.holder(uiSerial); !ok || h.Name != "agent-a" {
		t.Fatalf("after A's tap the holder is %+v, %v; want agent-a", h, ok)
	}
	sent, before := len(f.fake.got()), f.deviceCalls(t)
	for _, c := range calls {
		out := f.as(t, mcp("b"), c.name, c.params)
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != ErrOccupied {
			t.Errorf("%s as B: %s, %+v; want failed with %s", c.name, out.State, out.Error, ErrOccupied)
			continue
		}
		if !strings.Contains(out.Error.Message, "agent-a") {
			t.Errorf("%s: message %q does not name the holder", c.name, out.Error.Message)
		}
	}
	if n := len(f.fake.got()); n != sent {
		t.Errorf("B's refused calls reached the view's source: %d inputs, want %d", n, sent)
	}
	if after := f.deviceCalls(t); after != before {
		t.Errorf("B's refused calls ran %d device commands: %v", after-before, f.calls(t))
	}
}

// Decision (ADR-0030; m3g step 6): a gesture whose reader does not answer the actor's ping is
// not sent to adb shell input at once: the reader is started again, once, as a read would
// (restartReader reuses it when it answers then, and kills a wedged one before a new start,
// so no act reaches a wedged reader, m1n N8), and the gesture goes to it. Seen failing
// without restartedActor: the second tap left the reader and failed on the `input` path.
func TestAgentInputRestartsAReaderThatDidNotAnswer(t *testing.T) {
	files, r := withPersistentReaderR(t, nil, pixelDoc(t), pixelDoc(t))
	f := newUIFixture(t, files)
	if res := mustDeliver(t, f.agentCall(t, "device.tap", `{`+devParam+`,"x":0.5,"y":0.5}`)); res.Delivery != DeliveryReader {
		t.Fatalf("first tap %+v, want the reader", res)
	}
	r.SetMutePings(1)
	res := mustDeliver(t, f.agentCall(t, "device.tap", `{`+devParam+`,"x":0.5,"y":0.5}`))
	if res.Delivery != DeliveryReader || len(res.Warnings) != 0 {
		t.Errorf("second tap %+v, want the reader again with no warnings", res)
	}
	if got := r.Acts(); len(got) != 2 {
		t.Errorf("the reader received %q, want both taps", got)
	}
	if got := f.inputCommands(t); len(got) != 0 {
		t.Errorf("`input` ran: %q", got)
	}

	// Within the backoff, a reader whose restart failed is not restarted again: the next
	// gesture goes to `input` at once (m3g close verifier: another tool holding UiAutomation
	// would otherwise cost each gesture a full reader start).
	f.adb.reader(uiSerial).mu.Lock()
	f.adb.reader(uiSerial).gestureRestartFailed = time.Now()
	f.adb.reader(uiSerial).mu.Unlock()
	r.SetMutePings(1)
	before := r.Pings()
	f.agentCall(t, "device.tap", `{`+devParam+`,"x":0.5,"y":0.5}`)
	// Pings counts answered pings: the actor's is muted, and a restart would ping again.
	if got := r.Pings() - before; got != 0 {
		t.Errorf("the reader answered %d pings after a recent failed restart; want none (no restart)", got)
	}
}

// Decision (m3g close verifier N3): an `input` command that never started sent nothing, so it
// is never reported delivered: under a call already cancelled it is the cancel, and no
// `input` command runs. Seen failing with the context checked before the start failure: the
// key was reported with a "may have reached the device" warning.
func TestInputThatNeverStartedIsNotDelivered(t *testing.T) {
	f := newUIFixture(t, nil)
	tgt := &target{adb: f.adb.Resolve().Path, serial: uiSerial}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := f.adb.deliverByInput(ctx, tgt, screen.Key{Name: "back"})
	if !errors.Is(err, context.Canceled) || res != nil {
		t.Errorf("result %+v, error %v; want the cancel and no result", res, err)
	}
	if got := f.inputCommands(t); len(got) != 0 {
		t.Errorf("`input` ran: %q", got)
	}
}
