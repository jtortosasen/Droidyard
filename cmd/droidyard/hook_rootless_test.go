package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// The rootless hook (ADR-0124 §Decision 3): a user-level entry runs `droidyard hook
// --client <c>` with no --root, and the hook takes the project from the directory the
// client names as its own. These tests run the hook as a client would, in a process of its
// own with the client's environment, against a fake daemon that counts the dials (no dial
// is the claim) or a real in-process one (where a record landed is the claim).

const rootlessCmd = "./gradlew assembleDebug"

// rootlessEnv is the environment a client gives the hook.
func rootlessEnv(kv ...string) []string { return kv }

// cursorEvent is a Cursor event of the fixture, with the directories the client reports
// replaced: the fixtures carry the workspace_roots of the machine they were recorded on.
func cursorEvent(t *testing.T, fixture string, roots []string, set map[string]any) string {
	t.Helper()
	m := map[string]any{"workspace_roots": roots, "cwd": ""}
	for k, v := range set {
		m[k] = v
	}
	return cursorFixture(t, fixture, m)
}

func cursorAfterEvent(t *testing.T, command string, roots []string) string {
	t.Helper()
	return cursorEvent(t, "cursor-4-postToolUse", roots, map[string]any{"tool_input": map[string]any{"command": command, "cwd": ""}})
}

// wantHint fails unless the hook answered an after-command event with the hint, as every
// after-command event in a project does.
func wantHint(t *testing.T, client string, res hookResult) {
	t.Helper()
	event := map[string]string{"claude": "PostToolUse", "cursor": "postToolUse"}[client]
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("hook exited %d with stderr %q; want 0 and none", res.code, res.stderr)
	}
	if hint := hintOf(t, client, event, res.stdout); !strings.Contains(hint, "`build_assemble`") {
		t.Errorf("the hint is %q; want it to name build_assemble", hint)
	}
}

// wantPass fails unless the hook answered with the client's pass-through: outside a
// project the session has no tools, so no hint names one (ADR-0124 §2, §3).
func wantPass(t *testing.T, client string, res hookResult) {
	t.Helper()
	if res.code != 0 || res.stderr != "" || res.stdout != passOf(client) {
		t.Errorf("the hook exited %d and printed %q (stderr %q); want 0 and %q", res.code, res.stdout, res.stderr, passOf(client))
	}
}

// opened says whether the daemon holds a workspace for root.
func opened(t *testing.T, p *inProcess, root string) bool {
	t.Helper()
	env, code := p.call(t, "workspace.describe", "--workspace", root)
	return code == 0 && env.Error == nil
}

// TestRootlessHookFollowsTheClientsProjectNotCwd holds the central decision of ADR-0124
// §Decision 3: the root is the project of CLAUDE_PROJECT_DIR, and the event's cwd only
// places the command within that project and its worktrees (ADR-0085 §1). An agent that
// `cd`s into another Android project, or a client started in a folder that is no project,
// gets no record and no dial. A hook that took the root from cwd would open B here.
func TestRootlessHookFollowsTheClientsProjectNotCwd(t *testing.T) {
	t.Parallel()
	a := hookProject(t)
	b := hookProject(t)
	notProject := platform.Current().Canonical(t.TempDir())

	for _, c := range []struct {
		name, projectDir, cwd string
		hint                  bool
	}{
		{"the project is A and cwd is in the unrelated project B", a, b, true},
		{"the project dir is a folder that is no project and cwd is in B", notProject, b, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			state := shortDir(t)
			fake := startFakeDaemon(t, state, true)
			res := runHook(t, state, claudeAfter(t, rootlessCmd, c.cwd), rootlessEnv("CLAUDE_PROJECT_DIR="+c.projectDir), "--client", "claude")
			if c.hint {
				wantHint(t, "claude", res)
			} else {
				wantPass(t, "claude", res)
			}
			if n := fake.dials.Load(); n != 0 {
				t.Errorf("the hook dialled the daemon %d times; want 0: a command in %s is not the project's", n, c.cwd)
			}
			if locks := findLocks(t, state); len(locks) != 0 {
				t.Errorf("the hook started a daemon: %v", locks)
			}
		})
	}

	t.Run("cwd in a subfolder of A records in A", func(t *testing.T) {
		t.Parallel()
		p := startInProcess(t)
		sub := p.bus.Subscribe([]string{capability.StateTopic}, nil)
		defer sub.Close()
		if err := os.MkdirAll(filepath.Join(a, "app", "src"), 0o700); err != nil {
			t.Fatal(err)
		}
		res := runHook(t, p.state, claudeAfter(t, rootlessCmd, filepath.Join(a, "app", "src")), rootlessEnv("CLAUDE_PROJECT_DIR="+a), "--client", "claude")
		wantHint(t, "claude", res)
		if !opened(t, p, a) {
			t.Errorf("%s is not open; want the command recorded in the client's project", a)
		}
		if opened(t, p, b) {
			t.Errorf("%s is open; want only the client's project", b)
		}
		if got := recordsOf(t, p, sub, 1); len(got) != 1 {
			t.Errorf("the daemon holds records %v; want exactly one", got)
		}
	})

	t.Run("without CLAUDE_PROJECT_DIR the event's cwd is the client's directory", func(t *testing.T) {
		t.Parallel()
		p := startInProcess(t)
		sub := p.bus.Subscribe([]string{capability.StateTopic}, nil)
		defer sub.Close()
		res := runHook(t, p.state, claudeAfter(t, rootlessCmd, a), nil, "--client", "claude")
		wantHint(t, "claude", res)
		if !opened(t, p, a) {
			t.Errorf("%s is not open; want the project of the event's cwd when nothing else is named", a)
		}
		if got := recordsOf(t, p, sub, 1); len(got) != 1 {
			t.Errorf("the daemon holds records %v; want exactly one", got)
		}
	})
}

