//go:build !unix

package sdk

import "errors"

// freeBytes is not measured off Unix (Windows is out of scope for the MVP); the installer
// treats an error as "free space unknown" and goes on, with a warning.
func freeBytes(string) (uint64, error) {
	return 0, errors.New("free space is not measured on this platform")
}
