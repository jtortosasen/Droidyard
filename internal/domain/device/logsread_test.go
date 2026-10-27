package device

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The read shape of device.logs (plan m3i step 4; ADR-0135 §5): the window's end, which end
// survives a cap, context, cut messages, folded runs and the noise report, whole path from
// the fake adb to the published result.

const logsReadBase = 1791337000 // the epoch second lrow(0, …) is stamped with

// crashBlock is a Java crash block of app: four lines of one pid and tag in a row, as the
// crash classifier reads one (no applicationId is needed to recognise it).
func crashBlock(sec, pid int, app string) []string {
	return []string{
		lrow(sec, "10146", pid, "E", "AndroidRuntime", "FATAL EXCEPTION: main"),
		lrow(sec, "10146", pid, "E", "AndroidRuntime", "Process: "+app+", PID: "+fmt.Sprint(pid)),
		lrow(sec, "10146", pid, "E", "AndroidRuntime", "java.lang.IllegalStateException: boom"),
		lrow(sec, "10146", pid, "E", "AndroidRuntime", "\tat dev.app.Main.onCreate(Main.kt:3)"),
	}
}

// flaggedMessages are the messages of the returned lines that carry the crash flag.
func flaggedMessages(r LogsResult) []string {
	var out []string
	for _, l := range r.Lines {
		if l.Crash {
			out = append(out, l.Message)
		}
	}
	return out
}

// Decision (ADR-0135 §5, the agent's start buried by the newest-kept rule): keep picks the
// end that survives maxLines. Ten target lines, then 3,000 noise lines, all after since:
// oldest returns the ten, newest the last ten. The crash flag is the line's own in both:
// the oldest ten hold one crash block and the newest ten hold another, and each block's
// four lines, and only those, are flagged.
func TestLogsKeepOldestAfterSince(t *testing.T) {
	var rows []string
	rows = append(rows, lrow(0, "2000", 900, "I", "Old", "before since, never returned"))
	sec := 1
	for i := 0; i < 3; i++ {
		rows = append(rows, lrow(sec, "2000", 900, "I", "Target", fmt.Sprintf("target %d", i)))
		sec++
	}
	rows = append(rows, crashBlock(sec, 901, "dev.app.first")...)
	sec++
	for i := 3; i < 6; i++ {
		rows = append(rows, lrow(sec, "2000", 900, "I", "Target", fmt.Sprintf("target %d", i)))
		sec++
	}
	for i := 0; i < 3000; i++ {
		rows = append(rows, lrow(sec, "2000", 910, "D", "Noise", fmt.Sprintf("noise %d", i)))
		sec++
	}
	rows = append(rows, crashBlock(sec, 902, "dev.app.last")...)
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(rows...)})
	since := time.Unix(logsReadBase+1, 0).UTC().Format(time.RFC3339Nano)
	logs := func(extra string) LogsResult {
		t.Helper()
		return mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","since":"`+since+`","maxLines":10`+extra+`}`))
	}

	oldest := logs(`,"keep":"oldest"`)
	if got := lineTexts(oldest); len(got) != 10 || got[0] != "target 0" || got[2] != "target 2" || got[3] != "FATAL EXCEPTION: main" || got[9] != "target 5" {
		t.Errorf("keep oldest: %q; want the first ten lines after since: targets around the first crash block", got)
	}
	if got := flaggedMessages(oldest); len(got) != 4 || got[0] != "FATAL EXCEPTION: main" || !strings.HasPrefix(got[3], "\tat dev.app.Main") {
		t.Errorf("keep oldest: flagged %q; want the four lines of the first crash block", got)
	}
	if !oldest.Truncated || oldest.Counts.Matched != 3014 || strings.Contains(strings.Join(lineTexts(oldest), "\n"), "before since") {
		t.Errorf("keep oldest: truncated %v, matched %d; want truncated and 3014 matched after since", oldest.Truncated, oldest.Counts.Matched)
	}

	for _, newest := range []LogsResult{logs(``), logs(`,"keep":"newest"`)} {
		got := lineTexts(newest)
		if len(got) != 10 || got[0] != "noise 2994" || got[6] != "FATAL EXCEPTION: main" {
			t.Errorf("keep newest: %q; want the last ten lines, ending in the last crash block", got)
		}
		if fl := flaggedMessages(newest); len(fl) != 4 || fl[0] != "FATAL EXCEPTION: main" || !strings.HasPrefix(fl[3], "\tat dev.app.Main") {
			t.Errorf("keep newest: flagged %q; want the four lines of the last crash block, none of the noise before it", fl)
		}
	}

	// The capture is the matched set, cut at its size on the end keep names; its warning
	// says which end it kept.
	big := strings.Repeat("y", 900)
	var long []string
	for i := 0; i < 6000; i++ { // 6000 lines of ~1 KB pass the 4 MiB capture bound
		long = append(long, lrow(i, "2000", 910, "D", "Noise", fmt.Sprintf("line-%06d %s", i, big)))
	}
	f = logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(long...)})
	for _, c := range []struct{ keep, has, lacks, says string }{
		{"oldest", "line-000000", "line-005999", "kept the oldest lines"},
		{"newest", "line-005999", "line-000000", "kept the newest lines"},
	} {
		r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","maxLines":5,"keep":"`+c.keep+`"}`))
		e, err := f.store.Get(r.Capture.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(e.Data), c.has) || strings.Contains(string(e.Data), c.lacks) || len(e.Data) > logsCaptureCap {
			t.Errorf("keep %s: the capture (%d bytes) holds %s: %v, %s: %v; want the %s end only", c.keep, len(e.Data), c.has, strings.Contains(string(e.Data), c.has), c.lacks, strings.Contains(string(e.Data), c.lacks), c.keep)
		}
		if !slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, c.says) }) {
			t.Errorf("keep %s: warnings %q; want one saying %q", c.keep, r.Warnings, c.says)
		}
	}
}

