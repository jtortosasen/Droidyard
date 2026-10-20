package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The lease of a lent device end to end (plan m2t step 6; ADR-0104): one host, the clients
// anna and bob as in-process daemons paired over real TLS, a third peer, carol, as a raw link
// to read what the host sends, the host's MCP shim and UI, and the fake adb behind m2p's
// fixture listener.

// skewClock is the host registry's clock: the real time plus a skew a test moves, so that
// "20 minutes without a call" costs nothing.
type skewClock struct {
	mu   sync.Mutex
	skew time.Duration
}

func (c *skewClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.skew)
}

func (c *skewClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skew += d
}

// leaseLab is a screenLab whose host also lends the phone to bob (a daemon of his own) and
// to carol (a link only).
type leaseLab struct {
	*screenLab
	bob   *inProcess
	carol *hosting.Link
	clk   *skewClock
	trace *lockedBuffer // the host's trace lines
}

func newLeaseLab(t *testing.T) *leaseLab {
	t.Helper()
	// Bob's identity first: the host pins it. His fake adb lists one emulator of his own.
	bobDir := t.TempDir()
	devicetest.Write(t, bobDir, "devices.out", []byte("List of devices attached\nemulator-5556 device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"))
	devicetest.Write(t, bobDir, "emulator-5556.bootcompleted.out", []byte("1\n"))
	bob := startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{env: devicetest.Env(t, bobDir, unusedPort(t)), exec: devicetest.Exec(bobDir)})
	if err := bob.host.Init(); err != nil {
		t.Fatal(err)
	}
	carolState := shortDir(t)
	carolID, _, err := hosting.LoadIdentity(carolState)
	if err != nil {
		t.Fatal(err)
	}
	sl := newScreenLab(t, 3, labOptions{
		hostFiles:  map[string]string{phone1 + ".log": remoteLog},
		extraPeers: []labPeer{{"bob", bob.host.Identity().Fingerprint()}, {"carol", carolID.Fingerprint()}},
	})
	addr := sl.host.host.Health().Hosting.Listen
	hostFP := sl.host.host.Identity().Fingerprint()

	configAt(t, bob.state, clientConfig(t, addr, hostFP, true, fullAllowlist()...))
	stopBob := startPeers(bob.host, bob.reg, bob.ops, bob.bus, bob.store, bob.adb, os.Stderr)
	t.Cleanup(stopBob)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "bob's link to come up", func() bool { return bob.host.Remote(remoteName).Connected() })

	configAt(t, carolState, `{"remotes":[{"name":"qa","address":"`+addr+`","fingerprint":"`+hostFP+`"}]}`)
	carol := clientHost(t, carolState)
	carol.Start()
	t.Cleanup(carol.Stop)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "carol's link to come up", func() bool { return carol.Remote("qa").Connected() })

	trace := &lockedBuffer{}
	sl.host.ops.UseTrace(func(format string, v ...any) { fmt.Fprintf(trace, format+"\n", v...) })
	clk := &skewClock{}
	sl.host.ops.Sessions().UseClock(clk.now)
	return &leaseLab{screenLab: sl, bob: bob, carol: carol.Remote("qa"), clk: clk, trace: trace}
}

// invoke runs capName on p as the CLI does, through the daemon's operations and without
// spawning a process (a race-instrumented one costs about a second): the call's origin is the
// CLI's, which has no session. The envelope is what the CLI would print.
func (p *inProcess) invoke(capName, params string) capability.ResponseEnvelope {
	req := capability.Request{Capability: capName, Client: capability.ClientCLI}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	out, err := p.ops.Invoke(context.Background(), req)
	if err != nil {
		return capability.ResponseEnvelope{Error: capability.AsError(err)}
	}
	env := capability.ResponseEnvelope{Error: out.Error, Meta: out.Meta()}
	if out.Result != nil {
		env.Result, _ = json.Marshal(out.Result)
	}
	return env
}

// on runs capName on anna's daemon, the client of the lab.
func (l *leaseLab) on(t *testing.T, capName, params string) capability.ResponseEnvelope {
	t.Helper()
	return l.client.invoke(capName, params)
}

