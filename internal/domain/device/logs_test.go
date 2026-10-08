package device

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/schema"
)

// device.logs' snapshot (plan m1i step 2, check (a)): the fake over step 1's recordings,
// the whole path from the fake adb to the published result.

// logsDeviceClock is the device clock the fake's `date +%s.%N` answers with, well before
// the recordings' first line (1791336910.842325), so a read from it keeps them all.
const logsDeviceClock = "1791336900.500000000"

// logsPMTable is the fake's `pm list packages -U` table: the fixture's paid staging
// build, as step 1's pm-uid.txt recorded it.
const logsPMTable = "package:" + appID + " uid:10146\n"

// logsLog builds the fake's <serial>.log from the two step-1 recordings: the crash
// buffer's lines under the crash section and the merged dump's other lines under main.
// No crash block sits in two buffers, so crashes[] cannot list one twice. extraMain is
// appended to the main section, for a test that models another app's noise.
func logsLog(t *testing.T, extraMain ...string) string {
	t.Helper()
	var crash []string
	inCrash := map[string]bool{}
	for _, l := range rawLines(recording(t, "crash-buffer.txt")) {
		if strings.HasPrefix(l, "---") {
			continue
		}
		crash = append(crash, l)
		inCrash[l] = true
	}
	var b strings.Builder
	b.WriteString("--------- beginning of main\n")
	for _, l := range rawLines(recording(t, "app-lines.txt")) {
		if inCrash[l] {
			continue
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	for _, l := range extraMain {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString("--------- beginning of crash\n")
	for _, l := range crash {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// logsPlain is a log of just the given main-buffer lines: what a test that models a
// device's lines itself uses, so the recordings' own lines are not in the way.
func logsPlain(main ...string) string {
	return "--------- beginning of main\n" + strings.Join(main, "\n") + "\n"
}

// logsFixture is the UI fixture with the recorded log scenario and the pm table in
// place; a test overrides either through files.
func logsFixture(t *testing.T, files map[string]string) *uiFixture {
	t.Helper()
	base := map[string]string{
		uiSerial + ".log":    logsLog(t),
		uiSerial + ".pm.out": logsPMTable,
	}
	for k, v := range files {
		base[k] = v
	}
	return newUIFixture(t, base)
}

// launchedLogsFixture is logsFixture after a device.launch of the fixture's app, so the
// app record holds a launch and a pid, as a build-install-launch sequence leaves it.
func launchedLogsFixture(t *testing.T, files map[string]string) *uiFixture {
	t.Helper()
	quickLaunch(t)
	base := map[string]string{
		uiSerial + ".date.out":     logsDeviceClock + "\n",
		uiSerial + ".monkey.out":   "",
		uiSerial + ".apppidof.out": "6406\n",
	}
	for k, v := range files {
		base[k] = v
	}
	f := logsFixture(t, base)
	r := mustLaunch(t, f.launchCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))
	if r.PID == nil || *r.PID != 6406 {
		t.Fatalf("the launch recorded pid %v, want 6406", r.PID)
	}
	return f
}

func (f *uiFixture) logsCall(t *testing.T, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.logs", Params: []byte(params)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return f.wait(t, id)
}

func mustLogs(t *testing.T, out capability.Outcome) LogsResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(LogsResult)
	if !ok {
		t.Fatalf("result is %T, want LogsResult", out.Result)
	}
	return r
}

// quickLogs shrinks the snapshot's read bound so a test of a hung read is quick.
func quickLogs(t *testing.T) {
	t.Helper()
	read := logsReadTimeout
	logsReadTimeout = 400 * time.Millisecond
	t.Cleanup(func() { logsReadTimeout = read })
}

// Decision (plan m1i step 2, check (a)): after a launch, a device.logs of the fixture's
// app reads from the launch's device clock, filters by the uid pm gives, returns the
// created line, summarises the Java and the native crash (both still readable after the
// process died), marks the crash lines, and stores the capture as an untrusted text
// artifact.
func TestLogsSnapshotOfTheFixturesApp(t *testing.T) {
	f := launchedLogsFixture(t, nil)
	// maxLines high, so the whole app's log is inline and the assertions below read it
	// all; the cap itself is TestLogsTailWithoutAnApp's and TestLogsCaptureKeepsTheNewestWithinItsCap's.
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`","maxLines":1000}`))

	if r.Filter.ApplicationID != appID || r.Filter.By != "uid" || r.Filter.UID == nil || *r.Filter.UID != appUID {
		t.Fatalf("filter = %+v, want by uid %d of %s", r.Filter, appUID, appID)
	}
	if r.Device != uiSerial {
		t.Errorf("device = %q, want %q", r.Device, uiSerial)
	}

	// The read starts at the launch's device-clock instant, not the host's: the -T value
	// is the fake's `date`, formatted as step 1's G3 confirms.
	want := "-s " + uiSerial + " logcat -d -v threadtime,epoch,usec,uid -b main,system,crash -T 1791336900.500000"
	if n := count(f.calls(t), want); n != 1 {
		t.Errorf("%d reads like %q, want one: the device clock was not used for the window start", n, want)
	}

	created := -1
	for i, l := range r.Lines {
		if l.Tag == "DroidyardFixture" && l.PID == 6406 && strings.HasPrefix(l.Message, "created "+appID) {
			created = i
		}
	}
	if created < 0 {
		t.Fatalf("the first created line is not among the %d returned lines", len(r.Lines))
	}
	if r.Lines[created].Crash || r.Lines[created].Priority != "info" || r.Lines[created].PID != 6406 {
		t.Errorf("the created line = %+v, want an info line of pid 6406, not a crash line", r.Lines[created])
	}
	if r.Lines[created].Time != "2026-10-07T01:35:11.285389Z" {
		t.Errorf("the created line's time = %q, want 2026-10-07T01:35:11.285389Z", r.Lines[created].Time)
	}

	// The recording's three crash blocks, in its order: the first Java crash, then the
	// native one (its own lines carry crash_dump's uid, its header names the app), then
	// the second Java crash of the relaunch.
	if len(r.Crashes) != 3 {
		t.Fatalf("%d crashes, want the recording's 3: %+v", len(r.Crashes), r.Crashes)
	}
	java := r.Crashes[0]
	if java.PID != 6406 || java.Exception != "java.lang.IllegalStateException" || java.Message != "droidyard fixture crash" {
		t.Errorf("crashes[0] = %+v, want the fixture's Java crash", java)
	}
	if native := r.Crashes[1]; native.PID != 6874 || !strings.HasPrefix(native.Exception, "signal 11") {
		t.Errorf("crashes[1] = %+v, want the recorded native crash of pid 6874", native)
	}
	if second := r.Crashes[2]; second.PID != 7157 {
		t.Errorf("crashes[2] = %+v, want the relaunch's Java crash of pid 7157", second)
	}
	marked := 0
	for _, l := range r.Lines {
		if l.Crash {
			marked++
		}
	}
	if marked == 0 {
		t.Error("no returned line is marked as a crash line")
	}
	if r.EmptyReason != "" {
		t.Errorf("emptyReason = %q, want empty for a non-empty result", r.EmptyReason)
	}
	if r.Truncated {
		t.Error("truncated = true, want false: the whole log fits maxLines")
	}

	// The capture is the matched set as an untrusted ephemeral text artifact.
	if r.Capture == nil {
		t.Fatal("no capture artifact")
	}
	if r.Capture.MediaType != "text/plain" || !r.Capture.Untrusted || r.Capture.Durability != "ephemeral" {
		t.Errorf("capture ref = %+v, want an untrusted ephemeral text/plain artifact", *r.Capture)
	}
	e, err := f.store.Get(r.Capture.Ref)
	if err != nil {
		t.Fatalf("the capture is not readable: %v", err)
	}
	if !e.Ref.Untrusted || e.Ref.MediaType != "text/plain" {
		t.Errorf("capture entry untrusted %v media %q, want true and text/plain", e.Ref.Untrusted, e.Ref.MediaType)
	}
	if !strings.Contains(string(e.Data), "DroidyardFixture: created "+appID) {
		t.Error("the capture does not hold the created line")
	}
	if strings.Contains(string(e.Data), "1791336910.849349  1000") {
		t.Error("the capture holds a line of another uid")
	}
}

// Decision (plan m1i step 2, check (a)): another app's lines are excluded even when they
// carry the app's tags, and a forged `FATAL EXCEPTION` / `Process: <the app>, PID: …`
// block logged under another app's uid is not claimed as the app's crash.
func TestLogsAnotherAppsLinesAndForgedCrashExcluded(t *testing.T) {
	forged := []string{
		"         1791337500.000000  2000  9999  9999 E AndroidRuntime: FATAL EXCEPTION: main",
		"         1791337500.000000  2000  9999  9999 E AndroidRuntime: Process: " + appID + ", PID: 9999",
		"         1791337500.000000  2000  9999  9999 E AndroidRuntime: java.lang.IllegalStateException: forged",
		"         1791337501.000000  2000  8888  8888 I DroidyardFixture: created " + appID + " (another app)",
	}
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsLog(t, forged...)})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))

	for _, l := range r.Lines {
		if strings.Contains(l.Message, "(another app)") {
			t.Errorf("the other app's line is returned: %+v", l)
		}
	}
	for _, c := range r.Crashes {
		if c.PID == 9999 {
			t.Errorf("the forged crash block is claimed: %+v", c)
		}
	}
	// The app's own three crashes are still there.
	if len(r.Crashes) != 3 {
		t.Errorf("%d crashes, want the app's own 3: %+v", len(r.Crashes), r.Crashes)
	}
}

