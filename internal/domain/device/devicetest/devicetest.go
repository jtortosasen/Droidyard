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
//	<serial>.forward            `-s <serial> forward tcp:0 localabstract:droidyard-reader-<d12>` (.out holds the port)
//	<serial>.forwardremove      `-s <serial> forward --remove tcp:<port>`
//	<serial>.start              `-s <serial> shell "CLASSPATH=<dex> nohup app_process /system/bin --nice-name=… DroidyardReader serve …"`
//	<serial>.pidof              `-s <serial> shell pidof droidyard-reader-…`
//	<serial>.kill               `-s <serial> shell kill <pid>`
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
// exits 1. So a dex a fallback removed cannot be started from, and a test sees it.
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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// DirEnv names the scenario directory; its presence makes the binary a fake adb.
const DirEnv = "DROIDYARD_FAKE_ADB_DIR"

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

// Write puts a file in the scenario directory.
func Write(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
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
	if len(args) == 5 && args[0] == "-s" && args[2] == "push" && isReaderFile(args[4]) {
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
		cat("devices.out")
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
		cat(args[1] + ".avd")
		return 0
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
	n := 0
	if b, err := os.ReadFile(filepath.Join(dir, "calls.log")); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l == line {
				n++
			}
		}
	}
	return n
}

// servedPrefix names the scenario files that answer args, "" if they are not one of
// the commands served from them.
func servedPrefix(args []string) string {
	if len(args) < 4 || args[0] != "-s" {
		return ""
	}
	switch {
	case len(args) == 5 && args[2] == "push" && args[3] == "LOCAL" && isReaderFile(args[4]):
		return args[1] + ".push"
	case len(args) == 7 && args[2] == "exec-out" && isReaderClasspath(args[3]) && args[4] == "app_process" && args[5] == "/system/bin" && args[6] == "DroidyardReader":
		return args[1] + ".reader"
	case len(args) == 6 && args[2] == "shell" && args[3] == "rm" && args[4] == "-f" && isReaderFile(args[5]):
		return args[1] + ".rmreader"
	case len(args) == 5 && args[2] == "forward" && args[3] == "tcp:0" && strings.HasPrefix(args[4], "localabstract:droidyard-reader-"):
		return args[1] + ".forward"
	case len(args) == 5 && args[2] == "forward" && args[3] == "--remove" && strings.HasPrefix(args[4], "tcp:"):
		return args[1] + ".forwardremove"
	case len(args) == 4 && args[2] == "shell" && isReaderServe(args[3]):
		return args[1] + ".start"
	case len(args) == 5 && args[2] == "shell" && args[3] == "pidof" && strings.HasPrefix(args[4], "droidyard-reader-"):
		return args[1] + ".pidof"
	case len(args) == 5 && args[2] == "shell" && args[3] == "kill":
		return args[1] + ".kill"
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

func isReaderClasspath(a string) bool {
	return strings.HasPrefix(a, "CLASSPATH=") && isReaderFile(strings.TrimPrefix(a, "CLASSPATH="))
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
	if b, err := os.ReadFile(filepath.Join(dir, name+".out")); err == nil {
		stdout.Write(b)
	}
	if b, err := os.ReadFile(filepath.Join(dir, name+".err")); err == nil {
		stderr.Write(b)
	}
	event("end")
	code := 0
	if b, err := os.ReadFile(filepath.Join(dir, name+".exit")); err == nil {
		code, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	return deviceEffect(dir, prefix, raw, code, stderr)
}

// deviceEffect models the device's files for the commands that change them, and returns
// the exit code the command ends with. A push that exited 0 creates its remote path; an
// rm removes it; a serve start whose CLASSPATH= file is missing fails with status 1, the
// way app_process cannot start a class that is not there.
func deviceEffect(dir, prefix string, raw []string, code int, stderr io.Writer) int {
	switch {
	case strings.HasSuffix(prefix, ".push"):
		if code == 0 && len(raw) == 5 {
			if b, err := os.ReadFile(raw[3]); err == nil {
				os.WriteFile(filepath.Join(dir, raw[1]+".pushed"), b, 0o600)
				writeDeviceFile(dir, raw[4], b)
			}
		}
	case strings.HasSuffix(prefix, ".rmreader"):
		if code == 0 && len(raw) == 6 {
			removeDeviceFile(dir, raw[5])
		}
	case strings.HasSuffix(prefix, ".start"):
		if len(raw) == 4 {
			if cp := classpathOf(raw[3]); cp != "" && !deviceFileExists(dir, cp) {
				fmt.Fprintf(stderr, "%s: No such file or directory\n", cp)
				return 1
			}
		}
	}
	return code
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

func deviceFileExists(dir, remote string) bool {
	_, err := os.Stat(deviceFile(dir, remote))
	return err == nil
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
