//go:build unix

package sdk

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// freeBytes is the space a process of the user may still write on the file system that
// holds path, or would hold it: the nearest ancestor that exists is measured when path
// does not (ADR-0130 §2).
func freeBytes(path string) (uint64, error) {
	for {
		_, err := os.Stat(path)
		if err == nil {
			break
		}
		parent := filepath.Dir(path)
		if !errors.Is(err, fs.ErrNotExist) || parent == path {
			return 0, err
		}
		path = parent
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
