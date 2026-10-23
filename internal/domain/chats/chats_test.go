package chats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Decision (ADR-0127 §2): at most 30 chats per client and project, counted across all the
// project's directories, newest first by modification time, and the cut is not `truncated`
// (that flag is the call's budget only). A second call reads no file that did not change; a
// file that changed is read again.
func TestChatsBoundsAndCache(t *testing.T) {
	r := newRepo(t)
	wt := r.worktree(t, "feature")
	f := newFixture(t)
	f.known = []string{r.main}
	// 31 chats across the two directories, written oldest first: c00 is the oldest.
	for i := 0; i < 31; i++ {
		tree := r.main
		if i%2 == 1 {
			tree = wt
		}
		f.write(tree, fmt.Sprintf("c%02d", i), f.next(), userLine(tree))
	}
	s := f.service()
	c := count(s)
	res := list(t, s)
	if len(res.Chats) != MaxPerProject || res.Truncated {
		t.Fatalf("%d chats, truncated %v; want %d and not truncated", len(res.Chats), res.Truncated, MaxPerProject)
	}
	if res.Chats[0].ID != "c30" || res.Chats[MaxPerProject-1].ID != "c01" {
		t.Errorf("chats run from %s to %s; want newest c30 down to c01 (the oldest is the one cut)", res.Chats[0].ID, res.Chats[MaxPerProject-1].ID)
	}
	if c.files() != MaxPerProject {
		t.Errorf("%d files read; want %d: the cut file is never opened", c.files(), MaxPerProject)
	}

	c.reset()
	res = list(t, s)
	if c.calls != 0 || len(res.Chats) != MaxPerProject {
		t.Errorf("second call: %d reads, %d chats; want no read of an unchanged file and the same chats", c.calls, len(res.Chats))
	}

	// A touched file: new size and mtime, read again; the others are not.
	path := filepath.Join(f.dir(r.main), "c30.jsonl")
	if err := os.WriteFile(path, []byte(userLine(r.main)+"\n"+aiTitle("renamed")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := f.next()
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	c.reset()
	res = list(t, s)
	if c.files() != 1 || c.paths[path] == 0 {
		t.Errorf("after a touch %d files read (%v); want only the touched one", c.files(), c.paths)
	}
	if got, _ := byID(res, "c30"); got.Title != "renamed" {
		t.Errorf("c30 title %q; want the new one", got.Title)
	}
}

// Decision (ADR-0127 §2): a call reads at most 200 files and says truncated when it stops
// short of that, returning what it read; the next call goes further because the cache holds
// what was read. Reversed (no cap, or a cap that does not flag) 203 chats come back, or
// truncated is false.
func TestChatsReadBudget(t *testing.T) {
	f := newFixture(t)
	// 7 projects of 29 chats each: all needed (under the cut of 30), 203 files.
	for p := 0; p < 7; p++ {
		tree := fmt.Sprintf("/proj/p%d", p)
		f.known = append(f.known, tree)
		for i := 0; i < 29; i++ {
			f.write(tree, fmt.Sprintf("p%d-c%02d", p, i), f.next(), userLine(tree))
		}
	}
	s := f.service()
	c := count(s)
	res := list(t, s)
	if c.files() != MaxReads || len(res.Chats) != MaxReads || !res.Truncated {
		t.Fatalf("%d files read, %d chats, truncated %v; want %d, %d and truncated", c.files(), len(res.Chats), res.Truncated, MaxReads, MaxReads)
	}
	res = list(t, s)
	if len(res.Chats) != 203 || res.Truncated {
		t.Errorf("second call: %d chats, truncated %v; want all 203: the first 200 came from the cache", len(res.Chats), res.Truncated)
	}
}

// Decision (ADR-0127 §2): the cache holds at most MaxCache files' facts, so a store of any
// size cannot grow the daemon without bound. 2,100 files are read over several calls, and
// the cache never exceeds its bound.
func TestChatsCacheIsBounded(t *testing.T) {
	f := newFixture(t)
	for p := 0; p < 70; p++ {
		tree := fmt.Sprintf("/proj/p%02d", p)
		f.known = append(f.known, tree)
		for i := 0; i < 30; i++ {
			f.write(tree, fmt.Sprintf("p%02d-c%02d", p, i), f.next(), userLine(tree))
		}
	}
	s := f.service()
	for i := 0; i < 14; i++ {
		list(t, s)
		s.mu.Lock()
		n := len(s.cache)
		s.mu.Unlock()
		if n > MaxCache {
			t.Fatalf("after call %d the cache holds %d entries; want at most %d", i+1, n, MaxCache)
		}
	}
	s.mu.Lock()
	n := len(s.cache)
	s.mu.Unlock()
	if n < MaxCache-MaxReads {
		t.Errorf("the cache holds %d entries after 14 calls over 2,100 files; want it filled to near its bound", n)
	}
}

// Decision (ADR-0127 §2): a call stops at its time budget and answers with what it has,
// flagged truncated, even when a read never returns (a file on a stalled mount). The scan
// runs apart from the call so it cannot hold the call; reversed (the scan inline) the call
// would not come back until the reader does.
func TestChatsDeadline(t *testing.T) {
	f := newFixture(t)
	f.known = []string{"/proj/a"}
	f.write("/proj/a", "stuck", f.next(), userLine("/proj/a"))
	s := f.service()
	s.budget = testtime.Bound(150 * time.Millisecond)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	s.readAt = func(string, int64, int) ([]byte, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil, os.ErrClosed
	}
	t.Cleanup(func() { close(release) })
	start := time.Now()
	answered := make(chan Result, 1)
	go func() {
		res, err := s.list(context.Background())
		if err != nil {
			t.Errorf("list: %v", err)
		}
		answered <- res
	}()
	var res Result
	select {
	case res = <-answered:
	case <-time.After(testtime.Bound(5 * time.Second)):
		t.Fatal("the call did not come back while a read was blocked")
	}
	if elapsed := time.Since(start); elapsed < s.budget {
		t.Errorf("the call took %v; want at least its budget %v", elapsed, s.budget)
	}
	if !res.Truncated || len(res.Chats) != 0 {
		t.Errorf("truncated %v, %d chats; want truncated and none", res.Truncated, len(res.Chats))
	}
	select {
	case <-entered:
	default:
		t.Error("the reader was never entered, so the test proved nothing")
	}
}

// Decision (ADR-0127 §1, ADR-0095 §1): a Claude Code chat whose id is a live session's chat
// carries that session's id, and a session that ended leaves the chat unjoined.
func TestChatsJoined(t *testing.T) {
	const p = "/proj/a"
	f := newFixture(t)
	f.known = []string{p}
	f.write(p, "chat-live", f.next(), userLine(p))
	f.write(p, "chat-idle", f.next(), userLine(p))
	sessions := capability.NewSessions()
	s := f.service()
	s.o.Sessions = sessions
	sessions.Connect("sess-1", "chat-live")
	res := list(t, s)
	if got, _ := byID(res, "chat-live"); got.Session != "sess-1" {
		t.Errorf("chat-live session %q; want sess-1", got.Session)
	}
	if got, _ := byID(res, "chat-idle"); got.Session != "" {
		t.Errorf("chat-idle session %q; want none", got.Session)
	}
	sessions.End("sess-1")
	res = list(t, s)
	if got, _ := byID(res, "chat-live"); got.Session != "" {
		t.Errorf("after the end the session is %q; want none", got.Session)
	}
}

// Decision (ADR-0127 §1, A4): a store that is missing or unreadable is a sources entry with
// a cause and never an error, and the candidates that can be read are still listed. The
// daemon's CLAUDE_CONFIG_DIR is honoured (the directories come from assistant.ResolveUserDirs,
// as `init --user`'s do), and the default ~/.claude is then not read.
func TestChatsSources(t *testing.T) {
	t.Run("no projects directory is absent", func(t *testing.T) {
		f := newFixture(t)
		f.known = []string{"/proj/a"}
		if err := os.Remove(f.projects); err != nil {
			t.Fatal(err)
		}
		res := list(t, f.service())
		if len(res.Sources) != 1 || res.Sources[0].State != StateAbsent || res.Sources[0].Cause != "" || len(res.Chats) != 0 {
			t.Errorf("sources %+v, chats %d; want claude-code absent with no cause", res.Sources, len(res.Chats))
		}
		if res.Chats == nil || res.Sources == nil {
			t.Error("chats and sources must be empty lists, not null")
		}
	})

	t.Run("an unreadable directory fails the source and the others are read", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory at mode 000")
		}
		r := newRepo(t)
		wt := r.worktree(t, "feature")
		f := newFixture(t)
		f.known = []string{r.main}
		f.write(r.main, "main-chat", f.next(), userLine(r.main))
		f.write(wt, "wt-chat", f.next(), userLine(wt))
		if err := os.Chmod(f.dir(wt), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(f.dir(wt), 0o700) })
		res := list(t, f.service())
		src := res.Sources[0]
		if src.State != StateFailed || !strings.Contains(src.Cause, "permission denied") {
			t.Errorf("source %+v; want failed, permission denied", src)
		}
		if strings.Contains(src.Cause, f.home) {
			t.Errorf("the cause names a path: %q", src.Cause)
		}
		if got := strings.Join(ids(res), ","); got != "main-chat" {
			t.Errorf("chats %q; want the readable directory's main-chat", got)
		}
	})

	t.Run("no directory for the client at all fails with a cause", func(t *testing.T) {
		s := newService(Options{Known: func() []string { return []string{"/proj/a"} }})
		res := list(t, s)
		if res.Sources[0].State != StateFailed || res.Sources[0].Cause == "" {
			t.Errorf("source %+v; want failed with a cause", res.Sources[0])
		}
	})

	t.Run("CLAUDE_CONFIG_DIR is honoured", func(t *testing.T) {
		const p = "/proj/a"
		home := platform.Current().Canonical(t.TempDir())
		custom := filepath.Join(home, "elsewhere", "claude")
		getenv := func(k string) string {
			if k == "CLAUDE_CONFIG_DIR" {
				return custom
			}
			return ""
		}
		dirs := assistant.ResolveUserDirs(home, getenv)
		f := newFixture(t)
		f.known = []string{p}
		f.claude, f.projects = custom, filepath.Join(custom, "projects")
		f.write(p, "in-custom", f.next(), userLine(p))
		// A transcript under the default directory of the same home: not read.
		def := &fixture{t: t, home: home, claude: filepath.Join(home, ".claude"), projects: filepath.Join(home, ".claude", "projects"), clock: f.clock}
		def.write(p, "in-default", def.next(), userLine(p))
		s := newService(Options{Known: func() []string { return []string{p} }, Dirs: dirs})
		res := list(t, s)
		if got := strings.Join(ids(res), ","); got != "in-custom" {
			t.Errorf("chats %q; want only in-custom: CLAUDE_CONFIG_DIR replaces ~/.claude", got)
		}
	})
}

