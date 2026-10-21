package device

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/store"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// device.recordUi and device.stopUiRecording over the fake reader (ADR-0113, ADR-0114; plan
// m2x step 5). The ring, the cursor and the patient reach are device.uiEvents's and
// watchUiEvents's (their tests); these pin the recorder: what it stores and when, what it
// never does, how it ends, and who may stop it.

// fastRecorder shortens the recorder's intervals; the previous values return at the test's
// end, after the recorders it started have been stopped (cleanups run last in, first out).
func fastRecorder(t *testing.T) {
	t.Helper()
	tick, gmin, gmax, busy, lock, slock, drain, sum, all, sec := recordTick, recordGapMin, recordGapMax, recordBusyPause, recordLockWait, recordStopLockWait, recordDrain, recordSummaryWait, recordStopAllWait, recordSecond
	dumps, size, reserve := recordMaxDumps, recordMaxBytes, recordEndReserve
	recordTick, recordGapMin, recordGapMax, recordBusyPause = 10*time.Millisecond, 10*time.Millisecond, 80*time.Millisecond, 250*time.Millisecond
	recordLockWait, recordStopLockWait, recordDrain, recordSummaryWait, recordStopAllWait = 30*time.Millisecond, 500*time.Millisecond, 300*time.Millisecond, 5*time.Second, time.Second
	t.Cleanup(func() {
		recordTick, recordGapMin, recordGapMax, recordBusyPause, recordLockWait, recordStopLockWait, recordDrain, recordSummaryWait, recordStopAllWait, recordSecond = tick, gmin, gmax, busy, lock, slock, drain, sum, all, sec
		recordMaxDumps, recordMaxBytes, recordEndReserve = dumps, size, reserve
	})
}

// recDoc is a small full document whose only difference from another is its label.
func recDoc(label string) string {
	return `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">` + "\n" +
		`<node index="0" text="` + label + `" resource-id="" class="android.widget.FrameLayout" package="com.example" content-desc="" bounds="[0,0][1080,2400]" drawing-order="0" hint="">` + "\n" +
		`<node index="0" text="child" resource-id="" class="android.widget.TextView" package="com.example" content-desc="" bounds="[0,0][10,10]" drawing-order="1" hint="" />` + "\n" +
		"</node>\n</hierarchy>\n"
}

// recEvent is an event that happened now, told apart from another by its window id.
func recEvent(window int) devicetest.FakeEvent {
	return devicetest.FakeEvent{TimeMs: time.Now().UnixMilli(), Type: "click", Window: window, Package: "com.example", Class: "android.widget.Button"}
}

// recFix is the recorder over the fake adb and the fake reader: a registry with a state root,
// the recordings directory, and the session registry the stop rule asks for chats.
type recFix struct {
	*uiFixture
	r    *devicetest.FakeReader
	rec  *UIRecordings
	art  *artifact.Store
	root string
	// bus is the daemon's event bus the recorder announces on (device.watchUiRecordings).
	bus *event.Bus
}

// newRecFix starts the fake reader answering full dumps with docs in order, the last
// repeating (a recDoc("A") when none).
func newRecFix(t *testing.T, docs ...string) *recFix {
	t.Helper()
	fastRecorder(t)
	if len(docs) == 0 {
		docs = []string{recDoc("A")}
	}
	r := devicetest.StartReader(t, pixelDoc(t))
	r.SetFullDocs(docs...)
	dir := t.TempDir()
	for name, content := range map[string]string{
		"devices.out":                   uiList(phoneLine),
		uiSerial + ".bootcompleted.out": "1\n",
		uiSerial + ".push.exit":         "0",
		uiSerial + ".start.out":         "",
		uiSerial + ".forward.out":       r.Port,
	} {
		devicetest.Write(t, dir, name, []byte(content))
	}
	adb := newFake(t, dir, closedPort(t))
	root := t.TempDir()
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	art, err := artifact.New(artifact.Options{Epoch: "d-0a1b2c3d", StateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(art.Close)
	rec := NewUIRecordings(st)
	adb.SetRecordings(rec)
	reg := capability.NewRegistry()
	Register(reg, adb, art, nil)
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	adb.SetOccupation(ops.Sessions())
	bus := event.New(event.Options{})
	adb.SetBus(bus)
	f := &recFix{uiFixture: &uiFixture{dir: dir, adb: adb, ops: ops, store: art}, r: r, rec: rec, art: art, root: root, bus: bus}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		adb.StopRecordings(ctx)
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the recorders to leave the set", func() bool { return adb.LiveRecorders() == 0 })
	})
	return f
}

// call runs a capability as the given client and returns its outcome.
func (f *recFix) call(t *testing.T, name, params string, client capability.ClientKind, origin capability.Origin) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: name, Params: []byte(params), Client: client, Origin: origin})
	if err != nil {
		t.Fatalf("Start %s: %v", name, err)
	}
	return f.wait(t, id)
}

const recParams = `{"device":"` + uiSerial + `"}`

// start starts a recording as the UI and requires it to have started.
func (f *recFix) start(t *testing.T, params string) UIRecording {
	t.Helper()
	return mustRecording(t, f.call(t, "device.recordUi", params, capability.ClientUI, capability.Origin{}))
}

