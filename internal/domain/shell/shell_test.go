package shell

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/artifacts"
	"github.com/jtortosasen/droidyard/internal/domain/file"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
)

// These tests run real processes: every command runs with `/bin/sh -c` in a t.TempDir()
// root and the test's own environment. Nothing here touches adb, an emulator or the
// shared adb server, and nothing runs outside a t.TempDir().

const testWS = "ws-shell"

// envAt is a real environment whose configuration directory is dir: a test can never
// read or write this machine's configuration.
func envAt(dir string) platform.Env {
	e := platform.Current()
	base := e.Getenv
	e.Getenv = func(k string) string {
		switch k {
		case "DROIDYARD_CONFIG_DIR":
			return dir
		case "HOME":
			return "/nonexistent-droidyard-home"
		}
		return base(k)
	}
	return e
}

// alive reports whether a pid exists: kill(pid, 0) succeeds even for a zombie.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// groupAlive reports whether any process remains in a process group.
func groupAlive(pgid int) bool { return syscall.Kill(-pgid, 0) == nil }

// waitDead polls until pid is gone or the bound passes.
func waitDead(t *testing.T, pid int, bound time.Duration) bool {
	t.Helper()
	for deadline := time.Now().Add(bound); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !alive(pid) {
			return true
		}
	}
	return !alive(pid)
}

// harness is the shell capability over a real policy, a real supervisor and a real
// artifact store, wired as cmd/droidyard wires it (the workspace registry replaced by
// the one workspace admit below).
type harness struct {
	t       *testing.T
	env     platform.Env
	cfgDir  string
	procDir string
	// root is what RootOf returns for the call; a test may replace it (setRoot).
	root string
	ops  *capability.Operations
	reg  *capability.Registry
	arts *artifact.Store
	sup  *process.Supervisor
	pol  *policy.Policy
}

// admit admits testWS and nothing else, the way the workspace registry admits an open
// one (ADR-0027): the record func is how CancelWorkspace reaches the operation.
type admit struct{ ws string }

func (a admit) Admit(target string, record func(id string)) bool {
	if target != a.ws {
		return false
	}
	record(a.ws)
	return true
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, cfgDir: t.TempDir(), procDir: t.TempDir()}
	h.env = envAt(h.cfgDir)
	h.root = h.env.Canonical(t.TempDir())
	h.pol = policy.New(h.env)

	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	ops.UseWorkspaces(admit{ws: testWS})

	procStore, err := store.New(h.procDir)
	if err != nil {
		t.Fatal(err)
	}
	sup := process.New(process.Options{Store: procStore})
	if err := sup.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Shutdown)

	arts, err := artifact.New(artifact.Options{Epoch: "shell-test"})
	if err != nil {
		t.Fatal(err)
	}

	rootOf := func(call *capability.Call) (string, bool) {
		if call.Workspace != testWS {
			return "", false
		}
		return h.root, true
	}
	Register(reg, Options{
		RootOf:       rootOf,
		ShellAllowed: h.pol.ShellAllowed,
		Authorize:    h.pol.Authorize(rootOf),
		Recorded: func(cmd *exec.Cmd, kind string) (Recorded, error) {
			return sup.SpawnRecorded(cmd, process.Kind(kind))
		},
		ResolveDir: func(root, path string) (string, error) { return file.ResolveDir(h.env, root, path) },
		Env:        os.Environ(),
		Artifacts:  arts,
	})
	artifacts.Register(reg, arts)
	ops.RegisterCapabilities()

	h.reg, h.ops, h.arts, h.sup = reg, ops, arts, sup
	return h
}

