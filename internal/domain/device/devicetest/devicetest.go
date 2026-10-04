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
//
// `adb version` answers as client 1.0.41; any other invocation exits 9.
package devicetest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	os.Exit(run(dir, os.Args[1:]))
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

func run(dir string, args []string) int {
	if f, err := os.OpenFile(filepath.Join(dir, "calls.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	has := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	cat := func(name string) {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			os.Stdout.Write(b)
		}
	}
	hang := func() int { time.Sleep(time.Minute); return 0 }
	if prefix := servedPrefix(args); prefix != "" {
		return serve(dir, prefix, args)
	}
	switch {
	case len(args) == 1 && args[0] == "version":
		if has("version.hang") {
			return hang()
		}
		fmt.Print("Android Debug Bridge version 1.0.41\nVersion 36.0.0-13206524\nInstalled as /fake/adb\n")
		return 0
	case len(args) == 2 && args[0] == "devices" && args[1] == "-l":
		if has("devices.hang") {
			return hang()
		}
		cat("devices.out")
		if b, err := os.ReadFile(filepath.Join(dir, "devices.err")); err == nil {
			os.Stderr.Write(b)
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
	fmt.Fprintln(os.Stderr, "fake adb: unexpected arguments:", strings.Join(args, " "))
	return 9
}

// servedPrefix names the scenario files that answer args, "" if they are not one of
// the commands served from them.
func servedPrefix(args []string) string {
	if len(args) < 4 || args[0] != "-s" {
		return ""
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

// serve answers one invocation from its prefix's files, and brackets it in events.log
// so a test can see which invocations overlapped.
func serve(dir, prefix string, args []string) int {
	has := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	event := func(what string) {
		if f, err := os.OpenFile(filepath.Join(dir, "events.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
			fmt.Fprintln(f, what, prefix)
			f.Close()
		}
	}
	// n is this invocation's place among the identical ones: calls.log already holds it.
	n := 0
	if b, err := os.ReadFile(filepath.Join(dir, "calls.log")); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l == strings.Join(args, " ") {
				n++
			}
		}
	}
	name := prefix
	if v := prefix + "#" + strconv.Itoa(n); has(v+".out") || has(v+".err") || has(v+".exit") || has(v+".delay") || has(v+".hang") {
		name = v
	}
	event("begin")
	if has(name + ".hang") {
		time.Sleep(time.Minute)
		return 0
	}
	if b, err := os.ReadFile(filepath.Join(dir, name+".delay")); err == nil {
		ms, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if b, err := os.ReadFile(filepath.Join(dir, name+".out")); err == nil {
		os.Stdout.Write(b)
	}
	if b, err := os.ReadFile(filepath.Join(dir, name+".err")); err == nil {
		os.Stderr.Write(b)
	}
	event("end")
	if b, err := os.ReadFile(filepath.Join(dir, name+".exit")); err == nil {
		c, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return c
	}
	return 0
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
