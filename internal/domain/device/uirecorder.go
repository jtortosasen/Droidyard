package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// The recorder of ADR-0113: one goroutine per recording, owned by the daemon, with a context
// of its own. Each tick it reads the ring through ADR-0114's patient reach and writes what it
// finds; when an event came after the previous dump's request and the gap since that dump
// ended has passed, it takes the read lock for the length of one dump, reads with the
// persistent reader only (never the one-shot reader, never `uiautomator dump`), and lets go.

// errTimelineWrite is a write to the timeline that failed for a reason other than its cap.
var errTimelineWrite = errors.New("the timeline could not be written")

// endInfo is why a recording ends, decided once by whoever gets there first. final says the
// recorder takes a last dump first (a stop; not a bound, a failure or the daemon's exit).
type endInfo struct {
	reason, cause string
	final         bool
}

// uiRecorder is one recording.
type uiRecorder struct {
	adb    *ADB
	t      *target
	c      *readerClient
	store  *artifact.Store
	id     string
	serial string
	// invisible and limit are the recording's parameters, fixed at its start.
	invisible bool
	limit     time.Duration

	// ctx is the recorder's own, never the starting call's; cancel is the end, whose reason
	// is end (first writer wins).
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed after the recording is finalised and has left the set

	mu          sync.Mutex
	rec         UIRecord // the live counters; State ended only once finalised
	w           *TimelineWriter
	end         *endInfo
	failedStart bool

	// Everything below is the goroutine's own, once begin returned.
	inst string // the reader instance the ring is read from
	base uint64 // the sequence number the events were read to
	// clockWall is the device clock (ms since the epoch) as the last `events` answer gave it
	// and clockAt when that answer arrived: the device clock now is clockWall plus the time
	// since, so a dump's `time` is within its own duration of the truth.
	clockWall  int64
	clockAt    time.Time
	haveClock  bool
	lastReqDev int64 // the device clock when the previous dump was requested
	lastSHA    string
	haveSHA    bool
	gap        time.Duration // the least time between a dump's end and the next request
	lastEnd    time.Time     // when the previous dump ended
	changed    bool          // an event came after the previous dump's request
	changeAt   time.Time     // when the recorder learned of it
	pauseUntil time.Time     // after a busy answer, when the reader may be asked again
	startMono  time.Time
}

func newUIRecorder(a *ADB, t *target, id string, rec UIRecord, store *artifact.Store) *uiRecorder {
	rec.ID, rec.State = id, RecordingRunning
	ctx, cancel := context.WithCancel(context.Background())
	return &uiRecorder{
		adb: a, t: t, c: a.reader(t.serial), store: store, id: id, serial: t.serial,
		invisible: rec.Invisible, limit: time.Duration(rec.MaxSeconds) * recordSecond,
		ctx: ctx, cancel: cancel, done: make(chan struct{}), rec: rec, gap: recordGapMin,
	}
}

// devStamp is a device-clock time as the timeline writes it: RFC 3339 UTC to the millisecond.
func devStamp(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

func (r *uiRecorder) noteClock(wallMs int64) {
	r.clockWall, r.clockAt, r.haveClock = wallMs, time.Now(), true
}

// clockNow is the device clock now, in ms since the epoch.
func (r *uiRecorder) clockNow() int64 {
	return r.clockWall + time.Since(r.clockAt).Milliseconds()
}

func (r *uiRecorder) bump(f func(*UIRecord)) {
	r.mu.Lock()
	f(&r.rec)
	r.mu.Unlock()
}

func (r *uiRecorder) startedBy() RecordingStarter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.StartedBy
}

// stoppableBy is ADR-0101's reach rule over the starter the record keeps.
func (r *uiRecorder) stoppableBy(who capability.Origin, chatOf func(string) string) bool {
	st := r.startedBy()
	return capability.OwnerReaches(capability.ClientKind(st.Client), st.Session, st.Chat, st.Peer, who, chatOf)
}

func (r *uiRecorder) startFailed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failedStart
}

