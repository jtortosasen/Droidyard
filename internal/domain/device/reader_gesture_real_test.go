package device

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
)

// TestRealReaderGestures is plan m3g step 2's level-5 check, run by the orchestrating session
// against a scratch device and never by the unit run: it is skipped without
// DROIDYARD_READER_SERIAL. It uses the adb ADB names (ADR-0032's order, as the daemon does),
// through the real ADB and the actor, so the request is the daemon's and the reader is the
// embedded dex. The device must be unlocked, in portrait, with the launcher as its home
// screen and the notification shade closed; the shade's node is
// com.android.systemui:id/quick_qs_panel (the m3g spike, unlocked, API 31 and 36).
//
//	DROIDYARD_READER_SERIAL=emulator-<port> ADB=<guard> go test ./internal/domain/device -run TestRealReaderGestures -v
//
// It checks, in order: a 300 ms path from the top edge down opens the shade (a read rooted in
// SystemUI holds the node); `press 4` closes it; a 3,000 ms path is answered `done` after about
// that long; two equal points 800 ms apart on a launcher icon show its menu; `press 223` puts
// the device to sleep and `press 224` wakes it; and every read's screen equals `wm size`.
func TestRealReaderGestures(t *testing.T) {
	serial := os.Getenv("DROIDYARD_READER_SERIAL")
	if serial == "" {
		t.Skip("DROIDYARD_READER_SERIAL is not set: this check needs a real device")
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
	res := a.Resolve()
	if !res.Found {
		t.Fatalf("no adb: %+v", res)
	}
	adbPath := res.Path
	root, err := os.MkdirTemp("/tmp", "dyreal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	// The forward's directory and its parent must be this user's and hold no symlink
	// (platform.OwnDir; macOS's /tmp is one), and the path short enough for sun_path.
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	fwd := filepath.Join(root, "fwd")
	if err := os.Mkdir(fwd, 0o700); err != nil {
		t.Fatal(err)
	}
	a.SetForwardDir(fwd)
	tgt := &target{adb: adbPath, serial: serial}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	shell := func(cmd string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, adbPath, "-s", serial, "shell", cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("adb shell %s: %v\n%s", cmd, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		// Leave the device as found: the shade closed, awake, on the home screen. The reader
		// is stopped and its forward removed.
		bg, bgCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer bgCancel()
		for _, c := range []string{"cmd statusbar collapse", "input keyevent 224", "input keyevent 3"} {
			exec.CommandContext(bg, adbPath, "-s", serial, "shell", c).Run()
		}
		a.Close(bg)
	})

	// The size `wm size` reports, an override first, else the physical one.
	size := regexp.MustCompile(`(?m)^(Physical|Override) size: (\d+)x(\d+)`)
	var wm [2]int
	for _, m := range size.FindAllStringSubmatch(shell("wm size"), -1) {
		w, _ := strconv.Atoi(m[2])
		h, _ := strconv.Atoi(m[3])
		if wm == [2]int{} || m[1] == "Override" {
			wm = [2]int{w, h}
		}
	}
	if wm == [2]int{} {
		t.Fatal("wm size gave no size")
	}

	// read waits for cond on the screen (up to 6 s) and returns the last read, which every
	// call checks against wm size and for the reader (a uiautomator dump carries no size).
	read := func(what string, cond func(Snapshot) bool) (Snapshot, bool) {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for {
			s, _, err := a.readSnapshot(ctx, tgt, true)
			if err != nil {
				t.Fatalf("%s: read: %v", what, err)
			}
			if s.screen == [2]int{} {
				t.Fatalf("%s: the read carried no screen size: it did not come from the reader", what)
			}
			if s.screen != wm {
				t.Fatalf("%s: screen %v, wm size %v", what, s.screen, wm)
			}
			if s.Display.Rotation != 0 {
				t.Fatalf("%s: rotation %d: this check needs a portrait device", what, s.Display.Rotation)
			}
			if cond == nil || cond(s) {
				return s, true
			}
			if time.Now().After(deadline) {
				return s, false
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	hasShade := func(s Snapshot) bool {
		if s.Foreground != "com.android.systemui" {
			return false
		}
		for _, n := range s.Nodes {
			if n.ID == "quick_qs_panel" {
				return true
			}
		}
		return false
	}

	// The reader starts with the first read; the actor then pings it.
	read("the first read", nil)
	ac := a.newActor(ctx, tgt)
	if !ac.ok {
		t.Fatal("the reader cannot run on this device")
	}
	press := func(code int) {
		t.Helper()
		got, err := ac.press(ctx, code)
		if err != nil || got.kind != readerActDone {
			t.Fatalf("press %d: %+v, %v, want done", code, got, err)
		}
	}
	gesture := func(points ...pathPoint) (readerAct, time.Duration) {
		t.Helper()
		begin := time.Now()
		got, err := ac.gesture(ctx, points)
		if err != nil {
			t.Fatalf("gesture: %v", err)
		}
		return got, time.Since(begin)
	}

	press(224)
	press(3)
	time.Sleep(1500 * time.Millisecond)
	if s, _ := read("the home screen", nil); hasShade(s) {
		t.Fatal("the shade is open before the check starts")
	}

	// (1) The shade opens from the top edge: dense enough along the path for API 36.
	got, took := gesture(pathPoint{0.5, 0, 0}, pathPoint{0.5, 0.6, 300 * time.Millisecond})
	if got.kind != readerActDone {
		t.Fatalf("the edge swipe: %+v, want done", got)
	}
	t.Logf("the edge swipe took %s", took)
	if s, ok := read("after the edge swipe", hasShade); !ok {
		t.Errorf("no read rooted in com.android.systemui held quick_qs_panel after the edge swipe (foreground %q, %d nodes)", s.Foreground, len(s.Nodes))
	}

	// (2) BACK closes it.
	press(4)
	if _, ok := read("after BACK", func(s Snapshot) bool { return !hasShade(s) }); !ok {
		t.Error("the shade is still in the read after press 4")
	}

	// (3) A 3,000 ms path is answered done, and only after about that long: the reader paces it.
	press(3)
	time.Sleep(800 * time.Millisecond)
	got, took = gesture(pathPoint{0.7, 0.4, 0}, pathPoint{0.3, 0.4, 3 * time.Second})
	if got.kind != readerActDone {
		t.Errorf("the 3 s path: %+v, want done", got)
	}
	if took < 2900*time.Millisecond || took > 5*time.Second {
		t.Errorf("the 3 s path took %s, want about 3 s", took)
	}

	// (4) Two equal points 800 ms apart on a launcher icon are a long press: its menu shows.
	press(3)
	time.Sleep(1200 * time.Millisecond)
	home, _ := read("the home screen again", nil)
	icon := -1
	for i, n := range home.Nodes {
		w, h := n.Bounds[2]-n.Bounds[0], n.Bounds[3]-n.Bounds[1]
		if n.Text != "" && slices.Contains(n.Flags, "clickable") && w > 0 && w < 400 && h > 0 && h < 400 &&
			n.Bounds[0] >= 0 && n.Bounds[2] <= wm[0] && n.Bounds[3] <= wm[1] {
			icon = i
			break
		}
	}
	if icon < 0 {
		t.Fatalf("no launcher icon in the read (foreground %q, %d nodes)", home.Foreground, len(home.Nodes))
	}
	n := home.Nodes[icon]
	fx := float64(n.Bounds[0]+n.Bounds[2]) / 2 / float64(wm[0])
	fy := float64(n.Bounds[1]+n.Bounds[3]) / 2 / float64(wm[1])
	t.Logf("long press on %q at (%.3f, %.3f)", n.Text, fx, fy)
	got, _ = gesture(pathPoint{fx, fy, 0}, pathPoint{fx, fy, 800 * time.Millisecond})
	if got.kind != readerActDone {
		t.Fatalf("the long press: %+v, want done", got)
	}
	hasMenu := func(s Snapshot) bool {
		for _, n := range s.Nodes {
			if strings.EqualFold(n.Text, "App info") || strings.EqualFold(n.Desc, "App info") || strings.Contains(strings.ToLower(n.ID), "popup") {
				return true
			}
		}
		return false
	}
	if s, ok := read("after the long press", hasMenu); !ok {
		t.Errorf("no read showed the icon's menu (an \"App info\" node or a popup id) after the long press (foreground %q, %d nodes)", s.Foreground, len(s.Nodes))
	}
	press(4)

	// (5) Sleep and wake.
	wakefulness := func() string {
		m := regexp.MustCompile(`mWakefulness=(\w+)`).FindStringSubmatch(shell("dumpsys power"))
		if m == nil {
			return ""
		}
		return m[1]
	}
	awaits := func(want ...string) string {
		deadline := time.Now().Add(5 * time.Second)
		for {
			w := wakefulness()
			for _, x := range want {
				if w == x {
					return w
				}
			}
			if time.Now().After(deadline) {
				return w
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	press(223)
	// API 31 reports Asleep; API 36 reports Dozing (the m3g spike): either is the screen off.
	if w := awaits("Asleep", "Dozing"); w != "Asleep" && w != "Dozing" {
		t.Errorf("after press 223 mWakefulness=%s, want Asleep (Dozing on API 36)", w)
	} else {
		t.Logf("after press 223 mWakefulness=%s", w)
	}
	press(224)
	if w := awaits("Awake"); w != "Awake" {
		t.Errorf("after press 224 mWakefulness=%s, want Awake", w)
	}
	press(3)
	time.Sleep(1000 * time.Millisecond)
	read("after wake", nil)
}
