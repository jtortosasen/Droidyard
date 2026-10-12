package assistant

import (
	"encoding/json"
	"strings"
)

// The hook entries init writes (ADR-0061 §5). Claude Code's go into the settings file
// that already carries its allowlist; Cursor's have a file of their own. Both run
// `droidyard hook`, which decides what an event is.

// HookCommand is the command line of one client's hook entry: the absolute binary, the
// project root and, when init's own state root came from DROIDYARD_STATE_DIR, that
// variable inline. A client's hook environment does not carry it [obs, m1o], and
// without it the hook would dial the default state root's daemon and name it in its
// replacement.
func HookCommand(o Options, c Client) string {
	cmd := ""
	if o.StateDir != "" {
		cmd = "DROIDYARD_STATE_DIR=" + shellArg(o.StateDir) + " "
	}
	return cmd + shellArg(o.Command) + " hook --client " + string(c) + " --root " + shellArg(o.Root)
}

// hasHook reports whether init writes a hook entry for the client.
func hasHook(c Client) bool { return c == Claude || c == Cursor }

type claudeHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// claudeHookEntry is one element of hooks.PreToolUse in .claude/settings.json.
type claudeHookEntry struct {
	Matcher string              `json:"matcher"`
	Hooks   []claudeHookCommand `json:"hooks"`
}

// cursorHookEntry is one element of an event's array in .cursor/hooks.json.
type cursorHookEntry struct {
	Command string `json:"command"`
	Matcher string `json:"matcher,omitempty"`
}

// claudeHookArray is the array entry added to .claude/settings.json.
func claudeHookArray(o Options) arrayEntry {
	return arrayEntry{path: []string{"hooks"}, key: "PreToolUse", elems: []json.RawMessage{
		rawJSON(claudeHookEntry{Matcher: "Bash", Hooks: []claudeHookCommand{{Type: "command", Command: HookCommand(o, Claude)}}}),
	}}
}

// cursorHookSpec is everything written into .cursor/hooks.json: a shell command before
// it runs, and Cursor's own Delete tool. `version` is the file's format number; it is a
// sole key, so --uninstall keeps a user's while their hooks remain.
func cursorHookSpec(o Options) jsonSpec {
	cmd := HookCommand(o, Cursor)
	return jsonSpec{
		keys: []keyEntry{{key: "version", value: rawJSON(1), sole: true}},
		arrays: []arrayEntry{
			{path: []string{"hooks"}, key: "beforeShellExecution", elems: []json.RawMessage{rawJSON(cursorHookEntry{Command: cmd})}},
			{path: []string{"hooks"}, key: "preToolUse", elems: []json.RawMessage{rawJSON(cursorHookEntry{Command: cmd, Matcher: "Delete"})}},
		},
	}
}

// shellArg quotes a string as one POSIX shell word, leaving a plain path as it is. A
// single quote is closed, escaped and reopened, so any path survives.
func shellArg(s string) string {
	plain := s != ""
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_@%+=:,./-", c)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
