//go:build !darwin && !linux

package file

import (
	"errors"
	"os"
)

// Droidyard's file operations need a rename that refuses an existing target, which only
// darwin and linux give (ADR-0053 §3). Elsewhere every rename fails the item, so the
// build stays clean and no replacing rename ever stands in.

func renameNoReplace(fromDir *os.File, from string, toDir *os.File, to string) error {
	return errors.ErrUnsupported
}

func renameSameFile(fromDir *os.File, from string, toDir *os.File, to string) error {
	return errors.ErrUnsupported
}

func sameEntry(aDir *os.File, a string, bDir *os.File, b string) (bool, error) {
	return false, errors.ErrUnsupported
}
