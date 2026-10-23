package chats

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The Cursor tests build real SQLite databases with the host's `sqlite3`, in the shape
// plan m3c step 1 recorded and with synthetic values, and read them through the reader. They
// skip, naming why, where sqlite3 is not on PATH. Nothing touches the real ~/.cursor or the
// IDE's own directory.

func needSQLite(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 is not on PATH: these tests build and read real SQLite databases with it")
	}
}

// sqlRun runs script against the database file db, creating it, with the real sqlite3.
func sqlRun(t *testing.T, db, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sqlite3", db)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3 %s: %v\n%s", db, err, out)
	}
}

// sq is s as a SQL literal: the test's own, not the reader's.
func sq(s string) string { return "'" + strings.Replace(s, "'", "''", -1) + "'" }

func md5Hex(path string) string {
	sum := md5.Sum([]byte(path))
	return hex.EncodeToString(sum[:])
}

// ---- cursor-agent

// agentChat makes a chat directory of cursor-agent for a working directory: store.db with the
// meta row 0 (hex-encoded JSON holding name; no name when ""), and meta.json when metaJSON
// is not "". It returns the chat directory.
func (f *fixture) agentChat(tree, id, name, metaJSON string) string {
	f.t.Helper()
	dir := filepath.Join(f.cursor, "chats", md5Hex(tree), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	f.setAgentName(dir, name)
	if metaJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(metaJSON), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	return dir
}

// setAgentName creates or rewrites store.db's meta row 0.
func (f *fixture) setAgentName(dir, name string) {
	f.t.Helper()
	body := map[string]any{"agentId": "x", "latestRootBlobId": "y", "mode": "default"}
	if name != "" {
		body["name"] = name
	}
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	sqlRun(f.t, filepath.Join(dir, "store.db"), "PRAGMA journal_mode=wal;\n"+
		"CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);\n"+
		"CREATE TABLE IF NOT EXISTS blobs (id TEXT PRIMARY KEY, data BLOB);\n"+
		"INSERT OR REPLACE INTO meta VALUES ('0', "+sq(hex.EncodeToString(raw))+");\n")
}

func agentMetaJSON(cwd string, updatedMs int64, conversation bool) string {
	return fmt.Sprintf(`{"schemaVersion":1,"createdAtMs":1,"updatedAtMs":%d,"hasConversation":%v,"cwd":%s}`, updatedMs, conversation, quote(cwd))
}

func chatsOf(r Result, store string) []Chat {
	var out []Chat
	for _, c := range r.Chats {
		if c.Store == store {
			out = append(out, c)
		}
	}
	return out
}

func sourceOf(t *testing.T, r Result, store string) Source {
	t.Helper()
	for _, s := range r.Sources {
		if s.Store == store {
			return s
		}
	}
	t.Fatalf("no source for %s in %+v", store, r.Sources)
	return Source{}
}

// Decision (ADR-0127 §2, plan m3c step 1 (b) and step 4): a cursor-agent chat is filed under
// the md5 of its working directory, one directory per candidate (the project's and each live
// worktree's) and no other, so a directory for any other path is never read. The title is
// the `name` in the hex-encoded JSON of store.db's meta row 0; meta.json, when there is one,
// must agree on the cwd, says whether the chat has a conversation and when it was updated;
// without one (older chats) the chat is placed by its directory and dated by its files, and
// a chat with neither a meta.json nor a name says nothing a reader can use and is left out.
// Reversed (a listing of every md5 directory, no cwd check, hasConversation ignored) the
// other-folder, mismatch and no-conversation chats are listed.
func TestCursorAgentChats(t *testing.T) {
	needSQLite(t)
	r := newRepo(t)
	wt := r.worktree(t, "feature")
	f := newFixture(t)
	f.known = []string{r.main}
	const t1, t2 = int64(1_790_000_000_000), int64(1_790_000_100_000)

	f.agentChat(r.main, "a-main", "Main chat", agentMetaJSON(r.main, t1, true))
	f.agentChat(wt, "a-wt", "Worktree chat", agentMetaJSON(wt, t2, true))
	f.agentChat(r.main, "a-noconv", "Empty chat", agentMetaJSON(r.main, t1, false))
	f.agentChat(r.main, "a-wrongcwd", "Elsewhere", agentMetaJSON("/somewhere/else", t1, true))
	f.agentChat(r.main, "a-oldnamed", "Old chat", "")
	f.agentChat(r.main, "a-oldunnamed", "", "")
	f.agentChat(r.main, "a-untitled", "", agentMetaJSON(r.main, t1-1000, true))
	// A directory for a folder the daemon does not know, with a meta.json that would pass.
	other := filepath.Join(f.home, "other-project")
	otherMeta := f.agentChat(other, "a-other", "Other project's", agentMetaJSON(other, t2, true))
	// A title that is hostile: control and bidirectional-format characters, and too long.
	f.agentChat(r.main, "a-dirty", strings.Repeat("é", 200)+"‮\x07", agentMetaJSON(r.main, t1-2000, true))

	s := f.service()
	c := count(s)
	res := list(t, s)
	got := map[string]Chat{}
	for _, ch := range chatsOf(res, StoreCursorAgent) {
		got[ch.ID] = ch
	}
	var names []string
	for id := range got {
		names = append(names, id)
	}
	for _, want := range []string{"a-main", "a-wt", "a-oldnamed", "a-untitled", "a-dirty"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s is not listed (listed: %v)", want, names)
		}
	}
	for _, not := range []string{"a-noconv", "a-wrongcwd", "a-oldunnamed", "a-other"} {
		if _, ok := got[not]; ok {
			t.Errorf("%s is listed; want it left out", not)
		}
	}
	if len(got) != 5 {
		t.Errorf("%d cursor-agent chats %v; want 5", len(got), names)
	}
	if m := got["a-main"]; m.Title != "Main chat" || m.Client != ClientCursor || m.Project != r.main || m.Worktree != "" ||
		!m.UpdatedAt.Equal(time.UnixMilli(t1)) || m.Session != "" {
		t.Errorf("a-main %+v; want Cursor, titled, the main tree, updated at meta.json's updatedAtMs, joined to nothing", m)
	}
	if w := got["a-wt"]; w.Project != r.main || w.Worktree != wt || w.Title != "Worktree chat" {
		t.Errorf("a-wt %+v; want the project's worktree chat", w)
	}
	if o := got["a-oldnamed"]; o.Title != "Old chat" || o.UpdatedAt.IsZero() {
		t.Errorf("a-oldnamed %+v; want titled and dated by its files", o)
	}
	if u := got["a-untitled"]; u.Title != "" {
		t.Errorf("a-untitled %+v; want no title", u)
	}
	if d := got["a-dirty"]; utf8.RuneCountInString(d.Title) != MaxTitle || strings.ContainsAny(d.Title, "‮\x07") {
		t.Errorf("a-dirty title has %d characters and %q; want %d, no control or format characters", utf8.RuneCountInString(d.Title), d.Title, MaxTitle)
	}
	if src := sourceOf(t, res, StoreCursorAgent); src.State != StateRead {
		t.Errorf("source %+v; want cursor-agent read", src)
	}
	if n := c.paths[filepath.Join(otherMeta, "meta.json")]; n != 0 {
		t.Errorf("the unknown folder's chat was read %d times; want never", n)
	}

	// A second call changes nothing and starts no sqlite3: the facts are cached on the files'
	// size and modification time. Renaming a chat changes its store.db, and only that chat
	// is read again.
	started := f.starts.Load()
	c.reset()
	res = list(t, s)
	if f.starts.Load() != started || c.calls != 0 {
		t.Errorf("a second call started %d sqlite3 and read %d files; want none (cached)", f.starts.Load()-started, c.calls)
	}
	f.setAgentName(filepath.Join(f.cursor, "chats", md5Hex(r.main), "a-main"), "Renamed")
	res = list(t, s)
	if a, _ := byID(res, "a-main"); a.Title != "Renamed" {
		t.Errorf("after the rename a-main is %q; want Renamed", a.Title)
	}
	if n := f.starts.Load() - started; n != 1 {
		t.Errorf("the rename started %d sqlite3; want 1 (only the changed chat)", n)
	}
}

