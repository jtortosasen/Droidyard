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
//	<serial>.forward            `-s <serial> forward tcp:0 localabstract:droidyard-reader-<d12>` (.out holds the port)
//	<serial>.forwardremove      `-s <serial> forward --remove tcp:<port>`
//	<serial>.forwardlist        `-s <serial> forward --list` (see below)
//	<serial>.start              `-s <serial> shell "CLASSPATH=<dex> nohup app_process /system/bin --nice-name=… DroidyardReader serve …"`
//	<serial>.pidof              `-s <serial> shell pidof droidyard-reader-…`
//	<serial>.kill               `-s <serial> shell kill <pid>`
//	<serial>.ps                 `-s <serial> shell ps -A -o PID,NAME` (.out holds the listing)
//	<serial>.inputtap           `-s <serial> shell input tap <x> <y>`
//	<serial>.date               `-s <serial> shell date +%s.%N`
//	<serial>.monkey             `-s <serial> shell monkey --pct-syskeys 0 -p <id> -c android.intent.category.LAUNCHER 1`
//	<serial>.apppidof           `-s <serial> shell pidof <applicationId>`
//	<serial>.sdk                `-s <serial> shell getprop ro.build.version.sdk`
//	<serial>.inputtext          `-s <serial> shell input text <arg>`
//	<serial>.inputkeyevent      `-s <serial> shell input keyevent <codes…>`
//	<serial>.inputkeycombination `-s <serial> shell input keycombination <codes…>`
//	<serial>.inputmethod        `-s <serial> shell settings get secure default_input_method`
//	<serial>.broadcast          `-s <serial> shell am broadcast -a ADB_INPUT_B64 --es msg <base64>`
//	<serial>.install            `-s <serial> install -r <apk>`
//	<serial>.uninstall          `-s <serial> uninstall <applicationId>`
//	<serial>.pmpath             `-s <serial> shell pm path <applicationId>`
//
// The fake models the host's forwards in `forwards.list`, one `<serial> tcp:<port>
// <remote>` line per forward. A `forward tcp:0 localabstract:<remote>` binds a real host
// proxy on a free port, prints that port and records the line, so a client reaches the
// reader at `<serial>.forward.out` (its port) through it; `forward --remove tcp:<port>`
// drops the line and closes the proxy, and `forward --list` prints the table. So a
// removed forward refuses a dial, as real adb does (plan m1f step 7). A test seeds a
// forward another daemon or another digest made with AddForward, and drops one as an adb
// server restart would with ForgetForward.
//
// The fake models the device's processes too: a `kill <pid>` records the pid in
// `killed.pids`, and `pidof` and `ps` drop a recorded pid from their answer. So a test
// sees the old pid gone after a kill, as plan m1f step 7's wait requires.
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
	"net"
	"os"
	"path/filepath"
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
		if !waitDelay(ctx, dir, "devices") {
			return 0
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
	case len(args) == 5 && args[2] == "shell" && args[3] == "pidof":
		return args[1] + ".apppidof"
	case len(args) == 5 && args[2] == "shell" && args[3] == "kill":
		return args[1] + ".kill"
	case len(args) == 7 && args[2] == "shell" && args[3] == "ps" && args[4] == "-A" && args[5] == "-o" && args[6] == "PID,NAME":
		return args[1] + ".ps"
	case len(args) == 7 && args[2] == "shell" && args[3] == "input" && args[4] == "tap":
		return args[1] + ".inputtap"
	case len(args) == 5 && args[2] == "shell" && args[3] == "date" && args[4] == "+%s.%N":
		return args[1] + ".date"
	case len(args) == 11 && args[2] == "shell" && args[3] == "monkey" && args[4] == "--pct-syskeys" && args[5] == "0" && args[6] == "-p" && args[8] == "-c" && args[9] == "android.intent.category.LAUNCHER" && args[10] == "1":
		return args[1] + ".monkey"
	case len(args) == 5 && args[2] == "shell" && args[3] == "getprop" && args[4] == "ro.build.version.sdk":
		return args[1] + ".sdk"
	case len(args) == 5 && args[2] == "install" && args[3] == "-r":
		return args[1] + ".install"
	case len(args) == 4 && args[2] == "uninstall":
		return args[1] + ".uninstall"
	case len(args) == 6 && args[2] == "shell" && args[3] == "pm" && args[4] == "path":
		return args[1] + ".pmpath"
	case len(args) == 6 && args[2] == "shell" && args[3] == "input" && args[4] == "text":
		return args[1] + ".inputtext"
	case len(args) >= 6 && args[2] == "shell" && args[3] == "input" && args[4] == "keycombination":
		return args[1] + ".inputkeycombination"
	case len(args) >= 5 && args[2] == "shell" && args[3] == "input" && args[4] == "keyevent":
		return args[1] + ".inputkeyevent"
	case len(args) == 7 && args[2] == "shell" && args[3] == "settings" && args[4] == "get" && args[5] == "secure" && args[6] == "default_input_method":
		return args[1] + ".inputmethod"
	case len(args) == 10 && args[2] == "shell" && args[3] == "am" && args[4] == "broadcast" && args[5] == "-a" && args[6] == "ADB_INPUT_B64" && args[7] == "--es" && args[8] == "msg":
		return args[1] + ".broadcast"
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
	code := 0
	if b, err := os.ReadFile(filepath.Join(dir, name+".exit")); err == nil {
		code, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	// A successful forward binds a host proxy and prints its port, so a later dial
	// reaches the reader through it and a removed forward refuses the dial (plan m1f
	// step 7). A forward that failed falls through to the generic answer below.
	if strings.HasSuffix(prefix, ".forward") && code == 0 && len(raw) == 5 {
		if port, err := startForwardProxy(strings.TrimSpace(dirString(dir, name+".out"))); err == nil {
			fmt.Fprintln(stdout, port)
			addForward(dir, raw[1], port, raw[4])
			event("end")
			return 0
		} else {
			fmt.Fprintln(stderr, "fake adb: "+err.Error())
			event("end")
			return 1
		}
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
// class that is not there; a forward (handled in serve) adds its printed port to the
// listing and a remove drops that port; a kill records the pid as gone.
func deviceEffect(dir, prefix, name string, raw []string, code int, stderr io.Writer) int {
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
	case strings.HasSuffix(prefix, ".kill"):
		if code == 0 && len(raw) == 5 {
			recordKilled(dir, raw[4])
		}
	case strings.HasSuffix(prefix, ".forwardremove"):
		if code == 0 && len(raw) == 5 {
			removeForwardPort(dir, strings.TrimPrefix(raw[4], "tcp:"))
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

// The host's forwards live in forwards.list, one `<serial> tcp:<port> <remote>` line
// each, as `adb forward --list` prints them.

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

func addForward(dir, serial, port, remote string) {
	if serial == "" || port == "" || remote == "" {
		return
	}
	fwdMu.Lock()
	defer fwdMu.Unlock()
	line := serial + " tcp:" + port + " " + remote
	lines := readForwards(dir)
	for _, l := range lines {
		if l == line {
			return
		}
	}
	writeForwards(dir, append(lines, line))
}

func removeForwardPort(dir, port string) {
	fwdMu.Lock()
	var out []string
	for _, l := range readForwards(dir) {
		f := strings.Fields(l)
		if len(f) >= 2 && f[1] == "tcp:"+port {
			continue
		}
		out = append(out, l)
	}
	writeForwards(dir, out)
	fwdMu.Unlock()
	// The host port stops listening when its forward is removed: a dial then refuses,
	// as real adb does (plan m1f step 7). A seeded forward has no proxy.
	closeForwardProxy(port)
}

// AddForward seeds a forward in the fake's `forward --list`, as another daemon's or
// another digest's would be. remote is "localabstract:<name>".
func AddForward(t testing.TB, dir, serial, port, remote string) {
	t.Helper()
	addForward(dir, serial, port, remote)
}

// ForgetForward drops the forward to remote, as an adb server restart drops host
// forwards; the reader behind it is untouched, but its host port stops listening, so a
// dial refuses (plan m1f step 7).
func ForgetForward(t testing.TB, dir, remote string) {
	t.Helper()
	fwdMu.Lock()
	var dropped []string
	var out []string
	for _, l := range readForwards(dir) {
		f := strings.Fields(l)
		if len(f) >= 3 && f[2] == remote {
			dropped = append(dropped, strings.TrimPrefix(f[1], "tcp:"))
			continue
		}
		out = append(out, l)
	}
	writeForwards(dir, out)
	fwdMu.Unlock()
	for _, port := range dropped {
		closeForwardProxy(port)
	}
}

// Forwards returns the fake's forward table, one line per forward.
func Forwards(t testing.TB, dir string) []string {
	t.Helper()
	fwdMu.Lock()
	defer fwdMu.Unlock()
	return readForwards(dir)
}

// ForwardPort returns the host port of the first forward to remote, "" when none.
func ForwardPort(t testing.TB, dir, remote string) string {
	t.Helper()
	fwdMu.Lock()
	defer fwdMu.Unlock()
	for _, l := range readForwards(dir) {
		f := strings.Fields(l)
		if len(f) >= 3 && strings.TrimPrefix(f[2], "localabstract:") == remote {
			return strings.TrimPrefix(f[1], "tcp:")
		}
	}
	return ""
}

// The host forwards are real proxies: `forward tcp:0` binds one to the reader and prints
// it, `forward --remove` and ForgetForward close it. A removed forward therefore refuses
// a dial, driving recovery from the failed read (plan m1f step 7).

type forwardProxy struct {
	ln       net.Listener
	upstream string // host:port of the reader the forward points at
}

var (
	proxyMu sync.Mutex
	proxies = map[string]*forwardProxy{} // proxy port -> proxy
)

// startForwardProxy binds a host listener on a free port and relays it to upstream (a
// port or host:port). It is the port `adb forward tcp:0` prints.
func startForwardProxy(upstream string) (string, error) {
	if upstream == "" {
		return "", fmt.Errorf("no upstream port for the forward")
	}
	if !strings.Contains(upstream, ":") {
		upstream = "127.0.0.1:" + upstream
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p := &forwardProxy{ln: ln, upstream: upstream}
	proxyMu.Lock()
	proxies[port] = p
	proxyMu.Unlock()
	go p.serve()
	return port, nil
}

func closeForwardProxy(port string) {
	proxyMu.Lock()
	p := proxies[port]
	delete(proxies, port)
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
