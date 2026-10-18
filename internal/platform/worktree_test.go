package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// gitIn runs the host git in dir with the user's and the system's configuration out of
// the way, so a test never reads or changes this machine's git setup.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "protocol.file.allow=always",
		"-c", "init.defaultBranch=main",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newMain makes a repository with one commit under a fresh temporary directory and
// returns the directory holding it and the canonical main tree.
func newMain(t *testing.T) (base, main string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	base = Current().Canonical(t.TempDir())
	main = filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q")
	// No auto maintenance: a commit starts a detached `git maintenance run --auto`, whose
	// worktree-prune task (git 2.50 and later) deleted the entries a test was planting
	// (main's CI, 2026-10-18: "open …/.git/worktrees/w012/commondir: no such file").
	gitIn(t, main, "config", "maintenance.auto", "false")
	gitIn(t, main, "config", "gc.auto", "0")
	if err := os.WriteFile(filepath.Join(main, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "add", "f")
	gitIn(t, main, "commit", "-q", "-m", "one")
	return base, main
}

// addWorktree makes a genuine linked worktree of repo at path, on a branch named after
// it, and returns its administrative directory.
func addWorktree(t *testing.T, repo, path string, extra ...string) string {
	t.Helper()
	args := append(append([]string{}, extra...), "worktree", "add", "-q", "-b", filepath.Base(path), path)
	gitIn(t, repo, args...)
	return adminOf(t, path)
}

// adminOf is the administrative directory a worktree's `.git` file names.
func adminOf(t *testing.T, worktree string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	p := strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir: "))
	if !filepath.IsAbs(p) {
		p = filepath.Join(worktree, p)
	}
	return Current().Canonical(p)
}

func plant(t *testing.T, dir, dotGit string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(dotGit), 0o600); err != nil {
		t.Fatal(err)
	}
}

// listed reports whether git itself lists worktree among main's worktrees: the genuine
// cases are the ones git recognises, so the daemon's file test is checked against it.
func listed(t *testing.T, main, worktree string) bool {
	t.Helper()
	for _, line := range strings.Split(gitIn(t, main, "worktree", "list", "--porcelain"), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok && Current().Canonical(p) == Current().Canonical(worktree) {
			return true
		}
	}
	return false
}

// want asserts LinkedWorktree's answer: the main tree, or none.
func want(t *testing.T, what, root, main string) {
	t.Helper()
	got, ok := LinkedWorktree(Current(), root)
	if main == "" {
		if ok {
			t.Errorf("%s: LinkedWorktree(%s) = %q, true; want not a worktree", what, root, got)
		}
		return
	}
	if !ok || got != main {
		t.Errorf("%s: LinkedWorktree(%s) = %q, %v; want %q", what, root, got, ok, main)
	}
}

