package device

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// device.uiRecordings, device.watchUiRecordings and device.deleteUiRecording (ADR-0113 §5, §6;
// plan m2x step 6), over the recorder of uirecord_test.go.

// list calls device.uiRecordings as the UI.
func (f *recFix) list(t *testing.T, params string) []UIRecording {
	t.Helper()
	out := f.call(t, "device.uiRecordings", params, capability.ClientUI, capability.Origin{})
	if out.State != capability.StateCompleted {
		t.Fatalf("device.uiRecordings %s with error %+v", out.State, out.Error)
	}
	res, ok := out.Result.(UIRecordingsResult)
	if !ok {
		t.Fatalf("result is %T, want UIRecordingsResult", out.Result)
	}
	return res.Recordings
}

// watcher is a WebSocket session that called device.watchUiRecordings.
type watcher struct {
	sess *event.Session
	sub  *event.Subscription
	snap WatchUIRecordingsResult
}

func (f *recFix) watch(t *testing.T, sessionID string) *watcher {
	t.Helper()
	sess := event.NewSession(f.bus, sessionID)
	t.Cleanup(sess.Close)
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.watchUiRecordings", Client: capability.ClientUI, Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	out := f.wait(t, id)
	if out.State != capability.StateCompleted {
		t.Fatalf("device.watchUiRecordings %s with error %+v", out.State, out.Error)
	}
	res := out.Result.(WatchUIRecordingsResult)
	sub, ok := sess.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatalf("no subscription %s", res.SubscriptionID)
	}
	return &watcher{sess: sess, sub: sub, snap: res}
}

// next is the watcher's next event, which must be on the recordings' topic.
func (w *watcher) next(t *testing.T) event.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testtime.Bound(10*time.Second))
	defer cancel()
	e, err := w.sub.Next(ctx)
	if err != nil {
		t.Fatalf("no event: %v", err)
	}
	if e.Topic != TopicUIRecording {
		t.Fatalf("event on topic %q, want %q", e.Topic, TopicUIRecording)
	}
	return e
}

// nextState is the next state event of recording id, skipping the others of it, and the raw
// payload it came in.
func (w *watcher) nextState(t *testing.T, id string, until func(UIRecording) bool) (UIRecording, []byte) {
	t.Helper()
	for {
		e := w.next(t)
		if e.Type != EventState {
			continue
		}
		var p UIRecordingStatePush
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Recording.ID == id && until(p.Recording) {
			return p.Recording, e.Payload
		}
	}
}

func anyState(UIRecording) bool { return true }

// Decision (ADR-0113 §6: a running recording is listed with live counts and its growing
// ref; newest first; device and id filter; an unknown id is an empty list): the running
// recording's dumps and timeline size are the recorder's own, not the record on disk, which
// holds no counts until the recording ends; after the stop the same recording reads ended
// with the file's size; a second one is listed before the first. Seen failing with the list
// built from the records alone (the running recording listed 0 dumps and 0 bytes).
func TestUIRecordingsListsTheRunningWithLiveCounts(t *testing.T) {
	f := newRecFix(t, recDoc("A"), recDoc("B"))
	if got := f.list(t, `{}`); got == nil || len(got) != 0 {
		t.Fatalf("nothing kept: list = %#v, want an empty list that marshals to []", got)
	}
	first := f.start(t, recParams)
	f.event(1)
	live := f.until(t, "a second dump", func(r UIRecording) bool { return r.Dumps == 2 })
	got := f.list(t, `{}`)
	if len(got) != 1 || got[0].ID != first.ID || got[0].State != RecordingRunning {
		t.Fatalf("list while recording = %+v, want the running recording", got)
	}
	if got[0].Dumps < 2 || got[0].Events < 1 {
		t.Errorf("the running recording lists dumps=%d events=%d, want its live counts (dumps >= 2, events >= 1)", got[0].Dumps, got[0].Events)
	}
	if got[0].Bytes < live.Bytes || got[0].Timeline.Bytes != int(got[0].Bytes) || got[0].Bytes == 0 {
		t.Errorf("the running recording's growing ref says %d B and bytes %d; want the timeline's size so far (>= %d)", got[0].Timeline.Bytes, got[0].Bytes, live.Bytes)
	}
	f.stop(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	second := f.start(t, recParams)
	got = f.list(t, `{}`)
	if len(got) != 2 || got[0].ID != second.ID || got[1].ID != first.ID {
		t.Fatalf("list = %v, want newest first: %s then %s", ids(got), second.ID, first.ID)
	}
	if got[1].State != RecordingEnded || got[1].EndReason != EndStopped {
		t.Errorf("the stopped recording lists %q %q, want ended stopped", got[1].State, got[1].EndReason)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "recordings", first.ID, "timeline.jsonl"))
	if err != nil || got[1].Bytes != int64(len(data)) {
		t.Errorf("the ended recording lists %d B, the file holds %d (%v)", got[1].Bytes, len(data), err)
	}
	if one := f.list(t, `{"id":"`+first.ID+`"}`); len(one) != 1 || one[0].ID != first.ID {
		t.Errorf("id filter = %v, want %s alone", ids(one), first.ID)
	}
	if none := f.list(t, `{"id":"20200101T000000Z-00000000"}`); none == nil || len(none) != 0 {
		t.Errorf("an unknown id = %#v, want an empty list", none)
	}
	if none := f.list(t, `{"device":"emulator-9999"}`); len(none) != 0 {
		t.Errorf("another device = %v, want none", ids(none))
	}
	if all := f.list(t, `{"device":"`+uiSerial+`"}`); len(all) != 2 {
		t.Errorf("device filter = %v, want both", ids(all))
	}
}

