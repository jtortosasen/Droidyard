package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/store"
)

// A paired peer's UI recording on the host that lends the phone (plan m2x step 9; ADR-0115
// Decisions 2 to 5, ADR-0104 §2 to §4, ADR-0101): two daemons paired over real TLS on
// loopback, anna and bob as the clients of the lease lab, carol as a raw link, the host's CLI
// and a fake UI reader behind the lent phone. The calls are made on the links themselves,
// naming the host's serial: the client daemon's relay is step 10's.

// phoneArgs is the parameters of a call on the host's phone as the host names it.
const phoneArgs = `{"device":"` + phone1 + `"}`

// linkCall runs capName on params over link, as the peer behind it.
func linkCall(t *testing.T, link *hosting.Link, capName, params string) capability.ResponseEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := link.Call(ctx, capability.RequestEnvelope{Capability: capName, Params: json.RawMessage(params)})
	if err != nil {
		t.Fatalf("%s over the link: %v", capName, err)
	}
	return resp
}

// recordingOf decodes the result of device.recordUi, which must have succeeded.
func recordingOf(t *testing.T, what string, env capability.ResponseEnvelope) device.UIRecording {
	t.Helper()
	var rec device.UIRecording
	if env.Error != nil || json.Unmarshal(env.Result, &rec) != nil || rec.ID == "" {
		t.Fatalf("%s: %+v %s; want a recording", what, env.Error, env.Result)
	}
	return rec
}

// stoppedOf decodes the result of device.stopUiRecording, which must have succeeded.
func stoppedOf(t *testing.T, what string, env capability.ResponseEnvelope) device.StopUIRecordingResult {
	t.Helper()
	var res device.StopUIRecordingResult
	if env.Error != nil || json.Unmarshal(env.Result, &res) != nil {
		t.Fatalf("%s: %+v %s", what, env.Error, env.Result)
	}
	return res
}

// listedOf decodes the result of device.uiRecordings, which must have succeeded.
func listedOf(t *testing.T, what string, env capability.ResponseEnvelope) []device.UIRecording {
	t.Helper()
	var res device.UIRecordingsResult
	if env.Error != nil || json.Unmarshal(env.Result, &res) != nil {
		t.Fatalf("%s: %+v %s", what, env.Error, env.Result)
	}
	return res.Recordings
}

func ids(recs []device.UIRecording) []string {
	out := []string{}
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}

// hostRecords is the host's recordings directory, as the host's daemon reads it.
func hostRecords(t *testing.T, lab *leaseLab) *device.UIRecordings {
	t.Helper()
	st, err := store.New(lab.host.state)
	if err != nil {
		t.Fatal(err)
	}
	return device.NewUIRecordings(st)
}

// onDisk is the host's record of recording id.
func onDisk(t *testing.T, lab *leaseLab, id string) device.UIRecord {
	t.Helper()
	rec, found, err := hostRecords(t, lab).Get(id)
	if err != nil || !found {
		t.Fatalf("the host's record of %s: found=%v err=%v", id, found, err)
	}
	return rec
}

// timelineOf is the host's timeline file of recording id.
func timelineOf(lab *leaseLab, id string) string {
	return filepath.Join(lab.host.state, "recordings", id, "timeline.jsonl")
}

// waitEnded waits until the host's record of id is ended and returns it.
func waitEnded(t *testing.T, lab *leaseLab, id string) device.UIRecord {
	t.Helper()
	var rec device.UIRecord
	waitFor(t, "the host's record of "+id+" to be ended", func() bool {
		r, found, err := hostRecords(t, lab).Get(id)
		rec = r
		return err == nil && found && r.State == device.RecordingEnded
	})
	return rec
}

// startedByHost starts a recording of the phone as the host's CLI.
func startedByHost(t *testing.T, lab *leaseLab) device.UIRecording {
	t.Helper()
	return recordingOf(t, "the host's recordUi", lab.host.invoke("device.recordUi", phoneArgs))
}

// fetchRecording GETs ref ("r.<id>") from the host's peer artifact route with the identity of
// a peer, with a Range header when rng is not empty. The pin is the host's.
func fetchRecording(t *testing.T, lab *leaseLab, as *hosting.Identity, ref, rng string) (*http.Response, []byte) {
	t.Helper()
	addr := lab.host.host.Health().Hosting.Listen
	hostFP := lab.host.host.Identity().Fingerprint()
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   hosting.PinnedConfig(as, func(fp string) bool { return hosting.SamePin(fp, hostFP) }),
		DisableKeepAlives: true,
	}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "https://"+addr+capability.PeerArtifactsPath+ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", ref, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", ref, err)
	}
	return resp, body
}

// recEvents collects what a peer's watch of the recordings is sent: the id each event names.
type recEvent struct {
	typ, id, state string
}

