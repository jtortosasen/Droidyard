package device

import (
	"context"
	"strconv"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// The kept recordings (ADR-0113 §5, §6): device.uiRecordings lists them,
// device.watchUiRecordings tells the web UI as they change, device.deleteUiRecording removes
// one. None of the three touches the device; they read and change the daemon's own records.

// TopicUIRecording is the topic device.watchUiRecordings' events carry. Like session.state it
// is broadcast to every subscription that holds it, outside the replay ring, and
// events.subscribe does not name it: only the stream the call opened receives it.
const TopicUIRecording = "device.uiRecording"

// EventDeleted is the type of an event telling a recording was deleted; EventState (the
// screen stream's) is the other.
const EventDeleted = "deleted"

// uiRecordingsMax is the most recordings a listing holds: the retention's count.
const uiRecordingsMax = RecordingsKept

// recordPublishEvery is the least time between two `state` events of a running recording.
// A variable so a test is quick.
var recordPublishEvery = 2 * time.Second

// UIRecordingsParams is device.uiRecordings' parameters.
type UIRecordingsParams struct {
	Device string `json:"device,omitempty" jsonschema_description:"Only the recordings of this device serial."`
	ID     string `json:"id,omitempty" jsonschema_description:"Only the recording with this id; an unknown id gives an empty list."`
}

// UIRecordingsResult is device.uiRecordings' result.
type UIRecordingsResult struct {
	Recordings []UIRecording `json:"recordings" jsonschema:"required" jsonschema_description:"The recordings kept, newest first, at most 50: a running one with its live counts and its growing timeline."`
}

// WatchUIRecordingsResult is device.watchUiRecordings' result.
type WatchUIRecordingsResult struct {
	SubscriptionID string        `json:"subscriptionId" jsonschema:"required" jsonschema_description:"The subscription the device.uiRecording events arrive on, over the WebSocket session that made the call, until events.unsubscribe or the session's end."`
	Recordings     []UIRecording `json:"recordings" jsonschema:"required" jsonschema_description:"The recordings kept now, newest first, at most 50, taken after the subscription opened: merge it with the events by id, and call this again after an events.gap."`
}

// UIRecordingStatePush is the payload of a state event: the recording as it stands, at its
// start, at most every 2 s while it runs, and when it has ended (its record finalised and its
// timeline closed). It carries no device text and no chat id.
type UIRecordingStatePush struct {
	Recording UIRecording `json:"recording" jsonschema:"required"`
}

// UIRecordingDeleted is the payload of a deleted event.
type UIRecordingDeleted struct {
	ID string `json:"id" jsonschema:"required" jsonschema_description:"The id of the recording that was deleted, by device.deleteUiRecording or by the retention."`
}

// DeleteUIRecordingParams is device.deleteUiRecording's parameters.
type DeleteUIRecordingParams struct {
	ID string `json:"id" jsonschema:"required" jsonschema_description:"The recording's id, from device.uiRecordings."`
}

// DeleteUIRecordingResult is device.deleteUiRecording's result.
type DeleteUIRecordingResult struct {
	Deleted bool `json:"deleted" jsonschema:"required" jsonschema_description:"True when this call deleted a recording; false when none has this id (it may be gone already)."`
}

func registerUIRecordings(reg *capability.Registry, adb *ADB) {
	reg.MustRegister(capability.Descriptor{
		Name:        "device.uiRecordings",
		Kind:        capability.Query,
		Scope:       capability.Global,
		Since:       "0.33.0",
		Description: "Lists the UI recordings kept, newest first and at most 50, a running one with its live counts and growing timeline (save a timeline with droidyard artifact cat); device and id filter. Reads the daemon's records only, no adb.",
		Params:      UIRecordingsParams{},
		Target:      func(p any) *capability.OpTarget { return deviceTarget(p.(UIRecordingsParams).Device) },
		Result:      UIRecordingsResult{},
		Text:        capability.TextOf(uiRecordingsText),
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(UIRecordingsParams)
			adb.RecoverRecordings()
			return UIRecordingsResult{Recordings: adb.listRecordings(p.Device, p.ID)}, nil
		},
	})
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:         "device.watchUiRecordings",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Statefulness: capability.Streaming,
		Since:        "0.33.0",
		Description:  "Streams every UI recording's changes, whoever started it, to the calling WebSocket session on topic device.uiRecording: state at its start, at most every 2 s while it runs and at its end, and deleted. The result lists the recordings kept now, taken after the subscription opened: merge it with the events by id, and call this again after an events.gap.",
		Params:       struct{}{},
		Result:       WatchUIRecordingsResult{},
		Events: &capability.Events{
			Topic: TopicUIRecording,
			Types: map[string]any{EventState: UIRecordingStatePush{}, EventDeleted: UIRecordingDeleted{}},
		},
		Errors: []capability.ErrorCode{capability.ErrSessionRequired},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "adds a subscription to the calling session; changes no recording",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			sess, ok := call.Session.(capability.Follower)
			if !ok {
				return nil, capability.NewError(capability.ErrSessionRequired, "events need a session that can carry them").
					WithNextStep("subscribe over the WebSocket session, GET /v1/ws")
			}
			adb.RecoverRecordings()
			// The subscription first, the list second, as session.watch does: a recording that
			// starts or ends between the two reaches the watcher as an event, and one that did
			// so before is in the list.
			id, err := sess.Follow([]string{TopicUIRecording})
			if err != nil {
				return nil, err
			}
			list := adb.listRecordings("", "")
			if err := ctx.Err(); err != nil {
				sess.Unsubscribe(id)
				return nil, err
			}
			return WatchUIRecordingsResult{SubscriptionID: id, Recordings: list}, nil
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name:        "device.deleteUiRecording",
		Kind:        capability.Command,
		Scope:       capability.Global,
		Audience:    capability.AudienceHuman,
		Since:       "0.33.0",
		Description: "Deletes a kept UI recording and its timeline, which then reads ARTIFACT_EXPIRED; refused while it runs, and from an agent session. An unknown id answers deleted false.",
		Idempotent:  true,
		Params:      DeleteUIRecordingParams{},
		Result:      DeleteUIRecordingResult{},
		Errors:      []capability.ErrorCode{ErrUIRecordingActive, capability.ErrInvalidParams},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(DeleteUIRecordingParams)
			if p.ID == "" {
				return nil, capability.Errorf(capability.ErrInvalidParams, "id is required: a recording id from device.uiRecordings")
			}
			adb.RecoverRecordings()
			return adb.deleteRecording(p.ID)
		},
	})
}

