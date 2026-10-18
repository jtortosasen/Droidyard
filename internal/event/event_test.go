package event

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// drain takes everything the subscription holds now.
func drain(s *Subscription) []Event {
	var out []Event
	for {
		e, ok := s.TryNext()
		if !ok {
			return out
		}
		out = append(out, e)
	}
}

func gapOf(t *testing.T, e Event) Gap {
	t.Helper()
	if e.Topic != TopicGap || e.Type != TypeGap {
		t.Fatalf("%s/%s seq %d is not a gap marker", e.Topic, e.Type, e.Seq)
	}
	var g Gap
	if err := json.Unmarshal(e.Payload, &g); err != nil {
		t.Fatal(err)
	}
	if e.Seq != g.ToSeq {
		t.Errorf("the gap %+v has seq %d; want its toSeq", g, e.Seq)
	}
	return g
}

func seqs(es []Event) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.Seq
	}
	return out
}

// TestOneSequenceForTheDaemon: seq is one daemon-wide sequence, strictly increasing
// across topics and across global and workspace events, starting at 1; a new bus is a
// new epoch (capability-api.md §Event envelope; plan m1d, D4). The envelope encodes a
// global event's workspace as null and leaves out an absent operation.
func TestOneSequenceForTheDaemon(t *testing.T) {
	b := New(Options{})
	topics := []string{"t.a", "t.b", "t.c"}
	s := b.Subscribe(topics, nil)
	for i := 0; i < 1000; i++ {
		ws := ""
		if i%2 == 0 {
			ws = "ws-1"
		}
		b.Publish(topics[i%3], "x", ws, "", map[string]int{"i": i})
	}
	got := drain(s)
	if len(got) != 1000 {
		t.Fatalf("received %d events; want 1000", len(got))
	}
	for i, e := range got {
		if e.Seq != int64(i+1) || e.Epoch != b.epoch {
			t.Fatalf("event %d (%s) has seq %d in epoch %s; want %d in %s", i, e.Topic, e.Seq, e.Epoch, i+1, b.epoch)
		}
	}
	if p := b.Position(); p.Seq != 1000 || p.Epoch != b.epoch {
		t.Errorf("position %+v; want seq 1000", p)
	}

	other := New(Options{})
	re := regexp.MustCompile(`^d-[0-9a-f]{8}$`)
	if !re.MatchString(b.epoch) || !re.MatchString(other.epoch) || b.epoch == other.epoch {
		t.Errorf("epochs %q and %q; want two different d-<8 hex>", b.epoch, other.epoch)
	}

	raw, _ := json.Marshal(got[1])
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	if string(m["workspace"]) != "null" || m["operation"] != nil || m["time"] == nil {
		t.Errorf("a global event without an operation encodes as %s; want workspace null, no operation, a time", raw)
	}
}

// TestSubscriptionsFilterByTopic: a subscriber sees only its topics.
func TestSubscriptionsFilterByTopic(t *testing.T) {
	b := New(Options{})
	s := b.Subscribe([]string{"workspace.lifecycle"}, nil)
	b.Publish("operation.progress", "progress", "", "op-1", nil)
	b.Publish("workspace.lifecycle", "opening", "ws-1", "", nil)
	b.Publish("other", "x", "", "", nil)
	got := drain(s)
	if len(got) != 1 || got[0].Topic != "workspace.lifecycle" || got[0].Seq != 2 {
		t.Errorf("received %+v; want only the workspace.lifecycle event, seq 2", got)
	}
}

