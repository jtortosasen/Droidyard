package build

import (
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// Decision (ADR-0075 §5, ADR-0022): every published path is relative to the project
// root, the workspace's root, not to the Gradle root the build ran in. A Kotlin error in
// <top>/dev/app/... of a build run in <top>/dev reports dev/app/..., not external. Seen
// failing with the Gradle root as extract.Input.Root (the file came out
// app/src/main/kotlin/A.kt: a path that names no file of the project root).
func TestBuildErrorsRelativeToTheProjectRoot(t *testing.T) {
	// The fake's script names a file under the project root, so the root is made first.
	cfgFor := func(top string) gradletest.Config {
		line := "e: file://" + filepath.ToSlash(filepath.Join(top, "dev", "app", "src", "main", "kotlin", "A.kt")) + ":3:42 Unresolved reference: foo"
		return gradletest.Config{Build: &gradletest.Build{
			Steps: []gradletest.Step{
				{Kind: "output", Stream: "stderr", Lines: []string{line}},
				{Kind: "taskFinished", Path: ":app:compileDebugKotlin", Outcome: "failed", Reason: "Compilation error."},
			},
			Outcome: "failed",
		}}
	}
	top := t.TempDir()
	f := newFixtureTop(t, t.TempDir(), cfgFor(top), policy.BelowFloor{}, top, "dev")

	res := mustResult(t, f.wait(t, f.start(t, "build.run", `{"tasks":[":app:assembleDebug"]}`)))
	if len(res.Errors) != 1 {
		t.Fatalf("errors are %+v, want one merged Kotlin error", res.Errors)
	}
	const want = "dev/app/src/main/kotlin/A.kt"
	if e := res.Errors[0]; e.File != want || e.External {
		t.Errorf("error file is %q (external %v); want %q relative to the project root", e.File, e.External, want)
	}
	// The record is the Gradle root's: where the build ran.
	rec, err := f.history.newest(testWS)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Root != filepath.Join(top, "dev") {
		t.Errorf("record root is %q; want the Gradle root %q", rec.Root, filepath.Join(top, "dev"))
	}
}
