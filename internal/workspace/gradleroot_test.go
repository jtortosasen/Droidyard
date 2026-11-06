package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// fixture builds files and symlinks under root. A file path ending in "/" is an empty
// directory; the token OUTSIDE in a content or a link target is the directory outside
// the project root, which holds settings.gradle and a project.json naming tools/x.
type fixture struct {
	files map[string]string
	links map[string]string
}

func build(t *testing.T, root, outside string, fx fixture) {
	t.Helper()
	sub := func(s string) string { return strings.ReplaceAll(s, "OUTSIDE", outside) }
	mk := func(dir string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk(root)
	mk(outside)
	for name, content := range map[string]string{
		"settings.gradle": "",
		"project.json":    `{"schemaVersion":1,"gradleRoot":"tools/x"}`,
	} {
		if err := os.WriteFile(filepath.Join(outside, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for p, content := range fx.files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if strings.HasSuffix(p, "/") {
			mk(full)
			continue
		}
		mk(filepath.Dir(full))
		if err := os.WriteFile(full, []byte(sub(content)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range fx.links {
		full := filepath.Join(root, filepath.FromSlash(link))
		mk(filepath.Dir(full))
		if err := os.Symlink(sub(target), full); err != nil {
			t.Fatal(err)
		}
	}
}

// countExamined counts the directories rule (c) examines until the test ends.
func countExamined(t *testing.T) *int {
	t.Helper()
	n := new(int)
	examineHook = func(string) { *n++ }
	t.Cleanup(func() { examineHook = nil })
	return n
}

// canonical resolves symlinks of the temporary directory: t.TempDir() is under /var on
// macOS, which is /private/var canonically.
func canonical(t *testing.T, p string) string {
	t.Helper()
	c, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestResolveGradleRoot pins ADR-0075 §2 and §3 as ADR-0137 left them: the order of the
// rules (the project root, then the one folder below it), what the scan skips and where it
// stops descending, and that the project file's gradleRoot key, which was repository content,
// is not consulted any more: it chooses nothing and is not read.
func TestResolveGradleRoot(t *testing.T) {
	two := map[string]string{"dev/settings.gradle": "", "tools/x/settings.gradle": ""}
	withFile := func(content string) map[string]string {
		out := map[string]string{".droidyard/project.json": content}
		for k, v := range two {
			out[k] = v
		}
		return out
	}

	cases := []struct {
		name   string
		fx     fixture
		home   bool   // the project root is HOME
		rel    string // the Gradle root; "" when none
		cands  []string
		cause  string // a substring of the cause when there is none
		noScan bool   // rule (c) must not examine a directory
	}{
		{name: "rule 1: the project root, no scan even with a build below",
			fx:  fixture{files: map[string]string{"settings.gradle": "", "dev/settings.gradle": ""}},
			rel: ".", cands: []string{"."}, noScan: true},
		{name: "the owner's shape: a top with loose files and one build in dev, build-logic not a second",
			fx: fixture{files: map[string]string{
				".cursor/rules": "", "README.md": "",
				"dev/settings.gradle.kts":             "",
				"dev/build-logic/settings.gradle.kts": "",
				"dev/app/build.gradle.kts":            "",
			}},
			rel: "dev", cands: []string{"dev"}},
		{name: "two levels down",
			fx:  fixture{files: map[string]string{"a/b/settings.gradle": ""}},
			rel: "a/b", cands: []string{"a/b"}},
		{name: "three levels down is not searched",
			fx:    fixture{files: map[string]string{"a/b/c/settings.gradle": ""}},
			cands: []string{}, cause: "no settings.gradle"},
		{name: "two builds: none, both listed",
			fx:    fixture{files: two},
			cands: []string{"dev", "tools/x"}, cause: "several"},
		{name: "nothing",
			fx:    fixture{files: map[string]string{"README.md": "", "src/": ""}},
			cands: []string{}, cause: "no settings.gradle"},
		{name: "hidden, build and node_modules are skipped",
			fx: fixture{files: map[string]string{
				".hidden/settings.gradle":        "",
				"build/x/settings.gradle":        "",
				"node_modules/p/settings.gradle": "",
				"build/settings.gradle":          "",
			}},
			cands: []string{}, cause: "no settings.gradle"},
		{name: "a settings file that is a directory is not one",
			fx:    fixture{files: map[string]string{"dev/settings.gradle/": ""}},
			cands: []string{}, cause: "no settings.gradle"},
		{name: "a symlink to a folder outside holding settings is not entered",
			fx: fixture{links: map[string]string{
				"link":   "OUTSIDE",
				"a/link": "OUTSIDE",
			}, files: map[string]string{"a/real/": ""}},
			cands: []string{}, cause: "no settings.gradle"},
		// Rule (a) left with ADR-0137: the file chooses nothing, valid or not, however it is
		// shaped, and a file that is a link out of the root is not followed.
		{name: "a valid gradleRoot key among two chooses nothing",
			fx:    fixture{files: withFile(`{"schemaVersion":1,"gradleRoot":"tools/x"}`)},
			cands: []string{"dev", "tools/x"}, cause: "several"},
		{name: "a key that names a folder with no build chooses nothing either",
			fx:    fixture{files: withFile(`{"schemaVersion":1,"gradleRoot":"nope"}`)},
			cands: []string{"dev", "tools/x"}, cause: "several"},
		{name: "with one build below, the file does not move it",
			fx:  fixture{files: map[string]string{"dev/settings.gradle": "", "other/": "", ".droidyard/project.json": `{"schemaVersion":1,"gradleRoot":"other"}`}},
			rel: "dev", cands: []string{"dev"}},
		{name: "the home directory is never searched",
			fx:   fixture{files: map[string]string{"x/y/settings.gradle": ""}},
			home: true, cands: []string{}, cause: "not searched", noScan: true},
		{name: "a project at the home directory keeps rule (b)",
			fx:   fixture{files: map[string]string{"settings.gradle": "", "dev/settings.gradle": ""}},
			home: true, rel: ".", cands: []string{"."}, noScan: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "proj")
			outside := filepath.Join(base, "outside")
			build(t, root, outside, tc.fx)
			t.Setenv("HOME", filepath.Join(base, "home"))
			if tc.home {
				// The temporary directory is not canonical on macOS (/var is
				// /private/var): HOME is compared as the project root is.
				t.Setenv("HOME", root)
			}
			examined := countExamined(t)

			got := ResolveGradleRoot(root)

			if tc.noScan && *examined != 0 {
				t.Errorf("the scan examined %d directories; it must not run", *examined)
			}
			if !reflect.DeepEqual(got.Candidates, tc.cands) {
				t.Errorf("candidates = %v, want %v", got.Candidates, tc.cands)
			}
			if tc.rel == "" {
				if got.Abs != "" || got.Rel != "" {
					t.Fatalf("root = %q (%q), want none", got.Abs, got.Rel)
				}
				if got.Err == nil || got.Err.Code != capability.ErrProjectNoGradleRoot {
					t.Fatalf("error = %v, want PROJECT_NO_GRADLE_ROOT", got.Err)
				}
				if !strings.Contains(got.Err.Cause, tc.cause) {
					t.Errorf("cause = %q, want it to contain %q", got.Err.Cause, tc.cause)
				}
				if !reflect.DeepEqual(got.Err.Details["candidates"], tc.cands) {
					t.Errorf("details.candidates = %v, want %v", got.Err.Details["candidates"], tc.cands)
				}
				return
			}
			if got.Err != nil {
				t.Fatalf("error = %v, want root %q", got.Err, tc.rel)
			}
			if got.Rel != tc.rel {
				t.Errorf("rel = %q, want %q", got.Rel, tc.rel)
			}
			// Abs is canonical: it must equal the canonical project root joined with the
			// relative path, whichever spelling of the root the caller used.
			want := canonical(t, root)
			if tc.rel != "." {
				want = filepath.Join(want, filepath.FromSlash(tc.rel))
			}
			if got.Abs != want {
				t.Errorf("abs = %q, want %q", got.Abs, want)
			}
		})
	}
}

// TestResolveGradleRootScanBound pins the bound of rule (c): past maxScanDirs examined
// directories the scan stops, and that is no root whatever it found (the build it did not
// reach might be a second one).
func TestResolveGradleRootScanBound(t *testing.T) {
	old := maxScanDirs
	maxScanDirs = 10
	t.Cleanup(func() { maxScanDirs = old })

	empties := map[string]string{}
	for i := 0; i < 12; i++ {
		empties[fmt.Sprintf("d%02d/", i)] = ""
	}
	mk := func(t *testing.T, extra map[string]string) string {
		files := map[string]string{}
		for k, v := range empties {
			files[k] = v
		}
		for k, v := range extra {
			files[k] = v
		}
		base := t.TempDir()
		root := filepath.Join(base, "proj")
		build(t, root, filepath.Join(base, "outside"), fixture{files: files})
		t.Setenv("HOME", filepath.Join(base, "home"))
		return root
	}

	t.Run("empty folders past the bound: none, and the cause names the bound", func(t *testing.T) {
		got := ResolveGradleRoot(mk(t, nil))
		if got.Err == nil || !strings.Contains(got.Err.Cause, "stopped after examining 10") {
			t.Fatalf("got %+v, want an error naming the bound of 10", got)
		}
	})
	t.Run("one build among the first folders: still none", func(t *testing.T) {
		got := ResolveGradleRoot(mk(t, map[string]string{"d01/settings.gradle": ""}))
		if got.Rel != "" || got.Err == nil || !strings.Contains(got.Err.Cause, "stopped after examining") {
			t.Fatalf("got %+v, want none: the folders it did not reach may hold a second build", got)
		}
		if !reflect.DeepEqual(got.Candidates, []string{"d01"}) {
			t.Errorf("candidates = %v, want those found so far", got.Candidates)
		}
	})
}

// TestResolveGradleRootCapsCandidates pins the 50-folder cap on what is listed.
func TestResolveGradleRootCapsCandidates(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 60; i++ {
		files[fmt.Sprintf("m%02d/settings.gradle", i)] = ""
	}
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	build(t, root, filepath.Join(base, "outside"), fixture{files: files})
	got := ResolveGradleRoot(root)
	if got.Err == nil || len(got.Candidates) != 50 || got.Candidates[0] != "m00" || got.Candidates[49] != "m49" {
		t.Fatalf("got %d candidates (%v), want the first 50, sorted, and no root", len(got.Candidates), got.Err)
	}
}

// TestNoGradleRootError pins ADR-0075 §4 as ADR-0137 §3 changed it: the nextStep says to
// pass the absolute path of a candidate as the project (and, with none, of a folder with a
// build), carries the candidates in its details, and a missing candidate list is an empty
// list, not null. Seen failing with the old text (the UI and the gradleRoot key).
func TestNoGradleRootError(t *testing.T) {
	e := noGradleRoot("/p", "some cause", nil)
	if e.Code != capability.ErrProjectNoGradleRoot || e.Cause != "some cause" {
		t.Errorf("got %+v", e)
	}
	if want := "pass the absolute path of a folder that holds settings.gradle or settings.gradle.kts, or of a folder inside one, as the project"; e.NextStep != want {
		t.Errorf("nextStep with no candidates = %q, want %q", e.NextStep, want)
	}
	if want := "pass the absolute path of one of the candidates (details.candidates, relative to the project) as the project"; noGradleRoot("/p", "c", []string{"a", "b"}).NextStep != want {
		t.Errorf("nextStep with candidates = %q, want %q", noGradleRoot("/p", "c", []string{"a", "b"}).NextStep, want)
	}
	if c, ok := e.Details["candidates"].([]string); !ok || c == nil || len(c) != 0 {
		t.Errorf("details.candidates = %#v, want an empty, non-nil list", e.Details["candidates"])
	}
}
