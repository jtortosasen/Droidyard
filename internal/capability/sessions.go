package capability

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// MaxChats is how many chats the registry remembers a binding for (ADR-0088 §2): the
// oldest is dropped first. A working default.
const MaxChats = 256

// The values of config.json's occupation.conflict (ADR-0094 §5, §7). Where Droidyard
// cannot ask (workspace.enter), ask is refuse (ADR-0094 §6).
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

// Holder is who holds a worktree, as another session is told (ADR-0094 §5). It carries no
// session id (ADR-0048 §2).
type Holder struct {
	Name  string
	Since time.Time
	Stale bool
}

// Sessions is the core's registry of MCP sessions, one per connection (ADR-0082 §5). It
// holds what an adapter must not keep on its own: where each session is bound, so a
// capability that moves the binding (Descriptor.Binding) and the adapter that addresses
// the session's calls by it read one value, and each chat's last binding, so a resumed
// chat gets its worktree back while the daemon lives (ADR-0088). Nothing here is written
// to disk. Every method is safe for concurrent use.
//
// What session.watch shows (ADR-0083 §1) is published from here, under the same lock that
// changes it, so an event of a session is never published before the row session.watch
// reads can show it, and none after its end. The chat id is never published or listed
// (ADR-0088 §4).
type Sessions struct {
	mu    sync.Mutex
	byID  map[string]*sessionRec
	chats map[string]string // chat id → the root of its last binding
	order []string          // the keys of chats, oldest first
	next  uint64            // the connection order of the next session
	pub   StatePublisher    // session.state's bus; nil publishes nothing
	now   func() time.Time
	// holders is the one holder of each linked worktree, by canonical root: the session
	// that holds it (ADR-0094 §1). Under mu with the records, so one lock decides and
	// binds.
	holders map[string]string
	// occupation is config.json's occupation section, read at each decision (ADR-0094
	// §7); nil means the defaults. Injected at serve: capability cannot import policy.
	occupation func() (conflict string, staleAfter time.Duration)

	// watchHook and occupyHook are test seams: see SetWatchHook and SetOccupyHook.
	watchHook  atomic.Pointer[func()]
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
	// project is the session's project root: the main tree of its hello's root, or that
	// root itself when it is not a worktree (ADR-0082 §3). Empty without a hello root.
	project string
	// root is the bound root, empty until the session is bound; workspace is its id.
	root, workspace string
	// seen is the last MCP request of the session (its connection, until the first); stale
	// is set once it is older than the configured staleAfterMinutes, and cleared by the
	// next request (ADR-0094 §4).
	seen  time.Time
	stale bool
	// holdRoot is the linked worktree the session holds, holdWorkspace its id and holdSince
	// when it took it; all empty while it holds none.
	holdRoot, holdWorkspace string
	holdSince               time.Time
}

// NewSessions returns an empty registry.
func NewSessions() *Sessions {
	return &Sessions{byID: map[string]*sessionRec{}, chats: map[string]string{}, holders: map[string]string{}, now: time.Now}
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
		s.pub.Broadcast(SessionStateTopic, SessionStateType, "", "", SessionState{ID: id, Name: r.name, Workspace: r.workspace, Occupies: r.holdWorkspace, Stale: r.stale})
	}
}

// Connect registers the session id, with the chat it belongs to ("" when the client
// named none). It has no binding yet. It is the session's first event.
func (s *Sessions) Connect(id, chat string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	now := s.now()
	r := &sessionRec{chat: chat, seq: s.next, at: now.UTC().Truncate(time.Millisecond), seen: now}
	s.byID[id] = r
	s.publish(id, r)
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

// Opened sets the session's first binding, the hello's root, and its project (ADR-0082
// §3). It remembers nothing: the chat's memory is what a move last wrote, which the
// session may be about to read. A root that is a linked worktree of the project (root is
// not project) is held when no other live, non-idle session of another chat holds it; a
// held one is bound, not held, and the hello is never refused (ADR-0094 §2, §5).
func (s *Sessions) Opened(id, project, workspace, root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return
	}
	r.project, r.workspace, r.root = project, workspace, root
	if root != project {
		if c, hid := s.claimLocked(id, r, root); c == claimFree || c == claimTakeover {
			s.acquireLocked(id, r, root, workspace, c, hid)
		}
	}
	s.publish(id, r)
}

