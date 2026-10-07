package device

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// A device input command runs as `rm -f <pidfile>; echo "$$ <nonce>" > <pidfile>; exec
// <command>` with a fresh per-command nonce (plan m1m close, round 3), so the tests match
// its shape rather than one fixed string.

// inputNonceRE pins the nonce's shape: 12 hex characters.
var inputNonceRE = regexp.MustCompile(`^[0-9a-f]{12}$`)

// inputCommandOf splits a calls.log line for uiSerial into the wrapped command's nonce and
// command; ok is false when the line is not a wrapped input command.
func inputCommandOf(call string) (nonce, cmd string, ok bool) {
	rest, ok := strings.CutPrefix(call, "-s "+uiSerial+" shell "+inputPidBefore)
	if !ok {
		return "", "", false
	}
	nonce, rest, ok = strings.Cut(rest, inputPidAfter)
	if !ok || !inputNonceRE.MatchString(nonce) {
		return "", "", false
	}
	return nonce, rest, true
}

// inputCommandIssued reports whether a calls.log line is any device input command, wrapped
// or a bare tap — anything that could type or touch — so a test can assert nothing was.
func inputCommandIssued(c string) bool {
	if _, _, ok := inputCommandOf(c); ok {
		return true
	}
	return strings.Contains(c, " shell input ")
}

// countInput counts the wrapped device input commands for uiSerial carrying cmd.
func countInput(calls []string, cmd string) int {
	n := 0
	for _, c := range calls {
		if _, got, ok := inputCommandOf(c); ok && got == cmd {
			n++
		}
	}
	return n
}

// countInputCommandPrefix counts the wrapped device input commands for uiSerial whose
// command starts with prefix.
func countInputCommandPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if _, cmd, ok := inputCommandOf(c); ok && strings.HasPrefix(cmd, prefix) {
			n++
		}
	}
	return n
}

