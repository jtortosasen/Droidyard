package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// device.watchUiEvents end to end (ADR-0112; plan m2w step 7): a WebSocket client, the shipped
// wiring, the fake adb and a fake persistent reader with a scripted ring of events.

const uwSerial = "emulator-5554"

// uwDaemon is the shipped wiring over the fake adb and a fake reader. dir is the scenario, so
// a test can take the device away or make the reader unstartable.
func uwDaemon(t *testing.T) (p *inProcess, r *devicetest.FakeReader, dir string) {
	t.Helper()
	r = devicetest.StartReader(t)
	dir = t.TempDir()
	for name, content := range map[string]string{
		"devices.out":                   "List of devices attached\n" + uwSerial + " device product:sdk_gphone16k_arm64 transport_id:1\n",
		uwSerial + ".bootcompleted.out": "1\n",
		uwSerial + ".push.exit":         "0",
		uwSerial + ".start.out":         "",
		uwSerial + ".forward.out":       r.Port,
	} {
		devicetest.Write(t, dir, name, []byte(content))
	}
	p = startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{env: devicetest.Env(t, dir, unusedPort(t)), exec: devicetest.Exec(dir)})
	return p, r, dir
}

// uwClick is a click event with its own window id, the event's identity in these tests.
func uwClick(window int) devicetest.FakeEvent {
	return devicetest.FakeEvent{TimeMs: time.Now().UnixMilli(), Type: "click", Window: window, Package: "com.example", Class: "android.widget.Button"}
}

// uwWatch sends device.watchUiEvents as id and returns the subscription it opened and the
// events that reached the client before the response, which the poller may publish first.
func (w *wsClient) uwWatch(id string) (string, []wsFrame) {
	w.t.Helper()
	w.send(id, "device.watchUiEvents", "", `{"device":"`+uwSerial+`"}`, nil)
	events, f := w.collect(id)
	if f.Error != nil {
		w.t.Fatalf("device.watchUiEvents: %+v", f.Error)
	}
	return subscriptionOf(w.t, f), events
}

// uwFrame is one device.uiEvents event of a subscription.
type uwFrame struct {
	Type    string
	Windows []int
	Gap     string
	Reason  string
	Cause   string
}

// uwNext reads the next frame, which must be an event of topic device.uiEvents on sub.
func (w *wsClient) uwNext(sub string) uwFrame {
	w.t.Helper()
	return w.uwDecode(sub, w.must("a device.uiEvents event"))
}

func (w *wsClient) uwDecode(sub string, f wsFrame) uwFrame {
	w.t.Helper()
	if f.Event == nil || f.SubscriptionID != sub || f.Event.Topic != "device.uiEvents" {
		w.t.Fatalf("a frame that is not a device.uiEvents event of %s: %+v", sub, f)
	}
	var p struct {
		Device string `json:"device"`
		Events []struct {
			Window *int `json:"window"`
		} `json:"events"`
		Gap *struct {
			Reason string `json:"reason"`
		} `json:"gap"`
		Untrusted bool   `json:"untrusted"`
		Reason    string `json:"reason"`
		Cause     string `json:"cause"`
	}
	if err := json.Unmarshal(f.Event.Payload, &p); err != nil || p.Device != uwSerial {
		w.t.Fatalf("payload %s: %v", f.Event.Payload, err)
	}
	out := uwFrame{Type: f.Event.Type, Reason: p.Reason, Cause: p.Cause}
	if f.Event.Type == "events" && !p.Untrusted {
		w.t.Errorf("an events event that is not marked untrusted: %s", f.Event.Payload)
	}
	for _, e := range p.Events {
		out.Windows = append(out.Windows, *e.Window)
	}
	if p.Gap != nil {
		out.Gap = p.Gap.Reason
	}
	return out
}