// summary is the recording as it is published: the live counters and the timeline's size.
func (r *uiRecorder) summary() UIRecording {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := r.rec.Bytes
	if r.rec.State != RecordingEnded && r.w != nil {
		size = r.w.Size()
	}
	return publishedRecording(r.rec, size)
}

// requestEnd records why the recording ends, the first reason wins, and stops the loop. It
// never blocks. It reports whether this call decided the reason: false when the recording was
// already ending.
func (r *uiRecorder) requestEnd(reason, cause string, final bool) (first bool) {
	r.mu.Lock()
	if r.end == nil {
		r.end = &endInfo{reason: reason, cause: cause, final: final}
		first = true
	}
	r.mu.Unlock()
	r.cancel()
	return first
}

func (r *uiRecorder) endInfo() endInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.end == nil {
		return endInfo{reason: EndDaemonExit}
	}
	return *r.end
}

// write appends a line. A line past the cap is artifact.ErrTimelineFull; any other failure
// is errTimelineWrite, the system's message going to the daemon log.
func (r *uiRecorder) write(v any) error {
	err := r.w.Line(v)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, artifact.ErrTimelineFull):
		return err
	}
	r.adb.logf("recording %s: %v", r.id, err)
	return errTimelineWrite
}

// begin makes the recording: where the ring ends and the device clock, the start dump (so a
// reader that cannot run fails the call with nothing created), the record, the timeline, its
// header and the dump. The call's ctx bounds it; the recorder's own goroutine starts after.
func (r *uiRecorder) begin(ctx context.Context) error {
	a := r.adb
	begun := time.Now()
	reach := &eventsReach{a: a, t: r.t, c: r.c, end: begun.Add(eventsLockWait + readerPingCheck + readerStartBound + time.Second)}
	ans, err := r.baseline(ctx, reach)
	if err != nil {
		return err
	}
	r.inst, r.base = ans.Instance, ans.NextSeq-1
	deviceStart := r.clockNow()

	d := r.read(ctx, dumpDeadline)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if d.held {
		return recordStartFailed("another read or action held the device's UI reader for longer than " + dumpDeadline.String())
	}
	if d.kind != dumpOK {
		return startDumpError(d.kind)
	}
	line, err := r.dumpLine(triggerStart, d, d.reqAt.Sub(begun))
	if err != nil {
		return recordStartFailed(uiDumpUnreadable)
	}

	started := time.Now().UTC()
	r.mu.Lock()
	r.rec.StartedAt = started
	rec := r.rec
	r.mu.Unlock()
	fail := func(err error) error {
		a.logf("recording %s: %v", r.id, err)
		a.removeRecordingDir(r.id)
		return recordStartFailed("the recording could not be created in the daemon's state directory")
	}
	if err := a.recordings.Save(rec); err != nil {
		return fail(err)
	}
	h, err := r.store.CreateTimeline(r.id, recordMaxBytes)
	if err != nil {
		return fail(err)
	}
	w := NewTimelineWriter(h)
	w.Reserve(recordMaxBytes, recordEndReserve)
	r.mu.Lock()
	r.w = w
	r.mu.Unlock()
	header := RecordingLine{Kind: "recording", ID: r.id, Device: r.serial, Invisible: rec.Invisible,
		HostStartedAt: started.Format(time.RFC3339Nano), DeviceStartedAt: devStamp(deviceStart), Format: recordFormat}
	if err := w.Line(header); err != nil {
		w.Close()
		return fail(err)
	}
	if err := w.Line(line); err != nil {
		w.Close()
		return fail(err)
	}
	r.bump(func(rec *UIRecord) { rec.Dumps = 1 })
	r.lastReqDev, r.lastSHA, r.haveSHA = d.reqDev, line.SHA256, true
	r.lastEnd = time.Now()
	r.startMono = r.lastEnd
	return nil
}

