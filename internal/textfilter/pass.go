package textfilter

import (
	"context"
	"time"
)

// Stop says why a pass ended, or that it has not.
type Stop int

const (
	// Running: the pass may take another item.
	Running Stop = iota
	// BudgetSpent: the time budget ran out.
	BudgetSpent
	// Cancelled: the call's context ended first.
	Cancelled
)

// Checker is the time budget of one filter pass. A pass calls Check after every item:
// one subject matched against all of the call's patterns is the smallest unit that
// cannot be interrupted, so the pass overruns its budget by at most one item, whatever
// the patterns are. The clock is read every time, not every few items: a read costs tens
// of nanoseconds against a regex pass over a line, and sampling would let a slow item
// repeat for a whole sample. A Checker is for one goroutine.
type Checker struct {
	ctx      context.Context
	now      func() time.Time
	deadline time.Time
	stop     Stop
}

// Pass starts a pass: it ends when ctx ends or when budget has passed on the monotonic
// clock. A budget of zero or less is spent at the first Check.
func Pass(ctx context.Context, budget time.Duration) *Checker {
	return PassWithClock(ctx, budget, time.Now)
}

// PassWithClock is Pass with an injected clock, for tests. Its readings must carry the
// monotonic clock (time.Now's do), or the budget is judged on wall time.
func PassWithClock(ctx context.Context, budget time.Duration, now func() time.Time) *Checker {
	return &Checker{ctx: ctx, now: now, deadline: now().Add(budget)}
}

// Check is called after each item and says whether the pass may go on. Once it has said
// otherwise it keeps saying it.
func (c *Checker) Check() Stop {
	if c.stop != Running {
		return c.stop
	}
	if c.ctx.Err() != nil {
		c.stop = Cancelled
	} else if !c.now().Before(c.deadline) {
		c.stop = BudgetSpent
	}
	return c.stop
}
