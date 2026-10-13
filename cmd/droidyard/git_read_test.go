package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// TestGitReadIsHumanOnlyAndNeedsGit: ADR-0066 §1, ADR-0060 §2. git.read has audience
// human, so no MCP profile lists git_read (every agent has its own git) and the core
// refuses it from an MCP client (REQUEST_NOT_ALLOWED_HERE, no operation). The CLI, which
// the UI's client shares its wiring with, reads HEAD's text through the shipped
// registry after the working copy changed; with no git on the PATH the capability is
// listed unavailable with a reason, and the same call is GIT_NOT_FOUND with a next step,
// and with git back on the PATH it works again with no restart. Setting the audience to
// all would list the tool and let the MCP client in, so the first assertions fail.
func TestGitReadIsHumanOnlyAndNeedsGit(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	p := startInProcess(t)

	for _, args := range [][]string{nil, {"--tools", "all"}, {"--tools", "core"}} {
		c := startShim(t, p.state, "git", args...)
		c.initialize("git")
		names, _ := toolNames(t, c, 1)
		if len(names) == 0 {
			t.Errorf("%v lists no tools; the check below would pass vacuously", args)
		}
		for _, n := range names {
			if strings.HasPrefix(n, "git_") {
				t.Errorf("%v lists %s; git is never offered to agents (ADR-0060 §2)", args, n)
			}
		}
		c.exit()
	}

	root, _ := projectWith(t, "settings.gradle")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitPath, append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		// Never the caller's GIT_*: inside a pre-commit hook GIT_DIR and GIT_INDEX_FILE
		// name the repository being committed, and this git would init and add there.
		cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "HOME=" + t.TempDir()}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.kt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "c")
	if err := os.WriteFile(filepath.Join(root, "a.kt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	openEnv, code := callIn(t, p.state, root, "workspace.open")
	if code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, openEnv.Error)
	}
	ws := workspaceOf(t, openEnv).ID

	id, err := p.ops.Start(context.Background(), capability.Request{Capability: "git.read", Workspace: ws,
		Params: json.RawMessage(`{"path":"a.kt"}`), Client: capability.ClientMCP})
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere || id != "" {
		t.Errorf("git.read as client mcp: (%q, %v); want REQUEST_NOT_ALLOWED_HERE and no operation", id, err)
	}
	// git.watch is human-only too (ADR-0066 §1): no watch starts for an MCP client.
	id, err = p.ops.Start(context.Background(), capability.Request{Capability: "git.watch", Workspace: ws,
		Params: json.RawMessage(`{}`), Client: capability.ClientMCP})
	e = nil
	if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere || id != "" {
		t.Errorf("git.watch as client mcp: (%q, %v); want REQUEST_NOT_ALLOWED_HERE and no operation", id, err)
	}
	// git.log is human-only as well (ADR-0069 §1).
	id, err = p.ops.Start(context.Background(), capability.Request{Capability: "git.log", Workspace: ws,
		Params: json.RawMessage(`{}`), Client: capability.ClientMCP})
	e = nil
	if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere || id != "" {
		t.Errorf("git.log as client mcp: (%q, %v); want REQUEST_NOT_ALLOWED_HERE and no operation", id, err)
	}

	// git.refs is human-only as well (ADR-0069 §1).
	id, err = p.ops.Start(context.Background(), capability.Request{Capability: "git.refs", Workspace: ws,
		Params: json.RawMessage(`{}`), Client: capability.ClientMCP})
	e = nil
	if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere || id != "" {
		t.Errorf("git.refs as client mcp: (%q, %v); want REQUEST_NOT_ALLOWED_HERE and no operation", id, err)
	}

	// git.show is human-only as well (ADR-0069 §1).
	id, err = p.ops.Start(context.Background(), capability.Request{Capability: "git.show", Workspace: ws,
		Params: json.RawMessage(`{"oid":"` + strings.Repeat("a", 40) + `"}`), Client: capability.ClientMCP})
	e = nil
	if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere || id != "" {
		t.Errorf("git.show as client mcp: (%q, %v); want REQUEST_NOT_ALLOWED_HERE and no operation", id, err)
	}

	read := func() (capability.ResponseEnvelope, int) {
		return callIn(t, p.state, root, "git.read", "--params", `{"path":"a.kt"}`)
	}
	env, code := read()
	if code != 0 || env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("git.read from the CLI: exit %d, error %+v, operation %+v; want it completed", code, env.Error, env.Operation)
	}
	var got struct{ Path, Base, Text string }
	if err := json.Unmarshal(env.Result, &got); err != nil || got.Text != "committed\n" || got.Path != "a.kt" || got.Base != "HEAD" {
		t.Errorf("the result %s (%v); want HEAD's text, not the edited working copy", env.Result, err)
	}

	// git.log through the shipped registry: the one commit, its tip returned.
	lenv, code := callIn(t, p.state, root, "git.log")
	var page struct {
		Commits []struct{ Oid, Subject string }
		Tips    []string
		More    bool
	}
	if code != 0 || lenv.Error != nil || json.Unmarshal(lenv.Result, &page) != nil || len(page.Commits) != 1 || page.Commits[0].Subject != "c" || len(page.Tips) != 1 || page.More {
		t.Errorf("git.log from the CLI: exit %d, error %+v, result %s; want the one commit and its tip", code, lenv.Error, lenv.Result)
	}

	// git.refs through the shipped registry: HEAD on main, the one branch, its tip.
	renv, code := callIn(t, p.state, root, "git.refs")
	var refs struct {
		Head     struct{ Branch string }
		Branches []struct{ Name string }
		Tips     []string
	}
	if code != 0 || renv.Error != nil || json.Unmarshal(renv.Result, &refs) != nil || refs.Head.Branch != "main" || len(refs.Branches) != 1 || refs.Branches[0].Name != "main" || len(refs.Tips) != 1 {
		t.Errorf("git.refs from the CLI: exit %d, error %+v, result %s; want HEAD on main, its branch and its tip", code, renv.Error, renv.Result)
	}

	// git.show through the shipped registry: the one commit's message and its files (a.kt first),
	// and git.read at that commit (ADR-0066 §1 widened by ADR-0069 §1).
	senv, code := callIn(t, p.state, root, "git.show", "--params", `{"oid":"`+page.Commits[0].Oid+`"}`)
	var shown struct {
		Message string
		Files   []struct {
			Path, Status string
			Added        *int
		}
	}
	if code != 0 || senv.Error != nil || json.Unmarshal(senv.Result, &shown) != nil || shown.Message != "c\n" || len(shown.Files) == 0 || shown.Files[0].Path != "a.kt" || shown.Files[0].Status != "added" || shown.Files[0].Added == nil || *shown.Files[0].Added != 1 {
		t.Errorf("git.show from the CLI: exit %d, error %+v, result %s; want the commit's message and a.kt added with one line first", code, senv.Error, senv.Result)
	}
	aenv, code := callIn(t, p.state, root, "git.read", "--params", `{"path":"a.kt","base":"`+page.Commits[0].Oid+`"}`)
	var atCommit struct{ Base, Text string }
	if code != 0 || aenv.Error != nil || json.Unmarshal(aenv.Result, &atCommit) != nil || atCommit.Base != page.Commits[0].Oid || atCommit.Text != "committed\n" {
		t.Errorf("git.read at a commit from the CLI: exit %d, error %+v, result %s; want the committed text and the oid as base", code, aenv.Error, aenv.Result)
	}

	// No git on the PATH: unavailable with a reason, GIT_NOT_FOUND with a next step.
	t.Setenv("PATH", t.TempDir())
	caps, code := p.call(t, "system.capabilities")
	if code != 0 {
		t.Fatalf("system.capabilities exited %d: %+v", code, caps.Error)
	}
	var list system.Capabilities
	if err := json.Unmarshal(caps.Result, &list); err != nil {
		t.Fatal(err)
	}
	if info := capInfo(t, list, "git.read"); info.Available || !strings.Contains(info.Unavailable, "git") || !strings.Contains(info.Unavailable, "PATH") {
		t.Errorf("git.read with no git = %+v; want unavailable, with the reason", info)
	}
	env, code = read()
	if code == 0 || env.Error == nil || env.Error.Code != "GIT_NOT_FOUND" || env.Error.NextStep == "" {
		t.Errorf("git.read with no git: exit %d, error %+v; want GIT_NOT_FOUND with a next step", code, env.Error)
	}

	// Git back: no restart needed.
	t.Setenv("PATH", filepath.Dir(gitPath))
	if env, code := read(); code != 0 || env.Error != nil {
		t.Errorf("git.read with git back: exit %d, error %+v; want it to work again", code, env.Error)
	}
	// system.health reports the API version this step introduces.
	henv, code := p.call(t, "system.health")
	if code != 0 || health(t, henv).Version != "0.24.0" {
		t.Errorf("system.health: exit %d, %+v; want API version 0.24.0", code, henv.Error)
	}
}
