package main

import (
	"bufio"
	"bytes"
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
	"github.com/jtortosasen/droidyard/internal/platform"
)

// hookResult is one run of `droidyard hook`.
type hookResult struct {
	stdout, stderr string
	code           int
	elapsed        time.Duration
}

// runHook runs a re-executed `droidyard hook` with the state root in
// DROIDYARD_STATE_DIR, as an old client entry would have it from a user who set one, and
// stdin as its event. HOME is a scratch directory, so nothing the hook did by mistake
// could reach the developer's state.
func runHook(t *testing.T, state, stdin string, args ...string) hookResult {
	t.Helper()
	return runHookStdin(t, state, strings.NewReader(stdin), args...)
}

func runHookStdin(t *testing.T, state string, stdin io.Reader, args ...string) hookResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := &exec.Cmd{
		Path: os.Args[0],
		Args: append([]string{"droidyard", "hook"}, args...),
		Env: []string{reexec + "=1", testParentEnv + "=" + strconv.Itoa(os.Getpid()),
			"DROIDYARD_STATE_DIR=" + state, "HOME=" + shortDir(t), "PATH=/nonexistent"},
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	// A hook that waits on its stdin is killed here and reported by its exit code and time.
	hang := time.AfterFunc(10*time.Second, func() { cmd.Process.Kill() })
	defer hang.Stop()
	err := cmd.Wait()
	res := hookResult{stdout: stdout.String(), stderr: stderr.String(), elapsed: time.Since(start)}
	if exit, ok := err.(*exec.ExitError); ok {
		res.code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("waiting for the hook: %v", err)
	}
	return res
}

// claudeEvent is a Claude Code hook event in the shape of the reference's example (m2j
// step 6 (a)): cwd is the shell's directory.
func claudeEvent(event, tool string, input map[string]any, cwd string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": "s1", "transcript_path": "/nonexistent/t.jsonl", "cwd": cwd,
		"permission_mode": "default", "hook_event_name": event, "tool_name": tool,
		"tool_input": input, "tool_use_id": "toolu_1",
	})
	return string(b)
}