// TestLinkedWorktree pins ADR-0081 §2 against real repositories made with the host git.
// Each test of the rule has a case that fails when that test is removed; the comment on
// the case says which. A trust decision that a planted `.git` file could pass would
// run an attacker's code with the user's rights.
func TestLinkedWorktree(t *testing.T) {
	t.Run("genuine worktrees, inside the main tree and outside it", func(t *testing.T) {
		base, main := newMain(t)
		inside := filepath.Join(main, ".wt", "in")
		outside := filepath.Join(base, "out")
		addWorktree(t, main, inside)
		addWorktree(t, main, outside)
		for _, wt := range []string{inside, outside} {
			if !listed(t, main, wt) {
				t.Fatalf("git does not list %s as a worktree of %s", wt, main)
			}
			want(t, "genuine worktree", wt, main)
		}
		want(t, "the main tree is not a worktree", main, "")
	})

	t.Run("a planted .git file naming a genuine worktree's data", func(t *testing.T) {
		base, main := newMain(t)
		wt := filepath.Join(base, "wt")
		admin := addWorktree(t, main, wt)
		planted := filepath.Join(base, "planted")
		plant(t, planted, "gitdir: "+admin+"\n")
		// The decision: the back-link (P/gitdir names R/.git) is what proves a worktree.
		// Without that test the planted directory is accepted.
		want(t, "planted", planted, "")
		want(t, "the genuine worktree still", wt, main)
	})

	t.Run("a .git file naming the common directory, or a submodule's data", func(t *testing.T) {
		base, main := newMain(t)
		naming := filepath.Join(base, "naming-common")
		plant(t, naming, "gitdir: "+filepath.Join(main, ".git")+"\n")
		want(t, "naming the common directory itself", naming, "")

		sub := filepath.Join(base, "sub")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		gitIn(t, sub, "init", "-q")
		if err := os.WriteFile(filepath.Join(sub, "g"), []byte("y"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, sub, "add", "g")
		gitIn(t, sub, "commit", "-q", "-m", "s")
		gitIn(t, main, "submodule", "add", "-q", sub, "subm")
		module := adminOf(t, filepath.Join(main, "subm"))
		if !strings.Contains(module, filepath.Join(".git", "modules", "subm")) {
			t.Fatalf("the submodule's data is at %s; want .git/modules/subm", module)
		}
		want(t, "the submodule itself", filepath.Join(main, "subm"), "")
		naming = filepath.Join(base, "naming-submodule")
		plant(t, naming, "gitdir: "+module+"\n")
		want(t, "naming a submodule's modules/<x>", naming, "")
	})

	t.Run("an untrusted repository's administrative directory naming the trusted one", func(t *testing.T) {
		base, main := newMain(t)
		other := filepath.Join(base, "other")
		if err := os.MkdirAll(other, 0o700); err != nil {
			t.Fatal(err)
		}
		gitIn(t, other, "init", "-q")
		gitIn(t, other, "commit", "-q", "--allow-empty", "-m", "o")
		wt := filepath.Join(base, "other-wt")
		admin := addWorktree(t, other, wt)
		// The attacker owns other/.git, so they can write a commondir that names the
		// trusted repository; the entry is still not under main/.git/worktrees.
		if err := os.WriteFile(filepath.Join(admin, "commondir"), []byte(filepath.Join(main, ".git")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// The decision: P must be C/worktrees/<name> for the C its commondir names.
		// Without that parent test a back-linked entry elsewhere passes.
		want(t, "commondir naming another repository", wt, "")
	})

	t.Run("a link, a FIFO, an oversized file, a directory", func(t *testing.T) {
		base, main := newMain(t)
		wt := filepath.Join(base, "wt")
		admin := addWorktree(t, main, wt)

		linked := filepath.Join(base, "linked")
		if err := os.MkdirAll(linked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(wt, ".git"), filepath.Join(linked, ".git")); err != nil {
			t.Fatal(err)
		}
		want(t, ".git a symlink to a genuine worktree's .git file", linked, "")

		fifo := filepath.Join(base, "fifo")
		if err := os.MkdirAll(fifo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(filepath.Join(fifo, ".git"), 0o600); err != nil {
			t.Skip("no FIFO here:", err)
		}
		returns(t, ".git a FIFO", func() { want(t, ".git a FIFO", fifo, "") })

		// A genuine worktree whose .git file is padded past 4 KiB: only the size bound
		// rejects it, so the bound is what this case tests.
		big := filepath.Join(base, "big")
		bigAdmin := addWorktree(t, main, big)
		want(t, "the same worktree before padding", big, main)
		padded := "gitdir: " + bigAdmin + strings.Repeat(" ", 5<<10) + "\n"
		if err := os.WriteFile(filepath.Join(big, ".git"), []byte(padded), 0o600); err != nil {
			t.Fatal(err)
		}
		want(t, "a 5 KiB .git file", big, "")

		want(t, "a directory (the main tree itself)", main, "")

		// A FIFO planted in the administrative directory itself must not hang the
		// check either: the files in it are opened without blocking. Last, because git
		// itself blocks on such a FIFO (it reads every worktree's gitdir).
		if err := os.Rename(filepath.Join(admin, "gitdir"), filepath.Join(admin, "gitdir.orig")); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(filepath.Join(admin, "gitdir"), 0o600); err != nil {
			t.Fatal(err)
		}
		returns(t, "the back-link a FIFO", func() { want(t, "the back-link a FIFO", wt, "") })
	})

	t.Run("a worktree of a bare repository", func(t *testing.T) {
		base, main := newMain(t)
		// The bare repository sits inside the trusted tree, where its parent has a
		// `.git` directory: only the common directory's name (`.git`) tells it from a
		// main tree's, so that is the decision under test. One beside the tree is a
		// second case.
		for _, bare := range []string{filepath.Join(main, "inner.git"), filepath.Join(base, "bare.git")} {
			gitIn(t, base, "clone", "-q", "--bare", main, bare)
			wt := filepath.Join(base, "bare-wt-"+strings.TrimSuffix(filepath.Base(bare), ".git"))
			addWorktree(t, bare, wt)
			if !listed(t, bare, wt) {
				t.Fatal("git does not list the bare repository's worktree")
			}
			want(t, "worktree of the bare repository "+bare, wt, "")
		}
	})

	t.Run("a worktree moved by hand, then repaired", func(t *testing.T) {
		base, main := newMain(t)
		wt := filepath.Join(base, "wt")
		addWorktree(t, main, wt)
		moved := filepath.Join(base, "moved")
		if err := os.Rename(wt, moved); err != nil {
			t.Fatal(err)
		}
		want(t, "moved by hand: the back-link is stale", moved, "")
		gitIn(t, main, "worktree", "repair", moved)
		want(t, "after git worktree repair", moved, main)
		if !listed(t, main, moved) {
			t.Error("git does not list the repaired worktree")
		}
	})

	t.Run("accepted residual risk: a deleted worktree's path made again", func(t *testing.T) {
		base, main := newMain(t)
		wt := filepath.Join(base, "wt")
		admin := addWorktree(t, main, wt)
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		// ADR-0081 §3 accepts this: the administrative entry still names the path, so
		// a directory made there with a .git file naming the entry inherits trust until
		// the entry is pruned. The test pins the behaviour so a change is a decision.
		plant(t, wt, "gitdir: "+admin+"\n")
		want(t, "deleted by hand and made again, entry not pruned", wt, main)

		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		gitIn(t, main, "worktree", "prune")
		plant(t, wt, "gitdir: "+admin+"\n")
		want(t, "the same, after git worktree prune", wt, "")
	})

	t.Run("relative administrative paths", func(t *testing.T) {
		// A relative path resolves against the directory holding the file that names
		// it. Written by hand, this runs on every git; written by git it needs 2.48.
		base, main := newMain(t)
		wt := filepath.Join(base, "wt")
		admin := addWorktree(t, main, wt)
		rel := func(from, to string) string {
			r, err := filepath.Rel(from, to)
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		write := func(path, body string) {
			if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write(filepath.Join(wt, ".git"), "gitdir: "+rel(wt, admin))
		write(filepath.Join(admin, "gitdir"), rel(admin, filepath.Join(wt, ".git")))
		write(filepath.Join(admin, "commondir"), rel(admin, filepath.Join(main, ".git")))
		want(t, "relative paths written by hand", wt, main)

	})

	t.Run("relative administrative paths written by git", func(t *testing.T) {
		out := gitVersion(t)
		if major, minor := out[0], out[1]; major < 2 || (major == 2 && minor < 48) {
			t.Skipf("git %d.%d writes no relative worktree paths (needs 2.48); this row stays Unverified here", major, minor)
		}
		t.Logf("git %d.%d", out[0], out[1])
		base, main := newMain(t)
		wt := filepath.Join(base, "wt")
		addWorktree(t, main, wt, "-c", "worktree.useRelativePaths=true")
		data, err := os.ReadFile(filepath.Join(wt, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(strings.TrimPrefix(string(data), "gitdir: "), "/") {
			t.Fatalf("git wrote an absolute path (%q) despite worktree.useRelativePaths", data)
		}
		want(t, "git-written relative paths", wt, main)
		if !listed(t, main, wt) {
			t.Error("git does not list the relative worktree")
		}
	})
}

// returns fails the test when f does not return promptly: a check that runs before
// every call must never wait on a file a stranger planted.
func returns(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: LinkedWorktree blocked", what)
	}
}

var versionRE = regexp.MustCompile(`(\d+)\.(\d+)`)

func gitVersion(t *testing.T) [2]int {
	t.Helper()
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		t.Skip("no git on PATH")
	}
	m := versionRE.FindStringSubmatch(string(out))
	if m == nil {
		t.Skipf("unparseable git version %q", out)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return [2]int{major, minor}
}
