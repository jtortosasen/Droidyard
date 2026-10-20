package git

// The write runner and git.fetch (plan m2n part 2, step P2; ADR-0071 §1–§4, ADR-0073).
// Real git over the fixtures of gittest.Remote, started through the real supervisor's
// recorded spawner, so what is asserted about the process group, the session and the pid
// record is what the daemon does. Each test says which decision it holds and, where the
// plan asks, what it was seen failing under.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// wfx is git.fetch registered over a Remote fixture, with the recorded spawner of a real
// supervisor and a count of every git it starts.
type wfx struct {
	t   *testing.T
	e   *env
	r   *gittest.Remotes
	sup *process.Supervisor
	st  *store.Store
	reg *capability.Registry
	ops *capability.Operations

	// roots maps a workspace id to its root; "ws" is r.W.
	roots map[string]string
	// authorize is the trust hook; nil is the golden registry's (every root untrusted).
	authorize func(ctx context.Context, call *capability.Call) (any, error)
	// writeStarts and readStarts count the gits started recorded and plain.
	writeStarts, readStarts atomic.Int32
	// look, when set, replaces the git executable the capabilities find.
	look func(string) (string, error)
	// timeout, when > 0, shortens every write's limit.
	timeout time.Duration
	// pgids are the process groups of the write gits started, in order.
	pgidsMu sync.Mutex
	pgids   []int
	// dirs are the directories the write gits ran in, in the order they started.
	dirs []string
}

type fakeWorkspaces map[string]bool

func (w fakeWorkspaces) Admit(target string, record func(id string)) bool {
	if !w[target] {
		return false
	}
	record(target)
	return true
}

// newWfx is the write fixture over a Remote of its own, for a test that writes in it (or in
// anything it holds), or that changes it in any way.
func newWfx(t *testing.T) *wfx {
	t.Helper()
	e := needGit(t)
	return newWfxOn(t, e, gittest.Remote(t, filepath.Join(t.TempDir(), "fx")))
}

// newWfxShared is the write fixture over the shared Remote of shared_test.go, for a test
// that only reads it: the calls it makes are refused, or start no git, or only list. A call
// that is accepted and writes is not for this fixture.
func newWfxShared(t *testing.T) *wfx {
	t.Helper()
	e := needGit(t)
	return newWfxOn(t, e, sharedRemote(t))
}

// newWfxBare is the write fixture with no repository: workspace "ws" is an empty directory
// and f.r is nil. For a test that points "ws" at a fixture of its own (gittest.Submodules),
// or whose calls never reach a repository.
func newWfxBare(t *testing.T) *wfx {
	t.Helper()
	return newWfxOn(t, needGit(t), nil)
}

func newWfxOn(t *testing.T, e *env, r *gittest.Remotes) *wfx {
	t.Helper()
	f := &wfx{t: t, e: e, r: r, roots: map[string]string{}}
	if r != nil {
		f.roots["ws"] = platform.Current().Canonical(r.W)
	} else {
		f.roots["ws"] = platform.Current().Canonical(t.TempDir())
	}
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.st = st
	f.sup = process.New(process.Options{Store: st, Log: os.Stderr})
	if err := f.sup.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.sup.Shutdown)
	// Whatever a failing test leaves of the write gits it started, by their own groups.
	t.Cleanup(func() {
		for _, pgid := range f.groups() {
			syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})
	f.authorize = func(ctx context.Context, call *capability.Call) (any, error) { return f.roots[call.Workspace], nil }
	f.register()
	return f
}

// groups is a copy of the process groups started so far.
func (f *wfx) groups() []int {
	f.pgidsMu.Lock()
	defer f.pgidsMu.Unlock()
	return slices.Clone(f.pgids)
}

// groupsOnce waits for the first write git's group, up to 10 s: the supervisor notes the
// group when SpawnRecorded returns, after git started, so a fast host can run git's
// children (an uploadpack writing its pid) before the group is noted.
func (f *wfx) groupsOnce() []int {
	f.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if g := f.groups(); len(g) > 0 {
			return g
		}
	}
	f.t.Fatal("no write git was recorded in 10 s")
	return nil
}

// writeDirs is a copy of the directories the write gits ran in so far, in start order.
func (f *wfx) writeDirs() []string {
	f.pgidsMu.Lock()
	defer f.pgidsMu.Unlock()
	return slices.Clone(f.dirs)
}

func (f *wfx) register() {
	f.reg = capability.NewRegistry()
	Register(f.reg, Options{
		Root: func(id string) (string, bool) {
			root, ok := f.roots[id]
			return root, ok
		},
		Env:     platform.Current(),
		Trusted: func(string) (bool, *capability.Error) { return true, nil },
		Environ: f.e.osEnviron,
		Starter: func(cmd *exec.Cmd) error {
			f.readStarts.Add(1)
			return groupStarter(cmd)
		},
		Authorize: f.authorize,
		Recorded: func(cmd *exec.Cmd, kind string) (Recorded, error) {
			f.writeStarts.Add(1)
			rec, err := f.sup.SpawnRecorded(cmd, process.Kind(kind))
			if err != nil {
				return nil, err
			}
			f.pgidsMu.Lock()
			f.pgids = append(f.pgids, rec.Pid())
			f.dirs = append(f.dirs, cmd.Dir)
			f.pgidsMu.Unlock()
			return rec, nil
		},
		LookPath: func(name string) (string, error) {
			if f.look != nil {
				return f.look(name)
			}
			return exec.LookPath(name)
		},
		writeTimeout: f.timeout,
	})
	f.ops = capability.NewOperations(f.reg)
	ws := fakeWorkspaces{}
	for id := range f.roots {
		ws[id] = true
	}
	f.ops.UseWorkspaces(ws)
}

