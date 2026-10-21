package device

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/reader"
)

// device.recordUi and device.stopUiRecording (ADR-0113): a daemon-owned recorder that keeps
// the full dump of every screen a device shows between a start and a stop, with the events
// between, in one timeline artifact. This file is the contract and the start and stop; the
// recorder itself is uirecorder.go, and the record and the timeline's lines are
// uirecordstore.go.

// ErrUIRecordingActive is DEVICE_UI_RECORDING_ACTIVE: a recording already runs on the device
// (details {id}), or the caller may not stop the one that does (ADR-0113 §4: ADR-0101's rule).
const ErrUIRecordingActive capability.ErrorCode = "DEVICE_UI_RECORDING_ACTIVE"

// The bounds of a recording (ADR-0113 §3) and the recorder's working numbers (plan m2x step
// 1: S1 busy pause, S3 tick, S4 gap; S2 needed no exclusion). Variables so a test is quick.
var (
	// recordTick is the pause between two polls of the ring.
	recordTick = 100 * time.Millisecond
	// recordGapMin is the least time between the end of a dump and the next request, and
	// recordGapMax what it grows to by doubling after each dump identical to the one before.
	recordGapMin = 100 * time.Millisecond
	recordGapMax = 2 * time.Second
	// recordBusyPause is how long the recorder leaves the reader alone after a `busy` answer,
	// longer than the slowest dump measured, so it never holds both of the reader's workers.
	recordBusyPause = 5 * time.Second
	// recordLockWait is the most a tick's dump waits for the read lock; recordStopLockWait the
	// most the last dump does.
	recordLockWait     = 200 * time.Millisecond
	recordStopLockWait = 2 * time.Second
	// recordDrain bounds the read of the events that remain when a recording ends.
	recordDrain = time.Second
	// recordSummaryWait is how long device.stopUiRecording waits for the recording's end;
	// recordStopAllWait how long StopRecordings does: 1 s, so with the readers' own
	// readerCloseBound (3 s) and the streams' stops, Close stays inside AfterOps's 5 s.
	recordSummaryWait = 10 * time.Second
	recordStopAllWait = time.Second
	// recordSecond is the unit of maxSeconds.
	recordSecond = time.Second
	// recordMaxDumps is the most dumps one recording stores, recordMaxBytes the most bytes its
	// timeline holds, and recordEndReserve the room under that cap only the `end` line may use.
	recordMaxDumps         = 10000
	recordMaxBytes   int64 = 512 << 20
	recordEndReserve int64 = 4 << 10
)

const (
	recordSecondsMin = 10
	recordSecondsMax = 3600
	recordSecondsDef = 900
	// recordFormat is the timeline's format number (its header's `format`).
	recordFormat = 1

	// The reasons of a `missed` line.
	missedBound      = "bound"      // the reader answered busy: the app did not answer in 2.5 s
	missedTooLarge   = "tooLarge"   // the document passed the 8 MiB read cap
	missedUnreadable = "unreadable" // the reader had no window, or sent something that is not a hierarchy
	missedLockHeld   = "lockHeld"   // the last dump could not take the read lock
	missedPaused     = "paused"     // the last dump was not asked for: the reader was in its pause after a busy answer, or a dump of it was cut off

	// The triggers of a `dump` line.
	triggerStart  = "start"
	triggerChange = "change"
	triggerStop   = "stop"

	// recordWriteCause is the cause of a recording that ended because its file could not be
	// written; the system's message stays in the daemon log.
	recordWriteCause = "the recording's file could not be written"
)

// UIRecordParams is device.recordUi's parameters.
type UIRecordParams struct {
	Device     string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	Invisible  bool   `json:"invisible,omitempty" jsonschema_description:"Also record children not visible to the user, as device.uiDump's invisible does."`
	MaxSeconds int    `json:"maxSeconds,omitempty" jsonschema:"minimum=10,maximum=3600" jsonschema_description:"End the recording after this many seconds (10-3600, default 900)."`
}

