package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/emulator"
	"github.com/jtortosasen/droidyard/internal/domain/emulator/emulatortest"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/platform"
	proc "github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/process/processtest"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The environment a re-executed daemon reads its test seams from.
const (
	drainEnv    = "DROIDYARD_TEST_DRAIN"
	shutdownEnv = "DROIDYARD_TEST_SHUTDOWN"
	httpPortEnv = "DROIDYARD_TEST_HTTP_PORT"
	// sessionClockEnv names a file holding the seconds to add to the real time the session
	// registry reads, and sessionSweepEnv the interval of its idle sweep (ADR-0094 §4).
	sessionClockEnv = "DROIDYARD_TEST_SESSION_CLOCK"
	sessionSweepEnv = "DROIDYARD_TEST_SESSION_SWEEP"
	// workspaceClockEnv and workspaceSweepEnv are the same two seams for the workspace
	// registry's idle release (ADR-0137 §3): the file holds the seconds added to the real time.
	workspaceClockEnv = "DROIDYARD_TEST_WORKSPACE_CLOCK"
	workspaceSweepEnv = "DROIDYARD_TEST_WORKSPACE_SWEEP"
	// gradleFakeEnv names a gradletest configuration file: a re-executed daemon then
	// runs that fake sidecar instead of a real JVM, and gradleJVMEnv names the fake JDK
	// 17 it "runs on" (plan m1g step 5).
	gradleFakeEnv = "DROIDYARD_TEST_GRADLE_FAKE"
	gradleJVMEnv  = "DROIDYARD_TEST_GRADLE_JVM"
	// adbFakeEnv names a devicetest scenario directory: a re-executed daemon then runs
	// this binary as the fake adb (TestMain's devicetest.Main), so a follow's client is a
	// real process the supervisor can record and kill (plan m1i step 3). adbPortEnv is
	// the adb server port its clients are given, one nothing listens on.
	adbFakeEnv = "DROIDYARD_TEST_ADB_FAKE"
	adbPortEnv = "DROIDYARD_TEST_ADB_PORT"
	// adbFloorEnv is the shortest limit a re-executed daemon gives any adb command
	// (device.ADB.LengthenLimits), a Go duration. A test whose claim is not one of the
	// host's 3 s limits sets it (adbFloorVar); its fake adb is this binary re-executed,
	// which a loaded host starts later than the limit. A test whose claim is a limit leaves
	// it unset.
	adbFloorEnv = "DROIDYARD_TEST_ADB_FLOOR"
	// lockFifoEnv names a FIFO: a re-executed serve that finds daemon.lock busy prints
	// lockBusyLine and then blocks, before its next attempt, until the test writes to the
	// FIFO (daemon.AfterBusyAttempt). Only a serve whose test sets it blocks.
	lockFifoEnv  = "DROIDYARD_TEST_LOCK_FIFO"
	lockBusyLine = "serve: daemon.lock is busy, waiting for the test"
	// autostartWaitEnv, when set, makes a re-executed call or mcp that has started a daemon
	// print autostartWaitLine as it begins to wait for the daemon's socket.
	autostartWaitEnv  = "DROIDYARD_TEST_AUTOSTART_WAIT"
	autostartWaitLine = "droidyard: waiting for the daemon (test seam)"
	// emulatorDirEnv names a fake launcher's scenario directory and emulatorADBEnv the
	// fake adb's: a re-executed daemon then resolves both and boots through the table
	// they share (plan m1i step 5, D20).
	emulatorDirEnv = "DROIDYARD_TEST_EMULATOR_DIR"
	emulatorADBEnv = "DROIDYARD_TEST_EMULATOR_ADB_DIR"
	// logMaxEnv is the size, in bytes, past which a re-executed detached daemon rotates
	// its log (plan m2c, step 8).
	logMaxEnv = "DROIDYARD_TEST_LOG_MAX"
	// reconcileEveryEnv is how often a re-executed daemon matches its peer listeners and
	// links to the configuration: a long one shows what only a share's or a join's own
	// Reconcile does (plan m2u step 3).
	reconcileEveryEnv = "DROIDYARD_TEST_RECONCILE_EVERY"
	// joinReplyEnv names a file holding the JSON answer (a capability.PeerJoinReply) of a host
	// that a re-executed daemon's hosting.join dials instead of the real one: the answer of a
	// host whose text is hostile, read at each join.
	joinReplyEnv = "DROIDYARD_TEST_JOIN_REPLY"
	// terminalEnv makes a re-executed call take its standard error for a terminal, which the
	// pipe a test reads it from never is; progressEveryEnv sets the least time between two of
	// its progress lines (plan m2z step 5).
	terminalEnv      = "DROIDYARD_TEST_STDERR_TERMINAL"
	progressEveryEnv = "DROIDYARD_TEST_PROGRESS_EVERY"
)

