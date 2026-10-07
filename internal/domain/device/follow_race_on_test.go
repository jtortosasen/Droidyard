//go:build race

package device

import "time"

// followRaceOverhead is the wall time the race detector adds to the follow fixture beyond
// the window itself (plan m1n step 1); it is zero without -race (follow_race_off_test.go).
//
// The follow fixture re-executes the test binary as the fake adb, and a race-instrumented
// Go binary spends about a second in the race runtime's startup on this platform: an empty
// -race binary took ~1.05 s to start with 0 % CPU, so the cost is the race runtime, not the
// fake or the follow. The fixture spawns the fake four times before the window closes
// (`devices -l`, the boot probe, `date` and the logcat client), measured at ~4.28 s, which
// is why a follow that ran its whole 2 s window took ~6.3 s under -race against ~2.1 s
// without. Widened by 5 s to leave margin for a loaded machine; the window's own 2 s lower
// bound and the follow path are unchanged.
const followRaceOverhead = 5 * time.Second
