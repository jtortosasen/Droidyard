package capability

import (
	"strings"
	"testing"
	"time"
)

// TestCheckWriteVerdicts holds ADR-0094 §5 and ADR-0095 §3 for the verdict on a write into a
// held worktree, on the registry alone (the daemon's wiring is cmd/droidyard's
// TestCheckWrite):
//   - a write by the holder's session, or by a writer of the holder's chat, is allowed and
//     not reported;
//   - the writer is told apart for certain, and the verdict is the configured conflict, only
//     when its chat is joined to a live MCP session that is not the holder and not of the
//     holder's chat, or when it is Cursor's and the holder has a chat id (only Claude Code
//     sessions carry one);
//   - any other writer is warned, never refused: a chat that names no live MCP session (a
//     Claude Code /clear can give the hooks a new session id while the MCP connection keeps
//     the old one), a writer that names no chat, and an idle holder;
//   - a path in no held worktree is allowed, and a sibling whose name only starts like a held
//     root's is not in it (a component boundary, not a string prefix);
//   - the longest held root decides, so a worktree held inside another held one answers for
//     its own files.
//
// Seen failing: with the boundary test a string prefix, /wt1-other/x refused; with the
// longest-root rule the shortest, the nested worktree's file named the outer holder; with the
// old rule (a chat that differs from the holder's is enough), the chat-z row refused instead
// of warning.
func TestCheckWriteVerdicts(t *testing.T) {
	s, _, clk := occupied(OccupationRefuse)
	connectAt(s, "a", "chat-a", "claude-code")
	connectAt(s, "b", "chat-b", "claude-code")
	connectAt(s, "chatless", "", "cursor")
	connectAt(s, "a2", "chat-a", "claude-code")
	if _, refused := s.Occupy("a", "ws-wt1", "/wt1", true); refused {
		t.Fatal("a could not enter wt1")
	}

	type row struct {
		name     string
		path     string
		w        Writer
		verdict  string
		reported bool
		idle     bool
	}
	check := func(t *testing.T, rows []row) {
		t.Helper()
		for _, r := range rows {
			got := s.CheckWrite(r.path, r.w)
			if got.Verdict != r.verdict || (got.Root != "") != r.reported || got.Idle != r.idle {
				t.Errorf("%s: %+v; want %s, holder reported %v, idle %v", r.name, got, r.verdict, r.reported, r.idle)
			}
		}
	}
	joinedB := Writer{Joined: "b", Chat: "chat-b"}
	check(t, []row{
		{"joined to another session of another chat", "/wt1/x.kt", joinedB, "refuse", true, false},
		{"a chat that names no live session (a /clear, or no droidyard MCP)", "/wt1/x.kt", Writer{Chat: "chat-z"}, "warn", true, false},
		{"a Cursor write into a worktree a chat-bearing session holds", "/wt1/x.kt", Writer{Chat: "conv-c", Cursor: true}, "refuse", true, false},
		{"a Cursor write that names no conversation", "/wt1/x.kt", Writer{Cursor: true}, "refuse", true, false},
		{"joined to the holder", "/wt1/x.kt", Writer{Joined: "a", Chat: "chat-a"}, "allow", false, false},
		{"another live session of the holder's chat", "/wt1/x.kt", Writer{Joined: "a2", Chat: "chat-a"}, "allow", false, false},
		{"unjoined with the holder's chat", "/wt1/x.kt", Writer{Chat: "chat-a"}, "allow", false, false},
		{"the writer names no chat", "/wt1/x.kt", Writer{}, "warn", true, false},
		{"the holder's own root", "/wt1", joinedB, "refuse", true, false},
		{"a path in a free place", "/main/x.kt", joinedB, "allow", false, false},
		{"a sibling that starts like the root", "/wt1-other/x.kt", joinedB, "allow", false, false},
		{"a path that only contains the root", "/x/wt1/x.kt", joinedB, "allow", false, false},
	})

	// A holder with no chat id: a joined writer is another chat's, a Cursor one cannot be told.
	if _, refused := s.Occupy("chatless", "ws-wt2", "/wt2", true); refused {
		t.Fatal("chatless could not enter wt2")
	}
	check(t, []row{
		{"a chatless holder, a joined writer", "/wt2/x.kt", joinedB, "refuse", true, false},
		{"a chatless holder, a Cursor writer", "/wt2/x.kt", Writer{Chat: "conv-c", Cursor: true}, "warn", true, false},
		{"a chatless holder, a chat with no live session", "/wt2/x.kt", Writer{Chat: "chat-z"}, "warn", true, false},
	})

	// A worktree held inside another held one answers for its own files.
	if _, refused := s.Occupy("b", "ws-nested", "/wt1/.claude/worktrees/n", true); refused {
		t.Fatal("b could not enter the nested worktree")
	}
	check(t, []row{
		{"a file of the nested worktree, by the outer holder", "/wt1/.claude/worktrees/n/y.kt", Writer{Joined: "a", Chat: "chat-a"}, "refuse", true, false},
		{"a file of the nested worktree, by its holder", "/wt1/.claude/worktrees/n/y.kt", joinedB, "allow", false, false},
		{"a file beside it, by the nested holder", "/wt1/y.kt", joinedB, "refuse", true, false},
	})
	if got := s.CheckWrite("/wt1/.claude/worktrees/n/y.kt", Writer{Joined: "a", Chat: "chat-a"}); got.Root != "/wt1/.claude/worktrees/n" || got.Holder.Name != "claude-code" {
		t.Errorf("the nested file is decided by %q held by %+v; want the nested root", got.Root, got.Holder)
	}

	// Idle: the holder is warned about, whatever the chats say, until it is seen again.
	clk.advance(10 * time.Minute)
	s.Sweep()
	s.Seen("b") // the checker is live; a is idle
	check(t, []row{{"an idle holder", "/wt1/x.kt", joinedB, "warn", true, true}})
	s.Seen("a")
	check(t, []row{{"the holder is seen again", "/wt1/x.kt", joinedB, "refuse", true, false}})
}

