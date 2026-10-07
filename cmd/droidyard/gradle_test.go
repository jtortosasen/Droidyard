package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// fakeSidecar writes a gradletest configuration answering every root with model, and
// returns the configuration's path and its event log (the fake appends "started <pid>").
func fakeSidecar(t *testing.T, model gradle.Model) (cfg, events string) {
	t.Helper()
	events = filepath.Join(t.TempDir(), "events")
	return gradletest.WriteConfig(t, gradletest.Config{Events: events, Model: gradletest.MustModel(model)}), events
}

// fakeSidecarVersion is fakeSidecar with a chosen Gradle version, so a test can drive a
// Gradle below the floor (plan m1k step 7, D9).
func fakeSidecarVersion(t *testing.T, version string, model gradle.Model) (cfg, events string) {
	t.Helper()
	events = filepath.Join(t.TempDir(), "events")
	return gradletest.WriteConfig(t, gradletest.Config{Events: events, GradleVersion: version, Model: gradletest.MustModel(model)}), events
}

// fakeJDK is a directory discovery accepts as a JDK 17.
func fakeJDK(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("JAVA_VERSION=\"17.0.11\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// starts counts the fake sidecar's starts in its event log.
func starts(t *testing.T, events string) int {
	t.Helper()
	raw, err := os.ReadFile(events)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "started ") {
			n++
		}
	}
	return n
}

