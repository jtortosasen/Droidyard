package chats

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// cursor-agent keeps each chat in a directory of its own,
// <Dirs.Cursor>/chats/<md5 hex of the working directory>/<chat id>/, holding store.db (a WAL
// SQLite database) and, in most chats, meta.json (plan m3c step 1 (b)). The title is not in
// meta.json: it is `name` in the JSON that store.db's `meta` row `0` holds hex-encoded. A
// chat's place is the md5 directory it is filed under, one directory per candidate path, so
// no other project's directory is listed; meta.json's `cwd` only has to agree.

// maxMeta bounds the read of a chat's meta.json: it holds five small keys.
const maxMeta = 64 << 10

// agentTitleQuery is the one query run against a chat's store.db: a constant. The row is
// skipped when it is larger than anything a title needs.
const agentTitleQuery = `SELECT value FROM meta WHERE key = '0' AND length(value) <= 131072 LIMIT 1`

// agentMeta holds the only keys of meta.json that are decoded.
type agentMeta struct {
	Cwd             string  `json:"cwd"`
	UpdatedAtMs     float64 `json:"updatedAtMs"`
	HasConversation *bool   `json:"hasConversation"`
}

// agentNameRow holds the only key of store.db's meta row that is decoded.
type agentNameRow struct {
	Name string `json:"name"`
}

// agentFacts is what is learned from one chat directory: the cache's value, valid for the
// signature of the files it was learned from.
type agentFacts struct {
	sig     string
	keep    bool
	title   string
	updated time.Time
}

// agentRef is one chat directory found under a candidate's md5 directory.
type agentRef struct {
	dir   string
	id    string
	db    string
	mtime time.Time // the newer of store.db and its -wal: ranks the directory
	sig   string    // size and mtime of store.db and -wal
	cand  candidate
}

// scanAgent lists the candidates' chat directories and reads the ones the result needs.
func (s *service) scanAgent(ctx context.Context, acc *accum) {
	if !filepath.IsAbs(s.o.Dirs.Cursor) {
		acc.fail("cursor-agent's directory cannot be located (no home directory)")
		return
	}
	base := filepath.Join(s.o.Dirs.Cursor, "chats")
	if _, err := os.Stat(base); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			acc.setAbsent()
		} else {
			acc.fail("cannot read cursor-agent's chats directory: " + osCause(err))
		}
		return
	}
	noSQLite := false
	for _, cands := range s.projects(ctx) {
		var refs []agentRef
		for _, c := range cands {
			sum := md5.Sum([]byte(filepath.Clean(c.path())))
			list, err := listAgentChats(filepath.Join(base, hex.EncodeToString(sum[:])), c)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					acc.fail("cannot read a project's cursor-agent chats: " + osCause(err))
				}
				continue
			}
			refs = append(refs, list...)
		}
		sort.Slice(refs, func(i, j int) bool {
			if !refs[i].mtime.Equal(refs[j].mtime) {
				return refs[i].mtime.After(refs[j].mtime)
			}
			return refs[i].id < refs[j].id
		})
		kept := 0
		for _, ref := range refs {
			if kept == MaxPerProject || ctx.Err() != nil {
				break
			}
			f, ok := s.agentFacts(ctx, acc, ref, &noSQLite)
			if !ok || !f.keep {
				continue
			}
			kept++
			acc.add(rawChat{
				store: StoreCursorAgent, id: ref.id, project: ref.cand.project, worktree: ref.cand.worktree,
				title: f.title, updated: f.updated,
			})
		}
	}
}