func recWatch(t *testing.T, link *hosting.Link) (recs []device.UIRecording, events chan recEvent) {
	t.Helper()
	events = make(chan recEvent, 64)
	route := func(raw json.RawMessage) {
		var e struct {
			Topic, Type string
			Payload     struct {
				ID        string
				Recording struct{ ID, State string }
			}
		}
		if json.Unmarshal(raw, &e) != nil || e.Topic != device.TopicUIRecording {
			return
		}
		id, state := e.Payload.ID, ""
		if e.Type == "state" {
			id, state = e.Payload.Recording.ID, e.Payload.Recording.State
		}
		events <- recEvent{typ: e.Type, id: id, state: state}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, stop, err := link.CallRouted(ctx, capability.RequestEnvelope{Capability: "device.watchUiRecordings", Params: json.RawMessage(`{}`)}, route)
	t.Cleanup(stop)
	var res device.WatchUIRecordingsResult
	if err != nil || resp.Error != nil || json.Unmarshal(resp.Result, &res) != nil || res.SubscriptionID == "" {
		t.Fatalf("a peer's device.watchUiRecordings: %v %+v %s", err, resp.Error, resp.Result)
	}
	return res.Recordings, events
}

// TestPeerRecordingIsTheHosts: plan m2x step 9's first check (ADR-0115 Decision 2). A peer's
// device.recordUi on the lent phone runs the recorder on the host: the host's reader is asked
// for the full dump, the host's record names the peer who started it, the lease is the peer's,
// the host's UI lists it running and stops it; a host MCP agent may not stop it. Seen failing
// with the recording not lendable (the peer's call was REQUEST_NOT_ALLOWED_HERE) and with the
// peer's name not kept in the starter (the host's record read peer "").
func TestPeerRecordingIsTheHosts(t *testing.T) {
	lab, r := newUILab(t)
	anna := lab.client.host.Remote(remoteName)

	env := linkCall(t, anna, "device.recordUi", phoneArgs)
	rec := recordingOf(t, "anna's recordUi", env)
	if rec.State != device.RecordingRunning || rec.Device != phone1 {
		t.Errorf("anna's recording is %+v; want it running on %s", rec, phone1)
	}
	if got := lab.host.adb.LiveRecorders(); got != 1 {
		t.Errorf("the host runs %d recorders; want anna's", got)
	}
	if got := r.FullRequests(); len(got) == 0 {
		t.Error("the host's reader was asked for no full dump: the recorder did not run on the host")
	}
	disk := onDisk(t, lab, rec.ID)
	if disk.StartedBy.Client != "peer" || disk.StartedBy.Peer != "anna" || disk.Remote {
		t.Errorf("the host's record is started by %+v remote %v; want the peer anna on the host", disk.StartedBy, disk.Remote)
	}
	if got := lab.holder(t); got != "anna" {
		t.Errorf("%q holds the phone while anna records; want anna", got)
	}

	// The host's UI sees it, and what it publishes names no peer.
	hostEnv := lab.host.invoke("device.uiRecordings", "")
	if got := listedOf(t, "the host's uiRecordings", hostEnv); len(got) != 1 || got[0].ID != rec.ID || got[0].State != device.RecordingRunning {
		t.Errorf("the host lists %v; want anna's running recording %s", ids(got), rec.ID)
	}
	if strings.Contains(string(hostEnv.Result), "anna") || strings.Contains(string(env.Result), "anna") {
		t.Errorf("a published recording names the peer: %s", hostEnv.Result)
	}

	// A host MCP agent cannot stop a peer's recording; the host's UI can.
	out, err := lab.host.ops.Invoke(context.Background(), capability.Request{Capability: "device.stopUiRecording",
		Params: json.RawMessage(phoneArgs), Client: capability.ClientMCP, Origin: capability.Origin{ID: "agent-1"}})
	if err != nil || out.Error == nil || out.Error.Code != device.ErrUIRecordingActive {
		t.Errorf("a host MCP agent's stop: %v %+v; want DEVICE_UI_RECORDING_ACTIVE", err, out.Error)
	}
	if got := lab.host.adb.LiveRecorders(); got != 1 {
		t.Errorf("the agent's refused stop ended the recording: %d recorders", got)
	}
	stop := stoppedOf(t, "the host UI's stop", lab.host.invoke("device.stopUiRecording", phoneArgs))
	if !stop.Stopped || stop.Recording == nil || stop.Recording.ID != rec.ID || stop.Recording.EndReason != device.EndStopped {
		t.Errorf("the host's stop = %+v; want anna's recording stopped", stop)
	}
	if got := waitEnded(t, lab, rec.ID); got.StartedBy.Peer != "anna" {
		t.Errorf("the ended record names %+v", got.StartedBy)
	}
	waitFor(t, "the recorder to leave the set", func() bool { return lab.host.adb.LiveRecorders() == 0 })
}

// TestPeerSeesOnlyItsOwnRecordings: plan m2x step 9's ownership check (ADR-0115 Decision 3, 5;
// ADR-0101's peer branch). With bob's finished recording and the host's running one on the
// phone, anna's list, id-filtered list and device-filtered list hold neither; her stop and her
// start are DEVICE_UI_RECORDING_ACTIVE naming no id; her stop with nothing running answers
// with none of theirs; her delete of theirs is deleted false; her watch is sent nothing of
// theirs, only her own recording's changes; and on the peer route anna's fetch of bob's, of the
// host's, of a deleted one and of an unknown one is the same 404, her own finished one is
// served and bob's fetch of it is 404. Seen failing with recordingVisible always true (the lists
// held bob's and the host's, anna's delete removed bob's), with the refusals carrying the id of
// the recording they refuse, with the route's ownership check removed (bob fetched anna's), and with
// every owner's changes sent to anna's watch.
func TestPeerSeesOnlyItsOwnRecordings(t *testing.T) {
	lab, _ := newUILab(t)
	anna, bob := lab.client.host.Remote(remoteName), lab.bob.host.Remote(remoteName)
	annaID, bobID := lab.client.host.Identity(), lab.bob.host.Identity()

	bobRec := recordingOf(t, "bob's recordUi", linkCall(t, bob, "device.recordUi", phoneArgs))
	stoppedOf(t, "bob's stop", linkCall(t, bob, "device.stopUiRecording", phoneArgs))
	waitEnded(t, lab, bobRec.ID)
	lab.release(t)
	hostRec := startedByHost(t, lab)

	t.Run("the list", func(t *testing.T) {
		for _, q := range []struct{ what, params string }{
			{"the whole list", `{}`},
			{"bob's id", `{"id":"` + bobRec.ID + `"}`},
			{"the host's id", `{"id":"` + hostRec.ID + `"}`},
			{"the phone", phoneArgs},
		} {
			if got := listedOf(t, "anna's uiRecordings, "+q.what, linkCall(t, anna, "device.uiRecordings", q.params)); len(got) != 0 {
				t.Errorf("anna's list for %s holds %v; want none of bob's or the host's", q.what, ids(got))
			}
		}
		// The host sees both, and bob's.
		if got := ids(listedOf(t, "the host's list", lab.host.invoke("device.uiRecordings", ""))); len(got) != 2 {
			t.Fatalf("the host lists %v; want bob's and its own", got)
		}
	})

	t.Run("start and stop name nothing", func(t *testing.T) {
		for _, c := range []struct{ what, capName string }{{"stop", "device.stopUiRecording"}, {"start", "device.recordUi"}} {
			env := linkCall(t, anna, c.capName, phoneArgs)
			if env.Error == nil || env.Error.Code != device.ErrUIRecordingActive {
				t.Fatalf("anna's %s beside the host's recording: %+v; want DEVICE_UI_RECORDING_ACTIVE", c.what, env.Error)
			}
			raw, _ := json.Marshal(env.Error)
			for _, secret := range []string{hostRec.ID, bobRec.ID} {
				if strings.Contains(string(raw), secret) {
					t.Errorf("anna's %s refusal names a recording that is not hers: %s", c.what, raw)
				}
			}
			if len(env.Error.Details) != 0 {
				t.Errorf("anna's %s refusal carries details %v; want none", c.what, env.Error.Details)
			}
		}
	})

	t.Run("delete", func(t *testing.T) {
		for _, id := range []string{bobRec.ID, hostRec.ID, "20200101T000000Z-00000000"} {
			env := linkCall(t, anna, "device.deleteUiRecording", `{"id":"`+id+`"}`)
			var res device.DeleteUIRecordingResult
			if env.Error != nil || json.Unmarshal(env.Result, &res) != nil || res.Deleted {
				t.Errorf("anna's delete of %s: %+v %s; want deleted false and no error", id, env.Error, env.Result)
			}
		}
		if got := ids(listedOf(t, "the host's list", lab.host.invoke("device.uiRecordings", ""))); len(got) != 2 {
			t.Errorf("the host lists %v after anna's deletes; want both recordings still there", got)
		}
	})

	stoppedOf(t, "the host's stop", lab.host.invoke("device.stopUiRecording", phoneArgs))
	waitEnded(t, lab, hostRec.ID)

	t.Run("nothing running", func(t *testing.T) {
		res := stoppedOf(t, "anna's stop with nothing running", linkCall(t, anna, "device.stopUiRecording", phoneArgs))
		if res.Stopped || res.Recording != nil {
			t.Errorf("anna's stop answered %+v; want none running and none of the kept ones, which are not hers", res)
		}
		lab.release(t)
	})

	t.Run("watch and route", func(t *testing.T) {
		list, events := recWatch(t, anna)
		if len(list) != 0 {
			t.Errorf("anna's watch lists %v at the start; want none of bob's or the host's", ids(list))
		}
		// The host deletes bob's and its own: a peer hears of neither.
		for _, id := range []string{bobRec.ID, hostRec.ID} {
			env := lab.host.invoke("device.deleteUiRecording", `{"id":"`+id+`"}`)
			var res device.DeleteUIRecordingResult
			if env.Error != nil || json.Unmarshal(env.Result, &res) != nil || !res.Deleted {
				t.Fatalf("the host's delete of %s: %+v %s", id, env.Error, env.Result)
			}
		}
		own := recordingOf(t, "anna's recordUi", linkCall(t, anna, "device.recordUi", phoneArgs))
		stoppedOf(t, "anna's stop", linkCall(t, anna, "device.stopUiRecording", phoneArgs))
		waitEnded(t, lab, own.ID)
		var sawEnd bool
		for deadline := time.After(20 * time.Second); !sawEnd; {
			select {
			case e := <-events:
				if e.id != own.ID {
					t.Fatalf("anna's watch was sent %+v, a recording that is not hers (hers is %s)", e, own.ID)
				}
				sawEnd = e.typ == "state" && e.state == device.RecordingEnded
			case <-deadline:
				t.Fatal("anna's watch never heard her recording end")
			}
		}

		// The route: her own finished timeline is served; nothing else exists.
		resp, body := fetchRecording(t, lab, annaID, "r."+own.ID, "")
		want, err := os.ReadFile(timelineOf(lab, own.ID))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || string(body) != string(want) {
			t.Errorf("anna's own timeline: status %d, %d bytes; want 200 and the host's %d bytes", resp.StatusCode, len(body), len(want))
		}
		var refused []string
		for _, c := range []struct {
			what string
			as   *hosting.Identity
			ref  string
		}{
			{"bob's deleted recording", annaID, "r." + bobRec.ID},
			{"the host's deleted recording", annaID, "r." + hostRec.ID},
			{"an unknown recording", annaID, "r.20200101T000000Z-00000000"},
			{"anna's recording, asked by bob", bobID, "r." + own.ID},
			{"a malformed id", annaID, "r.not-an-id"},
		} {
			resp, body := fetchRecording(t, lab, c.as, c.ref, "")
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s: status %d; want 404", c.what, resp.StatusCode)
			}
			refused = append(refused, fmt.Sprintf("%d %q", resp.StatusCode, body))
		}
		for _, got := range refused[1:] {
			if got != refused[0] {
				t.Errorf("the refusals differ (%q and %q): a peer could tell which recordings exist", refused[0], got)
			}
		}

		// Another's recording that exists and is not deleted is the same 404.
		hostLive := startedByHost(t, lab)
		resp, body = fetchRecording(t, lab, annaID, "r."+hostLive.ID, "")
		if got := fmt.Sprintf("%d %q", resp.StatusCode, body); got != refused[0] {
			t.Errorf("anna's fetch of the host's running recording is %s; want %s, as a recording that never was", got, refused[0])
		}
		stoppedOf(t, "the host's stop", lab.host.invoke("device.stopUiRecording", phoneArgs))
		waitEnded(t, lab, hostLive.ID)
		resp, body = fetchRecording(t, lab, annaID, "r."+hostLive.ID, "")
		if got := fmt.Sprintf("%d %q", resp.StatusCode, body); got != refused[0] {
			t.Errorf("anna's fetch of the host's finished recording is %s; want %s", got, refused[0])
		}

		// Her own, deleted by her: the watch hears it, and the route forgets it.
		env := linkCall(t, anna, "device.deleteUiRecording", `{"id":"`+own.ID+`"}`)
		var del device.DeleteUIRecordingResult
		if env.Error != nil || json.Unmarshal(env.Result, &del) != nil || !del.Deleted {
			t.Fatalf("anna's delete of her own: %+v %s", env.Error, env.Result)
		}
		for deadline := time.After(20 * time.Second); ; {
			select {
			case e := <-events:
				if e.id != own.ID {
					t.Fatalf("anna's watch was sent %+v, a recording that is not hers", e)
				}
				if e.typ == "deleted" {
					goto deleted
				}
			case <-deadline:
				t.Fatal("anna's watch never heard her recording deleted")
			}
		}
	deleted:
		resp, body = fetchRecording(t, lab, annaID, "r."+own.ID, "")
		if got := fmt.Sprintf("%d %q", resp.StatusCode, body); got != refused[0] {
			t.Errorf("anna's fetch of her deleted recording is %s; want %s", got, refused[0])
		}
	})
}

