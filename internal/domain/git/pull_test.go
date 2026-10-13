package git

// git.pull (plan m2n part 3, step R3; ADR-0074 §1, §2). Real git over the fixtures of
// gittest.Remote, through the write runner of write_test.go's wfx, as git.fetch's tests
// are. Each test says which decision it holds and, where it was seen failing, under what.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// pull calls git.pull and fails the test on an error.
func (f *wfx) pull() PullResult {
	f.t.Helper()
	res, err := f.call("git.pull", "ws", PullParams{})
	if err != nil {
		f.t.Fatalf("git.pull: %s (%s; cause %q; nextStep %q)", err.Code, err.Message, err.Cause, err.NextStep)
	}
	return res.(PullResult)
}

// Decision (ADR-0074 §1): git.pull fast-forwards the current branch to its upstream, fetching
// first, and reports the branch and the object ids before and after. The control is the
// state before: main is behind origin/main's remote until the pull. No hook runs (the
// fixture's hooks write markers) and the colleague's file is in the work tree.
func TestPullFastForward(t *testing.T) {
	t.Log(gitVersion(t))
	f := newWfx(t)
	before := f.e.rev(f.r.W, "main")
	pushed := f.r.PushOther(t, 1)
	if before == pushed {
		t.Fatal("control: main is already the pushed commit")
	}
	got := f.pull()
	if got.Branch != "main" || got.Before != before || got.After != pushed {
		t.Errorf("result %+v; want main, %s -> %s", got, before, pushed)
	}
	if now := f.e.rev(f.r.W, "main"); now != pushed {
		t.Errorf("main = %s after the pull, want the colleague's commit %s", now, pushed)
	}
	if _, err := os.Stat(filepath.Join(f.r.W, "other-1.txt")); err != nil {
		t.Errorf("the colleague's file is not in the work tree: %v", err)
	}
	if marks := hookMarks(t, f.r.Markers, false); marks != "" {
		t.Errorf("a hook ran during the pull: %q", marks)
	}
	if n := f.writeStarts.Load(); n != 1 {
		t.Errorf("recorded starts = %d, want one for the pull", n)
	}
}

// Decision (ADR-0074 §1): equal oids mean the branch was up to date, and nothing moves.
func TestPullUpToDate(t *testing.T) {
	f := newWfx(t)
	head := f.e.rev(f.r.W, "main")
	before := treeState(t, f.e, f.r.W)
	got := f.pull()
	if got.Branch != "main" || got.Before != head || got.After != head {
		t.Errorf("result %+v; want main at %s before and after", got, head)
	}
	if after := treeState(t, f.e, f.r.W); after != before {
		t.Errorf("an up-to-date pull changed the repository:\n%s\nvs\n%s", before, after)
	}
}

// Decision (ADR-0074 §1, §2): a pull never merges or rebases. A branch with a commit its
// upstream lacks is refused with git's words and a nextStep, and HEAD, every ref and the work
// tree are as they were. The colleague's commit is fetched first through git.fetch, so the
// comparison is not about the remote-tracking ref the pull's own fetch moves. Seen failing
// with --ff-only (and the -c belts) removed from pullArgs: git merged, main moved to a merge
// commit.
func TestPullDiverged(t *testing.T) {
	f := newWfx(t)
	f.e.write(f.r.W, "local.txt", "local\n")
	f.e.commit(f.r.W)
	f.r.PushOther(t, 1)
	if _, err := f.call("git.fetch", "ws", FetchParams{}); err != nil {
		t.Fatalf("git.fetch: %+v", err)
	}
	before := treeState(t, f.e, f.r.W)
	_, err := f.call("git.pull", "ws", PullParams{})
	wantErrCode(t, err, CodeFailed)
	t.Logf("cause %q, nextStep %q", err.Cause, err.NextStep)
	if !strings.Contains(err.Cause, "fast-forward") {
		t.Errorf("cause %q does not say git could not fast-forward", err.Cause)
	}
	if err.NextStep == "" {
		t.Error("a diverged branch is refused without a nextStep")
	}
	if after := treeState(t, f.e, f.r.W); after != before {
		t.Errorf("a refused pull changed the repository:\n%s\nvs\n%s", before, after)
	}
}

