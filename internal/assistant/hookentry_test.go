package assistant

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// legacyHook is the command line the init of before ADR-0137 wrote into a client's hook
// entry: the absolute binary, `hook --client <c>`, --root unless the run was user-level,
// and the state root inline when init's came from DROIDYARD_STATE_DIR.
func legacyHook(c Client, bin, root, state string) string {
	cmd := ""
	if state != "" {
		cmd = "DROIDYARD_STATE_DIR=" + shellArg(state) + " "
	}
	cmd += shellArg(bin) + " hook --client " + string(c)
	if root != "" {
		cmd += " --root " + shellArg(root)
	}
	return cmd
}

// legacyClaudeHooks is the "hooks" object an earlier init wrote into .claude/settings.json,
// as JSON, with the user's own elements added to PreToolUse and PostToolUse when given.
func legacyClaudeHooks(cmd string, mine ...string) string {
	hc := []claudeHookCommand{{Type: "command", Command: cmd}}
	bash := string(rawJSON(claudeHookEntry{Matcher: "Bash", Hooks: hc}))
	edit := string(rawJSON(claudeHookEntry{Matcher: claudeEditMatcher, Hooks: hc}))
	list := func(els ...string) string {
		return "[" + strings.Join(append(append([]string(nil), els...), mine...), ",") + "]"
	}
	return `{"PreToolUse":` + list(bash, edit) + `,"PostToolUse":` + list(bash, edit) + `,"PostToolUseFailure":` + `[` + bash + `]}`
}

// legacyCursorHooks is the same for .cursor/hooks.json's "hooks" object.
func legacyCursorHooks(cmd string, mine ...string) string {
	el := func(matcher string) string { return string(rawJSON(cursorHookEntry{Command: cmd, Matcher: matcher})) }
	list := func(els ...string) string {
		return "[" + strings.Join(append(append([]string(nil), els...), mine...), ",") + "]"
	}
	return `{"beforeShellExecution":` + list(el("")) + `,"preToolUse":` + list(el("Delete"), el("Write")) +
		`,"postToolUse":` + list(el("Shell"), el("Write")) + `,"postToolUseFailure":[` + el("Shell") + `]}`
}

// hookCase is one client's hook file as an earlier init left it beside a user's own
// hooks and other keys.
type hookCase struct {
	client Client
	rel    string
	// file is the user's file with init's entries (for root, binary old) in it.
	file func(root, bin, state string) string
	// mine is the same file with only the user's part.
	mine string
}

const (
	userClaudeEntry = `{"matcher": "Edit", "hooks": [{"type": "command", "command": "mine"}]}`
	userCursorEntry = `{"command": "mine", "matcher": "Write"}`
)

var hookCases = []hookCase{
	{Claude, ".claude/settings.json",
		func(root, bin, state string) string {
			return `{"env": {"A": "b"}, "hooks": ` + legacyClaudeHooks(legacyHook(Claude, bin, root, state), userClaudeEntry) + `}`
		},
		`{"env": {"A": "b"}, "hooks": {"PreToolUse": [` + userClaudeEntry + `], "PostToolUse": [` + userClaudeEntry + `]}}`},
	{Cursor, ".cursor/hooks.json",
		func(root, bin, state string) string {
			return `{"version": 1, "hooks": ` + legacyCursorHooks(legacyHook(Cursor, bin, root, state), userCursorEntry) + `}`
		},
		`{"version": 1, "hooks": {"preToolUse": [` + userCursorEntry + `], "postToolUse": [` + userCursorEntry + `], "beforeShellExecution": [` + userCursorEntry + `], "postToolUseFailure": [` + userCursorEntry + `]}}`},
}

func parsed(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return v
}

