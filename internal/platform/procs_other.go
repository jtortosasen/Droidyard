//go:build !darwin && !linux

package platform

import "errors"

// Unix-first (ADR-0002): elsewhere no process can be read, and the caller counts the
// failure as a doubt.
func systemProcs() procSource {
	fail := errors.New("processes cannot be read on this system")
	return procSource{
		list:  func() ([]int, error) { return nil, fail },
		read:  func(int) (string, []string, error) { return "", nil, fail },
		state: func(int) (procState, error) { return procState{}, fail },
	}
}
