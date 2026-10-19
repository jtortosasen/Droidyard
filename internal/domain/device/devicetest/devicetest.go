// Package devicetest is a fake adb for tests: the test binary re-executed, answering
// from files in a scenario directory, so that no test finds or runs this machine's adb
// or reaches the adb server on 5037 (plan m1e, step 3). Only tests import it.
//
// A test calls Main first in TestMain, writes the scenario's files (Write) and gives
// the daemon or the runner the environment Env returns, in which ADB names the test
// binary. The scenario directory holds:
//
//	devices.out, devices.err    what `devices -l` prints to stdout and stderr
//	devices.exit                its exit status (default 0)
//	devices.hang                if present, `devices -l` never answers
//	devices.delay               milliseconds `devices -l` sleeps before its answer
//	<serial>.getprop            `-s <serial> shell getprop`; <serial>.getprop.hang hangs
//	<serial>.avd                `-s <serial> emu avd name`;  <serial>.avd.hang hangs
//	version.hang                if present, `adb version` never answers
//	calls.log                   one line per invocation: its arguments
//	events.log                  "begin <prefix>" and "end <prefix>" around each command below
//
// Commands answered from files named <prefix>.out (stdout), <prefix>.err (stderr),
// <prefix>.exit (status, default 0), <prefix>.delay (milliseconds slept before the
// answer) and <prefix>.hang (never answers); <prefix>#N.<ext> replaces any of them for
// the Nth identical invocation (counted from 1), so a scenario can fail once:
//
//	<serial>.bootcompleted      `-s <serial> shell getprop sys.boot_completed`
//	<serial>.dumptty            `-s <serial> exec-out uiautomator dump /dev/tty`
//	<serial>.dumpsdcard         `-s <serial> exec-out uiautomator dump /sdcard/window_dump.xml`
//	<serial>.catsdcard          `-s <serial> exec-out cat /sdcard/window_dump.xml`
//	<serial>.rmsdcard           `-s <serial> shell rm -f /sdcard/window_dump.xml`
//	<serial>.screencap          `-s <serial> exec-out screencap -p` (.out holds raw bytes: a PNG)
//	<serial>.push               `-s <serial> push <local> /data/local/tmp/droidyard-reader-*.dex` (persistent or one-shot)
//	<serial>.reader             `-s <serial> exec-out CLASSPATH=<that file> app_process /system/bin DroidyardReader`
//	<serial>.rmreader           `-s <serial> shell rm -f <that file>`
//	<serial>.forward            `-s <serial> forward localfilesystem:<path> localabstract:droidyard-reader-<d12>` (.out holds the reader's port)
//	<serial>.forwardremove      `-s <serial> forward --remove localfilesystem:<path>`
//	<serial>.forwardlist        `-s <serial> forward --list` (see below)
//	<serial>.stat               `-s <serial> shell stat -c %s <reader file>` (answers the fake device file's size, or fails as a missing file does, unless scripted)
//	<serial>.start              `-s <serial> shell "CLASSPATH=<dex> nohup app_process /system/bin --nice-name=… DroidyardReader serve …"`
//	<serial>.pidof              `-s <serial> shell pidof droidyard-reader-…`
//	<serial>.kill               `-s <serial> shell kill <pid>`
//	<serial>.ps                 `-s <serial> shell ps -A -o PID,NAME` (.out holds the listing)
//	<serial>.inputtap           `-s <serial> shell input tap <x> <y>`
//	<serial>.date               `-s <serial> shell date +%s.%N`
//	<serial>.monkey             `-s <serial> shell monkey --pct-syskeys 0 -p <id> -c android.intent.category.LAUNCHER 1`
//	<serial>.apppidof           `-s <serial> shell pidof <applicationId>`
//	<serial>.sdk                `-s <serial> shell getprop ro.build.version.sdk`
//	<serial>.inputtext          `-s <serial> shell rm -f <pidfile>; echo "$$ <nonce>" > <pidfile>; exec input text <arg>`
//	<serial>.inputkeyevent      `-s <serial> shell rm -f <pidfile>; echo "$$ <nonce>" > <pidfile>; exec input keyevent <codes…>`
//	<serial>.inputkeycombination `-s <serial> shell rm -f <pidfile>; echo "$$ <nonce>" > <pidfile>; exec input keycombination <codes…>`
//	<serial>.inputprobe         `-s <serial> shell <the running-probe for the issued nonce>` (the device-side input-command probe; .out holds its answer token: running, gone or unknown)
//	<serial>.inputmethod        `-s <serial> shell settings get secure default_input_method`
//	<serial>.dumpsyswindow      `-s <serial> shell dumpsys window` (the busy path's focus read)
//	<serial>.broadcast          `-s <serial> shell rm -f <pidfile>; echo "$$ <nonce>" > <pidfile>; exec am broadcast -a ADB_INPUT_B64 --es msg <base64>`
//	<serial>.install            `-s <serial> install -r <apk>` (the file's bytes are copied to <serial>.installed)
//	<serial>.uninstall          `-s <serial> uninstall <applicationId>`
//	<serial>.pmpath             `-s <serial> shell pm path <applicationId>`
//	<serial>.pm                 `-s <serial> shell pm list packages -U <filter>` (see below)
//	<serial>.log                `-s <serial> logcat …` (see below)
//	<serial>.pushscrcpy         `-s <serial> push <local> /data/local/tmp/droidyard-scrcpy-<d12>.jar` (the live view's server)
//	<serial>.forwardscrcpy      `-s <serial> forward localfilesystem:<path> localabstract:scrcpy_<scid>` (.out holds the listener's socket path, see below; with a non-zero .exit and a .made file it makes the forward and then fails)
//	<serial>.scrcpyserver       `-s <serial> shell CLASSPATH=<that file> app_process / com.genymobile.scrcpy.Server <version> …` (see below)
//
// The fake models the live view's server (plan m2p step 3). A push creates the jar on the
// fake device and copies its bytes to `<serial>.scrcpypushed`. A forward makes the socket
// file a symlink to the Unix socket in `<serial>.forwardscrcpy.out` (a test's own listener
// serving the video and control sockets; there is no proxy, since each invocation is a
// process of its own) and records the forward in `forwards.list`, so `forward --remove
// localfilesystem:<path>` and `forward --list` see it. The shell command is the long-lived child: with the jar missing it exits 1; else
// it deletes the jar, as the real server does as it starts, appends its pid to
// `<serial>.scrcpy.pids` and runs until it is killed or `<serial>.scrcpy.stop` exists,
// when it exits with the status that file holds. `<serial>.scrcpyserver.exit` (non-zero),
// `.err`, `.delay` and `.hang` script a start that fails, as for any served command.
//
// The fake models the device's log (§D20): `<serial>.log` holds logcat text with the
// `--------- beginning of <buffer>` markers, and a logcat command reads only the buffers
// it names, from the epoch it is given, merging them in time order as the device's own
// logcat does and keeping at most `-t` lines (the newest), printing the marker of each
// buffer it emits. Any `-v` other than `threadtime,epoch,usec,uid`
// exits 1, so a change to the daemon's format fails a test; `<serial>.nouid` makes the
// output drop the uid column, as a device that ignores the format element does.
// `<serial>.logcat.hang`, `.delay`, `.exit` and `.err` script its outcome.
//
// A logcat with no `-d` is a follow: the fake streams the log's lines as they become due
// — the backlog from `-T` at once, then each line once the device clock (`<serial>.date.out`,
// or `-T` when it is absent) has reached its epoch — and blocks until it is killed, as the
// daemon ends the window by killing the client's pid. It writes its pid to
// `<serial>.follow.pid`, so a test can tell the window's client is gone afterwards. A `-T`
// of digits only is a count, as logcat's is, not an epoch: the newest N lines already due
// are printed at once, then the follow continues; without `<serial>.date.out` every line
// of the log is due when it is read, so a line a test appends is printed at the next
// turn. A test ends a follow by itself, partway through, as a logcat does when
// its device goes, by writing the status to `<serial>.follow.exitcode`: the follow exits
// with it at its next turn.
//
// The fake models `pm list packages -U <filter>` as a table: `<serial>.pm.out` holds one
// `package:<name> uid:<n>` line per installed package and the command prints the lines
// whose package contains the filter, as real pm's substring match does, so a caller must
// take the exact package it asked for; without a filter it prints every line. `<serial>.pm.hang`,
// `.delay`, `.exit` and `.err` script its outcome, and `<serial>.pm#N.<ext>` the Nth call's;
// `<serial>.pm.pids` records the pid of each call.
//
// The fake models the host's forwards in `forwards.list`, one `<serial>
// localfilesystem:<path> <remote>` line per forward (ADR-0102). A `forward
// localfilesystem:<path> localabstract:<remote>` listens on a real Unix socket at <path>
// and records the line, so a client reaches the reader at `<serial>.forward.out` (its port)
// through it; `forward --remove localfilesystem:<path>` drops the line and stops the
// listener but, as adb does, leaves the file, and `forward --list` prints the table (with
// or without `-s`). So a removed forward refuses a dial, as real adb does (plan m1f
// step 7). A test seeds a
// forward another daemon or another digest made with AddForward, and drops one as an adb
// server restart would with ForgetForward.
//
// The fake models the device's processes too: a `kill <pid>` records the pid in
// `killed.pids`, and `pidof` and `ps` drop a recorded pid from their answer. So a test
// sees the old pid gone after a kill, as plan m1f step 7's wait requires.
//
// The fake models the emulator console's kill too (plan m1i, D20): `-s <serial> emu kill`
// records the serial in `killed.emulators`, and every later `devices -l` drops a recorded
// serial once its leave count is spent, so a test sees the serial leave as a real `emu
// kill` does. `<serial>.emukill.leave` is how many `devices -l` answers still list the
// serial before it leaves (default 0: the next one); `<serial>.emukill.stays` makes it
// never leave, for the stop-timeout case.
//
// The fake models a booting emulator through the table a fake launcher shares with it
// (plan m1i, D20): RegisterEmulator records a booted serial, `devices -l` lists it beside
// devices.out, `emu avd name` answers from it, and `sys.boot_completed` answers 0 for the
// AVD's `<avd>.bootpolls` polls (default 0: the first answers 1) and then 1, so a test
// drives a boot to readiness. A serial a recorded kill has taken away answers as real adb
// does ("error: device '<serial>' not found"), so a boot waiting on it sees it disconnect.
// A fake launcher (internal/domain/emulator/emulatortest) registers into this table.
//
// A FakeReader (reader.go) is the TCP listener the fake forward points at, so a test of
// the persistent reader writes its Port as <serial>.forward.out.
//
// A push is logged in calls.log with its local path replaced by "LOCAL" (it is a
// temporary file), and the bytes pushed are copied to <serial>.pushed, so a test can
// compare them with what it expected.
//
// The fake models the device's files: a push that exits 0 creates the remote path, `shell
// rm -f <reader file>` removes it, and a serve start whose CLASSPATH= file is missing
// exits 1, as does one whose file is 0 bytes long (a dex a restart truncated). So a dex a
// fallback removed cannot be started from, and a test sees it. SetDeviceFile and
// RemoveDeviceFile put the device in the state a restart or a wipe leaves; `shell stat -c
// %s <reader file>` answers from the same files.
//
// `adb version` answers as client 1.0.41; any other invocation exits 9.
//
// Two ways to use it, one scenario format: Main/Env make the test binary itself the
// adb, for tests whose subject is real process behaviour; Exec answers in process,
// through device.NewADBWithExec, for everything else.
package devicetest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitDelay sleeps the prefix's .delay milliseconds, honouring ctx; true when the whole
// wait passed.
func waitDelay(ctx context.Context, dir, prefix string) bool {
	b, err := os.ReadFile(filepath.Join(dir, prefix+".delay"))
	if err != nil {
		return true
	}
	ms, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return wait(ctx, time.Duration(ms)*time.Millisecond)
}

