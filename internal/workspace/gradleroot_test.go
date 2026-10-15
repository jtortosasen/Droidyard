package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// fixture builds files and symlinks under root. A file path ending in "/" is an empty
// directory; the token OUTSIDE in a content or a link target is the directory outside
// the project root, which holds settings.gradle and a project.json naming tools/x.
type fixture struct {
	files map[string]string
	links map[string]string
}

const (
	pf    = `{"schemaVersion":1,"gradleRoot":%q}`
	noKey = `{"schemaVersion":1}`
)

func keyFile(key string) string { return fmt.Sprintf(pf, key) }

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

// TestResolveGradleRoot pins ADR-0075 §2 and §3: the order of the rules, what the scan
// skips and where it stops descending, and that a gradleRoot key, which is repository
// content, selects a folder inside the canonical project root or nothing (never a
// fallback).
func TestResolveGradleRoot(t *testing.T) {
	devOnly := map[string]string{"dev/settings.gradle.kts": ""}
	two := map[string]string{"dev/settings.gradle": "", "tools/x/settings.gradle": ""}
	with := func(base map[string]string, extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
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
		{name: "key among two chooses; the scan still lists both",
			fx:  fixture{files: with(two, map[string]string{".droidyard/project.json": keyFile("tools/x")})},
			rel: "tools/x", cands: []string{"dev", "tools/x"}},
		{name: "key three deep is used and listed",
			fx: fixture{files: with(devOnly, map[string]string{
				"a/b/c/settings.gradle":   "",
				".droidyard/project.json": keyFile("a/b/c")})},
			rel: "a/b/c", cands: []string{"a/b/c", "dev"}},
		{name: "a bad key among two: none, both listed",
			fx:    fixture{files: with(two, map[string]string{".droidyard/project.json": keyFile("nope")})},
			cands: []string{"dev", "tools/x"}, cause: "gradleRoot"},
		{name: "key . on a root without settings is an error, not dev",
			fx:    fixture{files: with(devOnly, map[string]string{".droidyard/project.json": keyFile(".")})},
			cands: []string{"dev"}, cause: "holds no settings"},
		{name: "key ../sib is an error, not dev",
			fx:    fixture{files: with(devOnly, map[string]string{".droidyard/project.json": keyFile("../sib")})},
			cands: []string{"dev"}, cause: `".."`},
		{name: "an absolute key is an error, not dev",
			fx:    fixture{files: with(devOnly, map[string]string{".droidyard/project.json": keyFile("OUTSIDE")})},
			cands: []string{"dev"}, cause: "not a relative path"},
		{name: "dev/../.. is an error, not dev",
			fx:    fixture{files: with(devOnly, map[string]string{".droidyard/project.json": keyFile("dev/../..")})},
			cands: []string{"dev"}, cause: `".."`},
		{name: "an empty key is an error, not dev",
			fx:    fixture{files: with(devOnly, map[string]string{".droidyard/project.json": keyFile("")})},
			cands: []string{"dev"}, cause: "is empty"},
		{name: "a key through a symlink that leaves the root is an error",
			fx: fixture{
				files: with(devOnly, map[string]string{".droidyard/project.json": keyFile("inside")}),
				links: map[string]string{"inside": "../outside"}},
			cands: []string{"dev"}, cause: "outside the project root"},
		{name: "a key to a folder without settings is an error",
			fx:    fixture{files: with(devOnly, map[string]string{"docs/": "", ".droidyard/project.json": keyFile("docs")})},
			cands: []string{"dev"}, cause: "holds no settings"},
		{name: "a key to a file is an error",
			fx:    fixture{files: with(devOnly, map[string]string{"notes.txt": "", ".droidyard/project.json": keyFile("notes.txt")})},
			cands: []string{"dev"}, cause: "gradleRoot"},
		{name: "a key whose folder is missing is an error",
			fx:    fixture{files: with(devOnly, map[string]string{".droidyard/project.json": keyFile("gone")})},
			cands: []string{"dev"}, cause: "existing folder"},
		{name: "a key to a symlink that stays inside the root is used",
			fx: fixture{
				files: with(devOnly, map[string]string{".droidyard/project.json": keyFile("alias")}),
				links: map[string]string{"alias": "dev"}},
			rel: "dev", cands: []string{"dev"}},
		{name: "a non-string key is absent",
			fx:  fixture{files: with(devOnly, map[string]string{".droidyard/project.json": `{"schemaVersion":1,"gradleRoot":7}`})},
			rel: "dev", cands: []string{"dev"}},
		{name: "a null key is absent",
			fx:  fixture{files: with(devOnly, map[string]string{".droidyard/project.json": `{"schemaVersion":1,"gradleRoot":null}`})},
			rel: "dev", cands: []string{"dev"}},
		{name: "a newer schemaVersion is absent",
			fx:  fixture{files: with(devOnly, map[string]string{".droidyard/project.json": `{"schemaVersion":2,"gradleRoot":"nope"}`})},
			rel: "dev", cands: []string{"dev"}},
		{name: "an unparseable file is absent",
			fx:  fixture{files: with(devOnly, map[string]string{".droidyard/project.json": `{"schemaVersion":1,`})},
			rel: "dev", cands: []string{"dev"}},
		{name: "no key in a valid file is absent",
			fx:  fixture{files: with(devOnly, map[string]string{".droidyard/project.json": noKey})},
			rel: "dev", cands: []string{"dev"}},
		{name: "a file over 256 KiB is absent",
			fx: fixture{files: with(devOnly, map[string]string{
				".droidyard/project.json": `{"schemaVersion":1,"pad":"` + strings.Repeat("x", 300<<10) + `","gradleRoot":"nope"}`})},
			rel: "dev", cands: []string{"dev"}},
		{name: "a .droidyard that leaves the root is not read",
			fx: fixture{
				files: devOnly,
				links: map[string]string{".droidyard": "OUTSIDE"}},
			rel: "dev", cands: []string{"dev"}},
		{name: "a project.json that leaves the root is not read",
			fx: fixture{
				files: with(devOnly, map[string]string{".droidyard/": ""}),
				links: map[string]string{".droidyard/project.json": "OUTSIDE/project.json"}},
			rel: "dev", cands: []string{"dev"}},
		{name: "the home directory is never searched, and its key is not read",
			fx: fixture{files: map[string]string{
				"x/y/settings.gradle":     "",
				".droidyard/project.json": keyFile("x/y")}},
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

			got := ResolveGradleRoot(root, true)

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

// TestResolveGradleRootKeyResolvesUnderACanonicalisedTempDir pins that the containment
// check compares against the canonical project root. A temporary directory is under
// /var, /private/var canonically on macOS: a key that is valid there must resolve.
func TestResolveGradleRootKeyResolvesUnderACanonicalisedTempDir(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	build(t, root, filepath.Join(base, "outside"), fixture{files: map[string]string{
		"dev/settings.gradle":     "",
		"tools/x/settings.gradle": "",
		".droidyard/project.json": keyFile("tools/x"),
	}})
	got := ResolveGradleRoot(root, true)
	if got.Err != nil || got.Rel != "tools/x" {
		t.Fatalf("got %+v, want tools/x", got)
	}
	if want := filepath.Join(canonical(t, root), "tools", "x"); got.Abs != want {
		t.Errorf("abs = %q, want the canonical %q", got.Abs, want)
	}
}

// TestResolveGradleRootWithoutCandidates pins what the Gradle capabilities' hook relies
// on: a valid key answers without reading any directory of the scan, while a call that
// ends in an error still lists the candidates its details need.
func TestResolveGradleRootWithoutCandidates(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	build(t, root, filepath.Join(base, "outside"), fixture{files: map[string]string{
		"dev/settings.gradle":     "",
		"tools/x/settings.gradle": "",
		".droidyard/project.json": keyFile("tools/x"),
	}})
	examined := countExamined(t)

	got := ResolveGradleRoot(root, false)
	if got.Err != nil || got.Rel != "tools/x" {
		t.Fatalf("got %+v, want tools/x", got)
	}
	if *examined != 0 {
		t.Errorf("a valid key examined %d scan directories; the key's own check is all it needs", *examined)
	}

	// A key that fails is an error whose details list the candidates.
	if err := os.WriteFile(filepath.Join(root, ".droidyard", "project.json"), []byte(keyFile("nope")), 0o644); err != nil {
		t.Fatal(err)
	}
	got = ResolveGradleRoot(root, false)
	if got.Err == nil || !reflect.DeepEqual(got.Err.Details["candidates"], []string{"dev", "tools/x"}) {
		t.Errorf("got %+v, want an error listing both candidates", got)
	}

	// No key: the scan decides, whatever withCandidates says.
	if err := os.Remove(filepath.Join(root, ".droidyard", "project.json")); err != nil {
		t.Fatal(err)
	}
	if got = ResolveGradleRoot(root, false); got.Err == nil || len(got.Candidates) != 2 {
		t.Errorf("got %+v, want two candidates and no root", got)
	}
}

// TestResolveGradleRootScanBound pins the bound of rule (c): past maxScanDirs examined
// directories the scan stops, and without a key that is no root whatever it found (the
// build it did not reach might be a second one); a valid key still decides.
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
		got := ResolveGradleRoot(mk(t, nil), true)
		if got.Err == nil || !strings.Contains(got.Err.Cause, "stopped after examining 10") {
			t.Fatalf("got %+v, want an error naming the bound of 10", got)
		}
	})
	t.Run("one build among the first folders: still none", func(t *testing.T) {
		got := ResolveGradleRoot(mk(t, map[string]string{"d01/settings.gradle": ""}), true)
		if got.Rel != "" || got.Err == nil || !strings.Contains(got.Err.Cause, "stopped after examining") {
			t.Fatalf("got %+v, want none: the folders it did not reach may hold a second build", got)
		}
		if !reflect.DeepEqual(got.Candidates, []string{"d01"}) {
			t.Errorf("candidates = %v, want those found so far", got.Candidates)
		}
	})
	t.Run("a valid key still decides, with the candidates found so far", func(t *testing.T) {
		got := ResolveGradleRoot(mk(t, map[string]string{
			"d01/settings.gradle": "", "zz/settings.gradle": "",
			".droidyard/project.json": keyFile("zz")}), true)
		if got.Err != nil || got.Rel != "zz" {
			t.Fatalf("got %+v, want the key's root zz", got)
		}
		if !reflect.DeepEqual(got.Candidates, []string{"d01", "zz"}) {
			t.Errorf("candidates = %v, want d01 and the key's folder", got.Candidates)
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
	got := ResolveGradleRoot(root, true)
	if got.Err == nil || len(got.Candidates) != 50 || got.Candidates[0] != "m00" || got.Candidates[49] != "m49" {
		t.Fatalf("got %d candidates (%v), want the first 50, sorted, and no root", len(got.Candidates), got.Err)
	}
}

// TestResolveGradleRootKeyAtTheCap: a key whose folder sorts past the 50 listed takes the
// last place, so the folder in use is listed and the list stays at 50 (seen failing with
// withKey appending past the cap: 51).
func TestResolveGradleRootKeyAtTheCap(t *testing.T) {
	files := map[string]string{".droidyard/project.json": keyFile("zz")}
	for i := 0; i < 60; i++ {
		files[fmt.Sprintf("m%02d/settings.gradle", i)] = ""
	}
	files["zz/settings.gradle"] = ""
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	build(t, root, filepath.Join(base, "outside"), fixture{files: files})
	got := ResolveGradleRoot(root, true)
	if got.Err != nil || got.Rel != "zz" {
		t.Fatalf("root %q, error %v; want zz", got.Rel, got.Err)
	}
	if len(got.Candidates) != 50 || got.Candidates[49] != "zz" {
		t.Fatalf("got %d candidates, last %q; want 50 ending with zz", len(got.Candidates), got.Candidates[len(got.Candidates)-1])
	}
}

// TestResolveGradleRootProjectFileFIFO: a project file that is a FIFO is not read and does
// not block; it counts as absent (seen hanging with a blocking open).
func TestResolveGradleRootProjectFileFIFO(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	build(t, root, filepath.Join(base, "outside"), fixture{files: map[string]string{"dev/settings.gradle": "", ".droidyard/": ""}})
	if err := syscall.Mkfifo(filepath.Join(root, ".droidyard", "project.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan GradleRoot, 1)
	go func() { done <- ResolveGradleRoot(root, true) }()
	select {
	case got := <-done:
		if got.Err != nil || got.Rel != "dev" {
			t.Fatalf("root %q, error %v; want dev, the FIFO absent", got.Rel, got.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResolveGradleRoot blocked on a FIFO project file")
	}
}

// TestNoGradleRootError pins ADR-0075 §4: the error names the key and the UI in its
// nextStep, carries the candidates in its details, and a missing candidate list is an
// empty list, not null.
func TestNoGradleRootError(t *testing.T) {
	e := noGradleRoot("/p", "some cause", nil)
	if e.Code != capability.ErrProjectNoGradleRoot || e.Cause != "some cause" {
		t.Errorf("got %+v", e)
	}
	if !strings.Contains(e.NextStep, "gradleRoot") || !strings.Contains(e.NextStep, "UI") {
		t.Errorf("nextStep = %q, want it to name the UI and the gradleRoot key", e.NextStep)
	}
	if c, ok := e.Details["candidates"].([]string); !ok || c == nil || len(c) != 0 {
		t.Errorf("details.candidates = %#v, want an empty, non-nil list", e.Details["candidates"])
	}
}