// Decision (ADR-0127 §1): session.chats is a query for the web UI: stateless, global, human
// audience (never an MCP tool), with the untrusted flag derived from the title's tag, and a
// retained result drops its chats. The handler is the reader, so a registered call lists.
func TestRegisterDescribesTheCapability(t *testing.T) {
	const p = "/proj/a"
	f := newFixture(t)
	f.known = []string{p}
	f.write(p, "chat-1", f.next(), userLine(p), aiTitle("Titled"))
	reg := capability.NewRegistry()
	Register(reg, Options{Known: func() []string { return f.known }, Dirs: assistant.UserDirs{Claude: f.claude}})
	d, ok := reg.Lookup("session.chats")
	if !ok {
		t.Fatal("session.chats is not registered")
	}
	if d.Kind != capability.Query || d.Scope != capability.Global || d.Statefulness != capability.Stateless || d.Audience != capability.AudienceHuman {
		t.Errorf("kind %v, scope %v, statefulness %v, audience %v; want a stateless global human query", d.Kind, d.Scope, d.Statefulness, d.Audience)
	}
	if d.Since != "0.38.0" || !d.Untrusted() {
		t.Errorf("since %s, untrusted %v; want 0.38.0 and untrusted (the title)", d.Since, d.Untrusted())
	}
	if a := d.ResolveAnnotations(); !a.ReadOnly || a.Destructive || a.OpenWorld {
		t.Errorf("annotations %+v; want read-only, not destructive, closed-world", a)
	}
	out, err := d.Handler(context.Background(), &capability.Call{Params: Params{}})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(Result)
	if len(res.Chats) != 1 || res.Chats[0].Title != "Titled" {
		t.Errorf("result %+v; want chat-1 titled", res)
	}
	slim := d.Slim(res).(Result)
	if len(slim.Chats) != 0 || len(slim.Sources) != 1 || slim.Chats == nil {
		t.Errorf("slim result %+v; want no chats (an empty list), the sources kept", slim)
	}
}
