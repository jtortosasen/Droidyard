package assistant

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// hookCase is one client's hook file, with a user's own hooks already in it.
type hookCase struct {
	client  Client
	rel     string
	mine    string // the user's file: their own hooks beside other keys
	only    string // the user's file with nothing of init's in the way: just their hooks
	entries int    // how many elements init adds to it
}

var hookCases = []hookCase{
	{Claude, ".claude/settings.json",
		`{"env": {"A": "b"}, "hooks": {"PreToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "mine"}]}], "PostToolUse": [{"hooks": [{"type": "command", "command": "after"}]}]}}`,
		`{"hooks": {"PreToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "mine"}]}]}}`, 5},
	{Cursor, ".cursor/hooks.json",
		`{"version": 1, "hooks": {"beforeShellExecution": [{"command": "mine"}], "preToolUse": [{"command": "mine", "matcher": "Write"}], "afterFileEdit": [{"command": "after"}], "postToolUse": [{"command": "after", "matcher": "Shell"}]}}`,
		`{"version": 1, "hooks": {"beforeShellExecution": [{"command": "mine"}]}}`, 6},
}

// ADR-0061 §5, ADR-0063 §7: init writes the hook entries (before a shell command, and
// after it, on success and on failure) into the project's file, the second run
// writes nothing (the array merge compares elements, so a duplicate would show here),
// the user's own entries and keys survive both runs and --uninstall, and --uninstall
// puts the file back as it was.
func TestInitWritesTheHookEntry(t *testing.T) {
	for _, c := range hookCases {
		t.Run(string(c.client), func(t *testing.T) {
			root := projectRoot(t)
			write(t, root, c.rel, c.mine)
			before := snapshot(t, root)
			o := opts(root, c.client)

			first := statusOf(t, mustRun(t, o), c.rel)
			if first.Status != Updated {
				t.Fatalf("first run: %s (%s); want updated", first.Status, notesOf(first))
			}
			afterFirst := snapshot(t, root)
			second := statusOf(t, mustRun(t, o), c.rel)
			if second.Status != Unchanged {
				t.Errorf("second run: %s (%s); want unchanged", second.Status, notesOf(second))
			}
			sameText(t, afterFirst, snapshot(t, root))

			got := read(t, root, c.rel)
			if n := strings.Count(got, "hook --client "+string(c.client)); n != c.entries {
				t.Errorf("%s holds init's hook entry %d times; want %d:\n%s", c.rel, n, c.entries, got)
			}
			if !strings.Contains(got, `"command": "mine"`) || !strings.Contains(got, `"command": "after"`) {
				t.Errorf("a user's entry is gone after init:\n%s", got)
			}

			mustRun(t, uninstall(o))
			sameTree(t, before, snapshot(t, root))
		})
	}

	t.Run("a file init creates is deleted by --uninstall", func(t *testing.T) {
		for _, c := range hookCases {
			root := projectRoot(t)
			o := opts(root, c.client)
			if st := statusOf(t, mustRun(t, o), c.rel).Status; st != Created {
				t.Errorf("%s: first run %s; want created", c.client, st)
			}
			mustRun(t, uninstall(o))
			if exists(root, c.rel) {
				t.Errorf("%s is left after --uninstall of a file init created:\n%s", c.rel, read(t, root, c.rel))
			}
		}
	})
}