// TestPeerStopAndListTakeTheLease: plan m2x step 9 (ADR-0115 Decision 4, ADR-0104 §2). A peer's
// stop and its device-filtered list make no adb command, yet they hold the lease as any device
// call does: while anna holds the phone, bob's stop and bob's list of the phone are
// DEVICE_OCCUPIED naming anna, and the host's adb is asked nothing for them; bob's list with
// no device takes no lease; once anna lets go, bob's device-filtered list takes the phone.
// Seen failing with the lease left out of the two descriptors (bob's stop answered, the phone
// stayed anna's).
func TestPeerStopAndListTakeTheLease(t *testing.T) {
	lab, _ := newUILab(t)
	anna, bob := lab.client.host.Remote(remoteName), lab.bob.host.Remote(remoteName)

	annaRec := recordingOf(t, "anna's recordUi", linkCall(t, anna, "device.recordUi", phoneArgs))
	if got := lab.holder(t); got != "anna" {
		t.Fatalf("%q holds the phone; want anna", got)
	}

	before := len(lab.hostCalls(t))
	for _, c := range []struct{ what, capName, params string }{
		{"stop", "device.stopUiRecording", phoneArgs},
		{"list of the phone", "device.uiRecordings", phoneArgs},
	} {
		env := linkCall(t, bob, c.capName, c.params)
		if env.Error == nil || env.Error.Code != device.ErrOccupied || !strings.Contains(env.Error.Message, "anna") || !strings.Contains(env.Error.Message, phone1) {
			t.Errorf("bob's %s beside anna's lease: %+v; want DEVICE_OCCUPIED naming anna and %s", c.what, env.Error, phone1)
		}
	}
	if got := lab.holder(t); got != "anna" {
		t.Errorf("a refused peer changed the holder to %q", got)
	}
	if after := len(lab.hostCalls(t)); after != before {
		t.Errorf("the host's adb ran %d commands for calls that make none: %v", after-before, lab.hostCalls(t)[before:])
	}
	// A list that names no device holds nothing and is not refused.
	if got := listedOf(t, "bob's list", linkCall(t, bob, "device.uiRecordings", `{}`)); len(got) != 0 {
		t.Errorf("bob's list holds %v; want none of anna's", ids(got))
	}
	if got := lab.holder(t); got != "anna" {
		t.Errorf("a list with no device changed the holder to %q", got)
	}

	stoppedOf(t, "anna's stop", linkCall(t, anna, "device.stopUiRecording", phoneArgs))
	waitEnded(t, lab, annaRec.ID)
	lab.release(t)
	before = len(lab.hostCalls(t))
	listedOf(t, "bob's list of the phone", linkCall(t, bob, "device.uiRecordings", phoneArgs))
	if got := lab.holder(t); got != "bob" {
		t.Errorf("%q holds the phone after bob's list of it; want bob", got)
	}
	if after := len(lab.hostCalls(t)); after != before {
		t.Errorf("the host's adb ran commands for bob's list: %v", lab.hostCalls(t)[before:])
	}
}

