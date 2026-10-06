package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// registerProjectFixture registers fixture.project, a workspace-scoped capability
// whose handler runs project code, gated by the policy the daemon itself reads (plan
// m1g step 3). The channel receives once per run of the handler, so a test can prove
// the handler never ran (an untrusted root) and then did (after `droidyard trust`).
func registerProjectFixture(reg *capability.Registry, env platform.Env, root string, ran chan<- struct{}) {
	pol := policy.New(env)
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.project", Kind: capability.Command, Scope: capability.Workspace,
		Description: "Runs project code, gated by trust.", Params: struct{}{}, Result: struct{}{},
		Authorize: pol.Authorize(func(*capability.Call) (string, bool) { return root, true }),
		Handler: func(_ context.Context, _ *capability.Call) (any, error) {
			ran <- struct{}{}
			return struct{}{}, nil
		},
	})
}

// TestTrustGatesProjectCodeWithoutARestart is the authorization boundary end to end
// (ADR-0015 §5; plan m1g D2, D3): a project-code capability on an untrusted root fails
// WORKSPACE_UNTRUSTED with the exact nextStep and its handler never runs; the real
// `droidyard trust <root>` binary then makes the same in-process daemon admit the call
// with no restart; workspace.describe flips to trusted.
func TestTrustGatesProjectCodeWithoutARestart(t *testing.T) {
	root := platform.Current().Canonical(t.TempDir())
	ran := make(chan struct{}, 4)
	p := startInProcessEnv(t, func(reg *capability.Registry, env platform.Env) {
		registerProjectFixture(reg, env, root, ran)
	})

	if env, code := p.call(t, "workspace.open", "--workspace", root); code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, env.Error)
	}
	if info := workspaceOf(t, mustCall(t, p, "workspace.describe", "--workspace", root)); info.Trusted {
		t.Error("workspace.describe reports a fresh root trusted")
	}

	env, code := p.call(t, "fixture.project", "--workspace", root)
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
		t.Fatalf("an untrusted project call = (%+v, %d); want WORKSPACE_UNTRUSTED", env, code)
	}
	if want := policy.UntrustedError(root).NextStep; env.Error.NextStep != want {
		t.Errorf("nextStep = %q; want %q", env.Error.NextStep, want)
	}
	select {
	case <-ran:
		t.Fatal("the handler ran on an untrusted root")
	case <-time.After(200 * time.Millisecond):
	}

	// The real binary, against the running daemon, with no restart of anything.
	if code := droidyard(t, p.state, "trust", root).wait(t); code != 0 {
		t.Fatalf("droidyard trust exited %d", code)
	}

	env, code = p.call(t, "fixture.project", "--workspace", root)
	if code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("a trusted project call = (%+v, %d); want completed", env, code)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never ran after trust")
	}

	if info := workspaceOf(t, mustCall(t, p, "workspace.describe", "--workspace", root)); !info.Trusted {
		t.Error("workspace.describe still reports the root untrusted after `droidyard trust`")
	}
}

func mustCall(t *testing.T, p *inProcess, args ...string) capability.ResponseEnvelope {
	t.Helper()
	env, code := p.call(t, args...)
	if code != 0 {
		t.Fatalf("call %v exited %d: %+v", args, code, env.Error)
	}
	return env
}

// withoutVars is environ without the named variables, so a test can set its own and
// the inherited value does not win (Go resolves a duplicate key to its first match).
func withoutVars(environ []string, names ...string) []string {
	var out []string
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, n := range names {
			if key == n {
				drop = true
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// TestTrustFollowsTheStateDirAndNeverTheHome: the daemon and `droidyard trust`, given
// only DROIDYARD_STATE_DIR and a fake HOME, read and write
// <state>/config/config.json and never touch a configuration directory under HOME
// (plan m1g D1). It also proves the daemon picks the change up without a restart, from
// a second process.
func TestTrustFollowsTheStateDirAndNeverTheHome(t *testing.T) {
	state := shortDir(t)
	fakeHome := t.TempDir()
	root := platform.Current().Canonical(t.TempDir())
	env := append(withoutVars(os.Environ(), "HOME", "DROIDYARD_CONFIG_DIR", "DROIDYARD_STATE_DIR"),
		reexec+"=1", "DROIDYARD_STATE_DIR="+state, "HOME="+fakeHome)

	srv := droidyardProc(t, env, "", "serve")
	srv.expect(t, "listening")

	run := func(args ...string) (capability.ResponseEnvelope, int) {
		t.Helper()
		return droidyardProc(t, env, "", append([]string{"call", "--no-autostart"}, args...)...).envelope(t)
	}
	if env, code := run("workspace.open", "--workspace", root); code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, env.Error)
	}
	if info := workspaceOf(t, mustEnvelope(t, run, "workspace.describe", "--workspace", root)); info.Trusted {
		t.Error("the daemon trusts the root before `droidyard trust`")
	}

	if code := droidyardProc(t, env, "", "trust", root).wait(t); code != 0 {
		t.Fatalf("droidyard trust exited %d", code)
	}

	// The configuration followed DROIDYARD_STATE_DIR.
	cfg := filepath.Join(state, "config", policy.FileName)
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("the configuration is not at %s: %v", cfg, err)
	}
	if !strings.Contains(string(raw), root) {
		t.Errorf("%s does not name %s: %s", cfg, root, raw)
	}
	// Nothing was written under the fake HOME's configuration directories.
	for _, dir := range []string{
		filepath.Join(fakeHome, "Library", "Application Support", platform.Name+"-config"),
		filepath.Join(fakeHome, ".config", platform.Name),
	} {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the configuration directory %s under the fake HOME exists", dir)
		}
	}

	// The daemon re-reads the file: no restart, a second process sees the change.
	if info := workspaceOf(t, mustEnvelope(t, run, "workspace.describe", "--workspace", root)); !info.Trusted {
		t.Error("the running daemon did not pick up `droidyard trust`")
	}
}

func mustEnvelope(t *testing.T, run func(...string) (capability.ResponseEnvelope, int), args ...string) capability.ResponseEnvelope {
	t.Helper()
	env, code := run(args...)
	if code != 0 {
		t.Fatalf("call %v exited %d: %+v", args, code, env.Error)
	}
	return env
}
