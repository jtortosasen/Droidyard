package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cursorServers reads the droidyard entry's args and the names of the other servers in a
// Cursor mcp.json.
func cursorServers(t *testing.T, file string) (args []string, others []string) {
	t.Helper()
	var doc struct {
		Servers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	readJSON(t, file, &doc)
	for name, s := range doc.Servers {
		if name == "droidyard" {
			args = s.Args
		} else {
			others = append(others, name)
		}
	}
	return args, others
}

// TestCursorEntryHasNoFrom holds ADR-0137 §2 and ADR-0090 §1: init writes Cursor's entry as
// `mcp`, with no --from; an entry an earlier init wrote (`mcp --from ${workspaceFolder}` at
// user level, `mcp --root <path>` in a project) is replaced, as a stale entry is, and the
// user's other servers stay. An entry that only looks like it (an extra argument) is the
// user's and is kept.
//
// Seen failing before the change: the user's entry was written as `mcp --from
// ${workspaceFolder}`, and a stale one was kept ("kept, yours differs from what init would
// write").
func TestCursorEntryHasNoFrom(t *testing.T) {
	t.Parallel()
	// staleUser is an earlier init's entry; it carries the state root the run writes now
	// (the test's init runs with DROIDYARD_STATE_DIR set), as Cursor's entry did.
	staleUser := func(state string) string {
		return `{"mcpServers":{"mine":{"command":"x"},"droidyard":{"command":"/old/bin/droidyard","args":["mcp","--from","${workspaceFolder}"],"env":{"DROIDYARD_STATE_DIR":"` + state + `"}}}}`
	}
	t.Run("user level", func(t *testing.T) {
		t.Parallel()
		u := newUserSetup(t)
		file := filepath.Join(u.home, ".cursor", "mcp.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(staleUser(u.state)), 0o644); err != nil {
			t.Fatal(err)
		}
		out, errOut, code := u.init(t, projectDir(t), "--user", "--client", "cursor")
		if code != 0 {
			t.Fatalf("init --user exited %d:\n%s\n%s", code, out, errOut)
		}
		args, others := cursorServers(t, file)
		if strings.Join(args, " ") != "mcp" || len(others) != 1 || others[0] != "mine" {
			t.Errorf("after init: droidyard args %q, other servers %v; want `mcp` and the user's `mine` kept", args, others)
		}
		if !strings.Contains(out, "replaced a stale entry") {
			t.Errorf("init did not say it replaced a stale entry:\n%s", out)
		}
		// Written afresh, the entry has no --from either.
		fresh := newUserSetup(t)
		if out, errOut, code := fresh.init(t, projectDir(t), "--user", "--client", "cursor"); code != 0 {
			t.Fatalf("init --user exited %d:\n%s\n%s", code, out, errOut)
		}
		if args, _ := cursorServers(t, filepath.Join(fresh.home, ".cursor", "mcp.json")); strings.Join(args, " ") != "mcp" {
			t.Errorf("a fresh Cursor entry runs %q; want `mcp`", args)
		}
	})
	t.Run("a project's --root entry", func(t *testing.T) {
		t.Parallel()
		proj, state := projectDir(t), shortDir(t)
		file := filepath.Join(proj, ".cursor", "mcp.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		stale := `{"mcpServers":{"mine":{"command":"x"},"droidyard":{"command":"/old/bin/droidyard","args":["mcp","--root","` + proj + `"],"env":{"DROIDYARD_STATE_DIR":"` + state + `"}}}}`
		if err := os.WriteFile(file, []byte(stale), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := initRun(t, state, proj, "--client", "cursor", "--root", proj); code != 0 {
			t.Fatalf("init exited %d:\n%s", code, out)
		}
		args, others := cursorServers(t, file)
		if strings.Join(args, " ") != "mcp" || len(others) != 1 {
			t.Errorf("after init: droidyard args %q, other servers %v; want `mcp` and `mine` kept", args, others)
		}
	})
	t.Run("an entry that is not init's is kept", func(t *testing.T) {
		t.Parallel()
		u := newUserSetup(t)
		file := filepath.Join(u.home, ".cursor", "mcp.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		mine := `{"mcpServers":{"droidyard":{"command":"/bin/droidyard","args":["mcp","--from","${workspaceFolder}","--tools","core"]}}}`
		if err := os.WriteFile(file, []byte(mine), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, errOut, code := u.init(t, projectDir(t), "--user", "--client", "cursor"); code != 0 {
			t.Fatalf("init --user exited %d:\n%s\n%s", code, out, errOut)
		}
		if args, _ := cursorServers(t, file); strings.Join(args, " ") != "mcp --from ${workspaceFolder} --tools core" {
			t.Errorf("the user's own entry became %q; want it kept", args)
		}
	})
}

// TestInitReportsAStaleSkill holds that init says when the skill it finds installed is
// older, by its droidyard.version, than the one it ships, and refreshes it.
//
// Seen failing before the change: the note said only "differed from this build's skill:
// refreshed", with no version.
func TestInitReportsAStaleSkill(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	proj := projectDir(t)
	if out, errOut, code := u.init(t, proj, "--user", "--client", "claude"); code != 0 {
		t.Fatalf("init --user exited %d:\n%s\n%s", code, out, errOut)
	}
	skill := filepath.Join(u.claude, "skills", "droidyard-android", "SKILL.md")
	raw, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	cur := string(raw)
	old := strings.Replace(cur, `droidyard.version: "2"`, `droidyard.version: "1"`, 1)
	if old == cur {
		t.Fatalf("the shipped skill has no `droidyard.version: \"2\"` line; the test needs updating")
	}
	if err := os.WriteFile(skill, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := u.init(t, proj, "--user", "--client", "claude")
	if code != 0 {
		t.Fatalf("init --user exited %d:\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "the installed skill is version 1, older than this build's version 2: refreshed") {
		t.Errorf("init did not report the older skill:\n%s", out)
	}
	if got, _ := os.ReadFile(skill); string(got) != cur {
		t.Errorf("the older skill was not refreshed")
	}
	// A current skill is reported as it was: nothing to say about its version.
	if out, _, _ := u.init(t, proj, "--user", "--client", "claude"); strings.Contains(out, "older than") {
		t.Errorf("init called a current skill older:\n%s", out)
	}
}

// envOf reads the droidyard entry's env in a Cursor mcp.json.
func envOf(t *testing.T, file string) map[string]string {
	t.Helper()
	var doc struct {
		Servers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	readJSON(t, file, &doc)
	return doc.Servers["droidyard"].Env
}

// TestStaleEntryIsReplacedOnlyWhenItsValuesAreInits holds ADR-0090 §1's rule that the
// stale matcher matches only what init could have written: an old `mcp --root` or `--from`
// entry whose env names a state root other than the one this run writes, or that carries no
// env where the run writes one, is the user's (a client silently moved to another daemon
// otherwise), and OpenCode's with codemode true is the user's too. Each is kept.
//
// Seen failing before the fix: the entry was replaced and its custom state root dropped.
func TestStaleEntryIsReplacedOnlyWhenItsValuesAreInits(t *testing.T) {
	t.Parallel()
	t.Run("an env with another state root", func(t *testing.T) {
		t.Parallel()
		u := newUserSetup(t)
		file := filepath.Join(u.home, ".cursor", "mcp.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		mine := `{"mcpServers":{"droidyard":{"command":"/old/droidyard","args":["mcp","--from","${workspaceFolder}"],"env":{"DROIDYARD_STATE_DIR":"/custom/state"}}}}`
		if err := os.WriteFile(file, []byte(mine), 0o644); err != nil {
			t.Fatal(err)
		}
		out, errOut, code := u.init(t, projectDir(t), "--user", "--client", "cursor")
		if code != 0 {
			t.Fatalf("init --user exited %d:\n%s\n%s", code, out, errOut)
		}
		if got := envOf(t, file)["DROIDYARD_STATE_DIR"]; got != "/custom/state" {
			t.Errorf("the entry's state root became %q; want /custom/state kept", got)
		}
		if args, _ := cursorServers(t, file); strings.Join(args, " ") != "mcp --from ${workspaceFolder}" {
			t.Errorf("the entry became %q; want it kept", args)
		}
	})
	t.Run("OpenCode with codemode true", func(t *testing.T) {
		t.Parallel()
		proj, state := projectDir(t), shortDir(t)
		file := filepath.Join(proj, "opencode.json")
		mine := `{"mcp":{"servers":{"droidyard":{"type":"local","command":["/old/droidyard","mcp","--root","` + proj + `"],"codemode":true}}}}`
		if err := os.WriteFile(file, []byte(mine), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := initRun(t, state, proj, "--client", "opencode", "--root", proj); code != 0 {
			t.Fatalf("init exited %d:\n%s", code, out)
		}
		raw, _ := os.ReadFile(file)
		if !strings.Contains(strings.ReplaceAll(string(raw), " ", ""), `"codemode":true`) || !strings.Contains(string(raw), "--root") {
			t.Errorf("opencode.json:\n%s\nwant the entry with codemode true kept", raw)
		}
	})
}

// TestStaleEntryUninstallAndOpenCode holds ADR-0090 §1 for the two paths the Cursor
// install test does not reach: --uninstall removes an earlier init's entry, reported as
// stale, and leaves the user's other servers; and OpenCode's project entry `mcp --root`
// is replaced by `mcp`.
//
// Seen failing with the uninstall branch for stale entries disabled: the entry was kept
// ("kept, differs from what init writes now"); with staleOpenCodeServer returning false:
// the OpenCode entry kept its --root.
func TestStaleEntryUninstallAndOpenCode(t *testing.T) {
	t.Parallel()
	t.Run("--uninstall removes a stale Cursor entry", func(t *testing.T) {
		t.Parallel()
		u := newUserSetup(t)
		file := filepath.Join(u.home, ".cursor", "mcp.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		stale := `{"mcpServers":{"mine":{"command":"x"},"droidyard":{"command":"/old/bin/droidyard","args":["mcp","--from","${workspaceFolder}"],"env":{"DROIDYARD_STATE_DIR":"` + u.state + `"}}}}`
		if err := os.WriteFile(file, []byte(stale), 0o644); err != nil {
			t.Fatal(err)
		}
		out, errOut, code := u.init(t, projectDir(t), "--user", "--client", "cursor", "--uninstall")
		if code != 0 {
			t.Fatalf("init --uninstall exited %d:\n%s\n%s", code, out, errOut)
		}
		if !strings.Contains(out, "removed a stale entry") {
			t.Errorf("uninstall did not say it removed a stale entry:\n%s", out)
		}
		args, others := cursorServers(t, file)
		if args != nil || len(others) != 1 || others[0] != "mine" {
			t.Errorf("after uninstall: droidyard args %q, other servers %v; want the entry gone and `mine` kept", args, others)
		}
	})
	t.Run("OpenCode's project entry --root is replaced", func(t *testing.T) {
		t.Parallel()
		proj, state := projectDir(t), shortDir(t)
		file := filepath.Join(proj, "opencode.json")
		stale := `{"mcp":{"servers":{"droidyard":{"type":"local","command":["/old/droidyard","mcp","--root","` + proj + `"],"codemode":false}}}}`
		if err := os.WriteFile(file, []byte(stale), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := initRun(t, state, proj, "--client", "opencode", "--root", proj)
		if code != 0 {
			t.Fatalf("init exited %d:\n%s", code, out)
		}
		var doc struct {
			MCP struct {
				Servers map[string]struct {
					Command []string `json:"command"`
				} `json:"servers"`
			} `json:"mcp"`
		}
		readJSON(t, file, &doc)
		if got := doc.MCP.Servers["droidyard"].Command; len(got) != 2 || got[1] != "mcp" {
			t.Errorf("OpenCode's entry runs %q; want [binary mcp] with no --root", got)
		}
		if !strings.Contains(out, "replaced a stale entry") {
			t.Errorf("init did not say it replaced a stale entry:\n%s", out)
		}
	})
}