// TestPeerRecordingHoldsTheLease: plan m2x step 9 (ADR-0115 Decision 4, ADR-0104 §3). A peer's
// running recording is an open stream of its link for the idle rule: with no call from anna for
// 20 minutes (the registry's clock is moved; the idle time is 15) bob's call is still refused,
// naming anna; once the recording has ended the idle time starts then, and 16 minutes later
// bob takes the phone. Seen failing with the recorder not counted (bob took the phone while
// anna's recording ran).
func TestPeerRecordingHoldsTheLease(t *testing.T) {
	lab, _ := newUILab(t)
	anna, bob := lab.client.host.Remote(remoteName), lab.bob.host.Remote(remoteName)

	annaRec := recordingOf(t, "anna's recordUi", linkCall(t, anna, "device.recordUi", phoneArgs))
	lab.clk.advance(20 * time.Minute)
	env := linkCall(t, bob, "device.uiRecordings", phoneArgs)
	if env.Error == nil || env.Error.Code != device.ErrOccupied || !strings.Contains(env.Error.Message, "anna") {
		t.Fatalf("bob's call 20 minutes into anna's recording: %+v; want DEVICE_OCCUPIED naming anna", env.Error)
	}

	stoppedOf(t, "anna's stop", linkCall(t, anna, "device.stopUiRecording", phoneArgs))
	waitEnded(t, lab, annaRec.ID)
	waitFor(t, "the recorder to leave the set", func() bool { return lab.host.adb.LiveRecorders() == 0 })
	env = linkCall(t, bob, "device.uiRecordings", phoneArgs)
	if env.Error == nil || env.Error.Code != device.ErrOccupied {
		t.Fatalf("bob's call right after anna's recording ended: %+v; want DEVICE_OCCUPIED, the idle time starts at its end", env.Error)
	}
	lab.clk.advance(16 * time.Minute)
	if env := linkCall(t, bob, "device.uiRecordings", phoneArgs); env.Error != nil {
		t.Fatalf("bob's call 16 minutes after the recording ended: %+v; want the lease taken over", env.Error)
	}
	if got := lab.holder(t); got != "bob" {
		t.Errorf("%q holds the phone; want bob", got)
	}
}