func mustRecording(t *testing.T, out capability.Outcome) UIRecording {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("device.recordUi %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(UIRecording)
	if !ok {
		t.Fatalf("result is %T, want UIRecording", out.Result)
	}
	return r
}

func mustStop(t *testing.T, out capability.Outcome) StopUIRecordingResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("device.stopUiRecording %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(StopUIRecordingResult)
	if !ok {
		t.Fatalf("result is %T, want StopUIRecordingResult", out.Result)
	}
	return r
}

// stop stops the device's recording as the UI.
func (f *recFix) stop(t *testing.T) StopUIRecordingResult {
	t.Helper()
	return mustStop(t, f.call(t, "device.stopUiRecording", recParams, capability.ClientUI, capability.Origin{}))
}

// live is the running recording's summary.
func (f *recFix) live(t *testing.T) UIRecording {
	t.Helper()
	r := f.adb.recorders.get(uiSerial)
	if r == nil {
		t.Fatal("no recorder is running")
	}
	return r.summary()
}

// until waits for the running recording's summary to satisfy cond.
func (f *recFix) until(t *testing.T, what string, cond func(UIRecording) bool) UIRecording {
	t.Helper()
	var last UIRecording
	testtime.Eventually(t, testtime.Bound(10*time.Second), what, func() bool {
		r := f.adb.recorders.get(uiSerial)
		if r == nil {
			return false
		}
		last = r.summary()
		return cond(last)
	})
	return last
}

// event adds an event on the device, a little after the previous dump was requested (the
// recorder compares an event's device time with the dump's request).
func (f *recFix) event(window int) {
	time.Sleep(10 * time.Millisecond)
	f.r.AddEvents(recEvent(window))
}

// tlLine is one line of a timeline.
type tlLine map[string]any

func (l tlLine) str(k string) string { s, _ := l[k].(string); return s }

// timeline reads recording id's timeline: every line must be whole JSON and the file must end
// with a newline.
func (f *recFix) timeline(t *testing.T, id string) []tlLine {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "recordings", id, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("the timeline does not end with a newline: %q", data[max(0, len(data)-80):])
	}
	var out []tlLine
	for i, raw := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var l tlLine
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("line %d is not whole JSON: %v: %.120q", i+1, err, raw)
		}
		out = append(out, l)
	}
	return out
}

func kindsOf(lines []tlLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.str("kind")
	}
	return out
}

func ofKind(lines []tlLine, kind string) []tlLine {
	var out []tlLine
	for _, l := range lines {
		if l.str("kind") == kind {
			out = append(out, l)
		}
	}
	return out
}

func xmlsOf(lines []tlLine) []string {
	var out []string
	for _, l := range ofKind(lines, "dump") {
		out = append(out, l.str("xml"))
	}
	return out
}

func (f *recFix) endedRecording(t *testing.T, id string) UIRecord {
	t.Helper()
	rec, found, err := f.rec.Get(id)
	if err != nil || !found {
		t.Fatalf("Get %s: found=%v err=%v", id, found, err)
	}
	return rec
}

// Decision (ADR-0113 §1: a dump at start, one whenever an event came after the previous dump's
// request, and an identical one is counted, not stored): three changes and an unchanged
// screen make three stored dumps and the identical ones only a count, in the file and in the
// summary; the stop dump is identical too and is counted the same. Seen failing with the
// SHA-256 comparison removed (the identical dumps were stored, so the counts never reached
// 3 and 2).
func TestRecordStoresADumpPerChangeAndSkipsIdentical(t *testing.T) {
	f := newRecFix(t, recDoc("A"), recDoc("B"), recDoc("B"), recDoc("C"), recDoc("C"))
	rec := f.start(t, recParams)
	if rec.State != RecordingRunning || rec.Dumps != 1 || rec.Timeline.Ref != "droidyard://artifact/r."+rec.ID {
		t.Fatalf("started as %+v", rec)
	}
	f.event(1)
	f.until(t, "the change B stored", func(r UIRecording) bool { return r.Dumps == 2 && r.Unchanged == 0 })
	f.event(2)
	f.until(t, "the identical B counted", func(r UIRecording) bool { return r.Dumps == 2 && r.Unchanged == 1 })
	f.event(3)
	f.until(t, "the change C stored", func(r UIRecording) bool { return r.Dumps == 3 && r.Unchanged == 1 })
	res := f.stop(t)
	if !res.Stopped || res.Recording.State != RecordingEnded || res.Recording.Dumps != 3 || res.Recording.Unchanged != 2 {
		t.Fatalf("stopped = %+v, want ended with 3 dumps stored and 2 unchanged", res)
	}
	lines := f.timeline(t, rec.ID)
	if got, want := xmlsOf(lines), []string{recDoc("A"), recDoc("B"), recDoc("C")}; !slices.Equal(got, want) {
		t.Errorf("the stored dumps are %q, want A, B, C", got)
	}
	var triggers []string
	for _, l := range ofKind(lines, "dump") {
		triggers = append(triggers, l.str("trigger"))
	}
	if !slices.Equal(triggers, []string{"start", "change", "change"}) {
		t.Errorf("triggers %q, want start, change, change", triggers)
	}
	if k := kindsOf(lines); k[0] != "recording" || k[len(k)-1] != "end" {
		t.Errorf("kinds %q: want the header first and the end last", k)
	}
}

// Decision (ADR-0113 §1: due when an event came after the previous dump's *request*): an event
// that arrives while a dump is being read, the last of a burst, is not covered by that dump
// and makes another one due. Seen failing with the comparison made against the dump's end
// (the event was older than it and no third dump came).
func TestRecordDumpsAfterTheLastEventOfABurst(t *testing.T) {
	f := newRecFix(t, recDoc("A"), recDoc("B"), recDoc("C"), recDoc("C"))
	rec := f.start(t, recParams)
	f.r.SetDelay(150 * time.Millisecond) // every request takes 150 ms: a dump holds the lock that long
	f.event(1)
	c := f.adb.reader(uiSerial)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the second dump to hold the lock", func() bool { return len(c.lock) == 1 })
	time.Sleep(30 * time.Millisecond) // inside the second dump
	last := time.Now().UnixMilli()
	f.r.AddEvents(devicetest.FakeEvent{TimeMs: last, Type: "click", Window: 2, Package: "com.example", Class: "android.widget.Button"})
	f.until(t, "a dump after the last event", func(r UIRecording) bool { return r.Dumps == 3 })
	f.r.SetDelay(0)
	f.stop(t)
	dumps := ofKind(f.timeline(t, rec.ID), "dump")
	if got := dumps[2].str("time"); got < devStamp(last) {
		t.Errorf("the third dump is of %s, before the last event at %s", got, devStamp(last))
	}
	if got := dumps[2].str("xml"); got != recDoc("C") {
		t.Errorf("the third dump holds %.60q, want the settled screen C", got)
	}
}