// Decision (ADR-0135 §2, §5; the step 2-3 review): keep: oldest judges the lines forward, so a
// spent filter budget loses the newest lines, and the warning says it.
func TestLogsKeepOldestJudgesForward(t *testing.T) {
	var rows []string
	for i := 0; i < 5; i++ {
		rows = append(rows, lrow(i, "2000", 900, "I", "Tag", fmt.Sprintf("hit %d", i)))
	}
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(rows...)})
	budget := logsFilterBudget
	logsFilterBudget = 0
	t.Cleanup(func() { logsFilterBudget = budget })

	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","match":["hit"],"keep":"oldest"}`))
	if got := lineTexts(r); !slices.Equal(got, []string{"hit 0"}) || !r.Truncated {
		t.Errorf("lines %q truncated %v; want the oldest line alone, judged before the budget stopped the pass", got, r.Truncated)
	}
	if !slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, "newer lines were not filtered") }) {
		t.Errorf("warnings %q; want one saying the newer lines were not filtered", r.Warnings)
	}
}

// Decision (ADR-0135 §5): until closes the window on the device clock; a line after it is
// not read into the result and window.to is at or before it. An until that is not after
// since is REQUEST_INVALID_PARAMS before any adb command, whether since is explicit or the
// launch it resolves to; a follow takes it too.
func TestLogsUntil(t *testing.T) {
	var rows []string
	for i := 0; i < 10; i++ {
		rows = append(rows, lrow(i, "2000", 900, "I", "Tag", fmt.Sprintf("line %d", i)))
	}
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(rows...)})
	stamp := func(sec int, ns int) string {
		return time.Unix(logsReadBase+int64(sec), int64(ns)).UTC().Format(time.RFC3339Nano)
	}

	until := stamp(4, 500_000_000)
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","until":"`+until+`","maxLines":100}`))
	if got := lineTexts(r); len(got) != 5 || got[0] != "line 0" || got[4] != "line 4" {
		t.Errorf("until: %q; want lines 0 to 4", got)
	}
	if r.Window.To > rfcMicro(mustTime(t, until)) || r.Counts.Matched != 5 {
		t.Errorf("until: window.to %s matched %d; want to at or before %s and 5 matched", r.Window.To, r.Counts.Matched, until)
	}
	// A line exactly at until stays.
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","until":"`+time.Unix(logsReadBase+4, 123456*1000).UTC().Format(time.RFC3339Nano)+`","maxLines":100}`))
	if got := lineTexts(r); len(got) != 5 {
		t.Errorf("until at a line's own time: %q; want that line kept", got)
	}
	// The window between since and until.
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","since":"`+stamp(2, 0)+`","until":"`+until+`","maxLines":100}`))
	if got := lineTexts(r); !slices.Equal(got, []string{"line 2", "line 3", "line 4"}) {
		t.Errorf("since and until: %q; want lines 2 to 4", got)
	}

	before := len(f.calls(t))
	for _, params := range []string{
		`{"device":"emulator-5554","since":"` + stamp(5, 0) + `","until":"` + stamp(5, 0) + `"}`,
		`{"device":"emulator-5554","since":"` + stamp(5, 0) + `","until":"` + stamp(1, 0) + `"}`,
		`{"device":"emulator-5554","until":"yesterday"}`,
	} {
		mustFail(t, f.logsCall(t, params), capability.ErrInvalidParams)
	}
	if after := len(f.calls(t)); after != before {
		t.Errorf("the device was touched for an until that is not after since: %v", f.calls(t)[before:])
	}
}

