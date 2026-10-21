package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/event"
)

// The UI reads of a lent device end to end (plan m2x step 3; ADR-0115 Decision 1, ADR-0100
// §3, ADR-0104, ADR-0106): two daemons paired over real TLS on loopback, anna's client and the
// host with a fake UI reader behind the lent phone, bob as a daemon of his own and carol as a
// raw link (the lease lab).

// remoteUIDump is the host phone's full dump.
const remoteUIDump = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">
<node index="0" text="" class="android.widget.FrameLayout" package="com.example.host" bounds="[0,0][1080,2400]">
<node index="0" text="hello &amp; welcome" class="android.widget.TextView" package="com.example.host" bounds="[0,0][1080,100]" />
</node>
</hierarchy>
`

// remoteUIInstance is the instance the fake reader answers as, the first part of its cursors.
const remoteUIInstance = "0123456789abcdef"

// newUILab is the lease lab whose host phone has a fake UI reader that answers remoteUIDump.
func newUILab(t *testing.T) (*leaseLab, *devicetest.FakeReader) {
	t.Helper()
	r := devicetest.StartReader(t)
	r.SetFullDocs(remoteUIDump)
	lab := newLeaseLabWith(t, map[string]string{phone1 + ".push.exit": "0", phone1 + ".start.out": "", phone1 + ".forward.out": r.Port}, true)
	return lab, r
}

func clickAt(window int) devicetest.FakeEvent {
	return devicetest.FakeEvent{TimeMs: time.Now().UnixMilli(), Type: "click", Window: window, Package: "com.example.host", Class: "android.widget.Button"}
}

// watchUI sends device.watchUiEvents for the remote phone and returns its subscription.
func (w *wsClient) watchUI(reqID string) string {
	w.t.Helper()
	w.send(reqID, "device.watchUiEvents", "", `{`+dev(remotePhone)+`}`, nil)
	_, f := w.collect(reqID)
	if f.Error != nil {
		w.t.Fatalf("device.watchUiEvents %s: %+v", remotePhone, f.Error)
	}
	if !untrusted(f.ResponseEnvelope) {
		w.t.Errorf("a remote device's watchUiEvents is not marked untrusted: %+v", f.Meta)
	}
	var r device.WatchUIEventsResult
	if err := json.Unmarshal(f.Result, &r); err != nil || r.Device != remotePhone || r.SubscriptionID == "" {
		w.t.Fatalf("device.watchUiEvents result %s: %v; want device %s", f.Result, err, remotePhone)
	}
	return r.SubscriptionID
}

// uiFrame is one event of the UI events subscription sub: its type, the window ids of its
// events, and for a stopped event its reason and cause.
type uiFrame struct {
	typ     string
	windows []int
	reason  string
	cause   string
}

func (w *wsClient) uiFrameOf(sub string) uiFrame {
	w.t.Helper()
	f := w.must("a device.uiEvents event")
	if f.Event == nil || f.SubscriptionID != sub || f.Event.Topic != "device.uiEvents" {
		w.t.Fatalf("a frame that is not a device.uiEvents event of %s: %+v", sub, f)
	}
	var p struct {
		Device string `json:"device"`
		Events []struct {
			Window *int `json:"window"`
		} `json:"events"`
		Untrusted bool   `json:"untrusted"`
		Reason    string `json:"reason"`
		Cause     string `json:"cause"`
	}
	if err := json.Unmarshal(f.Event.Payload, &p); err != nil || p.Device != remotePhone {
		w.t.Fatalf("payload %s: %v; want device %s", f.Event.Payload, err, remotePhone)
	}
	if f.Event.Type == "events" && !p.Untrusted {
		w.t.Errorf("an events event that is not marked untrusted: %s", f.Event.Payload)
	}
	out := uiFrame{typ: f.Event.Type, reason: p.Reason, cause: p.Cause}
	for _, e := range p.Events {
		out.windows = append(out.windows, *e.Window)
	}
	return out
}

// nonEmptyUI is the next events event of sub that carries events (an empty one reports a gap
// only), or the stopped event if one comes first.
func (w *wsClient) nonEmptyUI(sub string) uiFrame {
	w.t.Helper()
	for {
		if f := w.uiFrameOf(sub); f.typ != "events" || len(f.windows) > 0 {
			return f
		}
	}
}

// TestRemoteUIReads: plan m2x step 3's check, over the lease lab.
//
//   - The client's device.uiDump on the remote id is an artifact in the client's store, under
//     its own reference, whose bytes are the host reader's document byte for byte, untrusted,
//     with the counts of the bytes fetched; the host's reference is nowhere in the result and
//     the client's adb is never asked for the device. Seen failing with the host's result
//     passed through (the reference was not in the client's store).
//   - device.uiEvents with a cursor and a wait across a toast scripted after the call began
//     returns the host's events and cursor, marked untrusted; a malformed cursor is refused on
//     the client and the host's reader is asked nothing. Seen failing with the cursor checked
//     only by the host's handler (the reader was asked).
//   - device.watchUiEvents delivers the host's events with the remote id, the client's
//     unsubscribe ends the host's poller (the reader gets no further `events` request), five
//     client sessions hold four host streams and the oldest hears `stopped replaced`, and a
//     cut link is `stopped deviceGone`. Seen failing with the host's poller not ended by the
//     unsubscribe, and with the host's cap removed.
//   - A second peer's call while anna holds the phone is DEVICE_OCCUPIED naming anna, for the
//     three reads. Seen failing with the code left out of the descriptors' errors (the relay
//     answered DEVICE_NOT_READY, incompatible).
func TestRemoteUIReads(t *testing.T) {
	lab, r := newUILab(t)

	t.Run("uiDump stores the host's document locally", func(t *testing.T) {
		env := lab.on(t, "device.uiDump", `{`+dev(remotePhone)+`}`)
		var res device.UIDumpResult
		if env.Error != nil || json.Unmarshal(env.Result, &res) != nil || !untrusted(env) {
			t.Fatalf("device.uiDump %s: %+v %s, untrusted %v", remotePhone, env.Error, env.Result, untrusted(env))
		}
		e, err := lab.client.store.Get(res.Dump.Ref)
		if err != nil {
			t.Fatalf("the result's reference %s is not in the client's store: %v (the host's reference passed through?)", res.Dump.Ref, err)
		}
		if string(e.Data) != remoteUIDump || !e.Ref.Untrusted || e.Ref.MediaType != "application/xml" || e.Producer != "device.uiDump" {
			t.Errorf("the client's artifact: %d bytes (want the host's %d), untrusted %v, %s, from %q", len(e.Data), len(remoteUIDump), e.Ref.Untrusted, e.Ref.MediaType, e.Producer)
		}
		if res.Device != remotePhone || res.Nodes != 2 || res.Foreground != "com.example.host" || res.Dump.Bytes != len(remoteUIDump) {
			t.Errorf("result %+v; want %s, 2 nodes, com.example.host, %d bytes", res, remotePhone, len(remoteUIDump))
		}
		if got := r.FullRequests(); len(got) != 1 || got[0] != "full" {
			t.Errorf("the host's reader was asked %q; want one `full` request", got)
		}
		if got := lab.host.store.Len(); got != 1 {
			t.Errorf("the host's store holds %d artifacts; want its own dump", got)
		}
	})

	t.Run("uiEvents returns the host's events and cursor", func(t *testing.T) {
		r.AddEvents(clickAt(1), clickAt(2), clickAt(3))
		env := lab.on(t, "device.uiEvents", `{`+dev(remotePhone)+`}`)
		var first device.UIEventsResult
		if env.Error != nil || json.Unmarshal(env.Result, &first) != nil || !untrusted(env) {
			t.Fatalf("device.uiEvents: %+v %s, untrusted %v", env.Error, env.Result, untrusted(env))
		}
		if first.Device != remotePhone || len(first.Events) != 3 || first.Cursor != remoteUIInstance+".3" {
			t.Fatalf("first read %+v; want the three clicks and the host's cursor %s.3", first, remoteUIInstance)
		}

		go func() {
			time.Sleep(300 * time.Millisecond)
			r.AddEvents(devicetest.FakeEvent{TimeMs: time.Now().UnixMilli(), Type: "toast", Window: -1, Package: "com.example.host", Text: "Saved"})
		}()
		start := time.Now()
		env = lab.on(t, "device.uiEvents", `{`+dev(remotePhone)+`,"since":"`+first.Cursor+`","waitMs":10000,"until":"change"}`)
		var second device.UIEventsResult
		if env.Error != nil || json.Unmarshal(env.Result, &second) != nil || !untrusted(env) {
			t.Fatalf("device.uiEvents with a wait: %+v %s", env.Error, env.Result)
		}
		if len(second.Events) != 1 || second.Events[0].Type != "toast" || second.Events[0].Text != "Saved" || second.Outcome != "changed" || second.Cursor != remoteUIInstance+".4" {
			t.Errorf("second read %+v; want the toast, outcome changed and the host's cursor %s.4", second, remoteUIInstance)
		}
		if took := time.Since(start); took < 250*time.Millisecond {
			t.Errorf("the wait returned after %v, before the toast was scripted", took)
		}

		asked := len(r.EventsRequests())
		bad := lab.on(t, "device.uiEvents", `{`+dev(remotePhone)+`,"since":"not-a-cursor"}`)
		if bad.Error == nil || bad.Error.Code != capability.ErrInvalidParams {
			t.Errorf("a malformed cursor: %+v; want REQUEST_INVALID_PARAMS", bad.Error)
		}
		if got := len(r.EventsRequests()); got != asked {
			t.Errorf("the host's reader was asked %d times for a malformed cursor", got-asked)
		}
	})

	t.Run("a second peer is refused while anna holds the phone", func(t *testing.T) {
		if got := lab.holder(t); got != "anna" {
			t.Fatalf("%q holds the phone; want anna", got)
		}
		asked := len(r.EventsRequests())
		occupied(t, "bob's uiDump", lab.onBob(t, "device.uiDump", `{`+dev(remotePhone)+`}`), "anna")
		occupied(t, "bob's uiEvents", lab.onBob(t, "device.uiEvents", `{`+dev(remotePhone)+`}`), "anna")
		bob := wsConnect(t, lab.bob)
		bob.send("w", "device.watchUiEvents", "", `{`+dev(remotePhone)+`}`, nil)
		_, f := bob.collect("w")
		occupied(t, "bob's watchUiEvents", f.ResponseEnvelope, "anna")
		if got := len(r.EventsRequests()); got != asked {
			t.Errorf("the host's reader was asked %d times for calls it refused", got-asked)
		}
		if got := lab.holder(t); got != "anna" {
			t.Errorf("a refused peer changed the holder to %q", got)
		}
	})

	t.Run("watchUiEvents delivers the host's events and ends with the client's unsubscribe", func(t *testing.T) {
		w := wsConnect(t, lab.client)
		sub := w.watchUI("w1")
		waitFor(t, "the host's stream", func() bool { return lab.host.adb.LiveUIEventStreams() == 1 })
		r.AddEvents(clickAt(20), clickAt(21))
		if f := w.nonEmptyUI(sub); f.typ != "events" || len(f.windows) != 2 || f.windows[0] != 20 || f.windows[1] != 21 {
			t.Fatalf("frame %+v; want the host's two clicks, with the remote id", f)
		}

		w.send("u1", "events.unsubscribe", "", `{"subscriptionId":"`+sub+`"}`, nil)
		w.collect("u1")
		waitFor(t, "the host's stream to end after the client unsubscribed", func() bool { return lab.host.adb.LiveUIEventStreams() == 0 })
		asked := len(r.EventsRequests())
		time.Sleep(800 * time.Millisecond) // three of the poller's ticks
		if got := len(r.EventsRequests()); got != asked {
			t.Errorf("the host's reader got %d events requests after the client unsubscribed; the poller outlived it", got-asked)
		}
	})

	t.Run("five client sessions hold four host streams", func(t *testing.T) {
		var sockets []*wsClient
		var subs []string
		for i := range 5 {
			w := wsConnect(t, lab.client)
			sockets = append(sockets, w)
			subs = append(subs, w.watchUI("w"+string(rune('1'+i))))
		}
		waitFor(t, "four host streams", func() bool { return lab.host.adb.LiveUIEventStreams() == 4 })
		if f := sockets[0].nonEmptyUI(subs[0]); f.typ != "stopped" || f.reason != "replaced" || !strings.Contains(f.cause, "to make room") {
			t.Errorf("the oldest session got %+v; want stopped replaced, saying the remote made room", f)
		}
		r.AddEvents(clickAt(30))
		for i := 1; i < 5; i++ {
			if f := sockets[i].nonEmptyUI(subs[i]); f.typ != "events" || len(f.windows) != 1 || f.windows[0] != 30 {
				t.Errorf("session %d got %+v; want the click", i+1, f)
			}
		}
		if got := lab.host.adb.LiveUIEventStreams(); got != 4 {
			t.Errorf("%d host streams; want the cap, 4", got)
		}
		for _, w := range sockets {
			w.c.Close(1000, "")
		}
		waitFor(t, "the host's streams to end with the client's sessions", func() bool { return lab.host.adb.LiveUIEventStreams() == 0 })
	})

	t.Run("a cut link is deviceGone", func(t *testing.T) {
		w := wsConnect(t, lab.client)
		sub := w.watchUI("w1")
		waitFor(t, "the host's stream", func() bool { return lab.host.adb.LiveUIEventStreams() == 1 })
		lab.stopHost()
		if f := w.nonEmptyUI(sub); f.typ != "stopped" || f.reason != "deviceGone" {
			t.Errorf("frame %+v; want stopped deviceGone", f)
		}
		if f, ok := w.next(500 * time.Millisecond); ok {
			t.Errorf("a frame after stopped: %+v", f)
		}
	})

	for _, c := range lab.clientCalls(t) {
		if strings.Contains(c, phone1) {
			t.Errorf("the client's adb was asked %q for a remote device", c)
		}
	}
}

// uiStreamOf starts device.watchUiEvents on the phone as the raw peer carol and returns the
// subscription the host named, and the stopped events of the subscription as the host sent them.
func (l *leaseLab) carolUIStream(t *testing.T) (sub string, stopped chan stoppedSeen) {
	t.Helper()
	stopped = make(chan stoppedSeen, 4)
	route := func(raw json.RawMessage) {
		var e struct {
			Topic, Type string
			Payload     struct{ Reason, Cause string }
		}
		if json.Unmarshal(raw, &e) == nil && e.Topic == "device.uiEvents" && e.Type == "stopped" {
			stopped <- stoppedSeen{e.Payload.Reason, e.Payload.Cause}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, stop, err := l.carol.CallRouted(ctx, capability.RequestEnvelope{Capability: "device.watchUiEvents", Params: json.RawMessage(`{"device":"` + phone1 + `"}`)}, route)
	t.Cleanup(stop)
	var res device.WatchUIEventsResult
	if err != nil || resp.Error != nil || json.Unmarshal(resp.Result, &res) != nil || res.SubscriptionID == "" {
		t.Fatalf("carol's watchUiEvents: %v %+v %s", err, resp.Error, resp.Result)
	}
	return res.SubscriptionID, stopped
}

// TestLeaseEndStopsPeerUIEventsStream: a peer's UI events stream ends with its lease
// (ADR-0104 §4, ADR-0115 Decision 1), by the owner's release and by the phone no longer
// lent: the host's poller stops (its reader gets no further `events` request), the stream's
// subscription is told `stopped deviceGone` with the host's cause, and nothing of the stream
// stays on the host. Seen failing with uiStreams left out of endLease (the stream ran on:
// no stopped event, the reader polled).
func TestLeaseEndStopsPeerUIEventsStream(t *testing.T) {
	lab, r := newUILab(t)
	for _, how := range []string{"release", "un-lent"} {
		t.Run(how, func(t *testing.T) {
			lab.release(t)
			// Lent again whatever happens, so one subtest's failure leaves the next its phone.
			t.Cleanup(func() { lab.lendPhone1To(t, "anna", "bob", "carol") })
			_, stopped := lab.carolUIStream(t)
			waitFor(t, "the host's stream", func() bool { return lab.host.adb.LiveUIEventStreams() == 1 })
			if got := lab.holder(t); got != "carol" {
				t.Fatalf("%q holds the phone while carol's stream runs; want carol", got)
			}
			asked := len(r.EventsRequests())
			time.Sleep(600 * time.Millisecond)
			if len(r.EventsRequests()) == asked {
				t.Fatal("the host's poller is not reading: the check below would pass for nothing")
			}

			if how == "release" {
				lab.release(t)
			} else {
				lab.lendPhone1To(t, "anna", "bob")
				lab.host.adb.SweepLeases()
			}
			select {
			case e := <-stopped:
				if e.reason != "deviceGone" || e.cause != "the host ended the lease" {
					t.Errorf("the stream stopped %q (%q); want deviceGone, the host ended the lease", e.reason, e.cause)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("a stream whose lease ended was not stopped")
			}
			waitFor(t, "the host to keep no stream", func() bool { return lab.host.adb.LiveUIEventStreams() == 0 })
			asked = len(r.EventsRequests())
			time.Sleep(800 * time.Millisecond) // three of the poller's ticks
			if got := len(r.EventsRequests()); got != asked {
				t.Errorf("the host's reader got %d events requests after the lease ended; the poller outlived it", got-asked)
			}
			if got := lab.holder(t); got != "" {
				t.Errorf("%q holds the phone after the lease ended", got)
			}
		})
	}
}

// TestLeaseEndsWhileAUIEventsStreamStarts: a UI events stream still starting when its lease
// ends is in no table, so the end finds nothing to stop; once in its table it checks that its
// link still holds the phone and, if not, ends as `stopped deviceGone` with the host's cause,
// leaving the host no stream (ADR-0104 §4). Seen failing with the check removed (the stream
// ran on, unheld).
func TestLeaseEndsWhileAUIEventsStreamStarts(t *testing.T) {
	lab, r := newUILab(t)
	lab.release(t)
	entered, proceed := make(chan struct{}, 1), make(chan struct{})
	lab.host.adb.SetStartHook(func() { entered <- struct{}{}; <-proceed })
	t.Cleanup(func() { lab.host.adb.SetStartHook(nil) })
	stoppedCh := make(chan stoppedSeen, 4)
	route := func(raw json.RawMessage) {
		var e struct {
			Topic, Type string
			Payload     struct{ Reason, Cause string }
		}
		if json.Unmarshal(raw, &e) == nil && e.Topic == "device.uiEvents" && e.Type == "stopped" {
			stoppedCh <- stoppedSeen{e.Payload.Reason, e.Payload.Cause}
		}
	}
	answered := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		resp, stop, err := lab.carol.CallRouted(ctx, capability.RequestEnvelope{Capability: "device.watchUiEvents", Params: json.RawMessage(`{"device":"` + phone1 + `"}`)}, route)
		t.Cleanup(stop)
		if err == nil && resp.Error != nil {
			err = &capability.Error{Code: resp.Error.Code, Message: resp.Error.Message}
		}
		answered <- err
	}()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the stream never reached its start")
	}
	if got := lab.holder(t); got != "carol" {
		t.Fatalf("%q holds the phone while carol's stream starts; want carol", got)
	}
	lab.release(t)
	lab.host.adb.SetStartHook(nil)
	close(proceed)
	if err := <-answered; err != nil {
		t.Fatalf("the stream's start answered %v", err)
	}
	select {
	case e := <-stoppedCh:
		if e.reason != "deviceGone" || e.cause != "the host ended the lease" {
			t.Errorf("the stream stopped %q (%q); want deviceGone, the host ended the lease", e.reason, e.cause)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stream that started after its lease ended was not stopped")
	}
	waitFor(t, "the host to keep no stream", func() bool { return lab.host.adb.LiveUIEventStreams() == 0 })
	asked := len(r.EventsRequests())
	time.Sleep(800 * time.Millisecond)
	if got := len(r.EventsRequests()); got != asked {
		t.Errorf("the host's reader got %d events requests after the lease ended", got-asked)
	}
}

// TestPeerUIEventsStreamKeepsTheLease: a peer's open UI events stream is activity of its
// lease (ADR-0104 §3): carol watches and says nothing for 20 minutes (the registry's clock is
// moved; the idle time is 15) and bob's call is still refused, naming carol; once the stream
// ends the idle time starts then, and 16 minutes later bob's call takes the phone. Seen
// failing with the stream not counted (bob took the phone while carol's stream was open).
func TestPeerUIEventsStreamKeepsTheLease(t *testing.T) {
	lab, _ := newUILab(t)
	lab.release(t)
	sub, _ := lab.carolUIStream(t)
	waitFor(t, "the host's stream", func() bool { return lab.host.adb.LiveUIEventStreams() == 1 })
	if got := lab.holder(t); got != "carol" {
		t.Fatalf("%q holds the phone; want carol", got)
	}

	lab.clk.advance(20 * time.Minute)
	occupied(t, "bob beside carol's open stream, 20 minutes on", lab.onBob(t, "device.uiTree", `{`+dev(remotePhone)+`}`), "carol")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, _ := json.Marshal(event.UnsubscribeParams{SubscriptionID: sub})
	if resp, err := lab.carol.Call(ctx, capability.RequestEnvelope{Capability: event.CapUnsubscribe, Params: p}); err != nil || resp.Error != nil {
		t.Fatalf("carol's unsubscribe: %v %+v", err, resp.Error)
	}
	waitFor(t, "the host's stream to end", func() bool { return lab.host.adb.LiveUIEventStreams() == 0 })
	occupied(t, "bob right after carol's stream ended", lab.onBob(t, "device.uiTree", `{`+dev(remotePhone)+`}`), "carol")
	lab.clk.advance(16 * time.Minute)
	free(t, "bob 16 minutes after carol's stream ended", lab.onBob(t, "device.uninstall", touchParams))
	if got := lab.holder(t); got != "bob" {
		t.Errorf("%q holds the phone; want bob", got)
	}
}
