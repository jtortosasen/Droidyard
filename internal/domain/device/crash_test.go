package device

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The crash and system-dialog tests (plan m1n step 6; ADR-0034 §2–3). The fake adb answers
// the crash buffer, the events buffer, the device clock and `dumpsys activity exit-info`
// from the scenario files (devicetest.go), so the whole path — a device action, its reads,
// the daemon's own reading — is exercised without a device. The log and exit-info fixtures
// are the step-2 level-5 recordings (testdata/m1n-step2), rendered into the daemon's format
// by step2Log; see that directory's RECORDED.txt for their provenance.

const (
	// crashPackage is the fixture app's package, the one the before read's foreground names,
	// and the package the recorded ANR names.
	crashPackage = tapPackage
	// crashLauncher is what the after read's foreground names when the app's activity is gone.
	crashLauncher = "com.google.android.apps.nexuslauncher"
	// crashClock is the device clock `date +%s.%N%z` answers with, in UTC: 2026-10-07
	// 20:57:00.500, just before the step-2 recordings' earliest event (20:57:23), so the
	// recorded log lines are all after the `-T` bound and the recorded exit-info timestamps
	// (21:11, 21:12) are all at or after the action's start. crashEpoch is the synthetic log
	// lines' epoch, after that clock.
	crashClock = "1791406620.500000000+0000"
	crashEpoch = "1791406700.000000"
	// crashClockSecs is crashClock's epoch, for the events-window assertions that compare a
	// read's `-T` bound with the clock it was taken from.
	crashClockSecs = 1791406620.5
	// crashDateCall is the one device-clock read the crash path pays (D7); its `%z` is what
	// lets the exit-info timestamps be compared in the device's own wall clock.
	crashDateCall = "-s " + uiSerial + " shell date +%s.%N%z"
	// crashLogcatHead is the prefix of the crash/events buffer read (logcatFormat + buffer).
	crashLogcatHead = "-s " + uiSerial + " logcat -d -v " + logcatFormat + " -b "
	// exitInfoCall is the `dumpsys activity exit-info` fallback command.
	exitInfoCall = "-s " + uiSerial + " shell dumpsys activity exit-info " + crashPackage
	// recordingCrashPackage is the package the step-2 recordings' am_crash lines name.
	recordingCrashPackage = "dev.droidyard.golden"
)

// --- the step-2 recordings -------------------------------------------------------------

// readStep2 reads a file from the milestone 1n step 2 recording set.
func readStep2(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "m1n-step2", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// step2LineRE reads one recorded `logcat -v threadtime` line:
// `MM-DD HH:MM:SS.mmm  pid  tid P tag: message`.
var step2LineRE = regexp.MustCompile(`^(\d\d)-(\d\d) (\d\d):(\d\d):(\d\d)\.(\d\d\d) +(\d+) +(\d+) +([VDIWEF]) +(.+)$`)

// step2Day is the UTC day the step-2 recordings were made on: their lines carry a month-day
// and a time of day only, so a recorded time of day maps onto this day.
var step2Day = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)

// step2Log renders one recorded line into the daemon's `threadtime,epoch,usec,uid` format,
// keeping the recorded time of day, pid, tid, priority, tag and message and giving it the uid
// the caller names. The recordings were taken in `-v threadtime`; the daemon reads the
// epoch-bearing format, so this is the one translation the recording needs — it is why the
// recorded content runs through the real parser rather than a paraphrase of it.
func step2Log(t *testing.T, uid int, line string) string {
	t.Helper()
	m := step2LineRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("recorded line is not in `threadtime` shape: %q", line)
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	at := step2Day.Add(time.Duration(atoi(m[3]))*time.Hour + time.Duration(atoi(m[4]))*time.Minute +
		time.Duration(atoi(m[5]))*time.Second + time.Duration(atoi(m[6]))*time.Millisecond)
	i := strings.Index(m[10], ": ")
	if i < 0 {
		t.Fatalf("recorded line has no tag: %q", line)
	}
	tag, msg := strings.TrimSpace(m[10][:i]), m[10][i+2:]
	return fmt.Sprintf("%d.%06d %d %d %d %s %s: %s",
		at.Unix(), at.Nanosecond()/1000, uid, atoi(m[7]), atoi(m[8]), m[9], tag, msg)
}

// step2Logs renders every non-empty line of a recording.
func step2Logs(t *testing.T, uid int, name string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(readStep2(t, name)), "\n"), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, step2Log(t, uid, l))
	}
	return out
}

