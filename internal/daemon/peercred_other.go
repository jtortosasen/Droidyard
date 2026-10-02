//go:build !darwin && !linux

package daemon

import "errors"

// Unix-first (ADR-0002): elsewhere no peer can be verified, so every one is refused.
func peerUIDOf(fd int) (int, error) {
	return -1, errors.New("peer credentials are not implemented on this system")
}
