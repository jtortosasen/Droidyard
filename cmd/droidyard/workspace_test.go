package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// callIn runs a re-executed `droidyard call --no-autostart` in dir and returns its
// envelope and exit code.
func callIn(t *testing.T, state, dir string, args ...string) (capability.ResponseEnvelope, int) {
	t.Helper()
	return droidyardIn(t, state, dir, append([]string{"call", "--no-autostart"}, args...)...).envelope(t)
}

// workspaceOf is the workspace a fixture.touch call answered.
func workspaceOf(t *testing.T, env capability.ResponseEnvelope) touched {
	t.Helper()
	return touchedOf(t, env)
}

// gradleProject is a project root holding settings.gradle.kts, by its canonical path,
// and a subdirectory of it.
func gradleProject(t *testing.T) (root, sub string) {
	t.Helper()
	root = platform.Current().Canonical(t.TempDir())
	sub = filepath.Join(root, "app", "src")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.gradle.kts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, sub
}

// TestCLIProjectDefaultsToCwd holds ADR-0137 §5 with real processes (a re-executed serve and
// re-executed CLIs): a CLI call of a project capability names its project with --project,
// else it is the current directory, whatever folder of the project that is; a relative
// --project is made absolute against the current directory; a capability that takes no
// project is sent none, so a global call from a project folder is unchanged; --project on a
// global call is the daemon's REQUEST_INVALID_PARAMS; the old --workspace is not a flag. A
// restarted daemon holds nothing open until a call names it, and then the same id answers
// (workspace-state.md §Cleanup and crash recovery; ADR-0137 §3). The derived command is the
// same call as `call` (ADR-0122).
func TestCLIProjectDefaultsToCwd(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	root, sub := gradleProject(t)
	id, _ := platform.Current().WorkspaceID(root)

	srv := droidyard(t, state, "serve")
	srv.expect(t, "listening")

	// From a subdirectory: the project is the root that holds settings.gradle.kts, two
	// levels up.
	opened, code := callIn(t, state, sub, "fixture.touch")
	if code != 0 {
		t.Fatalf("a call from %s exited %d: %+v", sub, code, opened.Error)
	}
	if got := workspaceOf(t, opened); got.Root != root || got.ID != id || got.GradleRoot != root {
		t.Fatalf("a call from a subdirectory ran in %+v; want root %s, id %s", got, root, id)
	}
	if env, code := callIn(t, state, sub, "system.health"); code != 0 || env.Error != nil {
		t.Errorf("a global call from a project directory: exit %d, %+v; the cwd is for project calls only", code, env.Error)
	}

	// --project names it from anywhere, absolute or relative to the cwd.
	for name, tc := range map[string]struct {
		dir  string
		args []string
	}{
		"an absolute path from /":        {"/", []string{"--project", root, "fixture.touch"}},
		"the = form":                     {"/", []string{"--project=" + sub, "fixture.touch"}},
		"a relative path":                {filepath.Dir(root), []string{"--project", filepath.Base(root), "fixture.touch"}},
		"the cwd beside a different one": {sub, []string{"--project", root, "fixture.touch"}},
	} {
		env, code := callIn(t, state, tc.dir, tc.args...)
		if code != 0 || workspaceOf(t, env).ID != id {
			t.Errorf("%s: exit %d, %+v; want workspace %s", name, code, env.Error, id)
		}
	}
	// A project in --params is the project: the cwd does not stand in beside it, and a --project
	// given too is two answers to one question, refused.
	inParams, code := callIn(t, state, "/", "fixture.touch", "--params", `{"project":`+quote(sub)+`}`)
	if code != 0 || workspaceOf(t, inParams).ID != id {
		t.Errorf("a project in --params, from /: exit %d, %+v; want workspace %s", code, inParams.Error, id)
	}
	both := droidyardIn(t, state, "/", "call", "--no-autostart", "fixture.touch", "--project", root, "--params", `{"project":`+quote(root)+`}`)
	if code := both.wait(t); code == 0 || !strings.Contains(both.stderr.String(), "use one") {
		t.Errorf("a --project beside a project in --params: exit %d, stderr %q; want it refused", code, both.stderr.String())
	}
	// The derived command is the same call, with the same default.
	derived := droidyardIn(t, state, sub, "fixture", "touch", "--json", "--no-autostart")
	if env, code := derived.envelope(t); code != 0 || workspaceOf(t, env).ID != id {
		t.Errorf("the derived command from %s: exit %d, %+v; want workspace %s", sub, code, env.Error, id)
	}

	// Where there is no project: a capability that takes one is refused by the daemon's
	// rules, naming the parameter, and a global one given --project is refused as well.
	none, code := callIn(t, state, platform.Current().Canonical(t.TempDir()), "fixture.touch")
	if code == 0 || none.Error == nil || none.Error.Code != capability.ErrProjectNoGradleRoot || none.Operation != nil {
		t.Errorf("a call from a folder with no build exited %d: %+v; want PROJECT_NO_GRADLE_ROOT and no operation", code, none)
	}
	unexpected, code := callIn(t, state, "/", "--project", root, "system.health")
	if code == 0 || unexpected.Error == nil || unexpected.Error.Code != capability.ErrInvalidParams || unexpected.Operation != nil {
		t.Errorf("system.health with --project exited %d: %+v; want REQUEST_INVALID_PARAMS, no operation", code, unexpected)
	}
	if code := droidyardIn(t, state, "/", "call", "--no-autostart", "--workspace", root, "fixture.touch").wait(t); code == 0 {
		t.Error("the --workspace flag, which --project replaced, was accepted")
	}

	// A restart: nothing is open until a call names the project, then the same id.
	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	srv.wait(t)
	srv = droidyard(t, state, "serve")
	srv.expect(t, "listening")
	if env, code := callIn(t, state, sub, "fixture.touch"); code != 0 || workspaceOf(t, env).ID != id {
		t.Errorf("a call after a restart exited %d: %+v; want the same workspace %s, opened by the call", code, env, id)
	}

	// A client of the previous API version is refused at the hello.
	_, _, _, err := daemon.Dial(sockFor(state), daemon.Hello{Protocol: daemon.Protocol, Version: "0.1.0", Client: capability.ClientCLI})
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrDaemonVersionMismatch {
		t.Errorf("a hello claiming 0.1.0: %v; want DAEMON_VERSION_MISMATCH", err)
	}

	droidyard(t, state, "stop").wait(t)
	srv.wait(t)
}

