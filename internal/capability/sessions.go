package capability

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// MaxChats is how many chats the registry remembers a binding for (ADR-0088 §2): the
// oldest is dropped first. A working default.
const MaxChats = 256

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

	// watchHook is a test seam: see SetWatchHook.
	watchHook atomic.Pointer[func()]
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
}

// NewSessions returns an empty registry.
func NewSessions() *Sessions {
	return &Sessions{byID: map[string]*sessionRec{}, chats: map[string]string{}, now: time.Now}
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
		s.pub.Broadcast(SessionStateTopic, SessionStateType, "", "", SessionState{ID: id, Name: r.name, Workspace: r.workspace})
	}
}

// Connect registers the session id, with the chat it belongs to ("" when the client
// named none). It has no binding yet. It is the session's first event.
func (s *Sessions) Connect(id, chat string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	r := &sessionRec{chat: chat, seq: s.next, at: s.now().UTC().Truncate(time.Millisecond)}
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
// session may be about to read.
func (s *Sessions) Opened(id, project, workspace, root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.byID[id]; r != nil {
		r.project, r.workspace, r.root = project, workspace, root
		s.publish(id, r)
	}
}

// Bound moves the session's binding to root, workspace being its id, and remembers it as
// its chat's last binding (ADR-0088 §2). A session that is not registered is left as it
// is.
func (s *Sessions) Bound(id, workspace, root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return
	}
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
	if s.byID[id] == nil {
		return
	}
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
		out[i] = SessionListed{ID: ids[r], Name: r.name, Workspace: r.workspace, ConnectedAt: r.at}
	}
	return out
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
