package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/file"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// trashDeadlineEnv lowers trashDeadline in a re-executed binary (installTestSeams); the
// shipped binary has no such variable.
const trashDeadlineEnv = "DROIDYARD_TEST_TRASH_DEADLINE"

// trashFake is a daemon on a state root's socket that speaks the hello and the envelopes
// as far as `droidyard trash` needs, and misbehaves where a test says. It never moves a
// file: a test reads from deletes whether the delete request arrived.
type trashFake struct {
	conns   atomic.Int32
	deletes atomic.Int32

	// silent: every connection is accepted and nothing is ever written to it.
	silent bool
	// noHelloOn: the nth connection (1-based) gets no answer to its hello.
	noHelloOn int32
	// openHangs: workspace.open is never answered.
	openHangs bool
	// wait handles operation.wait; it answers on c, or closes it.
	wait func(c net.Conn, req capability.RequestEnvelope)
}

func startTrashFake(t *testing.T, state string, f *trashFake) {
	t.Helper()
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	sock := env.SocketPath(state)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	reply := func(c net.Conn, req capability.RequestEnvelope, env capability.ResponseEnvelope) {
		env.ID = req.ID
		b, _ := json.Marshal(env)
		c.Write(append(b, '\n'))
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := f.conns.Add(1)
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go func() {
				r := bufio.NewReader(c)
				if _, err := r.ReadBytes('\n'); err != nil {
					return
				}
				if f.silent || n == f.noHelloOn {
					io.Copy(io.Discard, c) // hold the connection, answer nothing
					return
				}
				c.Write([]byte(`{"version":"fake","session":"fake"}` + "\n"))
				for {
					line, err := r.ReadBytes('\n')
					if err != nil {
						return
					}
					var req capability.RequestEnvelope
					if json.Unmarshal(line, &req) != nil {
						return
					}
					switch req.Capability {
					case "workspace.open":
						if f.openHangs {
							continue
						}
						info, _ := json.Marshal(map[string]any{"id": "ws1", "name": "p", "root": "/p", "state": "ready", "trusted": true})
						reply(c, req, capability.ResponseEnvelope{Operation: &capability.OperationStatus{ID: "op-open", State: capability.StateCompleted}, Result: info})
					case "file.delete":
						f.deletes.Add(1)
						reply(c, req, capability.ResponseEnvelope{Operation: &capability.OperationStatus{ID: "op-del", State: capability.StateRunning}})
					case capability.OperationWait:
						if f.wait != nil {
							f.wait(c, req)
						}
					}
				}
			}()
		}
	}()
}

// deleteAnswered is operation.wait's answer for a delete that trashed one path.
func deleteAnswered(c net.Conn, req capability.RequestEnvelope) {
	res, _ := json.Marshal(file.DeleteResult{Items: []file.DeletedItem{{Path: "a.kt", TrashID: "t1"}}, Done: 1})
	b, _ := json.Marshal(capability.ResponseEnvelope{ID: req.ID,
		Operation: &capability.OperationStatus{ID: "op-del", State: capability.StateCompleted}, Result: res})
	c.Write(append(b, '\n'))
}

// trashAgainst runs `droidyard trash -- a.kt` against the fake with the deadline lowered
// to deadline, and returns the run and how long it took.
func trashAgainst(t *testing.T, f *trashFake, deadline string) (trashRun, time.Duration, trashProject) {
	t.Helper()
	state := shortDir(t)
	proj := newTrashProject(t, "a.kt")
	startTrashFake(t, state, f)
	began := time.Now()
	res := runTrashEnv(t, proj.root, state, []string{trashDeadlineEnv + "=" + deadline}, "--", "a.kt")
	return res, time.Since(began), proj
}

// TestTrashGivesUpOnADaemonThatNeverAnswers holds ADR-0062 §2 for a daemon that hangs
// before the delete is sent: the command does not wait on it for ever, says nothing was
// moved, and hands back the plain rm. The deadline is lowered to 2 s, under the 10 s the
// hello itself is allowed (daemon.Dial), so only the deadline can end the run in time;
// the second case answers every hello and then never answers workspace.open, which
// nothing but the deadline bounds.
func TestTrashGivesUpOnADaemonThatNeverAnswers(t *testing.T) {
	t.Parallel()
	for name, f := range map[string]*trashFake{
		"accepts and never writes": {silent: true},
		"never answers the open":   {openHangs: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, took, proj := trashAgainst(t, f, "2s")
			if took > 8*time.Second {
				t.Errorf("trash took %s against a daemon that never answers; want it to give up at its 2s deadline", took)
			}
			if res.code != 1 || res.stdout != "" || !strings.Contains(res.stderr, "nothing was moved") ||
				!strings.Contains(res.stderr, "command rm -- a.kt") || strings.Contains(res.stderr, "unknown") {
				t.Errorf("trash exited %d with stdout %q, stderr %q; want 1, \"nothing was moved\" and the command rm line", res.code, res.stdout, res.stderr)
			}
			if !exists(proj.path("a.kt")) {
				t.Error("the file moved")
			}
			if n := f.deletes.Load(); n != 0 {
				t.Errorf("the daemon was sent %d file.delete requests; want none", n)
			}
		})
	}
}