// call runs a capability's handler directly (the scheduler and its gate not involved).
func (f *wfx) call(name, ws string, params any) (any, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup(name)
	if !ok {
		f.t.Fatalf("%s is not registered", name)
	}
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: ws, Params: params})
	if err != nil {
		return nil, capability.AsError(err)
	}
	return res, nil
}

// start starts a capability through the scheduler, as the daemon does.
func (f *wfx) start(name, ws string, params any) string {
	f.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Workspace: ws, Params: raw})
	if err != nil {
		f.t.Fatalf("Start %s: %v", name, err)
	}
	return id
}

func (f *wfx) snap(id string) capability.Outcome {
	f.t.Helper()
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(-time.Second))
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// until waits for the operation to be terminal and returns it; it fails after d.
func (f *wfx) until(id string, d time.Duration) capability.Outcome {
	f.t.Helper()
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(d))
	if err != nil {
		f.t.Fatal(err)
	}
	if !out.State.Terminal() {
		f.t.Fatalf("operation %s is still %s after %v", id, out.State, d)
	}
	return out
}

func (f *wfx) waitState(id string, want capability.State, d time.Duration) capability.Outcome {
	f.t.Helper()
	var out capability.Outcome
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if out = f.snap(id); out.State == want {
			return out
		}
	}
	f.t.Fatalf("operation %s is %s; want %s", id, out.State, want)
	return out
}

// records reads the pid records straight from the store's file.
func (f *wfx) records() []process.Record {
	f.t.Helper()
	var recs process.Records
	if _, err := f.st.Read(process.RecordsFile, process.RecordsVersion, &recs); err != nil {
		f.t.Fatal(err)
	}
	return recs.Processes
}

// hung makes the remote's uploadpack a script that records its pid and hangs; stubborn
// ignores SIGTERM (and so, inherited, does everything it starts). It returns the pid file.
func (f *wfx) hung(stubborn bool) string { return f.e.hangingUploadpack(f.r.W, stubborn) }