// DirEnv names the scenario directory; its presence makes the binary a fake adb.
const DirEnv = "DROIDYARD_FAKE_ADB_DIR"

// The wrappers the daemon's device input commands and its running-probe carry
// (internal/domain/device/input.go). Every input command runs as `rm -f <pidfile>; echo
// "$$ <nonce>" > <pidfile>; exec <command>` and the probe is built for that command's
// nonce, so both are matched by shape — the nonce varies per issued command. The fake
// recognizes those shapes, so a change to the daemon's strings makes the fake answer
// "unexpected arguments" and fails the test instead of silently serving the old shape
// (plan m1m close, round 3).
var (
	inputWrappedRE = regexp.MustCompile(`^rm -f /data/local/tmp/droidyard-input\.pid; echo "\$\$ ([0-9a-f]{12})" > /data/local/tmp/droidyard-input\.pid; exec (.*)$`)
	inputProbeRE   = regexp.MustCompile(`^set -- \$\(cat /data/local/tmp/droidyard-input\.pid 2>/dev/null\); if \[ "\$2" != ([0-9a-f]{12}) \]; then echo droidyard-input-unknown; elif \[ -d /proc/\$1 \]; then echo droidyard-input-running; else echo droidyard-input-gone; fi$`)
)

// Main runs the fake and exits if the environment names a scenario, and returns at
// once otherwise: call it first in TestMain.
func Main() {
	dir := os.Getenv(DirEnv)
	if dir == "" {
		return
	}
	os.Exit(run(context.Background(), dir, os.Args[1:], os.Stdout, os.Stderr))
}

// Env is the environment that makes the fake the resolved adb. port is the adb server
// port every client and version probe is given: a port nothing listens on, never 5037.
func Env(t *testing.T, dir, port string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{"ADB=" + exe, DirEnv + "=" + dir, "ANDROID_ADB_SERVER_PORT=" + port,
		"HOME=" + t.TempDir(), "PATH="}
}

// Write puts a file in the scenario directory, whole: it is written beside and renamed into
// place, so a fake child polling for it never reads it created but still empty (a stop file
// read empty made a child exit 0, not with its status: plan m2t step 10).
func Write(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path+".part", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".part", path); err != nil {
		t.Fatal(err)
	}
}

