package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// TestTrustGatesProjectCodeWithoutARestart is the authorization boundary end to end
// (ADR-0015 §5; plan m1g D2, D3): a project-code capability on an untrusted root fails
// WORKSPACE_UNTRUSTED with the exact nextStep and its handler never runs; the real
// `droidyard trust <root>` binary then makes the same in-process daemon admit the call
// with no restart. The root is the one the call's `project` resolved to (ADR-0137 §3):
// the call names a folder inside the project, and trust written for the project's root
// admits it.
func TestTrustGatesProjectCodeWithoutARestart(t *testing.T) {
	root := platform.Current().Canonical(t.TempDir())
	writeSettings(t, root) // a call naming the root resolves it by its build (ADR-0137 §3)
	inside := filepath.Join(root, "app", "src")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	p := startInProcess(t)

	env, code := p.call(t, "fixture.trusted", "--project", inside)
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
		t.Fatalf("an untrusted project call = (%+v, %d); want WORKSPACE_UNTRUSTED", env, code)
	}
	if want := policy.UntrustedError(root).NextStep; env.Error.NextStep != want {
		t.Errorf("nextStep = %q; want %q, which names the root %s and not the folder passed", env.Error.NextStep, want, root)
	}
	if env.Result != nil {
		t.Errorf("the untrusted call carries a result %s: its handler ran", env.Result)
	}

	// The real binary, against the running daemon, with no restart of anything.
	if code := droidyard(t, p.state, "trust", root).wait(t); code != 0 {
		t.Fatalf("droidyard trust exited %d", code)
	}

	env, code = p.call(t, "fixture.trusted", "--project", inside)
	if code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("a trusted project call = (%+v, %d); want completed", env, code)
	}
	if got := touchedOf(t, env); got.Root != root {
		t.Errorf("the call ran in %+v; want the project root %s", got, root)
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
	writeSettings(t, root)
	env := append(withoutVars(os.Environ(), "HOME", "DROIDYARD_CONFIG_DIR", "DROIDYARD_STATE_DIR"),
		reexec+"=1", "DROIDYARD_STATE_DIR="+state, "HOME="+fakeHome)

	srv := droidyardProc(t, env, "", "serve")
	srv.expect(t, "listening")

	run := func(args ...string) (capability.ResponseEnvelope, int) {
		t.Helper()
		return droidyardProc(t, env, "", append([]string{"call", "--no-autostart"}, args...)...).envelope(t)
	}
	if env, code := run("fixture.trusted", "--project", root); code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
		t.Fatalf("the daemon trusts the root before `droidyard trust`: exit %d, %+v", code, env.Error)
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
	if env, code := run("fixture.trusted", "--project", root); code != 0 || env.Error != nil {
		t.Errorf("the running daemon did not pick up `droidyard trust`: exit %d, %+v", code, env.Error)
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

// writeSettings gives root a settings.gradle, so that a call naming it resolves to it.
func writeSettings(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "settings.gradle"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
