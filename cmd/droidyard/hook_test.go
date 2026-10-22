package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// panicEnv makes a re-executed hook panic at the start of its decision, through
// hookSeam; the shipped binary has no such variable.
const panicEnv = "DROIDYARD_TEST_HOOK_PANIC"

func init() {
	if os.Getenv(reexec) == "1" && os.Getenv(panicEnv) == "1" {
		hookSeam = func() { panic("forced by the test") }
	}
}

// hookResult is one run of `droidyard hook`.
type hookResult struct {
	stdout, stderr string
	code           int
	elapsed        time.Duration
}

// runHook runs a re-executed `droidyard hook` with the state root in
// DROIDYARD_STATE_DIR, as a client's hook would have it from a user who set one, and
// stdin as its event. extraEnv adds to the environment; HOME is a scratch directory, so
// nothing the hook did by mistake could reach the developer's state.
func runHook(t *testing.T, state, stdin string, extraEnv []string, args ...string) hookResult {
	t.Helper()
	return runHookStdin(t, state, strings.NewReader(stdin), extraEnv, args...)
}

func runHookStdin(t *testing.T, state string, stdin io.Reader, extraEnv []string, args ...string) hookResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := &exec.Cmd{
		Path: os.Args[0],
		Args: append([]string{"droidyard", "hook"}, args...),
		Env: append([]string{reexec + "=1", testParentEnv + "=" + strconv.Itoa(os.Getpid()),
			"DROIDYARD_STATE_DIR=" + state, "HOME=" + shortDir(t), "PATH=/nonexistent"}, extraEnv...),
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	err := cmd.Wait()
	res := hookResult{stdout: stdout.String(), stderr: stderr.String(), elapsed: time.Since(start)}
	if exit, ok := err.(*exec.ExitError); ok {
		res.code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("waiting for the hook: %v", err)
	}
	return res
}

// claudeBash is a Claude Code PreToolUse event for a Bash command, in the shape of the
// reference's example (m2j step 6 (a)): cwd is the shell's directory.
func claudeBash(command, cwd string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": "s1", "transcript_path": "/nonexistent/t.jsonl", "cwd": cwd,
		"permission_mode": "default", "hook_event_name": "PreToolUse", "tool_name": "Bash",
		"tool_input": map[string]any{"command": command}, "tool_use_id": "toolu_1",
	})
	return string(b)
}

// cursorFixture is one of the events cursor-agent was observed sending (m2j step 6),
// with fields replaced by set.
func cursorFixture(t *testing.T, name string, set map[string]any) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "assistant", "testdata", "hooks", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	for k, v := range set {
		ev[k] = v
	}
	b, _ := json.Marshal(ev)
	return string(b)
}

