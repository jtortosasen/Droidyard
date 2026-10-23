package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// nonProject is a folder with no Gradle build in it or above it.
func nonProject(t *testing.T) string {
	t.Helper()
	return platform.Current().Canonical(t.TempDir())
}

// noTools asserts the session an `mcp` shim serves where it finds no project (ADR-0124
// §2): initialize is answered by the shim itself as server droidyard at the API version,
// with the tools capability declared and the one-line instructions for dir; tools/list is
// an empty list (not null, not METHOD_NOT_FOUND: the capability is declared); ping is
// answered.
func noTools(t *testing.T, s *shim, dir string) {
	t.Helper()
	res := s.initialize("noproject")
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "droidyard" || info["version"] != system.Version {
		t.Errorf("serverInfo = %v; want droidyard at %s", info, system.Version)
	}
	if caps, _ := res["capabilities"].(map[string]any); caps["tools"] == nil {
		t.Errorf("capabilities = %v; want the tools capability declared, or a client's tools/list is METHOD_NOT_FOUND", caps)
	}
	want := "No Android project at " + dir + ": droidyard has no tools in this session. Start the assistant in the project's folder or one of its worktrees."
	if res["instructions"] != want {
		t.Errorf("instructions = %q; want %q", res["instructions"], want)
	}
	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if got := s.recv(); !strings.Contains(string(got), `"tools":[]`) || strings.Contains(string(got), `"error"`) {
		t.Errorf("tools/list = %s; want result.tools to be []", got)
	}
	s.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	response(t, s.recv(), 2)
}

// TestMCPOutsideAProjectIsASessionWithNoTools holds ADR-0124 §2: where the shim finds no
// project it serves the session itself, with the tools capability declared and no tools,
// exits 0 at EOF, writes nothing on stderr and creates no file. The state directory
// stays empty, so there is no daemon.lock and no socket: no daemon was started.
//
// Seen failing on the code before the change: the shim bound its cwd as the root and
// started a daemon (a daemon.lock appeared, the session listed the workspace tools).
func TestMCPOutsideAProjectIsASessionWithNoTools(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	dir := nonProject(t)
	s := startShimIn(t, state, dir, "noproject", []string{"mcp"})
	noTools(t, s, dir)
	if code := s.exit(); code != 0 {
		t.Errorf("the shim exited %d at EOF; want 0", code)
	}
	if s.stderr.Len() != 0 {
		t.Errorf("stderr = %q; want nothing", s.stderr.String())
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Errorf("the state directory holds %v; want it empty (no daemon.lock, no socket)", entries)
	}
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	for _, f := range []string{filepath.Join(state, daemon.LockFile), env.SocketPath(state)} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s exists; the shim started a daemon", f)
		}
	}
}

// TestMCPOutsideAProjectNeverDials holds that outside a project the shim never connects
// to a daemon, whether or not one listens, with or without --no-autostart: the session is
// the same with or without a daemon (ADR-0124 §2). A daemon that listens counts every
// connection.
//
// Seen failing with the dial kept (the no-project branch removed): the fake daemon
// counted a connection and never answered initialize.
func TestMCPOutsideAProjectNeverDials(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"mcp"}, {"mcp", "--no-autostart"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			state := shortDir(t)
			fake := startFakeDaemon(t, state, false)
			dir := nonProject(t)
			s := startShimIn(t, state, dir, "noproject", args)
			noTools(t, s, dir)
			if code := s.exit(); code != 0 {
				t.Errorf("the shim exited %d at EOF; want 0", code)
			}
			if n := fake.dials.Load(); n != 0 {
				t.Errorf("the daemon was dialled %d time(s); want 0", n)
			}
		})
	}
}

