package emulator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// Whether an AVD runs (plan m3f Q7, step 6; ADR-0129 §7, ADR-0131 §2), asked before a
// delete (and, in step 7, an edit). An AVD runs when, in this order:
//
//  1. a device listed by this daemon's adb server answers `emu avd name` with it;
//  2. a process of the user whose executable is the launcher or a qemu-system-* has
//     `-avd <name>` or `@<name>` among its arguments, under any adb server and with none;
//  3. a candidate process (sdk.Candidates) holds a file open inside the AVD's directory.
//     The emulator's lock is such a file (ADR-0131 §2): its name was not observed and is
//     not matched, so a stale lock a crashed emulator left is not a running AVD.
//
// Any doubt counts as running: a process that could not be read, lsof that failed, an adb
// that answered with an error other than being absent.

// runState is the answer: why the AVD is held to run ("" when it is free), the serial when
// this daemon's adb knows one, and whether the reason is a doubt rather than a sighting.
type runState struct {
	Why    string
	Serial string
	Doubt  bool
}

// avdHome is one AVD home that holds the AVD's <name>.ini.
type avdHome struct {
	dir avdDir
	ini string
}

// iniHomes are the homes among avdDirs that hold <name>.ini, one per file (two spellings of
// one directory are one home).
func iniHomes(name string, env []string) []avdHome {
	var out []avdHome
	var seen []os.FileInfo
next:
	for _, d := range avdDirs(env) {
		p := filepath.Join(d.path, name+".ini")
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		for _, s := range seen {
			if os.SameFile(s, fi) {
				continue next
			}
		}
		seen = append(seen, fi)
		out = append(out, avdHome{dir: d, ini: p})
	}
	return out
}

// dataDir is the directory the AVD's files live in, as its .ini names it (path, else
// path.rel) or by convention (<name>.avd beside the .ini).
func (h avdHome) dataDir(name string) string {
	dir := filepath.Join(h.dir.path, name+".avd")
	if b, err := readCapped(h.ini, configCap); err == nil {
		if props, err := sdk.ParseProperties(strings.NewReader(string(b))); err == nil {
			if d := entryDir(props, h.dir.path); d != "" {
				dir = d
			}
		}
	}
	return dir
}

// running asks the three questions for the AVD name, whose files are in dirs. A directory is
// also asked about with its symlinks resolved, since lsof prints the real path (/private/var
// for a /var that is a link).
func (e *Emulator) running(ctx context.Context, name string, dirs []string) runState {
	var doubts []string

	// 1. This daemon's adb.
	if e.adb != nil {
		serial, err := e.runningSerialOf(ctx, name)
		var ce *capability.Error
		switch {
		case err == nil && serial != "":
			return runState{Why: "it is running as " + serial, Serial: serial}
		case err == nil:
		case errors.As(err, &ce) && ce.Code == device.ErrADBNotFound:
			// No adb on this host: this daemon has no listing to ask, and the processes are.
		case ctx.Err() != nil:
			return runState{Why: "the check was cancelled", Doubt: true}
		default:
			doubts = append(doubts, "adb could not be asked: "+err.Error())
		}
	}

	// 2. Any emulator process of the user.
	procs, err := e.procs()
	if err != nil {
		doubts = append(doubts, "the processes could not be listed: "+err.Error())
	}
	for _, p := range procs {
		if p.Unreadable != "" {
			doubts = append(doubts, fmt.Sprintf("process %d could not be read: %s", p.Pid, p.Unreadable))
			continue
		}
		if namesAVD(p, name) {
			return runState{Why: fmt.Sprintf("an emulator process (pid %d) was started with it", p.Pid)}
		}
	}

	// 3. A candidate process with a file open in its directory.
	for _, d := range dirs {
		if real, err := filepath.EvalSymlinks(d); err == nil && real != d {
			dirs = append(dirs, real)
		}
	}
	cands := sdk.Candidates(sdk.Host(e.env).Root, procs)
	if len(cands) > 0 && len(dirs) > 0 {
		pids := make([]int, len(cands))
		for i, c := range cands {
			pids[i] = c.Pid
		}
		files, err := e.open(ctx, pids, dirs)
		switch {
		case err != nil && ctx.Err() != nil:
			return runState{Why: "the check was cancelled", Doubt: true}
		case err != nil:
			doubts = append(doubts, "the files the emulator processes hold open could not be listed: "+err.Error())
		case len(files) > 0:
			return runState{Why: fmt.Sprintf("a process (pid %d) holds a file of the AVD open", files[0].Pid)}
		}
	}

	if len(doubts) > 0 {
		return runState{Why: "it cannot be shown to be stopped: " + doubts[0], Doubt: true}
	}
	return runState{}
}

// namesAVD reports whether p is the launcher or a qemu-system process started for the AVD:
// by its executable's base name (never the kernel's 16-character command name) and
// `-avd <name>` or `@<name>` among its arguments.
func namesAVD(p platform.Proc, name string) bool {
	base := filepath.Base(p.Exe)
	if base != "emulator" && !strings.HasPrefix(base, "qemu-system-") {
		return false
	}
	for i, a := range p.Args {
		if a == "@"+name || (a == "-avd" && i+1 < len(p.Args) && p.Args[i+1] == name) {
			return true
		}
	}
	return false
}
