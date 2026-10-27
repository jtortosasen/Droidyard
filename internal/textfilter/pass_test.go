package textfilter

import (
	"context"
	"testing"
	"time"
)

// fakeClock advances only when the test says so, one step per item.
type fakeClock struct {
	t     time.Time
	reads int
}

func (c *fakeClock) now() time.Time { c.reads++; return c.t }

// Decision (ADR-0135 §2): the clock is read after every item, so a pass overruns its
// budget by at most one item whatever the patterns cost. A pass that read the clock every
// 1,024 items could spend 1,023 slow items past the budget, and a 32 KiB subject on the
// NFA is not a fast item.
func TestPassStopsAtTheBudget(t *testing.T) {
	const step = time.Millisecond
	for _, k := range []int{1, 5, 40} {
		clock := &fakeClock{t: time.Unix(1_000_000, 0)}
		p := PassWithClock(context.Background(), time.Duration(k)*step, clock.now)
		stoppedAt := 0
		for item := 1; item <= 10_000; item++ {
			clock.t = clock.t.Add(step) // item `item` took one step
			if p.Check() != Running {
				stoppedAt = item
				break
			}
		}
		if stoppedAt != k && stoppedAt != k+1 {
			t.Errorf("budget of %d items: stopped after item %d; want %d or %d", k, stoppedAt, k, k+1)
		}
		if got := p.Check(); got != BudgetSpent {
			t.Errorf("budget of %d items: Check = %v after the stop; want BudgetSpent, and it must stay", k, got)
		}
	}

	// A cancelled context ends the pass the same way, and says it was the context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	p := PassWithClock(ctx, time.Hour, clock.now)
	stoppedAt := 0
	for item := 1; item <= 100; item++ {
		if item == 4 {
			cancel()
		}
		clock.t = clock.t.Add(step)
		if p.Check() != Running {
			stoppedAt = item
			break
		}
	}
	if stoppedAt != 4 && stoppedAt != 5 {
		t.Errorf("context cancelled before item 4: stopped after item %d; want 4 or 5", stoppedAt)
	}
	if got := p.Check(); got != Cancelled {
		t.Errorf("Check = %v after the cancel; want Cancelled", got)
	}

	// Nothing is lost by a budget never reached.
	p = PassWithClock(context.Background(), time.Hour, (&fakeClock{t: time.Unix(1_000_000, 0)}).now)
	for i := 0; i < 1000; i++ {
		if p.Check() != Running {
			t.Fatalf("a pass inside its budget stopped at check %d", i)
		}
	}

	// A budget of zero is spent at the first check, not unlimited.
	p = PassWithClock(context.Background(), 0, (&fakeClock{t: time.Unix(1_000_000, 0)}).now)
	if p.Check() != BudgetSpent {
		t.Error("a zero budget must be spent at the first Check")
	}
}

// Decision: the real clock is the monotonic one, so the production Pass is spent by
// elapsed time, not by a wall-clock step.
func TestPassUsesTheRealClock(t *testing.T) {
	p := Pass(context.Background(), 20*time.Millisecond)
	if p.Check() != Running {
		t.Fatal("a fresh 20 ms pass was already spent")
	}
	time.Sleep(40 * time.Millisecond)
	if p.Check() != BudgetSpent {
		t.Error("a 20 ms pass was not spent after 40 ms")
	}
}