// UIRecording is a recording's summary. It carries no device text and never the starter's
// chat id (ADR-0088 §4); the timeline is the artifact it names.
type UIRecording struct {
	ID         string           `json:"id" jsonschema:"required" jsonschema_description:"The recording's id; its timeline is the artifact r.<id>."`
	Device     string           `json:"device" jsonschema:"required"`
	Invisible  bool             `json:"invisible" jsonschema:"required"`
	MaxSeconds int              `json:"maxSeconds" jsonschema:"required"`
	StartedBy  PublishedStarter `json:"startedBy" jsonschema:"required" jsonschema_description:"Who started it: the client kind and its session, never a chat."`
	State      string           `json:"state" jsonschema:"required,enum=recording,enum=ended"`
	StartedAt  time.Time        `json:"startedAt" jsonschema:"required"`
	EndedAt    *time.Time       `json:"endedAt,omitempty"`
	EndReason  string           `json:"endReason,omitempty" jsonschema:"enum=stopped,enum=maxDuration,enum=maxDumps,enum=maxBytes,enum=deviceGone,enum=failed,enum=daemonExit" jsonschema_description:"Why it ended; the timeline's end line says the same."`
	Cause      string           `json:"cause,omitempty" jsonschema_description:"For deviceGone and failed, the daemon's words for why; never device text."`
	Dumps      int              `json:"dumps" jsonschema:"required" jsonschema_description:"Dumps stored in the timeline."`
	Unchanged  int              `json:"unchanged" jsonschema:"required" jsonschema_description:"Dumps identical to the one before, counted and not stored."`
	Missed     int              `json:"missed" jsonschema:"required" jsonschema_description:"Dumps lost: reader busy past its bound, over 8 MiB, unreadable, lock held or reader paused at the stop."`
	LockWaits  int              `json:"lockWaits" jsonschema:"required" jsonschema_description:"Times another read or action held the reader when the recorder wanted it."`
	Events     int              `json:"events" jsonschema:"required"`
	Gaps       int              `json:"gaps" jsonschema:"required" jsonschema_description:"Times events were lost (a reader restart, or its ring overflowed)."`
	Bytes      int64            `json:"bytes" jsonschema:"required" jsonschema_description:"Size of the timeline so far."`
	SHA256     string           `json:"sha256,omitempty" jsonschema_description:"SHA-256 of the finished timeline."`
	Timeline   artifact.Ref     `json:"timeline" jsonschema:"required" jsonschema_description:"The timeline, one JSON line per record. Save it with droidyard artifact cat <ref> > rec.jsonl."`
}

// publishedRecording is a record as anything published carries it: its starter without the
// chat id (RecordingStarter.Published), its timeline as a reference.
func publishedRecording(rec UIRecord, size int64) UIRecording {
	return UIRecording{
		ID: rec.ID, Device: rec.Device, Invisible: rec.Invisible, MaxSeconds: rec.MaxSeconds,
		StartedBy: rec.StartedBy.Published(), State: rec.State, StartedAt: rec.StartedAt, EndedAt: rec.EndedAt,
		EndReason: rec.EndReason, Cause: rec.Cause, Dumps: rec.Dumps, Unchanged: rec.Unchanged, Missed: rec.Missed,
		LockWaits: rec.LockWaits, Events: rec.Events, Gaps: rec.Gaps, Bytes: size, SHA256: rec.SHA256,
		Timeline: artifact.TimelineRef(rec.ID, size),
	}
}

// StopUIRecordingParams is device.stopUiRecording's parameters.
type StopUIRecordingParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
}

// StopUIRecordingResult is device.stopUiRecording's result.
type StopUIRecordingResult struct {
	Stopped   bool         `json:"stopped" jsonschema:"required" jsonschema_description:"True when this call ended a running recording; false when none ran or one was already ending."`
	Recording *UIRecording `json:"recording,omitempty" jsonschema_description:"The recording stopped or already ending (state ended within 10 s), else the newest one kept."`
}

func (p UIRecordParams) validate() (maxSeconds int, err error) {
	if p.Device == "" {
		return 0, capability.Errorf(capability.ErrInvalidParams, "device is required: a serial from device.list")
	}
	switch {
	case p.MaxSeconds == 0:
		return recordSecondsDef, nil
	case p.MaxSeconds < recordSecondsMin || p.MaxSeconds > recordSecondsMax:
		return 0, capability.Errorf(capability.ErrInvalidParams, "maxSeconds is %d; it must be between %d and %d", p.MaxSeconds, recordSecondsMin, recordSecondsMax)
	}
	return p.MaxSeconds, nil
}