// Decision (ADR-0135 §5): the launch since is resolved on the host, so an until before it is
// refused there, before the logcat read.
func TestLogsUntilBeforeTheLaunchIsInvalid(t *testing.T) {
	f := launchedLogsFixture(t, nil)
	out := f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`","until":"2020-01-01T00:00:00Z"}`)
	mustFail(t, out, capability.ErrInvalidParams)
	for _, c := range f.calls(t) {
		if strings.Contains(c, "logcat") {
			t.Errorf("logcat ran for an until before the launch: %q", c)
		}
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// Decision (ADR-0135 §5): context is grep -C over the call's own lines. Two matches three
// lines apart with context 2 are one group of seven lines; the context lines are flagged and
// are not in counts.matched; a crash line used as context keeps its crash flag; and a line
// of another process, out of scope, is never context.
func TestLogsContext(t *testing.T) {
	rows := []string{
		lrow(0, "2000", 900, "I", "App", "before 0"),
		lrow(1, "2000", 900, "I", "App", "before 1"),
		lrow(2, "2000", 900, "I", "App", "HIT one"),
		lrow(3, "3000", 999, "I", "Other", "another process between"),
		lrow(4, "2000", 900, "I", "App", "between 4"),
		lrow(5, "2000", 900, "I", "App", "between 5"),
		lrow(6, "2000", 900, "I", "App", "HIT two"),
		lrow(7, "2000", 900, "I", "App", "after 7"),
		lrow(8, "2000", 900, "I", "App", "after 8"),
		lrow(9, "2000", 900, "I", "App", "far 9"),
	}
	rows = append(rows, crashBlock(10, 900, "dev.app.x")...) // the pid of the process in scope
	rows = append(rows, lrow(14, "2000", 900, "I", "App", "HIT three"))
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(rows...), uiSerial + ".ps.out": "PID NAME\n900 dev.app.x\n"})
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","processes":["dev.app.x"],"match":["HIT"],"context":2,"maxLines":100}`))

	var got []string
	for _, l := range r.Lines {
		mark := ""
		if l.Context {
			mark = "~"
		}
		got = append(got, mark+l.Message)
	}
	// grep -C 2: the first two matches share one group; "far 9" is two lines past "HIT two" and
	// stays out; "HIT three" brings the last two lines of the crash block before it.
	want := []string{"~before 0", "~before 1", "HIT one", "~between 4", "~between 5", "HIT two", "~after 7", "~after 8",
		"~java.lang.IllegalStateException: boom", "~\tat dev.app.Main.onCreate(Main.kt:3)", "HIT three"}
	if !slices.Equal(got, want) {
		t.Errorf("context 2:\n got %q\nwant %q", got, want)
	}
	if r.Counts.Matched != 3 || r.Counts.Returned != len(want) {
		t.Errorf("counts matched %d returned %d; want 3 matched (context not counted) and %d returned", r.Counts.Matched, r.Counts.Returned, len(want))
	}
	for _, l := range r.Lines {
		if strings.Contains(l.Message, "another process") {
			t.Error("a line of a process out of scope came back as context")
		}
		isCrashLine := strings.HasPrefix(l.Message, "java.lang.IllegalState") || strings.HasPrefix(l.Message, "\tat dev.app.Main")
		if l.Crash != isCrashLine {
			t.Errorf("line %q: crash flag %v, want %v", l.Message, l.Crash, isCrashLine)
		}
	}
	// The two matches 4 lines apart are one group: no gap between "HIT one" and "HIT two".
	// maxLines counts the context lines: 5 lines keep the newest five, whole groups first.
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","processes":["dev.app.x"],"match":["HIT"],"context":2,"maxLines":5}`))
	if len(r.Lines) != 5 || !r.Truncated || r.Lines[4].Message != "HIT three" {
		t.Errorf("maxLines 5 with context: %q truncated %v; want the newest five lines counting context, and truncated", lineTexts(r), r.Truncated)
	}
	if r.Counts.Matched != 3 {
		t.Errorf("counts.matched %d with a cap; want 3", r.Counts.Matched)
	}
}

