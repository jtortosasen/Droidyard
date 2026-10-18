package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/agent"
	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// checkAs is agent.checkWrite over a CLI connection whose hello names chat, as the hook's
// does (ADR-0095 §1): the daemon joins it to the live MCP session of that chat, if any.
func (w *occupationWorld) checkAs(t *testing.T, chat string, paths ...string) agent.CheckResult {
	t.Helper()
	return w.checkClient(t, "claude", chat, paths...)
}

// checkClient is checkAs for the hook of client (claude or cursor).
func (w *occupationWorld) checkClient(t *testing.T, client, chat string, paths ...string) agent.CheckResult {
	t.Helper()
	params, _ := json.Marshal(agent.CheckParams{Client: client, Paths: paths})
	conn, r, _, err := daemon.Dial(sockFor(w.state), daemon.Hello{Protocol: daemon.Protocol, Version: system.Version,
		Client: capability.ClientCLI, Chat: chat, Root: w.main})
	if err != nil {
		t.Fatalf("dialling as chat %q: %v", chat, err)
	}
	defer conn.Close()
	env, err := runCall(conn, r, cli.Call{Capability: "agent.checkWrite", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("agent.checkWrite %v as chat %q: %+v", paths, chat, env.Error)
	}
	var res agent.CheckResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

// want asserts the verdict of paths for chat, and whether the holder is reported.
func (w *occupationWorld) want(t *testing.T, what, chat, verdict string, reported bool, paths ...string) agent.CheckResult {
	t.Helper()
	res := w.checkAs(t, chat, paths...)
	if res.Verdict != verdict || (res.OccupiedBy != nil) != reported {
		t.Errorf("%s: chat %q writing %v: %+v; want %s, holder reported %v", what, chat, paths, res, verdict, reported)
	}
	return res
}

// TestCheckWrite is ADR-0094 §5 and ADR-0095 §3 end to end: a real `serve`, real linked
// worktrees, scripted MCP shims, and the hook's own kind of connection (a CLI hello naming
// the writer's chat). Agent A, of chat-x, holds wt1.
//
//   - a caller joined to B (chat-b) writing in wt1 is refused, the message naming A, since
//     when and the way out; joined to A (chat-x), or only naming chat-x, it is allowed and
//     the holder is not reported; a chat with no live session (chat-z: a /clear, or no
//     droidyard MCP) is warned, never refused; a Cursor write is refused, since a Cursor chat
//     is never the holder's; a second live session of chat-x is allowed;
//   - a path in the main tree, in a free worktree, in /tmp, and in wt1-other beside wt1 is
//     allowed (the last is a component boundary, not a string prefix);
//   - a path in a folder wt1 does not have yet is wt1's, and so is one spelt through a
//     symlink, and in a worktree held under a /tmp spelling, written as /private/tmp;
//   - the longest held root decides: a worktree nested in wt1 answers for its own files, and
//     one nested in the main tree is held like any other;
//   - the configured conflict: ask asks, allow allows and still reports the holder (a chat
//     with no live session included), the caller naming no chat is warned, and so is a
//     caller that cannot be told from a holder with no chat id;
//   - an idle holder is a warn, until it is seen again.
//
// Seen failing: with the holder test removed (every path allowed) the first row did not
// refuse; with a string-prefix test, wt1-other refused; with the shortest held root in place
// of the longest, the file nested in wt1 was decided by wt1's holder; with the paths not
// canonicalised, the symlinked path and the /tmp spelling were allowed. (The main tree is
// never held, ADR-0094 §1, so a worktree nested in it cannot be shadowed by it: that row holds
// by construction, and the nested-in-wt1 pair is the one that can fail.)
func TestCheckWrite(t *testing.T) {
	t.Parallel()
	w := newOccupationWorld(t)
	a := w.agent(t, "agent-a", "chat-x", w.main)
	w.agent(t, "agent-b", "chat-b", w.main)
	if c := w.enter(t, a, 10, w.wt1); c.isError {
		t.Fatalf("a enters wt1: %s", c.raw)
	}
	w.occupies(t, "agent-a", w.wt1)
	base := filepath.Dir(w.main)
	other := w.wt1 + "-other"
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link-to-wt1")
	if err := os.Symlink(w.wt1, link); err != nil {
		t.Fatal(err)
	}

	// Who is told apart from the holder.
	res := w.want(t, "joined to another chat's session", "chat-b", capability.VerdictRefuse, true, filepath.Join(w.wt1, "x.kt"))
	if res.OccupiedBy.Name != "agent-a" || res.OccupiedBy.Since.IsZero() || !strings.Contains(res.Message, "another chat (agent-a, since ") ||
		!strings.Contains(res.Message, "git worktree add") || !strings.Contains(res.Message, "release "+w.wt1) {
		t.Errorf("the refusal is %+v; want the holder, since when and the way out", res)
	}
	for _, secret := range []string{"chat-x", "chat-b"} {
		if b, _ := json.Marshal(res); strings.Contains(string(b), secret) {
			t.Errorf("the verdict carries a chat id: %s", b)
		}
	}
	// A chat that names no live MCP session may be the holder's own after a /clear: warned.
	res = w.want(t, "naming a chat with no live session", "chat-z", capability.VerdictWarn, true, filepath.Join(w.wt1, "x.kt"))
	if !strings.Contains(res.Message, "cannot tell") {
		t.Errorf("the warning is %q; want it to say the caller cannot be told from the holder", res.Message)
	}
	// Cursor's chats are never the holder's: only a Claude Code session carries a chat id.
	if res := w.checkClient(t, "cursor", "conv-c", filepath.Join(w.wt1, "x.kt")); res.Verdict != capability.VerdictRefuse || res.OccupiedBy == nil {
		t.Errorf("a Cursor write into a worktree a Claude Code chat holds: %+v; want refuse, the holder reported", res)
	}
	w.want(t, "joined to the holder", "chat-x", capability.VerdictAllow, false, filepath.Join(w.wt1, "x.kt"))
	x2 := w.agent(t, "x-two", "chat-x", w.main)
	w.want(t, "a second live session of the holder's chat", "chat-x", capability.VerdictAllow, false, filepath.Join(w.wt1, "x.kt"))
	w.kill(t, x2)
	w.want(t, "naming no chat", "", capability.VerdictWarn, true, filepath.Join(w.wt1, "x.kt"))

	// Where a path lies.
	for what, path := range map[string]string{
		"the main tree":                filepath.Join(w.main, "x.kt"),
		"a free worktree":              filepath.Join(w.wt2, "x.kt"),
		"/tmp":                         "/tmp/droidyard-check-write-x.kt",
		"a sibling that starts like a": filepath.Join(other, "x.kt"),
	} {
		w.want(t, what, "chat-b", capability.VerdictAllow, false, path)
	}
	w.want(t, "wt1 itself", "chat-b", capability.VerdictRefuse, true, w.wt1)
	w.want(t, "a folder wt1 does not have yet", "chat-b", capability.VerdictRefuse, true, filepath.Join(w.wt1, "new", "dir", "x.kt"))
	w.want(t, "a path through a symlink", "chat-b", capability.VerdictRefuse, true, filepath.Join(link, "new", "dir", "x.kt"))
	w.want(t, "the worst of several paths", "chat-b", capability.VerdictRefuse, true, filepath.Join(w.main, "x.kt"), filepath.Join(w.wt1, "x.kt"), filepath.Join(w.wt2, "x.kt"))

	// A worktree held under a /tmp spelling answers to the /private/tmp one (macOS).
	if real, err := filepath.EvalSymlinks("/tmp"); err == nil && real != "/tmp" {
		dir, err := os.MkdirTemp("/tmp", "dy-check-write-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		spelt := filepath.Join(dir, "wt3")
		gitIn(t, w.main, "worktree", "add", "-q", spelt, "-b", "task3")
		w.agent(t, "agent-t", "chat-t", spelt)
		w.occupies(t, "agent-t", spelt)
		for what, path := range map[string]string{
			"the /tmp spelling":         filepath.Join(spelt, "x.kt"),
			"the /private/tmp spelling": filepath.Join(real, strings.TrimPrefix(spelt, "/tmp"), "x.kt"),
		} {
			w.want(t, "a worktree held as /tmp: "+what, "chat-b", capability.VerdictRefuse, true, path)
		}
	}

	// The longest held root decides.
	inner := filepath.Join(w.wt1, ".claude", "worktrees", "inner")
	gitIn(t, w.wt1, "worktree", "add", "-q", inner, "-b", "inner")
	w.agent(t, "agent-i", "chat-i", inner)
	w.occupies(t, "agent-i", inner)
	if res := w.want(t, "a file of a worktree nested in wt1, written by wt1's holder", "chat-x", capability.VerdictRefuse, true, filepath.Join(inner, "y.kt")); res.OccupiedBy != nil && res.OccupiedBy.Name != "agent-i" {
		t.Errorf("the nested file names %+v; want its own holder agent-i", res.OccupiedBy)
	}
	w.want(t, "a file of the nested worktree, written by its holder", "chat-i", capability.VerdictAllow, false, filepath.Join(inner, "y.kt"))
	nested := filepath.Join(w.main, ".claude", "worktrees", "n")
	gitIn(t, w.main, "worktree", "add", "-q", nested, "-b", "nested")
	w.agent(t, "agent-n", "chat-n", nested)
	w.occupies(t, "agent-n", nested)
	w.want(t, "a worktree held inside the main tree", "chat-b", capability.VerdictRefuse, true, filepath.Join(nested, "z.kt"))
	w.want(t, "the main tree beside it", "chat-b", capability.VerdictAllow, false, filepath.Join(w.main, "z.kt"))

	// A holder with no chat id: a writer joined to another chat's session is still another's, a
	// Cursor writer or a chat with no live session cannot be told from it.
	chatless := w.agent(t, "chatless", "", w.main)
	if c := w.enter(t, chatless, 20, w.wt2); c.isError {
		t.Fatalf("chatless enters wt2: %s", c.raw)
	}
	w.occupies(t, "chatless", w.wt2)
	w.want(t, "a chatless holder, a joined writer", "chat-b", capability.VerdictRefuse, true, filepath.Join(w.wt2, "x.kt"))
	w.want(t, "a chatless holder, a chat with no live session", "chat-z", capability.VerdictWarn, true, filepath.Join(w.wt2, "x.kt"))
	if res := w.checkClient(t, "cursor", "conv-c", filepath.Join(w.wt2, "x.kt")); res.Verdict != capability.VerdictWarn {
		t.Errorf("a Cursor write into a chatless holder's worktree: %+v; want warn", res)
	}

	// The configured conflict.
	w.configure(t, `{"conflict":"ask"}`)
	w.want(t, "conflict ask", "chat-b", capability.VerdictAsk, true, filepath.Join(w.wt1, "x.kt"))
	w.configure(t, `{"conflict":"allow"}`)
	w.want(t, "conflict allow", "chat-b", capability.VerdictAllow, true, filepath.Join(w.wt1, "x.kt"))
	w.want(t, "conflict allow, a chat with no live session", "chat-z", capability.VerdictAllow, true, filepath.Join(w.wt1, "x.kt"))
	w.configure(t, "")
	w.want(t, "conflict back to the default", "chat-b", capability.VerdictRefuse, true, filepath.Join(w.wt1, "x.kt"))

	// An idle holder is warned about, until it is seen again.
	w.configure(t, `{"staleAfterMinutes":1}`)
	w.advanceTo(120)
	w.waitRow(t, "agent-a", "stale", func(r capability.SessionListed) bool { return r.Stale })
	res = w.want(t, "an idle holder", "chat-b", capability.VerdictWarn, true, filepath.Join(w.wt1, "x.kt"))
	if !res.OccupiedBy.Stale || !strings.Contains(res.Message, "idle") {
		t.Errorf("the warning is %+v; want the holder idle", res)
	}
	describedWith(t, a, 30) // a call of the holder's own ends its idleness
	w.waitRow(t, "agent-a", "fresh", func(r capability.SessionListed) bool { return !r.Stale })
	w.want(t, "the holder seen again", "chat-b", capability.VerdictRefuse, true, filepath.Join(w.wt1, "x.kt"))
}