// abandon undoes a claim whose start failed: nothing was kept.
func (r *uiRecorder) abandon() {
	r.mu.Lock()
	r.failedStart = true
	r.mu.Unlock()
	r.cancel()
	r.adb.recorders.remove(r)
	close(r.done)
}

// baseline learns where the ring ends and the device clock. As device.uiEvents does with no
// cursor, it asks for the newest entry until an answer has returned it, so the reader seals
// it and does not fold the first events of the recording into it.
func (r *uiRecorder) baseline(ctx context.Context, reach *eventsReach) (eventsAnswer, error) {
	ans, err := reach.poll(ctx, 0, 1)
	if err != nil {
		return ans, err
	}
	for try := 0; try < 3 && ans.NextSeq-1 >= ans.OldestSeq; try++ {
		if n := len(ans.Events); n > 0 && ans.Events[n-1].Seq == ans.NextSeq-1 {
			break
		}
		if ans, err = reach.poll(ctx, ans.NextSeq-2, 1); err != nil {
			return ans, err
		}
	}
	r.noteClock(ans.NowWallMs)
	return ans, nil
}

// dumpRead is one try at a dump.
type dumpRead struct {
	doc  []byte
	kind dumpKind
	// held says the read lock stayed held for the whole wait: nothing was asked of the reader.
	held   bool
	reqAt  time.Time
	reqDev int64 // the device clock when it was requested
	dur    time.Duration
}

// read takes the device's read lock, waiting at most lockWait, reads one full dump with the
// persistent reader and lets the lock go. It is the only place the recorder holds the lock.
func (r *uiRecorder) read(ctx context.Context, lockWait time.Duration) dumpRead {
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	err := r.c.acquire(lctx)
	cancel()
	if err != nil {
		return dumpRead{held: true}
	}
	defer r.c.release()
	reqAt := time.Now()
	reqDev := r.clockNow()
	request := readerDumpRequest(nil, &fullOpts{invisible: r.invisible})
	// The persistent reader only: a failure is the reader's, recovered by the next poll.
	doc, kind, _, _, _, _ := r.adb.persistentRead(ctx, r.t, r.c, reqAt, reqAt.Add(readerStartBound+warmReadTimeout), request)
	return dumpRead{doc: doc, kind: kind, reqAt: reqAt, reqDev: reqDev, dur: time.Since(reqAt)}
}

// startDumpError is why a recording that cannot see its first screen is not started. The
// causes are fixed sentences.
func startDumpError(kind dumpKind) error {
	switch kind {
	case dumpAppBusy:
		return recordStartFailed(fullBusyCause)
	case dumpOverCap:
		return recordStartFailed(fullOverCapReason)
	case dumpNoRoot:
		return recordStartFailed(readerNoRootMessage)
	}
	return recordStartFailed("the UI reader did not answer a full dump")
}

// dumpLine is the timeline's line for a dump the reader gave; waited is how long it was due
// before it was requested. A document that does not parse is an error: it is no dump.
func (r *uiRecorder) dumpLine(trigger string, d dumpRead, waited time.Duration) (DumpLine, error) {
	_, roots, err := parseDump(d.doc)
	if err != nil {
		return DumpLine{}, err
	}
	sum := sha256.Sum256(d.doc)
	line := DumpLine{
		Kind: "dump", N: r.dumpsStored() + 1, Time: devStamp(d.reqDev), Ms: int(d.dur.Milliseconds()),
		WaitedMs: int(max(waited, 0).Milliseconds()), Trigger: trigger, Bytes: len(d.doc),
		SHA256: hex.EncodeToString(sum[:]), XML: string(d.doc),
	}
	for _, root := range roots {
		line.Nodes += countNodes(root)
	}
	if len(roots) > 0 {
		line.Foreground = roots[0].pkg
	}
	if chromePageHidden(roots) {
		line.Warning = chromePageHiddenTag
	}
	return line, nil
}

func (r *uiRecorder) dumpsStored() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Dumps
}