// hookFixture is one of the events the clients were observed sending (m2j step 6, m2q and
// m2s step 1), with fields replaced by set.
func hookFixture(t *testing.T, name string, set map[string]any) string {
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

// passOf is the answer a client reads as "let the command through": nothing for Claude
// Code, {} for Cursor.
func passOf(client string) string {
	if client == "cursor" {
		return "{}\n"
	}
	return ""
}

// fakeDaemon answers on a state root's socket as far as the hello and no further: it
// counts the connections and records each request it is sent, and holds every one
// unanswered. It stands in for a daemon whose workspace.open is slow or fails, which the
// real one is not made to be.
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

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// TestInstalledHookPasses holds plan m3j Q4: a client whose settings still run the `droidyard
// hook` an earlier init wrote gets, for every event shape that init's entries received, exit 0
// and that client's pass output, with no socket dialled and nothing written. The events are
// the ones the hook used to act on (an rm it routed to the trash, a Gradle or adb command it
// hinted at and recorded, a Cursor Delete, an edit tool before and after a write), the ones
// it passed, and bytes that are no event; the command lines are the project's (--root) and
// the user-level (none). A daemon is listening, so a hook that dialled would be counted; the
// project and the state root are listed before and after, so a hook that wrote would show; and
// a stdin that never ends does not hold it, as it would an exit that waited to read an event.
// Seen failing: with the hook dialling the daemon, the count was 1 per event; with it
// printing a deny for an rm, the output differed; with it reading its stdin to the end, the
// open stdin held it until the test gave up.
func TestInstalledHookPasses(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	fake := startFakeDaemon(t, state, true)
	root := t.TempDir()
	for _, f := range []string{"a.kt", "dir/b.kt"} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fun f() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	listing := func() string {
		var b strings.Builder
		for _, dir := range []string{root, state} {
			filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					fi, _ := d.Info()
					b.WriteString(p + " " + fi.Mode().String() + " " + strconv.FormatInt(fi.Size(), 10) + " " + fi.ModTime().String() + "\n")
				}
				return nil
			})
		}
		return b.String()
	}
	before := listing()

	file := filepath.Join(root, "a.kt")
	claude := []struct{ name, event string }{
		{"PreToolUse rm", claudeEvent("PreToolUse", "Bash", map[string]any{"command": "rm -f a.kt"}, root)},
		{"PostToolUse gradlew", claudeEvent("PostToolUse", "Bash", map[string]any{"command": "./gradlew assembleDebug"}, root)},
		{"PostToolUseFailure adb", claudeEvent("PostToolUseFailure", "Bash", map[string]any{"command": "adb devices"}, root)},
		{"PreToolUse Write", claudeEvent("PreToolUse", "Write", map[string]any{"file_path": file, "content": "x"}, root)},
		{"PostToolUse Edit", claudeEvent("PostToolUse", "Edit", map[string]any{"file_path": file}, root)},
		{"edit fixture, pre", hookFixture(t, "claude-edit-1-pre-Write", nil)},
		{"edit fixture, post", hookFixture(t, "claude-edit-3-post-Write-update", nil)},
		{"PostToolUse fixture", hookFixture(t, "claude-postToolUse", nil)},
		{"PostToolUseFailure fixture", hookFixture(t, "claude-postToolUseFailure", nil)},
		{"another event", claudeEvent("SessionStart", "", nil, root)},
		{"malformed", "this is not json"},
		{"empty", ""},
		{"an empty object", "{}"},
	}
	cursor := []struct{ name, event string }{
		{"beforeShellExecution rm", hookFixture(t, "cursor-3-beforeShellExecution", map[string]any{"command": "rm -f " + file})},
		{"beforeShellExecution", hookFixture(t, "cursor-0-beforeShellExecution", nil)},
		{"preToolUse Delete", hookFixture(t, "cursor-2-preToolUse", map[string]any{"tool_input": map[string]any{"file_path": file}})},
		{"postToolUse Shell", hookFixture(t, "cursor-4-postToolUse", nil)},
		{"postToolUseFailure", hookFixture(t, "cursor-6-postToolUseFailure", nil)},
		{"postToolUseFailure denied", hookFixture(t, "cursor-7-postToolUseFailure-denied", nil)},
		{"preToolUse Write", hookFixture(t, "cursor-edit-2-pre-Write", nil)},
		{"postToolUse Write", hookFixture(t, "cursor-edit-3-post-Write", nil)},
		{"malformed", "{"},
		{"empty", ""},
	}
	forms := map[string][]string{
		"project": {"--root", root},
		"user":    nil,
	}
	runs := 0
	for form, extra := range forms {
		for client, rows := range map[string][]struct{ name, event string }{"claude": claude, "cursor": cursor} {
			for _, r := range rows {
				res := runHook(t, state, r.event, append([]string{"--client", client}, extra...)...)
				runs++
				if res.code != 0 || res.stdout != passOf(client) || res.stderr != "" {
					t.Errorf("%s %s, %s: exit %d, stdout %q, stderr %q; want 0, %q and nothing on stderr",
						form, client, r.name, res.code, res.stdout, res.stderr, passOf(client))
				}
			}
		}
	}
	if n := fake.dials.Load(); n != 0 {
		t.Errorf("the hook dialled the daemon %d times in %d runs; want 0", n, runs)
	}
	if after := listing(); after != before {
		t.Errorf("the hook wrote: the project and the state root changed\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// An event is drained, not read: a stdin that does not end holds the hook no longer
	// than hookDrainLimit. An *os.File goes to the child as it is, so the run does not wait
	// on a copy.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pw.Close(); pr.Close() })
	res := runHookStdin(t, state, pr, "--client", "claude", "--root", root)
	if res.code != 0 || res.stdout != "" || res.elapsed > hookDrainLimit+1500*time.Millisecond {
		t.Errorf("with a stdin that never ends the hook exited %d, printed %q after %s; want 0, nothing, within the drain limit", res.code, res.stdout, res.elapsed)
	}
}

// TestHookMisuseExitsOne holds ADR-0061 §4 as it stays: exit 2 is Claude Code's deny, so a
// misused command line exits 1 with a note on stderr and nothing on stdout, and no run exits
// 2. --root is optional since ADR-0124: `--client claude` alone is the user-level entry, not a
// misuse.
func TestHookMisuseExitsOne(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	root := t.TempDir()
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
		res := runHook(t, state, "{}", args...)
		if res.code != 1 || res.stdout != "" || !strings.Contains(res.stderr, "droidyard hook:") {
			t.Errorf("hook %v: exit %d, stdout %q, stderr %q; want exit 1, no stdout and a note", args, res.code, res.stdout, res.stderr)
		}
	}
}
