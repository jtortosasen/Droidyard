package device

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
)

// TestRealScreenSource is plan m2p step 3's level-5 check, run by the orchestrating
// session against a scratch device and never by the unit run: it is skipped without
// DROIDYARD_SCREEN_SERIAL. It uses the adb that ADB names, else the one on PATH, and the
// adb server that adb talks to, so the serial must be a device whose owner allows it.
//
//	DROIDYARD_SCREEN_SERIAL=<serial> go test ./internal/domain/device -run TestRealScreenSource -v
//
// It starts the source, swipes the screen while it reads, wants at least one key frame and
// 20 packets within 5 s, sends the home key, releases, and then wants the server gone from
// the device's process list within 2 s, its forward gone from the forward list and its
// socket file gone from the forward directory (ADR-0102: adb leaves the file, the source
// unlinks it). The forward is to a socket file in a directory of the test's own, under /tmp.
func TestRealScreenSource(t *testing.T) {
	serial := os.Getenv("DROIDYARD_SCREEN_SERIAL")
	if serial == "" {
		t.Skip("DROIDYARD_SCREEN_SERIAL is not set: this check needs a real device")
	}
	tool := os.Getenv("ADB")
	if tool == "" {
		tool = "adb"
	}
	adbPath, err := exec.LookPath(tool)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sup := process.New(process.Options{Store: st, Log: io.Discard})
	t.Cleanup(sup.Shutdown)
	if err := sup.Load(); err != nil {
		t.Fatal(err)
	}
	a := NewADB(os.Environ(), sup.Spawn)
	a.SetStreamStarter(recordedStarter(sup))
	root, err := os.MkdirTemp("/tmp", "dyreal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	fwd := filepath.Join(root, "fwd")
	a.SetForwardDir(fwd)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		a.screens.StopAll(ctx)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	src, release, err := a.screens.acquire(ctx, a, &target{adb: adbPath, serial: serial})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Logf("size %v", src.Size())
	if left, _ := os.ReadDir(fwd); len(left) != 1 || !strings.HasPrefix(left[0].Name(), "screen-") {
		t.Errorf("forward directory holds %v while the source runs, want its one screen-<12 hex> socket", left)
	}

	swiper := exec.Command(adbPath, "-s", serial, "shell",
		"for i in 1 2 3 4 5 6 7 8; do input swipe 540 1600 540 600 300; input swipe 540 600 540 1600 300; done")
	if err := swiper.Start(); err != nil {
		t.Fatal(err)
	}
	defer swiper.Process.Kill()

	v := src.Join()
	vctx, vcancel := context.WithTimeout(ctx, 5*time.Second)
	defer vcancel()
	packets, keys := 0, 0
	for packets < 20 || keys < 1 {
		p, err := v.Next(vctx)
		if err != nil {
			t.Fatalf("after %d packets and %d key frames: %v", packets, keys, err)
		}
		packets++
		if p.Key {
			keys++
		}
	}
	t.Logf("%d packets, %d key frames", packets, keys)
	if err := src.Inject(ctx, screen.NewHolder(false), screen.Key{Name: "home"}); err != nil {
		t.Errorf("Inject: %v", err)
	}

	release()
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, err := exec.Command(adbPath, "-s", serial, "shell", "ps -A -o ARGS").CombinedOutput()
		if err != nil {
			t.Fatalf("ps: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "com.genymobile") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server is still in ps -A 2 s after release:\n%s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	out, err := exec.Command(adbPath, "forward", "--list").CombinedOutput()
	if err != nil {
		t.Fatalf("forward --list: %v\n%s", err, out)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, serial) && strings.Contains(l, "localabstract:scrcpy_") {
			t.Errorf("a forward is left: %s", l)
		}
	}
	if left, _ := os.ReadDir(fwd); len(left) != 0 {
		t.Errorf("socket files are left in the forward directory: %v", left)
	}
}
