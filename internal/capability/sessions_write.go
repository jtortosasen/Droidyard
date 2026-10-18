package capability

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// The verdicts of a write into a worktree (ADR-0094 §5, ADR-0095 §3).
const (
	VerdictAllow  = "allow"
	VerdictWarn   = "warn"
	VerdictAsk    = "ask"
	VerdictRefuse = "refuse"
)

// Who is how another session is told of the holder: no session id (ADR-0048 §2). "Another
// chat" is what it says because every Claude Code chat is named claude-code (ADR-0094 §5).
func (h Holder) Who() string {
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

// OccupiedWayOut is what a session that met an occupied worktree is told to do: the same
// words for workspace.enter's refusal and for a refused write (ADR-0094 §5).
func OccupiedWayOut(root string) string {
	return fmt.Sprintf("make a worktree of your own (git worktree add) and workspace_enter it, or ask the user to release %s in Droidyard's sessions panel", root)
}

// WriteCheck is what Sessions.CheckWrite decided for one path.
type WriteCheck struct {
	// Verdict is one of the Verdict constants.
	Verdict string
	// Root is the linked worktree the path lies in when another session holds it; "" when
	// none does, or the caller is the holder's own.
	Root string
	// Holder describes that holder; zero when Root is "".
	Holder Holder
	// Idle says a warn is because the holder is idle; otherwise a warn is because the caller
	// cannot be told apart from the holder.
	Idle bool
}

// Writer is who is about to write, as the hook's connection tells the daemon (ADR-0095 §1).
type Writer struct {
	// Joined is the live MCP session the hook's chat was joined to; "" when it was joined to
	// none.
	Joined string
	// Chat is the chat id the connection named; "" when it named none.
	Chat string
	// Cursor says the hook is cursor-agent's: only a Claude Code session carries a chat id
	// (ADR-0088), so a holder that has one is not that Cursor chat.
	Cursor bool
}

// CheckWrite is the verdict of a write by w into path: canonical and absolute (ADR-0094 §5,
// ADR-0095 §3). The longest held root containing it, at a component boundary, decides, so a
// worktree nested in another's is matched before the outer one.
//
//   - The write is the holder's own when w is joined to the holder's session or carries the
//     holder's chat: allow, unreported.
//   - Under conflict allow every other write in a held worktree is allow with the holder
//     reported, idle or not, told apart or not.
//   - Else a holder that is idle is warn; and so is a writer that cannot be told for certain
//     from the holder (a chat that names no live MCP session: a Claude Code /clear can give
//     the hooks a new session id while the MCP connection keeps the old one; or a chat with no
//     droidyard MCP server).
//   - The writer is told apart for certain when (a) its chat is joined to a live MCP session
//     that is neither the holder nor of the holder's chat, or (b) its client is Cursor and the
//     holder has a chat id: the verdict is then the configured conflict, refuse or ask.
//
// A path in no held worktree is allow.
func (s *Sessions) CheckWrite(path string, w Writer) WriteCheck {
	conflict, _ := s.occupationNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	root := ""
	for r := range s.holders {
		if within(path, r) && len(r) > len(root) {
			root = r
		}
	}
	if root == "" {
		return WriteCheck{Verdict: VerdictAllow}
	}
	hid := s.holders[root]
	h := s.byID[hid]
	if h == nil {
		return WriteCheck{Verdict: VerdictAllow}
	}
	j := s.byID[w.Joined]
	chat := w.Chat
	if j != nil && chat == "" {
		chat = j.chat
	}
	if w.Joined == hid || (chat != "" && chat == h.chat) {
		return WriteCheck{Verdict: VerdictAllow}
	}
	c := WriteCheck{Root: root, Holder: Holder{Name: h.name, Since: h.holdSince, Stale: h.stale}}
	told := j != nil || (w.Cursor && h.chat != "")
	switch {
	case conflict == OccupationAllow:
		c.Verdict = VerdictAllow
	case h.stale:
		c.Verdict, c.Idle = VerdictWarn, true
	case !told:
		c.Verdict = VerdictWarn
	case conflict == OccupationAsk:
		c.Verdict = VerdictAsk
	default:
		c.Verdict = VerdictRefuse
	}
	return c
}

// within reports whether path is root or lies under it, at a component boundary: wt1-other
// is not under wt1.
func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
