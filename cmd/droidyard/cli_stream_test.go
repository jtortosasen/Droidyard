package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// A CLI connection carries streams (ADR-0122): a streaming capability called over
// the native socket answers its envelope and then its events as {"subscriptionId","event"}
// lines, and the connection's end releases every stream it owns. These tests run the real
// `droidyard call` against the shipped wiring in this process, over a real socket.

// follower is a re-executed `droidyard call` whose stdout is read while it runs, which
// process.envelope cannot do: it waits for an exit a follower never makes.
type follower struct {
	cmd    *exec.Cmd
	lines  chan []byte
	exited chan error
	stderr lockedBuffer
}

// follow starts `droidyard call --no-autostart args...` against p's daemon.
func (p *inProcess) follow(t *testing.T, args ...string) *follower { return p.followIn(t, "", args...) }

// followIn is follow with dir as the working directory, whose project root the call's
// hello names.
func (p *inProcess) followIn(t *testing.T, dir string, args ...string) *follower {
	t.Helper()
	f := &follower{lines: make(chan []byte, 4096), exited: make(chan error, 1)}
	f.cmd = &exec.Cmd{
		Path: os.Args[0],
		Args: append([]string{"droidyard", "call", "--no-autostart"}, args...),
		Env:  append(os.Environ(), reexec+"=1", "DROIDYARD_STATE_DIR="+p.state),
		Dir:  dir,
	}
	stdout, err := f.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	f.cmd.Stderr = &f.stderr
	if err := f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		r := bufio.NewReader(stdout)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				f.lines <- line
			}
			if err != nil {
				break
			}
		}
		close(f.lines)
		f.exited <- f.cmd.Wait()
	}()
	t.Cleanup(func() { f.cmd.Process.Kill() }) // after Wait, a no-op
	return f
}

// next is the next line the call printed.
func (f *follower) next(t *testing.T, what string) []byte {
	t.Helper()
	select {
	case line, ok := <-f.lines:
		if !ok {
			t.Fatalf("the call exited before printing %s (stderr: %s)", what, f.stderr.String())
		}
		return line
	case <-time.After(testtime.Bound(processGuard)):
		t.Fatalf("the call printed no %s within %v (stderr: %s)", what, testtime.Bound(processGuard), f.stderr.String())
	}
	return nil
}

// envelope is the call's first line, which must be a response envelope and not a frame.
func (f *follower) envelope(t *testing.T) capability.ResponseEnvelope {
	t.Helper()
	line := f.next(t, "its response envelope")
	var fr wsFrame
	if err := json.Unmarshal(line, &fr); err != nil || fr.ID == "" || fr.Event != nil {
		t.Fatalf("the first line %q is not a response envelope: %v", line, err)
	}
	return fr.ResponseEnvelope
}

// opened is envelope for a stream's call: it must have succeeded and name a subscription.
func (f *follower) opened(t *testing.T) string {
	t.Helper()
	env := f.envelope(t)
	if env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("the stream's call answered %+v (error %+v); want a completed operation", env, env.Error)
	}
	var r struct {
		ID string `json:"subscriptionId"`
	}
	if err := json.Unmarshal(env.Result, &r); err != nil || r.ID == "" {
		t.Fatalf("the result %s names no subscription: %v", env.Result, err)
	}
	return r.ID
}

// frame is the call's next line, which must be an event frame.
func (f *follower) frame(t *testing.T) wsFrame {
	t.Helper()
	line := f.next(t, "an event frame")
	var fr wsFrame
	if err := json.Unmarshal(line, &fr); err != nil || fr.ID != "" || fr.SubscriptionID == "" || fr.Event == nil {
		t.Fatalf("the line %q is not an event frame: %v", line, err)
	}
	return fr
}

// frameOf reads frames until one satisfies match, failing on any line that is not a frame.
func (f *follower) frameOf(t *testing.T, what string, match func(wsFrame) bool) wsFrame {
	t.Helper()
	for {
		if fr := f.frame(t); match(fr) {
			return fr
		}
	}
}

// signal sends sig to the call.
func (f *follower) signal(sig syscall.Signal) { f.cmd.Process.Signal(sig) }

// kill ends the call as a killed terminal does: no signal handler runs, the kernel closes
// its connection.
func (f *follower) kill(t *testing.T) {
	t.Helper()
	f.cmd.Process.Kill()
	f.wait(t)
}

// wait returns the call's exit status once it has exited, draining what it printed.
func (f *follower) wait(t *testing.T) int {
	t.Helper()
	timeout := time.After(testtime.Bound(processGuard))
	for lines := f.lines; lines != nil; {
		select {
		case _, ok := <-lines:
			if !ok {
				lines = nil
			}
		case <-timeout:
			t.Fatalf("the call did not exit within %v", testtime.Bound(processGuard))
		}
	}
	select {
	case err := <-f.exited:
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("waiting for the call: %v", err)
	case <-timeout:
		t.Fatalf("the call did not exit within %v", testtime.Bound(processGuard))
	}
	return -1
}

