package device

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// The line filter and the processes of device.logs (plan m3i step 3; ADR-0135 §1-§4), whole
// path from the fake adb to the published result.

// lrow is one log line in the uid format the daemon asks for. sec orders the lines (they
// are one second apart, so a fake that merges buffers by time keeps this order).
func lrow(sec int, uid string, pid int, prio, tag, msg string) string {
	return fmt.Sprintf("%d.%06d %5s %5d %5d %s %-12s: %s", 1791337000+sec, 123456, uid, pid, pid, prio, tag, msg)
}

// lineTexts are the messages of the returned lines, in order.
func lineTexts(r LogsResult) []string {
	out := make([]string, 0, len(r.Lines))
	for _, l := range r.Lines {
		out = append(out, l.Message)
	}
	return out
}

// Decision (ADR-0135 §3, plan m3i Q2): the line filter is the owner's pipeline in
// parameters. excludeTags drops a tag; match is an OR of RE2 patterns over "tag: message",
// so "^Tag: " selects a tag and a message that only mentions it does not count; exclude
// drops; tags is still an AND with match; a tag's own priority replaces minPriority and S
// silences the tag; "filtered" covers the new filters.
func TestLogsLineFilter(t *testing.T) {
	rows := []string{
		lrow(0, "1000", 700, "I", "SurfaceFlinger", "setDisplayStateLocked"),
		lrow(1, "1000", 700, "E", "SurfaceFlinger", "buffer queue abandoned"),
		lrow(2, "1000", 700, "D", "SurfaceFlinger", "noise again"),
		lrow(3, "1000", 661, "I", "ActivityTaskManager", "Removed 1a2b3c com.facebook.orca#0"),
		lrow(4, "10150", 900, "D", "KeyButtonView", "Pressed back"),
		lrow(5, "2000", 910, "I", "Other", "mentions KeyButtonView: not a tag"),
		lrow(6, "2000", 920, "W", "Noise", "noisy chatter"),
		lrow(7, "2000", 930, "I", "Tagged1", "alpha"),
		lrow(8, "2000", 931, "E", "Tagged2", "beta"),
	}
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(rows...)})
	logs := func(extra string) LogsResult {
		t.Helper()
		return mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","maxLines":100,`+extra+`}`))
	}
	tagSet := func(r LogsResult) []string {
		var out []string
		for _, l := range r.Lines {
			out = append(out, l.Tag)
		}
		return out
	}

	r := logs(`"excludeTags":["SurfaceFlinger"]`)
	if r.Counts.Matched != 6 || slices.Contains(tagSet(r), "SurfaceFlinger") {
		t.Errorf("excludeTags: matched %d, tags %v; want the 6 lines of other tags", r.Counts.Matched, tagSet(r))
	}

	// Either of two patterns; the second selects a tag by anchoring on the subject, which
	// is "tag: message", so the "Other" line that only mentions "KeyButtonView: " stays out.
	r = logs(`"match":["Removed [0-9a-f]+ com\\.facebook\\.orca#","^KeyButtonView: "]`)
	if got, want := lineTexts(r), []string{"Removed 1a2b3c com.facebook.orca#0", "Pressed back"}; !slices.Equal(got, want) {
		t.Errorf("match: %q; want %q", got, want)
	}
	if r.Counts.Matched != 2 {
		t.Errorf("match: counts.matched %d, want 2", r.Counts.Matched)
	}
	if r = logs(`"match":["(?i)REMOVED"]`); r.Counts.Matched != 1 {
		t.Errorf("(?i): matched %d, want the Removed line", r.Counts.Matched)
	}

	r = logs(`"exclude":["noisy","^SurfaceFlinger: "]`)
	if r.Counts.Matched != 5 || slices.Contains(tagSet(r), "Noise") || slices.Contains(tagSet(r), "SurfaceFlinger") {
		t.Errorf("exclude: matched %d, tags %v; want 5 lines without Noise or SurfaceFlinger", r.Counts.Matched, tagSet(r))
	}

	// tags is an AND with the patterns, as it is with contains.
	r = logs(`"tags":["Tagged1","Tagged2"],"match":["alpha"]`)
	if got := lineTexts(r); !slices.Equal(got, []string{"alpha"}) {
		t.Errorf("tags AND match: %q; want alpha alone", got)
	}
	r = logs(`"tags":["Tagged1"],"match":["beta"]`)
	if r.Counts.Matched != 0 || r.EmptyReason != "filtered" {
		t.Errorf("tags AND a match of another tag: matched %d, emptyReason %q; want none, filtered", r.Counts.Matched, r.EmptyReason)
	}

	// A tag's own minimum: SurfaceFlinger keeps its error only, every other tag is
	// untouched; S drops the tag.
	r = logs(`"tagPriorities":["SurfaceFlinger:E"]`)
	var sf []string
	for _, l := range r.Lines {
		if l.Tag == "SurfaceFlinger" {
			sf = append(sf, l.Priority)
		}
	}
	if !slices.Equal(sf, []string{"error"}) || r.Counts.Matched != 7 {
		t.Errorf("SurfaceFlinger:E: its priorities %v, matched %d; want [error] and 7", sf, r.Counts.Matched)
	}
	if r = logs(`"tagPriorities":["Noise:S"]`); slices.Contains(tagSet(r), "Noise") || r.Counts.Matched != 8 {
		t.Errorf("Noise:S: tags %v, matched %d; want the other 8 lines", tagSet(r), r.Counts.Matched)
	}
	// The tag's minimum replaces minPriority: Noise at warn passes minPriority error.
	r = logs(`"minPriority":"error","tagPriorities":["Noise:W"]`)
	if got := lineTexts(r); !slices.Equal(got, []string{"buffer queue abandoned", "noisy chatter", "beta"}) {
		t.Errorf("minPriority error with Noise:W: %q; want the two errors and Noise's warn", got)
	}

	// Nothing survives: the lines were in scope, so the filters excluded them.
	r = logs(`"match":["no such text"]`)
	if r.Counts.Matched != 0 || r.EmptyReason != "filtered" {
		t.Errorf("a pattern nothing matches: matched %d, emptyReason %q; want 0 and filtered", r.Counts.Matched, r.EmptyReason)
	}
}

