package device

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Decision (plan m1n step 5, fix round L1; ui-automation.md §Typing): the guard reads whether
// the soft keyboard is up from `dumpsys input_method`'s `mInputShown` line, and a read that
// carries no such line is "cannot answer" (ok false), never "not shown", so an unparsed read
// cannot hide a keyboard the guard would otherwise have hidden.
func TestParseInputMethodShown(t *testing.T) {
	cases := []struct {
		name        string
		out         string
		shown, want bool
	}{
		{"shown", "  InputMethodService\n  mInputShown=true\n", true, true},
		{"not shown", "  mInputShown=false\n", false, true},
		{"no line", "  InputMethodService\n", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			shown, ok := parseInputMethodShown(c.out)
			if ok != c.want {
				t.Fatalf("parseInputMethodShown ok = %v, want %v", ok, c.want)
			}
			if ok && shown != c.shown {
				t.Errorf("shown = %v, want %v", shown, c.shown)
			}
		})
	}
}

// imeWindowDump is the IME window's touchable region as `dumpsys window windows` prints it
// (the line observed at level 5 on API 31, plan m1n step 5, fix round L1).
const imeWindowDump = "WINDOW MANAGER WINDOWS\n" +
	"  Window #3 Window{abc u0 InputMethod}:\n" +
	"    mTouchableInsets=3\n" +
	"    touchable region=SkRegion((0,1512,1080,2208))\n"

// Decision (plan m1n step 5, fix round L1): the IME's touchable region is read from the
// `touchable region=SkRegion((l,t,r,b))` line inside the InputMethod window's block only; a
// region line in some other window's block is not the keyboard's, so the guard is not fooled
// by an unrelated region.
func TestParseIMETouchableRegion(t *testing.T) {
	r, ok := parseIMETouchableRegion(imeWindowDump)
	if !ok || r != (rect{l: 0, t: 1512, r: 1080, b: 2208}) {
		t.Errorf("region = %+v, ok = %v, want {0 1512 1080 2208}, true", r, ok)
	}
	other := "WINDOW MANAGER WINDOWS\n  Window #0 Window{abc u0 SomeApp}:\n    touchable region=SkRegion((0,0,10,10))\n"
	if _, ok := parseIMETouchableRegion(other); ok {
		t.Errorf("a region outside the InputMethod window was taken as the keyboard's")
	}
	if _, ok := parseIMETouchableRegion("WINDOW MANAGER WINDOWS\n"); ok {
		t.Errorf("a dump with no InputMethod or region reported a region")
	}
}

// kbNode is a form-app node with explicit bounds, so a test can place a field or a target
// above or inside the soft keyboard's touchable region.
func kbNode(class, id, extra, bounds string) string {
	return fmt.Sprintf(`<node class="%s" package=%q resource-id="%s:id/%s" %s bounds="%s"></node>`,
		class, tapPackage, tapPackage, id, extra, bounds)
}

// kbField is a focused editable input field, the shape of a soft keyboard being up (plan m1n
// step 5, fix round L1): only the reader writes the editable flag, so the guard runs only on
// the reader path.
func kbField(bounds string) string {
	return kbNode("android.widget.EditText", "name", `focused="true" editable="true"`, bounds)
}

// kbLike is the tap target, a plain clickable node the test places where it likes.
func kbLike(bounds string) string {
	return kbNode("android.widget.Button", "like", `clickable="true"`, bounds)
}

// Decision (plan m1n step 5, fix round L1; ui-automation.md §Typing): when the before tree
// shows a focused editable node and the soft keyboard's touchable region contains the
// target's tap point, the guard hides the keyboard with one BACK, re-reads the tree,
// re-resolves the same selector and continues — the tap lands at the re-resolved point, and
// the result carries the soft-keyboard warning. Seen failing with the tap sent at the covered
// point (which at level 5 typed a character into the field the caller meant to leave).
func TestTapElementHidesTheSoftKeyboardOverTheTarget(t *testing.T) {
	quickAct(t)
	before := tappable(kbField("[0,0][200,200]"), kbLike("[100,1600][200,1700]")) // centre (150,1650), inside the region
	after := tappable(kbField("[0,0][200,200]"), kbLike("[100,1000][200,1100]"))  // centre (150,1050), above it
	files, r := withPersistentReaderR(t, map[string]string{
		uiSerial + ".dumpsysinputmethod.out":   "  mInputShown=true\n",
		uiSerial + ".dumpsyswindowwindows.out": imeWindowDump,
		uiSerial + ".dumpsysinputmethod#2.out": "  mInputShown=false\n",
	}, before, after)
	f := newUIFixture(t, files)

	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	res := mustTap(t, out)
	if n := countInput(f.calls(t), "input keyevent 4"); n != 1 {
		t.Errorf("%d BACK keys, want exactly one to hide the keyboard (%q)", n, f.calls(t))
	}
	if res.Tapped.Point != [2]int{150, 1050} {
		t.Errorf("point = %v, want the re-resolved point (150,1050)", res.Tapped.Point)
	}
	warned := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "soft keyboard") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %q, want one naming the soft keyboard", res.Warnings)
	}
	if got := r.Acts(); len(got) != 1 || got[0] != "tap 150 1050" {
		t.Errorf("the reader acts = %q, want [tap 150 1050]", got)
	}
	if n := count(f.calls(t), "-s "+uiSerial+" shell dumpsys window windows"); n != 1 {
		t.Errorf("%d IME-region reads, want one (%q)", n, f.calls(t))
	}
}

