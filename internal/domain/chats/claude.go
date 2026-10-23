package chats

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// Claude Code keeps one JSONL transcript per chat at
// <config dir>/projects/<working directory, every byte but [A-Za-z0-9] as '-']/<chat id>.jsonl,
// beside a directory of the same name for the chat's side files (ADR-0127 §2; plan m3c
// step 1 for what was observed). A transcript moves with the chat's working directory, so
// the directory that holds a file, not the lines inside it, says which tree the chat is
// in: the lines only settle the encoding, which is lossy ('/' and '-' both become '-').

// idPattern is a chat id: what the published schema says, and a safe file stem. It cannot
// start with '-': the id is copied into a resume command (`claude --resume <id>`), where a
// leading '-' would be read as an option.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// maxCwd bounds the working directory kept from a line: no path is longer, and a longer
// value would make a prefix test unsound.
const maxCwd = 4096

// The keys the reader decodes, and no others (A3: no message field is ever decoded into a
// value). A line is decoded only when it begins with one of the type prefixes below or
// holds the quoted key it is read for.
var (
	prefixCustom = []byte(`{"type":"custom-title"`)
	prefixAI     = []byte(`{"type":"ai-title"`)
	prefixAgent  = []byte(`{"type":"agent-name"`)
	prefixCont   = []byte(`{"type":"continued-in"`)
	keyCwd       = []byte(`"cwd":"`)
	keyEntry     = []byte(`"entrypoint":"`)
)

// titleLine holds the keys of the four line types that are decoded for what they say.
type titleLine struct {
	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
	AgentName   string `json:"agentName"`
	ContinuedIn string `json:"continuedInSessionId"`
}

// placeLine holds the two keys any line may carry that the reader wants: where the chat
// was working and how the client was started.
type placeLine struct {
	Cwd        string `json:"cwd"`
	Entrypoint string `json:"entrypoint"`
}

// facts is what is learned from one transcript: the cache's value.
type facts struct {
	size  int64
	mtime int64 // UnixNano
	// title is the chat's title by custom-title, else the last ai-title, else agent-name,
	// cleaned and cut; "" when none.
	title string
	// continuedIn is the id of the chat this one was continued in, if it says so.
	continuedIn string
	// cwd is the working directory of the last line that carries one in either window;
	// hasCwd is false when no line does, which is a transcript with no conversation.
	cwd    string
	hasCwd bool
	// sdk is true when the first line that carries an entrypoint in the head window says
	// sdk-cli: a headless `claude -p` run, which the client's resume list does not show
	// (plan m3c step 1 (f): 49 of 80 here, none titled).
	sdk bool
}

// candidate is a tree whose chats are looked for: a main tree, or a linked worktree of it.
type candidate struct {
	project  string // the main tree
	worktree string // the linked worktree; "" for the main tree
}

// path is the tree the candidate stands for.
func (c candidate) path() string {
	if c.worktree != "" {
		return c.worktree
	}
	return c.project
}

// fileRef is one transcript found in a candidate's directory.
type fileRef struct {
	path  string
	id    string
	mtime time.Time
	size  int64
	cand  candidate
}

// rawChat is a chat the reader kept, before the result is assembled.
type rawChat struct {
	store                                     string
	id, project, worktree, title, continuedIn string
	updated                                   time.Time
}

// claudeRun is what one call learned from one store (named for the first, Claude Code's).
type claudeRun struct {
	chats     []rawChat
	source    Source
	truncated bool
}

// accum collects a call's findings while the scan may still be running when the call
// answers: the scan appends under the lock, and the call takes a snapshot and closes it, so
// a scan blocked in a read can never change a result already returned.
type accum struct {
	store     string      // the store the findings come from, named in its source
	budget    *readBudget // the call's reads, shared by the three stores
	mu        sync.Mutex
	closed    bool
	chats     []rawChat
	truncated bool
	absent    bool
	cause     string
}

func (a *accum) add(c rawChat) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.chats = append(a.chats, c)
	}
}

// readBudget is the reads one call may make in all: MaxReads between the three stores. A
// file read, or one sqlite3 run, is one read; a file served from a cache is none.
type readBudget struct {
	mu    sync.Mutex
	reads int
}

func (b *readBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reads >= MaxReads {
		return false
	}
	b.reads++
	return true
}

