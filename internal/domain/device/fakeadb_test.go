package device

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// The fake adb is this test binary re-executed, as internal/process/processtest does:
// no test finds or runs the machine's real adb. fakeEnv names its mode; fakeLog is a
// file it appends one line per invocation to.
const (
	fakeEnv   = "DROIDYARD_FAKE_ADB"
	fakeLog   = "DROIDYARD_FAKE_ADB_LOG"
	fakeBuild = "DROIDYARD_FAKE_ADB_BUILD" // the client's version number, "41"
	fakeInfo  = "DROIDYARD_FAKE_ADB_INFO"  // a file it writes "<pid> <pgid> <childpid>" to

	modeVersion  = "version"   // answers `adb version`; anything else is logged as a start of a server
	modeForkHang = "fork-hang" // forks a child holding the pipe, then hangs
	modeForkExit = "fork-exit" // forks a child holding the pipe, prints, exits 0
	modeChild    = "child"     // the forked child: holds stdout and stderr, sleeps
)

func TestMain(m *testing.M) {
	devicetest.Main()
	if mode := os.Getenv(fakeEnv); mode != "" {
		runFake(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runFake(mode string) {
	if f := os.Getenv(fakeLog); f != "" {
		if h, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
			fmt.Fprintf(h, "%s port=%s\n", strings.Join(os.Args[1:], " "), os.Getenv("ANDROID_ADB_SERVER_PORT"))
			h.Close()
		}
	}
	switch mode {
	case modeVersion:
		b := os.Getenv(fakeBuild)
		fmt.Printf("Android Debug Bridge version 1.0.%s\nVersion 36.0.0-13206524\nInstalled as /fake/adb\n", b)
	case modeForkHang, modeForkExit:
		// A child in the client's own process group that keeps the output pipe, like the
		// adb server the client forks.
		child, err := os.StartProcess(os.Args[0], []string{os.Args[0]}, &os.ProcAttr{
			Env:   childEnv(),
			Files: []*os.File{nil, os.Stdout, os.Stderr},
		})
		if err != nil {
			os.Exit(3)
		}
		pgid, _ := syscall.Getpgid(0)
		os.WriteFile(os.Getenv(fakeInfo), []byte(fmt.Sprintf("%d %d %d", os.Getpid(), pgid, child.Pid)), 0o600)
		if mode == modeForkExit {
			fmt.Println("hello")
			return
		}
		time.Sleep(time.Minute)
	case modeChild:
		time.Sleep(2 * time.Minute)
	}
}

// fakeADB returns the environment that makes the fake adb in mode the resolved adb:
// ADB names the test binary, and the port is injected, so the shared server on 5037 is
// never reached.
func fakeEnvFor(t *testing.T, mode, port string, extra ...string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"ADB=" + exe, fakeEnv + "=" + mode, "ANDROID_ADB_SERVER_PORT=" + port,
		"HOME=" + t.TempDir(), "PATH="}
	return append(env, extra...)
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// childEnv is the environment with the mode replaced (os.StartProcess does not
// de-duplicate, and the first entry wins).
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, fakeEnv+"=") {
			env = append(env, kv)
		}
	}
	return append(env, fakeEnv+"="+modeChild)
}
