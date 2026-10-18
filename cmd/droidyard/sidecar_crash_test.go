package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// TestSidecarCrashDegradesTheWorkspace is plan m2c D21 / S6, on the shipped wiring over
// a fake sidecar that dies mid-build: the workspace whose build was running goes
// degraded with BUILD_SIDECAR_CRASHED as degradedBy, on workspace.lifecycle and on
// workspace.state; another open workspace is untouched; the next successful sidecar
// start returns the crashed one to ready, clearing that cause only, so a workspace
// degraded by WORKSPACE_STATE_NEWER stays degraded through the same sequence. Without
// the workspace hooks only the Gradle client remembers the crash (workspace-state.md
// §Lifecycle).
func TestSidecarCrashDegradesTheWorkspace(t *testing.T) {
	cfg := gradletest.WriteConfig(t, gradletest.Config{
		Model: gradletest.MustModel(gradle.Model{Projects: []gradle.Module{
			{Path: ":", Name: "root", Dir: "."},
			{Path: ":app", Name: "app", Dir: "app", BuildDir: t.TempDir(), Tasks: []gradle.Task{
				{Name: "assembleDebug", Path: ":app:assembleDebug", Group: "build", Public: true},
			}},
		}}),
		Build: &gradletest.Build{
			Steps:         []gradletest.Step{{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:compileDebugKotlin"}}},
			DieAfterSteps: 1,
		},
	})
	jdk := fakeJDK(t)
	p := startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{
		env: []string{"HOME=/nonexistent-droidyard-test", "PATH="},
		gradle: &gradleSeam{
			launch: gradletest.Launch(cfg),
			finder: func([]string) *gradle.Finder {
				return &gradle.Finder{GOOS: "linux", Env: []string{"JAVA_HOME=" + jdk}, StudioHomes: func() []string { return nil }}
			},
		},
	})
	crashed, _ := gradleProject(t)
	other, _ := gradleProject(t)
	newer, _ := gradleProject(t)
	for _, root := range []string{crashed, other} {
		if code := droidyard(t, p.state, "trust", root).wait(t); code != 0 {
			t.Fatalf("droidyard trust %s exited %d", root, code)
		}
	}
	// A workspace.json of a newer version: that workspace opens degraded by
	// WORKSPACE_STATE_NEWER and must stay so.
	newerID, _ := platform.Current().WorkspaceID(newer)
	newerFile := filepath.Join(p.state, "ws", newerID, "workspace.json")
	if err := os.MkdirAll(filepath.Dir(newerFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newerFile, []byte(`{"schemaVersion":2,"root":"`+newer+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	invoke := func(name, root, params string) capability.Outcome {
		t.Helper()
		out, err := p.ops.Invoke(context.Background(), capability.Request{Capability: name, Workspace: root, Params: json.RawMessage(params), Client: capability.ClientCLI})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	for _, root := range []string{crashed, other, newer} {
		if out := invoke("workspace.open", root, ""); out.State != capability.StateCompleted {
			t.Fatalf("workspace.open %s: %+v", root, out)
		}
	}
	describe := func(root string) workspace.Info {
		t.Helper()
		out := invoke("workspace.describe", root, "")
		info, ok := out.Result.(workspace.Described)
		if !ok {
			t.Fatalf("workspace.describe %s: %+v", root, out)
		}
		return info.Info
	}
	if s := describe(newer); s.State != workspace.Degraded || s.DegradedBy == nil || s.DegradedBy.Code != workspace.ErrStateNewer {
		t.Fatalf("the newer-version workspace is %+v; want degraded by WORKSPACE_STATE_NEWER", s)
	}

	sub := p.bus.Subscribe([]string{workspace.LifecycleTopic, workspace.StateTopic}, nil)
	defer sub.Close()
	// events returns what both topics carried since the last call, as "<topic> <type> <workspace>".
	events := func() (out []string, causes map[string]capability.ErrorCode) {
		causes = map[string]capability.ErrorCode{}
		for {
			e, ok := sub.TryNext()
			if !ok {
				return out, causes
			}
			ws := ""
			if e.Workspace != nil {
				ws = *e.Workspace
			}
			out = append(out, e.Topic+" "+e.Type+" "+ws)
			var body struct {
				DegradedBy *capability.Error `json:"degradedBy"`
			}
			if err := json.Unmarshal(e.Payload, &body); err != nil {
				t.Fatal(err)
			}
			if body.DegradedBy != nil {
				causes[e.Topic+" "+ws] = body.DegradedBy.Code
			}
		}
	}
	crashedID, _ := platform.Current().WorkspaceID(crashed)

	// The build dies mid-way.
	built := invoke("build.run", crashed, `{"tasks":[":app:assembleDebug"]}`)
	if built.State != capability.StateFailed || built.Error == nil || built.Error.Code != capability.ErrBuildSidecarCrashed {
		t.Fatalf("build.run = %+v; want failed with BUILD_SIDECAR_CRASHED", built)
	}
	got, causes := events()
	wantEvents := []string{"workspace.lifecycle degraded " + crashedID, "workspace.state state " + crashedID}
	if len(got) != 2 || got[0] != wantEvents[0] || got[1] != wantEvents[1] {
		t.Errorf("the crash published %v; want %v, and nothing for another workspace", got, wantEvents)
	}
	for _, topic := range []string{workspace.LifecycleTopic, workspace.StateTopic} {
		if causes[topic+" "+crashedID] != capability.ErrBuildSidecarCrashed {
			t.Errorf("%s carried degradedBy %q; want BUILD_SIDECAR_CRASHED", topic, causes[topic+" "+crashedID])
		}
	}
	if s := describe(crashed); s.State != workspace.Degraded || s.DegradedBy == nil || s.DegradedBy.Code != capability.ErrBuildSidecarCrashed {
		t.Errorf("the crashed workspace is %+v; want degraded by BUILD_SIDECAR_CRASHED", s)
	}
	if s := describe(other); s.State != workspace.Ready || s.DegradedBy != nil {
		t.Errorf("another open workspace is %+v; want it untouched", s)
	}
	// A degraded workspace still takes calls: it is partially usable, not closed.
	if out := invoke("workspace.describe", crashed, ""); out.State != capability.StateCompleted {
		t.Errorf("a degraded workspace refused a call: %+v", out)
	}

	// The next successful sidecar start: a model fetch for the other workspace.
	if out := invoke("project.modules", other, ""); out.State != capability.StateCompleted {
		t.Fatalf("project.modules on the other workspace = %+v; want a fresh sidecar to answer", out)
	}
	got, causes = events()
	wantEvents = []string{"workspace.lifecycle ready " + crashedID, "workspace.state state " + crashedID}
	if len(got) != 2 || got[0] != wantEvents[0] || got[1] != wantEvents[1] || len(causes) != 0 {
		t.Errorf("the restart published %v (causes %v); want %v, ready and no degradedBy", got, causes, wantEvents)
	}
	if s := describe(crashed); s.State != workspace.Ready || s.DegradedBy != nil {
		t.Errorf("after the next start the crashed workspace is %+v; want ready", s)
	}
	if s := describe(newer); s.State != workspace.Degraded || s.DegradedBy == nil || s.DegradedBy.Code != workspace.ErrStateNewer {
		t.Errorf("the newer-version workspace is %+v after the sequence; want it still degraded by WORKSPACE_STATE_NEWER", s)
	}
}

// TestSidecarDyingUnderTheRunRequestDegradesTheWorkspace: the sidecar that dies on the
// build.run request itself, before it answers {accepted}, ended a build as surely as one
// that dies mid-way (plan m2c D21): the workspace goes degraded with BUILD_SIDECAR_CRASHED.
// Without the report on the request path nothing degrades it.
func TestSidecarDyingUnderTheRunRequestDegradesTheWorkspace(t *testing.T) {
	cfg := gradletest.WriteConfig(t, gradletest.Config{
		Model: gradletest.MustModel(gradle.Model{Projects: []gradle.Module{{Path: ":", Name: "root", Dir: "."}}}),
		Build: &gradletest.Build{DieOnRun: true},
	})
	jdk := fakeJDK(t)
	p := startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{
		env: []string{"HOME=/nonexistent-droidyard-test", "PATH="},
		gradle: &gradleSeam{
			launch: gradletest.Launch(cfg),
			finder: func([]string) *gradle.Finder {
				return &gradle.Finder{GOOS: "linux", Env: []string{"JAVA_HOME=" + jdk}, StudioHomes: func() []string { return nil }}
			},
		},
	})
	root, _ := gradleProject(t)
	if code := droidyard(t, p.state, "trust", root).wait(t); code != 0 {
		t.Fatalf("droidyard trust exited %d", code)
	}
	invoke := func(name, params string) capability.Outcome {
		t.Helper()
		out, err := p.ops.Invoke(context.Background(), capability.Request{Capability: name, Workspace: root, Params: json.RawMessage(params), Client: capability.ClientCLI})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	if out := invoke("workspace.open", ""); out.State != capability.StateCompleted {
		t.Fatalf("workspace.open: %+v", out)
	}
	built := invoke("build.run", `{"tasks":[":assembleDebug"]}`)
	if built.State != capability.StateFailed || built.Error == nil || built.Error.Code != capability.ErrBuildSidecarCrashed {
		t.Fatalf("build.run = %+v; want failed with BUILD_SIDECAR_CRASHED", built)
	}
	described, ok := invoke("workspace.describe", "").Result.(workspace.Described)
	info := described.Info
	if !ok || info.State != workspace.Degraded || info.DegradedBy == nil || info.DegradedBy.Code != capability.ErrBuildSidecarCrashed {
		t.Errorf("after the sidecar died under build.run the workspace is %+v; want degraded by BUILD_SIDECAR_CRASHED", info)
	}
}

// TestSidecarCrashDegradesTheTopWorkspace is ADR-0075 §8 on the shipped wiring: the
// project root is a repository top whose Gradle build is its dev/ folder, and the sidecar
// dies on build.run, reporting <top>/dev, a path no workspace has as its root. The
// workspace of the top, whose calls resolved dev/, goes degraded with
// BUILD_SIDECAR_CRASHED and returns to ready at the next successful sidecar start; an
// open workspace that resolved another root is untouched. Seen failing with Degrade
// matching the reported root against the workspaces' project roots (step 2's state).
func TestSidecarCrashDegradesTheTopWorkspace(t *testing.T) {
	cfg := gradletest.WriteConfig(t, gradletest.Config{
		Model: gradletest.MustModel(gradle.Model{Projects: []gradle.Module{{Path: ":", Name: "root", Dir: "."}}}),
		DieOn: "build.run",
	})
	jdk := fakeJDK(t)
	p := startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{
		env: []string{"HOME=/nonexistent-droidyard-test", "PATH="},
		gradle: &gradleSeam{
			launch: gradletest.Launch(cfg),
			finder: func([]string) *gradle.Finder {
				return &gradle.Finder{GOOS: "linux", Env: []string{"JAVA_HOME=" + jdk}, StudioHomes: func() []string { return nil }}
			},
		},
	})
	top := topWith(t, ".cursor/", "README.md", "dev/settings.gradle.kts")
	other, _ := gradleProject(t)
	for _, root := range []string{top, other} {
		if code := droidyard(t, p.state, "trust", root).wait(t); code != 0 {
			t.Fatalf("droidyard trust %s exited %d", root, code)
		}
	}
	invoke := func(name, root, params string) capability.Outcome {
		t.Helper()
		out, err := p.ops.Invoke(context.Background(), capability.Request{Capability: name, Workspace: root, Params: json.RawMessage(params), Client: capability.ClientCLI})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	for _, root := range []string{top, other} {
		if out := invoke("workspace.open", root, ""); out.State != capability.StateCompleted {
			t.Fatalf("workspace.open %s: %+v", root, out)
		}
	}
	describe := func(root string) workspace.Info {
		t.Helper()
		described, ok := invoke("workspace.describe", root, "").Result.(workspace.Described)
		if !ok {
			t.Fatalf("workspace.describe %s failed", root)
		}
		return described.Info
	}
	// The other workspace resolves its own root first; the model fetch starts the sidecar.
	if out := invoke("project.modules", other, ""); out.State != capability.StateCompleted {
		t.Fatalf("project.modules on the other workspace = %+v", out)
	}

	built := invoke("build.run", top, `{"tasks":[":assembleDebug"]}`)
	if built.State != capability.StateFailed || built.Error == nil || built.Error.Code != capability.ErrBuildSidecarCrashed {
		t.Fatalf("build.run on the top = %+v; want failed with BUILD_SIDECAR_CRASHED", built)
	}
	if s := describe(top); s.State != workspace.Degraded || s.DegradedBy == nil || s.DegradedBy.Code != capability.ErrBuildSidecarCrashed {
		t.Errorf("the top's workspace is %+v; want degraded by BUILD_SIDECAR_CRASHED", s)
	}
	if s := describe(other); s.State != workspace.Ready || s.DegradedBy != nil {
		t.Errorf("a workspace that resolved another root is %+v; want it untouched", s)
	}

	// The next successful start, on any root, returns it to ready.
	if out := invoke("project.modules", other, ""); out.State != capability.StateCompleted {
		t.Fatalf("project.modules on the other workspace = %+v; want a fresh sidecar to answer", out)
	}
	if s := describe(top); s.State != workspace.Ready || s.DegradedBy != nil {
		t.Errorf("after the next start the top's workspace is %+v; want ready", s)
	}
}
