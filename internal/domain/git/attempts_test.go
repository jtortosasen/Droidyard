package git

import (
	"slices"
	"testing"
	"time"
)

// timeoutAttempts is how many attempts a timeout test makes before it gives up.
const timeoutAttempts = 5

// untilConclusive is the one retry of the three tests that end a hung git by its limit
// (TestTimeoutKillsTheGroup, TestUpdateTimeout, TestFetchTimeout). The limit under test must
// stay short, and the claim is about what happens to the process git started inside it, so
// an attempt whose limit passed before that process existed (its pid file, its start mark)
// proves nothing about it: it is inconclusive, and the next attempt gets twice the limit.
//
// attempt runs once with the limit and reports whether its precondition appeared. It checks
// every claim that holds either way (the call ended with the timeout, no group outlived it)
// itself, in every attempt, so a retry never hides a failure; a conclusive attempt, the only
// one that can show the hung process was ended, also checks that.
//
// It returns the limits tried; ok is false when no attempt was conclusive, and the caller
// then fails naming them.
func untilConclusive(start time.Duration, attempt func(limit time.Duration) (conclusive bool)) (tried []time.Duration, ok bool) {
	limit := start
	for i := 0; i < timeoutAttempts; i++ {
		tried = append(tried, limit)
		if attempt(limit) {
			return tried, true
		}
		limit *= 2
	}
	return tried, false
}

// Decision (plan m2v step 4): a retry cannot turn a real failure into a pass. The helper
// stops at the first conclusive attempt, gives each retry twice the limit before it, and
// reports failure, naming every limit, when five attempts in a row were inconclusive: a
// test whose precondition never appears fails, it does not pass for having nothing to say.
func TestUntilConclusive(t *testing.T) {
	t.Parallel()
	t.Run("stops at the first conclusive attempt, each retry with twice the limit", func(t *testing.T) {
		t.Parallel()
		var seen []time.Duration
		tried, ok := untilConclusive(2*time.Second, func(limit time.Duration) bool {
			seen = append(seen, limit)
			return len(seen) == 3
		})
		want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
		if !ok || !slices.Equal(tried, want) || !slices.Equal(seen, want) {
			t.Errorf("ok %v, tried %v, attempts saw %v; want true and %v", ok, tried, seen, want)
		}
	})
	t.Run("five inconclusive attempts fail, naming the limits", func(t *testing.T) {
		t.Parallel()
		calls := 0
		tried, ok := untilConclusive(time.Second, func(time.Duration) bool { calls++; return false })
		want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
		if ok || calls != 5 || !slices.Equal(tried, want) {
			t.Errorf("ok %v after %d attempts, tried %v; want false after 5 with %v", ok, calls, tried, want)
		}
	})
	t.Run("a conclusive first attempt is the only one", func(t *testing.T) {
		t.Parallel()
		calls := 0
		_, ok := untilConclusive(time.Second, func(time.Duration) bool { calls++; return true })
		if !ok || calls != 1 {
			t.Errorf("ok %v after %d attempts; want true after 1", ok, calls)
		}
	})
}
