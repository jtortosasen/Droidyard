package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// waitPid waits for the pid a script wrote.
func waitPid(t *testing.T, file string) int {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(file); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				return n
			}
		}
	}
	t.Fatalf("%s did not appear", file)
	return 0
}
