package chats

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// Every test works in a temporary directory that stands for the home: the Claude Code
// directory handed to the reader lies in it, the projects are paths below it or invented
// ones, and nothing reads or writes the real ~/.claude, ~/.cursor or any chat store.

// fixture is a fake Claude Code configuration directory.
type fixture struct {
	t        *testing.T
	home     string // canonical temporary directory
	claude   string // <home>/.claude
	projects string // <claude>/projects
	cursor   string // <home>/.cursor
	ide      string // <home>/ide, standing for the Cursor IDE's globalStorage
	known    []string
	clock    time.Time
	starts   atomic.Int64 // how many sqlite3 the service started
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := platform.Current().Canonical(t.TempDir())
	f := &fixture{t: t, home: home, claude: filepath.Join(home, ".claude"),
		cursor: filepath.Join(home, ".cursor"), ide: filepath.Join(home, "ide")}
	f.projects = filepath.Join(f.claude, "projects")
	if err := os.MkdirAll(f.projects, 0o700); err != nil {
		t.Fatal(err)
	}
	f.clock = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return f
}

// service is a reader over the fixture, knowing the projects named.
func (f *fixture) service() *service {
	return newService(Options{
		Known: func() []string { return append([]string(nil), f.known...) },
		Env:   platform.Current(),
		Dirs:  assistant.UserDirs{Claude: f.claude, Cursor: f.cursor},
		// Neither Cursor store exists in the fixture unless a test makes one: absent.
		CursorIDE: f.ide,
		Starter: func(cmd *exec.Cmd) error {
			f.starts.Add(1)
			return startGroup(cmd)
		},
	})
}

// next is a modification time later than the last one handed out.
func (f *fixture) next() time.Time {
	f.clock = f.clock.Add(time.Minute)
	return f.clock
}

// dir is the directory Claude Code files the chats of a working directory in.
func (f *fixture) dir(tree string) string {
	return filepath.Join(f.projects, encodePath(tree))
}

// write stores a transcript of the chat id for a working directory, one line per element,
// with the given modification time (the fixture's next one when zero).
func (f *fixture) write(tree, id string, mtime time.Time, lines ...string) string {
	f.t.Helper()
	dir := f.dir(tree)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if mtime.IsZero() {
		mtime = f.next()
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// userLine is a conversation line of a chat started the usual way, in cwd.
func userLine(cwd string) string {
	return fmt.Sprintf(`{"type":"user","cwd":%s,"entrypoint":"cli","sessionId":"s","message":{"role":"user","content":"hello"}}`, quote(cwd))
}

// plainLine is a conversation line with a cwd and no entrypoint.
func plainLine(cwd string) string {
	return fmt.Sprintf(`{"type":"assistant","cwd":%s,"message":{"role":"assistant","content":"ok"}}`, quote(cwd))
}

func aiTitle(s string) string {
	return fmt.Sprintf(`{"type":"ai-title","aiTitle":%s,"sessionId":"s"}`, quote(s))
}

func customTitle(s string) string {
	return fmt.Sprintf(`{"type":"custom-title","customTitle":%s,"sessionId":"s"}`, quote(s))
}

func agentName(s string) string {
	return fmt.Sprintf(`{"type":"agent-name","agentName":%s,"sessionId":"s"}`, quote(s))
}

func continuedIn(id string) string {
	return fmt.Sprintf(`{"type":"continued-in","continuedInSessionId":%s}`, quote(id))
}

// filler is n bytes of whole lines carrying nothing the reader wants.
func filler(n int) []string {
	line := fmt.Sprintf(`{"type":"progress","data":"%s"}`, strings.Repeat("x", 1000))
	var out []string
	for size := 0; size < n; size += len(line) + 1 {
		out = append(out, line)
	}
	return out
}

// list runs the reader and fails the test on an error.
func list(t *testing.T, s *service) Result {
	t.Helper()
	res, err := s.list(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return res
}

func ids(r Result) []string {
	out := []string{}
	for _, c := range r.Chats {
		out = append(out, c.ID)
	}
	return out
}

func byID(r Result, id string) (Chat, bool) {
	for _, c := range r.Chats {
		if c.ID == id {
			return c, true
		}
	}
	return Chat{}, false
}

// counter wraps a service's file reads and counts them.
type counter struct {
	mu    sync.Mutex
	calls int
	bytes int64
	paths map[string]int
}

func count(s *service) *counter {
	c := &counter{paths: map[string]int{}}
	real := s.readAt
	s.readAt = func(path string, off int64, n int) ([]byte, error) {
		b, err := real(path, off, n)
		c.mu.Lock()
		c.calls++
		c.bytes += int64(len(b))
		c.paths[path]++
		c.mu.Unlock()
		return b, err
	}
	return c
}

func (c *counter) reset() {
	c.mu.Lock()
	c.calls, c.bytes, c.paths = 0, 0, map[string]int{}
	c.mu.Unlock()
}

func (c *counter) files() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.paths)
}

// repo is a git repository with linked worktrees, made with the host git.
type repo struct {
	base, main string
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func newRepo(t *testing.T) repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	base := platform.Current().Canonical(t.TempDir())
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q")
	// No auto maintenance: a commit starts a detached `git maintenance run --auto`, whose
	// worktree-prune task deleted the entries a test was planting (see internal/platform).
	gitIn(t, main, "config", "maintenance.auto", "false")
	gitIn(t, main, "config", "gc.auto", "0")
	if err := os.WriteFile(filepath.Join(main, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "add", "f")
	gitIn(t, main, "commit", "-q", "-m", "one")
	return repo{base: base, main: main}
}

// worktree adds a linked worktree named name beside the main tree and returns its root.
func (r repo) worktree(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(r.base, name)
	gitIn(t, r.main, "worktree", "add", "-q", "-b", name, path)
	return platform.Current().Canonical(path)
}
