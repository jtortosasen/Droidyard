package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// worktreeRepo is a git repository with one commit holding a.txt and a Gradle settings
// file (at the top, or only in dev/ for the ADR-0075 layout), under a fresh canonical
// directory; it returns that directory and the main tree.
func worktreeRepo(t *testing.T, settingsInDev bool) (base, main string) {
	t.Helper()
	base = platform.Current().Canonical(t.TempDir())
	main = filepath.Join(base, "M")
	settings := filepath.Join(main, "settings.gradle.kts")
	if settingsInDev {
		settings = filepath.Join(main, "dev", "settings.gradle.kts")
	}
	for _, f := range []string{settings, filepath.Join(main, "a.txt")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, main, "init", "-q", "-b", "main")
	gitIn(t, main, "add", ".")
	gitIn(t, main, "commit", "-q", "-m", "one")
	return base, main
}

// addWT adds a linked worktree of main at path and returns it canonical.
func addWT(t *testing.T, main, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "worktree", "add", "-q", "-b", filepath.Base(path), path)
	return platform.Current().Canonical(path)
}

// claudeAfter is a Claude Code PostToolUse for command run in cwd.
func claudeAfter(t *testing.T, command, cwd string) string {
	t.Helper()
	return cursorFixture(t, "claude-postToolUse", map[string]any{"cwd": cwd, "tool_input": map[string]any{"command": command}})
}