// Decision (ADR-0074 §2): a detached HEAD is refused before any write, saying so. Seen
// failing with the detached check removed from pull: git ran and its own words came back.
func TestPullDetached(t *testing.T) {
	f := newWfx(t)
	f.r.PushOther(t, 1)
	f.e.g(f.r.W, "checkout", "-q", "--detach")
	before := f.e.rev(f.r.W, "HEAD") + f.e.g(f.r.W, "for-each-ref")
	_, err := f.call("git.pull", "ws", PullParams{})
	wantErrCode(t, err, CodeFailed)
	if !strings.Contains(err.Message, "detached") || !strings.Contains(err.Cause, "not on a branch") || err.NextStep == "" {
		t.Errorf("error %+v; want the detached HEAD named, with a cause and a nextStep", err)
	}
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started for a detached HEAD, want none", n)
	}
	if after := f.e.rev(f.r.W, "HEAD") + f.e.g(f.r.W, "for-each-ref"); after != before {
		t.Error("a refused pull changed the repository")
	}
}

// Decision (ADR-0074 §2): a branch with no upstream is refused before any write, naming the
// branch, with a cause and a nextStep. The control: main, which has one, pulls.
func TestPullNoUpstream(t *testing.T) {
	f := newWfx(t)
	f.e.g(f.r.W, "checkout", "-q", "-b", "no-upstream")
	_, err := f.call("git.pull", "ws", PullParams{})
	wantErrCode(t, err, CodeFailed)
	if !strings.Contains(err.Message, "no-upstream") || !strings.Contains(err.Message, "no upstream") || err.Cause == "" || err.NextStep == "" {
		t.Errorf("error %+v; want the branch named as having no upstream, with a cause and a nextStep", err)
	}
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started for a branch with no upstream, want none", n)
	}
	f.e.g(f.r.W, "checkout", "-q", "main")
	if got := f.pull(); got.Branch != "main" {
		t.Errorf("control: main pulled as %+v", got)
	}
}

// Decision (ADR-0071 §2, ADR-0074 §1): a pull on an untrusted root starts no git, not even
// a read: WORKSPACE_UNTRUSTED with the nextStep to trust the project, and the branch has not
// moved. Seen failing with the gate not installed on the descriptor: the pull ran.
func TestPullUntrusted(t *testing.T) {
	for name, hook := range map[string]func(*wfx){
		"the daemon's hook refuses": func(f *wfx) {
			f.authorize = func(ctx context.Context, call *capability.Call) (any, error) {
				return nil, policy.UntrustedError("/the/root")
			}
		},
		"no hook": func(f *wfx) { f.authorize = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newWfx(t)
			hook(f)
			f.register()
			before := f.e.rev(f.r.W, "main")
			f.r.PushOther(t, 1)
			out := f.until(f.start("git.pull", "ws", PullParams{}), 10*time.Second)
			if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != capability.ErrWorkspaceUntrusted {
				t.Fatalf("outcome %s, error %+v; want failed with WORKSPACE_UNTRUSTED", out.State, out.Error)
			}
			if !strings.Contains(out.Error.NextStep, "droidyard trust") {
				t.Errorf("nextStep %q does not say how to trust the project", out.Error.NextStep)
			}
			if w, r := f.writeStarts.Load(), f.readStarts.Load(); w != 0 || r != 0 {
				t.Errorf("starts: %d write gits and %d reads on an untrusted root, want none", w, r)
			}
			if now := f.e.rev(f.r.W, "main"); now != before {
				t.Errorf("main moved from %s to %s on an untrusted root", before, now)
			}
		})
	}
}

