package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/httpapi"
	mcpadapter "github.com/jtortosasen/droidyard/adapters/mcp"
	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/file"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
	proc "github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
)

// inProcess is the shipped wiring (registry(), daemonOptions) started in the test
// process, plus test-only capabilities that are never shipped: fixture.block hands the
// test its operation id and blocks until released; fixture.slow does the same and is
// long-running; fixture.human is for humans only.
// slowResult is fixture.slow's result.
type slowResult struct {
	Answer int `json:"answer"`
}

type inProcess struct {
	d        *daemon.Daemon
	mcp      *mcpadapter.Server
	http     *httpapi.Server // the UI's HTTP side, for its test seams
	ops      *capability.Operations
	bus      *event.Bus
	store    *artifact.Store // the daemon's artifact store
	watchers *file.Watchers  // the daemon's live file.watch pollers (plan m1j, D9)
	adb      *device.ADB     // the daemon's adb: its Close is the daemon's AfterOps
	token    string          // the HTTP token
	log      *lockedBuffer   // the daemon's log, also copied to stderr
	state    string
	started  chan string
	release  chan struct{}
}

func startInProcess(t *testing.T) *inProcess {
	t.Helper()
	return startInProcessWith(t, nil)
}

// startInProcessWith is startInProcess with more test-only capabilities, registered
// by extra before the daemon starts.
func startInProcessWith(t *testing.T, extra func(reg *capability.Registry)) *inProcess {
	t.Helper()
	return startInProcessAt(t, "127.0.0.1:0", extra)
}

// startInProcessAt is startInProcessWith with the UI's listener asked for at httpAddr.
func startInProcessAt(t *testing.T, httpAddr string, extra func(reg *capability.Registry)) *inProcess {
	t.Helper()
	return startInProcessADB(t, httpAddr, withoutEnv(extra), nil)
}

// startInProcessEnv is startInProcessAt for a test that needs the daemon's
// environment too, e.g. to build the same policy the daemon reads its configuration
// from (plan m1g step 3).
func startInProcessEnv(t *testing.T, extra func(reg *capability.Registry, env platform.Env)) *inProcess {
	t.Helper()
	return startInProcessADB(t, "127.0.0.1:0", extra, nil)
}

// withoutEnv adapts an extra that ignores the environment to the form
// startInProcessADB takes.
func withoutEnv(extra func(reg *capability.Registry)) func(reg *capability.Registry, env platform.Env) {
	if extra == nil {
		return nil
	}
	return func(reg *capability.Registry, _ platform.Env) { extra(reg) }
}

// adbSeam is the adb one in-process daemon is given: the environment it resolves adb
// in and the clients it runs (nil exec: it would start them as processes).
type adbSeam struct {
	env  []string
	exec device.Exec
	// wrapStream, when set, wraps the recorded starter of a Logcat stream, so a test can
	// hold a child's reaping (ADR-0041 §6).
	wrapStream func(device.StreamStarter) device.StreamStarter
	// gradle, when set, gives the daemon a fake Gradle sidecar in place of the sealed
	// "no JDK" of TestMain. It is applied beside the adb seam, under adbSeamMu.
	gradle *gradleSeam
}

// gradleSeam is the Gradle sidecar one in-process daemon is given: how it is launched
// and the finder that tells it a JDK exists.
type gradleSeam struct {
	launch gradle.Launch
	finder func(env []string) *gradle.Finder
}

// adbSeamMu orders the daemons that start at once (parallel tests): registry() reads
// the package's adb seams, so a daemon that needs its own sets them, builds its
// registry and puts them back, all under the lock.
var adbSeamMu sync.Mutex

// startInProcessADB is startInProcessAt with the adb the daemon is given; nil leaves the
// seal TestMain put on the package (no adb to find). extra sees the daemon's
// environment, so a test can build what the daemon itself derives from it.
func startInProcessADB(t *testing.T, httpAddr string, extra func(reg *capability.Registry, env platform.Env), adb *adbSeam) *inProcess {
	t.Helper()
	return startInProcessFull(t, httpAddr, extra, adb, 0)
}

// startInProcessFull is startInProcessADB with the file.watch poller's interval: tick
// > 0 replaces the one-second default for this daemon, so a test sees a change at once
// (plan m1j step 4). Zero keeps the default.
func startInProcessFull(t *testing.T, httpAddr string, extra func(reg *capability.Registry, env platform.Env), adb *adbSeam, tick time.Duration) *inProcess {
	t.Helper()
	return startInProcessConfig(t, httpAddr, extra, adb, tick, "")
}

