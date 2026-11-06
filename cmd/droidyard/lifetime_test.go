package main

import (
	"encoding/json"
	"testing"
)

// TestCLIOpensItsProject is ADR-0137 §3's end to end through a real daemon: `droidyard call
// build.tasks` in a trusted project that no workspace.open ever named runs, on the first
// call, and a second from a folder inside the project is the same workspace. The root is
// opened by the call that names it. Seen failing on step 4's tree (WORKSPACE_NOT_OPEN).
func TestCLIOpensItsProject(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg, events := fakeSidecar(t, gradleRootModel)
	top := topWith(t, "settings.gradle.kts", "app/src/")

	srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	srv.expect(t, "listening")
	if code := droidyard(t, state, "trust", top).wait(t); code != 0 {
		t.Fatalf("droidyard trust %s exited %d", top, code)
	}

	tasks := func(dir string) string {
		t.Helper()
		env, code := callIn(t, state, dir, "build.tasks")
		if code != 0 || env.Error != nil {
			t.Fatalf("build.tasks from %s exited %d: %+v", dir, code, env.Error)
		}
		var res struct {
			Tasks []struct {
				Path string `json:"path"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal(env.Result, &res); err != nil || len(res.Tasks) != 1 {
			t.Fatalf("build.tasks result %s: %v", env.Result, err)
		}
		return res.Tasks[0].Path
	}
	if got := tasks(top); got != ":app:assembleDebug" {
		t.Errorf("build.tasks = %s; want the fake's :app:assembleDebug", got)
	}
	if n := starts(t, events); n != 1 {
		t.Errorf("%d sidecar starts after the first call; want one", n)
	}
	// From a folder inside the project: the same workspace, so the same sidecar.
	tasks(top + "/app/src")
	if n := starts(t, events); n != 1 {
		t.Errorf("%d sidecar starts after the second call; want the one sidecar of the first", n)
	}
	// The workspace it opened is the project's, and is open.
	env, code := callIn(t, state, top, "workspace.describe")
	if code != 0 || env.Error != nil {
		t.Fatalf("describe exited %d: %+v", code, env.Error)
	}
	if info := workspaceOf(t, env); info.Root != top || string(info.State) != "ready" {
		t.Errorf("the workspace the call opened = %+v; want %s, ready", info, top)
	}
}