// How a session's claim on a linked worktree stands (claimLocked).
type claim int

const (
	// claimFree: nobody holds it, or the holder's record is gone.
	claimFree claim = iota
	// claimSelf: the session holds it already.
	claimSelf
	// claimShared: another live session of the same chat holds it; both are one chat
	// (ADR-0094 §1), so neither is refused and the second is bound, not holding.
	claimShared
	// claimTakeover: an idle session of another chat holds it; entering takes it over
	// (ADR-0094 §4).
	claimTakeover
	// claimBlocked: a live, non-idle session of another chat holds it.
	claimBlocked
)

// claimLocked says how root stands for session id, and the holder's id when there is one.
// mu is held.
func (s *Sessions) claimLocked(id string, r *sessionRec, root string) (claim, string) {
	hid, ok := s.holders[root]
	if !ok {
		return claimFree, ""
	}
	if hid == id {
		return claimSelf, hid
	}
	h := s.byID[hid]
	if h == nil {
		return claimFree, ""
	}
	switch {
	case r.chat != "" && r.chat == h.chat:
		return claimShared, hid
	case h.stale:
		return claimTakeover, hid
	}
	return claimBlocked, hid
}

// acquireLocked makes session id the holder of root, taking it from an idle holder when c
// is claimTakeover (whose row, now without it, is published). It does not publish id's
// own state: the caller does, once its binding is set. mu is held.
func (s *Sessions) acquireLocked(id string, r *sessionRec, root, workspace string, c claim, hid string) {
	if c == claimTakeover {
		if h := s.byID[hid]; h != nil {
			h.holdRoot, h.holdWorkspace, h.holdSince = "", "", time.Time{}
			s.publish(hid, h)
		}
	}
	r.holdRoot, r.holdWorkspace, r.holdSince = root, workspace, s.now().UTC().Truncate(time.Millisecond)
	s.holders[root] = id
}

// releaseLocked drops what the session holds, if anything. Another live session of the same
// chat bound at that root, the most recently seen, is handed the hold at once (its row is
// published), so a chat that shares a worktree between two connections keeps it while either
// lives (ADR-0094 §1). mu is held.
func (s *Sessions) releaseLocked(id string, r *sessionRec) {
	if r.holdRoot == "" {
		return
	}
	root, workspace := r.holdRoot, r.holdWorkspace
	r.holdRoot, r.holdWorkspace, r.holdSince = "", "", time.Time{}
	if s.holders[root] != id {
		return
	}
	delete(s.holders, root)
	if r.chat == "" {
		return
	}
	var next *sessionRec
	var nextID string
	for oid, o := range s.byID {
		if oid == id || o.chat != r.chat || o.root != root || o.holdRoot != "" {
			continue
		}
		if next == nil || o.seen.After(next.seen) || (o.seen.Equal(next.seen) && o.seq > next.seq) {
			next, nextID = o, oid
		}
	}
	if next != nil {
		next.holdRoot, next.holdWorkspace, next.holdSince = root, workspace, s.now().UTC().Truncate(time.Millisecond)
		s.holders[root] = nextID
		s.publish(nextID, next)
	}
}

// Occupy moves the session's binding to root, workspace being its id, and decides in the
// same critical section whether the session holds it (ADR-0094 §1 to §6), so two sessions
// entering one worktree cannot both succeed. linked says root is a linked worktree of the
// session's project; the main tree is never held. A linked worktree another live,
// non-idle session of another chat holds is refused unless the configured conflict is
// allow: refused is then true, held describes the holder, and nothing has changed
// (ask is refuse, where Droidyard cannot ask: ADR-0094 §6). Under allow it is bound, not
// held. A free one, or one held by an idle session, is held; a session that moves
// releases what it held before, the main tree included. A session that is not registered
// is left as it is.
func (s *Sessions) Occupy(id, workspace, root string, linked bool) (held Holder, refused bool) {
	conflict, _ := s.occupationNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return Holder{}, false
	}
	if linked {
		c, hid := s.claimLocked(id, r, root)
		if hook := s.occupyHook.Load(); hook != nil {
			(*hook)()
		}
		if c == claimBlocked && conflict != OccupationAllow {
			h := s.byID[hid]
			return Holder{Name: h.name, Since: h.holdSince}, true
		}
		if r.holdRoot != root {
			s.releaseLocked(id, r)
		}
		if c == claimFree || c == claimTakeover {
			s.acquireLocked(id, r, root, workspace, c, hid)
		}
	} else {
		s.releaseLocked(id, r)
	}
	s.bindLocked(id, r, workspace, root)
	return Holder{}, false
}