// tryRead takes one read of the call's budget; false when it is spent, and then the result
// is truncated.
func (a *accum) tryRead() bool {
	if !a.budget.take() {
		a.markTruncated()
		return false
	}
	return true
}

// markTruncated says the store stopped short of everything it could have read.
func (a *accum) markTruncated() {
	a.mu.Lock()
	a.truncated = true
	a.mu.Unlock()
}

func (a *accum) fail(cause string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cause == "" {
		a.cause = cause
	}
}

func (a *accum) setAbsent() {
	a.mu.Lock()
	a.absent = true
	a.mu.Unlock()
}

func (a *accum) snapshot() claudeRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	run := claudeRun{chats: append([]rawChat(nil), a.chats...), truncated: a.truncated}
	switch {
	case a.absent:
		run.source = Source{Store: a.store, State: StateAbsent}
	case a.cause != "":
		run.source = Source{Store: a.store, State: StateFailed, Cause: a.cause}
	default:
		run.source = Source{Store: a.store, State: StateRead}
	}
	return run
}

// service is the reader and its cache.
type service struct {
	o      Options
	budget time.Duration
	// readAt reads up to n bytes of a file at off. It is the one place a transcript is
	// opened; tests replace it to count what is read and to block it.
	readAt func(path string, off int64, n int) ([]byte, error)

	// sql starts the sqlite3 the Cursor stores are read through.
	sql sqliteRunner

	mu       sync.Mutex
	inflight map[string]*reading // files a read of which has begun and not returned
	cache    map[string]facts
	agents   map[string]agentFacts // cursor-agent chat directories, by path
	ide      ideCache              // the Cursor IDE's last result
}

func newService(o Options) *service {
	return &service{
		o: o, budget: Budget, readAt: readFileAt,
		sql:   sqliteRunner{start: o.Starter, timeout: sqliteTimeout, maxOut: sqliteMaxOut},
		cache: map[string]facts{}, agents: map[string]agentFacts{}, inflight: map[string]*reading{},
	}
}

// readFileAt opens a regular file and reads up to n bytes from off. The kind is checked on
// the open file, so a path swapped for a link between the listing and now is not followed
// into anything but a regular file.
func readFileAt(path string, off int64, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:got], nil
}

// callBudget is what the three stores of one call share: its deadline (Budget) and its reads
// (MaxReads in all).
type callBudget struct {
	ctx   context.Context // ends at the call's deadline, or when the caller gives up
	reads *readBudget
}

// reading is the reads of one path in flight: how many, and since when the oldest.
type reading struct {
	n     int
	since time.Time
}

// begin marks path as being read and reports whether a read may start. A read that never
// returns (a file on a stalled mount) is still in flight when its call has answered; a later
// call must not start a second read of the same path behind it, or every call would leave
// one more goroutine stuck. A read in flight for less than a call's budget is an ordinary
// one of an overlapping call (two tabs refreshing on one event), and is no reason to skip.
func (s *service) begin(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.inflight[path]
	if r == nil {
		s.inflight[path] = &reading{n: 1, since: time.Now()}
		return true
	}
	if time.Since(r.since) > s.budget {
		return false
	}
	r.n++
	return true
}

func (s *service) end(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.inflight[path]; r != nil {
		if r.n--; r.n == 0 {
			delete(s.inflight, path)
		}
	}
}

// runStore runs the scan of one store within the call's deadline. The scan runs in its own
// goroutine so a read that blocks cannot hold the call past it: the call then answers with
// what the scan had found and says truncated. (The caller's own cancellation is the
// caller's to check.)
func (s *service) runStore(c callBudget, store string, scan func(context.Context, *accum)) claudeRun {
	acc := &accum{store: store, budget: c.reads}
	done := make(chan struct{})
	go func() {
		defer close(done)
		scan(c.ctx, acc)
	}()
	select {
	case <-done:
	case <-c.ctx.Done():
	}
	finished := false
	select {
	case <-done:
		finished = true
	default:
	}
	run := acc.snapshot()
	if !finished {
		run.truncated = true
	}
	return run
}

