// Package emulatortest is a fake emulator launcher for tests: the test binary
// re-executed, answering `-list-avds` from files in a scenario directory, so that no test
// finds or runs this machine's emulator (plan m1i, D20; the shape of process/processtest).
// Only tests import it.
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
)

// DirEnv names the scenario directory; its presence makes the binary a fake emulator.
const DirEnv = "DROIDYARD_FAKE_EMULATOR_DIR"

// Main runs the fake and exits if the environment names a scenario, and returns at once
// otherwise: call it first in TestMain.
func Main() {
	dir := os.Getenv(DirEnv)
	if dir == "" {
		return
	}
	os.Exit(run(context.Background(), dir, os.Args[1:], os.Stdout, os.Stderr))
}

// Env is the environment in which the fake resolves as the launcher: a temporary
// directory on PATH holds an `emulator` symlink to the test binary, and HOME is a
// temporary directory so no host SDK is found. It carries no AVD home; a test that needs
// one appends ANDROID_AVD_HOME.
func Env(t *testing.T, dir string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, "emulator")); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + bin, DirEnv + "=" + dir, "HOME=" + t.TempDir()}
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

func run(ctx context.Context, dir string, args []string, stdout, stderr *os.File) int {
	if f, err := os.OpenFile(filepath.Join(dir, "launcher.calls.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	has := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
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
		if b, err := os.ReadFile(filepath.Join(dir, "list-avds.out")); err == nil {
			stdout.Write(b)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "list-avds.err")); err == nil {
			stderr.Write(b)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "list-avds.exit")); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			return n
		}
		return 0
	}
	fmt.Fprintln(stderr, "fake emulator: unexpected arguments:", strings.Join(args, " "))
	return 9
}