// Decision (ADR-0135 §4, plan m3i Q3): processes are named as ps prints them, resolved by
// one fixed ps per call on the host, plus trusted Start proc lines (ActivityManager, at the
// system uid); the scope is the union with the application's uid, and an application's
// crash lines join it only through applicationId.
func TestLogsProcesses(t *testing.T) {
	const ps = "  PID NAME\n  612 system_server\n  700 surfaceflinger\n 1033 com.android.systemui\n 6406 " + appID + "\n"
	base := []string{
		lrow(0, "1000", 612, "I", "SystemServer", "from system_server"),
		lrow(1, "1000", 1033, "I", "SysUI", "from systemui"),
		lrow(2, "10222", 2000, "I", "Other", "from another app"),
		lrow(3, "10146", 6406, "I", "DroidyardFixture", "from the app"),
		lrow(4, "1000", 700, "I", "SurfaceFlinger", "from surfaceflinger"),
	}
	start := []string{
		lrow(5, "1000", 612, "I", "ActivityManager", "Start proc 3001:com.android.systemui/u0a42 for service {x}"),
		lrow(6, "10155", 3001, "I", "SysUI", "from the restarted systemui"),
		// Forgeries: any app can log the text. Another tag, and an app's uid.
		lrow(7, "1000", 612, "I", "Forged", "Start proc 4002:com.android.systemui/u0a42 for x"),
		lrow(8, "10146", 6406, "I", "ActivityManager", "Start proc 4001:com.android.systemui/u0a42 for x"),
		lrow(9, "10155", 4001, "I", "SysUI", "from a pid a forged line named (uid)"),
		lrow(10, "10155", 4002, "I", "SysUI", "from a pid a forged line named (tag)"),
	}
	fixture := func(rows []string, extra map[string]string) *uiFixture {
		files := map[string]string{uiSerial + ".log": logsPlain(rows...), uiSerial + ".ps.out": ps}
		for k, v := range extra {
			files[k] = v
		}
		return logsFixture(t, files)
	}
	logs := func(f *uiFixture, params string) LogsResult {
		t.Helper()
		return mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","maxLines":100,`+params+`}`))
	}
	const both = `"processes":["system_server","com.android.systemui"]`

	// Lines of 612 and 1033 are in, 2000's are not; the list is read once.
	f := fixture(base, nil)
	r := logs(f, both)
	if got, want := lineTexts(r), []string{"from system_server", "from systemui"}; !slices.Equal(got, want) {
		t.Errorf("processes: %q; want %q", got, want)
	}
	if n := count(f.calls(t), "-s "+uiSerial+" shell ps -A -o PID,NAME"); n != 1 {
		t.Errorf("%d ps reads, want one", n)
	}
	wantProcs := []LogProcess{{Name: "system_server", PIDs: []int{612}}, {Name: "com.android.systemui", PIDs: []int{1033}}}
	if fmt.Sprint(r.Filter.Processes) != fmt.Sprint(wantProcs) || r.Filter.By != "none" {
		t.Errorf("filter = %+v; want processes %v and by none", r.Filter, wantProcs)
	}

	// With an applicationId the scope is the union of the two, not their intersection.
	r = logs(f, `"applicationId":"`+appID+`",`+both)
	if got, want := lineTexts(r), []string{"from system_server", "from systemui", "from the app"}; !slices.Equal(got, want) {
		t.Errorf("applicationId and processes: %q; want the union %q", got, want)
	}
	if r.Filter.By != "uid" || r.Filter.UID == nil || *r.Filter.UID != appUID {
		t.Errorf("filter = %+v; want the application selected by uid", r.Filter)
	}

	// A trusted Start proc line adds the pid it names; the same text under another tag, or
	// from an application's uid, adds nothing.
	f = fixture(append(slices.Clone(base), start...), nil)
	r = logs(f, `"processes":["com.android.systemui"]`)
	got := strings.Join(lineTexts(r), "|")
	if !strings.Contains(got, "from the restarted systemui") || !strings.Contains(got, "from systemui") {
		t.Errorf("a trusted Start proc: %q; want pids 1033 (ps) and 3001 (Start proc)", got)
	}
	if strings.Contains(got, "forged") {
		t.Errorf("a forged Start proc line was trusted: %q", got)
	}
	if want := "[{com.android.systemui [1033 3001]}]"; fmt.Sprint(r.Filter.Processes) != want {
		t.Errorf("filter.processes = %v; want %s", r.Filter.Processes, want)
	}

	// Another app's crash block stays out of the lines of a call that named processes
	// alone, and crashes[] still lists it, as it does for any call without an applicationId.
	f = logsFixture(t, map[string]string{uiSerial + ".ps.out": ps})
	all := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554"}`))
	r = logs(f, `"processes":["system_server"]`)
	if len(r.Crashes) == 0 || len(r.Crashes) != len(all.Crashes) {
		t.Errorf("crashes = %d, want the same %d as without processes", len(r.Crashes), len(all.Crashes))
	}
	for _, l := range r.Lines {
		if l.Crash {
			t.Errorf("a crash line of another app is in the lines of a processes-only call: %+v", l)
		}
	}
	if r.EmptyReason != "noLines" {
		t.Errorf("emptyReason = %q; want noLines (system_server is known and logged nothing here)", r.EmptyReason)
	}

	// A failed ps is a warning, and the Start proc lines still name the process.
	f = fixture(append(slices.Clone(base), start...), map[string]string{uiSerial + ".ps.exit": "1"})
	r = logs(f, `"processes":["com.android.systemui"]`)
	if !slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, "process list") && strings.Contains(w, "status 1") }) {
		t.Errorf("warnings = %q; want one naming the failed process list", r.Warnings)
	}
	if got := strings.Join(lineTexts(r), "|"); strings.Contains(got, "from systemui") || !strings.Contains(got, "from the restarted systemui") {
		t.Errorf("without ps: %q; want the Start proc pid 3001 only", got)
	}

	// Nothing named was found: no evidence the process ran.
	f = fixture(base, nil)
	r = logs(f, `"processes":["com.example.nothere"]`)
	if r.EmptyReason != "noKnownProcess" || len(r.Lines) != 0 {
		t.Errorf("an unknown process: emptyReason %q, %d lines; want noKnownProcess and none", r.EmptyReason, len(r.Lines))
	}
	if fmt.Sprint(r.Filter.Processes) != "[{com.example.nothere []}]" {
		t.Errorf("filter.processes = %v; want the name with no pids", r.Filter.Processes)
	}
}

