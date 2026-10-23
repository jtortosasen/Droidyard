package assistant

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// ADR-0124 §Decision 3: a hook without --root takes the directory the client names as its
// project. Claude: CLAUDE_PROJECT_DIR, else the event's cwd. Cursor: the first
// workspace_roots entry, else CURSOR_PROJECT_DIR, else cwd. The order is the decision: a
// reader that put cwd first would follow an agent that `cd`s into another project
// (ADR-0085 §Alternatives considered), one that read only the first source would lose the
// project of an event that lacks it, and one that took the second workspace root would
// bind the wrong window's folder.
func TestClientProjectDirTakesTheClientsOwnSourcesInOrder(t *testing.T) {
	rows := []struct {
		name   string
		client Client
		event  string
		env    map[string]string
		want   string
	}{
		{"claude: the project dir beats cwd", Claude, `{"cwd":"/b/sub"}`, map[string]string{"CLAUDE_PROJECT_DIR": "/a"}, "/a"},
		{"claude: cwd without the project dir", Claude, `{"cwd":"/b/sub"}`, nil, "/b/sub"},
		{"claude: nothing", Claude, `{}`, nil, ""},
		{"claude: the project dir cleaned", Claude, `{}`, map[string]string{"CLAUDE_PROJECT_DIR": "/a/./x/../"}, "/a"},
		{"claude: workspace_roots is Cursor's, not read", Claude, `{"workspace_roots":["/w"]}`, nil, ""},
		{"claude: CURSOR_PROJECT_DIR is Cursor's, not read", Claude, `{}`, map[string]string{"CURSOR_PROJECT_DIR": "/c"}, ""},
		{"cursor: the first workspace root beats the rest", Cursor, `{"workspace_roots":["/w1","/w2"],"cwd":"/b"}`, map[string]string{"CURSOR_PROJECT_DIR": "/c"}, "/w1"},
		{"cursor: the variable without a workspace root", Cursor, `{"workspace_roots":[],"cwd":"/b"}`, map[string]string{"CURSOR_PROJECT_DIR": "/c"}, "/c"},
		{"cursor: an empty first entry is not a source", Cursor, `{"workspace_roots":["","/w2"]}`, map[string]string{"CURSOR_PROJECT_DIR": "/c"}, "/c"},
		{"cursor: cwd last", Cursor, `{"workspace_roots":null,"cwd":"/b"}`, nil, "/b"},
		{"cursor: nothing", Cursor, `{"workspace_roots":null,"cwd":""}`, nil, ""},
		{"cursor: CLAUDE_PROJECT_DIR is Claude's, not read", Cursor, `{}`, map[string]string{"CLAUDE_PROJECT_DIR": "/a"}, ""},
		{"opencode has no hook", OpenCode, `{"cwd":"/b"}`, nil, ""},
	}
	for _, r := range rows {
		if got := ClientProjectDir(r.client, []byte(r.event), envOf(r.env)); got != r.want {
			t.Errorf("%s: ClientProjectDir = %q; want %q", r.name, got, r.want)
		}
	}
}

// The event and the environment are the client's input, not trusted: the first source that
// is set decides, and one that is set but is no plain absolute path gives no project, never
// the next source (a malformed CLAUDE_PROJECT_DIR must not hand the choice to the event's
// cwd, which an agent's `cd` moves). An event that is not JSON, or whose fields are not the
// types the client sends, names nothing.
func TestClientProjectDirRefusesWhatIsNotAPathAndDoesNotFallThrough(t *testing.T) {
	long := "/" + strings.Repeat("a", maxClientPath)
	rows := []struct {
		name   string
		client Client
		event  string
		env    map[string]string
	}{
		{"claude: a relative project dir does not fall to cwd", Claude, `{"cwd":"/b"}`, map[string]string{"CLAUDE_PROJECT_DIR": "rel/dir"}},
		{"claude: a project dir with a NUL does not fall to cwd", Claude, `{"cwd":"/b"}`, map[string]string{"CLAUDE_PROJECT_DIR": "/a\x00/b"}},
		{"claude: an over-long project dir", Claude, `{"cwd":"/b"}`, map[string]string{"CLAUDE_PROJECT_DIR": long}},
		{"claude: a relative cwd", Claude, `{"cwd":"b"}`, nil},
		{"claude: a NUL escaped in the event's cwd", Claude, `{"cwd":"/b\u0000x"}`, nil},
		{"claude: cwd of the wrong type", Claude, `{"cwd":42}`, map[string]string{"CLAUDE_PROJECT_DIR": "/a"}},
		{"claude: not JSON", Claude, `/a`, map[string]string{"CLAUDE_PROJECT_DIR": "/a"}},
		{"claude: empty event", Claude, ``, map[string]string{"CLAUDE_PROJECT_DIR": "/a"}},
		{"cursor: a relative first root does not fall to the variable", Cursor, `{"workspace_roots":["rel"]}`, map[string]string{"CURSOR_PROJECT_DIR": "/c"}},
		{"cursor: a first root that is no string is none", Cursor, `{"workspace_roots":[7,"/w2"]}`, nil},
		{"cursor: workspace_roots of the wrong type", Cursor, `{"workspace_roots":"/w"}`, map[string]string{"CURSOR_PROJECT_DIR": "/c"}},
		{"cursor: a relative variable does not fall to cwd", Cursor, `{"cwd":"/b"}`, map[string]string{"CURSOR_PROJECT_DIR": "c"}},
	}
	for _, r := range rows {
		if got := ClientProjectDir(r.client, []byte(r.event), envOf(r.env)); got != "" {
			t.Errorf("%s: ClientProjectDir = %q; want none", r.name, got)
		}
	}
}

