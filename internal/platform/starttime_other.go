//go:build !darwin && !linux

package platform

import "errors"

// Unix-first (ADR-0002): elsewhere no start time can be read, so no record matches
// and nothing is ever signalled.
func startTime(pid int) (int64, error) {
	return 0, errors.New("process start times are not implemented on this system")
}
