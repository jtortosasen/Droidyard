// Package emulatortest is a fake emulator launcher for tests: the test binary
// re-executed, answering `-list-avds` and booting an AVD from files in a scenario
// directory, so that no test finds or runs this machine's emulator (plan m1i, D20; the
// shape of process/processtest). Only tests import it.
//
// A test calls Main first in TestMain, writes the scenario's files (Write) and gives the
// subsystem the environment Env returns, in which a directory on PATH holds an `emulator`
// symlink to the test binary, so the launcher resolves without this machine's SDK. The
// scenario directory holds:
//
//	list-avds.out, .err    what `-list-avds` prints to stdout and stderr
//	list-avds.exit         its exit status (default 0)
//	list-avds.delay        milliseconds it sleeps before answering
//	list-avds.hang         if present, it never answers
//	launcher.calls.log     one line per invocation: its arguments
//
// A `-avd <name> …` invocation is a boot (plan m1i, D20): it prints a line naming the
// boot, then, after the delay, registers the serial it booted in the fake adb's table
// (TableEnv) and stays alive until that serial's `emu kill` reaches the table. Its files:
//
//	<name>.serial          the serial it boots (default emulator-5556); .serial.N for the
//	                       Nth boot of that AVD
//	<name>.extra           "<serial> <avd>" lines to register before its own, so a test can
//	                       make another emulator appear too
//	<name>.launch.delay    milliseconds before it registers
//	<name>.launch.out,.err what it prints to its log
//	<name>.launch.exit     if present, it exits at once with this status (the
//	                       "launcher exited early" and "second instance" scenarios)
//	<name>.stop            if present, it exits as a kill would
//
// Any other invocation exits 9, so a test sees a launcher asked to do something the fake
// does not model.
package emulatortest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// DirEnv names the scenario directory; its presence makes the binary a fake emulator.
const DirEnv = "DROIDYARD_FAKE_EMULATOR_DIR"

// TableEnv names the fake adb's scenario directory, where the emulator table lives: the
// launcher registers the serial it booted there and watches it for the kill. Empty means
// no table, so a boot registers nothing (a test of the launcher alone).
const TableEnv = "DROIDYARD_FAKE_EMULATOR_TABLE"

// defaultSerial is the serial a boot uses when the scenario names none.
const defaultSerial = "emulator-5556"

// bootMaxLifetime bounds a boot that is never killed, so a test that forgets its cleanup
// does not leave a process behind for ever.
const bootMaxLifetime = 10 * time.Minute

// Main runs the fake and exits if the environment names a scenario, and returns at once
// otherwise: call it first in TestMain.
func Main() {
	dir := os.Getenv(DirEnv)
	if dir == "" {
		return
	}
	os.Exit(run(context.Background(), dir, os.Getenv(TableEnv), os.Args[1:], os.Stdout, os.Stderr))
}

// Env is the environment in which the fake resolves as the launcher: a temporary
// directory on PATH holds an `emulator` symlink to the test binary, and HOME is a
// temporary directory so no host SDK is found. It carries no AVD home; a test that needs
// one appends ANDROID_AVD_HOME, and one that boots an AVD appends TableEnv.
func Env(t *testing.T, dir string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return EnvFor(exe, dir, t.TempDir(), t.TempDir())
}

// EnvFor is Env without a *testing.T, for a caller that already has the paths (a
// re-executed daemon's test seam): the binary at exe is the launcher, dir its scenario
// directory, bin the directory that gets the `emulator` symlink and home its HOME.
func EnvFor(exe, dir, bin, home string) []string {
	os.Symlink(exe, filepath.Join(bin, "emulator")) // an existing link is the same link
	return []string{"PATH=" + bin, DirEnv + "=" + dir, "HOME=" + home}
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
	b, err := os.ReadFile(filepath.Join(dir, "launcher.calls.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func run(ctx context.Context, dir, table string, args []string, stdout, stderr *os.File) int {
	if f, err := os.OpenFile(filepath.Join(dir, "launcher.calls.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	has := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	cat := func(name string, w *os.File) {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			w.Write(b)
		}
	}
	text := func(name, fallback string) string {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
		return fallback
	}
	if len(args) == 1 && args[0] == "-list-avds" {
		if has("list-avds.hang") {
			<-ctx.Done()
			return 0
		}
		if b, err := os.ReadFile(filepath.Join(dir, "list-avds.delay")); err == nil {
			ms, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
			case <-ctx.Done():
				return 0
			}
		}
		cat("list-avds.out", stdout)
		cat("list-avds.err", stderr)
		if b, err := os.ReadFile(filepath.Join(dir, "list-avds.exit")); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			return n
		}
		return 0
	}
	if len(args) >= 2 && args[0] == "-avd" {
		return boot(dir, table, args[1], stdout, stderr, has, cat, text)
	}
	fmt.Fprintln(stderr, "fake emulator: unexpected arguments:", strings.Join(args, " "))
	return 9
}

// boot is a `-avd <name> …` invocation: the early exit a scenario scripts, or a detached
// boot that registers its serial and stays until killed (plan m1i, D20).
func boot(dir, table, name string, stdout, stderr *os.File, has func(string) bool,
	cat func(string, *os.File), text func(string, string) string) int {
	if has(name + ".launch.exit") {
		cat(name+".launch.out", stdout)
		cat(name+".launch.err", stderr)
		n, _ := strconv.Atoi(text(name+".launch.exit", "1"))
		return n
	}
	if b, err := os.ReadFile(filepath.Join(dir, name+".launch.delay")); err == nil {
		ms, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	n := bootsOf(dir, name) // this is the nth boot of that AVD
	serial := text(name+".serial."+strconv.Itoa(n), text(name+".serial", defaultSerial))
	fmt.Fprintf(stdout, "fake emulator: boot %d of %s as %s\n", n, name, serial)
	cat(name+".launch.out", stdout)
	cat(name+".launch.err", stderr)
	if table != "" {
		// A decoy first, so a boot that resolves its serial by difference alone rather
		// than by `emu avd name` picks the wrong one.
		for _, line := range strings.Split(text(name+".extra", ""), "\n") {
			if f := strings.Fields(line); len(f) >= 2 {
				devicetest.RegisterEmulator(table, f[0], f[1], os.Getpid())
			}
		}
		devicetest.RegisterEmulator(table, serial, name, os.Getpid())
	}
	deadline := time.Now().Add(bootMaxLifetime)
	for time.Now().Before(deadline) {
		if has(name + ".stop") {
			return 0
		}
		if table != "" && killed(table, serial) {
			return 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0
}

// bootsOf is how many times the fake has been invoked for -avd name, this one included.
func bootsOf(dir, name string) int {
	b, err := os.ReadFile(filepath.Join(dir, "launcher.calls.log"))
	if err != nil {
		return 1
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "-avd" && f[1] == name {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

// killed is whether the fake adb's `emu kill` has taken serial out of the table.
func killed(table, serial string) bool {
	b, err := os.ReadFile(filepath.Join(table, "killed.emulators"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 1 && f[0] == serial {
			return true
		}
	}
	return false
}
