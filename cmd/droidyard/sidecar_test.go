package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	proc "github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/process/processtest"
)

// The environment a re-executed daemon reads its test seams from.
const (
	drainEnv    = "DROIDYARD_TEST_DRAIN"
	shutdownEnv = "DROIDYARD_TEST_SHUTDOWN"
	httpPortEnv = "DROIDYARD_TEST_HTTP_PORT"
	// launcherEnv names the file a re-executed `ui` writes the URL it would have opened
	// to: no test opens a browser.
	launcherEnv = "DROIDYARD_TEST_LAUNCHER"
)

// installTestSeams runs in a re-executed binary before main: it adds the test-only
// fixture.sidecar and shortens the bounds the test names (plan m1c step 7, R17).
func installTestSeams() {
	testCapabilities = registerSidecarFixture
	openURL = func(url string) error {
		path := os.Getenv(launcherEnv)
		if path == "" {
			return fmt.Errorf("the browser launcher was called, and the test set no %s", launcherEnv)
		}
		return os.WriteFile(path, []byte(url), 0o600)
	}
	// Test daemons bind an ephemeral port unless a test asks for one.
	httpPort = 0
	if v, err := strconv.Atoi(os.Getenv(httpPortEnv)); err == nil {
		httpPort = v
	}
	if d, err := time.ParseDuration(os.Getenv(drainEnv)); err == nil {
		testDrain = d
	}
	if d, err := time.ParseDuration(os.Getenv(shutdownEnv)); err == nil {
		shutdownTimeout = d
	}
}

// sidecarParams are fixture.sidecar's parameters.
type sidecarParams struct {
	// Events is the dummy's event file; the fixture appends to it too.
	Events string `json:"events" jsonschema:"required"`
	// Options are the dummy's (processtest.Sidecar).
	Options    []string `json:"options,omitempty"`
	IdleMs     int      `json:"idleMs,omitempty"`
	ShutdownMs int      `json:"shutdownMs,omitempty"`
	// ReleaseFile, if set, holds the lease until the file exists; and, unless
	// IgnoreContext, until the operation is cancelled.
	ReleaseFile   string `json:"releaseFile,omitempty"`
	IgnoreContext bool   `json:"ignoreContext,omitempty"`
}

type sidecarResult struct {
	Pid int `json:"pid"`
}

func appendEvent(path, event string) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err == nil {
		f.WriteString(event + "\n")
		f.Close()
	}
}