// cfg writes body to the configuration file the policy reads.
func (h *harness) cfg(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(h.cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.cfgDir, policy.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// enable turns the shell on for the harness's root and trusts it.
func (h *harness) enable(t *testing.T) {
	t.Helper()
	h.cfg(t, `{"trust":{"roots":[`+strconv.Quote(h.root)+`]},"shell":{"enabled":true}}`)
}

// setRoot replaces the root the call would run in (the boundary rows).
func (h *harness) setRoot(t *testing.T, raw string) {
	t.Helper()
	h.root = h.env.Canonical(raw)
}

// params encodes a call's parameters.
func params(command string, timeoutMs int) json.RawMessage {
	p := Params{Command: command}
	if timeoutMs != 0 {
		p.TimeoutMs = timeoutMs
	}
	raw, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return raw
}

// invoke runs shell.exec to its terminal state.
func (h *harness) invoke(t *testing.T, command string, timeoutMs int) capability.Outcome {
	t.Helper()
	out, err := h.ops.Invoke(context.Background(), capability.Request{
		Capability: "shell.exec", Workspace: testWS, Params: params(command, timeoutMs),
	})
	if err != nil {
		t.Fatalf("shell.exec: %v", err)
	}
	return out
}

// start starts shell.exec and returns its operation id.
func (h *harness) start(t *testing.T, command string, timeoutMs int) string {
	t.Helper()
	id, err := h.ops.Start(context.Background(), capability.Request{
		Capability: "shell.exec", Workspace: testWS, Params: params(command, timeoutMs),
	})
	if err != nil {
		t.Fatalf("shell.exec start: %v", err)
	}
	return id
}

// wait waits for an operation to reach a terminal state.
func (h *harness) wait(t *testing.T, id string) capability.Outcome {
	t.Helper()
	out, err := h.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatalf("shell.exec wait: %v", err)
	}
	return out
}

// records are the pid records processes.json holds right now.
func (h *harness) records(t *testing.T) []process.Record {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.procDir, process.RecordsFile))
	if err != nil {
		return nil
	}
	var recs process.Records
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatalf("processes.json: %v", err)
	}
	return recs.Processes
}

// mustResult requires a completed operation and returns its result.
func mustResult(t *testing.T, out capability.Outcome) Result {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation ended %s (error %+v), want completed", out.State, out.Error)
	}
	r, ok := out.Result.(Result)
	if !ok {
		t.Fatalf("result is %T, want Result", out.Result)
	}
	return r
}

// mustFail requires a failed operation with code and returns its error.
func mustFail(t *testing.T, out capability.Outcome, code capability.ErrorCode) *capability.Error {
	t.Helper()
	if out.State != capability.StateFailed {
		t.Fatalf("operation ended %s, want failed (error %+v)", out.State, out.Error)
	}
	if out.Error == nil || out.Error.Code != code {
		t.Fatalf("error = %+v, want code %s", out.Error, code)
	}
	return out.Error
}

// noMarker asserts the command never ran: a `touch marker` command leaves no file.
func (h *harness) noMarker(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(h.root, "marker")); !os.IsNotExist(err) {
		t.Errorf("the refused call ran its command: marker exists (%v)", err)
	}
}

// --- The gate (D4) -------------------------------------------------------------

