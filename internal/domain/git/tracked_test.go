package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func (e *env) tracked(root string, paths ...string) (Repo, error) {
	e.t.Helper()
	gitPath, _ := exec.LookPath("git")
	return Tracked(context.Background(), groupStarter, gitPath, e.osEnviron(), root, paths)
}

// Decision (ADR-0124 Q4): the facts init's ignore step needs are the top, the common
// directory (the main .git, from a linked worktree too) and which asked paths are
// tracked, named relative to the top whatever directory the root is.
func TestTrackedReportsTopCommonDirAndTrackedPaths(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	dir := e.repo()
	e.write(dir, "app/.mcp.json", "{}\n")
	e.commit(dir)
	e.write(dir, "app/opencode.json", "{}\n")
	wt := filepath.Join(t.TempDir(), "wt")
	e.g(dir, "worktree", "add", "-q", "-b", "side", wt)

	realDir, _ := filepath.EvalSymlinks(dir)
	repo, err := e.tracked(filepath.Join(dir, "app"), ".mcp.json", "opencode.json")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Top != realDir || repo.CommonDir != filepath.Join(realDir, ".git") {
		t.Errorf("top %q common %q, want %q and its .git", repo.Top, repo.CommonDir, realDir)
	}
	if len(repo.Tracked) != 1 || !repo.Tracked["app/.mcp.json"] {
		t.Errorf("tracked = %v, want only app/.mcp.json (top-relative)", repo.Tracked)
	}

	repo, err = e.tracked(filepath.Join(wt, "app"))
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
	e := needGit(t)
	dir := t.TempDir()
	if _, err := e.tracked(dir, "x"); !errors.Is(err, ErrNotRepository) {
		t.Errorf("err = %v, want ErrNotRepository", err)
	}
	if _, err := Tracked(context.Background(), groupStarter, "", nil, dir, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("no git: err = %v, want ErrNotFound", err)
	}
}

// Decision (ADR-0124 Q4): a path is matched literally, so a name with glob characters
// neither matches another file nor is read as magic.
func TestTrackedMatchesPathsLiterally(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	dir := e.repo()
	e.write(dir, "a.json", "{}\n")
	e.commit(dir)
	repo, err := e.tracked(dir, "*.json", ":(top)a.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.Tracked) != 0 {
		t.Errorf("tracked = %v: a glob or pathspec magic matched", repo.Tracked)
	}
}

// Decision (ADR-0124 Q4, ADR-0015 §5): the read goes through the runner and the root is
// untrusted, so a configured filter driver is overridden on the git calls it makes. A
// wrapper around git records the argv; with the root trusted instead the overrides would
// be missing.
func TestTrackedRunsThroughTheRunner(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	dir := e.repo()
	e.appendConfig(dir, "[filter \"dy\"]\n\tclean = cat\n\tsmudge = cat\n\tprocess = cat\n\trequired = true\n")
	e.write(dir, "a.txt", "a\n")
	e.commit(dir)

	realGit, _ := exec.LookPath("git")
	log := filepath.Join(t.TempDir(), "argv")
	wrapper := e.script("git", `echo "$*" >> '`+log+`'`+"\n"+`exec '`+realGit+`' "$@"`)
	repo, err := Tracked(context.Background(), groupStarter, wrapper, e.osEnviron(), dir, []string{"a.txt"})
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
		for _, want := range []string{"-c filter.dy.clean=", "-c filter.dy.smudge=", "-c filter.dy.process=", "-c filter.dy.required=false", "-c core.hooksPath=/dev/null"} {
			if !strings.Contains(l, want) {
				t.Errorf("%s ran without %q: %s", name, want, l)
			}
		}
	}
}