// TestWatchUiEventsEndToEnd: over a real WebSocket a session gets the events the reader's ring
// gains, in order and once, from the moment of the call; a reader that restarted behind it is
// a readerRestarted gap with the new reader's events; a second call of the same session
// replaces the first (its subscription gets stopped replaced); after events.unsubscribe the
// reader gets no further `events` request (no poller outlives its subscription); and a device
// that goes is stopped deviceGone. Seen failing with the end hook not stopping the poller (the
// reader kept getting requests), and with the instance check removed (the gap never came).
func TestWatchUiEventsEndToEnd(t *testing.T) {
	t.Parallel()
	p, r, dir := uwDaemon(t)
	w := wsConnect(t, p)

	r.AddEvents(uwClick(1)) // before the call: history, not the stream's
	first, early := w.uwWatch("w1")
	var gap uwFrame
	if len(early) > 0 {
		gap = w.uwDecode(first, early[0])
	} else {
		gap = w.uwNext(first)
	}
	if gap.Type != "events" || gap.Gap != "readerStarted" || len(gap.Windows) != 0 {
		t.Fatalf("the first frame %+v, want an empty events event with the readerStarted gap", gap)
	}
	r.AddEvents(uwClick(10), uwClick(11))
	if f := w.uwNext(first); f.Type != "events" || !slices.Equal(f.Windows, []int{10, 11}) || f.Gap != "" {
		t.Fatalf("frame %+v, want the two events added after the call, no gap", f)
	}
	r.RestartEvents("fedcba9876543210")
	r.AddEvents(uwClick(20))
	if f := w.uwNext(first); f.Type != "events" || f.Gap != "readerRestarted" || !slices.Equal(f.Windows, []int{20}) {
		t.Fatalf("frame %+v, want the new reader's event with a readerRestarted gap", f)
	}

	// A second call of the same session replaces the first.
	second, early := w.uwWatch("w2")
	if second == first {
		t.Fatal("the two streams share a subscription")
	}
	stopped := false
	for _, f := range early {
		if f.SubscriptionID == first {
			stopped = w.uwDecode(first, f).Reason == "replaced"
		}
	}
	if !stopped {
		if f := w.uwNext(first); f.Type != "stopped" || f.Reason != "replaced" {
			t.Fatalf("frame %+v, want stopped replaced on the first subscription", f)
		}
	}
	waitFor(t, "the replaced stream to leave the set", func() bool { return p.adb.LiveUIEventStreams() == 1 })
	r.AddEvents(uwClick(30))
	if f := w.uwNext(second); f.Type != "events" || !slices.Equal(f.Windows, []int{30}) {
		t.Fatalf("frame %+v, want the second stream's event", f)
	}

	// events.unsubscribe ends the stream, and the reader is not asked again.
	w.send("u1", "events.unsubscribe", "", `{"subscriptionId":"`+second+`"}`, nil)
	if _, f := w.collect("u1"); f.Error != nil {
		t.Fatalf("events.unsubscribe: %+v", f.Error)
	}
	waitFor(t, "the stream to leave the set", func() bool { return p.adb.LiveUIEventStreams() == 0 })
	asked := len(r.EventsRequests())
	time.Sleep(900 * time.Millisecond) // more than three polls
	if n := len(r.EventsRequests()); n != asked {
		t.Fatalf("the reader got %d `events` requests after events.unsubscribe", n-asked)
	}

	// The device goes: it is not listed, and its reader cannot be read or started.
	third, early := w.uwWatch("w3")
	if len(early) > 0 {
		t.Fatalf("frames before the third response: %+v", early)
	}
	devicetest.Write(t, dir, "devices.out", []byte("List of devices attached\n"))
	devicetest.Write(t, dir, uwSerial+".start.exit", []byte("1"))
	r.Close()
	if f := w.uwNext(third); f.Type != "stopped" || f.Reason != "deviceGone" || f.Cause == "" {
		t.Fatalf("frame %+v, want stopped deviceGone with a cause", f)
	}
	waitFor(t, "the stream to leave the set", func() bool { return p.adb.LiveUIEventStreams() == 0 })
}

// TestWatchUiEventsStopsFailedWhenTheReaderCannotStart: a reader that is gone behind a live
// forward, whose start fails, gets one start attempt; the stream publishes stopped failed with
// a cause and the daemon runs no adb command after it (the poller never retries a push and
// start on every tick). Seen failing with the poller carrying on after the failure (the start
// attempts and the commands kept coming).
func TestWatchUiEventsStopsFailedWhenTheReaderCannotStart(t *testing.T) {
	t.Parallel()
	p, r, dir := uwDaemon(t)
	w := wsConnect(t, p)
	sub, early := w.uwWatch("w1")
	if len(early) == 0 {
		w.uwNext(sub) // the readerStarted gap
	}
	starts := func() int {
		n := 0
		for _, c := range devicetest.Calls(t, dir) {
			if strings.Contains(c, "DroidyardReader serve ") {
				n++
			}
		}
		return n
	}
	before := starts()
	devicetest.Write(t, dir, uwSerial+".start.exit", []byte("1"))
	r.Close()

	f := w.uwNext(sub)
	if f.Type != "stopped" || f.Reason != "failed" || f.Cause == "" {
		t.Fatalf("frame %+v, want stopped failed with a cause", f)
	}
	waitFor(t, "the stream to leave the set", func() bool { return p.adb.LiveUIEventStreams() == 0 })
	if n := starts() - before; n != 1 {
		t.Errorf("%d start attempts, want one", n)
	}
	commands := len(devicetest.Calls(t, dir))
	time.Sleep(900 * time.Millisecond)
	if n := len(devicetest.Calls(t, dir)); n != commands {
		t.Errorf("%d adb commands after the stream stopped", n-commands)
	}
}

// TestWatchUiEventsNeedsASession: a POST /v1/call, which has no session to carry events, is
// refused with SESSION_REQUIRED, and the streaming capability is not an MCP tool (ADR-0010:
// the tool list a shim serves has no device_watchUiEvents, while device_uiEvents is there).
func TestWatchUiEventsNeedsASession(t *testing.T) {
	t.Parallel()
	p, _, _ := uwDaemon(t)
	resp := callRequest(portOf(t, p.d.HTTPAddr()), p.token,
		`{"id":"r","capability":"device.watchUiEvents","params":{"device":"`+uwSerial+`"}}`).send(t, p.d.HTTPAddr())
	env := envelopeOf(t, resp)
	if env.Error == nil || env.Error.Code != capability.ErrSessionRequired {
		t.Fatalf("POST /v1/call device.watchUiEvents: %d, error %+v; want SESSION_REQUIRED", resp.StatusCode, env.Error)
	}

	c := startShim(t, p.state, "all")
	c.initialize("all")
	names, _ := toolNames(t, c, 1)
	if slices.Contains(names, "device_watchUiEvents") || !slices.Contains(names, "device_uiEvents") {
		t.Errorf("the MCP tool list has device_watchUiEvents or lacks device_uiEvents: %v", names)
	}
	c.exit()
}