// countInputProbe counts the device-side running-probes for uiSerial, whose nonce varies.
func countInputProbe(calls []string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "-s "+uiSerial+" shell set -- $(cat ") && strings.Contains(c, inputUnknownToken) {
			n++
		}
	}
	return n
}

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
		if _, cmd, ok := inputCommandOf(c); ok && strings.HasPrefix(cmd, "input text ") {
			out = append(out, strings.TrimPrefix(cmd, "input text "))
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
		if inputCommandIssued(c) {
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
	want := "am broadcast -a ADB_INPUT_B64 --es msg " + base64.StdEncoding.EncodeToString([]byte("ñ"))
	if n := countInput(f.calls(t), want); n != 1 {
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
			"input keycombination 113 29",
			"input keyevent 67",
		}},
		{"api 30", "30", []string{
			"input keyevent 123 67 67 67",
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
				if n := countInput(f.calls(t), want); n != 1 {
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
	if n := countInput(f.calls(t), "input keyevent 66"); n != 1 {
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
	w := f.adb.submit(ctx, ctx, &target{adb: "/nonexistent/adb", serial: uiSerial})
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
		if inputCommandIssued(c) {
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

// waitInputTextArg waits until the fake has logged a wrapped `input text` invocation with
// argument arg (the single-quoted device-shell argument), so a test can act at the moment a
// chunk is in flight.
func waitInputTextArg(t *testing.T, read func() []string, arg string) {
	t.Helper()
	waitInputTextArgCount(t, read, arg, 1)
}

// waitInputTextArgCount waits until the fake has logged at least n wrapped `input text`
// invocations with argument arg, so a test can cancel at the second of several identical
// chunks, not the first.
func waitInputTextArgCount(t *testing.T, read func() []string, arg string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		count := 0
		for _, a := range inputTextArgs(read()) {
			if a == arg {
				count++
			}
		}
		if count >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the fake adb never logged %d input text %q", n, arg)
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
	waitInputTextArgCount(t, func() []string { return f.calls(t) }, "'"+strings.Repeat("%s", 100)+"'", 2)
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
	if n := countInput(f.calls(t), "input keyevent 66"); n != 0 {
		t.Errorf("%d ENTER keys, want none after a partial typing", n)
	}
	if dumps := r.Dumps(); dumps != 1 {
		t.Errorf("%d dumps, want only the before read: a cancelled call does not poll", dumps)
	}
}

// Decision (plan m1f D19, D21; close review): a cancellation while the first chunk is
// running ends completed: the issued command may have typed part of its text, so an
// error would invite a retry that types it again. The result counts nothing as typed for
// certain and warns. Seen failing with the cancellation returned as an error.
func TestInputTextCancelledDuringTheFirstChunkIsCompleted(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{uiSerial + ".inputtext.hang": ""})
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.inputText",
		Params:     []byte(`{"device":"emulator-5554","text":"x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitInputTextArg(t, func() []string { return f.calls(t) }, "'x'")
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out := f.wait(t, id)
	if out.State != capability.StateCompleted {
		t.Fatalf("state = %s with error %+v, want completed: the chunk was issued", out.State, out.Error)
	}
	r := mustInput(t, out)
	if r.Characters != 0 || len(r.Warnings) == 0 || !strings.Contains(strings.Join(r.Warnings, " "), "may still type") {
		t.Errorf("characters %d, warnings %q; want 0 for certain and a warning that the chunk may still type", r.Characters, r.Warnings)
	}
	if joined := strings.Join(r.Warnings, " "); !strings.Contains(joined, "cancelled") {
		t.Errorf("warnings = %q, want one naming the cancellation", r.Warnings)
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
	if n := countInput(f.calls(t), "input keyevent 66"); n != 0 {
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
				if inputCommandIssued(call) {
					t.Errorf("an undeliverable text issued %q", call)
				}
			}
		})
	}
}

// The running-probe's answers, as the fake prints them: the wait reads "gone" only from
// the exact gone token and everything else — the running token, the unknown token — as
// still running (plan m1m close, round 2; round 3).
const (
	probeRunning = inputRunningToken + "\n"
	probeGone    = inputGoneToken + "\n"
	probeUnknown = inputUnknownToken + "\n"
)

// Decision (plan m1f D19; m1m close, the observed defect): an `input text` whose
// host-side client outlives inputCommandTimeout but whose device-side command ends within
// the wait counts as typed — the command ran to completion on the device — so the call
// goes on to the next chunk and submit, and warns that it finished late. Seen failing with
// the wait removed (characters stop at the first chunk) and with a late-finished chunk
// counted as failed.
func TestInputTextLateDeviceCommandCountsAsTyped(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext#1.hang": "",
		uiSerial + ".inputprobe#1.out": probeRunning, // still running at the first probe
		uiSerial + ".inputprobe.out":   probeGone,    // gone afterwards
	})
	text := strings.Repeat(" ", 200) // two identical 200-byte chunks
	out := f.inputCall(t, `{"device":"emulator-5554","text":`+jsonString(t, text)+`,"submit":true}`)
	r := mustInput(t, out)
	if r.Characters != 200 {
		t.Fatalf("characters = %d, want all 200: the device-side command finished late, so the chunk was typed", r.Characters)
	}
	joined := strings.Join(r.Warnings, " ")
	if !strings.Contains(joined, "finished") {
		t.Errorf("warnings = %q, want one saying the device-side command finished late", r.Warnings)
	}
	if n := len(inputTextArgs(f.calls(t))); n != 2 {
		t.Errorf("%d input text invocations, want two: the next chunk was still sent", n)
	}
	if n := countInput(f.calls(t), "input keyevent 66"); n != 1 {
		t.Errorf("%d ENTER keys, want one: submit still ran after the late chunk", n)
	}
	if n := countInputProbe(f.calls(t)); n < 2 {
		t.Errorf("%d probes, want at least two: a running probe is retried after the interval", n)
	}
}

// Decision (plan m1f D19; m1m close, the observed defect): an `input text` still running
// on the device when the wait ends stops the typing — no further chunk, no submit — and
// warns that the device-side command may still type its text, telling the caller to wait
// and read the field until two reads agree. The warning never says "retry", the advice the
// defect showed to be unsafe. Seen failing with the old "retrying" warning.
func TestInputTextDeviceCommandStillRunningStopsTyping(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext.hang": "",
		uiSerial + ".inputprobe.out": probeRunning,
	})
	text := strings.Repeat(" ", 200) // two chunks, so a second chunk would be visible
	out := f.inputCall(t, `{"device":"emulator-5554","text":`+jsonString(t, text)+`,"submit":true}`)
	r := mustInput(t, out)
	if r.Characters != 0 {
		t.Fatalf("characters = %d, want 0: the first chunk's device-side command is still running", r.Characters)
	}
	if n := len(inputTextArgs(f.calls(t))); n != 1 {
		t.Errorf("%d input text invocations, want one: no further chunk after a still-running command", n)
	}
	if n := countInput(f.calls(t), "input keyevent 66"); n != 0 {
		t.Errorf("%d ENTER keys, want none after a still-running command", n)
	}
	joined := strings.Join(r.Warnings, " ")
	if !strings.Contains(joined, "still running") {
		t.Errorf("warnings = %q, want one saying the device-side command was still running", r.Warnings)
	}
	if !strings.Contains(joined, "two reads agree") {
		t.Errorf("warnings = %q, want the read-until-two-reads-agree advice", r.Warnings)
	}
	if strings.Contains(joined, "retry") {
		t.Errorf("warnings = %q, want no retry advice: a retry would type the text twice", r.Warnings)
	}
}

// Decision (plan m1m close, round 2; B1): when the typing slot (tctx) ends before a
// chunk's host-side command timeout, runShell returns the slot's DeadlineExceeded, not
// ErrTimeout. The chunk was issued, so the same wait applies: a device-side command proved
// gone counts as typed and the call goes on. Seen failing with the old timeout-only check,
// where the slot's deadline took the "read the field before retrying" path.
func TestInputTextChunkKilledByTheSlotCountsAsTypedWhenGone(t *testing.T) {
	quickAct(t)
	oldBefore, oldBound, oldCmd, oldType := actionBeforePhase, actionRunningBound, inputCommandTimeout, inputTypeBound
	actionBeforePhase, actionRunningBound = 100*time.Millisecond, 600*time.Millisecond
	inputCommandTimeout, inputTypeBound = 5*time.Second, 150*time.Millisecond
	defer func() {
		actionBeforePhase, actionRunningBound, inputCommandTimeout, inputTypeBound = oldBefore, oldBound, oldCmd, oldType
	}()

	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext.hang": "",
		uiSerial + ".inputprobe.out": probeGone,
	})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"hi"}`)
	r := mustInput(t, out)
	if r.Characters != 2 {
		t.Fatalf("characters = %d, want 2: the slot killed the client but the command finished, so the chunk was typed", r.Characters)
	}
	if joined := strings.Join(r.Warnings, " "); !strings.Contains(joined, "finished") {
		t.Errorf("warnings = %q, want a finished-late warning", r.Warnings)
	}
	if strings.Contains(strings.Join(r.Warnings, " "), "retrying") {
		t.Errorf("warnings = %q, want no 'read the field before retrying' advice", r.Warnings)
	}
}

// Decision (plan m1m close, round 2; B1): a chunk the typing slot killed whose device-side
// command is still running when the wait ends stops the typing with the no-retry warning —
// the same safe reading as the command-timeout case, reached through the slot's
// DeadlineExceeded instead of ErrTimeout.
func TestInputTextChunkKilledByTheSlotStillRunningStopsTyping(t *testing.T) {
	quickAct(t)
	oldBefore, oldBound, oldCmd, oldType := actionBeforePhase, actionRunningBound, inputCommandTimeout, inputTypeBound
	actionBeforePhase, actionRunningBound = 100*time.Millisecond, 600*time.Millisecond
	inputCommandTimeout, inputTypeBound = 5*time.Second, 150*time.Millisecond
	defer func() {
		actionBeforePhase, actionRunningBound, inputCommandTimeout, inputTypeBound = oldBefore, oldBound, oldCmd, oldType
	}()

	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext.hang": "",
		uiSerial + ".inputprobe.out": probeRunning,
	})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"hi","submit":true}`)
	r := mustInput(t, out)
	if r.Characters != 0 {
		t.Fatalf("characters = %d, want 0: the device-side command is still running", r.Characters)
	}
	if n := countInput(f.calls(t), "input keyevent 66"); n != 0 {
		t.Errorf("%d ENTER keys, want none after a still-running command", n)
	}
	joined := strings.Join(r.Warnings, " ")
	if !strings.Contains(joined, "still running") || !strings.Contains(joined, "two reads agree") {
		t.Errorf("warnings = %q, want the still-running, two-reads-agree warning", r.Warnings)
	}
	if strings.Contains(joined, "retry") {
		t.Errorf("warnings = %q, want no retry advice", r.Warnings)
	}
}

// Decision (plan m1f D19; m1m close): a non-zero exit is not a host-side timeout — the
// command ran and exited — so it stays the old partial path and issues no probe.
func TestInputTextNonZeroExitStaysPartial(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{uiSerial + ".inputtext.exit": "1"})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x"}`)
	r := mustInput(t, out)
	if r.Characters != 0 {
		t.Fatalf("characters = %d, want 0 for a non-zero exit", r.Characters)
	}
	if joined := strings.Join(r.Warnings, " "); !strings.Contains(joined, "exited with status 1") {
		t.Errorf("warnings = %q, want the old exited-with-status warning", r.Warnings)
	}
	if n := countInputProbe(f.calls(t)); n != 0 {
		t.Errorf("%d probes, want none: a non-zero exit is not a host timeout", n)
	}
}

// Decision (plan m1m close, round 2): the wait reads "gone" only from the exact gone
// token, so a probe that answers anything else — here garbage — reads as still running
// (fail closed) and the call stops typing rather than counting the chunk as typed. Seen
// failing with the probe read as "gone for any answer but running".
func TestInputTextProbeGarbageReadsAsRunning(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext.hang": "",
		uiSerial + ".inputprobe.out": "not a probe token\n",
	})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","submit":true}`)
	r := mustInput(t, out)
	if r.Characters != 0 {
		t.Fatalf("characters = %d, want 0: a garbage probe answer is not gone", r.Characters)
	}
	if n := countInput(f.calls(t), "input keyevent 66"); n != 0 {
		t.Errorf("%d ENTER keys, want none", n)
	}
	if joined := strings.Join(r.Warnings, " "); !strings.Contains(joined, "still running") {
		t.Errorf("warnings = %q, want the still-running warning", r.Warnings)
	}
}