// dialogDump is the recorded system dialog's tree, turned back from the recorded
// `device.uiTree` result into a reader dump: the recorded nodes, ids, texts and bounds, and
// the recorded foreground `android` as every node's package (so the parser's foreground is
// `android`). The `<pkg>:id/<id>` resource-ids are what the parser strips back to the
// recorded ids.
func dialogDump(t *testing.T) string {
	t.Helper()
	var tree struct {
		Result struct {
			Foreground string `json:"foreground"`
			Nodes      []struct {
				Class    string   `json:"class"`
				ID       string   `json:"id"`
				Text     string   `json:"text"`
				Bounds   []int    `json:"bounds"`
				Flags    []string `json:"flags"`
				Children []string `json:"children"`
			} `json:"nodes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(readStep2(t, "m1n-step2-dialog-uitree.json"), &tree); err != nil {
		t.Fatal(err)
	}
	byRef := map[string]int{}
	for i := range tree.Result.Nodes {
		byRef["n"+strconv.Itoa(i)] = i
	}
	var render func(ref string) string
	render = func(ref string) string {
		n := tree.Result.Nodes[byRef[ref]]
		var kids strings.Builder
		for _, c := range n.Children {
			kids.WriteString(render(c))
		}
		var flags string
		for _, f := range n.Flags {
			flags += " " + f + `="true"`
		}
		return fmt.Sprintf(`<node class=%q package=%q resource-id=%q text=%q bounds="[%d,%d][%d,%d]"%s>%s</node>`,
			n.Class, tree.Result.Foreground, tree.Result.Foreground+":id/"+n.ID, n.Text,
			n.Bounds[0], n.Bounds[1], n.Bounds[2], n.Bounds[3], flags, kids.String())
	}
	return string(synthDump(0, render("n0")))
}

// --- fixtures --------------------------------------------------------------------------

// formappDoc is the before read's tree: the fixture app in the foreground with a tappable.
func formappDoc() string { return tappable(tapLike("Like")) }

// launcherDoc is the after read's tree when the app's activity is gone: another window owns
// the foreground, so foreground.after differs from foreground.before.
func launcherDoc() string {
	return string(synthDump(0, fmt.Sprintf(
		`<node class="android.widget.FrameLayout" package=%q bounds="[0,0][1080,2340]"><node class="android.widget.TextView" package=%q text="All apps" bounds="[0,0][100,100]"></node></node>`,
		crashLauncher, crashLauncher)))
}

// crashDialogDump is the crash dialog's tree: the shared close button and app-info, the one
// only a crash dialog carries (plan m1n step 2 (b)).
func crashDialogDump() string {
	btn := func(id, text string) string {
		return fmt.Sprintf(`<node class="android.widget.Button" package="android" resource-id="android:id/%s" text=%q bounds="[300,1400][500,1500]" clickable="true"></node>`, id, text)
	}
	return string(synthDump(0, `<node class="android.widget.FrameLayout" package="android" bounds="[0,0][1080,2340]">`+
		btn("aerr_app_info", "App info")+btn("aerr_close", "Close app")+`</node>`))
}

// appNamedDialogDoc is an app's own tree that happens to carry a view with the dialog's id:
// its foreground is the app, so it must not be mistaken for the system's dialog.
func appNamedDialogDoc() string {
	return string(synthDump(0, fmt.Sprintf(
		`<node class="android.widget.FrameLayout" package=%q bounds="[0,0][1080,2340]"><node class="android.widget.Button" package=%q resource-id=%q text="Close" bounds="[300,1200][500,1300]" clickable="true"></node></node>`,
		crashPackage, crashPackage, crashPackage+":id/aerr_close")))
}

// systemUiDoc is the system ui's own window (foreground `android`) with none of the dialog's
// buttons: not a dialog, but still not an app whose crash could be reported.
func systemUiDoc() string {
	return string(synthDump(0, `<node class="android.widget.FrameLayout" package="android" bounds="[0,0][1080,2340]">`+
		`<node class="android.widget.Button" package="android" resource-id="android:id/dismiss" text="Dismiss" bounds="[300,1400][500,1500]" clickable="true"></node></node>`))
}

// crashLog is the device log for the crash tests in `threadtime,epoch,usec,uid`: a crash
// buffer holding the fixture app's Java crash, the blocks the package filter must drop — a
// second app's synthetic block and the recorded uiautomator shell-tool crash (which names
// neither the app nor, trimmed as recorded, a `Process:` header at all) — and, on its own
// section, the recorded events buffer.
func crashLog(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("--------- beginning of crash\n")
	block := func(uid, pid int, msgs ...string) {
		for _, m := range msgs {
			fmt.Fprintf(&b, "         %s %d  %d  %d E AndroidRuntime: %s\n", crashEpoch, uid, pid, pid, m)
		}
	}
	// The fixture app's own block: the one the read must report, with five frames.
	block(10100, 7000,
		"FATAL EXCEPTION: main",
		"Process: "+crashPackage+", PID: 7000",
		"java.lang.IllegalStateException: boom from the fixture",
		"\tat "+crashPackage+".MainActivity.onClick(MainActivity.java:35)",
		"\tat android.view.View.performClick(View.java:7441)",
		"\tat android.os.Handler.handleCallback(Handler.java:938)",
		"\tat android.os.Looper.loop(Looper.java:288)",
		"\tat com.android.internal.os.ZygoteInit.main(ZygoteInit.java:1003)",
		"\tat android.view.View.sixthFrame(View.java:9000)")
	// A second app's block: the package filter must drop it.
	block(10200, 8000,
		"FATAL EXCEPTION: main",
		"Process: com.example.other, PID: 8000",
		"java.lang.RuntimeException: another app")
	// The recorded uiautomator shell-tool crash (step-2 recordings, verbatim): pid 8895,
	// com.android.commands.uiautomator on every frame. It is not the app's crash, and the
	// read that reported it as one is what the level-5 run paid for.
	for _, l := range step2Logs(t, 2000, "m1n-step2-crashbuf.txt") {
		b.WriteString(l + "\n")
	}
	b.WriteString("--------- beginning of events\n")
	for _, l := range step2Logs(t, 1000, "m1n-step2-events.txt") {
		b.WriteString(l + "\n")
	}
	return b.String()
}

// emptyCrashBuffer is a log with no crash buffer, so the crash read finds no block and falls
// back to `dumpsys activity exit-info`.
func emptyCrashBuffer(t *testing.T) string {
	t.Helper()
	return "--------- beginning of main\n" +
		"         " + crashEpoch + " 10100  7000  7000 I ActivityManager: nothing to see\n" +
		"--------- beginning of events\n" +
		strings.Join(step2Logs(t, 1000, "m1n-step2-events.txt"), "\n") + "\n"
}

// formappExitInfo is the recorded `dumpsys activity exit-info` answer for the fixture app:
// one exit, `6 (ANR)` at 2026-10-07 21:12:06.039.
func formappExitInfo(t *testing.T) string {
	return string(readStep2(t, "m1n-step2-exitinfo-formapp.txt"))
}

// goldenExitInfo is the recorded answer for golden: two exits, both `4 (APP CRASH(EXCEPTION))`.
func goldenExitInfo(t *testing.T) string {
	return string(readStep2(t, "m1n-step2-exitinfo-golden.txt"))
}

// exitInfoOut renders a `dumpsys activity exit-info` body with the given exits in the order
// given (the timestamp string then the reason line), so a test can put an older exit first.
func exitInfoOut(exits ...[2]string) string {
	var b strings.Builder
	b.WriteString("ACTIVITY MANAGER PROCESS EXIT INFO (dumpsys activity exit-info)\n")
	b.WriteString("  package: " + crashPackage + "\n")
	for i, e := range exits {
		fmt.Fprintf(&b, "        ApplicationExitInfo #%d:\n", i)
		fmt.Fprintf(&b, "          timestamp=%s\n", e[0])
		fmt.Fprintf(&b, "          reason=%s\n", e[1])
	}
	return b.String()
}

// --- helpers ---------------------------------------------------------------------------

// countPrefix counts the calls that begin with prefix: the crash and events reads carry a
// `-T <epoch>` whose value a test cannot pin, so they are matched by their head.
func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// logcatSince returns the `-T` epoch of the first call that reads the named buffer, "" when
// there is none: the lower bound the read was given, which the window decisions turn on.
func logcatSince(calls []string, buffer string) string {
	head := crashLogcatHead + buffer
	for _, c := range calls {
		if !strings.HasPrefix(c, head) {
			continue
		}
		if i := strings.Index(c, " -T "); i >= 0 {
			return c[i+len(" -T "):]
		}
	}
	return ""
}

// epochSeconds parses a logcat `-T` epoch, failing the test when it is not one.
func epochSeconds(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("logcat -T %q is not an epoch: %v", s, err)
	}
	return f
}

// --- crash reporting (ADR-0034 §2) ------------------------------------------------------

// Decision (A3; ADR-0034 §2; plan m1n step 6): a tap whose after read leaves the app and
// whose process is gone carries appCrash from the crash buffer, filtered by the package,
// with the exception, the message and up to five frames — and neither a second app's block
// nor the recorded uiautomator shell-tool crash is reported. Seen failing with the crash read
// removed (appCrash nil), with the package filter dropped (the other app's block reported)
// and with the block selection taking the first rather than the newest.
func TestTapThatCrashesTheAppReportsTheCrash(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, formappDoc(), launcherDoc()))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	r := mustTap(t, out)
	if r.AppCrash == nil {
		t.Fatalf("no appCrash: %+v", r)
	}
	c := r.AppCrash
	if c.Source != "logcat" || c.Package != crashPackage || c.PID != 7000 {
		t.Errorf("appCrash = %+v, want logcat %s pid 7000", c, crashPackage)
	}
	if c.Exception != "java.lang.IllegalStateException" || c.Message != "boom from the fixture" {
		t.Errorf("exception/message = %q/%q, want the fixture's exception", c.Exception, c.Message)
	}
	if len(c.Frames) != 5 || !strings.Contains(c.Frames[0], "MainActivity.onClick") {
		t.Errorf("frames = %q, want five with the app's own frame first", c.Frames)
	}
	if c.Time == "" || c.Reason != "" {
		t.Errorf("time/reason = %q/%q, want a logcat time and no exit reason", c.Time, c.Reason)
	}
	if n := countPrefix(f.calls(t), crashLogcatHead+"crash"); n != 1 {
		t.Errorf("%d crash-buffer reads, want one (%q)", n, f.calls(t))
	}
}

// Decision (A3; ADR-0034 §2; plan m1n step 6): when no read after the action observed the
// foreground — every read comes back `busy` — the result still reports the crash, because
// "no read observed it" is a reason to read the crash buffer, not a reason not to. Seen
// failing with the crash read gated on a differing foreground only.
func TestCrashReportedWhenNoAfterReadObservedIt(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, formappDoc(), busyDoc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Nope"}}],"timeoutMs":60}`)
	r := mustTap(t, out)
	if r.Expect == nil || r.Expect.Reason != ReasonNoObservation {
		t.Fatalf("expect = %+v, want reason %s", r.Expect, ReasonNoObservation)
	}
	if r.Foreground != nil && r.Foreground.After != "" {
		t.Fatalf("foreground.after = %q, want it omitted when no read observed the screen", r.Foreground.After)
	}
	if r.AppCrash == nil || r.AppCrash.Source != "logcat" || r.AppCrash.Package != crashPackage {
		t.Fatalf("appCrash = %+v, want the crash from the logcat buffer", r.AppCrash)
	}
}

// Decision (A3; ADR-0034 §2; plan m1n step 6): with no crash-buffer block, the read falls
// back to `dumpsys activity exit-info` and reports its reason, with source exitInfo and no
// buffer fields. The answer is the recorded one (testdata/m1n-step2): `6 (ANR)`. Seen failing
// with the fallback removed (no appCrash) and with the buffer read skipped (the exit reason
// reported even when a block exists).
func TestCrashWithEmptyBufferFallsBackToExitInfo(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":          emptyCrashBuffer(t),
		uiSerial + ".date.out":     crashClock + "\n",
		uiSerial + ".exitinfo.out": formappExitInfo(t),
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, formappDoc(), launcherDoc()))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	r := mustTap(t, out)
	if r.AppCrash == nil {
		t.Fatalf("no appCrash: %+v", r)
	}
	c := r.AppCrash
	if c.Source != "exitInfo" || c.Package != crashPackage || c.Reason != "6 (ANR)" {
		t.Errorf("appCrash = %+v, want the recorded ANR reason for %s", c, crashPackage)
	}
	if c.Exception != "" || c.Message != "" || len(c.Frames) != 0 || c.PID != 0 {
		t.Errorf("the exit-info result carries buffer fields: %+v", c)
	}
	if n := count(f.calls(t), exitInfoCall); n != 1 {
		t.Errorf("%d exit-info reads, want one (%q)", n, f.calls(t))
	}
}