// Decision (ADR-0135 §5): maxMessageBytes cuts an inline message at a rune boundary and says
// so per line (cut); the pattern still runs on the whole message and the capture keeps the
// whole line.
func TestLogsMaxMessageBytes(t *testing.T) {
	// 169 ASCII bytes, then a 3-byte rune across byte 170, then the marker the pattern wants.
	msg := strings.Repeat("a", 169) + "€" + strings.Repeat("b", 50) + "ENDMARK"
	rows := []string{lrow(0, "2000", 900, "I", "Tag", msg), lrow(1, "2000", 900, "I", "Tag", "short")}
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(rows...)})

	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","maxMessageBytes":170,"match":["ENDMARK$"]}`))
	if len(r.Lines) != 1 {
		t.Fatalf("lines %q; want the pattern to match the whole message, past the cut", lineTexts(r))
	}
	l := r.Lines[0]
	if !l.Cut || len(l.Message) > 170 || !utf8.ValidString(l.Message) || l.Message != strings.Repeat("a", 169) {
		t.Errorf("cut %v message %d bytes valid %v; want at most 170 bytes cut on a rune boundary (169 bytes) and cut set", l.Cut, len(l.Message), utf8.ValidString(l.Message))
	}
	e, err := f.store.Get(r.Capture.Ref)
	if err != nil || !strings.Contains(string(e.Data), "ENDMARK") {
		t.Errorf("the capture must hold the whole line (%v): %q", err, e.Data)
	}
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","maxMessageBytes":170}`))
	if r.Lines[1].Cut || r.Lines[1].Message != "short" {
		t.Errorf("a short line is cut: %+v", r.Lines[1])
	}
	// Without the parameter nothing is flagged.
	if r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554"}`)); r.Lines[0].Cut || r.Lines[0].Message != msg {
		t.Errorf("without maxMessageBytes the message is cut: cut %v len %d", r.Lines[0].Cut, len(r.Lines[0].Message))
	}
}

