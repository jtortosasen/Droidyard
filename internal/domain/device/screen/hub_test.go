package screen

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

var (
	cfgA = []byte("\x00\x00\x00\x01CONFIG-A")
	cfgB = []byte("\x00\x00\x00\x01CONFIG-B-LONGER")
)

func cfgPacket(c []byte) Packet { return Packet{Config: true, Data: c} }
func keyPacket(pts int64, s string) Packet {
	return Packet{Key: true, PTS: pts, Data: []byte(s)}
}
func deltaPacket(pts int64, s string) Packet { return Packet{PTS: pts, Data: []byte(s)} }

func join(h *Hub) Viewer { return h.Join() }

// next reads one packet or fails after a second.
func next(t *testing.T, v Viewer) Packet {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p, err := v.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return p
}

// expectNothing fails if the viewer has a packet within a short wait.
func expectNothing(t *testing.T, v Viewer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if p, err := v.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected nothing, got key %v config %v pts %d, %d bytes, %v", p.Key, p.Config, p.PTS, len(p.Data), err)
	}
}

func expectMerged(t *testing.T, p Packet, cfg []byte, key Packet) {
	t.Helper()
	if !p.Key || !p.Config || p.PTS != key.PTS || !bytes.Equal(p.Data, append(append([]byte{}, cfg...), key.Data...)) {
		t.Fatalf("got %+v (% q), want the key frame %q with config %q in front, flagged Key and Config", p, p.Data, key.Data, cfg)
	}
}

func expectPlain(t *testing.T, p, want Packet) {
	t.Helper()
	if p.Key != want.Key || p.Config != want.Config || p.PTS != want.PTS || !bytes.Equal(p.Data, want.Data) {
		t.Fatalf("got %+v, want %+v", p, want)
	}
}

// Decision: a viewer that joins after the second key frame starts at it, and the config
// it gets is the stream's, which the device sent once, before the first key frame. A hub
// that took the config only from the key frame that followed it would hand this viewer a
// bare key frame.
func TestHubJoinAfterSecondKeyframe(t *testing.T) {
	h := NewHub()
	early := join(h)
	k1, k2 := keyPacket(100, "KEY-1"), keyPacket(200, "KEY-2")
	h.Push(cfgPacket(cfgA))
	h.Push(k1)
	h.Push(deltaPacket(110, "d1"))
	h.Push(deltaPacket(120, "d2"))
	h.Push(k2)
	h.Push(deltaPacket(210, "d3"))

	late := join(h)
	expectMerged(t, next(t, late), cfgA, k2)
	expectPlain(t, next(t, late), deltaPacket(210, "d3"))
	expectNothing(t, late)

	// The viewer that was there all along saw the first key frame with the config, and
	// the second as the device sent it.
	expectMerged(t, next(t, early), cfgA, k1)
	expectPlain(t, next(t, early), deltaPacket(110, "d1"))
	expectPlain(t, next(t, early), deltaPacket(120, "d2"))
	expectPlain(t, next(t, early), k2)
	expectPlain(t, next(t, early), deltaPacket(210, "d3"))
}

// Decision: the config appears exactly once in front of a viewer's first key frame, for
// a viewer waiting for it and for one that joins afterwards. A hub that kept the merged
// packet in its GOP and prepended again on a join would give the second viewer two.
func TestHubJoinAtFirstKeyframe(t *testing.T) {
	h := NewHub()
	before := join(h)
	k1 := keyPacket(100, "KEY-1")
	h.Push(cfgPacket(cfgA))
	expectNothing(t, before) // a config never goes out on its own
	h.Push(k1)
	h.Push(deltaPacket(110, "d1"))
	after := join(h)

	for name, v := range map[string]Viewer{"before": before, "after": after} {
		first := next(t, v)
		expectMerged(t, first, cfgA, k1)
		if n := bytes.Count(first.Data, cfgA); n != 1 {
			t.Fatalf("%s: the config appears %d times", name, n)
		}
		expectPlain(t, next(t, v), deltaPacket(110, "d1"))
		expectNothing(t, v)
	}
}

