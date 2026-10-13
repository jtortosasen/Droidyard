package git

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
)

// Fact 11 (ADR-0071 §3): no lock is left when a write is ended by SIGTERM to its group
// and SIGKILL after the grace. Three writes: a fetch hung in uploadpack; a fetch held
// in the prepared state of a reference transaction, with its ref locks taken (only a
// hook can hold it there, so this one runs with the hooks on, which the daemon never
// does: it is the case for git's own lock cleanup, not for the invocation); and a
// switch held mid-write by a slow smudge filter (a trusted fixture). Control: SIGKILL
// alone. git removes its lock files from its signal handlers, which SIGKILL skips; where
// the control leaves none the test cannot tell, and says so.
func TestWriteSpike11NoLockLeft(t *testing.T) {
	t.Log(gitVersion(t))
	type held struct {
		args    []string
		started string
		inv     wInv
	}
	hungFetch := func(e *env, r *gittest.Remotes) held {
		return held{fetchArgs("origin"), e.hangingUploadpack(r.W, false), e.inv(true)}
	}
	refLocks := func(e *env, r *gittest.Remotes) held {
		started := filepath.Join(e.t.TempDir(), "hook.pid")
		hook := "#!/bin/sh\nif [ \"$1\" = prepared ]; then echo $$ > '" + started + "'; exec sleep 60; fi\ncat >/dev/null\n"
		if err := os.WriteFile(filepath.Join(r.W, ".git", "hooks", "reference-transaction"), []byte(hook), 0o755); err != nil {
			e.t.Fatal(err)
		}
		r.PushOther(e.t, 1)
		return held{fetchArgs("origin"), started, e.inv(true).without("core.hooksPath=/dev/null", "")}
	}
	heldCheckout := func(e *env, r *gittest.Remotes) held {
		started := filepath.Join(e.t.TempDir(), "smudge.pid")
		e.g(r.W, "switch", "-q", "-c", "slowb")
		e.write(r.W, ".gitattributes", "slow.txt filter=slow\n")
		e.write(r.W, "slow.txt", "slow\n")
		e.commit(r.W)
		e.g(r.W, "switch", "-q", "main")
		e.g(r.W, "config", "filter.slow.smudge", e.script("smudge.sh", "echo $$ > '"+started+"'\nexec sleep 60"))
		return held{switchArgs("slowb"), started, e.inv(true)}
	}
	for _, w := range []struct {
		name  string
		setup func(e *env, r *gittest.Remotes) held
	}{{"fetch hung in uploadpack", hungFetch}, {"fetch held with its ref locks", refLocks}, {"switch held by a slow smudge filter", heldCheckout}} {
		results := map[bool][]string{}
		for _, term := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/term=%v", w.name, term), func(t *testing.T) {
				e, r := newRemote(t)
				hd := w.setup(e, r)
				if got := locks(t, r.W); len(got) > 0 {
					t.Fatalf("locks before the write: %v", got)
				}
				h := hd.inv.start(t, r.W, hd.args, &syscall.SysProcAttr{Setpgid: true})
				waitFile(t, hd.started, 20*time.Second)
				time.Sleep(200 * time.Millisecond)
				heldLocks := locks(t, r.W)
				took, needKill, gone := h.endGroup(term, 2*time.Second)
				left := locks(t, r.W)
				tmp, _ := filepath.Glob(filepath.Join(r.W, ".git", "objects", "pack", "tmp_pack_*"))
				t.Logf("locks while hung: %v; ended in %v (SIGKILL needed %v, gone %v); locks left: %v; tmp packs left: %d", heldLocks, took.Round(time.Millisecond), needKill, gone, left, len(tmp))
				if !gone {
					t.Errorf("the group is still there")
				}
				results[term] = left
				if term && len(left) > 0 {
					t.Errorf("SIGTERM then SIGKILL left locks: %v", left)
				}
			})
		}
		if len(results[false]) == 0 {
			t.Logf("%s: UNABLE TO DISCRIMINATE: SIGKILL alone left no lock, so the absence after SIGTERM proves nothing for this write", w.name)
		}
	}
}
