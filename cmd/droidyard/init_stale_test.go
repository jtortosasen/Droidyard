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
	const staleUser = `{"mcpServers":{"mine":{"command":"x"},"droidyard":{"command":"/old/bin/droidyard","args":["mcp","--from","${workspaceFolder}"]}}}`
	t.Run("user level", func(t *testing.T) {
		t.Parallel()
		u := newUserSetup(t)
		file := filepath.Join(u.home, ".cursor", "mcp.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(staleUser), 0o644); err != nil {
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
		proj := projectDir(t)
		file := filepath.Join(proj, ".cursor", "mcp.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		stale := `{"mcpServers":{"mine":{"command":"x"},"droidyard":{"command":"/old/bin/droidyard","args":["mcp","--root","` + proj + `"]}}}`
		if err := os.WriteFile(file, []byte(stale), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := initRun(t, shortDir(t), proj, "--client", "cursor", "--root", proj); code != 0 {
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
