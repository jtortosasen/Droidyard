package assistant

import (
	"path/filepath"
	"testing"
)

// Decision (ADR-0124, ADR-0127 §2): `init --user` and the daemon's chat reader resolve the
// client directories by the same function, so they cannot disagree. CLAUDE_CONFIG_DIR
// replaces ~/.claude and takes .claude.json with it; XDG_CONFIG_HOME moves OpenCode's
// directory only when it is absolute. Reversed (the variable ignored, or a relative
// XDG_CONFIG_HOME followed) the reader looks in the wrong home.
func TestResolveUserDirs(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := filepath.Join(string(filepath.Separator), "home", "u")

	d := ResolveUserDirs(home, env(nil))
	want := UserDirs{
		Claude:     filepath.Join(home, ".claude"),
		ClaudeJSON: filepath.Join(home, ".claude.json"),
		Cursor:     filepath.Join(home, ".cursor"),
		OpenCode:   filepath.Join(home, ".config", "opencode"),
		Agents:     filepath.Join(home, ".agents"),
	}
	if d != want {
		t.Errorf("defaults %+v; want %+v", d, want)
	}

	custom := filepath.Join(string(filepath.Separator), "cfg", "claude")
	d = ResolveUserDirs(home, env(map[string]string{"CLAUDE_CONFIG_DIR": custom, "XDG_CONFIG_HOME": filepath.Join(string(filepath.Separator), "xdg")}))
	if d.Claude != custom || d.ClaudeJSON != filepath.Join(custom, ".claude.json") {
		t.Errorf("CLAUDE_CONFIG_DIR: claude %q, .claude.json %q; want both inside %q", d.Claude, d.ClaudeJSON, custom)
	}
	if d.OpenCode != filepath.Join(string(filepath.Separator), "xdg", "opencode") || d.Cursor != want.Cursor {
		t.Errorf("XDG_CONFIG_HOME: opencode %q, cursor %q; want /xdg/opencode and the default cursor", d.OpenCode, d.Cursor)
	}

	d = ResolveUserDirs(home, env(map[string]string{"XDG_CONFIG_HOME": "relative/xdg"}))
	if d.OpenCode != want.OpenCode {
		t.Errorf("a relative XDG_CONFIG_HOME gave %q; want it ignored", d.OpenCode)
	}
}