// Decision (ADR-0135 §5): collapse folds consecutive matched lines equal in pid, priority,
// tag and message into the first, which says how many it stands for, and maxLines counts the
// fold once; counts.matched is still every matched line.
func TestLogsCollapse(t *testing.T) {
	var rows []string
	rows = append(rows, lrow(0, "2000", 900, "I", "Tag", "start"))
	for i := 0; i < 500; i++ {
		rows = append(rows, lrow(1, "2000", 900, "W", "Spam", "same again"))
	}
	rows = append(rows, lrow(2, "2000", 901, "W", "Spam", "same again")) // another pid: not folded
	rows = append(rows, lrow(3, "2000", 900, "I", "Tag", "end"))
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(rows...)})

	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","collapse":true,"maxLines":3}`))
	if got := lineTexts(r); !slices.Equal(got, []string{"same again", "same again", "end"}) {
		t.Fatalf("lines %q; want the fold, the other pid's line and the last", got)
	}
	if r.Lines[0].Repeats != 500 || r.Lines[1].Repeats != 0 || r.Lines[2].Repeats != 0 {
		t.Errorf("repeats %d, %d, %d; want 500, 0, 0", r.Lines[0].Repeats, r.Lines[1].Repeats, r.Lines[2].Repeats)
	}
	if r.Counts.Matched != 503 || r.Counts.Returned != 3 || !r.Truncated {
		t.Errorf("matched %d returned %d truncated %v; want 503 matched, the fold counted once (3 returned) and truncated by the 4th item", r.Counts.Matched, r.Counts.Returned, r.Truncated)
	}
	// Without collapse every line stands for itself.
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","maxLines":3}`))
	if r.Lines[0].Repeats != 0 {
		t.Errorf("repeats %d without collapse; want 0", r.Lines[0].Repeats)
	}
	// A line the filter rejects between two equal ones does not break the run: the fold is
	// over the matched sequence.
	between := []string{
		lrow(0, "2000", 900, "W", "Spam", "same"), lrow(1, "2000", 900, "I", "Quiet", "not matched"), lrow(2, "2000", 900, "W", "Spam", "same"),
	}
	f = logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(between...)})
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","collapse":true,"tags":["Spam"]}`))
	if len(r.Lines) != 1 || r.Lines[0].Repeats != 2 {
		t.Errorf("lines %+v; want one line standing for 2", r.Lines)
	}
}

// Decision (ADR-0135 §5): a truncated result names its noise. SurfaceFlinger's thousands of
// lines lead topTags by lines scanned; five tags by default, topTags: 2 gives two, and an
// untruncated result carries none unless asked.
func TestLogsTopTagsNameTheNoise(t *testing.T) {
	var rows []string
	sec := 0
	add := func(tag string, n int, msg string) {
		for i := 0; i < n; i++ {
			rows = append(rows, lrow(sec, "1000", 700, "D", tag, msg))
			sec++
		}
	}
	add("SurfaceFlinger", 300, "frame")
	add("Choreographer", 40, "skipped")
	add("Wanted", 5, "the needle")
	add("A", 20, "x")
	add("B", 20, "x")
	add("C", 10, "x")
	add("D", 3, "x")
	f := logsFixture(t, map[string]string{uiSerial + ".log": logsPlain(rows...)})

	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","maxLines":10}`))
	if !r.Truncated || len(r.TopTags) != 5 || r.TopTags[0].Tag != "SurfaceFlinger" || r.TopTags[0].Scanned != 300 || r.TopTags[0].Matched != 300 {
		t.Fatalf("truncated %v topTags %+v; want five, SurfaceFlinger first with 300 scanned", r.Truncated, r.TopTags)
	}
	if r.TopTags[1].Tag != "Choreographer" || r.TopTags[2].Tag != "A" || r.TopTags[3].Tag != "B" || r.TopTags[4].Tag != "C" {
		t.Errorf("topTags order %+v; want by lines scanned, ties by name", r.TopTags)
	}
	// Under a filter, scanned is the tag's lines in scope and matched what the filter kept.
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","match":["needle"],"topTags":2}`))
	if len(r.TopTags) != 2 || r.TopTags[0] != (LogTag{Tag: "SurfaceFlinger", Scanned: 300, Matched: 0}) || r.TopTags[1].Tag != "Choreographer" {
		t.Errorf("topTags 2 under a filter: %+v; want two, SurfaceFlinger scanned 300 matched 0", r.TopTags)
	}
	// Not truncated and not asked for: none. Asked for: present.
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","match":["needle"]}`))
	if r.Truncated || r.TopTags != nil {
		t.Errorf("an untruncated result carries topTags %+v (truncated %v)", r.TopTags, r.Truncated)
	}
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","match":["needle"],"topTags":50}`))
	if len(r.TopTags) != 7 {
		t.Errorf("topTags 50 gave %d tags; want all 7", len(r.TopTags))
	}
	for _, bad := range []string{`"topTags":51`, `"topTags":-1`, `"context":11`, `"context":-1`, `"maxMessageBytes":15`, `"maxMessageBytes":16385`, `"keep":"middle"`} {
		mustFail(t, f.logsCall(t, `{"device":"emulator-5554",`+bad+`}`), capability.ErrInvalidParams)
	}
}