// TestBroadcastCountsTheSubscriptionsItOffered: Broadcast returns how many subscriptions
// held the topic, which is workspace.show's tabs (ADR-0084 §1): none, then two of three,
// and a closed subscription is no longer counted. A count of every subscription, or a
// constant, fails the second assertion.
func TestBroadcastCountsTheSubscriptionsItOffered(t *testing.T) {
	b := New(Options{})
	if n := b.Broadcast("workspace.state", "show", "ws-1", "", struct{}{}); n != 0 {
		t.Errorf("with no subscription Broadcast returned %d; want 0", n)
	}
	a := b.Subscribe([]string{"workspace.state"}, nil)
	c := b.Subscribe([]string{"workspace.state", "other"}, nil)
	b.Subscribe([]string{"other"}, nil)
	if n := b.Broadcast("workspace.state", "show", "ws-1", "", struct{}{}); n != 2 {
		t.Errorf("Broadcast returned %d; want 2, the subscriptions holding the topic", n)
	}
	if len(drain(a)) != 1 || len(drain(c)) != 1 {
		t.Errorf("the two subscriptions did not each receive the event")
	}
	a.Close()
	if n := b.Broadcast("workspace.state", "show", "ws-1", "", struct{}{}); n != 1 {
		t.Errorf("after one closed, Broadcast returned %d; want 1", n)
	}
}

// TestResumeReplaysThenContinuesLive: a subscriber that resumes from a seq the ring
// still holds, while eight publishers run, receives every event of its topic after
// that seq exactly once: the replay and the registration are atomic with respect to
// Publish (plan m1d, step 2's Rests on). Ten rounds.
func TestResumeReplaysThenContinuesLive(t *testing.T) {
	for round := 0; round < 10; round++ {
		b := New(Options{Queue: 1 << 16})
		var mu sync.Mutex
		var published []Event
		var wg sync.WaitGroup
		for p := 0; p < 8; p++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 400; i++ {
					topic := "mine"
					if i%2 == 1 {
						topic = "theirs"
					}
					e := b.publish(topic, "x", "", "", i)
					mu.Lock()
					published = append(published, e)
					mu.Unlock()
				}
			}()
		}
		for b.Position().Seq < 1000 {
			time.Sleep(10 * time.Microsecond)
		}
		since := b.Position()
		since.Seq -= 500
		s := b.Subscribe([]string{"mine"}, &since)
		wg.Wait()

		got := drain(s)
		seen := map[int64]int{}
		for i, e := range got {
			if e.Topic != "mine" {
				t.Fatalf("round %d: received %s seq %d", round, e.Topic, e.Seq)
			}
			if i > 0 && e.Seq <= got[i-1].Seq {
				t.Fatalf("round %d: seq %d after %d", round, e.Seq, got[i-1].Seq)
			}
			seen[e.Seq]++
		}
		want := 0
		for _, e := range published {
			if e.Topic == "mine" && e.Seq > since.Seq {
				want++
				if seen[e.Seq] != 1 {
					t.Fatalf("round %d: seq %d, published after since %d, was received %d times", round, e.Seq, since.Seq, seen[e.Seq])
				}
			}
		}
		if len(got) != want {
			t.Fatalf("round %d: received %d events; want the %d published after since", round, len(got), want)
		}
	}
}

// TestResumeOlderThanTheRingStartsWithAGap: a since the ring no longer reaches is
// answered by events.gap {since.seq+1, oldest-1}, then the ring (capability-api.md
// §Subscriptions).
func TestResumeOlderThanTheRingStartsWithAGap(t *testing.T) {
	b := New(Options{RingEvents: 10})
	for i := 0; i < 50; i++ {
		b.Publish("t", "x", "", "", i)
	}
	s := b.Subscribe([]string{"t"}, &Position{Epoch: b.epoch, Seq: 5})
	got := drain(s)
	if len(got) != 11 {
		t.Fatalf("received %v; want a gap then 41..50", seqs(got))
	}
	if g := gapOf(t, got[0]); g != (Gap{6, 40}) {
		t.Errorf("gap %+v; want {6, 40}", g)
	}
	for i, e := range got[1:] {
		if e.Seq != int64(41+i) {
			t.Fatalf("after the gap: %v; want 41..50", seqs(got[1:]))
		}
	}

	// From within the ring: no gap.
	s = b.Subscribe([]string{"t"}, &Position{Epoch: b.epoch, Seq: 45})
	if got := seqs(drain(s)); len(got) != 5 || got[0] != 46 {
		t.Errorf("resume from 45 received %v; want 46..50", got)
	}
}

