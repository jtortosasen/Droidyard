package hosting

import (
	"slices"
	"sync"
)

// Why a foreground share ended (ADR-0108 §3, §6): the result of hosting.watchShare.
const (
	// ShareUnshared: hosting.unshare stopped it.
	ShareUnshared = "unshared"
	// ShareConnection: the native connection that made it ended.
	ShareConnection = "connection"
	// ShareStopping: the daemon is stopping.
	ShareStopping = "stopping"
)

// foreground is one share held in memory (ADR-0108 §3): a share the CLI that made it
// holds open. It is never in hosting.lend and never persisted, so a crash cannot leave a
// phone lent. to holds the fingerprints of the peers that joined it, not their names: a
// name a revoke freed and another machine takes later is not the one that joined.
type foreground struct {
	owner  string // the origin id of the native connection that made it
	to     []string
	done   chan struct{}
	reason string
}

// foregrounds are the foreground shares by serial.
type foregrounds struct {
	mu     sync.Mutex
	shares map[string]*foreground
}

// StartForeground begins a foreground share of serial, owned by the native connection with
// this origin id, lending to no one yet. It reports false, changing nothing, when serial is
// already shared in the foreground.
func (h *Host) StartForeground(serial, owner string) bool {
	h.fg.mu.Lock()
	defer h.fg.mu.Unlock()
	if h.fg.shares == nil {
		h.fg.shares = map[string]*foreground{}
	}
	if _, ok := h.fg.shares[serial]; ok {
		return false
	}
	h.fg.shares[serial] = &foreground{owner: owner, done: make(chan struct{})}
	return true
}

// ForegroundOwner is the origin id of the connection that owns the foreground share of
// serial; ok is false when there is none.
func (h *Host) ForegroundOwner(serial string) (owner string, ok bool) {
	h.fg.mu.Lock()
	defer h.fg.mu.Unlock()
	f := h.fg.shares[serial]
	if f == nil {
		return "", false
	}
	return f.owner, true
}

// ForegroundCount is how many foreground shares there are: the listeners are wanted while
// there is one, as while a device is lent (reconcileListeners).
func (h *Host) ForegroundCount() int {
	h.fg.mu.Lock()
	defer h.fg.mu.Unlock()
	return len(h.fg.shares)
}

// EndForeground ends the foreground share of serial for this reason, voiding its unused
// codes. It reports whether there was one.
func (h *Host) EndForeground(serial, reason string) bool {
	h.fg.mu.Lock()
	f := h.fg.shares[serial]
	if f != nil {
		delete(h.fg.shares, serial)
		f.reason = reason
		close(f.done)
	}
	h.fg.mu.Unlock()
	if f == nil {
		return false
	}
	h.pending.Void(serial)
	return true
}

// EndForegroundAll ends every foreground share and returns their serials.
func (h *Host) EndForegroundAll(reason string) []string {
	return h.endForegroundWhere(reason, func(*foreground) bool { return true })
}

// EndForegroundOwnedBy ends the foreground shares the connection with this origin id owns
// and returns their serials: the hook of a native connection that ended (ADR-0108 §3).
func (h *Host) EndForegroundOwnedBy(owner, reason string) []string {
	return h.endForegroundWhere(reason, func(f *foreground) bool { return f.owner == owner })
}

func (h *Host) endForegroundWhere(reason string, match func(*foreground) bool) []string {
	h.fg.mu.Lock()
	var ended []string
	for serial, f := range h.fg.shares {
		if match(f) {
			delete(h.fg.shares, serial)
			f.reason = reason
			close(f.done)
			ended = append(ended, serial)
		}
	}
	h.fg.mu.Unlock()
	slices.Sort(ended)
	for _, serial := range ended {
		h.pending.Void(serial)
	}
	return ended
}

// admitForeground adds the peer with this fingerprint to what the foreground share of serial
// lends to. It reports false when there is no such share.
func (h *Host) admitForeground(serial, fingerprint string) bool {
	h.fg.mu.Lock()
	defer h.fg.mu.Unlock()
	f := h.fg.shares[serial]
	if f == nil {
		return false
	}
	if !slices.ContainsFunc(f.to, func(p string) bool { return SamePin(p, fingerprint) }) {
		f.to = append(f.to, fingerprint)
	}
	return true
}

// Lent reports whether the host lends serial to the peer named peer now: hosting.lend, or a
// foreground share the peer joined (ADR-0099 §1, ADR-0108 §3). It is the lending alone, as
// policy.Policy.Lent is: that the device is physical and that the physical policy admits it
// are the caller's to check. A configuration that cannot be used lends nothing, a
// foreground share included, since the peer's name is read from it.
func (h *Host) Lent(peer, serial string) bool {
	hp, err := h.cfg.Hosting()
	if err != nil {
		return false
	}
	for _, l := range hp.Lend {
		if l.Device == serial && slices.Contains(l.To, peer) {
			return true
		}
	}
	h.fg.mu.Lock()
	defer h.fg.mu.Unlock()
	f := h.fg.shares[serial]
	if f == nil {
		return false
	}
	for _, p := range hp.Peers {
		if p.Name == peer {
			return slices.ContainsFunc(f.to, func(fp string) bool { return SamePin(fp, p.Fingerprint) })
		}
	}
	return false
}

// ShareWatch is a view of one foreground share, for hosting.watchShare.
type ShareWatch struct {
	h *Host
	f *foreground
}

// WatchForeground returns a view of the foreground share of serial that the connection with
// this origin id owns; ok is false when there is none, or another connection owns it.
func (h *Host) WatchForeground(serial, owner string) (*ShareWatch, bool) {
	h.fg.mu.Lock()
	defer h.fg.mu.Unlock()
	f := h.fg.shares[serial]
	if f == nil || f.owner != owner {
		return nil, false
	}
	return &ShareWatch{h: h, f: f}, true
}

// Done is closed when the share ends.
func (w *ShareWatch) Done() <-chan struct{} { return w.f.done }

// Reason is why the share ended (ShareUnshared, ShareConnection, ShareStopping); empty
// while it has not.
func (w *ShareWatch) Reason() string {
	w.h.fg.mu.Lock()
	defer w.h.fg.mu.Unlock()
	return w.f.reason
}

// Peers are the peers that joined the share, in the order they joined, each with whether it
// has a link open now. A peer the host has since unpaired is not listed.
func (w *ShareWatch) Peers() []PeerHealth {
	w.h.fg.mu.Lock()
	to := slices.Clone(w.f.to)
	w.h.fg.mu.Unlock()
	hp, err := w.h.cfg.Hosting()
	if err != nil {
		return []PeerHealth{}
	}
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	out := []PeerHealth{}
	for _, fp := range to {
		for _, p := range hp.Peers {
			if SamePin(p.Fingerprint, fp) {
				out = append(out, PeerHealth{Name: p.Name, Connected: w.h.open[p.Name] > 0})
			}
		}
	}
	return out
}