// Calls returns the argument lists the fake was invoked with, in order.
func Calls(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// logMu orders the calls.log append and the count of identical invocations, so that
// concurrent in-process clients each get their own place among them. A re-executed
// fake is one process per call and takes it uncontended.
var logMu sync.Mutex

// fwdMu orders the forward table's reads and writes, so concurrent in-process clients of
// two devices do not lose an entry.
var fwdMu sync.Mutex

// Exec returns the fake as an in-process adb client for device.NewADBWithExec: the
// same scenario files and the same answers as the re-executed binary, with no process
// start. It satisfies device.Exec (this package cannot import device). A hang waits
// for ctx, as a client that is killed when its context ends.
func Exec(dir string) func(ctx context.Context, path string, args []string) (stdout, stderr []byte, exitCode int, err error) {
	return func(ctx context.Context, _ string, args []string) ([]byte, []byte, int, error) {
		var so, se bytes.Buffer
		code := run(ctx, dir, args, &so, &se)
		return so.Bytes(), se.Bytes(), code, nil
	}
}

// wait sleeps d, or until ctx ends; it reports whether the whole time passed.
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func run(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) int {
	raw := args
	if len(args) == 5 && args[0] == "-s" && args[2] == "push" && (isReaderFile(args[4]) || isScrcpyFile(args[4])) {
		args = []string{args[0], args[1], args[2], "LOCAL", args[4]}
	}
	n := logCall(dir, args)
	has := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	cat := func(name string) {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			stdout.Write(b)
		}
	}
	hang := func() int { wait(ctx, time.Minute); return 0 }
	// The readiness signal of a serial a fake launcher registered: 0 until the AVD's
	// <avd>.bootpolls-th poll, then 1; a serial whose `emu kill` has taken it away answers
	// the not-found error real adb prints, so a boot waiting on an adopted device sees it
	// disconnect (plan m1i, D20; D12). A test's own <serial>.bootcompleted files win.
	if len(args) == 5 && args[0] == "-s" && args[2] == "shell" && args[3] == "getprop" && args[4] == "sys.boot_completed" {
		if _, ok := tableAVD(dir, args[1]); ok && !has(args[1]+".bootcompleted.out") {
			if serialGone(dir, args[1]) {
				fmt.Fprintf(stderr, "error: device '%s' not found\n", args[1])
				return 1
			}
			// A booting emulator is listed offline first, and getprop then fails with
			// "device offline" (spike S8 G11): <avd>.offlinepolls polls do that.
			if offlineFromTable(dir, args[1]) {
				fmt.Fprintln(stderr, "error: device offline")
				return 1
			}
			if bootCompletedFromTable(dir, args[1]) {
				fmt.Fprintln(stdout, "1")
			} else {
				fmt.Fprintln(stdout, "0")
			}
			return 0
		}
	}
	// The probe's meaning, not only its shape (m1m close review): the fake remembers the
	// nonce of the last wrapped input command per serial, as the device's pidfile would,
	// and a probe carrying any other nonce answers unknown whatever the scenario says, so
	// a probe built for the wrong command can never read "gone" in a test.
	if len(args) == 4 && args[0] == "-s" && args[2] == "shell" {
		if m := inputWrappedRE.FindStringSubmatch(args[3]); m != nil {
			os.WriteFile(filepath.Join(dir, args[1]+".lastnonce"), []byte(m[1]), 0o600)
		} else if m := inputProbeRE.FindStringSubmatch(args[3]); m != nil {
			if last, _ := os.ReadFile(filepath.Join(dir, args[1]+".lastnonce")); string(last) != m[1] {
				fmt.Fprintln(stdout, "droidyard-input-unknown")
				return 0
			}
		}
	}
	if prefix := servedPrefix(args); prefix != "" {
		return serve(ctx, dir, prefix, n, raw, stdout, stderr)
	}
	switch {
	case len(args) == 1 && args[0] == "version":
		if has("version.hang") {
			return hang()
		}
		fmt.Fprint(stdout, "Android Debug Bridge version 1.0.41\nVersion 36.0.0-13206524\nInstalled as /fake/adb\n")
		return 0
	case len(args) == 2 && args[0] == "devices" && args[1] == "-l":
		if has("devices.hang") {
			return hang()
		}
		if !waitDelay(ctx, dir, "devices") {
			return 0
		}
		// A registered emulator (one a fake launcher booted) is listed beside whatever
		// devices.out holds, and both lose a serial the fake `emu kill` ended (plan m1i,
		// D20).
		stdout.Write(dropKilledEmulators(dir, appendRegisteredEmulators(dir, dirBytes(dir, "devices.out"))))
		if b, err := os.ReadFile(filepath.Join(dir, "devices.err")); err == nil {
			stderr.Write(b)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "devices.exit")); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			return n
		}
		return 0
	case len(args) == 4 && args[0] == "-s" && args[2] == "shell" && args[3] == "getprop":
		if has(args[1] + ".getprop.hang") {
			return hang()
		}
		cat(args[1] + ".getprop")
		return 0
	case len(args) == 5 && args[0] == "-s" && args[2] == "emu" && args[3] == "avd" && args[4] == "name":
		if has(args[1] + ".avd.hang") {
			return hang()
		}
		if avd, ok := tableAVD(dir, args[1]); ok {
			fmt.Fprint(stdout, avd+"\r\nOK\r\n")
			return 0
		}
		cat(args[1] + ".avd")
		return 0
	case len(args) == 2 && args[0] == "forward" && args[1] == "--list":
		// The host's whole table, as adb prints it without a serial; `forwardlist.exit` scripts
		// its failure.
		if b, err := os.ReadFile(filepath.Join(dir, "forwardlist.exit")); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			return n
		}
		writeForwardList(dir, stdout)
		return 0
	case len(args) == 4 && args[0] == "-s" && args[2] == "forward" && args[3] == "--list":
		if has(args[1] + ".forwardlist.hang") {
			return hang()
		}
		if b, err := os.ReadFile(filepath.Join(dir, args[1]+".forwardlist.exit")); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			cat(args[1] + ".forwardlist.err")
			return n
		}
		if b, err := os.ReadFile(filepath.Join(dir, args[1]+".forwardlist.out")); err == nil {
			stdout.Write(b) // a test can script the listing, e.g. over the output cap
			return 0
		}
		writeForwardList(dir, stdout)
		return 0
	case len(args) == 8 && args[0] == "-s" && args[2] == "shell" && args[3] == "pm" && args[4] == "list" && args[5] == "packages" && args[6] == "-U":
		return runPM(ctx, dir, args[1], args[7], n, stdout, stderr)
	case len(args) == 7 && args[0] == "-s" && args[2] == "shell" && args[3] == "pm" && args[4] == "list" && args[5] == "packages" && args[6] == "-U":
		return runPM(ctx, dir, args[1], "", n, stdout, stderr)
	case len(args) >= 4 && args[0] == "-s" && args[2] == "logcat":
		return runLogcat(ctx, dir, args[1], args[3:], stdout, stderr)
	}
	fmt.Fprintln(stderr, "fake adb: unexpected arguments:", strings.Join(args, " "))
	return 9
}

// logCall appends args to calls.log and returns this invocation's place among the
// identical ones, counted from 1.
func logCall(dir string, args []string) int {
	logMu.Lock()
	defer logMu.Unlock()
	line := strings.Join(args, " ")
	if f, err := os.OpenFile(filepath.Join(dir, "calls.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
	key := inputCallKey(line)
	n := 0
	if b, err := os.ReadFile(filepath.Join(dir, "calls.log")); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if inputCallKey(l) == key {
				n++
			}
		}
	}
	return n
}

// inputCallKey is the key the fake counts identical invocations by: a wrapped device input
// command's nonce, and a running-probe's nonce, are dropped, so the Nth invocation of the
// same device command counts as n for the scenario files' #N suffix even though every
// command carries a fresh nonce (plan m1m close, round 3). Any other line is its own key,
// so every other command counts exactly as before.
func inputCallKey(line string) string {
	const sep = " shell "
	i := strings.Index(line, sep)
	if i < 0 {
		return line
	}
	head, script := line[:i+len(sep)], line[i+len(sep):]
	if inputProbeRE.MatchString(script) {
		return head + "input-probe"
	}
	if m := inputWrappedRE.FindStringSubmatch(script); m != nil {
		return head + "input-wrapped " + m[2]
	}
	return line
}

// servedPrefix names the scenario files that answer args, "" if they are not one of
// the commands served from them.
func servedPrefix(args []string) string {
	if len(args) < 4 || args[0] != "-s" {
		return ""
	}
	// The device input commands and the running-probe arrive as one wrapped shell string.
	if len(args) == 4 && args[2] == "shell" {
		if p := inputPrefix(args[1], args[3]); p != "" {
			return p
		}
	}
	switch {
	case len(args) == 4 && args[2] == "emu" && args[3] == "kill":
		return args[1] + ".emukill"
	case len(args) == 5 && args[2] == "push" && args[3] == "LOCAL" && isReaderFile(args[4]):
		return args[1] + ".push"
	case len(args) == 5 && args[2] == "push" && args[3] == "LOCAL" && isScrcpyFile(args[4]):
		return args[1] + ".pushscrcpy"
	case len(args) == 5 && args[2] == "forward" && strings.HasPrefix(args[3], "localfilesystem:") && strings.HasPrefix(args[4], "localabstract:scrcpy_"):
		return args[1] + ".forwardscrcpy"
	case len(args) >= 8 && args[2] == "shell" && isScrcpyClasspath(args[3]) && args[4] == "app_process" && args[5] == "/" && args[6] == "com.genymobile.scrcpy.Server":
		return args[1] + ".scrcpyserver"
	case len(args) == 7 && args[2] == "exec-out" && isReaderClasspath(args[3]) && args[4] == "app_process" && args[5] == "/system/bin" && args[6] == "DroidyardReader":
		return args[1] + ".reader"
	case len(args) == 6 && args[2] == "shell" && args[3] == "rm" && args[4] == "-f" && isReaderFile(args[5]):
		return args[1] + ".rmreader"
	case len(args) == 5 && args[2] == "forward" && strings.HasPrefix(args[3], "localfilesystem:") && strings.HasPrefix(args[4], "localabstract:droidyard-reader-"):
		return args[1] + ".forward"
	case len(args) == 5 && args[2] == "forward" && args[3] == "--remove" && strings.HasPrefix(args[4], "localfilesystem:"):
		return args[1] + ".forwardremove"
	case len(args) == 4 && args[2] == "shell" && isReaderServe(args[3]):
		return args[1] + ".start"
	case len(args) == 7 && args[2] == "shell" && args[3] == "stat" && args[4] == "-c" && args[5] == "%s" && isReaderFile(args[6]):
		return args[1] + ".stat"
	case len(args) == 5 && args[2] == "shell" && args[3] == "pidof" && strings.HasPrefix(args[4], "droidyard-reader-"):
		return args[1] + ".pidof"
	case len(args) == 5 && args[2] == "shell" && args[3] == "pidof":
		return args[1] + ".apppidof"
	case len(args) == 5 && args[2] == "shell" && args[3] == "kill":
		return args[1] + ".kill"
	case len(args) == 7 && args[2] == "shell" && args[3] == "ps" && args[4] == "-A" && args[5] == "-o" && args[6] == "PID,NAME":
		return args[1] + ".ps"
	case len(args) == 7 && args[2] == "shell" && args[3] == "input" && args[4] == "tap":
		return args[1] + ".inputtap"
	case len(args) == 5 && args[2] == "shell" && args[3] == "date" && (args[4] == "+%s.%N" || args[4] == "+%s.%N%z"):
		// `+%s.%N` is the launch/logs clock; `+%s.%N%z` also carries the device's zone, which
		// the crash path needs to compare exit-info's device-local timestamps (plan m1n step 6).
		return args[1] + ".date"
	case len(args) == 11 && args[2] == "shell" && args[3] == "monkey" && args[4] == "--pct-syskeys" && args[5] == "0" && args[6] == "-p" && args[8] == "-c" && args[9] == "android.intent.category.LAUNCHER" && args[10] == "1":
		return args[1] + ".monkey"
	case len(args) == 5 && args[2] == "shell" && args[3] == "getprop" && args[4] == "ro.build.version.sdk":
		return args[1] + ".sdk"
	case len(args) == 5 && args[2] == "shell" && args[3] == "dumpsys" && args[4] == "window":
		return args[1] + ".dumpsyswindow"
	case len(args) == 7 && args[2] == "shell" && args[3] == "dumpsys" && args[4] == "activity" && args[5] == "exit-info":
		// The crash path's fallback: the exit reason of the app's last process (plan m1n
		// step 6, ADR-0034 §2).
		return args[1] + ".exitinfo"
	case len(args) == 6 && args[2] == "shell" && args[3] == "dumpsys" && args[4] == "window" && args[5] == "windows":
		// The soft-keyboard guard's IME touchable region (plan m1n step 5, fix round L1).
		return args[1] + ".dumpsyswindowwindows"
	case len(args) == 5 && args[2] == "shell" && args[3] == "dumpsys" && args[4] == "input_method":
		// The soft-keyboard guard's shown/not-shown read (plan m1n step 5, fix round L1).
		return args[1] + ".dumpsysinputmethod"
	case len(args) == 5 && args[2] == "install" && args[3] == "-r":
		return args[1] + ".install"
	case len(args) == 4 && args[2] == "uninstall":
		return args[1] + ".uninstall"
	case len(args) == 6 && args[2] == "shell" && args[3] == "pm" && args[4] == "path":
		return args[1] + ".pmpath"
	case len(args) == 7 && args[2] == "shell" && args[3] == "settings" && args[4] == "get" && args[5] == "secure" && args[6] == "default_input_method":
		return args[1] + ".inputmethod"
	}
	switch strings.Join(args[2:], " ") {
	case "shell getprop sys.boot_completed":
		return args[1] + ".bootcompleted"
	case "exec-out uiautomator dump /dev/tty":
		return args[1] + ".dumptty"
	case "exec-out uiautomator dump /sdcard/window_dump.xml":
		return args[1] + ".dumpsdcard"
	case "exec-out cat /sdcard/window_dump.xml":
		return args[1] + ".catsdcard"
	case "shell rm -f /sdcard/window_dump.xml":
		return args[1] + ".rmsdcard"
	case "exec-out screencap -p":
		return args[1] + ".screencap"
	}
	return ""
}