// reasonOf is the text the model reads in a deny, for the client, and fails the test
// when stdout is not that client's deny.
func reasonOf(t *testing.T, client, stdout string) string {
	t.Helper()
	var out struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
		Permission  string `json:"permission"`
		UserMessage string `json:"user_message"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("%s hook printed %q, not one JSON answer: %v", client, stdout, err)
	}
	switch client {
	case "claude":
		if out.HookSpecificOutput.HookEventName != "PreToolUse" || out.HookSpecificOutput.PermissionDecision != "deny" || out.HookSpecificOutput.PermissionDecisionReason == "" {
			t.Fatalf("claude hook printed %q; want a PreToolUse deny with a reason", stdout)
		}
		return out.HookSpecificOutput.PermissionDecisionReason
	default:
		if out.Permission != "deny" || out.UserMessage == "" {
			t.Fatalf("cursor hook printed %q; want a deny with a user_message", stdout)
		}
		return out.UserMessage
	}
}

// replacementOf is the command a deny hands the agent: what follows the sentence.
func replacementOf(t *testing.T, reason string) string {
	t.Helper()
	_, cmd, ok := strings.Cut(reason, "in the same directory: ")
	if !ok || cmd == "" {
		t.Fatalf("the deny %q carries no command", reason)
	}
	return cmd
}

// passOf is the answer a client reads as "let the command through": nothing for Claude
// Code, {} for Cursor (ADR-0061 §4).
func passOf(client string) string {
	if client == "cursor" {
		return "{}\n"
	}
	return ""
}

// fakeDaemon answers on a state root's socket as far as the hello and no further: it
// counts the connections, records each request it is sent, and either holds every one
// unanswered or answers it with an error. It stands in for a daemon whose
// workspace.open is slow or fails, which the real one is not made to be.
type fakeDaemon struct {
	dials    atomic.Int32
	requests chan capability.RequestEnvelope
}

func startFakeDaemon(t *testing.T, state string, answerError bool) *fakeDaemon {
	t.Helper()
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	sock := env.SocketPath(state)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDaemon{requests: make(chan capability.RequestEnvelope, 8)}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.dials.Add(1)
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go func() {
				r := bufio.NewReader(c)
				if _, err := r.ReadBytes('\n'); err != nil {
					return
				}
				c.Write([]byte(`{"version":"fake","session":"fake"}` + "\n"))
				for {
					line, err := r.ReadBytes('\n')
					if err != nil {
						return
					}
					var req capability.RequestEnvelope
					if json.Unmarshal(line, &req) != nil {
						return
					}
					f.requests <- req
					if answerError {
						b, _ := json.Marshal(capability.ResponseEnvelope{ID: req.ID,
							Error: &capability.Error{Code: capability.ErrWorkspaceNotOpen, Message: "the fake daemon refuses"}})
						c.Write(append(b, '\n'))
					}
				}
			}()
		}
	}()
	return f
}

// hookProject is a project root with the files a hook test deletes.
func hookProject(t *testing.T) string {
	t.Helper()
	root, _ := gradleProject(t)
	for _, f := range []string{"a.kt", "dir/b.kt"} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fun "+f+"() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// findLocks lists every daemon.lock under dir.
func findLocks(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == daemon.LockFile {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// runReplacement runs the command a deny handed the agent, as the agent's shell would,
// in dir, the directory the rm was typed in: /bin/sh -c, a PATH that lacks droidyard,
// no DROIDYARD_STATE_DIR, and a scratch HOME (returned), so a daemon the command started
// would leave its lock there.
func runReplacement(t *testing.T, replacement, dir string) (stdout, stderr, home string, err error) {
	t.Helper()
	home = shortDir(t)
	sh := exec.Command("/bin/sh", "-c", replacement)
	sh.Dir = dir
	sh.Env = []string{reexec + "=1", testParentEnv + "=" + strconv.Itoa(os.Getpid()), "HOME=" + home, "PATH=/nonexistent"}
	var out, errb bytes.Buffer
	sh.Stdout, sh.Stderr = &out, &errb
	err = sh.Run()
	return out.String(), errb.String(), home, err
}

// cursorAgentMessage is the agent_message of a Cursor deny.
func cursorAgentMessage(t *testing.T, stdout string) string {
	t.Helper()
	var out struct {
		AgentMessage string `json:"agent_message"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatal(err)
	}
	return out.AgentMessage
}