// run is the recorder's goroutine: it loops until the recording ends, finishes it, leaves
// the set and only then says it is done.
func (r *uiRecorder) run() {
	defer close(r.done)
	defer r.adb.recorders.remove(r)
	r.loop()
	r.finish()
}

// loop polls every recordTick until something ends the recording.
func (r *uiRecorder) loop() {
	tick := time.NewTicker(recordTick)
	defer tick.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-tick.C:
		}
		if r.step() {
			return
		}
	}
}

// step is one tick: the events since the cursor, then a dump when one is due. It reports
// whether the recording ends (its reason recorded by requestEnd).
func (r *uiRecorder) step() (ended bool) {
	if r.ctx.Err() != nil {
		return true
	}
	if time.Since(r.startMono) >= r.limit {
		r.requestEnd(EndMaxDuration, "", false)
		return true
	}
	// The patient reach (ADR-0114): a lock another caller holds is an empty tick, never a failure.
	reach := &eventsReach{a: r.adb, t: r.t, c: r.c, end: patientEnd(), patient: true}
	err := r.pollEvents(r.ctx, reach, uiStreamPages)
	if r.ctx.Err() != nil {
		return true
	}
	if err != nil {
		if r.endOnError(err) {
			return true
		}
	}
	if at, ok := r.due(); ok {
		if err := r.takeDump(r.ctx, triggerChange, at, recordLockWait); err != nil && r.endOnError(err) {
			return true
		}
	}
	if r.dumpsStored() >= recordMaxDumps {
		r.requestEnd(EndMaxDumps, "", false)
		return true
	}
	return r.ctx.Err() != nil
}

// endOnError ends the recording for an error of the ring or the file, or says it does not:
// a lock held by another caller is a wait that is counted (ADR-0113 §1, lockWaits).
func (r *uiRecorder) endOnError(err error) bool {
	switch {
	case errors.Is(err, errReaderBusy):
		r.bump(func(rec *UIRecord) { rec.LockWaits++ })
		return false
	case errors.Is(err, artifact.ErrTimelineFull):
		r.requestEnd(EndMaxBytes, "", false)
	case errors.Is(err, errTimelineWrite):
		r.requestEnd(EndFailed, recordWriteCause, false)
	default:
		// The patient reach fails only when its own restart of the reader does (ADR-0114
		// Decision 2): the recording ends deviceGone or failed, never retrying.
		reason, cause := readerStopOf(r.ctx, r.adb, r.t, err)
		r.requestEnd(reason, cause, false)
	}
	return true
}

// due reports whether a dump is due now and since when: an event came after the previous
// dump's request, and the gap since that dump ended, and a pause after `busy`, have passed.
func (r *uiRecorder) due() (since time.Time, ok bool) {
	if !r.changed {
		return time.Time{}, false
	}
	since = r.changeAt
	if t := r.lastEnd.Add(r.gap); t.After(since) {
		since = t
	}
	if r.pauseUntil.After(since) {
		since = r.pauseUntil
	}
	return since, !time.Now().Before(since)
}

func (r *uiRecorder) markChanged() {
	if !r.changed {
		r.changed, r.changeAt = true, time.Now()
	}
}

