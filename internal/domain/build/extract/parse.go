package extract

import (
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// The recorded grammars (docs/subsystems/gradle.md §Console extractor; the raw
// recordings are in internal/domain/build/testdata/s1run2). Each is anchored and
// matched against a line with its leading indentation stripped, so the two
// renderings of the javac form (task output, then the failure rendering indented
// two spaces) parse alike and the merge key deduplicates them.
var (
	// Kotlin: e: file:///abs/SpikeBroken.kt:3:24 Unresolved reference 'x'.
	kotlinRE = regexp.MustCompile(`^([ew]): file://(.+?):(\d+):(\d+) (.*)$`)
	// KSP: w: [ksp] /abs/AppDatabase.kt:9: message (plain path, no column).
	kspRE = regexp.MustCompile(`^([ew]): \[ksp\] (.+?):(\d+): (.*)$`)
	// AAPT2 link: <blame-id>:/values/values.xml:210: error: message.
	aapt2RE = regexp.MustCompile(`^([^:\s][^:]*):(/[^:]+):(\d+): error:\s?(.*)$`)
	// AAPT2's file-less follow-up: error: failed linking references.
	aapt2FollowRE = regexp.MustCompile(`^error:\s*(.+)$`)
	// javac: /abs/Foo.java:83: error: message.
	javacRE = regexp.MustCompile(`^(.*\.java):(\d+): (error|warning): (.*)$`)
	// Manifest merger: /abs/AndroidManifest.xml:15:5-58:19 Error:.
	manifestRE = regexp.MustCompile(`^(.+\.xml):(\d+):(\d+)-(\d+):(\d+) Error:$`)
	// The javac-style location prefix on a problem's first detail line, stripped so
	// a structured message and the console message share a merge key.
	locationPrefixRE = regexp.MustCompile(`^\S+:\d+:\s*(?:error|warning):\s*`)
)

// parseText recognises every form in a sequence of lines. It keeps the AAPT2
// follow-up from being read as a generic "error:" line anywhere: the file-less form
// is recognised only while an AAPT2 block, opened by the linking-failed header or a
// located line, is open.
func parseText(root string, lines []string, resolver MergedResolver) []Record {
	var recs []Record
	inAAPT2 := false
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		if strings.Contains(t, "Android resource linking failed") {
			inAAPT2 = true
			continue
		}
		if m := aapt2RE.FindStringSubmatch(t); m != nil {
			inAAPT2 = true
			recs = append(recs, aapt2Record(root, m, resolver))
			continue
		}
		if inAAPT2 {
			if m := aapt2FollowRE.FindStringSubmatch(t); m != nil {
				recs = append(recs, Record{
					Severity: Error, Source: SourceConsole, Tool: ToolAAPT2,
					Message: m[1], head: messageHead(m[1]),
				})
				continue
			}
		}
		inAAPT2 = false
		if m := kotlinRE.FindStringSubmatch(t); m != nil {
			recs = append(recs, kotlinRecord(root, m))
			continue
		}
		if m := kspRE.FindStringSubmatch(t); m != nil {
			recs = append(recs, kspRecord(root, m))
			continue
		}
		if m := javacRE.FindStringSubmatch(t); m != nil {
			rec, next := javacBlock(root, lines, i, m)
			recs = append(recs, rec)
			i = next - 1
			continue
		}
		if m := manifestRE.FindStringSubmatch(t); m != nil {
			rec, next := manifestBlock(root, lines, i, m)
			recs = append(recs, rec)
			i = next - 1
			continue
		}
	}
	return recs
}

func kotlinRecord(root string, m []string) Record {
	rec := Record{
		Severity: severity(m[1]),
		Source:   SourceConsole,
		Tool:     ToolKotlin,
		Line:     atoi(m[3]),
		Column:   atoi(m[4]),
		Message:  m[5],
		head:     messageHead(m[5]),
	}
	rec.File, rec.External = normalize(root, decodeURI(m[2]))
	return rec
}

func kspRecord(root string, m []string) Record {
	rec := Record{
		Severity: severity(m[1]),
		Source:   SourceConsole,
		Tool:     ToolKSP,
		Line:     atoi(m[3]),
		Message:  m[4],
		head:     messageHead(m[4]),
	}
	rec.File, rec.External = normalize(root, m[2])
	return rec
}

