package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	builddomain "github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// buildDaemon is the shipped wiring in process over a fake Gradle sidecar scripted by build
// (nil: a build that succeeds at once), with the roots trusted.
func buildDaemon(t *testing.T, build *gradletest.Build, trust ...string) *inProcess {
	t.Helper()
	if build == nil {
		build = &gradletest.Build{Steps: []gradletest.Step{
			{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:compileDebugKotlin", "BUILD SUCCESSFUL in 1s"}},
		}}
	}
	cfg := gradletest.WriteConfig(t, gradletest.Config{
		Model: gradletest.MustModel(gradle.Model{Projects: []gradle.Module{
			{Path: ":", Name: "root", Dir: "."},
			{Path: ":app", Name: "app", Dir: "app", BuildDir: t.TempDir(), Tasks: []gradle.Task{
				{Name: "assembleDebug", Path: ":app:assembleDebug", Group: "build", Public: true},
				{Name: "packageDebug", Path: ":app:packageDebug", Group: "build", Public: true},
			}},
		}}),
		Build: build,
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
	for _, root := range trust {
		if code := droidyard(t, p.state, "trust", root).wait(t); code != 0 {
			t.Fatalf("droidyard trust %s exited %d", root, code)
		}
	}
	return p
}

// TestBuildByProject holds ADR-0137 §3 end to end over the dummy sidecar: `build.assemble
// {project}` as the first call that names a project completes, with no call before it that
// opens anything; `build.lastResult` by another spelling of the root (a folder inside it, the
// root with a trailing slash) answers that build, since both name one project root and so
// one workspace and one build history; a project that is another root has none. Seen failing
// with the build history keyed by the path passed: lastResult by another spelling answered
// BUILD_NOT_FOUND.
func TestBuildByProject(t *testing.T) {
	t.Parallel()
	root, sub := gradleProject(t)
	other, _ := gradleProject(t)
	p := buildDaemon(t, nil, root, other)

	out, err := p.ops.Invoke(context.Background(), capability.Request{Capability: "build.assemble",
		Params: withProject(`{"module":":app","variant":"debug"}`, root), Client: capability.ClientCLI})
	if err != nil || out.State != capability.StateCompleted {
		t.Fatalf("build.assemble as the first call: %+v, %v (%+v)", out, err, out.Error)
	}
	built, ok := out.Result.(builddomain.Result)
	if !ok || built.BuildID == "" || built.Outcome != "success" {
		t.Fatalf("build.assemble answered %+v; want a finished build", out.Result)
	}
	for _, spelling := range []string{root + "/", sub, filepath.Join(root, "app", "..")} {
		last, err := p.ops.Invoke(context.Background(), capability.Request{Capability: "build.lastResult",
			Params: withProject("", spelling), Client: capability.ClientCLI})
		if err != nil || last.State != capability.StateCompleted {
			t.Errorf("build.lastResult by %s: %+v, %v (%+v)", spelling, last, err, last.Error)
			continue
		}
		if got, _ := last.Result.(builddomain.Result); got.BuildID != built.BuildID {
			t.Errorf("build.lastResult by %s answered build %q; want %q, the build of the same project", spelling, got.BuildID, built.BuildID)
		}
	}
	if last, err := p.ops.Invoke(context.Background(), capability.Request{Capability: "build.lastResult",
		Params: withProject("", other), Client: capability.ClientCLI}); err != nil || last.State != capability.StateFailed || last.Error == nil || last.Error.Code != capability.ErrBuildNotFound {
		t.Errorf("build.lastResult of another project: %+v, %v; want BUILD_NOT_FOUND, the history is the project's", last, err)
	}
}

// TestTwoSpellingsQueue holds ADR-0137 §Consequences: the build lock is the Gradle root, so
// two spellings of one project queue. A repository whose one build is dev/ is named by its
// top and by dev/ itself: two project roots, two workspaces, one Gradle root. The first build
// is held at its gate; the second, by the other spelling, is `queued` with `blockedBy` the
// first, and runs once the first ends; a build of a project with another Gradle root does not
// queue. Seen failing with the lock keyed by the workspace id: both ran at once.
func TestTwoSpellingsQueue(t *testing.T) {
	t.Parallel()
	gate := filepath.Join(t.TempDir(), "gate")
	top := topWith(t, "README.md", "dev/settings.gradle.kts")
	dev := filepath.Join(top, "dev")
	elsewhere, _ := gradleProject(t)
	p := buildDaemon(t, &gradletest.Build{Steps: []gradletest.Step{{Kind: "gate", Path: gate}}}, top, dev, elsewhere)
	topID, _ := platform.Current().WorkspaceID(top)
	devID, _ := platform.Current().WorkspaceID(dev)
	if topID == devID {
		t.Fatalf("the top and dev/ are one workspace %s; the test needs two", topID)
	}

	start := func(project string) string {
		t.Helper()
		id, err := p.ops.Start(context.Background(), capability.Request{Capability: "build.run",
			Params: withProject(`{"tasks":["assembleDebug"]}`, project), Client: capability.ClientCLI})
		if err != nil {
			t.Fatalf("build.run in %s: %v", project, err)
		}
		return id
	}
	state := func(id string) capability.Outcome {
		t.Helper()
		out, err := p.ops.Wait(context.Background(), id, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := start(top)
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	waitFor("the first build to run", func() bool { return state(first).State == capability.StateRunning })

	second := start(dev)
	out := state(second)
	if out.State != capability.StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID != first || out.BlockedBy.Capability != "build.run" {
		t.Fatalf("the second spelling of the project: %+v, blockedBy %+v; want queued behind %s", out.State, out.BlockedBy, first)
	}
	// Another Gradle root does not wait for it.
	third := start(elsewhere)
	waitFor("a build of another Gradle root to run beside the held one", func() bool { return state(third).State != capability.StateQueued })

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first, second, third} {
		done, err := p.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
		if err != nil || done.State != capability.StateCompleted {
			t.Fatalf("build %s ended %+v, %v; want completed", id, done.State, err)
		}
	}
}