// recordedByCarol starts a recording of the phone as the raw peer carol, who must hold the
// lease afterwards.
func recordedByCarol(t *testing.T, lab *leaseLab) device.UIRecording {
	t.Helper()
	rec := recordingOf(t, "carol's recordUi", linkCall(t, lab.carol, "device.recordUi", phoneArgs))
	if got := lab.holder(t); got != "carol" {
		t.Fatalf("%q holds the phone while carol records; want carol", got)
	}
	return rec
}

// endedByLease requires the host's record of id to end deviceGone with cause, its timeline to
// say the same in its last line, and the host to keep no recorder and no lease.
func endedByLease(t *testing.T, lab *leaseLab, id, cause string) {
	t.Helper()
	rec := waitEnded(t, lab, id)
	if rec.EndReason != device.EndDeviceGone || rec.Cause != cause {
		t.Errorf("the recording ended %q (%q); want deviceGone, %q", rec.EndReason, rec.Cause, cause)
	}
	data, err := os.ReadFile(timelineOf(lab, id))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	var end struct{ Kind, Reason, Cause string }
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &end); err != nil || end.Kind != "end" || end.Reason != device.EndDeviceGone || end.Cause != cause {
		t.Errorf("the timeline's last line is %q; want an end line deviceGone, %q", lines[len(lines)-1], cause)
	}
	waitFor(t, "the host to keep no recorder", func() bool { return lab.host.adb.LiveRecorders() == 0 })
}

