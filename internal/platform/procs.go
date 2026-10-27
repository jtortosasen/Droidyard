package platform

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// errReadDenied is a reader's answer when the kernel refuses to show a process's executable
// to its own user (Linux: no ptrace read access to /proc/<pid>/exe).
var errReadDenied = errors.New("the kernel does not show this process's executable")

// errNoImage is a reader's answer when a process has no executable left (Linux: readlink of
// /proc/<pid>/exe is ENOENT): it released its address space on the way out, before the
// kernel marks it a zombie. Only an exiting process of the user's looks like this.
var errNoImage = errors.New("the process has no executable: it is exiting")

// Proc is one process of the daemon's own user, as the in-use check of the SDK (plan m3f
// step 5) needs it: its executable's full path and its arguments, never the kernel's
// 16-character command name, which misses a name such as qemu-system-aarch64-headless.
type Proc struct {
	Pid int
	// Exe is the executable's path and Args its argument vector, argv[0] first. Both are
	// empty when Unreadable is set.
	Exe  string
	Args []string
	// Unreadable is why the executable could not be read of a process that is alive,
	// not a zombie and not setuid to another user: a doubt the caller must not read as
	// free (ADR-0129 §8; ADR-0131 §1).
	Unreadable string
}

// procState is what the kernel says of a pid after its read failed.
type procState struct {
	Present bool
	Zombie  bool
	// SavedUID is the saved set-user-id: a setuid process such as login has the user's
	// real uid and another saved one, and the kernel refuses to show its arguments
	// (EINVAL, spike m3f step 1 (f)).
	SavedUID int
}

// procSource is the kernel's side of UserProcesses, split so tests can put a seam between
// the listing and the read of a pid, and answer the re-read.
type procSource struct {
	list  func() ([]int, error)
	read  func(pid int) (exe string, args []string, err error)
	state func(pid int) (procState, error)
}

// UserProcesses lists the processes of the daemon's real uid with their executable and
// arguments. A pid whose read fails is read again for its state (a failed read is
// ambiguous: the kernel gives the same errno for a process that is gone, a zombie and a
// setuid one): one that is absent or a zombie, which runs nothing and holds no file, is
// left out; so is one whose saved uid is not the user's, which cannot run an executable
// of the user's SDK (ADR-0131 §1); every other is kept with Unreadable set. The error is
// only that the listing itself failed.
func UserProcesses() ([]Proc, error) {
	return userProcesses(os.Getuid(), systemProcs())
}

func userProcesses(uid int, src procSource) ([]Proc, error) {
	pids, err := src.list()
	if err != nil {
		return nil, err
	}
	out := make([]Proc, 0, len(pids))
	for _, pid := range pids {
		exe, args, err := src.read(pid)
		if err == nil {
			out = append(out, Proc{Pid: pid, Exe: exe, Args: args})
			continue
		}
		if errors.Is(err, errReadDenied) {
			continue // the kernel will not show it: not a process running the user's SDK (ADR-0132)
		}
		if errors.Is(err, errNoImage) {
			continue // exiting: it runs nothing; seen on main's CI, its status not yet a zombie
		}
		st, serr := src.state(pid)
		switch {
		case serr != nil:
			out = append(out, Proc{Pid: pid, Unreadable: fmt.Sprintf("%v (and its state: %v)", err, serr)})
		case !st.Present, st.Zombie:
			// Gone between the listing and the read, or a zombie: nothing runs or is held.
		case st.SavedUID != uid:
			// A setuid process of another saved uid (ADR-0131 §1).
		default:
			out = append(out, Proc{Pid: pid, Unreadable: err.Error()})
		}
	}
	return out, nil
}

// processState is the kernel's answer for one pid, for the lsof check's "is this missing
// candidate really gone".
func processState(pid int) (procState, error) {
	return systemProcs().state(pid)
}

// parseStatus reads the state and the saved uid from a Linux /proc/<pid>/status.
func parseStatus(b []byte) (procState, error) {
	st := procState{Present: true, SavedUID: -1}
	sawState := false
	for _, line := range bytes.Split(b, []byte("\n")) {
		switch {
		case bytes.HasPrefix(line, []byte("State:")):
			f := bytes.Fields(line[len("State:"):])
			if len(f) == 0 {
				return procState{}, errors.New("status has an empty State line")
			}
			sawState = true
			st.Zombie = f[0][0] == 'Z'
		case bytes.HasPrefix(line, []byte("Uid:")):
			// Uid: real effective saved filesystem
			f := bytes.Fields(line[len("Uid:"):])
			if len(f) < 3 {
				return procState{}, fmt.Errorf("status has a Uid line of %d fields", len(f))
			}
			n, err := strconv.Atoi(string(f[2]))
			if err != nil {
				return procState{}, fmt.Errorf("status has a saved uid %q", f[2])
			}
			st.SavedUID = n
		}
	}
	if !sawState || st.SavedUID < 0 {
		return procState{}, errors.New("status has no State or no Uid line")
	}
	return st, nil
}

// parseRealUID reads the real uid, the first field of the Uid line of a Linux status file.
func parseRealUID(b []byte) (int, bool) {
	for _, line := range bytes.Split(b, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("Uid:")) {
			f := bytes.Fields(line[len("Uid:"):])
			if len(f) == 0 {
				return 0, false
			}
			n, err := strconv.Atoi(string(f[0]))
			return n, err == nil
		}
	}
	return 0, false
}
