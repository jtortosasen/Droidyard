package assistant

import (
	"bytes"
	"encoding/json"
	"path"
	"slices"
	"strings"
)

// The hook entries an earlier init wrote (ADR-0061 §5, ADR-0063 §7, ADR-0095 §3), which
// init now only removes (ADR-0137, plan m3j Q4): one before and one after a client's shell
// command and its edit tools, each client's own event names, all running `droidyard hook
// --client <c> [--root <abs>]`. Claude Code's went into the settings file that carries its
// allowlist; Cursor's had a file of their own. An entry is recognised by shape alone
// (ADR-0090 §1), so one a user wrote, or wrapped, stays.

// claudeEditMatcher is the matcher of Claude Code's edit entries an earlier init wrote.
const claudeEditMatcher = "Write|Edit|NotebookEdit"

// cursorEditMatcher is the tool name of cursor-agent's edit entries.
const cursorEditMatcher = "Write"

// PassOutput is what the client's hook prints to let the command through untouched:
// nothing for Claude Code (exit 0), `{}` for Cursor [obs, m2j step 6]. The exit status
// is 0 either way; the hook never exits 2, which Claude Code reads as a deny.
func PassOutput(c Client) []byte {
	if c == Cursor {
		return []byte("{}\n")
	}
	return nil
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
