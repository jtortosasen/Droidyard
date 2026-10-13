package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/platform"
	proc "github.com/jtortosasen/droidyard/internal/process"
)

// The shipped wiring of the Git writes (plan m2n P2; ADR-0071 §2, §3): the trust hook and
// the recorded starter main.go gives git.fetch, with a real supervisor and real git.

// gitEnv is the environment these tests run git and the daemon in: the caller's, with a
// temporary HOME (the owner's git configuration, credential helpers included, stays out)
// and no GIT_* variable.
func gitEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	env := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "xdg"), "GIT_CONFIG_NOSYSTEM=1"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	return env
}

func plainGit(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// asProject makes a fixture work tree a project the workspace registry opens.
func asProject(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "settings.gradle"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestGitFetchThroughTheShippedDaemon: ADR-0071 §2 end to end through the shipped registry,
// the daemon's trust hook and a real supervisor. On a trusted root git.fetch brings the
// colleague's commit in and leaves no pid record behind; on an untrusted one it answers
// WORKSPACE_UNTRUSTED with the trust nextStep, runs no git (no record was ever written, no
// hook marker, no remote-tracking ref moved). Seen failing with the hook not passed in
// main.go (Authorize left nil): the trusted root was refused too.
func TestGitFetchThroughTheShippedDaemon(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	env := gitEnv(t)
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == "HOME" || k == "XDG_CONFIG_HOME" || k == "GIT_CONFIG_NOSYSTEM" {
			t.Setenv(k, v)
		}
	}
	trusted := gittest.Remote(t, filepath.Join(t.TempDir(), "trusted"))
	untrusted := gittest.Remote(t, filepath.Join(t.TempDir(), "untrusted"))
	asProject(t, trusted.W)
	asProject(t, untrusted.W)
	canon := platform.Current().Canonical
	p := startInProcessConfig(t, "127.0.0.1:0", nil, nil, 0, `{"trust":{"roots":["`+canon(trusted.W)+`"]}}`)

	pushed := trusted.PushOther(t, 1)
	untrustedPushed := untrusted.PushOther(t, 1)
	for _, root := range []string{canon(trusted.W), canon(untrusted.W)} {
		if _, code := callIn(t, p.state, root, "workspace.open"); code != 0 {
			t.Fatalf("workspace.open %s exited %d", root, code)
		}
	}

	fenv, code := callIn(t, p.state, canon(trusted.W), "git.fetch")
	if code != 0 || fenv.Error != nil || !strings.Contains(string(fenv.Result), `"origin"`) {
		t.Fatalf("git.fetch on the trusted root: exit %d, error %+v, result %s", code, fenv.Error, fenv.Result)
	}
	if got := plainGit(t, env, trusted.W, "rev-parse", "origin/main"); got != pushed {
		t.Errorf("origin/main = %s after the fetch, want the colleague's %s", got, pushed)
	}
	if recs := shellRecords(t, p.state); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the fetch, want none", recs)
	}

	uenv, code := callIn(t, p.state, canon(untrusted.W), "git.fetch")
	if code == 0 || uenv.Error == nil || uenv.Error.Code != "WORKSPACE_UNTRUSTED" || !strings.Contains(uenv.Error.NextStep, "droidyard trust") {
		t.Fatalf("git.fetch on the untrusted root: exit %d, error %+v; want WORKSPACE_UNTRUSTED with the trust nextStep", code, uenv.Error)
	}
	if got := plainGit(t, env, untrusted.W, "rev-parse", "origin/main"); got == untrustedPushed {
		t.Errorf("origin/main moved on the untrusted root: a git ran")
	}
}

// TestGitWriteOutlivesAKilledDaemonAndTheNextStartReapsIt: a daemon SIGKILLed while a fetch
// hangs leaves the fetch's whole group alive and its git-write record on disk (no death
// signal: the leader survives); the next start's Reconcile ends the group before it
// listens, the transport child included, and no ref lock is left. The real-daemon
// counterpart of internal/process's Reconcile test, as shell_reconcile_test.go is for
// shell.exec (process-lifecycle.md §Crash recovery). Seen failing with Reconcile
// signalling the pid only (the Linux variant, the kind given the death signal, is not run on a
// macOS host).
func TestGitWriteOutlivesAKilledDaemonAndTheNextStartReapsIt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	env := gitEnv(t)
	state := shortDir(t)
	cfg := t.TempDir()
	fx := gittest.Remote(t, filepath.Join(t.TempDir(), "fx"))
	asProject(t, fx.W)
	root := platform.Current().Canonical(fx.W)
	if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte(`{"trust":{"roots":[`+strconv.Quote(root)+`]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A fetch that hangs in the transport: the uploadpack writes its pid and sleeps.
	pidFile := filepath.Join(t.TempDir(), "uploadpack.pid")
	script := filepath.Join(t.TempDir(), "uploadpack.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho $$ > '"+pidFile+"'\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plainGit(t, env, fx.W, "config", "remote.origin.uploadpack", script)
	daemonEnv := append(slicesClone(env), "DROIDYARD_CONFIG_DIR="+cfg, "DROIDYARD_STATE_DIR="+state, reexec+"=1")

	srv := droidyardProc(t, daemonEnv, "", "serve")
	srv.expect(t, "listening")
	if opened, code := callIn(t, state, root, "workspace.open"); code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, opened.Error)
	}
	detached := droidyardProc(t, daemonEnv, "", "call", "--no-autostart", "--detach", "--workspace", root, "git.fetch", "--params", `{"remote":"origin"}`)
	if code := detached.wait(t); code != 0 {
		t.Fatalf("the detached call exited %d", code)
	}
	rec := waitShellRecord(t, state)
	if rec.Kind != proc.GitWrite {
		t.Fatalf("record kind = %q, want %q", rec.Kind, proc.GitWrite)
	}
	upload := waitPid(t, pidFile)
	t.Cleanup(func() { syscall.Kill(-rec.PGID, syscall.SIGKILL) })
	if g, _ := syscall.Getpgid(upload); g != rec.PGID {
		t.Fatalf("the uploadpack is in group %d, the record's is %d", g, rec.PGID)
	}

	// Killed outright: nothing runs a shutdown.
	srv.cmd.Process.Signal(syscall.SIGKILL)
	srv.wait(t)
	if !alive(rec.PID) || groupGone(rec.PGID) {
		t.Fatalf("the fetch (pid %d, group %d) died with the daemon; it must outlive a kill -9", rec.PID, rec.PGID)
	}
	if recs := shellRecords(t, state); len(recs) != 1 || recs[0].PID != rec.PID {
		t.Fatalf("processes.json holds %+v after the kill, want the fetch's record %d", recs, rec.PID)
	}

	second := droidyardProc(t, daemonEnv, "", "serve")
	second.expect(t, "listening")
	if !groupGone(rec.PGID) {
		t.Errorf("the fetch's group %d survived the next daemon's reconciliation", rec.PGID)
	}
	if recs := shellRecords(t, state); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after reconciling, want none", recs)
	}
	var left []string
	filepath.WalkDir(filepath.Join(fx.W, ".git"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".lock") {
			left = append(left, p)
		}
		return nil
	})
	if len(left) > 0 {
		t.Errorf("lock files left under .git: %v", left)
	}
	if code := droidyardProc(t, daemonEnv, "", "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	second.wait(t)
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

// waitPid waits for the pid a script wrote.
func waitPid(t *testing.T, file string) int {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(file); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				return n
			}
		}
	}
	t.Fatalf("%s did not appear", file)
	return 0
}