// startInProcessConfig is startInProcessFull with a configuration file written into the
// state root before the daemon reads it. The tool keys are read once, at daemon start
// (ADR-0032 §3, §6; plan m1k D8), so a test that wants them set writes the file first;
// an empty body writes no file, and the daemon starts with the configuration missing.
func startInProcessConfig(t *testing.T, httpAddr string, extra func(reg *capability.Registry, env platform.Env), adb *adbSeam, tick time.Duration, configBody string) *inProcess {
	t.Helper()
	p := &inProcess{state: shortDir(t), log: &lockedBuffer{}, started: make(chan string, 4), release: make(chan struct{})}
	if configBody != "" {
		dir := filepath.Join(p.state, "config")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte(configBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.New(p.state)
	if err != nil {
		t.Fatal(err)
	}
	sup := proc.New(proc.Options{Store: st, Log: os.Stderr})
	// The daemon's environment: an explicit state root, so its configuration lives in
	// the test's state directory and never in this machine's (~/Library/Application
	// Support/droidyard-config) (plan m1g step 3).
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return p.state
		}
		return os.Getenv(k)
	}}
	adbSeamMu.Lock()
	oldEnv, oldExec, oldWrap := adbEnviron, adbExec, adbStreamWrap
	oldTick := watchTick
	oldLaunch, oldFinder := gradleLaunch, gradleFinder
	if adb != nil && adb.env != nil {
		adbEnviron, adbExec, adbStreamWrap = func() []string { return adb.env }, adb.exec, adb.wrapStream
	}
	if adb != nil && adb.gradle != nil {
		gradleLaunch, gradleFinder = adb.gradle.launch, adb.gradle.finder
	}
	watchTick = tick
	reg, ops, bus, artifacts, deviceADB, watchers := registry(env, st, sup, os.Stderr)
	adbEnviron, adbExec, adbStreamWrap, watchTick = oldEnv, oldExec, oldWrap, oldTick
	gradleLaunch, gradleFinder = oldLaunch, oldFinder
	adbSeamMu.Unlock()
	t.Cleanup(artifacts.Close)
	p.ops, p.bus, p.store = ops, bus, artifacts
	p.watchers = watchers
	p.adb = deviceADB
	if extra != nil {
		extra(reg, env)
	}
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.block", Kind: capability.Command, Scope: capability.Global,
		Description: "Blocks until released.", Params: struct{}{}, Result: struct{}{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p.started <- call.OperationID
			select {
			case <-p.release:
				return struct{}{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.slow", Kind: capability.Command, Scope: capability.Global, LongRunning: true,
		Description: "Blocks until released, then answers 42; long-running.", Params: struct{}{}, Result: slowResult{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p.started <- call.OperationID
			select {
			case <-p.release:
				return slowResult{Answer: 42}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.human", Kind: capability.Command, Scope: capability.Global, Audience: capability.AudienceHuman,
		Description: "Changes policy; for humans only.", Params: struct{}{}, Result: struct{}{},
		Handler: func(context.Context, *capability.Call) (any, error) {
			t.Error("the human-audience fixture ran")
			return struct{}{}, nil
		},
	})
	opts, mcpServer, err := daemonOptions(st, sup, env.SocketPath(p.state), httpAddr, reg, ops, bus, artifacts, deviceADB.Close, io.MultiWriter(os.Stderr, p.log))
	if err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	p.d = d
	p.token = opts.HTTP.Token
	p.mcp = mcpServer
	p.http = opts.HTTP.Handler.(*httpapi.Server)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		d.Shutdown(ctx)
	})
	return p
}

// lockedBuffer is a log the daemon writes while a test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// call runs a re-executed `droidyard call` and returns its envelope and exit code.
func (p *inProcess) call(t *testing.T, args ...string) (capability.ResponseEnvelope, int) {
	t.Helper()
	proc := droidyard(t, p.state, append([]string{"call", "--no-autostart"}, args...)...)
	return proc.envelope(t)
}

func (proc *process) envelope(t *testing.T) (capability.ResponseEnvelope, int) {
	t.Helper()
	code := proc.wait(t)
	var env capability.ResponseEnvelope
	if err := json.Unmarshal(proc.stdout.Bytes(), &env); err != nil {
		t.Fatalf("droidyard call printed %q, not an envelope: %v", proc.stdout.String(), err)
	}
	return env, code
}

func health(t *testing.T, env capability.ResponseEnvelope) system.Health {
	t.Helper()
	var h system.Health
	if err := json.Unmarshal(env.Result, &h); err != nil {
		t.Fatalf("system.health result %s: %v", env.Result, err)
	}
	return h
}