// TestHookDeniesAndTheReplacementReachesTheSameDaemon holds ADR-0062 §4–§6 end to end on
// a real daemon, for each client's event: a routed rm is denied with one command and
// nothing is deleted by the hook; the hook opened the workspace; and that exact command,
// run by /bin/sh in the subdirectory where the rm was typed, with a PATH that lacks
// droidyard and no DROIDYARD_STATE_DIR in the shell, reaches the daemon the hook
// dialled, trashes exactly the files the typed operands name there, and starts no second
// daemon. Claude Code's event carries that directory as cwd; Cursor's carries "", so
// its relative operands route on their text and `trash` does the placing.
//
// Each case is a decision the replacement must keep: a name resolves in the shell's
// directory and not the root (Obsolete.kt), a quoted star stays a literal name (a*b.kt,
// with aXb.kt beside it), a star next to a quoted word expands as sh would ("my dir"/*.kt),
// and -r reaches a relative directory. A replacement that did not name the state root
// would fail on all of them; one that made the quoted star bare would trash aXb.kt;
// one that quoted the unquoted star would find no file called *.kt.
func TestHookDeniesAndTheReplacementReachesTheSameDaemon(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, sub, cmd string
		files          []string
		gone, kept     []string
		items          int
	}{
		{"a name that also exists at the root", "app/src", "rm Obsolete.kt",
			[]string{"Obsolete.kt", "app/src/Obsolete.kt"}, []string{"app/src/Obsolete.kt"}, []string{"Obsolete.kt"}, 1},
		{"a quoted star is a literal name", "app/src", "rm 'a*b.kt'",
			[]string{"app/src/a*b.kt", "app/src/aXb.kt"}, []string{"app/src/a*b.kt"}, []string{"app/src/aXb.kt"}, 1},
		{"a star beside a double-quoted directory", "app", `rm "my dir"/*.kt`,
			[]string{"app/my dir/x.kt", "app/my dir/y.kt", "app/my dir/z.txt", "app/x.kt"},
			[]string{"app/my dir/x.kt", "app/my dir/y.kt"}, []string{"app/my dir/z.txt", "app/x.kt"}, 2},
		{"-rf of a relative directory", "app", "rm -rf src/old",
			[]string{"app/src/old/x.kt", "app/src/keep.kt"}, []string{"app/src/old"}, []string{"app/src/keep.kt"}, 1},
	}
	for _, client := range []string{"claude", "cursor"} {
		for _, c := range cases {
			t.Run(client+": "+c.name, func(t *testing.T) {
				t.Parallel()
				p := startInProcess(t)
				root := hookProject(t)
				dir := filepath.Join(root, c.sub)
				for _, f := range c.files {
					full := filepath.Join(root, f)
					if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(full, []byte(f), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				event := claudeBash(c.cmd, dir)
				if client == "cursor" {
					// Cursor carries no cwd while its shell keeps a cd [obs, m2j step 6].
					event = cursorFixture(t, "cursor-3-beforeShellExecution", map[string]any{
						"command": c.cmd, "cwd": "", "workspace_roots": []string{root}})
				}

				if env, code := p.call(t, "workspace.describe", "--workspace", root); code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceNotOpen {
					t.Fatalf("before the hook, workspace.describe exited %d with %+v; want WORKSPACE_NOT_OPEN", code, env.Error)
				}
				res := runHook(t, p.state, event, nil, "--client", client, "--root", root)
				if res.code != 0 || res.stderr != "" {
					t.Fatalf("hook exited %d with stderr %q; want 0 and none", res.code, res.stderr)
				}
				reason := reasonOf(t, client, res.stdout)
				if client == "cursor" && cursorAgentMessage(t, res.stdout) != reason {
					t.Errorf("Cursor's agent_message is %q; want the text of user_message (ADR-0062 §6)", cursorAgentMessage(t, res.stdout))
				}
				replacement := replacementOf(t, reason)
				if !strings.Contains(replacement, " trash --state-dir "+quoteArg(p.state)+" --root "+quoteArg(root)+" ") ||
					strings.Contains(replacement, "file.delete") || strings.Contains(replacement, "DROIDYARD_STATE_DIR") {
					t.Errorf("the replacement is %q; want droidyard trash with --state-dir and --root", replacement)
				}
				for _, f := range c.files {
					if !exists(filepath.Join(root, f)) {
						t.Fatalf("the hook deleted %s; it must delete nothing (ADR-0062 §4)", f)
					}
				}
				if env, code := p.call(t, "workspace.describe", "--workspace", root); code != 0 || env.Error != nil {
					t.Errorf("after the hook, workspace.describe exited %d with %+v; want the workspace open", code, env.Error)
				}

				stdout, stderr, shellHome, err := runReplacement(t, replacement, dir)
				if err != nil {
					t.Fatalf("the replacement failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
				}
				if n := strings.Count(stdout, "trashed "); n != c.items {
					t.Errorf("the replacement printed %q; want %d trashed lines", stdout, c.items)
				}
				for _, f := range c.gone {
					if exists(filepath.Join(root, f)) {
						t.Errorf("%s is still there after the replacement", f)
					}
				}
				for _, f := range c.kept {
					if !exists(filepath.Join(root, f)) {
						t.Errorf("%s was trashed; the replacement must trash exactly what the typed rm named", f)
					}
				}
				if n := trashed(t, p.state); n != c.items {
					t.Errorf("the daemon's trash holds %d items; want %d", n, c.items)
				}
				if locks := findLocks(t, shellHome); len(locks) != 0 {
					t.Errorf("the replacement started a daemon of its own: %v", locks)
				}
			})
		}
	}
}

// TestHookReplacementReadsAGlobsDashNameAsRmWould holds the step 2–3 verifier's N1 for
// Cursor, whose relative glob the hook cannot expand: the replacement carries `--` only
// when the rm did, so a name starting with `-` that the shell's expansion puts first is
// a flag to `trash` as it is to rm. `rm *` over `-f` and A.kt deletes A.kt and keeps
// `-f`; `rm *.kt` over `-x.kt` is a usage error that moves nothing. With `--` always
// added, both would trash the dash-named file.
func TestHookReplacementReadsAGlobsDashNameAsRmWould(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, cmd   string
		files, gone []string
		ok          bool
	}{
		{"a -f name is a flag", "rm *", []string{"d/-f", "d/A.kt"}, []string{"d/A.kt"}, true},
		{"a -x.kt name is a usage error", "rm *.kt", []string{"d/-x.kt", "d/A.kt"}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := startInProcess(t)
			root := hookProject(t)
			for _, f := range c.files {
				full := filepath.Join(root, f)
				if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(f), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			event := cursorFixture(t, "cursor-3-beforeShellExecution", map[string]any{
				"command": c.cmd, "cwd": "", "workspace_roots": []string{root}})
			res := runHook(t, p.state, event, nil, "--client", "cursor", "--root", root)
			replacement := replacementOf(t, reasonOf(t, "cursor", res.stdout))
			if strings.Contains(replacement, " -- ") {
				t.Fatalf("the replacement %q has --, which the rm did not", replacement)
			}
			_, stderr, _, err := runReplacement(t, replacement, filepath.Join(root, "d"))
			if (err == nil) != c.ok {
				t.Fatalf("the replacement's error is %v (stderr %s); want success %v", err, stderr, c.ok)
			}
			for _, f := range c.files {
				gone := false
				for _, g := range c.gone {
					gone = gone || g == f
				}
				if exists(filepath.Join(root, f)) == gone {
					t.Errorf("%s: exists %v; want %v", f, !gone, !gone)
				}
			}
			if n := trashed(t, p.state); n != len(c.gone) {
				t.Errorf("the daemon's trash holds %d items; want %d", n, len(c.gone))
			}
		})
	}
}

// TestHookHandsCursorsDeleteToolTheAbsolutePath holds ADR-0062 §4 for Cursor's Delete
// tool: its absolute file_path goes to `trash` in single quotes, a glob character in it
// included, and the replacement trashes that file and no other.
func TestHookHandsCursorsDeleteToolTheAbsolutePath(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	root := hookProject(t)
	for _, f := range []string{"del/a*.kt", "del/aX.kt"} {
		full := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "del", "a*.kt")
	event := cursorFixture(t, "cursor-2-preToolUse", map[string]any{
		"tool_input": map[string]any{"file_path": target}, "workspace_roots": []string{root}})
	res := runHook(t, p.state, event, nil, "--client", "cursor", "--root", root)
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("hook exited %d with stderr %q; want 0 and none", res.code, res.stderr)
	}
	reason := reasonOf(t, "cursor", res.stdout)
	if cursorAgentMessage(t, res.stdout) != reason {
		t.Error("Cursor's agent_message differs from user_message")
	}
	replacement := replacementOf(t, reason)
	if !strings.HasSuffix(replacement, " '"+target+"'") {
		t.Errorf("the replacement is %q; want the absolute path in single quotes", replacement)
	}
	stdout, stderr, _, err := runReplacement(t, replacement, root)
	if err != nil {
		t.Fatalf("the replacement failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if exists(target) || !exists(filepath.Join(root, "del", "aX.kt")) {
		t.Error("the replacement did not trash exactly the Delete tool's file")
	}
}

// TestHookPassesWithoutADaemonAndStartsNone holds ADR-0061 §1 and §4: with nothing on
// the state root's socket, a routed rm is let through and no daemon is started (the
// lock is absent afterwards, and so is the socket directory).
func TestHookPassesWithoutADaemonAndStartsNone(t *testing.T) {
	t.Parallel()
	for _, client := range []string{"claude", "cursor"} {
		t.Run(client, func(t *testing.T) {
			t.Parallel()
			state := shortDir(t)
			root := hookProject(t)
			event := claudeBash("rm -f a.kt", root)
			if client == "cursor" {
				event = cursorFixture(t, "cursor-3-beforeShellExecution", map[string]any{"command": "rm -f " + filepath.Join(root, "a.kt")})
			}
			res := runHook(t, state, event, nil, "--client", client, "--root", root)
			if res.code != 0 || res.stdout != passOf(client) {
				t.Fatalf("with no daemon the hook exited %d and printed %q; want 0 and %q", res.code, res.stdout, passOf(client))
			}
			if locks := findLocks(t, state); len(locks) != 0 {
				t.Errorf("the hook started a daemon: %v", locks)
			}
			if exists(filepath.Join(state, "run")) {
				t.Error("the state root has a run directory: something started a daemon")
			}
			if !exists(filepath.Join(root, "a.kt")) {
				t.Error("the file is gone")
			}
		})
	}
}

// TestHookPassesWhatItDoesNotRouteWithoutDialling holds ADR-0061 §2 at the process: an
// event that is not a routed rm (another command, a compound, a build output, a target
// outside the root or missing, another event or tool, malformed input) is let through
// without the daemon being dialled. The control at the end routes and dials once, so a
// count that never moved would show.
func TestHookPassesWhatItDoesNotRouteWithoutDialling(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	fake := startFakeDaemon(t, state, true)
	root := hookProject(t)
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(shortDir(t), "o.kt")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		name, client, event string
	}{
		{"claude: another command", "claude", claudeBash("ls -la", root)},
		{"claude: rm in a compound", "claude", claudeBash("rm a.kt && echo done", root)},
		{"claude: rm of a build output", "claude", claudeBash("rm -rf build", root)},
		{"claude: rm outside the root", "claude", claudeBash("rm "+outside, root)},
		{"claude: rm of nothing", "claude", claudeBash("rm nothere.kt", root)},
		{"claude: a directory without -r", "claude", claudeBash("rm dir", root)},
		{"claude: another tool", "claude", strings.Replace(claudeBash("rm a.kt", root), `"Bash"`, `"Edit"`, 1)},
		{"claude: another event", "claude", strings.Replace(claudeBash("rm a.kt", root), `"PreToolUse"`, `"PostToolUse"`, 1)},
		{"claude: malformed", "claude", "this is not json"},
		{"claude: empty", "claude", ""},
		{"claude: an empty object", "claude", "{}"},
		{"cursor: the touch event, as observed", "cursor", cursorFixture(t, "cursor-0-beforeShellExecution", nil)},
		{"cursor: the compound rm, as observed", "cursor", cursorFixture(t, "cursor-3-beforeShellExecution", nil)},
		{"cursor: a relative rm leaving the root, with no cwd", "cursor", cursorFixture(t, "cursor-3-beforeShellExecution", map[string]any{"command": "rm ../a.kt"})},
		{"cursor: a relative rm of a build output, with no cwd", "cursor", cursorFixture(t, "cursor-3-beforeShellExecution", map[string]any{"command": "rm -rf build/x"})},
		{"cursor: the Delete tool outside the root, as observed", "cursor", cursorFixture(t, "cursor-2-preToolUse", nil)},
		{"cursor: another event", "cursor", cursorFixture(t, "cursor-3-beforeShellExecution", map[string]any{"hook_event_name": "afterShellExecution", "command": "rm -f " + filepath.Join(root, "a.kt")})},
		{"cursor: malformed", "cursor", "{"},
	}
	for _, r := range rows {
		res := runHook(t, state, r.event, nil, "--client", r.client, "--root", root)
		if res.code != 0 || res.stdout != passOf(r.client) {
			t.Errorf("%s: the hook exited %d and printed %q; want 0 and %q", r.name, res.code, res.stdout, passOf(r.client))
		}
	}
	if n := fake.dials.Load(); n != 0 {
		t.Fatalf("the hook dialled the daemon %d times for events it does not route; want 0", n)
	}
	if !exists(filepath.Join(root, "a.kt")) || !exists(filepath.Join(root, "dir", "b.kt")) {
		t.Fatal("a file is gone")
	}

	// The control: a routed rm does dial, and the daemon's refusal is a pass.
	res := runHook(t, state, claudeBash("rm a.kt", root), nil, "--client", "claude", "--root", root)
	if res.code != 0 || res.stdout != "" {
		t.Errorf("a daemon that refuses workspace.open: the hook exited %d and printed %q; want 0 and nothing", res.code, res.stdout)
	}
	if n := fake.dials.Load(); n != 1 {
		t.Errorf("the routed control dialled %d times; want 1", n)
	}
	select {
	case req := <-fake.requests:
		if req.Capability != "workspace.open" || req.Workspace != root {
			t.Errorf("the hook sent %+v; want workspace.open on %s", req, root)
		}
	default:
		t.Error("the daemon saw no request from the control")
	}
}

