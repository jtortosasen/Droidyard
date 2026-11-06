package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// TestResolveProject is plan m3j Q2: what a project names. The nearest ancestor holding a
// settings file, itself included; else the folder itself when exactly one build lies one
// or two levels below; the home directory and `/` only by a settings file of their own; an
// absolute, existing, readable directory; every spelling of one folder the one canonical
// root. A refusal says which of REQUEST_INVALID_PARAMS and PROJECT_NO_GRADLE_ROOT it is
// and what to pass.
func TestResolveProject(t *testing.T) {
	env := platform.Current()
	touch := func(t *testing.T, root string, rels ...string) {
		t.Helper()
		for _, rel := range rels {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if strings.HasSuffix(rel, "/") {
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	type want struct {
		root       string // relative to the temp root, "." for itself; "" when refused
		code       capability.ErrorCode
		candidates []string
	}
	cases := []struct {
		name  string
		files []string
		arg   string // relative to the temp root; "" is the root; "!abs" prefixes use as is
		want  want
	}{
		{"the folder holds the settings file", []string{"settings.gradle"}, "", want{root: "."}},
		{"a Kotlin settings file", []string{"settings.gradle.kts"}, "", want{root: "."}},
		{"a folder inside the project", []string{"settings.gradle.kts", "app/src/main/"}, "app/src/main", want{root: "."}},
		{"the nearest ancestor wins", []string{"settings.gradle", "included/settings.gradle", "included/x/"}, "included/x", want{root: "included"}},
		{"one build below the folder", []string{"README.md", "dev/settings.gradle.kts"}, "", want{root: "."}},
		{"one build two levels below", []string{"a/b/settings.gradle"}, "", want{root: "."}},
		{"two builds below and none above", []string{"a/settings.gradle", "b/settings.gradle.kts"}, "", want{code: capability.ErrProjectNoGradleRoot, candidates: []string{"a", "b"}}},
		{"no build at all", []string{"README.md"}, "", want{code: capability.ErrProjectNoGradleRoot, candidates: []string{}}},
		{"a three-level build is not searched for", []string{"a/b/c/settings.gradle"}, "", want{code: capability.ErrProjectNoGradleRoot, candidates: []string{}}},
		{"the project file is not consulted", []string{"a/settings.gradle", "b/settings.gradle", ".droidyard/project.json"}, "", want{code: capability.ErrProjectNoGradleRoot, candidates: []string{"a", "b"}}},
		{"a relative path", nil, "!relative/app", want{code: capability.ErrInvalidParams}},
		{"a home-relative path", nil, "!~/app", want{code: capability.ErrInvalidParams}},
		{"an empty path", nil, "!", want{code: capability.ErrInvalidParams}},
		{"a path that does not exist", []string{"settings.gradle"}, "missing", want{code: capability.ErrInvalidParams}},
		{"a file", []string{"settings.gradle"}, "settings.gradle", want{code: capability.ErrInvalidParams}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := env.Canonical(t.TempDir())
			touch(t, root, c.files...)
			arg := filepath.Join(root, c.arg)
			if strings.HasPrefix(c.arg, "!") {
				arg = strings.TrimPrefix(c.arg, "!")
			}
			got, err := ResolveProject(env, arg)
			if c.want.code != "" {
				// The literal words an agent is told: seen failing with the old text, which
				// sent it to the UI and the project file ResolveProject does not read.
				wantStep := "pass the absolute path of the project's folder, or of a folder inside it"
				if c.want.code == capability.ErrProjectNoGradleRoot {
					wantStep = "pass the absolute path of one of the candidates (details.candidates, relative to the project) as the project"
					if len(c.want.candidates) == 0 {
						wantStep = "pass the absolute path of a folder that holds settings.gradle or settings.gradle.kts, or of a folder inside one, as the project"
					}
				}
				if err == nil || err.Code != c.want.code || err.NextStep != wantStep {
					t.Fatalf("ResolveProject(%q) = %q, %v; want %s with nextStep %q", arg, got, err, c.want.code, wantStep)
				}
				if c.want.candidates != nil {
					if cand, _ := err.Details["candidates"].([]string); !reflect.DeepEqual(cand, c.want.candidates) {
						t.Errorf("candidates = %v; want %v", err.Details["candidates"], c.want.candidates)
					}
				}
				return
			}
			if want := filepath.Join(root, c.want.root); err != nil || got != want {
				t.Fatalf("ResolveProject(%q) = %q, %v; want %q", arg, got, err, want)
			}
		})
	}

	t.Run("every spelling of one folder is one root", func(t *testing.T) {
		root := env.Canonical(t.TempDir())
		touch(t, root, "settings.gradle", "app/")
		link := filepath.Join(env.Canonical(t.TempDir()), "link")
		if err := os.Symlink(filepath.Join(root, "app"), link); err != nil {
			t.Fatal(err)
		}
		for _, arg := range []string{root, root + "/", filepath.Join(root, "app"), filepath.Join(root, "app", "..", "app"), link} {
			if got, err := ResolveProject(env, arg); err != nil || got != root {
				t.Errorf("ResolveProject(%q) = %q, %v; want %q", arg, got, err, root)
			}
		}
	})

	t.Run("the home directory is a project only by a settings file of its own", func(t *testing.T) {
		home := env.Canonical(t.TempDir())
		t.Setenv("HOME", home)
		touch(t, home, "only/settings.gradle")
		if got, err := ResolveProject(env, home); err == nil || err.Code != capability.ErrProjectNoGradleRoot {
			t.Fatalf("home with one build below it = %q, %v; want PROJECT_NO_GRADLE_ROOT: the folders below it are not searched", got, err)
		}
		touch(t, home, "settings.gradle")
		if got, err := ResolveProject(env, home); err != nil || got != home {
			t.Errorf("home with a settings file = %q, %v; want itself", got, err)
		}
	})
}