// onBob runs capName on bob's daemon.
func (l *leaseLab) onBob(t *testing.T, capName, params string) capability.ResponseEnvelope {
	t.Helper()
	return l.bob.invoke(capName, params)
}

// holder is who the host's registry says holds the phone: "" for no one. It reads what
// device.list reports without its `adb devices` (a re-executed fake, seconds under -race);
// listedHolder is the listing itself.
func (l *leaseLab) holder(t *testing.T) string {
	t.Helper()
	h, ok := l.host.ops.Sessions().DeviceHolder(phone1, "")
	if !ok {
		return ""
	}
	return h.Name
}

// listedHolder is who the host's device.list (asked by the CLI, which is told of every
// holder) says holds the phone: "" for no one.
func (l *leaseLab) listedHolder(t *testing.T) string {
	t.Helper()
	env := l.host.invoke("device.list", "")
	var r device.ListResult
	if err := json.Unmarshal(env.Result, &r); err != nil {
		t.Fatalf("the host's device.list: %s (%+v)", env.Result, env.Error)
	}
	for _, d := range r.Devices {
		if d.Device == phone1 {
			if d.OccupiedBy == nil {
				return ""
			}
			return d.OccupiedBy.Name
		}
	}
	t.Fatalf("the host's device.list lacks %s: %+v", phone1, r.Devices)
	return ""
}

// releaseLease is the host's owner releasing the phone (session.release, a human's action),
// which ends whoever's lease of it.
func (l *remoteLab) releaseLease(t *testing.T) {
	t.Helper()
	env := l.host.invoke("session.release", `{"device":"`+phone1+`"}`)
	if env.Error != nil {
		t.Fatalf("session.release: %+v", env.Error)
	}
}

func (l *leaseLab) release(t *testing.T) { t.Helper(); l.releaseLease(t) }

// setHostConfig rewrites the host's configuration from lab.hostConf, with edit applied.
func (l *leaseLab) setHostConfig(t *testing.T, edit func(conf map[string]any)) {
	t.Helper()
	edit(l.hostConf)
	configAt(t, l.host.state, jsonObject(t, l.hostConf))
}

// lendPhone1To sets who the host lends phone1 to.
func (l *leaseLab) lendPhone1To(t *testing.T, peers ...string) {
	t.Helper()
	l.setHostConfig(t, func(conf map[string]any) {
		lend := conf["hosting"].(map[string]any)["lend"].([]map[string]any)
		lend[0] = map[string]any{"device": phone1, "to": peers}
	})
}

// occupied requires env to be a DEVICE_OCCUPIED failure that names holder and the device as
// the caller calls it, and not an answer this version cannot read.
func occupied(t *testing.T, what string, env capability.ResponseEnvelope, holder string) {
	t.Helper()
	if env.Error == nil || env.Error.Code != device.ErrOccupied {
		t.Errorf("%s: %+v; want DEVICE_OCCUPIED", what, env.Error)
		return
	}
	if !strings.Contains(env.Error.Message, holder) || !strings.Contains(env.Error.Message, remotePhone) {
		t.Errorf("%s: message %q; want it to name %s and the device as %s", what, env.Error.Message, holder, remotePhone)
	}
	if !untrusted(env) {
		t.Errorf("%s: a host's refusal is not marked untrusted", what)
	}
}

// free requires env to have succeeded.
func free(t *testing.T, what string, env capability.ResponseEnvelope) {
	t.Helper()
	if env.Error != nil {
		t.Errorf("%s: %+v; want it to succeed", what, env.Error)
	}
}

// lentCalls is a call of each kind of lent capability that does not stream: a read, the live
// view's input and an action, with parameters that pass their validation on the remote id.
// Every lent capability is refused alike at the device (TestLeaseIsTakenByAnyCall), so the
// lab, where each call costs seconds under -race, runs a representative of each.
func lentCalls() []struct{ name, params string } {
	d := `"device":"` + remotePhone + `"`
	return []struct{ name, params string }{
		{"device.uiTree", `{` + d + `}`},
		{"device.screenshot", `{` + d + `}`},
		{"device.tap", `{` + d + `,"x":0.5,"y":0.5}`},
		{"device.launch", `{` + d + `,"applicationId":"dev.droidyard.formapp"}`},
	}
}