// TestHookPassesWhenOpenIsHeldPastTheDeadline holds ADR-0061 §1: a daemon that takes the
// connection and never answers workspace.open does not keep the command waiting. The
// hook answers the pass-through at 2 s, not before (it was waiting on the open) and not
// long after. A stdin the client never closes is held the same way.
func TestHookPassesWhenOpenIsHeldPastTheDeadline(t *testing.T) {
	t.Parallel()
	for _, client := range []string{"claude", "cursor"} {
		t.Run(client+" open held", func(t *testing.T) {
			t.Parallel()
			state := shortDir(t)
			fake := startFakeDaemon(t, state, false)
			root := hookProject(t)
			event := claudeBash("rm a.kt", root)
			if client == "cursor" {
				event = cursorFixture(t, "cursor-3-beforeShellExecution", map[string]any{"command": "rm " + filepath.Join(root, "a.kt")})
			}
			res := runHook(t, state, event, nil, "--client", client, "--root", root)
			if res.code != 0 || res.stdout != passOf(client) {
				t.Fatalf("hook exited %d and printed %q; want 0 and %q", res.code, res.stdout, passOf(client))
			}
			if res.elapsed < hookDeadline-100*time.Millisecond || res.elapsed > hookDeadline+3*time.Second {
				t.Errorf("hook answered after %s; want about %s", res.elapsed, hookDeadline)
			}
			select {
			case req := <-fake.requests:
				if req.Capability != "workspace.open" {
					t.Errorf("the held request is %s; want workspace.open", req.Capability)
				}
			default:
				t.Error("the daemon never saw workspace.open: the hold was not what delayed the hook")
			}
			if !exists(filepath.Join(root, "a.kt")) {
				t.Error("the file is gone")
			}
		})
	}
	t.Run("stdin never closed", func(t *testing.T) {
		t.Parallel()
		// An *os.File goes to the child as it is, so the run does not wait on a copy.
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pw.Close(); pr.Close() })
		res := runHookStdin(t, shortDir(t), pr, nil, "--client", "claude", "--root", hookProject(t))
		if res.code != 0 || res.stdout != "" || res.elapsed < hookDeadline-100*time.Millisecond || res.elapsed > hookDeadline+3*time.Second {
			t.Errorf("hook exited %d, printed %q after %s; want the pass at about %s", res.code, res.stdout, res.elapsed, hookDeadline)
		}
	})
}