// TestLeaseEndEndsThePeersRecording: plan m2x step 9 (ADR-0115 Decision 4, ADR-0104 §4). Each
// way a lease ends ends the peer's recording as deviceGone, with the cause of that way: the
// owner's session.release and the phone no longer lent (the host ended the lease), and the
// peer's link cut (the peer's link ended). What was recorded stays on the host, ending in an
// `end` line, and the host keeps no recorder. Seen failing with the recorders left out of
// endLease (the release left the recording running, reading the phone unheld) and with the
// link's subscription left out (the cut link left it running).
func TestLeaseEndEndsThePeersRecording(t *testing.T) {
	for _, how := range []string{"release", "un-lent"} {
		t.Run(how, func(t *testing.T) {
			lab, r := newUILab(t)
			lab.release(t)
			rec := recordedByCarol(t, lab)
			if how == "release" {
				lab.release(t)
			} else {
				lab.lendPhone1To(t, "anna", "bob")
				lab.host.adb.SweepLeases()
			}
			endedByLease(t, lab, rec.ID, "the host ended the lease")
			if got := lab.holder(t); got != "" {
				t.Errorf("%q holds the phone after the lease ended", got)
			}
			asked := len(r.EventsRequests())
			time.Sleep(800 * time.Millisecond)
			if got := len(r.EventsRequests()); got != asked {
				t.Errorf("the host's reader got %d events requests after the lease ended; the recorder outlived it", got-asked)
			}
		})
	}
	t.Run("link cut", func(t *testing.T) {
		lab, _ := newUILab(t)
		lab.release(t)
		rec := recordedByCarol(t, lab)
		lab.stopHost()
		endedByLease(t, lab, rec.ID, "the peer's link ended")
	})
}

// TestRecorderStartingAsTheLeaseEnds: plan m2x step 9 (ADR-0104 §4, as SetStartHook's other
// tests). A recording still starting when its lease ends is in no table, so the end finds
// nothing to stop; once it is in the set of recorders it checks that its link still holds the
// phone and, if not, the start is refused DEVICE_DISCONNECTED with the host's cause before the
// reader is asked anything: no record, no timeline, no recorder. Seen failing with the check
// removed (the recording ran on, unheld) and with the check ending the recording after its start
// dump (a record and a dump of a phone the link no longer held).
func TestRecorderStartingAsTheLeaseEnds(t *testing.T) {
	lab, r := newUILab(t)
	lab.release(t)
	entered, proceed := make(chan struct{}, 1), make(chan struct{})
	lab.host.adb.SetStartHook(func() { entered <- struct{}{}; <-proceed })
	t.Cleanup(func() { lab.host.adb.SetStartHook(nil) })
	type answer struct {
		env capability.ResponseEnvelope
		err error
	}
	answered := make(chan answer, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		env, err := lab.carol.Call(ctx, capability.RequestEnvelope{Capability: "device.recordUi", Params: json.RawMessage(phoneArgs)})
		answered <- answer{env, err}
	}()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the recording never reached its start")
	}
	if got := lab.holder(t); got != "carol" {
		t.Fatalf("%q holds the phone while carol's recording starts; want carol", got)
	}
	lab.release(t)
	lab.host.adb.SetStartHook(nil)
	close(proceed)
	a := <-answered
	if a.err != nil {
		t.Fatal(a.err)
	}
	if a.env.Error == nil || a.env.Error.Code != device.ErrDisconnected || a.env.Error.Cause != "the host ended the lease" {
		t.Fatalf("carol's recordUi whose lease ended while it started: %+v %s; want DEVICE_DISCONNECTED, the host ended the lease", a.env.Error, a.env.Result)
	}
	waitFor(t, "the host to keep no recorder", func() bool { return lab.host.adb.LiveRecorders() == 0 })
	if got := listedOf(t, "the host's list", lab.host.invoke("device.uiRecordings", "")); len(got) != 0 {
		t.Errorf("the refused start left the records %v", ids(got))
	}
	if _, err := os.Stat(filepath.Join(lab.host.state, "recordings")); err == nil {
		if ents, _ := os.ReadDir(filepath.Join(lab.host.state, "recordings")); len(ents) != 0 {
			t.Errorf("the refused start left %d directories under recordings/", len(ents))
		}
	}
	if got := len(r.EventsRequests()) + len(r.FullRequests()); got != 0 {
		t.Errorf("the host's reader was asked %d times for a recording whose lease had ended", got)
	}
	if got := lab.holder(t); got != "" {
		t.Errorf("%q holds the phone after the refused start", got)
	}
}