// TestRootlessHookOutsideAProjectDialsNothing holds ADR-0124 §Decision 3: with no project,
// every event kind passes at once with nothing dialled: an rm before a command, the edit
// tools before and after, and, after a command, the hint (it needs no daemon) without a
// record. A control run with --root on a project dials for the same events, so a count
// that never moved would show. A hook that took the root as `call` does (resolveRoot's
// fallback to the directory itself) would route the rm and dial here.
func TestRootlessHookOutsideAProjectDialsNothing(t *testing.T) {
	t.Parallel()
	dir := platform.Current().Canonical(t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dir", "b.kt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "dir", "b.kt")

	type ev struct {
		name, client, event string
		hint                bool
	}
	cl := func(name, event string, hint bool) ev { return ev{"claude: " + name, "claude", event, hint} }
	cu := func(name, event string, hint bool) ev { return ev{"cursor: " + name, "cursor", event, hint} }
	roots := []string{dir}
	events := []ev{
		cl("a build command after", claudeAfter(t, rootlessCmd, dir), false),
		cl("an rm before", claudeBash("rm -r dir", dir), false),
		cl("an Edit before", editEvent(t, "claude-edit-4-pre-Edit", file, "chat-1", dir), false),
		cl("a Write after", editEvent(t, "claude-edit-2-post-Write-create", file, "chat-1", dir), false),
		cu("a build command after", cursorAfterEvent(t, rootlessCmd, roots), false),
		cu("an rm before", cursorEvent(t, "cursor-3-beforeShellExecution", roots, map[string]any{"command": "rm -r " + filepath.Join(dir, "dir")}), false),
		cu("the Delete tool before", cursorEvent(t, "cursor-2-preToolUse", roots, map[string]any{"tool_input": map[string]any{"file_path": file}}), false),
		cu("a Write before", cursorEvent(t, "cursor-edit-2-pre-Write", roots, map[string]any{"tool_input": map[string]any{"file_path": file, "content": "x"}}), false),
		cu("a Write after", cursorEvent(t, "cursor-edit-3-post-Write", roots, map[string]any{"tool_input": map[string]any{"file_path": file, "content": "x"}}), false),
	}

	t.Run("outside a project", func(t *testing.T) {
		t.Parallel()
		state := shortDir(t)
		fake := startFakeDaemon(t, state, true)
		for _, e := range events {
			var env []string
			if e.client == "claude" {
				env = rootlessEnv("CLAUDE_PROJECT_DIR=" + dir)
			}
			res := runHook(t, state, e.event, env, "--client", e.client)
			if e.hint {
				wantHint(t, e.client, res)
			} else if res.code != 0 || res.stderr != "" || res.stdout != passOf(e.client) {
				t.Errorf("%s: the hook exited %d and printed %q (stderr %q); want 0 and %q", e.name, res.code, res.stdout, res.stderr, passOf(e.client))
			}
		}
		if n := fake.dials.Load(); n != 0 {
			t.Errorf("the hook dialled the daemon %d times outside a project; want 0", n)
		}
		if locks := findLocks(t, state); len(locks) != 0 {
			t.Errorf("the hook started a daemon: %v", locks)
		}
	})

	// The control: the same folder named as --root is a project to the old rules, and the
	// same events dial, so the zero above is the rootless rule and not events that never dial.
	t.Run("the same events with --root dial", func(t *testing.T) {
		t.Parallel()
		for _, e := range events {
			if e.name != "claude: a build command after" && e.name != "claude: an rm before" && e.name != "claude: an Edit before" {
				continue
			}
			state := shortDir(t)
			fake := startFakeDaemon(t, state, true)
			runHook(t, state, e.event, nil, "--client", e.client, "--root", dir)
			if n := fake.dials.Load(); n == 0 {
				t.Errorf("%s: the control with --root dialled 0 times; the test above would prove nothing", e.name)
			}
		}
	})
}