// TestResumeFromAnotherEpochStartsWithAGap: a since of another epoch starts with
// events.gap {1, oldest-1}, the empty {1, 0} while the ring still holds seq 1, so the
// client still learns that the epoch changed; then everything the ring holds
// (plan m1d, D4).
func TestResumeFromAnotherEpochStartsWithAGap(t *testing.T) {
	b := New(Options{RingEvents: 10})
	for i := 0; i < 5; i++ {
		b.Publish("t", "x", "", "", i)
	}
	stale := &Position{Epoch: "d-00000000", Seq: 3}
	got := drain(b.Subscribe([]string{"t"}, stale))
	if len(got) != 6 || gapOf(t, got[0]) != (Gap{1, 0}) || got[0].Seq != 0 || got[1].Seq != 1 || got[5].Seq != 5 {
		t.Fatalf("received %v; want the gap {1, 0} then 1..5", seqs(got))
	}

	for i := 0; i < 45; i++ {
		b.Publish("t", "x", "", "", i)
	}
	got = drain(b.Subscribe([]string{"t"}, stale))
	if len(got) != 11 || gapOf(t, got[0]) != (Gap{1, 40}) || got[1].Seq != 41 || got[10].Seq != 50 {
		t.Fatalf("received %v; want the gap {1, 40} then 41..50", seqs(got))
	}
}

// TestTheRingIsBoundedByCountAndBytes: with the default bounds, the ring holds at most
// 4096 events and at most 4 MiB of encoded envelope, whichever is reached first
// (plan m1d, D5); each cap is reached here without the other.
func TestTheRingIsBoundedByCountAndBytes(t *testing.T) {
	b := New(Options{})
	for i := 0; i < 5000; i++ {
		b.Publish("t", "x", "", "", i)
	}
	if len(b.ring) != 4096 || b.ring[0].Seq != 5000-4096+1 || b.ringBytes > 4<<20 {
		t.Errorf("after 5000 small events the ring holds %d (oldest %d, %d bytes); want 4096, oldest 905", len(b.ring), b.ring[0].Seq, b.ringBytes)
	}

	b = New(Options{})
	big := strings.Repeat("x", 10<<10)
	for i := 0; i < 1000; i++ {
		b.Publish("t", "x", "", "", big)
	}
	sum := 0
	for _, e := range b.ring {
		enc, _ := json.Marshal(e)
		sum += len(enc)
	}
	if sum != b.ringBytes || sum > 4<<20 || sum+b.ring[0].size <= 4<<20 || len(b.ring) >= 4096 {
		t.Errorf("after 1000 10 KiB events the ring holds %d events of %d encoded bytes (counted %d); want at most 4 MiB, full, under 4096 events", len(b.ring), sum, b.ringBytes)
	}
}

// TestASlowSubscriberNeverBlocksAPublisher: a subscriber that does not read never
// blocks Publish; what its queue cannot hold is dropped and reported as events.gap,
// so delivered seqs and gap ranges together cover every seq of its topics, in order
// (capability-api.md §Event envelope, "Loss is explicit").
func TestASlowSubscriberNeverBlocksAPublisher(t *testing.T) {
	b := New(Options{Queue: 16})
	s := b.Subscribe([]string{"mine"}, nil)
	var mine []int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			topic := "mine"
			if i%3 == 0 {
				topic = "theirs"
			}
			if e := b.publish(topic, "x", "", "", i); topic == "mine" {
				mine = append(mine, e.Seq)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("10000 publishes did not return within 1 s with a subscriber that does not read")
	}

	covered := map[int64]bool{}
	gaps := 0
	var last int64 = -1
	for _, e := range drain(s) {
		if e.Seq <= last {
			t.Fatalf("seq %d after %d", e.Seq, last)
		}
		last = e.Seq
		if e.Topic == TopicGap {
			g := gapOf(t, e)
			gaps++
			for q := g.FromSeq; q <= g.ToSeq; q++ {
				covered[q] = true
			}
			continue
		}
		covered[e.Seq] = true
	}
	if gaps == 0 {
		t.Error("10000 events through a 16-envelope queue produced no gap")
	}
	for _, q := range mine {
		if !covered[q] {
			t.Fatalf("seq %d was neither delivered nor covered by a gap", q)
		}
	}

	s.Close()
	b.Publish("mine", "x", "", "", nil)
	if e, ok := s.TryNext(); ok {
		t.Errorf("a closed subscription delivered seq %d", e.Seq)
	}
	if len(b.subs) != 0 {
		t.Errorf("the bus still holds %d subscriptions after Close", len(b.subs))
	}
}

