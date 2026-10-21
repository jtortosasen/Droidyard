package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// A recording of a remote device, from the client's side (plan m2x step 10; ADR-0115
// Decisions 2 to 5, ADR-0100 §2 to §4 and §6). The recorder runs on the host: the client's
// device.recordUi and device.stopUiRecording on <remote>/<serial> are relayed, and the client
// keeps a record of its own (its own id and reference, `remote`, `hostId`, `device` the remote
// id) that mirrors the host's counts and end. A supervisor per remote, alive only while a
// recording of that remote runs or has a timeline still to copy, keeps one relayed
// device.watchUiRecordings open while one runs, ends the client's record when the link is lost,
// and, when the host's recording has ended, copies its timeline by ranges into
// timeline.jsonl.part and renames it only when the host's SHA-256 matched (copy below). Until
// then the client's reference reads `growing` (artifact.Store: a remote record that is not
// `copied`), so nothing reads a remote timeline as complete before its digest matched. The
// client runs no adb command for any of it (ADR-0100 §6).

// The supervisor's and the copy's timings and sizes; variables so a test is quick.
var (
	// remoteRecPoll is how often a supervisor looks again: for a link that is down, for the host's
	// summary of a recording whose end it does not know yet, and for a copy to retry.
	remoteRecPoll = 2 * time.Second
	// remoteRecResync is how often it lists the host's recordings while only a watch tells it
	// anything, in case an event was dropped.
	remoteRecResync = 30 * time.Second
	// remoteRecWatchStart bounds device.watchUiRecordings on the host; remoteRecCleanup the call
	// that ends its subscription.
	remoteRecWatchStart = 30 * time.Second
	remoteRecCleanup    = 5 * time.Second
	// copyRange is the size of one ranged fetch of the host's timeline (ADR-0115 Decision 5).
	copyRange int64 = 8 << 20
	// copyRetry is how long after a copy that could not go on the next attempt waits.
	copyRetry = time.Second
	// hostDeleteWait bounds the call that deletes the host's recording.
	hostDeleteWait = 10 * time.Second
	// lostStartTTL is how long a start whose answer was lost is looked for on a host that
	// answers, counted from the first listing of the host's recordings after the start and not
	// from the start: a link that stays down keeps the search for as long as it takes. It
	// outlasts the longest recording, so a match still running when first listed is adopted
	// when it ends. lostStartSkew is how much earlier than the start the host's clock may read.
	lostStartTTL  = recordSecondsMax*time.Second + 5*time.Minute
	lostStartSkew = 10 * time.Minute
	// copyRetryMax bounds the backoff of a copy the host keeps failing with a status that is
	// neither "still running" (409) nor "gone": the wait doubles from copyRetry up to it.
	copyRetryMax = 30 * time.Second
	// remoteGoneAfter is how long a remote that is not in the configuration is waited for (a
	// daemon that has just started has no links yet) before the copies it holds are given up.
	remoteGoneAfter = 30 * time.Second
)

// The causes of a remote recording whose timeline will not be copied; fixed sentences, never
// the host's words.
const (
	causeCopyMismatch = "the timeline copied from the host did not match the host's SHA-256; the host still holds the recording"
	causeHostGone     = "the host no longer holds it"
	causeCopyRefused  = "the host did not serve the timeline as this daemon expects; the host still holds the recording"
	causeCopyBad      = "the host's summary of the recording is not one this daemon can copy; the host still holds it"
	causeRemoteGone   = "the remote was removed from this daemon's configuration; its host may still hold the recording"
)

// copyPart is the file a timeline is copied into.
const copyPart = artifact.TimelineFile + ".part"

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// lentRecordUI is device.recordUi or device.stopUiRecording made relayable (plain), with the
// handler of a remote id replaced by remote (recordRemote, stopRemote). declared are the codes
// the descriptor lists, the ones a host's answer may keep.
func (a *ADB) lentRecordUI(d capability.Descriptor, remote func(ctx context.Context, call *capability.Call, rt *remoteTarget, declared []capability.ErrorCode) (any, error)) capability.Descriptor {
	d = a.plain(d)
	handler, declared := d.Handler, d.Errors
	d.Handler = func(ctx context.Context, call *capability.Call) (any, error) {
		if rt, ok := call.Authorized.(*remoteTarget); ok {
			return remote(ctx, call, rt, declared)
		}
		return handler(ctx, call)
	}
	return d
}

// lostStart is a relayed start whose answer was lost: the host may have started the recording,
// which the next link up looks for (adoption).
type lostStart struct {
	seq     uint64
	id      string // the remote id
	serial  string
	starter RecordingStarter
	since   time.Time
	// listed is when the host's recordings were first listed after the start, zero until then:
	// the search is bounded from it (lostStartTTL), never from the start.
	listed time.Time
}

// remoteRecs is the client's state for the recordings of remote devices. The zero value is
// ready. mu guards every field and every read-modify-write of a remote record, so a delete, a
// copy's rename and a mirrored event never interleave.
type remoteRecs struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopping bool
	sup      map[string]*recSupervisor // by remote name
	live     map[string]bool           // client ids a session of this process mirrors
	lost     map[string][]lostStart    // by remote name
	lostSeq  uint64
	copying  map[string]context.CancelFunc      // by client id: the copy in progress
	hook     atomic.Pointer[func(off, n int64)] // test seam: see SetCopyHook
}

// SetCopyHook makes f called before each ranged fetch of a remote recording's timeline, with
// the range's offset and length (nil removes it): a test seam to see where a resumed copy
// continues, and to cut the link at a chosen range.
func (a *ADB) SetCopyHook(f func(off, n int64)) {
	if f == nil {
		a.remoteRec.hook.Store(nil)
		return
	}
	a.remoteRec.hook.Store(&f)
}