// Decision (plan m1i step 2, check (a)): an applicationId that is a prefix of another's
// claims none of its crashes.
func TestLogsPrefixApplicationIDClaimsNoCrash(t *testing.T) {
	f := launchedLogsFixture(t, nil)
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.flavoured.paid"}`))
	if len(r.Crashes) != 0 {
		t.Fatalf("the prefix id claimed %d crashes: %+v", len(r.Crashes), r.Crashes)
	}
	if r.Filter.By == "uid" && r.Filter.UID != nil && *r.Filter.UID == appUID {
		t.Error("the prefix id was given the staging app's uid")
	}
}

// Decision (plan m1i D5, check (a)): on a device whose logcat prints no uid column, the
// app is filtered by the pids the daemon knows (the launch's, pidof's and the Start proc
// lines'), the result says so, and the crash is still attributed.
func TestLogsWithoutAUIDColumnFiltersByPids(t *testing.T) {
	f := launchedLogsFixture(t, map[string]string{uiSerial + ".nouid": "1\n"})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))

	if r.Filter.By != "pids" {
		t.Fatalf("filter.by = %q, want pids on a device without the uid column", r.Filter.By)
	}
	found := false
	for _, p := range r.Filter.PIDs {
		if p == 6406 {
			found = true
		}
	}
	if !found {
		t.Errorf("filter.pids = %v, want the launch's pid 6406 among them", r.Filter.PIDs)
	}
	if len(r.Crashes) == 0 {
		t.Errorf("no crash attributed by pids: %+v", r)
	}
	warned := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "uid column") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want one naming the missing uid column", r.Warnings)
	}
}

