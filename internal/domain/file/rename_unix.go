//go:build darwin || linux

package file

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// renameSameFile is a plain renameat, for one case only: the target is the source under
// another spelling that the volume folds (case or normalisation), where the no-replace
// rename answers EEXIST because the name "exists" as the source itself. The caller has
// established that with sameEntry; this is the only replacing rename in the package.
func renameSameFile(fromDir *os.File, from string, toDir *os.File, to string) error {
	err := unix.Renameat(int(fromDir.Fd()), from, int(toDir.Fd()), to)
	runtime.KeepAlive(fromDir)
	runtime.KeepAlive(toDir)
	return err
}

// sameEntry reports whether the entries a (in aDir) and b (in bDir) are the same file
// object: an lstat of each on its own directory's descriptor, so nothing is followed and
// no path is resolved a second time, then the device and inode compared.
func sameEntry(aDir *os.File, a string, bDir *os.File, b string) (bool, error) {
	var sa, sb unix.Stat_t
	if err := unix.Fstatat(int(aDir.Fd()), a, &sa, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	if err := unix.Fstatat(int(bDir.Fd()), b, &sb, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	runtime.KeepAlive(aDir)
	runtime.KeepAlive(bDir)
	return sa.Dev == sb.Dev && sa.Ino == sb.Ino, nil
}
