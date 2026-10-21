package main

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Progress on a terminal (plan m2z D9; ADR-0122 §8): a call that is still running, with its
// standard error a terminal, opens a subscription to operation.progress on its own connection
// before it waits, and prints the reports of its own operation on stderr. These tests re-execute
// the real `droidyard call` against the shipped wiring in this process; the re-executed binary
// takes its stderr for a terminal when terminalEnv says so, since a pipe never is one.

// progressParamsFor is fixture.progress's parameters.
func progressParamsFor(reports []int, total int, hold bool) string {
	raw, _ := json.Marshal(progressParams{Reports: reports, Total: total, Hold: hold})
	return string(raw)
}

// terminalEnv1 is the environment of a call that takes its stderr for a terminal and has no
// limit on the progress lines, unless every says otherwise.
func terminalEnv1(every string) []string {
	return []string{terminalEnv + "=1", progressEveryEnv + "=" + every}
}

// stderrOf is what the call has written to stderr so far.
func (f *follower) stderrOf() string { return f.stderr.String() }

// expectStderr waits until the call's stderr is exactly want: nothing else may have been
// printed in the way (a line out of place or an extra one is a failure at once).
func (f *follower) expectStderr(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(testtime.Bound(processGuard))
	for {
		got := f.stderrOf()
		if got == want {
			return
		}
		if !strings.HasPrefix(want, got) {
			t.Fatalf("stderr is %q; want it to be, or to grow into, %q", got, want)
		}
		if time.Now().After(deadline) {
			t.Fatalf("stderr is %q after %v; want %q", got, testtime.Bound(processGuard), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// releaseOnSubscribe makes the bus release one step of f when a subscription is opened: the
// fixture then reports once the CLI's subscription exists. The hook runs in the daemon before
// events.subscribe returns, so it only hands the fixture its release and never waits for the
// report to be read. It counts the subscriptions it saw.
func releaseOnSubscribe(t *testing.T, p *inProcess, f *progressFixture) *atomic.Int32 {
	t.Helper()
	var seen atomic.Int32
	p.bus.SetSubscribeHook(func() {
		seen.Add(1)
		select {
		case f.release <- struct{}{}:
		case <-time.After(testtime.Bound(10 * time.Second)):
			t.Error("the fixture never took its release after the subscription")
		}
	})
	t.Cleanup(func() { p.bus.SetSubscribeHook(nil) })
	return &seen
}

// reportedAfterHook waits for the report the hook released.
func (f *progressFixture) reportedAfterHook(t *testing.T, report int) {
	t.Helper()
	select {
	case got := <-f.reported:
		if got != report {
			t.Fatalf("the fixture reported %d; want %d", got, report)
		}
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatalf("the fixture never reported %d after the subscription", report)
	}
}

var (
	operationIDRE = regexp.MustCompile(`"operation":\{"id":"[^"]*"`)
	durationRE    = regexp.MustCompile(`"durationMs":\d+`)
)

// withoutRunIdentity is an envelope line without what differs between two runs of the same
// call: the operation's id and its duration.
func withoutRunIdentity(line []byte) string {
	s := operationIDRE.ReplaceAllString(string(line), `"operation":{"id":"-"`)
	return durationRE.ReplaceAllString(s, `"durationMs":0`)
}

// TestCLIProgressOnATerminal: with stderr a terminal, a running call prints the progress its
// operation reports, one line each and in order, then the result; each report is released
// only once the previous line has been read, so a CLI that polled or batched could not pass
// them. stdout is the envelope of a run off a terminal, but for the operation's id. The
// first report is released by the bus's subscribe hook, so it exists only if the CLI
// subscribed on its connection before it waited. Seen failing with no subscription sent
// (the hook never fires and 1/3 never comes).
func TestCLIProgressOnATerminal(t *testing.T) {
	t.Parallel()
	p, f := startProgress(t)
	releaseOnSubscribe(t, p, f)
	args := []string{"fixture.progress", "--params", progressParamsFor([]int{1, 2, 3}, 3, true)}

	term := p.followEnv(t, "", terminalEnv1("0s"), args...)
	f.reportedAfterHook(t, 1)
	term.expectStderr(t, "progress 1/3\n")
	f.next(t, 2)
	term.expectStderr(t, "progress 1/3\nprogress 2/3\n")
	f.next(t, 3)
	term.expectStderr(t, "progress 1/3\nprogress 2/3\nprogress 3/3\n")
	f.next(t, 0) // the fixture returns only now, after the test has read 3/3
	termOut := term.next(t, "its result")
	if code := term.wait(t); code != 0 {
		t.Fatalf("the call on a terminal exited %d (stderr %q)", code, term.stderrOf())
	}
	if got := term.stderrOf(); got != "progress 1/3\nprogress 2/3\nprogress 3/3\n" {
		t.Errorf("stderr after the result is %q; want the three progress lines and nothing else", got)
	}

	// The same call off a terminal: no hook, so the test releases every step.
	p.bus.SetSubscribeHook(nil)
	pipe := p.followEnv(t, "", nil, args...)
	f.next(t, 1)
	f.next(t, 2)
	f.next(t, 3)
	f.next(t, 0)
	pipeOut := pipe.next(t, "its result")
	pipe.wait(t)
	if withoutRunIdentity(termOut) != withoutRunIdentity(pipeOut) {
		t.Errorf("stdout on a terminal is\n%s\noff a terminal\n%s\nwant them equal but for the operation id", termOut, pipeOut)
	}
}

// TestCLINoProgressOffATerminal: with stderr not a terminal (a script's pipe) a running call
// prints nothing on stderr and opens no subscription: the bus's subscribe hook never runs and
// the bus holds none while the call waits. Seen failing with progress printed on a pipe.
func TestCLINoProgressOffATerminal(t *testing.T) {
	t.Parallel()
	p, f := startProgress(t)
	var seen atomic.Int32
	p.bus.SetSubscribeHook(func() { seen.Add(1) })
	t.Cleanup(func() { p.bus.SetSubscribeHook(nil) })

	fo := p.followEnv(t, "", nil, "fixture.progress", "--params", progressParamsFor([]int{1, 2}, 2, true))
	f.next(t, 1)
	f.next(t, 2)
	f.next(t, 0)
	fo.next(t, "its result")
	if code := fo.wait(t); code != 0 {
		t.Fatalf("the call exited %d", code)
	}
	if got := fo.stderrOf(); got != "" {
		t.Errorf("stderr off a terminal is %q; want it empty", got)
	}
	if n := seen.Load(); n != 0 {
		t.Errorf("the bus saw %d subscriptions from a call off a terminal; want none", n)
	}
}

// startProgressTwo is startProgress with a second fixture, fixture.progress2, that another
// operation can run on its own release.
func startProgressTwo(t *testing.T) (*inProcess, *progressFixture, *progressFixture) {
	t.Helper()
	f := &progressFixture{release: make(chan struct{}), reported: make(chan int, 16), started: make(chan string, 4)}
	f2 := &progressFixture{release: make(chan struct{}), reported: make(chan int, 16), started: make(chan string, 4)}
	p := startInProcessWith(t, func(reg *capability.Registry) {
		for name, fx := range map[string]*progressFixture{"fixture.progress": f, "fixture.progress2": f2} {
			reg.MustRegister(capability.Descriptor{
				Name: name, Kind: capability.Command, Scope: capability.Global, LongRunning: true,
				Description: "Reports progress when released, then completes.", Params: progressParams{}, Result: struct{}{},
				Handler: fx.run,
			})
		}
	})
	return p, f, f2
}

// TestCLIProgressIsOwnOnly: the subscription receives every operation's progress daemon-wide
// and the CLI prints only its own operation's. Another operation reports 7/9 between this
// call's 1/3 and 2/3, on the same bus and so before 2/3 in the CLI's subscription; the line
// is not printed. Seen failing with another operation's progress printed.
func TestCLIProgressIsOwnOnly(t *testing.T) {
	t.Parallel()
	p, f, f2 := startProgressTwo(t)
	releaseOnSubscribe(t, p, f)

	fo := p.followEnv(t, "", terminalEnv1("0s"), "fixture.progress", "--params", progressParamsFor([]int{1, 2}, 3, true))
	f.reportedAfterHook(t, 1)
	fo.expectStderr(t, "progress 1/3\n")

	if _, err := p.ops.Start(context.Background(), capability.Request{
		Capability: "fixture.progress2", Params: json.RawMessage(progressParamsFor([]int{7}, 9, false)), Client: capability.ClientCLI,
	}); err != nil {
		t.Fatal(err)
	}
	f2.waitStarted(t)
	f2.next(t, 7)
	f.next(t, 2)
	fo.expectStderr(t, "progress 1/3\nprogress 2/3\n")
	f.next(t, 0)
	fo.next(t, "its result")
	if code := fo.wait(t); code != 0 {
		t.Fatalf("the call exited %d", code)
	}
	if got := fo.stderrOf(); strings.Contains(got, "7/9") {
		t.Errorf("stderr holds another operation's progress: %q", got)
	}
}

// TestCLIQueuedLine: a call that queues behind another on the same resource says so once on
// stderr, from the first answer's blockedBy, and goes on to wait. Seen failing with no line
// printed, and with the line printed more than once.
func TestCLIQueuedLine(t *testing.T) {
	t.Parallel()
	release, started := make(chan struct{}), make(chan string, 4)
	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	p := startInProcessWith(t, func(reg *capability.Registry) {
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.keyed", Kind: capability.Command, Scope: capability.Global, LongRunning: true,
			Description: "Holds the key k until released.", Params: struct{}{}, Result: struct{}{},
			Resource: func(*capability.Call) string { return "k" },
			Errors:   []capability.ErrorCode{capability.ErrOperationQueueTimeout},
			Handler: func(ctx context.Context, call *capability.Call) (any, error) {
				started <- call.OperationID
				select {
				case <-release:
					return struct{}{}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		})
	})
	t.Cleanup(closeRelease)

	holder, err := p.ops.Start(context.Background(), capability.Request{Capability: "fixture.keyed", Client: capability.ClientCLI})
	if err != nil {
		t.Fatal(err)
	}
	if id := <-started; id != holder {
		t.Fatalf("the first fixture.keyed to run is %s; want the holder %s", id, holder)
	}

	fo := p.followEnv(t, "", terminalEnv1("0s"), "fixture.keyed")
	want := "queued behind fixture.keyed (" + holder + ")\n"
	fo.expectStderr(t, want)
	closeRelease()
	fo.next(t, "its result")
	if code := fo.wait(t); code != 0 {
		t.Fatalf("the queued call exited %d (stderr %q)", code, fo.stderrOf())
	}
	if got := fo.stderrOf(); got != want {
		t.Errorf("stderr after the result is %q; want the queued line once and nothing else", got)
	}
}

// TestCLIProgressHeldNotDropped: the once-a-second limit holds a report back and never drops
// it. Two reports 10 ms apart under a one-second interval: the first is printed at once, the
// second is not before the tick, and is printed at it. Seen failing with a limit that drops
// (the second never comes) and with no limit (it comes at once).
func TestCLIProgressHeldNotDropped(t *testing.T) {
	t.Parallel()
	p, f := startProgress(t)
	releaseOnSubscribe(t, p, f)

	fo := p.followEnv(t, "", terminalEnv1("1s"), "fixture.progress", "--params", progressParamsFor([]int{1, 2}, 2, true))
	f.reportedAfterHook(t, 1)
	fo.expectStderr(t, "progress 1/2\n")
	first := time.Now()
	f.next(t, 2)
	time.Sleep(300 * time.Millisecond)
	if got := fo.stderrOf(); got != "progress 1/2\n" {
		t.Fatalf("300 ms after the second report stderr is %q; want the second held back until the tick", got)
	}
	fo.expectStderr(t, "progress 1/2\nprogress 2/2\n")
	if took := time.Since(first); took < 700*time.Millisecond {
		t.Errorf("the second line came %v after the first; want it at the one-second tick", took)
	}
	f.next(t, 0)
	fo.next(t, "its result")
	if code := fo.wait(t); code != 0 {
		t.Fatalf("the call exited %d", code)
	}
}

// TestDeviceWatchLogsHuman: `droidyard device watch-logs` prints its result and then each
// event for a person (a line per event, its type first), not as envelopes; SIGINT ends it
// with 130 and the logcat child goes with the connection (plan m2z D10).
func TestDeviceWatchLogsHuman(t *testing.T) {
	t.Parallel()
	p := startWatchDaemon(t, nil)
	fo := p.followRaw(t, "", nil, "device", "watch-logs", "--device", wlSerial, "--tail", "10", "--no-autostart")
	var sawLines string
	deadline := time.Now().Add(testtime.Bound(processGuard))
	for sawLines == "" {
		line := string(fo.next(t, "a human line"))
		if strings.HasPrefix(line, "lines") {
			sawLines = line
		}
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			t.Fatalf("the command printed an envelope or a frame, not a human line: %q", line)
		}
		if time.Now().After(deadline) {
			t.Fatal("no lines event within the guard")
		}
	}
	if !strings.Contains(sawLines, "device=") || !strings.Contains(sawLines, wlSerial) {
		t.Errorf("the lines event %q does not name the device the event came from", sawLines)
	}
	rec := waitStreamRecord(t, p.state)

	fo.signal(syscall.SIGINT)
	if code := fo.wait(t); code != cli.ExitInterrupted {
		t.Errorf("the interrupted command exited %d; want %d (stderr %q)", code, cli.ExitInterrupted, fo.stderrOf())
	}
	testtime.Eventually(t, testtime.Bound(20*time.Second), "the logcat child gone", func() bool {
		return syscall.Kill(rec.PID, 0) == syscall.ESRCH && p.adb.LiveStreams() == 0 && len(streamRecords(t, p.state)) == 0
	})
}
