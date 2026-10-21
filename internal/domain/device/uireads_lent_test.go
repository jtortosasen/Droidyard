package device

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// The UI reads lent to a peer (plan m2x step 3; ADR-0115 Decision 1): the host side of a lent
// device.uiDump, device.uiEvents and device.watchUiEvents, and the checks of what a host sends
// back. The two daemons end to end are cmd/droidyard's TestRemoteUIReads.

// uiHost is a host with R58M001 lent to anna, whose UI reader is a fake that answers a full
// dump and has events scripted.
func uiHost(t *testing.T) (*hostFixture, *devicetest.FakeReader) {
	t.Helper()
	listing := uiList(phoneLine, usbLine("R58M001"), usbLine("R58M002"))
	f := newHostFixture(t, listing, []string{"R58M001", "R58M002"}, lendingConfig)
	r := devicetest.StartReader(t, pixelDoc(t))
	r.SetFullDocs(fullDocument)
	for name, content := range map[string]string{"R58M001.push.exit": "0", "R58M001.start.out": "", "R58M001.forward.out": r.Port} {
		devicetest.Write(t, f.dir, name, []byte(content))
	}
	return f, r
}

// peerWatch starts device.watchUiEvents on serial as the link session of peer who, over sess.
func (f *hostFixture) peerWatch(t *testing.T, who string, sess *event.Session, serial string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.watchUiEvents", Params: []byte(`{"device":"` + serial + `"}`),
		Client: capability.ClientPeer, Origin: capability.Origin{ID: "peer-" + who, Name: who}, Session: sess,
	})
	if err != nil {
		t.Fatalf("a peer's device.watchUiEvents: %v", err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Decision (ADR-0115 Decision 1: the three UI reads are lendable): a paired peer's call of
// each reaches the reader of the device lent to it and answers, the dump is an artifact its
// peer owns (and only that peer: the peer route serves it to anna and to nobody else), and a
// device lent to another peer is DEVICE_NOT_FOUND for all three, with no adb command run for
// it. Seen failing with each descriptor left without Lendable (REQUEST_NOT_ALLOWED_HERE at
// Start).
func TestUIReadsAreLendableToAPeer(t *testing.T) {
	f, r := uiHost(t)
	r.AddEvents(clickEvents(1, 3)...)
	bus := event.New(event.Options{})
	f.adb.SetBus(bus)

	res := mustDump(t, f.call(t, anna, "device.uiDump", `{"device":"R58M001"}`))
	if res.Device != "R58M001" || res.Nodes != 3 || res.Foreground != "com.example" {
		t.Errorf("a peer's uiDump: %+v; want the three nodes of the fake reader's document", res)
	}
	e, err := f.store.GetOwned(res.Dump.Ref, artifact.PeerOwner(anna))
	if err != nil || e.Ref.MediaType != "application/xml" || string(e.Data) != fullDocument {
		t.Errorf("anna's own artifact: %v %+v; want the reader's document as application/xml", err, e.Ref)
	}
	if _, err := f.store.GetOwned(res.Dump.Ref, artifact.PeerOwner(bob)); err == nil {
		t.Error("another peer read anna's dump")
	}

	ev := mustEvents(t, f.call(t, anna, "device.uiEvents", `{"device":"R58M001"}`))
	if ev.Cursor == "" || len(ev.Events) != 3 {
		t.Errorf("a peer's uiEvents: %+v; want the cursor and the three events", ev)
	}

	sess := event.NewSession(bus, "peer-anna")
	t.Cleanup(func() {
		sess.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		f.adb.uiStreams.StopAll(ctx)
	})
	if out := f.peerWatch(t, anna, sess, "R58M001"); out.State != capability.StateCompleted {
		t.Fatalf("a peer's watchUiEvents: %s %+v", out.State, out.Error)
	}
	if n := f.adb.LiveUIEventStreams(); n != 1 {
		t.Errorf("%d streams after a peer's watch; want 1", n)
	}

	onPhone := func() (n int) {
		for _, c := range devicetest.Calls(t, f.dir) {
			if strings.HasPrefix(c, "-s R58M001 ") {
				n++
			}
		}
		return n
	}
	before := onPhone()
	for _, c := range []struct{ name, params string }{
		{"device.uiDump", `{"device":"R58M001"}`}, {"device.uiEvents", `{"device":"R58M001"}`},
	} {
		mustFail(t, f.call(t, bob, c.name, c.params), ErrNotFound)
	}
	mustFail(t, f.peerWatch(t, bob, event.NewSession(bus, "peer-bob"), "R58M001"), ErrNotFound)
	if after := onPhone(); after != before {
		t.Errorf("adb ran %d commands on the phone for calls of a peer it is not lent to", after-before)
	}
}

// Decision (ADR-0115 Decision 1, ADR-0106: at most peerStreamsMax streams of a device per link
// session, the next replacing the oldest): a peer's UI events streams are capped, not replaced
// by the session rule that holds for a local session. add returns the oldest of the link
// session and device once the cap is reached, ignores other devices and other link sessions,
// and a local session's second stream replaces its first. Seen failing with the peer flag not
// consulted (the second stream of a link session replaced the first).
func TestPeerUIStreamsAreCappedNotReplaced(t *testing.T) {
	var st uiEventStreams
	key := streamKey{session: "peer-link", serial: "R58M001"}
	var held []*uiEventStream
	for i := range peerStreamsMax {
		s := &uiEventStream{key: key, peer: true}
		if old, err := st.add(s); err != nil || old != nil {
			t.Fatalf("stream %d: replaced %v, %v; want room for %d", i+1, old != nil, err, peerStreamsMax)
		}
		held = append(held, s)
	}
	if old, _ := st.add(&uiEventStream{key: streamKey{session: "peer-link", serial: "R58M002"}, peer: true}); old != nil {
		t.Errorf("a stream of another device replaced %p", old)
	}
	if old, _ := st.add(&uiEventStream{key: streamKey{session: "peer-other", serial: "R58M001"}, peer: true}); old != nil {
		t.Errorf("a stream of another link session replaced %p", old)
	}
	fifth := &uiEventStream{key: key, peer: true}
	old, _ := st.add(fifth)
	if old != held[0] {
		t.Fatalf("the fifth stream replaced %p; want the oldest, %p", old, held[0])
	}
	st.remove(old)
	if sixth, _ := st.add(&uiEventStream{key: key, peer: true}); sixth != held[1] {
		t.Errorf("the sixth stream replaced %p; want the next oldest, %p", sixth, held[1])
	}

	local := streamKey{session: "ws-1", serial: "emulator-5554"}
	a, b := &uiEventStream{key: local}, &uiEventStream{key: local}
	if old, _ := st.add(a); old != nil {
		t.Fatal("the first local stream replaced something")
	}
	if old, _ := st.add(b); old != a {
		t.Errorf("a local session's second stream replaced %p; want its first, %p", old, a)
	}
}

const hostDumpRef = artifact.RefPrefix + "e.d-0a1b2c3d.8.0123456789ab"

// Decision (ADR-0115 Decision 1, ADR-0100 §3): a relayed device.uiDump is the bytes of the
// host's document stored in this daemon's store under its own reference, untrusted, from
// device.uiDump; the counts and the foreground come from the bytes fetched, never from the
// host's answer; the result names the device by its id and never carries the host's
// reference; the fetch is bounded by the local read's cap. Seen failing with the host's
// counts passed through (nodes 999, foreground evil.pkg) and with the host's reference
// returned.
func TestRelayedUIDumpIsStoredLocally(t *testing.T) {
	store := testStore(t)
	f := &fakeHostArtifacts{a: hosting.Artifact{MediaType: "application/xml", Untrusted: true, Data: []byte(fullDocument)}}
	host := UIDumpResult{
		Device: "R58M001", Dump: artifact.Ref{Ref: hostDumpRef, MediaType: "application/xml", Bytes: 7},
		Nodes: 999, Foreground: "evil.pkg", Warnings: []string{"R58M001 is slow"},
	}
	out, err := (&ADB{}).uiDumpFrom(context.Background(), f, qaPhone, store, host)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(UIDumpResult)
	if res.Dump.Ref == hostDumpRef || !strings.HasPrefix(res.Dump.Ref, artifact.RefPrefix) {
		t.Errorf("the result carries %q; want this daemon's own reference, not the host's %q", res.Dump.Ref, hostDumpRef)
	}
	if res.Device != "qa/R58M001" || res.Nodes != 3 || res.Foreground != "com.example" {
		t.Errorf("result %+v; want the id and the counts of the bytes fetched (3 nodes, com.example)", res)
	}
	if res.Dump.MediaType != "application/xml" || !res.Dump.Untrusted || res.Dump.Bytes != len(fullDocument) {
		t.Errorf("the reference record %+v; want untrusted application/xml of the %d bytes fetched (not the host's claim)", res.Dump, len(fullDocument))
	}
	e, err := store.Get(res.Dump.Ref)
	if err != nil || string(e.Data) != fullDocument || e.Producer != "device.uiDump" {
		t.Errorf("the stored artifact: %v, producer %q; want the host's bytes from device.uiDump", err, e.Producer)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "qa/R58M001 is slow" {
		t.Errorf("warnings %q; want the id in place of the serial", res.Warnings)
	}
	if len(f.ids) != 1 || f.ids[0] != "e.d-0a1b2c3d.8.0123456789ab" || f.bounds[0] != dumpOutputCap {
		t.Errorf("fetched %v with bounds %v; want the host's id once, bounded by the local dump cap %d", f.ids, f.bounds, dumpOutputCap)
	}
}

// Decision (ADR-0115 Decision 1; ADR-0015 §1): what the host serves as a dump is stored only
// after it parses as a hierarchy, and a refusal says so in the daemon's fixed words: it quotes
// nothing of the document, which is the host's. A document that is no hierarchy, a cut one,
// and an empty one are DEVICE_UI_DUMP_FAILED; another media type and a reference that is not
// an artifact's are incompatible, the latter fetching nothing. Nothing is stored by any. Seen
// failing with the parse skipped (the garbage was stored).
func TestRelayedUIDumpRefusesWhatIsNotADump(t *testing.T) {
	const secret = "secret-from-the-host"
	for name, c := range map[string]struct {
		ref, media string
		data       string
		code       capability.ErrorCode
		state      string
		fetched    int
	}{
		"text":                {hostDumpRef, "application/xml", secret, ErrUIDumpFailed, "", 1},
		"a cut document":      {hostDumpRef, "application/xml", fullDocument[:200] + secret, ErrUIDumpFailed, "", 1},
		"an empty document":   {hostDumpRef, "application/xml", "", ErrUIDumpFailed, "", 1},
		"another media type":  {hostDumpRef, "text/plain", fullDocument, ErrNotReady, "incompatible", 1},
		"a durable reference": {artifact.RefPrefix + "d.0123456789abcdef.1", "application/xml", fullDocument, ErrNotReady, "incompatible", 0},
		"no reference":        {"", "application/xml", fullDocument, ErrNotReady, "incompatible", 0},
	} {
		store := testStore(t)
		f := &fakeHostArtifacts{a: hosting.Artifact{MediaType: c.media, Data: []byte(c.data)}}
		_, err := (&ADB{}).uiDumpFrom(context.Background(), f, qaPhone, store, UIDumpResult{Dump: artifact.Ref{Ref: c.ref}})
		code, details := codeOf(t, err)
		if code != c.code || (c.state != "" && details["state"] != c.state) {
			t.Errorf("%s: %s %v; want %s %s", name, code, details, c.code, c.state)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error quotes the document: %v", name, err)
		}
		if ce := capability.AsError(err); c.code == ErrUIDumpFailed && ce.Cause != uiDumpUnreadable {
			t.Errorf("%s: cause %q; want the fixed %q", name, ce.Cause, uiDumpUnreadable)
		}
		if len(f.ids) != c.fetched || store.Len() != 0 {
			t.Errorf("%s: %d fetches, %d artifacts stored; want %d and none", name, len(f.ids), store.Len(), c.fetched)
		}
	}
}

// Decision (ADR-0115 Decision 1; ADR-0111 Decision 5: the cursor is opaque): a relayed
// device.uiEvents passes the host's cursor through unchanged, names the device by its id, and
// bounds the rest as the reader bounds it: package and class at 128 characters, a toast's text
// at 512, text on no other event, at most 200 events, types and gap reasons from the published
// lists. An answer the contract cannot hold is incompatible, not forwarded. Seen failing with
// the answer passed through as it came (the long names, the text on a click and the unknown
// type reached the caller).
func TestRelayedUIEventsAreBounded(t *testing.T) {
	long, longText := strings.Repeat("é", 300), strings.Repeat("é", 600)
	good := UIEventsResult{
		Device: "R58M001", Cursor: fakeInstance + ".7", More: true, Outcome: outcomeChanged,
		Gap:      &UIEventsGap{Reason: gapOverflow, Dropped: -4},
		Warnings: []string{"R58M001 is slow"},
		Events: []UIEvent{
			{Time: "2026-10-21T10:00:00.000Z", Type: "toast", Package: long, Class: long, Text: longText},
			{Time: "2026-10-21T10:00:01.000Z", Type: "click", Text: "typed text", Count: -1},
		},
	}
	out, err := uiEventsRelayed(qaPhone, good)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(UIEventsResult)
	if res.Device != "qa/R58M001" || res.Cursor != good.Cursor || !res.More || res.Outcome != outcomeChanged {
		t.Errorf("result %+v; want the id, the host's cursor unchanged, more and its outcome", res)
	}
	if n := len([]rune(res.Events[0].Package)); n != eventsNameCap || len([]rune(res.Events[0].Class)) != eventsNameCap || len([]rune(res.Events[0].Text)) != eventsTextCap {
		t.Errorf("a toast's package, class and text have %d, %d and %d characters; want %d, %d and %d", n, len([]rune(res.Events[0].Class)), len([]rune(res.Events[0].Text)), eventsNameCap, eventsNameCap, eventsTextCap)
	}
	if res.Events[1].Text != "" || res.Events[1].Count != 0 {
		t.Errorf("a click %+v; want no text and no negative count", res.Events[1])
	}
	if res.Gap == nil || res.Gap.Dropped != 0 || len(res.Warnings) != 1 || res.Warnings[0] != "qa/R58M001 is slow" {
		t.Errorf("gap %+v warnings %q; want the gap with no negative count and the id in the warning", res.Gap, res.Warnings)
	}

	many := make([]UIEvent, eventsMaxLimit+1)
	for i := range many {
		many[i] = UIEvent{Time: "2026-10-21T10:00:00.000Z", Type: "click"}
	}
	for name, mutate := range map[string]func(r *UIEventsResult){
		"a cursor that is no cursor": func(r *UIEventsResult) { r.Cursor = "../../x" },
		"an empty cursor":            func(r *UIEventsResult) { r.Cursor = "" },
		"an unknown type":            func(r *UIEventsResult) { r.Events[0].Type = "keylog" },
		"a time that is no time":     func(r *UIEventsResult) { r.Events[0].Time = "yesterday" },
		"too many events":            func(r *UIEventsResult) { r.Events = many },
		"an unknown gap":             func(r *UIEventsResult) { r.Gap = &UIEventsGap{Reason: "lost"} },
		"an unknown outcome":         func(r *UIEventsResult) { r.Outcome = "done" },
	} {
		bad := good
		bad.Events = append([]UIEvent(nil), good.Events...)
		mutate(&bad)
		_, err := uiEventsRelayed(qaPhone, bad)
		if code, details := codeOf(t, err); code != ErrNotReady || details["state"] != "incompatible" {
			t.Errorf("%s: %s %v; want incompatible", name, code, details)
		}
	}
}

// relayedUIFixture is a relayed stream with no host: its onEvent is fed the frames a host
// would send, and the caller's subscription is read.
func relayedUIFixture(t *testing.T) (*relayedUI, *event.Subscription) {
	t.Helper()
	bus := event.New(event.Options{})
	sess := event.NewSession(bus, "s1")
	t.Cleanup(sess.Close)
	id, sub, err := sess.Open([]string{TopicUIEvents}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	_ = id
	s := &relayedUI{adb: &ADB{}, rt: qaPhone, bus: bus, sub: sub, operation: "op1",
		key: streamKey{session: "s1", serial: qaPhone.id}, ready: make(chan struct{}), quit: make(chan struct{})}
	close(s.ready)
	return s, sub
}

func hostFrame(t *testing.T, typ string, payload any) json.RawMessage {
	t.Helper()
	p, _ := json.Marshal(payload)
	raw, _ := json.Marshal(map[string]any{"topic": TopicUIEvents, "type": typ, "payload": json.RawMessage(p)})
	return raw
}

// Decision (ADR-0115 Decision 1, ADR-0100 §3, ADR-0042): a relayed stream republishes the
// host's events with the remote id, bounded as a relayed read is, always marked untrusted; the
// host's `stopped` is mapped to this daemon's words (deviceGone and replaced keep their
// reason, anything else is failed) and its cause is never repeated; a batch the contract
// cannot hold ends the stream as failed instead of being forwarded; after a stopped nothing
// more reaches the subscription. Seen failing with the host's payload republished as it came.
func TestRelayedUIStreamRepublishesWhatTheContractHolds(t *testing.T) {
	long := strings.Repeat("a", 300)
	s, sub := relayedUIFixture(t)
	s.onEvent(hostFrame(t, EventUIEvents, UIEventsPush{
		Device: "R58M001", Untrusted: false,
		Events: []UIEvent{{Time: "2026-10-21T10:00:00.000Z", Type: "click", Package: long, Text: "typed"}},
		Gap:    &UIEventsGap{Reason: gapRestarted},
	}))
	e := next(t, sub)
	var p UIEventsPush
	if e.Type != EventUIEvents || json.Unmarshal(e.Payload, &p) != nil {
		t.Fatalf("event %s %s; want events", e.Type, e.Payload)
	}
	if p.Device != "qa/R58M001" || !p.Untrusted || len(p.Events) != 1 || len(p.Events[0].Package) != eventsNameCap || p.Events[0].Text != "" || p.Gap == nil || p.Gap.Reason != gapRestarted {
		t.Errorf("payload %+v; want the remote id, untrusted, the name cut at %d, no text on a click and the gap", p, eventsNameCap)
	}

	s.onEvent(hostFrame(t, EventUIEvents, UIEventsPush{Events: []UIEvent{{Time: "2026-10-21T10:00:00.000Z", Type: "keylog"}}}))
	var st UIEventsStopped
	e = next(t, sub)
	if e.Type != EventStopped || json.Unmarshal(e.Payload, &st) != nil || st.Reason != StopFailed || st.Device != "qa/R58M001" || strings.Contains(st.Cause, "keylog") {
		t.Fatalf("event %s %s; want stopped failed in this daemon's words", e.Type, e.Payload)
	}
	s.onEvent(hostFrame(t, EventUIEvents, UIEventsPush{Events: []UIEvent{{Time: "2026-10-21T10:00:00.000Z", Type: "click"}}}))
	s.onEvent(hostFrame(t, EventStopped, UIEventsStopped{Reason: StopDeviceGone}))
	quiet(t, sub, 100*time.Millisecond)
}

// Decision (ADR-0112 Decision 1, ADR-0100 §3): the host's stopped reasons reach the caller as
// this daemon's own words. Seen failing with the host's cause forwarded.
func TestRelayedUIStreamMapsTheHostsStop(t *testing.T) {
	for host, want := range map[string]string{StopDeviceGone: StopDeviceGone, StopReplaced: StopReplaced, StopFailed: StopFailed, "weird": StopFailed} {
		s, sub := relayedUIFixture(t)
		s.onEvent(hostFrame(t, EventStopped, UIEventsStopped{Device: "R58M001", Reason: host, Cause: "secret-from-the-host"}))
		var st UIEventsStopped
		e := next(t, sub)
		if e.Type != EventStopped || json.Unmarshal(e.Payload, &st) != nil || st.Reason != want || st.Device != "qa/R58M001" || strings.Contains(st.Cause, "secret") {
			t.Errorf("host reason %q: event %s %s; want stopped %s for qa/R58M001 without the host's cause", host, e.Type, e.Payload, want)
		}
	}
}