// TestRootlessHookRecordsInTheWorktree holds ADR-0085 §1 with the root the client names: a
// command run in a linked worktree of the client's project is recorded in the worktree's
// workspace, whether the client's directory is the main tree or the worktree itself (a
// chat started in a worktree: CLAUDE_PROJECT_DIR is that worktree).
func TestRootlessHookRecordsInTheWorktree(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		projectDir func(main, wt string) string
	}{
		{"the project dir is the main tree", func(main, _ string) string { return main }},
		{"the project dir is the worktree", func(_, wt string) string { return wt }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := startInProcess(t)
			base, main := worktreeRepo(t, false)
			wt := addWT(t, main, filepath.Join(base, "wt"))
			sub := p.bus.Subscribe([]string{capability.StateTopic}, nil)
			defer sub.Close()
			res := runHook(t, p.state, claudeAfter(t, rootlessCmd, wt), rootlessEnv("CLAUDE_PROJECT_DIR="+c.projectDir(main, wt)), "--client", "claude")
			wantHint(t, "claude", res)
			if !opened(t, p, wt) {
				t.Errorf("%s is not open; want the record in the worktree the command ran in", wt)
			}
			if opened(t, p, main) {
				t.Errorf("%s is open; want the record only in the worktree", main)
			}
			if got := recordsOf(t, p, sub, 1); len(got) != 1 {
				t.Errorf("the daemon holds records %v; want exactly one", got)
			}
		})
	}
}

// TestRootlessCursorHookReadsWorkspaceRoots holds the Cursor sources of ADR-0124 §Decision 3:
// the first workspace_roots entry, else CURSOR_PROJECT_DIR, else cwd (Cursor's is empty in
// every event observed). Cursor's hook environment cannot be relied on to carry a variable
// (cursor-agent passed none), so the event's workspace_roots is the first source; a hook
// that ignored it would find nothing here.
func TestRootlessCursorHookReadsWorkspaceRoots(t *testing.T) {
	t.Parallel()
	a := hookProject(t)
	b := hookProject(t)

	type row struct {
		name  string
		roots []string
		env   []string
		open  string // the project that must hold the record
		shut  string // one that must not be opened
	}
	rows := []row{
		{"workspace_roots alone", []string{a}, nil, a, b},
		{"the first of several roots", []string{a, b}, nil, a, b},
		{"workspace_roots beats CURSOR_PROJECT_DIR", []string{a}, rootlessEnv("CURSOR_PROJECT_DIR=" + b), a, b},
		{"CURSOR_PROJECT_DIR alone", nil, rootlessEnv("CURSOR_PROJECT_DIR=" + a), a, b},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			p := startInProcess(t)
			sub := p.bus.Subscribe([]string{capability.StateTopic}, nil)
			defer sub.Close()
			res := runHook(t, p.state, cursorAfterEvent(t, rootlessCmd, r.roots), r.env, "--client", "cursor")
			wantHint(t, "cursor", res)
			if !opened(t, p, r.open) {
				t.Errorf("%s is not open; want the command recorded there", r.open)
			}
			if opened(t, p, r.shut) {
				t.Errorf("%s is open; want only the first source's project", r.shut)
			}
			if got := recordsOf(t, p, sub, 1); len(got) != 1 {
				t.Errorf("the daemon holds records %v; want exactly one", got)
			}
		})
	}

	t.Run("neither source: nothing is dialled", func(t *testing.T) {
		t.Parallel()
		state := shortDir(t)
		fake := startFakeDaemon(t, state, true)
		wantPass(t, "cursor", runHook(t, state, cursorAfterEvent(t, rootlessCmd, nil), nil, "--client", "cursor"))
		if n := fake.dials.Load(); n != 0 {
			t.Errorf("the hook dialled the daemon %d times with no workspace root; want 0", n)
		}
	})
}