// Decision (ADR-0135 §5, plan m3i step 1 (a)): events is a buffer a call may name, off by
// default. `-b events` reaches logcat only when asked for, and its lines (recorded on API
// 36, a padded tag among them) parse like the main buffer's.
func TestLogsEventsBuffer(t *testing.T) {
	events, err := os.ReadFile("testdata/events36-sample.txt")
	if err != nil {
		t.Fatal(err)
	}
	log := logsPlain(lrow(0, "2000", 900, "I", "App", "a main line")) +
		"--------- beginning of events\n" + string(events)
	f := logsFixture(t, map[string]string{uiSerial + ".log": log})

	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","buffers":["events"],"maxLines":100}`))
	var argv string
	for _, c := range f.calls(t) {
		if strings.Contains(c, "logcat") && strings.Contains(c, "-b ") {
			argv = c
		}
	}
	if !strings.Contains(argv, "-b events") || strings.Contains(argv, "main") {
		t.Errorf("logcat argv %q; want -b events alone", argv)
	}
	if r.Counts.Scanned != 20 || r.Counts.Matched != 20 || len(r.Lines) != 20 {
		t.Fatalf("scanned %d matched %d lines %d; want the 20 recorded lines", r.Counts.Scanned, r.Counts.Matched, len(r.Lines))
	}
	if l := r.Lines[0]; l.Tag != "am_uid_active" || l.Message != "10105" || l.PID != 661 || l.TID != 1831 || l.Priority != "info" {
		t.Errorf("first events line %+v; want am_uid_active 10105 from pid 661 tid 1831", l)
	}
	if l := r.Lines[16]; l.Tag != "am_wtf" || !strings.HasPrefix(l.Message, "[0,1305,com.android.settings") {
		t.Errorf("the padded-tag line %+v; want tag am_wtf with its bracketed payload as the message", l)
	}

	// Off by default, and combinable with the default set.
	f = logsFixture(t, map[string]string{uiSerial + ".log": log})
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554"}`))
	if r.Counts.Scanned != 1 {
		t.Errorf("a default read scanned %d lines; want the main line only", r.Counts.Scanned)
	}
	r = mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","buffers":["events","main"]}`))
	if r.Counts.Scanned != 21 {
		t.Errorf("main and events scanned %d lines; want 21", r.Counts.Scanned)
	}
	var last string
	for _, c := range f.calls(t) {
		if strings.Contains(c, "logcat") && strings.Contains(c, "-b ") {
			last = c
		}
	}
	if !strings.Contains(last, "-b main,events") {
		t.Errorf("logcat argv %q; want -b main,events", last)
	}
}