// Decision (plan m1m close, round 2): a probe that cannot answer at all — it hangs past
// the wait's own bound — reads as still running (fail closed); the wait issues exactly one
// probe and ends at the bound rather than counting the chunk as typed.
func TestInputTextProbeCannotAnswerStopsTyping(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext.hang":  "",
		uiSerial + ".inputprobe.hang": "",
	})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","submit":true}`)
	r := mustInput(t, out)
	if r.Characters != 0 {
		t.Fatalf("characters = %d, want 0: the probe could not prove the command finished", r.Characters)
	}
	if n := len(inputTextArgs(f.calls(t))); n != 1 {
		t.Errorf("%d input text invocations, want one", n)
	}
	if n := countInput(f.calls(t), "input keyevent 66"); n != 0 {
		t.Errorf("%d ENTER keys, want none", n)
	}
	if joined := strings.Join(r.Warnings, " "); !strings.Contains(joined, "still running") {
		t.Errorf("warnings = %q, want the still-running warning on a probe failure", r.Warnings)
	}
	if n := countInputProbe(f.calls(t)); n != 1 {
		t.Errorf("%d probes, want one: a hanging probe consumes the wait's bound", n)
	}
}

// Decision (plan m1m close, round 2): a probe that answers "running" fast is still retried
// only every inputProbeInterval, so the wait issues a bounded number of probes. Seen
// failing with the interval sleep removed: the loop spins and issues thousands of probes
// in the same bounded wait.
func TestInputTextProbeAnswersRunningPollsAtTheInterval(t *testing.T) {
	quickAct(t)
	oldCmd, oldInterval := inputCommandTimeout, inputProbeInterval
	inputCommandTimeout, inputProbeInterval = 150*time.Millisecond, 25*time.Millisecond
	defer func() { inputCommandTimeout, inputProbeInterval = oldCmd, oldInterval }()

	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext.hang": "",
		uiSerial + ".inputprobe.out": probeRunning,
	})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x"}`)
	mustInput(t, out)
	n := countInputProbe(f.calls(t))
	if n < 2 {
		t.Fatalf("%d probes, want at least two: a running answer is retried", n)
	}
	if n > 60 {
		t.Errorf("%d probes in a %v wait, want at most 60 at %v apart: the sleep must bound the poll rate", n, inputCommandTimeout, inputProbeInterval)
	}
}