func ids(list []UIRecording) []string {
	out := make([]string, len(list))
	for i, r := range list {
		out[i] = r.ID
	}
	return out
}

// Decision (ADR-0113 §6: the watch is a broadcast to every watching session, whoever started
// the recording): an MCP session's start reaches a WebSocket watcher that never called
// anything on the recording; while it runs the watcher hears it again, and the end arrives
// only once the record is finalised and the timeline closed (the event's ref size is the
// file's final size and the record on disk says ended). Seen failing with the start sent by
// Bus.PublishTo to a nil subscription (the watcher heard nothing), and with the end published
// before the record was finalised (the record on disk still read recording).
func TestWatchUIRecordingsSeesAnotherSessionsStart(t *testing.T) {
	f := newRecFix(t)
	old := recordPublishEvery
	recordPublishEvery = 30 * time.Millisecond
	t.Cleanup(func() { recordPublishEvery = old })
	f.ops.Sessions().Connect("mcp-1", "chat-1")
	w := f.watch(t, "ws-1")
	if len(w.snap.Recordings) != 0 {
		t.Fatalf("snapshot = %v, want none", ids(w.snap.Recordings))
	}
	rec := mustRecording(t, f.call(t, "device.recordUi", recParams, capability.ClientMCP, capability.Origin{ID: "mcp-1", Name: "agent"}))
	first, _ := w.nextState(t, rec.ID, anyState)
	if first.State != RecordingRunning || first.StartedBy != (PublishedStarter{Client: "mcp", Session: "mcp-1"}) {
		t.Errorf("start event = %+v, want the running recording started by mcp-1", first)
	}
	// Still running: told again, not only at the start.
	w.nextState(t, rec.ID, func(r UIRecording) bool { return r.State == RecordingRunning })
	// The stop call waits for the recording's end, so it is not waited for here: the record
	// on disk is read the moment the end event arrives.
	stopID, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.stopUiRecording", Params: []byte(recParams), Client: capability.ClientMCP, Origin: capability.Origin{ID: "mcp-1"}})
	if err != nil {
		t.Fatal(err)
	}
	end, _ := w.nextState(t, rec.ID, func(r UIRecording) bool { return r.State == RecordingEnded })
	got := f.endedRecording(t, rec.ID)
	if got.State != RecordingEnded || got.SHA256 == "" {
		t.Errorf("the end event came while the record on disk read %q (sha %q)", got.State, got.SHA256)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "recordings", rec.ID, "timeline.jsonl"))
	if err != nil || end.Timeline.Bytes != len(data) || end.EndReason != EndStopped || end.SHA256 != got.SHA256 {
		t.Errorf("end event = %d B, %q, sha %q; the file holds %d B (%v) with sha %q", end.Timeline.Bytes, end.EndReason, end.SHA256, len(data), err, got.SHA256)
	}
	// A watcher that joins later has it in the snapshot.
	if res := mustStop(t, f.wait(t, stopID)); !res.Stopped {
		t.Errorf("stop = %+v, want stopped", res)
	}
	late := f.watch(t, "ws-2")
	if len(late.snap.Recordings) != 1 || late.snap.Recordings[0].ID != rec.ID || late.snap.Recordings[0].State != RecordingEnded {
		t.Errorf("late snapshot = %+v, want the ended recording", late.snap.Recordings)
	}
}