// Decision: packets before the first key frame mean nothing to a decoder, so a viewer
// that joins into them waits and is never given a delta frame first.
func TestHubJoinBeforeAnyKeyframe(t *testing.T) {
	h := NewHub()
	h.Push(deltaPacket(1, "stray")) // no GOP yet
	v := join(h)
	h.Push(cfgPacket(cfgA))
	h.Push(deltaPacket(2, "stray 2"))
	expectNothing(t, v)
	k := keyPacket(3, "KEY")
	h.Push(k)
	expectMerged(t, next(t, v), cfgA, k)
}

// Decision: a rotation (a new config, then a key frame) does not hand a joining viewer
// the old stream's key frame with the new config: between the config and its key frame
// there is no GOP, and the newcomer waits. The live viewer takes the new config in band.
func TestHubConfigChange(t *testing.T) {
	h := NewHub()
	live := join(h)
	k1, k2 := keyPacket(100, "KEY-1"), keyPacket(200, "KEY-2")
	h.Push(cfgPacket(cfgA))
	h.Push(k1)
	expectMerged(t, next(t, live), cfgA, k1)

	h.Push(cfgPacket(cfgB))
	h.Push(deltaPacket(150, "old stream"))
	mid := join(h)
	expectNothing(t, mid)
	h.Push(k2)

	expectPlain(t, next(t, live), deltaPacket(150, "old stream"))
	expectMerged(t, next(t, live), cfgB, k2)
	expectMerged(t, next(t, mid), cfgB, k2)
	late := join(h)
	expectMerged(t, next(t, late), cfgB, k2)
}