// TestHookNeverExitsTwoAndPrintsNothingElse holds ADR-0061 §4: exit 2 is Claude Code's
// deny, so a misuse exits 1 with a note on stderr and nothing on stdout, and a panic
// is recovered into the client's pass-through with exit 0. --root is optional since
// ADR-0124: `--client claude` alone is a rootless hook, not a misuse (hook_rootless_test.go).
func TestHookNeverExitsTwoAndPrintsNothingElse(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	root := hookProject(t)
	event := claudeBash("rm a.kt", root)
	for _, args := range [][]string{
		nil,
		{"--client", "opencode", "--root", root},
		{"--client", "all", "--root", root},
		{"--client", "claude", "--root", "relative/dir"},
		{"--client", "claude", "--root", ""},
		{"--client", "claude", "--root"},
		{"--client", "cursor", "--root", root, "extra"},
		{"--client", "cursor", "--root", root, "--bogus"},
	} {
		res := runHook(t, state, event, nil, args...)
		if res.code != 1 || res.stdout != "" || !strings.Contains(res.stderr, "droidyard hook:") {
			t.Errorf("hook %v: exit %d, stdout %q, stderr %q; want exit 1, no stdout and a note", args, res.code, res.stdout, res.stderr)
		}
	}

	for _, client := range []string{"claude", "cursor"} {
		res := runHook(t, state, event, []string{panicEnv + "=1"}, "--client", client, "--root", root)
		if res.code != 0 || res.stdout != passOf(client) || !strings.Contains(res.stderr, "recovered from a panic") {
			t.Errorf("%s with a forced panic: exit %d, stdout %q, stderr %q; want exit 0, %q and a note", client, res.code, res.stdout, res.stderr, passOf(client))
		}
	}
}