// TestDeliveryIsSynchronous: once Publish returns, its event is in every matching
// subscription's queue; once Position returns a seq, every matching event up to it is
// (plan m1d, step 2's Rests on; step 3's end-of-wait drain rests on it). With other
// publishers running, 1000 times.
func TestDeliveryIsSynchronous(t *testing.T) {
	b := New(Options{Queue: 1 << 20})
	s := b.Subscribe([]string{"mine"}, nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					b.Publish("theirs", "x", "", "", nil)
				}
			}
		}()
	}
	for i := 0; i < 1000; i++ {
		e := b.publish("mine", "x", "", "", i)
		got, ok := s.TryNext()
		if !ok || got.Seq != e.Seq {
			close(stop)
			wg.Wait()
			t.Fatalf("try %d: TryNext right after Publish of seq %d returned %v, %d", i, e.Seq, ok, got.Seq)
		}
	}
	close(stop)
	wg.Wait()

	// Position: another goroutine publishes on the subscription's topic; after each
	// Position read, everything up to it is already there.
	var published []int64
	var mu sync.Mutex
	stop = make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20000; i++ {
			select {
			case <-stop:
				return
			default:
			}
			e := b.publish("mine", "x", "", "", i)
			mu.Lock()
			published = append(published, e.Seq)
			mu.Unlock()
		}
	}()
	type check struct {
		pos      int64
		received int
	}
	var checks []check
	received := 0
	for i := 0; i < 1000; i++ {
		p := b.Position()
		received += len(drain(s))
		checks = append(checks, check{p.Seq, received})
	}
	close(stop)
	wg.Wait()
	for _, c := range checks {
		upTo := 0
		for _, q := range published {
			if q <= c.pos {
				upTo++
			}
		}
		if c.received < upTo {
			t.Fatalf("after Position %d, %d events had been received; %d of the subscription's topic were published up to it", c.pos, c.received, upTo)
		}
	}
}

// TestPublishToKeepsAStreamOutOfTheRing: PublishTo assigns a seq but appends nothing to
// the replay ring and offers the event to its one subscription alone (plan m1j, D8).
// The ring holds 16 events; two operation.progress events are followed by twenty
// file.changes events delivered through PublishTo, more than a ring of sixteen could
// hold. A subscriber that resumes from before the two progress events replays both and
// gets no gap — because the twenty never entered the ring. Were PublishTo to append to
// the ring (the mutation), the two progress events would be evicted and the resume would
// start with an events.gap.
func TestPublishToKeepsAStreamOutOfTheRing(t *testing.T) {
	b := New(Options{RingEvents: 16})
	before := b.Position()

	// The stream's one subscription: file.changes is offered to it alone.
	stream := b.Subscribe([]string{"file.changes"}, nil)
	// A second subscription to the same topic only hears what Publish delivers to the
	// topic, never what PublishTo offers to another subscription.
	bystander := b.Subscribe([]string{"file.changes"}, nil)

	b.Publish("operation.progress", "progress", "", "op-1", nil)
	b.Publish("operation.progress", "progress", "", "op-1", nil)
	for i := 0; i < 20; i++ {
		b.PublishTo(stream, "file.changes", "changes", "ws-1", "op-watch", map[string]int{"i": i})
	}

	if got := len(drain(stream)); got != 20 {
		t.Fatalf("the stream received %d file.changes events; want 20", got)
	}
	if got := drain(bystander); len(got) != 0 {
		t.Fatalf("a second subscription to file.changes received %d events; PublishTo offers to one subscription alone", len(got))
	}
	if p := b.Position(); p.Seq != 22 {
		t.Errorf("position seq %d; want 22 (each PublishTo takes a seq)", p.Seq)
	}

	resumed := b.Subscribe([]string{"operation.progress"}, &Position{Epoch: before.Epoch, Seq: before.Seq})
	got := drain(resumed)
	if len(got) != 2 {
		t.Fatalf("the resume replayed %d events; want the two operation.progress events and no gap", len(got))
	}
	for i, e := range got {
		if e.Topic != "operation.progress" || e.Seq != int64(i+1) {
			t.Fatalf("resumed event %d is %s seq %d; want operation.progress seq %d", i, e.Topic, e.Seq, i+1)
		}
	}
}

