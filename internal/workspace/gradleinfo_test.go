package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestOpenAndDescribeReportTheGradleRoot pins ADR-0075 §5 on real files: workspace.open
// and workspace.describe carry gradle {root?, candidates, error?}, root relative to the
// project root ("." for the project root itself), the error PROJECT_NO_GRADLE_ROOT when
// there is no root (a field, as ADR-0026 carries degradedBy), and the answer is resolved
// at every call, so a project file written after the open changes describe's answer with
// no reopen (ADR-0075 §2: nothing is cached).
func TestOpenAndDescribeReportTheGradleRoot(t *testing.T) {
	write := func(t *testing.T, root, rel, content string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// report opens root, then describes it, and requires both to say the same.
	report := func(t *testing.T, h *harness, root string) GradleInfo {
		t.Helper()
		opened := h.open(t, root)
		out := h.invoke(t, CapDescribe, root)
		if out.State != capability.StateCompleted {
			t.Fatalf("describe: %s %v", out.State, out.Error)
		}
		described := out.Result.(Described)
		if !reflect.DeepEqual(opened.Gradle, described.Gradle) {
			t.Fatalf("open reported %+v, describe %+v", opened.Gradle, described.Gradle)
		}
		return described.Gradle
	}

	t.Run("the build below the top", func(t *testing.T) {
		h := newHarness(t, 0)
		root := project(t)
		write(t, root, "dev/settings.gradle.kts", "")
		write(t, root, "README.md", "")
		g := report(t, h, root)
		if g.Root != "dev" || !reflect.DeepEqual(g.Candidates, []string{"dev"}) || g.Error != nil {
			t.Fatalf("gradle = %+v; want root dev, candidates [dev]", g)
		}
	})

	t.Run("the project root holds the settings file", func(t *testing.T) {
		h := newHarness(t, 0)
		root := project(t)
		write(t, root, "settings.gradle", "")
		write(t, root, "other/settings.gradle", "") // rule 1 wins; the scan does not run
		g := report(t, h, root)
		if g.Root != "." || !reflect.DeepEqual(g.Candidates, []string{"."}) || g.Error != nil {
			t.Fatalf("gradle = %+v; want root ., candidates [.]", g)
		}
	})

	t.Run("two builds and no choice", func(t *testing.T) {
		h := newHarness(t, 0)
		root := project(t)
		write(t, root, "a/settings.gradle", "")
		write(t, root, "b/settings.gradle.kts", "")
		g := report(t, h, root)
		if g.Root != "" || !reflect.DeepEqual(g.Candidates, []string{"a", "b"}) {
			t.Fatalf("gradle = %+v; want no root, candidates [a b]", g)
		}
		if g.Error == nil || g.Error.Code != capability.ErrProjectNoGradleRoot {
			t.Fatalf("error = %v; want PROJECT_NO_GRADLE_ROOT", g.Error)
		}
	})

	t.Run("none at all is an empty list on the wire, not null", func(t *testing.T) {
		h := newHarness(t, 0)
		root := project(t)
		raw, err := json.Marshal(report(t, h, root))
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if _, has := wire["root"]; has {
			t.Errorf("gradle encodes as %s; want root absent when there is none", raw)
		}
		if string(wire["candidates"]) != "[]" {
			t.Errorf("gradle encodes as %s; want candidates []", raw)
		}
		if _, has := wire["error"]; !has {
			t.Errorf("gradle encodes as %s; want the error present when there is no root", raw)
		}
	})

	t.Run("a project file written after the open decides at the next describe", func(t *testing.T) {
		h := newHarness(t, 0)
		root := project(t)
		write(t, root, "a/settings.gradle", "")
		write(t, root, "b/settings.gradle", "")
		if g := report(t, h, root); g.Root != "" {
			t.Fatalf("before the choice gradle = %+v; want no root", g)
		}
		write(t, root, ".droidyard/project.json", `{"schemaVersion":1,"gradleRoot":"b"}`)
		out := h.invoke(t, CapDescribe, root) // no reopen
		g := out.Result.(Described).Gradle
		if g.Root != "b" || g.Error != nil || !reflect.DeepEqual(g.Candidates, []string{"a", "b"}) {
			t.Fatalf("after the choice gradle = %+v; want root b, candidates [a b]", g)
		}
		write(t, root, ".droidyard/project.json", `{"schemaVersion":1,"gradleRoot":"gone"}`)
		g = h.invoke(t, CapDescribe, root).Result.(Described).Gradle
		if g.Root != "" || g.Error == nil || g.Error.Code != capability.ErrProjectNoGradleRoot {
			t.Fatalf("with a bad key gradle = %+v; want no root, PROJECT_NO_GRADLE_ROOT", g)
		}
	})
}

// TestRegistryDescribeResolvesNoGradleRoot pins ADR-0075 §5's boundary: Registry.Describe,
// which every RootOf and file/git root lookup calls, does no resolution; only the two
// handlers fill gradle.
func TestRegistryDescribeResolvesNoGradleRoot(t *testing.T) {
	h := newHarness(t, 0)
	root := project(t)
	if err := os.WriteFile(filepath.Join(root, "settings.gradle"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	id := h.open(t, root).ID
	info, err := h.ws.Describe(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info.Gradle, GradleInfo{}) {
		t.Fatalf("Registry.Describe resolved the Gradle root: %+v", info.Gradle)
	}
}