// Decision (plan m1i R4, ADR-0135 §1): no caller filter text reaches an adb argument.
// Every filter is set at once; the recorded argv holds none of its strings.
func TestLogsFilterTextNeverReachesADB(t *testing.T) {
	f := logsFixture(t, map[string]string{
		uiSerial + ".log":    logsPlain(lrow(0, "1000", 612, "I", "SystemServer", "x")),
		uiSerial + ".ps.out": "  PID NAME\n  612 system_server\n",
	})
	mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","tags":["TagQxz"],"minPriority":"info","contains":"needleQxz",
		"excludeTags":["ExcTagQxz"],"tagPriorities":["TpTagQxz:E"],"match":["matchPatQxz[0-9]+"],"exclude":["exclPatQxz"],
		"processes":["proc.nameQxz","system_server"]}`))
	calls := f.calls(t)
	if len(calls) == 0 {
		t.Fatal("no adb call was recorded")
	}
	for _, c := range calls {
		if strings.Contains(c, "Qxz") {
			t.Errorf("an adb argv holds filter text: %q", c)
		}
	}
}

// Decision (ADR-0135 §2): a filter pass that spends its budget stops, and says so. Lines
// are judged from the newest back, because the newest are the ones device.logs keeps: the
// lines filtered before the stop are the newest, and the warning names the device time of
// the oldest one judged. A call with no pattern never reads the clock, so a spent budget
// changes nothing for it.
func TestLogsFilterBudget(t *testing.T) {
	var rows []string
	for i := 0; i < 5; i++ {
		rows = append(rows, lrow(i, "2000", 900, "I", "Tag", fmt.Sprintf("hit %d", i)))
	}
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(rows...)})
	budget := logsFilterBudget
	logsFilterBudget = 0
	t.Cleanup(func() { logsFilterBudget = budget })

	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","match":["hit"]}`))
	if !r.Truncated {
		t.Error("truncated = false; want true: the budget was spent")
	}
	if got := lineTexts(r); !slices.Equal(got, []string{"hit 4"}) {
		t.Errorf("lines = %q; want the newest line alone, judged before the budget stopped the pass", got)
	}
	newest := time.Unix(1791337004, 123456*1000).UTC()
	if !slices.ContainsFunc(r.Warnings, func(w string) bool {
		return strings.Contains(w, "budget") && strings.Contains(w, rfcMicro(newest)) && strings.Contains(w, "older lines were not filtered")
	}) {
		t.Errorf("warnings = %q; want one naming the budget and the device time %s", r.Warnings, rfcMicro(newest))
	}

	// No pattern: the pass is not timed, whatever the budget.
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","tags":["Tag"]}`))
	if r.Truncated || r.Counts.Matched != 5 || len(r.Warnings) != 0 {
		t.Errorf("without a pattern: truncated %v, matched %d, warnings %q; want the budget untouched", r.Truncated, r.Counts.Matched, r.Warnings)
	}
}

