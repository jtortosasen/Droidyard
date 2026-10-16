package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/assistant"
)

// projectDir is an empty project with its own state directory, so nothing of the
// machine's is read or written.
func projectDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func initRun(t *testing.T, state, dir string, args ...string) (stdout string, code int) {
	t.Helper()
	p := droidyardIn(t, state, dir, append([]string{"init"}, args...)...)
	code = p.wait(t)
	return p.stdout.String(), code
}

func allowOf(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	sort.Strings(doc.Permissions.Allow)
	return doc.Permissions.Allow
}

// TestInitNeverStartsADaemon: `droidyard init` writes files in the project and nothing
// under the state root: no daemon.lock, no socket, no directory (ADR-0037 Decision 5;
// ADR-0019 §Processes 4).
func TestInitNeverStartsADaemon(t *testing.T) {
	t.Parallel()
	state, proj := shortDir(t), projectDir(t)
	out, code := initRun(t, state, proj, "--client", "all", "--root", proj)
	if code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	left, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("init left %v under the state root; want nothing (no daemon.lock, no socket)", names)
	}
	if _, err := os.Stat(filepath.Join(proj, ".cursor", "mcp.json")); err != nil {
		t.Errorf("init wrote no .cursor/mcp.json: %v", err)
	}
}

// TestInitAllowlistsTheToolsTheServerListsAsReadOnly: the allowlists init writes are
// exactly the tools a real MCP session lists with readOnlyHint true. The expectation
// comes from tools/list, not from the registry init reads, so a disagreement between
// what init thinks is exposed and what the adapter exposes fails here (ADR-0037
// Decision 4).
func TestInitAllowlistsTheToolsTheServerListsAsReadOnly(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	c := startShim(t, p.state, "init-allowlist")
	c.initialize("init-allowlist")
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Annotations struct {
					ReadOnly *bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(c.recv(), &list); err != nil {
		t.Fatal(err)
	}
	c.exit()
	var want []string
	for _, tool := range list.Result.Tools {
		if strings.HasPrefix(tool.Name, "fixture_") {
			continue
		}
		if tool.Annotations.ReadOnly != nil && *tool.Annotations.ReadOnly {
			want = append(want, tool.Name)
		}
	}
	sort.Strings(want)
	if len(want) < 10 || !contains(want, "device_uiTree") || contains(want, "build_assemble") {
		t.Fatalf("the session lists read-only tools %v; this test would prove nothing", want)
	}

	proj := projectDir(t)
	if out, code := initRun(t, shortDir(t), proj, "--client", "cursor,claude", "--root", proj); code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	var cursor, claude []string
	for _, a := range allowOf(t, filepath.Join(proj, ".cursor", "cli.json")) {
		cursor = append(cursor, strings.TrimSuffix(strings.TrimPrefix(a, "Mcp(droidyard:"), ")"))
	}
	for _, a := range allowOf(t, filepath.Join(proj, ".claude", "settings.json")) {
		claude = append(claude, strings.TrimPrefix(a, "mcp__droidyard__"))
	}
	sort.Strings(cursor)
	sort.Strings(claude)
	if strings.Join(cursor, ",") != strings.Join(want, ",") {
		t.Errorf(".cursor/cli.json allows %v; the server lists %v as read-only", cursor, want)
	}
	if strings.Join(claude, ",") != strings.Join(want, ",") {
		t.Errorf(".claude/settings.json allows %v; the server lists %v as read-only", claude, want)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestInitEntriesNameTheBinaryAndTheRoot: every client's server entry runs an absolute
// path to this binary and names the project root, since a GUI-launched client has no
// PATH and Cursor's working directory is unknown.
func TestInitEntriesNameTheBinaryAndTheRoot(t *testing.T) {
	t.Parallel()
	proj := projectDir(t)
	if out, code := initRun(t, shortDir(t), proj, "--client", "all", "--root", proj); code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	exe, _ := filepath.Abs(os.Args[0])
	for _, f := range []string{".cursor/mcp.json", ".mcp.json"} {
		var doc struct {
			Servers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		raw, _ := os.ReadFile(filepath.Join(proj, f))
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		s := doc.Servers["droidyard"]
		if !sameFile(s.Command, exe) || !filepath.IsAbs(s.Command) || strings.Join(s.Args, " ") != "mcp --root "+proj {
			t.Errorf("%s: %+v; want an absolute path to this binary and `mcp --root %s`", f, s, proj)
		}
	}
	var oc struct {
		MCP struct {
			Servers map[string]struct {
				Type     string   `json:"type"`
				Command  []string `json:"command"`
				Codemode *bool    `json:"codemode"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	raw, _ := os.ReadFile(filepath.Join(proj, "opencode.json"))
	if err := json.Unmarshal(raw, &oc); err != nil {
		t.Fatal(err)
	}
	s := oc.MCP.Servers["droidyard"]
	if s.Type != "local" || len(s.Command) != 4 || !sameFile(s.Command[0], exe) || s.Command[3] != proj || s.Codemode == nil || *s.Codemode {
		t.Errorf("opencode.json mcp.servers.droidyard: %+v; want a local server [binary mcp --root %s] and codemode false", s, proj)
	}
	// OpenCode 2.0.18's local server has no `enabled`, and reads mcp.servers.<name>
	// only: an entry elsewhere would leave it without droidyard, silently.
	if strings.Contains(string(raw), `"enabled"`) {
		t.Errorf("opencode.json carries an `enabled` key OpenCode's schema does not have:\n%s", raw)
	}
}

// TestBinaryCommand: the entry names the `droidyard` on PATH, as PATH names it and
// never resolved, when that is this binary, so an upgrade that repoints the link keeps
// it valid; any other `droidyard` on PATH is not this binary and is not used.
func TestBinaryCommand(t *testing.T) {
	dir := projectDir(t)
	real := filepath.Join(dir, "Cellar", "droidyard-1.2", "droidyard")
	other := filepath.Join(dir, "elsewhere", "droidyard")
	for _, f := range []string{real, other} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "bin", "droidyard")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	found := func(p string, err error) func(string) (string, error) {
		return func(string) (string, error) { return p, err }
	}
	for name, tc := range map[string]struct {
		exe  string
		look func(string) (string, error)
		want string
	}{
		"the symlink on PATH is this binary": {real, found(link, nil), link},
		"another droidyard on PATH":          {real, found(other, nil), real},
		"nothing on PATH":                    {real, found("", os.ErrNotExist), real},
	} {
		got, err := binaryCommand(tc.exe, tc.look)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, tc.want)
		}
	}
}

// TestInitOnTheCommandLine: the flags, the exit codes and the report.
func TestInitOnTheCommandLine(t *testing.T) {
	t.Parallel()
	state := shortDir(t)

	t.Run("usage errors exit 2 and write nothing", func(t *testing.T) {
		proj := projectDir(t)
		for _, args := range [][]string{
			{},
			{"--client", "codex"},
			{"--client", "cursor", "extra"},
			{"--client", "cursor", "--nope"},
		} {
			p := droidyardIn(t, state, proj, append([]string{"init"}, args...)...)
			if code := p.wait(t); code != 2 {
				t.Errorf("init %v exited %d; want 2", args, code)
			}
		}
		if left, _ := os.ReadDir(proj); len(left) != 0 {
			t.Errorf("a usage error wrote %v", left)
		}
	})

	t.Run("dry run writes nothing, then init, then a second init is unchanged, then uninstall", func(t *testing.T) {
		proj := projectDir(t)
		out, code := initRun(t, state, proj, "--client", "cursor", "--root", proj, "--dry-run")
		if code != 0 || !strings.Contains(out, "would create") || !strings.Contains(out, ".cursor/mcp.json") {
			t.Fatalf("dry run: exit %d\n%s", code, out)
		}
		if left, _ := os.ReadDir(proj); len(left) != 0 {
			t.Fatalf("a dry run wrote %v", left)
		}
		out, code = initRun(t, state, proj, "--client", "cursor", "--root", proj)
		if code != 0 || !strings.Contains(out, "read-only tools") {
			t.Fatalf("init: exit %d\n%s", code, out)
		}
		if !strings.Contains(out, "device_uiTree") {
			t.Errorf("init does not print the list it allowlisted:\n%s", out)
		}
		out, code = initRun(t, state, proj, "--client", "cursor", "--root", proj)
		if code != 0 || strings.Contains(out, "created") || strings.Contains(out, "updated") {
			t.Errorf("a second init reported changes: exit %d\n%s", code, out)
		}
		out, code = initRun(t, state, proj, "--client", "cursor", "--root", proj, "--uninstall")
		if code != 0 || !strings.Contains(out, "deleted") {
			t.Fatalf("uninstall: exit %d\n%s", code, out)
		}
		if left, _ := os.ReadDir(proj); len(left) != 0 {
			var names []string
			for _, e := range left {
				names = append(names, e.Name())
			}
			t.Errorf("--uninstall left %v", names)
		}
	})

	t.Run("a refused file exits 1, names the file and prints the entry", func(t *testing.T) {
		proj := projectDir(t)
		if err := os.MkdirAll(filepath.Join(proj, ".cursor"), 0o755); err != nil {
			t.Fatal(err)
		}
		bad := "{ // hand-written\n}"
		os.WriteFile(filepath.Join(proj, ".cursor", "cli.json"), []byte(bad), 0o644)
		out, code := initRun(t, state, proj, "--client", "cursor", "--root", proj)
		if code != 1 || !strings.Contains(out, "refused") || !strings.Contains(out, ".cursor/cli.json") || !strings.Contains(out, `"Mcp(droidyard:device_uiTree)"`) {
			t.Errorf("exit %d\n%s", code, out)
		}
		if got, _ := os.ReadFile(filepath.Join(proj, ".cursor", "cli.json")); string(got) != bad {
			t.Errorf("the refused file was changed: %q", got)
		}
	})

	t.Run("without --root and without a settings.gradle above, init refuses", func(t *testing.T) {
		proj := projectDir(t)
		p := droidyardIn(t, state, proj, "init", "--client", "opencode")
		if code := p.wait(t); code != 2 {
			t.Errorf("exit %d; want 2", code)
		}
		if !strings.Contains(p.stderr.String(), "--root") {
			t.Errorf("stderr %q does not ask for --root", p.stderr.String())
		}
		if left, _ := os.ReadDir(proj); len(left) != 0 {
			t.Errorf("init wrote %v into a directory it did not know to be a project", left)
		}
	})

	t.Run("at the top of a repository init names the top, and at a folder of two it refuses", func(t *testing.T) {
		top := projectDir(t)
		buildAt(t, top, "dev")
		out, code := initRun(t, state, top, "--client", "cursor", "--dry-run")
		if code != 0 || !strings.Contains(out, "droidyard init for "+top+" ") || !strings.Contains(out, "Gradle root: "+filepath.Join(top, "dev")) {
			t.Errorf("at the top: exit %d; want the top as the root and its Gradle root named\n%s", code, out)
		}
		two := projectDir(t)
		buildAt(t, two, "a")
		buildAt(t, two, "b")
		p := droidyardIn(t, state, two, "init", "--client", "cursor")
		if code := p.wait(t); code != 2 {
			t.Errorf("at a folder of two: exit %d; want 2", code)
		}
		for _, want := range []string{"or above it", "one or two levels below", "gradleRoot", "--root"} {
			if !strings.Contains(p.stderr.String(), want) {
				t.Errorf("the refusal %q does not say %q", p.stderr.String(), want)
			}
		}
		if left, _ := os.ReadDir(two); len(left) != 2 {
			t.Errorf("init wrote into a folder of two: %v", left)
		}
		// Named with --root, the folder of two is a project with no Gradle root: init says
		// so and how to choose one.
		out, code = initRun(t, state, two, "--client", "cursor", "--dry-run", "--root", two)
		if code != 0 || !strings.Contains(out, "Gradle root: none (several") || !strings.Contains(out, "gradleRoot") {
			t.Errorf("--root at a folder of two: exit %d; want its Gradle root reported as none, with the choice\n%s", code, out)
		}
	})

	t.Run("the home directory is refused, with --root or found by settings.gradle", func(t *testing.T) {
		home := projectDir(t)
		os.WriteFile(filepath.Join(home, "settings.gradle"), nil, 0o644)
		env := append(withoutVars(os.Environ(), "HOME"), reexec+"=1", "DROIDYARD_STATE_DIR="+state, "HOME="+home)
		for name, args := range map[string][]string{
			"--root":   {"init", "--client", "cursor", "--root", home},
			"found by": {"init", "--client", "cursor"},
		} {
			p := droidyardProc(t, env, home, args...)
			if code := p.wait(t); code != 2 {
				t.Errorf("%s: exit %d; want 2", name, code)
			}
			if !strings.Contains(p.stderr.String(), "home directory") {
				t.Errorf("%s: stderr %q does not say it is the home directory", name, p.stderr.String())
			}
		}
		if left, _ := os.ReadDir(home); len(left) != 1 {
			t.Errorf("init wrote into the home directory: %v", left)
		}
	})

	t.Run("a client's note is printed after a successful init and only then", func(t *testing.T) {
		proj := projectDir(t)
		for _, c := range []string{"cursor", "claude"} {
			want := assistant.Note(assistant.Client(c))
			out, code := initRun(t, state, proj, "--client", c, "--root", proj)
			if code != 0 || want == "" || !strings.Contains(out, want+"\n") {
				t.Errorf("%s: exit %d, note %q not in\n%s", c, code, want, out)
			}
		}
		for _, args := range [][]string{{"--dry-run"}, {"--uninstall"}} {
			out, _ := initRun(t, state, proj, append([]string{"--client", "cursor,claude", "--root", proj}, args...)...)
			if strings.Contains(out, assistant.Note(assistant.Cursor)) || strings.Contains(out, assistant.Note(assistant.Claude)) {
				t.Errorf("%v printed a client note:\n%s", args, out)
			}
		}
		out, _ := initRun(t, state, projectDir(t), "--client", "opencode", "--root", proj)
		if strings.Contains(out, "cursor-agent") || strings.Contains(out, "approve") {
			t.Errorf("OpenCode's run printed another client's note:\n%s", out)
		}
	})

	t.Run("the root defaults to the project root of the working directory", func(t *testing.T) {
		proj := projectDir(t)
		os.WriteFile(filepath.Join(proj, "settings.gradle.kts"), nil, 0o644)
		sub := filepath.Join(proj, "app", "src")
		os.MkdirAll(sub, 0o755)
		if out, code := initRun(t, state, sub, "--client", "opencode"); code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if _, err := os.Stat(filepath.Join(proj, "opencode.json")); err != nil {
			t.Errorf("init did not write at the settings.gradle root: %v", err)
		}
		if _, err := os.Stat(filepath.Join(sub, "opencode.json")); err == nil {
			t.Error("init wrote into the working directory, not the project root")
		}
	})
}

// TestInitHookEntriesNameTheBinaryTheRootAndTheStateRoot: the hook entry runs this
// binary with --root, and writes the state root inline when init's own came from
// DROIDYARD_STATE_DIR (set here by droidyardIn), since a client's hook environment lacks
// it and the hook would otherwise dial another daemon (ADR-0061 §3, §5). init prints it.
func TestInitHookEntriesNameTheBinaryTheRootAndTheStateRoot(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	proj := projectDir(t)
	out, code := initRun(t, state, proj, "--client", "claude,cursor", "--root", proj)
	if code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	exe, _ := filepath.Abs(os.Args[0])
	for _, c := range []struct{ client, file string }{{"claude", ".claude/settings.json"}, {"cursor", ".cursor/hooks.json"}} {
		raw, err := os.ReadFile(filepath.Join(proj, c.file))
		if err != nil {
			t.Fatal(err)
		}
		var found string
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "hook --client "+c.client) {
				found = line
				break
			}
		}
		prefix := "DROIDYARD_STATE_DIR=" + filepath.Clean(state) + " "
		i := strings.Index(found, prefix)
		if i < 0 || !strings.HasSuffix(strings.TrimRight(found, `",`), " hook --client "+c.client+" --root "+proj) {
			t.Fatalf("%s: %q; want %s<binary> hook --client %s --root %s", c.file, found, prefix, c.client, proj)
		}
		bin := strings.TrimPrefix(strings.TrimRight(found, `",`)[i+len(prefix):], "")
		bin = strings.Fields(bin)[0]
		if !sameFile(bin, exe) {
			t.Errorf("%s: the hook runs %s; want this binary %s", c.file, bin, exe)
		}
		if !strings.Contains(out, "The "+c.client+" hook") {
			t.Errorf("init does not print the %s hook it wrote:\n%s", c.client, out)
		}
	}
}
