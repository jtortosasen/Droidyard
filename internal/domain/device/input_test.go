package device

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

const inputTextHead = "-s " + uiSerial + " shell input text "

func (f *uiFixture) inputCall(t *testing.T, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.inputText", Params: []byte(params)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return f.wait(t, id)
}

func mustInput(t *testing.T, out capability.Outcome) InputTextResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(InputTextResult)
	if !ok {
		t.Fatalf("result is %T, want InputTextResult", out.Result)
	}
	return r
}

// inputTextArgs is the device-shell argument of each `input text` invocation.
func inputTextArgs(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, inputTextHead) {
			out = append(out, strings.TrimPrefix(c, inputTextHead))
		}
	}
	return out
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Decision (plan m1f step 9, check; R6): the text reaches the device shell
// single-quoted, with spaces as %s, with a literal %s split across two invocations and
// apostrophes escaped, so no character a caller sends can break out of the argument.
// Seen failing with spaces unescaped, %s unsplit and the quoting removed.
func TestInputTextEscapesForTheDeviceShell(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"space", "a b", []string{"'a%sb'"}},
		{"apostrophe", "it's", []string{"'it'\\''s'"}},
		{"dollar", "$HOME", []string{"'$HOME'"}},
		{"semicolon", ";reboot", []string{"';reboot'"}},
		{"percent-s", "50%s", []string{"'50%'", "'s'"}},
		{"double-quote", `"q"`, []string{`'"q"'`}},
		{"backslash", `\`, []string{`'\'`}},
		{"backtick", "\u0060", []string{"'\u0060'"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quickAct(t)
			f := newUIFixture(t, nil)
			out := f.inputCall(t, `{"device":"emulator-5554","text":`+jsonString(t, c.text)+`}`)
			r := mustInput(t, out)
			if r.Method != inputTextMethodIn || r.Characters != utf8.RuneCountInString(c.text) {
				t.Fatalf("result = %+v, want method %s and %d characters", r, inputTextMethodIn, utf8.RuneCountInString(c.text))
			}
			if got := inputTextArgs(f.calls(t)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("input text arguments = %q, want %q", got, c.want)
			}
		})
	}
}

// Decision (plan m1f step 9, check; D8; reversal): non-ASCII without ADBKeyboard is
// DEVICE_TEXT_NOT_DELIVERABLE naming the ime, with nothing tapped or typed. Seen failing
// with deliverability checked after the tap (the tap would appear in calls.log).
func TestInputTextNonASCIIWithoutADBKeyboardIsNotDeliverable(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, withPersistentReader(t, nil, tappable(tapNode("android.widget.EditText", "name", "old", ""))))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"ñ","target":{"id":"name"}}`)
	ce := mustFail(t, out, ErrTextNotDeliverable)
	if !strings.Contains(ce.NextStep, "ADBKeyboard") {
		t.Errorf("nextStep %q does not name ADBKeyboard", ce.NextStep)
	}
	for _, c := range f.calls(t) {
		if strings.Contains(c, " shell input ") {
			t.Errorf("an undeliverable text issued %q", c)
		}
	}
}

// Decision (plan m1f step 9, check; D8): with ADBKeyboard selected, non-ASCII goes out
// as one ADB_INPUT_B64 broadcast carrying the base64, and the result says adbkeyboard.
func TestInputTextADBKeyboardBroadcastsBase64(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{uiSerial + ".inputmethod.out": adbKeyboardIME + "\n"})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"ñ"}`)
	r := mustInput(t, out)
	if r.Method != inputTextMethodIME || r.Characters != 1 {
		t.Fatalf("result = %+v, want method %s and 1 character", r, inputTextMethodIME)
	}
	want := "-s " + uiSerial + " shell am broadcast -a ADB_INPUT_B64 --es msg " + base64.StdEncoding.EncodeToString([]byte("ñ"))
	if n := count(f.calls(t), want); n != 1 {
		t.Errorf("%d broadcasts, want one carrying the base64", n)
	}
	if n := len(inputTextArgs(f.calls(t))); n != 0 {
		t.Errorf("%d input text invocations, want none for the adbkeyboard path", n)
	}
}