// Decision (ADR-0113 §1: the gap since the previous dump ended is 100 ms, doubled after each
// dump identical to the one before up to 2 s, reset by a stored dump): with events arriving
// all the time, the requests are at least 40, 80, 160, 320 and 640 ms apart while the dumps
// are identical, and the one after a stored dump is back near 40 ms. Seen failing without
// the doubling (the requests were 40 ms apart) and without the reset (the request after the
// stored dump waited 640 ms).
func TestRecordGapDoublesOnIdenticalDumps(t *testing.T) {
	f := newRecFix(t, recDoc("A"), recDoc("A"), recDoc("A"), recDoc("A"), recDoc("A"), recDoc("B"), recDoc("B"))
	recordGapMin, recordGapMax = 40*time.Millisecond, 800*time.Millisecond
	f.start(t, recParams)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for w := 1; ; w++ {
			select {
			case <-stop:
				return
			case <-time.After(4 * time.Millisecond):
				f.r.AddEvents(recEvent(w))
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	testtime.Eventually(t, testtime.Bound(15*time.Second), "seven dump requests", func() bool { return len(f.r.DumpStarts()) >= 7 })
	starts := f.r.DumpStarts()
	for i, gap := range []time.Duration{40, 80, 160, 320, 640} {
		if d := starts[i+1].Sub(starts[i]); d < gap*time.Millisecond {
			t.Errorf("request %d came %v after the one before; with %d identical dumps before it the gap is at least %v", i+1, d, i, gap*time.Millisecond)
		}
	}
	// The sixth request read B, a stored dump: the gap is reset to its least.
	if d := starts[6].Sub(starts[5]); d > 300*time.Millisecond {
		t.Errorf("the request after a stored dump came %v later, want near the least gap (40 ms), not the doubled 640 ms", d)
	}
}

// holdFor holds the device's read lock until the returned release.
func (f *recFix) holdFor(t *testing.T) (release func()) {
	t.Helper()
	return holdReader(t, f.uiFixture)
}

// Decision (ADR-0113 §1, §2 and ADR-0114 §Consequences: a read lock another caller holds is
// waited for 200 ms, counted in lockWaits, and the dump stays due): with the lock held the
// recorder stores nothing and counts the waits; when it is let go the dump is taken at once
// and says in waitedMs how long it was due. Seen failing with the wait not counted (lockWaits
// stayed 0) and with a dump dropped after a failed wait (never taken after the release).
func TestRecordCountsLockWaits(t *testing.T) {
	f := newRecFix(t, recDoc("A"), recDoc("B"))
	rec := f.start(t, recParams)
	release := f.holdFor(t)
	f.event(1)
	held := f.until(t, "waits counted", func(r UIRecording) bool { return r.LockWaits >= 3 })
	if held.Dumps != 1 {
		t.Fatalf("a dump was stored (%d) while the lock was held", held.Dumps)
	}
	time.Sleep(200 * time.Millisecond)
	release()
	f.until(t, "the dump taken after the release", func(r UIRecording) bool { return r.Dumps == 2 })
	f.stop(t)
	dumps := ofKind(f.timeline(t, rec.ID), "dump")
	if w, _ := dumps[1]["waitedMs"].(float64); w < 250 {
		t.Errorf("the dump that waited for the lock says waitedMs %v, want at least 250", dumps[1]["waitedMs"])
	}
}

// Decision (ADR-0113 §1: "every event once", a reader restart a gap): the events from before
// the start are not in the recording; the ones after it are, once each, in order; the ring of
// a restarted reader is read from its start and the timeline says so with one gap line. Seen
// failing with the cursor kept across the restart (events 4 and 5 were never read: the old
// cursor was ahead of the new ring).
func TestRecordTimelineHoldsEveryEventOnce(t *testing.T) {
	f := newRecFix(t, recDoc("A"), recDoc("B"))
	f.r.AddEvents(recEvent(100), recEvent(101)) // before the start
	rec := f.start(t, recParams)
	f.r.AddEvents(recEvent(1), recEvent(2), recEvent(3))
	f.until(t, "three events", func(r UIRecording) bool { return r.Events == 3 })
	f.r.RestartEvents("fedcba9876543210")
	f.r.AddEvents(recEvent(4), recEvent(5))
	f.until(t, "five events and the gap", func(r UIRecording) bool { return r.Events == 5 && r.Gaps == 1 })
	res := f.stop(t)
	if res.Recording.Events != 5 || res.Recording.Gaps != 1 {
		t.Errorf("summary events %d gaps %d, want 5 and 1", res.Recording.Events, res.Recording.Gaps)
	}
	lines := f.timeline(t, rec.ID)
	var windows []int
	for _, l := range ofKind(lines, "event") {
		w, _ := l["window"].(float64)
		windows = append(windows, int(w))
	}
	if !slices.Equal(windows, []int{1, 2, 3, 4, 5}) {
		t.Errorf("event windows %v, want 1..5 once each and none from before the start", windows)
	}
	gaps := ofKind(lines, "gap")
	if len(gaps) != 1 || gaps[0].str("reason") != gapRestarted {
		t.Errorf("gap lines %v, want one readerRestarted", gaps)
	}
}

// Decision (ADR-0113 §5: each dump's XML is the reader's `dump full` document byte for byte):
// a document with markup characters, a tab, a newline and non-ASCII text is in the dump line
// exactly as the reader sent it, unescaped, with its SHA-256, size, node count and foreground.
// Seen failing with the line encoded with HTML escaping on (the XML held <).
func TestRecordDumpXMLIsTheReadersDocument(t *testing.T) {
	doc := fullDocument + "\t\n" // trailing bytes the reader may send are kept too
	f := newRecFix(t, doc, recDoc("B"))
	rec := f.start(t, recParams)
	f.stop(t)
	data, err := os.ReadFile(filepath.Join(f.root, "recordings", rec.ID, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if esc := "\\" + "u00"; strings.Contains(string(data), esc+"3c") || strings.Contains(string(data), esc+"26") {
		t.Errorf("the timeline escapes the document's markup characters")
	}
	dump := ofKind(f.timeline(t, rec.ID), "dump")[0]
	sum := sha256.Sum256([]byte(doc))
	if dump.str("xml") != doc || dump.str("sha256") != hex.EncodeToString(sum[:]) || int(dump["bytes"].(float64)) != len(doc) {
		t.Errorf("dump line %.200q: want the document byte for byte with its digest and size", dump)
	}
	if dump["nodes"] != float64(3) || dump.str("foreground") != "com.example" || dump.str("trigger") != "start" {
		t.Errorf("dump line nodes %v foreground %q trigger %q, want 3, com.example, start", dump["nodes"], dump.str("foreground"), dump.str("trigger"))
	}
}

// Decision (ADR-0113 §2 and ADR-0110: the persistent reader only, never the one-shot reader
// and never `uiautomator dump`): when the reader's dump fails the recorder writes nothing,
// pushes and runs no other reader, and takes the dump again once the reader answers; the
// recorder's only way to the reader is persistentRead. Seen failing with readFull in its
// place (a one-shot reader was pushed and run beside the persistent one).
func TestRecordNeverUsesTheOneShotOrUiautomator(t *testing.T) {
	f := newRecFix(t, recDoc("A"))
	f.start(t, recParams)
	f.r.SetFullDocs(devicetest.ReaderGone)
	f.event(1)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the failing dump to be requested again", func() bool { return len(f.r.FullRequests()) >= 4 })
	if r := f.live(t); r.Dumps != 1 || r.Missed != 0 {
		t.Errorf("a failed read left dumps=%d missed=%d, want 1 and 0 (a reader failure writes no line)", r.Dumps, r.Missed)
	}
	f.r.SetFullDocs(recDoc("B"))
	f.until(t, "the dump taken once the reader answers", func(r UIRecording) bool { return r.Dumps == 2 })
	for _, c := range f.calls(t) {
		if strings.Contains(c, "uiautomator") || strings.Contains(c, "-oneshot.dex") {
			t.Errorf("the recorder ran another reader: %q", c)
		}
	}
}

// Decision (ADR-0113 §1, S1: a 5 s pause after `busy`, shortened here): after the reader says
// busy the recorder writes a `missed` line (bound) and leaves the reader alone for the pause;
// the dump is still due and is taken after it. Seen failing with no pause (the next request
// came a gap later).
func TestRecordPausesAfterBusy(t *testing.T) {
	f := newRecFix(t, recDoc("A"), "droidyard-reader: busy 2500 com.example", recDoc("B"))
	rec := f.start(t, recParams)
	f.event(1)
	f.until(t, "the busy dump missed", func(r UIRecording) bool { return r.Missed == 1 })
	f.until(t, "the dump taken after the pause", func(r UIRecording) bool { return r.Dumps == 2 })
	starts := f.r.DumpStarts()
	if len(starts) != 3 {
		t.Fatalf("%d dump requests, want start, busy and the retry", len(starts))
	}
	if d := starts[2].Sub(starts[1]); d < recordBusyPause {
		t.Errorf("the retry came %v after the busy answer, want at least the pause %v", d, recordBusyPause)
	}
	f.stop(t)
	missed := ofKind(f.timeline(t, rec.ID), "missed")
	if len(missed) != 1 || missed[0].str("reason") != missedBound {
		t.Errorf("missed lines %v, want one with reason bound", missed)
	}
}

// Decision (ADR-0113 §2: the read lock is held only for each dump, so an agent's read waits at
// most one dump): between dumps the lock is free for every probe; during a dump it is held.
// Seen failing with the lock held across ticks (every probe of the idle recorder failed).
func TestRecordHoldsTheLockOnlyForADump(t *testing.T) {
	f := newRecFix(t, recDoc("A"), recDoc("B"))
	f.start(t, recParams)
	c := f.adb.reader(uiSerial)
	probe := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Millisecond)
		defer cancel()
		if c.acquire(ctx) != nil {
			return false
		}
		c.release()
		return true
	}
	for i := 0; i < 25; i++ {
		if !probe() {
			t.Fatalf("the idle recorder held the read lock at probe %d", i)
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.r.SetDelay(300 * time.Millisecond)
	f.event(1)
	heldDuring := false
	for i := 0; i < 60 && !heldDuring; i++ {
		heldDuring = !probe()
		time.Sleep(10 * time.Millisecond)
	}
	if !heldDuring {
		t.Errorf("the lock was free all through a 300 ms dump: the probe cannot tell the recorder holds it")
	}
	f.r.SetDelay(0)
	f.until(t, "the dump to finish", func(r UIRecording) bool { return r.Dumps == 2 })
	if !probe() {
		t.Errorf("the lock stayed held after the dump")
	}
}

// Decision (ADR-0113 §3: ends stopped, maxDuration, maxDumps, maxBytes, deviceGone, failed,
// daemonExit; the summary, the end line and the record agree, and a timeline at its cap still
// ends with its end line, no line cut): each reason, one recording each. Seen failing for
// maxBytes with no room kept for the end line (the ordinary lines went past cap minus the
// reserve).
func TestRecordEndReasons(t *testing.T) {
	// check requires the recording to have ended with reason everywhere it says so.
	check := func(t *testing.T, f *recFix, id, reason string) (UIRecording, []tlLine) {
		t.Helper()
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the recorder to leave the set", func() bool { return f.adb.LiveRecorders() == 0 })
		lines := f.timeline(t, id)
		end := lines[len(lines)-1]
		if end.str("kind") != "end" || end.str("reason") != reason || end.str("hostTime") == "" {
			t.Fatalf("last line %v, want an end line with reason %s and a host time", end, reason)
		}
		if n := len(ofKind(lines, "end")); n != 1 {
			t.Errorf("%d end lines, want one", n)
		}
		rec := f.endedRecording(t, id)
		if rec.State != RecordingEnded || rec.EndReason != reason {
			t.Errorf("record state %q end %q, want ended and %s", rec.State, rec.EndReason, reason)
		}
		data, _ := os.ReadFile(filepath.Join(f.root, "recordings", id, "timeline.jsonl"))
		sum := sha256.Sum256(data)
		if rec.Bytes != int64(len(data)) || rec.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("record bytes %d sha %.12s, the file is %d bytes sha %.12x", rec.Bytes, rec.SHA256, len(data), sum)
		}
		out := f.nothingToStopOf(t)
		if out.Recording == nil || out.Recording.ID != id || out.Recording.EndReason != reason {
			t.Errorf("the newest kept recording is %+v, want %s ended %s", out.Recording, id, reason)
		}
		return *out.Recording, lines
	}
	events := func(f *recFix) (stop func()) {
		quit := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for w := 1; ; w++ {
				select {
				case <-quit:
					return
				case <-time.After(8 * time.Millisecond):
					f.r.AddEvents(recEvent(w))
				}
			}
		}()
		return func() { close(quit); <-done }
	}
	distinct := func(n int) []string {
		var docs []string
		for i := 0; i < n; i++ {
			docs = append(docs, recDoc(fmt.Sprintf("screen %d", i)))
		}
		return docs
	}

	t.Run("stopped", func(t *testing.T) {
		f := newRecFix(t, recDoc("A"), recDoc("B"), recDoc("C"))
		rec := f.start(t, recParams)
		f.event(1)
		f.until(t, "B", func(r UIRecording) bool { return r.Dumps == 2 })
		res := f.stop(t)
		if res.Recording.EndReason != EndStopped || res.Recording.State != RecordingEnded {
			t.Fatalf("stop result %+v", res.Recording)
		}
		_, lines := check(t, f, rec.ID, EndStopped)
		var triggers []string
		for _, l := range ofKind(lines, "dump") {
			triggers = append(triggers, l.str("trigger"))
		}
		if !slices.Equal(triggers, []string{"start", "change", "stop"}) {
			t.Errorf("triggers %q: a stop takes a last dump", triggers)
		}
	})
	t.Run("maxDuration", func(t *testing.T) {
		f := newRecFix(t)
		recordSecond = 10 * time.Millisecond
		rec := f.start(t, `{"device":"`+uiSerial+`","maxSeconds":10}`)
		if rec.MaxSeconds != 10 {
			t.Fatalf("maxSeconds %d", rec.MaxSeconds)
		}
		_, lines := check(t, f, rec.ID, EndMaxDuration)
		if n := len(ofKind(lines, "dump")); n != 1 {
			t.Errorf("%d dumps, want only the start dump: a bound takes no last dump", n)
		}
	})
	t.Run("maxDumps", func(t *testing.T) {
		f := newRecFix(t, distinct(40)...)
		recordMaxDumps = 3
		rec := f.start(t, recParams)
		defer events(f)()
		sum, _ := check(t, f, rec.ID, EndMaxDumps)
		if sum.Dumps != 3 {
			t.Errorf("%d dumps stored, want exactly 3", sum.Dumps)
		}
	})
	t.Run("maxBytes", func(t *testing.T) {
		f := newRecFix(t, distinct(200)...)
		recordMaxBytes, recordEndReserve = 3000, 1000
		rec := f.start(t, recParams)
		defer events(f)()
		sum, lines := check(t, f, rec.ID, EndMaxBytes)
		data, _ := os.ReadFile(filepath.Join(f.root, "recordings", rec.ID, "timeline.jsonl"))
		endLen := len(marshalJSON(t, lines[len(lines)-1])) + 1
		if len(data) > 3000 {
			t.Errorf("the timeline is %d bytes, past its cap of 3000", len(data))
		}
		if before := len(data) - endLen; before > 3000-1000 {
			t.Errorf("ordinary lines took the file to %d bytes, past the cap minus the room kept for the end line (2000)", before)
		}
		if sum.Bytes != int64(len(data)) {
			t.Errorf("summary bytes %d, file %d", sum.Bytes, len(data))
		}
	})
	t.Run("deviceGone", func(t *testing.T) {
		shortRecovery(t)
		eventsLockWait = 5 * time.Millisecond
		f := newRecFix(t)
		rec := f.start(t, recParams)
		devicetest.Write(t, f.dir, "devices.out", []byte(listHead))
		devicetest.Write(t, f.dir, uiSerial+".start.exit", []byte("1"))
		f.r.Close()
		sum, lines := check(t, f, rec.ID, EndDeviceGone)
		end := lines[len(lines)-1]
		if sum.Cause == "" || end.str("cause") != sum.Cause || end.str("time") != "" {
			t.Errorf("cause %q, end line %v: want the same cause and no device time", sum.Cause, end)
		}
	})
	t.Run("failed", func(t *testing.T) {
		shortRecovery(t)
		eventsLockWait = 5 * time.Millisecond
		f := newRecFix(t)
		rec := f.start(t, recParams)
		devicetest.Write(t, f.dir, uiSerial+".start.exit", []byte("1"))
		f.r.Close()
		sum, lines := check(t, f, rec.ID, EndFailed)
		if end := lines[len(lines)-1]; sum.Cause == "" || end.str("cause") != sum.Cause {
			t.Errorf("cause %q, end line %v: want the same cause", sum.Cause, end)
		}
	})
	t.Run("daemonExit", func(t *testing.T) {
		f := newRecFix(t, recDoc("A"), recDoc("B"))
		rec := f.start(t, recParams)
		f.event(1)
		f.r.SetFullDocs(recDoc("Z"))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		began := time.Now()
		f.adb.StopRecordings(ctx)
		if d := time.Since(began); d > recordStopAllWait+time.Second {
			t.Errorf("StopRecordings took %v", d)
		}
		_, lines := check(t, f, rec.ID, EndDaemonExit)
		for _, l := range ofKind(lines, "dump") {
			if l.str("trigger") == "stop" {
				t.Errorf("a daemon exit took a last dump: %v", l)
			}
		}
	})
}

func marshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// nothingToStopOf is what device.stopUiRecording answers when nothing runs.
func (f *recFix) nothingToStopOf(t *testing.T) StopUIRecordingResult {
	t.Helper()
	res := f.stop(t)
	if res.Stopped {
		t.Fatalf("stopped = true with no recording running: %+v", res)
	}
	return res
}

// Decision (ADR-0113 §3: one recording per device; DEVICE_UI_RECORDING_ACTIVE with details
// {id}): a second start on the device is refused naming the first, which keeps running; after
// the stop the device records again. Seen failing with no claim (two recorders on one reader).
func TestRecordOnePerDevice(t *testing.T) {
	f := newRecFix(t)
	first := f.start(t, recParams)
	err := mustFail(t, f.call(t, "device.recordUi", recParams, capability.ClientUI, capability.Origin{}), ErrUIRecordingActive)
	if err.Details["id"] != first.ID {
		t.Errorf("details %v, want the running recording's id %s", err.Details, first.ID)
	}
	if f.adb.LiveRecorders() != 1 || f.live(t).ID != first.ID {
		t.Errorf("the first recording did not keep running")
	}
	f.stop(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	second := f.start(t, recParams)
	if second.ID == first.ID {
		t.Errorf("the second recording has the first's id %s", first.ID)
	}
}

// Decision (ADR-0113 §3: a daemon-owned job that outlives the session and the call that
// started it): the recording of an MCP session that has ended, started by an operation whose
// context is long gone, goes on storing dumps and is stopped by someone else. Seen failing
// with the recorder's context derived from the call's (it ended when the call completed).
func TestRecordOutlivesItsSession(t *testing.T) {
	f := newRecFix(t, recDoc("A"), recDoc("B"))
	f.ops.Sessions().Connect("s1", "chat-1")
	ctx, cancel := context.WithCancel(context.Background())
	id, err := f.ops.Start(ctx, capability.Request{Capability: "device.recordUi", Params: []byte(recParams), Client: capability.ClientMCP, Origin: capability.Origin{ID: "s1", Name: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := mustRecording(t, f.wait(t, id))
	cancel()
	f.ops.Sessions().End("s1")
	time.Sleep(150 * time.Millisecond)
	if r := f.live(t); r.State != RecordingRunning {
		t.Fatalf("the recording ended with its session: %+v", r)
	}
	f.event(1)
	f.until(t, "a dump after the session ended", func(r UIRecording) bool { return r.Dumps == 2 })
	if res := f.stop(t); !res.Stopped || res.Recording.ID != rec.ID {
		t.Errorf("stop by the UI = %+v", res)
	}
}

// Decision (ADR-0113 §4: ADR-0101's reach rule on the recording's starter): the UI and the CLI
// stop any recording; an MCP session stops the ones an MCP session of its own session or chat
// started and none a UI started; the refused stop leaves the recording running. Seen failing
// with the check removed (every stop succeeded).
func TestStopFollowsTheOperationReachRule(t *testing.T) {
	type who struct {
		name   string
		client capability.ClientKind
		origin capability.Origin
	}
	ui := who{"ui", capability.ClientUI, capability.Origin{}}
	cli := who{"cli", capability.ClientCLI, capability.Origin{ID: "cli-1"}}
	s1 := who{"mcp s1", capability.ClientMCP, capability.Origin{ID: "s1"}}
	s2 := who{"mcp s2 (same chat)", capability.ClientMCP, capability.Origin{ID: "s2"}}
	s3 := who{"mcp s3 (other chat)", capability.ClientMCP, capability.Origin{ID: "s3"}}
	rows := []struct {
		starter who
		caller  who
		ok      bool
	}{
		{s1, ui, true}, {s1, cli, true}, {s1, s1, true}, {s1, s2, true}, {s1, s3, false},
		{ui, ui, true}, {ui, cli, true}, {ui, s1, false},
		{cli, ui, true}, {cli, s3, false},
	}
	f := newRecFix(t)
	f.ops.Sessions().Connect("s1", "chat-1")
	f.ops.Sessions().Connect("s2", "chat-1")
	f.ops.Sessions().Connect("s3", "chat-2")
	for _, row := range rows {
		name := row.starter.name + " started, " + row.caller.name + " stops"
		rec := mustRecording(t, f.call(t, "device.recordUi", recParams, row.starter.client, row.starter.origin))
		out := f.call(t, "device.stopUiRecording", recParams, row.caller.client, row.caller.origin)
		if row.ok {
			if res := mustStop(t, out); !res.Stopped || res.Recording.ID != rec.ID || res.Recording.State != RecordingEnded {
				t.Errorf("%s: %+v, want it stopped", name, res)
			}
		} else {
			mustFail(t, out, ErrUIRecordingActive)
			if r := f.live(t); r.ID != rec.ID || r.State != RecordingRunning {
				t.Errorf("%s: refused, but the recording is %+v", name, r)
			}
			f.stop(t)
		}
		testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	}
}

// Decision (ADR-0113 §4, ADR-0088 §4: the chat id stays on disk and is never published):
// the start's result, the refused second start's error, the refused stop's error, the stop's
// result and the renderings carry no chat id, while the record on disk keeps it for the stop
// rule. Seen failing with the starter published as stored (the chat id was in startedBy).
func TestRecordingNeverPublishesTheChatID(t *testing.T) {
	const chat = "chat-secret-77"
	f := newRecFix(t)
	f.ops.Sessions().Connect("s1", chat)
	f.ops.Sessions().Connect("s3", "chat-other")
	mcp1 := capability.Origin{ID: "s1", Name: "agent"}
	w := f.watch(t, "ws-1")
	startOut := f.call(t, "device.recordUi", recParams, capability.ClientMCP, mcp1)
	rec := mustRecording(t, startOut)
	var published []string
	note := func(what string, v any) { published = append(published, what+": "+string(marshalJSON(t, v))) }
	note("start result", startOut.Result)
	note("list while running", f.list(t, `{}`))
	_, raw := w.nextState(t, rec.ID, anyState)
	published = append(published, "start event: "+string(raw))
	if rec.StartedBy != (PublishedStarter{Client: "mcp", Session: "s1"}) {
		t.Errorf("startedBy = %+v, want mcp and s1 alone", rec.StartedBy)
	}
	again := f.call(t, "device.recordUi", recParams, capability.ClientMCP, mcp1)
	note("second start error", mustFail(t, again, ErrUIRecordingActive))
	refused := f.call(t, "device.stopUiRecording", recParams, capability.ClientMCP, capability.Origin{ID: "s3"})
	note("refused stop error", mustFail(t, refused, ErrUIRecordingActive))
	published = append(published, "start text: "+uiRecordingText(rec))
	stopOut := f.call(t, "device.stopUiRecording", recParams, capability.ClientUI, capability.Origin{})
	note("stop result", stopOut.Result)
	published = append(published, "stop text: "+stopUIRecordingText(mustStop(t, stopOut)))
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the recording to end", func() bool { return f.adb.LiveRecorders() == 0 })
	_, raw = w.nextState(t, rec.ID, func(r UIRecording) bool { return r.State == RecordingEnded })
	published = append(published, "end event: "+string(raw))
	kept := f.list(t, `{}`)
	note("list after", kept)
	published = append(published, "list text: "+uiRecordingsText(UIRecordingsResult{Recordings: kept}))
	note("late watch snapshot", f.watch(t, "ws-2").snap)
	for _, p := range published {
		if strings.Contains(p, chat) {
			t.Errorf("the chat id was published: %s", p)
		}
	}
	if got := f.endedRecording(t, rec.ID).StartedBy.Chat; got != chat {
		t.Errorf("the record on disk keeps chat %q, want %q for the stop rule", got, chat)
	}
}

// Decision (device.stopUiRecording is idempotent: with nothing running it says so and names
// the newest recording kept): `stopped` is false, and recording is absent until one has been
// kept, then the newest of them. Seen failing with the oldest returned.
func TestStopWithoutRecording(t *testing.T) {
	f := newRecFix(t)
	if res := f.stop(t); res.Stopped || res.Recording != nil {
		t.Fatalf("stop with nothing kept = %+v, want stopped false and no recording", res)
	}
	first := f.start(t, recParams)
	f.stop(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	second := f.start(t, recParams)
	f.stop(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	res := f.stop(t)
	if res.Stopped || res.Recording == nil || res.Recording.ID != second.ID || res.Recording.ID == first.ID || res.Recording.State != RecordingEnded {
		t.Errorf("stop with two kept = %+v, want the newest (%s) ended and stopped false", res, second.ID)
	}
}

// Decision (ADR-0113 §3: StopRecordings sets stopping, ends every recorder daemonExit and a
// later start is DEVICE_UI_DUMP_FAILED): a recording running when the daemon closes ends
// within the bound, and no recording starts afterwards. Seen failing with the flag never set
// (the new recording started).
func TestStopRecordingsRefusesNewStarts(t *testing.T) {
	f := newRecFix(t)
	rec := f.start(t, recParams)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.adb.StopRecordings(ctx)
	if f.adb.LiveRecorders() != 0 {
		t.Errorf("%d recorders live after StopRecordings", f.adb.LiveRecorders())
	}
	if got := f.endedRecording(t, rec.ID); got.EndReason != EndDaemonExit {
		t.Errorf("end %q, want daemonExit", got.EndReason)
	}
	err := mustFail(t, f.call(t, "device.recordUi", recParams, capability.ClientUI, capability.Origin{}), ErrUIDumpFailed)
	if !strings.Contains(err.Cause, "stopping") {
		t.Errorf("cause %q, want it to say the daemon is stopping", err.Cause)
	}
	if ids, _ := f.rec.List(); len(ids) != 1 {
		t.Errorf("%d records after a refused start, want only the first", len(ids))
	}
}

// chromeDoc is a Chrome window whose WebView has children when kids is set.
func chromeDoc(pkg string, kids bool) string {
	child := ""
	if kids {
		child = `<node index="0" text="Article" resource-id="" class="android.view.View" package="com.android.chrome" content-desc="" bounds="[0,100][1080,200]" drawing-order="1" hint="" />`
	}
	return `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">` + "\n" +
		`<node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="` + pkg + `" content-desc="" bounds="[0,0][1080,2400]" drawing-order="0" hint="">` + "\n" +
		`<node index="0" text="" resource-id="" class="android.webkit.WebView" package="` + pkg + `" content-desc="" bounds="[0,0][1080,2400]" drawing-order="1" hint="">` + child + "</node>\n" +
		"</node>\n</hierarchy>\n"
}

// The WebView of chromeSurfaceDoc.
const (
	noWebView = iota
	webViewWithPage
	webViewChildless
)

// chromeSurfaceDoc is a window shaped as Chrome's is on API 36: a toolbar with the URL field,
// and, when surface is set, the SurfaceView the page is drawn on; webView adds Chrome's
// WebView, with a child or without.
func chromeSurfaceDoc(pkg string, surface bool, webView int) string {
	node := func(class, attrs string, kids string) string {
		return `<node index="0" text="" resource-id="" class="` + class + `" package="` + pkg + `" content-desc="" ` + attrs + `bounds="[0,0][1080,200]" drawing-order="1" hint="">` + kids + "</node>\n"
	}
	body := node("android.widget.LinearLayout", "", node("android.widget.EditText", `editable="true" `, "")+node("android.widget.ImageButton", "", ""))
	if surface {
		body += node("android.view.SurfaceView", "", "")
	}
	switch webView {
	case webViewWithPage:
		body += node("android.webkit.WebView", "", node("android.view.View", "", ""))
	case webViewChildless:
		body += node("android.webkit.WebView", "", "")
	}
	return `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">` + "\n" + node("android.widget.FrameLayout", "", body) + "</hierarchy>\n"
}

// Decision (plan m2x A14, step 1: Chrome's WebView with no children is a page not exposed yet,
// and on API 36 so is a window with Chrome's SurfaceView and no WebView at all):
// device.uiDump, device.uiTree and the recorder's dump line warn for Chrome's childless WebView
// and for no other: not a WebView with children, not another app's childless one. Seen failing
// with the package test dropped (another app's childless WebView warned).
func TestChromePageHiddenWarns(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		warn bool
	}{
		{"chrome, childless", chromeDoc("com.android.chrome", false), true},
		{"chrome, with a page", chromeDoc("com.android.chrome", true), false},
		{"another app, childless", chromeDoc("com.example.browser", false), false},
		// API 36: no WebView node at all, the toolbar and the surface the page is drawn on.
		{"chrome API 36, surface and no WebView", chromeSurfaceDoc("com.android.chrome", true, noWebView), true},
		{"chrome API 36, surface and a WebView with a page", chromeSurfaceDoc("com.android.chrome", true, webViewWithPage), false},
		{"chrome API 36, surface and a childless WebView", chromeSurfaceDoc("com.android.chrome", true, webViewChildless), true},
		{"chrome, neither a WebView nor a surface", chromeSurfaceDoc("com.android.chrome", false, noWebView), false},
		{"another app, a surface and no WebView", chromeSurfaceDoc("com.example.browser", true, noWebView), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRecFix(t, c.doc)
			f.r.SetDocs(c.doc)
			dump := mustDump(t, f.dumpCall(t, recParams))
			tree := mustComplete(t, f.tree(t, uiSerial))
			rec := f.start(t, recParams)
			f.stop(t)
			line := ofKind(f.timeline(t, rec.ID), "dump")[0]
			for what, got := range map[string]bool{
				"uiDump":    slices.Contains(dump.Warnings, chromePageHiddenWarning),
				"uiTree":    slices.Contains(tree.Warnings, chromePageHiddenWarning),
				"dump line": line.str("warning") == chromePageHiddenTag,
			} {
				if got != c.warn {
					t.Errorf("%s warns = %v, want %v (uiDump %q, uiTree %q, line %q)", what, got, c.warn, dump.Warnings, tree.Warnings, line.str("warning"))
				}
			}
		})
	}
}

// Decision (ADR-0015 §4, ADR-0113 §7: recordUi authorizes as every device call): a physical
// device the policy does not admit is DEVICE_NOT_ALLOWED, nothing is asked of it and nothing
// is recorded. Seen failing with the authorization skipped (DEVICE_NOT_FOUND from the
// readiness check).
func TestRecordPhysicalNotAllowed(t *testing.T) {
	f := newDevicePolicyFixture(t, nil, "")
	mustFail(t, f.run(t, "device.recordUi", `{"device":"`+physSerial+`"}`), ErrNotAllowed)
	for _, c := range devicetest.Calls(t, f.dir) {
		if strings.HasPrefix(c, "-s "+physSerial) {
			t.Errorf("a command reached the refused device: %q", c)
		}
	}
	if f.adb.LiveRecorders() != 0 {
		t.Errorf("a recorder runs for a refused device")
	}
}

// Decision (ADR-0113 §3: maxSeconds 10-3600): a value outside is refused before any adb
// command, the default is 900.
func TestRecordParameters(t *testing.T) {
	f := newRecFix(t)
	for _, p := range []string{`{"device":"` + uiSerial + `","maxSeconds":9}`, `{"device":"` + uiSerial + `","maxSeconds":3601}`, `{"device":""}`} {
		mustFail(t, f.call(t, "device.recordUi", p, capability.ClientUI, capability.Origin{}), capability.ErrInvalidParams)
	}
	if len(f.calls(t)) != 0 {
		t.Errorf("adb ran for refused parameters: %q", f.calls(t))
	}
	if rec := f.start(t, recParams); rec.MaxSeconds != 900 || rec.Invisible {
		t.Errorf("defaults: maxSeconds %d invisible %v, want 900 and false", rec.MaxSeconds, rec.Invisible)
	}
	f.stop(t)
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the device to be free", func() bool { return f.adb.LiveRecorders() == 0 })
	f.start(t, `{"device":"`+uiSerial+`","invisible":true}`)
	if got := f.r.FullRequests(); got[len(got)-1] != "full invisible" {
		t.Errorf("full dump requests %q: invisible was not asked for", got)
	}
}

// Decision (the hand-off of step 4: recovery runs before any recorder can start, once):
// a record a dead daemon left running is finalised when the first recording starts, and the
// recording that start makes is not (recovery would finalise every record still `recording`,
// a live one's included, so it must not run again). Seen failing with the recovery left out of
// the start (the dead daemon's record stayed running) and with it run on every call (the live
// recording was finalised under its recorder).
func TestRecordRecoversBeforeItStarts(t *testing.T) {
	f := newRecFix(t)
	dead := recID(1)
	w := startRecord(t, f.rec, f.art, dead, time.Now().Add(-time.Hour))
	if err := w.Line(RecordingLine{Kind: "recording", ID: dead, Format: 1}); err != nil {
		t.Fatal(err)
	}
	rec := f.start(t, recParams)
	if got := f.endedRecording(t, dead); got.State != RecordingEnded || got.EndReason != EndDaemonExit {
		t.Errorf("the dead daemon's record is %q %q, want ended daemonExit", got.State, got.EndReason)
	}
	if got := f.endedRecording(t, rec.ID); got.State != RecordingRunning {
		t.Errorf("the live recording's record is %q: recovery finalised it", got.State)
	}
	f.adb.RecoverRecordings() // a second call, as serve makes one: it does nothing
	if got := f.endedRecording(t, rec.ID); got.State != RecordingRunning {
		t.Errorf("a second recovery finalised the live recording")
	}
}

// Decision (ADR-0036 §2: a rendering is the format's specification by example): the
// recording's lines and the stop's, running, ended and with nothing to stop.
func TestRecordingRenderingsAreTheGoldenText(t *testing.T) {
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	end := start.Add(12400 * time.Millisecond)
	ended := UIRecording{
		ID: "20261021T100000Z-0a1b2c3d", Device: "emulator-5554", MaxSeconds: 900, StartedBy: PublishedStarter{Client: "mcp", Session: "s1"},
		State: RecordingEnded, StartedAt: start, EndedAt: &end, EndReason: EndStopped,
		Dumps: 14, Unchanged: 3, Missed: 1, LockWaits: 2, Events: 212, Gaps: 1, Bytes: 1843201, SHA256: "ab12",
		Timeline: artifact.TimelineRef("20261021T100000Z-0a1b2c3d", 1843201),
	}
	running := ended
	running.State, running.EndedAt, running.EndReason = RecordingRunning, nil, ""
	running.StartedAt = time.Now().Add(-5 * time.Second)
	golden(t, "testdata/uirecording-ended.golden", uiRecordingText(ended))
	// A running recording's seconds are live; the line's shape is what the golden holds.
	got := uiRecordingText(running)
	head, _, _ := strings.Cut(got, "\n")
	fields := strings.Fields(head)
	if len(fields) != 11 || fields[3] != "recording" || !strings.HasSuffix(fields[10], "s") {
		t.Errorf("running rendering %q: want 11 words, the state fourth, ending in seconds", head)
	}
	golden(t, "testdata/uirecording-stop-stopped.golden", stopUIRecordingText(StopUIRecordingResult{Stopped: true, Recording: &ended}))
	golden(t, "testdata/uirecording-stop-none.golden", stopUIRecordingText(StopUIRecordingResult{}))
}