// SetRemoteRecordingTimings shortens the supervisors' poll and the copy's retry delay, and sets
// the size of a ranged fetch, for a test; the returned function restores them. Called before
// the daemons of the test start.
func SetRemoteRecordingTimings(poll, retry time.Duration, rangeBytes int64) (restore func()) {
	oldPoll, oldRetry, oldRange := remoteRecPoll, copyRetry, copyRange
	remoteRecPoll, copyRetry, copyRange = poll, retry, rangeBytes
	return func() { remoteRecPoll, copyRetry, copyRange = oldPoll, oldRetry, oldRange }
}

// SetRemoteRecordingLimits shortens how long a start whose answer was lost is looked for, and how
// long a remote missing from the configuration is waited for, for a test; the returned function
// restores them.
func SetRemoteRecordingLimits(lostTTL, goneAfter time.Duration) (restore func()) {
	oldTTL, oldGone := lostStartTTL, remoteGoneAfter
	lostStartTTL, remoteGoneAfter = lostTTL, goneAfter
	return func() { lostStartTTL, remoteGoneAfter = oldTTL, oldGone }
}

// context is the supervisors' context, made on first use.
func (rr *remoteRecs) context() context.Context {
	if rr.ctx == nil {
		rr.ctx, rr.cancel = context.WithCancel(context.Background())
	}
	return rr.ctx
}

// ensureSupervisor makes sure the remote has a supervisor, and wakes it.
func (a *ADB) ensureSupervisor(remote string) {
	rr := &a.remoteRec
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.stopping {
		return
	}
	if s := rr.sup[remote]; s != nil {
		select {
		case s.kick <- struct{}{}:
		default:
		}
		return
	}
	if rr.sup == nil {
		rr.sup = map[string]*recSupervisor{}
	}
	s := &recSupervisor{a: a, remote: remote, kick: make(chan struct{}, 1)}
	rr.sup[remote] = s
	rr.wg.Add(1)
	go s.run(rr.context())
}

// remoteName is the remote a record's device id names, "" when it is not a remote id.
func remoteName(device string) string {
	name, serial, ok := strings.Cut(device, "/")
	if !ok || serial == "" {
		return ""
	}
	return name
}

// pendingCopy reports whether rec is a remote record whose timeline is still to come.
func pendingCopy(rec UIRecord) bool { return rec.Remote && !rec.Copied && !rec.CopyGaveUp }

// remoteWork is the records of the remote that need its supervisor: running, or ended with the
// copy still to do.
func (a *ADB) remoteWork(remote string) []UIRecord {
	if a.recordings == nil {
		return nil
	}
	recs, err := a.recordings.List()
	if err != nil {
		return nil
	}
	var out []UIRecord
	for _, rec := range recs {
		if rec.Remote && remoteName(rec.Device) == remote && (rec.State == RecordingRunning || pendingCopy(rec)) {
			out = append(out, rec)
		}
	}
	return out
}

// hasWorkLocked reports whether the remote needs its supervisor: a record to mirror or copy, or
// a start whose answer was lost and not yet settled (adopt settles it). rr.mu is held.
func (a *ADB) hasWorkLocked(remote string) bool {
	rr := &a.remoteRec
	if len(rr.lost[remote]) > 0 {
		return true
	}
	delete(rr.lost, remote)
	return len(a.remoteWork(remote)) > 0
}

// resumeRemoteRecordings wakes a supervisor for every remote that has a record to mirror or to
// copy: what a daemon that restarted finds in its state (RecoverRecordings).
func (a *ADB) resumeRemoteRecordings() {
	if a.recordings == nil || a.remotes == nil {
		return
	}
	recs, err := a.recordings.List()
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, rec := range recs {
		if name := remoteName(rec.Device); rec.Remote && name != "" && !seen[name] && (rec.State == RecordingRunning || pendingCopy(rec)) {
			seen[name] = true
			a.ensureSupervisor(name)
		}
	}
}

