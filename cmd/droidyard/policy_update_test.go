package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// envAtState is the environment the in-process daemon reads its configuration from: the
// same state root (plan m1g step 3), so a test reads what the daemon wrote.
func envAtState(p *inProcess) platform.Env {
	return platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return p.state
		}
		return os.Getenv(k)
	}}
}

// TestPolicyUpdateAnswersTheHumanChannels: policy.update is answered for a human client
// — `droidyard call` and POST /v1/call — applies the patch and returns policy.get's
// shape (ADR-0020; ADR-0032 §5; plan m1k D3). The second patch merges into the first's
// result.
func TestPolicyUpdateAnswersTheHumanChannels(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	httpAddr, port := p.d.HTTPAddr(), portOf(t, p.d.HTTPAddr())

	env, code := p.call(t, "policy.update", "--params", `{"shell": {"enabled": true}}`)
	if code != 0 || env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("CLI policy.update exited %d with %+v; want completed", code, env)
	}
	var got policy.GetResult
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("policy.update result %s: %v", env.Result, err)
	}
	if got.State != policy.StateOK || !got.Shell.Enabled {
		t.Errorf("CLI policy.update = %+v; want state ok and shell enabled", got)
	}

	root := platform.Current().Canonical(t.TempDir())
	resp := callRequest(port, p.token, `{"id":"u","capability":"policy.update","params":{"trust":{"roots":[`+strconv.Quote(root)+`]}}}`).send(t, httpAddr)
	if resp.StatusCode != 200 {
		t.Fatalf("POST /v1/call policy.update: %d; want 200", resp.StatusCode)
	}
	henv := envelopeOf(t, resp)
	if henv.Error != nil || henv.Result == nil {
		t.Fatalf("POST /v1/call policy.update: %+v; want a result", henv)
	}
	if g := policy.New(envAtState(p)).Get(); !g.Shell.Enabled || len(g.Trust.Roots) != 1 || g.Trust.Roots[0] != root {
		t.Errorf("after both updates: %+v; want shell enabled and the root %s trusted", g, root)
	}
}

// TestPolicyUpdateIsRefusedFromTheCoreOnMCP: the core refuses policy.update from an MCP
// client with REQUEST_NOT_ALLOWED_HERE and answers it for a human client (ADR-0015 §6;
// plan m1k A1). The adapter's tools/list omission is asserted by TestMCPTranscript,
// which holds tools/list to the golden file.
func TestPolicyUpdateIsRefusedFromTheCoreOnMCP(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	req := capability.Request{Capability: "policy.update", Params: json.RawMessage(`{"shell":{"enabled":true}}`)}

	req.Client = capability.ClientMCP
	_, err := p.ops.Invoke(context.Background(), req)
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere {
		t.Fatalf("policy.update as client mcp: %v; want REQUEST_NOT_ALLOWED_HERE", err)
	}
	req.Client = capability.ClientCLI
	if _, err := p.ops.Invoke(context.Background(), req); err != nil {
		t.Errorf("policy.update as client cli: %v; want it answered", err)
	}
}

// TestPolicyUpdateEnablesTheShellWithoutARestart is the effect row of step 3's check
// (plan m1k A3): a root trusted with the shell off refuses shell.exec with
// POLICY_SHELL_DISABLED; the human enables the shell for the root through
// policy.update; with no restart the next shell.exec there runs.
func TestPolicyUpdateEnablesTheShellWithoutARestart(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	root := platform.Current().Canonical(t.TempDir())
	writeConfig(t, p, `{"schemaVersion":1,"trust":{"roots":[`+strconv.Quote(root)+`]}}`)
	if env, code := p.call(t, "workspace.open", "--workspace", root); code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, env.Error)
	}

	env, code := p.call(t, "shell.exec", "--workspace", root, "--params", `{"command":"echo hi"}`)
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrPolicyShellDisabled {
		t.Fatalf("shell.exec before enabling = (%+v, %d); want POLICY_SHELL_DISABLED", env, code)
	}

	if env, code := p.call(t, "policy.update", "--params", `{"shell":{"roots":[`+strconv.Quote(root)+`]}}`); code != 0 || env.Error != nil {
		t.Fatalf("policy.update exited %d: %+v", code, env.Error)
	}

	env, code = p.call(t, "shell.exec", "--workspace", root, "--params", `{"command":"echo hi"}`)
	if code != 0 || env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("shell.exec after enabling = (%+v, %d); want completed", env, code)
	}
	if !strings.Contains(string(env.Result), "hi") {
		t.Errorf("shell.exec result %s; want the command's output", env.Result)
	}
}

