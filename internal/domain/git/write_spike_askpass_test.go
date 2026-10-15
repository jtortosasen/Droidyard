package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Fact 5, the askpass half (ADR-0071 §3, never a prompt). GIT_TERMINAL_PROMPT=0 stops
// git's terminal prompt only: git asks an askpass program first (GIT_ASKPASS,
// core.askPass, SSH_ASKPASS, in that order) and waits for it, whatever
// GIT_TERMINAL_PROMPT says. Two ways a user has one configured, each against a loopback
// server answering 401:
//
//   - SSH_ASKPASS in the daemon's environment: writeEnv drops it and empties GIT_ASKPASS;
//   - core.askPass in the repository's (or the user's) configuration: the empty
//     GIT_ASKPASS overrides it.
//
// The askpass script records the prompt it was given and sleeps. Control, in each case:
// the same invocation without the empty GIT_ASKPASS (and with the inherited SSH_ASKPASS)
// calls the script and waits on it; the test ends that git by its process group.
func TestWriteSpike5AskpassNeverRuns(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	for _, c := range []struct {
		name string
		// setup configures the askpass and returns the daemon's extra environment.
		setup func(e *env, repo, script string) (extra []string)
		// control is the environment of the control: the guarded one, undone.
		control func(env []string, script string) []string
	}{
		{"SSH_ASKPASS in the environment",
			func(e *env, repo, script string) []string {
				return []string{"SSH_ASKPASS=" + script, "SSH_ASKPASS_REQUIRE=force"}
			},
			func(env []string, script string) []string {
				env = slices.DeleteFunc(env, func(kv string) bool { return kv == "GIT_ASKPASS=" || strings.HasPrefix(kv, "SSH_ASKPASS_REQUIRE=") })
				return append(env, "SSH_ASKPASS="+script)
			}},
		{"core.askPass in the repository",
			func(e *env, repo, script string) []string { e.g(repo, "config", "core.askPass", script); return nil },
			func(env []string, script string) []string {
				return slices.DeleteFunc(env, func(kv string) bool { return kv == "GIT_ASKPASS=" })
			}},
		{"GIT_ASKPASS in the environment",
			func(e *env, repo, script string) []string { return []string{"GIT_ASKPASS=" + script} },
			func(env []string, script string) []string {
				env = slices.DeleteFunc(env, func(kv string) bool { return kv == "GIT_ASKPASS=" })
				return append(env, "GIT_ASKPASS="+script)
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, r := newRemote(t)
			srv := newAuthServer(t)
			e.g(r.W, "config", "remote.origin.url", srv.URL+"/r.git")
			rec := filepath.Join(t.TempDir(), "prompt")
			script := e.script("askpass.sh", "echo \"$1\" >> '"+rec+"'\nexec sleep 60")
			extra := c.setup(e, r.W, script)

			// The write invocation: fails at once, the script never runs.
			guarded := e.invWith(true, extra...)
			res := guarded.run(t, r.W, fetchArgs("origin"))
			t.Logf("write invocation: exit %d in %v, %q", res.Code, res.Took.Round(time.Millisecond), res.firstStderr())
			if res.Code == 0 || res.Took > 2*time.Second || !strings.Contains(res.Stderr, "terminal prompts disabled") {
				t.Errorf("the write invocation did not fail at once on the disabled prompt: exit %d after %v, %q", res.Code, res.Took, res.Stderr)
			}
			if _, err := os.Stat(rec); err == nil {
				b, _ := os.ReadFile(rec)
				t.Errorf("the askpass program ran: %q", b)
			}

			// Control: without the empty GIT_ASKPASS git calls it and waits.
			// The prefix's -c core.askPass= is undone as well: it alone hides every askpass
			// from git's own prompt (TestWritePrefixAskPassAloneHidesTheEnvironment).
			ctl := wInv{prefix: guarded.without("core.askPass=", "").prefix, env: c.control(slices.Clone(guarded.env), script)}
			h := ctl.start(t, r.W, fetchArgs("origin"), &syscall.SysProcAttr{Setpgid: true})
			prompt := waitFile(t, rec, 20*time.Second)
			select {
			case <-h.done:
				t.Errorf("control: git did not wait on the askpass program")
			case <-time.After(300 * time.Millisecond):
			}
			took, needKill, gone := h.endGroup(true, 2*time.Second)
			t.Logf("control: askpass called with %q and git waits; ended in %v (SIGKILL needed %v, gone %v)", strings.TrimSpace(prompt), took.Round(time.Millisecond), needKill, gone)
			if !gone {
				t.Errorf("the control's group is still there")
			}
		})
	}
}