// pidIn waits for the pid a hung uploadpack wrote.
func pidIn(t *testing.T, file string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(waitFile(t, file, 30*time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func wantErrCode(t *testing.T, err *capability.Error, code capability.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s, got success", code)
	}
	if err.Code != code {
		t.Fatalf("want %s, got %s (%s; cause %q; nextStep %q)", code, err.Code, err.Message, err.Cause, err.NextStep)
	}
}

// Decision (ADR-0071 §1): git.fetch brings the remote's new commits in as remote-tracking
// refs, for every remote or the one named, and the result names the remotes fetched. No
// hook runs (the fixture's hooks write markers) and no local branch moves. The control is
// the state before: origin/main is behind until the fetch.
func TestFetchFromALocalRemote(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	pushed := f.r.PushOther(t, 1)
	if got := f.e.rev(f.r.W, "origin/main"); got == pushed {
		t.Fatalf("control: origin/main is already the pushed commit %s", pushed)
	}
	mainBefore := f.e.rev(f.r.W, "main")

	res, err := f.call("git.fetch", "ws", FetchParams{})
	if err != nil {
		t.Fatalf("git.fetch: %s (%s; cause %q)", err.Code, err.Message, err.Cause)
	}
	if got := res.(FetchResult).Remotes; !slices.Equal(got, []string{"origin"}) {
		t.Errorf("remotes = %v, want [origin]", got)
	}
	if got := f.e.rev(f.r.W, "origin/main"); got != pushed {
		t.Errorf("origin/main = %s after the fetch, want the colleague's commit %s", got, pushed)
	}
	if got := f.e.rev(f.r.W, "main"); got != mainBefore {
		t.Errorf("the local main moved from %s to %s: a fetch changes no branch", mainBefore, got)
	}

	// One remote by name, with a second push.
	pushed = f.r.PushOther(t, 2)
	res, err = f.call("git.fetch", "ws", FetchParams{Remote: "origin"})
	if err != nil {
		t.Fatalf("git.fetch origin: %s (%s; cause %q)", err.Code, err.Message, err.Cause)
	}
	if got := res.(FetchResult).Remotes; !slices.Equal(got, []string{"origin"}) {
		t.Errorf("remotes = %v, want [origin]", got)
	}
	if got := f.e.rev(f.r.W, "origin/main"); got != pushed {
		t.Errorf("origin/main = %s after fetching origin, want %s", got, pushed)
	}
	if marks := hookMarks(t, f.r.Markers, false); marks != "" {
		t.Errorf("a hook ran during the fetch: %q", marks)
	}
	if n := f.writeStarts.Load(); n != 2 {
		t.Errorf("recorded starts = %d, want one per fetch", n)
	}
}

// Decision (ADR-0071 §2): a write (git.fetch, git.updateSubmodules) on a root the trust hook refuses starts no git at all,
// not even a read: WORKSPACE_UNTRUSTED with the nextStep to trust the project, from the
// scheduler's gate before the handler runs. Two gates: the daemon's hook refusing, and no
// hook at all (the golden registry), which refuses every root. Seen failing with the gate
// not installed on the descriptor (write's `d.Authorize = o.gate` removed): the fetch ran.
func TestWritesUntrustedStartNothing(t *testing.T) {
	t.Parallel()
	for _, call := range []struct {
		name   string
		params any
	}{
		{"git.fetch", FetchParams{}},
		{"git.updateSubmodules", UpdateSubmodulesParams{}},
	} {
		for _, c := range []struct {
			name string
			hook func(ctx context.Context, call *capability.Call) (any, error)
		}{
			{"the daemon's hook refuses", func(ctx context.Context, call *capability.Call) (any, error) {
				return nil, policy.UntrustedError("/the/root")
			}},
			{"no hook", nil},
		} {
			t.Run(call.name+", "+c.name, func(t *testing.T) {
				f := newWfxShared(t) // no git starts, so the fixture is only a directory
				f.authorize = c.hook
				f.register()
				id := f.start(call.name, "ws", call.params)
				out := f.until(id, 10*time.Second)
				if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != capability.ErrWorkspaceUntrusted {
					t.Fatalf("outcome %s, error %+v; want failed with WORKSPACE_UNTRUSTED", out.State, out.Error)
				}
				if !strings.Contains(out.Error.NextStep, "droidyard trust") {
					t.Errorf("nextStep %q does not say how to trust the project", out.Error.NextStep)
				}
				if w, r := f.writeStarts.Load(), f.readStarts.Load(); w != 0 || r != 0 {
					t.Errorf("starts: %d write gits and %d reads on an untrusted root, want none", w, r)
				}
			})
		}
	}
}

// Decision (ADR-0071 §3; process-lifecycle.md §Shutdown, cancellation and no-orphans): a
// cancelled write leaves nothing of its group, whatever the transport does with SIGTERM.
// Two hangs: an uploadpack that dies to SIGTERM, and one that ignores it (so git, which
// forwards SIGTERM to its child and waits for it, hangs on the SIGTERM and only the group's
// SIGKILL after the grace ends it). The group's pgid, taken from the uploadpack itself,
// must be the git's own, and kill(-pgid, 0) must be ESRCH by the time the operation ends
// cancelled. Seen failing with the runner signalling git's pid only (stopGroup's
// SignalGroup replaced by unix.Kill(rec.Pid(), sig)): the stubborn uploadpack outlived the
// call.
func TestFetchCancelKillsTheGroup(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	for _, stubborn := range []bool{false, true} {
		name := "uploadpack dies to SIGTERM"
		if stubborn {
			name = "uploadpack ignores SIGTERM"
		}
		t.Run(name, func(t *testing.T) {
			f := newWfx(t)
			pidFile := f.hung(stubborn)
			id := f.start("git.fetch", "ws", FetchParams{Remote: "origin"})
			child := pidIn(t, pidFile)
			pgid := mustPgid(child)
			if g := f.groupsOnce(); len(g) != 1 || pgid != g[0] {
				t.Fatalf("the uploadpack %d is in group %d, the recorded git's is %v", child, pgid, f.groups())
			}
			if !groupAlive(pgid) {
				t.Fatal("the group is not alive before the cancel")
			}
			start := time.Now()
			if err := f.ops.Cancel(id); err != nil {
				t.Fatal(err)
			}
			out := f.until(id, 10*time.Second)
			took := time.Since(start)
			if out.State != capability.StateCancelled {
				t.Errorf("outcome %s, error %+v; want cancelled", out.State, out.Error)
			}
			// The operation ends only when the handler has returned, which is after the
			// group is gone: so the group is gone the moment it is cancelled.
			if groupLive(pgid) {
				t.Errorf("group %d still has a member when the operation ended cancelled", pgid)
			}
			if pidLive(child) {
				t.Errorf("the uploadpack %d is still alive", child)
			}
			if took > 4*time.Second {
				t.Errorf("the cancel took %v; the group should be gone within 3 s", took.Round(time.Millisecond))
			}
			if recs := f.records(); len(recs) != 0 {
				t.Errorf("processes.json holds %+v after the cancel, want none", recs)
			}
		})
	}
}

// Decision (ADR-0071 §3): a write that runs past its limit is stopped, whole group, and
// answers GIT_FAILED saying how long it ran, with a nextStep. The limit is shortened for
// the test (the daemon's is five minutes for a fetch). Seen failing with the runner
// signalling git's pid only (the cancel test's mutation): both uploadpacks survived the call.
//
// The limit runs from the moment git starts, so on a loaded machine it can pass before git
// has started the uploadpack stub, and then the call ends having stopped nothing but git
// itself. That attempt proves nothing about the transport's process (no pid file), so
// untilConclusive tries again with twice the limit; the group is still checked each time. An
// attempt with the pid file is decisive and must end in GIT_FAILED with the group and the
// uploadpack gone. The test fails if no attempt in five got the uploadpack running before
// its limit.
func TestFetchTimeout(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	type answer struct {
		res any
		err *capability.Error
	}
	for _, stubborn := range []bool{false, true} {
		tried, ok := untilConclusive(2*time.Second, func(limit time.Duration) bool {
			f := newWfx(t)
			f.timeout = limit
			f.register()
			pidFile := f.hung(stubborn)
			got := make(chan answer, 1)
			go func() {
				res, err := f.call("git.fetch", "ws", FetchParams{Remote: "origin"})
				got <- answer{res, err}
			}()
			// The uploadpack's pid, unless the call ends first.
			child := 0
			var a answer
			var ended bool
			for !ended && child == 0 {
				select {
				case a = <-got:
					ended = true
				case <-time.After(10 * time.Millisecond):
					if b, err := os.ReadFile(pidFile); err == nil {
						child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
					}
				}
			}
			var pgid int
			if child != 0 {
				pgid = mustPgid(child)
				select {
				case a = <-got:
				case <-time.After(limit + testtime.Bound(20*time.Second)): // a hang guard past the limit
					t.Fatal("git.fetch did not return past its limit")
				}
			}
			wantErrCode(t, a.err, CodeFailed)
			if !strings.Contains(a.err.Message, "did not finish") || a.err.NextStep == "" {
				t.Errorf("stubborn=%v: error %+v; want the time limit named, with a nextStep", stubborn, a.err)
			}
			for _, g := range f.groups() {
				if groupLive(g) {
					t.Errorf("stubborn=%v: group %d outlived the timed-out call", stubborn, g)
				}
			}
			if child == 0 {
				t.Logf("stubborn=%v limit %s: it passed before the uploadpack started; trying again with twice the limit", stubborn, limit)
				return false
			}
			if groupLive(pgid) || pidLive(child) {
				t.Errorf("stubborn=%v: the group %d (uploadpack %d) outlived the timed-out call", stubborn, pgid, child)
			}
			return true
		})
		if !ok {
			t.Errorf("stubborn=%v: the uploadpack never started inside the limit, tried %v: the machine is too loaded to prove the group is ended", stubborn, tried)
		}
	}
}

// gitWithHooks is a git executable that is the host's git with `-c core.hooksPath=/dev/null`
// dropped from its arguments, a fake external tool for the one test that needs a hook to
// hold a fetch inside its ref transaction: the daemon never runs a hook, so only the test
// lets one run.
func gitWithHooks(t *testing.T) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	script := `#!/bin/sh
n=$#; i=0
while [ $i -lt $n ]; do
  a=$1; shift; i=$((i+1))
  if [ "$a" = "-c" ] && [ "$1" = "core.hooksPath=/dev/null" ]; then shift; i=$((i+1)); continue; fi
  set -- "$@" "$a"
done
exec '` + real + `' "$@"
`
	p := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Decision (ADR-0071 §3, P1 item 11): a cancelled write leaves no lock file. git removes its
// locks from its signal handlers, so the group is sent SIGTERM and only then, after the
// grace, SIGKILL. The fetch is held inside its ref transaction, holding ref locks, by a
// reference-transaction hook that sleeps in `prepared` (the test runs git with hooks on;
// the daemon never does). The control, in the same test: SIGKILL alone to the same group
// leaves a lock, so the absence after the cancel is the signal order's doing. Seen failing
// with the runner sending SIGKILL at once (stopGroup without its SIGTERM): the cancel left
// refs/remotes/origin/main.lock.
func TestWriteCancelLeavesNoLock(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	git := gitWithHooks(t)
	f.look = func(string) (string, error) { return git, nil }
	f.register()
	f.r.PushOther(t, 1)
	started := filepath.Join(t.TempDir(), "hook.pid")
	hook := "#!/bin/sh\nif [ \"$1\" = prepared ]; then echo $$ > '" + started + "'; exec sleep 60; fi\ncat >/dev/null\n"
	if err := os.WriteFile(filepath.Join(f.r.W, ".git", "hooks", "reference-transaction"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	id := f.start("git.fetch", "ws", FetchParams{Remote: "origin"})
	hookPid := pidIn(t, started)
	time.Sleep(200 * time.Millisecond)
	held := locks(t, f.r.W)
	if len(held) == 0 {
		t.Skipf("no ref lock is held while the hook sleeps (git %s): the test cannot discriminate", gitVersion(t))
	}
	pgid := mustPgid(hookPid)

	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out := f.until(id, 10*time.Second)
	if out.State != capability.StateCancelled {
		t.Errorf("outcome %s, error %+v; want cancelled", out.State, out.Error)
	}
	if groupLive(pgid) {
		t.Errorf("group %d is still alive", pgid)
	}
	if left := locks(t, f.r.W); len(left) > 0 {
		t.Errorf("the cancelled fetch left locks %v (held while it ran: %v)", left, held)
	}

	// Control, on a fresh hold of the same fetch: SIGKILL to the group leaves them.
	f2 := newWfx(t)
	f2.look = f.look
	f2.register()
	f2.r.PushOther(t, 1)
	started2 := filepath.Join(t.TempDir(), "hook.pid")
	hook2 := "#!/bin/sh\nif [ \"$1\" = prepared ]; then echo $$ > '" + started2 + "'; exec sleep 60; fi\ncat >/dev/null\n"
	if err := os.WriteFile(filepath.Join(f2.r.W, ".git", "hooks", "reference-transaction"), []byte(hook2), 0o755); err != nil {
		t.Fatal(err)
	}
	id2 := f2.start("git.fetch", "ws", FetchParams{Remote: "origin"})
	hookPid2 := pidIn(t, started2)
	time.Sleep(200 * time.Millisecond)
	pgid2 := mustPgid(hookPid2)
	syscall.Kill(-pgid2, syscall.SIGKILL)
	f2.until(id2, 10*time.Second)
	if left := locks(t, f2.r.W); len(left) == 0 {
		t.Logf("UNABLE TO DISCRIMINATE: SIGKILL alone left no lock either, so the absence above proves nothing about the signal order")
	} else {
		t.Logf("control: SIGKILL alone left %v", left)
	}
}

// Decision (ADR-0071 §3, ADR-0073): a credential git needs and may not ask for fails at
// once, with git's line as the cause and the nextStep that sends the user to a terminal
// once. A loopback server answers 401 to every request without credentials; the fixture's
// credential.helper is empty. Within 5 s, whatever askpass the environment names: the
// daemon's own SSH_ASKPASS (a script that sleeps) must not be run.
func TestFetchPromptFails(t *testing.T) {
	// Not parallel: it sets SSH_ASKPASS in the process environment (t.Setenv).
	t.Log(gitVersion(t))
	f := newWfx(t)
	srv := newAuthServer(t)
	f.e.g(f.r.W, "config", "remote.origin.url", srv.URL+"/r.git")
	rec := filepath.Join(t.TempDir(), "prompt")
	script := f.e.script("askpass.sh", "echo \"$1\" >> '"+rec+"'\nexec sleep 60")
	t.Setenv("SSH_ASKPASS", script)
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")

	start := time.Now()
	_, err := f.call("git.fetch", "ws", FetchParams{})
	took := time.Since(start)
	wantErrCode(t, err, CodeFailed)
	if took > 5*time.Second {
		t.Errorf("the fetch took %v; a credential it cannot ask for fails at once", took)
	}
	if !strings.Contains(err.Cause, "terminal prompts disabled") && !strings.Contains(err.Cause, "Authentication failed") {
		t.Errorf("cause %q does not name the credential", err.Cause)
	}
	if !strings.Contains(err.NextStep, "terminal") || !strings.Contains(err.NextStep, "git fetch") || !strings.Contains(err.NextStep, f.roots["ws"]) {
		t.Errorf("nextStep %q does not send the user to run git fetch once in a terminal in the root", err.NextStep)
	}
	if _, serr := os.Stat(rec); serr == nil {
		t.Errorf("the daemon's SSH_ASKPASS ran")
	}
	if srv.unauth.Load() == 0 {
		t.Errorf("the server was never asked: the failure is not the 401's")
	}
	if recs := f.records(); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the failed fetch, want none", recs)
	}
}

// Decision (ADR-0071 §5; plan m2n P1 verifier): the remote reaches argv only as a name of
// the repository's own. validName refuses what is an option or has a control character; a
// URL or a path passes it, so the name is also checked against `git remote`. Each shape is
// REQUEST_INVALID_PARAMS and no write git starts. Seen failing with the membership check
// removed (the `slices.Contains(names, p.Remote)` test): the path of another repository,
// the URL and the unknown name were fetched or failed as GIT_FAILED.
func TestFetchRemoteRefused(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	// The names validName refuses (remotesRefusedInGo) are refused before any repository is
	// read, in TestFetchRemoteRefusedBeforeAnyGit; these pass validName and are the
	// repository's own remotes' to refuse: a name it has no remote of, and a URL or a path.
	for _, remote := range []string{
		"a b", "unknown", "ORIGIN",
		"https://example.invalid/r.git", "file://" + f.r.Other, f.r.Other, f.r.Origin, "../w-other", "origin/main",
	} {
		_, err := f.call("git.fetch", "ws", FetchParams{Remote: remote})
		if err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("remote %q: %+v, want REQUEST_INVALID_PARAMS", remote, err)
		} else if err.NextStep == "" {
			t.Errorf("remote %q: refused without a nextStep", remote)
		}
	}
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d fetches started for refused remotes, want none", n)
	}
	// The control: the repository's own remote is accepted.
	if _, err := f.call("git.fetch", "ws", FetchParams{Remote: "origin"}); err != nil {
		t.Errorf("origin: %+v, want success", err)
	}
}

// remotesRefusedInGo are the remote names of TestFetchRemoteRefused that validName refuses:
// an option, a control character, a name too long, HEAD, another ref's prefix.
var remotesRefusedInGo = []string{
	"-x", "--all", "--upload-pack=touch /tmp/x", "a\x00b", "a\nb", strings.Repeat("a", 256), "HEAD", "refs/remotes/origin",
}

// Decision (ADR-0071 §5; plan m2n P1 verifier): the remote reaches argv only as a name of the
// repository's own, and validName comes first. The names it refuses (an option, a control
// character, a name past 255 bytes, HEAD, another ref's prefix) are REQUEST_INVALID_PARAMS
// with a nextStep with no git looked for and no repository to read, which a Registry whose git
// is not found would show as GIT_NOT_FOUND. TestValidName holds that validName refuses each.
func TestFetchRemoteRefusedBeforeAnyGit(t *testing.T) {
	t.Parallel()
	f := newWfxBare(t)
	f.look = func(string) (string, error) { return "", os.ErrNotExist }
	f.register()
	for _, remote := range remotesRefusedInGo {
		_, err := f.call("git.fetch", "ws", FetchParams{Remote: remote})
		if err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("remote %q: %+v, want REQUEST_INVALID_PARAMS before any git is looked for", truncate(remote, 40), err)
		} else if err.NextStep == "" {
			t.Errorf("remote %q: refused without a nextStep", truncate(remote, 40))
		}
	}
	if w, r := f.writeStarts.Load(), f.readStarts.Load(); w != 0 || r != 0 {
		t.Errorf("%d write gits and %d reads started for names validName refuses, want none", w, r)
	}
}