// ADR-0124 §Decision 3: where the client's directory holds init's own per-project hook, the
// user-level hook passes. What counts is what a project run writes (so a change to the
// writer that this reader does not follow shows here): it is run for real, for each client,
// and the reader says yes, and no after an uninstall.
func TestHasProjectHookRecognisesWhatAProjectRunWrites(t *testing.T) {
	for _, c := range []Client{Claude, Cursor} {
		t.Run(string(c), func(t *testing.T) {
			dir := projectRoot(t)
			if HasProjectHook(c, dir) {
				t.Fatal("HasProjectHook is true for a folder init never ran in")
			}
			o := opts(dir, c)
			mustRun(t, o)
			if !HasProjectHook(c, dir) {
				t.Errorf("HasProjectHook is false for a folder init wrote %s's hook entries in", c)
			}
			if HasProjectHook(c, projectRoot(t)) {
				t.Error("HasProjectHook is true for another folder; only the named folder's own file counts")
			}
			// With the state root inline (DROIDYARD_STATE_DIR), as init writes it when it has one.
			o2 := opts(dir, c)
			o2.StateDir = filepath.Join(dir, "state")
			mustRun(t, o2)
			if !HasProjectHook(c, dir) {
				t.Error("HasProjectHook is false for an entry that carries DROIDYARD_STATE_DIR")
			}
			mustRun(t, uninstall(o2))
			if HasProjectHook(c, dir) {
				t.Errorf("HasProjectHook is true after --uninstall removed %s's hook entries", c)
			}
		})
	}
}

