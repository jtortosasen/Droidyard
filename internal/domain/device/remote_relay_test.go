package device

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// scriptedHost is a link to a host that answers a start with a running operation after
// release, and records every request. Like the real link, a call whose context ends before
// its answer returns the context's error.
type scriptedHost struct {
	mu      sync.Mutex
	seen    []string
	started chan struct{}
	release chan struct{}
}

func newScriptedHost() *scriptedHost {
	return &scriptedHost{started: make(chan struct{}, 1), release: make(chan struct{})}
}

func (h *scriptedHost) Call(ctx context.Context, req capability.RequestEnvelope) (capability.ResponseEnvelope, error) {
	h.mu.Lock()
	h.seen = append(h.seen, req.Capability)
	h.mu.Unlock()
	switch req.Capability {
	case "device.launch":
		h.started <- struct{}{}
		select {
		case <-h.release:
			return capability.ResponseEnvelope{Operation: &capability.OperationStatus{ID: "op-1", State: capability.StateRunning}}, nil
		case <-ctx.Done():
			return capability.ResponseEnvelope{}, ctx.Err()
		}
	}
	return capability.ResponseEnvelope{}, nil
}

func (h *scriptedHost) requests() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.seen...)
}

// Decision (ADR-0100 §3): a client cancel stops the host's operation even when it arrives
// while the request that starts the call is still on its way. The start is made on a
// context the caller cannot cancel, so the operation's id is always learned, and the
// operation is then cancelled; asked on the caller's own context it would return the
// context's error with no id, and the host's operation would run on while the client says
// cancelled.
func TestCancelDuringTheStartStillCancelsTheHostsOperation(t *testing.T) {
	h := newScriptedHost()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := callHost(ctx, h, "device.launch", json.RawMessage(`{}`))
		done <- err
	}()
	<-h.started
	cancel() // while the host has not yet answered the start
	close(h.release)
	if err := <-done; err != context.Canceled {
		t.Errorf("callHost = %v; want the caller's cancellation", err)
	}
	reqs := h.requests()
	if len(reqs) != 2 || reqs[1] != capability.OperationCancel {
		t.Errorf("requests to the host = %v; want the start, then operation.cancel of the operation it started", reqs)
	}
}

// oneRemote is the Remotes of a daemon that borrows from "c". Its link is a zero one,
// which answers every call as a link that is down does: a call that reaches it is visible
// as DEVICE_NOT_READY.
type oneRemote struct{ l *hosting.Link }

func (o oneRemote) Remote(name string) *hosting.Link {
	if name == "c" {
		return o.l
	}
	return nil
}
func (o oneRemote) Links() []*hosting.Link { return []*hosting.Link{o.l} }

// Decision (ADR-0099 §1): a paired peer's call is never relayed onward over the host's own
// link. On a host that also borrows from "c" and whose allowlist names c/X1, a local caller
// naming c/X1 is relayed (here: the link is down, DEVICE_NOT_READY), and a peer naming it
// gets what it gets for any device the host does not lend, DEVICE_NOT_FOUND, with the
// borrowed device named in no candidate list.
func TestPeerCallIsNotRelayedOnward(t *testing.T) {
	f := newHostFixture(t, uiList(phoneLine, usbLine("R58M001")), []string{"R58M001"}, lendingConfig)
	f.adb.remotes = oneRemote{&hosting.Link{}}
	f.adb.policy = admitting{"c/X1", "R58M001"}
	f.adb.lending = lendAll{}

	own := mustFail(t, f.call(t, "", "device.uiTree", `{"device":"c/X1"}`), ErrNotReady)
	if own.Details["state"] != "unreachable" {
		t.Fatalf("the host's own call on c/X1: %+v; want it relayed (the link is down)", own)
	}
	for _, c := range []struct{ capName, params string }{
		{"device.uiTree", `{"device":"c/X1"}`},
		{"device.launch", `{"device":"c/X1","applicationId":"dev.droidyard.formapp"}`},
		{"device.uninstall", `{"device":"c/X1","applicationId":"dev.droidyard.formapp"}`},
	} {
		ce := mustFail(t, f.call(t, anna, c.capName, c.params), ErrNotFound)
		for _, id := range online(t, ce) {
			if id == "c/X1" {
				t.Errorf("%s: the candidates name the borrowed device: %v", c.capName, online(t, ce))
			}
		}
	}
}

// lendAll lends every device to every peer, so that only the relay decides.
type lendAll struct{}

func (lendAll) Lent(string, string) bool { return true }

// Decision (ADR-0099 §4; plan m2t Q3): adb's version warning names the host's adb path and
// its tools.adb key, the host's configuration. A peer is told none of it, in a list or in
// the warnings a device call carries; the host's own caller still is.
func TestPeerGetsNoHostWarnings(t *testing.T) {
	port, _ := fakeServer(t, []byte("OKAY0004002a")) // a server at version 42 against a 1.0.41 client
	dir := t.TempDir()
	devicetest.Write(t, dir, "devices.out", []byte(uiList(usbLine("R58M001"))))
	devicetest.Write(t, dir, "R58M001.bootcompleted.out", []byte("1\n"))
	a := newFake(t, dir, port)
	a.SetPolicy(admitting{"R58M001"})
	a.SetLending(lendAll{})

	peer := capability.Origin{Client: capability.ClientPeer, Name: anna}
	pt, err := a.authorize(context.Background(), peer, "R58M001", "device.uiTree")
	if err != nil {
		t.Fatal(err)
	}
	if len(pt.warnings) != 0 {
		t.Errorf("a peer's call carries the host's warnings: %q", pt.warnings)
	}
	if _, warnings, err := a.listFor(context.Background(), peer); err != nil || len(warnings) != 0 {
		t.Errorf("a peer's list: warnings %q (%v); want none", warnings, err)
	}
	lt, err := a.authorize(context.Background(), capability.Origin{}, "R58M001", "device.uiTree")
	if err != nil || len(lt.warnings) != 1 {
		t.Errorf("the host's own call: warnings %q (%v); want the version mismatch", lt.warnings, err)
	}
}
