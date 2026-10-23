package chats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Decision (ADR-0127 §2, plan m3c step 2): a project's chats are those filed under its main
// tree's directory and under the directory of each of its live linked worktrees, and a chat
// in a worktree's directory carries that worktree. Another project's directory is never
// read, and a worktree git no longer lists has no chats (its resume command would not run
// where the chat was).
func TestClaudeChatsOfAProjectAndItsWorktrees(t *testing.T) {
	r := newRepo(t)
	wt := r.worktree(t, "feature")
	gone := r.worktree(t, "gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.known = []string{r.main}
	f.write(r.main, "main-chat", f.next(), userLine(r.main))
	f.write(wt, "wt-chat", f.next(), userLine(wt))
	f.write(gone, "gone-chat", f.next(), userLine(gone))
	other := filepath.Join(f.home, "other-project")
	f.write(other, "other-chat", f.next(), userLine(other))

	res := list(t, f.service())
	if got, want := strings.Join(ids(res), ","), "wt-chat,main-chat"; got != want {
		t.Fatalf("chats %q; want %q (the main tree's and the live worktree's, newest first; not the removed worktree's nor another project's)", got, want)
	}
	m, _ := byID(res, "main-chat")
	w, _ := byID(res, "wt-chat")
	if m.Project != r.main || m.Worktree != "" || m.Client != ClientClaude || m.Store != StoreClaudeCode {
		t.Errorf("main tree chat %+v; want project %s, no worktree, claude, claude-code", m, r.main)
	}
	if w.Project != r.main || w.Worktree != wt {
		t.Errorf("worktree chat %+v; want project %s and worktree %s", w, r.main, wt)
	}
	if len(res.Sources) != 1 || res.Sources[0] != (Source{Store: StoreClaudeCode, State: StateRead}) {
		t.Errorf("sources %+v; want claude-code read", res.Sources)
	}
}

// bigFile is a transcript larger than both windows together: head lines, 200 KiB of lines
// that say nothing, tail lines.
func bigFile(head, tail []string) []string {
	lines := append([]string{}, head...)
	lines = append(lines, filler(200<<10)...)
	return append(lines, tail...)
}

// Decision (ADR-0127 §2): the title is custom-title, else the last ai-title, else agent-name,
// else none, wherever in the head or the tail the lines are. "Last" matters because the
// client revises the title and re-appends it near the end; reversed (the first ai-title
// kept) the revised-title cases fail.
func TestClaudeTitlePrecedence(t *testing.T) {
	const p = "/proj/a"
	user := userLine(p)
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"custom over ai over agent", []string{user, agentName("agent"), aiTitle("ai"), customTitle("custom")}, "custom"},
		{"custom, whatever the order", []string{user, customTitle("custom"), aiTitle("ai"), agentName("agent")}, "custom"},
		{"ai over agent", []string{user, agentName("agent"), aiTitle("ai")}, "ai"},
		{"agent alone", []string{user, agentName("agent")}, "agent"},
		{"the last ai-title wins", []string{user, aiTitle("first"), aiTitle("second"), aiTitle("third")}, "third"},
		{"no title", []string{user, plainLine(p)}, ""},
		{"an empty ai-title falls to agent-name", []string{user, agentName("agent"), aiTitle("  ")}, "agent"},
		{"only in the head", bigFile([]string{user, aiTitle("head only")}, []string{plainLine(p)}), "head only"},
		{"only in the tail", bigFile([]string{user}, []string{aiTitle("tail only"), plainLine(p)}), "tail only"},
		{"revised: head and tail differ, the tail wins", bigFile([]string{user, aiTitle("old")}, []string{aiTitle("new"), plainLine(p)}), "new"},
		{"revised twice in the tail", bigFile([]string{user}, []string{aiTitle("old"), plainLine(p), aiTitle("new")}), "new"},
		{"custom in the head beats ai in the tail", bigFile([]string{user, customTitle("custom")}, []string{aiTitle("ai"), plainLine(p)}), "custom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.known = []string{p}
			f.write(p, "chat-1", f.next(), c.lines...)
			res := list(t, f.service())
			got, ok := byID(res, "chat-1")
			if !ok {
				t.Fatalf("chat-1 not listed: %+v", res)
			}
			if got.Title != c.want {
				t.Errorf("title %q; want %q", got.Title, c.want)
			}
		})
	}
}

