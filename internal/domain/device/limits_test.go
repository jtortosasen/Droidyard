package device

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Decision (plan m2v step 5): LengthenLimits raises the limit of every adb command of one ADB to
// a floor, and zero, the product's value, raises nothing. A test whose fake adb is a process
// a loaded host starts late opts in; every other ADB keeps ADR-0078's and the probes' own
// limits, which the tests of a timeout rely on. Seen failing with the floor ignored in Run:
// the floored command ended at its own 20 ms.
func TestLengthenLimitsRaisesARunsLimitAndNothingElse(t *testing.T) {
	t.Parallel()
	const own = 20 * time.Millisecond

	// Without the floor the command's own limit ends it.
	plain := NewADBWithExec(nil, func(ctx context.Context, _ string, _ []string) ([]byte, []byte, int, error) {
		<-ctx.Done()
		return nil, nil, 0, ctx.Err()
	})
	if _, err := plain.Run(context.Background(), "adb", own, 1<<10, "devices"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("a Run with no floor ended with %v; want its own limit, ErrTimeout", err)
	}

	// With it, the command outlives its own limit, and ends when its adb answers.
	gate := make(chan struct{})
	floored := NewADBWithExec(nil, func(ctx context.Context, _ string, _ []string) ([]byte, []byte, int, error) {
		select {
		case <-gate:
			return []byte("ok"), nil, 0, nil
		case <-ctx.Done():
			return nil, nil, 0, ctx.Err()
		}
	})
	floored.LengthenLimits(time.Minute)
	type result struct {
		out Output
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := floored.Run(context.Background(), "adb", own, 1<<10, "devices")
		done <- result{out, err}
	}()
	// An absence waited for: the run is still going well past its own 20 ms.
	select {
	case r := <-done:
		t.Fatalf("a Run with a floor of a minute ended within %v with %v; want it to wait for adb", 10*own, r.err)
	case <-time.After(10 * own):
	}
	close(gate)
	r := <-done
	if r.err != nil || string(r.out.Stdout) != "ok" {
		t.Fatalf("the floored Run ended with (%q, %v); want adb's answer", r.out.Stdout, r.err)
	}

	// The wait for a remote's listing follows the floor too, and is the product's without it.
	if got, want := plain.remoteTimeout(), 2*detailsTimeout; got != want {
		t.Errorf("remoteTimeout() = %v with no floor; want %v", got, want)
	}
	if got := floored.remoteTimeout(); got != time.Minute {
		t.Errorf("remoteTimeout() = %v with a floor of a minute; want the floor", got)
	}
}