// TestCLIFollowsOperationWatch: `droidyard call operation.watch` prints its envelope and
// then the operation.state frames of an operation started meanwhile, as the daemon sent them
// (ADR-0055's frame, on the CLI's own connection), and a Ctrl-C ends it with 130. The order
// is made deterministic by the bus's watch hook, which runs in the operation.watch handler
// after its subscription is open and before the result returns: the operation it starts
// there publishes its running state into a subscription that already exists, so the frame
// is queued before the envelope is written, and still must not reach the caller before it.
// Seen failing with the drain started before the envelope is written: the hook's frame
// precedes the envelope.
func TestCLIFollowsOperationWatch(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	startedCh := make(chan string, 1)
	p.bus.SetWatchHook(func(string) {
		id, err := p.ops.Start(context.Background(), capability.Request{Capability: "fixture.block", Client: capability.ClientCLI})
		if err != nil {
			t.Errorf("starting fixture.block in the window: %v", err)
			return
		}
		startedCh <- id
		<-p.started
	})
	t.Cleanup(func() { p.bus.SetWatchHook(nil) })

	f := p.follow(t, "operation.watch")
	sub := f.opened(t)
	var started string
	select {
	case started = <-startedCh:
	default:
		t.Fatal("the watch hook did not run before the result")
	}

	stateOf := func(fr wsFrame) (capability.OperationState, bool) {
		var st capability.OperationState
		return st, fr.Event.Topic == capability.StateTopic && fr.Event.Operation == started && json.Unmarshal(fr.Event.Payload, &st) == nil
	}
	running := f.frameOf(t, "the running state of the operation started in the window", func(fr wsFrame) bool {
		st, ok := stateOf(fr)
		return ok && st.State == capability.StateRunning
	})
	if running.SubscriptionID != sub {
		t.Errorf("the frame names subscription %s; want the envelope's %s", running.SubscriptionID, sub)
	}

	close(p.release)
	f.frameOf(t, "the completed state of the operation", func(fr wsFrame) bool {
		st, ok := stateOf(fr)
		return ok && st.State == capability.StateCompleted
	})

	f.signal(syscall.SIGINT)
	if code := f.wait(t); code != cli.ExitInterrupted {
		t.Errorf("the interrupted follower exited %d; want %d", code, cli.ExitInterrupted)
	}
	waitSubscriptions(t, p, 0)
}

// TestCLIStreamEndsWithItsConnection: device.watchLogs called from the CLI streams the
// emulator's lines; a Ctrl-C exits 130 and the stream goes with the connection: the logcat
// child is gone and the bus holds no subscription of the session (D1; ADR-0041 §6, whose
// release is Session.Close). Seen failing with sess.Close() omitted from the native
// handler: the child survives the CLI.
func TestCLIStreamEndsWithItsConnection(t *testing.T) {
	t.Parallel()
	p := startWatchDaemon(t, nil)
	f := p.follow(t, "device.watchLogs", "--params", wlParams)
	sub := f.opened(t)
	lines := f.frameOf(t, "a lines frame", func(fr wsFrame) bool { return fr.Event.Type == "lines" })
	if lines.SubscriptionID != sub || lines.Event.Topic != "device.logcat" {
		t.Errorf("the first lines frame is %+v; want subscription %s on device.logcat", lines, sub)
	}
	rec := waitStreamRecord(t, p.state)

	f.signal(syscall.SIGINT)
	if code := f.wait(t); code != cli.ExitInterrupted {
		t.Errorf("the interrupted follower exited %d; want %d", code, cli.ExitInterrupted)
	}
	testtime.Eventually(t, testtime.Bound(20*time.Second), "the logcat child gone and the stream released", func() bool {
		return syscall.Kill(rec.PID, 0) == syscall.ESRCH && p.adb.LiveStreams() == 0 &&
			len(streamRecords(t, p.state)) == 0 && p.bus.Subscriptions() == 0
	})
}

