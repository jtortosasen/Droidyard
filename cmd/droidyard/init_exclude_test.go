package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The repository read of init --exclude and --gitignore moved from the Git domain into
// internal/assistant when the domain left (ADR-0137 §1); these two hold that the flags
// still work end to end, through the real binary.

// TestInitExcludeStillWorks: --exclude in a repository puts the block in the common
// directory's info/exclude and git status lists none of init's files; --gitignore puts it
// in the root's .gitignore. The untracked control first: without a flag the files show, so
// the clean status afterwards is the flag's doing. Reversed (the read failing, or its
// result dropped) the block would be missing and the status would list the files.
func TestInitExcludeStillWorks(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	repo := repoWithProject(t, u)

	if out, errOut, code := u.init(t, repo, "--client", "claude", "--root", repo); code != 0 {
		t.Fatalf("init exited %d:\n%s\n%s", code, out, errOut)
	}
	if st := userGit(t, u, repo, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(st, ".mcp.json") {
		t.Fatalf("init's files do not show in git status without a flag:\n%s", st)
	}
	if out, _, code := u.init(t, repo, "--client", "claude", "--root", repo, "--uninstall"); code != 0 {
		t.Fatalf("uninstall exited %d:\n%s", code, out)
	}

	out, errOut, code := u.init(t, repo, "--client", "claude", "--root", repo, "--exclude")
	if code != 0 {
		t.Fatalf("init --exclude exited %d:\n%s\n%s", code, out, errOut)
	}
	if st := userGit(t, u, repo, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("git status lists init's files after --exclude:\n%s", st)
	}
	if exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); !strings.Contains(string(exclude), "# droidyard init:") {
		t.Errorf("info/exclude holds no block:\n%s", exclude)
	}

	if out, _, code := u.init(t, repo, "--client", "claude", "--root", repo, "--uninstall"); code != 0 {
		t.Fatalf("uninstall exited %d:\n%s", code, out)
	}
	if out, errOut, code := u.init(t, repo, "--client", "claude", "--root", repo, "--gitignore"); code != 0 {
		t.Fatalf("init --gitignore exited %d:\n%s\n%s", code, out, errOut)
	}
	if ignore, _ := os.ReadFile(filepath.Join(repo, ".gitignore")); !strings.Contains(string(ignore), "\n/.mcp.json\n") {
		t.Errorf(".gitignore holds no line for .mcp.json:\n%s", ignore)
	}
}

// TestInitExcludeIgnoresFsmonitor: a repository whose config names a core.fsmonitor script
// has it run by no git that init --exclude starts (ADR-0015 §5: the repository is
// untrusted; the read passes core.fsmonitor=false). The control is plain git, which runs the
// same script, so a missing marker proves the guard and not a script that never fires.
// Seen failing with the guard taken out of the read's argv (internal/assistant/tracked.go).
func TestInitExcludeIgnoresFsmonitor(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	repo := repoWithProject(t, u)
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	script := filepath.Join(t.TempDir(), "fsmonitor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	userGit(t, u, repo, "config", "core.fsmonitor", script)

	out, errOut, code := u.init(t, repo, "--client", "claude", "--root", repo, "--exclude")
	if code != 0 {
		t.Fatalf("init --exclude exited %d:\n%s\n%s", code, out, errOut)
	}
	if exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); !strings.Contains(string(exclude), "# droidyard init:") {
		t.Errorf("the block was not written, so the read did not complete:\n%s", exclude)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("init --exclude ran the repository's fsmonitor script")
	}
	userGit(t, u, repo, "status", "--porcelain")
	if _, err := os.Stat(marker); err != nil {
		t.Error("control: plain git did not run the fsmonitor script; the test cannot fail")
	}
}
