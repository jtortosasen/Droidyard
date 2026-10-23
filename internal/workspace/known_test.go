package workspace

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Decision (ADR-0127 §2): session.chats reads the chats of the projects, never of a
// worktree as a project of its own. Known gives the main tree of each indexed entry (a
// worktree entry gives its MainRoot), each once, newest first, and at most fifty, so the
// reader's candidates are the panel's projects. Reversed (a worktree's own root listed, or
// duplicates kept, or no cut) the list differs from the one asserted.
func TestKnownIsTheMainTreesOfTheIndex(t *testing.T) {
	h := newWatchHarness(t, nil)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// The worktree is closed, so listed() keeps it only while its directory exists.
	wt := t.TempDir()
	idx := index{Workspaces: map[string]indexEntry{
		"a": {Root: "/p/a", Name: "a", LastOpenedAt: base.Add(1 * time.Hour)},
		"b": {Root: "/p/b", Name: "b", LastOpenedAt: base.Add(2 * time.Hour)},
		// A worktree of a, opened last: it gives a, and a is not listed twice.
		"a-wt": {Root: wt, Name: "x", MainRoot: "/p/a", LastOpenedAt: base.Add(3 * time.Hour)},
	}}
	if err := h.st.Write(indexFile, idx); err != nil {
		t.Fatal(err)
	}
	if got, want := h.ws.Known(), []string{"/p/a", "/p/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Known = %v; want %v", got, want)
	}
}

// Decision (ADR-0127 §2): at most fifty projects are known to the reader, the panel's own
// bound (the list Known is made from is cut at MaxListed, newest first).
func TestKnownIsBounded(t *testing.T) {
	h := newWatchHarness(t, nil)
	idx := index{Workspaces: map[string]indexEntry{}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("p%02d", i)
		idx.Workspaces[id] = indexEntry{Root: "/projects/" + id, Name: id, LastOpenedAt: base.Add(time.Duration(i) * time.Hour)}
	}
	if err := h.st.Write(indexFile, idx); err != nil {
		t.Fatal(err)
	}
	got := h.ws.Known()
	if len(got) != MaxListed || got[0] != "/projects/p59" {
		t.Errorf("Known has %d, first %q; want %d, newest first /projects/p59", len(got), got[0], MaxListed)
	}
}
