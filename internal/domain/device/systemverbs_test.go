package device

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The system verbs (plan m3g step 3; ADR-0133 §4, §5), over the fake adb: the commands each
// builds, what they refuse before adb runs, and what a refusing device says. Facts about the
// real device are the spike's (docs/research/spike-agent-gestures-2026-10.md); a fake only
// holds the daemon to them.

// verb runs a capability as the host's own caller and waits for its outcome. A call refused
// before an operation exists (a validation in the authorization hook) comes back as a failed
// outcome, as occFix.as does.
func (f *uiFixture) verb(t *testing.T, name, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Params: []byte(params)})
	if ce := capability.AsError(err); ce != nil {
		return capability.Outcome{State: capability.StateFailed, Error: ce}
	}
	return f.wait(t, id)
}

// shellCalls are the fake's recorded `-s <serial> shell …` commands that are not the
// preamble's boot check, in order.
func (f *uiFixture) shellCalls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, "-s "+uiSerial+" shell ") && !strings.HasSuffix(c, " getprop sys.boot_completed") {
			out = append(out, strings.TrimPrefix(c, "-s "+uiSerial+" shell "))
		}
	}
	return out
}

const devParam = `"device":"emulator-5554"`

// Decision (ADR-0133 §4; spike (b)): each panel is its own `cmd statusbar` word, and
// `cmd statusbar` is the only command device.systemPanel runs. A device that refuses it, by
// exit status or by an error line, is DEVICE_ACTION_FAILED with the exit status as the cause
// and the device's output in details, and the failed call is untrusted. The command exits 0
// with no output even when it did nothing, so the success claims nothing but the panel asked
// for. Seen failing with the notifications and quickSettings words swapped.
func TestSystemPanelCommands(t *testing.T) {
	for _, c := range []struct{ panel, word string }{
		{"notifications", "expand-notifications"},
		{"quickSettings", "expand-settings"},
		{"none", "collapse"},
	} {
		f := newUIFixture(t, nil)
		out := f.verb(t, "device.systemPanel", `{`+devParam+`,"panel":"`+c.panel+`"}`)
		if out.State != capability.StateCompleted {
			t.Fatalf("%s: %s, %+v", c.panel, out.State, out.Error)
		}
		if r := out.Result.(SystemPanelResult); r.Panel != c.panel {
			t.Errorf("%s: result %+v", c.panel, r)
		}
		if got := f.shellCalls(t); !slices.Equal(got, []string{"cmd statusbar " + c.word}) {
			t.Errorf("%s: device commands %q; want only cmd statusbar %s", c.panel, got, c.word)
		}
	}

	t.Run("a non-zero exit", func(t *testing.T) {
		f := newUIFixture(t, map[string]string{
			uiSerial + ".statusbar.exit": "1",
			uiSerial + ".statusbar.err":  "cmd: Can't find service: statusbar\n",
		})
		out := f.verb(t, "device.systemPanel", `{`+devParam+`,"panel":"quickSettings"}`)
		ce := mustFail(t, out, ErrActionFailed)
		if !strings.Contains(ce.Cause, "status 1") {
			t.Errorf("cause %q does not carry the exit status", ce.Cause)
		}
		if o, _ := ce.Details["output"].(string); !strings.Contains(o, "Can't find service") {
			t.Errorf("details %+v do not carry the device's output", ce.Details)
		}
		if !out.Untrusted {
			t.Error("a failure that carries the device's output is not marked untrusted")
		}
	})

	t.Run("an error line on exit 0", func(t *testing.T) {
		f := newUIFixture(t, map[string]string{uiSerial + ".statusbar.out": "Error: unknown command 'expand-settings'\n"})
		mustFail(t, f.verb(t, "device.systemPanel", `{`+devParam+`,"panel":"quickSettings"}`), ErrActionFailed)
	})

	t.Run("a panel that is not one", func(t *testing.T) {
		f := newUIFixture(t, nil)
		before := len(f.calls(t))
		mustFail(t, f.verb(t, "device.systemPanel", `{`+devParam+`,"panel":"notifications; reboot"}`), capability.ErrInvalidParams)
		if after := len(f.calls(t)); after != before {
			t.Errorf("a refused panel ran %d adb commands", after-before)
		}
	})
}