// TestGateRefusesAndSpawnsNothing: the shell policy is the capability's own gate, run
// before the trust check (ADR-0015 §7; plan m1k D4). With no file, with enabled false,
// with roots naming another directory, and at a path-component boundary (root /x/ab
// against entry /x/a), the call is POLICY_SHELL_DISABLED and no command runs. Seen
// failing with the gate read once at registration and with the boundary read as a plain
// prefix. Gate rows drive `touch marker`, whose absence is the proof nothing spawned.
func TestGateRefusesAndSpawnsNothing(t *testing.T) {
	h := newHarness(t)

	// No file at all.
	h.noMarker(t)
	mustFail(t, h.invoke(t, "touch marker", 0), capability.ErrPolicyShellDisabled)
	h.noMarker(t)

	// enabled: false.
	h.cfg(t, `{"trust":{"roots":[`+strconv.Quote(h.root)+`]},"shell":{"enabled":false}}`)
	ce := mustFail(t, h.invoke(t, "touch marker", 0), capability.ErrPolicyShellDisabled)
	// D6: POLICY_SHELL_DISABLED names the way out with its deciding argument — the
	// human-only policy.update, or shell.roots — and says repeating is safe, nothing ran
	// (plan m1n, D6).
	if !strings.Contains(ce.NextStep, "policy.update") || !strings.Contains(ce.NextStep, "safe to repeat") {
		t.Errorf("nextStep = %q; want policy.update and that repeating is safe (plan m1n, D6)", ce.NextStep)
	}
	h.noMarker(t)

	// roots naming another directory.
	h.cfg(t, `{"trust":{"roots":[`+strconv.Quote(h.root)+`]},"shell":{"roots":["/somewhere/else"]}}`)
	mustFail(t, h.invoke(t, "touch marker", 0), capability.ErrPolicyShellDisabled)
	h.noMarker(t)

	// The boundary: an entry that is a name-prefix but not a path-component prefix.
	parent := t.TempDir()
	for _, dir := range []string{"a", "ab"} {
		if err := os.MkdirAll(filepath.Join(parent, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	entry := h.env.Canonical(filepath.Join(parent, "a"))
	h.setRoot(t, filepath.Join(parent, "ab"))
	h.cfg(t, `{"trust":{"roots":[`+strconv.Quote(h.root)+`]},"shell":{"roots":[`+strconv.Quote(entry)+`]}}`)
	mustFail(t, h.invoke(t, "touch marker", 0), capability.ErrPolicyShellDisabled)
	h.noMarker(t)
}

// TestGateNamesAnInvalidFile: an invalid configuration fails closed and its cause names
// the file (ADR-0032 §4; plan m1k D1). Seen failing with the code returned but no file
// named.
func TestGateNamesAnInvalidFile(t *testing.T) {
	h := newHarness(t)
	h.cfg(t, `{"shell":{"enabled":"yes"}}`) // a string where a bool is required
	err := mustFail(t, h.invoke(t, "touch marker", 0), capability.ErrPolicyShellDisabled)
	if !strings.Contains(err.Cause, policy.FileName) {
		t.Errorf("cause %q does not name %s", err.Cause, policy.FileName)
	}
	h.noMarker(t)
}

// TestGateChecksTrustAfterTheShellPolicy: the shell policy admits the root, but the
// trust hook then fails WORKSPACE_UNTRUSTED, as project.* do (ADR-0032 §2; plan m1k D4),
// and nothing runs. Seen failing with the trust check skipped once the shell policy
// admits.
func TestGateChecksTrustAfterTheShellPolicy(t *testing.T) {
	h := newHarness(t)
	// The shell is on for the root, but the root is not trusted.
	h.cfg(t, `{"trust":{"roots":[]},"shell":{"enabled":true}}`)
	mustFail(t, h.invoke(t, "touch marker", 0), capability.ErrWorkspaceUntrusted)
	h.noMarker(t)
}

// TestGateFollowsAFileEditBetweenCalls: the policy is re-read per check from a
// size-and-mtime snapshot, so an edit between two calls decides the second with no
// restart (ADR-0031 §4; ADR-0032 §6; plan m1k D1). Seen failing with the gate read once
// at registration: the second call would keep the first decision.
func TestGateFollowsAFileEditBetweenCalls(t *testing.T) {
	h := newHarness(t)
	h.cfg(t, `{"shell":{"enabled":false}}`)
	mustFail(t, h.invoke(t, "touch marker", 0), capability.ErrPolicyShellDisabled)
	h.noMarker(t)
	// The edit enables the shell; the second call must run.
	h.enable(t)
	out := h.invoke(t, "touch marker", 0)
	mustResult(t, out)
	if _, err := os.Stat(filepath.Join(h.root, "marker")); err != nil {
		t.Errorf("the second call did not run after the edit: %v", err)
	}
}

// --- Parameters ----------------------------------------------------------------

// TestParamsAreValidatedBeforeAnythingRuns: REQUEST_INVALID_PARAMS and nothing spawned
// for an empty command, a command over 8192 bytes, a NUL byte and an out-of-range
// timeout (plan m1k D5).
func TestParamsAreValidatedBeforeAnythingRuns(t *testing.T) {
	h := newHarness(t)
	h.enable(t)
	long := strings.Repeat("x", commandMaxBytes+1)
	for _, c := range []struct {
		command string
		timeout int
	}{
		{"", 0},
		{long, 0},
		{"echo a\x00b", 0},
		{"echo hi", 500},
		{"echo hi", 600001},
	} {
		out := h.invoke(t, c.command, c.timeout)
		mustFail(t, out, capability.ErrInvalidParams)
	}
}

// --- Running commands (D5, D6) --------------------------------------------------

// TestRunReportsACompletedResult: a non-zero exit is a completed result with its exit
// code, and stdout and stderr are returned inline (plan m1k D6).
func TestRunReportsACompletedResult(t *testing.T) {
	h := newHarness(t)
	h.enable(t)
	res := mustResult(t, h.invoke(t, "echo hi; echo err >&2; exit 3", 0))
	if res.ExitCode == nil || *res.ExitCode != 3 {
		t.Errorf("exitCode = %v, want 3", res.ExitCode)
	}
	if res.Signal != "" {
		t.Errorf("signal = %q, want empty for a normal exit", res.Signal)
	}
	if res.Stdout != "hi\n" || res.Stderr != "err\n" {
		t.Errorf("stdout %q stderr %q, want %q and %q", res.Stdout, res.Stderr, "hi\n", "err\n")
	}
	if res.TimedOut {
		t.Error("a non-zero exit is not a timeout")
	}
}

// TestRunUsesTheCanonicalRoot: the command's working directory is the canonical root
// (plan m1k D5), and a command that reads the null device returns at once.
func TestRunUsesTheCanonicalRoot(t *testing.T) {
	h := newHarness(t)
	h.enable(t)
	res := mustResult(t, h.invoke(t, "pwd", 0))
	if got := strings.TrimSpace(res.Stdout); got != h.root {
		t.Errorf("pwd = %q, want the canonical root %q", got, h.root)
	}
	// `cat` reads stdin, the null device: it sees EOF at once.
	start := time.Now()
	res = mustResult(t, h.invoke(t, "cat", 0))
	if res.DurationMs > 3000 || time.Since(start) > 5*time.Second {
		t.Errorf("cat took %d ms; stdin is not the null device", res.DurationMs)
	}
}

// TestRunKeepsTheFirstFourMiBAndTheLastSixteen: a stream over the inline bound is also
// kept as an ephemeral text artifact of its first 4 MiB; a stream over the capture bound
// ends that artifact in a marker naming the bytes dropped, and the inline tail is the
// stream's actual end (plan m1k D6).
func TestRunBoundsItsOutput(t *testing.T) {
	h := newHarness(t)
	h.enable(t)

	// ~106 KiB: over the 16 KiB inline bound, under the 4 MiB capture bound.
	var b strings.Builder
	for i := 1; i <= 20000; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	over := b.String()
	res := mustResult(t, h.invoke(t, "seq 1 20000", 0))
	if res.StdoutBytes != int64(len(over)) {
		t.Fatalf("stdoutBytes = %d, want %d", res.StdoutBytes, len(over))
	}
	if res.Truncated {
		t.Error("a stream under 4 MiB is not truncated")
	}
	if len(res.Stdout) != inlineBytes || res.Stdout != over[len(over)-inlineBytes:] {
		t.Errorf("stdout is %d bytes; want the stream's last %d", len(res.Stdout), inlineBytes)
	}
	if res.StdoutRef == nil {
		t.Fatal("a stream over 16 KiB returned no stdoutRef")
	}
	if got := h.readArtifact(t, res.StdoutRef.Ref, len(over)+1); got != over {
		t.Errorf("artifact.read returned %d bytes, want the whole stream (%d)", len(got), len(over))
	}

	// 6 MiB: over the 4 MiB capture bound.
	res = mustResult(t, h.invoke(t, "yes A | head -c 6291456", 0))
	full := strings.Repeat("A\n", 6291456/2)
	if res.StdoutBytes != int64(len(full)) {
		t.Fatalf("stdoutBytes = %d, want %d", res.StdoutBytes, len(full))
	}
	if !res.Truncated {
		t.Error("a stream over 4 MiB must be truncated")
	}
	if res.StdoutRef == nil {
		t.Fatal("a stream over 4 MiB returned no stdoutRef")
	}
	if res.Stdout != full[len(full)-inlineBytes:] {
		t.Error("the inline tail is not the stream's actual end")
	}
	entry, err := h.arts.Get(res.StdoutRef.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !res.StdoutRef.Untrusted || !entry.Ref.Untrusted {
		t.Error("the stdout artifact is not marked untrusted")
	}
	marker := string(markerLine("stdout", captureBytes, int64(len(full)-captureBytes)))
	if !strings.HasSuffix(string(entry.Data), marker) {
		t.Errorf("the artifact does not end in the marker %q", marker)
	}
	if !strings.HasPrefix(string(entry.Data), full[:captureBytes]) {
		t.Error("the artifact does not start with the stream's first 4 MiB")
	}
	if got := len(entry.Data); got != captureBytes+len(marker) {
		t.Errorf("the artifact is %d bytes; want the first %d plus the marker", got, captureBytes)
	}
}

// readArtifact reads a text artifact whole through artifact.read's handler.
func (h *harness) readArtifact(t *testing.T, ref string, want int) string {
	t.Helper()
	d, ok := h.reg.Lookup("artifact.read")
	if !ok {
		t.Fatal("artifact.read is not registered")
	}
	limit := want
	if limit > artifact.MaxTextLimit {
		limit = artifact.MaxTextLimit
	}
	call := &capability.Call{
		Params: artifacts.ReadParams{Ref: ref, Limit: &limit},
	}
	got, err := d.Handler(context.Background(), call)
	if err != nil {
		t.Fatalf("artifact.read: %v", err)
	}
	// D6, ADR-0015 §1: command output read back through artifact.read is marked untrusted.
	if call.Untrusted == nil || !*call.Untrusted {
		t.Errorf("artifact.read of %s did not mark the call untrusted", ref)
	}
	r, ok := got.(artifacts.ReadResult)
	if !ok || r.Text == nil {
		t.Fatalf("artifact.read returned %T %+v, want text", got, got)
	}
	return *r.Text
}

// TestRunKillsTheCommandsBackgroundProcesses: at the end of the call the whole process
// group is killed, so a background process the command left behind does not outlive it
// (plan m1k D5). The command exits at once; its background `sleep 30` holds the output
// pipe, so only the group kill (and the bounded pipe read) ends the call. Seen failing
// with the shell killed by pid: the background sleep survives; and with the pipes read
// to EOF before the group kill: the call hangs on the background child.
func TestRunKillsTheCommandsBackgroundProcesses(t *testing.T) {
	h := newHarness(t)
	h.enable(t)
	start := time.Now()
	res := mustResult(t, h.invoke(t, "sleep 30 & echo $! > bg; exit 0", 0))
	if time.Since(start) > 5*time.Second {
		t.Errorf("the call took %s; the background child held it", time.Since(start))
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", res.ExitCode)
	}
	raw, err := os.ReadFile(filepath.Join(h.root, "bg"))
	if err != nil {
		t.Fatalf("the command did not write its background pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("background pid %q: %v", raw, err)
	}
	if !waitDead(t, pid, 3*time.Second) {
		t.Errorf("the background sleep (pid %d) survived the call", pid)
	}
}

// TestRunTimesOutAndKillsTheGroup: a command still running at timeoutMs is killed and
// the result is completed with timedOut true and the partial output; a shell that
// ignores SIGTERM (and hands the ignore to `sleep`) is SIGKILLed after the grace, and
// the group is gone (plan m1k D5, D6). Seen failing with the group never signalled.
func TestRunTimesOutAndKillsTheGroup(t *testing.T) {
	// An attempt counts only when the pid file exists: it is written after the trap, so
	// the SIGTERM was ignored as the test assumes. Under load the timeout can fire before
	// the shell reaches the trap; that attempt proves nothing, so it is made again, at
	// most five times, then skipped as inconclusive (as TestServeStartsThroughAProbe does).
	for attempt := 0; attempt < 5; attempt++ {
		h := newHarness(t)
		h.enable(t)
		start := time.Now()
		res := mustResult(t, h.invoke(t, "trap '' TERM; echo $$ > pid; sleep 60", 1000))
		raw, err := os.ReadFile(filepath.Join(h.root, "pid"))
		if err != nil || strings.TrimSpace(string(raw)) == "" {
			continue
		}
		if !res.TimedOut {
			t.Error("timedOut is false")
		}
		if res.Signal != "SIGKILL" {
			t.Errorf("signal = %q, want SIGKILL after the grace", res.Signal)
		}
		if d := time.Since(start); d > 6*time.Second {
			t.Errorf("the timeout took %s; want under the term grace plus the kill grace", d)
		}
		pgid := h.pidFromFile(t, "pid")
		if groupAlive(pgid) {
			t.Errorf("the process group %d survived the timeout", pgid)
		}
		return
	}
	t.Skip("the timeout fired before the shell reached its trap in all 5 attempts: inconclusive")
}

// TestCancelAndWorkspaceCloseEndTheCall: an operation.cancel and a workspace close
// during a long command end the call cancelled, not completed, with no process group
// left (ADR-0020; ADR-0027; plan m1k D5).
func TestCancelAndWorkspaceCloseEndTheCall(t *testing.T) {
	for _, c := range []struct {
		name   string
		cancel func(h *harness, id string)
	}{
		{"operation.cancel", func(h *harness, id string) { h.ops.Cancel(id) }},
		{"workspace.close", func(h *harness, _ string) { h.ops.CancelWorkspace(testWS) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.enable(t)
			id := h.start(t, "echo $$ > pid; sleep 60", 0)
			pgid := h.waitForPid(t, "pid")
			c.cancel(h, id)
			out := h.wait(t, id)
			if out.State != capability.StateCancelled {
				t.Fatalf("operation ended %s (error %+v), want cancelled", out.State, out.Error)
			}
			if !waitGroupDead(t, pgid, 3*time.Second) {
				t.Errorf("the process group %d survived %s", pgid, c.name)
			}
		})
	}
}

// TestRecordsTheCommandWhileItRuns: the command's pid is recorded while it runs (kind
// shell-command) and the record is gone once it ends, so a daemon that dies leaves it
// findable and the next start reconciles an empty file (plan m1k D5;
// ADR-0021 §Pid records).
func TestRecordsTheCommandWhileItRuns(t *testing.T) {
	h := newHarness(t)
	h.enable(t)
	id := h.start(t, "echo $$ > pid; sleep 60", 0)
	h.waitForPid(t, "pid")
	recs := h.records(t)
	if len(recs) != 1 {
		t.Fatalf("processes.json holds %d records while the command runs, want 1: %+v", len(recs), recs)
	}
	if recs[0].Kind != process.ShellCommand || recs[0].ID != string(process.ShellCommand) {
		t.Errorf("record kind/id = %q/%q, want %q/%q", recs[0].Kind, recs[0].ID, process.ShellCommand, process.ShellCommand)
	}
	h.ops.Cancel(id)
	if out := h.wait(t, id); out.State != capability.StateCancelled {
		t.Fatalf("operation ended %s, want cancelled", out.State)
	}
	if recs := h.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the call, want none", recs)
	}
}

// TestUnwritableRecordFailsAndLeavesNothingRunning: when the pid record cannot be
// written, the command's group is killed and the call fails SHELL_EXEC_FAILED, whose
// cause says the command had started and may have had effects (plan m1k D5). Seen
// failing with the error mapped as if nothing had run.
func TestUnwritableRecordFailsAndLeavesNothingRunning(t *testing.T) {
	h := newHarness(t)
	h.enable(t)
	// A path the store's write cannot pass: a non-empty directory where its tmp file
	// would go. This fails whatever the test's uid, so the case is deterministic.
	tmp := filepath.Join(h.procDir, process.RecordsFile+".tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "held"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := h.invoke(t, "echo $$ > pid; sleep 60", 0)
	err := mustFail(t, out, capability.ErrShellExecFailed)
	if !strings.Contains(err.Cause, "had started") {
		t.Errorf("cause %q does not say the command had started", err.Cause)
	}
	// The command had started, so its pid file may exist; if it does, the process must
	// be gone (the starter killed the group before returning the error).
	if raw, readErr := os.ReadFile(filepath.Join(h.root, "pid")); readErr == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		if pid > 0 && !waitDead(t, pid, 2*time.Second) {
			t.Errorf("the command (pid %d) survived a record that could not be written", pid)
		}
	}
	if recs := h.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v, want none", recs)
	}
}

// pidFromFile reads a pid the command wrote.
func (h *harness) pidFromFile(t *testing.T, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.root, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("%s holds %q: %v", name, raw, err)
	}
	return pid
}

// waitForPid waits until the command has written its pid, then returns the process group
// (the shell is its own leader).
func (h *harness) waitForPid(t *testing.T, name string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if raw, err := os.ReadFile(filepath.Join(h.root, name)); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
	}
	t.Fatalf("the command never wrote %s", name)
	return 0
}

// waitGroupDead polls until no process remains in the group or the bound passes.
func waitGroupDead(t *testing.T, pgid int, bound time.Duration) bool {
	t.Helper()
	for deadline := time.Now().Add(bound); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !groupAlive(pgid) {
			return true
		}
	}
	return !groupAlive(pgid)
}

