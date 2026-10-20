//go:build race

package testtime

// raceFactor is how much slower a test binary built with -race is taken to run: each
// re-executed droidyard starts the race runtime, and on CI's Linux runner a relayed screenshot
// through two daemons outlived the plain 10 s (the race job after 2t's close).
const raceFactor = 3