// Decision (plan m1i D8): an explicit since of "launch" with no recorded launch is
// REQUEST_INVALID_PARAMS, and so is a timestamp that is not RFC 3339.
func TestLogsSinceLaunchWithoutALaunchIsInvalidParams(t *testing.T) {
	f := logsFixture(t, nil)
	out := f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`","since":"launch"}`)
	e := mustFail(t, out, capability.ErrInvalidParams)
	if !strings.Contains(e.NextStep, "device.launch") {
		t.Errorf("nextStep = %q, want it to name device.launch", e.NextStep)
	}
	mustFail(t, f.logsCall(t, `{"device":"emulator-5554","since":"yesterday"}`), capability.ErrInvalidParams)
}

// Decision (plan m1i D4, check (a)): with no applicationId the whole buffer is the
// window, the inline lines are the newest maxLines, counts say what was scanned and
// matched, and truncated says more matched than were returned.
func TestLogsTailWithoutAnApp(t *testing.T) {
	f := logsFixture(t, nil)
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","maxLines":3}`))

	if r.Filter.By != "none" || r.Filter.ApplicationID != "" {
		t.Errorf("filter = %+v, want by none", r.Filter)
	}
	if r.Counts.Scanned != 218 || r.Counts.Matched != 218 {
		t.Errorf("counts = %+v, want 218 scanned and 218 matched", r.Counts)
	}
	if r.Counts.Returned != 3 || len(r.Lines) != 3 {
		t.Fatalf("returned %d lines, want the newest 3", len(r.Lines))
	}
	if !r.Truncated {
		t.Error("truncated = false, want true: more matched than were returned")
	}
	// Newest kept: the last returned line is the one with the greatest device-clock time.
	if got := r.Lines[2].Time; got != "2026-10-07T01:37:09.297487Z" {
		t.Errorf("the newest returned line's time = %q, want 2026-10-07T01:37:09.297487Z", got)
	}

	// tags, minPriority and contains are host-side filters.
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","tags":["DroidyardFixture"]}`))
	if r.Counts.Matched == 0 || r.Counts.Matched == 218 {
		t.Errorf("tags matched %d lines, want the fixture's own only", r.Counts.Matched)
	}
	for _, l := range r.Lines {
		if l.Tag != "DroidyardFixture" {
			t.Errorf("a line of tag %q passed the tags filter", l.Tag)
		}
	}
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","minPriority":"fatal"}`))
	for _, l := range r.Lines {
		if l.Priority != "fatal" {
			t.Errorf("a %s line passed minPriority=fatal", l.Priority)
		}
	}
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","contains":"created `+appID+`"}`))
	if r.Counts.Matched == 0 {
		t.Error("contains matched nothing")
	}
	// None of the host-side filter strings ever reached a device command (plan m1i R4).
	for _, c := range f.calls(t) {
		for _, s := range []string{"DroidyardFixture", "created " + appID, "fatal"} {
			if strings.Contains(c, s) {
				t.Errorf("a call carries the caller's own string %q: %q", s, c)
			}
		}
	}
}

// Decision (plan m1i D7, check (a)): the filter runs before any cap, so an app's three
// lines are all returned though 6 MiB of other apps' lines surround them.
func TestLogsFiltersBeforeTheCap(t *testing.T) {
	app := func(i int) string {
		return fmt.Sprintf("         1791337%03d.000000 10146  6406  6406 I DroidyardFixture: app line %d", i, i)
	}
	noise := func(n int) []string {
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("         1791336%03d.000000  2000  9999  9999 I Noise   : %s", i, strings.Repeat("x", 200)))
		}
		return out
	}
	var main []string
	main = append(main, noise(15000)...)
	main = append(main, app(1), app(2), app(3))
	main = append(main, noise(15000)...)

	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(main...)})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))

	if r.Counts.Matched != 3 || r.Counts.Returned != 3 {
		t.Fatalf("counts = %+v, want the app's 3 lines matched and returned", r.Counts)
	}
	if r.Counts.Scanned < 30000 {
		t.Errorf("scanned %d lines, want the whole 6 MiB buffer", r.Counts.Scanned)
	}
	for _, l := range r.Lines {
		if !strings.HasPrefix(l.Message, "app line ") {
			t.Errorf("a noise line survived the filter: %+v", l)
		}
	}
	if r.Truncated {
		t.Error("truncated = true, want false: everything that matched was returned")
	}
}

