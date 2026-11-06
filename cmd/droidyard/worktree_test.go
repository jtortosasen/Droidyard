package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// gitIn runs git in dir with the caller's GIT_* and global configuration kept out, as
// git_read_test does: inside a pre-commit hook GIT_DIR and GIT_INDEX_FILE name the
// repository being committed.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	cmd := exec.Command(gitPath, append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "HOME=" + t.TempDir()}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestLinkedWorktreeInheritsTrust holds ADR-0081 through the `project` parameter
// (ADR-0137 §3): a project named by a folder inside a linked worktree is judged by the
// worktree's root, which is trusted when its main tree is, and never by the path the caller
// passed, which no configuration names. Untrusted, a call on the worktree is refused with the
// nextStep that names the main tree and spawns nothing; once the main tree is trusted, the
// same call by the worktree's root and by a folder deep inside it runs the Gradle capabilities
// in the worktree, never in the main tree. Seen failing with the trust gate given the path the
// caller passed (a subdirectory of the worktree) instead of the resolved root:
// WORKSPACE_UNTRUSTED for the subdirectory of a worktree of a trusted project.
func TestLinkedWorktreeInheritsTrust(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg, events := fakeSidecar(t, gradleRootModel)
	base := platform.Current().Canonical(t.TempDir())
	main := filepath.Join(base, "main")
	wt := filepath.Join(base, "wt")
	if err := os.MkdirAll(main, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "settings.gradle.kts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q", "-b", "main")
	gitIn(t, main, "add", "-A")
	gitIn(t, main, "commit", "-q", "-m", "c")
	gitIn(t, main, "worktree", "add", "-q", "../wt", "-b", "task")
	deep := filepath.Join(wt, "app", "src", "main")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}

	srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	srv.expect(t, "listening")

	for _, project := range []string{wt, deep} {
		env, code := callIn(t, state, "/", "build.tasks", "--project", project)
		if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
			t.Fatalf("an untrusted worktree's build.tasks by %s = (%+v, %d); want WORKSPACE_UNTRUSTED", project, env.Error, code)
		}
		if !strings.Contains(env.Error.NextStep, main) {
			t.Errorf("the nextStep is %q; want it to name the main tree %s", env.Error.NextStep, main)
		}
	}
	if n := starts(t, events); n != 0 {
		t.Errorf("a sidecar was spawned for an untrusted worktree (%d starts); want none", n)
	}

	if code := droidyard(t, state, "trust", main).wait(t); code != 0 {
		t.Fatalf("droidyard trust %s exited %d", main, code)
	}
	for _, project := range []string{wt, deep} {
		env, code := callIn(t, state, "/", "build.tasks", "--project", project)
		if code != 0 || env.Error != nil {
			t.Fatalf("a trusted worktree's build.tasks by %s exited %d: %+v", project, code, env.Error)
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
			t.Errorf("build.tasks by %s = %+v; want the fake's :app:assembleDebug", project, res.Tasks)
		}
	}
	lines := eventLines(t, events)
	if !contains(lines, "opened "+wt) || contains(lines, "opened "+main) || contains(lines, "opened "+deep) {
		t.Errorf("the sidecar's log is %q; want it to have opened %s and never %s or %s", lines, wt, main, deep)
	}

	// A folder of a repository nobody trusted stays untrusted, whatever its spelling.
	other := topWith(t, "settings.gradle.kts", "app/src/")
	if env, code := callIn(t, state, "/", "build.tasks", "--project", filepath.Join(other, "app", "src")); code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
		t.Errorf("an untrusted project's subfolder = (%+v, %d); want WORKSPACE_UNTRUSTED", env.Error, code)
	}

	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	srv.wait(t)
}

// TestUntrustedProjectSpawnsNothing holds ADR-0015 §5 and ADR-0137 §3: a call that names an
// untrusted project, by its root or by a folder inside it, is refused WORKSPACE_UNTRUSTED
// before anything is spawned (the fake Gradle sidecar records every start), and the
// refusal is the root's, so trusting the root, never the passed folder, lets the next call
// through. The deciding fact is the resolved root: a folder two levels inside a project is
// refused when its root is untrusted and admitted when it is trusted, with no trust ever
// written for the folder itself. Seen failing with the gate given the passed path: trusting
// the root did not admit the folder.
func TestUntrustedProjectSpawnsNothing(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	cfg, events := fakeSidecar(t, gradleRootModel)
	top := topWith(t, "settings.gradle.kts", "app/src/main/")
	inside := filepath.Join(top, "app", "src", "main")

	srv := droidyardEnv(t, state, "", []string{gradleFakeEnv + "=" + cfg, gradleJVMEnv + "=" + fakeJDK(t)}, "serve")
	srv.expect(t, "listening")

	for _, c := range [][]string{
		{"build.tasks", "--project", top}, {"build.tasks", "--project", inside},
		{"project.modules", "--project", inside}, {"project.variants", "--project", inside},
		{"sdk.requirements", "--project", inside},
	} {
		env, code := callIn(t, state, "/", c...)
		if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceUntrusted {
			t.Errorf("%v on an untrusted project = (%+v, %d); want WORKSPACE_UNTRUSTED", c, env.Error, code)
		}
	}
	if n := starts(t, events); n != 0 {
		t.Errorf("a sidecar was spawned for an untrusted project (%d starts); want none", n)
	}

	if code := droidyard(t, state, "trust", top).wait(t); code != 0 {
		t.Fatalf("droidyard trust %s exited %d", top, code)
	}
	if env, code := callIn(t, state, "/", "build.tasks", "--project", inside); code != 0 || env.Error != nil {
		t.Fatalf("a folder inside a trusted project exited %d: %+v", code, env.Error)
	}
	if n := starts(t, events); n != 1 {
		t.Errorf("the trusted call started the sidecar %d times; want once", n)
	}
	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Errorf("stop exited %d", code)
	}
	srv.wait(t)
}