// Decision (plan m1m close, round 2; B1): the ADBKeyboard broadcast gets the same wait as
// a chunk. A broadcast whose host-side client timed out but whose device-side command
// finished counts as delivered, and its warning names the broadcast, not an "input
// command" — the phrasing the round-2 review flagged.
func TestInputTextADBKeyboardTimeoutPath(t *testing.T) {
	cases := []struct {
		name      string
		answer    string
		wantChars int
		want      string
	}{
		{"finished late", probeGone, 1, "ADBKeyboard broadcast"},
		{"still running", probeRunning, 0, "still running"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quickAct(t)
			f := newUIFixture(t, map[string]string{
				uiSerial + ".inputmethod.out": adbKeyboardIME + "\n",
				uiSerial + ".broadcast.hang":  "",
				uiSerial + ".inputprobe.out":  c.answer,
			})
			out := f.inputCall(t, `{"device":"emulator-5554","text":"ñ"}`)
			r := mustInput(t, out)
			if r.Method != inputTextMethodIME || r.Characters != c.wantChars {
				t.Fatalf("result = %+v, want method %s and %d characters", r, inputTextMethodIME, c.wantChars)
			}
			joined := strings.Join(r.Warnings, " ")
			if !strings.Contains(joined, c.want) {
				t.Errorf("warnings = %q, want %q", r.Warnings, c.want)
			}
			if strings.Contains(joined, "input command") {
				t.Errorf("warnings = %q, want no 'input command' phrasing for a broadcast", r.Warnings)
			}
			if strings.Contains(joined, "retry") {
				t.Errorf("warnings = %q, want no retry advice", r.Warnings)
			}
		})
	}
}