// Decision (ADR-0135 §2, plan m3i step 3): the pass has a direction, and a spent budget
// loses the lines it had not reached. keep: oldest (step 4) judges forward and loses the
// newest, with the warning saying so.
func TestRunPassDirection(t *testing.T) {
	var lines []logLine
	for i := 0; i < 4; i++ {
		lines = append(lines, logLine{at: time.Unix(int64(100+i), 0).UTC(), tag: "T", message: "hit", priority: "info"})
	}
	c, err := LineFilter{Match: []string{"hit"}}.compile()
	if err != nil {
		t.Fatal(err)
	}
	crash := make([]bool, len(lines))
	all := func(int) bool { return true }

	back, err := runPass(t.Context(), lines, crash, all, c, judgeNewestFirst, 0)
	if err != nil || !back.stopped || len(back.lines) != 1 || !back.lines[0].at.Equal(lines[3].at) || !back.edge.Equal(lines[3].at) {
		t.Errorf("newest first: %+v (%v); want the newest line judged and kept, edge at it", back, err)
	}
	fwd, err := runPass(t.Context(), lines, crash, all, c, judgeOldestFirst, 0)
	if err != nil || !fwd.stopped || len(fwd.lines) != 1 || !fwd.lines[0].at.Equal(lines[0].at) {
		t.Errorf("oldest first: %+v (%v); want the oldest line judged and kept", fwd, err)
	}
	if w := budgetWarning(time.Second, lines[0].at, judgeOldestFirst); !strings.Contains(w, "newer lines were not filtered") {
		t.Errorf("warning for a forward pass: %q; want it to say the newer lines were not filtered", w)
	}

	// With time to spare, both directions keep everything, in log order.
	for _, order := range []judgeOrder{judgeNewestFirst, judgeOldestFirst} {
		res, err := runPass(t.Context(), lines, crash, all, c, order, time.Hour)
		if err != nil || res.stopped || len(res.lines) != 4 || !res.lines[0].at.Equal(lines[0].at) || !res.lines[3].at.Equal(lines[3].at) {
			t.Errorf("order %d with time: %+v (%v); want 4 lines in log order", order, res, err)
		}
	}
}

