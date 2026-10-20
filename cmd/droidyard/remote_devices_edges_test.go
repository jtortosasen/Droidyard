package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Decision (ADR-0099 §1): the devices a host borrows are not the ones it lends. A host that
// is itself a client of a remote "far", whose allowlist names far/X1, answers a peer that
// names far/X1 as it answers any device it does not lend, DEVICE_NOT_FOUND: the call is not
// relayed onward over the host's own link, which would skip hosting.lend. The host's own
// link to far is down, so a relayed call would have answered DEVICE_NOT_READY instead.
func TestPeerCannotReachWhatTheHostBorrows(t *testing.T) {
	t.Parallel()
	lab := newRemoteLabWith(t, labOptions{hostBorrows: true})
	if lab.host.host.Remote("far") == nil {
		t.Fatal("the host has no link to far; the test would prove nothing")
	}
	// What the host's own caller gets: the call is relayed (and far is not there).
	env, _ := lab.host.call(t, "device.launch", "--params", `{"device":"far/X1","applicationId":"dev.droidyard.formapp"}`)
	if env.Error == nil || env.Error.Code != device.ErrNotReady {
		t.Fatalf("the host's own launch on far/X1: %+v; want DEVICE_NOT_READY (the relay is what the peer must not reach)", env.Error)
	}

	peer := lab.client.host.Remote(remoteName)
	for _, c := range []struct{ capName, params string }{
		{"device.launch", `{"device":"far/X1","applicationId":"dev.droidyard.formapp"}`},
		{"device.uiTree", `{"device":"far/X1"}`},
		{"device.uninstall", `{"device":"far/X1","applicationId":"dev.droidyard.formapp"}`},
		{"device.inputText", `{"device":"far/X1","text":"x"}`},
	} {
		resp, err := peer.Call(context.Background(), capability.RequestEnvelope{Capability: c.capName, Params: json.RawMessage(c.params)})
		if err != nil || resp.Error == nil || resp.Error.Code != device.ErrNotFound {
			t.Errorf("a peer's %s on far/X1: %+v / %v; want DEVICE_NOT_FOUND", c.capName, resp.Error, err)
		}
	}
	// And the host's listing for the peer holds no borrowed row either.
	resp, err := peer.Call(context.Background(), capability.RequestEnvelope{Capability: "device.list", Params: json.RawMessage(`{}`)})
	var r device.ListResult
	if err != nil || json.Unmarshal(resp.Result, &r) != nil || !slices.Equal(listedIDs(r.Devices), []string{phone1}) {
		t.Errorf("the peer's view of the host's list: %+v / %v; want %s alone", r.Devices, err, phone1)
	}
}

// Decision (ADR-0100 §6): no local adb is needed for a remote device. A client with no adb
// at all lists the devices its remotes lend, with a warning that its own cannot be listed,
// and uses them; a daemon with no remote still answers DEVICE_ADB_NOT_FOUND, as before.
func TestRemoteDevicesNeedNoLocalADB(t *testing.T) {
	t.Parallel()
	lab := newRemoteLabWith(t, labOptions{noClientADB: true})
	rows, warnings, env := lab.list(t)
	if got := listedIDs(rows); !slices.Equal(got, []string{remotePhone}) {
		t.Fatalf("device.list with no adb = %v (error %+v); want %s", got, env.Error, remotePhone)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "adb was not found") {
		t.Errorf("warnings = %q; want one saying adb was not found", warnings)
	}
	if !untrusted(env) {
		t.Error("a listing with a remote's rows is not marked untrusted")
	}
	if env := lab.on(t, "device.uiTree", `{`+dev(remotePhone)+`}`); env.Error != nil {
		t.Errorf("uiTree on %s with no local adb: %+v", remotePhone, env.Error)
	}

	alone := startInProcess(t)
	if env, _ := alone.call(t, "device.list"); env.Error == nil || env.Error.Code != device.ErrADBNotFound {
		t.Errorf("device.list with no adb and no remote: %+v; want DEVICE_ADB_NOT_FOUND", env.Error)
	}
}

// Decision (ADR-0100 §1): the remotes are asked while this machine's own listing runs, and a
// remote gets more than detailsTimeout (3 s), since its own listing can take `devices -l`
// and then the details. The host's `devices -l` takes 3.5 s here, over the details bound and
// under twice it, and the client's own 2.5 s: one after the other, the whole would take 6 s.
// The bound separates the two (3.5 s side by side, 6 s in sequence) and is the daemon's time
// for the operation, run in this process: the delays are the fakes', so they are not scaled,
// and the 3 s limit it also rests on is detailsTimeout, which no floor lengthens here. Seen failing
// with the remotes asked after the local listing (the operation took 6 s).
func TestRemoteListingBounds(t *testing.T) {
	t.Parallel()
	lab := newRemoteLabWith(t, labOptions{hostListDelay: 3500 * time.Millisecond, clientListDelay: 2500 * time.Millisecond})
	// The client daemon's operation, started in this process: the time is the listing's, not a
	// re-executed `droidyard call`'s start, which a loaded host takes seconds over.
	start := time.Now()
	id, err := lab.client.ops.Start(context.Background(), capability.Request{Capability: "device.list", Client: capability.ClientUI, Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := lab.client.ops.Wait(context.Background(), id, time.Now().Add(testtime.Bound(30*time.Second)))
	took := time.Since(start)
	raw, _ := json.Marshal(out.Result)
	var res device.ListResult
	if err != nil || out.State != capability.StateCompleted || json.Unmarshal(raw, &res) != nil {
		t.Fatalf("device.list: %+v, %v", out, err)
	}
	rows, warnings := res.Devices, res.Warnings
	if got := listedIDs(rows); !slices.Equal(got, []string{"emulator-5556", remotePhone}) {
		t.Errorf("device.list = %v (warnings %q); want the remote's row though its listing took over 3 s", got, warnings)
	}
	if took > 5500*time.Millisecond {
		t.Errorf("device.list took %s; the remote's listing and the local one must run side by side (3.5 s and 2.5 s)", took)
	}
}