// Decision (plan m1i D7): the capture keeps the newest matched lines within its 4 MiB
// bound, and the inline lines are the newest maxLines.
func TestLogsCaptureKeepsTheNewestWithinItsCap(t *testing.T) {
	line := func(i int) string {
		return fmt.Sprintf("         1791336%03d.000000 10146  6406  6406 I DroidyardFixture: line-%06d %s", i%1000, i, strings.Repeat("y", 64))
	}
	main := make([]string, 0, 60000)
	for i := 0; i < 60000; i++ {
		main = append(main, line(i))
	}
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(main...)})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`","maxLines":5}`))

	if r.Counts.Matched < 50000 {
		t.Fatalf("matched %d lines, want the app's whole 60000", r.Counts.Matched)
	}
	if r.Counts.Returned != 5 || !r.Truncated {
		t.Errorf("returned %d truncated %v, want 5 and true", r.Counts.Returned, r.Truncated)
	}
	if r.Capture == nil {
		t.Fatal("no capture artifact")
	}
	if r.Capture.Bytes > logsCaptureCap {
		t.Errorf("capture is %d bytes, want at most %d", r.Capture.Bytes, logsCaptureCap)
	}
	e, err := f.store.Get(r.Capture.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Data) > logsCaptureCap {
		t.Errorf("the capture's data is %d bytes, want at most %d", len(e.Data), logsCaptureCap)
	}
	if !strings.Contains(string(e.Data), "line-005999") {
		t.Error("the capture does not hold the newest matched line")
	}
	if strings.Contains(string(e.Data), "line-000000") {
		t.Error("the capture holds the oldest matched line: it did not keep the newest")
	}
}

// Decision (plan m1i D11, check (a)): emptyReason says which of the three it was.
func TestLogsEmptyReasons(t *testing.T) {
	otherApp := "         1791336911.000000  2000  9999  9999 I DroidyardFixture: not ours"
	// noKnownProcess: nothing about this app is known at all.
	f := logsFixture(t, map[string]string{uiSerial + ".pm.out": "", uiSerial + ".log": logsPlain(otherApp)})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.otherapp"}`))
	if r.EmptyReason != "noKnownProcess" {
		t.Errorf("emptyReason = %q, want noKnownProcess", r.EmptyReason)
	}

	// noLines: the app's process is known (the launch recorded it) but it logged nothing.
	f = launchedLogsFixture(t, map[string]string{uiSerial + ".log": logsPlain(otherApp)})
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))
	if r.EmptyReason != "noLines" {
		t.Errorf("emptyReason = %q, want noLines", r.EmptyReason)
	}

	// filtered: the app has lines, but the substring filter excluded them all.
	f = logsFixture(t, nil)
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`","contains":"no such text"}`))
	if r.EmptyReason != "filtered" {
		t.Errorf("emptyReason = %q, want filtered", r.EmptyReason)
	}
	if r.Counts.Matched != 0 {
		t.Errorf("matched = %d, want 0", r.Counts.Matched)
	}
}

// Decision (plan m1i D10, §Changes 2026-10-07): logcat exiting non-zero, and a read whose
// lines are not the format asked for, are DEVICE_LOGS_FAILED whose cause holds the
// command, not a device line; a read cut by its timeout is not a failure, only truncated.
func TestLogsLogcatFailuresAreLogsFailed(t *testing.T) {
	// A non-zero exit, which logcat gives for a bad argument while printing on stdout.
	f := logsFixture(t, map[string]string{
		uiSerial + ".logcat.exit": "1\n",
		uiSerial + ".logcat.err":  "device says: something went wrong\n",
	})
	e := mustFail(t, f.logsCall(t, `{"device":"emulator-5554"}`), ErrLogsFailed)
	if !strings.Contains(e.Cause, "exited with status 1") {
		t.Errorf("cause = %q, want the exit status", e.Cause)
	}
	if strings.Contains(e.Cause, "something went wrong") {
		t.Errorf("cause = %q, want no device line (ADR-0015 §1)", e.Cause)
	}

	// The calendar format: a read of the wrong shape, detected from the lines.
	f = logsFixture(t, map[string]string{uiSerial + ".log": string(recording(t, "format-threadtime,uid.txt"))})
	e = mustFail(t, f.logsCall(t, `{"device":"emulator-5554"}`), ErrLogsFailed)
	if strings.Contains(e.Cause, "10-07") {
		t.Errorf("cause = %q, want no device line", e.Cause)
	}

	// The read ran past its bound: a completed, truncated result, not an error.
	quickLogs(t)
	f = logsFixture(t, map[string]string{uiSerial + ".logcat.hang": "1\n"})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554"}`))
	if !r.Truncated || len(r.Lines) != 0 {
		t.Errorf("truncated %v with %d lines, want true and none", r.Truncated, len(r.Lines))
	}
	warned := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "did not finish") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want one naming the unfinished read", r.Warnings)
	}
}

