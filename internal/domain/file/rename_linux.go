package file

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames from, in fromDir, to to, in toDir, and fails with EEXIST when
// the target exists, whatever it is: renameat2 with RENAME_NOREPLACE (ADR-0053 §3). Both
// names are single components, resolved against the directories' descriptors, so the
// rename touches exactly those two entries and never follows a final symlink. A file
// system that cannot refuse a replacement answers EINVAL; the caller fails the item and
// never falls back to a replacing rename. Not run on a Linux machine (plan m2g,
// Unverified).
func renameNoReplace(fromDir *os.File, from string, toDir *os.File, to string) error {
	err := unix.Renameat2(int(fromDir.Fd()), from, int(toDir.Fd()), to, unix.RENAME_NOREPLACE)
	runtime.KeepAlive(fromDir)
	runtime.KeepAlive(toDir)
	return err
}
