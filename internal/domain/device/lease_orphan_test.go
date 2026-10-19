package device

import (
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Decision (ADR-0104 §2; ADR-0101): a peer's call is an operation that outlives its link
// (the peer waits on it by name after it reconnects), and its authorization can run after the
// link ended, when the call has waited its turn behind a busy key. A call whose link session
// is gone never passes the device preamble, or it would read the screen of a device another
// peer leased since. With a holder it is DEVICE_OCCUPIED naming the holder; with none it is
// DEVICE_DISCONNECTED (a code every lent capability declares): the link is gone, no holder
// exists to name, and nothing on the device was changed. Neither runs an adb command on the
// device, and neither takes the lease. Seen failing with an unregistered session passing as
// free (the verifier's probe: anna read bob's device).
func TestPeerCallOfAnEndedLinkNeverPasses(t *testing.T) {
	f := leaseFixture(t, "refuse")
	if out := f.tree(t, bob, leaseDevice); out.State != capability.StateCompleted {
		t.Fatalf("bob: %s %+v", out.State, out.Error)
	}
	f.ops.Sessions().End("peer-anna")
	before := f.addressed(t, leaseDevice)
	ce := mustFail(t, f.tree(t, anna, leaseDevice), ErrOccupied)
	if !strings.HasPrefix(ce.Message, "peer bob (since ") {
		t.Errorf("the refusal says %q; want it to name peer bob", ce.Message)
	}
	if f.addressed(t, leaseDevice) != before {
		t.Error("a call of an ended link ran adb commands on the device bob leases")
	}
	if h, _ := f.holder(leaseDevice); h.Name != bob {
		t.Errorf("the holder is %+v after the orphan's call; want bob", h)
	}

	// Nobody holds R58M001: the call is refused all the same, and takes nothing.
	before = f.addressed(t, "R58M001")
	ce = mustFail(t, f.tree(t, anna, "R58M001"), ErrDisconnected)
	if !strings.Contains(ce.Message, "link") || !strings.Contains(ce.NextStep, "safe to repeat") {
		t.Errorf("the refusal is %q / %q; want it to say the link ended and that the call is safe to repeat", ce.Message, ce.NextStep)
	}
	if f.addressed(t, "R58M001") != before {
		t.Error("a call of an ended link ran adb commands on a free device")
	}
	if h, ok := f.holder("R58M001"); ok {
		t.Errorf("a call of an ended link took the lease: %+v", h)
	}
}
