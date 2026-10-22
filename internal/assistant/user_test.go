package assistant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The run at user level (ADR-0124). Every test works in a temporary directory that stands
// for the home: HOME, CLAUDE_CONFIG_DIR and XDG_CONFIG_HOME point into it, every directory
// the run is given lies in it, and the Claude CLI is a fake. Nothing reads or writes the
// real ~/.claude, ~/.cursor, ~/.config/opencode or ~/.agents.

const (
	userClaudeHook = testCommand + " hook --client claude"
	userCursorHook = testCommand + " hook --client cursor"
	wantClaudeMCP  = `{"type":"stdio","command":"/opt/bin/droidyard","args":["mcp"]}`
)

// userEnv is a home directory for one test.
type userEnv struct {
	base string
	dirs UserDirs
}

func newUserEnv(t *testing.T) userEnv {
	t.Helper()
	base := projectRoot(t)
	t.Setenv("HOME", base)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, ".claude"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, ".config"))
	return userEnv{base: base, dirs: UserDirs{
		Claude:     filepath.Join(base, ".claude"),
		ClaudeJSON: filepath.Join(base, ".claude.json"),
		Cursor:     filepath.Join(base, ".cursor"),
		OpenCode:   filepath.Join(base, ".config", "opencode"),
		Agents:     filepath.Join(base, ".agents"),
	}}
}

func (e userEnv) opts(r ClaudeMCP, clients ...Client) Options {
	o := opts("", clients...)
	o.User, o.Dirs, o.ClaudeMCP = true, e.dirs, r
	return o
}

// fakeClaude records the calls and, like `claude mcp add-json --scope user`, writes the
// entry into .claude.json beside the keys that are already there (plan m3a P1: remove
// leaves `"mcpServers": {}`).
type fakeClaude struct {
	t       *testing.T
	file    string
	calls   []string
	addErr  error
	rmError error
}

func (f *fakeClaude) Add(entry json.RawMessage) error {
	f.calls = append(f.calls, "add "+string(entry))
	if f.addErr != nil {
		return f.addErr
	}
	f.edit(func(servers *object) { servers.set(ServerName, entry) })
	return nil
}

func (f *fakeClaude) Remove() error {
	f.calls = append(f.calls, "remove")
	if f.rmError != nil {
		return f.rmError
	}
	f.edit(func(servers *object) { servers.del(ServerName) })
	return nil
}

