package capability

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// The values of config.json's occupation.conflict (ADR-0094 §5, §7). Where Droidyard
// cannot ask, ask is refuse (ADR-0094 §6).
const (
	OccupationRefuse = "refuse"
	OccupationAsk    = "ask"
	OccupationAllow  = "allow"
)

// DefaultStaleAfter is how long a session may go without a request before it is shown
// idle, when the configuration says nothing (ADR-0094 §4: a working default).
const DefaultStaleAfter = 120 * time.Minute

// SweepInterval is how often the registry looks for sessions gone idle (ADR-0094 §4).
const SweepInterval = time.Minute

// PeerLeaseIdle is how long a paired peer's link session may go without a call and without
// an open stream before its lease on a lent device is idle and the next caller takes it
// over (ADR-0104 §3): a constant, not config.json's occupation.staleAfterMinutes.
const PeerLeaseIdle = 15 * time.Minute

// Holder is who holds a device, as another session is told (ADR-0094 §5). It carries no
// session id (ADR-0048 §2).
type Holder struct {
	Name  string
	Since time.Time
	Stale bool
	// Peer says the holder is a paired peer's link (ADR-0104 §1), whose Name is the peer's.
	Peer bool
}

// Sessions is the core's registry of MCP sessions, one per connection (ADR-0082 §5). It
// holds what an adapter must not keep on its own: which device each session holds, so the
// capabilities that act on one and the adapter read one value (ADR-0094). A session has no
// binding: its calls name their project (ADR-0137 §3). Nothing here is written to disk. A
// paired peer's link is a session too (ConnectPeer, ADR-0104): what it holds is the lease
// of the lent devices its calls name. Every method is safe for concurrent use.
//
// What a session shows (sessions_state.go) is published from here, under the same lock that
// changes it, so an event of a session is never published before the row listed can show it,
// and none after its end. The chat id is never published or listed (ADR-0088 §4).
type Sessions struct {
	mu   sync.Mutex
	byID map[string]*sessionRec
	next uint64         // the connection order of the next session
	pub  StatePublisher // session.state's bus; nil publishes nothing
	now  func() time.Time
	// devHolders is the one holder of each device, by serial (ADR-0094 §1, §2), under mu too.
	// devSeq counts the holds taken, so a listing read before a hold cannot release it
	// (ForgetDevicesAbsent).
	devHolders map[string]devHold
	devSeq     uint64
	// occupation is config.json's occupation section, read at each decision (ADR-0094
	// §7); nil means the defaults. Injected at serve: capability cannot import policy.
	occupation func() (conflict string, staleAfter time.Duration)
	// leaseEnd is told, outside mu, when the owner's release ends a paired peer's hold of a
	// device, so the holder's streams on it are stopped (ADR-0104 §4); nil tells no one.
	leaseEnd func(session, serial string)

	// occupyHook is a test seam: see SetOccupyHook.
	occupyHook atomic.Pointer[func()]
}

// sessionRec is one connection.
type sessionRec struct {
	chat string
	// name is what the client called itself, bounded by SessionName; empty until known.
	name string
	// seq is the connection order, at the connection time.
	seq uint64
	at  time.Time
	// seen is the last MCP request of the session (its connection, until the first); stale
	// is set once it is older than the configured staleAfterMinutes, and cleared by the
	// next request (ADR-0094 §4).
	seen  time.Time
	stale bool
	// peer is the configured name of the paired peer whose link this session is (ADR-0104
	// §1); empty for an MCP session. A peer's idleness is PeerLeaseIdle, and its open
	// streams count as activity: streams is how many it has.
	peer    string
	streams int
}

// NewSessions returns an empty registry.
func NewSessions() *Sessions {
	return &Sessions{byID: map[string]*sessionRec{}, devHolders: map[string]devHold{}, now: time.Now}
}

// UseOccupation gives the registry config.json's occupation section: f returns the
// conflict mode (OccupationRefuse, OccupationAsk or OccupationAllow) and the idle
// threshold, and is called outside the registry's lock, at each decision. Without one the
// defaults apply.
func (s *Sessions) UseOccupation(f func() (conflict string, staleAfter time.Duration)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.occupation = f
}

