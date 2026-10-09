package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	proc "github.com/jtortosasen/droidyard/internal/process"
)

// device.watchLogs end to end (ADR-0041; plan m2a step 4): a WebSocket client, the shipped
// wiring and a fake adb that is a real process the supervisor records and kills.

const (
	wlSerial = "emulator-5554"
	wlParams = `{"device":"emulator-5554","tail":10}`
)

// wlScenario writes the fake device: one online emulator whose log holds two lines, due
// at once (no device clock file: every line of the log is due when read).
func wlScenario(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	devicetest.Write(t, dir, "devices.out", []byte("List of devices attached\n"+wlSerial+" device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"))
	devicetest.Write(t, dir, wlSerial+".bootcompleted.out", []byte("1\n"))
	devicetest.Write(t, dir, wlSerial+".log", []byte("--------- beginning of main\n"+
		"1791337000.000001 10146 6406 6406 I App: first\n"+
		"1791337000.000002 10146 6406 6406 I App: second\n"))
	return dir
}

// startWatchDaemon starts the shipped wiring in this process over the fake adb as
// real processes, wrapping the stream's recorded starter when wrap is set.
func startWatchDaemon(t *testing.T, wrap func(device.StreamStarter) device.StreamStarter) *inProcess {
	t.Helper()
	dir := wlScenario(t)
	return startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{env: devicetest.Env(t, dir, unusedPort(t)), wrapStream: wrap})
}

// wlStream sends device.watchLogs as id and returns the subscription it opened.
func (w *wsClient) wlStream(id string) string {
	w.t.Helper()
	w.send(id, "device.watchLogs", "", wlParams, nil)
	_, f := w.collect(id)
	if f.Error != nil {
		w.t.Fatalf("device.watchLogs: %+v", f.Error)
	}
	return subscriptionOf(w.t, f)
}

// wlLines reads frames until n lines of the subscription's lines events have arrived,
// and returns their messages.
func (w *wsClient) wlLines(sub string, n int) []string {
	w.t.Helper()
	var msgs []string
	for len(msgs) < n {
		f := w.must("a lines event")
		if f.Event == nil || f.SubscriptionID != sub || f.Event.Type != "lines" {
			w.t.Fatalf("a frame that is not a lines event of %s: %+v", sub, f)
		}
		var le struct {
			Lines []struct {
				Message string `json:"message"`
			} `json:"lines"`
		}
		if err := json.Unmarshal(f.Event.Payload, &le); err != nil {
			w.t.Fatal(err)
		}
		for _, l := range le.Lines {
			msgs = append(msgs, l.Message)
		}
	}
	return msgs
}

// streamRecords are the pid records the daemon wrote to its state root.
func streamRecords(t *testing.T, state string) []proc.Record {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, proc.RecordsFile))
	if err != nil {
		return nil
	}
	var recs proc.Records
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatalf("processes.json: %v", err)
	}
	return recs.Processes
}

func waitStreamRecord(t *testing.T, state string) proc.Record {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if recs := streamRecords(t, state); len(recs) == 1 {
			return recs[0]
		}
	}
	t.Fatal("no stream child's pid record appeared in processes.json")
	return proc.Record{}
}

