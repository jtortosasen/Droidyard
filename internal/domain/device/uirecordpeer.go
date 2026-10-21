package device

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// A paired peer's recording on the host (ADR-0115 Decisions 2 to 4). The recorder is the
// host's own; what is here is what makes it safe to lend: a peer sees, stops and deletes only
// the recordings it started, its running recording is activity of its lease and ends with the
// lease, and the peer route serves it its own finished timelines and nothing else.

// linkEndedCause is the cause of a peer's recording that ended because the peer's link did
// (ADR-0104 §4); leaseEndedCause is the host's own end of the lease.
const linkEndedCause = "the peer's link ended"

// recordingVisible reports whether who may see, stop or delete a recording started by st.
// The host's own callers (the UI, the CLI, an MCP agent) are not narrowed here: an MCP agent
// still stops only its own session's or chat's (uiRecorder.stoppableBy). A paired peer
// reaches the recordings of its own configured name and no other (ADR-0101's peer branch).
func recordingVisible(st RecordingStarter, who capability.Origin) bool {
	if who.Client != capability.ClientPeer {
		return true
	}
	return capability.OwnerReaches(capability.ClientKind(st.Client), st.Session, st.Chat, st.Peer, who, nil)
}

// leaseOnly is the lease a paired peer's call takes on the device it names when the call
// makes no adb command (device.stopUiRecording, device.uiRecordings with a device), so the
// listing that checks the lending cannot be had (ADR-0115 Decision 4). The lending is the
// configuration's alone here. lent says the device is lent to the peer: an emulator's or a
// loopback serial never is (ADR-0099 §1), which the listing would tell and this cannot, and
// neither is one the configuration does not lend it. A device that is not lent is not leased,
// and the caller answers about it as about a serial that is not there: a peer is not told
// which devices exist (ADR-0099 §1).
func (a *ADB) leaseOnly(who capability.Origin, serial string) (lent bool, err error) {
	if who.Client != capability.ClientPeer || serial == "" {
		return false, nil
	}
	if IsEmulatorSerial(serial) || loopbackSerial.MatchString(serial) || !a.lentNow(who.Name, serial) {
		return false, nil
	}
	return true, a.leaseFor(who, serial)
}

// recordingWatchers are the subscriptions of paired peers' device.watchUiRecordings calls, by
// the peer's name. A peer's subscription holds no topic, so the bus's broadcast of every
// recording's change never reaches it; announceRecording delivers to it alone, and only the
// changes of its own recordings.
type recordingWatchers struct {
	mu   sync.Mutex
	subs map[*event.Subscription]string
}

func (w *recordingWatchers) add(sub *event.Subscription, peer string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.subs == nil {
		w.subs = map[*event.Subscription]string{}
	}
	w.subs[sub] = peer
}

func (w *recordingWatchers) remove(sub *event.Subscription) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.subs, sub)
}

// of is the subscriptions of the named peer.
func (w *recordingWatchers) of(peer string) []*event.Subscription {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []*event.Subscription
	for sub, name := range w.subs {
		if name == peer {
			out = append(out, sub)
		}
	}
	return out
}

// watchRecordingsForPeer is device.watchUiRecordings for a paired peer's session: the
// subscription is opened on no topic and registered with the peer's name, the list is taken
// after it as for a local watcher, and both hold only that peer's recordings.
func (a *ADB) watchRecordingsForPeer(ctx context.Context, call *capability.Call) (WatchUIRecordingsResult, error) {
	sess, err := event.SessionOf(call)
	if err != nil {
		return WatchUIRecordingsResult{}, err
	}
	name := capability.SessionName(call.Origin.Name)
	var mine atomic.Pointer[event.Subscription]
	id, sub, err := sess.Open(nil, func() {
		if s := mine.Load(); s != nil {
			a.recWatchers.remove(s)
		}
	})
	if err != nil {
		return WatchUIRecordingsResult{}, err
	}
	mine.Store(sub)
	a.recWatchers.add(sub, name)
	// A session that ended between Open and the registration would leave the entry for good:
	// its hook ran before the subscription was in the table.
	if _, ok := sess.Subscription(id); !ok {
		a.recWatchers.remove(sub)
		return WatchUIRecordingsResult{}, capability.Errorf(capability.ErrSessionRequired, "the session has ended")
	}
	list := a.listRecordings(call.Origin, "", "")
	if err := ctx.Err(); err != nil {
		sess.Unsubscribe(id)
		return WatchUIRecordingsResult{}, err
	}
	return WatchUIRecordingsResult{SubscriptionID: id, Recordings: list}, nil
}