// Decision (ADR-0127 §2): at most 30 chats per client and project; reversed all 35 come back.
func TestCursorAgentPerProjectCut(t *testing.T) {
	needSQLite(t)
	f := newFixture(t)
	const p = "/proj/a"
	f.known = []string{p}
	for i := 0; i < 35; i++ {
		id := fmt.Sprintf("c%02d", i)
		dir := f.agentChat(p, id, "T"+id, agentMetaJSON(p, 1_790_000_000_000+int64(i), true))
		mt := f.next()
		for _, name := range []string{"store.db", "store.db-wal"} {
			if err := os.Chtimes(filepath.Join(dir, name), mt, mt); err != nil && name == "store.db" {
				t.Fatal(err)
			}
		}
	}
	res := list(t, f.service())
	if got := len(chatsOf(res, StoreCursorAgent)); got != MaxPerProject {
		t.Errorf("%d chats; want %d", got, MaxPerProject)
	}
	if _, ok := byID(res, "c34"); !ok {
		t.Error("the newest chat c34 is not listed")
	}
	if _, ok := byID(res, "c00"); ok {
		t.Error("the oldest chat c00 is listed; the cut keeps the newest")
	}
}

// ---- Cursor IDE

type ideSeed struct {
	id, name, folder string
	archived         bool
	subagent         bool
	draft            bool
	noFolder         bool
	t                int64
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ideDB adds the composers to the IDE's state.vscdb (creating it in WAL mode with the
// recorded table): composerHeaders(workspaceId, isArchived, isSubagent, lastUpdatedAt, value).
func (f *fixture) ideDB(seeds ...ideSeed) string {
	f.t.Helper()
	db := filepath.Join(f.ide, "state.vscdb")
	var sb strings.Builder
	sb.WriteString("PRAGMA journal_mode=wal;\nCREATE TABLE IF NOT EXISTS composerHeaders (workspaceId TEXT, isArchived INTEGER, isSubagent INTEGER, lastUpdatedAt INTEGER, value TEXT);\n")
	for _, s := range seeds {
		v := map[string]any{"composerId": s.id, "isDraft": s.draft}
		if s.name != "" {
			v["name"] = s.name
		}
		if !s.noFolder {
			v["workspaceIdentifier"] = map[string]any{"id": "w1", "uri": map[string]any{"fsPath": s.folder, "scheme": "file"}}
		}
		raw, err := json.Marshal(v)
		if err != nil {
			f.t.Fatal(err)
		}
		fmt.Fprintf(&sb, "INSERT INTO composerHeaders VALUES ('w1', %d, %d, %d, %s);\n", b2i(s.archived), b2i(s.subagent), s.t, sq(string(raw)))
	}
	sqlRun(f.t, db, sb.String())
	return db
}

// Decision (ADR-0127 §2, plan m3c step 1 (c)): the IDE's composers are filtered in SQL to the
// candidate paths, the project's own and its live worktrees', by the folder each row carries;
// another folder's composer, an archived one, a subagent's and a draft are not listed, nor a
// row with no folder. Reversed (no folder filter, or the flags ignored) the other folder's and
// the flagged rows are listed.
func TestCursorIDEChatsOfKnownRootsOnly(t *testing.T) {
	needSQLite(t)
	r := newRepo(t)
	wt := r.worktree(t, "feature")
	f := newFixture(t)
	f.known = []string{r.main}
	other := filepath.Join(f.home, "other-project")
	const base = int64(1_790_000_000_000)
	f.ideDB(
		ideSeed{id: "i-main", name: "Main composer", folder: r.main, t: base + 1},
		ideSeed{id: "i-wt", name: "Worktree composer", folder: wt, t: base + 3},
		ideSeed{id: "i-other", name: "Other folder", folder: other, t: base + 4},
		ideSeed{id: "i-archived", name: "Archived", folder: r.main, archived: true, t: base + 5},
		ideSeed{id: "i-subagent", name: "Subagent", folder: r.main, subagent: true, t: base + 6},
		ideSeed{id: "i-draft", name: "Draft", folder: r.main, draft: true, t: base + 7},
		ideSeed{id: "i-nofolder", name: "No folder", noFolder: true, t: base + 8},
		ideSeed{id: "i-untitled", folder: r.main, t: base + 2},
	)
	s := f.service()
	res := list(t, s)
	got := chatsOf(res, StoreCursorIDE)
	var order []string
	for _, c := range got {
		order = append(order, c.ID)
	}
	if want := "i-wt,i-untitled,i-main"; strings.Join(order, ",") != want {
		t.Fatalf("IDE chats %v; want %s (newest first: the project's and its worktree's only, none archived, subagent, draft, folderless or from another folder)", order, want)
	}
	if w := got[0]; w.Title != "Worktree composer" || w.Worktree != wt || w.Project != r.main || w.Client != ClientCursor ||
		!w.UpdatedAt.Equal(time.UnixMilli(base+3)) || w.Session != "" {
		t.Errorf("i-wt %+v; want titled, in the worktree of the project, dated by lastUpdatedAt, unjoined", w)
	}
	if m := got[2]; m.Worktree != "" || m.Title != "Main composer" {
		t.Errorf("i-main %+v; want the main tree's", m)
	}
	if got[1].Title != "" {
		t.Errorf("i-untitled has title %q; want none", got[1].Title)
	}
	if src := sourceOf(t, res, StoreCursorIDE); src.State != StateRead {
		t.Errorf("source %+v; want cursor-ide read", src)
	}

	// One query a call is cached on the database's and -wal's size and mtime: an unchanged
	// store starts no sqlite3; a new composer does.
	started := f.starts.Load()
	res = list(t, s)
	if f.starts.Load() != started || len(chatsOf(res, StoreCursorIDE)) != 3 {
		t.Errorf("a second call started %d sqlite3 and listed %d; want none and 3 (cached)", f.starts.Load()-started, len(chatsOf(res, StoreCursorIDE)))
	}
	f.ideDB(ideSeed{id: "i-new", name: "New", folder: r.main, t: base + 9})
	res = list(t, s)
	if f.starts.Load() == started || len(chatsOf(res, StoreCursorIDE)) != 4 {
		t.Errorf("after a new composer %d sqlite3 started and %d listed; want a fresh read and 4", f.starts.Load()-started, len(chatsOf(res, StoreCursorIDE)))
	}
}

// Decision (ADR-0127 §2, plan m3c step 1 (c)): the folder filter is in the query, so the
// database never returns another folder's composer (its title does not even reach the
// daemon); the reader's own check afterwards is a second line. Reversed (the IN clause
// dropped) the query's raw answer holds the other folders' rows.
func TestCursorIDEFiltersInSQL(t *testing.T) {
	needSQLite(t)
	f := newFixture(t)
	db := f.ideDB(
		ideSeed{id: "i-mine", name: "Mine", folder: "/proj/a", t: 1},
		ideSeed{id: "i-theirs", name: "Theirs", folder: "/proj/b", t: 2},
		ideSeed{id: "i-nofolder", name: "Nowhere", noFolder: true, t: 3},
	)
	out, err := runner().query(context.Background(), db, ideQuery([]string{"/proj/a"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "i-mine") || strings.Contains(string(out), "i-theirs") || strings.Contains(string(out), "i-nofolder") {
		t.Errorf("the query answered %s; want only i-mine", out)
	}
}

// Decision (ADR-0127 §2): at most 30 chats per project, the newest; reversed all 35 come back.
func TestCursorIDEPerProjectCut(t *testing.T) {
	needSQLite(t)
	f := newFixture(t)
	const p = "/proj/a"
	f.known = []string{p}
	var seeds []ideSeed
	for i := 0; i < 35; i++ {
		seeds = append(seeds, ideSeed{id: fmt.Sprintf("i%02d", i), folder: p, t: 1_790_000_000_000 + int64(i)})
	}
	f.ideDB(seeds...)
	res := list(t, f.service())
	if got := len(chatsOf(res, StoreCursorIDE)); got != MaxPerProject {
		t.Errorf("%d chats; want %d", got, MaxPerProject)
	}
	if _, ok := byID(res, "i34"); !ok {
		t.Error("the newest i34 is not listed")
	}
	if _, ok := byID(res, "i00"); ok {
		t.Error("the oldest i00 is listed; the cut keeps the newest")
	}
}

// Decision (ADR-0127 §3): the candidate paths reach the query as SQL literals with every ' doubled.
// A project whose path holds a quote is listed with its own rows, and a path written to break
// out of the literal matches nothing instead of everything. Reversed (the quote not doubled)
// the first is a SQL error (the source fails) and the second lists every other folder's rows.
func TestCursorIDEQuotedPaths(t *testing.T) {
	needSQLite(t)
	f := newFixture(t)
	const tick = "/proj/it's here"
	const evil = "/x' OR '1'='1"
	f.ideDB(
		ideSeed{id: "i-tick", name: "In the quoted folder", folder: tick, t: 1_790_000_000_001},
		ideSeed{id: "i-plain", name: "Plain", folder: "/proj/plain", t: 1_790_000_000_002},
	)

	f.known = []string{tick}
	res := list(t, f.service())
	if src := sourceOf(t, res, StoreCursorIDE); src.State != StateRead {
		t.Fatalf("source %+v; want read (the quote is doubled)", src)
	}
	if got := ids(Result{Chats: chatsOf(res, StoreCursorIDE)}); strings.Join(got, ",") != "i-tick" {
		t.Errorf("chats %v; want only i-tick", got)
	}

	f.known = []string{evil}
	res = list(t, f.service())
	if src := sourceOf(t, res, StoreCursorIDE); src.State != StateRead {
		t.Fatalf("source %+v; want read", src)
	}
	if got := chatsOf(res, StoreCursorIDE); len(got) != 0 {
		t.Errorf("a path written as SQL listed %v; want nothing", ids(Result{Chats: got}))
	}
}
