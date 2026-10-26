//go:build !darwin && !linux

package platform

import "errors"

func totalMemory() (uint64, error) {
	return 0, errors.New("the host's memory cannot be read on this system")
}
