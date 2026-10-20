package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// Decision (m1g D12 and its step-2 change): over AGP 8.13's real layout (recorded in
// testdata/s1run2/aapt2-blame-values.json: two logs for one merged file, one naming the
// developer's file and one a library's for the same line), a reported merged path
// resolves to the candidate whose source line reads as the merged line, with 1-based
// line and column, and the merged line is quoted from intermediates/incremental/…/
// merged.dir. Found failing at level 4 (step 7): the outputFile's prefix, the
// merged-file tree, 0-based numbers, and a library file chosen from the other log.
func TestMergedPathResolvesOverTheRecordedLayout(t *testing.T) {
	blame, err := os.ReadFile("testdata/s1run2/aapt2-blame-values.json")
	if err != nil {
		t.Fatal(err)
	}
	buildDir := filepath.Join(t.TempDir(), "app", "build")
	in := filepath.Join(buildDir, "intermediates")
	bdir := filepath.Join(in, "merged_res_blame_folder", "debug", "mergeDebugResources", "out", "multi-v2")
	mdir := filepath.Join(in, "incremental", "debug", "mergeDebugResources", "merged.dir", "values")
	for _, d := range []string{bdir, mdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bdir, "values.json"), blame, 0o644); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 212)
	lines[209] = `    <string name="spike_alias">@string/this_string_does_not_exist</string>`
	if err := os.WriteFile(filepath.Join(mdir, "values.xml"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	// The developer's file the bare blame log names, and the library file the prefixed
	// one names for the same merged line, with the contents they had.
	root := t.TempDir()
	dev := filepath.Join(root, "spike_broken.xml")
	lib := filepath.Join(root, "ui-values.xml")
	if err := os.WriteFile(dev, []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<resources>\n    <string name=\"spike_alias\">@string/this_string_does_not_exist</string>\n</resources>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lib, []byte(strings.Repeat("    <string name=\"other\">x</string>\n", 300)), 0o644); err != nil {
		t.Fatal(err)
	}
	// The library's log first, so the content check, not the order, decides.
	var bf blameFile
	if err := json.Unmarshal(blame, &bf); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(bf.Logs)
	if blame, err = json.Marshal(bf); err != nil {
		t.Fatal(err)
	}
	blame = []byte(strings.NewReplacer("/w/dc/app/src/main/res/values/spike_broken.xml", dev,
		"/home/u/.gradle/caches/8.13/transforms/3e6911e2ddc0d6695a74b44253c34ac6/transformed/ui-release/res/values/values.xml", lib).Replace(string(blame)))
	if err := os.WriteFile(filepath.Join(bdir, "values.json"), blame, 0o644); err != nil {
		t.Fatal(err)
	}
	r := newMergedResolver(gradle.Model{Projects: []gradle.Module{{Path: ":app", BuildDir: buildDir}}})
	const reported = "com.example.ownerappa.app-mergeDebugResources-42:/values/values.xml"
	file, line, col, ok := r.ResolveBlame(reported, 210)
	if !ok || file != dev || line != 3 || col != 5 {
		t.Errorf("ResolveBlame = %q %d:%d ok=%v, want spike_broken.xml 3:5", file, line, col, ok)
	}
	text, ok := r.MergedLine(reported, 210)
	if !ok || !strings.Contains(text, "this_string_does_not_exist") {
		t.Errorf("MergedLine = %q ok=%v, want the offending <string>", text, ok)
	}
	if _, _, _, ok := r.ResolveBlame("com.example.other-mergeDebugResources-1:/values/values.xml", 210); ok {
		t.Error("another merge's blame id resolved")
	}
}
