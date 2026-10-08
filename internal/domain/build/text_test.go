package build

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/build/extract"
)

var updateText = flag.Bool("update-text", false, "rewrite the build rendering golden files under testdata/m1o-step5")

const m1o5 = "testdata/m1o-step5/"

// TestBuildRenderingIsTheGoldenText: the two recorded builds of the m1m golden-path run
// (RECORDED.txt there), one that succeeded and one that failed on a Kotlin error, render
// to the committed text, and the text is at least half the size of the compact JSON it
// replaces (plan m1o step 5, A5). The failed build is the one an agent reads closely:
// the error is in the text with its file, line and column.
func TestBuildRenderingIsTheGoldenText(t *testing.T) {
	for _, f := range []string{"dy1-84-build-ok", "dy1-158-build-failed"} {
		raw, err := os.ReadFile(m1o5 + f + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var r Result
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		got := resultText(r)
		path := m1o5 + f + ".txt"
		if *updateText {
			os.WriteFile(path, []byte(got+"\n"), 0o644)
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run with -update-text to create it)", err)
		}
		if got+"\n" != string(want) {
			t.Errorf("%s: rendering differs from %s:\n--- got ---\n%s\n--- want ---\n%s", f, path, got, want)
		}
		compact, _ := json.Marshal(r)
		t.Logf("%s: %d -> %d bytes", f, len(compact), len(got))
		if len(got)*2 > len(compact) {
			t.Errorf("%s: the rendering is %d bytes against %d of JSON: less than 50%% smaller", f, len(got), len(compact))
		}
	}
}

// TestBuildRenderingKeepsWhatChangesTheNextStep: a build the recordings do not have: a
// degraded profile, missing Tooling API capabilities, an unrecognised failure, an output
// that is not installable, and an error with every optional part. Default fields (the full
// profile, untested validation, zero counts) are left out; a message with a line break is
// one quoted line.
func TestBuildRenderingKeepsWhatChangesTheNextStep(t *testing.T) {
	vc := 3
	r := Result{
		BuildID: "op-1", Request: Request{Tasks: []string{":app:assembleDebug"}, Module: ":app", Variant: "debug"},
		Outcome: "failed", DurationMs: 1200,
		Gradle:  GradleInfo{Version: "8.5", Source: "wrapper"},
		Profile: "degraded", Validation: "untested", AGP: "8.1.0", KGP: "unknown",
		MissingCapabilities: []string{"problemEvents"},
		ExpectedSources:     &ExpectedSources{Kotlin: "console", Javac: "console", AAPT2: "console", ManifestMerger: "console", KSP: "console", Kapt: "console"},
		TaskSummary:         TaskSummary{Executed: 4, Failed: 1},
		FailedTasks:         []FailedTask{{Path: ":app:processDebugManifest", Reason: "line one\nline two"}},
		ErrorCount:          7, WarningCount: 0,
		Errors: []extract.Record{{Severity: "error", Tool: "manifest-merger", Source: "console", File: "app/src/main/AndroidManifest.xml", Line: 15, Column: 5, Message: "Attribute missing\nsecond line",
			ProblemID: "p1", Solutions: []string{"add the attribute"}, DocumentationLink: "https://example.test/doc", Excerpt: "<activity/>",
			Resolved: &extract.Location{File: "app/src/main/AndroidManifest.xml", Line: 15}}},
		Outputs:    []Output{{Module: ":app", Variant: "debug", ApplicationID: "dev.app", VersionCode: &vc, VersionName: "1.0 beta", APKs: []OutputAPK{{Name: "app-x86.apk", Filters: []string{"x86"}, SizeBytes: 10, Supported: false}}}, {Module: ":lib", Variant: "debug", Reason: "no metadata"}},
		Extraction: Extraction{Sources: []string{"console"}, Unrecognised: true},
		ConsoleRef: "droidyard://artifact/d.x.op-1",
	}
	got := resultText(r)
	for _, want := range []string{
		"build op-1 failed 1200ms :app:assembleDebug module=\":app\" variant=\"debug\"",
		"gradle 8.5 agp 8.1.0 kgp unknown profile=degraded missing=problemEvents expected kotlin=console",
		"tasks executed=4 failed=1",
		`failed :app:processDebugManifest "line one\nline two"`,
		"errors=7 extraction=console unrecognised",
		`error manifest-merger app/src/main/AndroidManifest.xml:15:5 "Attribute missing\nsecond line"`,
		" problem=\"p1\"", " resolved=\"app/src/main/AndroidManifest.xml:15\"", ` excerpt "<activity/>"`, ` solution "add the attribute"`, ` doc "https://example.test/doc"`,
		`output :app debug dev.app versionCode=3 versionName="1.0 beta"`,
		" app-x86.apk 10B filters=x86 unsupported",
		`output :lib debug reason="no metadata"`,
		"console droidyard://artifact/d.x.op-1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendering lacks %q:\n%s", want, got)
		}
	}
	for _, absent := range []string{"validation=", "warnings=", "startedAt", "profile=full"} {
		if strings.Contains(got, absent) {
			t.Errorf("the rendering holds the default %q:\n%s", absent, got)
		}
	}
	if strings.Count(got, "\n") != strings.Count(strings.TrimSpace(got), "\n") {
		t.Errorf("trailing blank lines in the rendering")
	}
}