func isReaderFile(p string) bool {
	return strings.HasPrefix(p, "/data/local/tmp/droidyard-reader-") && strings.HasSuffix(p, ".dex")
}

func isScrcpyFile(p string) bool {
	return strings.HasPrefix(p, "/data/local/tmp/droidyard-scrcpy-") && strings.HasSuffix(p, ".jar")
}

func isScrcpyClasspath(a string) bool {
	return strings.HasPrefix(a, "CLASSPATH=") && isScrcpyFile(strings.TrimPrefix(a, "CLASSPATH="))
}

func isReaderClasspath(a string) bool {
	return strings.HasPrefix(a, "CLASSPATH=") && isReaderFile(strings.TrimPrefix(a, "CLASSPATH="))
}

// inputPrefix classifies a wrapped device input command or the running-probe: the scenario
// file prefix to serve it from, or "" when it is neither. The daemon wraps every input
// command as `rm -f <pidfile>; echo "$$ <nonce>" > <pidfile>; exec <command>` and probes
// with one command built for that command's nonce (internal/domain/device/input.go); the
// fake recognizes those shapes, so a change to the daemon's strings makes the fake answer
// "unexpected arguments" and fails the test.
func inputPrefix(serial, script string) string {
	if inputProbeRE.MatchString(script) {
		return serial + ".inputprobe"
	}
	m := inputWrappedRE.FindStringSubmatch(script)
	if m == nil {
		return ""
	}
	switch inner := m[2]; {
	case strings.HasPrefix(inner, "input text "):
		return serial + ".inputtext"
	case strings.HasPrefix(inner, "input keycombination "):
		return serial + ".inputkeycombination"
	case strings.HasPrefix(inner, "input keyevent "):
		return serial + ".inputkeyevent"
	case strings.HasPrefix(inner, "am broadcast "):
		return serial + ".broadcast"
	}
	return ""
}

// isReaderServe recognizes the persistent reader's start script (plan m1f step 1):
// `CLASSPATH=<dex> nohup app_process /system/bin --nice-name=droidyard-reader-<d12>
// DroidyardReader serve droidyard-reader-<d12> <idleSeconds>`.
func isReaderServe(script string) bool {
	return strings.Contains(script, "DroidyardReader serve ") &&
		strings.Contains(script, "app_process /system/bin --nice-name="+readerName) &&
		strings.Contains(script, "CLASSPATH=")
}

// readerName is shared with the daemon's reader client but the fake cannot import it.
const readerName = "droidyard-reader-"

