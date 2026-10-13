package git

import (
	"slices"
	"strings"
	"testing"
)

// Decision (ADR-0071 §1): git.checkout of a name that is only a remote-tracking branch's
// is trackArgs's job, asked for explicitly, never switchArgs's. --no-guess is what keeps
// switchArgs from creating the local branch by itself. Control: without it git creates it.
func TestWriteSwitchArgsDoNotGuess(t *testing.T) {
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	net := e.inv(true)
	if e.revOr(r.W, "refs/heads/feature/x") != "" || e.revOr(r.W, "refs/remotes/origin/feature/x") == "" {
		t.Fatal("the fixture must have feature/x only as origin/feature/x")
	}
	res := net.run(t, r.W, switchArgs("feature/x"))
	t.Logf("switchArgs on a remote-only name: exit %d, %q", res.Code, res.firstStderr())
	if res.Code == 0 || e.revOr(r.W, "refs/heads/feature/x") != "" {
		t.Errorf("switchArgs created a local branch from a remote-tracking one (exit %d)", res.Code)
	}
	noFlag := slices.DeleteFunc(switchArgs("feature/x"), func(a string) bool { return a == "--no-guess" })
	res = net.run(t, r.W, noFlag)
	if res.Code != 0 || e.revOr(r.W, "refs/heads/feature/x") == "" {
		t.Errorf("control: without --no-guess git did not create the branch (exit %d, %q); the test cannot fail", res.Code, res.firstStderr())
	}
}

// Decision (ADR-0071 §1): the local branch trackArgs creates has the remote-tracking
// branch as its upstream whatever the user's branch.autoSetupMerge says. Control: without
// --track a user who turned auto set-up off gets a branch with no upstream.
func TestWriteTrackArgsSetTheUpstream(t *testing.T) {
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	e.g(r.W, "config", "branch.autoSetupMerge", "false")
	net := e.inv(true)
	upstream := func() string {
		out, _ := e.gErr(r.W, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "feature/x@{upstream}")
		if strings.Contains(out, "fatal") || strings.Contains(out, "error") {
			return ""
		}
		return strings.TrimSpace(out)
	}
	if res := net.run(t, r.W, trackArgs("feature/x", "origin/feature/x")); res.Code != 0 {
		t.Fatalf("trackArgs: exit %d, %q", res.Code, res.Stderr)
	}
	if got := upstream(); got != "origin/feature/x" {
		t.Errorf("upstream = %q, want origin/feature/x", got)
	}
	e.g(r.W, "switch", "-q", "main")
	e.g(r.W, "branch", "-q", "-D", "feature/x")
	noFlag := slices.DeleteFunc(trackArgs("feature/x", "origin/feature/x"), func(a string) bool { return a == "--track" })
	if res := net.run(t, r.W, noFlag); res.Code != 0 {
		t.Fatalf("control: exit %d, %q", res.Code, res.Stderr)
	}
	if got := upstream(); got != "" {
		t.Errorf("control: without --track the branch has upstream %q under autoSetupMerge=false; the test cannot fail", got)
	}
}
