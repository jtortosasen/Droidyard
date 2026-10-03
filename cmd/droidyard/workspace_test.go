package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// callIn runs a re-executed `droidyard call --no-autostart` in dir and returns its
// envelope and exit code.
func callIn(t *testing.T, state, dir string, args ...string) (capability.ResponseEnvelope, int) {
	t.Helper()
	return droidyardIn(t, state, dir, append([]string{"call", "--no-autostart"}, args...)...).envelope(t)
}

func workspaceOf(t *testing.T, env capability.ResponseEnvelope) workspace.Info {
	t.Helper()
	var info workspace.Info
	if err := json.Unmarshal(env.Result, &info); err != nil {
		t.Fatalf("workspace result %s: %v (%+v)", env.Result, err, env.Error)
	}
	return info
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

func lastOpenedAt(t *testing.T, state, id string) time.Time {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Workspaces map[string]struct {
			LastOpenedAt time.Time `json:"lastOpenedAt"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	return idx.Workspaces[id].LastOpenedAt
}

// TestCLIAddressesWorkspaces: the CLI names a workspace with --workspace, else with the
// root of its cwd (ADR-0020 §Scope and workspace addressing; plan m1c, R3), and a
// restarted daemon treats every workspace as closed until reopened, restoring its
// durable state (workspace-state.md §Cleanup and crash recovery). Real processes: a
// re-executed serve and re-executed CLIs.
func TestCLIAddressesWorkspaces(t *testing.T) {
	state := shortDir(t)
	root, sub := gradleProject(t)
	id, _ := platform.Current().WorkspaceID(root)

	srv := droidyard(t, state, "serve")
	srv.expect(t, "listening")

	// From a subdirectory: the root resolver finds settings.gradle.kts two levels up.
	opened, code := callIn(t, state, sub, "workspace.open")
	if code != 0 {
		t.Fatalf("open from %s exited %d: %+v", sub, code, opened.Error)
	}
	info := workspaceOf(t, opened)
	if info.Root != root || info.ID != id || info.Name != filepath.Base(root) || info.State != workspace.Ready || info.Trusted {
		t.Fatalf("open from a subdirectory = %+v; want root %s, id %s, ready, untrusted", info, root, id)
	}
	described, code := callIn(t, state, sub, "workspace.describe")
	if code != 0 || !reflect.DeepEqual(workspaceOf(t, described), info) {
		t.Fatalf("describe from %s exited %d: %+v", sub, code, described)
	}
	if env, code := callIn(t, state, sub, "system.health"); code != 0 || env.Error != nil {
		t.Errorf("a global call from a project directory: exit %d, %+v; the cwd's root is for workspace calls only", code, env.Error)
	}

	// From /, whose root is no project: --workspace names it.
	byID, code := callIn(t, state, "/", "--workspace", id, "workspace.describe")
	if code != 0 || string(byID.Result) != string(described.Result) {
		t.Errorf("describe --workspace %s from / exited %d: %s; want %s", id, code, byID.Result, described.Result)
	}
	unexpected, code := callIn(t, state, "/", "--workspace", root, "system.health")
	if code == 0 || unexpected.Error == nil || unexpected.Error.Code != capability.ErrUnexpectedWorkspace || unexpected.Operation != nil {
		t.Errorf("system.health with --workspace exited %d: %+v; want REQUEST_UNEXPECTED_WORKSPACE, no operation", code, unexpected)
	}

	if env, code := callIn(t, state, sub, "workspace.close"); code != 0 {
		t.Fatalf("close exited %d: %+v", code, env.Error)
	}
	if env, code := callIn(t, state, sub, "workspace.describe"); code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceNotOpen {
		t.Errorf("describe after close exited %d: %+v; want WORKSPACE_NOT_OPEN", code, env)
	}
	before := lastOpenedAt(t, state, id)

	// A restart: closed until reopened, then the same id and createdAt.
	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	srv.wait(t)
	srv = droidyard(t, state, "serve")
	srv.expect(t, "listening")
	if env, code := callIn(t, state, sub, "workspace.describe"); code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceNotOpen {
		t.Errorf("describe after a restart exited %d: %+v; want WORKSPACE_NOT_OPEN", code, env)
	}
	reopened, code := callIn(t, state, sub, "workspace.open")
	again := workspaceOf(t, reopened)
	if code != 0 || again.ID != id || again.CreatedAt == nil || info.CreatedAt == nil || !again.CreatedAt.Equal(*info.CreatedAt) {
		t.Errorf("reopened after a restart: exit %d, %+v; want id %s, createdAt %v", code, again, id, info.CreatedAt)
	}
	if after := lastOpenedAt(t, state, id); !after.After(before) {
		t.Errorf("lastOpenedAt %v after reopening; want later than %v", after, before)
	}

	// ADR-0026: the operation exists and fails; it is not a Start rejection.
	invalid, code := callIn(t, state, "/", "--workspace", "/nonexistent", "workspace.open")
	if code == 0 || invalid.Operation == nil || invalid.Operation.State != capability.StateFailed ||
		invalid.Error == nil || invalid.Error.Code != workspace.ErrRootInvalid {
		t.Errorf("open --workspace /nonexistent exited %d: %+v %+v; want a failed operation, WORKSPACE_ROOT_INVALID", code, invalid.Operation, invalid.Error)
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
// aside, the daemon runs, open works, and system.health lists the problem (ADR-0021
// §Format: "reported in system.health. Corrupt state never stops the daemon"; R9).
func TestCorruptIndexIsReportedNotFatal(t *testing.T) {
	state := shortDir(t)
	garbage := []byte("{not json at all\x00")
	if err := os.WriteFile(filepath.Join(state, "workspaces.json"), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	root, sub := gradleProject(t)

	srv := droidyard(t, state, "serve")
	srv.expect(t, "listening")
	if env, code := callIn(t, state, sub, "workspace.open"); code != 0 || workspaceOf(t, env).Root != root {
		t.Fatalf("open with a corrupt index exited %d: %+v", code, env.Error)
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