// Decision (A3): a transcript is read at most 64 KiB from its head and 64 KiB from its tail,
// so a 124 MB transcript costs 128 KiB, and a title that only the middle holds is not found:
// the reader trades it for the bound. A line longer than the window holds no whole line, so
// it is skipped. Reversed (the whole file read) the counter exceeds 128 KiB.
func TestClaudeReadsOnlyTheWindows(t *testing.T) {
	const p = "/proj/a"
	f := newFixture(t)
	f.known = []string{p}
	// 10 MiB: a first line, 5 MiB of filler, the title, 5 MiB of filler, a last line.
	lines := []string{userLine(p)}
	lines = append(lines, filler(5<<20)...)
	lines = append(lines, aiTitle("the middle"))
	lines = append(lines, filler(5<<20)...)
	lines = append(lines, plainLine(p))
	path := f.write(p, "big-chat", f.next(), lines...)
	if fi, _ := os.Stat(path); fi.Size() < 10<<20 {
		t.Fatalf("the fixture is %d bytes; want at least 10 MiB", fi.Size())
	}
	s := f.service()
	c := count(s)
	res := list(t, s)
	got, ok := byID(res, "big-chat")
	if !ok {
		t.Fatalf("big-chat not listed: %+v", res)
	}
	if got.Title != "" {
		t.Errorf("title %q; want none: the only title is outside both windows", got.Title)
	}
	if c.bytes > 2*Window {
		t.Errorf("%d bytes read; want at most %d (the two windows)", c.bytes, 2*Window)
	}

	// A line longer than the window: a 100 KiB title line at the head is cut by the window,
	// holds no newline in it, and is skipped.
	f2 := newFixture(t)
	f2.known = []string{p}
	long := bigFile([]string{aiTitle(strings.Repeat("t", 100<<10))}, []string{userLine(p)})
	f2.write(p, "long-line", f2.next(), long...)
	res = list(t, f2.service())
	got, ok = byID(res, "long-line")
	if !ok || got.Title != "" {
		t.Errorf("long-line: listed %v, title %q; want listed and untitled", ok, got.Title)
	}
}