// serve answers one invocation, the nth of its identical ones, from its prefix's
// files, and brackets it in events.log so a test can see which invocations
// overlapped. raw is the invocation before any LOCAL replacement, so a push's local
// file and a command's remote path are still visible. A cancelled ctx ends it where a
// kill would: no "end", no more output.
func serve(ctx context.Context, dir, prefix string, n int, raw []string, stdout, stderr io.Writer) int {
	has := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	event := func(what string) {
		logMu.Lock()
		defer logMu.Unlock()
		if f, err := os.OpenFile(filepath.Join(dir, "events.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
			fmt.Fprintln(f, what, prefix)
			f.Close()
		}
	}
	name := prefix
	if v := prefix + "#" + strconv.Itoa(n); has(v+".out") || has(v+".err") || has(v+".exit") || has(v+".delay") || has(v+".hang") {
		name = v
	}
	event("begin")
	if has(name + ".hang") {
		wait(ctx, time.Minute)
		return 0
	}
	if b, err := os.ReadFile(filepath.Join(dir, name+".delay")); err == nil {
		ms, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if !wait(ctx, time.Duration(ms)*time.Millisecond) {
			return 0
		}
	}
	code := 0
	if b, err := os.ReadFile(filepath.Join(dir, name+".exit")); err == nil {
		code, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	// The live view's server runs until it is killed or told to stop; one that is scripted
	// to fail (a non-zero .exit) takes the generic answer below.
	if strings.HasSuffix(prefix, ".scrcpyserver") && code == 0 && len(raw) >= 8 {
		event("end")
		return serveScrcpyServer(ctx, dir, raw, stderr)
	}
	// A stat of the reader's dex answers from the fake device's files unless the scenario
	// scripts it: the size, or the failure of a file that is not there.
	if strings.HasSuffix(prefix, ".stat") && code == 0 && len(raw) == 7 && !has(name+".out") {
		if b, ok := readDeviceFile(dir, raw[6]); ok {
			fmt.Fprintln(stdout, len(b))
			event("end")
			return 0
		}
		fmt.Fprintf(stderr, "stat: '%s': No such file or directory\n", raw[6])
		event("end")
		return 1
	}
	// A successful forward makes the socket file the client named and listens on it, relaying
	// to the reader at <serial>.forward.out (its port), so a later dial reaches the reader
	// through it and a removed forward refuses the dial (plan m1f step 7; ADR-0102). A
	// forward that failed falls through to the generic answer below.
	if strings.HasSuffix(prefix, ".forward") && code == 0 && len(raw) == 5 {
		sock := strings.TrimPrefix(raw[3], "localfilesystem:")
		if err := startForwardProxy(strings.TrimSpace(dirString(dir, name+".out")), sock); err == nil {
			addForward(dir, raw[1], sock, raw[4])
			event("end")
			return 0
		} else {
			fmt.Fprintln(stderr, "fake adb: "+err.Error())
			event("end")
			return 1
		}
	}
	// A forward cut short: <serial>.forwardscrcpy.made makes the forward and then fails with
	// the scripted status, as an adb client killed after its server made the forward does.
	if strings.HasSuffix(prefix, ".forwardscrcpy") && code != 0 && has(name+".made") && len(raw) == 5 {
		sock := strings.TrimPrefix(raw[3], "localfilesystem:")
		if err := linkForward(strings.TrimSpace(dirString(dir, name+".out")), sock); err == nil {
			addForward(dir, raw[1], sock, raw[4])
		}
	}
	// The live view's forward is a symlink at the socket file to the test's own listener
	// (<serial>.forwardscrcpy.out holds that listener's socket path): the fake is one
	// process per call here, so nothing of its own outlives it, and a connect follows the
	// link as it would a socket.
	if strings.HasSuffix(prefix, ".forwardscrcpy") && code == 0 && len(raw) == 5 {
		sock := strings.TrimPrefix(raw[3], "localfilesystem:")
		if err := linkForward(strings.TrimSpace(dirString(dir, name+".out")), sock); err != nil {
			fmt.Fprintln(stderr, "fake adb: "+err.Error())
			event("end")
			return 1
		}
		addForward(dir, raw[1], sock, raw[4])
		event("end")
		return 0
	}
	out := dirBytes(dir, name+".out")
	if strings.HasSuffix(prefix, ".pidof") {
		// A killed pid is gone from pidof, so the client's wait sees it (plan m1f
		// step 7). ps is not filtered: a listing a test scripts is what it says.
		out = dropKilledPids(dir, out)
	}
	stdout.Write(out)
	if b, err := os.ReadFile(filepath.Join(dir, name+".err")); err == nil {
		stderr.Write(b)
	}
	event("end")
	return deviceEffect(dir, prefix, name, raw, code, stderr)
}

// dirString reads a scenario file as trimmed text, "" when it is absent.
func dirString(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// dirBytes reads a scenario file, nil when it is absent.
func dirBytes(dir, name string) []byte {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil
	}
	return b
}

// deviceEffect models the device's files, processes and the host's forwards for the
// commands that change them, and returns the exit code the command ends with. A push
// that exited 0 creates its remote path; an rm removes it; a serve start whose
// CLASSPATH= file is missing fails with status 1, the way app_process cannot start a
// class that is not there; a forward (handled in serve) adds its socket to the listing
// and a remove drops that socket from it and stops its listener, leaving the file as adb
// does; a kill records the pid as gone.
func deviceEffect(dir, prefix, name string, raw []string, code int, stderr io.Writer) int {
	switch {
	case strings.HasSuffix(prefix, ".push"):
		if code == 0 && len(raw) == 5 {
			if b, err := os.ReadFile(raw[3]); err == nil {
				os.WriteFile(filepath.Join(dir, raw[1]+".pushed"), b, 0o600)
				writeDeviceFile(dir, raw[4], b)
			}
		}
	case strings.HasSuffix(prefix, ".install"):
		// The file adb reads, whatever the command then answers: its bytes are kept as
		// `<serial>.installed` (the last install's), so a test can compare them with the
		// APK it expected; a path that is not a file leaves nothing.
		if len(raw) == 5 {
			if b, err := os.ReadFile(raw[4]); err == nil {
				os.WriteFile(filepath.Join(dir, raw[1]+".installed"), b, 0o600)
			}
		}
	case strings.HasSuffix(prefix, ".pushscrcpy"):
		if code == 0 && len(raw) == 5 {
			if b, err := os.ReadFile(raw[3]); err == nil {
				os.WriteFile(filepath.Join(dir, raw[1]+".scrcpypushed"), b, 0o600)
				writeDeviceFile(dir, raw[4], b)
			}
		}
	case strings.HasSuffix(prefix, ".rmreader"):
		if code == 0 && len(raw) == 6 {
			removeDeviceFile(dir, raw[5])
		}
	case strings.HasSuffix(prefix, ".start"):
		if len(raw) == 4 {
			if cp := classpathOf(raw[3]); cp != "" {
				b, ok := readDeviceFile(dir, cp)
				if !ok {
					fmt.Fprintf(stderr, "%s: No such file or directory\n", cp)
					return 1
				}
				if len(b) == 0 {
					// app_process cannot load a class from an empty dex.
					fmt.Fprintf(stderr, "%s: Failed to open dex file: zero-length\n", cp)
					return 1
				}
			}
		}
	case strings.HasSuffix(prefix, ".kill"):
		if code == 0 && len(raw) == 5 {
			recordKilled(dir, raw[4])
		}
	case strings.HasSuffix(prefix, ".emukill"):
		if code == 0 && len(raw) == 4 {
			recordKilledEmulator(dir, raw[1])
		}
	case strings.HasSuffix(prefix, ".forwardremove"):
		if code == 0 && len(raw) == 5 {
			removeForwardSocket(dir, strings.TrimPrefix(raw[4], "localfilesystem:"))
		}
	}
	return code
}

// serveScrcpyServer is the fake live-view server (see the package comment): it fails as
// app_process does when the jar is missing, deletes the jar as the real server does as it
// starts, and then runs until it is killed or `<serial>.scrcpy.stop` exists.
func serveScrcpyServer(ctx context.Context, dir string, raw []string, stderr io.Writer) int {
	serial, jar := raw[1], strings.TrimPrefix(raw[3], "CLASSPATH=")
	if _, ok := readDeviceFile(dir, jar); !ok {
		fmt.Fprintf(stderr, "%s: No such file or directory\n", jar)
		return 1
	}
	removeDeviceFile(dir, jar)
	procMu.Lock()
	if f, err := os.OpenFile(filepath.Join(dir, serial+".scrcpy.pids"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		fmt.Fprintln(f, os.Getpid())
		f.Close()
	}
	procMu.Unlock()
	for ctx.Err() == nil {
		if b, err := os.ReadFile(filepath.Join(dir, serial+".scrcpy.stop")); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			return n
		}
		wait(ctx, 5*time.Millisecond)
	}
	return 0
}

// classpathOf reads the CLASSPATH= path out of a serve start script.
func classpathOf(script string) string {
	const p = "CLASSPATH="
	i := strings.Index(script, p)
	if i < 0 {
		return ""
	}
	rest := script[i+len(p):]
	if j := strings.IndexByte(rest, ' '); j >= 0 {
		return rest[:j]
	}
	return rest
}

// deviceFile is the scenario file standing for a remote path on the fake device.
func deviceFile(dir, remote string) string {
	return filepath.Join(dir, "dev"+strings.ReplaceAll(remote, "/", "_"))
}

func writeDeviceFile(dir, remote string, b []byte) {
	os.WriteFile(deviceFile(dir, remote), b, 0o600)
}

func removeDeviceFile(dir, remote string) {
	os.Remove(deviceFile(dir, remote))
}

func readDeviceFile(dir, remote string) ([]byte, bool) {
	b, err := os.ReadFile(deviceFile(dir, remote))
	return b, err == nil
}

// SetDeviceFile makes the fake device hold remote with data, as a restart that left a dex
// 0 bytes long does (nil data).
func SetDeviceFile(t testing.TB, dir, remote string, data []byte) {
	t.Helper()
	if data == nil {
		data = []byte{}
	}
	writeDeviceFile(dir, remote, data)
}

// RemoveDeviceFile removes remote from the fake device, as a wipe does.
func RemoveDeviceFile(t testing.TB, dir, remote string) {
	t.Helper()
	removeDeviceFile(dir, remote)
}

// DeviceFile returns what the fake device holds at remote, and whether it holds anything.
func DeviceFile(t testing.TB, dir, remote string) ([]byte, bool) {
	t.Helper()
	return readDeviceFile(dir, remote)
}

// Events returns events.log's lines: "begin <prefix>" and "end <prefix>", in order.
func Events(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "events.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// The host's forwards live in forwards.list, one `<serial> localfilesystem:<path> <remote>`
// line each, as `adb forward --list` prints them.

// readForwards reads the forward table; callers take fwdMu.
func readForwards(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, "forwards.list"))
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(l); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// writeForwards replaces the forward table; callers take fwdMu.
func writeForwards(dir string, lines []string) {
	if len(lines) == 0 {
		os.Remove(filepath.Join(dir, "forwards.list"))
		return
	}
	os.WriteFile(filepath.Join(dir, "forwards.list"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func writeForwardList(dir string, w io.Writer) {
	fwdMu.Lock()
	defer fwdMu.Unlock()
	for _, line := range readForwards(dir) {
		fmt.Fprintln(w, line)
	}
}

// localSpec is the local side of a forward as the table prints it.
const localSpec = "localfilesystem:"

func addForward(dir, serial, sock, remote string) {
	if serial == "" || sock == "" || remote == "" {
		return
	}
	fwdMu.Lock()
	defer fwdMu.Unlock()
	line := serial + " " + localSpec + sock + " " + remote
	lines := readForwards(dir)
	for _, l := range lines {
		if l == line {
			return
		}
	}
	writeForwards(dir, append(lines, line))
}

// removeForwardSocket drops the forward at sock and stops its listener. The socket file
// stays, as real adb leaves it: the client removes it.
func removeForwardSocket(dir, sock string) {
	fwdMu.Lock()
	var out []string
	for _, l := range readForwards(dir) {
		if _, local, _, ok := splitForward(l); ok && local == localSpec+sock {
			continue
		}
		out = append(out, l)
	}
	writeForwards(dir, out)
	fwdMu.Unlock()
	// The listener stops when its forward is removed: a dial then refuses, as real adb
	// does (plan m1f step 7). A seeded forward has none, a live view's is a link.
	closeForwardProxy(sock)
}

// AddForward seeds a forward at the socket file sock in the fake's `forward --list`, as
// another daemon's or another digest's would be. remote is "localabstract:<name>".
// upstream, when it is not empty, is the port of a reader the forward relays to, so it
// answers a dial; an empty one is only listed.
func AddForward(t testing.TB, dir, serial, sock, upstream, remote string) {
	t.Helper()
	if upstream != "" {
		if err := startForwardProxy(upstream, sock); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closeForwardProxy(sock) })
	}
	addForward(dir, serial, sock, remote)
}

// ForgetForward drops the forward to remote, as an adb server restart drops host
// forwards; the reader behind it is untouched, but its socket stops listening, so a dial
// refuses (plan m1f step 7), and its file stays.
func ForgetForward(t testing.TB, dir, remote string) {
	t.Helper()
	fwdMu.Lock()
	var dropped []string
	var out []string
	for _, l := range readForwards(dir) {
		if _, local, r, ok := splitForward(l); ok && r == remote {
			dropped = append(dropped, strings.TrimPrefix(local, localSpec))
			continue
		}
		out = append(out, l)
	}
	writeForwards(dir, out)
	fwdMu.Unlock()
	for _, sock := range dropped {
		closeForwardProxy(sock)
	}
}

// Forwards returns the fake's forward table, one line per forward.
func Forwards(t testing.TB, dir string) []string {
	t.Helper()
	fwdMu.Lock()
	defer fwdMu.Unlock()
	return readForwards(dir)
}

// ForwardSocket returns the socket file of the first forward to remote, "" when none.
func ForwardSocket(t testing.TB, dir, remote string) string {
	t.Helper()
	fwdMu.Lock()
	defer fwdMu.Unlock()
	for _, l := range readForwards(dir) {
		if _, local, r, ok := splitForward(l); ok && strings.TrimPrefix(r, "localabstract:") == remote {
			return strings.TrimPrefix(local, localSpec)
		}
	}
	return ""
}

// splitForward reads a line of the forward table as adb prints it: the serial up to the
// first space, the remote after the last, and between them the local spec as it is, spaces
// of a path included (`emulator-5554 localfilesystem:/Users/me/Application Support/x
// localabstract:y`).
func splitForward(line string) (serial, local, remote string, ok bool) {
	line = strings.TrimSpace(line)
	i, j := strings.IndexByte(line, ' '), strings.LastIndexByte(line, ' ')
	if i < 0 || j <= i {
		return "", "", "", false
	}
	return line[:i], strings.TrimSpace(line[i+1 : j]), line[j+1:], true
}

// The reader's forwards are real listeners: `forward localfilesystem:<path>` binds one to
// the reader, `forward --remove` and ForgetForward close it. A removed forward therefore
// refuses a dial, driving recovery from the failed read (plan m1f step 7), and the socket
// file stays behind, as real adb leaves it (ADR-0102).

type forwardProxy struct {
	ln       net.Listener
	upstream string // host:port of the reader the forward points at
}

var (
	proxyMu sync.Mutex
	proxies = map[string]*forwardProxy{} // socket path -> proxy
)

// startForwardProxy listens on the socket file sock and relays to upstream (a port or
// host:port). A file already there is replaced, as adb replaces it.
func startForwardProxy(upstream, sock string) error {
	if upstream == "" {
		return fmt.Errorf("no upstream port for the forward")
	}
	if !strings.Contains(upstream, ":") {
		upstream = "127.0.0.1:" + upstream
	}
	os.Remove(sock)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return err
	}
	ln.SetUnlinkOnClose(false)
	p := &forwardProxy{ln: ln, upstream: upstream}
	proxyMu.Lock()
	proxies[sock] = p
	proxyMu.Unlock()
	go p.serve()
	return nil
}

// linkForward makes sock a symlink to the listener at target, replacing a file there.
func linkForward(target, sock string) error {
	if target == "" {
		return fmt.Errorf("no listener for the forward")
	}
	os.Remove(sock)
	return os.Symlink(target, sock)
}

func closeForwardProxy(sock string) {
	proxyMu.Lock()
	p := proxies[sock]
	delete(proxies, sock)
	proxyMu.Unlock()
	if p != nil {
		p.ln.Close()
	}
}

func (p *forwardProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.relay(conn)
	}
}

func (p *forwardProxy) relay(conn net.Conn) {
	defer conn.Close()
	up, err := net.DialTimeout("tcp", p.upstream, 2*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, conn); done <- struct{}{} }()
	go func() { io.Copy(conn, up); done <- struct{}{} }()
	<-done
	// One side ended: close both so the other unblocks and the client sees EOF.
	conn.Close()
	up.Close()
	<-done
}