// Decision (plan m1n step 5, fix round L1): a target whose tap point lies outside the
// keyboard's region is tapped where it is, with no BACK and no re-read — the guard touches
// nothing the keyboard does not cover.
func TestTapElementLeavesAnUncoveredKeyboardAlone(t *testing.T) {
	quickAct(t)
	doc := tappable(kbField("[0,0][200,200]"), kbLike("[100,800][200,900]")) // centre (150,850), above the region
	files, r := withPersistentReaderR(t, map[string]string{
		uiSerial + ".dumpsysinputmethod.out":   "  mInputShown=true\n",
		uiSerial + ".dumpsyswindowwindows.out": imeWindowDump,
	}, doc)
	f := newUIFixture(t, files)

	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	res := mustTap(t, out)
	if n := countInput(f.calls(t), "input keyevent 4"); n != 0 {
		t.Errorf("%d BACK keys for a target above the keyboard, want none (%q)", n, f.calls(t))
	}
	if res.Tapped.Point != [2]int{150, 850} {
		t.Errorf("point = %v, want the target's own point (150,850)", res.Tapped.Point)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "soft keyboard") {
			t.Errorf("warnings = %q, want no soft-keyboard warning when nothing was covered", res.Warnings)
		}
	}
	if got := r.Acts(); len(got) != 1 || got[0] != "tap 150 850" {
		t.Errorf("the reader acts = %q, want [tap 150 850]", got)
	}
}

// Decision (plan m1n step 5, fix round L1): when `dumpsys input_method` reports the keyboard
// not shown, the guard stops before reading the IME's region and issues no BACK — the
// region read is not spent on a keyboard that is down.
func TestTapElementSkipsTheKeyboardGuardWhenNotShown(t *testing.T) {
	quickAct(t)
	doc := tappable(kbField("[0,0][200,200]"), kbLike("[100,1600][200,1700]")) // inside the region, but no keyboard
	files, r := withPersistentReaderR(t, map[string]string{
		uiSerial + ".dumpsysinputmethod.out": "  mInputShown=false\n",
	}, doc)
	f := newUIFixture(t, files)

	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	res := mustTap(t, out)
	if n := countInput(f.calls(t), "input keyevent 4"); n != 0 {
		t.Errorf("%d BACK keys with the keyboard down, want none (%q)", n, f.calls(t))
	}
	if n := count(f.calls(t), "-s "+uiSerial+" shell dumpsys window windows"); n != 0 {
		t.Errorf("%d IME-region reads with the keyboard down, want none (%q)", n, f.calls(t))
	}
	if res.Tapped.Point != [2]int{150, 1650} {
		t.Errorf("point = %v, want the target's own point (150,1650)", res.Tapped.Point)
	}
	if got := r.Acts(); len(got) != 1 || got[0] != "tap 150 1650" {
		t.Errorf("the reader acts = %q, want [tap 150 1650]", got)
	}
}

