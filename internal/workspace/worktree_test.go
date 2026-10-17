package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// fakeWorktree lays out, by hand and without git, the files platform.LinkedWorktree
// reads: a main tree with a `.git` directory, and a linked worktree whose `.git` file
// and administrative directory name each other. It returns the canonical roots.
func fakeWorktree(t *testing.T, base, name string) (main, wt string) {
	t.Helper()
	main = filepath.Join(base, "main")
	wt = filepath.Join(base, name)
	admin := filepath.Join(main, ".git", "worktrees", name)
	for _, d := range []string{admin, wt} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(wt, ".git"):         "gitdir: " + admin + "\n",
		filepath.Join(admin, "commondir"): "../..\n",
		filepath.Join(admin, "gitdir"):    filepath.Join(wt, ".git") + "\n",
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return main, wt
}

func canonicalBase(t *testing.T) string {
	t.Helper()
	return platform.Current().Canonical(t.TempDir())
}

func indexEntryOf(t *testing.T, st interface{ Root() string }, root string) indexEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(st.Root(), indexFile))
	if err != nil {
		t.Fatal(err)
	}
	var idx index
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	for _, e := range idx.Workspaces {
		if e.Root == root {
			return e
		}
	}
	t.Fatalf("workspaces.json holds no entry for %s", root)
	return indexEntry{}
}

// TestMainRootIsResolvedAtEveryOpenAndDescribe is ADR-0082 §6: a worktree is a workspace
// of its project, so open and describe name the main tree, the state events carry it, and
// a plain project carries none. The root is resolved at each call, not kept from the
// first open: breaking the back-link between two opens of an open workspace makes the
// second say there is no main tree, in the result and in the index entry. Reversed (the
// value kept from the first open), the second open still names main.
func TestMainRootIsResolvedAtEveryOpenAndDescribe(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, nil)
	base := canonicalBase(t)
	main, wt := fakeWorktree(t, base, "wt")
	plain := filepath.Join(base, "plain")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	_, sub, _ := h.watch(t)

	if got := h.open(t, plain); got.MainRoot != "" {
		t.Errorf("a plain project's mainRoot = %q; want none", got.MainRoot)
	}
	got := h.open(t, wt)
	if got.MainRoot != main {
		t.Errorf("open of a worktree: mainRoot = %q; want %q", got.MainRoot, main)
	}
	d, err := h.ws.Describe(got.ID)
	if err != nil || d.MainRoot != main {
		t.Errorf("Describe after the open = (%q, %v); want the root the open resolved", d.MainRoot, err)
	}

	// The same events a panel merges by id: every transition of the worktree names main,
	// every one of the plain project names none.
	seen := map[string]int{}
	for _, e := range drain(sub) {
		s := stateOf(t, e)
		want := ""
		if s.Root == wt {
			want = main
		}
		if s.MainRoot != want {
			t.Errorf("workspace.state %s of %s: mainRoot %q; want %q", s.State, s.Root, s.MainRoot, want)
		}
		seen[s.Root]++
	}
	if seen[wt] < 2 || seen[plain] < 2 {
		t.Errorf("transitions seen per root = %v; want the opening and ready of both", seen)
	}

	if e := indexEntryOf(t, h.st, wt); e.MainRoot != main {
		t.Errorf("the index entry of the worktree holds mainRoot %q; want %q", e.MainRoot, main)
	}
	if e := indexEntryOf(t, h.st, plain); e.MainRoot != "" {
		t.Errorf("the index entry of a plain project holds mainRoot %q; want none", e.MainRoot)
	}

	// Break the back-link: the directory is no longer a worktree of main.
	if err := os.Remove(filepath.Join(main, ".git", "worktrees", "wt", "gitdir")); err != nil {
		t.Fatal(err)
	}
	// workspace.describe resolves it afresh, not from the open (seen failing with the
	// handler's re-resolution removed: the open's main tree came back).
	if out, err := h.ops.Invoke(context.Background(), capability.Request{Capability: CapDescribe, Workspace: wt}); err != nil || out.State != capability.StateCompleted {
		t.Fatalf("describe: %v %+v", err, out)
	} else if d := out.Result.(Info); d.MainRoot != "" {
		t.Errorf("workspace.describe after the back-link broke: mainRoot = %q; want none, resolved afresh", d.MainRoot)
	}
	if again := h.open(t, wt); again.MainRoot != "" {
		t.Errorf("an open after the back-link broke: mainRoot = %q; want none, resolved afresh", again.MainRoot)
	}
	if e := indexEntryOf(t, h.st, wt); e.MainRoot != "" {
		t.Errorf("the index entry after the second open holds mainRoot %q; want it rewritten without", e.MainRoot)
	}
}

// TestWatchListsAWorktreeWithItsMainRoot and TestWatchLeavesOutRemovedWorktrees are
// ADR-0082 §6: workspace.watch rows carry mainRoot from the index, and a worktree whose
// directory is gone neither shows nor takes one of the MaxListed places, while a plain
// project whose directory is gone stays listed, as it always did. Reversed (no existence
// test), the removed worktrees fill the list and push the plain projects out as
// truncated.
func TestWatchListsAWorktreeWithItsMainRoot(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, nil)
	base := canonicalBase(t)
	main, wt := fakeWorktree(t, base, "wt")
	id := h.open(t, wt).ID
	h.closeWS(t, wt)

	res, _, _ := h.watch(t)
	row, ok := find(res.Workspaces, id)
	if !ok || row.MainRoot != main || row.State != Closed {
		t.Errorf("the closed worktree's row = (%+v, %v); want it listed, closed, with mainRoot %q", row, ok, main)
	}

	// One still open whose directory is removed stays listed, so the panel can close it
	// (seen failing with the existence test applied to open entries too).
	_, open := fakeWorktree(t, base, "open")
	openID := h.open(t, open).ID
	if err := os.RemoveAll(open); err != nil {
		t.Fatal(err)
	}
	res, _, _ = h.watch(t)
	if row, ok := find(res.Workspaces, openID); !ok || row.State == Closed {
		t.Errorf("an open worktree whose directory is gone = (%+v, %v); want it listed, open", row, ok)
	}
}

func TestWatchLeavesOutRemovedWorktrees(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, nil)
	base := canonicalBase(t)

	// Two plain projects, opened first (the oldest), one of whose directories is then gone.
	gone := filepath.Join(base, "gone")
	kept := filepath.Join(base, "kept")
	for _, d := range []string{gone, kept} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	goneID, keptID := h.open(t, gone).ID, h.open(t, kept).ID
	h.closeWS(t, gone)
	h.closeWS(t, kept)

	// MaxListed worktrees, all newer, all removed: with the plain two they would be 52.
	var removed []string
	for i := 0; i < MaxListed; i++ {
		_, wt := fakeWorktree(t, base, fmt.Sprintf("wt%02d", i))
		removed = append(removed, h.open(t, wt).ID)
		h.closeWS(t, wt)
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	res, _, _ := h.watch(t)
	for _, id := range removed {
		if _, ok := find(res.Workspaces, id); ok {
			t.Errorf("a removed worktree (%s) is listed", id)
		}
	}
	if res.Truncated {
		t.Errorf("the list is truncated: removed worktrees took places")
	}
	for name, id := range map[string]string{"the plain project whose directory is gone": goneID, "the plain project": keptID} {
		if _, ok := find(res.Workspaces, id); !ok {
			t.Errorf("%s is not listed; want it, as before", name)
		}
	}
	if len(res.Workspaces) != 2 {
		t.Errorf("%d rows; want the two plain projects", len(res.Workspaces))
	}
}
