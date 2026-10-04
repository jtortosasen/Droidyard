package artifact

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// fakeClock is driven by hand: Advance moves time and runs the timers that came due,
// outside its own lock, as a real timer's goroutine would.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func (t *fakeTimer) Stop() bool { t.stopped = true; return true }

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	rest := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.stopped:
		case !t.at.After(c.now):
			due = append(due, t)
		default:
			rest = append(rest, t)
		}
	}
	c.timers = rest
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

func newStore(t *testing.T, budget int64) (*Store, *fakeClock) {
	t.Helper()
	clk := newClock()
	s, err := New(Options{Epoch: "d-0a1b2c3d", Budget: budget, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, clk
}

func put(t *testing.T, s *Store, n int) Ref {
	t.Helper()
	r, err := s.Put(Item{Data: bytes.Repeat([]byte{'x'}, n), MediaType: "image/png", Width: 2, Height: 3, Untrusted: true, Producer: "device.screenshot"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func code(err error) capability.ErrorCode {
	if e, ok := err.(*capability.Error); ok {
		return e.Code
	}
	return ""
}

// Decision: an artifact lives 15 minutes from creation, and a read does not extend it.
func TestTTLCountsFromCreationAndAReadDoesNotExtendIt(t *testing.T) {
	s, clk := newStore(t, 1<<20)
	r := put(t, s, 10)
	clk.Advance(14 * time.Minute)
	if _, err := s.Get(r.Ref); err != nil {
		t.Fatalf("inside the TTL: %v", err)
	}
	clk.Advance(time.Minute) // 15 min after creation, though read at 14
	if _, err := s.Get(r.Ref); code(err) != capability.ErrArtifactExpired {
		t.Fatalf("past the TTL: %v, want ARTIFACT_EXPIRED", err)
	}
}

// Decision: expired bytes are released by the timer, with no read and no put.
func TestTheSweepReleasesBytesWithoutARead(t *testing.T) {
	s, clk := newStore(t, 1<<20)
	put(t, s, 100)
	clk.Advance(5 * time.Minute)
	put(t, s, 50)
	if s.Bytes() != 150 {
		t.Fatalf("bytes = %d, want 150", s.Bytes())
	}
	clk.Advance(10 * time.Minute) // the first is 15 min old, the second 10
	if s.Bytes() != 50 || s.Len() != 1 {
		t.Fatalf("after the first expiry: bytes %d len %d, want 50 and 1 (the timer must re-arm for the second)", s.Bytes(), s.Len())
	}
	clk.Advance(5 * time.Minute)
	if s.Bytes() != 0 || s.Len() != 0 {
		t.Fatalf("after both: bytes %d len %d, want 0", s.Bytes(), s.Len())
	}
}

// Decision: under the byte budget the least recently USED entry goes first, not the
// oldest: a read protects an entry.
func TestLRUEvictsTheLeastRecentlyUsed(t *testing.T) {
	s, _ := newStore(t, 300)
	a := put(t, s, 100)
	b := put(t, s, 100)
	c := put(t, s, 100)
	if _, err := s.Get(a.Ref); err != nil { // a is now the most recent; b the least
		t.Fatal(err)
	}
	d := put(t, s, 100) // over budget: evicts b
	if _, err := s.Get(b.Ref); code(err) != capability.ErrArtifactExpired {
		t.Fatalf("b: %v, want ARTIFACT_EXPIRED", err)
	}
	for _, r := range []Ref{a, c, d} {
		if _, err := s.Get(r.Ref); err != nil {
			t.Fatalf("%s should be held: %v", r.Ref, err)
		}
	}
	if s.Bytes() != 300 {
		t.Fatalf("bytes = %d, want 300", s.Bytes())
	}
}

// Decision: one big put evicts as many as it needs, and an artifact larger than the
// whole budget is refused instead of emptying the store.
func TestBudgetEvictsEnoughAndRefusesWhatCannotFit(t *testing.T) {
	s, _ := newStore(t, 300)
	a := put(t, s, 100)
	b := put(t, s, 100)
	big := put(t, s, 250)
	for _, r := range []Ref{a, b} {
		if _, err := s.Get(r.Ref); code(err) != capability.ErrArtifactExpired {
			t.Fatalf("%v, want ARTIFACT_EXPIRED", err)
		}
	}
	if _, err := s.Get(big.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(Item{Data: make([]byte, 301)}); err != ErrTooLarge {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if _, err := s.Get(big.Ref); err != nil {
		t.Fatalf("a refused put emptied the store: %v", err)
	}
}

// Decision: a hit returns the bytes and the ArtifactRef record as stored, in the
// ephemeral class, with the untrusted flag and image dimensions carried per artifact.
func TestGetReturnsTheRecordAndBytes(t *testing.T) {
	s, clk := newStore(t, 1<<20)
	r, _ := s.Put(Item{Data: []byte("abc"), MediaType: "text/plain", Untrusted: false, Producer: "device.logs"})
	img := put(t, s, 4)
	e, err := s.Get(r.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(e.Data) != "abc" || e.Producer != "device.logs" || e.Ref != r {
		t.Fatalf("entry = %+v", e)
	}
	if r.Durability != DurabilityEphemeral || r.Bytes != 3 || r.MediaType != "text/plain" || r.Untrusted || r.Width != 0 {
		t.Fatalf("ref = %+v", r)
	}
	if !img.Untrusted || img.Width != 2 || img.Height != 3 {
		t.Fatalf("image ref = %+v", img)
	}
	if want := clk.Now().Add(DefaultTTL); !r.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt = %v, want %v", r.ExpiresAt, want)
	}
	if !strings.HasPrefix(r.Ref, RefPrefix) {
		t.Fatalf("ref = %q", r.Ref)
	}
}

// Decision: Put copies, so a producer reusing its buffer cannot change a stored artifact.
func TestPutCopiesTheBytes(t *testing.T) {
	s, _ := newStore(t, 1<<20)
	buf := []byte("abc")
	r, _ := s.Put(Item{Data: buf})
	buf[0] = 'Z'
	e, _ := s.Get(r.Ref)
	if string(e.Data) != "abc" {
		t.Fatalf("stored %q", e.Data)
	}
}

// Decision: the miss classification, exactly as plan m1e step 7 says: EXPIRED for
// another epoch's id and for this epoch's counter at or below the highest issued but
// not held (including wrong random bits); NOT_FOUND for malformed ids and a counter
// above the highest issued.
func TestMissClassification(t *testing.T) {
	s, _ := newStore(t, 100)
	a := put(t, s, 100)
	b := put(t, s, 100) // evicts a
	id := strings.TrimPrefix(b.Ref, RefPrefix)
	parts := strings.Split(id, ".") // e . epoch . counter . random

	cases := []struct {
		name string
		ref  string
		want capability.ErrorCode
	}{
		{"evicted, this epoch", a.Ref, capability.ErrArtifactExpired},
		{"held counter, wrong random bits", RefPrefix + strings.Join([]string{"e", parts[1], parts[2], "000000000000"}, "."), capability.ErrArtifactExpired},
		{"another epoch", RefPrefix + strings.Join([]string{"e", "d-ffffffff", parts[2], parts[3]}, "."), capability.ErrArtifactExpired},
		{"another epoch, counter above anything here", RefPrefix + "e.d-ffffffff.99.0123456789ab", capability.ErrArtifactExpired},
		{"counter above the highest", RefPrefix + strings.Join([]string{"e", parts[1], "3", parts[3]}, "."), capability.ErrArtifactNotFound},
		{"counter zero", RefPrefix + strings.Join([]string{"e", parts[1], "0", parts[3]}, "."), capability.ErrArtifactNotFound},
		{"empty", "", capability.ErrArtifactNotFound},
		{"wrong scheme text", "http://artifact/" + id, capability.ErrArtifactNotFound},
		{"unknown class", RefPrefix + "d." + parts[1] + "." + parts[2] + "." + parts[3], capability.ErrArtifactNotFound},
		{"too few parts", RefPrefix + "e.d-0a1b2c3d.1", capability.ErrArtifactNotFound},
		{"too many parts", RefPrefix + id + ".x", capability.ErrArtifactNotFound},
		{"counter with leading zero", RefPrefix + strings.Join([]string{"e", parts[1], "01", parts[3]}, "."), capability.ErrArtifactNotFound},
		{"counter not a number", RefPrefix + strings.Join([]string{"e", parts[1], "x", parts[3]}, "."), capability.ErrArtifactNotFound},
		{"random too short", RefPrefix + strings.Join([]string{"e", parts[1], parts[2], "abc"}, "."), capability.ErrArtifactNotFound},
		{"random not hex", RefPrefix + strings.Join([]string{"e", parts[1], parts[2], "zzzzzzzzzzzz"}, "."), capability.ErrArtifactNotFound},
		{"empty epoch", RefPrefix + "e..1.0123456789ab", capability.ErrArtifactNotFound},
	}
	for _, c := range cases {
		_, err := s.Get(c.ref)
		if code(err) != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
	e := func() *capability.Error { _, err := s.Get(a.Ref); return err.(*capability.Error) }()
	if e.NextStep == "" {
		t.Error("ARTIFACT_EXPIRED carries no nextStep")
	}
}

// Decision: a store of another epoch (a restarted daemon) says EXPIRED, not NOT_FOUND,
// for a ref whose counter it has never issued.
func TestAStoreFromAnotherEpoch(t *testing.T) {
	clk := newClock()
	old, _ := New(Options{Epoch: "d-11111111", Clock: clk})
	fresh, _ := New(Options{Epoch: "d-22222222", Clock: clk})
	t.Cleanup(old.Close)
	t.Cleanup(fresh.Close)
	r := put(t, old, 10)
	// fresh has issued nothing: only the epoch check can say EXPIRED (not NOT_FOUND).
	_, err := fresh.Get(r.Ref)
	if code(err) != capability.ErrArtifactExpired {
		t.Fatalf("%v, want ARTIFACT_EXPIRED", err)
	}
}

// Decision: ids round-trip through Get as a full reference and as a bare id, and the
// epoch is validated at construction because the id is split on '.'.
func TestIDRoundTripAndEpochValidation(t *testing.T) {
	s, _ := newStore(t, 1<<20)
	r := put(t, s, 5)
	bare := strings.TrimPrefix(r.Ref, RefPrefix)
	if _, err := s.Get(bare); err != nil {
		t.Fatalf("bare id: %v", err)
	}
	if !strings.HasPrefix(bare, "e.d-0a1b2c3d.1.") || len(bare) != len("e.d-0a1b2c3d.1.")+randomHexLen {
		t.Fatalf("id = %q", bare)
	}
	if r2 := put(t, s, 5); r2.Ref == r.Ref || !strings.Contains(r2.Ref, ".2.") {
		t.Fatalf("counter did not advance: %q", r2.Ref)
	}
	for _, bad := range []string{"", "a.b", "a/b", "a b"} {
		if _, err := New(Options{Epoch: bad}); err == nil {
			t.Errorf("epoch %q accepted", bad)
		}
	}
}

// Decision: two puts of identical bytes get distinct, unguessable ids (random bits).
func TestIDsCarryRandomBits(t *testing.T) {
	s, _ := newStore(t, 1<<20)
	seen := map[string]bool{}
	for range 20 {
		_, _, rnd, ok := parseID(strings.TrimPrefix(put(t, s, 1).Ref, RefPrefix))
		if !ok {
			t.Fatal("unparseable id")
		}
		seen[rnd] = true
	}
	if len(seen) < 19 {
		t.Fatalf("only %d distinct random parts in 20", len(seen))
	}
}

// Decision: the store is safe under concurrent use (run with -race), and its byte
// accounting survives eviction pressure.
func TestConcurrentPutsAndGets(t *testing.T) {
	s, clk := newStore(t, 1000)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				r, err := s.Put(Item{Data: bytes.Repeat([]byte{byte(g)}, 10+i%50)})
				if err != nil {
					t.Error(err)
					return
				}
				if e, err := s.Get(r.Ref); err == nil && len(e.Data) != r.Bytes {
					t.Errorf("wrong data for %s", r.Ref)
				}
				if i%50 == 0 {
					clk.Advance(time.Minute)
				}
				_ = s.Bytes()
			}
		}()
	}
	wg.Wait()
	if b := s.Bytes(); b < 0 || b > 1000 {
		t.Fatalf("bytes = %d outside 0..1000", b)
	}
	var sum int64
	s.mu.Lock()
	for _, h := range s.entries {
		sum += int64(len(h.entry.Data))
	}
	s.mu.Unlock()
	if sum != s.Bytes() || s.lru.Len() != s.Len() {
		t.Fatalf("accounting drifted: sum %d bytes %d lru %d len %d", sum, s.Bytes(), s.lru.Len(), s.Len())
	}
}