// Decision (plan m1i R4): every parameter is validated before the device is touched, so a
// bad one runs no adb command at all.
func TestLogsRejectsBadParamsBeforeTheDevice(t *testing.T) {
	f := logsFixture(t, nil)
	for _, params := range []string{
		`{"device":"emulator-5554","applicationId":"not a package"}`,
		`{"device":"emulator-5554","tags":["a","b"],"minPriority":"loud"}`,
		`{"device":"emulator-5554","buffers":["radio"]}`,
		`{"device":"emulator-5554","maxLines":1001}`,
		`{"device":"emulator-5554","contains":"` + strings.Repeat("c", 257) + `"}`,
	} {
		mustFail(t, f.logsCall(t, params), capability.ErrInvalidParams)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("the device was touched for a bad parameter: %v", calls)
	}
}

// Decision (plan m1i D5, check (a)): the uid-column probe runs once per (serial,
// transport id) and is cached; a new transport id probes again.
func TestLogsUIDProbeIsCachedPerTransport(t *testing.T) {
	probe := "-s " + uiSerial + " logcat -d -v threadtime,epoch,usec,uid -b main -t 5"
	f := launchedLogsFixture(t, nil)
	params := `{"device":"emulator-5554","applicationId":"` + appID + `"}`
	mustLogs(t, f.logsCall(t, params))
	mustLogs(t, f.logsCall(t, params))
	if n := count(f.calls(t), probe); n != 1 {
		t.Fatalf("%d probes for two calls, want one: the answer was not cached", n)
	}
	// The same serial on a new transport: the cached answer is about the old transport.
	devicetest.Write(t, f.dir, "devices.out", []byte(uiList(strings.Replace(phoneLine, "transport_id:1", "transport_id:2", 1))))
	mustLogs(t, f.logsCall(t, params))
	if n := count(f.calls(t), probe); n != 2 {
		t.Errorf("%d probes after the transport changed, want 2", n)
	}
}

// Decision (step 2 review B1): a logged "device offline" is device text, not adb saying
// the device left; the read completes and the line is returned (as untrusted text).
// Seen failing when the disconnection check read the log's stdout.
func TestLogsDeviceTextNamingOfflineIsNotADisconnection(t *testing.T) {
	line := fmt.Sprintf("         1791336911.285000 %5d  6406  6406 I Bluetooth: device offline", appUID)
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(line)})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554"}`))
	found := false
	for _, l := range r.Lines {
		if strings.Contains(l.Message, "device offline") {
			found = true
		}
	}
	if !found {
		t.Errorf("the logged line is missing: %+v", r.Lines)
	}
}

// Decision (step 2 review B3; D7): a read cut by the cap ends mid-line; that partial line
// is dropped and the result is truncated with a warning, never a format error. Seen
// failing when the partial line went to the parser.
func TestLogsReadCutMidLineIsTruncatedNotFailed(t *testing.T) {
	c := logsReadCap
	logsReadCap = 300
	t.Cleanup(func() { logsReadCap = c })
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("         1791336911.%06d %5d  6406  6406 I DroidyardFixture: line %d of a long log", i, appUID, i))
	}
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(lines...)})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554"}`))
	if !r.Truncated || len(r.Lines) == 0 {
		t.Errorf("truncated %v with %d lines, want a truncated read with the lines before the cut", r.Truncated, len(r.Lines))
	}
}

// Decision (close review B1): after a wipe the serial is reused and pm no longer lists the
// app; pids recorded before it may now be another app's, so nothing is attributed and a
// warning says the app is not installed. Seen failing (an overlay reproduction): another
// app's line at the old pid came back as this app's, with no warning.
func TestLogsAppNotInstalledAttributesNothing(t *testing.T) {
	stranger := "         1791336911.500000 10200  6406  6406 I SomeOtherApp: secret of another app"
	f := launchedLogsFixture(t, map[string]string{uiSerial + ".pm.out": "", uiSerial + ".log": logsPlain(stranger)})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))
	if len(r.Lines) != 0 {
		t.Fatalf("lines = %+v, want none attributed to an app pm does not list", r.Lines)
	}
	warned := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "not installed") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want one saying the app is not installed", r.Warnings)
	}
}

// goldenCrashLog is the fake's <serial>.log holding dy3's recorded golden-app crash
// (testdata/logcat/golden-crash-dy3.txt) in the crash buffer.
func goldenCrashLog(t *testing.T) string {
	t.Helper()
	return string(recording(t, "golden-crash-dy3.txt"))
}