// registerUIRecord adds device.recordUi and device.stopUiRecording. Both are Lendable (ADR-0115
// Decision 2): a paired peer's recorder runs on the host that owns the device, and a peer's call
// holds the device's lease as any device call does (ADR-0104 §2), though the stop makes no adb
// command (leaseOnly). What a peer may stop is its own (uirecordpeer.go).
func registerUIRecord(reg *capability.Registry, adb *ADB, store *artifact.Store) {
	reg.MustRegister(capability.Descriptor{
		Name:         "device.recordUi",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Lendable:     true,
		Since:        "0.33.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Records the device's UI from now until device.stopUiRecording: the full dump (device.uiDump's document) each time the screen changes, with the events between, in one timeline artifact saved with droidyard artifact cat. The recording holds the device's UI reader, so a UiAutomation-based runner (UiAutomator, Appium, Maestro) cannot run beside it.",
		Params:       UIRecordParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(UIRecordParams).Device) },
		Result:       UIRecording{},
		Text:         capability.TextOf(uiRecordingText),
		Errors:       append(append([]capability.ErrorCode{}, LentErrors...), ErrUIDumpFailed, ErrUIRecordingActive, capability.ErrInvalidParams),
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			Reason:      "writes a recording file on the host and holds the device's UI reader; changes nothing on the device",
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(UIRecordParams)
			// Before the hook, so a malformed parameter runs no adb command at all.
			if _, err := p.validate(); err != nil {
				return nil, err
			}
			return adb.authorize(ctx, call.Origin, p.Device, "device.recordUi")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(UIRecordParams)
			maxSeconds, err := p.validate()
			if err != nil {
				return nil, err
			}
			return adb.recordUI(ctx, call, call.Authorized.(*target), p, maxSeconds, store)
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name:        "device.stopUiRecording",
		Kind:        capability.Command,
		Scope:       capability.Global,
		Lendable:    true,
		Since:       "0.33.0",
		Description: "Ends the device's UI recording after a last dump and returns its summary; stopped is false when none runs or it was already ending, and recording is then that one or the newest one kept. An MCP agent stops only its own session's or chat's recording.",
		Idempotent:  true,
		Params:      StopUIRecordingParams{},
		Target:      func(p any) *capability.OpTarget { return deviceTarget(p.(StopUIRecordingParams).Device) },
		Result:      StopUIRecordingResult{},
		Text:        capability.TextOf(stopUIRecordingText),
		Errors:      []capability.ErrorCode{ErrUIRecordingActive, ErrOccupied, capability.ErrInvalidParams},
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			Reason:      "ends a daemon-owned recording the caller is allowed to stop; makes no adb call",
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			lent, err := adb.leaseOnly(call.Origin, call.Params.(StopUIRecordingParams).Device)
			return lent, err
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(StopUIRecordingParams)
			if p.Device == "" {
				return nil, capability.Errorf(capability.ErrInvalidParams, "device is required: a serial from device.list")
			}
			// A device not lent to the calling peer is answered as a serial that is not there:
			// the host's recording on it is not the peer's to learn of (ADR-0099 §1).
			if lent, _ := call.Authorized.(bool); call.Origin.Client == capability.ClientPeer && !lent {
				return adb.nothingToStop(p.Device, call.Origin), nil
			}
			return adb.stopUIRecording(ctx, call, p.Device)
		},
	})
}

// uiRecorders is every recorder of the daemon, one per device. A recorder is in it from its
// claim, before its start dump, until its goroutine has finished it. The zero value is ready.
type uiRecorders struct {
	mu       sync.Mutex
	stopping bool
	byDevice map[string]*uiRecorder
	// delMu serialises device.deleteUiRecording.
	delMu sync.Mutex
}

var errRecordersStopping = errors.New("the daemon is stopping")