// parseHookCommand is the shape check (ADR-0090 §1): an entry a user wrote, one for the
// other client, one at user level (no root: the entry this very hook runs from) and one of
// init's elements with anything added are not "the project's own", so none of them makes
// the user-level hook pass. A hook that checked only for the word droidyard in the file
// would let all of them silence it.
func TestHasProjectHookDoesNotTakeAUsersEntryForInits(t *testing.T) {
	claude := func(matcher, command string) string {
		return `{"hooks":{"PreToolUse":[{"matcher":"` + matcher + `","hooks":[{"type":"command","command":` + jsonString(command) + `}]}]}}`
	}
	cursor := func(matcher, command string) string {
		return `{"version":1,"hooks":{"beforeShellExecution":[{"command":` + jsonString(command) + `,"matcher":"` + matcher + `"}]}}`
	}
	const root = "/p"
	rows := []struct {
		name   string
		client Client
		file   string
		want   bool
	}{
		{"claude: init's shape", Claude, claude("Bash", "/bin/droidyard hook --client claude --root "+root), true},
		{"claude: init's shape with a state root", Claude, claude("Bash", "DROIDYARD_STATE_DIR=/s /bin/droidyard hook --client claude --root "+root), true},
		{"claude: the edit tools' matcher", Claude, claude(claudeEditMatcher, "/bin/droidyard hook --client claude --root "+root), true},
		{"claude: a user-level entry has no root", Claude, claude("Bash", "/bin/droidyard hook --client claude"), false},
		{"claude: the other client's command", Claude, claude("Bash", "/bin/droidyard hook --client cursor --root "+root), false},
		{"claude: a user's wrapper", Claude, claude("Bash", "sh -c '/bin/droidyard hook --client claude --root "+root+"'"), false},
		{"claude: a user's extra word", Claude, claude("Bash", "/bin/droidyard hook --client claude --root "+root+" --verbose"), false},
		{"claude: a relative root", Claude, claude("Bash", "/bin/droidyard hook --client claude --root p"), false},
		{"claude: a relative binary", Claude, claude("Bash", "droidyard hook --client claude --root "+root), false},
		{"claude: another binary", Claude, claude("Bash", "/bin/notdroidyard hook --client claude --root "+root), false},
		{"claude: a matcher init does not write", Claude, claude("Read", "/bin/droidyard hook --client claude --root "+root), false},
		{"claude: an element with a key init does not write", Claude, `{"hooks":{"PreToolUse":[{"matcher":"Bash","extra":1,"hooks":[{"type":"command","command":"/bin/droidyard hook --client claude --root /p"}]}]}}`, false},
		{"claude: init's command beside a user's in one element", Claude, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/droidyard hook --client claude --root /p"},{"type":"command","command":"mine"}]}]}}`, false},
		{"claude: an event init does not use", Claude, `{"hooks":{"Stop":[{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/droidyard hook --client claude --root /p"}]}]}}`, false},
		{"claude: hooks of the wrong type", Claude, `{"hooks":[1]}`, false},
		{"claude: not JSON", Claude, `{"hooks":`, false},
		{"claude: a user's hook among init's own is found", Claude, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"mine"}]},{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/droidyard hook --client claude --root /p"}]}]}}`, true},
		{"cursor: init's shape", Cursor, cursor("", "/bin/droidyard hook --client cursor --root "+root), true},
		{"cursor: the Write matcher", Cursor, cursor(cursorEditMatcher, "/bin/droidyard hook --client cursor --root "+root), true},
		{"cursor: a user-level entry has no root", Cursor, cursor("", "/bin/droidyard hook --client cursor"), false},
		{"cursor: the other client's command", Cursor, cursor("", "/bin/droidyard hook --client claude --root "+root), false},
		{"cursor: a user's wrapper", Cursor, cursor("", "env X=1 /bin/droidyard hook --client cursor --root "+root), false},
		{"cursor: a matcher init does not write", Cursor, cursor("Read", "/bin/droidyard hook --client cursor --root "+root), false},
	}
	for _, r := range rows {
		dir := projectRoot(t)
		rel := ".claude/settings.json"
		if r.client == Cursor {
			rel = ".cursor/hooks.json"
		}
		write(t, dir, rel, r.file)
		if got := HasProjectHook(r.client, dir); got != r.want {
			t.Errorf("%s: HasProjectHook = %v; want %v", r.name, got, r.want)
		}
	}
	// The file is the client's: Claude's settings are not read for Cursor, nor the reverse.
	dir := projectRoot(t)
	write(t, dir, ".claude/settings.json", claude("Bash", "/bin/droidyard hook --client claude --root "+root))
	if HasProjectHook(Cursor, dir) || HasProjectHook(OpenCode, dir) {
		t.Error("HasProjectHook reads another client's file")
	}
	if HasProjectHook(Claude, "relative/dir") {
		t.Error("HasProjectHook is true for a relative directory")
	}
}

// What is read from the project is bounded and is only a file: a hook file over the bound,
// a directory, a FIFO (opening one waits for a writer that never comes, and the hook has two
// seconds) and a missing file all give false, quickly.
func TestHasProjectHookReadsOnlyARegularBoundedFile(t *testing.T) {
	entry := `{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/droidyard hook --client claude --root /p"}]}`
	check := func(t *testing.T, dir string, want bool) {
		t.Helper()
		done := make(chan bool, 1)
		go func() { done <- HasProjectHook(Claude, dir) }()
		select {
		case got := <-done:
			if got != want {
				t.Errorf("HasProjectHook = %v; want %v", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("HasProjectHook did not return: it waited on the file")
		}
	}

	t.Run("a control just under the bound is read", func(t *testing.T) {
		dir := projectRoot(t)
		pad := strings.Repeat(" ", maxProjectFile-len(entry)-100)
		write(t, dir, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+entry+`]}}`+pad)
		check(t, dir, true)
	})
	t.Run("a file over the bound is not read", func(t *testing.T) {
		dir := projectRoot(t)
		write(t, dir, ".claude/settings.json", `{"hooks":{"PreToolUse":[`+entry+`]}}`+strings.Repeat(" ", maxProjectFile))
		check(t, dir, false)
	})
	t.Run("a directory", func(t *testing.T) {
		dir := projectRoot(t)
		if err := os.MkdirAll(filepath.Join(dir, ".claude", "settings.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		check(t, dir, false)
	})
	t.Run("a FIFO", func(t *testing.T) {
		dir := projectRoot(t)
		if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(filepath.Join(dir, ".claude", "settings.json"), 0o600); err != nil {
			t.Skipf("no FIFO here: %v", err)
		}
		check(t, dir, false)
	})
	t.Run("a missing file", func(t *testing.T) {
		check(t, projectRoot(t), false)
	})
}

func jsonString(s string) string {
	return string(rawJSON(s))
}
