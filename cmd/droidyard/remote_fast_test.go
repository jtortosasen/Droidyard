package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/testtime"
)

// serverStarts are the host's starts of the screen server, one command line each.
func (l *remoteLab) serverStarts(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range l.hostCalls(t) {
		if strings.Contains(c, "app_process") {
			out = append(out, c)
		}
	}
	return out
}

// setRemoteFast marks the client's remote qa as policy.update does and reconciles at once, so
// the test does not wait for the 2 s pass.
func (l *remoteLab) setRemoteFast(t *testing.T, fast bool) {
	t.Helper()
	remote := map[string]any{"name": remoteName, "address": l.host.host.Health().Hosting.Listen, "fingerprint": l.host.host.Identity().Fingerprint()}
	if fast {
		remote["fast"] = true
	}
	body, err := json.Marshal(map[string]any{"remotes": []any{remote}})
	if err != nil {
		t.Fatal(err)
	}
	if env := l.on(t, "policy.update", string(body)); env.Error != nil {
		t.Fatalf("policy.update %s: %+v", body, env.Error)
	}
	l.client.host.Reconcile()
}

// TestAFastChangeReopensTheRemoteViewAtTheNewQuality holds ADR-0123 end to end, between two
// daemons: marking the remote fast on the client travels in the open session, ends the
// client's view of the remote's device with `renewed` (nothing else of the remote: the
// Logcat stream stays), and a view started at once is a new start on the host with the fast
// preset, in the same link session (the host counts one hello). The host's release of the
// old view's source is held for the whole test, so the new start can only be a new source if
// the old view's end dropped its reference before the answer to the client's unsubscribe.
// Seen failing:
//   - on step 1's base, without the `renewed` reason: the view's stopped event was deviceGone
//     (the link was dropped as before), and a second hello was counted;
//   - with the host's view end dropping its reference on the release goroutine again (step
//     1's drop reverted): the new view joined the old source, and the host logged no second
//     start.
func TestAFastChangeReopensTheRemoteViewAtTheNewQuality(t *testing.T) {
	t.Parallel()
	lab := newScreenLab(t, 3, labOptions{holdRelease: make(chan struct{}), hostFiles: map[string]string{phone1 + ".log": remoteLog}})
	w := wsConnect(t, lab.client)
	hellos := func() int { return strings.Count(lab.host.log.String(), "peer anna: link up") }
	if n := hellos(); n != 1 {
		t.Fatalf("the host counted %d hellos before the change, want 1", n)
	}

	viewSub, stream, _, _ := w.watchRemote("r1", remotePhone)
	sock := attachScreen(t, lab.client, lab.client.token, stream)
	sock.message("the first message of the view")
	w.send("l1", "device.watchLogs", "", `{"device":"`+remotePhone+`","tail":10}`, nil)
	_, f := w.collect("l1")
	if f.Error != nil {
		t.Fatalf("device.watchLogs: %+v", f.Error)
	}
	logSub := subscriptionOf(t, f)
	starts := lab.serverStarts(t)
	if len(starts) != 1 || !strings.Contains(starts[0], "max_size=1280") {
		t.Fatalf("the host's starts before the change: %q; want one, with max_size=1280", starts)
	}

	lab.setRemoteFast(t, true)

	// The view ends renewed; the Logcat stream says nothing.
	var reason, cause string
	logsEnded := false
	for reason == "" {
		fr, ok := w.next(testtime.Bound(30 * time.Second))
		if !ok {
			t.Fatalf("no stopped event for the view (the connection ended: %v)", w.endedErr())
		}
		switch {
		case fr.Event == nil:
			t.Fatalf("a frame that is not an event: %+v", fr)
		case fr.SubscriptionID == logSub && fr.Event.Type == "stopped":
			logsEnded = true // reported after the view's reason, which is the first thing to read
		case fr.SubscriptionID == viewSub && fr.Event.Type == "stopped":
			var se struct{ Reason, Cause string }
			if err := json.Unmarshal(fr.Event.Payload, &se); err != nil {
				t.Fatal(err)
			}
			reason, cause = se.Reason, se.Cause
		}
	}
	if reason != "renewed" || !strings.Contains(cause, "fast mark") {
		t.Fatalf("the view ended %q (%q); want renewed, naming the remote's fast mark", reason, cause)
	}
	if logsEnded {
		t.Fatal("the Logcat stream was ended by the mark change")
	}

	// A view started at once answers, and the host starts a new server with the fast preset.
	sub2, stream2, _, _ := w.watchRemote("r2", remotePhone)
	sock2 := attachScreen(t, lab.client, lab.client.token, stream2)
	sock2.message("the first message of the renewed view")
	if sub2 == viewSub {
		t.Error("the renewed view reused the ended view's subscription")
	}
	testtime.Eventually(t, testtime.Bound(15*time.Second), "the host's second start of the screen server", func() bool { return len(lab.serverStarts(t)) >= 2 })
	starts = lab.serverStarts(t)
	if len(starts) != 2 || !strings.Contains(starts[1], "max_size=1024") || strings.Contains(starts[1], "max_size=1280") {
		t.Errorf("the host's starts after the change: %q; want a second one with max_size=1024", starts)
	}

	// The link, and with it the lease and the Logcat stream, was never touched.
	if n := hellos(); n != 1 {
		t.Errorf("the host counted %d hellos, want 1: the change travelled in the session", n)
	}
	if n := lab.host.adb.LiveStreams(); n != 1 {
		t.Errorf("the host holds %d Logcat streams, want the one that stayed", n)
	}
	for {
		fr, ok := w.next(500 * time.Millisecond)
		if !ok {
			break
		}
		if fr.SubscriptionID == logSub && fr.Event != nil && fr.Event.Type == "stopped" {
			t.Fatalf("the Logcat stream ended after the change: %+v", fr.Event)
		}
	}
}