// Decision (plan m1m close, round 2; B1): a timed-out ENTER whose device-side command
// finished late counts as sent — the call does not report it as not run or as possibly not
// happened — and one still running gets the no-retry warning, never an invitation to press
// ENTER a second time.
func TestInputTextSubmitTimeoutPath(t *testing.T) {
	t.Run("finished late counts as sent", func(t *testing.T) {
		quickAct(t)
		f := newUIFixture(t, map[string]string{
			uiSerial + ".inputkeyevent.hang": "",
			uiSerial + ".inputprobe.out":     probeGone,
		})
		out := f.inputCall(t, `{"device":"emulator-5554","text":"x","submit":true}`)
		r := mustInput(t, out)
		if r.Characters != 1 {
			t.Fatalf("characters = %d, want 1", r.Characters)
		}
		joined := strings.Join(r.Warnings, " ")
		if !strings.Contains(joined, "ENTER") || !strings.Contains(joined, "sent") {
			t.Errorf("warnings = %q, want one saying ENTER was sent", r.Warnings)
		}
		if strings.Contains(joined, "may not have happened") {
			t.Errorf("warnings = %q, want no 'may not have happened': the command finished", r.Warnings)
		}
	})

	t.Run("still running warns against a second ENTER", func(t *testing.T) {
		quickAct(t)
		f := newUIFixture(t, map[string]string{
			uiSerial + ".inputkeyevent.hang": "",
			uiSerial + ".inputprobe.out":     probeRunning,
		})
		out := f.inputCall(t, `{"device":"emulator-5554","text":"x","submit":true}`)
		r := mustInput(t, out)
		joined := strings.Join(r.Warnings, " ")
		if !strings.Contains(joined, "still running") || !strings.Contains(joined, "ENTER") {
			t.Errorf("warnings = %q, want the still-running warning naming ENTER", r.Warnings)
		}
		if strings.Contains(joined, "retry") {
			t.Errorf("warnings = %q, want no retry advice for ENTER", r.Warnings)
		}
	})
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
		if inputCommandIssued(c) {
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
		if inputCommandIssued(c) {
			t.Errorf("a failed before read issued %q", c)
		}
	}
}