// projects are the trees whose chats are looked for, a group per project the daemon knows:
// its main tree first, then its live linked worktrees (ADR-0089 §2, no git). It stops when
// ctx ends.
func (s *service) projects(ctx context.Context) [][]candidate {
	var known []string
	if s.o.Known != nil {
		known = s.o.Known()
	}
	var out [][]candidate
	for _, main := range known {
		if ctx.Err() != nil {
			break
		}
		if !filepath.IsAbs(main) {
			continue
		}
		cands := []candidate{{project: main}}
		for _, w := range platform.Worktrees(s.o.Env, main) {
			cands = append(cands, candidate{project: main, worktree: w.Root})
		}
		out = append(out, cands)
	}
	return out
}

// scanClaude lists the candidates' transcripts and reads the ones the result needs.
func (s *service) scanClaude(ctx context.Context, acc *accum) {
	if !filepath.IsAbs(s.o.Dirs.Claude) {
		acc.fail("Claude Code's configuration directory cannot be located (no home directory, or CLAUDE_CONFIG_DIR is not an absolute path)")
		return
	}
	base := filepath.Join(s.o.Dirs.Claude, "projects")
	if _, err := os.Stat(base); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			acc.setAbsent()
		} else {
			acc.fail("cannot read Claude Code's projects directory: " + osCause(err))
		}
		return
	}
	for _, cands := range s.projects(ctx) {
		var refs []fileRef
		for _, c := range cands {
			dir := filepath.Join(base, encodePath(c.path()))
			list, err := listTranscripts(dir, c)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					acc.fail("cannot read a project's chat directory: " + osCause(err))
				}
				continue
			}
			refs = append(refs, list...)
		}
		s.readProject(ctx, acc, refs)
	}
}

// readProject keeps the newest MaxPerProject transcripts of one project that are chats.
func (s *service) readProject(ctx context.Context, acc *accum, refs []fileRef) {
	sort.Slice(refs, func(i, j int) bool {
		if !refs[i].mtime.Equal(refs[j].mtime) {
			return refs[i].mtime.After(refs[j].mtime)
		}
		return refs[i].id < refs[j].id
	})
	kept := 0
	for _, ref := range refs {
		if kept == MaxPerProject || ctx.Err() != nil {
			return
		}
		f, ok := s.factsOf(acc, ref)
		if !ok || f.sdk || !f.hasCwd || !within(f.cwd, ref.cand.path()) {
			continue
		}
		kept++
		acc.add(rawChat{
			store: StoreClaudeCode, id: ref.id, project: ref.cand.project, worktree: ref.cand.worktree,
			title: f.title, continuedIn: f.continuedIn, updated: ref.mtime,
		})
	}
}

// listTranscripts lists the top-level regular *.jsonl files of dir whose stem is a chat id.
// Subagent transcripts live below a chat's own directory and are not top-level; links and
// pipes are not regular files and are left out.
func listTranscripts(dir string, c candidate) ([]fileRef, error) {
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
	var out []fileRef
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok || !idPattern.MatchString(stem) {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, fileRef{
			path: filepath.Join(dir, e.Name()), id: stem, mtime: fi.ModTime().UTC().Truncate(time.Millisecond),
			size: fi.Size(), cand: c,
		})
	}
	return out, nil
}

// factsOf is what the transcript says: from the cache when its size and modification time
// are those listed, else read (one of the call's reads). false when the budget is spent or
// the file cannot be read.
func (s *service) factsOf(acc *accum, ref fileRef) (facts, bool) {
	key := ref.path
	mtime := ref.mtime.UnixNano()
	s.mu.Lock()
	f, ok := s.cache[key]
	s.mu.Unlock()
	if ok && f.size == ref.size && f.mtime == mtime {
		return f, true
	}
	// A read of this file from an earlier call that has not returned: not started again.
	if !s.begin(key) {
		acc.markTruncated()
		return facts{}, false
	}
	defer s.end(key)
	if !acc.tryRead() {
		return facts{}, false
	}
	f, err := s.readFacts(ref)
	if err != nil {
		return facts{}, false
	}
	f.size, f.mtime = ref.size, mtime
	s.mu.Lock()
	if _, present := s.cache[key]; !present && len(s.cache) >= MaxCache {
		for k := range s.cache {
			delete(s.cache, k)
			break
		}
	}
	s.cache[key] = f
	s.mu.Unlock()
	return f, true
}