// Decision (ADR-0071 §1, §2): a root that is no repository is GIT_NOT_A_REPOSITORY, from
// the remote listing, before any fetch starts.
func TestFetchNotARepository(t *testing.T) {
	t.Parallel()
	f := newWfxBare(t) // "ws" is an empty directory: no repository
	_, err := f.call("git.fetch", "ws", FetchParams{})
	wantErrCode(t, err, CodeNotARepository)
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d fetches started on a root that is no repository", n)
	}
}

// Decision (ADR-0071 §3, process-lifecycle.md §Crash recovery): the record of a write git
// goes with git's Wait, so whatever git left in its group would run unrecorded; the group
// is ended after git exits by itself too. The uploadpack here backgrounds a sleep and
// fails, so git exits non-zero at once with a stray in its group. Seen failing with the
// runner ending the group only on cancel and timeout: the sleep survived the call.
func TestWriteKillsWhatGitLeft(t *testing.T) {
	t.Parallel()
	f := newWfx(t)
	strayFile := filepath.Join(t.TempDir(), "stray.pid")
	script := f.e.script("uploadpack.sh", "sleep 120 >/dev/null 2>&1 </dev/null &\necho $! > '"+strayFile+"'\nexit 1")
	f.e.g(f.r.W, "config", "remote.origin.uploadpack", script)

	_, err := f.call("git.fetch", "ws", FetchParams{Remote: "origin"})
	wantErrCode(t, err, CodeFailed)
	stray := pidIn(t, strayFile)
	t.Cleanup(func() { syscall.Kill(stray, syscall.SIGKILL) })
	if syscall.Kill(stray, 0) != syscall.ESRCH {
		t.Errorf("the stray %d git left in its group outlived the call", stray)
	}
	if groupLive(f.groups()[0]) {
		t.Errorf("group %d has a member after the call", f.groups()[0])
	}
}