// Decision: Size is what the newest SPS says, and an SPS that does not parse leaves it.
func TestHubSizeFollowsTheNewestSPS(t *testing.T) {
	data := fixture(t, "scrcpy-api31-rotate.bin")
	h := NewHub()
	if got := h.Size(); got != (Size{}) {
		t.Fatalf("size %v before any config", got)
	}
	var seen []Size
	err := Demux(bytes.NewReader(data), func(p Packet) error {
		h.Push(p)
		if p.Config {
			seen = append(seen, h.Size())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != (Size{590, 1280}) || seen[1] != (Size{1280, 590}) {
		t.Fatalf("sizes at each config %v, want 590x1280 then 1280x590", seen)
	}
	h.Push(cfgPacket([]byte("not an SPS")))
	if h.Size() != (Size{1280, 590}) {
		t.Fatalf("an unreadable config changed the size to %v", h.Size())
	}
}

// Decision: a viewer is given the device's bytes unchanged, apart from the config merged
// into the key frames that start a stream or follow a config: replaying a recording
// through the hub gives a viewer exactly the recording's packets in order.
func TestHubDeliversTheRecordedStream(t *testing.T) {
	for _, name := range []string{"scrcpy-api31-portrait.bin", "scrcpy-api31-rotate.bin"} {
		pkts, _, err := collect(bytes.NewReader(fixture(t, name)))
		if err != nil {
			t.Fatal(err)
		}
		h := NewHub()
		v := join(h)
		var cfg []byte
		var pending bool
		var want []Packet
		for _, p := range pkts {
			h.Push(p)
			switch {
			case p.Config:
				cfg, pending = p.Data, true
			case p.Key && pending:
				want = append(want, Packet{Key: true, Config: true, PTS: p.PTS, Data: append(append([]byte{}, cfg...), p.Data...)})
				pending = false
			default:
				want = append(want, p)
			}
		}
		for i, w := range want {
			p := next(t, v)
			if p.Key != w.Key || p.Config != w.Config || p.PTS != w.PTS || !bytes.Equal(p.Data, w.Data) {
				t.Fatalf("%s: packet %d: key %v config %v pts %d %d bytes; want key %v config %v pts %d %d bytes",
					name, i, p.Key, p.Config, p.PTS, len(p.Data), w.Key, w.Config, w.PTS, len(w.Data))
			}
		}
		expectNothing(t, v)
		if h.Rejoins() != 0 {
			t.Fatalf("%s: %d rejoins for a viewer that never fell behind", name, h.Rejoins())
		}
	}
}

// big is a packet payload large enough for a handful of them to fill a queue. Packets
// share it: Data is read-only.
var big = make([]byte, 3<<20)

func bigKey(pts int64) Packet   { return Packet{Key: true, PTS: pts, Data: big} }
func bigDelta(pts int64) Packet { return Packet{PTS: pts, Data: big} }

func drain(t *testing.T, v Viewer, n int) {
	t.Helper()
	for range n {
		next(t, v)
	}
}

// Decision: a viewer that does not read is emptied when its queue would pass MaxQueue and
// rejoined at the next key frame, not the current GOP, once per key frame however many
// packets pass; the key frame it rejoins at carries the config current then, which may
// not be the one it first had. A hub that refilled the viewer from its GOP would start
// it at an old key frame; one that left the config off would hand the decoder a key frame
// it cannot start from.
func TestHubSlowViewerRejoinsOnce(t *testing.T) {
	h := NewHub()
	stalled, steady := join(h), join(h)
	push := func(p Packet) {
		h.Push(p)
		if !p.Config {
			drain(t, steady, 1) // the viewer that keeps up
		}
	}
	rejoins := func(want int) {
		t.Helper()
		if got := h.Rejoins(); got != want {
			t.Fatalf("%d rejoins, want %d", got, want)
		}
	}

	// MaxQueue is 20 MiB and the packets are 3 MiB. The stalled viewer queues the first
	// GOP's key frame and four deltas (15 MiB), then the second key frame (18 MiB): the
	// hub's GOP is now that key frame alone. The next delta (21 MiB) overflows the queue
	// while the hub holds a GOP, which a rejoin must not use.
	push(cfgPacket(cfgA))
	push(bigKey(1000))
	for i := int64(1); i <= 4; i++ {
		push(bigDelta(1000 + i))
	}
	push(bigKey(2000))
	rejoins(0)
	push(bigDelta(2001))
	rejoins(1)
	for i := int64(2); i <= 5; i++ { // however many packets pass, it stays one
		push(bigDelta(2000 + i))
	}
	rejoins(1)
	// Rejoined at the next key frame, not refilled from the current GOP (key frame 2000).
	expectNothing(t, stalled)

	// The gap spans a config change.
	push(cfgPacket(cfgB))
	push(Packet{Key: true, PTS: 3000, Data: big})
	first := next(t, stalled)
	if !first.Key || !first.Config || first.PTS != 3000 {
		t.Fatalf("the stalled viewer resumed with key %v config %v pts %d, want the next key frame (pts 3000) with the config", first.Key, first.Config, first.PTS)
	}
	if !bytes.HasPrefix(first.Data, cfgB) || len(first.Data) != len(cfgB)+len(big) || bytes.HasPrefix(first.Data, cfgA) {
		t.Fatalf("the rejoin key frame carries the wrong config: %q...", first.Data[:20])
	}
	expectNothing(t, stalled) // nothing older is queued behind it
	rejoins(1)

	// It stalls again: one more rejoin, and it resumes at the next key frame.
	for i := int64(1); i <= 8; i++ {
		push(bigDelta(3000 + i))
	}
	rejoins(2)
	push(Packet{Key: true, PTS: 4000, Data: big})
	third := next(t, stalled)
	if third.PTS != 4000 || !third.Key || !third.Config || !bytes.HasPrefix(third.Data, cfgB) {
		t.Fatalf("resumed at pts %d key %v config %v, want the key frame at 4000 with the config", third.PTS, third.Key, third.Config)
	}
}

// Decision: when the packet that overflows the queue is itself a key frame, it is the
// next key frame: the viewer rejoins at it, with the config, rather than waiting a
// whole GOP more.
func TestHubOverflowOnAKeyframeRejoinsAtIt(t *testing.T) {
	h := NewHub()
	stalled := join(h)
	h.Push(cfgPacket(cfgA))
	h.Push(bigKey(1))
	for i := 2; i <= 6; i++ { // 18 MiB queued
		h.Push(bigDelta(int64(i)))
	}
	if h.Rejoins() != 0 {
		t.Fatal("rejoined before the queue was full")
	}
	h.Push(bigKey(7)) // 21 MiB
	if h.Rejoins() != 1 {
		t.Fatalf("%d rejoins, want 1", h.Rejoins())
	}
	p := next(t, stalled)
	if p.PTS != 7 || !p.Key || !p.Config || !bytes.HasPrefix(p.Data, cfgA) {
		t.Fatalf("resumed at pts %d key %v config %v, want the overflowing key frame with the config", p.PTS, p.Key, p.Config)
	}
	expectNothing(t, stalled)
}

// Decision: the GOP is bounded at 16 MiB of device bytes. At exactly the bound it is
// kept whole; past it, it is emptied and a join waits for the next key frame, which it
// gets with the config. The join of a full GOP fits the viewer's queue.
func TestHubGOPBound(t *testing.T) {
	huge := make([]byte, 4<<20) // MaxPacket
	hugeKey := func(pts int64) Packet { return Packet{Key: true, PTS: pts, Data: huge} }
	hugeDelta := func(pts int64) Packet { return Packet{PTS: pts, Data: huge} }

	h := NewHub()
	h.Push(cfgPacket(cfgA))
	h.Push(hugeKey(1))
	for i := 2; i <= 4; i++ { // 4 packets: exactly 16 MiB
		h.Push(hugeDelta(int64(i)))
	}
	full := join(h)
	first := next(t, full)
	if !first.Key || !first.Config || len(first.Data) != len(cfgA)+len(huge) {
		t.Fatalf("a GOP at the bound did not join with its key frame and config")
	}
	drain(t, full, 3)
	expectNothing(t, full)
	if h.Rejoins() != 0 {
		t.Fatalf("joining a full GOP overflowed the queue (%d rejoins)", h.Rejoins())
	}

	h.Push(hugeDelta(5)) // 20 MiB: past the bound
	waiting := join(h)
	expectNothing(t, waiting)
	h.Push(hugeDelta(6))
	again := join(h)
	expectNothing(t, again)

	h.Push(hugeKey(7))
	for name, v := range map[string]Viewer{"waiting": waiting, "again": again} {
		p := next(t, v)
		if p.PTS != 7 || !p.Key || !p.Config || len(p.Data) != len(cfgA)+len(huge) {
			t.Fatalf("%s: first packet pts %d key %v config %v, want the next key frame with the config", name, p.PTS, p.Key, p.Config)
		}
	}
	// The GOP is rebuilt from that key frame.
	h.Push(hugeDelta(8))
	last := join(h)
	if p := next(t, last); p.PTS != 7 || !p.Config {
		t.Fatalf("a join after the rebuild starts at pts %d config %v", p.PTS, p.Config)
	}
	expectPlain(t, next(t, last), hugeDelta(8))
}

// Decision: the bounds also count packets, so a stream of tiny packets cannot cost
// unbounded bookkeeping behind a small byte count: past MaxPackets the GOP is emptied
// and a viewer that does not read overflows and rejoins at the next key frame.
func TestHubPacketCountBound(t *testing.T) {
	h := NewHub()
	stalled := join(h)
	h.Push(cfgPacket(cfgA))
	h.Push(keyPacket(1, "K1"))
	for i := 1; i < MaxPackets; i++ { // the GOP and the queue hold MaxPackets packets
		h.Push(deltaPacket(int64(1+i), "d"))
	}
	if h.Rejoins() != 0 {
		t.Fatal("rejoined at the bound")
	}
	full := join(h)
	expectMerged(t, next(t, full), cfgA, keyPacket(1, "K1"))
	drain(t, full, MaxPackets-1)
	expectNothing(t, full)
	if h.Rejoins() != 0 {
		t.Fatal("a join of a full GOP overflowed")
	}

	h.Push(deltaPacket(1<<20, "one too many"))
	if h.Rejoins() != 1 {
		t.Fatalf("%d rejoins, want 1", h.Rejoins())
	}
	expectNothing(t, join(h)) // the GOP is empty
	k2 := keyPacket(1<<21, "K2")
	h.Push(k2)
	expectMerged(t, next(t, stalled), cfgA, k2)
	expectNothing(t, stalled)
}

// Decision: Close ends every viewer, blocked or not, with the reason; a viewer that joins
// after has nothing to read but the reason.
func TestHubClose(t *testing.T) {
	h := NewHub()
	v := join(h)
	done := make(chan error, 1)
	go func() {
		_, err := v.Next(context.Background())
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if h.Reason() != nil {
		t.Fatal("a reason before Close")
	}
	select {
	case <-h.Done():
		t.Fatal("done before Close")
	default:
	}
	reason := errors.New("device gone")
	h.Close(reason)
	h.Close(errors.New("second"))
	select {
	case err := <-done:
		if err != reason {
			t.Fatalf("blocked Next returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a blocked Next was not woken by Close")
	}
	<-h.Done()
	if h.Reason() != reason {
		t.Fatalf("reason %v", h.Reason())
	}
	late := join(h)
	if _, err := late.Next(context.Background()); err != reason {
		t.Fatalf("a viewer joined after Close got %v", err)
	}
	h.Push(keyPacket(1, "ignored"))
	h.Close(nil)

	h2 := NewHub()
	h2.Close(nil)
	if _, err := join(h2).Next(context.Background()); err != ErrClosed {
		t.Fatalf("nil reason: %v, want ErrClosed", err)
	}
}

// Decision: a viewer's Close is its own: it unblocks its Next with ErrViewerClosed,
// frees its queue, and leaves the others and the stream alone.
func TestViewerClose(t *testing.T) {
	h := NewHub()
	a, b := join(h), join(h)
	done := make(chan error, 1)
	go func() {
		_, err := a.Next(context.Background())
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	a.Close()
	a.Close()
	select {
	case err := <-done:
		if err != ErrViewerClosed {
			t.Fatalf("blocked Next returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake Next")
	}
	if _, err := a.Next(context.Background()); err != ErrViewerClosed {
		t.Fatalf("Next after Close: %v", err)
	}
	h.mu.Lock()
	n := len(h.viewers)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d viewers registered, want 1", n)
	}
	h.Push(cfgPacket(cfgA))
	k := keyPacket(1, "K")
	h.Push(k)
	expectMerged(t, next(t, b), cfgA, k)
	if h.Reason() != nil {
		t.Fatal("a viewer closing ended the stream")
	}
}

// Decision: Next honours its context, before and while waiting.
func TestViewerNextContext(t *testing.T) {
	h := NewHub()
	v := join(h)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if _, err := v.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled while waiting: %v", err)
	}
	h.Push(cfgPacket(cfgA))
	h.Push(keyPacket(1, "K"))
	if _, err := v.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled: %v", err)
	}
	next(t, v) // the packet is still there for a live context
}

// Decision: viewers coming and going while the producer pushes is safe (run with -race),
// and every viewer's stream starts at a key frame with a config and never goes back in
// time, however it joined.
func TestHubConcurrentViewers(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan string, 64)
	fail := func(s string) {
		select {
		case errs <- s:
		default:
		}
	}

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				v := h.Join()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
				var last int64 = -1
				for n := 0; ; n++ {
					p, err := v.Next(ctx)
					if err != nil {
						break
					}
					if n == 0 && (!p.Key || !p.Config || !bytes.HasPrefix(p.Data, cfgA)) {
						fail("a viewer's first packet is not a key frame with the config")
					}
					if p.PTS < last {
						fail("a viewer went back in time")
					}
					last = p.PTS
				}
				cancel()
				v.Close()
			}
		}()
	}

	h.Push(cfgPacket(cfgA))
	for i := int64(0); i < 600; i++ {
		switch {
		case i%20 == 0:
			h.Push(Packet{Key: true, PTS: i, Data: bytes.Repeat([]byte{byte(i)}, 1<<10)})
		default:
			h.Push(Packet{PTS: i, Data: bytes.Repeat([]byte{byte(i)}, 1<<10)})
		}
		if i%50 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	close(stop)
	h.Close(nil)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