// Decision (ADR-0135 §6; the step 2-3 review): a relayed result is bounded as a local one:
// topTags to the 50 the request allows with each tag at 256 bytes and no negative count, a
// negative repeats is nothing, and a null pids list of a process is the empty array the
// published schema requires.
func TestRemoteLogsBoundsTheNewFields(t *testing.T) {
	long := strings.Repeat("t", 1000)
	tags := make([]LogTag, logsTopTagsMax+9)
	for i := range tags {
		tags[i] = LogTag{Tag: long, Scanned: -4, Matched: -1}
	}
	host := LogsResult{
		Lines:   []LogLine{{Repeats: -3, Message: "m"}, {Repeats: 7, Context: true, Cut: true}},
		TopTags: tags,
		Filter:  LogFilter{By: "none", Processes: []LogProcess{{Name: "p", PIDs: nil}}},
	}
	out, err := (&ADB{}).logsFrom(context.Background(), &fakeHostArtifacts{}, qaPhone, testStore(t), host)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(LogsResult)
	if len(got.TopTags) != logsTopTagsMax || len(got.TopTags[0].Tag) != logsTagBytesMax || got.TopTags[0].Scanned != 0 || got.TopTags[0].Matched != 0 {
		t.Errorf("topTags: %d entries, first %d-byte tag, counts %d/%d; want 50, 256 bytes, no negatives", len(got.TopTags), len(got.TopTags[0].Tag), got.TopTags[0].Scanned, got.TopTags[0].Matched)
	}
	if got.Lines[0].Repeats != 0 || got.Lines[1].Repeats != 7 || !got.Lines[1].Context || !got.Lines[1].Cut {
		t.Errorf("lines %+v; want a negative repeats dropped and the rest kept", got.Lines)
	}
	if got.Filter.Processes[0].PIDs == nil {
		t.Error("a relayed null pids is still null; want the empty array")
	}
}

// Decision (ADR-0036 Decision 2, ADR-0135 §5): the rendering marks what the read did to a
// line: ctx for context, x500 for a fold, cut for a cut message; a noise: line for topTags
// and a processes line with each name's pid count. Device text stays quoted.
func TestLogsTextShowsTheReadShape(t *testing.T) {
	r := LogsResult{
		Device: "emulator-5554",
		Window: LogWindow{From: "2026-10-07T14:25:06.000000Z", To: "2026-10-07T14:25:08.000000Z"},
		Filter: LogFilter{By: "none", Processes: []LogProcess{{Name: "system_server", PIDs: []int{612}}, {Name: "gone", PIDs: []int{}}}},
		Counts: LogCounts{Scanned: 900, Matched: 504, Returned: 3},
		Lines: []LogLine{
			{Time: "2026-10-07T14:25:06.100000Z", PID: 7, Priority: "info", Tag: "A", Message: "around", Context: true},
			{Time: "2026-10-07T14:25:07.100000Z", PID: 7, Priority: "warn", Tag: "Spam", Message: "same again", Repeats: 500},
			{Time: "2026-10-07T14:25:08.100000Z", PID: 7, Priority: "warn", Tag: "Long", Message: "abcdef", Cut: true},
		},
		TopTags:   []LogTag{{Tag: "SurfaceFlinger", Scanned: 800, Matched: 0}, {Tag: "Tag\nforged: line", Scanned: 4, Matched: 4}},
		Truncated: true,
	}
	got := logsText(r)
	want := `logs emulator-5554 by=none window 2026-10-07T14:25:06.000Z 2026-10-07T14:25:08.000Z scanned=900 matched=504 returned=3 truncated
processes system_server=1 gone=0
ctx 14:25:06.100 I/"A" p7 "around"
14:25:07.100 W/"Spam" "same again" x500
14:25:08.100 W/"Long" "abcdef" cut
noise: "SurfaceFlinger" 800/0 "Tag\nforged: line" 4/4`
	if got != want {
		t.Errorf("rendering:\n%s\nwant:\n%s", got, want)
	}
}