// --- The surface ---------------------------------------------------------------

// TestAnnotations: shell.exec is a destructive, non-idempotent, open-world command
// (plan m1k D6, R2; ADR-0016 §Decision). Seen failing with any hint derived wrongly.
func TestAnnotations(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, Options{})
	d, ok := reg.Lookup("shell.exec")
	if !ok {
		t.Fatal("shell.exec is not registered")
	}
	a := d.ResolveAnnotations()
	if a.ReadOnly {
		t.Error("read-only is true; shell.exec runs a command")
	}
	if !a.Destructive {
		t.Error("destructive is false; shell.exec may change anything")
	}
	if a.Idempotent {
		t.Error("idempotent is true; a command need not be")
	}
	if !a.OpenWorld {
		t.Error("open world is false; a command reaches the whole machine")
	}
	if d.Kind != capability.Command || d.Scope != capability.Workspace || !d.LongRunning {
		t.Errorf("kind/scope/longRunning = %v/%v/%v, want command/workspace/true", d.Kind, d.Scope, d.LongRunning)
	}
	if !d.Untrusted() {
		t.Error("the descriptor does not mark its result untrusted; stdout and stderr are a command's text")
	}
	for _, code := range []capability.ErrorCode{
		capability.ErrPolicyShellDisabled, capability.ErrWorkspaceUntrusted,
		capability.ErrShellExecFailed, capability.ErrInvalidParams,
	} {
		found := false
		for _, e := range d.Errors {
			if e == code {
				found = true
			}
		}
		if !found {
			t.Errorf("the descriptor does not list %s", code)
		}
	}
}

