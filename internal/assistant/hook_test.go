package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// hookProject builds the tree the table runs on and returns the root and the outside
// directory beside it. Every pass row names something that exists, so a rule that is
// removed turns that row into a route and the test fails: a row that passes only
// because its target is missing proves nothing.
func hookProject(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "proj")
	outside = filepath.Join(base, "outside")
	put := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		"a.kt", "b.kt", "a b.kt", "-x.kt", "{a,b}", "$X", "i", "f", "x", "a*",
		"src/a.kt", "src/b.kt", "src/c.kt", "src/d.txt", "src/.hidden", "src/.dot.kt", "src/sub/e.kt",
		"dir/inner.kt", "build/x", "app/build/tmp/y", ".gradle/z", ".kotlin/z", "app/.cxx/z",
		"src/build.kt", "keep/a.kt", "dots/.only",
		// What an operator would turn into an operand: with the operator unrecognised
		// every one of these exists, so the command would route.
		";", "&&", "||", "|", "&", ">", "<", "ls", "echo", "cat", "#", "(a.kt)", "a.kt;", "a.kt&&x", "a.kt&",
	} {
		put(f)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "o.kt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"link-out":     outside,                        // a link that leaves the root
		"link-out-dir": outside,                        // same, used as a path component
		"link-in":      filepath.Join(root, "a.kt"),    // a link that stays inside
		"link-in-dir":  filepath.Join(root, "keep"),    // a directory link that stays inside
		"link-dangle":  filepath.Join(root, "nowhere"), // a link to nothing
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	return root, outside
}

