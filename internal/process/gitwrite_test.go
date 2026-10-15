package process

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jtortosasen/droidyard/internal/store"
)

// Decision (plan m2n P1 item 6, P2): a git write is its own session leader and its own
// group, with no terminal, so an ssh or a credential helper it starts cannot prompt, and
// its group is the unit the daemon kills. Setsid with Setpgid fails at fork (a session
// leader cannot setpgid), so the kind is given Setsid alone. Portable: both sysproc files
// carry the branch. Seen failing with the kind given the same attributes as every other
// (Setpgid, no Setsid).
func TestGitWriteSessionAttributes(t *testing.T) {
	a := sysProcAttr(GitWrite)
	if !a.Setsid || a.Setpgid {
		t.Errorf("git-write attributes Setsid=%v Setpgid=%v, want Setsid alone", a.Setsid, a.Setpgid)
	}
	if o := sysProcAttr(DeviceStream); o.Setsid || !o.Setpgid {
		t.Errorf("device-stream attributes Setsid=%v Setpgid=%v, want Setpgid alone: the other kinds are unchanged", o.Setsid, o.Setpgid)
	}
}

// Decision (plan m2n P2): SpawnRecorded writes a git-write record while the git runs (kind
// and id git-write, pid and pgid its own) and Wait removes it; the child is its own
// session and group leader (getpgid == getsid == pid), the property the kill of the whole
// group and the terminal-less transport both rest on. Seen failing with the kind spawned
// with Setpgid only (getsid is then the test's) and with the record kept after Wait.
func TestGitWriteIsRecordedAndLeadsItsOwnSession(t *testing.T) {
	s, st := newSupervisor(t)
	cmd := sleepCmd()
	r, err := s.SpawnRecorded(cmd, GitWrite)
	if err != nil {
		t.Fatalf("SpawnRecorded: %v", err)
	}
	pid := r.Pid()
	pgid, err1 := syscall.Getpgid(pid)
	sid, err2 := unix.Getsid(pid)
	if err1 != nil || err2 != nil || pgid != pid || sid != pid {
		t.Errorf("git-write child pid %d has pgid %d (%v) and sid %d (%v), want both its own pid", pid, pgid, err1, sid, err2)
	}
	recs := onDisk(t, st)
	if len(recs) != 1 || recs[0].Kind != GitWrite || recs[0].ID != string(GitWrite) || recs[0].PID != pid || recs[0].PGID != pid {
		t.Fatalf("processes.json holds %+v while the git runs, want one git-write record for pid %d", recs, pid)
	}
	syscall.Kill(-pid, syscall.SIGKILL)
	r.Wait()
	if recs := onDisk(t, st); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after Wait, want none", recs)
	}
}

// Decision (plan m2n P2, process-lifecycle.md §Crash recovery): a daemon killed while a
// git write runs leaves the write's leader alive (no death signal), so the next start's
// Reconcile finds the record and ends the whole group, the transport child included. The
// supervisor here is simply abandoned, as a killed daemon is; the leader is reaped by the
// test, which is its parent. Seen failing with Reconcile signalling the leader's pid only
// (the sleep child survives).
func TestReconcileReapsAGitWriteGroup(t *testing.T) {
	root := t.TempDir()
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Log: testLog{t}})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $! > "+pidFile+"; wait")
	r, err := s.SpawnRecorded(cmd, GitWrite)
	if err != nil {
		t.Fatal(err)
	}
	leader := r.Pid()
	// Reap the leader when it dies; Recorded.Wait is not used, since it would touch the
	// records the "next daemon" is about to read.
	go cmd.Wait()
	child := 0
	eventually(t, 5*time.Second, "the write named its child", func() bool {
		b, err := os.ReadFile(pidFile)
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		child = n
		return err == nil && n > 0
	})
	t.Cleanup(func() { syscall.Kill(-leader, syscall.SIGKILL) })

	st2, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := Reconcile(st2, &log, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "the leader reaped", func() bool { return dead(leader) })
	eventually(t, 3*time.Second, "the child of the write gone with its group", func() bool { return dead(child) })
	if !strings.Contains(log.String(), "reaped") {
		t.Errorf("Reconcile's log does not name the git write reaped:\n%s", log.String())
	}
	if recs := onDisk(t, st2); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after reconciling, want none", recs)
	}
}