// Decision (ADR-0071 §3, process-lifecycle.md §Crash recovery): a write git is recorded for
// its life as kind git-write, its pid its group's and its session's (getpgid == getsid ==
// pid: no terminal, and a group of its own to kill), and the record is gone when the call
// ends. The kind's value is the one internal/process records. Seen failing with the
// starter given the other kinds' attributes: getsid was the test's.
func TestWriteIsRecorded(t *testing.T) {
	t.Parallel()
	if writeKind != string(process.GitWrite) {
		t.Fatalf("the runner's kind %q is not process.GitWrite %q", writeKind, process.GitWrite)
	}
	f := newWfx(t)
	pidFile := f.hung(false)
	id := f.start("git.fetch", "ws", FetchParams{Remote: "origin"})
	child := pidIn(t, pidFile)
	pgid := mustPgid(child)

	recs := f.records()
	if len(recs) != 1 || recs[0].Kind != process.GitWrite || recs[0].ID != "git-write" || recs[0].PID != pgid || recs[0].PGID != pgid {
		t.Fatalf("processes.json holds %+v while the fetch runs, want one git-write record for the git, pid %d", recs, pgid)
	}
	if !slices.Contains(recs[0].Command, "fetch") {
		t.Errorf("recorded command %v does not show the fetch", recs[0].Command)
	}
	if sid, err := unix.Getsid(pgid); err != nil || sid != pgid {
		t.Errorf("getsid(%d) = %d (%v), want its own pid: the git must lead its session", pgid, sid, err)
	}
	if g, err := syscall.Getpgid(pgid); err != nil || g != pgid {
		t.Errorf("getpgid(%d) = %d (%v), want its own pid", pgid, g, err)
	}

	f.ops.Cancel(id)
	f.until(id, 10*time.Second)
	if recs := f.records(); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the call, want none", recs)
	}
}