// killed.pids records the pids a `kill` command ended, so pidof and ps drop them.

var procMu sync.Mutex

func recordKilled(dir, pid string) {
	if pid == "" {
		return
	}
	procMu.Lock()
	defer procMu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "killed.pids"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintln(f, pid)
	f.Close()
}

func killedPids(dir string) map[string]bool {
	procMu.Lock()
	defer procMu.Unlock()
	b, err := os.ReadFile(filepath.Join(dir, "killed.pids"))
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, p := range strings.Fields(string(b)) {
		out[p] = true
	}
	return out
}

// dropKilledPids removes ended pids from a `pidof` answer, so a test sees the old pid
// gone after a kill (plan m1f step 7).
func dropKilledPids(dir string, out []byte) []byte {
	killed := killedPids(dir)
	if len(killed) == 0 {
		return out
	}
	var kept []string
	for _, p := range strings.Fields(string(out)) {
		if !killed[p] {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return []byte(strings.Join(kept, "\n") + "\n")
}

// logcatFormat is the -v value the daemon asks for; the fake answers only this one
// (plan m1i D20), so a change to the daemon's format fails a test.
const logcatFormat = "threadtime,epoch,usec,uid"

// logcatBuffers is logcat's default buffer set, the values a log file may name.
var logcatBuffers = []string{"main", "system", "crash"}

// logUIDRE pulls the uid column out of a line, so a device that ignored the format
// element can be modelled.
var logUIDRE = regexp.MustCompile(`^\s*([0-9]+\.[0-9]+)\s+(\S+)\s+([0-9]+)\s+([0-9]+)\s+([VDIWEF])\s+(.*)$`)

// pmLineRE reads one `pm list packages -U` line's package name.
var pmLineRE = regexp.MustCompile(`^package:(\S+) uid:[0-9]+$`)

// scripted applies <prefix>.hang and .delay, prints <prefix>.err and returns the
// scripted exit status as (code, true) when <prefix>.exit exists.
func scripted(ctx context.Context, dir, prefix string, stderr io.Writer) (int, bool) {
	has := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	if has(prefix + ".hang") {
		wait(ctx, time.Minute)
		return 0, true
	}
	if b, err := os.ReadFile(filepath.Join(dir, prefix+".delay")); err == nil {
		ms, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if !wait(ctx, time.Duration(ms)*time.Millisecond) {
			return 0, true
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, prefix+".err")); err == nil {
		stderr.Write(b)
	}
	if b, err := os.ReadFile(filepath.Join(dir, prefix+".exit")); err == nil {
		code, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return code, true
	}
	return 0, false
}

// runPM answers `-s <serial> shell pm list packages -U [<filter>]` from the <serial>.pm.out
// table: the lines whose package contains the filter (every line without one), as real
// pm's substring match does. n is this invocation's place among the identical ones: any
// of <serial>.pm#N.out, .err, .exit, .delay and .hang replaces the plain files for the Nth,
// as for the commands serve answers. The command's pid is appended to <serial>.pm.pids
// before it answers, so a test can tell a killed pm is gone.
func runPM(ctx context.Context, dir, serial, filter string, n int, stdout, stderr io.Writer) int {
	has := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	prefix := serial + ".pm"
	if v := prefix + "#" + strconv.Itoa(n); has(v+".out") || has(v+".err") || has(v+".exit") || has(v+".delay") || has(v+".hang") {
		prefix = v
	}
	if f, err := os.OpenFile(filepath.Join(dir, serial+".pm.pids"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		fmt.Fprintln(f, os.Getpid())
		f.Close()
	}
	if code, done := scripted(ctx, dir, prefix, stderr); done {
		return code
	}
	for _, l := range strings.Split(strings.TrimSuffix(string(dirBytes(dir, prefix+".out")), "\n"), "\n") {
		if m := pmLineRE.FindStringSubmatch(l); m != nil && strings.Contains(m[1], filter) {
			fmt.Fprintln(stdout, l)
		}
	}
	return 0
}

// runLogcat answers `-s <serial> logcat …` from <serial>.log, which holds logcat text
// with `--------- beginning of <buffer>` markers: it prints the buffers asked for, from
// the epoch `-T` gives, in time order as the device's own logcat does (the sections
// interleave), at most `-t` lines (the newest kept). Any other `-v` exits 1, and a
// <serial>.nouid file drops the uid column, as a device that ignored it does. A command
// with no `-d` is a follow, streamed as lines become due until it is killed.
func runLogcat(ctx context.Context, dir, serial string, rest []string, stdout, stderr io.Writer) int {
	if code, done := scripted(ctx, dir, serial+".logcat", stderr); done {
		return code
	}
	format, buffers, since, tail, dump := parseLogcatArgs(rest)
	if format != logcatFormat {
		fmt.Fprintf(stderr, "fake adb: unsupported logcat format %q\n", format)
		return 1
	}
	if !dump {
		return followLogcat(ctx, dir, serial, buffers, since, stdout)
	}
	nouid := nouidColumn(dir, serial)
	entries := logcatEntries(dir, serial, buffers, since)
	if tail >= 0 && len(entries) > tail {
		entries = entries[len(entries)-tail:]
	}
	cur := ""
	for _, e := range entries {
		if e.buffer != cur {
			fmt.Fprintf(stdout, "--------- beginning of %s\n", e.buffer)
			cur = e.buffer
		}
		fmt.Fprintln(stdout, maybeStripUID(nouid, e.line))
	}
	return 0
}

// followLogcat answers a follow (plan m1i D9, D20): one logcat client with no `-d`,
// started from the epoch `-T` gives. It streams the log's lines as they become due — the
// backlog (those at or before the device clock) at once, then each line once the device
// clock has reached its epoch, so a line in the future arrives `epoch - device clock`
// after the follow starts — and blocks until it is killed, as the daemon ends the window
// by killing the client's pid. It records its pid in <serial>.follow.pid so a test can
// tell the window's client is gone after the call.
func followLogcat(ctx context.Context, dir, serial string, buffers []string, since string, stdout io.Writer) int {
	writeFollowPid(dir, serial)
	// A -T of digits only is a count of lines, not an epoch (an epoch has its fraction).
	count := -1
	if n, err := strconv.Atoi(since); err == nil && !strings.Contains(since, ".") {
		count, since = n, ""
	}
	from, _ := strconv.ParseFloat(since, 64)
	deviceNow := from
	if count >= 0 {
		deviceNow = math.MaxFloat64 // every line is due when read, unless date.out paces them
	}
	// The device clock is the answer a `shell date +%s.%N` gives, so the fake paces by the
	// same instant the daemon read for its `-T` bound.
	if v := dirString(dir, serial+".date.out"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			deviceNow = f
		}
	}
	nouid := nouidColumn(dir, serial)
	start := time.Now()
	cur := ""
	emitted := 0
	if count >= 0 {
		// The backlog is the newest count of the lines already due; the rest are skipped.
		due := 0
		for _, e := range logcatEntries(dir, serial, buffers, "") {
			if !e.hasEpoch || e.epoch <= deviceNow {
				due++
			}
		}
		if due > count {
			emitted = due - count
		}
	}
	// Bounded: a test that fails before its window ends or before reconciliation must not
	// leave this process re-reading files forever on a shared machine (step 3 review 7).
	giveUp := time.Now().Add(2 * time.Minute)
	for {
		if time.Now().After(giveUp) {
			return 0
		}
		if v := dirString(dir, serial+".follow.exitcode"); v != "" {
			code, _ := strconv.Atoi(v)
			return code
		}
		entries := logcatEntries(dir, serial, buffers, since)
		elapsed := time.Since(start).Seconds()
		for ; emitted < len(entries); emitted++ {
			e := entries[emitted]
			// A line comes due at its epoch once the device clock would have reached it: a
			// line in the future waits, a line at or before the clock is backlog and comes
			// at once. The entries are in epoch order, so once one is not due, none after
			// it is either.
			if e.epoch > deviceNow && elapsed < e.epoch-deviceNow {
				break
			}
			if e.buffer != cur {
				fmt.Fprintf(stdout, "--------- beginning of %s\n", e.buffer)
				cur = e.buffer
			}
			fmt.Fprintln(stdout, maybeStripUID(nouid, e.line))
		}
		// The log file is re-read each turn, so a line a test appends is picked up; the
		// loop ends only when the client is killed or ctx ends (an in-process follow).
		if !wait(ctx, 20*time.Millisecond) {
			return 0
		}
	}
}

// writeFollowPid records the follow client's pid in <serial>.follow.pid, so a test can
// tell the window's client is gone after the call. Written and renamed, so a test
// polling for it never reads it half written (as fakeadb_test's process fakes do).
func writeFollowPid(dir, serial string) {
	name := filepath.Join(dir, serial+".follow.pid")
	tmp := name + ".tmp"
	os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	os.Rename(tmp, name)
}

// nouidColumn reports whether <serial>.nouid asks for the uid column to be dropped, as a
// device that ignored the format element does.
func nouidColumn(dir, serial string) bool {
	_, err := os.Stat(filepath.Join(dir, serial+".nouid"))
	return err == nil
}

// maybeStripUID drops the uid column from a line when the device ignored the format.
func maybeStripUID(nouid bool, line string) string {
	if nouid {
		return stripUID(line)
	}
	return line
}

// logEntry is one line of the device's log, its buffer and (when it has one) its leading
// epoch.
type logEntry struct {
	buffer   string
	line     string
	epoch    float64
	hasEpoch bool
}

// logcatEntries reads the buffers asked for from <serial>.log, from the epoch `-T` gives,
// in time order as the device's own logcat merges them (a scenario's sections are grouped
// by buffer, so they are sorted the way a read of the device arrives). The snapshot keeps
// the newest `-t` of them; a follow streams them as they become due.
func logcatEntries(dir, serial string, buffers []string, since string) []logEntry {
	var entries []logEntry
	for _, sec := range readLogSections(dir, serial) {
		if !containsString(buffers, sec.buffer) {
			continue
		}
		for _, l := range sec.lines {
			if since != "" && !epochAtLeast(l, since) {
				continue
			}
			e, ok := lineEpoch(l)
			entries = append(entries, logEntry{buffer: sec.buffer, line: l, epoch: e, hasEpoch: ok})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].hasEpoch && entries[j].hasEpoch && entries[i].epoch < entries[j].epoch
	})
	return entries
}

// logSection is one buffer's lines, in the order the log file holds them.
type logSection struct {
	buffer string
	lines  []string
}

// readLogSections reads <serial>.log into its buffer sections.
func readLogSections(dir, serial string) []logSection {
	var out []logSection
	cur := -1
	for _, l := range rawLogLines(dirBytes(dir, serial+".log")) {
		if m := beginningRE.FindStringSubmatch(l); m != nil {
			out = append(out, logSection{buffer: m[1]})
			cur = len(out) - 1
			continue
		}
		if cur < 0 {
			out = append(out, logSection{buffer: "main"})
			cur = 0
		}
		out[cur].lines = append(out[cur].lines, l)
	}
	return out
}

var beginningRE = regexp.MustCompile(`^-+ beginning of ([a-z]+)$`)

// rawLogLines splits raw log bytes into lines, dropping the trailing newline and any CR.
func rawLogLines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

// parseLogcatArgs reads the logcat command's own arguments: dump is true when `-d` makes
// it a snapshot, false a follow (plan m1i D9).
func parseLogcatArgs(rest []string) (format string, buffers []string, since string, tail int, dump bool) {
	buffers = append([]string(nil), logcatBuffers...)
	tail = -1
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "-v":
			if i+1 < len(rest) {
				i++
				format = rest[i]
			}
		case "-b":
			if i+1 < len(rest) {
				i++
				buffers = strings.Split(rest[i], ",")
			}
		case "-T":
			if i+1 < len(rest) {
				i++
				since = rest[i]
			}
		case "-t":
			if i+1 < len(rest) {
				i++
				tail, _ = strconv.Atoi(rest[i])
			}
		case "-d":
			dump = true
		}
	}
	return format, buffers, since, tail, dump
}

