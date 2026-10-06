// Package extract recognises the build output formats Droidyard recorded in spike
// S1 and merges them with the structured Problems API records into one bounded
// error list (ADR-0022; docs/subsystems/gradle.md §Error model, §Console extractor,
// §Merge rules).
//
// It is pure Go: the sidecar forwards console text raw and the core interprets it
// here, where golden tests over the recorded real output in
// internal/domain/build/testdata/s1run2 pin each producer's grammar. Nothing in
// this package touches a build, a JVM or a device.
//
// One grammar per form. Each form is independent, so a format change degrades one
// tool, never the whole extractor (gradle.md §Console extractor):
//
//   - Kotlin:   e: file:///abs/Foo.kt:3:42 message   (file:// URI, percent-decoded)
//   - KSP:      w: [ksp] /abs/Foo.kt:9: message      (plain path, no column)
//   - AAPT2:    <blame-id>:/values/values.xml:210: error: message, then a file-less
//     error: failed linking references.
//   - javac:    /abs/Foo.java:83: error: message, the source and caret lines, and
//     the symbol/location continuations; the caret gives the column.
//   - manifest: /abs/AndroidManifest.xml:15:5-58:19 Error: + an indented message.
//
// Not recognised here (no recording in S1 run 2): kapt, the older Kotlin
// "e: /abs/Foo.kt: (83, 12): message" form, AGP SDK signatures and problem
// summaries.
package extract

import "strings"

