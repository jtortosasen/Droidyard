package device

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Decision (ADR-0113 §5: after a crash the record is finalised from the whole lines kept): the
// counts the dead recorder held in memory are recounted from the file: dumps, events, gaps and
// missed, and not the half-written last line; unchanged was never written and stays 0. Seen
// failing with the recount removed (dumps 0, events 0, gaps 0, missed 0).
func TestRecoverRecountsTheLinesItKeeps(t *testing.T) {
	r, art, root := newRecordings(t)
	id := recID(1)
	w := startRecord(t, r, art, id, time.Now())
	for _, l := range []any{
		RecordingLine{Kind: "recording", ID: id, Format: 1},
		DumpLine{Kind: "dump", N: 1, Trigger: "start", XML: "<hierarchy/>"},
		EventLine{Kind: "event"}, EventLine{Kind: "event"}, EventLine{Kind: "event"},
		GapLine{Kind: "gap", Reason: "readerRestarted"},
		DumpLine{Kind: "dump", N: 2, Trigger: "change", XML: "<hierarchy/>"},
		MissedLine{Kind: "missed", Reason: missedBound},
		EventLine{Kind: "event"},
	} {
		if err := w.Line(l); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(filepath.Join(root, "recordings", id, "timeline.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"kind":"dump","n":3,"xml":"<hier`) // the cut line is not counted
	f.Close()
	if _, err := r.Recover(); err != nil {
		t.Fatal(err)
	}
	got, _, _ := r.Get(id)
	if got.Dumps != 2 || got.Events != 4 || got.Gaps != 1 || got.Missed != 1 || got.Unchanged != 0 {
		t.Errorf("recovered counts dumps=%d events=%d gaps=%d missed=%d unchanged=%d, want 2 4 1 1 0", got.Dumps, got.Events, got.Gaps, got.Missed, got.Unchanged)
	}
	if got.Bytes == 0 || got.SHA256 == "" || got.EndReason != EndDaemonExit {
		t.Errorf("recovered record = %+v", got)
	}
}

// Decision (ADR-0113 §5: oldest first, never the one that just ended): retention orders the
// ended recordings by when they ended, not when they started: a long recording that started
// before the others and ended last is the newest kept. Seen failing with the order by start
// (the just-ended long recording was swept at once).
func TestSweepOrdersByEndNotStart(t *testing.T) {
	r, art, _ := newRecordings(t)
	r.MaxRecordings = 2
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := func(i int, start, ended time.Time) {
		w := startRecord(t, r, art, recID(i), start)
		endRecord(t, r, w, recID(i), start, 10)
		rec, _, _ := r.Get(recID(i))
		rec.EndedAt = &ended
		if err := r.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	end(1, t0, t0.Add(10*time.Hour)) // started first, ended last
	end(2, t0.Add(time.Hour), t0.Add(2*time.Hour))
	end(3, t0.Add(2*time.Hour), t0.Add(3*time.Hour))
	gone, err := r.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0] != recID(2) {
		t.Errorf("swept %v, want only %s (the one that ended first)", gone, recID(2))
	}
}

// Decision (device.uiRecordings lists at most 50): a running recording that is the 50th
// newest does not let the older ones past the cap. Seen failing with the cap checked after the
// running branch's `continue` (55 listed).
func TestUIRecordingsCapsAtFiftyWithTheRunningAtTheEdge(t *testing.T) {
	f := newRecFix(t)
	running := f.start(t, recParams)
	now := time.Now()
	plant := func(i int, started time.Time) {
		ended := started.Add(time.Second)
		rec := UIRecord{ID: recID(i), Device: "other-device", MaxSeconds: 900, State: RecordingEnded, StartedAt: started, EndedAt: &ended, EndReason: EndStopped}
		if err := f.rec.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 49; i++ { // newer than the running recording
		plant(i, now.Add(time.Duration(i)*time.Minute))
	}
	for i := 50; i <= 55; i++ { // older
		plant(i, now.Add(-time.Duration(i)*time.Minute))
	}
	got := f.list(t, `{}`)
	if len(got) != uiRecordingsMax {
		t.Fatalf("listed %d, want %d", len(got), uiRecordingsMax)
	}
	if last := got[len(got)-1]; last.ID != running.ID || last.State != RecordingRunning {
		t.Errorf("the 50th is %s %s, want the running recording %s", last.ID, last.State, running.ID)
	}
}

// Decision (delete is idempotent and a record with no recorder is deletable): a record left
// `recording` with no live recorder (a failed Finalise) is deleted, not refused; and of
// several concurrent deletes of one id exactly one answers deleted true and announces it. Seen
// failing with the store refusing a `recording` record, and with the serialising lock removed
// (more than one true).
func TestDeleteOfAStaleRecordAndConcurrentDeletes(t *testing.T) {
	f := newRecFix(t)
	stale := recID(1)
	w := startRecord(t, f.rec, f.art, stale, time.Now().Add(-time.Hour))
	w.Line(RecordingLine{Kind: "recording", ID: stale})
	w.Close()
	if res, err := f.adb.deleteRecording(stale); err != nil || !res.Deleted {
		t.Fatalf("delete of a record left recording, with no recorder = %+v, %v; want deleted", res, err)
	}
	id := recID(2)
	w = startRecord(t, f.rec, f.art, id, time.Now())
	endRecord(t, f.rec, w, id, time.Now(), 5)
	w2 := f.watch(t, "ws-1")
	var wg sync.WaitGroup
	var mu sync.Mutex
	trues := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := f.adb.deleteRecording(id)
			if err != nil {
				t.Error(err)
			}
			if res.Deleted {
				mu.Lock()
				trues++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if trues != 1 {
		t.Errorf("%d deletes answered true, want exactly 1", trues)
	}
	e := w2.next(t)
	if e.Type != EventDeleted {
		t.Fatalf("event %s", e.Type)
	}
	if extra, ok := w2.sub.TryNext(); ok {
		t.Errorf("a second delete was announced: %s %s", extra.Type, extra.Payload)
	}
}