// TestPolicyUpdateHoldsTheLockAgainstAConcurrentTrust is the deterministic concurrency
// row (plan m1k D3): the update seam pauses policy.update between its read and its
// write, holding the exclusive lock; the real `droidyard trust` process runs, blocked
// on that lock; the seam releases; both the trusted root and the update's change are
// present. Without the lock a read-modify-write would lose one of them.
func TestPolicyUpdateHoldsTheLockAgainstAConcurrentTrust(t *testing.T) {
	p := startInProcess(t)
	root := platform.Current().Canonical(t.TempDir())

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	policy.SetUpdateSeam(func() {
		once.Do(func() { close(entered) })
		<-release
	})
	t.Cleanup(func() { policy.SetUpdateSeam(nil) })

	done := make(chan error, 1)
	go func() {
		_, err := p.ops.Invoke(context.Background(), capability.Request{
			Capability: "policy.update", Params: json.RawMessage(`{"devices":{"allowPhysical":true}}`), Client: capability.ClientCLI,
		})
		done <- err
	}()
	<-entered

	// The update holds the lock while paused in the seam. The trust process blocks on it.
	trust := droidyard(t, p.state, "trust", root)
	time.Sleep(400 * time.Millisecond)
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("policy.update: %v", err)
	}
	if code := trust.wait(t); code != 0 {
		t.Fatalf("droidyard trust exited %d", code)
	}

	g := policy.New(envAtState(p)).Get()
	if len(g.Trust.Roots) != 1 || g.Trust.Roots[0] != root {
		t.Errorf("the trust was lost to the update: roots = %v; want [%s]", g.Trust.Roots, root)
	}
	if !g.Devices.AllowPhysical {
		t.Errorf("the update was lost to the trust: devices = %+v; want allowPhysical true", g.Devices)
	}
}

// TestConcurrentTrustProcessesAndUpdatesLoseNothing is the racing row (plan m1k D3): 8
// real `droidyard trust` processes with distinct roots run while 8 updates toggle
// devices.allowPhysical; the exclusive lock keeps every root and leaves the file valid.
func TestConcurrentTrustProcessesAndUpdatesLoseNothing(t *testing.T) {
	p := startInProcess(t)
	want := make([]string, 8)
	for i := range want {
		want[i] = platform.Current().Canonical(t.TempDir())
	}

	procs := make([]*process, len(want))
	for i := range want {
		procs[i] = droidyard(t, p.state, "trust", want[i])
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(want))
	for i := range want {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := `{"devices":{"allowPhysical":true}}`
			if i%2 == 0 {
				body = `{"devices":{"allowPhysical":false}}`
			}
			out, err := p.ops.Invoke(context.Background(), capability.Request{
				Capability: "policy.update", Params: json.RawMessage(body), Client: capability.ClientCLI,
			})
			if err != nil {
				errs <- err
				return
			}
			if out.State != capability.StateCompleted {
				errs <- errors.New("an update did not complete: " + string(out.State))
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent update failed: %v", err)
	}
	for i := range procs {
		if code := procs[i].wait(t); code != 0 {
			t.Errorf("droidyard trust %s exited %d", want[i], code)
		}
	}

	g := policy.New(envAtState(p)).Get()
	if g.State != policy.StateOK {
		t.Fatalf("the raced file = %+v; want state ok", g)
	}
	for _, r := range want {
		if !containsStr(g.Trust.Roots, r) {
			t.Errorf("root %s was lost to a racing writer; roots = %v", r, g.Trust.Roots)
		}
	}
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