// Severity is a record's severity. Gradle assigns it per its own semantics; the
// extractor follows the producer's wording (gradle.md §Error model).
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Source is which of the three merge sources produced a record (gradle.md §Three
// sources, merged). It is the path, not the producer: every console-extractor
// record carries SourceConsole, including one read out of the failure-tree text,
// because it is the extractor that produced it.
type Source string

const (
	// SourceFailure is a failure-carried problem (Failure.getProblems(), 8.12+).
	SourceFailure Source = "failure"
	// SourceEvent is a Problems progress event (the only source for warnings).
	SourceEvent Source = "event"
	// SourceConsole is the core's console extractor.
	SourceConsole Source = "console"
)

// Tool is the producer a record is attributed to (gradle.md §Error model).
type Tool string

const (
	ToolKotlin         Tool = "kotlin"
	ToolJavac          Tool = "javac"
	ToolKapt           Tool = "kapt"
	ToolKSP            Tool = "ksp"
	ToolAAPT2          Tool = "aapt2"
	ToolManifestMerger Tool = "manifest-merger"
	ToolAGP            Tool = "agp"
	ToolGradle         Tool = "gradle"
)

// Bounds from gradle.md §Merge rules: errors keep the first ErrorCap in emission
// order and warnings the first WarningCap; the rest are counted in
// Result.Truncated. An unrecognised failure returns the last TailBytes of the
// console.
const (
	ErrorCap   = 200
	WarningCap = 50
	TailBytes  = 4 << 10
)

// Record is one merged error or warning (gradle.md §Error model).
type Record struct {
	Severity Severity `json:"severity"`
	Source   Source   `json:"source"`
	Tool     Tool     `json:"tool"`

	// ProblemID, Solutions and DocumentationLink exist only for Problems API
	// records (gradle.md §Error model).
	ProblemID         string   `json:"problemId,omitempty"`
	Solutions         []string `json:"solutions,omitempty"`
	DocumentationLink string   `json:"documentationLink,omitempty"`

	// File has three cases (gradle.md §Error model): relative to the project root;
	// an outside absolute path, reported as given with External set; or an AAPT2
	// merged-resource path, reported verbatim with Merged set and never a source
	// file. Line and Column are absent when the producer did not emit them.
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`

	External bool `json:"external,omitempty"`
	Merged   bool `json:"merged,omitempty"`

	// Resolved is the developer's source location D12's blame lookup recovered for
	// a merged path (m1g, step 2 F3). The merged path stays in File; this is the
	// additional origin, and either lookup failing omits its field.
	//
	// Plan step 2's change names this the record's source {file, line, column}; the
	// name is Resolved here because Record.Source already means the merge source
	// (ADR-0022 §Decision 3).
	Resolved *Location `json:"resolved,omitempty"`
	// Excerpt is the quoted line from the merged file (D12 option 2).
	Excerpt string `json:"excerpt,omitempty"`

	Message string `json:"message"`

	// head is the merge key's normalised message head (gradle.md §Merge rules 1);
	// it never leaves this package.
	head string
}

// Location is a resolved source position. Line and Column are 1-based.
type Location struct {
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
}

// Line is one forwarded console line and the stream it arrived on
// (event.build.output, gradle.md §Protocol).
type Line struct {
	Stream string `json:"stream,omitempty"` // "stdout" | "stderr"
	Text   string `json:"text"`
}

// Failure is one node of the build's failure tree (event.build.finished). Only the
// message and its causes are read; the sidecar's stable ids are the record's
// business, not the extractor's.
type Failure struct {
	Message string    `json:"message"`
	Causes  []Failure `json:"causes,omitempty"`
}

// Problem is one structured record from the Problems API, as the sidecar forwards
// it, whether failure-carried or a progress event.
type Problem struct {
	Severity           string   `json:"severity"`
	ProblemID          string   `json:"problemId,omitempty"`
	ProblemDisplayName string   `json:"problemDisplayName,omitempty"`
	ContextualLabel    string   `json:"contextualLabel,omitempty"`
	Details            string   `json:"details,omitempty"`
	File               string   `json:"file,omitempty"`
	Line               int      `json:"line,omitempty"`
	Column             int      `json:"column,omitempty"`
	Solutions          []string `json:"solutions,omitempty"`
	DocumentationLink  string   `json:"documentationLink,omitempty"`

	// Tool overrides the inferred tool. Set it when the failing task names the
	// compiler; left empty, the extractor infers from the problem's labels.
	Tool Tool `json:"-"`
}

// MergedResolver resolves an AAPT2 merged-resource blame path to what a developer
// can act on (m1g D12). It is an interface so this package stays pure Go: the
// implementation needs the module's build directory and variant, which only the
// build domain knows, and the AGP intermediate layout step 2 recorded (F3).
type MergedResolver interface {
	// ResolveBlame returns the developer's source file (absolute), its 1-based
	// line and column, for a reported blame path (<blame-id>:/<dir>/<file>) and its
	// 1-based line. ok is false when the blame file has no covering entry.
	ResolveBlame(blamePath string, line int) (file string, lineNo int, column int, ok bool)
	// MergedLine returns the text of the merged file's 1-based line for a reported
	// blame path. ok is false when the merged file or line is absent.
	MergedLine(blamePath string, line int) (text string, ok bool)
}

// Input is everything one build's errors are merged from.
type Input struct {
	// Root is the project root the file paths are made relative to. It is
	// canonicalised against macOS /private before use.
	Root string
	// Outcome is the build outcome ("success", "failed" or "cancelled"). The
	// unrecognised fallback fires only on "failed".
	Outcome string

	// Console is the forwarded console, in order (gradle.md §Output).
	Console []Line
	// Failures is the failure tree; its node messages are searched for the
	// extractor's forms, and its roots supply the unrecognised fallback's messages.
	Failures []Failure

	// FailureProblems are the failure-carried problems (source: failure) and
	// ProblemEvents the Problems progress events (source: event).
	FailureProblems []Problem
	ProblemEvents   []Problem

	// Resolver, when set, resolves AAPT2 merged paths. Never required.
	Resolver MergedResolver
}

// Result is build.errors' merged output (gradle.md §Merge rules). Errors come
// first in emission order, then warnings; Truncated counts the records the caps
// dropped, errors and warnings together.
type Result struct {
	Errors    []Record `json:"errors"`
	Warnings  []Record `json:"warnings"`
	Truncated int      `json:"truncated,omitempty"`

	// Unrecognised is set when the build failed and no source recognised an error.
	// It is the only place a raw tail appears, and RootFailures and Tail are set
	// with it (gradle.md §Merge rules 4; ADR-0022 §Decision 4).
	Unrecognised bool     `json:"unrecognised,omitempty"`
	RootFailures []string `json:"rootFailures,omitempty"`
	Tail         string   `json:"tail,omitempty"`

	// Sources are the merge sources that produced at least one record, in the
	// precedence order failure, event, console.
	Sources []Source `json:"sources,omitempty"`
}

// Extract recognises every recorded form, merges the records across the three
// sources and applies the caps. It never fails: an unrecognised failure is a
// result, not an error (ADR-0022 §Decision 4).
func Extract(in Input) Result {
	root := canonical(in.Root)

	// Candidates in emission order: the console stream, then the failure-tree
	// text, then the structured problems. The merge chooses the richest source
	// per key, so this order only fixes the output order and the winner's text.
	var cands []Record
	cands = append(cands, parseText(root, linesOf(in.Console), in.Resolver)...)
	cands = append(cands, parseFailureText(root, in.Failures, in.Resolver)...)
	for _, p := range in.FailureProblems {
		cands = append(cands, recordFromProblem(root, p, SourceFailure))
	}
	for _, p := range in.ProblemEvents {
		cands = append(cands, recordFromProblem(root, p, SourceEvent))
	}

	var res Result
	if s := sourcesOf(cands); len(s) > 0 {
		res.Sources = s
	}

	merged := merge(cands)
	for _, r := range merged {
		if r.Severity == Warning {
			res.Warnings = append(res.Warnings, r)
		} else {
			res.Errors = append(res.Errors, r)
		}
	}
	if n := len(res.Errors) - ErrorCap; n > 0 {
		res.Truncated += n
		res.Errors = res.Errors[:ErrorCap]
	}
	if n := len(res.Warnings) - WarningCap; n > 0 {
		res.Truncated += n
		res.Warnings = res.Warnings[:WarningCap]
	}

	if in.Outcome == "failed" && len(res.Errors) == 0 {
		res.Unrecognised = true
		res.RootFailures = rootMessages(in.Failures)
		res.Tail = tail(in.Console)
	}
	return res
}

func linesOf(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

func parseFailureText(root string, failures []Failure, resolver MergedResolver) []Record {
	var lines []string
	var walk func([]Failure)
	walk = func(fs []Failure) {
		for _, f := range fs {
			if f.Message != "" {
				lines = append(lines, strings.Split(f.Message, "\n")...)
			}
			walk(f.Causes)
		}
	}
	walk(failures)
	return parseText(root, lines, resolver)
}

// sourcesOf reports which sources produced a record, in precedence order.
func sourcesOf(cands []Record) []Source {
	seen := map[Source]bool{}
	for _, c := range cands {
		seen[c.Source] = true
	}
	var out []Source
	for _, s := range []Source{SourceFailure, SourceEvent, SourceConsole} {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out
}

// rootMessages is the unrecognised fallback's root failure messages: the top-level
// failures' messages, not their causes' (gradle.md §Merge rules 4).
func rootMessages(failures []Failure) []string {
	var out []string
	for _, f := range failures {
		if f.Message != "" {
			out = append(out, f.Message)
		}
	}
	return out
}

// tail is the last TailBytes of the console, cut at a UTF-8 boundary.
func tail(lines []Line) string {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l.Text)
	}
	s := b.String()
	if len(s) <= TailBytes {
		return s
	}
	s = s[len(s)-TailBytes:]
	for len(s) > 0 && !utf8Start(s[0]) {
		s = s[1:]
	}
	return s
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
