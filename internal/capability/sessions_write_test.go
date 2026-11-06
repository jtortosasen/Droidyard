package capability

import (
	"strings"
	"testing"
	"time"
)

// TestHolderWho holds the words a refused write and a refused enter share (ADR-0094 §5): the
// holder is "another chat", never a session id, with its name and since when where known; the
// way out names a worktree of one's own and the user's release.
func TestHolderWho(t *testing.T) {
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	for h, want := range map[Holder]string{
		{Name: "agent-a", Since: at}: "another chat (agent-a, since 2026-10-18T09:00:00Z)",
		{Name: "agent-a"}:            "another chat (agent-a)",
		{Since: at}:                  "another chat (since 2026-10-18T09:00:00Z)",
		{}:                           "another chat",
	} {
		if got := h.Who(); got != want {
			t.Errorf("Who of %+v = %q; want %q", h, got, want)
		}
	}
	if w := OccupiedWayOut("/wt1"); !strings.Contains(w, "git worktree add") || !strings.Contains(w, "release /wt1") {
		t.Errorf("the way out is %q; want a worktree of one's own and the release", w)
	}
}