// ADR-0137 §1, plan m3j Q4: init writes no hook entry. On a fresh project and on one that
// already holds the user's own hooks, no entry that runs `droidyard hook` appears, and
// Cursor's hooks.json is not created. Seen failing: with the hook writers kept (the
// pre-step claudeHookArrays and cursorHookSpec), the fresh runs created hooks.json and put
// "hook --client" into settings.json.
func TestInitWritesNoHook(t *testing.T) {
	root := projectRoot(t)
	res := mustRun(t, opts(root, Claude, Cursor, OpenCode))
	if exists(root, ".cursor/hooks.json") {
		t.Errorf("init created .cursor/hooks.json:\n%s", read(t, root, ".cursor/hooks.json"))
	}
	if got := read(t, root, ".claude/settings.json"); strings.Contains(got, "hook") {
		t.Errorf("settings.json names a hook:\n%s", got)
	}
	for _, c := range res.Changes {
		if strings.Contains(c.Path, "hooks.json") && c.Status != Unchanged {
			t.Errorf("init reports a change to %s: %s", c.Path, c.Status)
		}
	}

	for _, c := range hookCases {
		root := projectRoot(t)
		write(t, root, c.rel, c.mine)
		mustRun(t, opts(root, c.client))
		// The user's hooks are as they were (settings.json also gains the allowlist).
		var want, got struct {
			Hooks json.RawMessage `json:"hooks"`
		}
		if json.Unmarshal([]byte(c.mine), &want) != nil || json.Unmarshal([]byte(read(t, root, c.rel)), &got) != nil || !equalJSON(want.Hooks, got.Hooks) {
			t.Errorf("%s: the user's hooks changed:\n%s", c.rel, read(t, root, c.rel))
		}
	}
}

// ADR-0137 §1, plan m3j Q4, ADR-0037's uninstall rules: init, install or uninstall, removes
// the hook entries an earlier init wrote, init's own only. The user's elements beside them
// and every other key stay; the removal is reported in the change's notes; a second run
// finds nothing; --dry-run reports and writes nothing. The earlier entries are of any
// binary path and with the state root inline. Seen failing: with the cleanup left out of
// the install path, the file was Unchanged and kept the entries.
func TestInitRemovesItsHooksFromTheProject(t *testing.T) {
	for _, c := range hookCases {
		for _, u := range []bool{false, true} {
			name := string(c.client) + " install"
			if u {
				name = string(c.client) + " uninstall"
			}
			t.Run(name, func(t *testing.T) {
				root := projectRoot(t)
				write(t, root, c.rel, c.file(root, "/old/bin/droidyard", "/old/state"))
				o := opts(root, c.client)
				if u {
					o = uninstall(o)
				}

				dry := o
				dry.DryRun = true
				ch := statusOf(t, mustRun(t, dry), c.rel)
				if !strings.Contains(notesOf(ch), "removed") {
					t.Errorf("--dry-run notes %q do not report the removal", notesOf(ch))
				}
				if got := read(t, root, c.rel); got != c.file(root, "/old/bin/droidyard", "/old/state") {
					t.Errorf("--dry-run changed the file:\n%s", got)
				}

				ch = statusOf(t, mustRun(t, o), c.rel)
				if ch.Status != Updated && ch.Status != Removed {
					t.Errorf("status %s (%s); want the file rewritten with the user's part", ch.Status, notesOf(ch))
				}
				if !strings.Contains(notesOf(ch), "removed") {
					t.Errorf("notes %q do not report the removal", notesOf(ch))
				}
				got := read(t, root, c.rel)
				if strings.Contains(got, "hook --client") {
					t.Errorf("init's hook entries are left:\n%s", got)
				}
				if !strings.Contains(got, `"command": "mine"`) {
					t.Errorf("the user's own hook is gone:\n%s", got)
				}
				if c.client == Claude && !strings.Contains(got, `"env"`) {
					t.Errorf("a key of the user's is gone:\n%s", got)
				}
				if c.client == Cursor && !strings.Contains(got, `"version": 1`) {
					t.Errorf("cursor's version is gone while the user's hooks remain:\n%s", got)
				}
				if st := statusOf(t, mustRun(t, o), c.rel).Status; st != Unchanged {
					t.Errorf("second run is %s; want unchanged", st)
				}
			})
		}
	}

	t.Run("a Cursor file holding only init's entries is deleted", func(t *testing.T) {
		root := projectRoot(t)
		write(t, root, ".cursor/hooks.json", `{"version": 1, "hooks": `+legacyCursorHooks(legacyHook(Cursor, testCommand, root, ""))+`}`)
		ch := statusOf(t, mustRun(t, opts(root, Cursor)), ".cursor/hooks.json")
		if ch.Status != Deleted || exists(root, ".cursor/hooks.json") {
			t.Errorf("hooks.json: %s, exists %v; want deleted", ch.Status, exists(root, ".cursor/hooks.json"))
		}
	})

	t.Run("Claude's settings keep the allowlist and lose the emptied hooks object", func(t *testing.T) {
		root := projectRoot(t)
		write(t, root, ".claude/settings.json", `{"hooks": `+legacyClaudeHooks(legacyHook(Claude, testCommand, root, ""))+`}`)
		mustRun(t, opts(root, Claude))
		got := read(t, root, ".claude/settings.json")
		if strings.Contains(got, "hooks") || !strings.Contains(got, "mcp__droidyard__device_list") {
			t.Errorf("settings.json after init:\n%s", got)
		}
	})
}

