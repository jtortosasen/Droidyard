package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

// ADR-0090 §1 (superseding ADR-0037 §2 in part): an entry of init's shape written for
// another binary path is init's, so --uninstall removes it, and the file init made
// goes with it.
func TestUninstallRemovesStaleEntries(t *testing.T) {
	for _, c := range hookCases {
		t.Run(string(c.client), func(t *testing.T) {
			root := projectRoot(t)
			old := opts(root, c.client)
			old.Command = "/old/bin/droidyard"
			mustRun(t, old)
			res := mustRun(t, uninstall(opts(root, c.client)))
			if exists(root, c.rel) {
				t.Errorf("--uninstall left the stale entry in a file init made:\n%s", read(t, root, c.rel))
			}
			if n := notesOf(statusOf(t, res, c.rel)); !strings.Contains(n, "stale entr") {
				t.Errorf("notes do not say stale entries were removed: %s", n)
			}

			// beside the user's own hook, only theirs remains
			write(t, root, c.rel, c.only)
			mustRun(t, old)
			mustRun(t, uninstall(opts(root, c.client)))
			if got := read(t, root, c.rel); strings.Contains(got, "droidyard") || !strings.Contains(got, `"command": "mine"`) {
				t.Errorf("after --uninstall: %s; want the stale entry gone and the user's hook kept", got)
			}
		})
	}
}

// ADR-0090 §1: a later init replaces an entry written for another binary path (or with
// the state root inline) instead of appending a second beside it.
func TestStaleHookEntryIsReplaced(t *testing.T) {
	for _, c := range hookCases {
		t.Run(string(c.client), func(t *testing.T) {
			root := projectRoot(t)
			write(t, root, c.rel, c.mine)
			old := opts(root, c.client)
			old.Command = "/old/droidyard"
			mustRun(t, old)
			withState := old
			withState.StateDir = "/old/state"
			mustRun(t, withState) // already replaces old's; the prefixed form is what is on disk now

			now := opts(root, c.client)
			res := mustRun(t, now)
			if n := notesOf(statusOf(t, res, c.rel)); !strings.Contains(n, "replaced") {
				t.Errorf("notes do not say an entry was replaced: %s", n)
			}
			got := read(t, root, c.rel)
			if n := strings.Count(got, "hook --client "+string(c.client)); n != c.entries {
				t.Errorf("%d droidyard entries after the replacing init; want %d:\n%s", n, c.entries, got)
			}
			if strings.Contains(got, "/old/") {
				t.Errorf("a stale entry is left:\n%s", got)
			}
			if !strings.Contains(got, `"command": "mine"`) {
				t.Errorf("the user's entry is gone:\n%s", got)
			}
			if st := statusOf(t, mustRun(t, now), c.rel).Status; st != Unchanged {
				t.Errorf("a repeated init is %s; want unchanged", st)
			}

			// --dry-run reports the replacement and writes nothing
			older := opts(root, c.client)
			older.Command = "/older/droidyard"
			mustRun(t, older)
			before := snapshot(t, root)
			dry := now
			dry.DryRun = true
			if n := notesOf(statusOf(t, mustRun(t, dry), c.rel)); !strings.Contains(n, "replaced") {
				t.Errorf("--dry-run does not show the replacement: %s", n)
			}
			sameText(t, before, snapshot(t, root))
		})
	}
}

