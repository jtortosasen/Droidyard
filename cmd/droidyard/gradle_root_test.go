package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// gradleRootModel is the one model the fake serves in these tests.
var gradleRootModel = gradle.Model{Projects: []gradle.Module{
	{Path: ":", Name: "root", Dir: "."},
	{Path: ":app", Name: "app", Dir: "app", Tasks: []gradle.Task{
		{Name: "assembleDebug", Path: ":app:assembleDebug", Group: "build", Public: true},
	}},
}}

// topWith makes a project top (canonical) holding the given files, relative paths with
// `/`; a path ending in `/` is a directory.
func topWith(t *testing.T, files ...string) string {
	t.Helper()
	top := platform.Current().Canonical(t.TempDir())
	for _, f := range files {
		p := filepath.Join(top, filepath.FromSlash(f))
		if strings.HasSuffix(f, "/") {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return top
}

// eventLines is the fake sidecar's event log, one entry per line.
func eventLines(t *testing.T, events string) []string {
	t.Helper()
	raw, err := os.ReadFile(events)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestGradleRootBelowTheProjectRoot is ADR-0075 §1, §3's end to end: a repository whose
// top holds .cursor/, README.md and no settings file, and whose Gradle build is dev/,
// opened at the top. Trust is the project root's and is decided first: untrusted,
// build.tasks fails WORKSPACE_UNTRUSTED with no sidecar started. Trusted, the sidecar
// connects to <top>/dev, never to the top, and the tasks are listed. Seen failing when the
// Gradle capabilities are handed the project root as their Gradle root (the state of main).
func TestGradleRootBelowTheProjectRoot(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg, events := fakeSidecar(t, gradleRootModel)
	top := topWith(t, ".cursor/", "README.md", "dev/settings.gradle.kts")

	srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	srv.expect(t, "listening")
	if env, code := callIn(t, state, top, "workspace.open"); code != 0 {
		t.Fatalf("open exited %d: %+v", code, env.Error)
	}

	env, code := callIn(t, state, top, "build.tasks")
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
		t.Fatalf("an untrusted build.tasks = (%+v, %d); want WORKSPACE_UNTRUSTED", env.Error, code)
	}
	if n := starts(t, events); n != 0 {
		t.Errorf("a sidecar was spawned for an untrusted root (%d starts); want none", n)
	}

	if code := droidyard(t, state, "trust", top).wait(t); code != 0 {
		t.Fatalf("droidyard trust %s exited %d", top, code)
	}
	env, code = callIn(t, state, top, "build.tasks")
	if code != 0 || env.Error != nil {
		t.Fatalf("a trusted build.tasks exited %d: %+v", code, env.Error)
	}
	var res struct {
		Tasks []struct {
			Path string `json:"path"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Tasks) != 1 || res.Tasks[0].Path != ":app:assembleDebug" {
		t.Errorf("build.tasks = %+v; want the fake's :app:assembleDebug", res.Tasks)
	}
	// project.modules publishes its dirs relative to the project root, not the Gradle root
	// (ADR-0075 §5): the fake's "." and "app" are "dev" and "dev/app". Seen failing with
	// the daemon's hook handing the Gradle root as the project root.
	env, code = callIn(t, state, top, "project.modules")
	var mods struct {
		Modules []struct {
			Dir string `json:"dir"`
		} `json:"modules"`
	}
	if code != 0 || json.Unmarshal(env.Result, &mods) != nil {
		t.Fatalf("project.modules exited %d: %+v", code, env.Error)
	}
	var dirs []string
	for _, m := range mods.Modules {
		dirs = append(dirs, m.Dir)
	}
	if strings.Join(dirs, ",") != "dev,dev/app" {
		t.Errorf("project.modules dirs = %v; want [dev dev/app], relative to the project root", dirs)
	}
	// The other Gradle capabilities on the same workspace, their results aside: each must
	// reach the sidecar through dev/, so none opens the top. build.run may answer before
	// its build reaches the sidecar, so the log is read once the fake served it.
	for _, c := range [][]string{
		{"project.modules"}, {"project.variants"}, {"sdk.requirements"},
		{"build.run", "--params", `{"tasks":["assembleDebug"]}`},
	} {
		callIn(t, state, top, c...)
	}
	var lines []string
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if lines = eventLines(t, events); contains(lines, "served build.run") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake never served build.run; its log is %q", lines)
		}
	}
	if !contains(lines, "opened "+filepath.Join(top, "dev")) {
		t.Errorf("the sidecar's log is %q; want it to have opened %s", lines, filepath.Join(top, "dev"))
	}
	if contains(lines, "opened "+top) {
		t.Errorf("the sidecar connected to the project root %s; want only the Gradle root", top)
	}

	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	srv.wait(t)
}

// TestNoGradleRoot is ADR-0075 §3, §4: the six Gradle capabilities check the project
// root's trust first, then fail PROJECT_NO_GRADLE_ROOT when it has none (here two
// candidates), the nextStep naming the gradleRoot key and the candidates in the details,
// with no sidecar started; build.lastResult reads history and answers BUILD_NOT_FOUND
// whether or not there is a Gradle root. Writing the key takes effect on the next call
// with no reopen.
//
// Seen failing with the two checks swapped (a trusted-or-not top answering
// PROJECT_NO_GRADLE_ROOT), with build.lastResult given the Gradle authorization, and with
// the resolution cached at open (the key written afterwards ignored).
func TestNoGradleRoot(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg, events := fakeSidecar(t, gradleRootModel)
	top := topWith(t, "dev/settings.gradle.kts", "tools/x/settings.gradle.kts")

	srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	srv.expect(t, "listening")
	if env, code := callIn(t, state, top, "workspace.open"); code != 0 {
		t.Fatalf("open exited %d: %+v", code, env.Error)
	}

	calls := [][]string{
		{"project.modules"},
		{"project.variants"},
		{"build.tasks"},
		{"build.run", "--params", `{"tasks":["assembleDebug"]}`},
		{"build.assemble", "--params", `{"module":":app","variant":"debug"}`},
		{"sdk.requirements"},
	}
	for _, c := range calls {
		env, code := callIn(t, state, top, c...)
		if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
			t.Errorf("untrusted %s = (%+v, %d); want WORKSPACE_UNTRUSTED before any Gradle root is looked for", c[0], env.Error, code)
		}
	}

	if code := droidyard(t, state, "trust", top).wait(t); code != 0 {
		t.Fatalf("droidyard trust %s exited %d", top, code)
	}
	for _, c := range calls {
		env, code := callIn(t, state, top, c...)
		if code == 0 || env.Error == nil || env.Error.Code != capability.ErrProjectNoGradleRoot {
			t.Errorf("trusted %s = (%+v, %d); want PROJECT_NO_GRADLE_ROOT", c[0], env.Error, code)
			continue
		}
		got, _ := env.Error.Details["candidates"].([]any)
		if len(got) != 2 || got[0] != "dev" || got[1] != "tools/x" {
			t.Errorf("%s: details.candidates = %v; want [dev tools/x]", c[0], env.Error.Details["candidates"])
		}
		if !strings.Contains(env.Error.NextStep, "gradleRoot") {
			t.Errorf("%s: nextStep %q does not name the gradleRoot key", c[0], env.Error.NextStep)
		}
	}
	if n := starts(t, events); n != 0 {
		t.Errorf("a sidecar was spawned for a project with no Gradle root (%d starts); want none", n)
	}

	// build.lastResult reads the history, which has nothing to do with the Gradle root.
	env, code := callIn(t, state, top, "build.lastResult")
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrBuildNotFound {
		t.Errorf("build.lastResult = (%+v, %d); want BUILD_NOT_FOUND", env.Error, code)
	}

	// The choice is written to the project file; the next call uses it, without a reopen.
	if err := os.MkdirAll(filepath.Join(top, ".droidyard"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(top, ".droidyard", "project.json"), []byte(`{"schemaVersion":1,"gradleRoot":"tools/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env, code = callIn(t, state, top, "build.tasks")
	if code != 0 || env.Error != nil {
		t.Fatalf("build.tasks after the choice exited %d: %+v", code, env.Error)
	}
	want := []string{"opened " + filepath.Join(top, "tools", "x")}
	var opened []string
	for _, l := range eventLines(t, events) {
		if strings.HasPrefix(l, "opened ") {
			opened = append(opened, l)
		}
	}
	if !reflect.DeepEqual(opened, want) {
		t.Errorf("the sidecar opened %q; want %q", opened, want)
	}

	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	srv.wait(t)
}

// TestCloseClosesTheGradleRootConnection is ADR-0075 §8 on a real daemon over the fake
// sidecar: closing the top's workspace after build.tasks releases the connection to the
// Gradle root, <top>/dev, never to the top. With a second workspace open at <top>/dev
// that also ran build.tasks, the connection is shared: closing the top writes no `closed`
// line, and closing both writes one. Seen failing with the "no other open workspace"
// test removed from Close (the top's close then closes the shared connection).
func TestCloseClosesTheGradleRootConnection(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg, events := fakeSidecar(t, gradleRootModel)
	top := topWith(t, ".cursor/", "dev/settings.gradle.kts")
	dev := filepath.Join(top, "dev")

	srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	srv.expect(t, "listening")
	for _, root := range []string{top, dev} {
		if code := droidyard(t, state, "trust", root).wait(t); code != 0 {
			t.Fatalf("droidyard trust %s exited %d", root, code)
		}
	}
	call := func(root string, args ...string) {
		t.Helper()
		if env, code := callIn(t, state, root, args...); code != 0 {
			t.Fatalf("%s in %s exited %d: %+v", args[0], root, code, env.Error)
		}
	}
	closedLines := func() []string {
		var out []string
		for _, l := range eventLines(t, events) {
			if strings.HasPrefix(l, "closed ") {
				out = append(out, l)
			}
		}
		return out
	}

	// The top alone: its close releases dev/.
	call(top, "workspace.open")
	call(top, "build.tasks")
	call(top, "workspace.close")
	if got, want := closedLines(), []string{"closed " + dev}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after closing the top the sidecar closed %q; want %q", got, want)
	}

	// Two workspaces on one Gradle root: the top's close leaves the connection to the other.
	call(top, "workspace.open")
	call(dev, "workspace.open")
	call(top, "build.tasks")
	call(dev, "build.tasks")
	call(top, "workspace.close")
	if got := closedLines(); len(got) != 1 {
		t.Fatalf("closing the top while the workspace at dev/ is open wrote the `closed` lines %q; want no new one", got)
	}
	call(dev, "workspace.close")
	if got, want := closedLines(), []string{"closed " + dev, "closed " + dev}; !reflect.DeepEqual(got, want) {
		t.Errorf("after closing both the sidecar closed %q; want %q", got, want)
	}

	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	srv.wait(t)
}