// capInfo finds one capability in a system.capabilities result.
func capInfo(t *testing.T, caps system.Capabilities, name string) system.CapabilityInfo {
	t.Helper()
	for _, c := range caps.Capabilities {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("system.capabilities does not list %s", name)
	return system.CapabilityInfo{}
}

// TestProjectModulesRequiresTrustThenSharesOneSidecar is the step 5 integration check
// (plan m1g, check (a)): with a real daemon and a fake sidecar, an untrusted root fails
// project.modules WORKSPACE_UNTRUSTED with nothing spawned; after `droidyard trust` the
// same call runs, spawns the one sidecar, lists the modules from it and serves a second
// root on the same process (ADR-0019: one sidecar for every root); system.capabilities
// reports the three [gradle] capabilities available once a JVM was found.
func TestProjectModulesRequiresTrustThenSharesOneSidecar(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg, events := fakeSidecar(t, gradle.Model{Root: "", Projects: []gradle.Module{
		{Path: ":", Name: "root", Dir: "."},
		{Path: ":app", Name: "app", Dir: "app", Tasks: []gradle.Task{
			{Name: "assembleDebug", Path: ":app:assembleDebug", Group: "build", Public: true},
			{Name: "packageDebug", Path: ":app:packageDebug", Group: "build", Public: true},
		}},
	}})
	rootA, subA := gradleProject(t)
	rootB, _ := gradleProject(t)

	srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	srv.expect(t, "listening")
	if env, code := callIn(t, state, subA, "workspace.open"); code != 0 {
		t.Fatalf("open exited %d: %+v", code, env.Error)
	}

	// Untrusted: the trust re-check refuses before anything is spawned (ADR-0015 §5).
	env, code := callIn(t, state, subA, "project.modules")
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
		t.Fatalf("an untrusted project.modules = (%+v, %d); want WORKSPACE_UNTRUSTED", env.Error, code)
	}
	if n := starts(t, events); n != 0 {
		t.Errorf("a sidecar was spawned for an untrusted root (%d starts); want none", n)
	}

	// A JVM was found, so the three [gradle] capabilities are available.
	caps, code := callIn(t, state, subA, "system.capabilities")
	if code != 0 {
		t.Fatalf("system.capabilities exited %d: %+v", code, caps.Error)
	}
	var listed system.Capabilities
	if err := json.Unmarshal(caps.Result, &listed); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project.modules", "project.variants", "build.tasks"} {
		if info := capInfo(t, listed, name); !info.Available {
			t.Errorf("%s is unavailable (%s) though a JVM 17 was found", name, info.Unavailable)
		}
	}

	for _, root := range []string{rootA, rootB} {
		if code := droidyard(t, state, "trust", root).wait(t); code != 0 {
			t.Fatalf("droidyard trust %s exited %d", root, code)
		}
	}

	// Trusted: the call runs, spawns the one sidecar and lists the modules from it.
	modules, code := callIn(t, state, subA, "project.modules")
	if code != 0 || modules.Error != nil {
		t.Fatalf("a trusted project.modules exited %d: %+v", code, modules.Error)
	}
	var res struct {
		Modules []struct {
			Path string `json:"path"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(modules.Result, &res); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, m := range res.Modules {
		paths = append(paths, m.Path)
	}
	if strings.Join(paths, ",") != ":,:app" {
		t.Errorf("project.modules = %v; want the fake's : and :app", paths)
	}

	// The second root opens its workspace and calls project.modules; the one sidecar
	// process serves both (ADR-0019). Its model is per root, but its process is not.
	if env, code := callIn(t, state, rootB, "workspace.open"); code != 0 {
		t.Fatalf("open B exited %d: %+v", code, env.Error)
	}
	if env, code := callIn(t, state, rootB, "project.modules"); code != 0 {
		t.Fatalf("project.modules on the second root exited %d: %+v", code, env.Error)
	}
	if n := starts(t, events); n != 1 {
		t.Errorf("the sidecar started %d times; want one process serving both roots", n)
	}

	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	srv.wait(t)
}

// TestBelowFloorHatchIsReadFromTheDaemonConfiguration is plan m1k step 7's daemon-level
// wiring (D9; ADR-0018 rule 3): the daemon reads `gradle.allowBelowFloor` from the
// configuration at start, so a Gradle below the floor serves the project model when the
// key is set and is refused BUILD_GRADLE_UNSUPPORTED — its nextStep naming the hatch —
// when it is not. Seen failing when the daemon passes no key to the Gradle client.
func TestBelowFloorHatchIsReadFromTheDaemonConfiguration(t *testing.T) {
	t.Parallel()
	model := gradle.Model{Root: "", Projects: []gradle.Module{{Path: ":app", Name: "app", Dir: "app"}}}

	run := func(t *testing.T, allow bool) (capability.ResponseEnvelope, int) {
		t.Helper()
		state := shortDir(t)
		cfg, _ := fakeSidecarVersion(t, "7.6.4", model)
		root, sub := gradleProject(t)
		configDir := t.TempDir()
		body := `{"schemaVersion":1,"trust":{"roots":[` + strconv.Quote(root) + `]},"gradle":{"allowBelowFloor":` + strconv.FormatBool(allow) + `}}`
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		env := []string{"DROIDYARD_CONFIG_DIR=" + configDir, gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}
		srv := droidyardEnv(t, state, "", env, "serve")
		srv.expect(t, "listening")
		if _, code := callIn(t, state, sub, "workspace.open"); code != 0 {
			t.Fatalf("workspace.open exited %d", code)
		}
		res, code := callIn(t, state, sub, "project.modules")
		if c := droidyardEnv(t, state, "", env, "stop").wait(t); c != 0 {
			t.Errorf("stop exited %d", c)
		}
		srv.wait(t)
		return res, code
	}

	t.Run("with the hatch", func(t *testing.T) {
		res, code := run(t, true)
		if code != 0 || res.Error != nil {
			t.Fatalf("project.modules under the hatch = (%+v, %d); want it to run", res.Error, code)
		}
	})
	t.Run("without the hatch", func(t *testing.T) {
		res, code := run(t, false)
		if code == 0 || res.Error == nil || res.Error.Code != capability.ErrBuildGradleUnsupported {
			t.Fatalf("project.modules without the hatch = (%+v, %d); want BUILD_GRADLE_UNSUPPORTED", res.Error, code)
		}
		if !strings.Contains(res.Error.NextStep, "gradle.allowBelowFloor") {
			t.Errorf("the refusal's nextStep %q does not name the hatch", res.Error.NextStep)
		}
	})
}

// TestSystemCapabilitiesReportsGradleUnavailableWithoutAJVM pins the other half of the
// step 5 check: with no JVM 17 (this package's TestMain discovers none), system
// .capabilities reports the three [gradle] capabilities unavailable, and the reason
// names the floor and what to set (plan m1g D22) rather than a generic message.
func TestSystemCapabilitiesReportsGradleUnavailableWithoutAJVM(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	env, code := p.call(t, "system.capabilities")
	if code != 0 {
		t.Fatalf("system.capabilities exited %d: %+v", code, env.Error)
	}
	var caps system.Capabilities
	if err := json.Unmarshal(env.Result, &caps); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project.modules", "project.variants", "build.tasks"} {
		info := capInfo(t, caps, name)
		if info.Available {
			t.Errorf("%s is available though no JVM 17 can be found", name)
		}
		if !strings.Contains(info.Unavailable, "17") {
			t.Errorf("%s's reason %q does not name the JVM floor", name, info.Unavailable)
		}
	}
}
