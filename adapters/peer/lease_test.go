package peer_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// TestPeerLinkIsASession is ADR-0104 §1 and §3 at the adapter: a paired peer's link is a
// session of the host's registry named by the peer, for as long as the link lives; what its
// calls hold is held by it (shown by PeerHolds, with the peer's configured name); every
// request on the link, whatever it asks, is a request of the session and restarts the lease's
// idle time; and the link's end frees what it held. Seen failing without the registration
// (PeerHolds empty), without Seen on each request (the rival took the lease at once: the
// link's idle time ran from its connection), and without End (the hold outlived the link).
func TestPeerLinkIsASession(t *testing.T) {
	host, client, link := pair(t)
	take := func(session string) bool {
		_, refused := host.ops.Sessions().OccupyDevice(session, "S1")
		return !refused
	}
	host.reg.MustRegister(capability.Descriptor{Name: "peertest.take", Kind: capability.Command, Scope: capability.Global, Lendable: true,
		Description: "Lent: takes the lease of S1 for the calling link.", Params: none{}, Result: tick{},
		Handler: func(_ context.Context, call *capability.Call) (any, error) {
			if !take(call.Origin.ID) {
				return nil, capability.NewError("PEERTEST_REFUSED", "refused")
			}
			return tick{}, nil
		}})

	var clock atomic.Int64 // the registry reads it from the link's goroutines
	clock.Store(time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC).UnixNano())
	move := func(d time.Duration) { clock.Add(int64(d)) }
	host.ops.Sessions().UseClock(func() time.Time { return time.Unix(0, clock.Load()) })

	if env := call(t, link, "peertest.take", ""); env.Error != nil {
		t.Fatalf("the link's first call on a free device: %+v", env.Error)
	}
	holds := host.ops.Sessions().PeerHolds()
	if len(holds) != 1 || holds[0].Peer != "anna" || holds[0].Serial != "S1" {
		t.Fatalf("PeerHolds = %+v; want S1 held by the link of peer anna", holds)
	}
	link1 := holds[0].Session

	// A rival link of another peer is refused while the lease is held.
	host.ops.Sessions().ConnectPeer("rival", "bob")
	if take("rival") {
		t.Fatal("another peer's link took a leased device")
	}

	// 14 minutes of silence, then any request on the link (not a device call): the lease is
	// not idle 14 minutes later, though 28 minutes have passed since its device call.
	move(14 * time.Minute)
	if env := call(t, link, "peertest.echo", ""); env.Error != nil {
		t.Fatal(env.Error)
	}
	move(14 * time.Minute)
	if take("rival") {
		t.Fatal("a request on the link did not restart the lease's idle time")
	}
	move(2 * time.Minute)
	if !take("rival") {
		t.Fatal("a lease 16 minutes after the link's last request was not taken over")
	}
	host.ops.Sessions().Seen("rival") // a live link of bob's: its request was just now
	if env := call(t, link, "peertest.take", ""); env.Error == nil || env.Error.Code != "PEERTEST_REFUSED" {
		t.Errorf("the link's call on a device its rival now holds: %+v; want it refused", env.Error)
	}
	host.ops.Sessions().End("rival")

	// The link's end frees what it holds.
	if env := call(t, link, "peertest.take", ""); env.Error != nil {
		t.Fatalf("the link's call on the freed device: %+v", env.Error)
	}
	if got := host.ops.Sessions().PeerHolds(); len(got) != 1 || got[0].Session != link1 && got[0].Peer != "anna" {
		t.Fatalf("PeerHolds = %+v; want the link's again", got)
	}
	client.host.Stop()
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the link's hold to be freed with the link", func() bool {
		return len(host.ops.Sessions().PeerHolds()) == 0
	})
}
