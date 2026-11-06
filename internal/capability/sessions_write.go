package capability

import (
	"fmt"
	"time"
)

// Who is how another session is told of the holder: no session id (ADR-0048 §2). "Another
// chat" is what it says because every Claude Code chat is named claude-code (ADR-0094 §5).
func (h Holder) Who() string {
	if h.Peer {
		// A paired peer's link (ADR-0104), named by the host's configuration, not by the peer.
		if h.Since.IsZero() {
			return fmt.Sprintf("peer %s", h.Name)
		}
		return fmt.Sprintf("peer %s (since %s)", h.Name, h.Since.UTC().Format(time.RFC3339))
	}
	switch {
	case h.Name != "" && !h.Since.IsZero():
		return fmt.Sprintf("another chat (%s, since %s)", h.Name, h.Since.UTC().Format(time.RFC3339))
	case h.Name != "":
		return fmt.Sprintf("another chat (%s)", h.Name)
	case !h.Since.IsZero():
		return fmt.Sprintf("another chat (since %s)", h.Since.UTC().Format(time.RFC3339))
	}
	return "another chat"
}