// Decision (plan m1m close, round 3; B1): every wrapped input command carries a fresh
// 12-hex nonce between the `echo "$$ ` prologue and the `" > <pidfile>; exec ` tail, and the
// probe the wait issues is built for that same nonce — so the probe can only answer "gone"
// for the command it waits on, never for a stale record a different command left. Seen
// failing with a fixed pidfile prologue that carried no nonce.
func TestInputTextCommandAndProbeCarryTheSameNonce(t *testing.T) {
	quickAct(t)
	oldCmd := inputCommandTimeout
	inputCommandTimeout = 150 * time.Millisecond
	defer func() { inputCommandTimeout = oldCmd }()

	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext.hang": "",
		uiSerial + ".inputprobe.out": probeRunning,
	})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x"}`)
	mustInput(t, out)
	calls := f.calls(t)

	var nonce, cmd, chunk string
	for _, c := range calls {
		if n, got, ok := inputCommandOf(c); ok && strings.HasPrefix(got, "input text ") {
			nonce, cmd, chunk = n, got, c
			break
		}
	}
	if chunk == "" {
		t.Fatal("no wrapped `input text` command was issued")
	}
	if cmd != "input text 'x'" {
		t.Errorf("command = %q, want input text 'x'", cmd)
	}
	// The shape: `-s <serial> shell rm -f <pidfile>; echo "$$ <nonce>" > <pidfile>; exec <cmd>`.
	if !strings.HasPrefix(chunk, "-s "+uiSerial+" shell "+inputPidBefore) || !strings.Contains(chunk, inputPidAfter) {
		t.Errorf("chunk %q does not have the rm/echo/exec wrapper", chunk)
	}
	if !inputNonceRE.MatchString(nonce) {
		t.Errorf("nonce %q is not 12 hex characters", nonce)
	}
	// The probe the wait issues is built for the chunk's nonce, and it carries the unknown
	// branch that reads a foreign pidfile as not-gone.
	wantProbe := "-s " + uiSerial + " shell " + inputProbeCommand(nonce)
	if n := count(calls, wantProbe); n < 1 {
		t.Errorf("%d probes built for the chunk's nonce %q, want at least one; calls: %q", n, nonce, calls)
	}
	if !strings.Contains(wantProbe, `if [ "$2" != `+nonce) || !strings.Contains(wantProbe, inputUnknownToken) {
		t.Errorf("probe %q does not compare the pidfile's nonce and answer unknown", wantProbe)
	}
}

// Decision (plan m1m close, round 3; B1): two issued chunks never share a nonce, so the
// probe for one cannot read another chunk's pidfile record as its own. Seen failing with a
// fixed prologue that reused one token.
func TestInputTextChunksUseDistinctNonces(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, nil)
	text := strings.Repeat(" ", 200) // two identical 200-byte chunks
	out := f.inputCall(t, `{"device":"emulator-5554","text":`+jsonString(t, text)+`}`)
	mustInput(t, out)
	var nonces []string
	for _, c := range f.calls(t) {
		if n, cmd, ok := inputCommandOf(c); ok && strings.HasPrefix(cmd, "input text ") {
			nonces = append(nonces, n)
		}
	}
	if len(nonces) != 2 {
		t.Fatalf("%d chunks, want two", len(nonces))
	}
	if nonces[0] == nonces[1] {
		t.Errorf("both chunks carried the nonce %q, want distinct nonces", nonces[0])
	}
}

// Decision (plan m1m close, round 3; B1, fail closed): a probe that answers "unknown" —
// the pidfile's nonce is not this command's, so nothing proves the command ran or ended —
// reads as still running: the chunk is not counted as typed and the call stops typing.
// Seen failing with the unknown answer read as gone.
func TestInputTextUnknownProbeAnswerStopsTyping(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext.hang": "",
		uiSerial + ".inputprobe.out": probeUnknown,
	})
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","submit":true}`)
	r := mustInput(t, out)
	if r.Characters != 0 {
		t.Fatalf("characters = %d, want 0: an unknown probe answer is not gone", r.Characters)
	}
	if n := countInput(f.calls(t), "input keyevent 66"); n != 0 {
		t.Errorf("%d ENTER keys, want none after an unknown probe answer", n)
	}
	joined := strings.Join(r.Warnings, " ")
	if !strings.Contains(joined, "still running") || !strings.Contains(joined, "two reads agree") {
		t.Errorf("warnings = %q, want the fail-closed still-running warning", r.Warnings)
	}
}

// Decision (plan m1m close, round 3; N5): a wait that ends because the caller cancelled it
// is worded as a cancellation, never as a timeout. Seen failing with the cause hard-coded
// to "timed out".
func TestInputWaitCauseCancellationIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := stillRunningWarning(inputWaitCause(ctx, inputCommandClient, "the input command"), 0)
	if !strings.Contains(w, "cancelled") {
		t.Errorf("warning = %q, want it to name the cancellation", w)
	}
	if strings.Contains(w, "timed out") {
		t.Errorf("warning = %q, want no 'timed out' for a cancellation", w)
	}
	// A context that is still live reads as a timeout.
	ok := stillRunningWarning(inputWaitCause(context.Background(), inputCommandClient, "the input command"), 0)
	if !strings.Contains(ok, "timed out") {
		t.Errorf("warning = %q, want 'timed out' when the caller did not cancel", ok)
	}
}