// Decision (ADR-0133 §5; ui-automation.md §Design rules): auto-rotate is pinned off before the
// user rotation is written, because the user rotation is ignored while the sensor decides;
// `auto` writes auto-rotate on and nothing else; the result returns the previous setting from
// the two reads; and a write that fails after the first succeeded is a completed result that
// says what was written. Seen failing with the two writes in the other order.
func TestRotatePinsAutoRotateFirst(t *testing.T) {
	reads := map[string]string{
		uiSerial + ".get_accelerometer_rotation.out": "1\n",
		uiSerial + ".get_user_rotation.out":          "3\n",
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range reads {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	t.Run("an angle", func(t *testing.T) {
		f := newUIFixture(t, with(nil))
		out := f.verb(t, "device.rotate", `{`+devParam+`,"rotation":"90"}`)
		if out.State != capability.StateCompleted {
			t.Fatalf("%s, %+v", out.State, out.Error)
		}
		r := out.Result.(RotateResult)
		if r.Previous != (RotationState{Rotation: 270, Auto: true}) || len(r.Warnings) != 0 {
			t.Errorf("result %+v; want previous 270 with auto on, no warnings", r)
		}
		want := []string{
			"settings get system accelerometer_rotation",
			"settings get system user_rotation",
			"settings put system accelerometer_rotation 0",
			"settings put system user_rotation 1",
		}
		if got := f.shellCalls(t); !slices.Equal(got, want) {
			t.Errorf("device commands %q; want %q", got, want)
		}
	})

	t.Run("each angle is its quarter turns", func(t *testing.T) {
		for angle, turns := range map[string]string{"0": "0", "90": "1", "180": "2", "270": "3"} {
			f := newUIFixture(t, with(nil))
			mustOKOutcome(t, f.verb(t, "device.rotate", `{`+devParam+`,"rotation":"`+angle+`"}`))
			if got := f.shellCalls(t); got[len(got)-1] != "settings put system user_rotation "+turns {
				t.Errorf("%s: last command %q; want user_rotation %s", angle, got[len(got)-1], turns)
			}
		}
	})

	t.Run("auto writes one setting", func(t *testing.T) {
		f := newUIFixture(t, with(map[string]string{uiSerial + ".get_accelerometer_rotation.out": "0\n"}))
		out := f.verb(t, "device.rotate", `{`+devParam+`,"rotation":"auto"}`)
		mustOKOutcome(t, out)
		if r := out.Result.(RotateResult); r.Previous.Auto || r.Previous.Rotation != 270 {
			t.Errorf("previous %+v; want 270 with auto off", r.Previous)
		}
		var puts []string
		for _, c := range f.shellCalls(t) {
			if strings.HasPrefix(c, "settings put ") {
				puts = append(puts, c)
			}
		}
		if !slices.Equal(puts, []string{"settings put system accelerometer_rotation 1"}) {
			t.Errorf("writes %q; want only accelerometer_rotation 1", puts)
		}
	})

	t.Run("the second write failing is a completed result with a warning", func(t *testing.T) {
		f := newUIFixture(t, with(map[string]string{uiSerial + ".put_user_rotation.exit": "1"}))
		out := f.verb(t, "device.rotate", `{`+devParam+`,"rotation":"180"}`)
		mustOKOutcome(t, out)
		r := out.Result.(RotateResult)
		if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "accelerometer_rotation was set to 0") || !strings.Contains(r.Warnings[0], "user_rotation 180 was not written") {
			t.Errorf("warnings %q; want one naming the write that happened and the one that did not", r.Warnings)
		}
		if r.Previous != (RotationState{Rotation: 270, Auto: true}) {
			t.Errorf("previous %+v", r.Previous)
		}
	})

	t.Run("the first write failing is an error", func(t *testing.T) {
		f := newUIFixture(t, with(map[string]string{uiSerial + ".put_accelerometer_rotation.exit": "1"}))
		mustFail(t, f.verb(t, "device.rotate", `{`+devParam+`,"rotation":"90"}`), ErrActionFailed)
		for _, c := range f.shellCalls(t) {
			if strings.HasPrefix(c, "settings put system user_rotation") {
				t.Errorf("user_rotation was written after auto-rotate could not be turned off: %q", c)
			}
		}
	})

	t.Run("an unset setting reads as the platform's default, 0", func(t *testing.T) {
		f := newUIFixture(t, with(map[string]string{
			uiSerial + ".get_user_rotation.out":          "null\n",
			uiSerial + ".get_accelerometer_rotation.out": "null\n",
		}))
		out := f.verb(t, "device.rotate", `{`+devParam+`,"rotation":"90"}`)
		mustOKOutcome(t, out)
		if r := out.Result.(RotateResult); r.Previous != (RotationState{Rotation: 0, Auto: false}) {
			t.Errorf("previous %+v; want 0 with auto off", r.Previous)
		}
	})

	t.Run("a setting that cannot be read writes nothing", func(t *testing.T) {
		f := newUIFixture(t, with(map[string]string{uiSerial + ".get_user_rotation.out": "sideways\n"}))
		out := f.verb(t, "device.rotate", `{`+devParam+`,"rotation":"90"}`)
		mustFail(t, out, ErrActionFailed)
		for _, c := range f.shellCalls(t) {
			if strings.HasPrefix(c, "settings put ") {
				t.Errorf("a write ran with the previous setting unknown: %q", c)
			}
		}
	})

	t.Run("a rotation that is not one", func(t *testing.T) {
		f := newUIFixture(t, with(nil))
		before := len(f.calls(t))
		mustFail(t, f.verb(t, "device.rotate", `{`+devParam+`,"rotation":"45"}`), capability.ErrInvalidParams)
		if after := len(f.calls(t)); after != before {
			t.Errorf("a refused rotation ran %d adb commands", after-before)
		}
	})
}

func mustOKOutcome(t *testing.T, out capability.Outcome) {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s, %+v", out.State, out.Error)
	}
}