// Decision (ADR-0071 §1, §4; ADR-0020 §Queue bound): every write is long-running, because
// each takes a Resource and can queue behind a five-minute fetch, past the bound a call
// not so flagged has; and each is a command for the human UI, needing git, gated on trust
// and serialised per repository. Over every git.* descriptor that takes a Resource, so a
// write added later is held to it. Seen failing with LongRunning removed from the shared
// write() and OPERATION_QUEUE_TIMEOUT added to git.fetch's Errors (so that it registers):
// the first assertion names git.fetch. The scheduler's side is internal/capability's
// TestLongRunningHasNoQueueBound.
func TestWritesAreLongRunning(t *testing.T) {
	t.Parallel()
	reg := capability.NewRegistry()
	Register(reg, Options{})
	var writes []string
	for _, d := range reg.All() {
		if !strings.HasPrefix(d.Name, "git.") || d.Resource == nil {
			continue
		}
		writes = append(writes, d.Name)
		if !d.LongRunning {
			t.Errorf("%s takes a Resource and is not LongRunning: it could fail at the queue bound behind a five-minute fetch", d.Name)
		}
		if d.Kind != capability.Command || d.Scope != capability.Workspace || d.Audience != capability.AudienceHuman {
			t.Errorf("%s: kind %v, scope %v, audience %v; want a workspace command for the human UI", d.Name, d.Kind, d.Scope, d.Audience)
		}
		if !slices.Contains(d.Availability, capability.RequiresGit) {
			t.Errorf("%s does not require git", d.Name)
		}
		if d.Authorize == nil {
			t.Errorf("%s has no trust gate", d.Name)
		}
		if slices.Contains(d.Errors, capability.ErrOperationQueueTimeout) {
			t.Errorf("%s lists OPERATION_QUEUE_TIMEOUT: a long-running call has no queue bound", d.Name)
		}
		if !slices.Contains(d.Errors, capability.ErrWorkspaceUntrusted) {
			t.Errorf("%s does not list WORKSPACE_UNTRUSTED", d.Name)
		}
	}
	// The writes the loop must have covered, so that it cannot pass over a write that lost its Resource.
	for _, want := range []string{"git.fetch", "git.pull", "git.checkout", "git.createBranch", "git.updateSubmodules"} {
		if !slices.Contains(writes, want) {
			t.Errorf("%s takes no Resource: the loop did not cover it (covered %v)", want, writes)
		}
	}
	// The reads take none.
	for _, name := range []string{"git.read", "git.watch", "git.log", "git.refs", "git.show"} {
		if d, ok := reg.Lookup(name); !ok || d.Resource != nil {
			t.Errorf("%s: registered %v with a Resource; a read never waits for a write", name, ok)
		}
	}
}

