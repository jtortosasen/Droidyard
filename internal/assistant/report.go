package assistant

import "strings"

// What `droidyard init` reports besides the changes themselves (ADR-0124 Q1, Q4). Both
// functions only read; neither writes a file.

// ProjectEntry names, slash-separated and relative to root, the project file that holds
// droidyard's MCP entry for c, or "" when the project has none. A user-level install
// (ADR-0124 §1) is overridden by such an entry, so init --user reports it. As everywhere
// in this package, a file that cannot be read counts as holding one: init will not guess.
func ProjectEntry(root string, c Client) string {
	r, err := newFSRoot(root)
	if err != nil {
		return ""
	}
	var file, jsonc string
	var path []string
	switch c {
	case Claude:
		file, path = ".mcp.json", cursorServers
	case Cursor:
		file, path = ".cursor/mcp.json", cursorServers
	case OpenCode:
		file, jsonc, path = "opencode.json", "opencode.jsonc", openCodeServers
	default:
		return ""
	}
	if r.hasServerEntry(file, jsonc, path) {
		return file
	}
	return ""
}

// IgnoreBlock returns the lines of init's block in the ignore file at file (an absolute
// path), as Ignore wrote them; nil when the file or the block is not there.
func IgnoreBlock(file string) []string {
	data, exists, err := readFile(file)
	if err != nil || !exists {
		return nil
	}
	var lines []string
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == ignoreHeader:
			in = true
		case line == ignoreEnd && in:
			return lines
		case in:
			lines = append(lines, line)
		}
	}
	return nil
}
