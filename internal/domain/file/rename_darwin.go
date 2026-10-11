package file

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames from, in fromDir, to to, in toDir, and fails with EEXIST when
// the target exists, whatever it is: renamex_np with RENAME_EXCL refuses a file, a
// symlink and an empty folder alike, where rename(2) replaces them (ADR-0053 §3). Both
// names are single components, resolved against the directories' descriptors, so the
// rename touches exactly those two entries and never follows a final symlink. A volume
// that cannot refuse a replacement answers ENOTSUP or EINVAL; the caller fails the item
// and never falls back to a replacing rename.
func renameNoReplace(fromDir *os.File, from string, toDir *os.File, to string) error {
	err := unix.RenameatxNp(int(fromDir.Fd()), from, int(toDir.Fd()), to, unix.RENAME_EXCL)
	runtime.KeepAlive(fromDir)
	runtime.KeepAlive(toDir)
	return err
}
