package chats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Cursor IDE keeps its chats (composers) in globalStorage/state.vscdb, a WAL SQLite
// database whose `composerHeaders` table has one row per composer: the columns isArchived,
// isSubagent and lastUpdatedAt, and a JSON `value` holding the title (`name`), `isDraft`,
// and the folder it was opened on (`workspaceIdentifier.uri.fsPath`) (plan m3c step 1 (c)).
// The folder is in the row, so the candidate paths filter in SQL and workspaceStorage is
// not read. [Assumed, not recorded by step 1: the composer's id is `composerId` in `value`;
// a row without one is left out.]

// IDEGlobalStorage is the directory the Cursor IDE keeps state.vscdb in, for a home
// directory: ~/Library/Application Support/Cursor/User/globalStorage on macOS,
// ~/.config/Cursor/User/globalStorage elsewhere.
func IDEGlobalStorage(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage")
	}
	return filepath.Join(home, ".config", "Cursor", "User", "globalStorage")
}

// maxIDERows bounds the rows one query returns, newest first.
const maxIDERows = 5000

const (
	ideQueryHead = `SELECT json_extract(value, '$.composerId') AS id, json_extract(value, '$.name') AS name, ` +
		`json_extract(value, '$.workspaceIdentifier.uri.fsPath') AS path, lastUpdatedAt AS t FROM composerHeaders ` +
		`WHERE COALESCE(isArchived, 0) = 0 AND COALESCE(isSubagent, 0) = 0 ` +
		`AND COALESCE(json_extract(value, '$.isDraft'), 0) = 0 ` +
		`AND json_extract(value, '$.workspaceIdentifier.uri.fsPath') IN (`
	ideQueryTail = `) ORDER BY lastUpdatedAt DESC LIMIT 5000`
)

// ideQuery is the one query run against the IDE's store: constants, and the candidate paths
// as SQL literals. A composer of any other folder is not returned by the database at all.
func ideQuery(paths []string) string {
	lits := make([]string, len(paths))
	for i, p := range paths {
		lits[i] = sqlLiteral(p)
	}
	return ideQueryHead + strings.Join(lits, ", ") + ideQueryTail
}

// ideRow is one row of the query: the only values read.
type ideRow struct {
	ID   *string `json:"id"`
	Name *string `json:"name"`
	Path *string `json:"path"`
	T    any     `json:"t"`
}

// ideCache is the last result, valid while the candidate paths and the database's and
// -wal's size and modification time are those it was read for.
type ideCache struct {
	key   string
	chats []rawChat
}

// readIDE reads the Cursor IDE's store: its chats and how the read went. The error is the
// call's context's, when it ended.
func (s *service) readIDE(ctx context.Context) ([]rawChat, Source, error) {
	src := func(state, cause string) Source { return Source{Store: StoreCursorIDE, State: state, Cause: cause} }
	if !filepath.IsAbs(s.o.CursorIDE) {
		return nil, src(StateFailed, "the Cursor IDE's directory cannot be located (no home directory)"), nil
	}
	db := filepath.Join(s.o.CursorIDE, "state.vscdb")
	fi, err := os.Lstat(db)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, src(StateAbsent, ""), nil
		}
		return nil, src(StateFailed, "cannot read the Cursor IDE's store: "+osCause(err)), nil
	}
	if !fi.Mode().IsRegular() {
		return nil, src(StateFailed, "the Cursor IDE's store is not a regular file"), nil
	}
	byPath := map[string]candidate{}
	var paths []string
	for _, cands := range s.projects(ctx) {
		for _, c := range cands {
			p := filepath.Clean(c.path())
			if _, dup := byPath[p]; dup || strings.ContainsRune(p, 0) {
				continue
			}
			byPath[p] = c
			paths = append(paths, p)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, Source{}, err
	}
	if len(paths) == 0 {
		return nil, src(StateRead, ""), nil
	}
	sort.Strings(paths)
	key := strings.Join(paths, "\x00") + "\x01" + statSig(db) + "\x01" + statSig(db+"-wal")
	s.mu.Lock()
	cached := s.ide
	s.mu.Unlock()
	if cached.key == key {
		return cached.chats, src(StateRead, ""), nil
	}

	out, err := s.sql.query(ctx, db, ideQuery(paths))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, Source{}, cerr
		}
		if errors.Is(err, errNoSQLite) {
			return nil, src(StateFailed, err.Error()), nil
		}
		return nil, src(StateFailed, "cannot read the Cursor IDE's store: "+osCause(err)), nil
	}
	var rows []ideRow
	if len(strings.TrimSpace(string(out))) > 0 {
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, src(StateFailed, "the Cursor IDE's store answered in a form this reader does not know"), nil
		}
	}
	var chats []rawChat
	seen := map[string]bool{}
	perProject := map[string]int{}
	// The query is newest first; the cut per project keeps the newest.
	for _, r := range rows {
		if r.ID == nil || r.Path == nil || !idPattern.MatchString(*r.ID) || seen[*r.ID] {
			continue
		}
		c, ok := byPath[filepath.Clean(*r.Path)]
		if !ok || perProject[c.project] >= MaxPerProject {
			continue
		}
		seen[*r.ID] = true
		perProject[c.project]++
		updated := fi.ModTime().UTC()
		if ms, ok := millis(r.T); ok && ms > 0 {
			updated = time.UnixMilli(ms).UTC()
		}
		title := ""
		if r.Name != nil {
			title = cleanTitle(*r.Name)
		}
		chats = append(chats, rawChat{
			store: StoreCursorIDE, id: *r.ID, project: c.project, worktree: c.worktree,
			title: title, updated: updated,
		})
	}
	s.mu.Lock()
	s.ide = ideCache{key: key, chats: chats}
	s.mu.Unlock()
	return chats, src(StateRead, ""), nil
}

// statSig is the size and modification time of a file, or "-" when it is not there.
func statSig(path string) string {
	fi, err := os.Lstat(path)
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}

// millis is a time in milliseconds as sqlite3's JSON may hold it: a number, or text.
func millis(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return int64(f), err == nil
	}
	return 0, false
}
