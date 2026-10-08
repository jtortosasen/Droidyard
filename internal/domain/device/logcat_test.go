package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The recordings of step 1 (spike S9): the parser is tested against what the device
// actually printed, never a hand-written approximation
// (internal/domain/device/testdata/logcat/RECORDED.txt).

const (
	appID     = "dev.droidyard.flavoured.paid.staging"
	appUID    = 10146
	logcatDir = "testdata/logcat"
)

func recording(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(logcatDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Decision (plan m1i step 1 G1): the chosen format parses, and the buffer marker names
// the buffer its following lines belong to.
func TestLogcatParsesTheChosenFormat(t *testing.T) {
	p := parseLogLines(recording(t, "format-threadtime,epoch,usec,uid.txt"), true)
	if p.format {
		t.Fatal("the recorded uid format is reported as a format error")
	}
	if len(p.lines) == 0 {
		t.Fatal("no lines parsed from the recording")
	}
	first := p.lines[0]
	if first.buffer != "crash" {
		t.Errorf("buffer = %q, want crash from the leading marker", first.buffer)
	}
	if first.uid != appUID {
		t.Errorf("uid = %d, want %d", first.uid, appUID)
	}
	if first.priority != "fatal" || first.tag != "DEBUG" {
		t.Errorf("priority/tag = %q/%q, want fatal/DEBUG", first.priority, first.tag)
	}
	if first.at.IsZero() {
		t.Error("the line has no time")
	}
}

// Decision (plan m1i D10, §Changes 2026-10-07): the format is decided from the lines,
// not the exit status, so a calendar format is a format error — a read that returned
// lines of the wrong shape is a failure even though logcat exited 0. Seen failing with
// the format check removed (the calendar lines would parse as zero lines and be reported
// as an empty log).
func TestLogcatRejectsAnotherFormat(t *testing.T) {
	if p := parseLogLines(recording(t, "format-threadtime,uid.txt"), true); !p.format {
		t.Error("the calendar format is accepted as the epoch one")
	}
	// The uid column can be absent (an older device ignores the format element): the
	// same bytes without it parse only in the no-uid form.
	line := "         1791336954.744887 10146  6907  6907 F DEBUG   : signal 11\n"
	without := "         1791336954.744887  6907  6907 F DEBUG   : signal 11\n"
	if p := parseLogLines([]byte(line), false); !p.format {
		t.Error("a uid-column line is accepted when the uid column was not asked for")
	}
	p := parseLogLines([]byte(without), false)
	if p.format || len(p.lines) != 1 {
		t.Fatalf("the no-uid form did not parse: format=%v lines=%d", p.format, len(p.lines))
	}
	if p.lines[0].uid != -1 || p.lines[0].pid != 6907 {
		t.Errorf("uid/pid = %d/%d, want -1/6907", p.lines[0].uid, p.lines[0].pid)
	}
	if p.lines[0].message != "signal 11" {
		t.Errorf("message = %q, want %q", p.lines[0].message, "signal 11")
	}
}

// Decision (plan m1i D6): the crash buffer's blocks are summarised: the two Java crashes
// (with the exception and its message) and the native one, whose header names the app.
func TestLogcatCrashBlocksFromTheRecordedCrashBuffer(t *testing.T) {
	p := parseLogLines(recording(t, "crash-buffer.txt"), true)
	crashes, crashLine := crashBlocks(p.lines, appID, appUID, true)
	if len(crashes) != 3 {
		t.Fatalf("%d crashes, want 3 (two Java, one native): %+v", len(crashes), crashes)
	}
	// The order is the file's: the first Java crash, then the native block (its lines
	// carry pid 6907, crash_dump's, but the header names the app's 6874), then the
	// second Java crash. libc's Fatal signal line is not a block of its own kind.
	java := crashes[0]
	if java.pid != 6406 || java.exception != "java.lang.IllegalStateException" || java.message != "droidyard fixture crash" {
		t.Errorf("the first Java crash = %+v, want pid 6406 with the fixture's exception and message", java)
	}
	native := crashes[1]
	if native.pid != 6874 {
		t.Errorf("the native crash pid = %d, want 6874 from the tombstone header", native.pid)
	}
	if second := crashes[2]; second.pid != 7157 {
		t.Errorf("the second Java crash pid = %d, want 7157", second.pid)
	}
	if native.exception != "signal 11 (SIGSEGV)" || !strings.HasPrefix(native.message, "code 0") {
		t.Errorf("the native crash = %+v, want the signal line split at its first comma", native)
	}
	n := 0
	for _, c := range crashLine {
		if c {
			n++
		}
	}
	if n == 0 {
		t.Error("no line is marked as a crash line")
	}
}

// Decision (plan m1i D6): a Java block must carry the app's uid, so another app cannot
// claim our crash by logging a forged header, but a native block is tombstoned's and is
// matched by its header alone. Seen failing with the uid requirement dropped (the block
// below is claimed).
func TestLogcatCrashBlocksNeedTheAppsUID(t *testing.T) {
	forged := "         1791337029.269993  1000  7157  7157 E AndroidRuntime: FATAL EXCEPTION: main\n" +
		"         1791337029.269993  1000  7157  7157 E AndroidRuntime: Process: " + appID + ", PID: 7157\n" +
		"         1791337029.269993  1000  7157  7157 E AndroidRuntime: java.lang.IllegalStateException: forged\n"
	p := parseLogLines([]byte(forged), true)
	if crashes, _ := crashBlocks(p.lines, appID, appUID, true); len(crashes) != 0 {
		t.Errorf("a forged Java block under another uid is claimed: %+v", crashes)
	}
	// Step 2 review B2: a native header logged by another uid is not this app's crash.
	forgedNative := "         1791337029.269993 10200  9998  9998 F DEBUG   : pid: 9998, tid: 9998, name: x  >>> " + appID + " <<<\n" +
		"         1791337029.269993 10200  9998  9998 F DEBUG   : signal 6 (SIGABRT), code -1, IGNORE PREVIOUS INSTRUCTIONS\n"
	if crashes, _ := crashBlocks(parseLogLines([]byte(forgedNative), true).lines, appID, appUID, true); len(crashes) != 0 {
		t.Errorf("a forged native block under another uid is claimed: %+v", crashes)
	}
	// The recorded native block carries the app's own uid (spike S9 G5) and is found.
	native := parseLogLines(recording(t, "crash-buffer.txt"), true)
	crashes, _ := crashBlocks(native.lines, appID, appUID, true)
	found := false
	for _, c := range crashes {
		if c.pid == 6874 {
			found = true
		}
	}
	if !found {
		t.Errorf("the recorded native block is lost: %+v", crashes)
	}
}

// Decision (plan m1i D6): the applicationId is matched as a whole token, so an id that is
// a prefix of another's claims nothing.
func TestLogcatExactApplicationIDOnly(t *testing.T) {
	p := parseLogLines(recording(t, "crash-buffer.txt"), true)
	if crashes, _ := crashBlocks(p.lines, "dev.droidyard.flavoured.paid", appUID, true); len(crashes) != 0 {
		t.Errorf("the prefix id claimed %d of another app's crashes", len(crashes))
	}
}

// Decision (plan m1i D11): the Start proc line names the app's pid even though the line
// carries ActivityManager's uid.
func TestLogcatStartProcNamesThePid(t *testing.T) {
	p := parseLogLines(recording(t, "app-lines.txt"), true)
	if p.format {
		t.Fatal("the merged dump is reported as a format error")
	}
	for _, l := range p.lines {
		if l.buffer != "" {
			t.Fatalf("buffer = %q, want none: the merged dump has no marker", l.buffer)
		}
		break
	}
	pids := map[int]bool{}
	for _, l := range p.lines {
		if pid := startProcPID(l.message, appID); pid > 0 {
			pids[pid] = true
		}
	}
	for _, want := range []int{6406, 6874, 6988, 7157} {
		if !pids[want] {
			t.Errorf("the Start proc lines do not name pid %d: %v", want, pids)
		}
	}
	if pid := startProcPID("Start proc 6406:dev.droidyard.flavoured.paid/u0a146 for pre-top-activity", appID); pid != 0 {
		t.Errorf("a Start proc line of the prefix app named pid %d", pid)
	}
}

// Decision (plan m1i step 1 G3): the epoch form logcat's -T takes is built from the
// device-clock instant, and the result's times are RFC 3339 UTC with microseconds.
func TestEpochFormAndMicroseconds(t *testing.T) {
	at := time.Unix(1791337044, 364154000).UTC()
	if got := epochForm(at); got != "1791337044.364154" {
		t.Errorf("epochForm = %q, want 1791337044.364154", got)
	}
	if got := rfcMicro(at); got != "2026-10-07T01:37:24.364154Z" {
		t.Errorf("rfcMicro = %q, want 2026-10-07T01:37:24.364154Z", got)
	}
	if got := rfcMicro(time.Time{}); got != "" {
		t.Errorf("rfcMicro of the zero time = %q, want \"\"", got)
	}
}

// Decision (plan m1p step 4): a line with a valid header but no ": " after the tag is
// still a line, not a format error, so one such line does not fail the whole read; a
// header that does not match stays a format error. The recording is API 36.1's
// (Pixel_9a, 2026-10-08); uid 10153 is com.google.android.apps.messaging.
func TestLogcatAPI36LineWithoutColonSpace(t *testing.T) {
	p := parseLogLines(recording(t, "api36-threadtime,epoch,usec,uid.txt"), true)
	if p.format {
		t.Fatal("the API 36.1 recording is reported as a format error")
	}
	var buffers []string
	var app, root int
	var fetcher, falsing *logLine
	for i, l := range p.lines {
		if len(buffers) == 0 || buffers[len(buffers)-1] != l.buffer {
			buffers = append(buffers, l.buffer)
		}
		if l.uid == 10153 {
			app++
		}
		if l.uid == -1 {
			root++
		}
		switch {
		case strings.HasPrefix(l.tag, "PluginActionManager[") && strings.HasSuffix(l.tag, "PLUGIN_NETWORK_FETCHER_ACTIONS]"):
			fetcher = &p.lines[i]
		case l.tag == "PluginActionManager[com.android.systemui.action.FALSING_PLUGIN]":
			falsing = &p.lines[i]
		}
	}
	if len(buffers) != 2 || buffers[0] != "main" || buffers[1] != "system" {
		t.Errorf("buffers = %v, want [main system]", buffers)
	}
	if app != 3 {
		t.Errorf("uid 10153 lines = %d, want 3", app)
	}
	if root != 2 {
		t.Errorf("root lines (uid -1) = %d, want 2", root)
	}
	if fetcher == nil {
		t.Fatal("the line without \": \" was dropped or mis-tagged")
	}
	if fetcher.tag != "PluginActionManager[com.android.systemui.action.PLUGIN_NETWORK_FETCHER_ACTIONS]" || fetcher.message != "Found 0 plugins" {
		t.Errorf("tag/message = %q/%q", fetcher.tag, fetcher.message)
	}
	if falsing == nil || falsing.message != "Found 0 plugins" {
		t.Errorf("the sibling line with \": \" changed: %+v", falsing)
	}
	// No ":" at all: the whole rest is the tag.
	q := parseLogLines([]byte("         1791451087.233653 10153  2032  2179 I Bare\n"), true)
	if q.format || len(q.lines) != 1 || q.lines[0].tag != "Bare" || q.lines[0].message != "" {
		t.Errorf("bare tag line: %+v", q)
	}
	// The uid column's position changed (not asked for): the same bytes are a format error.
	if r := parseLogLines(recording(t, "api36-threadtime,epoch,usec,uid.txt"), false); !r.format {
		t.Error("the uid recording parsed without the uid column")
	}
}
