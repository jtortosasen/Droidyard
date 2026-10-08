package device

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The renderings of text.go held against recorded results (ADR-0036 Decision 2). The
// fixtures under testdata/m1o-step5 are real results of the m1m golden-path runs
// (RECORDED.txt there); the three *-uitree.golden.json are real device trees. The
// quoting rule over every tagged string is held in internal/schema
// (TestEveryTaggedStringIsQuoted), where every descriptor is at hand.

const m1o5 = "testdata/m1o-step5/"

func readRecorded(t *testing.T, path string, into any) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return raw
}

// compactJSON is the text a result had before it had a rendering: json.Marshal of the
// result, which is what the adapter sent as the text block.
func compactJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// golden compares got with the file at path, or rewrites it with -update.
func golden(t *testing.T, path, got string) {
	t.Helper()
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got+"\n" != string(want) {
		t.Errorf("rendering differs from %s:\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// TestRenderingsAreTheGoldenText: each recorded result renders to the committed text.
// The golden files are the format's specification by example (text.go's doc comment
// states it in words); a change to a format shows up as a diff here, to be read.
func TestRenderingsAreTheGoldenText(t *testing.T) {
	cases := []struct {
		fixture string
		render  func(raw []byte) string
	}{
		{m1o5 + "dy1-246-uitree", func(raw []byte) string { var v Snapshot; mustJSON(t, raw, &v); return uiTreeText(v) }},
		{m1o5 + "dy3-249-uitree", func(raw []byte) string { var v Snapshot; mustJSON(t, raw, &v); return uiTreeText(v) }},
		{m1o5 + "dy5-248-uitree", func(raw []byte) string { var v Snapshot; mustJSON(t, raw, &v); return uiTreeText(v) }},
		{"testdata/pixel9a-uitree.golden", func(raw []byte) string { var v Snapshot; mustJSON(t, raw, &v); return uiTreeText(v) }},
		{"testdata/pixeltablet-uitree.golden", func(raw []byte) string { var v Snapshot; mustJSON(t, raw, &v); return uiTreeText(v) }},
		{m1o5 + "dy1-247-tap-tree", func(raw []byte) string { var v TapResult; mustJSON(t, raw, &v); return tapText(v) }},
		{m1o5 + "dy4-250-tap-unmet", func(raw []byte) string { var v TapResult; mustJSON(t, raw, &v); return tapText(v) }},
		{m1o5 + "dy4-264-tap-unmet-left-app", func(raw []byte) string { var v TapResult; mustJSON(t, raw, &v); return tapText(v) }},
		{m1o5 + "dy5-261-tap-unmet-tree", func(raw []byte) string { var v TapResult; mustJSON(t, raw, &v); return tapText(v) }},
		{m1o5 + "dy1-248-type-warning", func(raw []byte) string { var v InputTextResult; mustJSON(t, raw, &v); return typedText(v) }},
		{m1o5 + "dy1-253-logs-crash", func(raw []byte) string { var v LogsResult; mustJSON(t, raw, &v); return logsText(v) }},
		{m1o5 + "dy1-244-logs", func(raw []byte) string { var v LogsResult; mustJSON(t, raw, &v); return logsText(v) }},
		{m1o5 + "dy4-265-logs-crash", func(raw []byte) string { var v LogsResult; mustJSON(t, raw, &v); return logsText(v) }},
	}
	for _, c := range cases {
		raw, err := os.ReadFile(c.fixture + ".json")
		if err != nil {
			t.Fatal(err)
		}
		golden(t, c.fixture+".txt", c.render(raw))
	}
}

func mustJSON(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

// TestRenderingIsAtLeastHalfSmaller holds the plan's A5 number: on the m1m recordings,
// the rendered tree text and the rendered log text are at least 50 % smaller than the
// compact JSON they replace. The trees are held one by one; the logs by their sum, because
// a log read of two 850-byte lines (the emulator's HostComposition line) cannot shrink by
// half whatever is done to it, and its own floor is stated.
func TestRenderingIsAtLeastHalfSmaller(t *testing.T) {
	saved := func(before, after int) float64 { return 100 * float64(before-after) / float64(before) }

	for _, f := range []string{"dy1-246-uitree", "dy3-249-uitree", "dy5-248-uitree"} {
		var v Snapshot
		readRecorded(t, m1o5+f+".json", &v)
		before, after := len(compactJSON(t, v)), len(uiTreeText(v))
		t.Logf("%s: %d -> %d bytes, %.0f%% smaller", f, before, after, saved(before, after))
		if saved(before, after) < 50 {
			t.Errorf("%s: the rendering is %d bytes against %d of JSON: only %.0f%% smaller, want at least 50%%", f, after, before, saved(before, after))
		}
	}

	floors := map[string]float64{"dy1-253-logs-crash": 50, "dy4-265-logs-crash": 50, "dy1-244-logs": 35}
	var sumBefore, sumAfter int
	for f, floor := range floors {
		var v LogsResult
		readRecorded(t, m1o5+f+".json", &v)
		before, after := len(compactJSON(t, v)), len(logsText(v))
		sumBefore, sumAfter = sumBefore+before, sumAfter+after
		t.Logf("%s: %d -> %d bytes, %.0f%% smaller", f, before, after, saved(before, after))
		if saved(before, after) < floor {
			t.Errorf("%s: only %.0f%% smaller, want at least %.0f%%", f, saved(before, after), floor)
		}
	}
	if saved(sumBefore, sumAfter) < 50 {
		t.Errorf("the log fixtures together are only %.0f%% smaller, want at least 50%%", saved(sumBefore, sumAfter))
	}
}

// TestCrashLinesAreFoldedIntoTheirEntry: a crash block is its crashes[] entry in the
// rendering, not also its lines (ADR-0036 Decision 2): once the read has an entry, every
// line flagged crash is gone, a native tombstone's crash_dump lines (another pid) included.
// A log with no crash entry keeps every line, since they are all it has of the crash.
func TestCrashLinesAreFoldedIntoTheirEntry(t *testing.T) {
	line := func(pid int, msg string, crash bool) LogLine {
		return LogLine{Time: "2026-10-07T14:25:06.443049Z", PID: pid, Priority: "error", Tag: "AndroidRuntime", Message: msg, Crash: crash}
	}
	r := LogsResult{
		Device: "emulator-5554",
		Lines:  []LogLine{line(7, "kept: not flagged", false), line(7, "folded: flagged, pid has an entry", true), line(8, "folded: flagged, another pid (crash_dump)", true)},
		Crashes: []Crash{{PID: 7, Time: "2026-10-07T14:25:06.443049Z", Exception: "java.lang.IllegalStateException", Message: "boom",
			Causes: []string{"java.io.IOException: disk"}, Frames: []string{"dev.app.Main.onCreate(Main.kt:3)"}}},
	}
	got := logsText(r)
	for _, want := range []string{`"kept: not flagged"`, `crash pid=7 14:25:06.443`, `exception "java.lang.IllegalStateException" message="boom"`, `causedBy "java.io.IOException: disk"`, `at "dev.app.Main.onCreate(Main.kt:3)"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendering lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "folded:") {
		t.Errorf("a line flagged crash is still rendered once the read has a crash entry:\n%s", got)
	}
	r.Crashes = nil
	if got := logsText(r); !strings.Contains(got, "folded:") {
		t.Errorf("without a crash entry the flagged line must stay:\n%s", got)
	}
}

// TestCrashLinesOlderThanTheKeptEntriesStay: a read keeps at most logsCrashesMax crash
// entries, the newest; a flagged line older than the oldest kept entry belongs to a block
// with no entry, so it stays in the rendering rather than vanish.
func TestCrashLinesOlderThanTheKeptEntriesStay(t *testing.T) {
	var crashes []Crash
	for i := 0; i < logsCrashesMax; i++ {
		crashes = append(crashes, Crash{PID: 100 + i, Time: fmt.Sprintf("2026-10-07T14:25:%02d.000000Z", 10+i), Exception: "java.lang.IllegalStateException"})
	}
	r := LogsResult{Device: "emulator-5554", Crashes: crashes, Lines: []LogLine{
		{Time: "2026-10-07T14:25:01.000000Z", PID: 99, Priority: "error", Tag: "AndroidRuntime", Message: "older: its block has no entry", Crash: true},
		{Time: "2026-10-07T14:25:10.000000Z", PID: 100, Priority: "error", Tag: "AndroidRuntime", Message: "folded: the oldest kept entry", Crash: true},
	}}
	got := logsText(r)
	if !strings.Contains(got, "older: its block has no entry") {
		t.Errorf("a flagged line older than every kept entry was folded:\n%s", got)
	}
	if strings.Contains(got, "folded:") {
		t.Errorf("a line of a kept entry's block is still rendered:\n%s", got)
	}
}

// TestConsecutiveLinesOfOneHeaderAreGrouped: lines of the same time to the millisecond,
// priority, tag and pid, one after the other, share one header; a different header, or the
// same one after another, starts a new one. The pid is written when it changes, so two
// processes that log the same tag in the same millisecond stay distinguishable.
func TestConsecutiveLinesOfOneHeaderAreGrouped(t *testing.T) {
	l := func(ts, tag, msg string) LogLine {
		return LogLine{Time: ts, PID: 7, Priority: "debug", Tag: tag, Message: msg}
	}
	r := LogsResult{Lines: []LogLine{
		l("2026-10-07T14:00:00.123111Z", "A", "one"),
		l("2026-10-07T14:00:00.123999Z", "A", "two"), // same millisecond, same header
		l("2026-10-07T14:00:00.124000Z", "A", "three"),
		l("2026-10-07T14:00:00.124000Z", "B", "four"),
		l("2026-10-07T14:00:00.124000Z", "A", "five"), // A again, but not consecutive
	}}
	other := l("2026-10-07T14:00:00.124000Z", "A", "six")
	other.PID = 9 // another process, the same header otherwise: its own header, with its pid
	r.Lines = append(r.Lines, other, l("2026-10-07T14:00:00.124000Z", "A", "seven"))
	got := logsText(r)
	if !strings.HasSuffix(got, "\n14:00:00.124 D/\"A\" p9 \"six\"\n14:00:00.124 D/\"A\" p7 \"seven\"") {
		t.Errorf("a line of another pid shares a header with the one before, or a pid is missing:\n%s", got)
	}
	r.Lines = r.Lines[:5]
	got = logsText(r)
	want := "14:00:00.123 D/\"A\" p7 \"one\"\n \"two\"\n14:00:00.124 D/\"A\" \"three\"\n14:00:00.124 D/\"B\" \"four\"\n14:00:00.124 D/\"A\" \"five\""
	if !strings.HasSuffix(got, want) {
		t.Errorf("the lines render as\n%s\nwant them to end with\n%s", got, want)
	}
}

// TestSystemBarsAndUnnamedWrappersAreNotShown: an empty status or navigation bar view
// and an unnamed wrapper around one child have no line; their children are not indented
// for them; a bar view with a child, text or flag, and a wrapper that has an id, are
// shown. The snapshot itself is not touched, so every ref still resolves.
func TestSystemBarsAndUnnamedWrappersAreNotShown(t *testing.T) {
	s := Snapshot{ID: "s-1", Foreground: "p.q", Display: UIDisplay{Width: 10, Height: 20}, Nodes: []UINode{
		{Ref: "n0", Class: "FrameLayout", ID: "content", Children: []string{"n1", "n3"}},
		{Ref: "n1", Class: "ViewGroup", Children: []string{"n2"}}, // unnamed wrapper of one child
		{Ref: "n2", Class: "Button", Text: "Go", Flags: []string{"clickable"}},
		{Ref: "n3", Class: "View", ID: "statusBarBackground"},                           // empty bar view
		{Ref: "n4", Class: "View", ID: "navigationBarBackground", Text: "has text"},     // not empty
		{Ref: "n5", Class: "View", ID: "wrapper", Children: []string{"n6"}},             // named wrapper
		{Ref: "n6", Class: "View", ID: "statusBarBackground", Children: []string{"n7"}}, // has a child
		{Ref: "n7", Class: "Text", Text: "x"},
	}}
	before := compactJSON(t, s)
	got := uiTreeText(s)
	if after := compactJSON(t, s); after != before {
		t.Errorf("rendering changed the snapshot:\n%s\n%s", before, after)
	}
	lines := strings.Split(got, "\n")[1:]
	want := []string{
		`n0 "FrameLayout" #"content"`,
		` n2 "Button" "Go" clickable`, // n1 is not shown, so n2 sits one level below n0
		`n4 "View" #"navigationBarBackground" "has text"`,
		`n5 "View" #"wrapper"`,
		` n6 "View" #"statusBarBackground"`,
		`  n7 "Text" "x"`,
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d node lines, want %d:\n%s", len(lines), len(want), got)
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("line %d = %q, want it to start with %q", i, lines[i], w)
		}
	}
}

// TestRenderedLineResolves: a selector built only from what a rendered node line says
// resolves to that node in the same snapshot (the rendering is what the agent acts on).
// The selector is the line's id, or its text when it has no id, with the index among the
// matches when several nodes read the same. The lines are parsed back from the text, so a
// quoting that does not round-trip (an escaped quote, a non-ASCII text) fails here.
func TestRenderedLineResolves(t *testing.T) {
	var snaps []Snapshot
	for _, f := range []string{"pixel9a", "pixeltablet", "mediumphonea17"} {
		var s Snapshot
		readRecorded(t, "testdata/"+f+"-uitree.golden.json", &s)
		snaps = append(snaps, s)
	}
	for _, f := range []string{"dy1-246-uitree", "dy3-249-uitree"} {
		var s Snapshot
		readRecorded(t, m1o5+f+".json", &s)
		snaps = append(snaps, s)
	}
	// Texts a tree can hold that a naive quoting breaks.
	snaps = append(snaps, Snapshot{ID: "s-odd", Foreground: "p.q", Display: UIDisplay{Width: 100, Height: 100}, Nodes: []UINode{
		{Ref: "n0", Class: "FrameLayout", ID: "root", Bounds: [4]int{0, 0, 100, 100}, Children: []string{"n1", "n2", "n3"}},
		{Ref: "n1", Class: "TextView", Text: `say "hi" \ bye`, Bounds: [4]int{0, 0, 50, 10}},
		{Ref: "n2", Class: "TextView", Text: "línea\ndos — ñ ✓ <b>&", Bounds: [4]int{0, 10, 50, 20}},
		{Ref: "n3", Class: "TextView", Desc: "only a description", Text: "desc=\"x\" [1,2,3,4]", Bounds: [4]int{0, 20, 50, 30}},
	}})

	checked := 0
	for _, s := range snaps {
		text := uiTreeText(s)
		lines := strings.Split(text, "\n")[1:]
		var shown []UINode
		for _, n := range s.Nodes {
			if !hidden(n) {
				shown = append(shown, n)
			}
		}
		if len(lines) < len(shown) {
			t.Fatalf("%s: %d lines for %d shown nodes", s.ID, len(lines), len(shown))
		}
		st := newSnapshotStore()
		for _, line := range lines {
			p := parseNodeLine(t, line)
			if p.id == "" && p.text == "" {
				continue
			}
			// A node the resolver refuses for what it is, not for how it is written
			// (off the screen), has nothing to prove here.
			if n := nodeOf(s, p.ref); !onScreen(n, s.Display) {
				continue
			}
			sel := Selector{ID: p.id}
			if p.id == "" {
				sel = Selector{Text: p.text}
			}
			found := false
			for i := 0; i < 50 && !found; i++ {
				try := sel
				try.Index = &i
				m, err := st.resolve(s, try)
				if err != nil {
					break
				}
				found = m.Node.Ref == p.ref
			}
			if !found {
				t.Errorf("%s: the selector %+v built from the line %q resolves to no index of its node", s.ID, sel, line)
			}
			checked++
		}
	}
	if checked < 40 {
		t.Errorf("only %d lines were checked: the fixtures do not exercise the rendering", checked)
	}
}

func nodeOf(s Snapshot, ref string) UINode {
	for _, n := range s.Nodes {
		if n.Ref == ref {
			return n
		}
	}
	return UINode{}
}

type nodeLine struct{ ref, class, id, text, desc string }

// parseNodeLine reads a node line back: indentation, ref, quoted class, then #"id",
// "text" and desc="..." in any combination, then flags and bounds, which it ignores.
func parseNodeLine(t *testing.T, line string) nodeLine {
	t.Helper()
	var out nodeLine
	rest := strings.TrimLeft(line, " ")
	next := func() string {
		rest = strings.TrimLeft(rest, " ")
		i := 0
		for i < len(rest) && rest[i] != ' ' && rest[i] != '"' {
			i++
		}
		if i < len(rest) && rest[i] == '"' {
			// a quoted string follows the prefix: read it to its closing quote.
			j := i + 1
			for j < len(rest) && rest[j] != '"' {
				if rest[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
		}
		tok := rest[:i]
		rest = rest[i:]
		return tok
	}
	unq := func(s string) string {
		var v string
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("line %q: %s is not a JSON string: %v", line, s, err)
		}
		return v
	}
	out.ref = next()
	out.class = unq(next())
	for rest != "" {
		tok := next()
		switch {
		case strings.HasPrefix(tok, `#"`):
			out.id = unq(tok[1:])
		case strings.HasPrefix(tok, `desc="`):
			out.desc = unq(tok[len("desc="):])
		case strings.HasPrefix(tok, `"`):
			out.text = unq(tok)
		}
	}
	return out
}

// TestActionResultRendersWhatAnAgentNeeds: the pieces of an action result that change what
// the agent does next are in the rendering: a missed expectation with what was still
// there, a foreground that left the app, a crash with its causes and frames, a system
// dialog. (The recorded cases are in the golden text; these are the ones no recording has.)
func TestActionResultRendersWhatAnAgentNeeds(t *testing.T) {
	r := TapResult{
		Tapped: Tapped{Ref: "n3", Snapshot: "s-1", MatchedBy: "contains", Class: "View", ID: "go", Point: [2]int{1, 2}, Clickable: false,
			ClickableAncestor: &Candidate{Ref: "n2", Class: "Row", Bounds: [4]int{0, 0, 9, 9}}},
		Expect: &ExpectResult{Matched: false, AfterMs: 10, Polls: 2, Reason: "noObservation",
			Vacuous: []Condition{{Foreground: "dev.app"}},
			Unmet: []Unmet{{Condition: Condition{Gone: &Selector{Text: "Spinner"}},
				Candidates: []Candidate{{Ref: "n9", Class: "ProgressBar", Text: "Spinner", Bounds: [4]int{1, 2, 3, 4}}}}}},
		Foreground:   &Foreground{Before: "dev.app"},
		AppCrash:     &AppCrash{Package: "dev.app", PID: 7, Time: "2026-10-07T14:25:06.443049Z", Exception: "java.lang.IllegalStateException", Message: "boom", Frames: []string{"dev.app.A.b(A.kt:1)"}, Source: "logcat"},
		SystemDialog: &SystemDialog{Kind: "crash", Package: "dev.app"},
	}
	got := tapText(r)
	for _, want := range []string{
		`tapped n3 "View" #"go" contains point 1,2 notClickable in s-1`,
		`clickableAncestor n2 "Row" [0,0,9,9]`,
		`expect matched=false afterMs=10 polls=2 reason=noObservation`,
		`vacuous foreground "dev.app"`,
		`unmet gone text="Spinner"`,
		` candidate n9 "ProgressBar" "Spinner" [1,2,3,4]`,
		`foreground "dev.app" -> none`,
		`appCrash "dev.app" source=logcat pid=7 time=14:25:06.443`,
		` exception "java.lang.IllegalStateException" message="boom"`,
		` at "dev.app.A.b(A.kt:1)"`,
		`systemDialog crash package="dev.app"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendering lacks %q:\n%s", want, got)
		}
	}
}

// TestRecordedFixturesAreAllUsed keeps the fixture directory honest: every recorded
// result in it is rendered by a golden case above, so none is dead weight.
func TestRecordedFixturesAreAllUsed(t *testing.T) {
	matches, err := filepath.Glob(m1o5 + "*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		txt := strings.TrimSuffix(m, ".json") + ".txt"
		if _, err := os.Stat(txt); err != nil {
			t.Errorf("%s has no golden text %s: add it to TestRenderingsAreTheGoldenText", m, txt)
		}
	}
	if len(matches) == 0 {
		t.Error("no fixtures")
	}
}

// TestAReadFromThePipelineRendersItsCrashAsItsEntry: over the result device.logs really
// produces at the default maxLines (50 of 208 matched lines, so the read is cut), the
// rendering prints no AndroidRuntime line of a pid that crashes[] names: the block is its
// entry, with its exception, and the lines around the block (the process being killed) stay.
// The fix round's reason: the crash flags of a cut read were once shifted, and a rendering
// that folds on them hid the wrong lines (m1o step 5, B1).
func TestAReadFromThePipelineRendersItsCrashAsItsEntry(t *testing.T) {
	f := launchedLogsFixture(t, nil)
	r := mustLogs(t, f.logsCall(t, `{"device":"emulator-5554","applicationId":"`+appID+`"}`))
	if !r.Truncated || len(r.Lines) != 50 || len(r.Crashes) == 0 {
		t.Fatalf("truncated %v, %d lines, %d crashes: the read must be cut and hold a crash", r.Truncated, len(r.Lines), len(r.Crashes))
	}
	text := logsText(r)
	pids := map[int]bool{}
	for _, c := range r.Crashes {
		pids[c.PID] = true
	}
	inBlock := 0
	for _, l := range r.Lines {
		if l.Tag == "AndroidRuntime" && pids[l.PID] && l.Crash {
			inBlock++
		}
	}
	if inBlock == 0 {
		t.Fatal("no flagged AndroidRuntime line of a crashed pid is among the returned lines: the test would prove nothing")
	}
	for _, gone := range []string{`"FATAL EXCEPTION: main"`, `"Process: ` + appID, `"java.lang.IllegalStateException: `, `"\tat dev.droidyard.flavoured.MainActivity`, `"\tat ` + "com.android.internal.os.ZygoteInit"} {
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, `/"AndroidRuntime"`) && strings.Contains(line, gone) {
				t.Errorf("the rendering prints a crash-block line: %s", line)
			}
		}
	}
	for _, want := range []string{`crash pid=7157 `, ` exception "java.lang.IllegalStateException" message="droidyard fixture crash"`, `"Sending signal. PID: 7157 SIG: 9"`, `"Shutting down VM"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the rendering lacks %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "FATAL EXCEPTION") {
		t.Errorf("FATAL EXCEPTION is still in the rendering:\n%s", text)
	}
}