// Decision (A3, ADR-0127 §2): no store byte outside the named keys reaches the result. A
// message, a prompt and the last-prompt line carry a marker; the marshalled result holds
// none of it, and its fields are exactly the contract's. Reversed (a first-prompt fallback
// title, or a message field decoded into the chat) the marker appears.
func TestClaudeNoContentLeaves(t *testing.T) {
	const p = "/proj/a"
	const marker = "SECRET-MARKER-7f3a"
	f := newFixture(t)
	f.known = []string{p}
	f.write(p, "chat-1", f.next(),
		`{"type":"user","cwd":"/proj/a","entrypoint":"cli","gitBranch":"`+marker+`","message":{"role":"user","content":"`+marker+` first prompt"}}`,
		`{"type":"assistant","cwd":"/proj/a","message":{"role":"assistant","content":[{"type":"text","text":"`+marker+`"}]}}`,
		`{"type":"last-prompt","lastPrompt":"`+marker+`","sessionId":"s"}`,
		`{"type":"summary","summary":"`+marker+`"}`,
	)
	res := list(t, f.service())
	if len(res.Chats) != 1 {
		t.Fatalf("chats %+v; want chat-1", res.Chats)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), marker) {
		t.Errorf("the result carries message content: %s", raw)
	}
	if res.Chats[0].Title != "" {
		t.Errorf("title %q; want none: the first prompt is never a fallback title", res.Chats[0].Title)
	}
	var decoded struct {
		Chats []map[string]any `json:"chats"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"client": true, "store": true, "id": true, "project": true, "worktree": true, "title": true, "updatedAt": true, "session": true}
	for k := range decoded.Chats[0] {
		if !allowed[k] {
			t.Errorf("chat field %q is not part of the contract", k)
		}
	}
}

// Decision (plan m3c step 2, Starting point): the chat's place is the candidate whose
// directory holds the file, because a transcript moves with its working directory; the cwd
// only settles the encoding, which is lossy. So: a relocated chat is its directory's; a chat
// whose last cwd lies below the root (the agent ran `cd app`) is the root's, which equality
// would lose; and a file whose cwd is another path that encodes to the same directory is not
// this project's, whether it equals, or lies below, that other path.
func TestClaudePlace(t *testing.T) {
	r := newRepo(t)
	wt := r.worktree(t, "feature")
	f := newFixture(t)
	f.known = []string{r.main}
	// Relocated: started in the main tree, moved into the worktree; the file moved with it.
	f.write(wt, "relocated", f.next(), userLine(r.main), plainLine(wt))
	// `cd app`: last cwd below the root.
	f.write(r.main, "below", f.next(), userLine(r.main), plainLine(filepath.Join(r.main, "app")))
	res := list(t, f.service())
	if c, ok := byID(res, "relocated"); !ok || c.Worktree != wt || c.Project != r.main {
		t.Errorf("relocated: %+v listed %v; want the worktree %s of %s", c, ok, wt, r.main)
	}
	if c, ok := byID(res, "below"); !ok || c.Worktree != "" || c.Project != r.main {
		t.Errorf("below: %+v listed %v; want the main tree's chat (cwd below the root)", c, ok)
	}

	// The encoding is lossy: /proj/a/b-c and /proj/a/b/c share one directory name.
	g := newFixture(t)
	g.known = []string{"/proj/a/b-c"}
	if encodePath("/proj/a/b-c") != encodePath("/proj/a/b/c") {
		t.Fatal("the fixture's two paths no longer encode alike")
	}
	g.write("/proj/a/b-c", "mine", g.next(), userLine("/proj/a/b-c"), plainLine("/proj/a/b-c/sub"))
	g.write("/proj/a/b-c", "other-equal", g.next(), userLine("/proj/a/b/c"))
	g.write("/proj/a/b-c", "other-below", g.next(), userLine("/proj/a/b/c/x"))
	g.write("/proj/a/b-c", "sibling-prefix", g.next(), userLine("/proj/a/b-cd"))
	res = list(t, g.service())
	if got, want := strings.Join(ids(res), ","), "mine"; got != want {
		t.Errorf("chats %q; want %q: a cwd that merely encodes alike, or shares a name prefix, is not the project's", got, want)
	}
}

// Decision (ADR-0127 §2: a chat is one the client's resume list would show): subagent
// transcripts, ids that are not safe stems, links, headless sdk-cli runs, transcripts with no
// conversation line and a chat whose continuation is listed are left out. The sdk-cli
// decision comes from the first line that carries an entrypoint in the head window.
func TestClaudeLeftOut(t *testing.T) {
	const p = "/proj/a"
	f := newFixture(t)
	f.known = []string{p}
	f.write(p, "kept", f.next(), userLine(p))
	// A subagent's transcript lives below the chat's own directory, not at the top level.
	sub := filepath.Join(f.dir(p), "kept", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-x.jsonl"), []byte(userLine(p)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stems that are not chat ids.
	f.write(p, "has space", f.next(), userLine(p))
	f.write(p, "has.dot", f.next(), userLine(p))
	f.write(p, strings.Repeat("a", 65), f.next(), userLine(p))
	// A link to a good transcript.
	target := f.write(p, "target-chat", f.next(), userLine(p))
	if err := os.Symlink(target, filepath.Join(f.dir(p), "linked.jsonl")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	// No line with a cwd: no conversation.
	f.write(p, "no-conversation", f.next(), aiTitle("titled but empty"), `{"type":"summary","summary":"x"}`)
	// Headless runs: the first line that carries an entrypoint says sdk-cli.
	f.write(p, "headless", f.next(), aiTitle("t"), strings.Replace(userLine(p), `"cli"`, `"sdk-cli"`, 1))
	// The first entrypoint decides, not a later one.
	f.write(p, "cli-then-sdk", f.next(), userLine(p), strings.Replace(userLine(p), `"cli"`, `"sdk-cli"`, 1))
	// Continued: A says B continues it, and B is listed; C names a continuation nobody lists.
	f.write(p, "continued-a", f.next(), userLine(p), continuedIn("continuation-b"))
	f.write(p, "continuation-b", f.next(), userLine(p))
	f.write(p, "continued-c", f.next(), userLine(p), continuedIn("not-listed"))

	res := list(t, f.service())
	got := map[string]bool{}
	for _, id := range ids(res) {
		got[id] = true
	}
	for _, want := range []string{"kept", "target-chat", "cli-then-sdk", "continuation-b", "continued-c"} {
		if !got[want] {
			t.Errorf("%s is not listed; chats: %v", want, ids(res))
		}
	}
	for _, left := range []string{"agent-x", "has space", "has.dot", strings.Repeat("a", 65), "linked", "no-conversation", "headless", "continued-a"} {
		if got[left] {
			t.Errorf("%s is listed; it is not a chat the client lists", left)
		}
	}
	if len(res.Chats) != 5 {
		t.Errorf("chats %v; want exactly kept, target-chat, cli-then-sdk, continuation-b and continued-c", ids(res))
	}
}

// Decision (A3): a file that is not a regular file is never opened: a named pipe called
// *.jsonl would block a read forever. The listing leaves it out by its kind.
func TestClaudeLeftOutPipe(t *testing.T) {
	const p = "/proj/a"
	f := newFixture(t)
	f.known = []string{p}
	f.write(p, "kept", f.next(), userLine(p))
	if !makePipe(t, filepath.Join(f.dir(p), "pipe.jsonl")) {
		return
	}
	s := f.service()
	c := count(s)
	res := list(t, s)
	if got, want := strings.Join(ids(res), ","), "kept"; got != want {
		t.Errorf("chats %q; want %q", got, want)
	}
	for path := range c.paths {
		if strings.HasSuffix(path, "pipe.jsonl") {
			t.Errorf("the pipe was opened: %s", path)
		}
	}
}

// Decision (ADR-0015 §1): a title is cleaned of control and format characters (a bidi
// override, a newline) and cut to 120 characters before it leaves the reader; reversed the
// override or the 121st character is in the result.
func TestChatsTitleCleaned(t *testing.T) {
	const p = "/proj/a"
	f := newFixture(t)
	f.known = []string{p}
	f.write(p, "dirty", f.next(), userLine(p), aiTitle("Fix ‮evil‬\nbuild ​now\x00"))
	f.write(p, "long", f.next(), userLine(p), aiTitle(strings.Repeat("é", 121)))
	f.write(p, "padded", f.next(), userLine(p), aiTitle("  ‮  "))
	res := list(t, f.service())
	d, _ := byID(res, "dirty")
	if d.Title != "Fix evilbuild now" {
		t.Errorf("dirty title %q; want the override, the newline, the zero-width space and the NUL removed", d.Title)
	}
	l, _ := byID(res, "long")
	if n := len([]rune(l.Title)); n != MaxTitle {
		t.Errorf("long title has %d characters; want %d", n, MaxTitle)
	}
	p2, _ := byID(res, "padded")
	if p2.Title != "" {
		t.Errorf("a title of nothing but invisible characters is %q; want none", p2.Title)
	}
}