// Decision (A3; ADR-0034 §2; plan m1n step 6): with two qualifying exits, the newest is
// reported, not the first in the dump — the recorded golden answer lists the newest exit
// first, and a dump can list an older one first. Seen failing with the first match taken (the
// older reason reported).
func TestExitInfoPicksTheNewestQualifyingExit(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      emptyCrashBuffer(t),
		uiSerial + ".date.out": crashClock + "\n",
		// The older exit first: only a real "newest" rule picks the ANR.
		uiSerial + ".exitinfo.out": exitInfoOut(
			[2]string{"2026-10-07 21:09:47.643", "4 (APP CRASH(EXCEPTION))"},
			[2]string{"2026-10-07 21:12:06.039", "6 (ANR)"},
		),
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, formappDoc(), launcherDoc()))
	r := mustTap(t, f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`))
	if r.AppCrash == nil || r.AppCrash.Reason != "6 (ANR)" {
		t.Fatalf("appCrash = %+v, want the newest exit's ANR reason", r.AppCrash)
	}
}

// Decision (A3; ADR-0034 §2; plan m1n step 6, the level-5 fix): exit-info is only a crash or
// an ANR when its reason is 4, 5 or 6. A normal exit — the recorded level-5 false positive
// was `1 (EXIT_SELF)` after the system ui's own dialog was tapped — is not the app's crash,
// so no appCrash is reported. Seen failing with the reason filter removed (the EXIT_SELF
// reason reported as an appCrash).
func TestExitInfoIgnoresANormalExit(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":          emptyCrashBuffer(t),
		uiSerial + ".date.out":     crashClock + "\n",
		uiSerial + ".exitinfo.out": exitInfoOut([2]string{"2026-10-07 21:12:06.039", "1 (EXIT_SELF)"}),
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, formappDoc(), launcherDoc()))
	r := mustTap(t, f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`))
	if r.AppCrash != nil {
		t.Fatalf("appCrash = %+v, want none for a normal exit", r.AppCrash)
	}
}