// waitFor polls cond for up to 20 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestWSWatchLogsStopped: over a real WebSocket a client gets lines frames, and when the
// same session starts a second stream of the device, the first subscription gets its
// stopped event with reason replaced and then nothing more, though it stays open. The
// stopped event is queued while the session's writer is held (SetWriteHook), so the loss
// the test names is deterministic: a stream that closes its subscription after publishing
// stopped frees the queue the event sits in (plan m2a step 4, B2). Seen failing with a
// Close of the subscription after the publish: stopped never arrives.
func TestWSWatchLogsStopped(t *testing.T) {
	t.Parallel()
	p := startWatchDaemon(t, nil)
	w := wsConnect(t, p)

	first := w.wlStream("w1")
	if got := w.wlLines(first, 2); strings.Join(got, ",") != "first,second" {
		t.Fatalf("the history is %v, want first,second", got)
	}

	gate := make(chan struct{})
	var once sync.Once
	release := func() { p.http.SetWriteHook(nil); once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	p.http.SetWriteHook(func() { <-gate })
	w.send("w2", "device.watchLogs", "", wlParams, nil)
	// The first stream ended and left the set once its stopped event was published, and
	// nothing has left the writer: the event is only queued.
	waitFor(t, "the replaced stream to leave the set", func() bool { return p.adb.LiveStreams() == 1 && len(streamRecords(t, p.state)) == 1 })
	release()

	var second string
	var stopped *wsFrame
	for second == "" || stopped == nil {
		f := w.must("the response and the stopped event")
		switch {
		case f.ID == "w2":
			second = subscriptionOf(t, f)
		case f.Event != nil && f.SubscriptionID == first && f.Event.Type == "stopped":
			stopped = &f
		case f.Event != nil && f.SubscriptionID != first && f.Event.Type == "lines":
			// the second stream's own history
		default:
			t.Fatalf("an unexpected frame: %+v", f)
		}
	}
	var sp struct {
		Device string `json:"device"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(stopped.Event.Payload, &sp); err != nil {
		t.Fatal(err)
	}
	if stopped.Event.Topic != "device.logcat" || sp.Reason != "replaced" || sp.Device != wlSerial {
		t.Errorf("stopped %+v payload %+v, want device.logcat replaced for %s", stopped.Event, sp, wlSerial)
	}
	if second == first {
		t.Error("the two streams share a subscription")
	}
	// Nothing follows on the first subscription, and it stays open with the second.
	for {
		f, ok := w.next(300 * time.Millisecond)
		if !ok {
			break
		}
		if f.Event != nil && f.SubscriptionID == first {
			t.Fatalf("a frame on the stopped subscription: %+v", f)
		}
	}
	if n := p.bus.Subscriptions(); n != 2 {
		t.Errorf("the bus holds %d subscriptions, want the stopped one and the new one", n)
	}
	// events.subscribe does not serve the topic.
	w.send("s1", "events.subscribe", "", `{"topics":["device.logcat"]}`, nil)
	if _, f := w.collect("s1"); f.Error == nil || f.Error.Code != capability.ErrInvalidParams {
		t.Fatalf("events.subscribe to device.logcat: %+v; want REQUEST_INVALID_PARAMS", f)
	}
}

// heldWait is a recorded child whose reaping is held until release is closed: its Wait,
// which reaps the child and removes its pid record, blocks first.
type heldWait struct {
	device.Stream
	release <-chan struct{}
}

func (h heldWait) Wait() error {
	<-h.release
	return h.Stream.Wait()
}

// TestStopAllWaitsForTheReap: the daemon's AfterOps (ADB.Close) returns only when every
// stream's child is reaped and its record removed, both for a stream StopAll ends and for
// one whose end hook fired first. The injected starter blocks the reaping, so what waits
// for it is observable. Seen failing with a StopAll that does not wait (Close returned
// with the reaping held) and with the stream leaving the set at its end hook (the second
// subtest: Close returned with the reaping held, because the stream was no longer there
// to wait for).
func TestStopAllWaitsForTheReap(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		// hookFirst ends the stream with events.unsubscribe before the daemon stops.
		hookFirst bool
	}{
		{"a stream StopAll ends", false},
		{"a stream whose end hook fired first", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			var once sync.Once
			free := func() { once.Do(func() { close(release) }) }
			t.Cleanup(free)
			p := startWatchDaemon(t, func(real device.StreamStarter) device.StreamStarter {
				return func(cmd *exec.Cmd, kind string) (device.Stream, error) {
					st, err := real(cmd, kind)
					if err != nil {
						return nil, err
					}
					return heldWait{Stream: st, release: release}, nil
				}
			})
			w := wsConnect(t, p)
			sub := w.wlStream("w1")
			w.wlLines(sub, 2)
			waitStreamRecord(t, p.state)

			if c.hookFirst {
				w.send("u1", "events.unsubscribe", "", `{"subscriptionId":"`+sub+`"}`, nil)
				if _, f := w.collect("u1"); f.Error != nil {
					t.Fatalf("events.unsubscribe: %+v", f.Error)
				}
				// The hook fired and the child was killed; its reaping is what is held.
				time.Sleep(300 * time.Millisecond)
				if n := p.adb.LiveStreams(); n != 1 {
					t.Fatalf("%d streams in the set after the hook; want the one still being reaped", n)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			closed := make(chan struct{})
			go func() { p.adb.Close(ctx); close(closed) }()
			select {
			case <-closed:
				t.Fatal("the daemon's AfterOps returned while a stream's child was not yet reaped")
			case <-time.After(500 * time.Millisecond):
			}
			if recs := streamRecords(t, p.state); len(recs) != 1 {
				t.Errorf("processes.json holds %+v while the reaping is held; want the child's record", recs)
			}
			free()
			select {
			case <-closed:
			case <-time.After(15 * time.Second):
				t.Fatal("AfterOps did not return once the reaping could finish")
			}
			if n := p.adb.LiveStreams(); n != 0 {
				t.Errorf("%d streams in the set after AfterOps", n)
			}
			if recs := streamRecords(t, p.state); len(recs) != 0 {
				t.Errorf("processes.json holds %+v after AfterOps, want none", recs)
			}
		})
	}
}

// TestWatchLogsNeedsAWebSocketSession: device.watchLogs is streaming, so through plain
// HTTP, a CLI call and the core's MCP client kind it is SESSION_REQUIRED at Start, naming
// GET /v1/ws, with no operation and no subscription (ADR-0010; ADR-0041 §7). The MCP
// tool list holds no streaming capability: TestMCPTranscript pins it against the golden
// file (34 stateless tools, the same as before this capability). Seen failing with the
// capability stateless (and so without its Events, which the registry refuses on one):
// each call ran and failed on adb, none was refused for want of a session.
func TestWatchLogsNeedsAWebSocketSession(t *testing.T) {
	t.Parallel()
	p, _ := startHTTP(t, "127.0.0.1:0")
	port := portOf(t, p.d.HTTPAddr())
	want := func(what string, env capability.ResponseEnvelope) {
		t.Helper()
		if env.Error == nil || env.Error.Code != capability.ErrSessionRequired || env.Operation != nil || env.Result != nil ||
			!strings.Contains(env.Error.NextStep, "GET /v1/ws") {
			t.Errorf("%s: %+v (error %+v); want SESSION_REQUIRED naming GET /v1/ws, no operation, no result", what, env, env.Error)
		}
	}

	resp := callRequest(port, p.token, `{"id":"w","capability":"device.watchLogs","params":`+wlParams+`}`).send(t, p.d.HTTPAddr())
	want("POST /v1/call device.watchLogs", envelopeOf(t, resp))

	env, code := p.call(t, "device.watchLogs", "--params", wlParams)
	if code == 0 {
		t.Error("droidyard call device.watchLogs exited 0")
	}
	want("droidyard call device.watchLogs", env)

	out, err := p.ops.Invoke(context.Background(), capability.Request{Capability: "device.watchLogs", Params: json.RawMessage(wlParams), Client: capability.ClientMCP})
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrSessionRequired || out.OperationID != "" {
		t.Errorf("device.watchLogs as client mcp: (%+v, %v); want SESSION_REQUIRED", out, err)
	}
	if n := p.bus.Subscriptions(); n != 0 {
		t.Errorf("the rejected calls left %d subscriptions on the bus", n)
	}
}

// streamDaemon is a re-executed daemon over a fake adb whose log is quiet once its lines
// are out, and a WebSocket to it: the stream's child is a real process.
type streamDaemon struct {
	*fakeFollowDaemon
	srv  *process
	addr string
}

func startStreamDaemon(t *testing.T, d *fakeFollowDaemon) *streamDaemon {
	t.Helper()
	srv := d.serve(t)
	info, err := daemon.ReadLock(d.state)
	if err != nil || info.HTTP == nil {
		t.Fatalf("daemon.lock says (%+v, %v) once serve listens", info, err)
	}
	return &streamDaemon{fakeFollowDaemon: d, srv: srv, addr: *info.HTTP}
}

// connect opens an authenticated WebSocket to the daemon.
func (s *streamDaemon) connect(t *testing.T) *wsClient {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.state, daemon.TokenFile))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws://"+s.addr+"/v1/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://" + s.addr}}})
	if err != nil {
		t.Fatalf("the upgrade: %v (response %v)", err, resp)
	}
	w := pump(t, c)
	w.writeText(`{"auth":"` + strings.TrimSpace(string(raw)) + `"}`)
	return w
}

// TestGracefulStopEndsAStream pins plan m2a step 4's level-4 check (ADR-0041 §6; ADR-0021
// §Pid records), after TestGracefulStopCancelsABoundedFollow: with a stream open over a
// WebSocket, `droidyard stop` leaves the fake's follow dead and no device-stream record,
// and the daemon exits 0. A stop that did not end the stream would leave a logcat
// process the next start reaps as an orphan it never owned.
func TestGracefulStopEndsAStream(t *testing.T) {
	t.Parallel()
	s := startStreamDaemon(t, newFakeFollowDaemon(t))
	w := s.connect(t)
	w.send("w1", "device.watchLogs", "", wlParams, nil)
	_, f := w.collect("w1")
	if f.Error != nil {
		t.Fatalf("device.watchLogs: %+v", f.Error)
	}
	rec := s.waitFollowRecord(t)
	if rec.Kind != proc.DeviceStream {
		t.Errorf("record kind = %q, want %q", rec.Kind, proc.DeviceStream)
	}
	if !alive(rec.PID) {
		t.Fatalf("the stream's child (pid %d) is not running", rec.PID)
	}

	if code := droidyardEnv(t, s.state, "", s.env, "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	if code := s.srv.wait(t); code != 0 {
		t.Errorf("serve exited %d after stop; want 0", code)
	}
	if alive(rec.PID) {
		t.Errorf("the stream's child (pid %d) survived the graceful stop", rec.PID)
	}
	if recs := s.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after the stop, want none", recs)
	}
}

// TestKilledDaemonLeavesTheStreamForTheNextStart: a daemon killed with SIGKILL mid-stream
// cannot end the child, which writes nothing once its lines are out and so survives its
// reader; its record is on disk, and the next daemon's Reconcile reaps it before it
// listens (ADR-0041 §6; ADR-0021 §Pid records). Seen failing reversed (the child gone
// after the kill, or alive after the next start): the record was not written, or the next
// daemon never reconciled.
func TestKilledDaemonLeavesTheStreamForTheNextStart(t *testing.T) {
	t.Parallel()
	s := startStreamDaemon(t, newFakeFollowDaemon(t))
	w := s.connect(t)
	w.send("w1", "device.watchLogs", "", wlParams, nil)
	if _, f := w.collect("w1"); f.Error != nil {
		t.Fatalf("device.watchLogs: %+v", f.Error)
	}
	rec := s.waitFollowRecord(t)
	if !alive(rec.PID) {
		t.Fatalf("the stream's child (pid %d) is not running", rec.PID)
	}

	s.srv.cmd.Process.Signal(syscall.SIGKILL)
	s.srv.wait(t)
	// On macOS the child outlives a kill -9 and the next daemon reaps it. On Linux the
	// death signal (internal/process/deathsignal.go) ends it with the daemon; its record
	// stays all the same, for the next daemon to drop. Not run on Linux here.
	if runtime.GOOS != "linux" && !alive(rec.PID) {
		t.Fatalf("the child (pid %d) died with the daemon; it must outlive a kill -9", rec.PID)
	}
	if recs := s.records(t); len(recs) != 1 || recs[0].PID != rec.PID || recs[0].Kind != proc.DeviceStream {
		t.Fatalf("processes.json holds %+v after the kill, want the child's device-stream record %d", recs, rec.PID)
	}

	second := s.serve(t)
	if alive(rec.PID) {
		t.Errorf("the child (pid %d) survived the next daemon's reconciliation", rec.PID)
	}
	if recs := s.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v after reconciling, want none", recs)
	}
	if code := droidyardEnv(t, s.state, "", s.env, "stop").wait(t); code != 0 {
		t.Fatalf("the second stop exited %d", code)
	}
	second.wait(t)
}
