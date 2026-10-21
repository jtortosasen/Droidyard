package device

import (
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Decision (ADR-0113 §1, ADR-0034 §1: after a `busy` answer the reader is left alone for the
// pause, so the recorder never holds both of its workers): a stop inside the pause takes no
// stop dump. It writes a `missed` line (paused) and ends stopped. Seen failing with the stop
// dump taken regardless (a third dump request came 10 ms after the busy answer).
func TestStopInsideTheBusyPauseTakesNoDump(t *testing.T) {
	f := newRecFix(t, recDoc("A"), "droidyard-reader: busy 2500 com.example", recDoc("B"))
	recordBusyPause = 2 * time.Second
	rec := f.start(t, recParams)
	f.event(1)
	f.until(t, "the busy dump missed", func(r UIRecording) bool { return r.Missed == 1 })
	res := f.stop(t)
	if got := len(f.r.DumpStarts()); got != 2 {
		t.Errorf("%d dump requests, want the start and the busy one: none inside the pause", got)
	}
	if !res.Stopped || res.Recording.EndReason != EndStopped || res.Recording.Missed != 2 {
		t.Errorf("stop result %+v, want stopped with the busy dump and the skipped stop dump missed", res.Recording)
	}
	missed := ofKind(f.timeline(t, rec.ID), "missed")
	if len(missed) != 2 || missed[1].str("reason") != missedPaused {
		t.Errorf("missed lines %v, want the busy one then one with reason %s", missed, missedPaused)
	}
}

// Decision (the same, for a dump the stop interrupted: the reader's worker for it keeps
// running on the device): a stop that cancels a dump in flight takes no stop dump either.
// Seen failing with the pause set only by a busy answer (a third request followed).
func TestStopThatInterruptsADumpTakesNoDump(t *testing.T) {
	f := newRecFix(t, recDoc("A"), recDoc("B"), recDoc("C"))
	recordBusyPause = 2 * time.Second // longer than the stop's drain of the events (300 ms, each read delayed 400 ms)
	f.start(t, recParams)
	f.r.SetDelay(400 * time.Millisecond)
	f.event(1)
	c := f.adb.reader(uiSerial)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "a dump to hold the lock", func() bool { return len(c.lock) == 1 })
	res := f.stop(t)
	time.Sleep(1200 * time.Millisecond) // requests the fake delays land late
	if got := len(f.r.DumpStarts()); got != 2 {
		t.Errorf("%d dump requests, want the start and the interrupted one: no stop dump", got)
	}
	if !res.Stopped || res.Recording.EndReason != EndStopped {
		t.Errorf("stop result %+v", res.Recording)
	}
}

// Decision (device.stopUiRecording's `stopped` is true when this call ended the recording): a
// stop that finds the recording already ending for another reason returns stopped false and
// that recording, with its own end reason. Seen failing with stopped always true.
func TestStopOfARecordingAlreadyEnding(t *testing.T) {
	f := newRecFix(t)
	rec := f.start(t, recParams)
	f.adb.recorders.get(uiSerial).requestEnd(EndMaxDumps, "", false)
	out := f.call(t, "device.stopUiRecording", recParams, capability.ClientUI, capability.Origin{})
	res := mustStop(t, out)
	if res.Stopped || res.Recording == nil || res.Recording.ID != rec.ID || res.Recording.EndReason != EndMaxDumps {
		t.Errorf("stop of an ending recording = %+v, want stopped false and the recording ended maxDumps", res)
	}
}
