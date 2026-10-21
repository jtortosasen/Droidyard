package device

import (
	"context"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The lease of a lent device (ADR-0104): a paired peer's link holds it as the registry's
// occupation of the device, taken in the preamble (leaseFor). What is here is what the
// registry cannot do: stop the holder's streams when the lease ends, count them as activity
// while they are open, and notice that the device is no longer lent to its holder.

// leaseEndedCause is the cause of the stopped event (deviceGone) of a stream whose lease
// ended on the host's side: the host's release, or the device no longer lent (ADR-0104 §4).
const leaseEndedCause = "the host ended the lease"

// LeaseSweepEvery is how often a lent device is checked against what is lent now, the
// interval of the peer listener's pin check (ADR-0098 §3).
const LeaseSweepEvery = 2 * time.Second

// RunLeases checks the leases every LeaseSweepEvery until ctx ends, so un-lending a device
// ends its holder's view within that time (ADR-0104 §4).
func (a *ADB) RunLeases(ctx context.Context) {
	t := time.NewTicker(LeaseSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.SweepLeases()
		}
	}
}

// SweepLeases ends the lease of every device that is no longer lent to its holder, or that
// the host's own physical policy no longer admits, and stops the holder's streams on it.
// The device leaving and the link closing end a lease by their own paths.
func (a *ADB) SweepLeases() {
	if a.occupation == nil {
		return
	}
	for _, h := range a.occupation.PeerHolds() {
		if !a.lentNow(h.Peer, h.Serial) {
			// Only while the same link still holds it: a lease taken over meanwhile is not ended.
			a.occupation.ReleaseDeviceOf(h.Session, h.Serial)
		}
	}
}

// lentNow is whether serial is lent to the named peer by the configuration as it stands.
func (a *ADB) lentNow(peer, serial string) bool {
	return a.admitted(serial).Admitted && a.lending != nil && a.lending.Lent(peer, serial)
}

// endLease stops the streams the link session holds on serial (ADR-0104 §4): the live view,
// every Logcat stream and every UI events stream, each with `stopped deviceGone` and the
// cause of the host's end. The registry calls it after the lease is gone, outside its lock.
func (a *ADB) endLease(session, serial string) {
	a.screenViews.endLease(session, serial)
	a.streams.endLease(session, serial)
	a.uiStreams.endLease(session, serial)
}

// SetStartHook makes every live view, Logcat stream and UI events stream run f after it has
// started or joined its source and before it enters its table. Test-only: it is where a lease can end while a
// stream is starting, the window endLease cannot see. Nil removes it.
func (a *ADB) SetStartHook(f func()) {
	if f == nil {
		a.startHook.Store(nil)
		return
	}
	a.startHook.Store(&f)
}

// LengthenLimits makes no adb command of this ADB (Run: the probes, the listing, the
// forward, the secure check), no wait for a remote's listing and no wait for an evicted
// log stream's child end sooner than floor. Test-only, and for a test whose claim is not one
// of those limits: a fake adb is a process the test binary re-executes, and under load it
// starts later than a limit of 3 s (plan m2v step 5). A window the call asks for, a follow's,
// is not a limit and is not lengthened. Called once, at wiring, before any call; zero, the
// product's value, changes nothing.
func (a *ADB) LengthenLimits(floor time.Duration) { a.floor = floor }

// limit is d, or the floor LengthenLimits set when that is longer.
func (a *ADB) limit(d time.Duration) time.Duration { return max(d, a.floor) }

func (a *ADB) started() {
	if h := a.startHook.Load(); h != nil {
		(*h)()
	}
}

// leaseLost reports that a paired peer's call no longer holds the serial its stream is on: the
// lease ended while the stream was starting, before endLease could find it in its table
// (ADR-0104 §4). Called once the stream is in its table, so a lease that ends after the check
// finds the stream there. A call that is not a peer's has no lease.
func (a *ADB) leaseLost(call *capability.Call, serial string) bool {
	return a.occupation != nil && call.Origin.Client == capability.ClientPeer && call.Origin.ID != "" &&
		!a.occupation.HoldsDevice(call.Origin.ID, serial)
}

// streamActivity counts one open stream of a paired peer's link session as activity of its
// lease (ADR-0104 §3) and returns the function that ends the count, once; nil for a call
// that is not a peer's.
func (a *ADB) streamActivity(call *capability.Call) func() {
	if a.occupation == nil || call.Origin.Client != capability.ClientPeer || call.Origin.ID == "" {
		return nil
	}
	id := call.Origin.ID
	a.occupation.StreamOpened(id)
	var once sync.Once
	return func() { once.Do(func() { a.occupation.StreamClosed(id) }) }
}