// TestHookReplacementStartsNoDaemonWhenTheDaemonIsGone holds ADR-0062 §1: if the daemon
// the hook dialled is gone by the time the agent runs the replacement, `trash` fails and
// starts none. Were it to auto-start, it would start a daemon at the shell's own state
// root, a place the agent and the user never chose.
func TestHookReplacementStartsNoDaemonWhenTheDaemonIsGone(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	root := hookProject(t)
	res := runHook(t, p.state, claudeBash("rm a.kt", root), nil, "--client", "claude", "--root", root)
	replacement := replacementOf(t, reasonOf(t, "claude", res.stdout))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// An auto-started daemon takes a moment to appear, so the command's own exit is the
	// proof: it must fail at once, not wait for one.
	stdout, stderr, home, err := runReplacement(t, replacement, root)
	if err == nil {
		t.Fatalf("the replacement succeeded with the daemon gone; stdout %q", stdout)
	}
	if !strings.Contains(stderr, "no daemon answers") {
		t.Errorf("the replacement said %q; want it to report that no daemon answers", stderr)
	}
	// The state root is in the command (--state-dir), so a daemon it started would hold that
	// state root's lock (the file stays after the first daemon's shutdown; the hold is
	// the sign); the scratch HOME covers a command that lost the state root.
	if _, held, _ := daemon.Holder(p.state); held {
		t.Error("the replacement started a daemon at the hook's state root")
	}
	if locks := findLocks(t, home); len(locks) != 0 {
		t.Errorf("the replacement started a daemon at the shell's own state root: %v", locks)
	}
	if !exists(filepath.Join(root, "a.kt")) {
		t.Error("the file is gone")
	}
}
