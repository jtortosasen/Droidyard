package device

import (
	"context"
	"strings"

	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// The lease of a lent device at the device capabilities (ADR-0104), over the fake adb, the
// real policy (hosting.lend and occupation, read live) and a registry of real sessions: which
// calls take the lease, who is refused, what ends it, and that a refusal comes before any adb
// command that addresses the device (the fake's log).

const leaseDevice = "R58M004" // lent to anna and bob

const leaseConfigBody = `"devices":{"allowPhysical":true,"allowlist":["R58M001","R58M002","R58M004"]},
 "hosting":{"peers":[{"name":"anna","fingerprint":"` + fp1 + `"},{"name":"bob","fingerprint":"` + fp2 + `"}],
  "lend":[{"device":"R58M001","to":["anna"]},{"device":"R58M002","to":["bob"]},{"device":"R58M004","to":["anna","bob"]}]}`

// leaseConfig is a host's configuration with occupation.conflict set to conflict.
func leaseConfig(conflict string) string {
	return `{"schemaVersion":1,"occupation":{"conflict":"` + conflict + `"},` + leaseConfigBody + `}`
}

// leaseFixture is a host with two phones lent as above, the registry wired as the daemon wires
// it, the links of anna and bob as peer sessions and one MCP agent, "host-agent".
func leaseFixture(t *testing.T, conflict string) *hostFixture {
	t.Helper()
	f := newHostFixture(t, uiList(phoneLine, usbLine("R58M001"), usbLine("R58M002"), usbLine(leaseDevice)),
		[]string{"R58M001", "R58M002", leaseDevice}, leaseConfig(conflict))
	devicetest.Write(t, f.dir, leaseDevice+".uninstall.out", []byte("Success\n"))
	s := f.ops.Sessions()
	// The registry reads occupation.conflict from the same configuration, as the daemon wires it.
	s.UseOccupation(policyAt(t, f.cfg).Occupation)
	f.adb.SetOccupation(s)
	s.ConnectPeer("peer-anna", anna)
	s.ConnectPeer("peer-bob", bob)
	s.Connect("mcp-1", "chat-1")
	s.Named("mcp-1", "host-agent")
	return f
}

// as runs capName as the host's own caller of that kind: an MCP agent, the UI or the CLI.
func (f *hostFixture) as(t *testing.T, client capability.ClientKind, capName, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: capName, Params: []byte(params), Client: client,
		Origin: capability.Origin{ID: string(client) + "-1", Client: client}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// agent runs capName as the MCP session "mcp-1".
func (f *hostFixture) agent(t *testing.T, capName, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: capName, Params: []byte(params), Client: capability.ClientMCP,
		Origin: capability.Origin{ID: "mcp-1", Client: capability.ClientMCP}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *hostFixture) holder(serial string) (capability.Holder, bool) {
	return f.ops.Sessions().DeviceHolder(serial, "")
}

// addressed counts the fake's invocations that name serial (-s <serial> ...).
func (f *hostFixture) addressed(t *testing.T, serial string) int {
	n := 0
	for _, c := range devicetest.Calls(t, f.dir) {
		if strings.HasPrefix(c, "-s "+serial+" ") {
			n++
		}
	}
	return n
}

// leased is every capability a peer may call that names a device, with parameters that pass
// their validation: all of them take the lease, the reads and the live view's input included.
func leased(serial string) []struct{ name, params string } {
	d := `"device":"` + serial + `"`
	return []struct{ name, params string }{
		{"device.uiTree", `{` + d + `}`},
		{"device.screenshot", `{` + d + `}`},
		{"device.logs", `{` + d + `}`},
		{"device.tap", `{` + d + `,"x":0.5,"y":0.5}`},
		{"device.swipe", `{` + d + `,"path":[{"x":0.5,"y":0.8,"t":0},{"x":0.5,"y":0.2,"t":200}]}`},
		{"device.pressKey", `{` + d + `,"key":"home"}`},
		{"device.tapElement", `{` + d + `,"target":{"desc":"Navigate up"}}`},
		{"device.launch", `{` + d + `,"applicationId":"dev.droidyard.formapp"}`},
		{"device.uninstall", `{` + d + `,"applicationId":"dev.droidyard.formapp"}`},
		{"device.inputText", `{` + d + `,"text":"abc"}`},
		{"device.installUpload", `{` + d + `,"upload":"00000000000000000000000000000000"}`},
	}
}

// Decision (ADR-0104 §2): a paired peer's call of any kind on a lent device takes the lease in
// the preamble, so while anna holds it every other lent capability, reads and the live view's
// input included, fails DEVICE_OCCUPIED for bob naming anna, before any adb command addresses
// the device. Seen failing with the lease taken only by acting calls (authorizeActing): the
// reads, the input and installUpload answered bob. The lease is taken after the lending check:
// a device that is not lent to a peer is DEVICE_NOT_FOUND for it whoever holds it, so a peer
// is never told who uses a device it may not know.
func TestLeaseIsTakenByAnyCall(t *testing.T) {
	f := leaseFixture(t, "refuse")
	if out := f.tree(t, anna, leaseDevice); out.State != capability.StateCompleted {
		t.Fatalf("anna's uiTree: %s, %+v", out.State, out.Error)
	}
	if h, ok := f.holder(leaseDevice); !ok || h.Name != anna || !h.Peer {
		t.Fatalf("after anna's read the device is held by %+v, %v; want peer anna", h, ok)
	}
	for _, c := range leased(leaseDevice) {
		before := f.addressed(t, leaseDevice)
		ce := mustFail(t, f.call(t, bob, c.name, c.params), ErrOccupied)
		if !strings.HasPrefix(ce.Message, "peer anna (since ") || !strings.HasSuffix(ce.Message, " uses the device "+leaseDevice) {
			t.Errorf("%s: message %q; want it to name peer anna and the device", c.name, ce.Message)
		}
		if !strings.Contains(ce.NextStep, "person who runs the host") {
			t.Errorf("%s: nextStep %q; a peer is told to wait or ask the host's person", c.name, ce.NextStep)
		}
		if after := f.addressed(t, leaseDevice); after != before {
			t.Errorf("%s: a refused call ran %d adb commands on the device", c.name, after-before)
		}
	}
	if h, _ := f.holder(leaseDevice); h.Name != anna {
		t.Errorf("a refused peer changed the holder to %+v", h)
	}

	// bob holds R58M002, which is lent to him alone: anna is told it does not exist.
	if out := f.tree(t, bob, "R58M002"); out.State != capability.StateCompleted {
		t.Fatalf("bob's uiTree of his device: %s, %+v", out.State, out.Error)
	}
	ce := mustFail(t, f.tree(t, anna, "R58M002"), ErrNotFound)
	if strings.Contains(ce.Message, bob) {
		t.Errorf("a device not lent to anna said who holds it: %q", ce.Message)
	}
	// device.list takes no lease: a peer listing devices holds nothing.
	f2 := leaseFixture(t, "refuse")
	f2.listed(t, anna)
	if h, ok := f2.holder(leaseDevice); ok {
		t.Errorf("device.list took the lease: %+v", h)
	}
}

// Decision (ADR-0104 §2): the host's agents and the host's people. With a peer holding, an
// agent's acting call is refused whatever occupation.conflict says (allow lets two agents
// share a device, never an agent and a peer) while its reads answer, and the host's UI and
// CLI act; with an agent holding first, a peer's first call is refused naming "another chat".
// Seen failing with the configuration honoured for peers: under allow the agent's launch
// went on beside anna.
func TestLeaseAndTheHostsOwnCallers(t *testing.T) {
	for _, conflict := range []string{"refuse", "allow"} {
		f := leaseFixture(t, conflict)
		f.tree(t, anna, leaseDevice)

		before := f.addressed(t, leaseDevice)
		for _, c := range []struct{ name, params string }{
			{"device.launch", `{"device":"` + leaseDevice + `","applicationId":"dev.droidyard.formapp"}`},
			{"device.uninstall", `{"device":"` + leaseDevice + `","applicationId":"dev.droidyard.formapp"}`},
			{"device.inputText", `{"device":"` + leaseDevice + `","text":"abc"}`},
		} {
			ce := mustFail(t, f.agent(t, c.name, c.params), ErrOccupied)
			if !strings.HasPrefix(ce.Message, "peer anna (since ") {
				t.Errorf("%s: an agent's refusal says %q; want it to name peer anna", c.name, ce.Message)
			}
			if !strings.Contains(ce.NextStep, "release "+leaseDevice) {
				t.Errorf("%s: nextStep %q; an agent is told the user can release the device", c.name, ce.NextStep)
			}
		}
		if f.addressed(t, leaseDevice) != before {
			t.Errorf("%s: a refused agent ran adb commands on the device", conflict)
		}
		if out := f.agent(t, "device.uiTree", `{"device":"`+leaseDevice+`"}`); out.State != capability.StateCompleted {
			t.Errorf("%s: the agent's read: %s, %+v", conflict, out.State, out.Error)
		}
		for _, client := range []capability.ClientKind{capability.ClientUI, capability.ClientCLI} {
			out := f.as(t, client, "device.uninstall", `{"device":"`+leaseDevice+`","applicationId":"dev.droidyard.formapp"}`)
			if out.State != capability.StateCompleted {
				t.Errorf("%s: the host's %s was refused: %s, %+v", conflict, client, out.State, out.Error)
			}
		}
		if h, _ := f.holder(leaseDevice); h.Name != anna {
			t.Errorf("%s: the host's own callers took the lease: %+v", conflict, h)
		}

		// The agent first.
		g := leaseFixture(t, conflict)
		if out := g.agent(t, "device.uninstall", `{"device":"`+leaseDevice+`","applicationId":"dev.droidyard.formapp"}`); out.State != capability.StateCompleted {
			t.Fatalf("%s: the agent's uninstall: %s, %+v", conflict, out.State, out.Error)
		}
		before = g.addressed(t, leaseDevice)
		ce := mustFail(t, g.tree(t, anna, leaseDevice), ErrOccupied)
		if !strings.HasPrefix(ce.Message, "another chat (host-agent, since ") {
			t.Errorf("%s: anna's refusal says %q; want it to name the agent", conflict, ce.Message)
		}
		if g.addressed(t, leaseDevice) != before {
			t.Errorf("%s: a refused peer ran adb commands on the device", conflict)
		}
	}
}

// Decision (ADR-0104 §4): the lease ends when the device is no longer lent to its holder. The
// check reads the configuration as it stands, ends only a peer's lease whose device is no
// longer lent to that peer (or admitted by the host's physical policy), and leaves the host
// agents' holds and the leases still in force. Seen failing with the check answering "still
// lent" for everything: anna's hold of R58M001 outlived its un-lending.
func TestSweepLeasesEndsAnUnlentLease(t *testing.T) {
	f := leaseFixture(t, "refuse")
	f.tree(t, anna, "R58M001")
	f.tree(t, bob, "R58M002")
	f.tree(t, anna, leaseDevice)
	if out := f.agent(t, "device.uninstall", `{"device":"`+leaseDevice+`","applicationId":"dev.droidyard.formapp"}`); out.State != capability.StateFailed || out.Error.Code != ErrOccupied {
		t.Fatalf("the agent's uninstall of anna's device: %s, %+v", out.State, out.Error)
	}

	f.adb.SweepLeases()
	for _, serial := range []string{"R58M001", "R58M002", leaseDevice} {
		if _, ok := f.holder(serial); !ok {
			t.Errorf("a sweep ended the lease of %s, which is still lent", serial)
		}
	}

	// R58M001 is no longer lent to anna; R58M004 is no longer in the host's allowlist.
	body := strings.Replace(leaseConfig("refuse"), `{"device":"R58M001","to":["anna"]},`, "", 1)
	body = strings.Replace(body, `"allowlist":["R58M001","R58M002","R58M004"]`, `"allowlist":["R58M001","R58M002"]`, 1)
	writePolicyConfig(t, f.cfg, body)
	f.adb.SweepLeases()
	for _, serial := range []string{"R58M001", leaseDevice} {
		if h, ok := f.holder(serial); ok {
			t.Errorf("the lease of %s survived its device leaving the lent set: held by %+v", serial, h)
		}
	}
	if h, ok := f.holder("R58M002"); !ok || h.Name != bob {
		t.Errorf("a sweep touched bob's lease of R58M002, still lent to him: %+v, %v", h, ok)
	}
	mustFail(t, f.tree(t, anna, "R58M001"), ErrNotFound)

	// A host agent's hold is not a lease: it is never ended by the check.
	g := leaseFixture(t, "refuse")
	g.agent(t, "device.uninstall", `{"device":"`+leaseDevice+`","applicationId":"dev.droidyard.formapp"}`)
	writePolicyConfig(t, g.cfg, strings.Replace(leaseConfig("refuse"), `{"device":"R58M004","to":["anna","bob"]}`, `{"device":"R58M001","to":["anna"]}`, 1))
	g.adb.SweepLeases()
	if h, ok := g.holder(leaseDevice); !ok || h.Name != "host-agent" {
		t.Errorf("the check ended an agent's hold: %+v, %v", h, ok)
	}
}