// TestPeerStopIsNoOracleOnADeviceNotLentToIt: the host records on its phone and does not lend it
// to carol. Carol's stop and her list of that phone answer exactly as for a serial that does not
// exist, and her start says DEVICE_NOT_FOUND (ADR-0099 §1): a peer learns nothing of the host's
// recordings on devices that are not its to see. Seen failing with the stop reaching the
// recorder (DEVICE_UI_RECORDING_ACTIVE naming the phone).
func TestPeerStopIsNoOracleOnADeviceNotLentToIt(t *testing.T) {
	lab, _ := newUILab(t)
	lab.release(t)
	host := startedByHost(t, lab)
	lab.lendPhone1To(t, "anna", "bob") // carol no longer lent the phone
	answer := func(capName, params string) string {
		env := linkCall(t, lab.carol, capName, params)
		raw, _ := json.Marshal(struct {
			Result json.RawMessage
			Error  *capability.Error
		}{env.Result, env.Error})
		return string(raw)
	}
	const unknown = `{"device":"NOSUCH01"}`
	for _, capName := range []string{"device.stopUiRecording", "device.uiRecordings"} {
		got, want := answer(capName, phoneArgs), answer(capName, unknown)
		if got != want {
			t.Errorf("carol's %s on the phone she is not lent: %s; want what a serial that does not exist gets, %s", capName, got, want)
		}
		if strings.Contains(got, host.ID) {
			t.Errorf("carol's %s names the host's recording: %s", capName, got)
		}
	}
	if env := linkCall(t, lab.carol, "device.recordUi", phoneArgs); env.Error == nil || env.Error.Code != device.ErrNotFound {
		t.Errorf("carol's recordUi on the phone she is not lent: %+v; want DEVICE_NOT_FOUND", env.Error)
	}
	if got := lab.holder(t); got != "" {
		t.Errorf("%q holds the phone after carol's calls on a phone she is not lent", got)
	}
	if got := lab.host.adb.LiveRecorders(); got != 1 {
		t.Errorf("the host runs %d recorders; carol's calls must leave its own", got)
	}
	stoppedOf(t, "the host's stop", lab.host.invoke("device.stopUiRecording", phoneArgs))
}

// TestDeviceLeavingEndsThePeersRecording: a phone that leaves the host's listing frees its
// lease without a lease-end call, and the peer's recording on it ends with the lease's cause
// (ADR-0104 §4), though its reader still answers. Seen failing with the recorders not told (the
// recording ran on after the phone had left).
func TestDeviceLeavingEndsThePeersRecording(t *testing.T) {
	lab, _ := newUILab(t)
	lab.release(t)
	rec := recordedByCarol(t, lab)
	// A listing that still holds the phone leaves the lease and the recording alone.
	lab.host.invoke("device.list", "")
	if got := lab.host.adb.LiveRecorders(); got != 1 {
		t.Fatalf("%d recorders after a listing that holds the phone; want carol's", got)
	}
	if got := onDisk(t, lab, rec.ID); got.State != device.RecordingRunning {
		t.Fatalf("carol's record reads %q after a listing that holds the phone; want it running", got.State)
	}
	if got := lab.holder(t); got != "carol" {
		t.Fatalf("%q holds the phone after that listing; want carol", got)
	}
	listing := "List of devices attached\n" + hostEmulator + " device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"
	devicetest.Write(t, lab.hostDir, "devices.out", []byte(listing))
	lab.host.invoke("device.list", "") // a listing without the phone frees what was held on it
	endedByLease(t, lab, rec.ID, "the host ended the lease")
}