// gateScript is an uploadpack that appends a line to log when git starts it, waits until
// gate exists, then runs the real one: a fetch that holds until the test opens the gate.
func gateScript(e *env, log, gate string) string {
	return e.script("gated-uploadpack.sh", "echo started >> '"+log+"'\nwhile [ ! -f '"+gate+"' ]; do sleep 0.05; done\nexec git upload-pack \"$@\"")
}

func countLines(path string) int {
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "\n")
}

// Decision (ADR-0071 §4): one write at a time per repository, in the order started; reads
// never wait; another repository is unaffected. A fetch holds on a gate; a second fetch on
// the same repository is queued, names the first in blockedBy and has started no git; so is
// a fetch from a linked work tree of the same repository (one key per common directory); a
// git.log completes meanwhile; a fetch of another repository runs at once. When the gate
// opens they all complete, the queued ones after the first. Seen failing with the
// key taken from the work tree's own git directory (commondir not followed): the linked work
// tree's fetch ran at once.
func TestWritesQueuePerRepository(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	logA, gateA := filepath.Join(t.TempDir(), "a.log"), filepath.Join(t.TempDir(), "a.gate")
	f.e.g(f.r.W, "config", "remote.origin.uploadpack", gateScript(f.e, logA, gateA))
	wt := filepath.Join(t.TempDir(), "wt")
	f.e.g(f.r.W, "worktree", "add", "-q", wt, "-b", "wt-branch")
	f.roots["wt"] = platform.Current().Canonical(wt)

	other := gittest.Remote(t, filepath.Join(t.TempDir(), "fx2"))
	logB, gateB := filepath.Join(t.TempDir(), "b.log"), filepath.Join(t.TempDir(), "b.gate")
	f.e.g(other.W, "config", "remote.origin.uploadpack", gateScript(f.e, logB, gateB))
	f.roots["other"] = platform.Current().Canonical(other.W)
	f.register()

	first := f.start("git.fetch", "ws", FetchParams{Remote: "origin"})
	waitLines(t, logA, 1)
	second := f.start("git.fetch", "ws", FetchParams{Remote: "origin"})
	third := f.start("git.fetch", "wt", FetchParams{Remote: "origin"})
	for name, id := range map[string]string{"second (same workspace)": second, "third (linked work tree)": third} {
		out := f.snap(id)
		if out.State != capability.StateQueued || out.BlockedBy == nil {
			t.Errorf("%s: %s, blockedBy %+v; want queued behind a write", name, out.State, out.BlockedBy)
		}
	}
	if out := f.snap(second); out.BlockedBy != nil && out.BlockedBy.OperationID != first {
		t.Errorf("the second names %s as the one it waits for, want the first %s", out.BlockedBy.OperationID, first)
	}
	if got := countLines(logA); got != 1 {
		t.Errorf("%d uploadpacks started on the repository with one write running, want 1", got)
	}

	// A read meanwhile.
	logID := f.start("git.log", "ws", LogParams{})
	if out := f.until(logID, 15*time.Second); out.State != capability.StateCompleted {
		t.Errorf("git.log while a write holds the repository: %s, %+v; want completed", out.State, out.Error)
	}
	// Another repository meanwhile.
	otherID := f.start("git.fetch", "other", FetchParams{Remote: "origin"})
	waitLines(t, logB, 1)
	if out := f.snap(otherID); out.State != capability.StateRunning {
		t.Errorf("the fetch of another repository is %s, want running beside the first", out.State)
	}
	if out := f.snap(first); out.State != capability.StateRunning {
		t.Errorf("the first is %s, want running", out.State)
	}
	if err := os.WriteFile(gateB, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out := f.until(otherID, 30*time.Second); out.State != capability.StateCompleted {
		t.Errorf("the other repository's fetch: %s, %+v", out.State, out.Error)
	}
	if out := f.snap(second); out.State != capability.StateQueued {
		t.Errorf("the second is %s after another repository's fetch ended, want still queued", out.State)
	}

	// Open the gate: the first ends, then the second runs, then the third.
	if err := os.WriteFile(gateA, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first, second, third} {
		if out := f.until(id, 60*time.Second); out.State != capability.StateCompleted {
			t.Errorf("%s: %s, %+v; want completed", id, out.State, out.Error)
		}
	}
	if got := countLines(logA); got != 3 {
		t.Errorf("%d uploadpacks started in all, want 3, one per fetch", got)
	}
}

func waitLines(t *testing.T, path string, n int) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if countLines(path) >= n {
			return
		}
	}
	t.Fatalf("%s has %d lines, want %d", path, countLines(path), n)
}

