//go:build !race

package main

// raceTimeScale is 1 without -race; race_on_test.go says why it grows with it.
const raceTimeScale = 1