// Decision (plan m1n step 5, fix round L1): a read that cannot answer — no `mInputShown` line,
// or no InputMethod block — leaves the guard behaving as it did before this fix: the target
// is tapped where it is, with no BACK. A read failure never fails or changes a call the
// earlier steps allowed.
func TestTapElementKeyboardGuardIsSilentWhenTheReadCannotAnswer(t *testing.T) {
	quickAct(t)
	doc := tappable(kbField("[0,0][200,200]"), kbLike("[100,1600][200,1700]"))
	files, r := withPersistentReaderR(t, map[string]string{
		uiSerial + ".dumpsysinputmethod.out": "  InputMethodService\n", // no mInputShown line
	}, doc)
	f := newUIFixture(t, files)

	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	res := mustTap(t, out)
	if n := countInput(f.calls(t), "input keyevent 4"); n != 0 {
		t.Errorf("%d BACK keys after an unreadable input_method, want none (%q)", n, f.calls(t))
	}
	if res.Tapped.Point != [2]int{150, 1650} {
		t.Errorf("point = %v, want the target's own point (150,1650)", res.Tapped.Point)
	}
	if got := r.Acts(); len(got) != 1 || got[0] != "tap 150 1650" {
		t.Errorf("the reader acts = %q, want [tap 150 1650]", got)
	}
}

// Decision (plan m1n step 5, fix round L1): device.inputText's focusing tap takes the same
// guard, and a target the keyboard still covers after it was hidden fails
// DEVICE_ELEMENT_OFFSCREEN naming typing — never a tap on a key or a text typed into the
// wrong place. Seen failing with the tap sent at the covered point and the text delivered
// into whatever field the key press landed in.
func TestInputTextRefusesATargetTheKeyboardStillCovers(t *testing.T) {
	quickAct(t)
	doc := tappable(kbField("[100,1600][200,1700]")) // the field's own centre is inside the region
	files, r := withPersistentReaderR(t, map[string]string{
		uiSerial + ".dumpsysinputmethod.out":   "  mInputShown=true\n",
		uiSerial + ".dumpsysinputmethod#2.out": "  mInputShown=true\n",
		uiSerial + ".dumpsyswindowwindows.out": imeWindowDump,
	}, doc, doc)
	f := newUIFixture(t, files)

	out := f.inputCall(t, `{"device":"emulator-5554","text":"Ana","target":{"id":"name"}}`)
	ce := mustFail(t, out, ErrElementOffscreen)
	if !strings.Contains(ce.Message, "typed") {
		t.Errorf("message %q does not name typing", ce.Message)
	}
	if n := countInput(f.calls(t), "input keyevent 4"); n != 1 {
		t.Errorf("%d BACK keys, want exactly one (%q)", n, f.calls(t))
	}
	if got := r.Acts(); len(got) != 0 {
		t.Errorf("the reader received %q, want none: the target was never reached", got)
	}
	if got := inputTextArgs(f.calls(t)); len(got) != 0 {
		t.Errorf("the device was told to type %q, want nothing", got)
	}
}

// Decision (plan m1n step 5, fix-round review K1): the keyboard reported shown but its
// region unreadable (no InputMethod block, or a region in another shape) behaves as today:
// no BACK, the target's own point tapped, no soft-keyboard warning. Seen failing with the
// guard's `!ok || !region.contains(point)` turned into `ok && !region.contains(point)`.
func TestTapElementKeyboardGuardSendsNoBackWhenTheRegionIsUnreadable(t *testing.T) {
	for name, windows := range map[string]string{
		"no InputMethod block":       "WINDOW MANAGER WINDOWS\n  Window #1 Window{abc u0 dev.droidyard.formapp/.FormActivity}:\n    touchable region=SkRegion((0,0,1080,2340))\n",
		"a region of two rectangles": "WINDOW MANAGER WINDOWS\n  Window #7 Window{9e04b92 u0 InputMethod}:\n    touchable region=SkRegion((0,1512,1080,1800)(0,1800,1080,2208))\n",
	} {
		t.Run(name, func(t *testing.T) {
			quickAct(t)
			doc := tappable(kbField("[0,0][200,200]"), kbLike("[100,1600][200,1700]"))
			files, r := withPersistentReaderR(t, map[string]string{
				uiSerial + ".dumpsysinputmethod.out":   "  mInputShown=true\n",
				uiSerial + ".dumpsyswindowwindows.out": windows,
			}, doc)
			f := newUIFixture(t, files)

			out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
			res := mustTap(t, out)
			if n := countInput(f.calls(t), "input keyevent 4"); n != 0 {
				t.Errorf("%d BACK keys with an unreadable region, want none (%q)", n, f.calls(t))
			}
			if res.Tapped.Point != [2]int{150, 1650} {
				t.Errorf("point = %v, want the target's own point (150,1650)", res.Tapped.Point)
			}
			for _, w := range res.Warnings {
				if strings.Contains(w, "soft keyboard") {
					t.Errorf("warnings = %q, want no soft-keyboard warning", res.Warnings)
				}
			}
			if got := r.Acts(); len(got) != 1 || got[0] != "tap 150 1650" {
				t.Errorf("the reader acts = %q, want [tap 150 1650]", got)
			}
		})
	}
}