func (f *fakeClaude) edit(fn func(*object)) {
	f.t.Helper()
	root := newObject()
	if b, err := os.ReadFile(f.file); err == nil {
		var perr error
		if root, perr = parseObject(b); perr != nil {
			f.t.Fatal(perr)
		}
	}
	if string(root.vals["mcpServers"]) == "null" {
		root.del("mcpServers")
	}
	if _, err := root.editPath([]string{"mcpServers"}, true, false, func(servers *object) (bool, error) {
		fn(servers)
		return true, nil
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.file, root.file(), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (e userEnv) claude(t *testing.T) *fakeClaude { return &fakeClaude{t: t, file: e.dirs.ClaudeJSON} }

func sameJSON(t *testing.T, name, got, want string) {
	t.Helper()
	if !equalJSON(json.RawMessage(got), json.RawMessage(want)) {
		t.Errorf("%s:\n%s\nwant (after parsing):\n%s", name, got, want)
	}
}

func fileSet(snap map[string]string) []string {
	var out []string
	for k := range snap {
		if !strings.HasSuffix(k, "/") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ADR-0124 Decision 1, the table: every file and key each client gets at user level,
// written out in full. No entry names a root, Cursor's server carries --from
// ${workspaceFolder}, every command is the binary's absolute path, and nothing is written
// outside the directories the run was given (the project is not involved at all).
func TestUserRunWritesEachClientsFiles(t *testing.T) {
	e := newUserEnv(t)
	fake := e.claude(t)
	res := mustRun(t, e.opts(fake, Cursor, Claude, OpenCode))
	if res.Refused() {
		t.Fatalf("refused: %+v", res.Changes)
	}

	claudeHooks := func(cmd string) string {
		bash := `{"matcher":"Bash","hooks":[{"type":"command","command":"` + cmd + `"}]}`
		edit := `{"matcher":"Write|Edit|NotebookEdit","hooks":[{"type":"command","command":"` + cmd + `"}]}`
		return `{"PreToolUse":[` + bash + `,` + edit + `],"PostToolUse":[` + bash + `,` + edit + `],"PostToolUseFailure":[` + bash + `]}`
	}
	cursorEntry := func(matcher string) string {
		if matcher == "" {
			return `{"command":"` + userCursorHook + `"}`
		}
		return `{"command":"` + userCursorHook + `","matcher":"` + matcher + `"}`
	}
	files := map[string]string{
		".claude/settings.json": `{"permissions":{"allow":["mcp__droidyard__device_list","mcp__droidyard__device_uiTree"]},"hooks":` + claudeHooks(userClaudeHook) + `}`,
		".cursor/mcp.json":      `{"mcpServers":{"droidyard":{"command":"/opt/bin/droidyard","args":["mcp","--from","${workspaceFolder}"]}}}`,
		".cursor/hooks.json": `{"version":1,"hooks":{"beforeShellExecution":[` + cursorEntry("") + `],` +
			`"preToolUse":[` + cursorEntry("Delete") + `,` + cursorEntry("Write") + `],` +
			`"postToolUse":[` + cursorEntry("Shell") + `,` + cursorEntry("Write") + `],` +
			`"postToolUseFailure":[` + cursorEntry("Shell") + `]}}`,
		// No deny array: step 1 saw no need of one in cli-config.json.
		".cursor/cli-config.json":        `{"permissions":{"allow":["Mcp(droidyard:device_list)","Mcp(droidyard:device_uiTree)"]}}`,
		".config/opencode/opencode.json": `{"mcp":{"servers":{"droidyard":{"type":"local","command":["/opt/bin/droidyard","mcp"],"codemode":false}}}}`,
		".claude.json":                   `{"mcpServers":{"droidyard":` + wantClaudeMCP + `}}`,
	}
	for rel, want := range files {
		sameJSON(t, rel, read(t, e.base, rel), want)
	}
	// The skill: Claude's own, and the one Cursor and OpenCode share.
	skill := "---\nname: droidyard-android\n---\nbody\n"
	for _, dir := range []string{".claude/skills/droidyard-android/", ".agents/skills/droidyard-android/"} {
		if got := read(t, e.base, dir+"SKILL.md"); got != skill {
			t.Errorf("%sSKILL.md = %q", dir, got)
		}
		if got := read(t, e.base, dir+"references/tools.md"); got != "| tool |\n" {
			t.Errorf("%sreferences/tools.md = %q", dir, got)
		}
	}

	// Claude's entry went through the runner, once, as the entry P1 observed.
	if want := []string{"add " + wantClaudeMCP}; !reflect.DeepEqual(fake.calls, want) {
		t.Errorf("claude calls = %q; want %q", fake.calls, want)
	}
	// The decision: no entry anywhere names a root, and every command starts at "/".
	snap := snapshot(t, e.base)
	for rel, text := range snap {
		if strings.Contains(text, "--root") {
			t.Errorf("%s names a root:\n%s", rel, text)
		}
	}
	if res.Hooks[Claude] != userClaudeHook || res.Hooks[Cursor] != userCursorHook || len(res.Hooks) != 2 {
		t.Errorf("Hooks = %v", res.Hooks)
	}
	if len(res.Allowed[Claude]) != 2 || len(res.Allowed[Cursor]) != 2 {
		t.Errorf("Allowed = %v", res.Allowed)
	}
	// Nothing outside the home, and every reported path is absolute and inside it.
	want := []string{
		".agents/skills/droidyard-android/SKILL.md", ".agents/skills/droidyard-android/references/tools.md",
		".claude.json", ".claude/settings.json",
		".claude/skills/droidyard-android/SKILL.md", ".claude/skills/droidyard-android/references/tools.md",
		".config/opencode/opencode.json",
		".cursor/cli-config.json", ".cursor/hooks.json", ".cursor/mcp.json",
	}
	if got := fileSet(snap); !reflect.DeepEqual(got, want) {
		t.Errorf("files written = %v\nwant %v", got, want)
	}
	for _, c := range res.Changes {
		if !strings.HasPrefix(c.Path, e.base+"/") {
			t.Errorf("change path %q is not an absolute path inside the home", c.Path)
		}
	}
	// cursor-agent may rewrite cli-config.json: init says to close it first.
	cli := statusOf(t, res, filepath.ToSlash(filepath.Join(e.dirs.Cursor, "cli-config.json")))
	if !strings.Contains(notesOf(cli), "close cursor-agent first") {
		t.Errorf("cli-config.json notes = %q; want the cursor-agent warning", notesOf(cli))
	}
}

// A user-level run must be a no-op the second time, including Claude's entry: it is
// present and equal, so the runner is not called again (no `Add` on a rerun).
func TestUserRunIsIdempotent(t *testing.T) {
	e := newUserEnv(t)
	fake := e.claude(t)
	o := e.opts(fake, Cursor, Claude, OpenCode)
	mustRun(t, o)
	first := snapshot(t, e.base)

	res := mustRun(t, o)
	for _, c := range res.Changes {
		if c.Status != Unchanged {
			t.Errorf("second run: %s is %s (%s); want unchanged", c.Path, c.Status, notesOf(c))
		}
	}
	sameText(t, first, snapshot(t, e.base))
	if len(fake.calls) != 1 {
		t.Errorf("claude calls after two runs = %q; want the first run's Add only", fake.calls)
	}
}

// The seed of the next two tests: a home with the user's own keys, rules and hooks in every
// file init edits, among them a `droidyard hook --root /p` entry in each hook file. That
// entry names a root, so at user level it is the user's (ADR-0124 Decision 1, ADR-0090 §1
// extended): init must leave it through an install and an uninstall, and not double its
// own rootless entry beside it.
func seedUserHome(t *testing.T, e userEnv) {
	t.Helper()
	write(t, e.base, ".claude/settings.json", `{
  "env": {"A": "b"},
  "permissions": {"allow": ["Bash(ls)"], "deny": ["Read(.env)"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/droidyard hook --client claude --root /p"}]},
      {"matcher": "Edit", "hooks": [{"type": "command", "command": "mine"}]}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "bye"}]}]
  }
}`)
	write(t, e.base, ".claude/skills/mine/SKILL.md", "mine\n")
	write(t, e.base, ".claude.json", `{"numStartups": 3, "mcpServers": {"other": {"type": "stdio", "command": "o"}}}`)
	write(t, e.base, ".cursor/mcp.json", `{"mcpServers": {"other": {"command": "o"}}}`)
	write(t, e.base, ".cursor/hooks.json", `{"version": 1, "hooks": {
  "beforeShellExecution": [{"command": "/usr/local/bin/droidyard hook --client cursor --root /p"}, {"command": "mine"}],
  "postToolUse": [{"command": "after", "matcher": "Shell"}]}}`)
	write(t, e.base, ".cursor/cli-config.json", `{"version": 1, "editor": {"vimMode": true}, "permissions": {"allow": ["Shell(ls)"], "deny": []}}`)
	write(t, e.base, ".config/opencode/opencode.json", `{"theme": "dark", "mcp": {"servers": {"theirs": {"type": "remote", "url": "https://x"}}}}`)
	write(t, e.base, ".agents/skills/mine/SKILL.md", "theirs\n")
}

// Rests on: an uninstall at user level removes only what init wrote. The files carry the
// user's keys, rules, hooks and servers, and a rooted `droidyard hook` entry; an install
// followed by an uninstall must give each file back as it was, after parsing. Seen failing
// with initHookShape matching any `droidyard hook` (the rooted entry is then taken for a
// stale one of init's and replaced, then lost).
func TestUserUninstallRestoresTheFiles(t *testing.T) {
	e := newUserEnv(t)
	seedUserHome(t, e)
	before := snapshot(t, e.base)
	fake := e.claude(t)
	o := e.opts(fake, Cursor, Claude, OpenCode)

	res := mustRun(t, o)
	if res.Refused() {
		t.Fatalf("install refused: %+v", res.Changes)
	}
	// The user's rooted entries are still there, beside init's rootless ones.
	for rel, root := range map[string]string{".claude/settings.json": "claude --root /p", ".cursor/hooks.json": "cursor --root /p"} {
		got := read(t, e.base, rel)
		if !strings.Contains(got, "droidyard hook --client "+root) {
			t.Errorf("%s lost the user's `--root /p` entry:\n%s", rel, got)
		}
		if !strings.Contains(got, testCommand+" hook --client "+strings.Fields(root)[0]+`"`) {
			t.Errorf("%s lacks init's rootless entry:\n%s", rel, got)
		}
	}

	res = mustRun(t, uninstall(o))
	if res.Refused() {
		t.Fatalf("uninstall refused: %+v", res.Changes)
	}
	sameTree(t, before, snapshot(t, e.base))
	if want := []string{"add " + wantClaudeMCP, "remove"}; !reflect.DeepEqual(fake.calls, want) {
		t.Errorf("claude calls = %q; want %q", fake.calls, want)
	}
}

// ADR-0090 §1 at user level: a rootless entry of init's for another binary path or state
// root is stale and is replaced, not doubled, and an uninstall removes it too. An entry of
// the other client, one with a wrapper and one with an extra word stay the user's.
func TestUserStaleHookIsReplaced(t *testing.T) {
	e := newUserEnv(t)
	bash := func(cmd string) string {
		return `{"matcher":"Bash","hooks":[{"type":"command","command":"` + cmd + `"}]}`
	}
	edit := func(cmd string) string {
		return `{"matcher":"Write|Edit|NotebookEdit","hooks":[{"type":"command","command":"` + cmd + `"}]}`
	}
	stale := "DROIDYARD_STATE_DIR=/old/state /old/bin/droidyard hook --client claude"
	// The user's, kept through an install and an uninstall.
	otherCmds := []string{
		"/old/bin/droidyard hook --client cursor",
		"/old/bin/droidyard hook --client claude --extra",
		"sh -c '/old/bin/droidyard hook --client claude'",
	}
	var others []string
	for _, c := range otherCmds {
		others = append(others, bash(c))
	}
	seed := `{"hooks":{"PreToolUse":[` + bash(stale) + `,` + edit("/older/bin/droidyard hook --client claude") + `,` + strings.Join(others, ",") + `]}}`
	write(t, e.base, ".claude/settings.json", seed)
	write(t, e.base, ".cursor/hooks.json", `{"version":1,"hooks":{"beforeShellExecution":[{"command":"/old/bin/droidyard hook --client cursor"}]}}`)

	o := e.opts(e.claude(t), Cursor, Claude)
	res := mustRun(t, o)
	if c := statusOf(t, res, filepath.ToSlash(filepath.Join(e.dirs.Claude, "settings.json"))); !strings.Contains(notesOf(c), "replaced 2 stale entries") {
		t.Errorf("settings.json notes = %q; want 2 stale entries replaced", notesOf(c))
	}
	got := read(t, e.base, ".claude/settings.json")
	if strings.Contains(got, "/old/bin/droidyard hook --client claude\"") || strings.Contains(got, "/older/") || strings.Contains(got, "/old/state") {
		t.Errorf("a stale rootless entry is left:\n%s", got)
	}
	if n := strings.Count(got, `"`+userClaudeHook+`"`); n != 5 {
		t.Errorf("init's current entry appears %d times; want 5 (Pre x2, Post x2, Failure x1)\n%s", n, got)
	}
	for _, cmd := range otherCmds {
		if !strings.Contains(got, `"command": "`+cmd+`"`) {
			t.Errorf("the user's entry %q is gone:\n%s", cmd, got)
		}
	}
	hooks := read(t, e.base, ".cursor/hooks.json")
	if strings.Contains(hooks, "/old/bin") || strings.Count(hooks, userCursorHook) != 6 {
		t.Errorf("cursor hooks.json after the run:\n%s", hooks)
	}

	// Uninstall: init's entries go, the stale-shaped ones with them, the user's stay.
	mustRun(t, uninstall(o))
	left := read(t, e.base, ".claude/settings.json")
	if strings.Contains(left, userClaudeHook) {
		t.Errorf("uninstall left init's entry:\n%s", left)
	}
	var parsed struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(left), &parsed); err != nil {
		t.Fatal(err)
	}
	if n := len(parsed.Hooks["PreToolUse"]); n != len(others) {
		t.Errorf("PreToolUse holds %d entries after uninstall; want the user's %d:\n%s", n, len(others), left)
	}
	if exists(e.base, ".cursor/hooks.json") {
		t.Errorf("cursor hooks.json held only init's entries and was not deleted:\n%s", read(t, e.base, ".cursor/hooks.json"))
	}
}

// Claude's MCP entry (ADR-0124 Decision 1): .claude.json is read, never written; the
// change goes through the runner. Absent, it is added; equal, it is left on install and
// removed on --uninstall; different, it is the user's and kept; a dry run calls nothing;
// with no runner the change is refused and the command is the entry for the user.
func TestClaudeUserMCPGoesThroughTheRunner(t *testing.T) {
	const (
		stored = `{"type":"stdio","command":"/opt/bin/droidyard","args":["mcp"]}`
		other  = `{"type":"stdio","command":"/elsewhere/droidyard","args":["mcp","--root","/p"]}`
	)
	addCmd := "claude mcp add-json --scope user droidyard '" + wantClaudeMCP + "'"
	rmCmd := "claude mcp remove --scope user droidyard"
	state := func(servers string) string {
		return `{"numStartups":3,"mcpServers":` + servers + `}`
	}

	cases := []struct {
		name      string
		claudeDoc string // .claude.json; "" for no file
		mod       func(*Options)
		wantCalls []string
		status    Status
		notes     string // a fragment of the change's notes
		entry     string // Change.Entry
	}{
		{name: "absent, no file: added", wantCalls: []string{"add " + wantClaudeMCP}, status: Created},
		{name: "absent, other servers: added", claudeDoc: state(`{"other":{"type":"stdio","command":"o"}}`), wantCalls: []string{"add " + wantClaudeMCP}, status: Updated},
		{name: "absent, mcpServers null", claudeDoc: `{"mcpServers":null}`, wantCalls: []string{"add " + wantClaudeMCP}, status: Updated},
		{name: "equal: unchanged", claudeDoc: state(`{"droidyard":` + stored + `}`), status: Unchanged, notes: "already there"},
		{name: "equal after parsing, other key order", claudeDoc: state(`{"droidyard":{"args":["mcp"],"command":"/opt/bin/droidyard","type":"stdio"}}`), status: Unchanged, notes: "already there"},
		{name: "different: kept and reported", claudeDoc: state(`{"droidyard":` + other + `}`), status: Unchanged, notes: "yours differs"},
		{name: "equal, uninstall: removed", claudeDoc: state(`{"droidyard":` + stored + `}`), mod: func(o *Options) { o.Uninstall = true }, wantCalls: []string{"remove"}, status: Removed},
		{name: "different, uninstall: kept", claudeDoc: state(`{"droidyard":` + other + `}`), mod: func(o *Options) { o.Uninstall = true }, status: Unchanged, notes: "differs"},
		{name: "absent, uninstall: nothing", claudeDoc: state(`{}`), mod: func(o *Options) { o.Uninstall = true }, status: Unchanged, notes: "not there"},
		{name: "no file, uninstall: nothing", mod: func(o *Options) { o.Uninstall = true }, status: Unchanged, notes: "not there"},
		{name: "dry run, absent: would add, calls nothing", mod: func(o *Options) { o.DryRun = true }, status: Created, notes: "added"},
		{name: "dry run, equal, uninstall: would remove, calls nothing", claudeDoc: state(`{"droidyard":` + stored + `}`), mod: func(o *Options) { o.DryRun, o.Uninstall = true, true }, status: Removed, notes: "removed"},
		{name: "no runner, absent: refused with the command", mod: func(o *Options) { o.ClaudeMCP = nil }, status: Refused, notes: "no `claude`", entry: addCmd},
		{name: "no runner, dry run, absent: refused too", mod: func(o *Options) { o.ClaudeMCP, o.DryRun = nil, true }, status: Refused, entry: addCmd},
		{name: "no runner, equal, uninstall: refused with the removal", claudeDoc: state(`{"droidyard":` + stored + `}`), mod: func(o *Options) { o.ClaudeMCP, o.Uninstall = nil, true }, status: Refused, entry: rmCmd},
		{name: "no runner, equal: nothing to do", claudeDoc: state(`{"droidyard":` + stored + `}`), mod: func(o *Options) { o.ClaudeMCP = nil }, status: Unchanged, notes: "already there"},
		{name: "unparseable state: refused, never edited", claudeDoc: `{"mcpServers": `, status: Refused, notes: "cannot read it", entry: addCmd},
		{name: "mcpServers not an object: refused", claudeDoc: `{"mcpServers": [1]}`, status: Refused, notes: "cannot read it", entry: addCmd},
		{name: "a state file over 1 MiB is ordinary", claudeDoc: `{"history":"` + strings.Repeat("x", 3<<20) + `","mcpServers":{"droidyard":` + stored + `}}`, status: Unchanged, notes: "already there"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newUserEnv(t)
			if tc.claudeDoc != "" {
				write(t, e.base, ".claude.json", tc.claudeDoc)
			}
			fake := e.claude(t)
			o := e.opts(fake, Claude)
			if tc.mod != nil {
				tc.mod(&o)
			}
			res := mustRun(t, o)
			c := statusOf(t, res, filepath.ToSlash(e.dirs.ClaudeJSON))
			if c.Status != tc.status {
				t.Errorf("status = %s (%s); want %s", c.Status, notesOf(c), tc.status)
			}
			if tc.notes != "" && !strings.Contains(notesOf(c), tc.notes) {
				t.Errorf("notes = %q; want %q in them", notesOf(c), tc.notes)
			}
			if c.Entry != tc.entry {
				t.Errorf("entry = %q; want %q", c.Entry, tc.entry)
			}
			if !reflect.DeepEqual(fake.calls, tc.wantCalls) {
				t.Errorf("runner calls = %q; want %q", fake.calls, tc.wantCalls)
			}
			// A refused or unchanged .claude.json is exactly what it was; init itself
			// never writes it, only the runner does.
			if tc.claudeDoc != "" && len(fake.calls) == 0 {
				if got := read(t, e.base, ".claude.json"); got != tc.claudeDoc {
					t.Errorf(".claude.json was written by init:\n%.200s", got)
				}
			}
			if tc.claudeDoc == "" && len(fake.calls) == 0 && exists(e.base, ".claude.json") {
				t.Error("init created .claude.json")
			}
		})
	}

	t.Run("a runner error is refused with the command", func(t *testing.T) {
		e := newUserEnv(t)
		fake := e.claude(t)
		fake.addErr = errors.New("exit status 1")
		c := statusOf(t, mustRun(t, e.opts(fake, Claude)), filepath.ToSlash(e.dirs.ClaudeJSON))
		if c.Status != Refused || c.Entry != addCmd || !strings.Contains(notesOf(c), "exit status 1") {
			t.Errorf("change = %+v; want refused with the error and the command", c)
		}
		write(t, e.base, ".claude.json", state(`{"droidyard":`+stored+`}`))
		fake.rmError = errors.New("exit status 1")
		o := e.opts(fake, Claude)
		o.Uninstall = true
		c = statusOf(t, mustRun(t, o), filepath.ToSlash(e.dirs.ClaudeJSON))
		if c.Status != Refused || c.Entry != rmCmd {
			t.Errorf("uninstall change = %+v; want refused with %q", c, rmCmd)
		}
	})
}

// Cursor and OpenCode share ~/.agents/skills/droidyard-android (ADR-0037 §2's keep rule at
// user level): uninstalling one client keeps the skill while the other still has its
// droidyard entry, in its own directory, and the last one takes it away. Claude's skill is
// its own. Seen failing with the shared-skill check dropped (the skill is deleted under
// OpenCode).
func TestUserSkillSharedByCursorAndOpenCode(t *testing.T) {
	const shared = ".agents/skills/droidyard-android/SKILL.md"
	e := newUserEnv(t)
	mustRun(t, e.opts(e.claude(t), Cursor, OpenCode, Claude))

	res := mustRun(t, uninstall(e.opts(e.claude(t), Cursor)))
	if !exists(e.base, shared) {
		t.Fatal("uninstalling Cursor removed the skill OpenCode still reads")
	}
	if c := statusOf(t, res, filepath.ToSlash(filepath.Join(e.dirs.Agents, "skills/droidyard-android/SKILL.md"))); !strings.Contains(notesOf(c), "still used by opencode") {
		t.Errorf("skill notes = %q", notesOf(c))
	}
	if exists(e.base, ".cursor/mcp.json") {
		t.Error("Cursor's entry is left")
	}

	mustRun(t, uninstall(e.opts(e.claude(t), OpenCode)))
	if exists(e.base, shared) {
		t.Error("the shared skill is left after both clients were uninstalled")
	}
	if !exists(e.base, ".claude/skills/droidyard-android/SKILL.md") {
		t.Error("uninstalling Cursor and OpenCode removed Claude's own skill")
	}

	// The other way: OpenCode first keeps it for Cursor.
	e2 := newUserEnv(t)
	mustRun(t, e2.opts(nil, Cursor, OpenCode))
	mustRun(t, uninstall(e2.opts(nil, OpenCode)))
	if !exists(e2.base, shared) {
		t.Error("uninstalling OpenCode removed the skill Cursor still reads")
	}

	// A client with no entry does not keep the skill.
	e3 := newUserEnv(t)
	mustRun(t, e3.opts(nil, Cursor))
	mustRun(t, uninstall(e3.opts(nil, Cursor)))
	if exists(e3.base, shared) {
		t.Error("the skill is left though no client uses it")
	}
}

// A ~/.claude kept in a dotfiles repository is a link to a directory: the run follows it
// and writes into the target, leaving the link a link. A file in it that links out of it is
// refused, and what it links to is not touched (ADR-0037 §2's confinement, one directory
// at a time).
func TestUserConfigDirectoriesFollowLinksButNothingLeavesThem(t *testing.T) {
	e := newUserEnv(t)
	dotfiles := filepath.Join(e.base, "dotfiles", "claude")
	if err := os.MkdirAll(dotfiles, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, e.dirs.Claude); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(e.base, "elsewhere", "settings.json")
	write(t, e.base, "elsewhere/settings.json", `{"mine":true}`)
	if err := os.Symlink(outside, filepath.Join(dotfiles, "settings.json")); err != nil {
		t.Fatal(err)
	}

	res := mustRun(t, e.opts(e.claude(t), Claude))
	if fi, err := os.Lstat(e.dirs.Claude); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the linked config directory was replaced: %v %v", fi, err)
	}
	// The skill went into the link's target.
	if got := read(t, dotfiles, "skills/droidyard-android/SKILL.md"); !strings.Contains(got, "droidyard-android") {
		t.Errorf("the skill is not in the dotfiles directory: %q", got)
	}
	// settings.json links outside the directory: refused, and the target is as it was.
	c := statusOf(t, res, filepath.ToSlash(filepath.Join(e.dirs.Claude, "settings.json")))
	if c.Status != Refused || !strings.Contains(notesOf(c), "leaves") {
		t.Errorf("settings.json = %s (%s); want refused, it links out of the directory", c.Status, notesOf(c))
	}
	if got := read(t, e.base, "elsewhere/settings.json"); got != `{"mine":true}` {
		t.Errorf("the file outside was written: %s", got)
	}
	if !res.Refused() {
		t.Error("Refused() is false")
	}
}

// OpenCode at user level: opencode.jsonc is refused as in a project, opencode.json is left
// alone, and --uninstall still takes init's entry out of opencode.json.
func TestUserOpenCodeJSONCIsRefused(t *testing.T) {
	e := newUserEnv(t)
	write(t, e.base, ".config/opencode/opencode.jsonc", "{ // mine\n}")
	res := mustRun(t, e.opts(nil, OpenCode))
	c := statusOf(t, res, filepath.ToSlash(filepath.Join(e.dirs.OpenCode, "opencode.jsonc")))
	if c.Status != Refused || !strings.Contains(c.Entry, `"command": [`) || strings.Contains(c.Entry, "--root") {
		t.Errorf("opencode.jsonc change = %+v; want refused with the rootless entry", c)
	}
	if exists(e.base, ".config/opencode/opencode.json") {
		t.Error("opencode.json was written beside a jsonc")
	}
}

// A custom state root travels in Cursor's server entry and in the hook commands, as in a
// project (ADR-0061 §3), and Claude's own MCP entry carries none (not observed to lose it).
func TestUserRunCarriesACustomStateRoot(t *testing.T) {
	e := newUserEnv(t)
	o := e.opts(e.claude(t), Cursor, Claude)
	o.StateDir = "/var/state dir"
	res := mustRun(t, o)
	hook := "DROIDYARD_STATE_DIR='/var/state dir' " + userClaudeHook
	if res.Hooks[Claude] != hook {
		t.Errorf("claude hook = %q; want %q", res.Hooks[Claude], hook)
	}
	if got := read(t, e.base, ".cursor/mcp.json"); !strings.Contains(got, `"DROIDYARD_STATE_DIR": "/var/state dir"`) || !strings.Contains(got, `${workspaceFolder}`) {
		t.Errorf("cursor mcp.json:\n%s", got)
	}
	sameJSON(t, ".claude.json", read(t, e.base, ".claude.json"), `{"mcpServers":{"droidyard":`+wantClaudeMCP+`}}`)

	// A second run recognises its own entries: nothing changes.
	again := mustRun(t, o)
	for _, c := range again.Changes {
		if c.Status != Unchanged {
			t.Errorf("rerun: %s %s (%s)", c.Path, c.Status, notesOf(c))
		}
	}
}

// A dry run writes nothing and calls nothing, in either direction.
func TestUserDryRunWritesNothing(t *testing.T) {
	e := newUserEnv(t)
	fake := e.claude(t)
	o := e.opts(fake, Cursor, Claude, OpenCode)
	o.DryRun = true
	res := mustRun(t, o)
	if left := snapshot(t, e.base); len(left) != 0 {
		t.Errorf("a dry run wrote %v", fileSet(left))
	}
	if len(fake.calls) != 0 {
		t.Errorf("a dry run called claude: %q", fake.calls)
	}
	if statusOf(t, res, filepath.ToSlash(filepath.Join(e.dirs.Cursor, "mcp.json"))).Status != Created {
		t.Error("the dry run did not report what it would create")
	}

	mustRun(t, e.opts(fake, Cursor, Claude, OpenCode))
	before, calls := snapshot(t, e.base), len(fake.calls)
	o.Uninstall = true
	mustRun(t, o)
	sameText(t, before, snapshot(t, e.base))
	if len(fake.calls) != calls {
		t.Errorf("a dry-run uninstall called claude: %q", fake.calls[calls:])
	}
}

func TestUserOptionsAreChecked(t *testing.T) {
	e := newUserEnv(t)
	bad := map[string]func(*Options){
		"a root at user level":      func(o *Options) { o.Root = e.base },
		"a relative Claude dir":     func(o *Options) { o.Dirs.Claude = ".claude" },
		"no Cursor dir":             func(o *Options) { o.Dirs.Cursor = "" },
		"no agents dir":             func(o *Options) { o.Dirs.Agents = "" },
		"a relative .claude.json":   func(o *Options) { o.Dirs.ClaudeJSON = "x.json" },
		"a relative command":        func(o *Options) { o.Command = "droidyard" },
		"a relative state dir":      func(o *Options) { o.StateDir = "state" },
		"a Claude dir that is file": func(o *Options) { o.Dirs.Claude = filepath.Join(e.base, "afile") },
	}
	write(t, e.base, "afile", "x")
	for name, mod := range bad {
		o := e.opts(e.claude(t), Cursor, Claude)
		mod(&o)
		if _, err := Run(o); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if left := fileSet(snapshot(t, e.base)); !reflect.DeepEqual(left, []string{"afile"}) {
		t.Errorf("a refused run wrote %v", left)
	}
}