// claim puts r in the set for its device: refused once StopRecordings has begun, and while
// another recorder holds the device (the existing one is returned).
func (s *uiRecorders) claim(r *uiRecorder) (existing *uiRecorder, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, errRecordersStopping
	}
	if old := s.byDevice[r.serial]; old != nil {
		return old, nil
	}
	if s.byDevice == nil {
		s.byDevice = map[string]*uiRecorder{}
	}
	s.byDevice[r.serial] = r
	return nil, nil
}

func (s *uiRecorders) get(serial string) *uiRecorder {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byDevice[serial]
}

// remove takes r out of the set.
func (s *uiRecorders) remove(r *uiRecorder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byDevice[r.serial] == r {
		delete(s.byDevice, r.serial)
	}
}

// refuse sets stopping and returns every recorder.
func (s *uiRecorders) refuse() []*uiRecorder {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
	out := make([]*uiRecorder, 0, len(s.byDevice))
	for _, r := range s.byDevice {
		out = append(out, r)
	}
	return out
}

// LiveRecorders is how many recorders are running: zero once each has finished its recording.
func (a *ADB) LiveRecorders() int {
	a.recorders.mu.Lock()
	defer a.recorders.mu.Unlock()
	return len(a.recorders.byDevice)
}

// RecoverRecordings runs the recovery of a crashed daemon's recordings and the retention
// sweep, once (ADR-0113 §5). The daemon calls it as soon as it holds its lock, and a
// recorder's start calls it first, so no recorder starts before it ran: recovery finalises
// every record still `recording`, which would be a live recorder's own.
func (a *ADB) RecoverRecordings() (RecoverReport, error) {
	a.recoverOnce.Do(func() {
		if a.recordings == nil {
			return
		}
		a.recoverRep, a.recoverErr = a.recordings.Recover()
		if _, err := a.recordings.Sweep(); err != nil {
			a.recoverErr = errors.Join(a.recoverErr, err)
		}
		if n := len(a.recoverRep.Finalised); n > 0 {
			a.logf("recordings: %d recording(s) cut by a daemon exit finalised", n)
		}
		if a.recoverErr != nil {
			a.logf("recordings: recovery: %v", a.recoverErr)
		}
	})
	return a.recoverRep, a.recoverErr
}

// chatOf is the session registry's chat of a session id, for ADR-0101's rule.
func (a *ADB) chatOf() func(string) string {
	if a.occupation == nil {
		return nil
	}
	return a.occupation.Chat
}