// Decision (plan m2c, S8): a failure that follows the guard's BACK says a BACK was sent.
// The steps' own wording ("nothing on the device was changed", "this call is safe to
// repeat") is untrue once the BACK is on the device (it goes back a screen if the keyboard had
// already closed), so the error names the BACK and drops those claims. Seen failing with the
// errors passed through, as before this fix. The tap and the typing both run the guard.
func TestKeyboardBackFailureSaysBackWasSent(t *testing.T) {
	quickAct(t)
	other := kbNode("android.widget.TextView", "other", "", "[0,0][10,10]")
	// For a tap the target is a button under the keyboard; for typing it is the field itself.
	tapBefore := tappable(kbField("[0,0][200,200]"), kbLike("[100,1600][200,1700]"))
	tapGone := tappable(kbField("[0,0][200,200]"), other)
	inBefore := tappable(kbField("[100,1600][200,1700]"))
	inGone := tappable(other)
	untrue := []string{"safe to repeat", "nothing on the device was changed"}
	tapCall := func(f *uiFixture, t *testing.T) capability.Outcome {
		return f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	}
	inCall := func(f *uiFixture, t *testing.T) capability.Outcome {
		return f.inputCall(t, `{"device":"emulator-5554","text":"Ana","target":{"id":"name"}}`)
	}
	for _, tc := range []struct {
		name          string
		before, after string
		stillShown    bool // the keyboard is still reported shown after the BACK
		code          capability.ErrorCode
		call          func(f *uiFixture, t *testing.T) capability.Outcome
		what          string
	}{
		{"tap, target gone after the BACK", tapBefore, tapGone, false, ErrElementNotFound, tapCall, "tapped"},
		{"tap, still covered", tapBefore, tapBefore, true, ErrElementOffscreen, tapCall, "tapped"},
		{"input, target gone after the BACK", inBefore, inGone, false, ErrElementNotFound, inCall, "typed"},
		{"input, still covered", inBefore, inBefore, true, ErrElementOffscreen, inCall, "typed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shownAfter := "  mInputShown=false\n"
			if tc.stillShown {
				shownAfter = "  mInputShown=true\n"
			}
			files, _ := withPersistentReaderR(t, map[string]string{
				uiSerial + ".dumpsysinputmethod.out":   "  mInputShown=true\n",
				uiSerial + ".dumpsysinputmethod#2.out": shownAfter,
				uiSerial + ".dumpsyswindowwindows.out": imeWindowDump,
			}, tc.before, tc.after, tc.after)
			f := newUIFixture(t, files)
			ce := mustFail(t, tc.call(f, t), tc.code)
			if n := countInput(f.calls(t), "input keyevent 4"); n != 1 {
				t.Fatalf("%d BACK keys, want the one the claim is about (%q)", n, f.calls(t))
			}
			text := ce.Message + " | " + ce.NextStep
			if !strings.Contains(text, "BACK key event") {
				t.Errorf("the failure does not say a BACK was sent: %s", text)
			}
			if !strings.Contains(ce.NextStep, "nothing was "+tc.what) {
				t.Errorf("the next step lost the true claim that nothing was %s: %s", tc.what, ce.NextStep)
			}
			for _, claim := range untrue {
				if strings.Contains(text, claim) {
					t.Errorf("the failure still claims %q after a BACK was sent: %s", claim, text)
				}
			}
		})
	}

	// The wrapper: the before-phase budget's wording is cut, a cancellation passes through.
	budget := phaseError(context.Background(), context.DeadlineExceeded, ErrUIDumpFailed, "the UI could not be re-read")
	got := afterKeyboardBack(budget, "tapped").(*capability.Error)
	if strings.Contains(got.NextStep, "nothing on the device was changed") || !strings.Contains(got.NextStep, "BACK key event") || got.Code != ErrUIDumpFailed {
		t.Errorf("a budget failure after the BACK: %+v", got)
	}
	// Guidance after the claim's sentence survives the cut.
	dump := afterKeyboardBack(dumpFailed("it exited"), "tapped").(*capability.Error)
	if !strings.Contains(dump.NextStep, "the screen may be animating") || strings.Contains(dump.NextStep, "nothing on the device was changed") {
		t.Errorf("the dump failure's next step after the BACK: %s", dump.NextStep)
	}
	if err := afterKeyboardBack(context.Canceled, "tapped"); err != context.Canceled {
		t.Errorf("a cancellation was changed: %v", err)
	}
}