// listAgentChats lists the chat directories of one md5 directory that hold a regular
// store.db, bounded as the Claude Code listing is.
func listAgentChats(dir string, c candidate) ([]agentRef, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.ReadDir(maxDirEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxDirEntries {
		return nil, errors.New("more chats than can be listed")
	}
	var out []agentRef
	for _, e := range entries {
		// Type() is the entry's own kind: a link to a directory is not one.
		if !e.Type().IsDir() || !idPattern.MatchString(e.Name()) {
			continue
		}
		chat := filepath.Join(dir, e.Name())
		db := filepath.Join(chat, "store.db")
		fi, err := os.Lstat(db)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		ref := agentRef{dir: chat, id: e.Name(), db: db, mtime: fi.ModTime().UTC(), cand: c}
		ref.sig = fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
		if wi, err := os.Lstat(db + "-wal"); err == nil {
			ref.sig += fmt.Sprintf("|%d:%d", wi.Size(), wi.ModTime().UnixNano())
			if wi.ModTime().After(ref.mtime) {
				ref.mtime = wi.ModTime().UTC()
			}
		}
		out = append(out, ref)
	}
	return out, nil
}

// agentFacts is what one chat directory says: from the cache while store.db, its -wal and
// meta.json are as they were, else read (a read of the call's budget for meta.json, another
// for the title). false when the budget is spent.
func (s *service) agentFacts(ctx context.Context, acc *accum, ref agentRef, noSQLite *bool) (agentFacts, bool) {
	metaPath := filepath.Join(ref.dir, "meta.json")
	hasMeta := false
	sig := ref.sig
	if fi, err := os.Lstat(metaPath); err == nil && fi.Mode().IsRegular() && fi.Size() <= maxMeta {
		hasMeta = true
		sig += fmt.Sprintf("|m%d:%d", fi.Size(), fi.ModTime().UnixNano())
	}
	s.mu.Lock()
	f, ok := s.agents[ref.dir]
	s.mu.Unlock()
	if ok && f.sig == sig {
		return f, true
	}
	f = agentFacts{sig: sig, updated: ref.mtime.Truncate(time.Millisecond)}
	if hasMeta {
		// A read of this file from an earlier call that has not returned: not started again.
		if !s.begin(metaPath) {
			acc.markTruncated()
			return agentFacts{}, false
		}
		if !acc.tryRead() {
			s.end(metaPath)
			return agentFacts{}, false
		}
		var m agentMeta
		data, err := s.readAt(metaPath, 0, maxMeta+1)
		s.end(metaPath)
		if err != nil || len(data) > maxMeta || json.Unmarshal(data, &m) != nil {
			hasMeta = false
		} else {
			if (m.HasConversation != nil && !*m.HasConversation) || filepath.Clean(m.Cwd) != filepath.Clean(ref.cand.path()) {
				f.keep = false
				s.putAgent(ref.dir, f)
				return f, true
			}
			if m.UpdatedAtMs > 0 {
				f.updated = time.UnixMilli(int64(m.UpdatedAtMs)).UTC()
			}
		}
	}
	cacheable := true
	if *noSQLite {
		cacheable = false
	} else {
		if !acc.tryRead() {
			return agentFacts{}, false
		}
		out, err := s.sql.query(ctx, ref.db, agentTitleQuery)
		switch {
		case err == nil:
			f.title = agentTitle(out)
		case ctx.Err() != nil:
			return agentFacts{}, false
		default:
			if errors.Is(err, errNoSQLite) {
				*noSQLite = true
				acc.fail(err.Error())
			} else {
				acc.fail("cannot read a cursor-agent chat's title: " + osCause(err))
			}
			cacheable = false
		}
	}
	// A chat the client shows has a title or a meta.json; one with neither has said nothing
	// this reader can place (plan m3c step 1 (b)).
	f.keep = hasMeta || f.title != ""
	if cacheable {
		s.putAgent(ref.dir, f)
	}
	return f, true
}

func (s *service) putAgent(dir string, f agentFacts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, present := s.agents[dir]; !present && len(s.agents) >= MaxCache {
		for k := range s.agents {
			delete(s.agents, k)
			break
		}
	}
	s.agents[dir] = f
}

// agentTitle is the title in sqlite3's JSON for agentTitleQuery: the `name` of the JSON that
// the one row holds hex-encoded. "" when there is no row or the value is not what is known.
func agentTitle(out []byte) string {
	var rows []struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(out, &rows) != nil || len(rows) == 0 {
		return ""
	}
	raw, err := hex.DecodeString(strings.TrimSpace(rows[0].Value))
	if err != nil {
		return ""
	}
	var n agentNameRow
	if json.Unmarshal(raw, &n) != nil {
		return ""
	}
	return cleanTitle(n.Name)
}
