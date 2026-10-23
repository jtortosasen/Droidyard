package chats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/testtime"
)

// plainAgentChat makes a cursor-agent chat directory whose store.db is only a file: for
// tests where a stand-in sqlite3 answers.
func (f *fixture) plainAgentChat(tree, id string) {
	f.t.Helper()
	dir := filepath.Join(f.cursor, "chats", md5Hex(tree), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.db"), []byte("not a database"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(agentMetaJSON(tree, 1_790_000_000_000, true)), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// Decision (ADR-0127 §2, §3): the call's deadline covers all three stores, the Cursor IDE's
// query included: a query that has not answered when the 2 s (scaled here) are over is a
// failed source with a cause, the call says truncated, and the other stores' chats are
// listed. Reversed (the IDE read bounded only by sqlite3's own 3 s kill) the call comes back
// at the kill, not at the deadline.
func TestChatsIDEQueryHonoursTheCallDeadline(t *testing.T) {
	fakeSQLite(t, "sleep 60")
	f := newFixture(t)
	const p = "/proj/a"
	f.known = []string{p}
	f.write(p, "claude-chat", f.next(), userLine(p), aiTitle("Claude title"))
	if err := os.MkdirAll(f.ide, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ide, "state.vscdb"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.service()
	s.budget = testtime.Bound(400 * time.Millisecond)
	s.sql.timeout = 10 * s.budget // the query's own kill is far past the call's deadline
	start := time.Now()
	res := list(t, s)
	if d := time.Since(start); d > 5*s.budget {
		t.Errorf("the call took %s; want about its deadline %s, not the query's own timeout %s", d, s.budget, s.sql.timeout)
	}
	if !res.Truncated {
		t.Error("truncated is false; want true: the deadline stopped the call")
	}
	if src := sourceOf(t, res, StoreCursorIDE); src.State != StateFailed || src.Cause == "" {
		t.Errorf("IDE source %+v; want failed with a cause", src)
	}
	if c, ok := byID(res, "claude-chat"); !ok || c.Title != "Claude title" {
		t.Errorf("claude-chat %+v, %v; want listed and titled", c, ok)
	}
}

// Decision (ADR-0127 §2): a call makes at most MaxReads reads in all, one count shared by the
// three stores (a file read or one sqlite3 run is one), and says truncated when it stops
// there. 150 Claude Code transcripts (5 projects of 30) and 100 cursor-agent chats (4 of 25,
// two reads each: meta.json and the title query) want 350. Reversed (a count per store) the
// 350 are read.
func TestChatsReadsAreSharedAcrossStores(t *testing.T) {
	fakeSQLite(t, "echo '[]'")
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("/proj/p%d", i)
		f.known = append(f.known, p)
		for j := 0; j < 30; j++ {
			f.write(p, fmt.Sprintf("p%d-c%02d", i, j), f.next(), userLine(p))
		}
		if i < 4 {
			for j := 0; j < 25; j++ {
				f.plainAgentChat(p, fmt.Sprintf("a%d-c%02d", i, j))
			}
		}
	}
	s := f.service()
	s.budget = testtime.Bound(60 * time.Second) // the reads are the bound under test
	c := count(s)
	res := list(t, s)
	if reads := c.calls + int(f.starts.Load()); reads != MaxReads {
		t.Errorf("%d reads (%d files, %d sqlite3); want %d in all", reads, c.calls, f.starts.Load(), MaxReads)
	}
	if !res.Truncated {
		t.Error("truncated is false; want true: the reads ran out")
	}
	if len(res.Chats) == 0 {
		t.Error("no chat listed; want what was read")
	}
}

// Decision (ADR-0127 §1): a chat id never starts with '-': it is copied into a resume
// command, where a name like --dangerously-skip-permissions would be read as an option. A
// transcript, a cursor-agent chat directory and an IDE composer so named are left out.
// Reversed (the old pattern, which allowed it) all three are listed.
func TestChatsIDsNeverStartWithADash(t *testing.T) {
	const p = "/proj/a"
	t.Run("claude code and cursor-agent", func(t *testing.T) {
		fakeSQLite(t, "echo '[]'")
		f := newFixture(t)
		f.known = []string{p}
		f.write(p, "--dangerously-skip-permissions", f.next(), userLine(p))
		f.write(p, "fine-1", f.next(), userLine(p))
		f.plainAgentChat(p, "-rf")
		f.plainAgentChat(p, "fine-2")
		res := list(t, f.service())
		got := map[string]bool{}
		for _, id := range ids(res) {
			got[id] = true
		}
		if !got["fine-1"] || !got["fine-2"] || got["--dangerously-skip-permissions"] || got["-rf"] || len(got) != 2 {
			t.Errorf("chats %v; want only fine-1 and fine-2", ids(res))
		}
	})
	t.Run("cursor ide", func(t *testing.T) {
		needSQLite(t)
		f := newFixture(t)
		f.known = []string{p}
		f.ideDB(ideSeed{id: "-x", name: "Dash", folder: p, t: 2}, ideSeed{id: "fine-3", name: "Fine", folder: p, t: 1})
		res := list(t, f.service())
		if got := strings.Join(ids(res), ","); got != "fine-3" {
			t.Errorf("chats %q; want only fine-3", got)
		}
	})
}

// Decision (ADR-0127 §2): a read that never returns is not started again. The call that
// began it has answered (truncated) and left the read running; a later call that reached the
// same file would leave one more goroutine stuck per call. Reversed (no in-flight set) the
// second call enters the reader again.
func TestChatsBlockedReadIsNotRepeated(t *testing.T) {
	const p = "/proj/a"
	f := newFixture(t)
	f.known = []string{p}
	stuck := f.write(p, "stuck", f.next(), userLine(p))
	f.write(p, "fine", f.next(), userLine(p))
	s := f.service()
	s.budget = testtime.Bound(150 * time.Millisecond)
	release := make(chan struct{})
	var mu sync.Mutex
	entered := map[string]int{}
	real := s.readAt
	s.readAt = func(path string, off int64, n int) ([]byte, error) {
		mu.Lock()
		entered[path]++
		mu.Unlock()
		if path == stuck {
			<-release
			return nil, os.ErrClosed
		}
		return real(path, off, n)
	}
	t.Cleanup(func() { close(release) })

	first, err := s.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := entered[stuck]
	mu.Unlock()
	if n != 1 {
		t.Errorf("the blocked file was entered %d times over two calls; want once", n)
	}
	for i, res := range []Result{first, second} {
		if !res.Truncated {
			t.Errorf("call %d: truncated is false; want true (a read is stuck)", i+1)
		}
	}
	if _, ok := byID(second, "fine"); !ok {
		t.Errorf("the second call listed %v; want the readable chat, the stuck one skipped", ids(second))
	}
}

// Two calls overlapping on a file that reads normally (two tabs refreshing on one event)
// both list its chat: only a read in flight past a call's budget is skipped.
func TestChatsOverlappingCallsBothList(t *testing.T) {
	const p = "/proj/a"
	f := newFixture(t)
	f.known = []string{p}
	f.write(p, "busy", f.next(), userLine(p))
	s := f.service()
	real := s.readAt
	s.readAt = func(path string, off int64, n int) ([]byte, error) {
		time.Sleep(100 * time.Millisecond)
		return real(path, off, n)
	}
	var wg sync.WaitGroup
	res := make([]Result, 2)
	for i := range res {
		wg.Add(1)
		go func(i int) { defer wg.Done(); res[i] = list(t, s) }(i)
	}
	wg.Wait()
	for i, r := range res {
		if _, ok := byID(r, "busy"); !ok || r.Truncated {
			t.Errorf("call %d: chats %v truncated %v; want busy listed, not truncated", i+1, ids(r), r.Truncated)
		}
	}
}
