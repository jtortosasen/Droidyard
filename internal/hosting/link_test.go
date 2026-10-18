package hosting

import (
	"context"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestLinkBacksOffAndTellsWhy is ADR-0098 §4: a link whose host is not there tries again
// from the minimum wait, doubling it up to the maximum and no further, and says why it is
// down in its health instead of being silently absent. The bounds are shortened to
// milliseconds; their ratio is the decision (1 s doubling to 30 s).
func TestLinkBacksOffAndTellsWhy(t *testing.T) {
	// An address nothing listens on: bind a port, then free it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	oldMin, oldMax := backoffMin, backoffMax
	backoffMin, backoffMax = 20*time.Millisecond, 80*time.Millisecond
	t.Cleanup(func() { backoffMin, backoffMax = oldMin, oldMax })

	id, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := &lockedLog{}
	l := newLink(policy.Remote{Name: "qa-lab", Address: addr, Fingerprint: strings.Repeat("a", 64)}, id, "0.0.0", log)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.run(ctx) }()
	defer func() { cancel(); <-done }()

	waits := regexp.MustCompile(`trying again in (\S+)`)
	var got []time.Duration
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && len(got) < 5; time.Sleep(5 * time.Millisecond) {
		got = got[:0]
		for _, m := range waits.FindAllStringSubmatch(log.String(), -1) {
			d, err := time.ParseDuration(m[1])
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, d)
		}
	}
	want := []time.Duration{20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond}
	if len(got) < len(want) {
		t.Fatalf("the link tried %d times in 10 s: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("waits = %v; want %v (doubling from the minimum to the maximum)", got[:len(want)], want)
			break
		}
	}
	h := l.health()
	if h.Connected || h.Error == "" || !strings.Contains(h.Error, addr) {
		t.Errorf("health = %+v; want down, with the reason naming %s", h, addr)
	}
	if _, err := l.Call(context.Background(), capability.RequestEnvelope{Capability: "operation.wait"}); err != ErrNotConnected {
		t.Errorf("a call on a link that is down: %v; want ErrNotConnected", err)
	}
}