// readFacts reads the two windows of a transcript. A file no longer than both is read whole.
func (s *service) readFacts(ref fileRef) (facts, error) {
	var f facts
	var titles struct{ custom, ai, agent string }
	var entrypointSeen bool
	scan := func(lines []byte, head bool) {
		for len(lines) > 0 {
			var line []byte
			if i := bytes.IndexByte(lines, '\n'); i >= 0 {
				line, lines = lines[:i], lines[i+1:]
			} else {
				line, lines = lines, nil
			}
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				continue
			}
			// Only the four line types named are decoded for what they say, into a struct
			// that holds only their keys.
			if bytes.HasPrefix(line, []byte(`{"type":"`)) {
				var t titleLine
				switch {
				case bytes.HasPrefix(line, prefixCustom):
					if json.Unmarshal(line, &t) == nil {
						titles.custom = last(titles.custom, cleanTitle(t.CustomTitle))
					}
				case bytes.HasPrefix(line, prefixAI):
					if json.Unmarshal(line, &t) == nil {
						titles.ai = last(titles.ai, cleanTitle(t.AITitle))
					}
				case bytes.HasPrefix(line, prefixAgent):
					if json.Unmarshal(line, &t) == nil {
						titles.agent = last(titles.agent, cleanTitle(t.AgentName))
					}
				case bytes.HasPrefix(line, prefixCont):
					if json.Unmarshal(line, &t) == nil && idPattern.MatchString(t.ContinuedIn) {
						f.continuedIn = t.ContinuedIn
					}
				}
			}
			wantEntry := head && !entrypointSeen && bytes.Contains(line, keyEntry)
			if wantEntry || bytes.Contains(line, keyCwd) {
				var p placeLine
				if json.Unmarshal(line, &p) != nil {
					continue
				}
				if p.Cwd != "" && len(p.Cwd) <= maxCwd {
					f.cwd, f.hasCwd = p.Cwd, true
				}
				if wantEntry && p.Entrypoint != "" {
					entrypointSeen = true
					f.sdk = p.Entrypoint == "sdk-cli"
				}
			}
		}
	}
	if ref.size <= 2*Window {
		buf, err := s.readAt(ref.path, 0, int(ref.size))
		if err != nil {
			return facts{}, err
		}
		scan(buf, true)
	} else {
		head, err := s.readAt(ref.path, 0, Window)
		if err != nil {
			return facts{}, err
		}
		// A window ends mid-line almost always: only the lines it holds whole are read.
		if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
			scan(head[:i+1], true)
		}
		tail, err := s.readAt(ref.path, ref.size-Window, Window)
		if err != nil {
			return facts{}, err
		}
		if i := bytes.IndexByte(tail, '\n'); i >= 0 {
			scan(tail[i+1:], false)
		}
	}
	switch {
	case titles.custom != "":
		f.title = titles.custom
	case titles.ai != "":
		f.title = titles.ai
	default:
		f.title = titles.agent
	}
	return f, nil
}

// last is the later of two values of one kind of line: the new one when it says something.
func last(old, v string) string {
	if v != "" {
		return v
	}
	return old
}

// cleanTitle is a title as it may be shown: invalid UTF-8, control and format characters
// removed, surrounding space trimmed, at most MaxTitle characters (ADR-0015 §1).
func cleanTitle(s string) string {
	s = strings.TrimSpace(capability.StripInvisible(s))
	if utf8.RuneCountInString(s) > MaxTitle {
		s = strings.TrimSpace(string([]rune(s)[:MaxTitle]))
	}
	return s
}

// dropContinued leaves out a chat whose continuation is listed too: the client shows one
// chat there, and it is the continuation.
func dropContinued(chats []rawChat) []rawChat {
	listed := make(map[string]bool, len(chats))
	for _, c := range chats {
		listed[c.id] = true
	}
	out := chats[:0:0]
	for _, c := range chats {
		if c.continuedIn != "" && c.continuedIn != c.id && listed[c.continuedIn] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// encodePath is Claude Code's directory name for a working directory: every byte that is
// not an ASCII letter or digit becomes '-'.
func encodePath(p string) string {
	b := []byte(p)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

// within reports whether dir is root or lies below it, by path components: "/a/bc" is not
// within "/a/b".
func within(dir, root string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	dir, root = filepath.Clean(dir), filepath.Clean(root)
	if dir == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(dir, root)
}

// osCause is an operating system error's own words, without the path it names: the cause is
// shown, and a path is not the part that explains.
func osCause(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