// UseLeaseEnd sets what is told when the owner's release ends a paired peer's hold of a
// device: the holder's session id and the serial. It is called outside the registry's lock.
func (s *Sessions) UseLeaseEnd(f func(session, serial string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseEnd = f
}

// UseClock replaces the registry's clock. Test-only seam.
func (s *Sessions) UseClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// occupationNow reads the configuration without holding mu, so no file is read under it.
func (s *Sessions) occupationNow() (string, time.Duration) {
	s.mu.Lock()
	f := s.occupation
	s.mu.Unlock()
	if f == nil {
		return OccupationRefuse, DefaultStaleAfter
	}
	return f()
}

// usePublisher sets the bus session.state is published on. Without one nothing is.
func (s *Sessions) usePublisher(p StatePublisher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pub = p
}

// publish puts the session's current state on session.state. Called with mu held, so the
// events of one session arrive in the order its changes were made.
func (s *Sessions) publish(id string, r *sessionRec) {
	if s.pub != nil {
		s.pub.Broadcast(SessionStateTopic, SessionStateType, "", "", SessionState{ID: id, Name: r.name, Devices: s.devicesOf(id), Stale: r.stale})
	}
}

// Connect registers the session id, with the chat it belongs to ("" when the client
// named none). It is the session's first event.
func (s *Sessions) Connect(id, chat string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	now := s.now()
	r := &sessionRec{chat: chat, seq: s.next, at: now.UTC().Truncate(time.Millisecond), seen: now}
	s.byID[id] = r
	s.publish(id, r)
}

// ConnectPeer registers the session id of a paired peer's link, named by the peer's
// configured name (ADR-0104 §1). It has no chat; what it holds is the devices its calls
// lease. It is the session's first event.
func (s *Sessions) ConnectPeer(id, name string) {
	name = SessionName(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	now := s.now()
	r := &sessionRec{seq: s.next, at: now.UTC().Truncate(time.Millisecond), seen: now, name: name, peer: name}
	s.byID[id] = r
	s.publish(id, r)
}

// StreamOpened counts an open stream of a peer's link session: a stream is activity, so a
// lease with one open is not idle (ADR-0104 §3). It also counts as a request. A session that
// is not registered is left as it is.
func (s *Sessions) StreamOpened(id string) { s.stream(id, 1) }

// StreamClosed is StreamOpened's pair: the stream's end counts as a request too, so the idle
// time starts from it.
func (s *Sessions) StreamClosed(id string) { s.stream(id, -1) }

func (s *Sessions) stream(id string, delta int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return
	}
	r.streams = max(r.streams+delta, 0)
	s.seenLocked(id, r)
}

// idleLocked says the session is idle (ADR-0094 §4): an MCP session by the flag Sweep sets
// from the configured time, a peer's link by the constant PeerLeaseIdle at the moment of
// the decision, with no open stream. mu is held.
func (s *Sessions) idleLocked(r *sessionRec) bool {
	if r.peer != "" {
		return r.streams == 0 && s.now().Sub(r.seen) > PeerLeaseIdle
	}
	return r.stale
}

// Named records the name the session's client gave itself, from initialize or
// server/discover, bounded by SessionName. A name that is empty once bounded, or the one
// already recorded, changes nothing and publishes nothing; a session that is not
// registered is left as it is.
func (s *Sessions) Named(id, name string) {
	name = SessionName(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil || name == "" || r.name == name {
		return
	}
	r.name = name
	s.publish(id, r)
}

// End removes the session and publishes its end, and the devices it held go with it
// (ADR-0094 §3). A session that is not registered ends nothing.
func (s *Sessions) End(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return
	}
	s.releaseDevicesLocked(id)
	delete(s.byID, id)
	if s.pub != nil {
		s.pub.Broadcast(SessionStateTopic, SessionStateType, "", "", SessionState{ID: id, Ended: true})
	}
}

// listed is the sessions in progress, oldest connection first.
func (s *Sessions) listed() []SessionListed {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs := make([]*sessionRec, 0, len(s.byID))
	ids := make(map[*sessionRec]string, len(s.byID))
	for id, r := range s.byID {
		recs = append(recs, r)
		ids[r] = id
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].seq < recs[j].seq })
	out := make([]SessionListed, len(recs))
	for i, r := range recs {
		out[i] = SessionListed{ID: ids[r], Name: r.name, Devices: s.devicesOf(ids[r]), Stale: r.stale, ConnectedAt: r.at}
	}
	return out
}

// Seen records that the session made a request, which ends its idleness (ADR-0094 §4).
// The adapter calls it on every MCP request that succeeds, and the daemon on every request
// of a connection joined to the session (ADR-0095 §2). A session that is not registered
// is left as it is.
func (s *Sessions) Seen(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return
	}
	s.seenLocked(id, r)
}

// seenLocked records a request of the session and ends its idleness. mu is held.
func (s *Sessions) seenLocked(id string, r *sessionRec) {
	r.seen = s.now()
	if r.stale {
		r.stale = false
		s.publish(id, r)
	}
}

// Join is the MCP session a non-MCP connection was joined to (ADR-0095 §1): what its
// requests are attributed to, and nothing else.
type Join struct {
	// ID and Name are the session's id and the name its client gave, as they stood when the
	// connection asked.
	ID, Name string
}

// Joined is the live MCP session of the chat, if there is one: the most recently seen when
// two share it (ADR-0095 §1). A connection joined to it is attributed to it and keeps it
// from going idle (Seen); it holds nothing. An empty chat joins no one.
func (s *Sessions) Joined(chat string) (Join, bool) {
	if chat == "" {
		return Join{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *sessionRec
	var bestID string
	for id, r := range s.byID {
		if r.chat != chat {
			continue
		}
		if best == nil || r.seen.After(best.seen) || (r.seen.Equal(best.seen) && r.seq > best.seq) {
			best, bestID = r, id
		}
	}
	if best == nil {
		return Join{}, false
	}
	return Join{ID: bestID, Name: best.name}, true
}

// Sweep marks idle every session whose last request is older than the configured
// staleAfterMinutes, and publishes the change (ADR-0094 §4). A session already idle stays
// so until it is seen.
func (s *Sessions) Sweep() {
	_, after := s.occupationNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, r := range s.byID {
		if r.stale {
			continue
		}
		idle := now.Sub(r.seen) > after
		if r.peer != "" {
			idle = s.idleLocked(r)
		}
		if idle {
			r.stale = true
			s.publish(id, r)
		}
	}
}

// Run sweeps every interval until ctx ends.
func (s *Sessions) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep()
		}
	}
}

// SetOccupyHook makes every OccupyDevice run f between its decision (the claim) and its
// effect (the hold), with the registry's lock held. Test-only: it is where a second one can be started, to see that it cannot decide
// in that window. Nil removes it.
func (s *Sessions) SetOccupyHook(f func()) {
	if f == nil {
		s.occupyHook.Store(nil)
		return
	}
	s.occupyHook.Store(&f)
}

// Registered reports whether id is a session in progress.
func (s *Sessions) Registered(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id] != nil
}

// Chat is the chat id the session's hello named, "" when it named none or the session is
// not registered.
func (s *Sessions) Chat(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.byID[id]; r != nil {
		return r.chat
	}
	return ""
}
