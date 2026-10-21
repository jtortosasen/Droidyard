package screen

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lagClock is the Hub's clock in the lag tests: it moves only when a test says so, so an
// age is exact and no test sleeps for one.
type lagClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *lagClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *lagClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// lagHub is a Hub on a fake clock whose OnLag calls are counted.
func lagHub() (*Hub, *lagClock, *atomic.Int32) {
	c := &lagClock{t: time.Unix(1_000_000, 0)}
	h := NewHub()
	h.now = c.now
	lags := new(atomic.Int32)
	h.OnLag(func() { lags.Add(1) })
	return h, c, lags
}

// Decision (ADR-0118 §1): a viewer is behind by its queue's age, not by its size. One whose
// oldest queued packet entered more than MaxViewerLag ago is emptied when the next packet
// arrives, rejoins at the NEXT key frame (with the config), is counted as a rejoin, and the
// Hub's owner is told once; a viewer that kept up is untouched. With only the byte bound the
// tiny packets here would queue forever.
func TestLaggingViewerRejoinsAtTheNextKeyFrame(t *testing.T) {
	h, clock, lags := lagHub()
	behind, steady := join(h), join(h)
	h.Push(cfgPacket(cfgA))
	h.Push(keyPacket(1, "K1"))
	h.Push(deltaPacket(2, "d2"))
	drain(t, steady, 2)

	clock.advance(MaxViewerLag) // exactly the bound is not past it
	h.Push(deltaPacket(3, "d3"))
	drain(t, steady, 1)
	if h.Rejoins() != 0 || lags.Load() != 0 {
		t.Fatalf("%d rejoins and %d lag calls at exactly MaxViewerLag, want none", h.Rejoins(), lags.Load())
	}

	clock.advance(time.Millisecond)
	h.Push(deltaPacket(4, "d4"))
	drain(t, steady, 1)
	if h.Rejoins() != 1 || lags.Load() != 1 {
		t.Fatalf("%d rejoins and %d lag calls past MaxViewerLag, want one each", h.Rejoins(), lags.Load())
	}
	expectNothing(t, behind) // emptied: the stale backlog is never replayed

	h.Push(deltaPacket(5, "d5")) // still waiting for a key frame
	drain(t, steady, 1)
	expectNothing(t, behind)

	h.Push(keyPacket(6, "K6"))
	drain(t, steady, 1)
	expectMerged(t, next(t, behind), cfgA, keyPacket(6, "K6"))
	h.Push(deltaPacket(7, "d7"))
	expectPlain(t, next(t, behind), deltaPacket(7, "d7"))
	if h.Rejoins() != 1 || lags.Load() != 1 {
		t.Fatalf("%d rejoins and %d lag calls after the rejoin, want one each", h.Rejoins(), lags.Load())
	}
}

// Decision (ADR-0118 §1): the age counts from the packet's entry into THIS viewer's queue,
// so a join, whose GOP is seconds old, is not lag. A viewer that joins a 3 s old GOP and
// reads it at once is never reset, nor by the packet that arrives while it reads. An age
// taken from when the Hub received the packets would reset every late joiner at once.
func TestAJoinIsNotLag(t *testing.T) {
	h, clock, lags := lagHub()
	h.Push(cfgPacket(cfgA))
	h.Push(keyPacket(1, "K1"))
	h.Push(deltaPacket(2, "d2"))
	clock.advance(3 * time.Second)

	v := join(h)
	h.Push(deltaPacket(3, "d3")) // arrives before the joiner has read anything
	expectMerged(t, next(t, v), cfgA, keyPacket(1, "K1"))
	expectPlain(t, next(t, v), deltaPacket(2, "d2"))
	expectPlain(t, next(t, v), deltaPacket(3, "d3"))
	if h.Rejoins() != 0 || lags.Load() != 0 {
		t.Fatalf("%d rejoins and %d lag calls for a join, want none", h.Rejoins(), lags.Load())
	}

	// A joiner that then does not read is lag like any other, from its join.
	late := join(h)
	clock.advance(MaxViewerLag + time.Millisecond)
	h.Push(deltaPacket(4, "d4"))
	if h.Rejoins() != 1 {
		t.Fatalf("%d rejoins for a joiner that never read, want 1", h.Rejoins())
	}
	expectNothing(t, late)
}

// Decision (ADR-0118 §1): a viewer that keeps up is never reset, however rarely packets come.
// A static screen sends one packet a minute and each is read at once: the age is the wait in
// the viewer's queue, not the time since the last packet.
func TestAViewerThatKeepsUpIsNeverReset(t *testing.T) {
	h, clock, lags := lagHub()
	v := join(h)
	h.Push(cfgPacket(cfgA))
	h.Push(keyPacket(1, "K1"))
	expectMerged(t, next(t, v), cfgA, keyPacket(1, "K1"))
	for i := int64(2); i <= 6; i++ {
		clock.advance(time.Minute)
		d := deltaPacket(i, "d")
		h.Push(d)
		expectPlain(t, next(t, v), d)
	}
	if h.Rejoins() != 0 || lags.Load() != 0 {
		t.Fatalf("%d rejoins and %d lag calls for a viewer that kept up, want none", h.Rejoins(), lags.Load())
	}
}

// Decision (ADR-0118 §1): the age is also checked when the viewer reads, so a viewer that
// stops reading on a screen that stops changing (no push to find it) is still emptied at its
// next read, not shown a packet 600 ms old followed by a gap; the owner is told.
func TestAViewerThatReadsLateIsRejoined(t *testing.T) {
	h, clock, lags := lagHub()
	v := join(h)
	h.Push(cfgPacket(cfgA))
	h.Push(keyPacket(1, "K1"))
	clock.advance(MaxViewerLag + time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if p, err := v.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a read after MaxViewerLag returned %+v, %v; want the stale packet dropped", p, err)
	}
	if h.Rejoins() != 1 || lags.Load() != 1 {
		t.Fatalf("%d rejoins and %d lag calls, want one each", h.Rejoins(), lags.Load())
	}
	h.Push(keyPacket(2, "K2"))
	expectMerged(t, next(t, v), cfgA, keyPacket(2, "K2"))
}