// newRecordingID is a recording id: the start time and 32 random bits.
func newRecordingID(now time.Time) string {
	var b [4]byte
	rand.Read(b[:]) // never returns an error (crypto/rand, Go 1.24)
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

// recordingActive is the refusal of a start on a device that a recording already holds. A
// paired peer is told the id only of its own recording: another's, or the host's, is a
// recording it must not learn the name of (ADR-0115 Decision 3).
func recordingActive(device string, existing *uiRecorder, who capability.Origin) *capability.Error {
	if !recordingVisible(existing.startedBy(), who) {
		return capability.NewError(ErrUIRecordingActive, "a recording is already running on "+device).
			WithNextStep("Try again later, or ask the person who runs the host.")
	}
	return capability.NewError(ErrUIRecordingActive, "a recording is already running on "+device).
		WithDetails(map[string]any{"id": existing.id}).
		WithNextStep("Stop it with device.stopUiRecording, then start again; its timeline is the artifact r." + existing.id + ".")
}

// notYoursToStop is the refusal of a stop by a caller ADR-0101 does not let reach the
// recording's starter. A paired peer is told nothing of the recording it may not stop, not
// even its id.
func notYoursToStop(device, id string, who capability.Origin) *capability.Error {
	err := capability.NewError(ErrUIRecordingActive, "this caller may not stop the recording on "+device).
		WithCause("a recording is stopped by the UI, the CLI, or the session or chat that started it")
	if who.Client == capability.ClientPeer {
		return err.WithNextStep("Ask the person who runs the host to stop it, or try again later.")
	}
	return err.WithDetails(map[string]any{"id": id}).
		WithNextStep("Ask the user to stop it from the UI, or use another device.")
}

// recordStartFailed is DEVICE_UI_DUMP_FAILED for a recording that could not start: nothing
// was created. cause is one of the fixed sentences, never device text.
func recordStartFailed(cause string) *capability.Error {
	return capability.NewError(ErrUIDumpFailed, "the device's UI could not be recorded").
		WithCause(cause).
		WithNextStep("Retry device.recordUi: nothing was recorded and nothing on the device was changed. If it keeps failing, wake the screen or wait for an animation to end.")
}

// recordUI serves device.recordUi: it claims the device, reads where the ring ends and takes
// the start dump (so a reader that cannot run fails the call with nothing created), writes
// the record, the timeline's header and the dump, and hands the recording to its own
// goroutine, which outlives the call and its session.
func (a *ADB) recordUI(ctx context.Context, call *capability.Call, t *target, p UIRecordParams, maxSeconds int, store *artifact.Store) (UIRecording, error) {
	if a.recordings == nil || store == nil {
		return UIRecording{}, recordStartFailed("the daemon has no state directory to record into")
	}
	if got := readerDigest(); got != reader.SHA256 {
		return UIRecording{}, eventsFailed("the embedded reader.dex has SHA-256 " + got + ", not the pinned " + reader.SHA256)
	}
	if err := a.ready(ctx, t); err != nil {
		return UIRecording{}, err
	}
	// Before the first recorder can run: recovery finalises records that are still recording.
	a.RecoverRecordings()

	now := time.Now()
	session, chat, peer := capability.OwnerStamp(call.Origin.Client, call.Origin, a.chatOf())
	r := newUIRecorder(a, t, newRecordingID(now), UIRecord{
		Device: t.serial, Invisible: p.Invisible, MaxSeconds: maxSeconds,
		StartedBy: RecordingStarter{Client: string(call.Origin.Client), Session: session, Chat: chat, Peer: peer},
	}, store)
	// A paired peer's recording is held by its lease (ADR-0115 Decision 4): the lease's end,
	// found through the recorder's place in the set, ends it.
	leased := call.Origin.Client == capability.ClientPeer && call.Origin.ID != ""
	if leased {
		r.leaseSession = call.Origin.ID
	}
	// A lease that ends before the recorder is in the set finds nothing to stop; the check below,
	// once it is in the set, catches it (the start hook is where a test ends the lease here).
	a.started()
	existing, err := a.recorders.claim(r)
	if err != nil {
		return UIRecording{}, recordStartFailed("the daemon is stopping, so it starts no recording")
	}
	if existing != nil {
		return UIRecording{}, recordingActive(t.serial, existing, call.Origin)
	}
	if leased {
		// An open recording is activity of the lease for the idle rule (ADR-0104 §3), and the
		// peer's link ending ends it, as it ends the peer's streams.
		r.activity = a.streamActivity(call)
		r.watchLink(call)
		if a.leaseLost(call, t.serial) {
			// The device is no longer this link's: it is not read, and nothing is created.
			r.abandon()
			return UIRecording{}, capability.NewError(ErrDisconnected, "the lease of the device "+t.serial+" ended before the recording started").
				WithCause(leaseEndedCause).
				WithNextStep("Call again; nothing was recorded and nothing on the device was changed, so this call is safe to repeat.")
		}
	}
	if err := r.begin(ctx); err != nil {
		r.abandon()
		return UIRecording{}, err
	}
	// Before the goroutine can end the recording, so no watcher sees an end before the start.
	sum := r.summary()
	a.publishRecording(r.startedBy(), sum)
	go r.run()
	return sum, nil
}

// stopUIRecording serves device.stopUiRecording. A caller ADR-0101 does not let reach the
// recording's starter is refused; the others end it and wait for its summary.
func (a *ADB) stopUIRecording(ctx context.Context, call *capability.Call, device string) (StopUIRecordingResult, error) {
	r := a.recorders.get(device)
	if r == nil {
		return a.nothingToStop(device, call.Origin), nil
	}
	if !r.stoppableBy(call.Origin, a.chatOf()) {
		return StopUIRecordingResult{}, notYoursToStop(device, r.id, call.Origin)
	}
	// Stopped is true when this call ended the recording; one already ending for another
	// reason (a bound, a failure, the daemon's exit) is reported as it is, with stopped false.
	ended := r.requestEnd(EndStopped, "", true)
	wait := time.NewTimer(recordSummaryWait)
	defer wait.Stop()
	select {
	case <-r.done:
		if r.startFailed() {
			return a.nothingToStop(device, call.Origin), nil
		}
	case <-wait.C:
		// The end is slow (a dump that is still being read): the summary says it still runs.
	case <-ctx.Done():
		return StopUIRecordingResult{}, ctx.Err()
	}
	s := r.summary()
	return StopUIRecordingResult{Stopped: ended, Recording: &s}, nil
}

// nothingToStop is the answer when no recording runs on the device: the newest one kept that
// the caller may see (a paired peer's is its own).
func (a *ADB) nothingToStop(device string, who capability.Origin) StopUIRecordingResult {
	if a.recordings == nil {
		return StopUIRecordingResult{}
	}
	recs, err := a.recordings.List()
	if err != nil {
		return StopUIRecordingResult{}
	}
	for _, rec := range recs { // newest first
		if rec.Device == device && rec.State == RecordingEnded && recordingVisible(rec.StartedBy, who) {
			s := publishedRecording(rec, rec.Bytes)
			return StopUIRecordingResult{Recording: &s}
		}
	}
	return StopUIRecordingResult{}
}

// StopRecordings ends every recording as daemonExit, without a last dump, and waits at most
// recordStopAllWait (and ctx) for each to write its end; a later start is refused (ADR-0113
// §3). ADB.Close calls it first. No operation is involved.
func (a *ADB) StopRecordings(ctx context.Context) {
	list := a.recorders.refuse()
	for _, r := range list {
		r.requestEnd(EndDaemonExit, "", false)
	}
	wait := time.NewTimer(recordStopAllWait)
	defer wait.Stop()
	for _, r := range list {
		select {
		case <-r.done:
		case <-wait.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// removeRecordingDir deletes a recording's directory when its start failed after the record
// was written.
func (a *ADB) removeRecordingDir(id string) {
	if a.recordings != nil && artifact.ValidTimelineID(id) {
		os.RemoveAll(a.recordings.dir(id))
	}
}

// uiRecordingText is the rendering of a recording (ADR-0036 §2). Nothing in it is device text.
//
//	recording <id> <device> <state> [end=<reason>] dumps=<n> unchanged=<n> missed=<n> events=<n> gaps=<n> <bytes>B <seconds>s
//	save "droidyard artifact cat <ref> > <id>.jsonl"
func uiRecordingText(r UIRecording) string {
	var t capability.Text
	writeRecording(&t, r)
	return t.String()
}

func writeRecording(t *capability.Text, r UIRecording) {
	t.W("recording", r.ID, r.Device, r.State)
	if r.EndReason != "" {
		t.W("end=" + r.EndReason)
	}
	end := time.Now()
	if r.EndedAt != nil {
		end = *r.EndedAt
	}
	secs := end.Sub(r.StartedAt).Seconds()
	t.W(kv("dumps", int64(r.Dumps)), kv("unchanged", int64(r.Unchanged)), kv("missed", int64(r.Missed)),
		kv("events", int64(r.Events)), kv("gaps", int64(r.Gaps)),
		strconv.FormatInt(r.Bytes, 10)+"B", strconv.FormatFloat(max(secs, 0), 'f', 1, 64)+"s").Nl()
	t.W("save").Q("droidyard artifact cat " + r.Timeline.Ref + " > " + r.ID + ".jsonl").Nl()
}

// stopUIRecordingText is the rendering of device.stopUiRecording's result: whether a
// recording was stopped, then the recording's lines.
//
//	stopped | none running
//	[recording lines]
func stopUIRecordingText(r StopUIRecordingResult) string {
	var t capability.Text
	if r.Stopped {
		t.W("stopped").Nl()
	} else {
		t.W("none", "running").Nl()
	}
	if r.Recording != nil {
		writeRecording(&t, *r.Recording)
	}
	return t.String()
}