// bindLocked sets the session's binding, publishes it and remembers it as the chat's
// last. mu is held.
func (s *Sessions) bindLocked(id string, r *sessionRec, workspace, root string) {
	r.workspace, r.root = workspace, root
	s.publish(id, r)
	if r.chat == "" {
		return
	}
	if _, ok := s.chats[r.chat]; ok {
		for i, c := range s.order {
			if c == r.chat {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
	}
	s.chats[r.chat] = root
	s.order = append(s.order, r.chat)
	for len(s.order) > MaxChats {
		delete(s.chats, s.order[0])
		s.order = s.order[1:]
	}
}

// End removes the session and publishes its end. Its chat's memory stays: a resumed chat
// is a new session. A session that is not registered ends nothing.
func (s *Sessions) End(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return
	}
	s.releaseLocked(id, r)
	delete(s.byID, id)
	if s.pub != nil {
		s.pub.Broadcast(SessionStateTopic, SessionStateType, "", "", SessionState{ID: id, Ended: true})
	}
}

// listed is the sessions in progress, oldest connection first. Called by session.watch
// after it has opened its subscription.
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
		out[i] = SessionListed{ID: ids[r], Name: r.name, Workspace: r.workspace, Occupies: r.holdWorkspace, Stale: r.stale, ConnectedAt: r.at}
	}
	return out
}

// Seen records that the session made a request, which ends its idleness (ADR-0094 §4).
// The adapter calls it on every MCP request that succeeds. A session that is not
// registered is left as it is.
func (s *Sessions) Seen(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return
	}
	r.seen = s.now()
	if r.stale {
		r.stale = false
		s.publish(id, r)
	}
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
		if !r.stale && now.Sub(r.seen) > after {
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

// HolderOf reports who holds the linked worktree root, for the session asking ("" for
// the UI or the CLI, which are told of every holder). A holder that is the asking session,
// or a session of the asking session's chat, is not reported: occupiedBy names another
// chat (ADR-0094 §1).
func (s *Sessions) HolderOf(root, asking string) (Holder, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hid, ok := s.holders[root]
	if !ok {
		return Holder{}, false
	}
	h := s.byID[hid]
	if h == nil || hid == asking {
		return Holder{}, false
	}
	if a := s.byID[asking]; a != nil && a.chat != "" && a.chat == h.chat {
		return Holder{}, false
	}
	return Holder{Name: h.name, Since: h.holdSince, Stale: h.stale}, true
}

// SetWatchHook makes every session.watch call run f after it has opened its subscription
// and before it takes the snapshot. Test-only: it is where a session can connect or end
// inside the window ADR-0083 §1 closes. Nil removes it.
func (s *Sessions) SetWatchHook(f func()) {
	if f == nil {
		s.watchHook.Store(nil)
		return
	}
	s.watchHook.Store(&f)
}

// SetOccupyHook makes every Occupy of a linked worktree run f between its decision (the
// claim) and its effect (the hold), with the registry's lock held. Test-only: it is where
// a second Occupy can be started, to see that it cannot decide in that window. Nil removes
// it.
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

// Binding is the root the session's calls address; ok is false for a session that is not
// registered or not bound.
func (s *Sessions) Binding(id string) (root string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil || r.root == "" {
		return "", false
	}
	return r.root, true
}

// Project is the session's project root, "" when it has none (not registered, or
// registered without a hello root).
func (s *Sessions) Project(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.byID[id]; r != nil {
		return r.project
	}
	return ""
}

// Remembered is the root the session's chat was last bound to, if the chat is known.
func (s *Sessions) Remembered(chat string) (root string, ok bool) {
	if chat == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok = s.chats[chat]
	return root, ok
}

// Forget drops the chat's memory (ADR-0088 §3: a root that cannot be entered again).
func (s *Sessions) Forget(chat string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.chats[chat]; !ok {
		return
	}
	delete(s.chats, chat)
	for i, c := range s.order {
		if c == chat {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}