// seedEnded writes an ended record of a recording that peer (or the host's UI, for "") started
// n minutes ago, bytes large, with no timeline: the retention counts the record.
func seedEnded(t *testing.T, recs *device.UIRecordings, n int, peer string, bytes int64) string {
	t.Helper()
	at := time.Now().UTC().Add(-time.Duration(n) * time.Minute).Truncate(time.Second)
	id := at.Format("20060102T150405Z") + fmt.Sprintf("-%08x", n)
	end := at.Add(time.Second)
	st := device.RecordingStarter{Client: "ui"}
	if peer != "" {
		st = device.RecordingStarter{Client: "peer", Session: "link-" + peer, Peer: peer}
	}
	if err := recs.Save(device.UIRecord{ID: id, Device: phone1, MaxSeconds: 900, StartedBy: st, State: device.RecordingEnded,
		StartedAt: at, EndedAt: &end, EndReason: device.EndStopped, Bytes: bytes}); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestPeerRecordingRetention: plan m2x step 9 (ADR-0115 Decision 3). A peer keeps at most 10
// ended recordings and 1 GiB on the host, its oldest first, inside the global bounds, and
// another peer's and the host's own are not touched for it. With nine of anna's seeded, her
// real recording makes ten and nothing goes; the next makes eleven and her oldest goes (bob's,
// the host's and her newer ones stay); then two seeded of 600 MiB each pass her 1 GiB and the
// oldest of those goes. Seen failing with the per-peer bound removed (the oldest stayed).
func TestPeerRecordingRetention(t *testing.T) {
	lab, _ := newUILab(t)
	anna := lab.client.host.Remote(remoteName)
	recs := hostRecords(t, lab)
	var annas []string
	for i := 30; i >= 22; i-- { // nine, oldest first
		annas = append(annas, seedEnded(t, recs, i, "anna", 1000))
	}
	bobs := []string{seedEnded(t, recs, 40, "bob", 1000), seedEnded(t, recs, 41, "bob", 1000)}
	hosts := []string{seedEnded(t, recs, 42, "", 1000)}
	exists := func(id string) bool {
		_, found, err := hostRecords(t, lab).Get(id)
		return err == nil && found
	}
	record := func() string {
		rec := recordingOf(t, "anna's recordUi", linkCall(t, anna, "device.recordUi", phoneArgs))
		stoppedOf(t, "anna's stop", linkCall(t, anna, "device.stopUiRecording", phoneArgs))
		waitEnded(t, lab, rec.ID)
		waitFor(t, "the recorder to leave the set", func() bool { return lab.host.adb.LiveRecorders() == 0 })
		return rec.ID
	}

	first := record() // ten of anna's
	for _, id := range append(append(append([]string{first}, annas...), bobs...), hosts...) {
		if !exists(id) {
			t.Errorf("recording %s was swept with anna at ten", id)
		}
	}
	second := record() // eleven: her oldest goes
	if exists(annas[0]) {
		t.Errorf("anna's oldest recording %s stayed with eleven of hers", annas[0])
	}
	for _, id := range append(append(append([]string{first, second}, annas[1:]...), bobs...), hosts...) {
		if !exists(id) {
			t.Errorf("recording %s was swept, and is not anna's oldest", id)
		}
	}

	// The size bound: two of 600 MiB are over 1 GiB together, whatever their number.
	big1 := seedEnded(t, recs, 20, "anna", 600<<20)
	big2 := seedEnded(t, recs, 19, "anna", 600<<20)
	third := record()
	if exists(big1) {
		t.Errorf("the older of anna's two 600 MiB recordings stayed past her 1 GiB")
	}
	for what, id := range map[string]string{"the newer 600 MiB recording": big2, "her newest recording": third} {
		if !exists(id) {
			t.Errorf("%s was swept with her oldest", what)
		}
	}
	for _, id := range append(bobs, hosts...) {
		if !exists(id) {
			t.Errorf("recording %s, not anna's, was swept for her bounds", id)
		}
	}
}

// TestPeerTimelineRange: plan m2x step 9 (ADR-0115 Decision 5). The peer route serves a peer
// its own finished timeline by Range as application/x-ndjson, marked untrusted: two ranges
// concatenate to the host's file, whose SHA-256 is the one the recording published; several
// ranges in one header get the whole file; and its own recording that still runs is 409 with
// nothing of the file. Seen failing with the running recording served as a growing file (200
// with half a timeline).
func TestPeerTimelineRange(t *testing.T) {
	lab, r := newUILab(t)
	anna := lab.client.host.Remote(remoteName)
	annaID := lab.client.host.Identity()

	rec := recordingOf(t, "anna's recordUi", linkCall(t, anna, "device.recordUi", phoneArgs))
	r.AddEvents(clickAt(1), clickAt(2), clickAt(3))
	waitFor(t, "the events in the timeline", func() bool {
		return listedOf(t, "anna's list", linkCall(t, anna, "device.uiRecordings", `{"id":"`+rec.ID+`"}`))[0].Events >= 3
	})

	resp, body := fetchRecording(t, lab, annaID, "r."+rec.ID, "")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("anna's own running recording: status %d; want 409", resp.StatusCode)
	}
	if strings.Contains(string(body), `"kind"`) {
		t.Errorf("the 409 carries part of the timeline: %q", body)
	}
	resp, _ = fetchRecording(t, lab, annaID, "r."+rec.ID, "bytes=0-9")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("a ranged fetch of her running recording: status %d; want 409", resp.StatusCode)
	}

	end := stoppedOf(t, "anna's stop", linkCall(t, anna, "device.stopUiRecording", phoneArgs))
	if end.Recording == nil || end.Recording.SHA256 == "" {
		t.Fatalf("anna's stop = %+v; want the finished recording with its SHA-256", end)
	}
	waitEnded(t, lab, rec.ID)
	want, err := os.ReadFile(timelineOf(lab, rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(want); hex.EncodeToString(sum[:]) != end.Recording.SHA256 {
		t.Fatalf("the published SHA-256 %s is not the host's file's", end.Recording.SHA256)
	}
	cut := len(want) / 3

	resp1, a := fetchRecording(t, lab, annaID, "r."+rec.ID, fmt.Sprintf("bytes=0-%d", cut-1))
	resp2, b := fetchRecording(t, lab, annaID, "r."+rec.ID, fmt.Sprintf("bytes=%d-", cut))
	if resp1.StatusCode != http.StatusPartialContent || resp2.StatusCode != http.StatusPartialContent {
		t.Fatalf("the ranges answered %d and %d; want 206", resp1.StatusCode, resp2.StatusCode)
	}
	if string(a)+string(b) != string(want) {
		t.Errorf("the two ranges are %d and %d bytes and do not concatenate to the host's %d", len(a), len(b), len(want))
	}
	if got := resp2.Header.Get("Content-Range"); got != fmt.Sprintf("bytes %d-%d/%d", cut, len(want)-1, len(want)) {
		t.Errorf("the second range's Content-Range is %q", got)
	}
	if sum := sha256.Sum256([]byte(string(a) + string(b))); hex.EncodeToString(sum[:]) != end.Recording.SHA256 {
		t.Error("the concatenated ranges do not hash to the published SHA-256")
	}
	if ct := resp1.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type %q; want application/x-ndjson", ct)
	}
	if resp1.Header.Get("X-Droidyard-Untrusted") != "true" || resp1.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the timeline is not marked untrusted and uncacheable: %v", resp1.Header)
	}

	resp, whole := fetchRecording(t, lab, annaID, "r."+rec.ID, "bytes=0-1,5-6")
	if resp.StatusCode != http.StatusOK || string(whole) != string(want) || strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/") {
		t.Errorf("several ranges: status %d, %q, %d bytes; want the whole file as one body", resp.StatusCode, resp.Header.Get("Content-Type"), len(whole))
	}
}