// lineEpoch is a log line's leading epoch, false when it has none (a marker).
func lineEpoch(line string) (float64, bool) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

// epochAtLeast reports whether line's leading epoch is at or after from.
func epochAtLeast(line, from string) bool {
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	a, err1 := strconv.ParseFloat(f[0], 64)
	b, err2 := strconv.ParseFloat(from, 64)
	if err1 != nil || err2 != nil {
		return true
	}
	return a >= b
}

// stripUID removes the uid column from a line.
func stripUID(line string) string {
	m := logUIDRE.FindStringSubmatch(line)
	if m == nil {
		return line
	}
	return m[1] + " " + m[3] + " " + m[4] + " " + m[5] + " " + m[6]
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// killed.emulators records the emulator serials `emu kill` ended, each with how many more
// `devices -l` answers still list it ("never" for one that never leaves), so a test sees
// the serial leave as a real emu kill does (plan m1i, D20).

var emulMu sync.Mutex

// neverLeaves marks a serial that never leaves the listing.
const neverLeaves = "never"

// goneForever marks a serial that has left the listing and does not come back: the row is
// kept so a later answer does not serve the base devices.out line again.
const goneForever = "gone"

// recordKilledEmulator records a serial ended by `emu kill`, with its leave count.
func recordKilledEmulator(dir, serial string) {
	if serial == "" {
		return
	}
	rem := "0"
	if _, err := os.Stat(filepath.Join(dir, serial+".emukill.stays")); err == nil {
		rem = neverLeaves
	} else if b, err := os.ReadFile(filepath.Join(dir, serial+".emukill.leave")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			rem = s
		}
	}
	emulMu.Lock()
	defer emulMu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "killed.emulators"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintln(f, serial+" "+rem)
	f.Close()
}