// TestMCPServerDiscoverFallsBack holds the handshake Claude Code 2.1.308 opens with: it
// sends server/discover first and falls back to initialize when that is a JSON-RPC error
// (METHOD_NOT_FOUND, -32601). The session with no tools answers a server/discover that
// does not name the modern protocol with that error, by mcp-go as the daemon's session
// does, and initialize works after it.
func TestMCPServerDiscoverFallsBack(t *testing.T) {
	t.Parallel()
	dir := nonProject(t)
	s := startShimIn(t, shortDir(t), dir, "noproject", []string{"mcp"})
	s.send(`{"jsonrpc":"2.0","id":7,"method":"server/discover"}`)
	var msg struct {
		ID    *int `json:"id"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	line := s.recv()
	if err := json.Unmarshal(line, &msg); err != nil || msg.ID == nil || *msg.ID != 7 {
		t.Fatalf("server/discover answered %s; want an answer to id 7", line)
	}
	if msg.Error == nil || msg.Error.Code != -32601 {
		t.Errorf("server/discover answered %s; want a JSON-RPC error with METHOD_NOT_FOUND (-32601), the code Claude Code falls back to initialize on", line)
	}
	noTools(t, s, dir)
	s.exit()
}

// TestMCPFrom holds --from (ADR-0124 §2): the folder the project is found from, in place
// of the cwd. Each row says what it binds, or that the session has no tools and nothing
// is dialled.
//
// Seen failing with --from handled as --root: the non-project --from dialled the fake
// daemon (and was bound as a workspace), and the unexpanded and empty values were taken
// for folders.
func TestMCPFrom(t *testing.T) {
	t.Parallel()
	t.Run("a folder that is no project gives a session with no tools and dials nothing", func(t *testing.T) {
		t.Parallel()
		state := shortDir(t)
		fake := startFakeDaemon(t, state, false)
		_, inProject := projectWith(t, "settings.gradle") // the cwd is a project: --from still wins
		other := nonProject(t)
		s := startShimIn(t, state, inProject, "from", []string{"mcp", "--from", other})
		noTools(t, s, other)
		s.exit()
		if n := fake.dials.Load(); n != 0 {
			t.Errorf("the daemon was dialled %d time(s); want 0", n)
		}
	})
	t.Run("a subfolder of a project binds the project, whatever the cwd", func(t *testing.T) {
		t.Parallel()
		p := startInProcess(t)
		root, sub := projectWith(t, "settings.gradle")
		s := startShimIn(t, p.state, nonProject(t), "from", []string{"mcp", "--no-autostart", "--from", sub})
		s.initialize("from")
		boundReady(t, "--from a project subfolder", s.workspaceTool(1, "workspace_describe"), root)
	})
	t.Run("--from=<dir> is the same flag", func(t *testing.T) {
		t.Parallel()
		p := startInProcess(t)
		root, sub := projectWith(t, "settings.gradle")
		s := startShimIn(t, p.state, nonProject(t), "from", []string{"mcp", "--no-autostart", "--from=" + sub})
		s.initialize("from")
		boundReady(t, "--from=", s.workspaceTool(1, "workspace_describe"), root)
	})
	t.Run("a relative --from is resolved against the cwd", func(t *testing.T) {
		t.Parallel()
		p := startInProcess(t)
		root, _ := projectWith(t, "settings.gradle")
		s := startShimIn(t, p.state, filepath.Dir(root), "from", []string{"mcp", "--no-autostart", "--from", filepath.Join(filepath.Base(root), "app", "src")})
		s.initialize("from")
		boundReady(t, "a relative --from", s.workspaceTool(1, "workspace_describe"), root)
	})
	// A client variable it did not expand, or the empty value an IDE window with no
	// folder expands to, names no folder: the cwd's project is bound, else no tools.
	for _, from := range []string{"${workspaceFolder}", ""} {
		t.Run("--from "+strings.TrimSpace(from+" (ignored)")+" binds the cwd's project", func(t *testing.T) {
			t.Parallel()
			p := startInProcess(t)
			root, sub := projectWith(t, "settings.gradle")
			s := startShimIn(t, p.state, sub, "from", []string{"mcp", "--no-autostart", "--from", from})
			s.initialize("from")
			boundReady(t, "an ignored --from in a project", s.workspaceTool(1, "workspace_describe"), root)
		})
		t.Run("--from "+strings.TrimSpace(from+" (ignored)")+" outside a project gives a session with no tools", func(t *testing.T) {
			t.Parallel()
			state := shortDir(t)
			fake := startFakeDaemon(t, state, false)
			dir := nonProject(t)
			s := startShimIn(t, state, dir, "from", []string{"mcp", "--from", from})
			noTools(t, s, dir)
			s.exit()
			if n := fake.dials.Load(); n != 0 {
				t.Errorf("the daemon was dialled %d time(s); want 0", n)
			}
		})
	}
	t.Run("--from and --root exclude each other", func(t *testing.T) {
		t.Parallel()
		state := shortDir(t)
		root, _ := projectWith(t, "settings.gradle")
		for _, from := range []string{root, "${workspaceFolder}", ""} {
			p := droidyardIn(t, state, nonProject(t), "mcp", "--from", from, "--root", root)
			if code := p.wait(t); code != 2 {
				t.Errorf("mcp --from %q --root exited %d; want 2", from, code)
			}
			if got := p.stderr.String(); !strings.Contains(got, "--from and --root exclude each other") {
				t.Errorf("stderr = %q; want it to say the flags exclude each other", got)
			}
		}
		if entries, _ := os.ReadDir(state); len(entries) != 0 {
			t.Errorf("the refusal left %v in the state directory; want nothing started", entries)
		}
	})
}

// TestMCPInALinkedWorktreeBindsTheWorktree holds that with no --root a session started
// in a `git worktree add` worktree is bound to that worktree, whose mainRoot is the main
// tree (ADR-0082 §6): the project of the cwd, not the main tree.
func TestMCPInALinkedWorktreeBindsTheWorktree(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	base, main := worktreeRepo(t, false)
	wt := addWT(t, main, filepath.Join(base, "wt"))
	s := startShimIn(t, p.state, wt, "wt", []string{"mcp", "--no-autostart"})
	s.initialize("worktree")
	got := s.workspaceTool(1, "workspace_describe")
	if got.isError || got.info.Root != wt || got.info.MainRoot != main {
		t.Errorf("workspace_describe = %s; want root %s with mainRoot %s", got.raw, wt, main)
	}
}