func sockFor(state string) string {
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	return env.SocketPath(state)
}

// TestCorruptIndexIsReportedNotFatal: a workspaces.json that does not parse is moved
// aside, the daemon runs, a call that names a project works, and system.health lists the
// problem (ADR-0021 §Format: "reported in system.health. Corrupt state never stops the
// daemon"; R9).
func TestCorruptIndexIsReportedNotFatal(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	garbage := []byte("{not json at all\x00")
	if err := os.WriteFile(filepath.Join(state, "workspaces.json"), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	root, sub := gradleProject(t)

	srv := droidyard(t, state, "serve")
	srv.expect(t, "listening")
	if env, code := callIn(t, state, sub, "fixture.touch"); code != 0 || workspaceOf(t, env).Root != root {
		t.Fatalf("a call with a corrupt index exited %d: %+v", code, env.Error)
	}
	env, code := callIn(t, state, sub, "system.health")
	if code != 0 {
		t.Fatalf("system.health exited %d", code)
	}
	var h system.Health
	if err := json.Unmarshal(env.Result, &h); err != nil {
		t.Fatal(err)
	}
	if len(h.StateProblems) != 1 || h.StateProblems[0].File != "workspaces.json" || h.StateProblems[0].Problem != "corrupt" || h.StateProblems[0].MovedTo == "" {
		t.Fatalf("stateProblems = %+v; want workspaces.json, corrupt, movedTo", h.StateProblems)
	}
	if moved, err := os.ReadFile(filepath.Join(state, h.StateProblems[0].MovedTo)); err != nil || string(moved) != string(garbage) {
		t.Errorf("the moved file holds %q (%v); want the garbage", moved, err)
	}

	droidyard(t, state, "stop").wait(t)
	srv.wait(t)
}