// readKilledEmulators reads the table; callers take emulMu.
func readKilledEmulators(dir string) [][2]string {
	b, err := os.ReadFile(filepath.Join(dir, "killed.emulators"))
	if err != nil {
		return nil
	}
	var out [][2]string
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			out = append(out, [2]string{f[0], f[1]})
		}
	}
	return out
}

// writeKilledEmulators replaces the table; callers take emulMu.
func writeKilledEmulators(dir string, rows [][2]string) {
	if len(rows) == 0 {
		os.Remove(filepath.Join(dir, "killed.emulators"))
		return
	}
	var lines []string
	for _, r := range rows {
		lines = append(lines, r[0]+" "+r[1])
	}
	os.WriteFile(filepath.Join(dir, "killed.emulators"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// dropKilledEmulators removes from a `devices -l` answer the serials whose leave count is
// spent, decrements the rest, and keeps those that never leave. So an emulator leaves the
// listing the number of answers after its `emu kill` its leave count names, and once it has
// left it stays gone (plan m1i, D20). A serial that has just left is recorded in
// gone.emulators, so a readiness probe against it answers as real adb does.
func dropKilledEmulators(dir string, out []byte) []byte {
	emulMu.Lock()
	defer emulMu.Unlock()
	table := readKilledEmulators(dir)
	if len(table) == 0 {
		return out
	}
	var rows [][2]string
	gone := map[string]bool{}
	for _, r := range table {
		switch {
		case r[1] == neverLeaves:
			rows = append(rows, r)
		case r[1] == goneForever:
			rows = append(rows, r)
			gone[r[0]] = true
		default:
			n, _ := strconv.Atoi(r[1])
			if n <= 0 {
				rows = append(rows, [2]string{r[0], goneForever})
				gone[r[0]] = true
			} else {
				rows = append(rows, [2]string{r[0], strconv.Itoa(n - 1)})
			}
		}
	}
	writeKilledEmulators(dir, rows)
	for serial := range gone {
		recordGoneEmulator(dir, serial)
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		if f := strings.Fields(line); len(f) > 0 && gone[f[0]] {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return nil
	}
	return []byte(strings.Join(kept, "\n") + "\n")
}

// The emulator table is what a fake launcher and the fake adb share (plan m1i, D20): a
// launcher invocation registers the serial it booted ("<serial> <avd> <pid>"), `devices
// -l` lists it beside devices.out, `emu avd name` answers from it, and `sys.boot_completed`
// turns 1 after the AVD's <avd>.bootpolls-th poll. The fake launcher is a process of its
// own, so the table is a file the two share, single-writer per file:
//
//	emulators.table   the launcher appends "<serial> <avd> <pid>" once it boots
//	<serial>.polls    the fake adb's readiness poll count for that serial
//	<avd>.bootpolls   how many polls answer 0 before the serial answers 1 (default 0)
//	gone.emulators    serials whose `emu kill` took them away, written by the fake adb

// RegisterEmulator records an emulator a fake launcher booted: its serial, the AVD it
// runs and its pid. The fake launcher calls it; a test may seed one too.
func RegisterEmulator(dir, serial, avd string, pid int) {
	if serial == "" || avd == "" {
		return
	}
	emulMu.Lock()
	defer emulMu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "emulators.table"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%s %s %d\n", serial, avd, pid)
	f.Close()
}

// tableAVD is the AVD a registered serial runs, false when the table has no such serial.
func tableAVD(dir, serial string) (string, bool) {
	emulMu.Lock()
	defer emulMu.Unlock()
	b, err := os.ReadFile(filepath.Join(dir, "emulators.table"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == serial {
			return f[1], true
		}
	}
	return "", false
}

// EmulatorPID is the pid a registered serial's launcher reported, 0 when none.
func EmulatorPID(dir, serial string) int {
	emulMu.Lock()
	defer emulMu.Unlock()
	b, err := os.ReadFile(filepath.Join(dir, "emulators.table"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == serial {
			n, _ := strconv.Atoi(f[2])
			return n
		}
	}
	return 0
}

// appendRegisteredEmulators adds a row for every serial the table holds that devices.out
// does not already list, so `devices -l` shows a booted emulator as real adb would.
func appendRegisteredEmulators(dir string, out []byte) []byte {
	emulMu.Lock()
	b, err := os.ReadFile(filepath.Join(dir, "emulators.table"))
	emulMu.Unlock()
	if err != nil {
		return out
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			have[f[0]] = true
		}
	}
	lines := strings.TrimSuffix(string(out), "\n")
	if lines == "" {
		lines = "List of devices attached"
	}
	added := false
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || have[f[0]] {
			continue
		}
		have[f[0]] = true
		lines += "\n" + f[0] + " device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:90"
		added = true
	}
	if !added {
		return out
	}
	return []byte(lines + "\n")
}

// bootCompletedFromTable is whether a registered serial has finished booting: false for
// its first <avd>.bootpolls polls, then true.
func bootCompletedFromTable(dir, serial string) bool {
	avd, ok := tableAVD(dir, serial)
	if !ok {
		return false
	}
	want := 0
	if b, err := os.ReadFile(filepath.Join(dir, avd+".bootpolls")); err == nil {
		want, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	emulMu.Lock()
	defer emulMu.Unlock()
	name := filepath.Join(dir, serial+".polls")
	n := 0
	if b, err := os.ReadFile(name); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	n++
	os.WriteFile(name, []byte(strconv.Itoa(n)), 0o600)
	return n > want
}

// offlineFromTable is whether a registered serial still answers as offline: true for its
// first <avd>.offlinepolls polls (default 0).
func offlineFromTable(dir, serial string) bool {
	avd, ok := tableAVD(dir, serial)
	if !ok {
		return false
	}
	want := 0
	if b, err := os.ReadFile(filepath.Join(dir, avd+".offlinepolls")); err == nil {
		want, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	emulMu.Lock()
	defer emulMu.Unlock()
	name := filepath.Join(dir, serial+".offline")
	n := 0
	if b, err := os.ReadFile(name); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	n++
	os.WriteFile(name, []byte(strconv.Itoa(n)), 0o600)
	return n <= want
}

// serialGone is whether a serial's `emu kill` took it out of the listing for good.
func serialGone(dir, serial string) bool {
	emulMu.Lock()
	defer emulMu.Unlock()
	b, err := os.ReadFile(filepath.Join(dir, "gone.emulators"))
	if err != nil {
		return false
	}
	for _, s := range strings.Fields(string(b)) {
		if s == serial {
			return true
		}
	}
	return false
}

// recordGoneEmulator records a serial that has left the listing for good; callers take
// emulMu.
func recordGoneEmulator(dir, serial string) {
	b, err := os.ReadFile(filepath.Join(dir, "gone.emulators"))
	if err == nil {
		for _, s := range strings.Fields(string(b)) {
			if s == serial {
				return
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "gone.emulators"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintln(f, serial)
	f.Close()
}
