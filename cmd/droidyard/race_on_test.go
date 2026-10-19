//go:build race

package main

// raceTimeScale stretches the bounds a test puts on a re-executed droidyard process: under
// -race each one starts the race runtime and runs several times slower, and on CI's Linux
// runner a relayed screenshot through two daemons outlived the plain 10 s (the race job after
// 2t's close). It is 1 without -race (race_off_test.go).
const raceTimeScale = 3
