package capability

import (
	"testing"
	"time"
)

// TestHolderWho holds the words a refused device call is told (ADR-0094 §5): the holder is
// "another chat", never a session id, with its name and since when where known.
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
}