// TestRootlessHookYieldsToTheProjectsOwn holds ADR-0124 §Decision 3: where the client's
// directory holds init's own per-project hook (any root, any binary), the client runs that
// one as well, so the user-level hook passes and a project set up both ways records once.
// The controls are the same events without the entry, with an entry that is a user's
// wrapper, and with a user-level (rootless) entry: each dials, so the pass is the check.
// Seen failing without the check: one record from the user-level hook as well.
func TestRootlessHookYieldsToTheProjectsOwn(t *testing.T) {
	t.Parallel()
	entry := func(c assistant.Client) string {
		return assistant.HookCommand(assistant.Options{Command: "/opt/elsewhere/droidyard", Root: "/some/other/root"}, c)
	}
	settings := func(command string) string {
		return `{"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":` + quoteJSON(command) + `}]}],` +
			`"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":` + quoteJSON(command) + `}]}]}}`
	}
	cursorHooks := func(command string) string {
		return `{"version":1,"hooks":{"postToolUse":[{"command":` + quoteJSON(command) + `,"matcher":"Shell"}],` +
			`"beforeShellExecution":[{"command":` + quoteJSON(command) + `}]}}`
	}
	rows := []struct {
		name, client, file, content string
		yields                      bool
	}{
		{"claude: init's entry", "claude", ".claude/settings.json", settings(entry(assistant.Claude)), true},
		{"claude: no entry", "claude", "", "", false},
		{"claude: a user's wrapper", "claude", ".claude/settings.json", settings("sh -c '" + entry(assistant.Claude) + "'"), false},
		{"claude: a user-level entry", "claude", ".claude/settings.json", settings(assistant.HookCommand(assistant.Options{Command: "/opt/droidyard"}, assistant.Claude)), false},
		{"cursor: init's entry", "cursor", ".cursor/hooks.json", cursorHooks(entry(assistant.Cursor)), true},
		{"cursor: no entry", "cursor", "", "", false},
		{"cursor: a user's wrapper", "cursor", ".cursor/hooks.json", cursorHooks("env X=1 " + entry(assistant.Cursor)), false},
	}
	for _, r := range rows {
		for _, kind := range []string{"after", "rm"} {
			t.Run(r.name+" "+kind, func(t *testing.T) {
				t.Parallel()
				project := hookProject(t)
				if r.file != "" {
					p := filepath.Join(project, filepath.FromSlash(r.file))
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(r.content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var event string
				var env []string
				switch {
				case r.client == "claude" && kind == "after":
					event, env = claudeAfter(t, rootlessCmd, project), rootlessEnv("CLAUDE_PROJECT_DIR="+project)
				case r.client == "claude":
					event, env = claudeBash("rm -r dir", project), rootlessEnv("CLAUDE_PROJECT_DIR="+project)
				case kind == "after":
					event = cursorAfterEvent(t, rootlessCmd, []string{project})
				default:
					event = cursorEvent(t, "cursor-3-beforeShellExecution", []string{project}, map[string]any{"command": "rm -r " + filepath.Join(project, "dir")})
				}
				state := shortDir(t)
				fake := startFakeDaemon(t, state, true)
				res := runHook(t, state, event, env, "--client", r.client)
				if res.code != 0 || res.stderr != "" {
					t.Fatalf("hook exited %d with stderr %q; want 0 and none", res.code, res.stderr)
				}
				if r.yields {
					if res.stdout != passOf(r.client) {
						t.Errorf("the hook printed %q; want the pass-through, the project's own hook answers", res.stdout)
					}
					if n := fake.dials.Load(); n != 0 {
						t.Errorf("the hook dialled the daemon %d times; want 0 where the project's own hook runs", n)
					}
					return
				}
				if n := fake.dials.Load(); n == 0 {
					t.Errorf("the hook dialled 0 times without a hook of init's in the project; want it to act")
				}
				if kind == "after" {
					wantHint(t, r.client, res)
				}
			})
		}
	}
}

// --root, when given, is still an absolute path: an empty or relative value is a usage
// error and not the rootless hook, which is for a command line that does not name one.
func TestHookRootWhenGivenIsStillAnAbsolutePath(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"", "relative/dir", "."} {
		res := runHook(t, shortDir(t), claudeAfter(t, rootlessCmd, "/"), nil, "--client", "claude", "--root", root)
		if res.code != 1 || res.stdout != "" || !strings.Contains(res.stderr, "--root needs an absolute project root") {
			t.Errorf("--root %q: exit %d, stdout %q, stderr %q; want a usage error", root, res.code, res.stdout, res.stderr)
		}
	}
	res := runHook(t, shortDir(t), claudeAfter(t, rootlessCmd, "/"), nil, "--client", "claude", "stray")
	if res.code != 1 || !strings.Contains(res.stderr, "unexpected argument") {
		t.Errorf("a stray argument: exit %d, stderr %q; want a usage error", res.code, res.stderr)
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
