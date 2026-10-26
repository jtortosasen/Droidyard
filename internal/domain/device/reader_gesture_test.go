package device

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// readerRequestCap is DroidyardReader's REQUEST_CAP: the longest request line it keeps. The
// test mirrors the constant so a path that no longer fits is caught here, not on a device.
const readerRequestCap = 4096

// Decision (plan m3g step 2; ADR-0133): a path travels as the view wire's normalised units,
// never as pixels, so the daemon needs no screen size and a rotation cannot make a request
// stale: 0 is 0, 1 is 65535 and 0.5 is 32768 (rounded half up), a time is whole
// milliseconds, a value outside 0..1 is clamped to the screen, and the longest path the
// reader takes, 64 points at their widest, still fits under its request cap. The expected
// lines are literals. Seen failing with pixels sent (the encoder scaling by 1080 and 2400
// instead of 65535: the first row read `gesture 2 0 0 0 1080 2400 300`).
func TestGestureRequestEncoding(t *testing.T) {
	cases := []struct {
		name   string
		points []pathPoint
		want   string
	}{
		{"edge to 60 percent", []pathPoint{{0.5, 0, 0}, {0.5, 0.6, 300 * time.Millisecond}}, "gesture 2 32768 0 0 32768 39321 300"},
		{"corners", []pathPoint{{0, 0, 0}, {1, 1, 5 * time.Second}}, "gesture 2 0 0 0 65535 65535 5000"},
		{"hold", []pathPoint{{0.25, 0.25, 0}, {0.25, 0.25, 800 * time.Millisecond}}, "gesture 2 16384 16384 0 16384 16384 800"},
		{"clamped", []pathPoint{{-0.5, 2, 0}, {1.5, -1, 100 * time.Millisecond}}, "gesture 2 0 65535 0 65535 0 100"},
		{"whole milliseconds", []pathPoint{{0, 0, 0}, {1, 1, 1500 * time.Microsecond}}, "gesture 2 0 0 0 65535 65535 1"},
	}
	for _, tc := range cases {
		if got := gestureRequest(tc.points); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := pressRequest(4); got != "press 4" {
		t.Errorf("pressRequest(4) = %q, want %q", got, "press 4")
	}

	widest := make([]pathPoint, gestureMaxPoints)
	for i := range widest {
		widest[i] = pathPoint{X: 1, Y: 1, At: time.Duration(4936+i) * time.Millisecond}
	}
	if got := len(gestureRequest(widest)) + 1; got >= readerRequestCap {
		t.Errorf("a %d-point path is %d bytes with its newline, not under the reader's cap of %d", gestureMaxPoints, got, readerRequestCap)
	}
}

// Decision (plan m3g step 2; ADR-0035 §1): a path holds the reader's single acting thread
// for as long as it lasts, so both bounds grow with its last time: the device's is the base
// plus the span, the host's sits the margin above it. A 5,000 ms path is cut off by neither
// at the tap's base, and the host's bound is above the device's so the reader's own
// `timeout` reaches the client. Seen failing with actGesture on the base bound (a 5 s path
// was reported unanswered at 4 s).
func TestGestureBoundGrowsWithThePath(t *testing.T) {
	span := 5000 * time.Millisecond
	dev := readerActBound(0, span)
	host := readerGestureHostBound(span)
	if !(host > dev && dev > 5*time.Second) {
		t.Errorf("a %s path: host bound %s, device bound %s, want host > device > 5s", span, host, dev)
	}
	if want := readerActBoundBase + span; dev != want {
		t.Errorf("device bound %s, want the base plus the span %s", dev, want)
	}
	if base := readerActHostBound(0); host <= base {
		t.Errorf("the host bound of a 5 s path (%s) is not above a tap's (%s)", host, base)
	}
	if got := readerActBound(0, time.Hour); got != readerActBoundCap {
		t.Errorf("a path longer than the cap is bounded at %s, want the cap %s", got, readerActBoundCap)
	}
	if got := readerActDeviceBound(10); got != readerActBound(10, 0) {
		t.Errorf("the keys formula and the shared one differ: %s against %s", got, readerActBound(10, 0))
	}

	// And actGesture uses it: with a base bound of 0 and a margin of 300 ms, a reader that
	// answers after 800 ms is waited for when the path lasts a second (bound 1.3 s), where the
	// base bound (0.3 s) would have reported the gesture unanswered.
	oldBase, oldMargin := readerActBoundBase, readerActMargin
	readerActBoundBase, readerActMargin = 0, 300*time.Millisecond
	t.Cleanup(func() { readerActBoundBase, readerActMargin = oldBase, oldMargin })
	f, r := actingReader(t)
	r.SetActAnswers("droidyard-reader: done")
	ac := f.adb.newActor(context.Background(), readerTarget(t, f.adb, uiSerial))
	if !ac.ok {
		t.Fatal("the fake reader is not ready")
	}
	r.SetDelay(800 * time.Millisecond)
	got, err := ac.gesture(context.Background(), []pathPoint{{0.5, 0, 0}, {0.5, 0.6, time.Second}})
	if err != nil || got.kind != readerActDone {
		t.Fatalf("a one-second path answered after 800 ms: %+v, %v, want done", got, err)
	}
}

// Decision (ADR-0035 §1, §3; plan m3g step 2): a gesture and a press are answered as the
// other acting requests are, and the answer says whether anything was executed. `done`
// executed; `denied` is an error before anything ran, so another method is allowed;
// `busy` and `refused nosize` executed nothing and fail DEVICE_NOT_READY (the screen's size
// could not be read: a retry may succeed); `refused malformed` executed nothing and no
// retry of the same request helps. The request line the reader got is asserted, so the
// verb reaches the reader. Seen failing with `nosize` unknown to the client: it was
// refusalOther, "it did not say which refusal this was".
func TestActGestureOutcomes(t *testing.T) {
	path := []pathPoint{{0.5, 0, 0}, {0.5, 0.6, 300 * time.Millisecond}}
	checkVerbOutcomes(t, "gesture 2 32768 0 0 32768 39321 300", "gestured", func(ac *actor) (readerAct, error) {
		return ac.gesture(context.Background(), path)
	})
}

// Decision: as TestActGestureOutcomes, for `press <keycode>`.
func TestActPressOutcomes(t *testing.T) {
	checkVerbOutcomes(t, "press 4", "pressed", func(ac *actor) (readerAct, error) {
		return ac.press(context.Background(), 4)
	})
}

func checkVerbOutcomes(t *testing.T, wantLine, what string, send func(ac *actor) (readerAct, error)) {
	t.Helper()
	f, r := actingReader(t)
	ac := f.adb.newActor(context.Background(), readerTarget(t, f.adb, uiSerial))
	if !ac.ok {
		t.Fatal("the fake reader is not ready")
	}
	rows := []struct {
		name   string
		answer string
		check  func(got readerAct, err error) string
	}{
		{"done", "droidyard-reader: done", func(got readerAct, err error) string {
			if err != nil || got.kind != readerActDone {
				return fmt.Sprintf("done = %+v, %v, want an executed request", got, err)
			}
			return ""
		}},
		{"denied", "droidyard-reader: denied 10123", func(got readerAct, err error) string {
			if err == nil || !strings.Contains(err.Error(), "fence") {
				return fmt.Sprintf("denied = %+v, %v, want the error naming the uid fence (nothing ran, another method allowed)", got, err)
			}
			return ""
		}},
		{"busy", "droidyard-reader: busy", func(got readerAct, err error) string {
			if err != nil || got.kind != readerActBusy {
				return fmt.Sprintf("busy = %+v, %v, want readerActBusy", got, err)
			}
			if e := capability.AsError(actingBusyError(what)); e == nil || e.Code != ErrNotReady {
				return fmt.Sprintf("the busy error is %v, want DEVICE_NOT_READY", e)
			}
			return ""
		}},
		{"refused nosize", "droidyard-reader: refused nosize", func(got readerAct, err error) string {
			if err != nil || got.kind != readerActRefused || got.refusal != refusalNoSize {
				return fmt.Sprintf("nosize = %+v, %v, want a refusal that is the size's", got, err)
			}
			e := capability.AsError(actRefusedError(got, what))
			if e == nil || e.Code != ErrNotReady || !strings.Contains(e.Cause, "screen's size") {
				return fmt.Sprintf("the nosize error is %+v, want DEVICE_NOT_READY naming the screen's size", e)
			}
			return ""
		}},
		{"refused malformed", "droidyard-reader: refused malformed", func(got readerAct, err error) string {
			if err != nil || got.kind != readerActRefused || got.refusal != refusalMalformed {
				return fmt.Sprintf("malformed = %+v, %v, want a refusal that is malformed", got, err)
			}
			if e := capability.AsError(actRefusedError(got, what)); e == nil || e.Code != ErrNotReady {
				return fmt.Sprintf("the malformed error is %v, want an error, not a success", e)
			}
			return ""
		}},
		{"timeout", "droidyard-reader: timeout 3000", func(got readerAct, err error) string {
			if err != nil || got.kind != readerActUnanswered {
				return fmt.Sprintf("timeout = %+v, %v, want may-have-applied", got, err)
			}
			return ""
		}},
	}
	for i, row := range rows {
		r.SetActAnswers(row.answer)
		got, err := send(ac)
		if msg := row.check(got, err); msg != "" {
			t.Errorf("%s: %s", row.name, msg)
		}
		acts := r.Acts()
		if len(acts) != i+1 || acts[i] != wantLine {
			t.Errorf("%s: the reader received %q, want the last request to be %q", row.name, acts, wantLine)
		}
	}
}

// Decision (ADR-0035 §3; ADR-0020's 2026-10-06 line): a gesture or a press the reader took and
// never answered (EOF after the write, the reader's acting thread wedged) may have applied: it
// is not an error and not a refusal, and exactly one request reached the reader, so nothing
// is injected twice. Seen failing with the unanswered case returned as an error.
func TestUnansweredGestureAndPressMayHaveApplied(t *testing.T) {
	oldBase, oldMargin := readerActBoundBase, readerActMargin
	readerActBoundBase, readerActMargin = 0, 300*time.Millisecond
	t.Cleanup(func() { readerActBoundBase, readerActMargin = oldBase, oldMargin })

	f, r := actingReader(t)
	ac := f.adb.newActor(context.Background(), readerTarget(t, f.adb, uiSerial))
	if !ac.ok {
		t.Fatal("the fake reader is not ready")
	}
	r.SetActAnswers(devicetest.ReaderGone)
	got, err := ac.gesture(context.Background(), []pathPoint{{0.5, 0, 0}, {0.5, 0.6, 100 * time.Millisecond}})
	if err != nil || got.kind != readerActUnanswered {
		t.Errorf("an unanswered gesture = %+v, %v, want may-have-applied", got, err)
	}
	got, err = ac.press(context.Background(), 4)
	if err != nil || got.kind != readerActUnanswered {
		t.Errorf("an unanswered press = %+v, %v, want may-have-applied", got, err)
	}
	if n := len(r.Acts()); n != 2 {
		t.Errorf("the reader received %d requests, want one for each call", n)
	}
}

// Decision (plan m3g step 2): the reader's <hierarchy> carries the real screen's size,
// rotation applied, and the parser keeps it apart from `display`, which is the root
// window's extent: with a dialog on top, display is the dialog's and the screen is still
// 1080x2400, so a path can be normalised against the screen. A document with no size (a
// `uiautomator dump`), or one whose values are not sizes (zero, a unit suffix, a negative, a
// float), gives none rather than a wrong one. Seen failing on the parser before this step
// (no field), and with the dialog's extent taken for the screen.
func TestScreenSizeFromTheReader(t *testing.T) {
	doc := func(attrs string) []byte {
		return []byte(`<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0"` + attrs + `>` +
			`<node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="android" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[132,800][948,1600]">` +
			`<node index="0" text="Close app" resource-id="android:id/aerr_close" class="android.widget.Button" package="android" content-desc="" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[200,1400][500,1500]"></node>` +
			`</node></hierarchy>`)
	}
	s, err := BuildSnapshot(doc(` screenWidth="1080" screenHeight="2400"`), "s-1", DefaultUITreeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if s.Display != (UIDisplay{Width: 948, Height: 1600}) {
		t.Errorf("display %+v, want the dialog's extent 948x1600", s.Display)
	}
	if s.screen != [2]int{1080, 2400} {
		t.Errorf("screen %v, want 1080x2400", s.screen)
	}

	none := map[string]string{
		"a uiautomator dump": ``,
		"zero width":         ` screenWidth="0" screenHeight="2400"`,
		"a unit suffix":      ` screenWidth="2147483647x" screenHeight="2400"`,
		"a negative height":  ` screenWidth="1080" screenHeight="-2400"`,
		"a float":            ` screenWidth="1080.0" screenHeight="2400"`,
		"one attribute only": ` screenWidth="1080"`,
		"a value too long":   ` screenWidth="99999999999999999999" screenHeight="2400"`,
		"empty":              ` screenWidth="" screenHeight=""`,
		"a sign":             ` screenWidth="+1080" screenHeight="2400"`,
		"zero both":          ` screenWidth="0" screenHeight="0"`,
	}
	for name, attrs := range none {
		s, err := BuildSnapshot(doc(attrs), "s-1", DefaultUITreeBytes)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.screen != [2]int{} {
			t.Errorf("%s: screen %v, want none", name, s.screen)
		}
		if s.Display != (UIDisplay{Width: 948, Height: 1600}) {
			t.Errorf("%s: display %+v changed", name, s.Display)
		}
	}
}
