//go:build !race

package device

import "time"

// followRaceOverhead is the race detector's cost in the follow fixture (plan m1n step 1):
// without -race there is none, so the window's bound stays the 3 s the test has always
// used. See follow_race_on_test.go for the measurement and the count of fake-adb spawns.
const followRaceOverhead = time.Duration(0)