// Decision (plan m1o step 4; ADR-0036 Decision 3): a crashes[] entry carries the crash's
// top stack frames, the app's own first, next to the headline fields, which keep their
// values. dy3's recorded golden crash is the ground truth: the app's frame
// GoldenListScreen.kt:62 is the first of at most five, and exception and message are what
// they were before the fields existed.
func TestCrashEntryCarriesCausesAndFrames(t *testing.T) {
	f := logsFixture(t, map[string]string{
		uiSerial + ".log":    goldenCrashLog(t),
		uiSerial + ".pm.out": "package:dev.droidyard.golden uid:10146\n",
	})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"dev.droidyard.golden"}`))
	if len(r.Crashes) != 1 {
		t.Fatalf("%d crashes, want dy3's one: %+v", len(r.Crashes), r.Crashes)
	}
	c := r.Crashes[0]
	if c.PID != 2934 || c.Exception != "java.lang.IllegalStateException" || c.Message != "golden app crash" || c.Time != "2026-10-07T14:35:43.706519Z" {
		t.Errorf("the headline fields changed: %+v", c)
	}
	if len(c.Frames) != crashFramesMax {
		t.Fatalf("%d frames, want %d: %q", len(c.Frames), crashFramesMax, c.Frames)
	}
	if !strings.Contains(c.Frames[0], "GoldenListScreen.kt:62") || !strings.HasPrefix(c.Frames[0], "dev.droidyard.golden.") {
		t.Errorf("frames[0] = %q, want the app's GoldenListScreen.kt:62 frame first", c.Frames[0])
	}
	for _, fr := range c.Frames {
		if strings.HasPrefix(fr, "\tat ") || strings.HasPrefix(fr, "at ") {
			t.Errorf("frame %q keeps the log's \"at\" prefix", fr)
		}
	}
	if len(c.Causes) != 0 {
		t.Errorf("causes = %q, want none: dy3's block has no Caused by line", c.Causes)
	}
}

// Decision (plan m1o step 4): causes is the block's "Caused by:" chain in order, each the
// exception class and message as logcat printed them without the prefix; a "... N more"
// elision and a "Suppressed:" line are not causes; and the chain is bounded like frames.
// The block is constructed (no recording has a Caused by), in the format of the recorded
// ones.
func TestCrashEntryCausesAreTheCausedByChain(t *testing.T) {
	line := func(msg string) string {
		return "         1791337500.000000 10146  4000  4000 E AndroidRuntime: " + msg
	}
	block := []string{
		"--------- beginning of crash",
		line("FATAL EXCEPTION: main"),
		line("Process: " + appID + ", PID: 4000"),
		line("java.lang.RuntimeException: Unable to start activity"),
		line("\tat android.app.ActivityThread.performLaunchActivity(ActivityThread.java:3449)"),
		line("Caused by: java.lang.IllegalStateException: no network"),
		line("\tat dev.droidyard.flavoured.Repo.load(Repo.kt:12)"),
		line("\t... 11 more"),
		line("\tSuppressed: java.lang.Exception: ignored"),
		line("Caused by: java.io.IOException: disk full"),
	}
	for i := 0; i < 6; i++ {
		block = append(block, line(fmt.Sprintf("Caused by: e%d.Cause: n%d", i, i)))
	}
	f := logsFixture(t, map[string]string{
		uiSerial + ".log":    strings.Join(block, "\n") + "\n",
		uiSerial + ".pm.out": "package:" + appID + " uid:10146\n",
	})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))
	if len(r.Crashes) != 1 {
		t.Fatalf("%d crashes, want one: %+v", len(r.Crashes), r.Crashes)
	}
	c := r.Crashes[0]
	if c.Exception != "java.lang.RuntimeException" || c.Message != "Unable to start activity" {
		t.Errorf("the headline fields changed: %+v", c)
	}
	want := []string{
		"java.lang.IllegalStateException: no network",
		"java.io.IOException: disk full",
		"e0.Cause: n0", "e1.Cause: n1", "e2.Cause: n2",
	}
	if !slices.Equal(c.Causes, want) {
		t.Errorf("causes = %q, want the first %d of the chain, in order, without the prefix: %q", c.Causes, crashCausesMax, want)
	}
	// Frames are every "at" line of the block, whichever exception it belongs to, the
	// app's own first and the top of the stack after it. appID's suffix is not the code
	// package (dev.droidyard.flavoured), so a frame is the app's by exclusion of platform
	// prefixes, not by the applicationId.
	wantFrames := []string{
		"dev.droidyard.flavoured.Repo.load(Repo.kt:12)",
		"android.app.ActivityThread.performLaunchActivity(ActivityThread.java:3449)",
	}
	if !slices.Equal(c.Frames, wantFrames) {
		t.Errorf("frames = %q, want %q", c.Frames, wantFrames)
	}
}

// Decision (plan m1o step 4, fix round B1): the onCreate shape. The top exception holds
// seven framework frames and the app's own sit only under "Caused by:"; frames lead with
// the app's two, in stack order, and fill the other three from the top of the stack, so the
// entry carries an app location instead of five framework lines. A platform class is the
// app's frame never, and a library the app ships (not a platform prefix) is. Seen failing
// with the old extractor (the first five "at" lines).
func TestCrashEntryFramesLeadWithTheAppsOwn(t *testing.T) {
	line := func(msg string) string {
		return "         1791337500.000000 10146  4000  4000 E AndroidRuntime: " + msg
	}
	block := []string{
		"--------- beginning of crash",
		line("FATAL EXCEPTION: main"),
		line("Process: " + appID + ", PID: 4000"),
		line("java.lang.RuntimeException: Unable to start activity ComponentInfo{x/y}"),
		line("\tat android.app.ActivityThread.performLaunchActivity(ActivityThread.java:3449)"),
		line("\tat android.app.ActivityThread.handleLaunchActivity(ActivityThread.java:3601)"),
		line("\tat android.app.servertransaction.LaunchActivityItem.execute(LaunchActivityItem.java:85)"),
		line("\tat android.app.ActivityThread$H.handleMessage(ActivityThread.java:2066)"),
		line("\tat android.os.Handler.dispatchMessage(Handler.java:106)"),
		line("\tat android.os.Looper.loop(Looper.java:223)"),
		line("\tat com.android.internal.os.ZygoteInit.main(ZygoteInit.java:1003)"),
		line("Caused by: java.lang.IllegalStateException: no network"),
		line("\tat kotlinx.coroutines.DispatchedTask.run(DispatchedTask.kt:100)"),
		line("\tat dev.droidyard.flavoured.Repo.load(Repo.kt:12)"),
		line("\tat com.squareup.okhttp3.RealCall.execute(RealCall.kt:30)"),
		line("\tat dev.droidyard.flavoured.MainActivity.onCreate(MainActivity.kt:30)"),
	}
	f := logsFixture(t, map[string]string{
		uiSerial + ".log":    strings.Join(block, "\n") + "\n",
		uiSerial + ".pm.out": "package:" + appID + " uid:10146\n",
	})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))
	if len(r.Crashes) != 1 {
		t.Fatalf("%d crashes, want one: %+v", len(r.Crashes), r.Crashes)
	}
	want := []string{
		"dev.droidyard.flavoured.Repo.load(Repo.kt:12)",
		"com.squareup.okhttp3.RealCall.execute(RealCall.kt:30)",
		"dev.droidyard.flavoured.MainActivity.onCreate(MainActivity.kt:30)",
		"android.app.ActivityThread.performLaunchActivity(ActivityThread.java:3449)",
		"android.app.ActivityThread.handleLaunchActivity(ActivityThread.java:3601)",
	}
	if got := r.Crashes[0].Frames; !slices.Equal(got, want) {
		t.Errorf("frames = %q, want %q", got, want)
	}
}

// Decision (plan m1o step 4, fix round nit): only a line that begins with "Caused by: " is
// a cause. A "Caused by:" nested under a "Suppressed:" exception is printed indented
// (tab-prefixed) and belongs to the suppressed one; a substring match would take it for
// the crash's own cause. Seen failing with strings.Contains in place of HasPrefix.
func TestCrashEntryCauseIndentedUnderSuppressedIsNotACause(t *testing.T) {
	line := func(msg string) string {
		return "         1791337500.000000 10146  4000  4000 E AndroidRuntime: " + msg
	}
	block := []string{
		"--------- beginning of crash",
		line("FATAL EXCEPTION: main"),
		line("Process: " + appID + ", PID: 4000"),
		line("java.lang.IllegalStateException: top"),
		line("\tat dev.droidyard.flavoured.Repo.load(Repo.kt:12)"),
		line("\tSuppressed: java.lang.Exception: ignored"),
		line("\t\tat dev.droidyard.flavoured.Cleanup.run(Cleanup.kt:5)"),
		line("\tCaused by: java.lang.Exception: the suppressed one's cause"),
		line("Caused by: java.io.IOException: the crash's own cause"),
	}
	f := logsFixture(t, map[string]string{
		uiSerial + ".log":    strings.Join(block, "\n") + "\n",
		uiSerial + ".pm.out": "package:" + appID + " uid:10146\n",
	})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))
	if len(r.Crashes) != 1 {
		t.Fatalf("%d crashes, want one: %+v", len(r.Crashes), r.Crashes)
	}
	c := r.Crashes[0]
	if want := []string{"java.io.IOException: the crash's own cause"}; !slices.Equal(c.Causes, want) {
		t.Errorf("causes = %q, want %q", c.Causes, want)
	}
	// The suppressed exception's own frame is indented twice, and is no frame of the crash.
	if want := []string{"dev.droidyard.flavoured.Repo.load(Repo.kt:12)"}; !slices.Equal(c.Frames, want) {
		t.Errorf("frames = %q, want %q", c.Frames, want)
	}
}

// Decision (ADR-0036 Decision 3): the change is additive. The two new fields are optional
// and omitted when empty, so an entry without them is the pre-0.13 entry byte for byte;
// and both are device text, tagged untrusted in the published schema (ADR-0015 §1).
func TestCrashEntryFieldsAreAdditiveAndUntrusted(t *testing.T) {
	b, err := json.Marshal(Crash{PID: 1, Time: "t", Exception: "e", Message: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"pid":1,"time":"t","exception":"e","message":"m"}`; got != want {
		t.Errorf("an entry without causes and frames marshals to %s, want %s", got, want)
	}

	reg := capability.NewRegistry()
	Register(reg, NewADB(nil, nil), testStore(t), nil)
	doc, err := schema.Generate(reg, "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Capabilities {
		if c.Name != "device.logs" {
			continue
		}
		var out any
		if err := json.Unmarshal(c.Output, &out); err != nil {
			t.Fatal(err)
		}
		props := findProperties(out, "crashes")
		if props == nil {
			t.Fatal("no crashes[] item schema in device.logs's output")
		}
		for _, name := range []string{"causes", "frames"} {
			p, _ := props[name].(map[string]any)
			if p == nil || p["x-droidyard-untrusted"] != true {
				t.Errorf("crashes[].%s = %v, want a property tagged x-droidyard-untrusted", name, props[name])
			}
		}
		for _, name := range []string{"pid", "time", "exception", "message"} {
			if props[name] == nil {
				t.Errorf("crashes[].%s is gone: the change must keep every existing field", name)
			}
		}
		return
	}
	t.Fatal("device.logs is not registered")
}