// Decision (A3; ADR-0034 §2; plan m1n step 6): only an exit at or after the action's start
// counts. A crash from before the action is not this action's, so no appCrash is reported
// (the action would carry a crash it did not cause). Seen failing with the timestamp filter
// removed (the old exit's reason reported).
func TestExitInfoIgnoresAnExitBeforeTheAction(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      emptyCrashBuffer(t),
		uiSerial + ".date.out": crashClock + "\n",
		// The device clock is 2026-10-07 20:57:00.500, so this exit predates the action.
		uiSerial + ".exitinfo.out": exitInfoOut([2]string{"2026-10-07 20:00:00.000", "4 (APP CRASH(EXCEPTION))"}),
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, formappDoc(), launcherDoc()))
	r := mustTap(t, f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`))
	if r.AppCrash != nil {
		t.Fatalf("appCrash = %+v, want none for a crash before the action", r.AppCrash)
	}
}

// Decision (A3; ADR-0034 §2; plan m1n step 6): while the foreground holds, no crash read is
// paid at all — no `logcat -b crash`, no `logcat -b events`, no device-clock read. Seen
// failing with the crash read gated on the process being gone alone (a `date` and a
// crash-buffer read on every tap).
func TestNoCrashReadWhenTheForegroundHeld(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, withReadOnlyReader(t, nil, formappDoc()))
	r := mustTap(t, f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`))
	if r.AppCrash != nil {
		t.Errorf("appCrash = %+v, want none while the foreground held", r.AppCrash)
	}
	calls := f.calls(t)
	if n := countPrefix(calls, crashLogcatHead+"crash"); n != 0 {
		t.Errorf("%d crash-buffer reads while the foreground held, want none (%q)", n, calls)
	}
	if n := countPrefix(calls, crashLogcatHead+"events"); n != 0 {
		t.Errorf("%d events-buffer reads while the foreground held, want none (%q)", n, calls)
	}
	if n := count(calls, crashDateCall); n != 0 {
		t.Errorf("%d device-clock reads while the foreground held, want none (%q)", n, calls)
	}
}

