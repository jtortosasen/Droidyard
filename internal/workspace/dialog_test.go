package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// startGroup is the spawner the daemon passes, as the supervisor's Spawn does it: the
// child in a process group of its own, so a group kill reaches what it forked.
func startGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

// fakeDialog is a Dialog whose osascript is a shell script with the given body.
func fakeDialog(t *testing.T, body string) Dialog {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "osascript")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Dialog{GOOS: "darwin", Binary: bin, Spawn: startGroup}
}

type dialogHarness struct {
	ops *capability.Operations
}

func newDialogHarness(t *testing.T, d Dialog) *dialogHarness {
	t.Helper()
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	Register(reg, New(Options{Dialog: d}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ops.Shutdown(ctx)
	})
	return &dialogHarness{ops: ops}
}

func (h *dialogHarness) choose(t *testing.T) capability.Outcome {
	t.Helper()
	out, err := h.ops.Invoke(context.Background(), capability.Request{Capability: CapChooseRoot, Client: capability.ClientUI})
	if err != nil {
		t.Fatalf("workspace.chooseRoot rejected at Start: %v", err)
	}
	return out
}

func (h *dialogHarness) start(t *testing.T) string {
	t.Helper()
	id, err := h.ops.Start(context.Background(), capability.Request{Capability: CapChooseRoot, Client: capability.ClientUI})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if raw, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(raw), "\n") {
			return strings.TrimSpace(string(raw))
		}
	}
	t.Fatalf("%s was never written", path)
	return ""
}

func gone(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }

func waitGone(t *testing.T, what string, pid int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if gone(pid) {
			return
		}
	}
	t.Errorf("%s (pid %d) is still running", what, pid)
}

// TestDialogArgv: the dialog's command line is the one observed at plan m2c step 2 D,
// with no shell between the daemon and osascript: the window is raised first, then a
// folder is asked for. The fake osascript records the arguments it received, so the
// quotes survive the spawn unchanged.
func TestDialogArgv(t *testing.T) {
	want := []string{"/usr/bin/osascript",
		"-e", "tell me to activate",
		"-e", `POSIX path of (choose folder with prompt "Choose a project folder")`}
	if got := NewDialog(nil).argv(); !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q; want %q", got, want)
	}
	rec := filepath.Join(t.TempDir(), "args")
	d := fakeDialog(t, `for a in "$@"; do printf '%s\n' "$a"; done > '`+rec+`.tmp'; mv '`+rec+`.tmp' '`+rec+`'; echo /tmp/x/`)
	if _, _, err := d.choose(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n"); !reflect.DeepEqual(got, want[1:]) {
		t.Errorf("osascript received %q; want %q", got, want[1:])
	}
}

// TestChooseRootAppliesTheBindingRule: a folder inside a project answers the project's
// root, the nearest ancestor holding settings.gradle(.kts); a folder outside any project
// answers itself, cleaned of osascript's trailing slash.
func TestChooseRootAppliesTheBindingRule(t *testing.T) {
	base := project(t)
	proj := filepath.Join(base, "proj")
	inside := filepath.Join(proj, "app", "src")
	loose := filepath.Join(base, "loose")
	for _, d := range []string{inside, loose} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(proj, "settings.gradle.kts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, chosen, want string }{
		{"inside a project", inside, proj},
		{"the project itself", proj, proj},
		{"outside any project", loose, loose},
	} {
		h := newDialogHarness(t, fakeDialog(t, "echo '"+c.chosen+"/'"))
		out := h.choose(t)
		if out.State != capability.StateCompleted {
			t.Fatalf("%s: %s %v", c.name, out.State, out.Error)
		}
		if got := out.Result.(ChooseRootResult); got != (ChooseRootResult{Root: c.want}) {
			t.Errorf("%s: %+v; want root %s", c.name, got, c.want)
		}
	}
}

// TestChooseRootDismissedIsNotAFailure: the user's Cancel is osascript's error -128 on
// stderr and a nonzero exit, and answers {dismissed: true}; any other nonzero exit is a
// failure that carries osascript's words.
func TestChooseRootDismissedIsNotAFailure(t *testing.T) {
	cancelled := newDialogHarness(t, fakeDialog(t, `echo "execution error: Cancelado por el usuario. (-128)" >&2; exit 1`))
	out := cancelled.choose(t)
	if out.State != capability.StateCompleted || out.Result.(ChooseRootResult) != (ChooseRootResult{Dismissed: true}) {
		t.Errorf("cancel: %s %+v %v; want completed, dismissed", out.State, out.Result, out.Error)
	}

	broken := newDialogHarness(t, fakeDialog(t, `echo "osascript: no window server" >&2; exit 1`))
	out = broken.choose(t)
	if out.State != capability.StateFailed || out.Error.Code != ErrDialogFailed || !strings.Contains(out.Error.Cause, "no window server") {
		t.Errorf("a crash: %s %+v; want WORKSPACE_DIALOG_FAILED carrying osascript's words", out.State, out.Error)
	}

	relative := newDialogHarness(t, fakeDialog(t, `echo "not/absolute"`))
	if out = relative.choose(t); out.State != capability.StateFailed || out.Error.Code != ErrDialogFailed {
		t.Errorf("a relative answer: %s %+v; want WORKSPACE_DIALOG_FAILED", out.State, out.Error)
	}
}

// pidsOf starts the sleeping dialog and returns its pid and its child's, once written.
const sleeper = `echo $$ > "$DIR/pid.tmp"; mv "$DIR/pid.tmp" "$DIR/pid"; sleep 60 & echo $! > "$DIR/child.tmp"; mv "$DIR/child.tmp" "$DIR/child"; wait`