// findProperties returns the item schema's properties of the array property named name,
// wherever it sits in the schema tree.
func findProperties(v any, name string) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		if p, ok := t[name].(map[string]any); ok {
			if items, ok := p["items"].(map[string]any); ok {
				if props, ok := items["properties"].(map[string]any); ok {
					return props
				}
			}
		}
		for _, x := range t {
			if r := findProperties(x, name); r != nil {
				return r
			}
		}
	case []any:
		for _, x := range t {
			if r := findProperties(x, name); r != nil {
				return r
			}
		}
	}
	return nil
}

// Decision (plan m1o step 4; ADR-0036 Decision 3): maxLines defaults to 50, not 100, and
// an explicit value is still honoured up to 1000. The recorded log holds 218 lines.
func TestMaxLinesDefaultsTo50(t *testing.T) {
	f := logsFixture(t, nil)
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554"}`))
	if r.Counts.Matched != 218 || r.Counts.Returned != 50 || len(r.Lines) != 50 || !r.Truncated {
		t.Errorf("without maxLines: matched %d returned %d lines %d truncated %v, want 218 matched and the newest 50", r.Counts.Matched, r.Counts.Returned, len(r.Lines), r.Truncated)
	}
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","maxLines":100}`))
	if r.Counts.Returned != 100 {
		t.Errorf("maxLines 100 returned %d lines, want 100: an explicit value is honoured", r.Counts.Returned)
	}
}

