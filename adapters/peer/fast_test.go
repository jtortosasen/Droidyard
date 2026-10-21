package peer_test

import (
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Decision (ADR-0118 §4): the client's per-remote fast mark travels in its hello, the host
// records it per peer name at each hello, and a change of the mark on the client makes the
// link reconnect so the host learns it within seconds (the link is not kept as it is, as it is
// for an address change). Seen failing with the mark left out of the hello (the host never saw
// true) and with a change of the mark not dropping the connection (the host kept true).
func TestTheFastMarkTravelsInTheHelloAndAChangeReconnects(t *testing.T) {
	host, client := newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	var mu sync.Mutex
	seen := map[string]bool{}
	host.setFast = func(peer string, fast bool) {
		mu.Lock()
		defer mu.Unlock()
		seen[peer] = fast
	}
	fastOf := func() (bool, bool) {
		mu.Lock()
		defer mu.Unlock()
		v, ok := seen["anna"]
		return v, ok
	}
	addr, _ := host.listen(t, "127.0.0.1:0")
	remote := policy.Remote{Name: "qa-lab", Address: addr, Fingerprint: host.host.Identity().Fingerprint(), Fast: true}
	client.cfg.remotes = []policy.Remote{remote}
	client.host.Start()
	t.Cleanup(client.host.Stop)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to learn the mark", func() bool {
		v, ok := fastOf()
		return ok && v
	})

	remote.Fast = false
	client.cfg.mu.Lock()
	client.cfg.remotes = []policy.Remote{remote}
	client.cfg.mu.Unlock()
	client.host.Reconcile()
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to learn the mark was lifted", func() bool {
		v, ok := fastOf()
		return ok && !v
	})
}
