package assistant

import (
	"bytes"
	"encoding/json"
	"path"
	"slices"
	"strings"
)

// The hook entries init writes (ADR-0061 §5, ADR-0063 §7, ADR-0095 §3): one before a shell
// command, for the trash, and one after it, for the observation, and one before and one
// after the client's edit tools, for the verdict and the record, each client's own event
// names.
// Claude Code's go into the settings file that already carries its allowlist; Cursor's
// have a file of their own. All run the same `droidyard hook` command line, which
// decides what an event is.

// HookCommand is the command line of one client's hook entry: the absolute binary, the
// project root and, when init's own state root came from DROIDYARD_STATE_DIR, that
// variable inline. A client's hook environment does not carry it [obs, m1o], and
// without it the hook would dial the default state root's daemon and name it in its
// replacement. A user-level run has no root (Options.Root is empty) and its command names
// none: the hook finds the project from the client's event (ADR-0124).
func HookCommand(o Options, c Client) string {
	cmd := ""
	if o.StateDir != "" {
		cmd = "DROIDYARD_STATE_DIR=" + shellArg(o.StateDir) + " "
	}
	cmd += shellArg(o.Command) + " hook --client " + string(c)
	if o.Root != "" {
		cmd += " --root " + shellArg(o.Root)
	}
	return cmd
}

// hookShape is a hook command line of the shape HookCommand writes, taken apart.
type hookShape struct {
	client Client
	// root is the --root word, "" when hasRoot is false.
	root    string
	hasRoot bool
	// state is the DROIDYARD_STATE_DIR value, "" when hasState is false.
	state    string
	hasState bool
}

// parseHookCommand takes a hook command apart when it has init's shape (ADR-0090 §1,
// extended by ADR-0124): an optional DROIDYARD_STATE_DIR=<absolute> word, an absolute
// path whose base name is droidyard, and exactly `hook --client <c>` for a client with a
// hook, with or without `--root <absolute>` after it. The command is split as the hook
// splits it. A wrapper, `sh -c`, an extra word or a relative root is not init's, so an
// entry a user wrote by hand is never taken for one of init's.
func parseHookCommand(command string) (hookShape, bool) {
	words, ok := splitWords(command)
	if !ok {
		return hookShape{}, false
	}
	var hs hookShape
	if len(words) > 0 {
		if v, found := strings.CutPrefix(words[0].String(), "DROIDYARD_STATE_DIR="); found {
			if !strings.HasPrefix(v, "/") {
				return hookShape{}, false
			}
			hs.state, hs.hasState = v, true
			words = words[1:]
		}
	}
	if len(words) != 4 && len(words) != 6 {
		return hookShape{}, false
	}
	bin := words[0].String()
	if !strings.HasPrefix(bin, "/") || path.Base(bin) != "droidyard" ||
		words[1].String() != "hook" || words[2].String() != "--client" {
		return hookShape{}, false
	}
	hs.client = Client(words[3].String())
	if !hasHook(hs.client) {
		return hookShape{}, false
	}
	if len(words) == 6 {
		root := words[5].String()
		if words[4].String() != "--root" || !strings.HasPrefix(root, "/") {
			return hookShape{}, false
		}
		hs.root, hs.hasRoot = root, true
	}
	return hs, true
}

// initHookShape reports whether a hook command has the shape HookCommand writes for this
// client and root, with any absolute binary path and any state root. An empty root means
// the rootless shape of a user-level entry: a command that names a root is not it.
func initHookShape(command string, c Client, root string) bool {
	hs, ok := parseHookCommand(command)
	return ok && hs.client == c && hs.root == root
}

// strictUnmarshal decodes v from raw, refusing a field the type does not have: an
// element with an extra key is not init's.
func strictUnmarshal(raw json.RawMessage, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil && !dec.More()
}

// claudeStale recognises a hook array element init wrote for this root, whatever binary
// path it names: matcher Bash or the edit tools' (ADR-0095 §3) and only commands of init's
// shape.
func claudeStale(root string) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool {
		var e claudeHookEntry
		if !strictUnmarshal(raw, &e) || (e.Matcher != "Bash" && e.Matcher != claudeEditMatcher) || len(e.Hooks) == 0 {
			return false
		}
		for _, h := range e.Hooks {
			if h.Type != "command" || !initHookShape(h.Command, Claude, root) {
				return false
			}
		}
		return true
	}
}

// cursorStale is the same for one Cursor event, whose elements carry one of the matchers
// init writes for it.
func cursorStale(root string, matchers ...string) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool {
		var e cursorHookEntry
		return strictUnmarshal(raw, &e) && slices.Contains(matchers, e.Matcher) && initHookShape(e.Command, Cursor, root)
	}
}

// hasHook reports whether init writes a hook entry for the client.
func hasHook(c Client) bool { return c == Claude || c == Cursor }

type claudeHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// claudeHookEntry is one element of an event's array under hooks in .claude/settings.json.
type claudeHookEntry struct {
	Matcher string              `json:"matcher"`
	Hooks   []claudeHookCommand `json:"hooks"`
}

// cursorHookEntry is one element of an event's array in .cursor/hooks.json.
type cursorHookEntry struct {
	Command string `json:"command"`
	Matcher string `json:"matcher,omitempty"`
}

// claudeHookArrays are the array entries added to .claude/settings.json: a Bash command
// before it runs, and after it (PostToolUse after an exit 0, PostToolUseFailure after a
// non-zero exit), and the edit tools before and after a write (ADR-0095 §3).
func claudeHookArrays(o Options) []arrayEntry {
	cmd := []claudeHookCommand{{Type: "command", Command: HookCommand(o, Claude)}}
	bash := rawJSON(claudeHookEntry{Matcher: "Bash", Hooks: cmd})
	edit := rawJSON(claudeHookEntry{Matcher: claudeEditMatcher, Hooks: cmd})
	var out []arrayEntry
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		elems := []json.RawMessage{bash}
		if event != "PostToolUseFailure" {
			elems = append(elems, edit)
		}
		out = append(out, arrayEntry{path: []string{"hooks"}, key: event, elems: elems, stale: claudeStale(o.Root)})
	}
	return out
}

// cursorHookSpec is everything written into .cursor/hooks.json: a shell command before
// it runs, Cursor's own Delete tool and its Write tool before it writes, and a shell
// command after it (postToolUse, and postToolUseFailure for a non-zero exit) and a Write
// after it (ADR-0095 §3). `version` is the file's format number; it is a sole key, so
// --uninstall keeps a user's while their hooks remain.
func cursorHookSpec(o Options) jsonSpec {
	cmd := HookCommand(o, Cursor)
	entry := func(matcher string) json.RawMessage { return rawJSON(cursorHookEntry{Command: cmd, Matcher: matcher}) }
	hooks := []string{"hooks"}
	return jsonSpec{
		keys: []keyEntry{{key: "version", value: rawJSON(1), sole: true}},
		arrays: []arrayEntry{
			{path: hooks, key: "beforeShellExecution", elems: []json.RawMessage{entry("")}, stale: cursorStale(o.Root, "")},
			{path: hooks, key: "preToolUse", elems: []json.RawMessage{entry("Delete"), entry(cursorEditMatcher)}, stale: cursorStale(o.Root, "Delete", cursorEditMatcher)},
			{path: hooks, key: "postToolUse", elems: []json.RawMessage{entry("Shell"), entry(cursorEditMatcher)}, stale: cursorStale(o.Root, "Shell", cursorEditMatcher)},
			{path: hooks, key: "postToolUseFailure", elems: []json.RawMessage{entry("Shell")}, stale: cursorStale(o.Root, "Shell")},
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