// pollEvents reads up to pages pages of the ring after the cursor and writes them. A reader
// that is not the one the cursor names (it restarted) is read from its oldest entry, and a
// reader the reach started or a ring that moved past the cursor is a gap line, as
// device.watchUiEvents publishes it (watchuievents.go step). Events lost may have changed the
// screen, so a gap makes a dump due.
func (r *uiRecorder) pollEvents(ctx context.Context, reach *eventsReach, pages int) error {
	for page := 0; page < pages; page++ {
		ans, err := reach.poll(ctx, r.base, eventsMaxLimit)
		if err != nil {
			return err
		}
		r.noteClock(ans.NowWallMs)
		restarted := false
		inst, base := r.inst, r.base
		for changes := 0; ans.Instance != inst || base > ans.NextSeq-1; changes++ {
			if changes >= eventsInstanceMax {
				return eventsFailed("the reader behind the device's forward keeps changing")
			}
			restarted = true
			inst, base = ans.Instance, 0
			if ans, err = reach.poll(ctx, 0, eventsMaxLimit); err != nil {
				return err
			}
			r.noteClock(ans.NowWallMs)
		}
		r.inst, r.base = inst, base
		var gap *UIEventsGap
		switch {
		case reach.started:
			gap = &UIEventsGap{Reason: gapStarted}
			reach.started = false
		case restarted:
			gap = &UIEventsGap{Reason: gapRestarted}
		case ans.OldestSeq > r.base+1:
			gap = &UIEventsGap{Reason: gapOverflow, Dropped: int(ans.OldestSeq - 1 - r.base)}
		}
		if gap != nil {
			if err := r.write(GapLine{Kind: "gap", Reason: gap.Reason, Dropped: gap.Dropped}); err != nil {
				return err
			}
			r.bump(func(rec *UIRecord) { rec.Gaps++ })
			r.markChanged()
		}
		last := ans.NextSeq - 1
		for _, ev := range ans.Events {
			if err := r.write(EventLine{Kind: "event", UIEvent: uiEventOf(ev)}); err != nil {
				return err
			}
			r.bump(func(rec *UIRecord) { rec.Events++ })
			r.base = ev.Seq
			last = ev.Seq
			if ev.TimeMs > r.lastReqDev {
				r.markChanged()
			}
		}
		r.base = last
		if last >= ans.NextSeq-1 {
			break
		}
	}
	return nil
}

// takeDump takes one dump and writes what it found. The reader's failures are not the
// recording's: a lock held is counted and the dump stays due; a reader that is gone writes
// nothing and the next poll recovers it; a dump the reader could not give is a `missed` line.
// An error returned is the file's (the cap, or a write failure). due is when the dump became
// due, for waitedMs.
func (r *uiRecorder) takeDump(ctx context.Context, trigger string, due time.Time, lockWait time.Duration) error {
	d := r.read(ctx, lockWait)
	if ctx.Err() != nil {
		if !d.held {
			// The end cut a dump off: the reader's worker for it keeps running on the device,
			// so it is left alone for the pause, as after a busy answer (ADR-0034 §1).
			r.pauseUntil = time.Now().Add(recordBusyPause)
		}
		return nil
	}
	if d.held {
		r.bump(func(rec *UIRecord) { rec.LockWaits++ })
		if trigger == triggerStop {
			return r.missed(r.clockNow(), missedLockHeld)
		}
		return nil
	}
	now := time.Now()
	switch d.kind {
	case dumpOK:
		r.lastReqDev, r.lastEnd = d.reqDev, now
		return r.keepDump(trigger, d, due)
	case dumpAppBusy:
		// The app did not answer within the reader's bound; the reader is alive with a worker
		// left to finish the dump, so it is left alone for recordBusyPause. The screen is
		// still unrecorded: the dump stays due.
		r.lastReqDev, r.lastEnd, r.pauseUntil = d.reqDev, now, now.Add(recordBusyPause)
		return r.missed(d.reqDev, missedBound)
	case dumpOverCap:
		r.lastReqDev, r.lastEnd, r.changed, r.gap = d.reqDev, now, false, recordGapMin
		return r.missed(d.reqDev, missedTooLarge)
	case dumpNoRoot, dumpOther:
		r.lastReqDev, r.lastEnd, r.changed, r.gap = d.reqDev, now, false, recordGapMin
		return r.missed(d.reqDev, missedUnreadable)
	}
	// dumpVanished, dumpUnreachable: the reader is gone or wedged. No line; the next tick's
	// poll recovers it (and writes the gap), or ends the recording when it cannot.
	r.lastEnd = now
	return nil
}