// Decision (ADR-0133 §5; ADR-0015 §4): the applicationId is the one caller text device.forceStop
// puts in a device command, so it is checked before any adb command runs, and a name that is
// not a package name never reaches the shell. A running app reports wasRunning, and the stop
// runs either way (am force-stop of an absent package is harmless, spike (e)).
func TestForceStopValidatesBeforeADB(t *testing.T) {
	for _, bad := range []string{"-rf", "a;reboot", "x", "com.example.app --user 0", "com.example;id"} {
		f := newUIFixture(t, nil)
		before := len(f.calls(t))
		mustFail(t, f.verb(t, "device.forceStop", `{`+devParam+`,"applicationId":"`+bad+`"}`), capability.ErrInvalidParams)
		if after := len(f.calls(t)); after != before {
			t.Errorf("%q ran %d adb commands before it was refused: %q", bad, after-before, f.calls(t))
		}
	}

	f := newUIFixture(t, map[string]string{uiSerial + ".apppidof.out": "4242\n"})
	out := f.verb(t, "device.forceStop", `{`+devParam+`,"applicationId":"dev.droidyard.formapp"}`)
	mustOKOutcome(t, out)
	if r := out.Result.(ForceStopResult); !r.WasRunning || r.ApplicationID != "dev.droidyard.formapp" {
		t.Errorf("result %+v; want wasRunning for the app", r)
	}
	if got := f.shellCalls(t); !slices.Equal(got, []string{"pidof dev.droidyard.formapp", "am force-stop dev.droidyard.formapp"}) {
		t.Errorf("device commands %q; want pidof, then am force-stop", got)
	}

	// Not running: pidof exits 1 with no output, and the stop still runs.
	g := newUIFixture(t, map[string]string{uiSerial + ".apppidof.exit": "1"})
	out = g.verb(t, "device.forceStop", `{`+devParam+`,"applicationId":"dev.droidyard.formapp"}`)
	mustOKOutcome(t, out)
	if r := out.Result.(ForceStopResult); r.WasRunning {
		t.Errorf("result %+v; want wasRunning false for an empty pidof", r)
	}

	// A device that refuses the stop.
	h := newUIFixture(t, map[string]string{uiSerial + ".forcestop.exit": "255", uiSerial + ".forcestop.err": "Error: no such user\n"})
	mustFail(t, h.verb(t, "device.forceStop", `{`+devParam+`,"applicationId":"dev.droidyard.formapp"}`), ErrActionFailed)
}