// Decision (plan m1m close, round 3; N5): a cancellation that lands during the wait — after
// the chunk's host-side client was killed and while the probe is in flight — is worded as a
// cancellation, not a timeout. Seen failing with the cause hard-coded to "timed out".
func TestInputTextCancelledDuringTheWaitIsNotATimeout(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".inputtext.hang":  "",
		uiSerial + ".inputprobe.hang": "",
	})
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.inputText",
		Params:     []byte(`{"device":"emulator-5554","text":"x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitInputTextArg(t, func() []string { return f.calls(t) }, "'x'")
	// Wait until the wait's probe is in flight, then cancel mid-wait.
	deadline := time.Now().Add(10 * time.Second)
	for countInputProbe(f.calls(t)) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out := f.wait(t, id)
	r := mustInput(t, out)
	joined := strings.Join(r.Warnings, " ")
	if !strings.Contains(joined, "cancelled") {
		t.Errorf("warnings = %q, want one naming the cancellation", r.Warnings)
	}
	if strings.Contains(joined, "timed out") {
		t.Errorf("warnings = %q, want no 'timed out': the call was cancelled during the wait", r.Warnings)
	}
}

// Decision (plan m1m close, round 3; N2): the pre-send slot check stops a chunk whose typing
// slot has already ended. Here the clear's second command hangs past the slot and the
// probe answers gone, so clear finishes; the chunk that follows is never sent — no `input
// text` invocation — and the result carries the not-sent warning, not a late-typed count.
// Seen failing with the chunk pre-check removed: the chunk counts as typed (characters 1,
// not 0) and the not-sent warning is absent.
func TestInputTextChunkNotSentWhenTheSlotEndedDuringClear(t *testing.T) {
	quickAct(t)
	oldCmd := inputCommandTimeout
	inputCommandTimeout = 5 * time.Second // the typing slot, not the command timeout, ends the chunk
	defer func() { inputCommandTimeout = oldCmd }()

	doc := tappable(tapNode("android.widget.EditText", "name", "old", ""))
	f := newUIFixture(t, withPersistentReader(t, map[string]string{
		uiSerial + ".sdk.out":            "34\n",
		uiSerial + ".inputkeyevent.hang": "",
		uiSerial + ".inputprobe.out":     probeGone,
	}, doc))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","target":{"id":"name"},"clear":true}`)
	r := mustInput(t, out)
	if r.Characters != 0 {
		t.Fatalf("characters = %d, want 0: no chunk was sent", r.Characters)
	}
	if n := len(inputTextArgs(f.calls(t))); n != 0 {
		t.Errorf("%d input text invocations, want none: the slot ended before the chunk", n)
	}
	joined := strings.Join(r.Warnings, " ")
	if !strings.Contains(joined, "typing budget ended before the next input command was sent") {
		t.Errorf("warnings = %q, want the not-sent warning", r.Warnings)
	}
	if strings.Contains(joined, "finished") {
		t.Errorf("warnings = %q, want no late-finished warning: the chunk was never sent", r.Warnings)
	}
	if n := countInputProbe(f.calls(t)); n < 1 {
		t.Errorf("%d probes, want the clear command's late wait to probe", n)
	}
}

// Decision (plan m1m close, round 3; N2): the same pre-send slot check on the ADBKeyboard
// path: when the clear pushed the typing slot past its end and the device-side command is
// gone, the broadcast is never sent. Seen failing with the broadcast pre-check removed: the
// broadcast counts as delivered (characters 1, not 0).
func TestInputTextBroadcastNotSentWhenTheSlotEndedDuringClear(t *testing.T) {
	quickAct(t)
	oldCmd := inputCommandTimeout
	inputCommandTimeout = 5 * time.Second
	defer func() { inputCommandTimeout = oldCmd }()

	doc := tappable(tapNode("android.widget.EditText", "name", "old", ""))
	f := newUIFixture(t, withPersistentReader(t, map[string]string{
		uiSerial + ".sdk.out":            "34\n",
		uiSerial + ".inputkeyevent.hang": "",
		uiSerial + ".inputprobe.out":     probeGone,
		uiSerial + ".inputmethod.out":    adbKeyboardIME + "\n",
	}, doc))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"ñ","target":{"id":"name"},"clear":true}`)
	r := mustInput(t, out)
	if r.Method != inputTextMethodIME || r.Characters != 0 {
		t.Fatalf("result = %+v, want method %s and 0 characters", r, inputTextMethodIME)
	}
	if n := countInputCommandPrefix(f.calls(t), "am broadcast "); n != 0 {
		t.Errorf("%d broadcasts, want none: the slot ended before the broadcast", n)
	}
	joined := strings.Join(r.Warnings, " ")
	if !strings.Contains(joined, "typing budget ended before the next input command was sent") {
		t.Errorf("warnings = %q, want the not-sent warning", r.Warnings)
	}
	if strings.Contains(joined, "finished") {
		t.Errorf("warnings = %q, want no late warning: the broadcast was never sent", r.Warnings)
	}
}

// Decision (plan m1m close, round 3; N2): clear's own pre-send slot check. When the first
// clear command hangs past the typing slot and the device-side command is gone, the second
// clear command is never sent; the call fails before any keystroke. Seen failing with the
// clear pre-check removed: the call completes (the gone device-side command counts as done)
// instead of failing.
func TestInputTextClearCommandNotSentWhenTheSlotEndedFirst(t *testing.T) {
	quickAct(t)
	oldCmd := inputCommandTimeout
	inputCommandTimeout = 5 * time.Second
	defer func() { inputCommandTimeout = oldCmd }()

	doc := tappable(tapNode("android.widget.EditText", "name", "old", ""))
	f := newUIFixture(t, withPersistentReader(t, map[string]string{
		uiSerial + ".sdk.out":                  "34\n",
		uiSerial + ".inputkeycombination.hang": "",
		uiSerial + ".inputprobe.out":           probeGone,
	}, doc))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","target":{"id":"name"},"clear":true}`)
	ce := mustFail(t, out, ErrNotReady)
	if !strings.Contains(ce.Cause, "typing budget ended before the clear command was sent") {
		t.Errorf("cause = %q, want the budget-ended message naming the unsent clear", ce.Cause)
	}
	if ce.NextStep == "" {
		t.Error("no next step on the unsent-clear failure")
	}
	if n := countInputCommandPrefix(f.calls(t), "input keyevent 67"); n != 0 {
		t.Errorf("%d delete commands, want none: the budget ended before the second clear command", n)
	}
	if n := len(inputTextArgs(f.calls(t))); n != 0 {
		t.Errorf("%d input text invocations, want none", n)
	}
}