// listRecordings is the kept recordings, newest first and at most uiRecordingsMax, filtered
// by device and id when given. A running recording is the recorder's own live summary.
func (a *ADB) listRecordings(device, id string) []UIRecording {
	out := []UIRecording{}
	if a.recordings == nil {
		return out
	}
	recs, err := a.recordings.List()
	if err != nil {
		a.logf("recordings: list: %v", err)
		return out
	}
	for _, rec := range recs {
		if len(out) >= uiRecordingsMax {
			break
		}
		if (device != "" && rec.Device != device) || (id != "" && rec.ID != id) {
			continue
		}
		if rec.State == RecordingRunning {
			if r := a.recorders.byID(rec.ID); r != nil {
				out = append(out, r.summary())
				continue
			}
		}
		out = append(out, publishedRecording(rec, rec.Bytes))
	}
	return out
}

// deleteRecording removes a kept recording: an unknown id is deleted false, a running one is
// DEVICE_UI_RECORDING_ACTIVE, and a deleted one is announced to the watchers.
func (a *ADB) deleteRecording(id string) (DeleteUIRecordingResult, error) {
	if a.recordings == nil || !artifact.ValidTimelineID(id) {
		return DeleteUIRecordingResult{}, nil
	}
	running := func() error {
		return capability.NewError(ErrUIRecordingActive, "the recording is still running").
			WithDetails(map[string]any{"id": id}).
			WithNextStep("Stop it with device.stopUiRecording, then delete it.")
	}
	// Only a live recorder makes a recording running here: a record left `recording` with no
	// recorder (a failed Finalise) can be deleted. One call at a time, so of two deletes of an
	// id only the one that finds it answers true and announces it.
	a.recorders.delMu.Lock()
	defer a.recorders.delMu.Unlock()
	if a.recorders.byID(id) != nil {
		return DeleteUIRecordingResult{}, running()
	}
	_, found, err := a.recordings.Get(id)
	if err != nil {
		return DeleteUIRecordingResult{}, err
	}
	if !found {
		return DeleteUIRecordingResult{}, nil
	}
	if err := a.recordings.Delete(id); err != nil {
		return DeleteUIRecordingResult{}, err
	}
	a.broadcastRecording(EventDeleted, UIRecordingDeleted{ID: id})
	return DeleteUIRecordingResult{Deleted: true}, nil
}

// publishRecording tells every watching session where a recording stands. It carries what
// publishedRecording does: no device text and no chat id.
func (a *ADB) publishRecording(rec UIRecording) {
	a.broadcastRecording(EventState, UIRecordingStatePush{Recording: rec})
}

func (a *ADB) broadcastRecording(typ string, payload any) {
	if a.bus != nil {
		a.bus.Broadcast(TopicUIRecording, typ, "", "", payload)
	}
}

// byID is the running recorder of recording id, or nil.
func (s *uiRecorders) byID(id string) *uiRecorder {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.byDevice {
		if r.id == id {
			return r
		}
	}
	return nil
}

// uiRecordingsText is the rendering of the list: a count, then each recording as
// writeRecording gives it. Nothing in it is device text.
//
//	recordings <n>
//	[recording lines]
func uiRecordingsText(r UIRecordingsResult) string {
	var t capability.Text
	t.W("recordings", strconv.Itoa(len(r.Recordings))).Nl()
	for _, rec := range r.Recordings {
		writeRecording(&t, rec)
	}
	return t.String()
}