// Decision (A3; ADR-0034 §2; plan m1n step 6, fix round N1): the foreground changing is not
// enough — the app's process must also be gone. When the after read shows another window but
// `pidof` still answers the app's pid, the app is alive (a transient foreground change, not a
// crash) and no crash read is paid. Seen failing with the pidof half removed (a `date` and a
// crash-buffer read on a tap that only moved the foreground).
func TestNoCrashReadWhenTheAppIsAlive(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":          crashLog(t),
		uiSerial + ".date.out":     crashClock + "\n",
		uiSerial + ".apppidof.out": "7000\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, formappDoc(), launcherDoc()))
	r := mustTap(t, f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`))
	if r.AppCrash != nil {
		t.Fatalf("appCrash = %+v, want none while the app's process is alive", r.AppCrash)
	}
	calls := f.calls(t)
	if n := countPrefix(calls, crashLogcatHead+"crash"); n != 0 {
		t.Errorf("%d crash-buffer reads while the app was alive, want none (%q)", n, calls)
	}
	if n := count(calls, crashDateCall); n != 0 {
		t.Errorf("%d device-clock reads while the app was alive, want none (%q)", n, calls)
	}
	if n := count(calls, exitInfoCall); n != 0 {
		t.Errorf("%d exit-info reads while the app was alive, want none (%q)", n, calls)
	}
}

// Decision (A3; ADR-0034 §2; plan m1n step 6, fix round B3): `android` is the system ui, not
// an app, and it has no app process to have gone. A call whose before read shows its window
// never pays a crash read, whatever the process and the foreground afterwards. Seen failing
// with the package guard removed (the crash path runs for the system ui).
func TestNoCrashPathForTheSystemUiWindow(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, systemUiDoc(), launcherDoc()))
	r := mustTap(t, f.tapCall(t, `{"device":"emulator-5554","target":{"id":"dismiss"}}`))
	if r.AppCrash != nil {
		t.Errorf("appCrash = %+v, want none for the system ui's own window", r.AppCrash)
	}
	calls := f.calls(t)
	if n := count(calls, crashDateCall); n != 0 {
		t.Errorf("%d device-clock reads for the system ui, want none (%q)", n, calls)
	}
	if n := countPrefix(calls, crashLogcatHead+"crash"); n != 0 {
		t.Errorf("%d crash-buffer reads for the system ui, want none (%q)", n, calls)
	}
}

// Decision (A3; ADR-0034 §2; plan m1n step 6, fix round B3): the recorded level-5 false
// positive — tapping the dialog's own close button reported `appCrash {package: "android",
// reason: "1 (EXIT_SELF)"}`. When the before read is the system dialog, the call acted on the
// dialog, not the app, so the crash path never runs: no `date`, no crash read, no exit-info,
// even though the foreground afterwards is another window. Seen failing with the before-read
// dialog guard removed (the false appCrash reappears).
func TestNoCrashPathWhenTheCallActedOnTheDialog(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":          crashLog(t),
		uiSerial + ".date.out":     crashClock + "\n",
		uiSerial + ".exitinfo.out": exitInfoOut([2]string{"2026-10-07 21:12:06.039", "1 (EXIT_SELF)"}),
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, dialogDump(t), launcherDoc()))
	r := mustTap(t, f.tapCall(t, `{"device":"emulator-5554","target":{"id":"aerr_close"}}`))
	if r.AppCrash != nil {
		t.Fatalf("appCrash = %+v, want none: the call acted on the dialog", r.AppCrash)
	}
	if r.SystemDialog == nil || r.SystemDialog.Kind != "anr" {
		t.Errorf("systemDialog = %+v, want the anr dialog named", r.SystemDialog)
	}
	calls := f.calls(t)
	// One clock read is the dialog naming's own (`dialogSince` for a standalone window); the
	// crash path would add a second, with its crash-buffer and exit-info reads.
	if n := count(calls, crashDateCall); n != 1 {
		t.Errorf("%d device-clock reads after acting on the dialog, want one: the dialog naming's (%q)", n, calls)
	}
	if n := countPrefix(calls, crashLogcatHead+"crash"); n != 0 {
		t.Errorf("%d crash-buffer reads after acting on the dialog, want none (%q)", n, calls)
	}
	if n := count(calls, exitInfoCall); n != 0 {
		t.Errorf("%d exit-info reads after acting on the dialog, want none (%q)", n, calls)
	}
}

// --- the system dialog (ADR-0034 §3) ----------------------------------------------------

// Decision (A4; ADR-0034 §3; plan m1n step 6): when the before read shows the system dialog,
// a target outside it fails DEVICE_NOT_READY before anything is tapped: no `input` command,
// no acting request to the reader; details carry the dialog and its kind, and the failed call
// is marked untrusted. The cause names the kind only, never the package — the package is
// device text, and device text belongs in details (fix round B1). Seen failing with the
// refusal removed (the tap falls through to a resolution error), with the untrusted mark
// removed, and with the package quoted in the cause.
func TestTargetBehindADialogIsRefusedBeforeTheTap(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	files, r := withPersistentReaderR(t, files, dialogDump(t))
	f := newUIFixture(t, files)
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	ce := mustFail(t, out, ErrNotReady)
	if got, _ := ce.Details["state"].(string); got != "systemDialog" {
		t.Errorf("details.state = %v, want systemDialog (%+v)", ce.Details["state"], ce.Details)
	}
	sd, ok := ce.Details["systemDialog"].(*SystemDialog)
	if !ok || sd == nil || sd.Kind != "anr" || sd.Package != crashPackage {
		t.Errorf("details.systemDialog = %#v, want anr for %s", ce.Details["systemDialog"], crashPackage)
	}
	// D6: DEVICE_NOT_READY's dialog variant names the capability to call (device.tapElement)
	// and says repeating is safe — nothing was tapped or typed (plan m1n, D6).
	if !strings.Contains(ce.NextStep, "device.tapElement") || !strings.Contains(ce.NextStep, "safe to repeat") {
		t.Errorf("nextStep = %q; want device.tapElement and that repeating is safe (plan m1n, D6)", ce.NextStep)
	}
	if strings.Contains(ce.Cause, crashPackage) {
		t.Errorf("cause %q quotes the device-derived package; it belongs in details", ce.Cause)
	}
	if !out.Untrusted || out.Meta() == nil || !out.Meta().Untrusted {
		t.Errorf("the dialog refusal is not marked untrusted: %+v", out)
	}
	if !strings.HasSuffix(ce.Message, capability.UntrustedErrorNote) {
		t.Errorf("message %q does not end with the untrusted note", ce.Message)
	}
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps for a target behind the dialog, want none (%q)", n, f.calls(t))
	}
	if got := r.Acts(); len(got) != 0 {
		t.Errorf("the reader received %q for a target behind the dialog, want no act", got)
	}
	// N3: a dialog seen in the before read appeared before the action, so the events read
	// that names it looks the whole standalone window back, not from the action's start.
	since := epochSeconds(t, logcatSince(f.calls(t), "events"))
	if d := crashClockSecs - since; math.Abs(d-60) > 1 {
		t.Errorf("the before read's events window is %.1f s back, want ~60", d)
	}
}