// keepDump stores a dump that is not identical to the one before it; an identical one is
// counted and the gap doubles (ADR-0113 §1).
func (r *uiRecorder) keepDump(trigger string, d dumpRead, due time.Time) error {
	line, err := r.dumpLine(trigger, d, d.reqAt.Sub(due))
	if err != nil {
		r.changed, r.gap = false, recordGapMin
		return r.missed(d.reqDev, missedUnreadable)
	}
	r.changed = false
	if r.haveSHA && line.SHA256 == r.lastSHA {
		r.bump(func(rec *UIRecord) { rec.Unchanged++ })
		r.gap = min(r.gap*2, recordGapMax)
		return nil
	}
	if err := r.write(line); err != nil {
		return err
	}
	r.bump(func(rec *UIRecord) { rec.Dumps++ })
	r.lastSHA, r.haveSHA, r.gap = line.SHA256, true, recordGapMin
	return nil
}

// missed writes a `missed` line for a dump that was lost.
func (r *uiRecorder) missed(devMs int64, reason string) error {
	if err := r.write(MissedLine{Kind: "missed", Time: devStamp(devMs), Reason: reason}); err != nil {
		return err
	}
	r.bump(func(rec *UIRecord) { rec.Missed++ })
	return nil
}

// drain reads the events that remain, for at most d. Its errors are not the recording's: it
// has ended already.
func (r *uiRecorder) drain(ctx context.Context, d time.Duration) {
	dctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	reach := &eventsReach{a: r.adb, t: r.t, c: r.c, end: time.Now().Add(d), patient: true}
	_ = r.pollEvents(dctx, reach, uiStreamPages)
}

// finish ends the recording: the events that remain, a last dump for a stop, the `end` line,
// the timeline closed (its size and SHA-256 into the record), the record finalised, the
// retention swept. It uses a context that outlives the one that ended the loop.
func (r *uiRecorder) finish() {
	info := r.endInfo()
	fctx := context.WithoutCancel(r.ctx)
	// The device is there to ask unless the recording ended because it was not.
	deviceThere := info.reason != EndDeviceGone && info.reason != EndFailed
	// A daemon exit has less time than a stop: Close gives the recordings recordStopAllWait.
	drain := recordDrain
	if info.reason == EndDaemonExit {
		drain /= 2
	}
	if deviceThere {
		r.drain(fctx, drain)
	}
	if info.final && r.dumpsStored() < recordMaxDumps {
		if now := time.Now(); now.Before(r.pauseUntil) {
			// In the pause after a busy answer, or after a dump the end cut off: the reader may
			// still be reading one, and a second dump would take its other worker (ADR-0113 §1).
			// The last screen is not recorded, and the timeline says so.
			_ = r.missed(r.clockNow(), missedPaused)
		} else {
			_ = r.takeDump(fctx, triggerStop, now, recordStopLockWait)
		}
		r.drain(fctx, drain)
	}
	end := EndLine{Kind: "end", HostTime: time.Now().UTC().Format(time.RFC3339Nano), Reason: info.reason, Cause: info.cause}
	if deviceThere && r.haveClock {
		end.Time = devStamp(r.clockNow())
	}
	// The end line may use the room the cap kept for it, so a recording that reached the cap
	// still ends with one (ADR-0113 §5).
	if err := r.w.End(end); err != nil {
		r.adb.logf("recording %s: the end line: %v", r.id, err)
	}
	if err := r.w.Close(); err != nil {
		r.adb.logf("recording %s: closing the timeline: %v", r.id, err)
	}
	ended := time.Now().UTC()
	r.mu.Lock()
	rec := r.rec
	r.mu.Unlock()
	rec.EndedAt, rec.EndReason, rec.Cause = &ended, info.reason, info.cause
	rec.Bytes, rec.SHA256 = r.w.Size(), r.w.SHA256()
	if err := r.adb.recordings.Finalise(rec); err != nil {
		r.adb.logf("recording %s: finalising: %v", r.id, err)
	}
	rec.State = RecordingEnded
	r.mu.Lock()
	r.rec = rec
	r.mu.Unlock()
	if _, err := r.adb.recordings.Sweep(); err != nil {
		r.adb.logf("recordings: sweep: %v", err)
	}
}
