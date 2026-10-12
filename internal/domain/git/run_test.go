package git

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/status/*.z from real git")

// groupStarter is the supervisor's contract: the child in a process group of its own.
func groupStarter(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

// testEnv is the environment real git runs in for a test: the host's ~/.gitconfig
// (signing, fsmonitor, LFS) cannot change the result, and commits are reproducible.
func testEnv(t testing.TB) []string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
}

// env is a test's real-git setup.
type env struct {
	t   testing.TB
	env []string
	r   *runner
}

// needGit skips the test without git on PATH and returns the setup.
func needGit(t testing.TB) *env {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	e := testEnv(t)
	// The runner scrubs GIT_*, so the system-config switch is added to its environment
	// after the fact; the scrub itself is what TestGitEnvironmentIgnored checks.
	r := newRunner(groupStarter, path, os.Environ())
	r.env = append(r.env, "GIT_CONFIG_NOSYSTEM=1")
	return &env{t: t, env: e, r: r}
}

// g runs plain git (not the runner) in dir and fails the test on a non-zero exit.
func (e *env) g(dir string, args ...string) string {
	e.t.Helper()
	out, err := e.gErr(dir, args...)
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func (e *env) gErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *env) repo() string {
	e.t.Helper()
	dir := e.t.TempDir()
	e.g(dir, "init", "-q", "-b", "main")
	return dir
}

func (e *env) write(dir, rel, content string) {
	e.t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) commit(dir string) {
	e.t.Helper()
	e.g(dir, "add", "-A")
	e.g(dir, "commit", "-q", "-m", "c")
}

// touch gives a file a new mtime and leaves its bytes alone: stat-dirty, same content.
func (e *env) touch(dir, rel string) {
	e.t.Helper()
	when := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, rel), when, when); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) script(name, body string) string {
	e.t.Helper()
	p := filepath.Join(e.t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) appendConfig(dir, text string) {
	e.t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) status(dir string, trusted bool) result {
	e.t.Helper()
	res, err := e.r.run(context.Background(), dir, trusted, nil, statusArgs...)
	if err != nil {
		e.t.Fatalf("status: %v", err)
	}
	return res
}

// entries returns the non-header records of a porcelain v2 -z status.
func entries(out []byte) []string {
	var got []string
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec != "" && !strings.HasPrefix(rec, "# ") {
			got = append(got, rec)
		}
	}
	return got
}

// pathsOf reduces entries to "XY path" (the XY of a `1` record; `?` for untracked).
func pathsOf(out []byte) []string {
	var got []string
	for _, rec := range entries(out) {
		f := strings.SplitN(rec, " ", 9)
		switch f[0] {
		case "1":
			got = append(got, f[1]+" "+f[8])
		case "?":
			got = append(got, "? "+rec[2:])
		default:
			got = append(got, rec)
		}
	}
	return got
}

func equal(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Decision (plan m2l Q1 §2): the daemon's git never takes .git/index.lock and never
// rewrites a stale index. A stat-dirty file (new mtime, same bytes) is the case where
// git's own refresh would write the index; the control proves the setup would have.
func TestStatusLeavesTheIndexAlone(t *testing.T) {
	e := needGit(t)
	dir := e.repo()
	e.write(dir, "a.txt", "one\n")
	e.commit(dir)
	e.touch(dir, "a.txt")
	idx := filepath.Join(dir, ".git", "index")
	before, _ := os.ReadFile(idx)
	st0, _ := os.Stat(idx)

	res := e.status(dir, true)
	if res.ExitCode != 0 {
		t.Fatalf("status exited %d: %s", res.ExitCode, res.Stderr)
	}
	after, _ := os.ReadFile(idx)
	st1, _ := os.Stat(idx)
	if !bytes.Equal(before, after) || !st0.ModTime().Equal(st1.ModTime()) {
		t.Errorf("the index was rewritten by the daemon's status")
	}
	if exists(idx + ".lock") {
		t.Errorf("index.lock was left behind")
	}
	if got := entries(res.Stdout); len(got) != 0 {
		t.Errorf("a stat-dirty file with the same bytes is listed: %q", got)
	}

	// Control: plain git refreshes the stale index, so the assertion above can fail.
	e.g(dir, "status", "--porcelain")
	refreshed, _ := os.ReadFile(idx)
	if bytes.Equal(before, refreshed) {
		t.Errorf("control: plain git status did not rewrite the stale index; the test cannot fail")
	}
}

// Decision (Q1 §2): a commit another process is making holds .git/index.lock; our read
// must not fail on it and must not touch it.
func TestStatusBesideAHeldIndexLock(t *testing.T) {
	e := needGit(t)
	dir := e.repo()
	e.write(dir, "a.txt", "one\n")
	e.write(dir, "b.txt", "two\n")
	e.commit(dir)
	e.write(dir, "a.txt", "changed\n")
	e.write(dir, "new.txt", "n\n")
	e.touch(dir, "b.txt")
	lock := filepath.Join(dir, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("held by a commit in progress"), 0o644); err != nil {
		t.Fatal(err)
	}
	stBefore, _ := os.Stat(lock)

	res := e.status(dir, true)
	if res.ExitCode != 0 {
		t.Fatalf("status exited %d beside a held lock: %s", res.ExitCode, res.Stderr)
	}
	want := []string{".M a.txt", "? new.txt"}
	if got := pathsOf(res.Stdout); !equal(got, want) {
		t.Errorf("entries = %q, want %q", got, want)
	}
	b, err := os.ReadFile(lock)
	stAfter, _ := os.Stat(lock)
	if err != nil || string(b) != "held by a commit in progress" || !stBefore.ModTime().Equal(stAfter.ModTime()) {
		t.Errorf("the held lock changed: %q %v", b, err)
	}
}

// Decision (ADR-0015 §5, Q1 §2): on an untrusted root no repository-configured filter
// or fsmonitor runs, a required driver (git lfs install) does not kill status, and a
// trusted root keeps the user's filters. The control at the end proves the same config
// does run under plain git.
func TestUntrustedRootRunsNoRepositoryFilter(t *testing.T) {
	e := needGit(t)
	marks := t.TempDir()
	filterMark := filepath.Join(marks, "filter")
	fsmMark := filepath.Join(marks, "fsmonitor")
	reqMark := filepath.Join(marks, "required")
	clean := e.script("clean.sh", "touch '"+filterMark+"'\ncat >/dev/null\necho POINTER")
	fsm := e.script("fsm.sh", "touch '"+fsmMark+"'")
	fail := e.script("fail.sh", "touch '"+reqMark+"'\ncat >/dev/null\nexit 1")

	dir := e.repo()
	e.appendConfig(dir, "[filter \"dy\"]\n\tclean = "+clean+"\n\tsmudge = cat\n")
	e.write(dir, ".gitattributes", "c.txt filter=dy\nr.txt filter=req\n")
	e.write(dir, "b.txt", "b\n")
	e.write(dir, "c.txt", "real content\n") // the index holds POINTER, as an LFS pointer would
	e.write(dir, "r.txt", "r\n")
	e.commit(dir)
	e.appendConfig(dir, "[core]\n\tfsmonitor = "+fsm+"\n")
	e.write(dir, "b.txt", "b changed\n")
	e.touch(dir, "c.txt")
	clear := func() {
		for _, m := range []string{filterMark, fsmMark, reqMark} {
			os.Remove(m)
		}
	}
	clear()

	// Untrusted: the repository's filter is not run; c.txt, whose cleaned form would equal
	// the index, shows as modified (the stated cost), and status exits 0.
	res := e.status(dir, false)
	if res.ExitCode != 0 {
		t.Fatalf("untrusted status exited %d: %s", res.ExitCode, res.Stderr)
	}
	for _, m := range []string{filterMark, fsmMark} {
		if exists(m) {
			t.Errorf("untrusted: %s ran", filepath.Base(m))
		}
	}
	if got, want := pathsOf(res.Stdout), []string{".M b.txt", ".M c.txt"}; !equal(got, want) {
		t.Errorf("untrusted entries = %q, want %q", got, want)
	}

	// Trusted: the user's filter runs (c.txt is clean once filtered), fsmonitor never.
	clear()
	res = e.status(dir, true)
	if res.ExitCode != 0 {
		t.Fatalf("trusted status exited %d: %s", res.ExitCode, res.Stderr)
	}
	if !exists(filterMark) {
		t.Errorf("trusted: the user's filter did not run")
	}
	if exists(fsmMark) {
		t.Errorf("trusted: core.fsmonitor ran")
	}
	if got, want := pathsOf(res.Stdout), []string{".M b.txt"}; !equal(got, want) {
		t.Errorf("trusted entries = %q, want %q", got, want)
	}

	// A required driver whose filter produces nothing: git would die; the override
	// is required=false.
	e.appendConfig(dir, "[filter \"req\"]\n\tclean = "+fail+"\n\tsmudge = cat\n\trequired = true\n")
	e.write(dir, "r.txt", "s\n") // same size: git must run the filter to know
	clear()
	res = e.status(dir, false)
	if res.ExitCode != 0 {
		t.Fatalf("untrusted status with a required driver exited %d: %s", res.ExitCode, res.Stderr)
	}
	if exists(reqMark) || exists(filterMark) {
		t.Errorf("untrusted: a filter ran beside the required driver")
	}
	if got, want := pathsOf(res.Stdout), []string{".M b.txt", ".M c.txt", ".M r.txt"}; !equal(got, want) {
		t.Errorf("untrusted entries with a required driver = %q, want %q", got, want)
	}

	// Control: the same configuration under plain git runs every one of them.
	clear()
	e.gErr(dir, "-c", "core.fsmonitor="+fsm, "status", "--porcelain")
	for _, m := range []string{filterMark, fsmMark, reqMark} {
		if !exists(m) {
			t.Errorf("control: plain git did not run %s; the test cannot fail", filepath.Base(m))
		}
	}
}

// Decision (Q1 §2): a `process` filter (git-lfs filter-process) is overridden too.
func TestUntrustedRootRunsNoProcessFilter(t *testing.T) {
	e := needGit(t)
	mark := filepath.Join(t.TempDir(), "process")
	proc := e.script("proc.sh", "touch '"+mark+"'\nexit 1")
	dir := e.repo()
	e.write(dir, ".gitattributes", "p.txt filter=pp\n")
	e.write(dir, "p.txt", "p\n")
	e.commit(dir)
	e.appendConfig(dir, "[filter \"pp\"]\n\tprocess = "+proc+"\n")
	e.write(dir, "p.txt", "q\n") // same size: git must run the filter to know

	res := e.status(dir, false)
	if res.ExitCode != 0 || exists(mark) {
		t.Fatalf("untrusted: exit %d, process filter ran %v: %s", res.ExitCode, exists(mark), res.Stderr)
	}
	if got, want := pathsOf(res.Stdout), []string{".M p.txt"}; !equal(got, want) {
		t.Errorf("entries = %q, want %q", got, want)
	}
	e.status(dir, true) // control: a trusted root starts it (git may die on the broken filter)
	if !exists(mark) {
		t.Errorf("control: a trusted run did not start the process filter; the test cannot fail")
	}
}

// Decision (Q1 §2): the whole process group dies on cancel; a filter's child (git-lfs
// filter-process) must not outlive git.
func TestCancelKillsTheGroup(t *testing.T) {
	e := needGit(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	slow := e.script("slow.sh", "echo $$ > '"+pidFile+"'\nexec sleep 60")
	dir := e.repo()
	e.write(dir, ".gitattributes", "c.txt filter=slow\n")
	e.write(dir, "c.txt", "one\n")
	e.commit(dir)
	e.appendConfig(dir, "[filter \"slow\"]\n\tclean = "+slow+"\n")
	e.write(dir, "c.txt", "two\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := e.r.run(ctx, dir, true, nil, statusArgs...)
		done <- err
	}()
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pid = n
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the filter never started")
	}
	defer syscall.Kill(pid, syscall.SIGKILL) // the test's own child, if the assertion below fails
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	gone := false
	for i := 0; i < 150 && !gone; i++ {
		gone = syscall.Kill(pid, 0) == syscall.ESRCH
		if !gone {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !gone {
		t.Errorf("filter process %d outlived the cancelled status", pid)
	}
}

// Decision (Q1 §2): the run's own timeout ends the call with ErrTimeout, not a bare
// context error, and kills the group.
func TestTimeoutKillsTheGroup(t *testing.T) {
	e := needGit(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	slow := e.script("slow.sh", "echo $$ > '"+pidFile+"'\nexec sleep 60")
	dir := e.repo()
	e.write(dir, ".gitattributes", "c.txt filter=slow\n")
	e.write(dir, "c.txt", "one\n")
	e.commit(dir)
	e.appendConfig(dir, "[filter \"slow\"]\n\tclean = "+slow+"\n")
	e.write(dir, "c.txt", "two\n")
	e.r.timeout = 500 * time.Millisecond
	_, err := e.r.run(context.Background(), dir, true, nil, statusArgs...)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatal("the filter never started")
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	defer syscall.Kill(pid, syscall.SIGKILL)
	gone := false
	for i := 0; i < 150 && !gone; i++ {
		gone = syscall.Kill(pid, 0) == syscall.ESRCH
		if !gone {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !gone {
		t.Errorf("filter process %d outlived the timeout", pid)
	}
}

// Decision (Q1 §2): GIT_* in the daemon's environment cannot point our git at another
// repository.
func TestGitEnvironmentIgnored(t *testing.T) {
	e := needGit(t)
	other := e.repo()
	e.write(other, "other.txt", "x\n")
	e.commit(other)
	e.write(other, "other.txt", "y\n")
	dir := e.repo()
	e.write(dir, "mine.txt", "x\n")
	e.commit(dir)
	e.write(dir, "mine.txt", "y\n")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	t.Setenv("GIT_WORK_TREE", other)
	path, _ := exec.LookPath("git")
	r := newRunner(groupStarter, path, os.Environ()) // the daemon's environment, GIT_* included
	r.env = append(r.env, "GIT_CONFIG_NOSYSTEM=1")
	res, err := r.run(context.Background(), dir, true, nil, statusArgs...)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("status: %v exit %d: %s", err, res.ExitCode, res.Stderr)
	}
	if got, want := pathsOf(res.Stdout), []string{".M mine.txt"}; !equal(got, want) {
		t.Errorf("entries = %q, want %q (the environment's repository leaked in)", got, want)
	}
}

// Decision: a filter driver named so that it cannot be written as a -c override is a
// refusal, not a silent gap.
func TestUnwritableDriverNameIsRefused(t *testing.T) {
	e := needGit(t)
	dir := e.repo()
	e.appendConfig(dir, "[filter \"a=b\"]\n\tclean = cat\n")
	_, err := e.r.run(context.Background(), dir, false, nil, statusArgs...)
	if !errors.Is(err, ErrUnsafeDriver) {
		t.Fatalf("err = %v, want ErrUnsafeDriver", err)
	}
}

// Decision: stdout is capped as it is read, and the flag says so.
func TestStdoutIsCapped(t *testing.T) {
	e := needGit(t)
	dir := e.repo()
	e.write(dir, "a.txt", strings.Repeat("x", stdoutLimit+1000))
	e.commit(dir)
	res, err := e.r.run(context.Background(), dir, true, []byte("HEAD:a.txt\n"), "cat-file", "--batch")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("cat-file: %v exit %d", err, res.ExitCode)
	}
	if !res.Truncated || len(res.Stdout) != stdoutLimit {
		t.Errorf("stdout = %d bytes, truncated %v; want %d and true", len(res.Stdout), res.Truncated, stdoutLimit)
	}
}

// scenario builds a repository and returns the directory status runs in.
type scenario struct {
	name  string
	build func(e *env) string
}

var scenarios = []scenario{
	{"clean", func(e *env) string {
		d := e.repo()
		e.write(d, "a.txt", "a\n")
		e.commit(d)
		return d
	}},
	{"worktree", func(e *env) string {
		d := e.repo()
		for _, f := range []string{"m_unstaged.txt", "m_staged.txt", "mm.txt", "d_unstaged.txt", "d_staged.txt", "md.txt", "rmcached.txt", "sp ace.txt", "ünï.txt", "dir/in dir.txt"} {
			e.write(d, f, "base\n")
		}
		e.write(d, ".gitignore", "*.log\n")
		e.commit(d)
		e.write(d, "m_unstaged.txt", "changed\n")
		e.write(d, "m_staged.txt", "changed\n")
		e.g(d, "add", "m_staged.txt")
		e.write(d, "mm.txt", "staged\n")
		e.g(d, "add", "mm.txt")
		e.write(d, "mm.txt", "staged and more\n")
		os.Remove(filepath.Join(d, "d_unstaged.txt"))
		e.g(d, "rm", "-q", "d_staged.txt")
		e.write(d, "md.txt", "staged\n")
		e.g(d, "add", "md.txt")
		os.Remove(filepath.Join(d, "md.txt"))
		e.g(d, "rm", "-q", "--cached", "rmcached.txt")
		e.write(d, "sp ace.txt", "changed\n")
		e.write(d, "ünï.txt", "changed\n")
		e.write(d, "dir/in dir.txt", "changed\n")
		e.write(d, "a_staged.txt", "a_staged\n")
		e.g(d, "add", "a_staged.txt")
		e.write(d, "am.txt", "am\n")
		e.g(d, "add", "am.txt")
		e.write(d, "am.txt", "new and more\n")
		e.write(d, "ad.txt", "ad\n")
		e.g(d, "add", "ad.txt")
		os.Remove(filepath.Join(d, "ad.txt"))
		e.write(d, "ita.txt", "ita\n")
		e.g(d, "add", "-N", "ita.txt")
		e.write(d, "u.txt", "untracked\n")
		e.write(d, "dir2/u2.txt", "untracked\n")
		e.write(d, "ignored.log", "ignored\n")
		return d
	}},
	{"rename", func(e *env) string {
		d := e.repo()
		for _, f := range []string{"r1.txt", "r2.txt", "r3.txt", "r4.txt"} {
			e.write(d, f, "line one\nline two\nline three\nline four\n")
		}
		e.commit(d)
		e.g(d, "mv", "r1.txt", "moved1.txt")
		e.g(d, "mv", "r2.txt", "moved2.txt")
		e.write(d, "moved2.txt", "line one\nline two\nline three\nline four\nline five\n")
		e.g(d, "mv", "r3.txt", "moved3.txt")
		os.Remove(filepath.Join(d, "moved3.txt"))
		// A plain mv, not told to git: deleted plus untracked.
		if err := os.Rename(filepath.Join(d, "r4.txt"), filepath.Join(d, "plain-moved.txt")); err != nil {
			e.t.Fatal(err)
		}
		return d
	}},
	// An intent-to-add file whose bytes equal a deleted tracked file's: git pairs them
	// as ".R", a code Q1 §4's table does not list.
	{"intent-rename", func(e *env) string {
		d := e.repo()
		e.write(d, "old.txt", "line one\nline two\nline three\n")
		e.commit(d)
		os.Remove(filepath.Join(d, "old.txt"))
		e.write(d, "new.txt", "line one\nline two\nline three\n")
		e.g(d, "add", "-N", "new.txt")
		return d
	}},
	{"conflict", func(e *env) string {
		d := e.repo()
		e.write(d, "f.txt", "base\n")
		e.commit(d)
		e.g(d, "checkout", "-q", "-b", "other")
		e.write(d, "f.txt", "other\n")
		e.write(d, "both.txt", "other add\n")
		e.commit(d)
		e.g(d, "checkout", "-q", "main")
		e.write(d, "f.txt", "main\n")
		e.write(d, "both.txt", "main add\n")
		e.commit(d)
		if out, err := e.gErr(d, "merge", "other"); err == nil {
			e.t.Fatalf("the merge did not conflict:\n%s", out)
		}
		return d
	}},
	{"unborn", func(e *env) string {
		d := e.repo()
		e.write(d, "staged.txt", "s\n")
		e.g(d, "add", "staged.txt")
		e.write(d, "untracked.txt", "u\n")
		return d
	}},
	{"detached", func(e *env) string {
		d := e.repo()
		e.write(d, "a.txt", "a\n")
		e.commit(d)
		e.g(d, "checkout", "-q", "--detach")
		e.write(d, "a.txt", "changed\n")
		return d
	}},
	{"nested-repo", func(e *env) string {
		d := e.repo()
		e.write(d, "a.txt", "a\n")
		e.commit(d)
		n := filepath.Join(d, "nested")
		if err := os.MkdirAll(n, 0o755); err != nil {
			e.t.Fatal(err)
		}
		e.g(n, "init", "-q", "-b", "main")
		e.write(n, "x.txt", "x\n")
		e.commit(n)
		return d
	}},
	{"subdir", func(e *env) string {
		d := e.repo()
		e.write(d, "top.txt", "t\n")
		e.write(d, "app/in.txt", "i\n")
		e.write(d, "app/sub/deep.txt", "d\n")
		e.commit(d)
		e.write(d, "top.txt", "changed\n")
		e.write(d, "app/in.txt", "changed\n")
		e.write(d, "app/sub/new.txt", "n\n")
		return filepath.Join(d, "app")
	}},
}

// Decision: step 2's fixtures are real git output, saved, and a git that changes its
// output fails here first. `go test ./internal/domain/git -update` rewrites them.
func TestStatusFixtures(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			e := needGit(t)
			root := sc.build(e)
			res := e.status(root, true)
			if res.ExitCode != 0 {
				t.Fatalf("status exited %d: %s", res.ExitCode, res.Stderr)
			}
			p := filepath.Join("testdata", "status", sc.name+".z")
			if *update {
				if err := os.WriteFile(p, res.Stdout, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("%v (run with -update)", err)
			}
			if !bytes.Equal(want, res.Stdout) {
				t.Errorf("status output differs from %s:\n got %q\nwant %q", p, res.Stdout, want)
			}
		})
	}
}

// BenchmarkStatus is the cost of Q1's status on a generated 5,000-file repository,
// every file clean (the daemon's steady state).
func BenchmarkStatus(b *testing.B) {
	e := needGit(b)
	dir := e.repo()
	for i := 0; i < 5000; i++ {
		e.write(dir, fmt.Sprintf("d%02d/f%04d.txt", i%50, i), fmt.Sprintf("file %d\n", i))
	}
	e.commit(dir)
	e.g(dir, "status", "--porcelain") // refresh the index once, as a developer's own git does
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := e.r.run(context.Background(), dir, true, nil, statusArgs...)
		if err != nil || res.ExitCode != 0 {
			b.Fatalf("status: %v exit %d", err, res.ExitCode)
		}
	}
}