// TestCheckWriteFollowsTheConfiguredConflict holds ADR-0094 §5: the verdict for a writer told
// apart for certain is the configured conflict, read at each decision: refuse refuses, ask
// asks. Under allow every conflicting write is allowed and still reports who holds the
// worktree, so the record of the write can say: told apart or not, idle or not, with a chat or
// not; there is no warn there. Seen failing: with the conflict ignored, the ask and allow rows
// refused; with warn kept under allow, the idle and unjoined rows warned.
func TestCheckWriteFollowsTheConfiguredConflict(t *testing.T) {
	conflict := OccupationRefuse
	s, _, clk := occupied(OccupationRefuse)
	s.UseOccupation(func() (string, time.Duration) { return conflict, time.Minute })
	connectAt(s, "a", "chat-a", "claude-code")
	connectAt(s, "b", "chat-b", "claude-code")
	s.Occupy("a", "ws-wt1", "/wt1", true)
	joined, unknown, none := Writer{Joined: "b", Chat: "chat-b"}, Writer{Chat: "chat-z"}, Writer{}
	for conf, want := range map[string][3]string{
		OccupationRefuse: {VerdictRefuse, VerdictWarn, VerdictWarn},
		OccupationAsk:    {VerdictAsk, VerdictWarn, VerdictWarn},
		OccupationAllow:  {VerdictAllow, VerdictAllow, VerdictAllow},
	} {
		conflict = conf
		for i, w := range []Writer{joined, unknown, none} {
			got := s.CheckWrite("/wt1/x.kt", w)
			if got.Verdict != want[i] || got.Root != "/wt1" || got.Holder.Since.IsZero() {
				t.Errorf("conflict %s, writer %+v: %+v; want %s with the holder reported", conf, w, got, want[i])
			}
		}
	}
	// An idle holder: warn under refuse and ask, allow under allow.
	clk.advance(10 * time.Minute)
	s.Sweep()
	for conf, want := range map[string]string{OccupationRefuse: VerdictWarn, OccupationAsk: VerdictWarn, OccupationAllow: VerdictAllow} {
		conflict = conf
		if got := s.CheckWrite("/wt1/x.kt", joined); got.Verdict != want || !got.Holder.Stale {
			t.Errorf("conflict %s, idle holder: %+v; want %s, the holder reported idle", conf, got, want)
		}
	}
}

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