// Decision (A4; ADR-0034 §3; plan m1n step 6): an `inputText` with no target cannot be aimed
// inside the dialog, so it fails the same way before anything is typed. Seen failing with the
// no-target branch removed (the text is typed behind the dialog).
func TestInputTextWithoutATargetIsRefusedBehindADialog(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, dialogDump(t)))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"hi"}`)
	ce := mustFail(t, out, ErrNotReady)
	if got, _ := ce.Details["state"].(string); got != "systemDialog" {
		t.Errorf("details.state = %v, want systemDialog (%+v)", ce.Details["state"], ce.Details)
	}
	if !out.Untrusted || out.Meta() == nil || !out.Meta().Untrusted {
		t.Errorf("the refusal is not marked untrusted: %+v", out)
	}
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps, want none: nothing was typed (%q)", n, f.calls(t))
	}
}

// Decision (A4; ADR-0034 §3; plan m1n step 6): an `inputText` with a target the dialog hides
// is refused the same way — the target does not resolve at all, so it is behind the dialog
// and nothing is typed. Seen failing with the target branch removed (the call proceeds to a
// resolution error or types behind the dialog).
func TestInputTextTargetBehindADialogIsRefused(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, dialogDump(t)))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"hi","target":{"id":"like"}}`)
	ce := mustFail(t, out, ErrNotReady)
	if got, _ := ce.Details["state"].(string); got != "systemDialog" {
		t.Errorf("details.state = %v, want systemDialog (%+v)", ce.Details["state"], ce.Details)
	}
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps, want none: nothing was typed (%q)", n, f.calls(t))
	}
}

// Decision (A4; ADR-0034 §3; plan m1n step 6): a target inside the dialog is acted on as
// usual — the dialog does not block the button the agent aimed at — and the result names the
// dialog, whose kind comes from the tree's own buttons and whose package from the recorded
// events buffer. Seen failing with the refusal applied to every target (the button is not
// tapped) and with the result's systemDialog dropped.
func TestTargetInsideTheDialogIsTapped(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, dialogDump(t)))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"aerr_wait"}}`)
	r := mustTap(t, out)
	if r.Tapped.ID != "aerr_wait" {
		t.Errorf("tapped %+v, want the dialog's wait button", r.Tapped)
	}
	if r.SystemDialog == nil || r.SystemDialog.Kind != "anr" || r.SystemDialog.Package != crashPackage {
		t.Errorf("systemDialog = %+v, want the anr dialog named", r.SystemDialog)
	}
	if n := countInputTaps(f.calls(t)); n != 1 {
		t.Errorf("%d input taps, want one on the button inside the dialog (%q)", n, f.calls(t))
	}
	// N7b: the before read named the dialog, so the after read is not searched again.
	if n := countPrefix(f.calls(t), crashLogcatHead+"events"); n != 1 {
		t.Errorf("%d events reads, want one: the before read's (%q)", n, f.calls(t))
	}
}

// Decision (A4; ADR-0034 §3; plan m1n step 6, fix round N7): the dialog's kind comes from the
// tree's own buttons, and the events buffer can hold both an am_crash and an am_anr. With an
// app-info button the tree is a crash dialog, so the newest am_crash names it — the recorded
// events' newest event overall is the form app's ANR, so a plain "newest wins" would name the
// wrong package. Seen failing with the kind preference removed (the ANR's package reported
// for a crash dialog).
func TestCrashDialogTakesItsPackageFromTheCrashEvent(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, crashDialogDump()))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"aerr_app_info"}}`)
	r := mustTap(t, out)
	if r.SystemDialog == nil || r.SystemDialog.Kind != "crash" {
		t.Fatalf("systemDialog = %+v, want the crash dialog named crash", r.SystemDialog)
	}
	if r.SystemDialog.Package != recordingCrashPackage {
		t.Errorf("systemDialog.package = %q, want the recorded am_crash's %s (the newest event is the ANR)",
			r.SystemDialog.Package, recordingCrashPackage)
	}
}