// aapt2Record keeps the merged path verbatim (D12 option 3 always) and adds what
// the resolver recovers (option 1 and 2): either lookup failing omits its field.
func aapt2Record(root string, m []string, resolver MergedResolver) Record {
	blame := m[1] + ":" + m[2]
	line := atoi(m[3])
	rec := Record{
		Severity: Error, Source: SourceConsole, Tool: ToolAAPT2,
		File: blame, Line: line, Merged: true,
		Message: m[4], head: messageHead(m[4]),
	}
	if resolver != nil {
		if file, l, c, ok := resolver.ResolveBlame(blame, line); ok && file != "" {
			// The resolve contract is root-relative (m1g step 2's change); an
			// outside result is reported as given.
			rel, _ := normalize(root, file)
			rec.Resolved = &Location{File: rel, Line: l, Column: c}
		}
		if text, ok := resolver.MergedLine(blame, line); ok {
			rec.Excerpt = text
		}
	}
	return rec
}

// javacBlock consumes a javac error: the source line, the caret line that gives the
// column, and the symbol/location continuations, all folded into the message. The
// block is de-indented by its own minimum indentation before the column is read, so
// the failure rendering (the whole block indented two spaces) yields the same
// column as the task's own output.
func javacBlock(root string, lines []string, i int, m []string) (Record, int) {
	sev := Error
	if m[3] == "warning" {
		sev = Warning
	}
	block := []string{lines[i]}
	next := i + 1
	if next < len(lines) {
		t := strings.TrimSpace(lines[next])
		if isIndented(lines[next]) && t != "" && !isCaret(t) && !javacContinuation(t) {
			block = append(block, lines[next])
			next++
		}
	}
	if next < len(lines) && isCaret(strings.TrimSpace(lines[next])) {
		block = append(block, lines[next])
		next++
	}
	for next < len(lines) {
		t := strings.TrimSpace(lines[next])
		if t == "" || !javacContinuation(t) {
			break
		}
		block = append(block, lines[next])
		next++
	}

	indent := minIndent(block)
	ded := make([]string, len(block))
	for k, l := range block {
		ded[k] = stripIndent(l, indent)
	}
	column := 0
	for _, l := range ded {
		if isCaret(strings.TrimSpace(l)) {
			column = strings.IndexByte(l, '^') + 1
			break
		}
	}
	// The location lives in File/Line/Column; drop the redundant prefix from the
	// message's header line.
	if len(ded) > 0 {
		ded[0] = locationPrefixRE.ReplaceAllString(ded[0], "")
	}

	rec := Record{
		Severity: sev, Source: SourceConsole, Tool: ToolJavac,
		Line: atoi(m[2]), Column: column,
		Message: strings.Join(ded, "\n"),
		head:    messageHead(m[4]),
	}
	rec.File, rec.External = normalize(root, m[1])
	return rec, next
}

// manifestBlock consumes the range header and its indented message. The file-only
// "…AndroidManifest.xml Error:" line and its "Validation failed, exiting"
// continuation are noise, not a record (m1g step 2's change).
func manifestBlock(root string, lines []string, i int, m []string) (Record, int) {
	next := i + 1
	var msg []string
	for next < len(lines) {
		l := lines[next]
		t := strings.TrimSpace(l)
		if t == "" || !isIndented(l) {
			break
		}
		if manifestRE.MatchString(t) || javacRE.MatchString(t) || kotlinRE.MatchString(t) {
			break
		}
		msg = append(msg, t)
		next++
	}
	message := strings.Join(msg, "\n")
	rec := Record{
		Severity: Error, Source: SourceConsole, Tool: ToolManifestMerger,
		Line: atoi(m[2]), Column: atoi(m[3]),
		Message: message, head: messageHead(message),
	}
	rec.File, rec.External = normalize(root, m[1])
	return rec, next
}

