package workspace

import (
	"reflect"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestUseGradleRootFollowsTheCurrentRoot is ADR-0092 §1, §3: a workspace remembers one
// Gradle root, the latest its calls resolved. After A then B a crash of A leaves it
// ready and a crash of B degrades it; a resolution that gives no root forgets B, and
// each switch returns the roots forgotten that no other open workspace remembers (§2),
// so the caller closes exactly those. A workspace that is closing or closed refuses and
// records nothing. Seen failing with the set kept growing: the crash of A degraded the
// workspace.
func TestUseGradleRootFollowsTheCurrentRoot(t *testing.T) {
	h := newWatchHarness(t, nil)
	const ga, gb, gshared = "/gradle/a", "/gradle/b", "/gradle/shared"
	crash := capability.Errorf(capability.ErrBuildSidecarCrashed, "the sidecar died")
	use := func(id, abs string) []string {
		t.Helper()
		forgotten, err := h.ws.UseGradleRoot(id, abs)
		if err != nil {
			t.Fatalf("UseGradleRoot(%s) = %v", abs, err)
		}
		return forgotten
	}

	root := project(t)
	id := h.open(t, root).ID
	if got := use(id, ga); got != nil {
		t.Errorf("the first root forgot %v; want nothing", got)
	}
	if got := use(id, ga); got != nil {
		t.Errorf("resolving the current root again forgot %v; want nothing", got)
	}
	if got := use(id, gb); !reflect.DeepEqual(got, []string{ga}) {
		t.Errorf("switching A to B forgot %v; want [%s]", got, ga)
	}
	if got := rootsOf(h.ws, id); !reflect.DeepEqual(got, []string{gb}) {
		t.Errorf("the workspace remembers %v; want only the current root [%s]", got, gb)
	}
	h.ws.Degrade(ga, crash)
	if got := h.ws.State(root); got != Ready {
		t.Fatalf("a crash of the forgotten root A left the workspace %q; want ready", got)
	}
	h.ws.Degrade(gb, crash)
	if got := h.ws.State(root); got != Degraded {
		t.Fatalf("a crash of the current root B left the workspace %q; want degraded", got)
	}

	// No root: the previous one is forgotten and returned; nothing is remembered.
	if got := use(id, ""); !reflect.DeepEqual(got, []string{gb}) {
		t.Errorf("a resolution with no root forgot %v; want [%s]", got, gb)
	}
	if got := rootsOf(h.ws, id); len(got) != 0 {
		t.Errorf("the workspace remembers %v after a resolution with no root; want none", got)
	}
	if got := use(id, ""); got != nil {
		t.Errorf("no root after no root forgot %v; want nothing", got)
	}

	// A root another open workspace remembers is forgotten but not returned.
	other := project(t)
	idOther := h.open(t, other).ID
	use(idOther, gshared)
	use(id, gshared)
	if got := use(id, ga); got != nil {
		t.Errorf("switching away from a root another open workspace remembers returned %v; want nothing", got)
	}
	if got := use(idOther, gb); !reflect.DeepEqual(got, []string{gshared}) {
		t.Errorf("the last workspace switching away from the shared root returned %v; want [%s]", got, gshared)
	}

	// A closed workspace refuses and records nothing.
	h.closeWS(t, root)
	_, err := h.ws.UseGradleRoot(id, "/gradle/late")
	if e, ok := err.(*capability.Error); !ok || e.Code != capability.ErrWorkspaceNotOpen {
		t.Errorf("UseGradleRoot on a closed workspace = %v; want WORKSPACE_NOT_OPEN", err)
	}
	if got := rootsOf(h.ws, id); len(got) != 0 {
		t.Errorf("a closed workspace holds %v; want none", got)
	}
}

// TestUseGradleRootRefusesAClosingWorkspace is the same refusal while the workspace is
// closing (its drain still running): the call is refused and nothing is recorded, so a
// connection it would open is not left behind unrecorded (ADR-0075 §8, ADR-0092 §1).
func TestUseGradleRootRefusesAClosingWorkspace(t *testing.T) {
	h := newHarness(t, 300*time.Millisecond)
	a := project(t)
	idA := h.open(t, a).ID
	if _, err := h.ws.UseGradleRoot(idA, "/gradle/own"); err != nil {
		t.Fatal(err)
	}
	h.start(t, "fixture.ignores", a)
	done := h.run(CapClose, a)
	waitState(t, h.ws, a, Closing)

	forgotten, err := h.ws.UseGradleRoot(idA, "/gradle/late")
	if e, ok := err.(*capability.Error); !ok || e.Code != capability.ErrWorkspaceNotOpen {
		t.Errorf("UseGradleRoot on a closing workspace = %v; want WORKSPACE_NOT_OPEN", err)
	}
	if forgotten != nil {
		t.Errorf("a refused call returned %v; want nothing", forgotten)
	}
	if got := rootsOf(h.ws, idA); len(got) != 0 {
		t.Errorf("the closing workspace remembers %v after a refused call; want nothing", got)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the close did not end")
	}
}

// TestGradleRootInUse is what a close that waited for a root's operations asks before
// closing it (ADR-0092 §2): whether an open workspace has it as its current root now. It
// is true for the current root of any open workspace, false for a forgotten root and for
// the root of a workspace that is closed. Seen failing with the answer taken from a set
// that keeps forgotten roots.
func TestGradleRootInUse(t *testing.T) {
	h := newWatchHarness(t, nil)
	const ga, gb = "/gradle/a", "/gradle/b"
	a, b := project(t), project(t)
	idA, idB := h.open(t, a).ID, h.open(t, b).ID
	if h.ws.GradleRootInUse(ga) {
		t.Fatal("a root nobody resolved is in use")
	}
	h.ws.UseGradleRoot(idA, ga)
	h.ws.UseGradleRoot(idB, ga)
	if !h.ws.GradleRootInUse(ga) {
		t.Error("the current root of two open workspaces is not in use")
	}
	h.ws.UseGradleRoot(idA, gb)
	if !h.ws.GradleRootInUse(ga) {
		t.Error("a root B still has as its current root is not in use after A left it")
	}
	h.ws.UseGradleRoot(idB, "")
	if h.ws.GradleRootInUse(ga) {
		t.Error("a root every workspace has left is in use")
	}
	h.closeWS(t, a)
	if h.ws.GradleRootInUse(gb) {
		t.Error("the root of a closed workspace is in use")
	}
}
