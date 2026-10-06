package extract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The recordings S1 run 2 committed (m1g step 2, D16): trimmed real output from
// DopamineControl with the scratch path rewritten to /w/dc. Each golden test below
// pins one producer's grammar and the merge decision it exercises.
const testdata = "../testdata/s1run2"

// fixture is the shape the sidecar's event.build.finished carries; only the fields
// the extractor reads are declared.
type fixture struct {
	Outcome         string    `json:"outcome"`
	Failures        []Failure `json:"failures"`
	FailureProblems []Problem `json:"failureProblems"`
	ProblemEvents   []Problem `json:"problemEvents"`
}

func loadConsole(t *testing.T, name string) []Line {
	t.Helper()
	return parseConsoleFile(t, filepath.Join(testdata, name))
}

// parseConsoleFile reads a forwarded console recording: one line per line, its
// stream and a tab first. A bare line is taken as stdout.
func parseConsoleFile(t *testing.T, p string) []Line {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	var out []Line
	for _, raw := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		stream, text := "stdout", raw
		if i := strings.IndexByte(raw, '\t'); i >= 0 {
			stream, text = raw[:i], raw[i+1:]
		}
		out = append(out, Line{Stream: stream, Text: text})
	}
	return out
}

func loadFixture(t *testing.T, name string) fixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdata, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return f
}

// sameRecord compares a record's published fields, ignoring the private merge key.
func sameRecord(t *testing.T, got, want Record) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("record mismatch\n got: %s\nwant: %s", g, w)
	}
}