// TestHookRecordsInTheWorktree holds ADR-0085 §1 and §2 end to end on a real daemon: a
// command is recorded under the root its directory gives, not always --root. Each row
// says which workspace the hook opened (the record is made on the one it opened) and
// which it did not, or that it opened none. A hook that opened --root whatever the
// directory, that applied ADR-0019's settings-file rule from the directory, that tested
// "under --root" before looking for a worktree, that compared paths lexically, or that
// recorded the unrelated project, fails a row.
func TestHookRecordsInTheWorktree(t *testing.T) {
	t.Parallel()
	const cmd = "./gradlew assembleDebug"
	type row struct {
		name string
		// setup returns --root, the command's directory, the workspace that must be open
		// afterwards and one that must not be ("" for none).
		setup func(t *testing.T) (flagRoot, cwd, open, closed string)
	}
	rows := []row{
		{"a linked worktree outside the project", func(t *testing.T) (string, string, string, string) {
			base, main := worktreeRepo(t, false)
			wt := addWT(t, main, filepath.Join(base, "wt"))
			return main, wt, wt, main
		}},
		{"a subfolder of the worktree", func(t *testing.T) (string, string, string, string) {
			base, main := worktreeRepo(t, false)
			wt := addWT(t, main, filepath.Join(base, "wt"))
			return main, filepath.Join(wt, "dev"), wt, main
		}},
		{"a worktree nested in the project, in a subfolder", func(t *testing.T) (string, string, string, string) {
			_, main := worktreeRepo(t, false)
			x := addWT(t, main, filepath.Join(main, ".claude", "worktrees", "x"))
			if err := os.MkdirAll(filepath.Join(x, "app"), 0o700); err != nil {
				t.Fatal(err)
			}
			return main, filepath.Join(x, "app"), x, main
		}},
		{"the project's dev/ where only dev/ holds the settings file", func(t *testing.T) (string, string, string, string) {
			_, main := worktreeRepo(t, true)
			return main, filepath.Join(main, "dev"), main, filepath.Join(main, "dev")
		}},
		{"--root through a symlink, a physical directory", func(t *testing.T) (string, string, string, string) {
			base, main := worktreeRepo(t, false)
			link := filepath.Join(base, "link")
			if err := os.Symlink(main, link); err != nil {
				t.Fatal(err)
			}
			return link, filepath.Join(main, "app"), main, ""
		}},
		{"a submodule of a worktree nested in the project", func(t *testing.T) (string, string, string, string) {
			_, main := worktreeRepo(t, false)
			x := addWT(t, main, filepath.Join(main, ".claude", "worktrees", "x"))
			sub := filepath.Join(x, "sub")
			if err := os.MkdirAll(sub, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+filepath.Join(main, ".git", "modules", "sub")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			// The walk stops at the submodule's .git, which is no worktree; the directory
			// lies under --root, so the record goes to --root (pinned as behaviour; a
			// register row at step 10).
			return main, sub, main, x
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			p := startInProcess(t)
			flagRoot, cwd, open, closed := r.setup(t)
			sub := p.bus.Subscribe([]string{capability.StateTopic}, nil)
			defer sub.Close()
			res := runHook(t, p.state, claudeAfter(t, cmd, cwd), nil, "--client", "claude", "--root", flagRoot)
			if res.code != 0 || res.stderr != "" {
				t.Fatalf("hook exited %d with stderr %q; want 0 and none", res.code, res.stderr)
			}
			if hint := hintOf(t, "claude", "PostToolUse", res.stdout); !strings.Contains(hint, "`build_assemble`") {
				t.Errorf("the hint is %q; want it to name build_assemble", hint)
			}
			if env, code := p.call(t, "workspace.describe", "--workspace", open); code != 0 || env.Error != nil {
				t.Errorf("workspace.describe %s exited %d with %+v; want the workspace the command ran in open", open, code, env.Error)
			}
			if closed != "" {
				if env, code := p.call(t, "workspace.describe", "--workspace", closed); code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceNotOpen {
					t.Errorf("workspace.describe %s exited %d with %+v; want it not open (the record is not under it)", closed, code, env.Error)
				}
			}
			if got := recordsOf(t, p, sub, 1); len(got) != 1 {
				t.Errorf("the daemon holds records %v; want exactly one", got)
			}
		})
	}

	t.Run("an unrelated project", func(t *testing.T) {
		t.Parallel()
		p := startInProcess(t)
		_, main := worktreeRepo(t, false)
		_, other := worktreeRepo(t, false)
		sub := p.bus.Subscribe([]string{capability.StateTopic}, nil)
		defer sub.Close()
		before, _ := os.ReadFile(filepath.Join(p.state, "workspaces.json"))
		res := runHook(t, p.state, claudeAfter(t, cmd, other), nil, "--client", "claude", "--root", main)
		if res.code != 0 {
			t.Fatalf("hook exited %d; want 0", res.code)
		}
		if hint := hintOf(t, "claude", "PostToolUse", res.stdout); !strings.Contains(hint, "`build_assemble`") {
			t.Errorf("the hint is %q; the hint still goes for a command that is not recorded", hint)
		}
		if got := recordsOf(t, p, sub, 0); len(got) != 0 {
			t.Errorf("the daemon holds records %v; want none for a command in another project", got)
		}
		after, _ := os.ReadFile(filepath.Join(p.state, "workspaces.json"))
		if string(before) != string(after) {
			t.Errorf("workspaces.json changed: %q -> %q; the hook may open nothing for another project", before, after)
		}
		for _, root := range []string{main, other} {
			if _, code := p.call(t, "workspace.describe", "--workspace", root); code == 0 {
				t.Errorf("%s is open; want no workspace opened", root)
			}
		}
	})

	t.Run("a Cursor event without a directory", func(t *testing.T) {
		t.Parallel()
		p := startInProcess(t)
		_, main := worktreeRepo(t, false)
		sub := p.bus.Subscribe([]string{capability.StateTopic}, nil)
		defer sub.Close()
		res := runHook(t, p.state, afterEvent(t, "cursor", "postToolUse", cmd), nil, "--client", "cursor", "--root", main)
		if res.code != 0 {
			t.Fatalf("hook exited %d; want 0", res.code)
		}
		if env, code := p.call(t, "workspace.describe", "--workspace", main); code != 0 || env.Error != nil {
			t.Errorf("workspace.describe exited %d with %+v; want --root open, as before", code, env.Error)
		}
		if got := recordsOf(t, p, sub, 1); len(got) != 1 {
			t.Errorf("the daemon holds records %v; want exactly one", got)
		}
	})
}

// TestHookRmInTheWorktree holds ADR-0085 §3: the rm route resolves against the root the
// command's directory gives, so `rm a.txt` in a worktree outside --root's project is
// redirected to `droidyard trash` on that worktree. Under --root's rules the target
// lies outside and the command passes.
func TestHookRmInTheWorktree(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	base, main := worktreeRepo(t, false)
	wt := addWT(t, main, filepath.Join(base, "wt"))
	res := runHook(t, p.state, claudeBash("rm a.txt", wt), nil, "--client", "claude", "--root", main)
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("hook exited %d with stderr %q; want 0 and none", res.code, res.stderr)
	}
	replacement := replacementOf(t, reasonOf(t, "claude", res.stdout))
	if !strings.Contains(replacement, " --root "+quoteArg(wt)+" ") {
		t.Errorf("the replacement is %q; want droidyard trash with --root %s", replacement, wt)
	}
	if !exists(filepath.Join(wt, "a.txt")) {
		t.Error("the hook deleted a.txt; it must delete nothing")
	}
}