// Decision (A4; ADR-0034 §3; plan m1n step 6, fix round N2): a tree is the system dialog's
// only when its foreground is the system ui, not merely when some view carries one of the
// dialog's ids. An app that owns a view called aerr_close is tapped normally, and no dialog
// is named. Seen failing with the foreground requirement removed (the app's own view refused
// the call as a system dialog).
func TestAppNamedIdsAreNotASystemDialog(t *testing.T) {
	quickAct(t)
	f := newUIFixture(t, withReadOnlyReader(t, nil, appNamedDialogDoc(), appNamedDialogDoc()))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"aerr_close"}}`)
	r := mustTap(t, out)
	if r.SystemDialog != nil {
		t.Errorf("systemDialog = %+v, want none: the tree is the app's, not the system's", r.SystemDialog)
	}
	if n := countInputTaps(f.calls(t)); n != 1 {
		t.Errorf("%d input taps, want one on the app's own button (%q)", n, f.calls(t))
	}
}

// Decision (A4; ADR-0034 §3; plan m1n step 6, fix round N5): the dialog only hides a target
// that does not resolve at all. An ambiguous target describes itself, so its own error is
// returned — the dialog does not swallow it into a DEVICE_NOT_READY. Seen failing with the
// refusal applied to every resolution error (an ambiguous target refused as systemDialog).
func TestAmbiguousTargetIsNotHiddenByTheDialog(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, dialogDump(t)))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"class":"Button"}}`)
	ce := mustFail(t, out, ErrElementAmbiguous)
	if got := ce.Details["state"]; got != nil {
		t.Errorf("details.state = %v, want none: the error is the target's own, not a dialog", got)
	}
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps, want none (%q)", n, f.calls(t))
	}
}

// Decision (A4; ADR-0034 §3; plan m1n step 6, fix round N9): `device.uiTree` names the system
// dialog too — a read alone, with no action, still reports the dialog over the screen. The
// recorded dump's foreground is `android` and its buttons are the dialog's. Seen failing with
// the uiTree dialog wiring removed (the result carries no systemDialog).
func TestUITreeNamesTheDialog(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, dialogDump(t)))
	s := mustComplete(t, f.tree(t, uiSerial))
	if s.SystemDialog == nil || s.SystemDialog.Kind != "anr" || s.SystemDialog.Package != crashPackage {
		t.Fatalf("systemDialog = %+v, want the recorded anr dialog named", s.SystemDialog)
	}
	if s.Foreground != androidPackage {
		t.Errorf("foreground = %q, want %s from the recorded dump", s.Foreground, androidPackage)
	}
	// N3: a standalone read looks the whole window back.
	since := epochSeconds(t, logcatSince(f.calls(t), "events"))
	if d := crashClockSecs - since; math.Abs(d-60) > 1 {
		t.Errorf("uiTree's events window is %.1f s back, want ~60", d)
	}
}

// Decision (A4; ADR-0034 §3; plan m1n step 6, fix round N9): the read taken after the action
// names a dialog the action raised. The before read shows none, the after read shows one, and
// the result carries it — searched from the action's start, not the standalone window, since
// the action is what raised it (N3). Seen failing with the after read's dialog wiring removed
// (the result carries no systemDialog).
func TestAfterReadNamesTheDialog(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, formappDoc(), dialogDump(t)))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	r := mustTap(t, out)
	if r.SystemDialog == nil || r.SystemDialog.Kind != "anr" || r.SystemDialog.Package != crashPackage {
		t.Fatalf("systemDialog = %+v, want the after read's anr dialog", r.SystemDialog)
	}
	// The after read's events window starts at the action, not a minute back.
	since := epochSeconds(t, logcatSince(f.calls(t), "events"))
	if d := crashClockSecs - since; d > 5 {
		t.Errorf("the after read's events window is %.1f s back, want the action's start", d)
	}
}

