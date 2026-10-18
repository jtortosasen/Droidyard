package device

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// FLAG_SECURE (plan m2p step 8; ADR-0080) over step 1's recorded `dumpsys window` outputs
// (testdata of package screen) and the re-executed fake adb.

func secureDump(t *testing.T, level, state string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("screen", "testdata", "dumpsys-window-api"+level+"-"+state+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Decision (ADR-0080 §1): the check reads the recorded outputs, on and off, of both API
// levels: the toggle's window is the focused one and carries SECURE only in the "on" runs.
func TestParseSecureFocusRecorded(t *testing.T) {
	for _, level := range []string{"31", "36"} {
		for state, want := range map[string]bool{"on": true, "off": false} {
			got, err := parseSecureFocus(secureDump(t, level, state))
			if err != nil || got != want {
				t.Errorf("api %s %s: %v, %v; want %v", level, state, got, err, want)
			}
		}
	}
}

// Decision (ADR-0080 §1): only the focused window's block is read; another window's SECURE
// is ignored, and a focused window's SECURE is found though other windows lack it. Both are
// built from the recordings. Seen failing with the parser searching the whole output for
// the SECURE flag: "api 31 other window secure: true, <nil>; want false" (the first
// window's fl= line given SECURE in the "off" output).
func TestParseSecureFocusReadsOnlyTheFocusedWindow(t *testing.T) {
	for _, level := range []string{"31", "36"} {
		off := secureDump(t, level, "off")
		first := "fl=NOT_FOCUSABLE NOT_TOUCH_MODAL LAYOUT_IN_SCREEN SPLIT_TOUCH FLAG_SLIPPERY"
		if level == "36" {
			first = "fl=NOT_FOCUSABLE NOT_TOUCHABLE NOT_TOUCH_MODAL LAYOUT_IN_SCREEN FLAG_SLIPPERY"
		}
		if !strings.Contains(off, first) {
			t.Fatalf("api %s: the recording no longer holds %q", level, first)
		}
		other := strings.Replace(off, first, first+" SECURE", 1)
		if got, err := parseSecureFocus(other); err != nil || got {
			t.Errorf("api %s other window secure: %v, %v; want false", level, got, err)
		}

		// The focused window not secure while the status bar is: the "on" output with the
		// flag moved from the focused block to the first one.
		on := secureDump(t, level, "on")
		moved := strings.Replace(strings.Replace(on, " SECURE", "", 1), first, first+" SECURE", 1)
		if strings.Count(moved, "SECURE") != 1 {
			t.Fatalf("api %s: the synthetic output holds %d SECURE", level, strings.Count(moved, "SECURE"))
		}
		if got, err := parseSecureFocus(moved); err != nil || got {
			t.Errorf("api %s flag moved to another window: %v, %v; want false", level, got, err)
		}
	}
}

// Decision (ADR-0080 §1): an output without a readable focus, block or flags is an error,
// not "not secure", so a failed read changes nothing; flags are whole words.
func TestParseSecureFocusUnreadable(t *testing.T) {
	on := secureDump(t, "31", "on")
	cases := map[string]string{
		"empty":         "",
		"no focus":      strings.Replace(on, "mCurrentFocus=Window{252d9a2", "mCurrentFocus=null{", 1),
		"focus unknown": strings.Replace(on, "mCurrentFocus=Window{252d9a2", "mCurrentFocus=Window{aaaaaaa", 1),
	}
	for name, out := range cases {
		if got, err := parseSecureFocus(out); err == nil {
			t.Errorf("%s: %v with no error", name, got)
		}
	}
	notWord := strings.Replace(on, "LAYOUT_IN_SCREEN SECURE LAYOUT_INSET_DECOR", "LAYOUT_IN_SCREEN NOT_SECURE_AT_ALL LAYOUT_INSET_DECOR", 1)
	if got, err := parseSecureFocus(notWord); err != nil || got {
		t.Errorf("a flag containing SECURE: %v, %v; want false", got, err)
	}
}

// Decision (ADR-0080 §2): a capture that succeeds with a secure window focused carries the
// warning; with an insecure one, or a check that fails or cannot be parsed, none. The
// warning names no package. Seen failing with the check not run (warning never added):
// "secure: warnings []".
func TestScreenshotWarnsWhenTheFocusedWindowIsSecure(t *testing.T) {
	png := fixture(t, "pixel9a-screencap.png")
	cases := []struct {
		name  string
		extra map[string]string
		warn  bool
	}{
		{"secure", map[string]string{uiSerial + ".dumpsyswindow.out": secureDump(t, "36", "on")}, true},
		{"insecure", map[string]string{uiSerial + ".dumpsyswindow.out": secureDump(t, "36", "off")}, false},
		{"check fails", map[string]string{uiSerial + ".dumpsyswindow.err": "boom\n", uiSerial + ".dumpsyswindow.exit": "1"}, false},
		{"unparsable", map[string]string{uiSerial + ".dumpsyswindow.out": "nothing useful\n"}, false},
	}
	for _, c := range cases {
		files := map[string]string{uiSerial + ".screencap.out": png}
		for k, v := range c.extra {
			files[k] = v
		}
		out := shotFixture(t, files).shoot(t, uiSerial)
		if out.State != capability.StateCompleted {
			t.Fatalf("%s: %s, %+v", c.name, out.State, out.Error)
		}
		w := out.Result.(ScreenshotResult).Warnings
		if c.warn {
			if len(w) != 1 || w[0] != secureWarning || strings.Contains(w[0], "formapp") {
				t.Errorf("%s: warnings %q, want [%q]", c.name, w, secureWarning)
			}
		} else if len(w) != 0 {
			t.Errorf("%s: warnings %q, want none", c.name, w)
		}
	}
}

// Decision (plan m2p §Changes, step 8): on API 31 screencap with a secure window focused
// exits 1 with no output; the check runs also when the capture fails, and the failure's
// cause then says the window is secure. An insecure window, or no answer, adds nothing.
// Seen failing with the check run only after a capture: "cause ... does not say it".
func TestScreenshotFailureSaysTheFocusedWindowIsSecure(t *testing.T) {
	failing := map[string]string{uiSerial + ".screencap.out": "", uiSerial + ".screencap.exit": "1"}
	with := func(extra map[string]string) *capability.Error {
		files := map[string]string{}
		for k, v := range failing {
			files[k] = v
		}
		for k, v := range extra {
			files[k] = v
		}
		return mustFail(t, shotFixture(t, files).shoot(t, uiSerial), ErrScreenshotFailed)
	}
	ce := with(map[string]string{uiSerial + ".dumpsyswindow.out": secureDump(t, "31", "on")})
	if !strings.Contains(ce.Cause, secureCause) || !strings.Contains(ce.Cause, "status 1") || strings.Contains(ce.Cause, "formapp") {
		t.Errorf("secure: cause %q", ce.Cause)
	}
	for name, extra := range map[string]map[string]string{
		"insecure": {uiSerial + ".dumpsyswindow.out": secureDump(t, "31", "off")},
		"no check": nil,
	} {
		if ce := with(extra); strings.Contains(ce.Cause, "secure") {
			t.Errorf("%s: cause %q", name, ce.Cause)
		}
	}
}

// secureEvent waits for the subscription's next event and requires a state event.
func secureEvent(t *testing.T, sub *event.Subscription) ScreenStateEvent {
	t.Helper()
	e := next(t, sub)
	if e.Topic != TopicScreen || e.Type != EventState {
		t.Fatalf("event %s/%s, want %s/%s", e.Topic, e.Type, TopicScreen, EventState)
	}
	var se ScreenStateEvent
	if err := json.Unmarshal(e.Payload, &se); err != nil {
		t.Fatal(err)
	}
	if se.Device != uiSerial {
		t.Errorf("state names device %q, want %q", se.Device, uiSerial)
	}
	return se
}

// Decision (ADR-0080 §3): the source polls the check while it runs and publishes state at
// the start and on each change, never on a repeat. The fake answers off, on, on, then off
// for good: three events (false, true, false) and no fourth. A viewer that joins later
// hears the current state at once. Seen failing with every answer published: a second
// "true" arrives where the test expects "false".
func TestScreenSourcePublishesSecureStateOnChange(t *testing.T) {
	old := secureInterval
	secureInterval = 25 * time.Millisecond
	t.Cleanup(func() { secureInterval = old })
	off, on := secureDump(t, "31", "off"), secureDump(t, "31", "on")
	w := func() *wsFix {
		f := startedFixture(t, map[string]string{
			uiSerial + ".dumpsyswindow#1.out": off,
			uiSerial + ".dumpsyswindow#2.out": on,
			uiSerial + ".dumpsyswindow#3.out": on,
			uiSerial + ".dumpsyswindow.out":   off,
		})
		bus := event.New(event.Options{})
		f.adb.SetBus(bus)
		w := &wsFix{ssFix: f, bus: bus, sess: event.NewSession(bus, "s1")}
		t.Cleanup(func() {
			w.sess.Close()
			ctx, cancel := ssCtx()
			defer cancel()
			f.adb.screenViews.StopAll(ctx)
		})
		return w
	}()
	_, sub := w.watch(t)
	for i, want := range []bool{false, true, false} {
		if got := secureEvent(t, sub).Secure; got != want {
			t.Fatalf("state event %d: secure %v, want %v", i+1, got, want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if e, err := sub.Next(ctx); err == nil {
		t.Fatalf("a fourth event %s/%s with the answer unchanged", e.Topic, e.Type)
	}

	// A second viewer hears the known state at once, though no change follows.
	sess2 := event.NewSession(w.bus, "s2")
	defer sess2.Close()
	out := w.watchOn(t, sess2)
	if out.State != capability.StateCompleted {
		t.Fatalf("second watch: %s, %+v", out.State, out.Error)
	}
	sub2, _ := sess2.Subscription(out.Result.(WatchScreenResult).SubscriptionID)
	if secureEvent(t, sub2).Secure {
		t.Error("the second viewer was told the window is secure")
	}
}
