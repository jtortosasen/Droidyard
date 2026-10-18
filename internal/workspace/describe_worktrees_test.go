package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// gitHere runs the host git in dir with the user's and the system's configuration out
// of the way.
func gitHere(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// repoWithCommit makes a repository with one commit at base/main and returns main.
func repoWithCommit(t *testing.T, base string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o700); err != nil {
		t.Fatal(err)
	}
	gitHere(t, main, "init", "-q")
	if err := os.WriteFile(filepath.Join(main, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitHere(t, main, "add", "f")
	gitHere(t, main, "commit", "-q", "-m", "one")
	return main
}

// describe calls workspace.describe on root and returns the typed result and its wire
// form.
func (h *watchHarness) describe(t *testing.T, root string) (Described, string) {
	t.Helper()
	out, err := h.ops.Invoke(context.Background(), capability.Request{Capability: CapDescribe, Workspace: root})
	if err != nil || out.State != capability.StateCompleted {
		t.Fatalf("describe %s: %v %+v", root, err, out)
	}
	wire, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	return out.Result.(Described), string(wire)
}

// TestDescribeListsWorktrees is ADR-0089: workspace.describe lists the project's linked
// worktrees with their branches, the same from the main tree and from one of them, read
// from the repository's own files and kept only while each names the repository back; the
// field is absent when there is nothing to list, capped at 50, and no other call or
// event carries it.
func TestDescribeListsWorktrees(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, nil)
	base := canonicalBase(t)
	main := repoWithCommit(t, base)
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	gitHere(t, main, "worktree", "add", "-q", "-b", "task/a", a)
	gitHere(t, main, "worktree", "add", "-q", "--detach", b)
	// A plain project and a repository with no linked worktree, opened too.
	plain := filepath.Join(base, "plain")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	alone := filepath.Join(base, "alone")
	if err := os.MkdirAll(alone, 0o700); err != nil {
		t.Fatal(err)
	}
	gitHere(t, alone, "init", "-q")

	_, sub, _ := h.watch(t)
	for _, root := range []string{main, a, b, plain, alone} {
		h.open(t, root)
	}

	// Both worktrees, the branch of the first and none for the detached one; the same
	// list from the main tree and from a worktree, which appears in it.
	want := []WorktreeInfo{{Root: a, Branch: "task/a"}, {Root: b}}
	for _, from := range []string{main, a, b} {
		got, wire := h.describe(t, from)
		if !reflect.DeepEqual(got.Worktrees, want) {
			t.Errorf("describe of %s lists %+v; want %+v", from, got.Worktrees, want)
		}
		// On the wire the workspace object's fields stay at the top level beside it.
		if !strings.Contains(wire, `"worktrees":[`) || !strings.Contains(wire, `"gradle":`) || !strings.Contains(wire, `"id":`) {
			t.Errorf("describe of %s wrote %s; want worktrees beside the workspace object's own fields", from, wire)
		}
		if strings.Contains(wire, `"branch":""`) {
			t.Errorf("describe of %s wrote an empty branch (%s); a detached worktree has none", from, wire)
		}
	}

	// Nothing to list: the field is absent from the wire, not an empty list.
	for name, root := range map[string]string{"a plain project": plain, "a repository with no linked worktree": alone} {
		got, wire := h.describe(t, root)
		if len(got.Worktrees) != 0 || strings.Contains(wire, `"worktrees"`) {
			t.Errorf("describe of %s: worktrees %+v in %s; want the field absent", name, got.Worktrees, wire)
		}
	}

	// Never on open, nor on a state event (the panel's streams). The open of a worktree
	// of a project that has two is where a leak would show.
	if raw, err := json.Marshal(h.open(t, a)); err != nil || strings.Contains(string(raw), "worktrees") {
		t.Errorf("workspace.open of a worktree carries worktrees: %s (%v)", raw, err)
	}
	events := drain(sub)
	if len(events) == 0 {
		t.Fatal("no workspace.state event was published")
	}
	for _, e := range events {
		if strings.Contains(string(e.Payload), "worktrees") {
			t.Errorf("a workspace.state event carries worktrees: %s", e.Payload)
		}
	}
	res, _, _ := h.watch(t)
	if raw, _ := json.Marshal(res); strings.Contains(string(raw), "worktrees") {
		t.Errorf("workspace.watch carries worktrees: %s", raw)
	}

	// Three kinds of stale entry are not listed: a worktree directory removed by hand
	// (b), an entry whose back-link names another path (d), and one that lost its
	// commondir (c). The decision is that the list keeps an entry only while it passes
	// platform.LinkedWorktree. c is the case only that call rejects: its gitdir and the
	// worktree's .git still name each other (seen failing with the call removed: c stays).
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	c := filepath.Join(base, "c")
	d := filepath.Join(base, "d")
	gitHere(t, main, "worktree", "add", "-q", "-b", "task/c", c)
	gitHere(t, main, "worktree", "add", "-q", "-b", "task/d", d)
	if got, _ := h.describe(t, main); len(got.Worktrees) != 3 || got.Worktrees[1].Root != c || got.Worktrees[2].Root != d {
		t.Fatalf("with a removed directory and two new worktrees: %+v; want a, c and d", got.Worktrees)
	}
	if err := os.Remove(filepath.Join(main, ".git", "worktrees", "c", "commondir")); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(base, "elsewhere", ".git") + "\n"
	if err := os.WriteFile(filepath.Join(main, ".git", "worktrees", "d", "gitdir"), []byte(elsewhere), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.describe(t, main); !reflect.DeepEqual(got.Worktrees, []WorktreeInfo{{Root: a, Branch: "task/a"}}) {
		t.Errorf("with a removed directory, a missing commondir and a broken back-link: %+v; want only a", got.Worktrees)
	}
}

// TestDescribeBoundsTheWorktreeList is ADR-0089 §1: at most 50 worktrees, however many
// the repository holds. Reversed (no cap) the 51st is listed.
func TestDescribeBoundsTheWorktreeList(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, nil)
	base := canonicalBase(t)
	var main string
	for i := 0; i < platform.MaxWorktrees+1; i++ {
		main, _ = fakeWorktree(t, base, fmt.Sprintf("wt%02d", i))
	}
	h.open(t, main)
	got, _ := h.describe(t, main)
	if len(got.Worktrees) != platform.MaxWorktrees {
		t.Errorf("%d worktrees listed of %d; want %d", len(got.Worktrees), platform.MaxWorktrees+1, platform.MaxWorktrees)
	}
}
