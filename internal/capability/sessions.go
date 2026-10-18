package capability

import "sync"

// MaxChats is how many chats the registry remembers a binding for (ADR-0088 §2): the
// oldest is dropped first. A working default.
const MaxChats = 256

// Sessions is the core's registry of MCP sessions, one per connection (ADR-0082 §5). It
// holds what an adapter must not keep on its own: where each session is bound, so a
// capability that moves the binding (Descriptor.Binding) and the adapter that addresses
// the session's calls by it read one value, and each chat's last binding, so a resumed
// chat gets its worktree back while the daemon lives (ADR-0088). Nothing here is written
// to disk. Every method is safe for concurrent use.
type Sessions struct {
	mu    sync.Mutex
	byID  map[string]*sessionRec
	chats map[string]string // chat id → the root of its last binding
	order []string          // the keys of chats, oldest first
}

// sessionRec is one connection.
type sessionRec struct {
	chat string
	// project is the session's project root: the main tree of its hello's root, or that
	// root itself when it is not a worktree (ADR-0082 §3). Empty without a hello root.
	project string
	// root is the bound root, empty until the session is bound; workspace is its id.
	root, workspace string
}

// NewSessions returns an empty registry.
func NewSessions() *Sessions {
	return &Sessions{byID: map[string]*sessionRec{}, chats: map[string]string{}}
}

// Connect registers the session id, with the chat it belongs to ("" when the client
// named none). It has no binding yet.
func (s *Sessions) Connect(id, chat string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id] = &sessionRec{chat: chat}
}

// Opened sets the session's first binding, the hello's root, and its project (ADR-0082
// §3). It remembers nothing: the chat's memory is what a move last wrote, which the
// session may be about to read.
func (s *Sessions) Opened(id, project, workspace, root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.byID[id]; r != nil {
		r.project, r.workspace, r.root = project, workspace, root
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

// End removes the session. Its chat's memory stays: a resumed chat is a new session.
func (s *Sessions) End(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
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