// Decision (A4; ADR-0034 §3; plan m1n step 6, fix round N7): the events read that names the
// dialog's package is bounded by its own short budget, so a device that does not answer it
// promptly leaves the dialog named from its own buttons rather than stalling the call. Seen
// failing with the bound removed (the read waits the fake's whole 5 s delay).
func TestDialogEventsReadIsBounded(t *testing.T) {
	quickAct(t)
	// The before phase is widened past the events read's own bound (and past the delay), so
	// the only thing capping the read is systemDialogReadBound: were the read unbounded the
	// call would wait the full 5 s and come back with the recorded event's package.
	oldPhase := actionBeforePhase
	actionBeforePhase = 8 * time.Second
	defer func() { actionBeforePhase = oldPhase }()
	files := map[string]string{
		uiSerial + ".log":          crashLog(t),
		uiSerial + ".date.out":     crashClock + "\n",
		uiSerial + ".logcat.delay": "5000",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, dialogDump(t)))
	start := time.Now()
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	elapsed := time.Since(start)
	ce := mustFail(t, out, ErrNotReady)
	sd, ok := ce.Details["systemDialog"].(*SystemDialog)
	if !ok || sd == nil || sd.Kind != "anr" {
		t.Fatalf("details.systemDialog = %#v, want the anr dialog from the tree's buttons", ce.Details["systemDialog"])
	}
	if sd.Package != "" {
		t.Errorf("systemDialog.package = %q, want none: the events read never answered", sd.Package)
	}
	if elapsed > 4*time.Second {
		t.Errorf("the call took %v; the events read was not bounded to %v", elapsed, systemDialogReadBound)
	}
}

// --- inputText's crash path (ADR-0034 §2) ----------------------------------------------

// Decision (A3; ADR-0034 §2; plan m1n step 6, fix round N9): typing that leaves the app's
// process gone reports the crash, the same as a tap. A targetless call has no after read, so
// "no read observed the foreground" applies and the process check alone decides. Seen failing
// with inputText's crash path removed (no appCrash after a crash during typing).
func TestInputTextThatCrashesReportsTheCrash(t *testing.T) {
	quickAct(t)
	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, formappDoc()))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"hi"}`)
	r := mustInput(t, out)
	if r.AppCrash == nil || r.AppCrash.Source != "logcat" || r.AppCrash.Package != crashPackage {
		t.Fatalf("appCrash = %+v, want the crash from the logcat buffer", r.AppCrash)
	}
}

// Decision (A3; ADR-0034 §2; ADR-0020's 2026-10-06 line; plan m1n step 6, fix round N6): a
// focusing tap that was issued but whose field never reports focus ends the call completed —
// not with an error that invites a retry that taps twice — with the "nothing was typed"
// warning; and the crash it caused is diagnosed too, bounded at the tap's own instant, even
// though the before phase's clock has run out. Seen failing with the focus-wait-out branch
// returning an error (no result, no appCrash) and with the crash bound taken from the after
// phase (the window opens too late to catch the tap's crash).
func TestInputTextFocusWaitOutDiagnosesACrash(t *testing.T) {
	quickAct(t)
	oldPhase := actionBeforePhase
	actionBeforePhase = 700 * time.Millisecond
	defer func() { actionBeforePhase = oldPhase }()

	files := map[string]string{
		uiSerial + ".log":      crashLog(t),
		uiSerial + ".date.out": crashClock + "\n",
	}
	// The reader answers the focusing tap, but the tree never shows a focused node at the
	// tapped point, so the focus wait runs out.
	f := newUIFixture(t, withPersistentReader(t, files, formappDoc()))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"hi","target":{"id":"like"}}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v; a tap that landed must end completed", out.State, out.Error)
	}
	r, ok := out.Result.(InputTextResult)
	if !ok {
		t.Fatalf("result is %T, want InputTextResult", out.Result)
	}
	if !containsWarning(r.Warnings, nothingTypedWarning) {
		t.Errorf("warnings = %q, want the nothing-typed warning", r.Warnings)
	}
	if r.AppCrash == nil || r.AppCrash.Package != crashPackage {
		t.Fatalf("appCrash = %+v, want the tap's crash diagnosed", r.AppCrash)
	}
}

// containsWarning reports whether warnings holds want.
func containsWarning(warnings []string, want string) bool {
	for _, w := range warnings {
		if w == want {
			return true
		}
	}
	return false
}

// Decision (A3; ADR-0034 §2; plan m1n step 9): a crash that raises the system's "keeps
// stopping" dialog leaves the app's process alive while the dialog waits — its crash
// handler blocks on it — so `pidof` finds it; the dialog naming the app is then the proof,
// and the crash buffer is read anyway. Seen failing at level 5 (a second crash within a
// minute reported the dialog and no appCrash) and here with crashDialogFor removed.
func TestCrashDialogForTheAppReadsTheCrashThoughItsProcessLives(t *testing.T) {
	quickAct(t)
	before := string(synthDump(0, fmt.Sprintf(
		`<node class="android.widget.FrameLayout" package=%q bounds="[0,0][1080,2340]"><node class="android.widget.Button" package=%q resource-id=%q text="Like" bounds="[100,100][300,200]" clickable="true"></node></node>`,
		recordingCrashPackage, recordingCrashPackage, recordingCrashPackage+":id/like")))
	files := map[string]string{
		uiSerial + ".log":          crashLog(t),
		uiSerial + ".date.out":     crashClock + "\n",
		uiSerial + ".apppidof.out": "7000\n", // alive: the dialog holds its crash handler
	}
	f := newUIFixture(t, withReadOnlyReader(t, files, before, crashDialogDump()))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	r := mustTap(t, out)
	if r.SystemDialog == nil || r.SystemDialog.Kind != "crash" || r.SystemDialog.Package != recordingCrashPackage {
		t.Fatalf("systemDialog = %+v, want the crash dialog naming %s", r.SystemDialog, recordingCrashPackage)
	}
	if n := countPrefix(f.calls(t), crashLogcatHead+"crash"); n != 1 {
		t.Errorf("%d crash-buffer reads, want one although pidof found the process (%q)", n, f.calls(t))
	}
}