// Decision (plan m1f step 9, check; D9): clear on API 33+ is select-all then delete;
// below, move-to-end then one delete per character of the node's text.
func TestInputTextClearByAPILevel(t *testing.T) {
	cases := []struct {
		name string
		sdk  string
		want []string
	}{
		{"api 34", "34", []string{
			"-s " + uiSerial + " shell input keycombination 113 29",
			"-s " + uiSerial + " shell input keyevent 67",
		}},
		{"api 30", "30", []string{
			"-s " + uiSerial + " shell input keyevent 123 67 67 67",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quickAct(t)
			doc := tappable(tapNode("android.widget.EditText", "name", "old", ""))
			f := newUIFixture(t, withPersistentReader(t, map[string]string{uiSerial + ".sdk.out": c.sdk + "\n"}, doc))
			out := f.inputCall(t, `{"device":"emulator-5554","text":"x","target":{"id":"name"},"clear":true}`)
			mustInput(t, out)
			for _, want := range c.want {
				if n := count(f.calls(t), want); n != 1 {
					t.Errorf("%d of %q, want one; calls: %q", n, want, f.calls(t))
				}
			}
		})
	}
}

// Decision (plan m1f step 9, check; D9): clear without a target is REQUEST_INVALID_PARAMS
// before the device is touched.
func TestInputTextClearWithoutTargetIsInvalid(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, nil)
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","clear":true}`)
	mustFail(t, out, capability.ErrInvalidParams)
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("calls = %q, want none", calls)
	}
}

// Decision (plan m1f step 9, check; D9): submit presses ENTER after typing.
func TestInputTextSubmitPressesEnter(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, nil)
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","submit":true}`)
	mustInput(t, out)
	if n := count(f.calls(t), "-s "+uiSerial+" shell input keyevent 66"); n != 1 {
		t.Errorf("%d ENTER keys, want one", n)
	}
}

// Decision (plan m1f step 9 item 5, reversal): an ENTER whose typing phase had already
// elapsed is reported as not run, not as "may not have happened", and issues no command.
// Seen failing reversed, where the warning said the command may have happened.
func TestSubmitAfterTheTypingPhaseWarnsItDidNotRun(t *testing.T) {
	f := newUIFixture(t, nil)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	w := f.adb.submit(ctx, &target{adb: "/nonexistent/adb", serial: uiSerial})
	if w == "" {
		t.Fatal("no warning for an ENTER after the typing phase elapsed")
	}
	if !strings.Contains(w, "did not run") {
		t.Errorf("warning %q does not say submit did not run", w)
	}
	if strings.Contains(w, "may not have happened") {
		t.Errorf("warning %q still says may not have happened; the command was never issued", w)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("calls = %q, want none: nothing was issued", calls)
	}
}

// Decision (plan m1f step 9, check; D7): expect without target still takes a before read
// and flags a condition already true before typing as vacuous.
func TestInputTextExpectWithoutTargetReadsBeforeAndFlagsVacuous(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLikes("Likes: 0"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc, doc))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","expect":[{"appears":{"text":"Likes: 0"}}]}`)
	r := mustInput(t, out)
	if r.Expect == nil || !r.Expect.Matched {
		t.Fatalf("expect = %+v, want matched", r.Expect)
	}
	if len(r.Expect.Vacuous) != 1 {
		t.Errorf("vacuous = %+v, want one condition", r.Expect.Vacuous)
	}
}

// Decision (plan m1f step 9, check; D13; reversal): the clock is the authorization
// hook's start, so a hook delayed in `devices -l` leaves the before read less of the
// 14 s phase and the call fails within it, tapping and typing nothing. Seen failing with
// the clock started at handler entry (the read would get the full phase).
func TestInputTextBeforePhaseIsClockedFromTheHook(t *testing.T) {
	oldPhase, oldBound, oldType := actionBeforePhase, actionRunningBound, inputTypeBound
	actionBeforePhase, actionRunningBound, inputTypeBound = 400*time.Millisecond, 3*time.Second, 500*time.Millisecond
	defer func() { actionBeforePhase, actionRunningBound, inputTypeBound = oldPhase, oldBound, oldType }()

	f := newUIFixture(t, map[string]string{
		"devices.delay":               "200",
		uiSerial + ".push.exit":       "0",
		uiSerial + ".start.out":       "",
		uiSerial + ".forward.exit":    "1",
		uiSerial + ".reader.hang":     "",
		uiSerial + ".dumptty.hang":    "",
		uiSerial + ".dumpsdcard.hang": "",
	})
	start := time.Now()
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","target":{"id":"name"}}`)
	if d := time.Since(start); d > 520*time.Millisecond {
		t.Errorf("the call took %v; want within the 400 ms before phase measured from the hook", d)
	}
	mustFail(t, out, ErrUIDumpFailed)
	for _, c := range f.calls(t) {
		if strings.Contains(c, " shell input ") {
			t.Errorf("a failed before read issued %q", c)
		}
	}
}