// registerSidecarFixture adds fixture.sidecar: Use of processtest's dummy in the
// call's workspace, one round trip on its stdin and output, then the release. A held
// one appends "lease held", waits, appends "lease released", releases, and then uses
// the dummy once more with its own context, by then cancelled by a close, appending
// "use refused" or "use <pid>" (ADR-0027: a cancelled operation cannot start a
// sidecar again).
func registerSidecarFixture(reg *capability.Registry, sup *proc.Supervisor) {
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.sidecar", Kind: capability.Command, Scope: capability.Workspace,
		Description: "Uses the dummy sidecar; test-only.", Params: sidecarParams{}, Result: sidecarResult{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(sidecarParams)
			cmd, env := processtest.SidecarCommand(p.Events, p.Options...)
			d := proc.Descriptor{
				ID: "dummy", Kind: proc.WorkspaceSidecar, Command: cmd, Env: env,
				Scope:       proc.WorkspaceScope(call.Workspace),
				Ready:       func(line string) bool { return line == "ready" },
				IdleTimeout: time.Duration(p.IdleMs) * time.Millisecond,
				Shutdown:    proc.ShutdownPolicy{Signal: syscall.SIGTERM, Timeout: time.Duration(p.ShutdownMs) * time.Millisecond},
				LogLines:    100,
			}
			l, err := sup.Use(ctx, d)
			if err != nil {
				return nil, err
			}
			nonce := "ping-" + call.OperationID
			if _, err := fmt.Fprintln(l.Stdin(), nonce); err != nil {
				l.Release()
				return nil, err
			}
			for deadline := time.Now().Add(5 * time.Second); !slices.Contains(sup.Output(d.ID, d.Scope), "echo "+nonce); time.Sleep(5 * time.Millisecond) {
				if time.Now().After(deadline) {
					l.Release()
					return nil, fmt.Errorf("the dummy did not answer %s", nonce)
				}
			}
			if p.ReleaseFile != "" {
				appendEvent(p.Events, "lease held")
				for {
					if _, err := os.Stat(p.ReleaseFile); err == nil || (!p.IgnoreContext && ctx.Err() != nil) {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			appendEvent(p.Events, "lease released")
			l.Release()
			if p.ReleaseFile != "" {
				if again, err := sup.Use(ctx, d); err != nil {
					appendEvent(p.Events, "use refused")
				} else {
					appendEvent(p.Events, "use "+strconv.Itoa(again.Pid()))
					again.Release()
				}
			}
			return sidecarResult{Pid: l.Pid()}, nil
		},
	})
}

// snapshot is pids with the start times they had before an act, as the
// verify-daemon recipe counts orphans: a pid reused since is not a survivor.
type snapshot map[int]int64

func snap(t *testing.T, pids ...int) snapshot {
	t.Helper()
	s := snapshot{}
	for _, pid := range pids {
		st, err := platform.StartTime(pid)
		if err != nil {
			t.Fatalf("pid %d is not running before the act: %v", pid, err)
		}
		s[pid] = st
	}
	t.Cleanup(func() {
		for _, pid := range s.survivors() {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return s
}

func (s snapshot) survivors() []int {
	var alive []int
	for pid, st := range s {
		if now, err := platform.StartTime(pid); err == nil && now == st {
			alive = append(alive, pid)
		}
	}
	return alive
}

// sidecarCase is a re-executed daemon, a project opened in it, and a dummy's event
// file.
type sidecarCase struct {
	t      *testing.T
	state  string
	root   string
	sub    string
	events string
	srv    *process
}

func newSidecarCase(t *testing.T, env map[string]string) *sidecarCase {
	t.Helper()
	// The bounds are the daemon's: it alone is given them (not the test process, whose
	// environment other tests share), so sidecar tests can run beside each other.
	var daemonEnv []string
	for k, v := range env {
		daemonEnv = append(daemonEnv, k+"="+v)
	}
	c := &sidecarCase{t: t, state: shortDir(t), events: filepath.Join(t.TempDir(), "events")}
	c.root, c.sub = gradleProject(t)
	c.srv = droidyardEnv(t, c.state, "", daemonEnv, "serve")
	c.srv.expect(t, "listening")
	if env, code := callIn(t, c.state, c.sub, "workspace.open"); code != 0 {
		t.Fatalf("open: %+v", env.Error)
	}
	return c
}

func (c *sidecarCase) params(p sidecarParams) string {
	p.Events = c.events
	raw, _ := json.Marshal(p)
	return string(raw)
}

// sidecar calls fixture.sidecar to completion and returns the dummy's pid.
func (c *sidecarCase) sidecar(p sidecarParams) int {
	c.t.Helper()
	env, code := callIn(c.t, c.state, c.sub, "fixture.sidecar", "--params", c.params(p))
	var res sidecarResult
	if code != 0 || json.Unmarshal(env.Result, &res) != nil || res.Pid == 0 {
		c.t.Fatalf("fixture.sidecar exited %d: %+v %s", code, env.Error, env.Result)
	}
	return res.Pid
}

// held starts fixture.sidecar holding its lease and returns once it holds it.
func (c *sidecarCase) held(p sidecarParams) *process {
	c.t.Helper()
	call := droidyardIn(c.t, c.state, c.sub, "call", "--no-autostart", "fixture.sidecar", "--params", c.params(p))
	eventually(c.t, "the fixture holds its lease", func() bool { return slices.Contains(c.eventList(), "lease held") })
	return call
}

func (c *sidecarCase) eventList() []string {
	raw, _ := os.ReadFile(c.events)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func (c *sidecarCase) helper() int {
	c.t.Helper()
	for _, e := range c.eventList() {
		if pid, ok := strings.CutPrefix(e, "helper "); ok {
			n, _ := strconv.Atoi(pid)
			return n
		}
	}
	c.t.Fatalf("no helper in %v", c.eventList())
	return 0
}

// records are the pids processes.json lists.
func (c *sidecarCase) records() []int {
	c.t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.state, proc.RecordsFile))
	if os.IsNotExist(err) {
		return nil
	}
	var recs proc.Records
	if err := json.Unmarshal(raw, &recs); err != nil {
		c.t.Fatalf("processes.json: %v", err)
	}
	var pids []int
	for _, r := range recs.Processes {
		pids = append(pids, r.PID)
	}
	return pids
}

func (c *sidecarCase) close() time.Duration {
	c.t.Helper()
	began := time.Now()
	if env, code := callIn(c.t, c.state, c.sub, "workspace.close"); code != 0 {
		c.t.Fatalf("close exited %d: %+v", code, env.Error)
	}
	return time.Since(began)
}

func (c *sidecarCase) stop() {
	c.t.Helper()
	if code := droidyard(c.t, c.state, "stop").wait(c.t); code != 0 {
		c.t.Fatalf("stop exited %d", code)
	}
}

func indexOf(events []string, e string) int { return slices.Index(events, e) }

// TestSidecarStartsLazilyIdlesOutAndEndsWithClose: the Phase 0 acceptance. A
// supervised dummy starts on first use, not on open; idles out with its record
// removed; starts again as a new process; and when workspace.close returns, it and
// the helper it forked are gone and unrecorded (ADR-0027 §Consequences).
func TestSidecarStartsLazilyIdlesOutAndEndsWithClose(t *testing.T) {
	t.Parallel()
	c := newSidecarCase(t, nil)
	if len(c.records()) != 0 || len(c.eventList()) != 1 || c.eventList()[0] != "" {
		t.Fatalf("after open: records %v, events %v; want none", c.records(), c.eventList())
	}

	first := c.sidecar(sidecarParams{IdleMs: 800, ShutdownMs: 300})
	if recs := c.records(); len(recs) != 1 || recs[0] != first || len(snap(t, first).survivors()) != 1 {
		t.Fatalf("after the first use: records %v, pid %d", recs, first)
	}
	gone := snap(t, first)
	eventually(t, "the dummy idles out", func() bool { return len(gone.survivors()) == 0 && len(c.records()) == 0 })

	second := c.sidecar(sidecarParams{Options: []string{"fork-helper"}, IdleMs: 60_000, ShutdownMs: 300})
	if second == first {
		t.Fatalf("the second use got pid %d again", second)
	}
	before := snap(t, second, c.helper())
	c.close()
	if left := before.survivors(); len(left) != 0 || len(c.records()) != 0 {
		t.Errorf("when close returned: survivors %v, records %v; want none", left, c.records())
	}
	c.stop()
}

// TestCloseCancelsTheLeaseHolderBeforeTheSidecar: close cancels the operation that
// holds the dummy's lease and waits for it to end before the dummy gets its signal;
// the cancelled operation cannot start the dummy again (ADR-0027 §Decision).
func TestCloseCancelsTheLeaseHolderBeforeTheSidecar(t *testing.T) {
	t.Parallel()
	c := newSidecarCase(t, map[string]string{drainEnv: "200ms"})
	holder := c.held(sidecarParams{Options: []string{"ignore-eof"}, IdleMs: 60_000, ShutdownMs: 300,
		ReleaseFile: filepath.Join(t.TempDir(), "never")})
	before := snap(t, c.records()...)
	c.close()
	if left := before.survivors(); len(left) != 0 || len(c.records()) != 0 {
		t.Errorf("when close returned: survivors %v, records %v", left, c.records())
	}
	env, _ := holder.envelope(t)
	if env.Operation == nil || env.Operation.State != capability.StateCancelled || env.Error == nil || env.Error.Code != capability.ErrOperationCancelled {
		t.Errorf("the lease holder: %+v %+v; want cancelled", env.Operation, env.Error)
	}
	ev := c.eventList()
	t.Logf("events: %v", ev)
	released, term := indexOf(ev, "lease released"), indexOf(ev, "SIGTERM")
	if released < 0 || term < 0 || released > term || (indexOf(ev, "eof") >= 0 && released > indexOf(ev, "eof")) {
		t.Errorf("events %v: the lease must be released before the dummy is told to stop", ev)
	}
	if !slices.Contains(ev, "use refused") {
		t.Errorf("events %v: the cancelled operation's second Use was not refused", ev)
	}
	c.stop()
}

// TestCloseBoundsADrainALeaseHolderIgnores: a lease holder that ignores its
// cancellation holds close for the drain bound only; close then stops the dummy, one
// that ignores SIGTERM and EOF, by SIGKILL after its shutdown timeout, and returns
// with nothing left. Released later, the holder ends cancelled and cannot start a new
// dummy (ADR-0027 §Decision, §Consequences).
func TestCloseBoundsADrainALeaseHolderIgnores(t *testing.T) {
	t.Parallel()
	c := newSidecarCase(t, map[string]string{drainEnv: "200ms"})
	release := filepath.Join(t.TempDir(), "release")
	holder := c.held(sidecarParams{Options: []string{"ignore-eof", "ignore-term"}, IdleMs: 60_000, ShutdownMs: 300,
		ReleaseFile: release, IgnoreContext: true})
	before := snap(t, c.records()...)
	took := c.close()
	if left := before.survivors(); len(left) != 0 || len(c.records()) != 0 {
		t.Errorf("when close returned after %s: survivors %v, records %v", took, left, c.records())
	}
	if took > 200*time.Millisecond+300*time.Millisecond+1500*time.Millisecond {
		t.Errorf("close took %s; want the drain bound plus the shutdown timeout and a margin", took)
	}

	os.WriteFile(release, nil, 0o600)
	env, _ := holder.envelope(t)
	if env.Operation == nil || env.Operation.State != capability.StateCancelled {
		t.Errorf("the released lease holder: %+v %+v; want cancelled", env.Operation, env.Error)
	}
	ev := c.eventList()
	if !slices.Contains(ev, "use refused") {
		t.Errorf("events %v: the second Use was not refused", ev)
	}
	readies := 0
	for _, e := range ev {
		if e == "ready" {
			readies++
		}
	}
	if readies != 1 || len(c.records()) != 0 {
		t.Errorf("events %v, records %v: a new dummy started", ev, c.records())
	}
	c.stop()
}

// TestStopLeavesNoSidecar: `droidyard stop` with a dummy running leaves none.
func TestStopLeavesNoSidecar(t *testing.T) {
	t.Parallel()
	c := newSidecarCase(t, nil)
	pid := c.sidecar(sidecarParams{Options: []string{"fork-helper"}, IdleMs: 60_000, ShutdownMs: 300})
	before := snap(t, pid, c.helper())
	c.stop()
	c.srv.wait(t)
	if left := before.survivors(); len(left) != 0 {
		t.Errorf("after stop: survivors %v", left)
	}
}

// TestStopKillsSidecarsPastTheShutdownDeadline: an operation that ignores its
// cancellation uses up the shutdown deadline, yet the dummy, which ignores SIGTERM,
// and its helper are killed on the dummy's own shutdown timeout: the supervisor does
// not take the spent deadline (process-lifecycle.md §Shutdown).
func TestStopKillsSidecarsPastTheShutdownDeadline(t *testing.T) {
	t.Parallel()
	c := newSidecarCase(t, map[string]string{shutdownEnv: "300ms"})
	c.held(sidecarParams{Options: []string{"ignore-eof", "ignore-term", "fork-helper"}, IdleMs: 60_000, ShutdownMs: 500,
		ReleaseFile: filepath.Join(t.TempDir(), "never"), IgnoreContext: true})
	before := snap(t, append(c.records(), c.helper())...)
	c.stop()
	if left := before.survivors(); len(left) != 0 {
		t.Errorf("after stop: survivors %v", left)
	}
}

// TestKilledDaemonsSidecarExitsOnEOF: layer 1. A daemon killed outright closes the
// dummy's stdin by dying, and a dummy that honours EOF exits by itself.
func TestKilledDaemonsSidecarExitsOnEOF(t *testing.T) {
	t.Parallel()
	c := newSidecarCase(t, nil)
	before := snap(t, c.sidecar(sidecarParams{IdleMs: 60_000, ShutdownMs: 300}))
	c.srv.cmd.Process.Signal(syscall.SIGKILL)
	c.srv.wait(t)
	eventually(t, "the dummy exits on EOF", func() bool { return len(before.survivors()) == 0 })
	if !slices.Contains(c.eventList(), "eof") {
		t.Errorf("events %v: no eof", c.eventList())
	}
}

// TestKilledDaemonsStubbornSidecarIsReapedByTheNext: layer 3. A dummy that ignores
// EOF outlives a daemon killed outright; the next serve reaps it from its pid record
// before its socket answers (ADR-0021 §Pid records; ADR-0019 §Lifetime).
func TestKilledDaemonsStubbornSidecarIsReapedByTheNext(t *testing.T) {
	t.Parallel()
	c := newSidecarCase(t, nil)
	before := snap(t, c.sidecar(sidecarParams{Options: []string{"ignore-eof"}, IdleMs: 60_000, ShutdownMs: 300}))
	c.srv.cmd.Process.Signal(syscall.SIGKILL)
	c.srv.wait(t)
	time.Sleep(500 * time.Millisecond)
	if len(before.survivors()) != 1 || len(c.records()) != 1 {
		t.Fatalf("after the daemon was killed: survivors %v, records %v; want the dummy alive and recorded", before.survivors(), c.records())
	}

	sock := sockFor(c.state)
	next := droidyard(t, c.state, "serve")
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if conn, err := net.Dial("unix", sock); err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the next serve never answered")
		}
	}
	if left := before.survivors(); len(left) != 0 {
		t.Errorf("when the next serve first answered: survivors %v", left)
	}
	next.expect(t, "listening")
	// The next daemon's supervisor starts from the reconciled, empty records.
	if env, code := callIn(t, c.state, c.sub, "workspace.open"); code != 0 {
		t.Fatalf("open: %+v", env.Error)
	}
	fresh := c.sidecar(sidecarParams{IdleMs: 60_000, ShutdownMs: 300})
	if recs := c.records(); len(recs) != 1 || recs[0] != fresh {
		t.Errorf("records %v; want only the new dummy %d", recs, fresh)
	}
	droidyard(t, c.state, "stop").wait(t)
	next.wait(t)
}