// touchParams is the call the lab makes where a call has only to succeed or be refused and
// what it reads is of no interest: an uninstall takes a few adb commands where a uiTree
// takes a dozen, which under -race is seconds. Any call of a peer takes the lease alike.
const touchParams = `{"device":"` + remotePhone + `","applicationId":"dev.droidyard.formapp"}`

// monkeys counts the launches the host's adb ran.
func monkeys(calls []string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, " shell monkey ") {
			n++
		}
	}
	return n
}

// TestLease: plan m2t step 6, ADR-0104.
//
//   - Anna's uiTree (a read) leases the phone: the host's device.list shows her in occupiedBy.
//     Bob's uiTree, launch, tap, swipe, pressKey, screenshot, logs, tapElement, uninstall,
//     inputText, watchScreen and watchLogs are DEVICE_OCCUPIED naming anna and the device as
//     bob names it, not "incompatible", and the host runs no launch for him. Seen failing
//     with the lease taken only by acting calls (bob's reads, input and live view answered),
//     and with the code left undeclared (LentErrors without DEVICE_OCCUPIED: bob's reads came
//     back DEVICE_NOT_READY, incompatible).
//   - occupation.conflict allow, set from here on, still refuses bob, and an agent of the
//     host: its launch is refused naming anna while its uiTree answers, and the host's UI acts
//     (launch, and a tap that is delivered) and so does the CLI. Seen failing with the
//     configuration honoured for peers.
//   - The host's agent holding first refuses anna's first call, naming another chat; the
//     host's release frees it.
//   - A clock seam: anna idle 14 minutes, bob is refused; 16, bob holds and anna is refused.
//     Bob watching the screen for 20 minutes without a call is not idle; 15 minutes after the
//     view ends he is. Seen failing with streams not counted as activity.
//   - The owner's release stops the holder's screen and Logcat streams (the client's views
//     end deviceGone, the host's tables empty) and anna's next call takes the phone again;
//     carol, a raw link, reads the host's own stopped events: deviceGone, "the host ended the
//     lease". Seen failing with the release not telling the lease's end (the streams ran on).
//   - Two peers' first calls at once: one holds, one is refused (run with -race).
//   - The host's trace lines of peers' operations carry peer=<name>. Seen failing without.
//   - The client's own MCP sessions coordinate on the remote id, as on a local device: the
//     second session's acting call is refused by the client, with the host asked nothing.
//   - Un-lending the phone ends anna's open view within the 2 s check and the next call is
//     DEVICE_NOT_FOUND. Seen failing with the check answering "still lent".
//   - A closed link frees what it held.
func TestLease(t *testing.T) {
	lab := newLeaseLab(t)
	annaView := wsConnect(t, lab.client)

	t.Run("a read leases the phone and another peer is refused on every kind of call", func(t *testing.T) {
		free(t, "anna's uiTree", lab.on(t, "device.uiTree", `{`+dev(remotePhone)+`}`))
		if got := lab.listedHolder(t); got != "anna" {
			t.Fatalf("the host's device.list says %q holds the phone; want anna", got)
		}
		before := monkeys(lab.hostCalls(t))
		for _, c := range lentCalls() {
			occupied(t, "bob's "+c.name, lab.onBob(t, c.name, c.params), "anna")
		}
		bobView := wsConnect(t, lab.bob)
		bobView.send("w1", "device.watchScreen", "", `{`+dev(remotePhone)+`}`, nil)
		_, f := bobView.collect("w1")
		occupied(t, "bob's watchScreen", f.ResponseEnvelope, "anna")
		bobView.send("w2", "device.watchLogs", "", `{`+dev(remotePhone)+`,"tail":10}`, nil)
		_, f = bobView.collect("w2")
		occupied(t, "bob's watchLogs", f.ResponseEnvelope, "anna")
		if n := monkeys(lab.hostCalls(t)); n != before {
			t.Errorf("the host ran %d launches for calls it refused", n-before)
		}
		if got := lab.holder(t); got != "anna" {
			t.Errorf("a refused peer changed the holder to %q", got)
		}
		// Bob is told who holds it in the listing too, without a call on the phone.
		env := lab.onBob(t, "device.list", `{}`)
		var r device.ListResult
		if err := json.Unmarshal(env.Result, &r); err != nil {
			t.Fatal(err)
		}
		for _, d := range r.Devices {
			if d.Device == remotePhone && (d.OccupiedBy == nil || d.OccupiedBy.Name != "anna") {
				t.Errorf("bob's listing shows %+v for the phone; want occupiedBy anna", d.OccupiedBy)
			}
		}
	})

	t.Run("the configuration does not let a second person in, and the host's own callers", func(t *testing.T) {
		lab.setHostConfig(t, func(conf map[string]any) { conf["occupation"] = map[string]any{"conflict": "allow"} })
		occupied(t, "bob's uiTree under conflict allow", lab.onBob(t, "device.uiTree", `{`+dev(remotePhone)+`}`), "anna")
		occupied(t, "bob's launch under conflict allow", lab.onBob(t, "device.launch", `{`+dev(remotePhone)+`,"applicationId":"dev.droidyard.formapp"}`), "anna")

		agent := startShimEnv(t, lab.host.state, "", "host-agent", []string{chatEnv + "=chat-host"}, []string{"mcp", "--no-autostart", "--wait", "5s"})
		agent.initialize("host-agent")
		defer agent.exit()
		hostID := `{"device":"` + phone1 + `"`
		if c := agent.toolWith(1, "device_uiTree", hostID+`}`); c.isError {
			t.Errorf("the agent's read of a leased phone: %s", c.raw)
		}
		before := monkeys(lab.hostCalls(t))
		if c := agent.toolWith(2, "device_launch", hostID+`,"applicationId":"dev.droidyard.formapp"}`); !c.isError || c.err.Code != device.ErrOccupied || !strings.Contains(c.err.Message, "anna") {
			t.Errorf("the agent's launch under conflict allow: %s; want DEVICE_OCCUPIED naming anna", c.raw)
		}
		if n := monkeys(lab.hostCalls(t)); n != before {
			t.Errorf("the host ran a launch for an agent it refused")
		}

		// The host's UI acts: a launch, and a tap delivered through a live view of its own.
		ui := wsConnect(t, lab.host)
		ui.send("l", "device.launch", "", hostID+`,"applicationId":"dev.droidyard.formapp"}`, nil)
		if _, f := ui.collect("l"); f.Error != nil {
			t.Errorf("the host UI's launch: %+v", f.Error)
		}
		ui.send("v", "device.watchScreen", "", hostID+`}`, nil)
		if _, f := ui.collect("v"); f.Error != nil {
			t.Fatalf("the host UI's watchScreen: %+v", f.Error)
		}
		ui.send("t", "device.tap", "", hostID+`,"x":0.5,"y":0.5}`, nil)
		_, f := ui.collect("t")
		var tr device.ScreenInputResult
		if f.Error != nil || json.Unmarshal(f.Result, &tr) != nil || !tr.Delivered {
			t.Errorf("the host UI's tap: %+v %s; want it delivered", f.Error, f.Result)
		}
		ui.c.Close(1000, "")
		if env, _ := lab.host.call(t, "device.launch", "--params", hostID+`,"applicationId":"dev.droidyard.formapp"}`); env.Error != nil {
			t.Errorf("the CLI's launch on the host: %+v", env.Error)
		}
		if got := lab.holder(t); got != "anna" {
			t.Errorf("the host's UI and CLI took the lease: %q holds the phone", got)
		}

		// The agent first: the owner's release, then the agent holds, and anna is refused
		// naming another chat; then released again, anna holds.
		lab.release(t)
		if c := agent.toolWith(3, "device_launch", hostID+`,"applicationId":"dev.droidyard.formapp"}`); c.isError {
			t.Fatalf("the agent's launch of a free phone: %s", c.raw)
		}
		env := lab.on(t, "device.uiTree", `{`+dev(remotePhone)+`}`)
		if env.Error == nil || env.Error.Code != device.ErrOccupied || !strings.Contains(env.Error.Message, "another chat (host-agent") {
			t.Errorf("anna's first call on the agent's phone: %+v; want DEVICE_OCCUPIED naming another chat (host-agent)", env.Error)
		}
		lab.release(t)
		free(t, "anna's call after the release", lab.on(t, "device.uninstall", touchParams))
		if got := lab.holder(t); got != "anna" {
			t.Errorf("after the release anna's call left %q holding the phone", got)
		}
	})

	t.Run("an idle lease is taken over and a watched screen is not idle", func(t *testing.T) {
		lab.clk.advance(14 * time.Minute)
		occupied(t, "bob at 14 minutes", lab.onBob(t, "device.uiTree", `{`+dev(remotePhone)+`}`), "anna")
		lab.clk.advance(2 * time.Minute)
		free(t, "bob at 16 minutes", lab.onBob(t, "device.uninstall", touchParams))
		if got := lab.holder(t); got != "bob" {
			t.Fatalf("after the takeover %q holds the phone; want bob", got)
		}
		occupied(t, "anna after the takeover", lab.on(t, "device.uiTree", `{`+dev(remotePhone)+`}`), "bob")

		// Bob watches the phone's screen and stays silent for 20 minutes.
		bobView := wsConnect(t, lab.bob)
		_, stream, _, _ := bobView.watchRemote("w", remotePhone)
		sock := attachScreen(t, lab.bob, lab.bob.token, stream)
		sock.message("the first message of bob's view")
		lab.clk.advance(20 * time.Minute)
		occupied(t, "anna beside bob's open view, 20 minutes on", lab.on(t, "device.uiTree", `{`+dev(remotePhone)+`}`), "bob")
		// The view ends, and the idle time starts then.
		sock.c.Close(1000, "")
		bobView.c.Close(1000, "")
		waitFor(t, "the host's view of bob to end", func() bool { return lab.host.adb.LiveScreenStreams() == 0 })
		occupied(t, "anna right after bob's view ended", lab.on(t, "device.uiTree", `{`+dev(remotePhone)+`}`), "bob")
		lab.clk.advance(16 * time.Minute)
		free(t, "anna 16 minutes after bob's view ended", lab.on(t, "device.uninstall", touchParams))
		if got := lab.holder(t); got != "anna" {
			t.Errorf("%q holds the phone; want anna", got)
		}
	})

	t.Run("the owner's release stops the holder's streams", func(t *testing.T) {
		screenWS, logsWS := wsConnect(t, lab.client), wsConnect(t, lab.client)
		subScreen, stream, _, _ := screenWS.watchRemote("s", remotePhone)
		sock := attachScreen(t, lab.client, lab.client.token, stream)
		sock.message("the first message of anna's view")
		logsWS.send("l", "device.watchLogs", "", `{`+dev(remotePhone)+`,"tail":10}`, nil)
		_, f := logsWS.collect("l")
		var wl device.WatchLogsResult
		if f.Error != nil || json.Unmarshal(f.Result, &wl) != nil || wl.SubscriptionID == "" {
			t.Fatalf("anna's watchLogs: %+v %s", f.Error, f.Result)
		}
		waitFor(t, "the host's streams", func() bool { return lab.host.adb.LiveScreenStreams() == 1 && lab.host.adb.LiveStreams() == 1 })

		start := time.Now()
		lab.release(t)
		if reason, _ := screenWS.stoppedOn(subScreen, remotePhone); reason != "deviceGone" {
			t.Errorf("anna's view stopped as %q after the owner's release; want deviceGone", reason)
		}
		for {
			f := logsWS.must("anna's Logcat stopped event")
			if f.Event == nil || f.SubscriptionID != wl.SubscriptionID {
				t.Fatalf("a frame that is not an event of the Logcat subscription: %+v", f)
			}
			if f.Event.Type != "stopped" {
				continue
			}
			var p device.StoppedEvent
			if err := json.Unmarshal(f.Event.Payload, &p); err != nil || p.Reason != "deviceGone" {
				t.Errorf("anna's Logcat stopped as %s (%v); want deviceGone", f.Event.Payload, err)
			}
			break
		}
		waitFor(t, "the host's streams to end", func() bool { return lab.host.adb.LiveScreenStreams() == 0 && lab.host.adb.LiveStreams() == 0 })
		t.Logf("the holder's screen and Logcat streams ended %v after the owner's release", time.Since(start))
		if got := lab.holder(t); got != "" {
			t.Errorf("after the release %q holds the phone", got)
		}
		free(t, "anna's next call", lab.on(t, "device.uninstall", touchParams))
		if got := lab.holder(t); got != "anna" {
			t.Errorf("anna's next call left %q holding the phone", got)
		}
		sock.c.Close(1000, "")
	})

	t.Run("what the host itself sends when it ends a lease", func(t *testing.T) {
		lab.release(t)
		type seen struct{ reason, cause string }
		stopped := make(chan seen, 4)
		route := func(topic string) func(json.RawMessage) {
			return func(raw json.RawMessage) {
				// The event as the host sent it: its envelope, not a frame of a session.
				var e struct {
					Topic, Type string
					Payload     struct{ Reason, Cause string }
				}
				if json.Unmarshal(raw, &e) != nil || e.Topic != topic || e.Type != "stopped" {
					return
				}
				stopped <- seen{e.Payload.Reason, e.Payload.Cause}
			}
		}
		for _, w := range []struct{ name, topic, params string }{
			{"device.watchScreen", "device.screen", `{"device":"` + phone1 + `"}`},
			{"device.watchLogs", "device.logcat", `{"device":"` + phone1 + `","tail":10}`},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			resp, stop, err := lab.carol.CallRouted(ctx, capability.RequestEnvelope{Capability: w.name, Params: json.RawMessage(w.params)}, route(w.topic))
			cancel()
			if err != nil || resp.Error != nil {
				t.Fatalf("carol's %s: %v %+v", w.name, err, resp.Error)
			}
			t.Cleanup(stop)
		}
		if got := lab.holder(t); got != "carol" {
			t.Fatalf("%q holds the phone; want carol", got)
		}
		lab.release(t)
		for range 2 {
			select {
			case s := <-stopped:
				if s.reason != "deviceGone" || s.cause != "the host ended the lease" {
					t.Errorf("the host sent stopped %q (%q); want deviceGone, the host ended the lease", s.reason, s.cause)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no stopped event from the host after the owner's release")
			}
		}
		waitFor(t, "the host's streams to end", func() bool { return lab.host.adb.LiveScreenStreams() == 0 && lab.host.adb.LiveStreams() == 0 })
	})

	t.Run("two first calls at once", func(t *testing.T) {
		for round := range 3 {
			lab.release(t)
			if got := lab.holder(t); got != "" {
				t.Fatalf("round %d: %q holds a released phone", round, got)
			}
			var wg sync.WaitGroup
			var anna, bob capability.ResponseEnvelope
			start := make(chan struct{})
			wg.Add(2)
			go func() { defer wg.Done(); <-start; anna = lab.on(t, "device.uninstall", touchParams) }()
			go func() { defer wg.Done(); <-start; bob = lab.onBob(t, "device.uninstall", touchParams) }()
			close(start)
			wg.Wait()
			switch {
			case anna.Error == nil && bob.Error == nil:
				t.Errorf("round %d: both first calls went on", round)
			case anna.Error != nil && bob.Error != nil:
				t.Errorf("round %d: both first calls were refused: %+v / %+v", round, anna.Error, bob.Error)
			case anna.Error != nil:
				occupied(t, "anna, the second", anna, "bob")
			default:
				occupied(t, "bob, the second", bob, "anna")
			}
		}
		lab.release(t)
		free(t, "anna after the rounds", lab.on(t, "device.uninstall", touchParams))
	})

	t.Run("the host's trace names the peer", func(t *testing.T) {
		testtime.Eventually(t, testtime.Bound(10*time.Second), "peer trace lines", func() bool {
			log := lab.trace.String()
			return strings.Contains(log, "client=peer capability=device.uiTree") && strings.Contains(log, " peer=anna") && strings.Contains(log, " peer=bob")
		})
		for _, line := range strings.Split(lab.trace.String(), "\n") {
			if strings.Contains(line, "droidyard: op ") && strings.Contains(line, "client=peer") && !strings.Contains(line, " peer=") {
				t.Errorf("a peer's trace line names no peer: %q", line)
			}
			if strings.Contains(line, "droidyard: op ") && !strings.Contains(line, "client=peer") && strings.Contains(line, " peer=") {
				t.Errorf("a trace line that is not a peer's names a peer: %q", line)
			}
		}
	})

	t.Run("the client's own sessions coordinate on the remote id", func(t *testing.T) {
		lab.release(t)
		open := func(name, chat string) *shim {
			s := startShimEnv(t, lab.client.state, "", name, []string{chatEnv + "=" + chat}, []string{"mcp", "--no-autostart", "--wait", "5s"})
			s.initialize(name)
			return s
		}
		m1, m2 := open("agent-one", "chat-1"), open("agent-two", "chat-2")
		defer m1.exit()
		defer m2.exit()
		args := `{` + dev(remotePhone) + `,"applicationId":"dev.droidyard.formapp"}`
		if c := m1.toolWith(1, "device_launch", args); c.isError {
			t.Fatalf("the first session's launch: %s", c.raw)
		}
		before := monkeys(lab.hostCalls(t))
		c := m2.toolWith(1, "device_launch", args)
		if !c.isError || c.err.Code != device.ErrOccupied || !strings.Contains(c.err.Message, "agent-one") || strings.Contains(c.err.Message, "peer ") {
			t.Errorf("the second session's launch: %s; want the client's own DEVICE_OCCUPIED naming agent-one", c.raw)
		}
		if n := monkeys(lab.hostCalls(t)); n != before {
			t.Errorf("the host was asked to launch for a call the client refused")
		}
		if c := m2.toolWith(2, "device_uiTree", `{`+dev(remotePhone)+`}`); c.isError {
			t.Errorf("the second session's read of the remote id: %s", c.raw)
		}
		env, _ := lab.client.call(t, "device.list")
		var r device.ListResult
		if err := json.Unmarshal(env.Result, &r); err != nil {
			t.Fatal(err)
		}
		for _, d := range r.Devices {
			if d.Device == remotePhone && (d.OccupiedBy == nil || d.OccupiedBy.Name != "agent-one") {
				t.Errorf("the client's listing shows %+v for the remote phone; want occupiedBy agent-one", d.OccupiedBy)
			}
		}
	})

	t.Run("un-lending ends the view, and the next call finds no device", func(t *testing.T) {
		lab.release(t)
		free(t, "anna's call", lab.on(t, "device.uninstall", touchParams))
		sub, stream, _, _ := annaView.watchRemote("u", remotePhone)
		sock := attachScreen(t, lab.client, lab.client.token, stream)
		sock.message("the first message of anna's view")

		start := time.Now()
		lab.lendPhone1To(t, "bob", "carol")
		reason, _ := annaView.stoppedOn(sub, remotePhone)
		took := time.Since(start)
		t.Logf("anna's view ended %v after the phone was un-lent", took)
		if reason != "deviceGone" || took > device.LeaseSweepEvery+2*time.Second {
			t.Errorf("anna's view stopped as %q after %v; want deviceGone within the 2 s check (and a close)", reason, took)
		}
		env := lab.on(t, "device.uiTree", `{`+dev(remotePhone)+`}`)
		if env.Error == nil || env.Error.Code != device.ErrNotFound {
			t.Errorf("anna's call on a phone no longer lent to her: %+v; want DEVICE_NOT_FOUND", env.Error)
		}
		if got := lab.holder(t); got != "" {
			t.Errorf("%q holds a phone un-lent from its holder", got)
		}
		free(t, "bob's call on the phone anna lost", lab.onBob(t, "device.uninstall", touchParams))
		sock.c.Close(1000, "")
		lab.lendPhone1To(t, "anna", "bob", "carol")
	})

	t.Run("a closed link frees what it held", func(t *testing.T) {
		lab.release(t)
		free(t, "anna's call", lab.on(t, "device.uninstall", touchParams))
		if got := lab.holder(t); got != "anna" {
			t.Fatalf("%q holds the phone; want anna", got)
		}
		lab.client.host.Stop()
		testtime.Eventually(t, testtime.Bound(10*time.Second), "anna's lease to end with her link", func() bool { return lab.holder(t) == "" })
		free(t, "bob's call after anna's link closed", lab.onBob(t, "device.uninstall", touchParams))
	})
}