// TestRmFormsTable pins ADR-0061 §2: which commands route and which pass, one row per
// form. A row that routes lists the root-relative paths; a nil list is a pass.
func TestRmFormsTable(t *testing.T) {
	root, outside := hookProject(t)
	abs := func(rel string) string { return filepath.Join(root, rel) }
	rows := []struct {
		name string
		cmd  string
		cwd  string // "" means root, "none" means the event carries none (Cursor)
		want []string
	}{
		// Routes.
		{"plain file", "rm a.kt", "", []string{"a.kt"}},
		{"-rf of a plain directory", "rm -rf dir", "", []string{"dir"}},
		{"-fr, quoted name with a space", "rm -fr 'a b.kt'", "", []string{"a b.kt"}},
		{"-R", "rm -R dir", "", []string{"dir"}},
		{"-rRf together", "rm -rRf dir", "", []string{"dir"}},
		{"double-quoted name", `rm "a b.kt"`, "", []string{"a b.kt"}},
		{"backslash space", `rm a\ b.kt`, "", []string{"a b.kt"}},
		{"-- ends the flags", "rm -- -x.kt", "", []string{"-x.kt"}},
		{"-f and -- both", "rm -f -- -x.kt", "", []string{"-x.kt"}},
		{"several operands", "rm a.kt b.kt", "", []string{"a.kt", "b.kt"}},
		{"the same operand twice goes once", "rm a.kt a.kt", "", []string{"a.kt"}},
		{"-f of a file", "rm -f a.kt", "", []string{"a.kt"}},
		{"absolute operand inside", "rm " + abs("a.kt"), "", []string{"a.kt"}},
		{"absolute operand, no cwd (Cursor)", "rm " + abs("a.kt"), "none", []string{"a.kt"}},
		// Cursor sends cwd "": a relative operand routes on its text alone (ADR-0062 §5),
		// listed by its lexical path, a glob unexpanded.
		{"Cursor: relative file", "rm a.kt", "none", []string{"a.kt"}},
		{"Cursor: relative directory", "rm -rf src/old", "none", []string{"src/old"}},
		{"Cursor: relative glob, not expanded", "rm src/*.kt", "none", []string{"src/*.kt"}},
		{"Cursor: a name that is not there is trash's to refuse", "rm nothing.kt", "none", []string{"nothing.kt"}},
		{"Cursor: a dot segment inside", "rm ./src/a.kt", "none", []string{"src/a.kt"}},
		{"Cursor: relative and absolute together", "rm a.kt " + abs("b.kt"), "none", []string{"a.kt", "b.kt"}},
		{"relative to a subdirectory cwd", "rm a.kt", abs("src"), []string{"src/a.kt"}},
		{"parent of the cwd", "rm ../a.kt", abs("src"), []string{"a.kt"}},
		{"a dot segment inside", "rm ./src/./a.kt", "", []string{"src/a.kt"}},
		{"trailing slash on a directory", "rm -r dir/", "", []string{"dir"}},
		{"glob * sorted", "rm src/*.kt", "", []string{"src/a.kt", "src/b.kt", "src/build.kt", "src/c.kt"}},
		{"glob * leaves out a dot-name", "rm -r src/*", "", []string{"src/a.kt", "src/b.kt", "src/build.kt", "src/c.kt", "src/d.txt", "src/sub"}},
		{"glob ? matches one character", "rm src/?.kt", "", []string{"src/a.kt", "src/b.kt", "src/c.kt"}},
		{"glob starting with a dot matches dot-names", "rm src/.*.kt", "", []string{"src/.dot.kt"}},
		{"glob in a directory component", "rm -r s?c/a.kt", "", []string{"src/a.kt"}},
		{"a name with a quoted star is literal", "rm 'a*'", "", []string{"a*"}},
		{"a link inside the root goes as the link", "rm link-in", "", []string{"link-in"}},
		{"a path through a link inside the root", "rm link-in-dir/a.kt", "", []string{"keep/a.kt"}},
		{"a name merely containing build", "rm src/build.kt", "", []string{"src/build.kt"}},

		// Passes: another command or a prefix.
		{"-i", "rm -i a.kt", "", nil},
		{"-v", "rm -v a.kt", "", nil},
		{"--force long flag", "rm --force a.kt", "", nil},
		{"-d", "rm -d dir", "", nil},
		{"a lone dash", "rm - a.kt", "", nil},
		{"sudo", "sudo rm a.kt", "", nil},
		{"command", "command rm a.kt", "", nil},
		{"path to rm", "/bin/rm a.kt", "", nil},
		{"backslash rm", `\rm a.kt`, "", nil},
		{"quoted rm", `'rm' a.kt`, "", nil},
		{"env prefix", "X=1 rm a.kt", "", nil},
		{"find -delete", "find . -delete", "", nil},
		{"git rm", "git rm a.kt", "", nil},
		{"rm alone", "rm", "", nil},
		{"rm with flags only", "rm -rf", "", nil},
		{"empty operand", "rm ''", "", nil},
		{"empty command", "", "", nil},

		// Passes: operators and expansions.
		{"semicolon", "rm a.kt; ls", "", nil},
		{"semicolon, spaced", "rm a.kt ; ls", "", nil},
		{"and", "rm a.kt && echo x", "", nil},
		{"and, glued", "rm a.kt&&x", "", nil},
		{"or", "rm a.kt || echo x", "", nil},
		{"pipe", "rm a.kt | cat", "", nil},
		{"background", "rm a.kt &", "", nil},
		{"background, glued", "rm a.kt&", "", nil},
		{"variable", "rm $X", "", nil},
		{"variable braces", "rm ${X}", "", nil},
		{"backticks", "rm `echo a.kt`", "", nil},
		{"command substitution", "rm $(echo a.kt)", "", nil},
		{"redirect out", "rm a.kt > f", "", nil},
		{"redirect in", "rm a.kt < f", "", nil},
		{"tilde", "rm ~/a.kt", "", nil},
		{"tilde alone", "rm ~", "", nil},
		{"brace expansion", "rm {a,b}", "", nil},
		{"subshell", "(rm a.kt)", "", nil},
		{"parenthesised operand", "rm (a.kt)", "", nil},
		{"comment", "rm a.kt # x", "", nil},
		{"substitution in double quotes", `rm "$X"`, "", nil},
		{"backtick in double quotes", "rm \"`x`\"", "", nil},
		{"unterminated quote", "rm 'a.kt", "", nil},
		{"unterminated double quote", `rm "a.kt`, "", nil},
		{"trailing backslash", `rm a.kt\`, "", nil},
		{"newline in the command", "rm a.kt\nls", "", nil},
		{"carriage return", "rm a.kt\rls", "", nil},
		{"control character", "rm a\x01", "", nil},
		{"invalid UTF-8", "rm a\xff", "", nil},
		{"bracket glob", "rm src/[ab].kt", "", nil},

		// Passes: where the target is, or what it is.
		{"relative outside", "rm ../outside/o.kt", "", nil},
		{"absolute outside", "rm " + filepath.Join(outside, "o.kt"), "", nil},
		{"relative with a relative cwd", "rm a.kt", "src", nil},
		{"cwd outside the root", "rm o.kt", outside, nil},
		{"a glob matching nothing", "rm src/*.java", "", nil},
		{"a glob matching only dot-names", "rm dots/*", "", nil},
		{"a link leaving the root", "rm link-out", "", nil},
		{"a path through a link leaving the root", "rm link-out-dir/o.kt", "", nil},
		{"a dangling link", "rm link-dangle", "", nil},
		{"a link to a directory with a trailing slash", "rm -r link-in-dir/", "", nil},
		{"a trailing slash on a file", "rm a.kt/", "", nil},
		{"a glob with one match leaving the root", "rm link-out*", "", nil},
		{"one operand routes, one is outside", "rm a.kt ../outside/o.kt", "", nil},
		{"a missing operand beside a good one", "rm a.kt missing.kt", "", nil},
		{"-f of a missing file", "rm -f nothing.kt", "", nil},
		{"a directory without -r", "rm dir", "", nil},
		{"a directory with only -f", "rm -f dir", "", nil},
		{"a glob matching a directory without -r", "rm src/s*", "", nil},
		{"dot", "rm -rf .", "", nil},
		{"dot slash", "rm -rf ./", "", nil},
		{"dot dot", "rm -rf ..", "", nil},
		{"ending in a dot dot", "rm -rf src/..", "", nil},
		{"ending in a dot", "rm -rf src/.", "", nil},
		{"the root itself", "rm -rf " + root, "", nil},
		{"the root by a detour", "rm -rf src/../../proj", "", nil},
		{"a path above the root", "rm -rf " + filepath.Dir(root), "", nil},
		{"slash alone", "rm -rf /", "", nil},

		// Passes: build outputs (ADR-0061 §2).
		{"build", "rm -rf build", "", nil},
		{"under build", "rm -rf build/x", "", nil},
		// On macOS's case-insensitive default volume `Build` is `build`: it must pass
		// there too (a case-sensitive volume has no `Build`, so the row passes either way).
		{"build in another case", "rm -rf Build", "", nil},
		{".gradle in another case", "rm -rf .Gradle", "", nil},
		// BSD rm stops reading flags at the first operand.
		{"a flag after an operand", "rm a.kt -r dir", "", nil},
		{"a dash word after an operand", "rm a.kt -f", "", nil},
		{"a glob expanding to a dash-name before --", "rm *.kt", "", nil},
		{"nested build", "rm -rf app/build/tmp", "", nil},
		{"nested build file", "rm app/build/tmp/y", "", nil},
		{".gradle", "rm -rf .gradle", "", nil},
		{".kotlin", "rm -rf .kotlin", "", nil},
		{".cxx", "rm -rf app/.cxx", "", nil},
		{"build by a glob", "rm -rf bui*", "", nil},

		// Passes: Cursor's relative operands, lexical checks only (ADR-0062 §5).
		{"Cursor: a parent component", "rm ../x", "none", nil},
		{"Cursor: a parent component inside", "rm src/../a.kt", "none", nil},
		{"Cursor: under build", "rm build/x", "none", nil},
		{"Cursor: build in another case", "rm -rf Build", "none", nil},
		{"Cursor: .gradle", "rm -rf .gradle", "none", nil},
		{"Cursor: nested .cxx", "rm -rf app/.cxx", "none", nil},
		{"Cursor: dot", "rm .", "none", nil},
		{"Cursor: dot slash", "rm -rf ./", "none", nil},
		{"Cursor: ending in a dot", "rm -rf src/.", "none", nil},
		{"Cursor: a relative operand beside one that leaves the root", "rm a.kt " + filepath.Join(outside, "o.kt"), "none", nil},
		{"Cursor: an operator is still no rm", "rm a.kt; ls", "none", nil},
	}
	for _, r := range rows {
		cwd := r.cwd
		switch cwd {
		case "":
			cwd = root
		case "none":
			cwd = ""
		}
		got, ok := RouteRm(HookEvent{Command: r.cmd, Cwd: cwd}, root)
		if (r.want != nil) != ok {
			t.Errorf("%s: %q: routed=%v paths=%v, want routed=%v %v", r.name, r.cmd, ok, got.Paths, r.want != nil, r.want)
			continue
		}
		if ok && !reflect.DeepEqual(got.Paths, r.want) {
			t.Errorf("%s: %q: paths %v, want %v", r.name, r.cmd, got.Paths, r.want)
		}
	}
}

// TestRmGlobSkipsDotNames pins the glob rule `sh` has and filepath.Glob lacks: `*`
// never matches a name starting with a dot, so `rm src/*` leaves src/.hidden alone;
// only a pattern component that itself starts with a dot reaches one. A quoted or
// escaped star is no glob.
func TestRmGlobSkipsDotNames(t *testing.T) {
	root, _ := hookProject(t)
	got, ok := RouteRm(HookEvent{Command: "rm -r src/*", Cwd: root}, root)
	if !ok {
		t.Fatal("rm -r src/* did not route")
	}
	for _, p := range got.Paths {
		if strings.Contains(p, "/.") {
			t.Errorf("rm -r src/* routed the dot-name %q", p)
		}
	}
	got, ok = RouteRm(HookEvent{Command: "rm src/.h*", Cwd: root}, root)
	if !ok || !reflect.DeepEqual(got.Paths, []string{"src/.hidden"}) {
		t.Errorf("rm src/.h*: %v %v, want src/.hidden", got, ok)
	}
	for _, cmd := range []string{`rm "a*"`, `rm a\*`} {
		got, ok = RouteRm(HookEvent{Command: cmd, Cwd: root}, root)
		if !ok || !reflect.DeepEqual(got.Paths, []string{"a*"}) {
			t.Errorf("%s: %v %v, want only a*", cmd, got, ok)
		}
	}
}

// TestRmRelativeNeverUsesTheHookProcessDirectory pins ADR-0061 §2 and ADR-0062 §5: a
// relative operand is placed against the cwd the event carries, never against the hook
// process's own directory, and with no cwd (Cursor, [obs, m2j step 6]) it is not placed
// at all, so the file system is not read for it. The hook runs inside the project here,
// and the operand names a file that exists only there: were the hook's directory used,
// `rm a.kt` and the glob would be placed (and a missing name refused), not routed by
// text.
func TestRmRelativeNeverUsesTheHookProcessDirectory(t *testing.T) {
	root, _ := hookProject(t)
	t.Chdir(root)
	// With an event cwd elsewhere, the operand is placed there and is not the root's file.
	if got, ok := RouteRm(HookEvent{Command: "rm a.kt", Cwd: filepath.Join(root, "src", "sub")}, root); ok {
		t.Errorf("a.kt placed in the process's directory instead of the event's cwd: %v", got.Paths)
	}
	// With no cwd, a glob matching nothing anywhere is still routed by text: `trash` sees
	// what the shell expands, and a placed glob that matched nothing would pass.
	got, ok := RouteRm(HookEvent{Command: "rm *.nothing"}, root)
	if !ok || !reflect.DeepEqual(got.Paths, []string{"*.nothing"}) {
		t.Errorf("a relative glob with no event cwd: %v %v; want it routed by its text", got.Paths, ok)
	}
	// A relative cwd is no cwd to place against: it still passes.
	if got, ok := RouteRm(HookEvent{Command: "rm a.kt", Cwd: "src"}, root); ok {
		t.Errorf("a relative event cwd routed to %v", got.Paths)
	}
	if got, ok := RouteRm(HookEvent{Command: "rm " + filepath.Join(root, "a.kt")}, root); !ok || !reflect.DeepEqual(got.Paths, []string{"a.kt"}) {
		t.Errorf("an absolute operand with no event cwd: %v %v", got.Paths, ok)
	}
}

// TestRmRootAliasAndTooMany pins two edges: a root given through a symlink (macOS's
// /var is /private/var) still places targets, and a command with more targets than
// file.delete accepts passes rather than routes a partial list.
func TestRmRootAliasAndTooMany(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxRoutedPaths+1; i++ {
		if err := os.WriteFile(filepath.Join(real, "fx"+strconv.Itoa(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if got, ok := RouteRm(HookEvent{Command: "rm fx1", Cwd: alias}, alias); !ok || !reflect.DeepEqual(got.Paths, []string{"fx1"}) {
		t.Errorf("through an alias root: %v %v", got, ok)
	}
	if got, ok := RouteRm(HookEvent{Command: "rm " + filepath.Join(real, "fx1")}, alias); !ok || !reflect.DeepEqual(got.Paths, []string{"fx1"}) {
		t.Errorf("real path under an alias root: %v %v", got, ok)
	}
	if got, ok := RouteRm(HookEvent{Command: "rm fx*", Cwd: alias}, alias); ok {
		t.Errorf("%d targets routed; file.delete takes %d", len(got.Paths), maxRoutedPaths)
	}
	if _, ok := RouteRm(HookEvent{Command: "rm fx1", Cwd: alias}, "relative/root"); ok {
		t.Error("a relative root routed")
	}
	if _, ok := RouteRm(HookEvent{Command: "rm fx1", Cwd: alias}, filepath.Join(base, "missing")); ok {
		t.Error("a missing root routed")
	}
}

// TestDeleteToolRoutes pins that Cursor's Delete tool routes by the same rules as rm
// without -r: an absolute file inside the root routes; a directory, a path outside, a
// relative path, a glob character (the path is literal, never expanded) and a build
// output pass.
func TestDeleteToolRoutes(t *testing.T) {
	root, outside := hookProject(t)
	for path, want := range map[string][]string{
		filepath.Join(root, "a.kt"):        {"a.kt"},
		filepath.Join(root, "src", "a.kt"): {"src/a.kt"},
		filepath.Join(root, "a*"):          {"a*"},
		filepath.Join(root, "dir"):         nil, // a directory: Delete is not rm -r
		filepath.Join(outside, "o.kt"):     nil,
		filepath.Join(root, "src", "*.kt"): nil, // no such literal name
		"a.kt":                             nil,
		filepath.Join(root, "build", "x"):  nil,
		filepath.Join(root, "link-out"):    nil,
		filepath.Join(root, "nothing.kt"):  nil,
		root:                               nil,
	} {
		got, ok := RouteRm(HookEvent{DeletePath: path}, root)
		if (want != nil) != ok || (ok && !reflect.DeepEqual(got.Paths, want)) {
			t.Errorf("Delete %q: %v %v, want %v", path, got.Paths, ok, want)
		}
	}
}

// TestDecodeRecordedEvents decodes the events Cursor 2026.10.01 sent (recorded in m2j
// step 6, user_email removed) and a Claude Code event. Claude Code's has no recording
// yet: it is synthetic, written from the shape in its hooks reference, and level 2
// until m2j step 11.
func TestDecodeRecordedEvents(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", "hooks", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ev, ok := DecodeHookEvent(Cursor, read("cursor-3-beforeShellExecution.json"))
	if !ok || ev.Command != "rm -f notes-delete-me.txt && echo after-delete" || ev.Cwd != "" || ev.DeletePath != "" {
		t.Errorf("Cursor beforeShellExecution: %+v %v", ev, ok)
	}
	ev, ok = DecodeHookEvent(Cursor, read("cursor-2-preToolUse.json"))
	want := "/Users/jorgetortosa/Library/Caches/droidyard-dev/m2j/probe/notes-delete-me.txt"
	if !ok || ev.DeletePath != want || ev.Command != "" {
		t.Errorf("Cursor preToolUse Delete: %+v %v", ev, ok)
	}
	// The recorded Delete is outside any test root, so it passes.
	root, _ := hookProject(t)
	if _, ok := RouteRm(ev, root); ok {
		t.Error("the recorded Delete routed in an unrelated root")
	}
	// The recorded shell event carries cwd "" and a && : it passes whatever the root.
	ev, _ = DecodeHookEvent(Cursor, read("cursor-3-beforeShellExecution.json"))
	if _, ok := RouteRm(ev, root); ok {
		t.Error("the recorded rm && echo routed")
	}
	if _, ok := DecodeHookEvent(Claude, read("cursor-3-beforeShellExecution.json")); ok {
		t.Error("Claude decoded a Cursor event")
	}

	// SYNTHETIC: Claude Code's PreToolUse for Bash, from its reference; not recorded.
	claude := `{"session_id":"s","transcript_path":"/t.jsonl","cwd":"/p/src","permission_mode":"default","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm a.kt","description":"d"},"tool_use_id":"toolu_1"}`
	ev, ok = DecodeHookEvent(Claude, []byte(claude))
	if !ok || ev.Command != "rm a.kt" || ev.Cwd != "/p/src" {
		t.Errorf("Claude PreToolUse Bash: %+v %v", ev, ok)
	}
	if _, ok := DecodeHookEvent(Cursor, []byte(claude)); ok {
		t.Error("Cursor decoded a Claude event")
	}
}

// TestDecodeRefusesWhatItDoesNotRoute pins fail-open at the reader: malformed JSON,
// another event, another tool and an empty command all pass.
func TestDecodeRefusesWhatItDoesNotRoute(t *testing.T) {
	for _, c := range []struct {
		client Client
		in     string
	}{
		{Claude, ``},
		{Claude, `{`},
		{Claude, `[]`},
		{Claude, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":"rm a"}`},
		{Claude, `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"command":"rm a"}}`},
		{Claude, `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"rm a"}}`},
		{Claude, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{}}`},
		{Cursor, `{"hook_event_name":"beforeShellExecution","command":""}`},
		{Cursor, `{"hook_event_name":"sessionStart","command":"rm a"}`},
		{Cursor, `{"hook_event_name":"preToolUse","tool_name":"Shell","tool_input":{"command":"rm a"}}`},
		{Cursor, `{"hook_event_name":"preToolUse","tool_name":"Delete","tool_input":{}}`},
		{OpenCode, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm a"}}`},
	} {
		if ev, ok := DecodeHookEvent(c.client, []byte(c.in)); ok {
			t.Errorf("%s %q decoded to %+v", c.client, c.in, ev)
		}
	}
}

// TestHookAnswers pins each client's answers (ADR-0061 §4, ADR-0062 §6): pass is
// nothing for Claude Code and {} for Cursor; a deny carries the reason where the model
// reads it, and Cursor's in both user_message and agent_message.
func TestHookAnswers(t *testing.T) {
	if got := PassOutput(Claude); len(got) != 0 {
		t.Errorf("Claude pass: %q, want no output", got)
	}
	if got := strings.TrimSpace(string(PassOutput(Cursor))); got != "{}" {
		t.Errorf("Cursor pass: %q, want {}", got)
	}
	reason := `use "this" <now> & 'later'`
	raw := DenyOutput(Claude, reason)
	var m map[string]map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	h := m["hookSpecificOutput"]
	if h["hookEventName"] != "PreToolUse" || h["permissionDecision"] != "deny" || h["permissionDecisionReason"] != reason {
		t.Errorf("Claude deny: %s", raw)
	}
	if strings.Contains(string(raw), `\u003c`) {
		t.Errorf("Claude deny HTML-escapes the reason: %s", raw)
	}
	var cu map[string]string
	raw = DenyOutput(Cursor, reason)
	if err := json.Unmarshal(raw, &cu); err != nil {
		t.Fatal(err)
	}
	// user_message is the field that reaches the model [obs, plan m2k step 1 c1];
	// agent_message carries the same text too (ADR-0062 §6).
	if cu["permission"] != "deny" || cu["user_message"] != reason || cu["agent_message"] != reason || len(cu) != 3 {
		t.Errorf("Cursor deny: %s", raw)
	}
	if DenyOutput(OpenCode, "x") != nil {
		t.Error("OpenCode has a hook answer")
	}
}

// routeOf is the Route of a command typed with an absolute cwd, for the message tests.
func routeOf(t *testing.T, root, cmd string) Route {
	t.Helper()
	rt, ok := RouteRm(HookEvent{Command: cmd, Cwd: root}, root)
	if !ok {
		t.Fatalf("%q did not route", cmd)
	}
	return rt
}

// TestRedirectMessage pins ADR-0062 §4's replacement: the absolute binary, `trash`,
// --state-dir only when the hook's state root came from the environment, --root, the
// flags the `rm` had, `--` only when it was typed (step 2–3 verifier N1), and the
// operands as typed; and a refusal for what the quoting cannot carry.
func TestRedirectMessage(t *testing.T) {
	root, _ := hookProject(t)
	r := Redirect{Binary: "/usr/local/bin/droidyard", Root: "/p/my app"}
	msg, ok := r.Message(routeOf(t, root, "rm -rf src/sub a.kt"))
	if !ok {
		t.Fatal("refused")
	}
	want := `/usr/local/bin/droidyard trash --root '/p/my app' -r -f 'src/sub' 'a.kt'`
	if !strings.HasSuffix(msg, ": "+want) {
		t.Errorf("message does not end with the command %s:\n%s", want, msg)
	}
	if strings.Contains(msg, "--state-dir") || strings.Contains(msg, "cd ") || strings.Contains(msg, "file.delete") {
		t.Errorf("message has a state dir, a cd or the old call though none belongs:\n%s", msg)
	}
	// Flags: only what was typed.
	msg, _ = r.Message(routeOf(t, root, "rm a.kt"))
	if !strings.HasSuffix(msg, `trash --root '/p/my app' 'a.kt'`) {
		t.Errorf("an rm without flags:\n%s", msg)
	}
	// `--` only when typed: without it, a glob that expands to a `-name` is a flag to
	// rm, and must be one to trash too.
	msg, _ = r.Message(routeOf(t, root, "rm -- a.kt"))
	if !strings.HasSuffix(msg, `trash --root '/p/my app' -- 'a.kt'`) {
		t.Errorf("an rm --:\n%s", msg)
	}
	msg, _ = r.Message(routeOf(t, root, "rm -f a.kt"))
	if !strings.HasSuffix(msg, `trash --root '/p/my app' -f 'a.kt'`) {
		t.Errorf("an rm -f:\n%s", msg)
	}
	r.StateDir = "/s/state dir"
	msg, ok = r.Message(routeOf(t, root, "rm a.kt"))
	if !ok || !strings.HasSuffix(msg, `trash --state-dir '/s/state dir' --root '/p/my app' 'a.kt'`) {
		t.Errorf("state dir not given before --root (%v):\n%s", ok, msg)
	}

	// The command is a POSIX shell command: a single quote or a control character in
	// any part would break its quoting, so the command passes.
	good := routeOf(t, root, "rm a.kt")
	for _, bad := range []Redirect{
		{Binary: "/b'in/droidyard", Root: "/p"},
		{Binary: "/bin/droidyard", Root: "/p'q"},
		{Binary: "/bin/droidyard", Root: "/p", StateDir: "/s'"},
		{Binary: "/bin/droidyard", Root: "/p\n"},
		{Binary: "droidyard", Root: "/p"},
		{Binary: "/bin/droidyard", Root: "p"},
	} {
		if _, ok := bad.Message(good); ok {
			t.Errorf("%+v produced a command", bad)
		}
	}
	// A single quote or a control character in a typed operand, quoted or escaped so
	// the parser carries it: the whole rm passes.
	plain := Redirect{Binary: "/bin/droidyard", Root: "/p"}
	for _, cmd := range []string{`rm "it's.kt"`, `rm a\'b`, "rm 'a\tb.kt'"} {
		rt, ok := RouteRm(HookEvent{Command: cmd, Cwd: ""}, root)
		if !ok {
			t.Fatalf("%q did not route with no cwd", cmd)
		}
		if _, ok := plain.Message(rt); ok {
			t.Errorf("%q produced a command", cmd)
		}
	}
	if _, ok := plain.Message(Route{}); ok {
		t.Error("no operands produced a command")
	}
}

// TestRedirectRequotesOperandsAsTyped pins ADR-0062 §4 and the plan-review's B2: the
// replacement's operands expand in the shell as the typed ones would. A `*` or `?` that
// was unquoted stays bare; a quoted or escaped one, and every other character, is
// single-quoted. Made bare, a quoted glob would also trash its neighbours; quoted, a
// real glob would match nothing.
func TestRedirectRequotesOperandsAsTyped(t *testing.T) {
	root, _ := hookProject(t)
	r := Redirect{Binary: "/bin/droidyard", Root: "/p"}
	for _, c := range []struct{ typed, operands string }{
		{`rm 'a*b.kt'`, `'a*b.kt'`},
		{`rm "a*b.kt"`, `'a*b.kt'`},
		{`rm a\*b.kt`, `'a*b.kt'`},
		{`rm a?b.kt`, `'a'?'b.kt'`},
		{`rm src/*.kt`, `'src/'*'.kt'`},
		{`rm "my dir"/*.kt`, `'my dir/'*'.kt'`},
		{`rm 'a'*`, `'a'*`},
		{`rm *`, `*`},
		{`rm src/'*'*`, `'src/*'*`},
		{`rm a\ b.kt`, `'a b.kt'`},
		{`rm "a\"b"`, `'a"b'`},
	} {
		for _, cwd := range []string{root, ""} {
			rt, ok := RouteRm(HookEvent{Command: c.typed, Cwd: cwd}, root)
			if !ok {
				// With the project's own files a placed glob can match nothing; the
				// no-cwd run covers that form.
				if cwd != "" {
					continue
				}
				t.Fatalf("%q did not route with no cwd", c.typed)
			}
			msg, ok := r.Message(rt)
			if !ok || !strings.HasSuffix(msg, " /p "+c.operands) {
				t.Errorf("%q (cwd %q): message %q (%v); want operands %s", c.typed, cwd, msg, ok, c.operands)
			}
		}
	}
	// Cursor's Delete: the path is literal, a glob character in it included.
	rt, ok := RouteRm(HookEvent{DeletePath: filepath.Join(root, "a*")}, root)
	if !ok {
		t.Fatal("Delete of a* did not route")
	}
	msg, _ := r.Message(rt)
	if !strings.HasSuffix(msg, " /p '"+filepath.Join(root, "a*")+"'") {
		t.Errorf("Delete message %q; want the absolute path in single quotes, the star included", msg)
	}
}