// ADR-0090 §1 and ADR-0137: an entry for another root or client, a wrapper or an extra word
// is the user's and survives the cleanup of an install and of an uninstall; the file the
// user's elements are in is not rewritten for them.
func TestInitKeepsHookEntriesThatAreNotItsOwn(t *testing.T) {
	root := projectRoot(t)
	other := legacyHook(Claude, testCommand, "/some/other/root", "")
	wrapped := "sh -c '" + legacyHook(Claude, testCommand, root, "") + "'"
	claudeFile := `{"hooks": ` + legacyClaudeHooks(other, `{"matcher": "Bash", "hooks": [{"type": "command", "command": `+string(rawJSON(wrapped))+`}]}`) + `}`
	cursorOther := legacyHook(Cursor, testCommand, "/some/other/root", "")
	cursorFile := `{"version": 1, "hooks": ` + legacyCursorHooks(cursorOther) + `}`
	for _, tc := range []struct {
		c    Client
		rel  string
		file string
	}{{Claude, ".claude/settings.json", claudeFile}, {Cursor, ".cursor/hooks.json", cursorFile}} {
		t.Run(string(tc.c), func(t *testing.T) {
			write(t, root, tc.rel, tc.file)
			o := opts(root, tc.c)
			mustRun(t, o)
			mustRun(t, uninstall(o))
			got := read(t, root, tc.rel)
			for _, must := range []string{"/some/other/root"} {
				if !strings.Contains(got, must) {
					t.Errorf("a user's hook entry (%s) is gone:\n%s", must, got)
				}
			}
			if tc.c == Claude && !strings.Contains(got, "sh -c") {
				t.Errorf("the user's wrapped hook is gone:\n%s", got)
			}
		})
	}
}

// A file init cannot parse is refused and left as it is (ADR-0037 §2): Cursor's hooks.json,
// which init only cleans, is refused with the reason and not written, and so is an
// uninstall; a hook array that is not an array is the user's and left alone.
func TestHookFilesThatCannotBeParsedAreLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		hookCase
		bad     string
		refused bool
	}{
		{hookCases[0], `{"hooks": {"PreToolUse": `, true},
		{hookCases[0], `{"hooks": {"PreToolUse": {"matcher": "Bash"}}}`, false},
		{hookCases[1], `{"version": 1, "hooks": [`, true},
		{hookCases[1], `{"version": 1, "hooks": {"preToolUse": "x"}}`, false},
	} {
		t.Run(string(tc.client)+" "+tc.bad, func(t *testing.T) {
			root := projectRoot(t)
			write(t, root, tc.rel, tc.bad)
			ch := statusOf(t, mustRun(t, opts(root, tc.client)), tc.rel)
			if tc.refused && ch.Status != Refused {
				t.Errorf("%s: %s (%s); want refused", tc.rel, ch.Status, notesOf(ch))
			}
			// A refused file is as it was; a parsable one may gain init's allowlist, and its
			// "hooks" are as the user wrote them.
			same := func(what string) {
				got := read(t, root, tc.rel)
				var want, have struct {
					Hooks json.RawMessage `json:"hooks"`
				}
				if tc.refused && got != tc.bad {
					t.Errorf("%s changed a file it cannot parse: %q", what, got)
				} else if !tc.refused && (json.Unmarshal([]byte(tc.bad), &want) != nil || json.Unmarshal([]byte(got), &have) != nil || !equalJSON(want.Hooks, have.Hooks)) {
					t.Errorf("%s changed the user's hooks: %q", what, got)
				}
			}
			same("init")
			mustRun(t, uninstall(opts(root, tc.client)))
			same("--uninstall")
		})
	}
}

