package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// dumps counts the UI dumps the host's adb ran.
func dumps(calls []string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, "uiautomator dump") {
			n++
		}
	}
	return n
}

// TestLeaseOrphanedCall: a peer's call is an operation that outlives its link, and its
// authorization runs when it has had its turn (ADR-0101, ADR-0104 §2). Anna queues a uiTree
// behind a dump that hangs, her link drops, bob leases the phone, and the dump is cancelled:
// anna's call gets its turn with a link that is gone and is refused, naming bob, with no dump
// run for it on the phone. Seen failing with an unregistered session let through the preamble
// (the call dumped bob's screen, which anna reads once she reconnects).
func TestLeaseOrphanedCall(t *testing.T) {
	lab := newLeaseLab(t)
	ctx := context.Background()
	devicetest.Write(t, lab.hostDir, phone1+".dumptty.hang", nil)
	holdID, err := lab.host.ops.Start(ctx, capability.Request{Capability: "device.uiTree", Client: capability.ClientCLI,
		Params: json.RawMessage(`{"device":"` + phone1 + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host's dump to hang", func() bool { return dumps(lab.hostCalls(t)) == 1 })

	go lab.client.invoke("device.uiTree", `{`+dev(remotePhone)+`}`)
	var annaOp, annaLink string
	testtime.Eventually(t, testtime.Bound(10*time.Second), "anna's call to queue on the host", func() bool {
		for _, op := range lab.host.ops.Active() {
			if op.Capability == "device.uiTree" && op.Client == capability.ClientPeer && op.State == capability.StateQueued && op.Session != nil {
				annaOp, annaLink = op.OperationID, op.Session.ID
			}
		}
		return annaOp != ""
	})
	lab.client.host.Stop()
	testtime.Eventually(t, testtime.Bound(10*time.Second), "anna's link session to end", func() bool { return !lab.host.ops.Sessions().Registered(annaLink) })

	free(t, "bob's launch", lab.onBob(t, "device.launch", `{`+dev(remotePhone)+`,"applicationId":"dev.droidyard.formapp"}`))
	if got := lab.holder(t); got != "bob" {
		t.Fatalf("%q holds the phone; want bob", got)
	}

	if err := lab.host.ops.Cancel(holdID); err != nil {
		t.Fatal(err)
	}
	out, err := lab.host.ops.Wait(ctx, annaOp, time.Now().Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != device.ErrOccupied || !strings.Contains(out.Error.Message, "bob") {
		t.Fatalf("anna's orphaned call: %s %+v; want DEVICE_OCCUPIED naming bob", out.State, out.Error)
	}
	if n := dumps(lab.hostCalls(t)); n != 1 {
		t.Errorf("the host ran %d dumps; want only the hung one: nothing was run for the orphaned call", n)
	}
	if got := lab.holder(t); got != "bob" {
		t.Errorf("%q holds the phone after the orphan's turn; want bob", got)
	}
}

// stoppedSeen is a stopped event as the host sent it.
type stoppedSeen struct{ reason, cause string }

// carolStreams calls name with params as the raw peer carol, who is lent the phone, and
// returns the stopped events of its subscription as the host sent them, and the call's end.
func (l *leaseLab) carolStream(t *testing.T, name, topic, params string) (stopped chan stoppedSeen, answered chan error) {
	t.Helper()
	stopped, answered = make(chan stoppedSeen, 4), make(chan error, 1)
	route := func(raw json.RawMessage) {
		var e struct {
			Topic, Type string
			Payload     struct{ Reason, Cause string }
		}
		if json.Unmarshal(raw, &e) == nil && e.Topic == topic && e.Type == "stopped" {
			stopped <- stoppedSeen{e.Payload.Reason, e.Payload.Cause}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		resp, stop, err := l.carol.CallRouted(ctx, capability.RequestEnvelope{Capability: name, Params: json.RawMessage(params)}, route)
		t.Cleanup(stop)
		if err == nil && resp.Error != nil {
			err = fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
		}
		answered <- err
	}()
	return stopped, answered
}

// TestLeaseEndsWhileAStreamStarts: a stream that is still starting when its lease ends is in
// no table, so the end finds nothing to stop (ADR-0104 §4). The stream checks, once it is in
// its table, that its link still holds the phone, and if not ends as `stopped deviceGone`
// with the cause of the host's end, leaving the host no stream and no server. For both
// streams and for both ways the lease ends: the owner's release and the phone no longer
// lent (the check on a 2 s loop is called by hand so the window is deterministic). Seen
// failing with the check removed: the stream ran on unheld, on a phone no longer lent.
func TestLeaseEndsWhileAStreamStarts(t *testing.T) {
	lab := newLeaseLab(t)
	for _, s := range []struct{ name, topic, params string }{
		{"device.watchScreen", "device.screen", `{"device":"` + phone1 + `"}`},
		{"device.watchLogs", "device.logcat", `{"device":"` + phone1 + `","tail":10}`},
	} {
		for _, how := range []string{"release", "un-lent"} {
			t.Run(s.name+" "+how, func(t *testing.T) {
				lab.release(t)
				entered, proceed := make(chan struct{}, 1), make(chan struct{})
				lab.host.adb.SetStartHook(func() { entered <- struct{}{}; <-proceed })
				t.Cleanup(func() { lab.host.adb.SetStartHook(nil) })
				// Lent again whatever happens, so one subtest's failure leaves the next its phone.
				t.Cleanup(func() { lab.lendPhone1To(t, "anna", "bob", "carol") })
				stopped, answered := lab.carolStream(t, s.name, s.topic, s.params)
				select {
				case <-entered:
				case <-time.After(20 * time.Second):
					t.Fatal("the stream never reached its start")
				}
				if got := lab.holder(t); got != "carol" {
					t.Fatalf("%q holds the phone while carol's stream starts; want carol", got)
				}
				switch how {
				case "release":
					lab.release(t)
				default:
					lab.lendPhone1To(t, "anna", "bob")
					lab.host.adb.SweepLeases()
				}
				if got := lab.holder(t); got != "" {
					t.Fatalf("%q holds the phone after the lease ended", got)
				}
				lab.host.adb.SetStartHook(nil)
				close(proceed)

				if err := <-answered; err != nil {
					t.Fatalf("the stream's start answered %v", err)
				}
				select {
				case e := <-stopped:
					if e.reason != "deviceGone" || e.cause != "the host ended the lease" {
						t.Errorf("the stream stopped %q (%q); want deviceGone, the host ended the lease", e.reason, e.cause)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("a stream that started after its lease ended was not stopped")
				}
				waitFor(t, "the host to keep no stream", func() bool {
					return lab.host.adb.LiveScreenStreams() == 0 && lab.host.adb.LiveStreams() == 0 && lab.host.adb.LiveScreenSources() == 0
				})
				if got := lab.holder(t); got != "" {
					t.Errorf("%q holds the phone after the stream ended", got)
				}
			})
		}
	}
}