// TestCLIStreamsReleaseOnClose: every stream callable without a device is released by the
// connection's end, however the CLI ends: after it is killed the bus holds no subscription,
// and the file and git pollers a watch started hold none of its watches. Seen failing with
// sess.Close() omitted: the subscriptions, and with them the pollers, remain.
func TestCLIStreamsReleaseOnClose(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, capability, params string
		inProject                bool // the call runs in an open project: file.watch and git.watch
	}{
		{"operation.watch", "operation.watch", "", false},
		{"session.watch", "session.watch", "", false},
		{"workspace.watch", "workspace.watch", "", false},
		{"events.subscribe", "events.subscribe", `{"topics":["operation.progress"]}`, false},
		{"file.watch", "file.watch", `{"path":"."}`, true},
		{"git.watch", "git.watch", `{}`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := startInProcess(t)
			dir := ""
			if c.inProject {
				root, _ := projectWith(t, "settings.gradle")
				gitIn(t, root, "init", "-q", "-b", "main")
				writeFile(t, root, "a.kt", "committed\n")
				gitIn(t, root, "add", "-A")
				gitIn(t, root, "commit", "-q", "-m", "c")
				if env, code := callIn(t, p.state, root, "workspace.open"); code != 0 {
					t.Fatalf("workspace.open exited %d: %+v", code, env.Error)
				}
				dir = root
			}
			args := []string{c.capability}
			if c.params != "" {
				args = append(args, "--params", c.params)
			}
			f := p.followIn(t, dir, args...)
			f.opened(t)
			if n := p.bus.Subscriptions(); n != 1 {
				t.Fatalf("while the call is connected the bus holds %d subscriptions; want its one", n)
			}
			switch c.capability {
			case "file.watch":
				if n := p.watchers.Live(); n != 1 {
					t.Fatalf("%d file pollers while the watch is connected; want 1", n)
				}
			case "git.watch":
				if n := p.gitWatchers.Live(); n != 1 {
					t.Fatalf("%d git pollers while the watch is connected; want 1", n)
				}
			}

			f.kill(t)
			testtime.Eventually(t, testtime.Bound(20*time.Second), "the killed CLI's streams released", func() bool {
				return p.bus.Subscriptions() == 0 && p.watchers.Live() == 0 && p.gitWatchers.Live() == 0
			})
		})
	}
}

// TestCLIStreamStopped: a stream's own end, the stopped event, ends the call with exit 0
// after printing that frame (D10): the device vanishes from the fake adb's list and its
// logcat exits, which is stopped deviceGone.
func TestCLIStreamStopped(t *testing.T) {
	t.Parallel()
	dir := wlScenario(t)
	p := startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{env: devicetest.Env(t, dir, unusedPort(t)), floor: adbFloorFor()})
	f := p.follow(t, "device.watchLogs", "--params", wlParams)
	f.opened(t)
	f.frameOf(t, "a lines frame", func(fr wsFrame) bool { return fr.Event.Type == "lines" })
	waitStreamRecord(t, p.state)

	devicetest.Write(t, dir, "devices.out", []byte("List of devices attached\n"))
	devicetest.Write(t, dir, wlSerial+".follow.exitcode", []byte("7"))
	stopped := f.frameOf(t, "the stopped frame", func(fr wsFrame) bool { return fr.Event.Type == "stopped" })
	var sp struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(stopped.Event.Payload, &sp); err != nil || sp.Reason != "deviceGone" {
		t.Errorf("stopped %s (%v); want reason deviceGone", stopped.Event.Payload, err)
	}
	if code := f.wait(t); code != 0 {
		t.Errorf("the call exited %d after the stopped frame; want 0 (stderr: %s)", code, f.stderr.String())
	}
}

// TestShutdownWithAStalledFollower (regression): a CLI connection that subscribed and never
// reads cannot hold the daemon's shutdown. Frame writes carry no deadline (D1), so the one
// blocked on the full socket is ended by the daemon closing the connection after its grace
// (2 s), not by a timer of the handler's. 10 000 events of 1 KiB overflow any socket buffer.
func TestShutdownWithAStalledFollower(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	conn, _, _, err := daemon.Dial(sockFor(p.state), daemon.Hello{Protocol: daemon.Protocol, Version: system.Version, Client: capability.ClientCLI})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"id":"s","capability":"events.subscribe","params":{"topics":["operation.progress"]}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	waitSubscriptions(t, p, 1)
	payload := strings.Repeat("x", 1024)
	for i := 0; i < 10_000; i++ {
		p.bus.Publish("operation.progress", "progress", "", "", map[string]string{"message": payload})
	}
	// Let the drain fill the socket and block in its write. Nothing is asserted of the
	// wait; a drain that has not blocked yet only makes the test weaker.
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.Bound(30*time.Second))
	defer cancel()
	start := time.Now()
	if err := p.d.Shutdown(ctx); err != nil {
		t.Logf("shutdown: %v", err)
	}
	took := time.Since(start)
	t.Logf("Shutdown took %v with a follower that never reads", took)
	if limit := testtime.Bound(3 * time.Second); took > limit {
		t.Errorf("Shutdown took %v with a follower that never reads; want at most its grace plus 1 s (%v)", took, limit)
	}
}