// TestCallPrintsTheEnvelope: `droidyard call` prints the native response envelope and
// exits non-zero iff it carries an error (plan Readings). operation.wait answers with
// the waited operation in the envelope (ADR-0020 §Operation capabilities; plan
// Readings, operation.wait (a)); a rejection has an error and no operation
// (docs/architecture/capability-api.md §Response envelope).
func TestCallPrintsTheEnvelope(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)

	first, code := p.call(t, "system.health")
	if code != 0 || first.Operation == nil || first.Operation.State != capability.StateCompleted || health(t, first).Version != system.Version {
		t.Fatalf("call system.health exited %d with %+v; want 0, completed, version %s", code, first, system.Version)
	}

	waited, code := p.call(t, "operation.wait", "--params", `{"operationId":"`+first.Operation.ID+`"}`)
	if code != 0 || waited.Operation == nil || waited.Operation.ID != first.Operation.ID || waited.Operation.State != capability.StateCompleted {
		t.Fatalf("call operation.wait exited %d with %+v; want 0 and operation %s, completed", code, waited, first.Operation.ID)
	}
	if string(waited.Result) != string(first.Result) {
		t.Errorf("operation.wait carried result %s; want the waited operation's, %s", waited.Result, first.Result)
	}

	nope, code := p.call(t, "nope.nope")
	if code == 0 || nope.Error == nil || nope.Error.Code != capability.ErrCapabilityNotFound || nope.Operation != nil {
		t.Fatalf("call nope.nope exited %d with %+v; want non-zero, REQUEST_CAPABILITY_NOT_FOUND, no operation", code, nope)
	}
}

// TestHealthCountsOperationsInFlight: system.health's operations counts operations not
// yet terminal, itself excluded; a finished operation's retained record is not in
// flight (plan Readings).
func TestHealthCountsOperationsInFlight(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)

	if env, _ := p.call(t, "system.health"); health(t, env).Operations != 0 {
		t.Fatalf("idle system.health reported %d operations; want 0", health(t, env).Operations)
	}

	blocking := droidyard(t, p.state, "call", "--no-autostart", "fixture.block")
	var id string
	select {
	case id = <-p.started:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture.block never started")
	}
	if env, _ := p.call(t, "system.health"); health(t, env).Operations != 1 {
		t.Fatalf("system.health reported %d operations while one runs; want 1", health(t, env).Operations)
	}

	close(p.release)
	if env, code := blocking.envelope(t); code != 0 || env.Operation == nil || env.Operation.ID != id || env.Operation.State != capability.StateCompleted {
		t.Fatalf("the blocking call exited %d with %+v; want 0, operation %s completed", code, env, id)
	}
	if env, _ := p.call(t, "system.health"); health(t, env).Operations != 0 {
		t.Fatalf("system.health reported %d operations after the other completed; want 0", health(t, env).Operations)
	}
	if env, code := p.call(t, "operation.wait", "--params", `{"operationId":"`+id+`","timeoutMs":0}`); code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("the completed operation's record is not retained: exit %d, %+v", code, env)
	}
}

// TestStopEndsServe: `droidyard stop` signals the daemon named in a held lock and
// waits until the lock is released (ADR-0019 §3; ADR-0021 §Locking). The daemon exits
// 0, its socket is gone, and a new daemon takes the lock.
func TestStopEndsServe(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	first := droidyard(t, state, "serve")
	first.expect(t, "listening")

	status := droidyard(t, state, "status")
	if code := status.wait(t); code != 0 {
		t.Fatalf("status exited %d with a daemon running; want 0", code)
	}

	stop := droidyard(t, state, "stop")
	if code := stop.wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	if code := first.wait(t); code != 0 {
		t.Fatalf("serve exited %d after stop; want 0", code)
	}
	if _, err := os.Lstat(filepath.Join(state, "run", "droidyard.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket is still there after stop: %v", err)
	}
	if code := droidyard(t, state, "status").wait(t); code == 0 {
		t.Error("status exited 0 with no daemon running")
	}

	second := droidyard(t, state, "serve")
	second.expect(t, "listening")
	if info, err := daemon.ReadLock(state); err != nil || info.PID != second.cmd.Process.Pid {
		t.Fatalf("after stop, a new serve did not take the lock: (%+v, %v)", info, err)
	}
	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Fatalf("the second stop exited %d", code)
	}
	second.wait(t)
}

// shutdown stops the in-process daemon as SIGTERM would.
func shutdown(t *testing.T, p *inProcess) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.d.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}