// TestTrashAfterATransportFailureHandsBackRm holds the m2r decision for row 210: once the
// delete was sent, a connection that breaks while waiting for it leaves the outcome
// unknown, and the agent is given the plain rm all the same (an operand already trashed
// is reported missing by it; one not trashed is deleted as first asked).
func TestTrashAfterATransportFailureHandsBackRm(t *testing.T) {
	t.Parallel()
	f := &trashFake{wait: func(c net.Conn, _ capability.RequestEnvelope) { c.Close() }}
	res, _, _ := trashAgainst(t, f, "30s")
	if res.code != 1 || res.stdout != "" || !strings.Contains(res.stderr, "the outcome is unknown") ||
		!strings.Contains(res.stderr, "command rm -- a.kt") || strings.Contains(res.stderr, "nothing was moved") {
		t.Errorf("trash exited %d with stdout %q, stderr %q; want 1, \"the outcome is unknown\" and the command rm line, not \"nothing was moved\"", res.code, res.stdout, res.stderr)
	}
	if n := f.deletes.Load(); n != 1 {
		t.Errorf("the daemon was sent %d file.delete requests; want 1", n)
	}
}

// TestTrashGivesUpOnAHelloAfterTheStart holds that the third connection's hello is inside
// the deadline: the delete was started, the wait connection is accepted and its hello is
// never answered, so the outcome is unknown, not "nothing was moved", and the command
// ends at the deadline. The deadline is 2 s, under the hello's own 10 s.
func TestTrashGivesUpOnAHelloAfterTheStart(t *testing.T) {
	t.Parallel()
	f := &trashFake{noHelloOn: 3}
	res, took, _ := trashAgainst(t, f, "2s")
	if took > 8*time.Second {
		t.Errorf("trash took %s; want it to give up at its 2s deadline", took)
	}
	if res.code != 1 || !strings.Contains(res.stderr, "the outcome is unknown") ||
		!strings.Contains(res.stderr, "command rm -- a.kt") || strings.Contains(res.stderr, "nothing was moved") {
		t.Errorf("trash exited %d with stderr %q; want 1, \"the outcome is unknown\" and the command rm line", res.code, res.stderr)
	}
	if n := f.deletes.Load(); n != 1 {
		t.Errorf("the daemon was sent %d file.delete requests; want 1", n)
	}
}

// TestTrashWaitsForALongDelete holds that only the wait is unbounded: a delete that takes
// 3 s completes under a 1 s deadline, because the deadline ends when the wait connection
// is up.
func TestTrashWaitsForALongDelete(t *testing.T) {
	t.Parallel()
	f := &trashFake{wait: func(c net.Conn, req capability.RequestEnvelope) {
		time.Sleep(3 * time.Second)
		deleteAnswered(c, req)
	}}
	res, took, _ := trashAgainst(t, f, "1s")
	if res.code != 0 || res.stdout != "trashed a.kt\n" {
		t.Errorf("trash exited %d with stdout %q, stderr %q; want 0 and \"trashed a.kt\"", res.code, res.stdout, res.stderr)
	}
	if took < 3*time.Second {
		t.Errorf("trash returned after %s, before the fake's 3s delete answered", took)
	}
}

// TestTrashGateDecidesOnce holds the rule that no window exists between the main
// goroutine's "nothing was moved" and the goroutine's write: once the main side has given
// up, the request cannot be written, and once the request may be written, the main side
// cannot give up.
func TestTrashGateDecidesOnce(t *testing.T) {
	t.Parallel()
	var sink strings.Builder
	gave := &trashGate{}
	if !gave.giveUp() {
		t.Fatal("a fresh gate refused to give up")
	}
	if n, err := (gatedWriter{&sink, gave}).Write([]byte("delete")); err == nil || n != 0 || sink.Len() != 0 {
		t.Errorf("after the main side gave up, a write returned %d, %v and wrote %q; want nothing written and an error", n, err, sink.String())
	}

	sent := &trashGate{}
	if _, err := (gatedWriter{&sink, sent}).Write([]byte("delete")); err != nil || sink.String() != "delete" {
		t.Fatalf("a fresh gate refused the write: %v, wrote %q", err, sink.String())
	}
	if sent.giveUp() {
		t.Error("the main side gave up after the request was written: it would say nothing was moved")
	}
}