// ADR-0090 §1: the shape test matches only init's command. An entry for another root or
// client, a wrapper, another base name or an extra word is the user's, and survives
// both install and uninstall.
func TestHookEntriesThatAreNotInitsAreKept(t *testing.T) {
	root := projectRoot(t)
	claude := func(cmd string) string {
		return `{"matcher": "Bash", "hooks": [{"type": "command", "command": ` + string(rawJSON(cmd)) + `}]}`
	}
	cursor := func(cmd string) string { return `{"command": ` + string(rawJSON(cmd)) + `}` }
	foreign := []string{
		"/opt/bin/droidyard hook --client claude --root /other/root",
		"/opt/bin/droidyard hook --client cursor --root " + root,
		"sh -c '/opt/bin/droidyard hook --client claude --root " + root + "'",
		"/opt/bin/droidyard-wrapper hook --client claude --root " + root,
		"/opt/bin/droidyard hook --client claude --root " + root + " --verbose",
		"droidyard hook --client claude --root " + root,
		"DROIDYARD_STATE_DIR=rel /opt/bin/droidyard hook --client claude --root " + root,
		"/opt/bin/droidyard hook --client claude --root " + root + " && true",
	}
	var cl, cu []string
	for i, cmd := range foreign {
		cl = append(cl, claude(cmd))
		if i == 1 {
			continue // for Cursor this is init's own command
		}
		cu = append(cu, cursor(strings.ReplaceAll(cmd, "--client claude", "--client cursor")))
	}
	// a Cursor element for the other client, and one whose matcher init does not write
	// for the event
	cu = append(cu, cursor("/opt/bin/droidyard hook --client claude --root "+root),
		`{"command": "/old/droidyard hook --client cursor --root `+root+`", "matcher": "Write"}`)
	// a Claude element with another matcher, and one with an extra key
	cl = append(cl, `{"matcher": "Edit", "hooks": [{"type": "command", "command": "/old/droidyard hook --client claude --root `+root+`"}]}`,
		`{"matcher": "Bash", "timeout": 5, "hooks": [{"type": "command", "command": "/old/droidyard hook --client claude --root `+root+`"}]}`)
	claudeFile := `{"hooks": {"PreToolUse": [` + strings.Join(cl, ",") + `], "PostToolUse": [` + strings.Join(cl, ",") + `], "PostToolUseFailure": [` + strings.Join(cl, ",") + `]}}`
	cursorFile := `{"version": 1, "hooks": {"beforeShellExecution": [` + strings.Join(cu, ",") + `], "preToolUse": [` + strings.Join(cu, ",") + `], "postToolUse": [` + strings.Join(cu, ",") + `], "postToolUseFailure": [` + strings.Join(cu, ",") + `]}}`
	count := func(t *testing.T, rel string) map[string]int {
		var doc struct {
			Hooks map[string][]json.RawMessage `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(read(t, root, rel)), &doc); err != nil {
			t.Fatal(err)
		}
		n := map[string]int{}
		for ev, list := range doc.Hooks {
			n[ev] = len(list)
		}
		return n
	}
	for _, tc := range []struct {
		c    Client
		rel  string
		file string
		n    int
	}{{Claude, ".claude/settings.json", claudeFile, len(cl)}, {Cursor, ".cursor/hooks.json", cursorFile, len(cu)}} {
		t.Run(string(tc.c), func(t *testing.T) {
			write(t, root, tc.rel, tc.file)
			o := opts(root, tc.c)
			mustRun(t, o)
			for ev, n := range count(t, tc.rel) {
				if n != tc.n+1 {
					t.Errorf("%s holds %d elements after init; want the user's %d and one of init's", ev, n, tc.n)
				}
			}
			mustRun(t, uninstall(o))
			after := count(t, tc.rel)
			if len(after) == 0 {
				t.Fatal("the user's hooks are gone")
			}
			for ev, n := range after {
				if n != tc.n {
					t.Errorf("%s holds %d elements after --uninstall; want the user's %d", ev, n, tc.n)
				}
			}
		})
	}
}

// ADR-0090 §1: the shape accepts the state-root prefix and a quoted path, and nothing
// that merely resembles it.
func TestInitHookShape(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/a/droidyard hook --client claude --root /p":                                  true,
		"DROIDYARD_STATE_DIR=/s /a/droidyard hook --client claude --root /p":           true,
		`DROIDYARD_STATE_DIR='/s x' '/a b/droidyard' hook --client claude --root '/p'`: true,
		"/a/droidyard hook --client cursor --root /p":                                  false,
		"/a/droidyard hook --client claude --root /q":                                  false,
		"/a/other hook --client claude --root /p":                                      false,
		"a/droidyard hook --client claude --root /p":                                   false,
		"/a/droidyard hook --client claude --root /p extra":                            false,
		"FOO=1 /a/droidyard hook --client claude --root /p":                            false,
		"/a/droidyard hook --client claude --root $HOME":                               false,
		"": false,
	} {
		if got := initHookShape(cmd, Claude, "/p"); got != want {
			t.Errorf("initHookShape(%q) = %v; want %v", cmd, got, want)
		}
	}
}

// ADR-0090 §2: allow rules for the retired tools go with init and with --uninstall; a
// user's rule that merely ends in the same tool name stays.
func TestRetiredAllowRulesGo(t *testing.T) {
	for _, tc := range []struct {
		c         Client
		rel, file string
		retired   []string
		mine      string
	}{
		{Claude, ".claude/settings.json",
			`{"permissions": {"allow": ["mcp__droidyard__file_list", "mcp__droidyard__file_read", "mcp__other__file_list", "Bash(ls)"]}}`,
			[]string{"mcp__droidyard__file_list", "mcp__droidyard__file_read"}, "mcp__other__file_list"},
		{Cursor, ".cursor/cli.json",
			`{"permissions": {"allow": ["Mcp(droidyard:file_list)", "Mcp(droidyard:file_read)", "Mcp(other:file_list)", "Shell(ls)"], "deny": []}}`,
			[]string{"Mcp(droidyard:file_list)", "Mcp(droidyard:file_read)"}, "Mcp(other:file_list)"},
	} {
		t.Run(string(tc.c), func(t *testing.T) {
			root := projectRoot(t)
			write(t, root, tc.rel, tc.file)
			o := opts(root, tc.c)

			dry := o
			dry.DryRun = true
			if n := notesOf(statusOf(t, mustRun(t, dry), tc.rel)); !strings.Contains(n, "stale entries") {
				t.Errorf("--dry-run does not show the removal: %s", n)
			}
			if got := read(t, root, tc.rel); got != tc.file {
				t.Errorf("--dry-run wrote:\n%s", got)
			}

			mustRun(t, o)
			got := read(t, root, tc.rel)
			for _, r := range tc.retired {
				if strings.Contains(got, r) {
					t.Errorf("init left %s:\n%s", r, got)
				}
			}
			if !strings.Contains(got, tc.mine) {
				t.Errorf("init removed the user's %s:\n%s", tc.mine, got)
			}

			// a file as an earlier init left it: --uninstall takes the retired rules too
			write(t, root, tc.rel, tc.file)
			mustRun(t, uninstall(o))
			got = read(t, root, tc.rel)
			for _, r := range tc.retired {
				if strings.Contains(got, r) {
					t.Errorf("--uninstall left %s:\n%s", r, got)
				}
			}
			if !strings.Contains(got, tc.mine) {
				t.Errorf("--uninstall removed the user's %s:\n%s", tc.mine, got)
			}
		})
	}
}
