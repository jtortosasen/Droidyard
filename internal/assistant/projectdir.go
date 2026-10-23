package assistant

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The hook of a user-level run names no root (ADR-0124 §Decision 3): it takes the
// project from the directory the client says is its own. Everything here reads client
// input, the event and the environment, and none of it is trusted: a value is a path to
// look at, never a command, and what is read is bounded.

// maxClientPath bounds a directory taken from the client; PATH_MAX is 4096 on Linux and
// 1024 on macOS, so a longer one is not a path.
const maxClientPath = 4096

// maxProjectFile bounds the project hook file HasProjectHook reads; a bigger one is not
// one init wrote, and the hook has a deadline.
const maxProjectFile = 1 << 20

// ClientProjectDir is the directory the client names as its project, from the event and
// the environment (getenv): for Claude Code CLAUDE_PROJECT_DIR, else the event's cwd; for
// Cursor the first workspace_roots entry, else CURSOR_PROJECT_DIR, else the event's cwd.
// The first source that is set decides: a value that is set but is not a plain absolute
// path (relative, with a NUL, too long) gives "", never the next source, so a malformed
// value cannot move the hook to a directory the client did not name as its project. It is
// "" too for any other client and for an event that is not JSON.
func ClientProjectDir(c Client, event []byte, getenv func(string) string) string {
	var raw struct {
		Cwd            string            `json:"cwd"`
		WorkspaceRoots []json.RawMessage `json:"workspace_roots"`
	}
	if json.Unmarshal(event, &raw) != nil {
		return ""
	}
	var sources []string
	switch c {
	case Claude:
		sources = []string{getenv("CLAUDE_PROJECT_DIR"), raw.Cwd}
	case Cursor:
		first := ""
		if len(raw.WorkspaceRoots) > 0 {
			// An entry that is not a string stays "": the list's first is the one that counts.
			_ = json.Unmarshal(raw.WorkspaceRoots[0], &first)
		}
		sources = []string{first, getenv("CURSOR_PROJECT_DIR"), raw.Cwd}
	default:
		return ""
	}
	for _, s := range sources {
		if s == "" {
			continue
		}
		if len(s) > maxClientPath || strings.ContainsRune(s, 0) || !filepath.IsAbs(s) {
			return ""
		}
		return filepath.Clean(s)
	}
	return ""
}

// HasProjectHook reports whether dir holds init's per-project hook entry for the client:
// in dir/.claude/settings.json (Claude) or dir/.cursor/hooks.json (Cursor), an element of
// the shape a project run writes, whatever binary, state root and project root it names. A
// rootless entry is a user-level one and does not count, nor does a user's own wrapper
// (parseHookCommand). Where it holds, the client runs the project's own hook as well, so
// the user-level one leaves the event to it (ADR-0124 §Decision 3). A missing, unreadable,
// oversized or malformed file, a file that is not a regular file and a dir that is not
// absolute all give false.
func HasProjectHook(c Client, dir string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	var file string
	var element func(json.RawMessage) bool
	var events []string
	switch c {
	case Claude:
		file, element = filepath.Join(dir, ".claude", "settings.json"), projectClaudeElement
		events = []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"}
	case Cursor:
		file, element = filepath.Join(dir, ".cursor", "hooks.json"), projectCursorElement
		events = []string{"beforeShellExecution", "preToolUse", "postToolUse", "postToolUseFailure"}
	default:
		return false
	}
	// Stat before open: a FIFO the project holds in that name would block the read.
	if fi, err := os.Stat(file); err != nil || !fi.Mode().IsRegular() || fi.Size() > maxProjectFile {
		return false
	}
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxProjectFile+1))
	if err != nil || len(data) > maxProjectFile {
		return false
	}
	var doc struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return false
	}
	for _, event := range events {
		if slices.ContainsFunc(doc.Hooks[event], element) {
			return true
		}
	}
	return false
}

// projectCommand is a hook command line of the per-project shape for the client: init's
// (parseHookCommand) and naming a root.
func projectCommand(command string, c Client) bool {
	hs, ok := parseHookCommand(command)
	return ok && hs.client == c && hs.hasRoot
}

// projectClaudeElement recognises one element of a Claude event's array as a project
// run's: the elements claudeStale recognises for a given root, here for any.
func projectClaudeElement(raw json.RawMessage) bool {
	var e claudeHookEntry
	if !strictUnmarshal(raw, &e) || (e.Matcher != "Bash" && e.Matcher != claudeEditMatcher) || len(e.Hooks) == 0 {
		return false
	}
	for _, h := range e.Hooks {
		if h.Type != "command" || !projectCommand(h.Command, Claude) {
			return false
		}
	}
	return true
}

// projectCursorElement is the same for one element of a Cursor event's array.
func projectCursorElement(raw json.RawMessage) bool {
	var e cursorHookEntry
	return strictUnmarshal(raw, &e) && slices.Contains([]string{"", "Delete", "Shell", cursorEditMatcher}, e.Matcher) && projectCommand(e.Command, Cursor)
}