// Decision (plan m1f step 9, check; D19; reversal): the second chunk failing is a
// completed result with the first chunk's characters and a warning, never an error,
// because a retry would type the first chunk again. Seen failing with a partial typing
// returned as an error. The two 100-space chunks escape to identical 200-byte
// arguments, so the fake can fail the second identical invocation.
func TestInputTextSecondChunkFailureIsPartial(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{uiSerial + ".inputtext#2.exit": "1"})
	text := strings.Repeat(" ", 200)
	out := f.inputCall(t, `{"device":"emulator-5554","text":`+jsonString(t, text)+`}`)
	r := mustInput(t, out)
	if r.Characters != 100 {
		t.Fatalf("characters = %d, want the first chunk's 100", r.Characters)
	}
	if len(r.Warnings) == 0 {
		t.Error("a partial typing carried no warning")
	}
	if n := len(inputTextArgs(f.calls(t))); n != 2 {
		t.Errorf("%d input text invocations, want two", n)
	}
}

// Decision (plan m1f step 9, check; D17, D21): after a target is given, the focusing tap
// and its settle happen once, before typing, and the post-typing read never falls back
// to uiautomator dump.
func TestInputTextFocusesOnceAndNeverDumpsAfterTyping(t *testing.T) {
	quickAct(t)
	doc := tappable(tapNode("android.widget.EditText", "name", "old", ""))
	f := newUIFixture(t, withPersistentReader(t, nil, doc, doc))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"Ana","target":{"id":"name"},"expect":[{"appears":{"id":"name"}}]}`)
	r := mustInput(t, out)
	if r.Tapped == nil || r.Tapped.ID != "name" {
		t.Fatalf("tapped = %+v, want the name node", r.Tapped)
	}
	if n := countInputTaps(f.calls(t)); n != 1 {
		t.Errorf("%d focusing taps, want one", n)
	}
	if n := count(f.calls(t), ttyCall); n != 0 {
		t.Errorf("uiautomator ran after typing: %q", f.calls(t))
	}
}

// waitLineCount waits until the fake has logged line at least n times, so a test can
// cancel at the second of several identical invocations, not the first.
func waitLineCount(t *testing.T, read func() []string, line string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		count := 0
		for _, l := range read() {
			if l == line {
				count++
			}
		}
		if count >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the fake adb never logged %q %d times", line, n)
}

// Decision (plan m1f D19, D21; step 9 item 1, reversal): a cancellation after the first
// chunk ran is a completed result with the first chunk's characters, a warning saying the
// call was cancelled, no ENTER and no poll, and reason cancelled when expect was given.
// Seen failing with the cancellation returned as an error (or the characters reset).
func TestInputTextCancelledAfterTheFirstChunkIsPartial(t *testing.T) {
	oldType, oldCmd := inputTypeBound, inputCommandTimeout
	inputTypeBound, inputCommandTimeout = 10*time.Second, 10*time.Second
	t.Cleanup(func() { inputTypeBound, inputCommandTimeout = oldType, oldCmd })

	doc := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	r := devicetest.StartReader(t, doc)
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":        "0",
		uiSerial + ".start.out":        "",
		uiSerial + ".forward.out":      r.Port,
		uiSerial + ".inputtext#2.hang": "",
	})
	text := strings.Repeat(" ", 200)
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.inputText",
		Params:     []byte(`{"device":"emulator-5554","text":` + jsonString(t, text) + `,"expect":[{"appears":{"text":"Likes: 1"}}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := inputTextHead + "'" + strings.Repeat("%s", 100) + "'"
	waitLineCount(t, func() []string { return f.calls(t) }, input, 2)
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out := f.wait(t, id)
	rt := mustInput(t, out)
	if rt.Characters != 100 {
		t.Fatalf("characters = %d, want the first chunk's 100", rt.Characters)
	}
	if len(rt.Warnings) == 0 || !strings.Contains(strings.Join(rt.Warnings, " "), "cancelled") {
		t.Errorf("warnings = %q, want a partial-typing warning naming the cancellation", rt.Warnings)
	}
	if rt.Expect == nil || rt.Expect.Reason != ReasonCancelled {
		t.Errorf("expect = %+v, want reason %s", rt.Expect, ReasonCancelled)
	}
	if n := count(f.calls(t), "-s "+uiSerial+" shell input keyevent 66"); n != 0 {
		t.Errorf("%d ENTER keys, want none after a partial typing", n)
	}
	if dumps := r.Dumps(); dumps != 1 {
		t.Errorf("%d dumps, want only the before read: a cancelled call does not poll", dumps)
	}
}

// Decision (plan m1f D19; step 9 item 1): a cancellation while the first chunk is still
// running stays cancelled, because nothing was typed.
func TestInputTextCancelledDuringTheFirstChunkIsCancelled(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{uiSerial + ".inputtext.hang": ""})
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.inputText",
		Params:     []byte(`{"device":"emulator-5554","text":"x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitLine(t, func() []string { return f.calls(t) }, inputTextHead+"'x'")
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out := f.wait(t, id)
	if out.State != capability.StateCancelled {
		t.Errorf("state = %s with error %+v, want cancelled: nothing was typed", out.State, out.Error)
	}
}

// Decision (plan m1f D19; step 9 item 2, reversal): ENTER is never pressed after a partial
// typing; the result warns that submit was skipped. Seen failing reversed, where the
// partial call still pressed ENTER.
func TestInputTextNeverSubmitsAfterAPartialTyping(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{uiSerial + ".inputtext#2.exit": "1"})
	text := strings.Repeat(" ", 200)
	out := f.inputCall(t, `{"device":"emulator-5554","text":`+jsonString(t, text)+`,"submit":true}`)
	r := mustInput(t, out)
	if r.Characters != 100 {
		t.Fatalf("characters = %d, want the first chunk's 100", r.Characters)
	}
	if n := count(f.calls(t), "-s "+uiSerial+" shell input keyevent 66"); n != 0 {
		t.Errorf("%d ENTER keys after a partial typing, want none", n)
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "submit was skipped") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %q, want one saying submit was skipped", r.Warnings)
	}
}

// Decision (plan m1f step 9 item 3a, reversal): a 200-byte chunk boundary never splits a
// space's %s or an apostrophe's '\” escape. Seen failing with a boundary inside a token.
func TestInputTextChunkBoundaryKeepsEscapesWhole(t *testing.T) {
	texts := []string{
		"x" + strings.Repeat(" ", 100),
		strings.Repeat("a", 198) + "'" + "b",
		strings.Repeat("a", 199) + "'",
		strings.Repeat("a", 197) + "'" + strings.Repeat(" ", 40),
		strings.Repeat("%s", 70),
		strings.Repeat("'", 60),
		strings.Repeat(" ", 1000),
	}
	for _, text := range texts {
		// The token stream the chunker must partition: each rune is one token, a space is
		// %s and an apostrophe is '\''; a literal %s is two single-char tokens, so a
		// boundary between them is allowed.
		type span struct{ start, end int }
		var spans []span
		var stream strings.Builder
		for _, r := range text {
			start := stream.Len()
			switch r {
			case ' ':
				stream.WriteString("%s")
			case '\'':
				stream.WriteString(`'\''`)
			default:
				stream.WriteRune(r)
			}
			spans = append(spans, span{start, stream.Len()})
		}

		chunks := inputTextChunks(text)
		var boundaries []int
		var got strings.Builder
		for _, c := range chunks {
			content := strings.TrimSuffix(strings.TrimPrefix(c.arg, "'"), "'")
			got.WriteString(content)
			boundaries = append(boundaries, got.Len())
			if len(c.arg) > 2+inputChunkBytes {
				t.Errorf("text %q: chunk %q exceeds the %d-byte cap", text, c.arg, inputChunkBytes)
			}
		}
		if got.String() != stream.String() {
			t.Errorf("text %q: chunks rejoin to %q, want %q", text, got.String(), stream.String())
		}
		for _, s := range spans {
			for _, b := range boundaries[:len(boundaries)-1] {
				if b > s.start && b < s.end {
					t.Errorf("text %q: a chunk boundary at %d splits the token [%d,%d)", text, b, s.start, s.end)
				}
			}
		}
	}
}

// Decision (plan m1f step 9 item 3a, reversal): the boundary example shows a whole %s
// escape starting the second invocation. Seen failing with the space escape split.
func TestInputTextSpaceEscapeStartsTheSecondChunk(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, nil)
	out := f.inputCall(t, `{"device":"emulator-5554","text":`+jsonString(t, "x"+strings.Repeat(" ", 100))+`}`)
	mustInput(t, out)
	args := inputTextArgs(f.calls(t))
	if len(args) < 2 || !strings.HasPrefix(args[1], "'%s") {
		t.Errorf("input text arguments = %q, want the second to start with a whole %%s escape", args)
	}
}

// Decision (plan m1f step 9 item 3b; D8, reversal): a newline or a tab is outside
// printable ASCII, so without ADBKeyboard it is DEVICE_TEXT_NOT_DELIVERABLE and nothing
// is typed. Seen failing with the byte accepted into `input text`.
func TestInputTextNewlineAndTabAreNotDeliverable(t *testing.T) {
	for _, c := range []struct{ name, text string }{
		{"newline", "a\nb"},
		{"tab", "a\tb"},
	} {
		t.Run(c.name, func(t *testing.T) {
			quickAct(t)
			f := newUIFixture(t, nil)
			out := f.inputCall(t, `{"device":"emulator-5554","text":`+jsonString(t, c.text)+`}`)
			mustFail(t, out, ErrTextNotDeliverable)
			for _, call := range f.calls(t) {
				if strings.Contains(call, " shell input ") || strings.Contains(call, " shell am broadcast ") {
					t.Errorf("an undeliverable text issued %q", call)
				}
			}
		})
	}
}

// Decision (plan m1f step 9, check; D13): with `adb version` and `devices -l` hanging to
// their (injected) timeouts, the call fails inside the before phase, tapping and typing
// nothing. The read is never reached.
func TestInputTextHangingPreambleFailsBeforeTheRead(t *testing.T) {
	oldVersion, oldDevices := versionTimeout, devicesTimeout
	versionTimeout, devicesTimeout = 60*time.Millisecond, 60*time.Millisecond
	defer func() { versionTimeout, devicesTimeout = oldVersion, oldDevices }()
	oldPhase := actionBeforePhase
	actionBeforePhase = 2 * time.Second
	defer func() { actionBeforePhase = oldPhase }()

	f := newUIFixture(t, map[string]string{
		"version.hang":             "",
		"devices.hang":             "",
		uiSerial + ".push.exit":    "0",
		uiSerial + ".start.out":    "",
		uiSerial + ".forward.exit": "1",
		uiSerial + ".reader.hang":  "",
		uiSerial + ".dumptty.hang": "",
	})
	start := time.Now()
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","target":{"id":"name"}}`)
	if d := time.Since(start); d > actionBeforePhase {
		t.Errorf("the call took %v, want within the %v before phase", d, actionBeforePhase)
	}
	mustFail(t, out, ErrADBFailed)
	for _, c := range f.calls(t) {
		if strings.Contains(c, " shell input ") {
			t.Errorf("a hanging preamble issued %q", c)
		}
	}
}

// Decision (plan m1f step 9, check): a failed before read with a target types nothing.
func TestInputTextFailedBeforeReadTypesNothing(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":       "0",
		uiSerial + ".start.out":       "",
		uiSerial + ".forward.exit":    "1",
		uiSerial + ".reader.hang":     "",
		uiSerial + ".dumptty.hang":    "",
		uiSerial + ".dumpsdcard.hang": "",
	})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","target":{"id":"name"}}`)
	mustFail(t, out, ErrUIDumpFailed)
	for _, c := range f.calls(t) {
		if strings.Contains(c, " shell input ") {
			t.Errorf("a failed before read issued %q", c)
		}
	}
}
