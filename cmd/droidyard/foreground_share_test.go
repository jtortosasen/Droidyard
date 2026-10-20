package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The foreground share and the device listing end to end (plan m2u step 4; ADR-0108 §1, §3,
// §6): real `droidyard share` processes against a real host daemon, a client daemon in this
// process joined by the shipped `join`, and every share --on 127.0.0.1.

// liveProc is a re-executed droidyard whose stdout is read while it runs.
type liveProc struct {
	cmd    *exec.Cmd
	lines  chan string
	mu     sync.Mutex
	all    []string
	stderr lockedBuffer
	exited chan error
}

func startLive(t *testing.T, state string, args ...string) *liveProc {
	t.Helper()
	cmd := &exec.Cmd{
		Path: os.Args[0],
		Args: append([]string{"droidyard"}, args...),
		Env:  append(os.Environ(), reexec+"=1", "DROIDYARD_STATE_DIR="+state),
	}
	p := &liveProc{cmd: cmd, lines: make(chan string, 256), exited: make(chan error, 1)}
	cmd.Stderr = &p.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			p.mu.Lock()
			p.all = append(p.all, sc.Text())
			p.mu.Unlock()
			p.lines <- sc.Text()
		}
		close(p.lines)
		p.exited <- cmd.Wait()
	}()
	t.Cleanup(func() { cmd.Process.Kill() })
	return p
}

// expect waits up to d for a stdout line containing want.
func (p *liveProc) expect(t *testing.T, want string, d time.Duration) string {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case l, ok := <-p.lines:
			if !ok {
				t.Fatalf("pid %d exited before printing %q; stdout:\n%s\nstderr:\n%s", p.cmd.Process.Pid, want, p.stdout(), p.stderr.String())
			}
			if strings.Contains(l, want) {
				return l
			}
		case <-timeout:
			t.Fatalf("pid %d printed no %q within %v; stdout:\n%s", p.cmd.Process.Pid, want, d, p.stdout())
		}
	}
}

func (p *liveProc) stdout() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.all, "\n")
}

// exit waits for the process to end and returns its status, or -1 and the signal that killed it.
func (p *liveProc) exit(t *testing.T, d time.Duration) (code int, signal syscall.Signal) {
	t.Helper()
	select {
	case err := <-p.exited:
		var ee *exec.ExitError
		switch {
		case err == nil:
			return 0, 0
		case errors.As(err, &ee):
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return -1, ws.Signal()
			}
			return ee.ExitCode(), 0
		}
		t.Fatalf("waiting for pid %d: %v", p.cmd.Process.Pid, err)
	case <-time.After(d):
		t.Fatalf("pid %d did not exit within %v; stdout:\n%s", p.cmd.Process.Pid, d, p.stdout())
	}
	return 0, 0
}

// codeOf is the join code on the `droidyard join <code>` line.
func codeOf(t *testing.T, line string) string {
	t.Helper()
	code, ok := strings.CutPrefix(strings.TrimSpace(line), "droidyard join ")
	if !ok {
		t.Fatalf("%q is not a join line", line)
	}
	return code
}