// Decision (ADR-0071 §3): no hook of the repository runs in a pull, whether it is in
// .git/hooks or named by core.hooksPath in the repository's own configuration. The control,
// in the same test: a plain git pull with that configuration runs the post-merge hook, so
// the silence after the daemon's pull is the invocation's doing. Seen failing with
// `core.hooksPath=/dev/null` removed from writeFixedPrefix: the hook ran.
func TestPullNoHook(t *testing.T) {
	f := newWfx(t)
	hooks := t.TempDir()
	for _, dir := range []string{filepath.Join(f.r.W, ".git", "hooks"), hooks} {
		for _, name := range []string{"post-merge", "post-checkout", "reference-transaction"} {
			body := "#!/bin/sh\necho \"" + name + " $PWD\" >> '" + f.r.Markers + "'\ncat >/dev/null\n"
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.e.g(f.r.W, "config", "core.hooksPath", hooks)

	// The control: the user's own git runs the hook for a fast-forward.
	f.r.PushOther(t, 1)
	f.e.g(f.r.W, "pull", "-q", "--ff-only")
	if marks := hookMarks(t, f.r.Markers, true); !strings.Contains(marks, "post-merge") {
		t.Fatalf("control: a plain pull ran no post-merge hook (%q): the hooks are not set up", marks)
	}

	pushed := f.r.PushOther(t, 2)
	if got := f.pull(); got.After != pushed {
		t.Fatalf("the pull ended at %s, want %s", got.After, pushed)
	}
	if marks := hookMarks(t, f.r.Markers, false); marks != "" {
		t.Errorf("a hook ran during the daemon's pull: %q", marks)
	}
}

// Decision (ADR-0071 §3, as for git.fetch): a cancelled pull leaves nothing of its group,
// the fetch git starts and the transport under it included, and no pid record. The remote's
// uploadpack hangs, once dying to SIGTERM and once ignoring it. Seen failing with the runner
// signalling git's pid only: the uploadpack outlived the call.
func TestPullCancel(t *testing.T) {
	t.Log(gitVersion(t))
	for _, stubborn := range []bool{false, true} {
		name := "uploadpack dies to SIGTERM"
		if stubborn {
			name = "uploadpack ignores SIGTERM"
		}
		t.Run(name, func(t *testing.T) {
			f := newWfx(t)
			pidFile := f.hung(stubborn)
			id := f.start("git.pull", "ws", PullParams{})
			child := pidIn(t, pidFile)
			pgid := mustPgid(child)
			if len(f.groups()) != 1 || pgid != f.groups()[0] {
				t.Fatalf("the uploadpack %d is in group %d, the recorded git's is %v", child, pgid, f.groups())
			}
			if err := f.ops.Cancel(id); err != nil {
				t.Fatal(err)
			}
			out := f.until(id, 10*time.Second)
			if out.State != capability.StateCancelled {
				t.Errorf("outcome %s, error %+v; want cancelled", out.State, out.Error)
			}
			if groupAlive(pgid) || syscall.Kill(child, 0) != syscall.ESRCH {
				t.Errorf("group %d or the uploadpack %d outlived the cancelled pull", pgid, child)
			}
			if recs := f.records(); len(recs) != 0 {
				t.Errorf("processes.json holds %+v after the cancel, want none", recs)
			}
		})
	}
}

// Decision (ADR-0074 §2, ADR-0071 §5): local changes in the way are git's refusal, in
// git's words naming the file, and nothing is stashed or merged: HEAD, refs and the work
// tree (the user's change included) are as they were. Two shapes: an untracked file the
// pull would overwrite, and a tracked file both sides changed. Seen failing with
// --no-autostash and merge.autoStash=false removed and autostash set in the repository's
// configuration: the second shape was stashed and applied, and the tree changed.
func TestPullLocalChangesInTheWay(t *testing.T) {
	t.Run("an untracked file", func(t *testing.T) {
		f := newWfx(t)
		f.r.PushOther(t, 1)
		f.e.write(f.r.W, "other-1.txt", "mine\n")
		f.fetchFirst()
		before := treeState(t, f.e, f.r.W)
		_, err := f.call("git.pull", "ws", PullParams{})
		wantErrCode(t, err, CodeFailed)
		t.Logf("cause %q, nextStep %q", err.Cause, err.NextStep)
		if !strings.Contains(err.Cause, "other-1.txt") {
			t.Errorf("cause %q does not name the file in the way", err.Cause)
		}
		if after := treeState(t, f.e, f.r.W); after != before {
			t.Error("a refused pull changed the repository")
		}
	})
	t.Run("a modified tracked file", func(t *testing.T) {
		f := newWfx(t)
		f.e.g(f.r.W, "config", "pull.rebase", "true")
		f.e.g(f.r.W, "config", "rebase.autoStash", "true")
		f.e.g(f.r.W, "config", "merge.autoStash", "true")
		f.e.write(f.r.Other, "shared.txt", "theirs\n")
		f.e.commit(f.r.Other)
		f.e.g(f.r.Other, "push", "-q", "origin", "main")
		f.e.write(f.r.W, "shared.txt", "mine\n")
		f.fetchFirst()
		before := treeState(t, f.e, f.r.W)
		_, err := f.call("git.pull", "ws", PullParams{})
		wantErrCode(t, err, CodeFailed)
		t.Logf("cause %q, nextStep %q", err.Cause, err.NextStep)
		if !strings.Contains(err.Cause, "shared.txt") {
			t.Errorf("cause %q does not name the file in the way", err.Cause)
		}
		if after := treeState(t, f.e, f.r.W); after != before {
			t.Error("a refused pull changed the repository")
		}
		if stash := f.e.g(f.r.W, "stash", "list"); stash != "" {
			t.Errorf("the pull stashed: %q", stash)
		}
	})
}

// fetchFirst brings the remote's commits in with git.fetch, so a later comparison of the
// repository is not about the remote-tracking ref the pull's own fetch would move.
func (f *wfx) fetchFirst() {
	f.t.Helper()
	if _, err := f.call("git.fetch", "ws", FetchParams{}); err != nil {
		f.t.Fatalf("git.fetch: %+v", err)
	}
}

// Decision (ADR-0074 §1): git.pull takes no parameter, and a stray one is refused as every
// capability refuses an unknown parameter, before any git starts.
func TestPullTakesNoParameter(t *testing.T) {
	f := newWfx(t)
	raw, err := json.Marshal(map[string]string{"remote": "origin"})
	if err != nil {
		t.Fatal(err)
	}
	id, serr := f.ops.Start(t.Context(), capability.Request{Capability: "git.pull", Workspace: "ws", Params: raw})
	if serr == nil {
		t.Fatalf("a stray parameter was accepted (operation %s)", id)
	}
	if ce := capability.AsError(serr); ce.Code != capability.ErrInvalidParams {
		t.Errorf("error %+v, want REQUEST_INVALID_PARAMS", ce)
	}
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started for a refused call", n)
	}
}

// TestPullNeverPrunes: a pull's fetch prunes nothing, as git.fetch does not (ADR-0071 §1),
// even when the repository sets remote.origin.prune and remote.origin.pruneTags, which
// outrank fetch.prune: a gone remote branch and a tag the remote lacks are both kept.
// Seen failing with --no-prune removed (the verifier's probe: -c fetch.prune=false alone prunes).
func TestPullNeverPrunes(t *testing.T) {
	f := newWfx(t)
	f.e.g(f.r.Origin, "branch", "gone", "main")
	f.e.g(f.r.Origin, "tag", "gonetag", "main")
	f.fetchFirst()
	f.e.g(f.r.W, "fetch", "-q", "origin", "refs/tags/gonetag:refs/tags/gonetag")
	if f.e.revOr(f.r.W, "refs/remotes/origin/gone") == "" {
		t.Fatal("control: origin/gone was not fetched")
	}
	f.e.g(f.r.Origin, "branch", "-D", "gone")
	f.e.g(f.r.Origin, "tag", "-d", "gonetag")
	pushed := f.r.PushOther(t, 1)
	f.e.g(f.r.W, "config", "remote.origin.prune", "true")
	f.e.g(f.r.W, "config", "remote.origin.pruneTags", "true")
	if got := f.pull(); got.After != pushed {
		t.Fatalf("after %s, want the fast-forward to %s", got.After, pushed)
	}
	if f.e.revOr(f.r.W, "refs/remotes/origin/gone") == "" {
		t.Error("refs/remotes/origin/gone was pruned by git.pull under remote.origin.prune=true")
	}
	if f.e.revOr(f.r.W, "refs/tags/gonetag") == "" {
		t.Error("the tag gonetag was pruned by git.pull under remote.origin.pruneTags=true")
	}
}

// TestPullIgnoresTheRepositorysPullConfig: whatever the repository says about merging,
// rebasing or recursing for its own pulls, git.pull fast-forwards and makes no merge commit.
func TestPullIgnoresTheRepositorysPullConfig(t *testing.T) {
	f := newWfx(t)
	for _, kv := range [][2]string{{"branch.main.mergeoptions", "--no-ff"}, {"pull.ff", "false"}, {"merge.ff", "false"}, {"branch.main.rebase", "true"}, {"pull.rebase", "merges"}, {"submodule.recurse", "true"}, {"fetch.recurseSubmodules", "true"}} {
		f.e.g(f.r.W, "config", kv[0], kv[1])
	}
	pushed := f.r.PushOther(t, 1)
	if got := f.pull(); got.After != pushed {
		t.Errorf("after %s, want the fast-forward to %s", got.After, pushed)
	}
}
