package chats

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runner is a sqliteRunner as the daemon builds one, with a generous timeout.
func runner() sqliteRunner {
	return sqliteRunner{start: startGroup, timeout: 20 * time.Second, maxOut: sqliteMaxOut}
}

// fakeSQLite puts a `sqlite3` made of script first on PATH for the test.
func fakeSQLite(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sqlite3"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// tree is what a directory holds: every file's name, and the content hash of the files
// named in hashed.
func tree(t *testing.T, dir string, hashed ...string) (names []string, hashes map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	hashes = map[string]string{}
	for _, h := range hashed {
		b, err := os.ReadFile(filepath.Join(dir, h))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		hashes[h] = hex.EncodeToString(sum[:])
	}
	return names, hashes
}

// sameTree fails the test when a directory's listing or the named files' content differ from
// before.
func sameTree(t *testing.T, what, dir string, beforeNames []string, beforeHashes map[string]string) {
	t.Helper()
	var files []string
	for h := range beforeHashes {
		files = append(files, h)
	}
	names, hashes := tree(t, dir, files...)
	if strings.Join(names, ",") != strings.Join(beforeNames, ",") {
		t.Errorf("%s: the directory held %v and now holds %v; want no file created or removed", what, beforeNames, names)
	}
	for h, was := range beforeHashes {
		if hashes[h] != was {
			t.Errorf("%s: %s changed", what, h)
		}
	}
}

// closedWALDB makes a WAL database that is cleanly closed: neither a -wal nor a -shm beside
// it (the host's sqlite3 may leave empty ones; they are removed, which is lawful with the
// last writer gone and the log empty).
func closedWALDB(t *testing.T, db string) {
	t.Helper()
	sqlRun(t, db, "PRAGMA journal_mode=wal;\nCREATE TABLE t (v TEXT);\nINSERT INTO t VALUES ('closed');\n")
	for _, side := range []string{"-wal", "-shm"} {
		if err := os.Remove(db + side); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if !closedWAL(db) {
		t.Fatal("the premise failed: the database is not a closed WAL database")
	}
}

// Decision (ADR-0127 §3, A3): reading a Cursor database writes nothing to it and creates no
// file beside it. Three states of a store: a WAL database cleanly closed (read immutable,
// since a read-only open would have to create its index and cannot: observed, error 14, so
// a read that does not return its rows is the failure a reversed `mode=ro` shows), a
// rollback-journal database, and a WAL database held open by a writer with rows still in
// its -wal (the live-Cursor case): those rows are read, the database and its -wal do not
// change, and only the -shm index every WAL reader maps may be touched. Reversed (`immutable`
// for the open database) the uncheckpointed row is not read; (`mode=ro` for the closed one)
// the closed database cannot be opened.
func TestSQLiteWritesNothing(t *testing.T) {
	needSQLite(t)
	ctx := context.Background()

	t.Run("a cleanly closed WAL database", func(t *testing.T) {
		dir := t.TempDir()
		db := filepath.Join(dir, "closed.db")
		closedWALDB(t, db)
		names, hashes := tree(t, dir, "closed.db")
		out, err := runner().query(ctx, db, "SELECT v FROM t")
		if err != nil || strings.TrimSpace(string(out)) != `[{"v":"closed"}]` {
			t.Fatalf("read %q, %v; want its row", out, err)
		}
		sameTree(t, "closed WAL", dir, names, hashes)
	})

	t.Run("a rollback-journal database", func(t *testing.T) {
		dir := t.TempDir()
		db := filepath.Join(dir, "rollback.db")
		sqlRun(t, db, "CREATE TABLE t (v TEXT);\nINSERT INTO t VALUES ('rollback');\n")
		if closedWAL(db) {
			t.Fatal("the premise failed: the database is in WAL mode")
		}
		names, hashes := tree(t, dir, "rollback.db")
		out, err := runner().query(ctx, db, "SELECT v FROM t")
		if err != nil || strings.TrimSpace(string(out)) != `[{"v":"rollback"}]` {
			t.Fatalf("read %q, %v; want its row", out, err)
		}
		sameTree(t, "rollback journal", dir, names, hashes)
	})

	t.Run("a WAL database held open with rows in its -wal", func(t *testing.T) {
		dir := t.TempDir()
		db := filepath.Join(dir, "live.db")
		holdWAL(t, db)
		if fi, err := os.Stat(db + "-wal"); err != nil || fi.Size() == 0 {
			t.Fatalf("the premise failed: the -wal is %v, %v; want rows in it", fi, err)
		}
		names, hashes := tree(t, dir, "live.db", "live.db-wal")
		out, err := runner().query(ctx, db, "SELECT v FROM t")
		if err != nil || strings.TrimSpace(string(out)) != `[{"v":"uncheckpointed"}]` {
			t.Fatalf("read %q, %v; want the row that is only in the -wal", out, err)
		}
		sameTree(t, "live WAL", dir, names, hashes)
	})
}

// holdWAL starts a sqlite3 that keeps db open in WAL mode with an uncheckpointed row until
// the test ends.
func holdWAL(t *testing.T, db string) {
	t.Helper()
	cmd := exec.Command("sqlite3", db)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	outp, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	})
	io.WriteString(in, "PRAGMA journal_mode=wal;\nPRAGMA wal_autocheckpoint=0;\nCREATE TABLE t (v TEXT);\nINSERT INTO t VALUES ('uncheckpointed');\n.print READY\n")
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(outp)
		for sc.Scan() {
			if sc.Text() == "READY" {
				ready <- true
				io.Copy(io.Discard, outp)
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("the writer exited before it was ready")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the writer was not ready in 10 s")
	}
}

// Decision (ADR-0127 §3): the argument list is fixed: `-init /dev/null -safe -readonly -json`,
// then the URI and the query. A ~/.sqliterc cannot be tested through HOME (the host's
// sqlite3 finds the home directory from the password database, observed), so `-init
// /dev/null` is pinned here, where the arguments are recorded by a stand-in sqlite3.
// Reversed (any option dropped) the recorded list differs.
func TestSQLiteInvocationArguments(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	fakeSQLite(t, fmt.Sprintf("for a in \"$@\"; do printf '%%s\\n' \"$a\"; done > '%s'\necho '[]'", log))
	db := filepath.Join(dir, "some.db")
	if err := os.WriteFile(db, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner().query(context.Background(), db, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(args) != 7 || strings.Join(args[:5], " ") != "-init /dev/null -safe -readonly -json" || args[6] != "SELECT 1" {
		t.Fatalf("arguments %q; want -init /dev/null -safe -readonly -json <uri> <query>", args)
	}
	if want := "file:" + db + "?mode=ro"; args[5] != want {
		t.Errorf("uri %q; want %q", args[5], want)
	}
}

// Decision (ADR-0127 §3): the path is percent-encoded into the URI, so a `?`, `#`, `%` or `'`
// in it is a character of the name and not URI syntax. Reversed (the path put in raw) the
// database opened is the decoy `w`, the name cut at its `?`.
func TestSQLiteInvocationPathCharacters(t *testing.T) {
	needSQLite(t)
	dir := t.TempDir()
	sqlRun(t, filepath.Join(dir, "w"), "CREATE TABLE t (v TEXT);\nINSERT INTO t VALUES ('decoy');\n")
	db := filepath.Join(dir, "w?e#i%rd'x.db")
	sqlRun(t, db, "CREATE TABLE t (v TEXT);\nINSERT INTO t VALUES ('right');\n")
	out, err := runner().query(context.Background(), db, "SELECT v FROM t")
	if err != nil || strings.TrimSpace(string(out)) != `[{"v":"right"}]` {
		t.Errorf("read %q, %v; want the row of the file with the odd name", out, err)
	}
}

// Decision (ADR-0127 §3): a link is not followed to a database, and the database is not
// opened at all. Reversed (no Lstat) the stand-in sqlite3 starts and leaves its log.
func TestSQLiteDoesNotOpenALink(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "started")
	fakeSQLite(t, fmt.Sprintf("echo started > '%s'\necho '[]'", log))
	target := filepath.Join(dir, "real.db")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	_, err := runner().query(context.Background(), link, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("error %v; want not a regular file", err)
	}
	if _, serr := os.Stat(log); serr == nil {
		t.Error("sqlite3 was started for a link")
	}
	// The reader treats a store that is a link the same way: a failed source, not a read.
	f := newFixture(t)
	f.known = []string{"/proj/a"}
	if err := os.MkdirAll(f.ide, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(f.ide, "state.vscdb")); err != nil {
		t.Fatal(err)
	}
	res := list(t, f.service())
	if src := sourceOf(t, res, StoreCursorIDE); src.State != StateFailed || !strings.Contains(src.Cause, "not a regular file") {
		t.Errorf("source %+v; want failed, not a regular file", src)
	}
	if _, serr := os.Stat(log); serr == nil {
		t.Error("sqlite3 was started for a linked store")
	}
}

// boundedWorld is a fixture with one Claude Code chat, one cursor-agent chat (a meta.json
// and a store.db that is only a file) and an IDE store that is only a file, for the stand-in
// sqlite3 on PATH to answer for.
func boundedWorld(t *testing.T) (*fixture, *service) {
	t.Helper()
	f := newFixture(t)
	const p = "/proj/a"
	f.known = []string{p}
	f.write(p, "claude-chat", f.next(), userLine(p), aiTitle("Claude title"))
	dir := filepath.Join(f.cursor, "chats", md5Hex(p), "agent-chat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.db"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(agentMetaJSON(p, 1_790_000_000_000, true)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.ide, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ide, "state.vscdb"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, f.service()
}

// Decision (ADR-0127 §3): a sqlite3 that does not finish is killed with its whole group at
// the timeout (scaled down here from 3 s), and one that writes more than 4 MiB is cut there
// and killed; either way the store's source is failed with a cause, the chats the other
// stores gave are listed, and a cursor-agent chat that has a meta.json is still listed,
// untitled. Reversed (no kill, or the cap not enforced) the sleeper holds the call for
// 60 s and its pids stay, or the flood is read whole.
func TestSQLiteBounded(t *testing.T) {
	t.Run("a sqlite3 that sleeps is killed with its group", func(t *testing.T) {
		pids := t.TempDir()
		fakeSQLite(t, fmt.Sprintf("sleep 60 &\necho $! > '%s'/child-$$\necho $$ > '%s'/self-$$\nwait", pids, pids))
		_, s := boundedWorld(t)
		s.sql.timeout = 600 * time.Millisecond
		start := time.Now()
		res := list(t, s)
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("the call took %s; want about the timeout", d)
		}
		for _, store := range []string{StoreCursorAgent, StoreCursorIDE} {
			if src := sourceOf(t, res, store); src.State != StateFailed || !strings.Contains(src.Cause, "did not finish") {
				t.Errorf("%s source %+v; want failed, did not finish", store, src)
			}
		}
		if got := strings.Join(ids(res), ","); got != "claude-chat,agent-chat" && got != "agent-chat,claude-chat" {
			t.Errorf("chats %q; want the Claude Code chat and the cursor-agent chat with a meta.json", got)
		}
		if a, _ := byID(res, "agent-chat"); a.Title != "" {
			t.Errorf("agent-chat title %q; want none (sqlite3 never answered)", a.Title)
		}
		if c, _ := byID(res, "claude-chat"); c.Title != "Claude title" {
			t.Errorf("claude-chat %+v; want its title read", c)
		}
		files, _ := filepath.Glob(filepath.Join(pids, "*"))
		if len(files) < 2 {
			t.Fatalf("the stand-in recorded %d pid files; want its own and its child's", len(files))
		}
		deadline := time.Now().Add(5 * time.Second)
		for _, file := range files {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			for !processGone(pid) && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if !processGone(pid) {
				t.Errorf("process %d (%s) is still running after the call", pid, filepath.Base(file))
			}
		}
	})

	t.Run("a sqlite3 that writes 10 MiB is cut at 4 MiB", func(t *testing.T) {
		fakeSQLite(t, "head -c 10485760 /dev/zero | tr '\\000' x")
		_, s := boundedWorld(t)
		res := list(t, s)
		for _, store := range []string{StoreCursorAgent, StoreCursorIDE} {
			if src := sourceOf(t, res, store); src.State != StateFailed || !strings.Contains(src.Cause, "more than 4 MiB") {
				t.Errorf("%s source %+v; want failed, more than 4 MiB", store, src)
			}
		}
		if src := sourceOf(t, res, StoreClaudeCode); src.State != StateRead {
			t.Errorf("claude-code source %+v; want read", src)
		}
		if _, ok := byID(res, "claude-chat"); !ok {
			t.Error("the Claude Code chat is not listed")
		}
		if a, ok := byID(res, "agent-chat"); !ok || a.Title != "" {
			t.Errorf("agent-chat %+v, %v; want listed, untitled", a, ok)
		}
	})
}

// Decision (ADR-0127 §3): without `sqlite3` on PATH both Cursor stores are a failed source
// with that cause and Claude Code's is read; a cursor-agent chat that has a meta.json is
// still listed (untitled), one that is known only from its database is not, and the IDE gives
// no chat. Reversed (a missing sqlite3 reported as absent, or as an error of the call) the
// sources read absent, or the call fails and the Claude Code chat is lost.
func TestChatsWithoutSqlite3(t *testing.T) {
	needSQLite(t)
	r := newRepo(t)
	f := newFixture(t)
	f.known = []string{r.main}
	f.write(r.main, "claude-chat", f.next(), userLine(r.main), aiTitle("Claude title"))
	f.agentChat(r.main, "agent-meta", "Named", agentMetaJSON(r.main, 1_790_000_000_000, true))
	f.agentChat(r.main, "agent-old", "Old", "")
	f.ideDB(ideSeed{id: "i-1", name: "IDE", folder: r.main, t: 1_790_000_000_000})

	t.Setenv("PATH", t.TempDir())
	res := list(t, f.service())
	if src := sourceOf(t, res, StoreClaudeCode); src.State != StateRead {
		t.Errorf("claude-code source %+v; want read", src)
	}
	for _, store := range []string{StoreCursorAgent, StoreCursorIDE} {
		if src := sourceOf(t, res, store); src.State != StateFailed || src.Cause != "sqlite3 not found on PATH" {
			t.Errorf("%s source %+v; want failed, sqlite3 not found on PATH", store, src)
		}
	}
	if got := strings.Join(ids(res), ","); got != "claude-chat,agent-meta" && got != "agent-meta,claude-chat" {
		t.Errorf("chats %q; want the Claude Code chat and the cursor-agent chat with a meta.json, untitled", got)
	}
	if a, _ := byID(res, "agent-meta"); a.Title != "" {
		t.Errorf("agent-meta title %q; want none without sqlite3", a.Title)
	}
	if got := f.starts.Load(); got != 0 {
		t.Errorf("%d sqlite3 started; want none", got)
	}
}
