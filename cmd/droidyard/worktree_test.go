package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// gitIn runs git in dir with the caller's GIT_* and global configuration kept out, as
// git_read_test does: inside a pre-commit hook GIT_DIR and GIT_INDEX_FILE name the
// repository being committed.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	cmd := exec.Command(gitPath, append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "HOME=" + t.TempDir()}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// watchRows connects a WebSocket to the daemon serving state, calls workspace.watch and
// returns its rows.
func watchRows(t *testing.T, state string) []workspace.Listed {
	t.Helper()
	info, err := daemon.ReadLock(state)
	if err != nil || info.HTTP == nil {
		t.Fatalf("daemon.lock says (%+v, %v) once serve listens", info, err)
	}
	addr := *info.HTTP
	raw, err := os.ReadFile(filepath.Join(state, daemon.TokenFile))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws://"+addr+"/v1/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://" + addr}}})
	if err != nil {
		t.Fatalf("the upgrade: %v (response %v)", err, resp)
	}
	w := pump(t, c)
	w.writeText(`{"auth":"` + strings.TrimSpace(string(raw)) + `"}`)
	w.send("w", "workspace.watch", "", "", nil)
	_, f := w.collect("w")
	if f.Error != nil {
		t.Fatalf("workspace.watch: %+v", f.Error)
	}
	var res workspace.WatchResult
	if err := json.Unmarshal(f.Result, &res); err != nil {
		t.Fatalf("workspace.watch result %s: %v", f.Result, err)
	}
	return res.Workspaces
}

func rowFor(rows []workspace.Listed, root string) (workspace.Listed, bool) {
	for _, r := range rows {
		if r.Root == root {
			return r, true
		}
	}
	return workspace.Listed{}, false
}

// TestWorktreeOfATrustedProject is ADR-0081 §1, §5 and ADR-0082 §6 end to end, on a real
// `serve`, a real `git worktree add` and the fake sidecar. The project is the main tree,
// the worktree one of its workspaces. Untrusted, the worktree opens with trusted false and
// the main tree as mainRoot, and build.tasks answers WORKSPACE_UNTRUSTED naming the main
// tree, with no sidecar started. After `droidyard trust` on the MAIN tree only,
// build.tasks on the worktree lists the tasks and the sidecar opens the worktree, never
// the main tree: trust is the project's, the Gradle root the worktree's own. Closed,
// workspace.watch lists the worktree's row with mainRoot; removed from disk, it is gone,
// while a closed plain project whose directory is gone is still listed.
//
// Seen failing: before step 2 (policy.Trusted by path only) the trusted build.tasks was
// WORKSPACE_UNTRUSTED; with workspace.watch's existence test removed the removed worktree
// stayed listed.
func TestWorktreeOfATrustedProject(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg, events := fakeSidecar(t, gradleRootModel)
	base := platform.Current().Canonical(t.TempDir())
	main := filepath.Join(base, "main")
	wt := filepath.Join(base, "wt")
	plain := filepath.Join(base, "plain")
	for _, d := range []string{main, plain} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(main, "settings.gradle.kts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q", "-b", "main")
	gitIn(t, main, "add", "-A")
	gitIn(t, main, "commit", "-q", "-m", "c")
	gitIn(t, main, "worktree", "add", "-q", "../wt", "-b", "task")

	srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	srv.expect(t, "listening")

	env, code := callIn(t, state, wt, "workspace.open")
	if code != 0 {
		t.Fatalf("open of the worktree exited %d: %+v", code, env.Error)
	}
	if info := workspaceOf(t, env); info.Trusted || info.MainRoot != main || info.Root != wt {
		t.Errorf("worktree open = %+v; want untrusted, root %s, mainRoot %s", info, wt, main)
	}

	env, code = callIn(t, state, wt, "build.tasks")
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
		t.Fatalf("an untrusted worktree's build.tasks = (%+v, %d); want WORKSPACE_UNTRUSTED", env.Error, code)
	}
	if !strings.Contains(env.Error.NextStep, main) {
		t.Errorf("the nextStep is %q; want it to name the main tree %s", env.Error.NextStep, main)
	}
	if n := starts(t, events); n != 0 {
		t.Errorf("a sidecar was spawned for an untrusted worktree (%d starts); want none", n)
	}

	if code := droidyard(t, state, "trust", main).wait(t); code != 0 {
		t.Fatalf("droidyard trust %s exited %d", main, code)
	}
	env, code = callIn(t, state, wt, "build.tasks")
	if code != 0 || env.Error != nil {
		t.Fatalf("a trusted worktree's build.tasks exited %d: %+v", code, env.Error)
	}
	var res struct {
		Tasks []struct {
			Path string `json:"path"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Tasks) != 1 || res.Tasks[0].Path != ":app:assembleDebug" {
		t.Errorf("build.tasks = %+v; want the fake's :app:assembleDebug", res.Tasks)
	}
	lines := eventLines(t, events)
	if !contains(lines, "opened "+wt) || contains(lines, "opened "+main) {
		t.Errorf("the sidecar's log is %q; want it to have opened %s and never %s", lines, wt, main)
	}
	if env, code = callIn(t, state, wt, "workspace.describe"); code != 0 {
		t.Fatalf("describe exited %d: %+v", code, env.Error)
	} else if info := workspaceOf(t, env); !info.Trusted || info.MainRoot != main {
		t.Errorf("worktree describe = %+v; want trusted and mainRoot %s", info, main)
	}

	// A plain project, closed and then deleted, to compare with the worktree.
	if env, code := callIn(t, state, plain, "workspace.open"); code != 0 {
		t.Fatalf("open of the plain project exited %d: %+v", code, env.Error)
	}
	for _, d := range []string{wt, plain} {
		if env, code := callIn(t, state, d, "workspace.close"); code != 0 {
			t.Fatalf("close of %s exited %d: %+v", d, code, env.Error)
		}
	}
	row, ok := rowFor(watchRows(t, state), wt)
	if !ok || row.MainRoot != main || row.State != workspace.Closed {
		t.Errorf("the closed worktree's row = (%+v, %v); want it listed, closed, with mainRoot %s", row, ok, main)
	}
	if row, ok := rowFor(watchRows(t, state), plain); !ok || row.MainRoot != "" {
		t.Errorf("the plain project's row = (%+v, %v); want it listed without mainRoot", row, ok)
	}

	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(plain); err != nil {
		t.Fatal(err)
	}
	rows := watchRows(t, state)
	if r, ok := rowFor(rows, wt); ok {
		t.Errorf("the removed worktree is still listed: %+v", r)
	}
	if _, ok := rowFor(rows, plain); !ok {
		t.Errorf("the closed plain project whose directory is gone is not listed; want it, as before")
	}

	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	srv.wait(t)
}