// The entries' shape is each client's own format (ADR-0061 §5, ADR-0063 §7, ADR-0095 §3):
// Claude Code's are a Bash matcher on PreToolUse, PostToolUse and PostToolUseFailure, and
// the edit tools' matcher on PreToolUse and PostToolUse; Cursor's cover a shell command
// before it runs, its Delete and Write tools by matcher before they run, and a shell command
// and a Write after them, on success and, for a shell command, on failure, by the matcher
// (the tool name its events carry). A postToolUse entry without the matcher would run on
// every Cursor tool; one with another matcher would never see a shell command or a write.
func TestHookEntryShape(t *testing.T) {
	root := projectRoot(t)
	mustRun(t, opts(root, Claude, Cursor))

	var claude struct {
		Hooks map[string][]struct {
			Matcher string
			Hooks   []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal([]byte(read(t, root, ".claude/settings.json")), &claude); err != nil {
		t.Fatal(err)
	}
	want := testCommand + " hook --client claude --root " + root
	if len(claude.Hooks) != 3 {
		t.Errorf("claude hooks = %+v; want PreToolUse, PostToolUse and PostToolUseFailure", claude.Hooks)
	}
	for event, matchers := range map[string][]string{
		"PreToolUse": {"Bash", "Write|Edit|NotebookEdit"}, "PostToolUse": {"Bash", "Write|Edit|NotebookEdit"}, "PostToolUseFailure": {"Bash"},
	} {
		e := claude.Hooks[event]
		if len(e) != len(matchers) {
			t.Errorf("claude %s = %+v; want the matchers %v", event, e, matchers)
			continue
		}
		for i, m := range matchers {
			if e[i].Matcher != m || len(e[i].Hooks) != 1 || e[i].Hooks[0].Type != "command" || e[i].Hooks[0].Command != want {
				t.Errorf("claude %s[%d] = %+v; want matcher %q running %q", event, i, e[i], m, want)
			}
		}
	}

	var cursor struct {
		Version float64
		Hooks   map[string][]struct{ Command, Matcher string }
	}
	if err := json.Unmarshal([]byte(read(t, root, ".cursor/hooks.json")), &cursor); err != nil {
		t.Fatal(err)
	}
	want = testCommand + " hook --client cursor --root " + root
	if cursor.Version != 1 || len(cursor.Hooks) != 4 {
		t.Errorf("cursor hooks.json = %+v; want version 1 and four events", cursor)
	}
	for event, matchers := range map[string][]string{
		"beforeShellExecution": {""}, "preToolUse": {"Delete", "Write"}, "postToolUse": {"Shell", "Write"}, "postToolUseFailure": {"Shell"},
	} {
		e := cursor.Hooks[event]
		if len(e) != len(matchers) {
			t.Errorf("cursor %s = %+v; want the matchers %q", event, e, matchers)
			continue
		}
		for i, m := range matchers {
			if e[i].Command != want || e[i].Matcher != m {
				t.Errorf("cursor %s[%d] = %+v; want matcher %q running %q", event, i, e[i], m, want)
			}
		}
	}

	// OpenCode has no hook (a register row), so its run writes neither file.
	other := projectRoot(t)
	mustRun(t, opts(other, OpenCode))
	for _, rel := range []string{".claude/settings.json", ".cursor/hooks.json"} {
		if exists(other, rel) {
			t.Errorf("an OpenCode run wrote %s", rel)
		}
	}
}

// Cursor's `version` is init's only while nothing else is in the file: a user who has
// hooks of their own keeps it through --uninstall (a removed version would make Cursor
// drop their file), and a file of init's alone is deleted whole.
func TestUninstallKeepsCursorsVersionWhileTheUsersHooksRemain(t *testing.T) {
	root := projectRoot(t)
	write(t, root, ".cursor/hooks.json", hookCases[1].only)
	o := opts(root, Cursor)
	mustRun(t, o)
	mustRun(t, uninstall(o))
	got := read(t, root, ".cursor/hooks.json")
	if !strings.Contains(got, `"version": 1`) || !strings.Contains(got, `"command": "mine"`) || strings.Contains(got, "droidyard") {
		t.Errorf("after --uninstall: %s; want version and the user's hook kept, init's gone", got)
	}
}

// A custom state root travels in the command (the client's hook environment lacks it),
// and any path quotes to one shell word.
func TestHookCommandCarriesTheStateRootAndQuotesPaths(t *testing.T) {
	o := Options{Root: "/p/my app", Command: "/b/droidyard", StateDir: "/s/it's here"}
	got := HookCommand(o, Cursor)
	want := `DROIDYARD_STATE_DIR='/s/it'\''s here' /b/droidyard hook --client cursor --root '/p/my app'`
	if got != want {
		t.Errorf("HookCommand = %s\nwant %s", got, want)
	}
	if plain := HookCommand(Options{Root: "/p/app", Command: "/b/droidyard"}, Claude); strings.Contains(plain, "DROIDYARD_STATE_DIR") {
		t.Errorf("a default state root is written inline: %s", plain)
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		for _, s := range []string{"/p/my app", "it's", `a"b$c`, "", "x\ny"} {
			out, err := exec.Command(sh, "-c", "printf %s "+shellArg(s)).Output()
			if err != nil || string(out) != s {
				t.Errorf("shellArg(%q) read back as %q (%v)", s, out, err)
			}
		}
	}

	root := projectRoot(t)
	o = opts(root, Claude)
	o.StateDir = "/s/state"
	mustRun(t, o)
	if got := read(t, root, ".claude/settings.json"); !strings.Contains(got, "DROIDYARD_STATE_DIR=/s/state /opt/bin/droidyard hook") {
		t.Errorf("settings.json lacks the state root:\n%s", got)
	}
	o.StateDir = "state"
	if _, err := Run(o); err == nil {
		t.Error("a relative state dir was accepted")
	}
}

// A file init cannot merge into is refused and left as it is (ADR-0037 §2), and the
// other files are still written; --dry-run computes the same outcome and writes none.
func TestHookFilesThatCannotBeMergedAreRefused(t *testing.T) {
	cases := []struct {
		hookCase
		bad string
	}{
		{hookCases[0], `{"hooks": {"PreToolUse": `},
		{hookCases[0], `{"hooks": {"PreToolUse": {"matcher": "Bash"}}}`},
		{hookCases[1], `{"version": 1, "hooks": [`},
		{hookCases[1], `{"version": 1, "hooks": {"preToolUse": "x"}}`},
	}
	for _, c := range cases {
		t.Run(string(c.client)+" "+c.bad, func(t *testing.T) {
			root := projectRoot(t)
			write(t, root, c.rel, c.bad)
			res := mustRun(t, opts(root, c.client))
			ch := statusOf(t, res, c.rel)
			if ch.Status != Refused || !strings.Contains(ch.Entry, "hook --client") {
				t.Errorf("%s: %s, entry %q; want refused with the entry to add", c.rel, ch.Status, ch.Entry)
			}
			if got := read(t, root, c.rel); got != c.bad {
				t.Errorf("a refused file was changed: %q", got)
			}
			mustRun(t, uninstall(opts(root, c.client)))
			if got := read(t, root, c.rel); got != c.bad {
				t.Errorf("--uninstall changed a file it cannot parse: %q", got)
			}
		})
	}
}

func TestHookEntriesAreNotWrittenByADryRun(t *testing.T) {
	for _, c := range hookCases {
		root := projectRoot(t)
		write(t, root, c.rel, c.mine)
		before := snapshot(t, root)
		o := opts(root, c.client)
		o.DryRun = true
		if st := statusOf(t, mustRun(t, o), c.rel).Status; st != Updated {
			t.Errorf("%s dry run reports %s; want updated", c.rel, st)
		}
		sameText(t, before, snapshot(t, root))
	}
	root := projectRoot(t)
	o := opts(root, Cursor, Claude)
	o.DryRun = true
	mustRun(t, o)
	if left := snapshot(t, root); len(left) != 0 {
		t.Errorf("a dry run wrote %v", left)
	}
}
