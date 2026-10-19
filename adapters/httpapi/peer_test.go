package httpapi

import (
	"context"
	"testing"
)

// A peer listener that stopped and binds again ends its sessions without refusing the next
// ones; only the daemon's shutdown latches (m2t step 1 review).
func TestPeerEndAllKeepsAccepting(t *testing.T) {
	p := &PeerSessions{h: newWSHandler(nil, nil, "")}
	p.EndAll(context.Background())
	s := &wsSession{h: p.h}
	if !p.h.add(s) {
		t.Fatal("after EndAll a new peer session was refused: a rebound listener would refuse every peer")
	}
	p.h.remove(s)
	p.CloseAll(context.Background())
	if p.h.add(&wsSession{h: p.h}) {
		t.Fatal("after CloseAll a new peer session was accepted")
	}
}
