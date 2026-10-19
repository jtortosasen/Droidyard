package httpapi

import (
	"context"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// PeerSessions serves the WebSocket sessions of paired peers (ADR-0098) with the session
// loop of the UI's /v1/ws: the same envelopes, the same subscriptions and pings. What
// differs is how a session starts (a hello, not the token frame) and who its requests are
// made as (client peer, the peer's configured name). The route, the TLS and the pin are
// adapters/peer's; nothing of the UI's fence applies here and none of it is reachable
// from the peer port.
type PeerSessions struct {
	h        *wsHandler
	sessions *capability.Sessions
}

// NewPeerSessions returns the sessions of peers calling into reg through ops, with
// subscriptions on bus. Each peer's link is a session in ops' registry (ADR-0104 §1).
func NewPeerSessions(reg *capability.Registry, ops *capability.Operations, bus *event.Bus) *PeerSessions {
	return &PeerSessions{h: newWSHandler(&caller{reg: reg, ops: ops}, bus, ""), sessions: ops.Sessions()}
}

// Peer is who a session's requests are made as.
type Peer struct {
	// Name is the peer's configured name: the session name its operations carry.
	Name string
	// Allowed is asked before each request is served; false ends the session without
	// serving the request. The adapter closes the connection with its reason first.
	Allowed func() bool
}

// Serve runs one peer's session on c until it ends. hello is the exchange that starts it:
// it reads the peer's hello and answers it, and false ends the session (it has closed c
// itself, with the reason). Before it, c reads no frame over 4 KiB.
func (p *PeerSessions) Serve(ctx context.Context, c *websocket.Conn, peer Peer, hello func(ctx context.Context) bool) {
	c.SetReadLimit(preAuthLimit) // raised to MaxBody once the session has started
	s := &wsSession{h: p.h, c: c, started: map[string]bool{}, who: requester{client: capability.ClientPeer, name: peer.Name}, allowed: peer.Allowed, registry: p.sessions}
	if !p.h.add(s) {
		c.Close(websocket.StatusGoingAway, "the daemon is stopping")
		return
	}
	defer p.h.remove(s)
	s.run(ctx, hello)
}

// CloseAll ends every open session and refuses new ones, as the UI's CloseSessions does.
func (p *PeerSessions) CloseAll(ctx context.Context) { p.h.closeAll(ctx) }

// EndAll ends every open session and keeps accepting new ones: the listener stopped and
// will bind again.
func (p *PeerSessions) EndAll(ctx context.Context) { p.h.endAll(ctx, false) }