// Decision (ADR-0135 §1, plan m1i R4): a malformed filter is REQUEST_INVALID_PARAMS before
// any adb command, naming the field and index; a good one of every kind is accepted.
func TestLogsBadPatternRunsNothing(t *testing.T) {
	f := logsFixture(t, nil)
	bad := []struct{ params, names string }{
		{`"match":["("]`, "match[0]"},
		{`"match":["fine","(?P<x"]`, "match[1]"},
		{`"exclude":["["]`, "exclude[0]"},
		{`"match":[""]`, "match[0]"},
		{`"excludeTags":[` + strings.Repeat(`"a",`, 20) + `"a"]`, "excludeTags lists 21"},
		{`"tagPriorities":["Tag"]`, "tagPriorities[0]"},
		{`"tagPriorities":["Tag:Z"]`, "tagPriorities[0]"},
		{`"tagPriorities":[":E"]`, "tagPriorities[0]"},
		{`"tagPriorities":["*:S"]`, "tagPriorities[0]"},
		{`"tagPriorities":["A:E","A:W"]`, "tagPriorities[1]"},
		{`"processes":["has space"]`, "processes[0]"},
		{`"processes":["fine","semi;colon"]`, "processes[1]"},
		{`"processes":["` + strings.Repeat("a", 129) + `"]`, "processes[0]"},
		{`"processes":["a","b","c","d","e","f","g","h","i","j","k"]`, "processes lists 11"},
	}
	for _, c := range bad {
		out := f.logsCall(t, `{"device":"emulator-5554",`+c.params+`}`)
		e := mustFail(t, out, capability.ErrInvalidParams)
		if !strings.Contains(e.Message, c.names) {
			t.Errorf("%s: message %q; want it to name %q", c.params, e.Message, c.names)
		}
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("a malformed filter ran adb: %q", calls)
	}
	mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","tagPriorities":["Odd:Tag:E"],"processes":["com.google.android.webview:webview_apk","a@b/c+d-e_f.g"]}`))

	// On a remote id the client checks first: the id is admitted, the link is down (a call
	// that reached it would be DEVICE_NOT_READY), and a bad pattern still answers
	// REQUEST_INVALID_PARAMS, so it cost no host call. A good filter does reach the link.
	h := newHostFixture(t, uiList(phoneLine), nil, lendingConfig)
	h.adb.remotes = oneRemote{&hosting.Link{}}
	h.adb.policy = admitting{"c/X1"}
	e := mustFail(t, h.call(t, "", "device.logs", `{"device":"c/X1","match":["("]}`), capability.ErrInvalidParams)
	if !strings.Contains(e.Message, "match[0]") {
		t.Errorf("remote id: message %q; want it to name match[0]", e.Message)
	}
	if e := mustFail(t, h.call(t, "", "device.logs", `{"device":"c/X1","match":["x"]}`), ErrNotReady); e.Details["state"] != "unreachable" {
		t.Errorf("remote id with a good filter: %+v; want it relayed (the link is down)", e)
	}
}
