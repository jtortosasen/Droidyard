package workspace

import (
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestDegradeOnlyTouchesAReadyWorkspace is the rule of plan m2c D21 at the registry: a
// workspace that is not open is not degraded by a crash that reports its root, one
// already degraded keeps the cause it has (a second crash is not a new cause, and a
// recovery from the crash must not clear another cause), and Recover clears only the
// code it is given.
func TestDegradeOnlyTouchesAReadyWorkspace(t *testing.T) {
	h := newWatchHarness(t, nil)
	root := project(t)
	crash := capability.Errorf(capability.ErrBuildSidecarCrashed, "the sidecar died")

	h.ws.Degrade(root, crash) // never opened
	if got := h.ws.State(root); got != "" {
		t.Fatalf("a crash reported for a root never opened left it %q", got)
	}

	h.open(t, root)
	h.ws.Degrade(root, crash)
	other := capability.Errorf(ErrStateNewer, "a newer file")
	h.ws.Degrade(root, other) // already degraded: keeps the first cause
	info, err := h.ws.Describe(h.open(t, root).ID)
	if err != nil || info.State != Degraded || info.DegradedBy.Code != capability.ErrBuildSidecarCrashed {
		t.Fatalf("after two reports the workspace is %+v (%v); want degraded by the first cause", info, err)
	}

	h.ws.Recover(ErrStateNewer) // another cause's recovery leaves it alone
	if got := h.ws.State(root); got != Degraded {
		t.Fatalf("Recover(WORKSPACE_STATE_NEWER) returned a crash-degraded workspace to %q", got)
	}
	h.ws.Recover(capability.ErrBuildSidecarCrashed)
	if info, _ := h.ws.Describe(info.ID); info.State != Ready || info.DegradedBy != nil {
		t.Fatalf("after Recover the workspace is %+v; want ready with no degradedBy", info)
	}

	h.closeWS(t, root)
	h.ws.Degrade(root, crash) // closed: untouched
	if got := h.ws.State(root); got != Closed {
		t.Fatalf("a crash reported for a closed workspace left it %q", got)
	}
}