// stopRemoteRecordings ends the supervisors and the copies, waits for them within ctx, and
// ends as daemonExit the records this process still mirrors (the host ends the recordings
// with the link; the next start takes its final reason). It is called by StopRecordings, which
// refuses later starts first.
func (a *ADB) stopRemoteRecordings(ctx context.Context) {
	rr := &a.remoteRec
	rr.mu.Lock()
	rr.stopping = true
	cancel := rr.cancel
	rr.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { rr.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	rr.mu.Lock()
	var ids []string
	for id := range rr.live {
		ids = append(ids, id)
	}
	rr.live = nil
	rr.mu.Unlock()
	for _, id := range ids {
		a.endMirrored(id, EndDaemonExit, "")
	}
}

// ---- the client's calls -------------------------------------------------------------

// clientRecordOf is the client's record of the host's recording hostID of the remote, if any.
func (a *ADB) clientRecordOf(remote, hostID string) (UIRecord, bool) {
	if a.recordings == nil || hostID == "" {
		return UIRecord{}, false
	}
	recs, err := a.recordings.List()
	if err != nil {
		return UIRecord{}, false
	}
	for _, rec := range recs {
		if rec.Remote && rec.HostID == hostID && remoteName(rec.Device) == remote {
			return rec, true
		}
	}
	return UIRecord{}, false
}

// runningRemoteOn is the client's running record of the remote device id, if any.
func (a *ADB) runningRemoteOn(id string) (UIRecord, bool) {
	if a.recordings == nil {
		return UIRecord{}, false
	}
	recs, err := a.recordings.List()
	if err != nil {
		return UIRecord{}, false
	}
	for _, rec := range recs {
		if rec.Remote && rec.Device == id && rec.State == RecordingRunning {
			return rec, true
		}
	}
	return UIRecord{}, false
}

// activeError is a host's DEVICE_UI_RECORDING_ACTIVE in this daemon's terms: the recording it
// names is the client's own id when the client holds a record of it, and no id at all when it
// does not: the host's reference means nothing here.
func (a *ADB) activeError(err error, rt *remoteTarget) error {
	var ce *capability.Error
	if !errors.As(err, &ce) || ce.Code != ErrUIRecordingActive {
		return err
	}
	out := *ce
	out.Details = nil
	out.NextStep = "Stop it with device.stopUiRecording, then start again, or try again later."
	if hostID, _ := ce.Details["id"].(string); hostID != "" {
		if rec, ok := a.clientRecordOf(rt.remote, hostID); ok {
			out.Details = map[string]any{"id": rec.ID}
			out.NextStep = "Stop it with device.stopUiRecording, then start again; its timeline is the artifact r." + rec.ID + "."
		}
	}
	return &out
}

// recordRemote is device.recordUi on a remote id: the host records, the client keeps its own
// record of it and returns its own reference, never the host's.
func (a *ADB) recordRemote(ctx context.Context, call *capability.Call, rt *remoteTarget, declared []capability.ErrorCode) (any, error) {
	p := call.Params.(UIRecordParams)
	maxSeconds, err := p.validate()
	if err != nil {
		return nil, err
	}
	if a.recordings == nil {
		return nil, recordStartFailed("the daemon has no state directory to record into")
	}
	a.RecoverRecordings()
	if existing, ok := a.runningRemoteOn(rt.id); ok {
		return nil, capability.NewError(ErrUIRecordingActive, "a recording is already running on "+rt.id).
			WithDetails(map[string]any{"id": existing.ID}).
			WithNextStep("Stop it with device.stopUiRecording, then start again; its timeline is the artifact r." + existing.ID + ".")
	}
	session, chat, peer := capability.OwnerStamp(call.Origin.Client, call.Origin, a.chatOf())
	starter := RecordingStarter{Client: string(call.Origin.Client), Session: session, Chat: chat, Peer: peer}
	asked := time.Now()
	res, err := a.relay(ctx, call, "device.recordUi", UIRecording{}, declared, rt)
	if err != nil {
		var ce *capability.Error
		if ctx.Err() != nil || (errors.As(err, &ce) && ce.Code == ErrDisconnected) {
			// The answer may have been lost with the host's recording started: the next link up
			// looks for it.
			a.noteLostStart(rt, starter, asked)
		}
		return nil, a.activeError(err, rt)
	}
	hs := res.(UIRecording)
	if !artifact.ValidTimelineID(hs.ID) || (hs.State != RecordingRunning && hs.State != RecordingEnded) {
		return nil, incompatible(rt, "it named a recording this version cannot read")
	}
	now := time.Now().UTC()
	rec := UIRecord{
		ID: newRecordingID(now), Device: rt.id, Remote: true, HostID: hs.ID,
		Invisible: p.Invisible, MaxSeconds: maxSeconds, StartedBy: starter,
		State: RecordingRunning, StartedAt: now,
	}
	if t, ok := hostTime(&hs.StartedAt, now); ok {
		rec.StartedAt = t
	}
	applyHostSummary(&rec, hs, rt.remote)
	rr := &a.remoteRec
	rr.mu.Lock()
	if rr.stopping {
		rr.mu.Unlock()
		return nil, recordStartFailed("the daemon is stopping, so it starts no recording")
	}
	if rec.State == RecordingRunning {
		if rr.live == nil {
			rr.live = map[string]bool{}
		}
		rr.live[rec.ID] = true
	}
	err = a.recordings.Save(rec)
	if err != nil {
		delete(rr.live, rec.ID)
	}
	rr.mu.Unlock()
	if err != nil {
		return nil, err
	}
	a.ensureSupervisor(rt.remote)
	sum := publishedRecording(rec, rec.Bytes)
	a.publishRecording(rec.StartedBy, sum)
	return sum, nil
}

// noteLostStart remembers a start whose answer was lost, so the next link up looks for the
// recording on the host.
func (a *ADB) noteLostStart(rt *remoteTarget, starter RecordingStarter, since time.Time) {
	rr := &a.remoteRec
	rr.mu.Lock()
	if rr.lost == nil {
		rr.lost = map[string][]lostStart{}
	}
	rr.lostSeq++
	rr.lost[rt.remote] = append(rr.lost[rt.remote], lostStart{seq: rr.lostSeq, id: rt.id, serial: rt.serial, starter: starter, since: since})
	rr.mu.Unlock()
	a.ensureSupervisor(rt.remote)
}

// stopRemote is device.stopUiRecording on a remote id: the client's own reach rule over the
// record it holds, then the host's stop; the answer names the client's record.
func (a *ADB) stopRemote(ctx context.Context, call *capability.Call, rt *remoteTarget, declared []capability.ErrorCode) (any, error) {
	if rec, ok := a.runningRemoteOn(rt.id); ok {
		if !capability.OwnerReaches(capability.ClientKind(rec.StartedBy.Client), rec.StartedBy.Session, rec.StartedBy.Chat, rec.StartedBy.Peer, call.Origin, a.chatOf()) {
			return nil, notYoursToStop(rt.id, rec.ID, call.Origin)
		}
	}
	res, err := a.relay(ctx, call, "device.stopUiRecording", StopUIRecordingResult{}, declared, rt)
	if err != nil {
		return nil, a.activeError(err, rt)
	}
	out := res.(StopUIRecordingResult)
	result := StopUIRecordingResult{Stopped: out.Stopped}
	switch hs := out.Recording; {
	case hs == nil:
		// The host holds none (it deletes its copy once this daemon has the timeline): the
		// newest the client keeps, when nothing was stopped.
		if !out.Stopped {
			result.Recording = a.nothingToStop(rt.id, call.Origin).Recording
		}
	case artifact.ValidTimelineID(hs.ID):
		// A recording of the host's that the client holds no record of (a start whose answer
		// was lost, not adopted yet) is not named: the host's reference means nothing here.
		if rec, ok := a.mirrorHost(rt.remote, *hs); ok {
			s := publishedRecording(rec, rec.Bytes)
			result.Recording = &s
		}
	}
	return result, nil
}

// deleteRemote deletes the client's record of a remote recording: refused while it runs, and
// before its copy it also deletes the host's (best effort: a host that cannot be asked keeps it
// until its own retention). The caller holds delMu.
func (a *ADB) deleteRemote(rec UIRecord) (DeleteUIRecordingResult, error) {
	if rec.State == RecordingRunning {
		return DeleteUIRecordingResult{}, capability.NewError(ErrUIRecordingActive, "the recording is still running").
			WithDetails(map[string]any{"id": rec.ID}).
			WithNextStep("Stop it with device.stopUiRecording, then delete it.")
	}
	rr := &a.remoteRec
	rr.mu.Lock()
	if cancel := rr.copying[rec.ID]; cancel != nil {
		cancel()
	}
	delete(rr.live, rec.ID)
	err := a.recordings.Delete(rec.ID)
	if err == nil {
		// Under the lock mutate publishes under: no `state` of this record follows it.
		a.announceRecording(rec.StartedBy, EventDeleted, UIRecordingDeleted{ID: rec.ID})
	}
	rr.mu.Unlock()
	if err != nil {
		return DeleteUIRecordingResult{}, err
	}
	if !rec.Copied && rec.HostID != "" {
		a.deleteOnHost(rec.Device, rec.HostID)
	}
	return DeleteUIRecordingResult{Deleted: true}, nil
}

// deleteOnHost asks the host of the remote device id to delete its recording hostID, best
// effort.
func (a *ADB) deleteOnHost(device, hostID string) {
	link, remote, _, ok := a.splitRemoteID(device)
	if !ok || !link.Connected() {
		a.logf("recordings: the host's recording %s of %s was not deleted: the link to the remote is down", hostID, device)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostDeleteWait)
	defer cancel()
	params, _ := json.Marshal(DeleteUIRecordingParams{ID: hostID})
	resp, err := callHost(ctx, link, "device.deleteUiRecording", params)
	if err != nil || resp.Error != nil {
		a.logf("recordings: the host %s's recording %s was not deleted", remote, hostID)
	}
}

// ---- records ---------------------------------------------------------------------------

// mutate reads the client's record id, applies f and, when f says it changed, saves it and
// publishes it, all under rr.mu: a change reaches the watchers in the order the records were
// written, and never after the `deleted` of a record deleteRemote removed. found is false when
// the record is gone (deleted meanwhile): nothing is written. A record that could not be saved
// is returned as f left it with the error, and is not published.
func (a *ADB) mutate(id string, f func(*UIRecord) bool) (rec UIRecord, found bool, err error) {
	rr := &a.remoteRec
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rec, found, gerr := a.recordings.Get(id)
	if gerr != nil || !found {
		return UIRecord{}, false, nil
	}
	if f(&rec) {
		if err := a.recordings.Save(rec); err != nil {
			a.logf("recordings: the record of %s was not saved: %v", id, err)
			return rec, true, err
		}
		a.publishRecord(rec)
	}
	return rec, true, nil
}

// publishRecord tells the watchers where a client record stands.
func (a *ADB) publishRecord(rec UIRecord) {
	a.publishRecording(rec.StartedBy, publishedRecording(rec, rec.Bytes))
}

// mirrorHost folds the host's summary hs of one of its recordings into the client's record of
// it (found false when the client holds none), saves and publishes it, and wakes the copy.
func (a *ADB) mirrorHost(remote string, hs UIRecording) (UIRecord, bool) {
	known, ok := a.clientRecordOf(remote, hs.ID)
	if !ok {
		return UIRecord{}, false
	}
	var before UIRecord
	rec, found, _ := a.mutate(known.ID, func(r *UIRecord) bool {
		before = *r
		applyHostSummary(r, hs, remote)
		return !reflect.DeepEqual(*r, before)
	})
	if !found {
		return UIRecord{}, false
	}
	if !reflect.DeepEqual(rec, before) {
		if rec.State == RecordingEnded {
			a.remoteRec.mu.Lock()
			delete(a.remoteRec.live, rec.ID)
			a.remoteRec.mu.Unlock()
		}
		if pendingCopy(rec) && rec.State == RecordingEnded {
			a.ensureSupervisor(remote)
		}
	}
	return rec, true
}

// hostEndReason is a host's end reason when it is one of the published ones, failed otherwise.
func hostEndReason(r string) string {
	switch r {
	case EndStopped, EndMaxDuration, EndMaxDumps, EndMaxBytes, EndDeviceGone, EndFailed, EndDaemonExit:
		return r
	}
	return EndFailed
}

// endCause is the daemon's own words for why a remote recording ended as reason, when the
// reason needs one; the host's cause text is never repeated.
func endCause(reason, remote string) string {
	switch reason {
	case EndDeviceGone:
		return "the device on the host " + remote + " is gone, or the host ended the lease"
	case EndFailed:
		return "the recording failed on the host " + remote
	}
	return ""
}

func nonNeg(n int) int { return max(n, 0) }

// hostTime is a time the host sent, when it is one this daemon keeps: set, no later than a
// minute after now, and no earlier than a day before it. The host is another machine, and what
// it sends sorts the client's list.
func hostTime(t *time.Time, now time.Time) (time.Time, bool) {
	if t == nil || t.IsZero() || t.After(now.Add(time.Minute)) || t.Before(now.Add(-24*time.Hour)) {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// applyHostSummary folds the host's summary hs into the client's record rec of it. A running
// record mirrors the counts, and ends when the host's recording has. An ended one whose copy is
// still to do takes the host's final reason, counts, size and digest, which the copy checks
// against. A summary of an ended recording that cannot be copied (a digest that is not one, a
// size past the host's own bound) gives the copy up.
func applyHostSummary(rec *UIRecord, hs UIRecording, remote string) {
	if rec.State == RecordingEnded && !pendingCopy(*rec) {
		return
	}
	rec.Dumps, rec.Unchanged, rec.Missed = nonNeg(hs.Dumps), nonNeg(hs.Unchanged), nonNeg(hs.Missed)
	rec.LockWaits, rec.Events, rec.Gaps = nonNeg(hs.LockWaits), nonNeg(hs.Events), nonNeg(hs.Gaps)
	rec.Bytes = min(max(hs.Bytes, 0), recordMaxBytes)
	if hs.State != RecordingEnded {
		return
	}
	reason := hostEndReason(hs.EndReason)
	if rec.EndReason != reason || rec.Cause == "" {
		rec.Cause = endCause(reason, remote)
	}
	rec.State, rec.EndReason = RecordingEnded, reason
	if t, ok := hostTime(hs.EndedAt, time.Now()); ok {
		rec.EndedAt = &t
	} else if rec.EndedAt == nil {
		t := time.Now().UTC()
		rec.EndedAt = &t
	}
	if !sha256Hex.MatchString(hs.SHA256) || hs.Bytes <= 0 || hs.Bytes > recordMaxBytes {
		rec.CopyGaveUp, rec.EndReason, rec.Cause = true, EndFailed, causeCopyBad
		return
	}
	rec.SHA256 = hs.SHA256
}

// endMirrored ends the client's running record id, which no summary of the host's can end now
// (the link is lost, or this daemon exits), with reason and cause.
func (a *ADB) endMirrored(id, reason, cause string) {
	a.mutate(id, func(r *UIRecord) bool {
		if r.State != RecordingRunning {
			return false
		}
		t := time.Now().UTC()
		r.State, r.EndReason, r.Cause, r.EndedAt = RecordingEnded, reason, cause, &t
		return true
	})
	a.remoteRec.mu.Lock()
	delete(a.remoteRec.live, id)
	a.remoteRec.mu.Unlock()
}

// giveUpCopy marks the client's record id as one whose timeline will not be copied, ended
// failed with the fixed cause, and removes the half-copied file when discard says so.
func (a *ADB) giveUpCopy(id, cause string, discard bool) {
	a.mutate(id, func(r *UIRecord) bool {
		if !pendingCopy(*r) {
			return false
		}
		t := time.Now().UTC()
		if r.EndedAt == nil {
			r.EndedAt = &t
		}
		r.State, r.EndReason, r.Cause, r.CopyGaveUp = RecordingEnded, EndFailed, cause, true
		if discard {
			// Before the record says so: nothing is left of a copy that was given up once
			// anyone can read that it was.
			os.Remove(filepath.Join(a.recordings.dir(id), copyPart))
		}
		return true
	})
	a.remoteRec.mu.Lock()
	delete(a.remoteRec.live, id)
	a.remoteRec.mu.Unlock()
}

// ---- the supervisor ------------------------------------------------------------------

// recSupervisor serves the recordings of one remote while it has any to mirror or copy.
type recSupervisor struct {
	a      *ADB
	remote string
	kick   chan struct{}
}

// run is the supervisor's life: it waits for the link, serves it until it is lost, and ends
// when the remote has nothing left to mirror or copy.
func (s *recSupervisor) run(ctx context.Context) {
	rr := &s.a.remoteRec
	defer rr.wg.Done()
	s.endStale()
	var missingSince time.Time
	for ctx.Err() == nil {
		rr.mu.Lock()
		if !s.a.hasWorkLocked(s.remote) {
			delete(rr.sup, s.remote)
			rr.mu.Unlock()
			return
		}
		rr.mu.Unlock()
		var link *hosting.Link
		if s.a.remotes != nil {
			link = s.a.remotes.Remote(s.remote)
		}
		if link != nil || !remotesRead(s.a.remotes) {
			missingSince = time.Time{}
		} else if missingSince.IsZero() {
			missingSince = time.Now()
		} else if time.Since(missingSince) >= remoteGoneAfter {
			// No link for this remote for long enough that it is not one still to start (a
			// daemon that has just started has none yet): it was removed from the configuration.
			s.removed()
			continue
		}
		if link == nil || !link.Connected() {
			select {
			case <-ctx.Done():
			case <-s.kick:
			case <-time.After(remoteRecPoll):
			}
			continue
		}
		s.serve(ctx, link)
	}
}

// removed gives up what is left of a remote that is no longer configured: the copies still to do
// (the host is not asked about them, since it is no longer reachable by name) and the starts
// looked for. Running records end deviceGone first, as when a link is lost.
// remotesRead says the remotes' configuration was read, so a remote it does not name was
// removed; one that could not be read (caught mid-edit) or not yet read proves nothing.
func remotesRead(r Remotes) bool {
	rr, ok := r.(interface{ RemotesRead() bool })
	return !ok || rr.RemotesRead()
}

func (s *recSupervisor) removed() {
	rr := &s.a.remoteRec
	s.linkLost()
	for _, rec := range s.a.remoteWork(s.remote) {
		s.a.giveUpCopy(rec.ID, causeRemoteGone, true)
	}
	rr.mu.Lock()
	delete(rr.lost, s.remote)
	rr.mu.Unlock()
}

// endStale ends the running records of the remote that no session of this process mirrors:
// they were left by a daemon that exited, whose link ended with it, so the host ended them.
func (s *recSupervisor) endStale() {
	rr := &s.a.remoteRec
	for _, rec := range s.a.remoteWork(s.remote) {
		if rec.State != RecordingRunning {
			continue
		}
		rr.mu.Lock()
		live := rr.live[rec.ID]
		rr.mu.Unlock()
		if !live {
			s.a.endMirrored(rec.ID, EndDaemonExit, "")
		}
	}
}

// linkLost ends the records this process mirrors of the remote: deviceGone, with the link's
// cause. Their copies wait for the next link up, which learns the host's final reason.
func (s *recSupervisor) linkLost() {
	for _, rec := range s.a.remoteWork(s.remote) {
		if rec.State == RecordingRunning {
			s.a.endMirrored(rec.ID, EndDeviceGone, "the link to "+s.remote+" was lost")
		}
	}
}

// recWatch is the relayed device.watchUiRecordings of one session.
type recWatch struct {
	stop func()
	sub  string
}

// copyResult is how one copy attempt ended: retry says the next attempt waits copyRetry.
type copyResult struct {
	retry bool
	// slow says the host failed the copy in a way that may last: the wait grows.
	slow bool
}

// serve serves one connection of the link: it syncs with the host's recordings, mirrors what
// the watch tells, copies the timelines that are ready, and returns when the link is lost, the
// context ends or there is nothing left to do.
func (s *recSupervisor) serve(ctx context.Context, link *hosting.Link) {
	a := s.a
	down := link.Down()
	sctx, cancel := context.WithCancel(ctx)
	events := make(chan json.RawMessage, 256)
	var overflow atomic.Bool
	var watch *recWatch
	copyDone := make(chan copyResult, 1)
	copyBusy := false
	slowN := 0
	var copyAfter, lastSync time.Time
	defer func() {
		cancel()
		if copyBusy {
			<-copyDone
		}
		if watch != nil {
			s.closeWatch(link, watch)
		}
	}()
	tick := time.NewTicker(remoteRecPoll)
	defer tick.Stop()
	resync := true
	for {
		if resync || overflow.Swap(false) {
			resync = false
			lastSync = time.Now()
			s.sync(sctx, link, &watch, events, &overflow)
		}
		rr := &a.remoteRec
		rr.mu.Lock()
		work := a.hasWorkLocked(s.remote)
		rr.mu.Unlock()
		if !work {
			return
		}
		if !copyBusy && !time.Now().Before(copyAfter) {
			if rec, ok := s.nextCopy(); ok {
				copyBusy = true
				go func() { copyDone <- s.copyOne(sctx, link, rec) }()
			}
		}
		select {
		case <-down:
			s.linkLost()
			return
		case <-ctx.Done():
			return
		case raw := <-events:
			s.onEvent(raw)
		case <-s.kick:
			resync = true
		case <-tick.C:
			// The watch keeps the running ones; what it cannot tell (a summary the link lost,
			// a start's answer, a copy that waits) is asked for each tick, the rest rarely.
			if s.needsInfo() || time.Since(lastSync) >= remoteRecResync {
				resync = true
			}
		case r := <-copyDone:
			copyBusy = false
			switch {
			case r.retry && r.slow:
				// A host that keeps failing is asked less often, up to copyRetryMax.
				slowN = min(slowN+1, 8)
				copyAfter = time.Now().Add(min(copyRetry<<slowN, copyRetryMax))
			case r.retry:
				slowN = 0
				copyAfter = time.Now().Add(copyRetry)
			default:
				slowN = 0
			}
		}
	}
}

// needsInfo reports whether the host's summaries are wanted now: an ended record whose final
// summary this daemon lacks, or a start that is looked for.
func (s *recSupervisor) needsInfo() bool {
	rr := &s.a.remoteRec
	rr.mu.Lock()
	hints := len(rr.lost[s.remote]) > 0
	rr.mu.Unlock()
	if hints {
		return true
	}
	for _, rec := range s.a.remoteWork(s.remote) {
		if rec.State == RecordingEnded && rec.SHA256 == "" {
			return true
		}
	}
	return false
}

// nextCopy is a record of the remote whose timeline is ready to copy: ended, with the host's
// size and digest known.
func (s *recSupervisor) nextCopy() (UIRecord, bool) {
	for _, rec := range s.a.remoteWork(s.remote) {
		if rec.State == RecordingEnded && pendingCopy(rec) && rec.SHA256 != "" && rec.HostID != "" {
			return rec, true
		}
	}
	return UIRecord{}, false
}

// sync opens or closes the watch as running records need it, then lists the host's own
// recordings and folds them into the client's: each record it holds takes its summary, one
// the host no longer holds gives its copy up, and a start whose answer was lost is adopted.
func (s *recSupervisor) sync(ctx context.Context, link *hosting.Link, watch **recWatch, events chan json.RawMessage, overflow *atomic.Bool) {
	a := s.a
	running := false
	for _, rec := range a.remoteWork(s.remote) {
		if rec.State == RecordingRunning {
			running = true
		}
	}
	switch {
	case running && *watch == nil:
		w, err := s.openWatch(ctx, link, events, overflow)
		if err != nil {
			a.logf("recordings: the watch of %s's recordings could not be opened: %v", s.remote, err)
		} else {
			*watch = w
		}
	case !running && *watch != nil:
		s.closeWatch(link, *watch)
		*watch = nil
	}
	// The records that existed before the host was asked: one saved after (a start that
	// returned meanwhile) is not one the list could have held.
	before := a.remoteWork(s.remote)
	list, err := s.hostList(ctx, link)
	if err != nil {
		return
	}
	held := map[string]bool{}
	for _, hs := range list {
		held[hs.ID] = true
		a.mirrorHost(s.remote, hs)
	}
	for _, rec := range before {
		if rec.HostID != "" && !held[rec.HostID] {
			a.giveUpCopy(rec.ID, causeHostGone, true)
		}
	}
	s.adopt(list)
}

// hostList is the recordings the host holds of this daemon's name, as it lists them, bounded
// and with the ids that are not recording ids left out.
func (s *recSupervisor) hostList(ctx context.Context, link *hosting.Link) ([]UIRecording, error) {
	lctx, cancel := context.WithTimeout(ctx, relayStart+relayWait)
	defer cancel()
	resp, err := callHost(lctx, link, "device.uiRecordings", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	var res UIRecordingsResult
	if resp.Error != nil || resp.Result == nil || json.Unmarshal(resp.Result, &res) != nil {
		return nil, errors.New("the host's list of recordings is not one this version reads")
	}
	out := make([]UIRecording, 0, min(len(res.Recordings), uiRecordingsMax))
	for _, hs := range res.Recordings {
		if len(out) == uiRecordingsMax {
			break
		}
		if artifact.ValidTimelineID(hs.ID) && (hs.State == RecordingRunning || hs.State == RecordingEnded) {
			out = append(out, hs)
		}
	}
	return out, nil
}

// adopt creates the client's record of an ended recording the host holds of a start whose
// answer was lost, when the client holds none of it. A start the host shows nothing for is
// forgotten; one that still runs is waited for.
func (s *recSupervisor) adopt(list []UIRecording) {
	a := s.a
	rr := &a.remoteRec
	rr.mu.Lock()
	hints := append([]lostStart(nil), rr.lost[s.remote]...)
	rr.mu.Unlock()
	for _, h := range hints {
		// The search is bounded from the first listing the host answered, not from the start:
		// a link that was down a long time has not looked yet.
		rr.mu.Lock()
		for i := range rr.lost[s.remote] {
			if l := &rr.lost[s.remote][i]; l.seq == h.seq && l.listed.IsZero() {
				l.listed = time.Now()
				h.listed = l.listed
			} else if l.seq == h.seq {
				h.listed = l.listed
			}
		}
		rr.mu.Unlock()
		var pick *UIRecording
		wait := false
		for i := range list {
			hs := list[i]
			if hs.Device != h.serial || hs.StartedAt.Before(h.since.Add(-lostStartSkew)) {
				continue
			}
			if _, known := a.clientRecordOf(s.remote, hs.ID); known {
				continue
			}
			if hs.State != RecordingEnded {
				wait = true
				continue
			}
			if pick == nil || hs.StartedAt.After(pick.StartedAt) {
				pick = &list[i]
			}
		}
		if pick == nil && wait && time.Since(h.listed) < lostStartTTL {
			continue
		}
		if pick != nil {
			now := time.Now().UTC()
			rec := UIRecord{ID: newRecordingID(now), Device: h.id, Remote: true, HostID: pick.ID, Invisible: pick.Invisible,
				MaxSeconds: min(max(pick.MaxSeconds, recordSecondsMin), recordSecondsMax), StartedBy: h.starter,
				State: RecordingRunning, StartedAt: now}
			if t, ok := hostTime(&pick.StartedAt, now); ok {
				rec.StartedAt = t
			}
			applyHostSummary(&rec, *pick, s.remote)
			rr.mu.Lock()
			err := a.recordings.Save(rec)
			if err == nil {
				a.publishRecord(rec)
			}
			rr.mu.Unlock()
			if err != nil {
				a.logf("recordings: the recording %s of %s was not adopted: %v", pick.ID, s.remote, err)
				continue
			}
		}
		rr.mu.Lock()
		kept := rr.lost[s.remote][:0:0]
		for _, l := range rr.lost[s.remote] {
			if l.seq != h.seq {
				kept = append(kept, l)
			}
		}
		rr.lost[s.remote] = kept
		rr.mu.Unlock()
	}
}

// openWatch opens device.watchUiRecordings on the host, routing its events to events before
// the next frame is read. A full channel is not waited for: the next sync lists instead.
func (s *recSupervisor) openWatch(ctx context.Context, link *hosting.Link, events chan json.RawMessage, overflow *atomic.Bool) (*recWatch, error) {
	cctx, cancel := context.WithTimeout(ctx, remoteRecWatchStart)
	defer cancel()
	route := func(raw json.RawMessage) {
		select {
		case events <- raw:
		default:
			overflow.Store(true)
		}
	}
	resp, stop, err := link.CallRouted(cctx, capability.RequestEnvelope{Capability: "device.watchUiRecordings", Params: json.RawMessage(`{}`)}, route)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil || resp.Operation == nil || resp.Operation.State != capability.StateCompleted || resp.Result == nil {
		stop()
		return nil, errors.New("the host refused the watch")
	}
	var r WatchUIRecordingsResult
	if json.Unmarshal(resp.Result, &r) != nil || r.SubscriptionID == "" || len(r.SubscriptionID) > maxRelayedID {
		stop()
		return nil, errors.New("the host named a subscription this version cannot use")
	}
	return &recWatch{stop: stop, sub: r.SubscriptionID}, nil
}

// closeWatch ends the host's subscription and the routing of its events.
func (s *recSupervisor) closeWatch(link *hosting.Link, w *recWatch) {
	w.stop()
	ctx, cancel := context.WithTimeout(context.Background(), remoteRecCleanup)
	defer cancel()
	p, _ := json.Marshal(event.UnsubscribeParams{SubscriptionID: w.sub})
	link.Call(ctx, capability.RequestEnvelope{Capability: event.CapUnsubscribe, Params: p})
}

// onEvent folds an event of the host's watch into the client's records.
func (s *recSupervisor) onEvent(raw json.RawMessage) {
	var e struct {
		Topic   string          `json:"topic"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Topic != TopicUIRecording {
		return
	}
	switch e.Type {
	case EventState:
		var p UIRecordingStatePush
		if json.Unmarshal(e.Payload, &p) == nil && artifact.ValidTimelineID(p.Recording.ID) &&
			(p.Recording.State == RecordingRunning || p.Recording.State == RecordingEnded) {
			s.a.mirrorHost(s.remote, p.Recording)
		}
	case EventDeleted:
		var p UIRecordingDeleted
		if json.Unmarshal(e.Payload, &p) != nil {
			return
		}
		if rec, ok := s.a.clientRecordOf(s.remote, p.ID); ok && !rec.Copied {
			s.a.giveUpCopy(rec.ID, causeHostGone, true)
		}
	}
}

// ---- the copy ----------------------------------------------------------------------

// copyOne copies the timeline of the client's record rec from the host (ADR-0115 Decision 5):
// by ranges of copyRange, into timeline.jsonl.part, from the part's own size when an earlier
// attempt left some, bounded by the size the host's summary gave. Only when the whole part
// hashes to the host's SHA-256 is it renamed to timeline.jsonl and the record marked copied;
// then the host is asked to delete its copy. A mismatch discards the part and gives the copy
// up, the host's kept. Anything that only needs another try (the recording not finished, the
// link lost) leaves the part and asks to be retried.
func (s *recSupervisor) copyOne(ctx context.Context, link *hosting.Link, rec UIRecord) copyResult {
	a := s.a
	cctx, cancel := context.WithCancel(ctx)
	rr := &a.remoteRec
	rr.mu.Lock()
	if rr.copying == nil {
		rr.copying = map[string]context.CancelFunc{}
	}
	rr.copying[rec.ID] = cancel
	rr.mu.Unlock()
	defer func() {
		cancel()
		rr.mu.Lock()
		delete(rr.copying, rec.ID)
		rr.mu.Unlock()
	}()

	dir := a.recordings.dir(rec.ID)
	part, final := filepath.Join(dir, copyPart), filepath.Join(dir, artifact.TimelineFile)
	// A timeline.jsonl that is not marked copied: either the rename was made and the record's
	// write was not (it hashes to the host's digest), or it is not the copy and goes.
	if fi, err := os.Lstat(final); err == nil {
		if fi.Mode().IsRegular() && fi.Size() == rec.Bytes {
			if sum, err := hashFile(final); err == nil && sum == rec.SHA256 {
				return s.finishCopy(link, rec, "")
			}
		}
		os.Remove(final)
	}
	f, err := a.openPartIfPending(rec.ID, part)
	if f == nil && err == nil {
		return copyResult{} // deleted, or given up: no part is made for it
	}
	if err != nil {
		os.Remove(part)
		return copyResult{retry: true}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		os.Remove(part)
		return copyResult{retry: true}
	}
	size := fi.Size()
	if size > rec.Bytes {
		if f.Truncate(0) != nil {
			return copyResult{retry: true}
		}
		size = 0
	}
	if _, err := f.Seek(size, 0); err != nil {
		return copyResult{retry: true}
	}
	for size < rec.Bytes {
		n := min(copyRange, rec.Bytes-size)
		if h := rr.hook.Load(); h != nil {
			(*h)(size, n)
		}
		rctx, rcancel := context.WithTimeout(cctx, fetchTimeout)
		w, total, err := link.ArtifactRange(rctx, rec.HostID, size, n, f)
		rcancel()
		size += w
		if err == nil && total != rec.Bytes {
			// The host's file is not the size its summary gave.
			a.giveUpCopy(rec.ID, causeCopyMismatch, true)
			return copyResult{}
		}
		if err != nil {
			return s.copyFailed(cctx, rec, err)
		}
		if w == 0 {
			return copyResult{retry: true}
		}
	}
	if err := f.Sync(); err != nil {
		return copyResult{retry: true}
	}
	sum, err := hashReader(f, size)
	if err != nil {
		return copyResult{retry: true}
	}
	if sum != rec.SHA256 {
		a.giveUpCopy(rec.ID, causeCopyMismatch, true)
		return copyResult{}
	}
	return s.finishCopy(link, rec, part)
}

// copyFailed classifies a ranged fetch that did not complete.
func (s *recSupervisor) copyFailed(cctx context.Context, rec UIRecord, err error) copyResult {
	if cctx.Err() != nil {
		return copyResult{} // deleted, or the daemon is stopping
	}
	var refused *hosting.ErrArtifactRefused
	switch {
	case errors.As(err, &refused):
		switch refused.Status {
		case http.StatusNotFound, http.StatusGone:
			s.a.giveUpCopy(rec.ID, causeHostGone, true)
			return copyResult{}
		case http.StatusRequestedRangeNotSatisfiable:
			// The host's file is shorter than the part it is asked to continue: it is not the
			// file its summary described.
			s.a.giveUpCopy(rec.ID, causeCopyRefused, true)
			return copyResult{}
		case http.StatusConflict:
			return copyResult{retry: true} // the recording is not finished on the host
		}
		return copyResult{retry: true, slow: true}
	case errors.Is(err, hosting.ErrArtifactRange), errors.Is(err, hosting.ErrArtifactReply), errors.Is(err, hosting.ErrArtifactID):
		s.a.giveUpCopy(rec.ID, causeCopyRefused, true)
		return copyResult{}
	}
	return copyResult{retry: true}
}

// finishCopy makes the part the timeline: under rr.mu, so a delete meanwhile is respected, the
// part is renamed and the record is marked copied; only then does anything read the file. The
// host is then asked to delete its copy.
func (s *recSupervisor) finishCopy(link *hosting.Link, rec UIRecord, part string) copyResult {
	a := s.a
	var done bool
	got, found, err := a.mutate(rec.ID, func(r *UIRecord) bool {
		if r.Copied || r.CopyGaveUp {
			return false
		}
		if part != "" {
			if err := os.Rename(part, filepath.Join(a.recordings.dir(r.ID), artifact.TimelineFile)); err != nil {
				return false
			}
		}
		r.Copied, done = true, true
		return true
	})
	switch {
	case !found:
		return copyResult{}
	case err != nil:
		// The record could not be written: nothing was published, and the host's copy stays.
		// The timeline renamed above, if it was, is found by the next attempt, which hashes
		// it and marks the record, after the slow wait: each attempt hashes the whole file.
		return copyResult{retry: true, slow: true}
	case !done:
		return copyResult{retry: !got.Copied && !got.CopyGaveUp}
	}
	a.deleteOnHost(got.Device, got.HostID)
	return copyResult{}
}

// openPartIfPending opens (and creates) the part file of record id only while the record is
// still one whose copy is pending, under rr.mu as giveUpCopy and a delete are: a part is never
// made in the directory of a record that gave its copy up, or is gone. It returns nil and no
// error for such a record.
func (a *ADB) openPartIfPending(id, part string) (*os.File, error) {
	rr := &a.remoteRec
	rr.mu.Lock()
	defer rr.mu.Unlock()
	cur, found, err := a.recordings.Get(id)
	if err != nil || !found || !pendingCopy(cur) {
		return nil, nil
	}
	return openPart(part)
}

// openPart opens the part file, never following a link planted in its place.
func openPart(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
}

// hashFile is the hex SHA-256 of the regular file at path.
func hashFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	return hashReader(f, fi.Size())
}

// hashReader is the hex SHA-256 of the first size bytes of f.
func hashReader(f *os.File, size int64) (string, error) {
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(f, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