// livePayload is spike agent-gestures (e)'s payload: 0x21 to 0x7E only, so it passes the url
// validation, and, wrapped in '…' without escaping its quotes, it ran `touch` on the device.
const livePayload = `https://x.example/a?b=';touch${IFS}/data/local/tmp/x;'`

// Decision (ADR-0133 §5; ADR-0015 §4): device.launch's url reaches the device shell as one
// literal word. The recorded argv holds it as one single-quoted word, and the host's own
// shell, given that word, prints the url unchanged: nothing in it was expanded, split or run.
// Quoted naively (no '\” for the payload's quotes) the shell drops the quotes and prints a
// different string. Seen failing with the url unquoted. What a url the validation refuses
// looks like is in the table below; none of them reaches adb.
func TestLaunchURLReachesTheShellLiterally(t *testing.T) {
	quickLaunch(t)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".date.out":     "1696118400.0\n",
		uiSerial + ".amstart.out":  "Starting: Intent { act=android.intent.action.VIEW dat=https://x.example/... pkg=dev.droidyard.formapp }\n",
		uiSerial + ".apppidof.out": "4242\n",
	})
	out := f.launchCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp","url":`+jsonString(t, livePayload)+`}`)
	r := mustLaunch(t, out)
	if r.PID == nil || *r.PID != 4242 {
		t.Errorf("launch result %+v; want the pid poll to run as after a monkey launch", r)
	}

	var word string
	for _, c := range f.shellCalls(t) {
		if !strings.HasPrefix(c, "am start ") {
			continue
		}
		fields := strings.Fields(c)
		// am start -a <action> -d <word> -p <app>: the url is one argv word, so one field here.
		if len(fields) != 8 || fields[2] != "-a" || fields[4] != "-d" || fields[6] != "-p" || fields[7] != "dev.droidyard.formapp" {
			t.Fatalf("the am start command %q is not -a <action> -d <one word> -p <app>", c)
		}
		word = fields[5]
	}
	if word == "" {
		t.Fatalf("no am start in %q", f.shellCalls(t))
	}
	for _, c := range f.shellCalls(t) {
		if strings.HasPrefix(c, "monkey ") {
			t.Errorf("a launch with a url also ran monkey: %q", c)
		}
	}
	got, err := exec.Command("/bin/sh", "-c", "printf %s "+word).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != livePayload {
		t.Errorf("the host's shell read the word %s as %q; want the url unchanged, %q", word, got, livePayload)
	}

	// Not a url the shell may see: each is refused before any adb command.
	for name, url := range map[string]string{
		"a space":         "https://x.example/a b",
		"a control byte":  "https://x.example/a\u0001b",
		"a newline":       "https://x.example/a\nb",
		"non-ASCII":       "https://x.example/ñ",
		"too long":        "https://" + strings.Repeat("a", 2049-len("https://")),
		"no scheme":       "x.example/a",
		"a leading digit": "1http://x.example",
	} {
		g := newUIFixture(t, map[string]string{uiSerial + ".date.out": "1696118400.0\n"})
		before := len(g.calls(t))
		out := g.launchCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp","url":`+jsonString(t, url)+`}`)
		mustFail(t, out, capability.ErrInvalidParams)
		if after := len(g.calls(t)); after != before {
			t.Errorf("%s: %d adb commands ran for a refused url", name, after-before)
		}
	}
	// The longest url is accepted.
	h := newUIFixture(t, map[string]string{uiSerial + ".date.out": "1696118400.0\n"})
	mustLaunch(t, h.launchCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp","url":`+jsonString(t, "https://"+strings.Repeat("a", 2048-len("https://")))+`}`))
}

