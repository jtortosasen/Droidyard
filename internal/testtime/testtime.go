// Package testtime is the one time scale and the one polling helper the tests share. Only
// tests import it, so it is linked into no binary, and the environment variable it reads is
// read by test binaries alone (ADR-0024 is untouched).
//
// A test holds four kinds of time, and only the first is scaled freely:
//
//   - A hang guard: a bound whose only job is to end a test that would hang. It is
//     testtime.Bound(d): d times Scale().
//   - A product claim: a bound that is the number a document promises (ADR-0104 §4's 2 s
//     loss, ADR-0109 §3's 2 s reconcile). It is scaled only with the level-5 run that timed
//     it named beside it; with none, it stays unscaled.
//   - A window the test itself must meet (a race between two goroutines it starts). It is
//     redesigned so no clock decides it, never scaled.
//   - An absence waited for ("nothing within X"). Load never makes it fail, it only makes the
//     test weaker, so it is not scaled to make it pass.
//
// A scaled bound that separates two product numbers (3 s against a 10 s retry) is held below
// the larger.
package testtime

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// EnvScale is the variable a person on a loaded machine sets to stretch every guard.
// CI and the hook do not set it: the tests must hold without it.
const EnvScale = "DROIDYARD_TEST_TIMESCALE"

// Scale is 3 when the binary is built with -race and 1 otherwise, times the positive integer
// in DROIDYARD_TEST_TIMESCALE when it is set. A malformed value panics, naming the variable.
func Scale() int {
	n := raceFactor
	if v, ok := os.LookupEnv(EnvScale); ok {
		k, err := strconv.Atoi(v)
		if err != nil || k < 1 {
			panic(EnvScale + " must be a positive integer, got " + strconv.Quote(v))
		}
		n *= k
	}
	return n
}

// Bound is d scaled for a hang guard.
func Bound(d time.Duration) time.Duration { return d * time.Duration(Scale()) }

// Eventually polls cond every 10 ms until within has passed and fails t, naming what and
// within, when it never held. It scales nothing: a caller whose bound is a hang guard passes
// Bound(d).
func Eventually(t testing.TB, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if cond() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("not within %s: %s", within, what)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
