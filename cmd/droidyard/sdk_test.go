package main

import (
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// TestSDKRequirementsRequiresTrust is the step 4 integration check for sdk.requirements
// (plan m1h, D12; ADR-0015 §5): with a real daemon and a fake sidecar, an untrusted root
// fails WORKSPACE_UNTRUSTED with nothing spawned, and after `droidyard trust` the same
// call runs the model fetch and spawns the one sidecar.
func TestSDKRequirementsRequiresTrust(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg, events := fakeSidecar(t, gradle.Model{Root: "", Projects: []gradle.Module{
		{Path: ":", Name: "root", Dir: "."},
	}})
	root, sub := gradleProject(t)

	srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	srv.expect(t, "listening")
	if env, code := callIn(t, state, sub, "workspace.open"); code != 0 {
		t.Fatalf("open exited %d: %+v", code, env.Error)
	}

	// Untrusted: the trust re-check refuses before the model fetch (ADR-0015 §5).
	env, code := callIn(t, state, sub, "sdk.requirements")
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
		t.Fatalf("an untrusted sdk.requirements = (%+v, %d); want WORKSPACE_UNTRUSTED", env.Error, code)
	}
	if n := starts(t, events); n != 0 {
		t.Errorf("a sidecar was spawned for an untrusted root (%d starts); want none", n)
	}

	if code := droidyard(t, state, "trust", root).wait(t); code != 0 {
		t.Fatalf("droidyard trust %s exited %d", root, code)
	}

	// Trusted: the call runs and spawns the one sidecar it fetches the model from.
	res, code := callIn(t, state, sub, "sdk.requirements")
	if code != 0 || res.Error != nil {
		t.Fatalf("a trusted sdk.requirements exited %d: %+v", code, res.Error)
	}
	if n := starts(t, events); n != 1 {
		t.Errorf("the sidecar started %d times; want one", n)
	}

	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	srv.wait(t)
}