// Decision (spike (e); ADR-0133 §5): `am start` refuses a link nothing resolves with its own
// line, "Error: Activity not started, unable to resolve Intent", and its exit status is 0 on
// API 31 and 1 on API 36. Both are DEVICE_LAUNCH_FAILED carrying the line: the line decides,
// never the exit. Seen failing with the exit status alone deciding (API 31's refusal looked
// like a launch).
func TestLaunchURLFailure(t *testing.T) {
	const line = "Error: Activity not started, unable to resolve Intent { act=android.intent.action.VIEW dat=zzq://... pkg=dev.droidyard.formapp }"
	for name, files := range map[string]map[string]string{
		"exit 0 (API 31)": {uiSerial + ".amstart.out": line + "\n"},
		"exit 1 (API 36)": {uiSerial + ".amstart.out": line + "\n", uiSerial + ".amstart.exit": "1"},
		"line on stderr":  {uiSerial + ".amstart.err": line + "\n"},
	} {
		quickLaunch(t)
		files[uiSerial+".date.out"] = "1696118400.0\n"
		f := newUIFixture(t, files)
		out := f.launchCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp","url":"zzq://x"}`)
		ce := mustFail(t, out, ErrLaunchFailed)
		if !strings.Contains(ce.Cause, "unable to resolve Intent") {
			t.Errorf("%s: cause %q does not carry am's line", name, ce.Cause)
		}
		for _, c := range f.shellCalls(t) {
			if strings.HasPrefix(c, "pidof ") {
				t.Errorf("%s: a refused launch still polled for a pid", name)
			}
		}
	}

	// A refusal with no line still fails on its exit status.
	quickLaunch(t)
	f := newUIFixture(t, map[string]string{uiSerial + ".date.out": "1696118400.0\n", uiSerial + ".amstart.exit": "126"})
	ce := mustFail(t, f.launchCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.formapp","url":"zzq://x"}`), ErrLaunchFailed)
	if !strings.Contains(ce.Cause, "126") {
		t.Errorf("cause %q does not carry the exit status", ce.Cause)
	}
}

// Decision (ADR-0094 §2; ADR-0133 §8): the system verbs are acting calls. While another chat
// holds the device each fails DEVICE_OCCUPIED naming the holder before any adb command runs,
// and for a free device the first one holds it. Seen failing with the verbs registered
// without OccupiesAct.
func TestSystemVerbsOccupy(t *testing.T) {
	f := newOccFix(t)
	f.connect("a", "agent-a")
	f.connect("b", "agent-b")
	calls := []struct{ name, params string }{
		{"device.systemPanel", `{` + devParam + `,"panel":"quickSettings"}`},
		{"device.forceStop", `{` + devParam + `,"applicationId":"dev.droidyard.formapp"}`},
		{"device.rotate", `{` + devParam + `,"rotation":"90"}`},
	}
	// A free device is held by the first verb: here by agent-a's panel.
	mustOK(t, "A's systemPanel", f.as(t, mcp("a"), calls[0].name, calls[0].params))
	if h, ok := f.holder(uiSerial); !ok || h.Name != "agent-a" {
		t.Fatalf("after A's systemPanel the holder is %+v, %v; want agent-a", h, ok)
	}
	before := f.deviceCalls(t)
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
	if after := f.deviceCalls(t); after != before {
		t.Errorf("B's refused verbs ran %d device commands: %v", after-before, f.calls(t))
	}
}
