package assistant

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// groupStarter is the supervisor's contract: the child in a process group of its own.
func groupStarter(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func (g gitT) tracked(root string, paths ...string) (Repo, error) {
	g.t.Helper()
	gitPath, _ := exec.LookPath("git")
	return Tracked(context.Background(), groupStarter, gitPath, g.env, root, paths)
}

func (g gitT) script(name, body string) string {
	g.t.Helper()
	p := filepath.Join(g.t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		g.t.Fatal(err)
	}
	return p
}

func (g gitT) appendConfig(dir, text string) {
	g.t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		g.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		g.t.Fatal(err)
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Decision (ADR-0124 Q4): the facts init's ignore step needs are the top, the common
// directory (the main .git, from a linked worktree too) and which asked paths are tracked,
// named relative to the top whatever directory the root is.
func TestTrackedReportsTopCommonDirAndTrackedPaths(t *testing.T) {
	t.Parallel()
	g := needGit(t)
	dir := g.repo()
	writeAt(t, dir, "app/.mcp.json", "{}\n")
	g.run(dir, "add", "-A")
	g.run(dir, "commit", "-q", "-m", "c")
	writeAt(t, dir, "app/opencode.json", "{}\n")
	wt := filepath.Join(t.TempDir(), "wt")
	g.run(dir, "worktree", "add", "-q", "-b", "side", wt)

	realDir, _ := filepath.EvalSymlinks(dir)
	repo, err := g.tracked(filepath.Join(dir, "app"), ".mcp.json", "opencode.json")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Top != realDir || repo.CommonDir != filepath.Join(realDir, ".git") {
		t.Errorf("top %q common %q, want %q and its .git", repo.Top, repo.CommonDir, realDir)
	}
	if len(repo.Tracked) != 1 || !repo.Tracked["app/.mcp.json"] {
		t.Errorf("tracked = %v, want only app/.mcp.json (top-relative)", repo.Tracked)
	}

	repo, err = g.tracked(filepath.Join(wt, "app"))
	if err != nil {
		t.Fatal(err)
	}
	realWT, _ := filepath.EvalSymlinks(wt)
	if repo.Top != realWT || repo.CommonDir != filepath.Join(realDir, ".git") {
		t.Errorf("worktree: top %q common %q, want %q and the main .git", repo.Top, repo.CommonDir, realWT)
	}
}

func TestTrackedOutsideARepository(t *testing.T) {
	t.Parallel()
	g := needGit(t)
	dir := t.TempDir()
	if _, err := g.tracked(dir, "x"); !errors.Is(err, ErrNotRepository) {
		t.Errorf("err = %v, want ErrNotRepository", err)
	}
	if _, err := Tracked(context.Background(), groupStarter, "", nil, dir, nil); !errors.Is(err, ErrGitNotFound) {
		t.Errorf("no git: err = %v, want ErrGitNotFound", err)
	}
}

// Decision (ADR-0124 Q4): a path is matched literally, so a name with glob characters
// neither matches another file nor is read as magic.
func TestTrackedMatchesPathsLiterally(t *testing.T) {
	t.Parallel()
	g := needGit(t)
	dir := g.repo()
	writeAt(t, dir, "a.json", "{}\n")
	g.run(dir, "add", "-A")
	g.run(dir, "commit", "-q", "-m", "c")
	repo, err := g.tracked(dir, "*.json", ":(top)a.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.Tracked) != 0 {
		t.Errorf("tracked = %v: a glob or pathspec magic matched", repo.Tracked)
	}
}

// Decision (ADR-0124 Q4, ADR-0015 §5): the read goes through the hardened runner and the
// root is untrusted, so a configured filter driver is overridden on the git calls it makes.
// A wrapper around git records the argv; without the overrides they would be missing.
func TestTrackedRunsThroughTheRunner(t *testing.T) {
	t.Parallel()
	g := needGit(t)
	dir := g.repo()
	g.appendConfig(dir, "[filter \"dy\"]\n\tclean = cat\n\tsmudge = cat\n\tprocess = cat\n\trequired = true\n")
	writeAt(t, dir, "a.txt", "a\n")
	g.run(dir, "add", "-A")
	g.run(dir, "commit", "-q", "-m", "c")

	realGit, _ := exec.LookPath("git")
	log := filepath.Join(t.TempDir(), "argv")
	wrapper := g.script("git", `echo "$*" >> '`+log+`'`+"\n"+`exec '`+realGit+`' "$@"`)
	repo, err := Tracked(context.Background(), groupStarter, wrapper, g.env, dir, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !repo.Tracked["a.txt"] {
		t.Errorf("tracked = %v, want a.txt", repo.Tracked)
	}
	b, _ := os.ReadFile(log)
	var lsFiles, revParse string
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.Contains(l, " ls-files "):
			lsFiles = l
		case strings.Contains(l, " rev-parse "):
			revParse = l
		}
	}
	for name, l := range map[string]string{"rev-parse": revParse, "ls-files": lsFiles} {
		if l == "" {
			t.Fatalf("%s was not run through the wrapper; log:\n%s", name, b)
		}
		for _, want := range []string{"-c filter.dy.clean=", "-c filter.dy.smudge=", "-c filter.dy.process=", "-c filter.dy.required=false", "-c core.hooksPath=/dev/null", "-c core.fsmonitor=false"} {
			if !strings.Contains(l, want) {
				t.Errorf("%s ran without %q: %s", name, want, l)
			}
		}
	}
}

// Decision (ADR-0015 §5, fail closed): a filter git selects with `filter=` and runs, whose
// driver name cannot be written as an override (here the empty one), refuses the read; it is
// never skipped.
func TestEmptyDriverNameIsRefusedNotSkipped(t *testing.T) {
	t.Parallel()
	g := needGit(t)
	dir := g.repo()
	g.appendConfig(dir, "[filter \"\"]\n\tclean = cat\n")
	if _, err := g.tracked(dir, "README.md"); !errors.Is(err, ErrUnsafeDriver) {
		t.Errorf("err = %v, want ErrUnsafeDriver", err)
	}
}

// Decision: a two-part key (filter.<key>, no subsection) is not a driver, because git
// ignores it; the read skips it. Proven against real git in the Git domain's test before it
// left; here only that the read still goes through.
func TestTwoPartFilterKeyIsNoDriver(t *testing.T) {
	t.Parallel()
	g := needGit(t)
	dir := g.repo()
	g.appendConfig(dir, "[filter]\n\tclean = cat\n")
	repo, err := g.tracked(dir, "README.md")
	if err != nil || !repo.Tracked["README.md"] {
		t.Errorf("a two-part filter key: %v, tracked %v; want the read to go through", err, repo.Tracked)
	}
}

// Decision: the list of configured filters is never trusted incomplete. A driver listed
// after the stdout cap would otherwise run.
func TestTruncatedDriverListIsRefused(t *testing.T) {
	t.Parallel()
	g := needGit(t)
	dir := g.repo()
	g.appendConfig(dir, "[filter \"j\"]\n"+strings.Repeat("\txxxxxx = 1\n", 300000))
	if _, err := g.tracked(dir, "README.md"); !errors.Is(err, ErrUnsafeDriver) {
		t.Errorf("err = %v, want ErrUnsafeDriver", err)
	}
}

// Decision (ADR-0015 §5): a repository that configures core.fsmonitor as a script never has
// it run by the ignore step's read. The control is plain git running the same hook.
func TestTrackedIgnoresFsmonitor(t *testing.T) {
	t.Parallel()
	g := needGit(t)
	dir := g.repo()
	mark := filepath.Join(t.TempDir(), "mark")
	fsm := g.script("fsm.sh", "touch '"+mark+"'")
	g.appendConfig(dir, "[core]\n\tfsmonitor = "+fsm+"\n")
	writeAt(t, dir, "a.txt", "changed\n")
	repo, err := g.tracked(dir, "README.md")
	if err != nil || !repo.Tracked["README.md"] {
		t.Fatalf("Tracked: %v, tracked %v", err, repo.Tracked)
	}
	if pathExists(mark) {
		t.Errorf("the repository's fsmonitor hook ran")
	}
	g.run(dir, "status", "--porcelain")
	if !pathExists(mark) {
		t.Errorf("control: plain git did not run the fsmonitor hook; the test cannot fail")
	}
}
