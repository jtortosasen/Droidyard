//go:build !race

package testtime

// raceFactor is 1 without -race; race_on.go says why it is 3 with it.
const raceFactor = 1
