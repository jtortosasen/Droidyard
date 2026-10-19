package hosting

import (
	"context"
	"fmt"
	"slices"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// Admit is the host's side of a join whose code was accepted (ADR-0107 §3, §5): the
// certificate with this fingerprint is paired, under the name it already has in hosting.peers
// or one made from proposed (another machine's text, cleaned by NameFrom), and that name is
// added to what the device's hosting.lend entry lends it to, or to what its foreground share
// lends it to (ADR-0108 §3); one write. host is the name this machine gives itself. A device
// whose share has stopped refuses.
//
// Admit does not call Reconcile. It runs on a join connection, and a Reconcile that closed
// the listener this very connection came in on would wait for the connection it runs on
// (the shutdown of a listener waits for its handlers); the join route is not counted among
// those handlers, so that could not deadlock here, but nothing needs it: the pin and the
// lending are read live, at the peer's next handshake and call.
//
// The code was consumed before Admit runs, so an Admit that fails leaves it spent: the host's
// human shares again for a new one. Its errors say so and never carry proposed, which is
// another machine's text and ends up in daemon.log.
func (h *Host) Admit(fingerprint, proposed, device string) (peer, host string, err error) {
	notLent := false
	detached := false
	_, cerr := h.Edit(context.Background(), func(cur policy.GetResult) (map[string]any, *capability.Error) {
		at := slices.IndexFunc(cur.Hosting.Lend, func(l policy.Lend) bool { return l.Device == device })
		_, held := h.ForegroundOwner(device)
		detached = at >= 0
		if at < 0 && !held {
			notLent = true
			return nil, capability.NewError(capability.ErrInvalidParams, errNotLent.Error())
		}
		peers := slices.Clone(cur.Hosting.Peers)
		if peers == nil {
			peers = []policy.HostPeer{}
		}
		peer = ""
		for _, p := range peers {
			if SamePin(p.Fingerprint, fingerprint) {
				peer = p.Name
			}
		}
		if peer == "" {
			// A name lent to before it was admitted (host lend --to) is taken too: whoever is
			// admitted under it gets what it is lent, and a code reaches one device only.
			taken := make([]string, 0, len(peers))
			for _, p := range peers {
				taken = append(taken, p.Name)
			}
			for _, l := range cur.Hosting.Lend {
				taken = append(taken, l.To...)
			}
			peer = NameFrom(proposed, taken)
			peers = append(peers, policy.HostPeer{Name: peer, Fingerprint: fingerprint})
		}
		if at < 0 {
			// A foreground share lends in memory; the pairing alone is written.
			return map[string]any{"hosting": map[string]any{"peers": peers}}, nil
		}
		lend := make([]policy.Lend, len(cur.Hosting.Lend))
		for i, l := range cur.Hosting.Lend {
			lend[i] = policy.Lend{Device: l.Device, To: slices.Clone(l.To)}
			if lend[i].To == nil {
				lend[i].To = []string{}
			}
		}
		if !slices.Contains(lend[at].To, peer) {
			lend[at].To = append(lend[at].To, peer)
		}
		return map[string]any{"hosting": map[string]any{"peers": peers, "lend": lend}}, nil
	})
	if cerr != nil {
		if notLent {
			return "", "", errNotLent
		}
		return "", "", fmt.Errorf("the configuration could not take the peer (%s), so the join is refused; its code is spent", cerr.Code)
	}
	// The foreground share, when there is one, lends to the peer too; it may have ended since
	// the write, and then only a hosting.lend entry could still lend the device.
	if !h.admitForeground(device, fingerprint) && !detached {
		return "", "", errNotLent
	}
	return peer, OwnName(nil), nil
}