// Fact 5, ssh's half: an ssh that has no terminal asks SSH_ASKPASS; git runs it through
// core.sshCommand, so a fake ssh sees the variables it would get. Under writeEnv it gets
// SSH_ASKPASS_REQUIRE=never and no SSH_ASKPASS. Control: the inherited environment
// reaches it unchanged.
func TestWriteSpike5SshGetsAskpassNever(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	rec := filepath.Join(t.TempDir(), "ssh-env")
	ssh := e.script("ssh.sh", "echo \"SSH_ASKPASS=${SSH_ASKPASS-unset} REQUIRE=${SSH_ASKPASS_REQUIRE-unset}\" > '"+rec+"'\nexit 1")
	e.g(r.W, "config", "core.sshCommand", ssh)
	e.g(r.W, "config", "remote.origin.url", "ssh://nobody@example.invalid/r.git")
	extra := []string{"SSH_ASKPASS=/their/askpass", "SSH_ASKPASS_REQUIRE=force"}

	got := func(i wInv) string {
		os.Remove(rec)
		res := i.run(t, r.W, fetchArgs("origin"))
		t.Logf("fetch over the fake ssh: exit %d, %q", res.Code, res.Stderr)
		b, err := os.ReadFile(rec)
		if err != nil {
			t.Fatalf("the fake ssh did not run: %v", err)
		}
		return strings.TrimSpace(string(b))
	}
	if g := got(e.invWith(true, extra...)); g != "SSH_ASKPASS=unset REQUIRE=never" {
		t.Errorf("ssh received %q under writeEnv, want SSH_ASKPASS=unset REQUIRE=never", g)
	}
	// Control: the same daemon environment, scrubbed as the reads are, hands ssh the user's.
	ctl := wInv{prefix: writeFixedPrefix(true), env: append(slices.DeleteFunc(scrubEnv(append(e.osEnviron(), extra...)), func(kv string) bool { return kv == "GIT_ALLOW_PROTOCOL=none" }), "GIT_CONFIG_NOSYSTEM=1")}
	if g := got(ctl); g != "SSH_ASKPASS=/their/askpass REQUIRE=force" {
		t.Errorf("control: ssh received %q with the inherited environment; the test cannot fail", g)
	}
}

// Decision (ADR-0073 §Consequences, plan m2n P2): the prefix's `-c core.askPass=` (empty) is
// not only for git-lfs. Alone, with GIT_ASKPASS not set and the user's SSH_ASKPASS in the
// environment, git's own prompt finds an empty askpass and never runs the program (git
// looks at GIT_ASKPASS, then core.askPass, then SSH_ASKPASS, and stops at the first that
// is set). Control: the same invocation without the pair calls the program and waits.
func TestWritePrefixAskPassAloneHidesTheEnvironment(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	srv := newAuthServer(t)
	e.g(r.W, "config", "remote.origin.url", srv.URL+"/r.git")
	rec := filepath.Join(t.TempDir(), "prompt")
	script := e.script("askpass.sh", "echo \"$1\" >> '"+rec+"'\nexec sleep 60")
	inv := e.invWith(true)
	// The environment of a daemon that has SSH_ASKPASS, with writeEnv's two variables undone.
	inv.env = append(slices.DeleteFunc(inv.env, func(kv string) bool {
		return kv == "GIT_ASKPASS=" || strings.HasPrefix(kv, "SSH_ASKPASS_REQUIRE=")
	}), "SSH_ASKPASS="+script)

	res := inv.run(t, r.W, fetchArgs("origin"))
	if _, err := os.Stat(rec); err == nil || res.Code == 0 || res.Took > 2*time.Second {
		t.Errorf("with core.askPass= alone: exit %d after %v, askpass ran %v; want a fast failure and no askpass", res.Code, res.Took, err == nil)
	}

	ctl := inv.without("core.askPass=", "")
	h := ctl.start(t, r.W, fetchArgs("origin"), &syscall.SysProcAttr{Setpgid: true})
	waitFile(t, rec, 20*time.Second)
	select {
	case <-h.done:
		t.Errorf("control: git did not wait on SSH_ASKPASS without the pair")
	case <-time.After(300 * time.Millisecond):
	}
	h.endGroup(true, 2*time.Second)
}
