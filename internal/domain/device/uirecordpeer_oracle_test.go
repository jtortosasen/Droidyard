package device

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Decision (ADR-0104 §4): a listing frees the leases of devices that left it and ends the peer
// recordings on them; the host's own recording holds no lease and is never ended by one. Seen
// failing with every recorder ended by a listing (the host's recording ended deviceGone).
func TestAListingLeavesTheHostsRecordingAlone(t *testing.T) {
	f := newRecFix(t)
	f.start(t, recParams)
	out := f.call(t, "device.list", `{}`, capability.ClientUI, capability.Origin{})
	if out.State != capability.StateCompleted {
		t.Fatalf("device.list: %s %+v", out.State, out.Error)
	}
	time.Sleep(600 * time.Millisecond) // an ending recording leaves the set after its drain
	if got := f.adb.LiveRecorders(); got != 1 || f.live(t).State != RecordingRunning {
		t.Errorf("after a listing: %d recorders, recording %+v; want the host's, running", got, f.live(t))
	}
	f.stop(t)
}

// Decision (ADR-0099 §1): the serial of an emulator is never lent, so a peer's stop on the
// emulator the host records is answered exactly as for a serial that does not exist, and so is
// its list of that serial: a peer learns nothing of the host's recordings on devices that are not
// its to see. Seen failing with the stop reaching the recorder (DEVICE_UI_RECORDING_ACTIVE
// naming the emulator).
func TestPeerStopOnAnEmulatorIsNoOracle(t *testing.T) {
	f := newRecFix(t)
	rec := f.start(t, recParams)
	peer := capability.Origin{Client: capability.ClientPeer, Name: "anna", ID: "link-anna"}
	answer := func(capName, serial string) string {
		out := f.call(t, capName, `{"device":"`+serial+`"}`, capability.ClientPeer, peer)
		raw, _ := json.Marshal(struct {
			State  capability.State
			Result any
			Error  *capability.Error
		}{out.State, out.Result, out.Error})
		return string(raw)
	}
	for _, capName := range []string{"device.stopUiRecording", "device.uiRecordings"} {
		got, want := answer(capName, uiSerial), answer(capName, "NOSUCH01")
		if got != want {
			t.Errorf("anna's %s on the host's emulator: %s; want what a serial that does not exist gets, %s", capName, got, want)
		}
		if strings.Contains(got, rec.ID) {
			t.Errorf("anna's %s names the host's recording: %s", capName, got)
		}
	}
	if f.adb.LiveRecorders() != 1 {
		t.Error("the peer's calls ended the host's recording")
	}
	f.stop(t)
}