// Decision (ADR-0113 §6: `deleted {id}`; the retention deletes too): a recording the sweep at
// another's end removes is announced to the watchers. Seen failing with the sweep's result
// dropped (no deleted event, only the state of the new one).
func TestRetentionSweepIsAnnounced(t *testing.T) {
	f := newRecFix(t)
	f.rec.MaxRecordings = 1
	first := f.start(t, recParams)
	f.stop(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	w := f.watch(t, "ws-1")
	if len(w.snap.Recordings) != 1 || w.snap.Recordings[0].ID != first.ID {
		t.Fatalf("snapshot = %v, want the first recording", ids(w.snap.Recordings))
	}
	second := f.start(t, recParams)
	f.stop(t)
	for {
		e := w.next(t)
		if e.Type != EventDeleted {
			continue
		}
		var p UIRecordingDeleted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.ID != first.ID {
			t.Fatalf("deleted %s, want %s (the oldest); %s is the newest", p.ID, first.ID, second.ID)
		}
		break
	}
	if got := f.list(t, `{}`); len(got) != 1 || got[0].ID != second.ID {
		t.Errorf("list after the sweep = %v, want %s alone", ids(got), second.ID)
	}
}

// Decision (ADR-0113 §6, ADR-0015 §6: delete is for the web and the CLI): an MCP session is
// refused before the call exists, so nothing is deleted; the UI and the CLI delete. Seen
// failing with the audience left open (the MCP call deleted the recording).
func TestDeleteIsHumanOnly(t *testing.T) {
	f := newRecFix(t)
	f.ops.Sessions().Connect("s1", "chat-1")
	rec := f.start(t, recParams)
	f.stop(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	params := `{"id":"` + rec.ID + `"}`
	_, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.deleteUiRecording", Params: []byte(params), Client: capability.ClientMCP, Origin: capability.Origin{ID: "s1"}})
	if err == nil {
		t.Fatalf("MCP delete started, want %s", capability.ErrNotAllowedHere)
	}
	if ce := asCapError(t, err); ce.Code != capability.ErrNotAllowedHere {
		t.Fatalf("MCP delete = %v, want %s", err, capability.ErrNotAllowedHere)
	}
	if got := f.list(t, `{}`); len(got) != 1 {
		t.Fatalf("the MCP call deleted: list = %v", ids(got))
	}
	out := f.call(t, "device.deleteUiRecording", params, capability.ClientCLI, capability.Origin{ID: "cli-1"})
	if res, ok := out.Result.(DeleteUIRecordingResult); out.State != capability.StateCompleted || !ok || !res.Deleted {
		t.Fatalf("CLI delete = %s %+v %+v, want deleted", out.State, out.Result, out.Error)
	}
	// The UI's, on a second recording.
	second := f.start(t, recParams)
	f.stop(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	out = f.call(t, "device.deleteUiRecording", `{"id":"`+second.ID+`"}`, capability.ClientUI, capability.Origin{})
	if res, ok := out.Result.(DeleteUIRecordingResult); out.State != capability.StateCompleted || !ok || !res.Deleted {
		t.Fatalf("UI delete = %s %+v %+v, want deleted", out.State, out.Result, out.Error)
	}
}

// Decision (ADR-0113 §5, §6: a running recording is never deleted): DEVICE_UI_RECORDING_ACTIVE
// with the id, and the recording keeps running with its timeline intact. Seen failing with the
// running check removed (the directory of the running recording was deleted under its writer).
func TestDeleteRunningRefused(t *testing.T) {
	f := newRecFix(t)
	rec := f.start(t, recParams)
	e := mustFail(t, f.call(t, "device.deleteUiRecording", `{"id":"`+rec.ID+`"}`, capability.ClientUI, capability.Origin{}), ErrUIRecordingActive)
	if e.Details["id"] != rec.ID {
		t.Errorf("details = %v, want the id %s", e.Details, rec.ID)
	}
	if _, err := os.Stat(filepath.Join(f.root, "recordings", rec.ID, "timeline.jsonl")); err != nil {
		t.Errorf("the running recording's timeline: %v", err)
	}
	if r := f.live(t); r.ID != rec.ID || r.State != RecordingRunning {
		t.Errorf("recording after the refused delete = %+v", r)
	}
	if res := f.stop(t); !res.Stopped {
		t.Errorf("stop after the refused delete = %+v", res)
	}
}

// Decision (ADR-0113 §5, ADR-0012: a deleted recording's reference reads ARTIFACT_EXPIRED;
// delete is idempotent; the watchers are told): after the delete the timeline artifact is
// gone, the list does not hold the recording, the watcher got `deleted {id}`, and deleting
// again, or an id nobody issued, answers deleted false. Seen failing with the delete leaving
// the directory (the reference still resolved).
func TestDeleteThenRefExpired(t *testing.T) {
	f := newRecFix(t)
	rec := f.start(t, recParams)
	f.stop(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	if _, err := f.art.Get(rec.Timeline.Ref); err != nil {
		t.Fatalf("the timeline before the delete: %v", err)
	}
	w := f.watch(t, "ws-1")
	del := func(id string) DeleteUIRecordingResult {
		out := f.call(t, "device.deleteUiRecording", `{"id":"`+id+`"}`, capability.ClientUI, capability.Origin{})
		if out.State != capability.StateCompleted {
			t.Fatalf("delete %s: %s %+v", id, out.State, out.Error)
		}
		return out.Result.(DeleteUIRecordingResult)
	}
	if res := del(rec.ID); !res.Deleted {
		t.Fatalf("delete = %+v, want deleted", res)
	}
	e := w.next(t)
	var p UIRecordingDeleted
	if json.Unmarshal(e.Payload, &p) != nil || e.Type != EventDeleted || p.ID != rec.ID {
		t.Errorf("event = %s %s, want deleted %s", e.Type, e.Payload, rec.ID)
	}
	_, err := f.art.Get(rec.Timeline.Ref)
	if ce, ok := err.(*capability.Error); !ok || ce.Code != capability.ErrArtifactExpired {
		t.Errorf("the timeline after the delete: %v, want %s", err, capability.ErrArtifactExpired)
	}
	if got := f.list(t, `{}`); len(got) != 0 {
		t.Errorf("list after the delete = %v", ids(got))
	}
	for _, id := range []string{rec.ID, "20200101T000000Z-00000000", "not-an-id"} {
		if res := del(id); res.Deleted {
			t.Errorf("delete of %s again = %+v, want deleted false", id, res)
		}
	}
	mustFail(t, f.call(t, "device.deleteUiRecording", `{"id":""}`, capability.ClientUI, capability.Origin{}), capability.ErrInvalidParams)
	if e, ok := w.sub.TryNext(); ok {
		t.Errorf("an unknown id was announced: %s %s", e.Type, e.Payload)
	}
}

// Decision (ADR-0036 §2: a rendering is the format's specification by example): the list's
// count and each recording's lines.
func TestUIRecordingsRenderingIsTheGoldenText(t *testing.T) {
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	end := start.Add(12400 * time.Millisecond)
	ended := UIRecording{
		ID: "20261021T100000Z-0a1b2c3d", Device: "emulator-5554", MaxSeconds: 900, StartedBy: PublishedStarter{Client: "mcp", Session: "s1"},
		State: RecordingEnded, StartedAt: start, EndedAt: &end, EndReason: EndStopped,
		Dumps: 14, Unchanged: 3, Missed: 1, LockWaits: 2, Events: 212, Gaps: 1, Bytes: 1843201, SHA256: "ab12",
		Timeline: artifact.TimelineRef("20261021T100000Z-0a1b2c3d", 1843201),
	}
	golden(t, "testdata/uirecordings-two.golden", uiRecordingsText(UIRecordingsResult{Recordings: []UIRecording{ended, ended}}))
	golden(t, "testdata/uirecordings-none.golden", uiRecordingsText(UIRecordingsResult{Recordings: []UIRecording{}}))
}
