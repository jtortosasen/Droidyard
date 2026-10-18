package assistant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
)

const testCommand = "/opt/bin/droidyard"

var testTools = []string{"device_uiTree", "device_list"} // unsorted on purpose

func testSkill() fs.FS {
	return fstest.MapFS{
		"SKILL.md":            {Data: []byte("---\nname: droidyard-android\n---\nbody\n")},
		"references/tools.md": {Data: []byte("| tool |\n")},
	}
}

func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func opts(root string, clients ...Client) Options {
	return Options{Root: root, Command: testCommand, Clients: clients, Tools: testTools, Skill: testSkill()}
}

func mustRun(t *testing.T, o Options) Result {
	t.Helper()
	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func uninstall(o Options) Options { o.Uninstall = true; return o }

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// snapshot is every file under root by slash path, and every directory as "path/".
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		out[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameTree compares two snapshots: text byte for byte, JSON equal after parsing, which
// is the contract --uninstall makes (ADR-0037 Decision 2).
func sameTree(t *testing.T, want, got map[string]string) {
	t.Helper()
	var names []string
	for n := range want {
		names = append(names, n)
	}
	for n := range got {
		if _, ok := want[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		w, wok := want[n]
		g, gok := got[n]
		switch {
		case !wok:
			t.Errorf("%s exists after, and did not before:\n%s", n, g)
		case !gok:
			t.Errorf("%s existed before and is gone after:\n%s", n, w)
		case strings.HasSuffix(n, ".json"):
			var a, b any
			if json.Unmarshal([]byte(w), &a) != nil || json.Unmarshal([]byte(g), &b) != nil {
				t.Errorf("%s: not JSON\nbefore:\n%s\nafter:\n%s", n, w, g)
			} else if !reflect.DeepEqual(a, b) {
				t.Errorf("%s differs after parsing\nbefore:\n%s\nafter:\n%s", n, w, g)
			}
		case w != g:
			t.Errorf("%s differs\nbefore: %q\nafter:  %q", n, w, g)
		}
	}
}

func statusOf(t *testing.T, res Result, path string) Change {
	t.Helper()
	for _, c := range res.Changes {
		if c.Path == path {
			return c
		}
	}
	t.Fatalf("no change reported for %s: %+v", path, res.Changes)
	return Change{}
}

func notesOf(c Change) string { return strings.Join(c.Notes, " | ") }

// The files each client gets, written out in full: the golden trees of plan m1o step 8.
// Every command is the binary's absolute path and every server names the root, since
// a client's spawn directory is not known.
func TestInitWritesEachClientsFiles(t *testing.T) {
	skill := "---\nname: droidyard-android\n---\nbody\n"
	tools := "| tool |\n"
	// deny: Cursor's CLI refuses a project cli.json without a deny array (m1o step 9).
	cursorAllow := `{
  "permissions": {
    "deny": [],
    "allow": [
      "Mcp(droidyard:device_list)",
      "Mcp(droidyard:device_uiTree)"
    ]
  }
}
`
	// The hook entries (ADR-0061 §5, ADR-0063 §7); a temp root has no character shellArg
	// quotes.
	claudeAllow := func(root string) string {
		event := func(name string, last bool) string {
			end := ","
			if last {
				end = ""
			}
			return fmt.Sprintf(`    %q: [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "/opt/bin/droidyard hook --client claude --root %s"
          }
        ]
      }
    ]%s
`, name, root, end)
		}
		return `{
  "permissions": {
    "allow": [
      "mcp__droidyard__device_list",
      "mcp__droidyard__device_uiTree"
    ]
  },
  "hooks": {
` + event("PreToolUse", false) + event("PostToolUse", false) + event("PostToolUseFailure", true) + `  }
}
`
	}
	cursorHooks := func(root string) string {
		return fmt.Sprintf(`{
  "version": 1,
  "hooks": {
    "beforeShellExecution": [
      {
        "command": "/opt/bin/droidyard hook --client cursor --root %[1]s"
      }
    ],
    "preToolUse": [
      {
        "command": "/opt/bin/droidyard hook --client cursor --root %[1]s",
        "matcher": "Delete"
      }
    ],
    "postToolUse": [
      {
        "command": "/opt/bin/droidyard hook --client cursor --root %[1]s",
        "matcher": "Shell"
      }
    ],
    "postToolUseFailure": [
      {
        "command": "/opt/bin/droidyard hook --client cursor --root %[1]s",
        "matcher": "Shell"
      }
    ]
  }
}
`, root)
	}
	server := func(root string) string {
		return fmt.Sprintf(`{
  "mcpServers": {
    "droidyard": {
      "command": "/opt/bin/droidyard",
      "args": [
        "mcp",
        "--root",
        %q
      ]
    }
  }
}
`, root)
	}
	opencode := func(root string) string {
		return fmt.Sprintf(`{
  "mcp": {
    "servers": {
      "droidyard": {
        "type": "local",
        "command": [
          "/opt/bin/droidyard",
          "mcp",
          "--root",
          %q
        ],
        "codemode": false
      }
    }
  }
}
`, root)
	}
	cases := []struct {
		client Client
		want   func(root string) map[string]string
	}{
		{Cursor, func(root string) map[string]string {
			return map[string]string{
				".agents/":                                             "",
				".agents/skills/":                                      "",
				".agents/skills/droidyard-android/":                    "",
				".agents/skills/droidyard-android/SKILL.md":            skill,
				".agents/skills/droidyard-android/references/":         "",
				".agents/skills/droidyard-android/references/tools.md": tools,
				".cursor/":           "",
				".cursor/mcp.json":   server(root),
				".cursor/cli.json":   cursorAllow,
				".cursor/hooks.json": cursorHooks(root),
			}
		}},
		{Claude, func(root string) map[string]string {
			return map[string]string{
				".claude/":                                             "",
				".claude/skills/":                                      "",
				".claude/skills/droidyard-android/":                    "",
				".claude/skills/droidyard-android/SKILL.md":            skill,
				".claude/skills/droidyard-android/references/":         "",
				".claude/skills/droidyard-android/references/tools.md": tools,
				".claude/settings.json":                                claudeAllow(root),
				".mcp.json":                                            server(root),
			}
		}},
		{OpenCode, func(root string) map[string]string {
			return map[string]string{
				".agents/":                                             "",
				".agents/skills/":                                      "",
				".agents/skills/droidyard-android/":                    "",
				".agents/skills/droidyard-android/SKILL.md":            skill,
				".agents/skills/droidyard-android/references/":         "",
				".agents/skills/droidyard-android/references/tools.md": tools,
				"opencode.json":                                        opencode(root),
			}
		}},
	}
	for _, c := range cases {
		t.Run(string(c.client), func(t *testing.T) {
			root := projectRoot(t)
			res := mustRun(t, opts(root, c.client))
			if res.Refused() {
				t.Fatalf("refused: %+v", res.Changes)
			}
			got := snapshot(t, root)
			// The golden is the whole tree: a stray file (a temp file left by a write)
			// fails here too.
			sameText(t, c.want(root), got)
		})
	}
}

// sameText is an exact comparison, JSON included: a golden tree pins the bytes.
func sameText(t *testing.T, want, got map[string]string) {
	t.Helper()
	for n, w := range want {
		g, ok := got[n]
		if !ok {
			t.Errorf("%s was not written", n)
		} else if g != w {
			t.Errorf("%s\nwant:\n%s\ngot:\n%s", n, w, g)
		}
	}
	for n := range got {
		if _, ok := want[n]; !ok {
			t.Errorf("%s was written and the golden tree has no such file", n)
		}
	}
}

// A second init changes nothing: not a byte, and it says so.
func TestSecondInitChangesNothing(t *testing.T) {
	root := projectRoot(t)
	o := opts(root, Cursor, Claude, OpenCode)
	mustRun(t, o)
	first := snapshot(t, root)
	res := mustRun(t, o)
	sameText(t, first, snapshot(t, root))
	for _, c := range res.Changes {
		if c.Status != Unchanged {
			t.Errorf("second init: %s is %s (%s); want unchanged", c.Path, c.Status, notesOf(c))
		}
	}
}

// ADR-0038: init writes, refreshes and uninstalls nothing in AGENTS.md or CLAUDE.md,
// whatever they hold, including a block from the 1o wording.
func TestInitNeverTouchesAgentsOrClaudeMd(t *testing.T) {
	oldBlock := "<!-- droidyard:begin -->\nold wording\n<!-- droidyard:end -->\n"
	files := map[string]string{
		"AGENTS.md": "# Rules\n\nBe kind.\n",
		"CLAUDE.md": "mine\n\n" + oldBlock,
	}
	root := projectRoot(t)
	for n, c := range files {
		write(t, root, n, c)
	}
	check := func(when string, res Result) {
		t.Helper()
		for n, want := range files {
			if got := read(t, root, n); got != want {
				t.Errorf("%s: %s is %q; want it byte-identical %q", when, n, got, want)
			}
			for _, c := range res.Changes {
				if c.Path == n {
					t.Errorf("%s: %s is in the result (%s)", when, n, c.Status)
				}
			}
		}
	}
	check("init", mustRun(t, opts(root, Cursor, Claude, OpenCode)))
	check("--uninstall", mustRun(t, uninstall(opts(root, Cursor, Claude, OpenCode))))
}

// The merge is by key: the user's servers stay in their order, and a droidyard entry
// that is already there, even a different one, is kept and reported, never replaced.
func TestMergeKeepsTheUsersServersAndAnExistingEntry(t *testing.T) {
	t.Run("added beside the user's servers", func(t *testing.T) {
		root := projectRoot(t)
		write(t, root, ".cursor/mcp.json", `{"other": [1, 2], "mcpServers": {"zeta": {"command": "z"}, "alpha": {"command": "a", "args": ["<&>"]}}}`)
		res := mustRun(t, opts(root, Cursor))
		if c := statusOf(t, res, ".cursor/mcp.json"); c.Status != Updated {
			t.Errorf("status %s; want updated", c.Status)
		}
		var got struct {
			Other      []int                      `json:"other"`
			McpServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(read(t, root, ".cursor/mcp.json")), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Other, []int{1, 2}) || len(got.McpServers) != 3 {
			t.Errorf("the user's content changed: %+v", got)
		}
		if raw := string(got.McpServers["alpha"]); !strings.Contains(raw, `"<&>"`) {
			t.Errorf("the user's alpha server was rewritten: %s", raw)
		}
		text := read(t, root, ".cursor/mcp.json")
		if !(strings.Index(text, `"zeta"`) < strings.Index(text, `"alpha"`) && strings.Index(text, `"alpha"`) < strings.Index(text, `"droidyard"`)) {
			t.Errorf("the keys were reordered: %s", text)
		}
	})

	t.Run("an existing droidyard entry is kept and reported", func(t *testing.T) {
		root := projectRoot(t)
		orig := `{"mcpServers": {"droidyard": {"command": "/somewhere/else/droidyard", "args": ["mcp"], "env": {"X": "1"}}, "mine": {"command": "m"}}}`
		write(t, root, ".cursor/mcp.json", orig)
		res := mustRun(t, opts(root, Cursor))
		c := statusOf(t, res, ".cursor/mcp.json")
		if c.Status != Unchanged || !strings.Contains(notesOf(c), "kept") {
			t.Errorf("status %s, notes %q; want unchanged with the entry kept and reported", c.Status, notesOf(c))
		}
		if got := read(t, root, ".cursor/mcp.json"); got != orig {
			t.Errorf("an existing entry was rewritten:\n%s", got)
		}
	})
}

// Arrays: droidyard's element is appended only if no equal element exists, and the
// user's elements are never touched.
func TestAllowlistAppendsOnlyWhatIsAbsent(t *testing.T) {
	root := projectRoot(t)
	write(t, root, ".cursor/cli.json", `{"version": 1, "permissions": {"allow": ["Shell(ls)", "Mcp(droidyard:device_list)", "Mcp(droidyard:build_assemble)"], "deny": ["Shell(rm)"]}}`)
	res := mustRun(t, opts(root, Cursor))
	var got struct {
		Version     int `json:"version"`
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(read(t, root, ".cursor/cli.json")), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"Shell(ls)", "Mcp(droidyard:device_list)", "Mcp(droidyard:build_assemble)", "Mcp(droidyard:device_uiTree)"}
	if !reflect.DeepEqual(got.Permissions.Allow, want) || got.Version != 1 || !reflect.DeepEqual(got.Permissions.Deny, []string{"Shell(rm)"}) {
		t.Errorf("allow = %v, deny = %v; want the user's elements untouched and only device_uiTree appended", got.Permissions.Allow, got.Permissions.Deny)
	}
	if c := statusOf(t, res, ".cursor/cli.json"); c.Status != Updated {
		t.Errorf("status %s; want updated", c.Status)
	}
}

// A user's own allow survives init and --uninstall; --uninstall takes out the elements
// init wrote and the keys they leave empty, and nothing else.
func TestUsersOwnAllowSurvivesInitAndUninstall(t *testing.T) {
	root := projectRoot(t)
	// Not read-only, so init does not write it and --uninstall cannot mistake it for its own.
	// A valid Cursor cli.json has a deny array (cursor-agent refuses one without); a file
	// that lacked it keeps the empty one init added, which makes it valid.
	write(t, root, ".cursor/cli.json", `{"permissions": {"allow": ["Mcp(droidyard:build_assemble)", "Shell(ls)"], "deny": []}}`)
	write(t, root, ".claude/settings.json", `{"model": "x", "permissions": {"allow": ["Bash(git status)", "mcp__droidyard__build_assemble"], "ask": ["Bash(rm:*)"]}}`)
	before := snapshot(t, root)

	mustRun(t, opts(root, Cursor, Claude))
	for _, f := range []string{".cursor/cli.json", ".claude/settings.json"} {
		for _, mine := range []string{"Mcp(droidyard:build_assemble)", "Shell(ls)", "Bash(git status)", "mcp__droidyard__build_assemble"} {
			if strings.Contains(before[f], mine) && !strings.Contains(read(t, root, f), mine) {
				t.Errorf("init lost the user's %q from %s", mine, f)
			}
		}
	}
	res := mustRun(t, uninstall(opts(root, Cursor, Claude)))
	if res.Refused() {
		t.Fatalf("refused: %+v", res.Changes)
	}
	for _, f := range []string{".cursor/cli.json", ".claude/settings.json"} {
		sameTree(t, map[string]string{f: before[f]}, map[string]string{f: read(t, root, f)})
	}
	// init's own files are gone, with the directories it made.
	for _, gone := range []string{".mcp.json", ".cursor/mcp.json", ".claude/skills", ".agents"} {
		if exists(root, gone) {
			t.Errorf("--uninstall left %s", gone)
		}
	}
}

// --uninstall puts the tree back: text byte for byte, JSON equal after parsing, and no
// file or directory that init made.
func TestUninstallRestoresTheTree(t *testing.T) {
	t.Run("a project with the user's own files", func(t *testing.T) {
		root := projectRoot(t)
		write(t, root, "AGENTS.md", "# Rules\n\nBe kind.")
		write(t, root, "CLAUDE.md", "mine\n")
		write(t, root, ".cursor/mcp.json", `{"mcpServers": {"other": {"command": "o"}}}`)
		write(t, root, ".cursor/cli.json", `{"permissions": {"allow": ["Shell(ls)"], "deny": []}}`)
		write(t, root, ".mcp.json", `{"mcpServers": {"theirs": {"command": "t"}}, "x": 1}`)
		write(t, root, ".claude/settings.json", `{"permissions": {"allow": ["Bash(ls)"]}, "env": {"A": "b"}}`)
		write(t, root, "opencode.json", `{"theme": "dark", "mcp": {"timeout": 5000, "servers": {"theirs": {"type": "remote", "url": "https://x"}}}}`)
		write(t, root, ".agents/skills/mine/SKILL.md", "theirs\n")
		write(t, root, "src/Main.kt", "fun main() {}\n")
		before := snapshot(t, root)

		mustRun(t, opts(root, Cursor, Claude, OpenCode))
		if reflect.DeepEqual(before, snapshot(t, root)) {
			t.Fatal("init changed nothing")
		}
		res := mustRun(t, uninstall(opts(root, Cursor, Claude, OpenCode)))
		if res.Refused() {
			t.Fatalf("refused: %+v", res.Changes)
		}
		sameTree(t, before, snapshot(t, root))
	})

	t.Run("an empty directory", func(t *testing.T) {
		root := projectRoot(t)
		mustRun(t, opts(root, Cursor, Claude, OpenCode))
		mustRun(t, uninstall(opts(root, Cursor, Claude, OpenCode)))
		if left := snapshot(t, root); len(left) != 0 {
			t.Errorf("--uninstall left %v", left)
		}
	})
}

// The one thing --uninstall cannot put back: an empty object the user had on the path
// of init's entry looks like one init created, so it is pruned with the entry. It
// leaves no empty `mcpServers` behind in the usual case (the file did not have one).
func TestUninstallPrunesTheObjectsItEmptied(t *testing.T) {
	root := projectRoot(t)
	write(t, root, ".mcp.json", `{"mcpServers": {}, "x": 1}`)
	mustRun(t, opts(root, Claude))
	mustRun(t, uninstall(opts(root, Claude)))
	if got := read(t, root, ".mcp.json"); strings.Contains(got, "mcpServers") || !strings.Contains(got, `"x": 1`) {
		t.Errorf(".mcp.json = %s; want the emptied mcpServers gone and x kept", got)
	}
}

// --uninstall removes only what is equal to what init writes now: a changed entry, a
// changed skill are the user's, and are reported.
func TestUninstallKeepsWhatDiffersFromWhatInitWrites(t *testing.T) {
	root := projectRoot(t)
	mustRun(t, opts(root, Cursor))
	write(t, root, ".cursor/mcp.json", `{"mcpServers": {"droidyard": {"command": "/my/own/droidyard", "args": ["mcp"]}}}`)
	write(t, root, ".agents/skills/droidyard-android/SKILL.md", "my edited skill\n")
	before := snapshot(t, root)

	res := mustRun(t, uninstall(opts(root, Cursor)))
	for _, p := range []string{".cursor/mcp.json", ".agents/skills/droidyard-android/SKILL.md"} {
		c := statusOf(t, res, p)
		if c.Status != Unchanged || !strings.Contains(notesOf(c), "kept") {
			t.Errorf("%s: %s (%s); want unchanged, kept and reported", p, c.Status, notesOf(c))
		}
		if before[p] != read(t, root, p) {
			t.Errorf("--uninstall changed %s, which differs from what init writes", p)
		}
	}
}

// A file that does not parse is refused, named and untouched with the entry to add
// printed; the client's other files are still written.
func TestUnparseableFilesAreRefusedAndUntouched(t *testing.T) {
	for name, content := range map[string]string{
		"not JSON":              `{"permissions": {"allow": [`,
		"a JSON array":          `["a"]`,
		"an empty file":         ``,
		"comments":              "{\n// a comment\n\"permissions\": {}}\n",
		"text after the object": `{} {}`,
		"a duplicate key":       `{"permissions": {}, "permissions": {}}`,
		"permissions a string":  `{"permissions": "all"}`,
		"allow an object":       `{"permissions": {"allow": {"a": 1}}}`,
		"allow null":            `{"permissions": {"allow": null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := projectRoot(t)
			write(t, root, ".cursor/cli.json", content)
			res := mustRun(t, opts(root, Cursor))
			c := statusOf(t, res, ".cursor/cli.json")
			if c.Status != Refused || !res.Refused() {
				t.Fatalf("status %s; want refused", c.Status)
			}
			if !strings.Contains(c.Entry, "Mcp(droidyard:device_list)") {
				t.Errorf("the refusal does not print the entry to add: %q", c.Entry)
			}
			if got := read(t, root, ".cursor/cli.json"); got != content {
				t.Errorf("a refused file was changed: %q", got)
			}
			if !exists(root, ".cursor/mcp.json") || !exists(root, ".agents/skills/droidyard-android/SKILL.md") {
				t.Error("one refused file stopped the others from being written")
			}
			// And --uninstall does not rewrite it either.
			un := mustRun(t, uninstall(opts(root, Cursor)))
			if got := read(t, root, ".cursor/cli.json"); got != content {
				t.Errorf("--uninstall changed an unparseable file: %q", got)
			}
			_ = un
		})
	}
}

// opencode.jsonc may hold comments that a rewrite would lose: it is refused and
// untouched, the entry is printed, and opencode.json is not written beside it.
func TestOpenCodeJSONCIsRefused(t *testing.T) {
	root := projectRoot(t)
	orig := "{\n  // my settings\n  \"theme\": \"dark\"\n}\n"
	write(t, root, "opencode.jsonc", orig)
	res := mustRun(t, opts(root, OpenCode))
	c := statusOf(t, res, "opencode.jsonc")
	if c.Status != Refused || !strings.Contains(c.Entry, `"codemode": false`) || !strings.Contains(c.Entry, testCommand) {
		t.Errorf("opencode.jsonc: %s, entry %q; want refused with the entry", c.Status, c.Entry)
	}
	if read(t, root, "opencode.jsonc") != orig {
		t.Error("opencode.jsonc was changed")
	}
	if exists(root, "opencode.json") {
		t.Error("opencode.json was written beside a refused opencode.jsonc")
	}
}

// No path may leave the root, however it gets out.
func TestNothingIsWrittenOutsideTheRoot(t *testing.T) {
	t.Run("a directory that is a symlink out", func(t *testing.T) {
		root, outside := projectRoot(t), projectRoot(t)
		if err := os.Symlink(outside, filepath.Join(root, ".cursor")); err != nil {
			t.Fatal(err)
		}
		res := mustRun(t, opts(root, Cursor))
		for _, p := range []string{".cursor/mcp.json", ".cursor/cli.json"} {
			if c := statusOf(t, res, p); c.Status != Refused {
				t.Errorf("%s: %s; want refused", p, c.Status)
			}
		}
		if left := snapshot(t, outside); len(left) != 0 {
			t.Errorf("init wrote through the link: %v", left)
		}
	})
	t.Run("a file that is a symlink out", func(t *testing.T) {
		root, outside := projectRoot(t), projectRoot(t)
		target := filepath.Join(outside, "mcp.json")
		os.WriteFile(target, []byte("theirs\n"), 0o644)
		os.MkdirAll(filepath.Join(root, ".cursor"), 0o755)
		os.Symlink(target, filepath.Join(root, ".cursor", "mcp.json"))
		res := mustRun(t, opts(root, Cursor))
		if c := statusOf(t, res, ".cursor/mcp.json"); c.Status != Refused {
			t.Errorf(".cursor/mcp.json: %s; want refused", c.Status)
		}
		if b, _ := os.ReadFile(target); string(b) != "theirs\n" {
			t.Errorf("init wrote through the link: %q", b)
		}
	})
	t.Run("a dangling link", func(t *testing.T) {
		root := projectRoot(t)
		os.MkdirAll(filepath.Join(root, ".cursor"), 0o755)
		os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, ".cursor", "mcp.json"))
		if c := statusOf(t, mustRun(t, opts(root, Cursor)), ".cursor/mcp.json"); c.Status != Refused {
			t.Errorf("a dangling .cursor/mcp.json: %s; want refused", c.Status)
		}
	})
	t.Run("a link inside the root is followed and stays a link", func(t *testing.T) {
		root := projectRoot(t)
		write(t, root, "shared.json", "{\"theme\": \"dark\"}\n")
		os.Symlink("shared.json", filepath.Join(root, "opencode.json"))
		mustRun(t, opts(root, OpenCode))
		if fi, err := os.Lstat(filepath.Join(root, "opencode.json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("opencode.json is no longer a symlink: %v", err)
		}
		if got := read(t, root, "shared.json"); !strings.Contains(got, ServerName) || !strings.Contains(got, "theme") {
			t.Errorf("shared.json = %q; want the user's key and droidyard's entry", got)
		}
		mustRun(t, uninstall(opts(root, OpenCode)))
		if got := read(t, root, "shared.json"); strings.Contains(got, ServerName) || !strings.Contains(got, "theme") {
			t.Errorf("--uninstall left %q", got)
		}
	})
}

// --dry-run reports what it would do and writes nothing, for init and --uninstall.
func TestDryRunWritesNothing(t *testing.T) {
	root := projectRoot(t)
	write(t, root, ".cursor/mcp.json", `{"mcpServers": {"other": {"command": "o"}}}`)
	before := snapshot(t, root)
	o := opts(root, Cursor, Claude, OpenCode)
	o.DryRun = true
	res := mustRun(t, o)
	sameTree(t, before, snapshot(t, root))
	if c := statusOf(t, res, ".cursor/mcp.json"); c.Status != Updated {
		t.Errorf("the dry run reports .cursor/mcp.json %s; want updated", c.Status)
	}
	if c := statusOf(t, res, ".mcp.json"); c.Status != Created {
		t.Errorf("the dry run reports .mcp.json %s; want created", c.Status)
	}

	o.DryRun = false
	mustRun(t, o)
	installed := snapshot(t, root)
	o.DryRun, o.Uninstall = true, true
	res = mustRun(t, o)
	sameText(t, installed, snapshot(t, root))
	if c := statusOf(t, res, ".mcp.json"); c.Status != Deleted {
		t.Errorf("the dry-run uninstall reports .mcp.json %s; want deleted", c.Status)
	}
}

// What the allowlists hold is the tools the caller passed, sorted, in each client's
// own spelling, and nothing for OpenCode.
func TestAllowedRulesPerClient(t *testing.T) {
	root := projectRoot(t)
	res := mustRun(t, opts(root, Cursor, Claude, OpenCode))
	if want := []string{"Mcp(droidyard:device_list)", "Mcp(droidyard:device_uiTree)"}; !reflect.DeepEqual(res.Allowed[Cursor], want) {
		t.Errorf("cursor allowed %v; want %v", res.Allowed[Cursor], want)
	}
	if want := []string{"mcp__droidyard__device_list", "mcp__droidyard__device_uiTree"}; !reflect.DeepEqual(res.Allowed[Claude], want) {
		t.Errorf("claude allowed %v; want %v", res.Allowed[Claude], want)
	}
	if _, ok := res.Allowed[OpenCode]; ok {
		t.Error("OpenCode has an allowlist; init writes none for it")
	}
}

// An existing file keeps its mode, and no temporary file is left beside what was written.
func TestWritesKeepTheModeAndLeaveNoTemporaryFiles(t *testing.T) {
	root := projectRoot(t)
	write(t, root, ".mcp.json", `{}`)
	if err := os.Chmod(filepath.Join(root, ".mcp.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, opts(root, Claude))
	fi, err := os.Stat(filepath.Join(root, ".mcp.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf(".mcp.json mode = %v, %v; want 0600 kept", fi.Mode().Perm(), err)
	}
	for name := range snapshot(t, root) {
		if strings.Contains(name, ".droidyard-") {
			t.Errorf("a temporary file was left: %s", name)
		}
	}
}

func TestOptionsAreChecked(t *testing.T) {
	root := projectRoot(t)
	for name, mutate := range map[string]func(*Options){
		"a relative command": func(o *Options) { o.Command = "droidyard" },
		"a relative root":    func(o *Options) { o.Root = "proj" },
		"no client":          func(o *Options) { o.Clients = nil },
		"no tools":           func(o *Options) { o.Tools = nil },
		"a missing root":     func(o *Options) { o.Root = filepath.Join(root, "nope") },
	} {
		o := opts(root, Cursor)
		mutate(&o)
		if _, err := Run(o); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestParseClients(t *testing.T) {
	if got, err := ParseClients("all"); err != nil || !reflect.DeepEqual(got, Clients()) {
		t.Errorf("all = %v, %v", got, err)
	}
	if got, err := ParseClients("claude, cursor,claude"); err != nil || !reflect.DeepEqual(got, []Client{Claude, Cursor}) {
		t.Errorf("a list = %v, %v", got, err)
	}
	if _, err := ParseClients("codex"); err == nil {
		t.Error("codex was accepted; init writes nothing for it")
	}
}

// The values init does not touch come back with the same text, big numbers included
// (the file is re-indented, and equal after parsing).
func TestUntouchedValuesKeepTheirText(t *testing.T) {
	root := projectRoot(t)
	write(t, root, ".mcp.json", `{"big": 12345678901234567890, "mcpServers": {"s": {"n": 1.50}}}`)
	mustRun(t, opts(root, Claude))
	got := read(t, root, ".mcp.json")
	if !bytes.Contains([]byte(got), []byte("12345678901234567890")) || !strings.Contains(got, "1.50") {
		t.Errorf("a number was rewritten: %s", got)
	}
}

// Cursor and OpenCode share the .agents skill. Uninstalling one
// leaves them while the other's droidyard entry is still in its file; uninstalling the
// last one takes them (ADR-0037 Decision 2: remove exactly what this run wrote, and not
// what another client still reads).
func TestUninstallKeepsWhatAnotherClientStillUses(t *testing.T) {
	sharedSkill := ".agents/skills/droidyard-android/SKILL.md"
	shared := []string{".agents/skills/droidyard-android/SKILL.md", ".agents/skills/droidyard-android/references/tools.md"}
	for _, order := range [][2]Client{{Cursor, OpenCode}, {OpenCode, Cursor}} {
		first, second := order[0], order[1]
		t.Run(string(first)+" then "+string(second), func(t *testing.T) {
			root := projectRoot(t)
			mustRun(t, opts(root, Cursor, OpenCode))
			res := mustRun(t, uninstall(opts(root, first)))
			for _, p := range shared {
				c := statusOf(t, res, p)
				if c.Status != Unchanged || !strings.Contains(notesOf(c), "kept: still used by "+string(second)) {
					t.Errorf("%s: %s (%s); want kept, still used by %s", p, c.Status, notesOf(c), second)
				}
				if !exists(root, p) {
					t.Errorf("uninstalling %s removed %s, which %s still uses", first, p, second)
				}
			}
			mustRun(t, uninstall(opts(root, second)))
			if left := snapshot(t, root); len(left) != 0 {
				t.Errorf("after both were uninstalled, %v is left", left)
			}
		})
	}

	t.Run("one run for both takes the shared files", func(t *testing.T) {
		root := projectRoot(t)
		mustRun(t, opts(root, Cursor, OpenCode))
		mustRun(t, uninstall(opts(root, Cursor, OpenCode)))
		if left := snapshot(t, root); len(left) != 0 {
			t.Errorf("left %v", left)
		}
	})

	t.Run("a client that was never configured does not hold them", func(t *testing.T) {
		root := projectRoot(t)
		mustRun(t, opts(root, Cursor))
		mustRun(t, uninstall(opts(root, Cursor)))
		if left := snapshot(t, root); len(left) != 0 {
			t.Errorf("left %v", left)
		}
	})

	t.Run("an unreadable entry file counts as in use", func(t *testing.T) {
		root := projectRoot(t)
		mustRun(t, opts(root, Cursor, OpenCode))
		write(t, root, "opencode.json", "{not json")
		mustRun(t, uninstall(opts(root, Cursor)))
		if !exists(root, sharedSkill) {
			t.Error("the shared skill was removed while opencode.json could not be read to say OpenCode is gone")
		}
	})

	t.Run("an opencode.jsonc counts as in use", func(t *testing.T) {
		root := projectRoot(t)
		mustRun(t, opts(root, Cursor))
		write(t, root, "opencode.jsonc", "{\n  // the user's own, edited by hand\n}\n")
		mustRun(t, uninstall(opts(root, Cursor)))
		if !exists(root, sharedSkill) {
			t.Error("the shared skill was removed while an opencode.jsonc may still configure OpenCode")
		}
	})

	t.Run("claude shares nothing", func(t *testing.T) {
		root := projectRoot(t)
		mustRun(t, opts(root, Cursor, Claude))
		mustRun(t, uninstall(opts(root, Claude)))
		if exists(root, ".claude/skills") || !exists(root, sharedSkill) || !exists(root, ".cursor/mcp.json") {
			t.Errorf("uninstalling claude left %v", snapshot(t, root))
		}
	})
}

// A path that is not a regular file is refused before it is opened (a FIFO would block
// the read forever), and so is a file too large to be a config.
func TestFilesThatAreNotRegularAreRefused(t *testing.T) {
	type place struct {
		rel     string
		clients []Client
	}
	places := []place{
		{".cursor/mcp.json", []Client{Cursor}},
		{".cursor/cli.json", []Client{Cursor}},
		{".mcp.json", []Client{Claude}},
		{".claude/settings.json", []Client{Claude}},
		{"opencode.json", []Client{OpenCode}},
		{"opencode.jsonc", []Client{OpenCode}},
		{".agents/skills/droidyard-android/SKILL.md", []Client{Cursor}},
		{".claude/skills/droidyard-android/SKILL.md", []Client{Claude}},
	}
	within := func(t *testing.T, f func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { defer close(done); f() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("init blocked on a file that is not a regular file")
		}
	}
	for _, pl := range places {
		for _, kind := range []string{"a FIFO", "a directory"} {
			for _, un := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s is %s uninstall=%v", pl.rel, kind, un), func(t *testing.T) {
					root := projectRoot(t)
					p := filepath.Join(root, filepath.FromSlash(pl.rel))
					os.MkdirAll(filepath.Dir(p), 0o755)
					if kind == "a FIFO" {
						if err := syscall.Mkfifo(p, 0o644); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Mkdir(p, 0o755); err != nil {
						t.Fatal(err)
					}
					o := opts(root, pl.clients...)
					o.Uninstall = un
					var res Result
					within(t, func() { res = mustRun(t, o) })
					if un && pl.rel == "opencode.jsonc" {
						return // uninstall never reads opencode.jsonc; it returned without blocking
					}
					c := statusOf(t, res, pl.rel)
					if c.Status != Refused {
						t.Errorf("%s: %s (%s); want refused", pl.rel, c.Status, notesOf(c))
					}
					if !un && pl.rel != "opencode.jsonc" && !strings.Contains(notesOf(c), "not a regular file") {
						t.Errorf("the refusal says %q; want it to say the file is not a regular file", notesOf(c))
					}
					fi, err := os.Lstat(p)
					if err != nil || fi.Mode().IsRegular() {
						t.Errorf("the %s was replaced: %v, %v", kind, fi, err)
					}
				})
			}
		}
	}

	t.Run("too large", func(t *testing.T) {
		root := projectRoot(t)
		big := strings.Repeat("x", maxFileSize+1)
		write(t, root, ".cursor/mcp.json", big)
		res := mustRun(t, opts(root, Cursor))
		if c := statusOf(t, res, ".cursor/mcp.json"); c.Status != Refused || !strings.Contains(notesOf(c), "larger than") {
			t.Errorf("a 1 MiB+ .cursor/mcp.json: %s (%s); want refused", c.Status, notesOf(c))
		}
		if read(t, root, ".cursor/mcp.json") != big {
			t.Error("the large file was changed")
		}
	})
}

// opencode.jsonc that cannot be resolved inside the root is refused, and opencode.json
// is not written beside it (it could be shadowing the real config).
func TestOpenCodeJSONCOutsideTheRootIsRefused(t *testing.T) {
	root, outside := projectRoot(t), projectRoot(t)
	target := filepath.Join(outside, "opencode.jsonc")
	os.WriteFile(target, []byte("{}"), 0o644)
	os.Symlink(target, filepath.Join(root, "opencode.jsonc"))
	res := mustRun(t, opts(root, OpenCode))
	if c := statusOf(t, res, "opencode.jsonc"); c.Status != Refused {
		t.Errorf("opencode.jsonc: %s; want refused", c.Status)
	}
	if exists(root, "opencode.json") {
		t.Error("opencode.json was written beside an opencode.jsonc init could not look at")
	}
}

// A duplicate key below the top level is said to be a duplicate key, on install and
// on uninstall (where it is not "nothing of ours here").
func TestNestedDuplicateKeyIsNamed(t *testing.T) {
	root := projectRoot(t)
	orig := `{"permissions": {"allow": ["a"], "allow": ["b"]}}`
	write(t, root, ".cursor/cli.json", orig)
	for _, un := range []bool{false, true} {
		o := opts(root, Cursor)
		o.Uninstall = un
		c := statusOf(t, mustRun(t, o), ".cursor/cli.json")
		if c.Status != Refused || !strings.Contains(notesOf(c), "duplicate key") || strings.Contains(notesOf(c), "not a JSON object") {
			t.Errorf("uninstall=%v: %s (%s); want refused for a duplicate key", un, c.Status, notesOf(c))
		}
		if read(t, root, ".cursor/cli.json") != orig {
			t.Error("the file was changed")
		}
	}
}

func TestClientNotes(t *testing.T) {
	if !strings.Contains(Note(Cursor), "cursor-agent mcp enable droidyard") || !strings.Contains(Note(Claude), "approve") {
		t.Errorf("notes: %q, %q", Note(Cursor), Note(Claude))
	}
	if Note(OpenCode) != "" {
		t.Errorf("OpenCode has a note: %q", Note(OpenCode))
	}
}

// Decision (row 199, ADR-0037 §Decision 2): a custom state root reaches Cursor's server
// through the entry's env, since Cursor rebuilds the environment; Claude Code's entry has
// none, and without a state root the entry is as before. Uninstall compares with what
// this run writes.
func TestCursorEntryCarriesACustomStateRoot(t *testing.T) {
	root := projectRoot(t)
	o := opts(root, Cursor, Claude)
	o.StateDir = "/s"
	mustRun(t, o)
	if env := serverEnv(t, root, ".cursor/mcp.json"); env["DROIDYARD_STATE_DIR"] != "/s" || len(env) != 1 {
		t.Errorf(".cursor/mcp.json env = %v; want only DROIDYARD_STATE_DIR=/s", env)
	}
	if got := read(t, root, ".mcp.json"); strings.Contains(got, "env") || strings.Contains(got, "DROIDYARD_STATE_DIR") {
		t.Errorf(".mcp.json carries an env:\n%s", got)
	}
	before := snapshot(t, root)
	res := mustRun(t, o)
	for _, c := range res.Changes {
		if c.Status != Unchanged {
			t.Errorf("second run: %s is %s", c.Path, c.Status)
		}
	}
	sameTree(t, before, snapshot(t, root))

	// Without the variable, uninstall keeps the entry written with it and says so.
	plain := opts(root, Cursor)
	c := statusOf(t, mustRun(t, uninstall(plain)), ".cursor/mcp.json")
	if c.Status != Unchanged || !strings.Contains(notesOf(c), "kept") {
		t.Errorf("uninstall without the state root: %s (%s); want unchanged, kept", c.Status, notesOf(c))
	}
	if !strings.Contains(read(t, root, ".cursor/mcp.json"), "DROIDYARD_STATE_DIR") {
		t.Error("uninstall without the state root removed an entry written with it")
	}

	// With it, uninstall removes the entry.
	u := uninstall(o)
	mustRun(t, u)
	if exists(root, ".cursor/mcp.json") && strings.Contains(read(t, root, ".cursor/mcp.json"), ServerName) {
		t.Error("uninstall with the state root left the entry")
	}

	// Without a state root, no env at all.
	root2 := projectRoot(t)
	mustRun(t, opts(root2, Cursor))
	if strings.Contains(read(t, root2, ".cursor/mcp.json"), "env") {
		t.Error("an entry written without a state root has an env")
	}
}

// serverEnv is the env of the droidyard entry in a client's server file.
func serverEnv(t *testing.T, root, rel string) map[string]string {
	t.Helper()
	var f struct {
		Servers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, root, rel)), &f); err != nil {
		t.Fatal(err)
	}
	return f.Servers[ServerName].Env
}