// recordFromProblem turns a Problems API record into a merge candidate. Its message
// is the problem's detail text (the only actionable field; the display name is
// generic and the contextual label sometimes is too), whose first line shares the
// merge key with the console's message for the same error.
func recordFromProblem(root string, p Problem, src Source) Record {
	message := p.Details
	if message == "" {
		message = p.ContextualLabel
	}
	if message == "" {
		message = p.ProblemDisplayName
	}
	rec := Record{
		Severity:          severity(p.Severity),
		Source:            src,
		Tool:              problemTool(p),
		ProblemID:         p.ProblemID,
		Message:           message,
		Solutions:         p.Solutions,
		DocumentationLink: p.DocumentationLink,
		head:              messageHead(message),
	}
	if p.File != "" {
		rec.File, rec.External = normalize(root, p.File)
		rec.Line = p.Line
		rec.Column = p.Column
	}
	return rec
}

// problemTool attributes a problem to a tool. The sidecar's Problem shape carries no
// tool, so it is inferred from the labels; a caller that knows the failing task sets
// Problem.Tool instead. The Kotlin diagnostic ids are SCREAMING_CASE
// (UNRESOLVED_REFERENCE); javac's are compiler-err-* and its display name says
// "Java compilation error".
func problemTool(p Problem) Tool {
	if p.Tool != "" {
		return p.Tool
	}
	name := strings.ToLower(p.ProblemDisplayName)
	switch {
	case strings.Contains(name, "kotlin"):
		return ToolKotlin
	case strings.Contains(name, "java compilation"):
		return ToolJavac
	}
	switch {
	case strings.HasPrefix(p.ProblemID, "compiler-err"):
		return ToolJavac
	case isScreamingCase(p.ProblemID):
		return ToolKotlin
	}
	return ToolGradle
}

func isScreamingCase(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' {
			return false
		}
	}
	return true
}

// severity maps a record's severity word ("error"/"warning") or a console line's
// letter ("e"/"w") to a Severity. Anything else is an error, so a record is never
// silently dropped.
func severity(s string) Severity {
	if s == "warning" || s == "w" {
		return Warning
	}
	return Error
}

// messageHead is the merge key's message component: the message's first line with a
// javac-style location prefix stripped, whitespace collapsed and case folded, so the
// structured and console records of one error meet on the same key.
func messageHead(message string) string {
	line := message
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = locationPrefixRE.ReplaceAllString(line, "")
	line = strings.Join(strings.Fields(line), " ")
	line = strings.ToLower(line)
	const max = 120
	if len(line) > max {
		line = line[:max]
	}
	return line
}

// canonical resolves macOS /private on a path so a /private/var root and a /var
// path meet, and cleans the rest. It is deliberately unconditional: /private is a
// macOS alias for /, and resolving it on every platform keeps the golden tests
// host-independent.
func canonical(p string) string {
	if p == "" {
		return ""
	}
	c := path.Clean(p)
	if c == "/private" {
		return "/"
	}
	if strings.HasPrefix(c, "/private/") {
		return c[len("/private"):]
	}
	return c
}

// normalize reports the project-relative path of a file and whether it lies outside
// root. A file outside is reported as given and flagged external (gradle.md §Error
// model).
func normalize(root, file string) (string, bool) {
	r := canonical(root)
	f := canonical(file)
	if r != "" && r != "/" && (f == r || strings.HasPrefix(f, r+"/")) {
		return strings.TrimPrefix(strings.TrimPrefix(f, r), "/"), false
	}
	return file, true
}

func decodeURI(p string) string {
	if u, err := url.PathUnescape(p); err == nil {
		return u
	}
	return p
}

func isIndented(l string) bool {
	return len(l) > 0 && (l[0] == ' ' || l[0] == '\t')
}

func isCaret(t string) bool { return t == "^" }

func javacContinuation(t string) bool {
	for _, p := range []string{"symbol:", "location:", "note:", "required:", "found:", "reason:"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

func leadingIndent(l string) int {
	n := 0
	for n < len(l) && (l[n] == ' ' || l[n] == '\t') {
		n++
	}
	return n
}

func minIndent(lines []string) int {
	min := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := leadingIndent(l)
		if min < 0 || n < min {
			min = n
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

func stripIndent(l string, n int) string {
	i := 0
	for i < len(l) && i < n && (l[i] == ' ' || l[i] == '\t') {
		i++
	}
	return l[i:]
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