// Decision (ADR-0071 §4): the lock key is the repository's common directory, read from the
// files: a plain repository, a folder of it, a linked work tree and a submodule's own git
// directory, and a root that is no repository (its own key).
func TestWriteKeyIsTheCommonDirectory(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	r := gittest.Remote(t, filepath.Join(t.TempDir(), "fx"))
	canon := platform.Current().Canonical
	main := canon(r.W)
	wantMain := "git.write:" + filepath.Join(main, ".git")
	key := func(root string) string {
		o := Options{Root: func(string) (string, bool) { return root, true }}
		return o.resource(&capability.Call{Workspace: "ws"})
	}
	if got := key(main); got != wantMain {
		t.Errorf("key of the repository = %q, want %q", got, wantMain)
	}
	sub := filepath.Join(main, "some", "folder")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := key(sub); got != wantMain {
		t.Errorf("key of a folder = %q, want the repository's %q", got, wantMain)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	e.g(r.W, "worktree", "add", "-q", wt, "-b", "wt-branch")
	if got := key(canon(wt)); got != wantMain {
		t.Errorf("key of a linked work tree = %q, want the repository's %q", got, wantMain)
	}
	if got := key(wt); got != wantMain {
		t.Errorf("key of the linked work tree by its unresolved path = %q, want %q", got, wantMain)
	}
	other := e.repo() // any other repository will do: only its key is read
	if got := key(canon(other)); got == wantMain {
		t.Errorf("two repositories share the key %q", got)
	}
	bare := t.TempDir()
	if got := key(bare); got != "git.write:"+resolved(bare) && got != "git.write:"+bare {
		t.Errorf("key of a root that is no repository = %q, want its own path", got)
	}

	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx3"))
	super := canon(s.Super)
	lib := filepath.Join(super, "lib")
	if got := key(lib); got == "git.write:"+filepath.Join(super, ".git") || !strings.Contains(got, filepath.Join(".git", "modules", "lib")) {
		t.Errorf("key of a submodule = %q, want its own git directory under .git/modules/lib: a submodule is a repository of its own", got)
	}
}

// Decision (ADR-0071 §5): a write's GIT_FAILED cause starts at git's first fatal: or error:
// line, runs for at most 20 lines and 4 KiB, and is valid UTF-8; stderr with neither line
// gives its first. A fetch's progress lines before the error are not the cause.
func TestWriteCause(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 300)
	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, "\tfile"+strconv.Itoa(i))
	}
	for _, c := range []struct {
		name, stderr, want string
		check              func(string) bool
	}{
		{"empty", "", "", nil},
		{"the first line only when there is no marker", "From /x\n * branch main -> FETCH_HEAD\n", "From /x", nil},
		{"progress lines are skipped", "From /x\n * [new branch] a -> origin/a\nfatal: unable to access 'u': nope\n", "fatal: unable to access 'u': nope", nil},
		{"an error: line starts it and its file lines follow", "error: Your local changes would be overwritten:\n\tshared.txt\nPlease commit.\n", "error: Your local changes would be overwritten:\n\tshared.txt\nPlease commit.", nil},
		{"twenty lines", "error: a\n" + strings.Join(many, "\n") + "\n", "", func(s string) bool { return strings.Count(s, "\n") == 19 && strings.HasSuffix(s, "\tfile18") }},
		{"four KiB", "error: " + strings.Repeat(long+"\n", 14), "", func(s string) bool { return len(s) <= 4096 && len(s) > 4000 && strings.HasPrefix(s, "error: ") }},
		{"invalid UTF-8", "fatal: bad \xff byte\n", "fatal: bad � byte", nil},
	} {
		got := writeCause([]byte(c.stderr))
		if c.check != nil {
			if !c.check(got) {
				t.Errorf("%s: cause %q (%d bytes)", c.name, truncate(got, 120), len(got))
			}
		} else if got != c.want {
			t.Errorf("%s: cause %q, want %q", c.name, got, c.want)
		}
	}
}

// Decision (ADR-0071 §3): a git that cannot be found is GIT_NOT_FOUND with no write started.
func TestFetchNoGit(t *testing.T) {
	t.Parallel()
	f := newWfxBare(t) // no git is looked for in any repository
	f.look = func(string) (string, error) { return "", errors.New("not found") }
	f.register()
	_, err := f.call("git.fetch", "ws", FetchParams{})
	wantErrCode(t, err, CodeNotFound)
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started with no git", n)
	}
}
