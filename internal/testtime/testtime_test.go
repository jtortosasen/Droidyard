package testtime

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Decision: the scale is the race factor (1 plain, 3 under -race) times the person's
// variable, and a malformed variable is a panic that names it, never a silent 1.
func TestScale(t *testing.T) {
	want := 1
	if raceBuilt {
		want = 3
	}
	if raceFactor != want {
		t.Fatalf("raceFactor = %d; want %d for this build", raceFactor, want)
	}
	t.Setenv(EnvScale, "1")
	if got := Scale(); got != want {
		t.Errorf("Scale() = %d; want %d", got, want)
	}
	t.Setenv(EnvScale, "2")
	if got := Scale(); got != 2*want {
		t.Errorf("Scale() with %s=2 is %d; want %d", EnvScale, got, 2*want)
	}
	if got, w := Bound(time.Second), time.Duration(2*want)*time.Second; got != w {
		t.Errorf("Bound(1s) = %v; want %v", got, w)
	}
	for _, bad := range []string{"0", "-1", "x"} {
		t.Setenv(EnvScale, bad)
		func() {
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, EnvScale) {
					t.Errorf("%s=%q: no panic naming the variable (got %q)", EnvScale, bad, msg)
				}
			}()
			Scale()
		}()
	}
}

type fakeTB struct {
	testing.TB
	msg   string
	fatal int
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fatal++
	f.msg = fmt.Sprintf(format, args...)
}

// Decision: Eventually fails naming what and within, after within and not Bound(within):
// scaling is the caller's choice. Under -race the scaled wait would be at least 3*within.
func TestEventuallyFailsAtWithinUnscaled(t *testing.T) {
	f := &fakeTB{}
	const within = time.Second
	start := time.Now()
	Eventually(f, within, "the thing", func() bool { return false })
	elapsed := time.Since(start)
	if f.fatal != 1 || !strings.Contains(f.msg, within.String()) || !strings.Contains(f.msg, "the thing") {
		t.Errorf("Fatalf called %d times with %q; want once, naming %s and the condition", f.fatal, f.msg, within)
	}
	if elapsed < within || elapsed >= 3*within {
		t.Errorf("gave up after %v; want after %v and before the scaled %v", elapsed, within, 3*within)
	}
}

// Decision: a condition that holds returns at once, with no failure.
func TestEventuallyReturnsAtOnce(t *testing.T) {
	f := &fakeTB{}
	start := time.Now()
	Eventually(f, time.Minute, "holds", func() bool { return true })
	if f.fatal != 0 || time.Since(start) > time.Second {
		t.Errorf("Fatalf called %d times, took %v; want neither", f.fatal, time.Since(start))
	}
}