func assertRecords(t *testing.T, what string, got, want []Record) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d records, want %d\ngot: %s", what, len(got), len(want), mustJSON(got))
	}
	for i := range want {
		if g, w := mustJSON(got[i]), mustJSON(want[i]); g != w {
			t.Errorf("%s[%d] mismatch\n got: %s\nwant: %s", what, i, g, w)
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestKotlinKGP2021Console pins the recorded Kotlin grammar on the milestone's
// primary cell (KGP 2.0.21): the location comes from the console only, the failure
// tree carries none, and the file:// URI is decoded to a root-relative path. This is
// A5's Kotlin half and the reason the extractor is first-class (ADR-0022 §Decision 2).
func TestKotlinKGP2021Console(t *testing.T) {
	f := loadFixture(t, "kotlin-kgp2.0.21.finished.json")
	got := Extract(Input{
		Root:     "/w/dc",
		Outcome:  f.Outcome,
		Console:  loadConsole(t, "kotlin-kgp2.0.21.console.txt"),
		Failures: f.Failures,
	})
	assertRecords(t, "errors", got.Errors, []Record{
		{
			Severity: Error, Source: SourceConsole, Tool: ToolKotlin,
			File: "app/src/main/java/com/example/dopaminecontrol/SpikeBroken.kt", Line: 3, Column: 24,
			Message: "Unresolved reference 'thisSymbolDoesNotExist'.",
		},
		{
			Severity: Error, Source: SourceConsole, Tool: ToolKotlin,
			File: "app/src/main/java/com/example/dopaminecontrol/SpikeBroken.kt", Line: 6, Column: 12,
			Message: "Return type mismatch: expected 'kotlin.String', actual 'kotlin.Int'.",
		},
	})
	if len(got.Warnings) != 0 || got.Unrecognised {
		t.Errorf("unexpected warnings or unrecognised: %+v", got)
	}
	if len(got.Sources) != 1 || got.Sources[0] != SourceConsole {
		t.Errorf("sources = %v, want [console]", got.Sources)
	}
}

// TestKotlinKGP2420Console pins the untagged Kotlin w: (KGP 2.4.20) and its merge
// with the Problems events the same errors arrive as (F11): the event is the richer
// source, so the merged record is source: event, and the console's e: line and the
// event agree on file, line and column.
func TestKotlinKGP2420Console(t *testing.T) {
	f := loadFixture(t, "kotlin-kgp2.4.20.finished.json")
	got := Extract(Input{
		Root:          "/w/dc",
		Outcome:       f.Outcome,
		Console:       loadConsole(t, "kotlin-kgp2.4.20.console.txt"),
		Failures:      f.Failures,
		ProblemEvents: f.ProblemEvents,
	})
	if len(got.Warnings) != 1 || len(got.Errors) != 1 {
		t.Fatalf("got %d warnings and %d errors, want 1 and 1: %s", len(got.Warnings), len(got.Errors), mustJSON(got))
	}
	w := got.Warnings[0]
	if w.Source != SourceEvent || w.Tool != ToolKotlin {
		t.Errorf("warning source/tool = %s/%s, want event/kotlin", w.Source, w.Tool)
	}
	if w.File != "feature/applist/src/main/kotlin/com/example/dopaminecontrol/feature/applist/ui/AppListScreen.kt" || w.Line != 400 || w.Column != 75 {
		t.Errorf("warning location = %s:%d:%d", w.File, w.Line, w.Column)
	}
	if w.ProblemID != "UNNECESSARY_NOT_NULL_ASSERTION" {
		t.Errorf("warning problemId = %q", w.ProblemID)
	}
	e := got.Errors[0]
	if e.Source != SourceEvent || e.Tool != ToolKotlin || e.ProblemID != "UNRESOLVED_REFERENCE" {
		t.Errorf("error source/tool/id = %s/%s/%s", e.Source, e.Tool, e.ProblemID)
	}
	if e.File != "app/src/main/java/com/example/dopaminecontrol/SpikeBroken.kt" || e.Line != 3 || e.Column != 24 {
		t.Errorf("error location = %s:%d:%d", e.File, e.Line, e.Column)
	}
}

// TestKSPConsole pins the KSP form: a [ksp]-tagged line with a plain path, a line
// and no column.
func TestKSPConsole(t *testing.T) {
	got := Extract(Input{Root: "/w/dc", Outcome: "failed", Console: loadConsole(t, "ksp.console.txt")})
	if len(got.Warnings) != 1 || len(got.Errors) != 0 {
		t.Fatalf("got %d warnings and %d errors: %s", len(got.Warnings), len(got.Errors), mustJSON(got))
	}
	sameRecord(t, got.Warnings[0], Record{
		Severity: Warning, Source: SourceConsole, Tool: ToolKSP,
		File: "core/database/src/main/kotlin/com/example/dopaminecontrol/core/database/AppDatabase.kt",
		Line: 9,
		Message: "Schema export directory was not provided to the annotation processor so Room " +
			"cannot export the schema. You can either provide `room.schemaLocation` annotation " +
			"processor argument by applying the Room Gradle plugin (id 'androidx.room') OR set " +
			"exportSchema to false.",
	})
}

// TestManifestMergerConsole pins the manifest merger's range header and its
// tab-indented message, and that the file-only "… Error:" line plus its
// "\tValidation failed, exiting" continuation are noise, not a second record
// (m1g step 2's change).
func TestManifestMergerConsole(t *testing.T) {
	f := loadFixture(t, "manifest-merger.finished.json")
	got := Extract(Input{Root: "/w/dc", Outcome: f.Outcome, Console: loadConsole(t, "manifest-merger.console.txt"), Failures: f.Failures})
	assertRecords(t, "errors", got.Errors, []Record{{
		Severity: Error, Source: SourceConsole, Tool: ToolManifestMerger,
		File: "app/src/main/AndroidManifest.xml", Line: 15, Column: 5,
		Message: "tools:replace specified at line:15 for attribute android:fooBar, but no new value specified",
	}})
}

// TestJavacConsoleDeduplicatesTheTwoRenderings pins the javac block: the caret
// gives the column, the continuations fold into the message, and the same form
// rendered again indented two spaces by the failure merges into one record (m1g
// step 2's change: leading indentation is stripped and the merge key deduplicates).
func TestJavacConsoleDeduplicatesTheTwoRenderings(t *testing.T) {
	got := Extract(Input{Root: "/w/dc", Outcome: "failed", Console: loadConsole(t, "javac.console.txt")})
	if len(got.Errors) != 2 {
		t.Fatalf("got %d errors, want 2 (the two renderings must merge): %s", len(got.Errors), mustJSON(got))
	}
	if got.Errors[0].Column != 13 || got.Errors[1].Column != 16 {
		t.Errorf("columns = %d, %d, want 13, 16", got.Errors[0].Column, got.Errors[1].Column)
	}
	if got.Errors[0].Tool != ToolJavac || got.Errors[0].Source != SourceConsole {
		t.Errorf("tool/source = %s/%s", got.Errors[0].Tool, got.Errors[0].Source)
	}
	if !strings.HasPrefix(got.Errors[0].Message, "cannot find symbol") {
		t.Errorf("message = %q", got.Errors[0].Message)
	}
	if !strings.Contains(got.Errors[0].Message, "symbol:") {
		t.Errorf("continuation not folded into message: %q", got.Errors[0].Message)
	}
}

// TestJavacProblemsMergeIntoTheConsoleRecord pins merge rule 2 on the recorded
// javac cell: the console record and the structured problem of the same error
// become one source: failure record.
func TestJavacProblemsMergeIntoTheConsoleRecord(t *testing.T) {
	f := loadFixture(t, "javac.finished.json")
	got := Extract(Input{
		Root: "/w/dc", Outcome: f.Outcome,
		Console:         loadConsole(t, "javac.console.txt"),
		Failures:        f.Failures,
		FailureProblems: f.FailureProblems,
		ProblemEvents:   f.ProblemEvents,
	})
	if len(got.Errors) != 2 {
		t.Fatalf("got %d errors, want 2: %s", len(got.Errors), mustJSON(got))
	}
	for i, e := range got.Errors {
		if e.Source != SourceFailure {
			t.Errorf("errors[%d].source = %s, want failure (failure > console)", i, e.Source)
		}
		if e.Tool != ToolJavac {
			t.Errorf("errors[%d].tool = %s", i, e.Tool)
		}
		if e.Message != f.FailureProblems[i].Details {
			t.Errorf("errors[%d].message is not the richer problem's details", i)
		}
	}
	if got.Errors[0].Column != 13 || got.Errors[1].Column != 16 {
		t.Errorf("columns = %d, %d", got.Errors[0].Column, got.Errors[1].Column)
	}
	if len(got.Sources) != 3 {
		t.Errorf("sources = %v, want failure, event and console", got.Sources)
	}
}

// fakeResolver stands in for the AGP blame lookup (D12): the extractor must place
// what it returns and leave the merged path verbatim.
type fakeResolver struct {
	blamePath string
	line      int
	source    string
	srcLine   int
	srcColumn int
	excerpt   string
	dropAll   bool
}

func (f fakeResolver) ResolveBlame(blamePath string, line int) (string, int, int, bool) {
	if f.dropAll || blamePath != f.blamePath || line != f.line {
		return "", 0, 0, false
	}
	return f.source, f.srcLine, f.srcColumn, true
}

func (f fakeResolver) MergedLine(blamePath string, line int) (string, bool) {
	if f.dropAll || blamePath != f.blamePath || line != f.line {
		return "", false
	}
	return f.excerpt, true
}

// TestAAPT2LinkWithResolution pins D12: the merged path stays verbatim with
// merged: true, the blame lookup fills the resolved source location, the merged
// file's line becomes the excerpt, and the file-less follow-up is its own record.
func TestAAPT2LinkWithResolution(t *testing.T) {
	f := loadFixture(t, "aapt2-link.finished.json")
	const blame = "com.example.dopaminecontrol.app-mergeDebugResources-42:/values/values.xml"
	r := fakeResolver{
		blamePath: blame, line: 210,
		source: "/w/dc/app/src/main/res/values/strings.xml", srcLine: 3, srcColumn: 4,
		excerpt: `<string name="this_string_does_not_exist">false</string>`,
	}
	got := Extract(Input{
		Root: "/w/dc", Outcome: f.Outcome,
		Console:  loadConsole(t, "aapt2-link.console.txt"),
		Failures: f.Failures,
		Resolver: r,
	})
	if len(got.Errors) != 2 {
		t.Fatalf("got %d errors, want 2: %s", len(got.Errors), mustJSON(got))
	}
	located := got.Errors[0]
	if located.File != blame || !located.Merged || located.Line != 210 {
		t.Errorf("merged record = %s merged=%v line=%d", located.File, located.Merged, located.Line)
	}
	if located.Resolved == nil || located.Resolved.File != "app/src/main/res/values/strings.xml" || located.Resolved.Line != 3 || located.Resolved.Column != 4 {
		t.Errorf("resolved = %s", mustJSON(located.Resolved))
	}
	if located.Excerpt != r.excerpt {
		t.Errorf("excerpt = %q", located.Excerpt)
	}
	follow := got.Errors[1]
	if follow.File != "" || follow.Message != "failed linking references." || follow.Tool != ToolAAPT2 {
		t.Errorf("follow-up record = %s", mustJSON(follow))
	}
}

// TestAAPT2LinkWithoutResolution pins the other half of "either lookup failing
// omits its field": with no resolver the merged path is all that is reported.
func TestAAPT2LinkWithoutResolution(t *testing.T) {
	f := loadFixture(t, "aapt2-link.finished.json")
	got := Extract(Input{Root: "/w/dc", Outcome: f.Outcome, Console: loadConsole(t, "aapt2-link.console.txt"), Failures: f.Failures})
	if len(got.Errors) != 2 {
		t.Fatalf("got %d errors: %s", len(got.Errors), mustJSON(got))
	}
	if got.Errors[0].Resolved != nil || got.Errors[0].Excerpt != "" {
		t.Errorf("resolved/excerpt should be omitted: %s", mustJSON(got.Errors[0]))
	}
	if got.Errors[0].File != "com.example.dopaminecontrol.app-mergeDebugResources-42:/values/values.xml" {
		t.Errorf("file = %q", got.Errors[0].File)
	}
}

// TestProblemsRecordAndConsoleMergeKeepingTheConsoleColumn pins merge rule 2's
// location filling directly: a failure-carried problem with no column and the
// console's same error with a caret column become one source: failure record that
// keeps the console's column.
func TestProblemsRecordAndConsoleMergeKeepingTheConsoleColumn(t *testing.T) {
	const java = "/w/dc/app/src/main/java/com/example/dopaminecontrol/SpikeBrokenJava.java"
	console := []Line{
		{Stream: "stderr", Text: java + ":4: error: cannot find symbol"},
		{Stream: "stderr", Text: "    int x = undefinedSymbol;"},
		{Stream: "stderr", Text: "            ^"},
	}
	problem := Problem{
		Severity: "error", ProblemID: "compiler-err-cant-resolve-location",
		ProblemDisplayName: "Java compilation error", ContextualLabel: "cannot find symbol",
		Details: java + ":4: error: cannot find symbol", File: java, Line: 4, // no column
	}
	got := Extract(Input{Root: "/w/dc", Outcome: "failed", Console: console, FailureProblems: []Problem{problem}})
	if len(got.Errors) != 1 {
		t.Fatalf("got %d errors, want 1 merged record: %s", len(got.Errors), mustJSON(got))
	}
	e := got.Errors[0]
	if e.Source != SourceFailure {
		t.Errorf("source = %s, want failure", e.Source)
	}
	if e.Column != 13 {
		t.Errorf("column = %d, want the console's 13", e.Column)
	}
}

// TestUnrecognisedFailure pins merge rule 4: a failed build whose output matches no
// grammar is not an empty success. It carries unrecognised: true, the root failure
// messages and a bounded tail.
func TestUnrecognisedFailure(t *testing.T) {
	got := Extract(Input{
		Root: "/w/dc", Outcome: "failed",
		Console: []Line{
			{Stream: "stdout", Text: "> Task :app:whatever FAILED"},
			{Stream: "stderr", Text: "Execution failed for task ':app:whatever'."},
			{Stream: "stderr", Text: "Something no grammar knows about."},
		},
		Failures: []Failure{{
			Message: "Execution failed for task ':app:whatever'.",
			Causes:  []Failure{{Message: "Something no grammar knows about."}},
		}},
	})
	if len(got.Errors) != 0 {
		t.Fatalf("errors = %s", mustJSON(got.Errors))
	}
	if !got.Unrecognised {
		t.Error("unrecognised = false, want true")
	}
	if len(got.RootFailures) != 1 || got.RootFailures[0] != "Execution failed for task ':app:whatever'." {
		t.Errorf("rootFailures = %v", got.RootFailures)
	}
	if !strings.Contains(got.Tail, "Something no grammar knows about.") {
		t.Errorf("tail = %q", got.Tail)
	}
}

// TestErrorCap pins the errors cap: 250 recognised errors keep the first 200 and
// count the remaining 50 as truncated.
func TestErrorCap(t *testing.T) {
	var console []Line
	for i := 1; i <= 250; i++ {
		console = append(console,
			Line{Stream: "stderr", Text: fmt.Sprintf("/w/dc/app/src/main/java/Foo.java:%d: error: e%d", i, i)},
			Line{Stream: "stderr", Text: "    int x = 0;"},
			Line{Stream: "stderr", Text: "    ^"},
		)
	}
	got := Extract(Input{Root: "/w/dc", Outcome: "failed", Console: console})
	if len(got.Errors) != ErrorCap {
		t.Fatalf("errors = %d, want %d", len(got.Errors), ErrorCap)
	}
	if got.Truncated != 50 {
		t.Errorf("truncated = %d, want 50", got.Truncated)
	}
}

// TestWarningCap pins the warnings cap: only the first 50 are kept and the rest
// are counted as truncated, as the error cap is.
func TestWarningCap(t *testing.T) {
	var console []Line
	for i := 1; i <= 60; i++ {
		console = append(console, Line{Stream: "stdout", Text: fmt.Sprintf("w: file:///w/dc/app/W%d.kt:1:1 warn", i)})
	}
	got := Extract(Input{Root: "/w/dc", Outcome: "failed", Console: console})
	if len(got.Warnings) != WarningCap || got.Truncated != 10 {
		t.Fatalf("warnings = %d, truncated = %d; want %d and 10", len(got.Warnings), got.Truncated, WarningCap)
	}
}

// TestAAPT2FollowUpNeedsAContext pins that the file-less "error: …" form is not
// read as an AAPT2 record outside an AAPT2 block, where it would swallow unrelated
// lines.
func TestAAPT2FollowUpNeedsAContext(t *testing.T) {
	got := Extract(Input{Root: "/w/dc", Outcome: "failed", Console: []Line{
		{Stream: "stderr", Text: "error: something unrelated"},
	}})
	if len(got.Errors) != 0 {
		t.Fatalf("a lone error: line became a record: %s", mustJSON(got.Errors))
	}
}

// TestPercentEncodedPath pins "percent-decoded": Kotlin's file:// URI may carry
// %20, which is the file's real name.
func TestPercentEncodedPath(t *testing.T) {
	got := Extract(Input{Root: "/w/dc", Outcome: "failed", Console: []Line{
		{Stream: "stderr", Text: "e: file:///w/dc/app/src/main/java/com/example/My%20File.kt:3:5 Unresolved reference 'X'."},
	}})
	if len(got.Errors) != 1 {
		t.Fatalf("errors = %s", mustJSON(got.Errors))
	}
	if got.Errors[0].File != "app/src/main/java/com/example/My File.kt" {
		t.Errorf("file = %q", got.Errors[0].File)
	}
}

// TestPrivateVarResolved pins macOS /private resolution: a /private/var path under
// a /var root is inside the project, and run 1's Kotlin line (which used
// /private/var/folders) parses to the same relative file.
func TestPrivateVarResolved(t *testing.T) {
	got := Extract(Input{Root: "/var/w/dc", Outcome: "failed", Console: []Line{
		{Stream: "stderr", Text: "e: file:///private/var/w/dc/app/Foo.kt:3:5 boom"},
	}})
	if len(got.Errors) != 1 {
		t.Fatalf("errors = %s", mustJSON(got.Errors))
	}
	if got.Errors[0].File != "app/Foo.kt" || got.Errors[0].External {
		t.Errorf("file = %q external=%v", got.Errors[0].File, got.Errors[0].External)
	}

	// An outside file is reported as given and flagged external.
	out := Extract(Input{Root: "/var/w/dc", Outcome: "failed", Console: []Line{
		{Stream: "stderr", Text: "e: file:///private/var/other/Foo.kt:3:5 boom"},
	}})
	if out.Errors[0].File != "/private/var/other/Foo.kt" || !out.Errors[0].External {
		t.Errorf("outside file = %q external=%v", out.Errors[0].File, out.Errors[0].External)
	}
}

// TestRunOneLines pins run 1's three recorded console lines where run 2's grammar
// did not differ (gradle.md §Spikes): the same Kotlin, KSP and AAPT2 forms parse
// from the earlier recording too.
func TestRunOneLines(t *testing.T) {
	const root = "/private/var/folders/xy/T/spike"
	got := Extract(Input{Root: root, Outcome: "failed", Console: []Line{
		{Stream: "stderr", Text: "e: file:///private/var/folders/xy/T/spike/app/src/main/java/com/example/dopaminecontrol/SpikeBroken.kt:3:42 Unresolved reference 'thisSymbolDoesNotExist'."},
		{Stream: "stdout", Text: "w: [ksp] /private/var/folders/xy/T/spike/app/src/main/java/com/example/Foo.kt:9: schema"},
		{Stream: "stderr", Text: "Android resource linking failed"},
		{Stream: "stderr", Text: "com.example.dopaminecontrol.app-mergeDebugResources-42:/values/values.xml:210: error: resource string/this_string_does_not_exist not found."},
		{Stream: "stderr", Text: "error: failed linking references."},
	}})
	assertRecords(t, "errors", got.Errors[:2], []Record{
		{
			Severity: Error, Source: SourceConsole, Tool: ToolKotlin,
			File: "app/src/main/java/com/example/dopaminecontrol/SpikeBroken.kt", Line: 3, Column: 42,
			Message: "Unresolved reference 'thisSymbolDoesNotExist'.",
		},
		{
			Severity: Error, Source: SourceConsole, Tool: ToolAAPT2,
			File: "com.example.dopaminecontrol.app-mergeDebugResources-42:/values/values.xml", Line: 210, Merged: true,
			Message: "resource string/this_string_does_not_exist not found.",
		},
	})
	if len(got.Warnings) != 1 || got.Warnings[0].Tool != ToolKSP || got.Warnings[0].File != "app/src/main/java/com/example/Foo.kt" || got.Warnings[0].Line != 9 {
		t.Errorf("ksp warning = %s", mustJSON(got.Warnings))
	}
	if len(got.Errors) != 3 {
		t.Errorf("errors = %s (want the located AAPT2 error and its follow-up)", mustJSON(got.Errors))
	}
}

// TestTailIsBounded pins the 4 KiB tail and its UTF-8 boundary.
func TestTailIsBounded(t *testing.T) {
	big := strings.Repeat("x", 5000) + "é"
	got := Extract(Input{Root: "/w/dc", Outcome: "failed", Console: []Line{{Stream: "stdout", Text: big}}})
	if !got.Unrecognised {
		t.Fatal("want unrecognised")
	}
	if len(got.Tail) > TailBytes {
		t.Errorf("tail is %d bytes, want at most %d", len(got.Tail), TailBytes)
	}
	if !utf8.ValidString(got.Tail) {
		t.Error("tail is not valid UTF-8 (cut mid-rune)")
	}
}