// Decision (plan m1m close, round 3; N1): when a clear command's device-side command is
// still running, the next step is to wait and read the field, not to retry at once — an
// immediate retry could meet old deletions still arriving. Seen failing with the "Retry
// device.inputText; nothing was typed" next step the other clear failures carry.
func TestInputTextClearStillRunningWarnsToWait(t *testing.T) {
	quickAct(t)
	oldCmd := inputCommandTimeout
	inputCommandTimeout = 200 * time.Millisecond
	defer func() { inputCommandTimeout = oldCmd }()

	doc := tappable(tapNode("android.widget.EditText", "name", "old", ""))
	f := newUIFixture(t, withPersistentReader(t, map[string]string{
		uiSerial + ".sdk.out":                  "34\n",
		uiSerial + ".inputkeycombination.hang": "",
		uiSerial + ".inputprobe.out":           probeRunning,
	}, doc))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","target":{"id":"name"},"clear":true}`)
	ce := mustFail(t, out, ErrNotReady)
	if strings.Contains(ce.NextStep, "Retry device.inputText; nothing was typed") {
		t.Errorf("nextStep %q invites an immediate retry; the clear command may still be running", ce.NextStep)
	}
	if !strings.Contains(ce.NextStep, "two reads agree") {
		t.Errorf("nextStep = %q, want the read-until-two-reads-agree advice", ce.NextStep)
	}
	if !strings.Contains(ce.Cause, "still running") {
		t.Errorf("cause = %q, want one saying the device-side command is still running", ce.Cause)
	}
}