// Any path quotes to one shell word, the way the stale test's fixtures and the Claude
// `mcp add-json` command line are built.
func TestShellArgQuotesPaths(t *testing.T) {
	if sh, err := exec.LookPath("sh"); err == nil {
		for _, s := range []string{"/p/my app", "it's", `a"b$c`, "", "x\ny"} {
			out, err := exec.Command(sh, "-c", "printf %s "+shellArg(s)).Output()
			if err != nil || string(out) != s {
				t.Errorf("shellArg(%q) read back as %q (%v)", s, out, err)
			}
		}
	}
	if got, want := legacyHook(Cursor, "/b/droidyard", "/p/my app", "/s/it's here"),
		`DROIDYARD_STATE_DIR='/s/it'\''s here' /b/droidyard hook --client cursor --root '/p/my app'`; got != want {
		t.Errorf("legacyHook = %s\nwant %s", got, want)
	}
}

// ADR-0090 §1 extended by ADR-0124: one parse of a hook command into its client, its root
// (optional) and its state word, shared by the per-project and the user-level runs. Init's
// shape is an absolute droidyard binary, `hook --client <c>` for a client with a hook, an
// optional `--root <absolute>` and an optional absolute DROIDYARD_STATE_DIR word first;
// everything else is a user's. The rootless and rooted shapes are told apart, which is what
// keeps a user-level cleanup from taking a user's `--root /p` entry for init's.
func TestParseHookCommand(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    hookShape
		ok      bool
	}{
		{"/a/droidyard hook --client claude", hookShape{client: Claude}, true},
		{"/a/droidyard hook --client cursor --root /p", hookShape{client: Cursor, root: "/p", hasRoot: true}, true},
		{"DROIDYARD_STATE_DIR=/s /a/droidyard hook --client claude", hookShape{client: Claude, state: "/s", hasState: true}, true},
		{"DROIDYARD_STATE_DIR='/s t' '/a b/droidyard' hook --client cursor --root '/p q'", hookShape{client: Cursor, root: "/p q", hasRoot: true, state: "/s t", hasState: true}, true},
		{"/a/droidyard hook --client opencode", hookShape{}, false},        // no hook for the client
		{"/a/droidyard hook --client claude --root p", hookShape{}, false}, // a relative root
		{"/a/droidyard hook --client claude --root", hookShape{}, false},
		{"/a/droidyard hook --client claude --from /p", hookShape{}, false},
		{"/a/droidyard hook --client claude --extra", hookShape{}, false},
		{"/a/droidyard hook --client claude --root /p --extra", hookShape{}, false},
		{"DROIDYARD_STATE_DIR=s /a/droidyard hook --client claude", hookShape{}, false}, // a relative state root
		{"droidyard hook --client claude", hookShape{}, false},                          // not an absolute binary
		{"/a/other hook --client claude", hookShape{}, false},
		{"sh -c '/a/droidyard hook --client claude'", hookShape{}, false},
		{"/a/droidyard mcp", hookShape{}, false},
		{"", hookShape{}, false},
	} {
		got, ok := parseHookCommand(tc.command)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseHookCommand(%q) = %+v, %v; want %+v, %v", tc.command, got, ok, tc.want, tc.ok)
		}
	}

	// initHookShape is the per-client, per-root view of it: "" is the rootless shape and a
	// rooted command is not it, nor is the rootless one any root's.
	if !initHookShape("/a/droidyard hook --client claude", Claude, "") ||
		initHookShape("/a/droidyard hook --client claude --root /p", Claude, "") ||
		initHookShape("/a/droidyard hook --client claude", Claude, "/p") ||
		initHookShape("/a/droidyard hook --client cursor", Claude, "") {
		t.Error("initHookShape does not tell the rootless shape from the rooted one")
	}
}
