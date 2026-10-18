package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
)

// useGradleRoot writes the project file's gradleRoot choice, as the UI does: the next
// call of the workspace resolves it, with no reopen.
func useGradleRoot(t *testing.T, top, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(top, ".droidyard"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"schemaVersion":1,"gradleRoot":"` + rel + `"}`
	if err := os.WriteFile(filepath.Join(top, ".droidyard", "project.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// linesWithPrefix is the fake sidecar's log entries starting with prefix.
func linesWithPrefix(t *testing.T, events, prefix string) []string {
	t.Helper()
	var out []string
	for _, l := range eventLines(t, events) {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// waitLines polls the fake's log until it holds n entries starting with prefix.
func waitLines(t *testing.T, events, prefix string, n int) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if len(linesWithPrefix(t, events, prefix)) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake's log never held %d %q entries; it is %q", n, prefix, eventLines(t, events))
		}
	}
}

// TestSwitchedGradleRootCloses is ADR-0092 §1, §2 on a real daemon over the fake sidecar:
// a workspace whose gradleRoot is changed closes the connection to the root it switched
// away from (the sidecar logs `closed`), unless another open workspace still uses that
// root, and not before a build in flight on it ends, however long the build runs. A
// switch back reopens the root and lists its tasks.
//
// Seen failing on main (the old root kept until the workspace closes: no `closed`), and
// with the close bounded by the 5 s of workspace close's closeRoot instead of waiting for
// the build (the held-build case: `closed` while the build ran).
func TestSwitchedGradleRootCloses(t *testing.T) {
	t.Parallel()

	// setup starts a daemon over a fake sidecar scripted by cfg, with a trusted top holding
	// dev/ and tools/x/. It returns what the cases drive.
	setup := func(t *testing.T, cfg gradletest.Config) (state, top, events string, stop func()) {
		t.Helper()
		state = shortDir(t)
		events = filepath.Join(t.TempDir(), "events")
		cfg.Events = events
		cfg.Model = gradletest.MustModel(gradleRootModel)
		path := gradletest.WriteConfig(t, cfg)
		top = topWith(t, "dev/settings.gradle.kts", "tools/x/settings.gradle.kts")
		srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + path, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
		srv.expect(t, "listening")
		for _, root := range []string{top, filepath.Join(top, "dev")} {
			if code := droidyard(t, state, "trust", root).wait(t); code != 0 {
				t.Fatalf("droidyard trust %s exited %d", root, code)
			}
		}
		return state, top, events, func() {
			if code := droidyard(t, state, "stop").wait(t); code != 0 {
				t.Errorf("stop exited %d", code)
			}
			srv.wait(t)
		}
	}
	call := func(t *testing.T, state, dir string, args ...string) {
		t.Helper()
		if env, code := callIn(t, state, dir, args...); code != 0 {
			t.Fatalf("%s in %s exited %d: %+v", args[0], dir, code, env.Error)
		}
	}

	t.Run("a switch closes the root left behind and a switch back reopens it", func(t *testing.T) {
		t.Parallel()
		state, top, events, stop := setup(t, gradletest.Config{})
		dev, tools := filepath.Join(top, "dev"), filepath.Join(top, "tools", "x")
		call(t, state, top, "workspace.open")

		useGradleRoot(t, top, "dev")
		call(t, state, top, "build.tasks")
		if got := linesWithPrefix(t, events, "opened "); len(got) != 1 || got[0] != "opened "+dev {
			t.Fatalf("the first call opened %q; want only %s", got, dev)
		}
		if got := linesWithPrefix(t, events, "closed "); len(got) != 0 {
			t.Fatalf("a first call closed %q; want nothing", got)
		}

		useGradleRoot(t, top, "tools/x")
		call(t, state, top, "build.tasks")
		waitLines(t, events, "closed ", 1)
		if got := linesWithPrefix(t, events, "closed "); len(got) != 1 || got[0] != "closed "+dev {
			t.Errorf("after the switch the sidecar closed %q; want only %s", got, dev)
		}
		if !contains(eventLines(t, events), "opened "+tools) {
			t.Errorf("the sidecar's log is %q; want it to have opened %s", eventLines(t, events), tools)
		}

		useGradleRoot(t, top, "dev")
		env, code := callIn(t, state, top, "build.tasks")
		if code != 0 || env.Error != nil || !strings.Contains(string(env.Result), ":app:assembleDebug") {
			t.Errorf("build.tasks after the switch back = (%s, %+v, %d); want the tasks listed", env.Result, env.Error, code)
		}
		if got := linesWithPrefix(t, events, "opened "); len(got) != 3 || got[2] != "opened "+dev {
			t.Errorf("after the switch back the sidecar opened %q; want dev/ opened again", got)
		}
		stop()
	})

	t.Run("a root another open workspace uses is kept", func(t *testing.T) {
		t.Parallel()
		state, top, events, stop := setup(t, gradletest.Config{})
		dev := filepath.Join(top, "dev")
		call(t, state, top, "workspace.open")
		call(t, state, dev, "workspace.open")

		useGradleRoot(t, top, "dev")
		call(t, state, top, "build.tasks")
		call(t, state, dev, "build.tasks") // a call that has resolved dev/ itself
		useGradleRoot(t, top, "tools/x")
		call(t, state, top, "build.tasks")
		waitLines(t, events, "opened ", 2)
		time.Sleep(700 * time.Millisecond) // the close, were it to come, is a goroutine of its own
		if got := linesWithPrefix(t, events, "closed "); len(got) != 0 {
			t.Errorf("the switch closed %q while the workspace at dev/ is open and uses it; want nothing", got)
		}
		stop()
	})

	t.Run("a build in flight finishes before the close, however long it runs", func(t *testing.T) {
		t.Parallel()
		gate := filepath.Join(t.TempDir(), "gate")
		build := &gradletest.Build{Steps: []gradletest.Step{{Kind: "gate", Path: gate}}}
		state, top, events, stop := setup(t, gradletest.Config{Build: build})
		dev := filepath.Join(top, "dev")
		call(t, state, top, "workspace.open")

		useGradleRoot(t, top, "dev")
		// `call` waits for the operation, so the build is left running and read at the end.
		run := droidyardIn(t, state, top, "call", "--no-autostart", "build.run", "--params", `{"tasks":["assembleDebug"]}`)
		waitLines(t, events, "served build.run", 1)

		// The switch happens while the build is held at its gate; longer than the 5 s a
		// workspace close allows a root's close.
		useGradleRoot(t, top, "tools/x")
		call(t, state, top, "build.tasks")
		waitLines(t, events, "opened ", 2)
		time.Sleep(6 * time.Second)
		if got := linesWithPrefix(t, events, "closed "); len(got) != 0 {
			t.Fatalf("the sidecar closed %q while the build still ran on dev/; want it after the build ends", got)
		}

		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		waitLines(t, events, "closed ", 1)
		if got := linesWithPrefix(t, events, "closed "); got[0] != "closed "+dev {
			t.Errorf("after the build ended the sidecar closed %q; want %s", got, dev)
		}
		if env, code := run.envelope(t); code != 0 || env.Error != nil {
			t.Errorf("the held build ended with exit %d, %+v; want it to finish", code, env.Error)
		}
		stop()
	})
}
