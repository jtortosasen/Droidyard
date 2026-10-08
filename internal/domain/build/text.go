package build

import (
	"strconv"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/extract"
)

// resultText is the rendering of build.run, build.assemble and build.lastResult
// (ADR-0036 Decision 2; mcp.md §Results). Empty and default fields are left out; the
// structured content has them all. One line per fact, the first word says what it is:
//
//	build <id> <outcome> <durationMs>ms tasks <task>... [module=<m> variant=<v>]
//	gradle <version> agp <version> kgp <version> [profile=<p>] [validation=<v>]
//	    [missing=<capability>,...] [expected kotlin=<s> ...]  (profile and validation
//	    only when not the usual full and untested)
//	tasks executed=<n> upToDate=<n> fromCache=<n> noSource=<n> skipped=<n> failed=<n>
//	    (only the non-zero counts)
//	failed <task> ["<reason>"]
//	errors=<n> warnings=<n> [extraction=<source>,... ] [unrecognised]  (non-zero only)
//	error|warning <tool> <file>:<line>:<column> "<message>"  with, indented, problem=<id>,
//	    resolved=<file>:<line>:<column>, excerpt "<line>", solution "<text>", doc "<link>"
//	output <module> <variant> [<applicationId> versionCode=<n> versionName="<v>"] <apk> <n>B
//	    [filters=<f>,...] [unsupported]  or  output <module> <variant> reason="<why>"
//	console <ref>
//
// Free text (a reason, a message, an excerpt) is quoted like device text is: a Gradle
// message can carry line breaks, and a rendering is lines.
func resultText(r Result) string {
	var t capability.Text
	t.W("build", r.BuildID, r.Outcome, strconv.FormatInt(r.DurationMs, 10)+"ms")
	for _, task := range r.Request.Tasks {
		t.W(task)
	}
	if r.Request.Module != "" {
		t.Pre("module=", r.Request.Module)
	}
	if r.Request.Variant != "" {
		t.Pre("variant=", r.Request.Variant)
	}
	t.Nl()

	t.W("gradle", r.Gradle.Version, "agp", r.AGP, "kgp", r.KGP)
	if r.Profile != "full" {
		t.W("profile=" + r.Profile)
	}
	if r.Validation != "untested" {
		t.W("validation=" + r.Validation)
	}
	if len(r.MissingCapabilities) > 0 {
		t.W("missing=" + joinWords(r.MissingCapabilities))
	}
	if r.Profile != "full" && r.ExpectedSources != nil {
		e := r.ExpectedSources
		t.W("expected", "kotlin="+e.Kotlin, "javac="+e.Javac, "aapt2="+e.AAPT2, "manifestMerger="+e.ManifestMerger, "ksp="+e.KSP, "kapt="+e.Kapt)
	}
	t.Nl()

	ts := r.TaskSummary
	counts := []struct {
		name string
		n    int
	}{{"executed", ts.Executed}, {"upToDate", ts.UpToDate}, {"fromCache", ts.FromCache}, {"noSource", ts.NoSource}, {"skipped", ts.Skipped}, {"failed", ts.Failed}}
	for _, c := range counts {
		if c.n != 0 {
			if !t.Started() {
				t.W("tasks")
			}
			t.W(c.name + "=" + strconv.Itoa(c.n))
		}
	}
	t.Nl()
	for _, f := range r.FailedTasks {
		t.W("failed", f.Path)
		if f.Reason != "" {
			t.Q(f.Reason)
		}
		t.Nl()
	}

	if r.ErrorCount != 0 {
		t.W("errors=" + strconv.Itoa(r.ErrorCount))
	}
	if r.WarningCount != 0 {
		t.W("warnings=" + strconv.Itoa(r.WarningCount))
	}
	if len(r.Extraction.Sources) > 0 {
		t.W("extraction=" + joinWords(r.Extraction.Sources))
	}
	if r.Extraction.Unrecognised {
		t.W("unrecognised")
	}
	t.Nl()
	for _, e := range r.Errors {
		writeRecord(&t, e)
	}
	for _, w := range r.Warnings {
		writeRecord(&t, w)
	}

	for _, o := range r.Outputs {
		t.W("output", o.Module, o.Variant)
		if o.ApplicationID != "" {
			t.W(o.ApplicationID)
		}
		if o.VersionCode != nil {
			t.W("versionCode=" + strconv.Itoa(*o.VersionCode))
		}
		if o.VersionName != "" {
			t.Pre("versionName=", o.VersionName)
		}
		if o.Reason != "" {
			t.Pre("reason=", o.Reason)
		}
		t.Nl()
		for _, a := range o.APKs {
			t.Indent(1).W(a.Name, strconv.FormatInt(a.SizeBytes, 10)+"B")
			if len(a.Filters) > 0 {
				t.W("filters=" + joinWords(a.Filters))
			}
			if !a.Supported {
				t.W("unsupported")
			}
			t.Nl()
		}
	}
	t.W("console", r.ConsoleRef).Nl()
	return t.String()
}

// joinWords joins items with commas for a key=a,b,c word; the writer quotes the whole
// word if any item is not safe.
func joinWords(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func where(file string, line, column int) string {
	if file == "" {
		return ""
	}
	if line == 0 {
		return file
	}
	w := file + ":" + strconv.Itoa(line)
	if column != 0 {
		w += ":" + strconv.Itoa(column)
	}
	return w
}

func writeRecord(t *capability.Text, r extract.Record) {
	t.W(string(r.Severity), string(r.Tool))
	if w := where(r.File, r.Line, r.Column); w != "" {
		t.W(w)
	}
	if r.External {
		t.W("external")
	}
	if r.Merged {
		t.W("merged")
	}
	t.Q(r.Message).Nl()
	if r.ProblemID != "" {
		t.Indent(1).Pre("problem=", r.ProblemID).Nl()
	}
	if r.Resolved != nil {
		t.Indent(1).Pre("resolved=", where(r.Resolved.File, r.Resolved.Line, r.Resolved.Column)).Nl()
	}
	if r.Excerpt != "" {
		t.Indent(1).W("excerpt").Q(r.Excerpt).Nl()
	}
	for _, s := range r.Solutions {
		t.Indent(1).W("solution").Q(s).Nl()
	}
	if r.DocumentationLink != "" {
		t.Indent(1).W("doc").Q(r.DocumentationLink).Nl()
	}
}