// TestForegroundShare: a foreground `share` prints its code and stays; the client joins and
// the CLI prints the join within 3 s; however the CLI ends (SIGINT, SIGHUP, SIGTERM, kill -9)
// the share ends with its connection and the client loses the device and its open Logcat
// stream (`stopped deviceGone`) within 2 s; `unshare` from another command ends it and the
// attached CLI says so; the share is never in hosting.lend; a second share of it gives a fresh
// code and returns; watchShare is refused on another connection's share and on a detached
// one; a foreground share over the UI's WebSocket is refused.
// Seen failing with the native hook removed (ops.OnDisconnect not registered): the SIGINT row,
// the first, fails, its device staying listed.
func TestForegroundShare(t *testing.T) {
	t.Parallel()
	host := newShareNode(t, shareHostListing())
	devicetest.Write(t, host.dir, sharePhone+".log", []byte(remoteLog))
	cdir := t.TempDir()
	devicetest.Write(t, cdir, "devices.out", []byte("List of devices attached\n"))
	client := startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{env: devicetest.Env(t, cdir, unusedPort(t)), exec: devicetest.Exec(cdir)})
	stop := startPeers(client.host, client.reg, client.ops, client.bus, client.store, client.adb, os.Stderr)
	t.Cleanup(stop)
	hn, _ := os.Hostname()
	base := hosting.NameFrom(hn, nil)
	remoteID := base + "/" + sharePhone

	listed := func() bool {
		env := callOn(t, client.state, "device.list")
		var res device.ListResult
		if env.Error != nil || json.Unmarshal(env.Result, &res) != nil {
			t.Fatalf("client device.list: %+v %s", env.Error, env.Result)
		}
		for _, d := range res.Devices {
			if d.Device == remoteID {
				return true
			}
		}
		return false
	}

	// listedOnWS is listed over a WebSocket of the client daemon, which the loss measurement
	// uses: no process is spawned between the share's end and the answer.
	lw := wsConnect(t, client)
	listings := 0
	listedOnWS := func() bool {
		listings++
		id := fmt.Sprintf("list%d", listings)
		lw.send(id, "device.list", "", `{}`, nil)
		_, f := lw.collect(id)
		var res device.ListResult
		if f.Error != nil || json.Unmarshal(f.Result, &res) != nil {
			t.Fatalf("client device.list over the WebSocket: %+v %s", f.Error, f.Result)
		}
		for _, d := range res.Devices {
			if d.Device == remoteID {
				return true
			}
		}
		return false
	}

	// lossBound is ADR-0104 §4's 2 s with the slack the pre-commit hook's load needed (above).
	// It is a product claim, scaled because level 5 timed every way the share ends: m2u's runs
	// 7 (b) and 8 (a) timed SIGINT (190 ms, 89 ms), and SIGINT, SIGTERM and SIGHUP share one
	// stop path (share.go: one signal.Notify channel, one handler that closes the connection);
	// m2v step 3's run timed the client's device.list losing the device after SIGINT, kill -9
	// and unshare, three rounds each, all under 700 ms (plan m2v, step 3's evidence).
	lossBound := testtime.Bound(3 * time.Second)

	// scenario shares in the foreground, joins, opens a Logcat stream, runs checks while
	// the share is up, ends the share with end and returns the share process.
	scenario := func(t *testing.T, during func(sh *liveProc, code string), end func(sh *liveProc)) *liveProc {
		t.Helper()
		sh := startLive(t, host.state, "share", sharePhone, "--on", "127.0.0.1")
		code := codeOf(t, sh.expect(t, "droidyard join dy1.", testtime.Bound(10*time.Second)))
		join := droidyardEnv(t, client.state, "", nil, "join", code)
		if exit := join.wait(t); exit != 0 {
			t.Fatalf("join exited %d: %s", exit, join.stderr.String())
		}
		sh.expect(t, base+" joined", testtime.Bound(3*time.Second))
		testtime.Eventually(t, testtime.Bound(3*time.Second), "the borrowed phone to be listed", listed)
		if l, ok := lendOf(host.policy(t), sharePhone); ok {
			t.Errorf("hosting.lend holds %+v while a foreground share is up; it must not", l)
		}

		ws := wsConnect(t, client)
		ws.send("l", "device.watchLogs", "", `{`+dev(remoteID)+`,"tail":10}`, nil)
		_, f := ws.collect("l")
		var wl device.WatchLogsResult
		if f.Error != nil || json.Unmarshal(f.Result, &wl) != nil || wl.SubscriptionID == "" {
			t.Fatalf("watchLogs on the borrowed phone: %+v %s", f.Error, f.Result)
		}
		if during != nil {
			during(sh, code)
		}

		// The bound is 2 s (ADR-0104 §4; measured 40–600 ms alone and at level 5), scaled to
		// lossBound. It is measured on the daemon's own pushes: the stream's stopped event is
		// read from the WebSocket the moment it arrives, and the listing is asked over another
		// WebSocket of the same client daemon, not through a spawned CLI, whose start-up under
		// load (seconds, at a load average of 20) took the budget the loss itself did not.
		start := time.Now()
		end(sh)
		for {
			f, ok := ws.next(lossBound - time.Since(start))
			if !ok {
				t.Fatalf("no stopped event for the Logcat stream within %v of the end of the share (%v)", lossBound, time.Since(start))
			}
			if f.Event == nil || f.Event.Type != "stopped" {
				continue
			}
			var p device.StoppedEvent
			if err := json.Unmarshal(f.Event.Payload, &p); err != nil || p.Reason != "deviceGone" {
				t.Errorf("the Logcat stream stopped as %s (%v); want deviceGone", f.Event.Payload, err)
			}
			break
		}
		for listedOnWS() {
			if time.Since(start) > lossBound {
				t.Fatalf("the client still lists %s %v after the share ended", remoteID, time.Since(start))
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("the client lost the device and its stream %v after the share ended", time.Since(start))
		return sh
	}

	signalRow := func(name string, sig syscall.Signal) {
		scenario(t, nil, func(sh *liveProc) { sh.cmd.Process.Signal(sig) }).
			exitStopped(t, name)
	}

	// SIGINT, with the other checks made while the share is up.
	sh := scenario(t, func(sh *liveProc, code string) {
		// A second share of the serial: a fresh code, returned at once, the first still held.
		second := startLive(t, host.state, "share", sharePhone, "--on", "127.0.0.1")
		line := second.expect(t, "droidyard join dy1.", testtime.Bound(5*time.Second))
		if codeOf(t, line) == code {
			t.Error("the second share printed the same code")
		}
		if exit, _ := second.exit(t, testtime.Bound(5*time.Second)); exit != 0 || !strings.Contains(second.stdout(), "already shared in the foreground of another command") {
			t.Errorf("the second share exited %d with:\n%s", exit, second.stdout())
		}
		// watchShare from another connection: refused, for a share that exists.
		env := callOn(t, host.state, "hosting.watchShare", "--params", `{"device":"`+sharePhone+`"}`)
		if env.Error == nil || env.Error.Code != capability.ErrInvalidParams {
			t.Errorf("watchShare from another connection: %+v; want REQUEST_INVALID_PARAMS", env.Error)
		}
		// watchShare on a detached share: refused.
		if exit, _, stderr := host.run(t, "share", "-d", sharePhone2, "--on", "127.0.0.1"); exit != 0 {
			t.Fatalf("detached share exited %d: %s", exit, stderr)
		}
		env = callOn(t, host.state, "hosting.watchShare", "--params", `{"device":"`+sharePhone2+`"}`)
		if env.Error == nil || env.Error.Code != capability.ErrInvalidParams {
			t.Errorf("watchShare on a detached share: %+v; want REQUEST_INVALID_PARAMS", env.Error)
		}
		// A foreground share over the UI's WebSocket: refused.
		ws := wsConnect(t, client)
		ws.send("f", "hosting.share", "", `{"device":"`+sharePhone+`"}`, nil)
		if _, f := ws.collect("f"); f.Error == nil || f.Error.Code != capability.ErrInvalidParams {
			t.Errorf("a foreground share over the WebSocket: %+v; want REQUEST_INVALID_PARAMS", f.Error)
		}
		if lend := host.policy(t).Hosting.Lend; len(lend) != 1 || lend[0].Device != sharePhone2 {
			t.Errorf("hosting.lend is %+v; want the detached share alone", lend)
		}
	}, func(sh *liveProc) { sh.cmd.Process.Signal(os.Interrupt) })
	sh.exitStopped(t, "SIGINT")

	signalRow("SIGHUP", syscall.SIGHUP)
	signalRow("SIGTERM", syscall.SIGTERM)

	// kill -9: the CLI says nothing; only the connection closing ends the share.
	sh = scenario(t, nil, func(sh *liveProc) { sh.cmd.Process.Kill() })
	if _, sig := sh.exit(t, testtime.Bound(5*time.Second)); sig != syscall.SIGKILL {
		t.Errorf("kill -9 ended the CLI with signal %v", sig)
	}

	// unshare from another command: the attached CLI says so and exits 0.
	sh = scenario(t, nil, func(sh *liveProc) {
		if exit, out, stderr := host.run(t, "unshare", sharePhone); exit != 0 || !strings.Contains(out, "no longer sharing "+sharePhone) {
			t.Errorf("unshare exited %d: %q %q", exit, out, stderr)
		}
	})
	if exit, _ := sh.exit(t, testtime.Bound(5*time.Second)); exit != 0 || !strings.Contains(sh.stdout(), "unshared from another command") {
		t.Errorf("the attached CLI exited %d with:\n%s\nwant 0 and a word that it was unshared", exit, sh.stdout())
	}
}

// exitStopped checks a CLI that was signalled: it exits 0 and says it stopped sharing.
func (p *liveProc) exitStopped(t *testing.T, how string) {
	t.Helper()
	if exit, sig := p.exit(t, testtime.Bound(5*time.Second)); exit != 0 || sig != 0 || !strings.Contains(p.stdout(), "stopped sharing "+sharePhone) {
		t.Errorf("%s: the CLI exited %d (signal %v) with:\n%s\nwant 0 and `stopped sharing`", how, exit, sig, p.stdout())
	}
}

// TestShareListing: with no serial, `share` lists the physical devices as `<transport>
// <manufacturer model> <serial>` with a reason for each that cannot be shared, and no
// emulator; exactly one shareable device is shared, saying which; two leave the list and exit
// 2 with nothing written, with and without -d; none is exit 1.
// Seen failing with the kind filter removed: the emulator is listed.
func TestShareListing(t *testing.T) {
	t.Parallel()
	const dup = "0123456789ABCDEF"
	listing := func(shareable ...string) string {
		l := "List of devices attached\n" +
			shareEmulator + " device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n" +
			shareUnauth + " unauthorized usb:1-1.3 transport_id:3\n" +
			dup + " device usb:1-1.8 product:a model:Dup transport_id:7\n" +
			dup + " device usb:1-1.9 product:a model:Dup transport_id:8\n"
		for _, s := range shareable {
			l += s + " device usb:1-1.1 product:raven model:Pixel_6_Pro transport_id:2\n"
		}
		return l
	}
	one := newShareNode(t, listing(sharePhone))
	devicetest.Write(t, one.dir, sharePhone+".manufacturer.out", []byte("Google\n"))

	exit, out, stderr := one.run(t, "share", "-d", "--on", "127.0.0.1")
	if exit != 0 {
		t.Fatalf("share -d with one shareable device exited %d: %s", exit, stderr)
	}
	for _, want := range []string{
		"usb  Google Pixel_6_Pro  " + sharePhone + "\n",
		"usb  -  " + shareUnauth + "  (unauthorized: accept the debugging prompt on the device)",
		"usb  Dup  " + dup + "  (more than one device reports this serial",
		"only one device can be shared: " + sharePhone,
		"sharing " + sharePhone + " until `droidyard unshare " + sharePhone + "`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, shareEmulator) {
		t.Errorf("an emulator is listed:\n%s", out)
	}
	if l, ok := lendOf(one.policy(t), sharePhone); !ok || len(l.To) != 0 {
		t.Errorf("the only shareable device was not shared: %+v", one.policy(t).Hosting.Lend)
	}
	if calls := callsIn(t, one.dir); slices.Contains(calls, "-s "+dup+" shell getprop ro.product.manufacturer") || slices.Contains(calls, "-s "+shareUnauth+" shell getprop ro.product.manufacturer") {
		t.Errorf("the manufacturer was asked of a device that is duplicated or not online: %v", calls)
	}

	// Two shareable: the list, exit 2, nothing written; the same with -d.
	two := newShareNode(t, listing(sharePhone, sharePhone2))
	for _, args := range [][]string{{"share", "--on", "127.0.0.1"}, {"share", "-d", "--on", "127.0.0.1"}} {
		exit, out, stderr := two.run(t, args...)
		if exit != 2 || !strings.Contains(out, sharePhone) || !strings.Contains(out, sharePhone2) || !strings.Contains(stderr, "name one") {
			t.Errorf("%v with two shareable devices: exit %d, %q, %q; want 2, both devices listed and `name one`", args, exit, out, stderr)
		}
		if two.policy(t).State != policy.StateMissing {
			t.Errorf("%v wrote the configuration", args)
		}
	}

	// None: exit 1 and the reasons.
	none := newShareNode(t, "List of devices attached\n"+shareEmulator+" device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n")
	if exit, out, stderr := none.run(t, "share", "-d", "--on", "127.0.0.1"); exit != 1 || strings.Contains(out, shareEmulator) || !strings.Contains(stderr, "no physical device") {
		t.Errorf("no physical device: exit %d, %q, %q; want 1, no emulator listed", exit, out, stderr)
	}
}