// adbFloorFor is the limit floor a test gives the adb commands of a daemon it starts, when that
// test's claim is not one of the limits: a hang guard, so testtime.Bound of 30 s, the least
// that outlasts a re-executed fake adb starting on a loaded host (plan m2v step 5).
func adbFloorFor() time.Duration { return testtime.Bound(30 * time.Second) }

// adbFloorVar is the environment variable that gives a re-executed daemon that floor.
func adbFloorVar() string { return adbFloorEnv + "=" + adbFloorFor().String() }

// installTestSeams runs in a re-executed binary before main: it adds the test-only
// fixture.sidecar and shortens the bounds the test names (plan m1c step 7, R17).
func installTestSeams() {
	testCapabilities = registerSidecarFixture
	testHosting = registerPeerProbe
	// Test daemons bind an ephemeral port unless a test asks for one; so do their private
	// peer listeners, which would otherwise all try 7318.
	peerDefaultPort = 0
	httpPort = 0
	if v, err := strconv.Atoi(os.Getenv(httpPortEnv)); err == nil {
		httpPort = v
	}
	if d, err := time.ParseDuration(os.Getenv(drainEnv)); err == nil {
		testDrain = d
	}
	if d, err := time.ParseDuration(os.Getenv(reconcileEveryEnv)); err == nil {
		reconcileEvery = d
	}
	if path := os.Getenv(joinReplyEnv); path != "" {
		joinSeam = func(_ context.Context, _ *hosting.Identity, code hosting.Code, _, _ string) (hosting.JoinResult, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return hosting.JoinResult{}, err
			}
			var reply capability.PeerJoinReply
			if err := json.Unmarshal(raw, &reply); err != nil {
				return hosting.JoinResult{}, err
			}
			return hosting.JoinResult{Reply: reply, Addr: code.Addrs[0]}, nil
		}
	}
	if d, err := time.ParseDuration(os.Getenv(adbFloorEnv)); err == nil {
		adbFloor = d
	}
	if path := os.Getenv(lockFifoEnv); path != "" {
		daemon.AfterBusyAttempt = func() {
			fmt.Fprintln(os.Stderr, lockBusyLine)
			f, err := os.Open(path) // blocks until the test opens the other end
			if err != nil {
				return
			}
			defer f.Close()
			f.Read(make([]byte, 1)) // and until it writes
		}
	}
	if os.Getenv(autostartWaitEnv) != "" {
		autostartWaiting = func() { fmt.Fprintln(os.Stderr, autostartWaitLine) }
	}
	if n, err := strconv.ParseInt(os.Getenv(logMaxEnv), 10, 64); err == nil {
		logMaxBytes = n
	}
	if d, err := time.ParseDuration(os.Getenv(shutdownEnv)); err == nil {
		shutdownTimeout = d
	}
	if os.Getenv(terminalEnv) == "1" {
		stderrTerminal = func() bool { return true }
	}
	if d, err := time.ParseDuration(os.Getenv(progressEveryEnv)); err == nil {
		progressEvery = d
	}
	// A test may give the daemon a fake adb (plan m1i step 3): its clients are then this
	// binary, which TestMain turns into the fake — as devicetest.Main, before the reexec
	// branch, when DROIDYARD_FAKE_ADB_DIR is set — so a follow's client is a real process
	// the supervisor records and, at the window's end, kills by pid. The daemon's whole
	// environment is replaced, as TestMain's seal is, so no client inherits this machine's
	// adb, its SDK or the shared server on 5037.
	if dir := os.Getenv(adbFakeEnv); dir != "" {
		exe, err := os.Executable()
		if err != nil {
			panic(err)
		}
		adbEnviron = func() []string {
			return []string{"ADB=" + exe, devicetest.DirEnv + "=" + dir,
				"ANDROID_ADB_SERVER_PORT=" + os.Getenv(adbPortEnv), "HOME=" + dir, "PATH="}
		}
	}
	// A test may give the daemon a fake emulator launcher and a fake adb (plan m1i step
	// 5): the launcher is this binary re-executed through the emulatortest scenario, and
	// the adb answers in process, so the daemon boots an AVD without this machine's SDK.
	// Both share the fake adb's directory as the launcher's table (emulatortest.TableEnv),
	// and neither the launcher's environment nor the daemon's names DROIDYARD_FAKE_ADB_DIR,
	// so emulatortest.Main runs the launcher rather than devicetest.Main hijacking it.
	if dir := os.Getenv(emulatorDirEnv); dir != "" {
		exe, err := os.Executable()
		if err != nil {
			panic(err)
		}
		bin, home := filepath.Join(dir, "bin"), filepath.Join(dir, "home")
		for _, p := range []string{bin, home} {
			if err := os.MkdirAll(p, 0o700); err != nil {
				panic(err)
			}
		}
		adbDir := os.Getenv(emulatorADBEnv)
		adbExec = devicetest.Exec(adbDir)
		// The daemon's reconcile probes emulator consoles 5554-5584; a test must not depend
		// on what runs on this machine.
		emulator.SetConsolePortsForTest(nil)
		env := append(emulatortest.EnvFor(exe, dir, bin, home),
			"ADB="+exe, emulatortest.TableEnv+"="+adbDir)
		// A test that gives the daemon an adb server of its own (a fake on a loopback port)
		// names the port; without one the launch is recorded for the default 5037, which no
		// test of a second daemon may leave for reconcile to read (plan m1p step 6).
		if port := os.Getenv(adbPortEnv); port != "" {
			env = append(env, "ANDROID_ADB_SERVER_PORT="+port)
		}
		adbEnviron = func() []string { return env }
	}
	// A test may give the daemon an SDK holding the fake avdmanager and a JDK for it
	// (plan m3f step 6; sdk_tool_test.go).
	installSDKToolSeam()
	// A test may move the session registry's clock (a file holding the seconds to add to
	// the real time, read at each use) and shorten the idle sweep (ADR-0094 §4).
	if path := os.Getenv(sessionClockEnv); path != "" {
		sessionClock = func() time.Time {
			raw, _ := os.ReadFile(path)
			secs, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			return time.Now().Add(time.Duration(secs) * time.Second)
		}
	}
	if d, err := time.ParseDuration(os.Getenv(sessionSweepEnv)); err == nil {
		sessionSweep = d
	}
	if path := os.Getenv(workspaceClockEnv); path != "" {
		workspaceClock = func() time.Time {
			raw, _ := os.ReadFile(path)
			secs, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			return time.Now().Add(time.Duration(secs) * time.Second)
		}
	}
	if d, err := time.ParseDuration(os.Getenv(workspaceSweepEnv)); err == nil {
		workspaceSweep = d
	}
	// A test may give the daemon a fake Gradle sidecar (plan m1g step 5): the sidecar
	// command runs the test binary again in the fake's mode, so reexec must not reach
	// it, or it would run the daemon instead of the fake.
	if cfg := os.Getenv(gradleFakeEnv); cfg != "" {
		jvm := os.Getenv(gradleJVMEnv)
		gradleLaunch = func(j gradle.JVM, jars gradle.Jars) (string, []string, []string, error) {
			command, args, env, err := gradletest.Launch(cfg)(j, jars)
			return command, args, withoutVars(env, reexec), err
		}
		gradleFinder = func([]string) *gradle.Finder {
			return &gradle.Finder{GOOS: "linux", Env: []string{"JAVA_HOME=" + jvm}, StudioHomes: func() []string { return nil }}
		}
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
	registerTouchFixture(reg)
	registerTrustedFixture(reg, platform.Current())
	// fixture.stray writes to standard error by the process's own handle, as a panic or
	// a library's print would, bypassing the daemon's log writer.
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.stray", Kind: capability.Command, Scope: capability.Global,
		Description: "Prints a line to standard error; test-only.", Params: struct{}{}, Result: struct{}{},
		Handler: func(context.Context, *capability.Call) (any, error) {
			fmt.Fprintln(os.Stderr, "STRAY-LINE")
			return struct{}{}, nil
		},
	})
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
	testtime.Eventually(c.t, testtime.Bound(10*time.Second), "the fixture holds its lease", func() bool { return slices.Contains(c.eventList(), "lease held") })
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