// announceRecording tells the watchers of a change of a recording started by owner: every
// local watcher, and the paired peer who started it if it is watching, which hears of its own
// recordings only.
func (a *ADB) announceRecording(owner RecordingStarter, typ string, payload any) {
	if a.bus == nil {
		return
	}
	a.bus.Broadcast(TopicUIRecording, typ, "", "", payload)
	if owner.Client == string(capability.ClientPeer) && owner.Peer != "" {
		for _, sub := range a.recWatchers.of(owner.Peer) {
			a.bus.PublishTo(sub, TopicUIRecording, typ, "", "", payload)
		}
	}
}

// ErrTimelineRunning is a peer's request for the timeline of its own recording that has not
// ended: the peer route answers 409, and the client asks again (ADR-0115 Decision 5).
var ErrTimelineRunning = errors.New("the recording is still running")

// PeerTimeline resolves "r.<id>" for the named paired peer: the entry of its own finished
// recording's timeline, and nothing else (ADR-0115 Decision 5). Its own recording that has
// not been finalised is ErrTimelineRunning. Every other id, another peer's, the host's own,
// a deleted one or one that never was, is the same ARTIFACT_NOT_FOUND, so nothing tells a
// peer which recordings the host holds (artifact.Store.GetOwned keeps the same for ephemeral
// ids).
func (a *ADB) PeerTimeline(store *artifact.Store, peer, id string) (artifact.Entry, error) {
	notFound := capability.NewError(capability.ErrArtifactNotFound, "no artifact has this reference")
	if a.recordings == nil || store == nil || !artifact.ValidTimelineID(id) {
		return artifact.Entry{}, notFound
	}
	rec, found, err := a.recordings.Get(id)
	if err != nil || !found || rec.Remote {
		return artifact.Entry{}, notFound
	}
	who := capability.Origin{Client: capability.ClientPeer, Name: peer}
	if !recordingVisible(rec.StartedBy, who) {
		return artifact.Entry{}, notFound
	}
	if rec.State != RecordingEnded || a.recorders.byID(id) != nil {
		return artifact.Entry{}, ErrTimelineRunning
	}
	e, err := store.Get(artifact.RefPrefix + "r." + id)
	if err != nil {
		return artifact.Entry{}, notFound
	}
	if e.Growing {
		return artifact.Entry{}, ErrTimelineRunning
	}
	if e.File == "" {
		return artifact.Entry{}, notFound
	}
	return e, nil
}

// endLease ends the recording a paired peer's link session started on serial, as deviceGone
// with the host's cause (ADR-0104 §4). The recorder is in the set from its claim, so one
// that is still starting is found too.
func (s *uiRecorders) endLease(session, serial string) {
	s.mu.Lock()
	r := s.byDevice[serial]
	s.mu.Unlock()
	if r != nil && r.leaseSession != "" && r.leaseSession == session {
		r.requestEnd(EndDeviceGone, leaseEndedCause, false)
	}
}

// endUnheld ends the recording of a paired peer's link that no longer holds its device, as
// deviceGone with the host's cause: ForgetDevicesAbsent frees the lease of a device that left
// the listing without telling the lease's end, so a recording would otherwise run on until
// its reader failed (ADR-0104 §4). A lease taken after the listing was read is still held, and
// its recorder stays.
func (s *uiRecorders) endUnheld(o Occupation) {
	s.mu.Lock()
	var gone []*uiRecorder
	for serial, r := range s.byDevice {
		if r.leaseSession != "" && !o.HoldsDevice(r.leaseSession, serial) {
			gone = append(gone, r)
		}
	}
	s.mu.Unlock()
	for _, r := range gone {
		r.requestEnd(EndDeviceGone, leaseEndedCause, false)
	}
}