func sleepingDialog(t *testing.T) (Dialog, string) {
	t.Helper()
	dir := t.TempDir()
	return fakeDialog(t, "DIR='"+dir+"'\n"+sleeper), dir
}

func pidIn(t *testing.T, dir, name string) int {
	t.Helper()
	pid, err := strconv.Atoi(waitForFile(t, filepath.Join(dir, name)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// TestChooseRootCancelTakesTheDialogDown: operation.cancel kills the dialog's group, the
// panel goes with it, and the operation ends cancelled. A dialog left on the screen of a
// user who was told it was cancelled is the defect this holds against.
func TestChooseRootCancelTakesTheDialogDown(t *testing.T) {
	d, dir := sleepingDialog(t)
	h := newDialogHarness(t, d)
	id := h.start(t)
	pid, child := pidIn(t, dir, "pid"), pidIn(t, dir, "child")
	if gone(pid) {
		t.Fatal("the dialog is not running before the cancel")
	}
	if err := h.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out, err := h.ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	if err != nil || out.State != capability.StateCancelled || out.Error == nil || out.Error.Code != capability.ErrOperationCancelled {
		t.Fatalf("after the cancel: %+v, %v; want cancelled with OPERATION_CANCELLED", out, err)
	}
	waitGone(t, "the dialog", pid)
	waitGone(t, "what the dialog forked", child)
}

// TestChooseRootBoundDismisses: a dialog nobody answers within the bound is taken down and
// answers dismissed, rather than staying on the screen for ever (plan m2c D18).
func TestChooseRootBoundDismisses(t *testing.T) {
	d, dir := sleepingDialog(t)
	d.Bound = 2 * time.Second
	h := newDialogHarness(t, d)
	start := time.Now()
	out := h.choose(t)
	if out.State != capability.StateCompleted || out.Result.(ChooseRootResult) != (ChooseRootResult{Dismissed: true}) {
		t.Fatalf("after the bound: %s %+v %v; want completed, dismissed", out.State, out.Result, out.Error)
	}
	if took := time.Since(start); took < 1900*time.Millisecond || took > 8*time.Second {
		t.Errorf("the dialog ended after %v; want about its bound, 2s", took)
	}
	waitGone(t, "the dialog", pidIn(t, dir, "pid"))
	waitGone(t, "what the dialog forked", pidIn(t, dir, "child"))
}

// TestChooseRootIsOneAtATime: a second call waits behind the first, on the dialog's key,
// and runs when the first ends; two windows are never up together.
func TestChooseRootIsOneAtATime(t *testing.T) {
	d, dir := sleepingDialog(t)
	h := newDialogHarness(t, d)
	first := h.start(t)
	pidIn(t, dir, "pid")
	second := h.start(t)
	out, err := h.ops.Wait(context.Background(), second, time.Now().Add(-time.Second))
	if err != nil || out.State != capability.StateQueued || out.BlockedBy == nil || out.BlockedBy.OperationID != first {
		t.Errorf("the second call is %+v, %v; want queued behind %s", out, err, first)
	}
	h.ops.Cancel(first)
	h.ops.Cancel(second)
}

// TestChooseRootIsRefusedToAnAgent: the audience rule of ADR-0045 §4: the dialog is a
// person's choice, so an MCP session is refused at Start, before any window exists.
func TestChooseRootIsRefusedToAnAgent(t *testing.T) {
	d, dir := sleepingDialog(t)
	h := newDialogHarness(t, d)
	_, err := h.ops.Start(context.Background(), capability.Request{Capability: CapChooseRoot, Client: capability.ClientMCP})
	if codeOf(err) != capability.ErrNotAllowedHere {
		t.Errorf("an MCP session got %v; want NOT_ALLOWED_HERE", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, statErr := os.Stat(filepath.Join(dir, "pid")); statErr == nil {
		t.Error("the dialog was raised for an MCP session")
	}
}

// TestChooseRootUnavailable: off macOS, without osascript or without a spawner the call
// fails WORKSPACE_DIALOG_FAILED with the reason, and the requirement reports the same
// reason, so the UI can fall back to a typed field.
func TestChooseRootUnavailable(t *testing.T) {
	spawned := false
	spawn := func(cmd *exec.Cmd) error { spawned = true; return startGroup(cmd) }
	for _, c := range []struct {
		name   string
		d      Dialog
		reason string
	}{
		{"linux", Dialog{GOOS: "linux", Binary: "/bin/sh", Spawn: spawn}, "macOS only"},
		{"no osascript", Dialog{GOOS: "darwin", Binary: filepath.Join(t.TempDir(), "osascript"), Spawn: spawn}, "osascript was not found"},
		{"no spawner", Dialog{GOOS: "darwin", Binary: "/bin/sh"}, "no process spawner"},
		{"not configured", Dialog{}, "no folder dialog"},
	} {
		reason, missing := c.d.Missing()
		if !missing || !strings.Contains(reason, c.reason) {
			t.Errorf("%s: Missing = %q, %v; want missing, naming %q", c.name, reason, missing, c.reason)
		}
		out := newDialogHarness(t, c.d).choose(t)
		if out.State != capability.StateFailed || out.Error.Code != ErrDialogFailed || !strings.Contains(out.Error.Cause, c.reason) {
			t.Errorf("%s: %s %+v; want WORKSPACE_DIALOG_FAILED naming %q", c.name, out.State, out.Error, c.reason)
		}
	}
	if spawned {
		t.Error("an unavailable dialog was spawned")
	}
	if _, missing := fakeDialog(t, "true").Missing(); missing {
		t.Error("a darwin dialog with an executable and a spawner is reported missing")
	}
}