// TestResumeGapCountsOnlyEvictedEvents: seqs PublishTo assigned never entered the ring,
// so a resume from just before them reports no gap for them; only what the ring dropped
// is a loss (m1j §Changes, step 4 N5).
func TestResumeGapCountsOnlyEvictedEvents(t *testing.T) {
	b := New(Options{RingEvents: 3})
	stream := b.Subscribe([]string{"file.changes"}, nil)
	b.Publish("t", "x", "", "", nil) // seq 1, seen by the client
	seen := b.Position()
	for i := 0; i < 4; i++ { // seqs 2-5, never in the ring
		b.PublishTo(stream, "file.changes", "changes", "ws", "op", nil)
	}
	for i := 0; i < 3; i++ { // seqs 6-8; the ring of 3 drops seq 1
		b.Publish("t", "x", "", "", nil)
	}
	got := drain(b.Subscribe([]string{"t"}, &seen))
	if len(got) != 3 || got[0].Seq != 6 {
		t.Fatalf("resume from seq 1 gave %d events starting %s seq %d; want seqs 6-8 and no gap", len(got), got[0].Topic, got[0].Seq)
	}

	// From before the dropped seq 1, the gap is reported and ends at it.
	got = drain(b.Subscribe([]string{"t"}, &Position{Epoch: seen.Epoch, Seq: 0}))
	if len(got) != 4 || got[0].Type != "gap" {
		t.Fatalf("resume from seq 0 gave %+v; want a gap then seqs 6-8", got)
	}
}

// TestNextWaitsAndEndsOnClose: the blocking read returns what is published, and
// ErrClosed once the subscription is closed.
func TestNextWaitsAndEndsOnClose(t *testing.T) {
	b := New(Options{})
	s := b.Subscribe([]string{"t"}, nil)
	go func() {
		time.Sleep(20 * time.Millisecond)
		b.Publish("t", "x", "", "", nil)
	}()
	ctx := t.Context()
	if e, err := s.Next(ctx); err != nil || e.Seq != 1 {
		t.Fatalf("Next: %d, %v", e.Seq, err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.Close()
	}()
	if _, err := s.Next(ctx); err != ErrClosed {
		t.Errorf("Next after Close: %v; want ErrClosed", err)
	}
}

// TestTheCoreDoesNotImportTheBus: internal/capability and internal/workspace publish
// through capability.Publisher and never import internal/event, which imports
// capability (plan m1d, B4).
func TestTheCoreDoesNotImportTheBus(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "../capability", "../workspace").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	if !strings.Contains(string(out), "internal/capability") || !strings.Contains(string(out), "internal/workspace") {
		t.Fatalf("go list -deps did not list the packages: %s", out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasSuffix(pkg, "/internal/event") {
			t.Errorf("internal/capability or internal/workspace depends on %s", pkg)
		}
	}
}