func (c *sidecarCase) stop() {
	c.t.Helper()
	if code := droidyard(c.t, c.state, "stop").wait(c.t); code != 0 {
		c.t.Fatalf("stop exited %d", code)
	}
}

func indexOf(events []string, e string) int { return slices.Index(events, e) }

// TestSidecarStartsLazilyAndIdlesOut: the Phase 0 acceptance. A supervised dummy starts on
// first use, not on the call that opens its project; idles out with its record removed; and
// starts again as a new process (ADR-0027 §Consequences). That a workspace's release stops
// its sidecars is the registry's, in the workspace package's tests: no capability closes a
// workspace any more (ADR-0137 §1).
func TestSidecarStartsLazilyAndIdlesOut(t *testing.T) {
	t.Parallel()
	c := newSidecarCase(t, nil)
	if env, code := callIn(t, c.state, c.sub, "fixture.touch"); code != 0 {
		t.Fatalf("fixture.touch: %+v", env.Error)
	}
	if len(c.records()) != 0 || len(c.eventList()) != 1 || c.eventList()[0] != "" {
		t.Fatalf("after the call that opened the project: records %v, events %v; want none", c.records(), c.eventList())
	}

	first := c.sidecar(sidecarParams{IdleMs: 800, ShutdownMs: 300})
	if recs := c.records(); len(recs) != 1 || recs[0] != first || len(snap(t, first).survivors()) != 1 {
		t.Fatalf("after the first use: records %v, pid %d", recs, first)
	}
	gone := snap(t, first)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the dummy idles out", func() bool { return len(gone.survivors()) == 0 && len(c.records()) == 0 })

	second := c.sidecar(sidecarParams{Options: []string{"fork-helper"}, IdleMs: 60_000, ShutdownMs: 300})
	if second == first {
		t.Fatalf("the second use got pid %d again", second)
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

// withoutDeathSignal adds the dummy options for a killed-daemon test. On Linux a killed
// daemon also delivers Pdeathsig SIGTERM (layer 2, process-lifecycle.md §Shutdown), which
// would end the dummy before the layer under test acts; the dummy ignores it there, so the
// test sees EOF (layer 1) or the pid record (layer 3) alone, as on macOS.
func withoutDeathSignal(opts ...string) []string {
	if runtime.GOOS == "linux" {
		opts = append(opts, "ignore-term")
	}
	return opts
}

// TestKilledDaemonsSidecarExitsOnEOF: layer 1. A daemon killed outright closes the
// dummy's stdin by dying, and a dummy that honours EOF exits by itself.
func TestKilledDaemonsSidecarExitsOnEOF(t *testing.T) {
	t.Parallel()
	c := newSidecarCase(t, nil)
	before := snap(t, c.sidecar(sidecarParams{Options: withoutDeathSignal(), IdleMs: 60_000, ShutdownMs: 300}))
	c.srv.cmd.Process.Signal(syscall.SIGKILL)
	c.srv.wait(t)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the dummy exits on EOF", func() bool { return len(before.survivors()) == 0 })
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
	before := snap(t, c.sidecar(sidecarParams{Options: withoutDeathSignal("ignore-eof"), IdleMs: 60_000, ShutdownMs: 300}))
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
	fresh := c.sidecar(sidecarParams{IdleMs: 60_000, ShutdownMs: 300})
	if recs := c.records(); len(recs) != 1 || recs[0] != fresh {
		t.Errorf("records %v; want only the new dummy %d", recs, fresh)
	}
	droidyard(t, c.state, "stop").wait(t)
	next.wait(t)
}