// Decision (the m1o step 5 fix round; a bug since m1i): when a read is truncated, the
// inline lines are the newest maxLines, and each keeps the crash flag of its own line. The
// flags were once read from the start of the matched set, so every returned line of a
// truncated read carried the flag of an earlier one: the crash block's first lines went
// unflagged and unrelated lines after it were flagged. The check names lines instead of
// counting flags: at the default maxLines (50 of 208 matched) the relaunch's FATAL
// EXCEPTION block is flagged, and the lines around it are not.
func TestTruncatedReadKeepsEachLinesCrashFlag(t *testing.T) {
	f := launchedLogsFixture(t, nil)
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))
	if !r.Truncated || r.Counts.Matched <= r.Counts.Returned {
		t.Fatalf("the read is not truncated (matched %d, returned %d): the test needs a cut", r.Counts.Matched, r.Counts.Returned)
	}
	flagged := func(tag, msgPrefix string) (crash, found bool) {
		for _, l := range r.Lines {
			if l.Tag == tag && strings.HasPrefix(l.Message, msgPrefix) {
				return l.Crash, true
			}
		}
		return false, false
	}
	for tag, msgs := range map[string][]string{
		"AndroidRuntime": {"FATAL EXCEPTION: main", "Process: " + appID, "java.lang.IllegalStateException", "\tat dev.droidyard.flavoured.MainActivity", "\tat com.android.internal.os.ZygoteInit.main"},
	} {
		for _, m := range msgs {
			if crash, found := flagged(tag, m); !found || !crash {
				t.Errorf("%s line %q: found %v, crash flag %v; want a line of the crash block, flagged", tag, m, found, crash)
			}
		}
	}
	for _, c := range []struct{ tag, msg string }{
		{"Process", "Sending signal. PID: 7157 SIG: 9"}, // after the block, not part of it
		{"AndroidRuntime", "Shutting down VM"},          // before it
		{"DroidyardFixture", "created " + appID},
	} {
		if crash, found := flagged(c.tag, c.msg); !found || crash {
			t.Errorf("%s line %q: found %v, crash flag %v; want a line outside the crash block, not flagged", c.tag, c.msg, found, crash)
		}
	}
}