// Decision (D6): invalid UTF-8 in a stream is replaced inline, never passed through.
func TestRunReplacesInvalidUTF8(t *testing.T) {
	h := newHarness(t)
	h.enable(t)
	res := mustResult(t, h.invoke(t, `printf '\377ok'`, 0))
	if res.Stdout != "\uFFFDok" {
		t.Errorf("stdout = %q; want %q", res.Stdout, "\uFFFDok")
	}
}

// --- cwd and env (plan m1p D1) ---------------------------------------------------

// invokeP runs shell.exec with full parameters.
func (h *harness) invokeP(t *testing.T, p Params) capability.Outcome {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.ops.Invoke(context.Background(), capability.Request{
		Capability: "shell.exec", Workspace: testWS, Params: raw,
	})
	if err != nil {
		t.Fatalf("shell.exec: %v", err)
	}
	return out
}

// TestCwdRunsInAnInnerDirectoryAndRefusesEscapes decides that cwd is a root-relative
// directory resolved with the file resolver's rules: an inner directory is the working
// directory; "..", a symlink that leaves the root, a missing path and a regular file are
// REQUEST_INVALID_PARAMS naming cwd (not a FILE_* code) and nothing is spawned. A
// symlink that stays inside the root is followed.
func TestCwdRunsInAnInnerDirectoryAndRefusesEscapes(t *testing.T) {
	h := newHarness(t)
	h.enable(t)
	if err := os.Mkdir(filepath.Join(h.root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "plain"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	outside := h.env.Canonical(t.TempDir())
	if err := os.Symlink(outside, filepath.Join(h.root, "leak")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app", filepath.Join(h.root, "inner")); err != nil {
		t.Fatal(err)
	}

	for _, cwd := range []string{"app", "inner"} {
		res := mustResult(t, h.invokeP(t, Params{Command: "pwd", Cwd: cwd}))
		if got := strings.TrimSpace(res.Stdout); got != filepath.Join(h.root, "app") {
			t.Errorf("cwd %q: pwd = %q, want %q", cwd, got, filepath.Join(h.root, "app"))
		}
	}
	for _, cwd := range []string{"../x", "..", "leak", "leak/sub", "missing", "plain", outside} {
		out := h.invokeP(t, Params{Command: "touch marker; touch " + outside + "/marker", Cwd: cwd})
		e := mustFail(t, out, capability.ErrInvalidParams)
		if !strings.Contains(e.Message, "cwd") {
			t.Errorf("cwd %q: message %q does not name cwd", cwd, e.Message)
		}
		h.noMarker(t)
		if _, err := os.Stat(filepath.Join(outside, "marker")); err == nil {
			t.Fatalf("cwd %q: the command ran", cwd)
		}
	}
}

// TestEnvIsAppliedOverTheDaemonsAndBounded decides that env is set over the daemon's
// environment (a name already there is replaced, the rest stay) and that a bad name, a
// NUL in a value, 65 entries and more than 32 KiB are REQUEST_INVALID_PARAMS naming env
// before anything runs.
func TestEnvIsAppliedOverTheDaemonsAndBounded(t *testing.T) {
	t.Setenv("DROIDYARD_TEST_KEEP", "kept")
	t.Setenv("DROIDYARD_TEST_OVER", "old")
	h := newHarness(t) // captures os.Environ() as the daemon's environment
	h.enable(t)
	res := mustResult(t, h.invokeP(t, Params{
		Command: `echo "$FOO|$DROIDYARD_TEST_OVER|$DROIDYARD_TEST_KEEP"`,
		Env:     map[string]string{"FOO": "bar", "DROIDYARD_TEST_OVER": "new"},
	}))
	if got := strings.TrimSpace(res.Stdout); got != "bar|new|kept" {
		t.Errorf("env = %q, want bar|new|kept", got)
	}

	many := map[string]string{}
	for i := 0; i < 65; i++ {
		many["V"+strconv.Itoa(i)] = "x"
	}
	for name, env := range map[string]map[string]string{
		"bad name":     {"1A": "x"},
		"dash in name": {"A-B": "x"},
		"empty name":   {"": "x"},
		"NUL in value": {"A": "x\x00y"},
		"65 entries":   many,
		"over 32 KiB":  {"BIG": strings.Repeat("x", envMaxBytes)},
	} {
		out := h.invokeP(t, Params{Command: "touch marker", Env: env})
		e := mustFail(t, out, capability.ErrInvalidParams)
		if !strings.Contains(e.Message, "env") {
			t.Errorf("%s: message %q does not name env", name, e.Message)
		}
		h.noMarker(t)
	}
	// 64 entries and exactly the byte bound are accepted.
	ok := map[string]string{}
	for i := 0; i < 64; i++ {
		ok["V"+strconv.Itoa(i)] = "x"
	}
	mustResult(t, h.invokeP(t, Params{Command: "true", Env: ok}))
	mustResult(t, h.invokeP(t, Params{Command: "true", Env: map[string]string{"B": strings.Repeat("x", envMaxBytes-1)}}))
}
