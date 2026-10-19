package device

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// flagSecure is WindowManager.LayoutParams.FLAG_SECURE, for builds that print `fl=` as hex.
const flagSecure = 0x2000

// FLAG_SECURE (plan m2p step 8; ADR-0080): a window with it is not captured, so a screenshot
// comes back black (API 36) or fails (API 31) and the live view shows the window black. One
// `dumpsys window` names the focused window and, in that window's block, its flags. No
// package or window title leaves this file: the answer is a bool, and the texts below are
// the daemon's own.

// secureWarning is device.screenshot's warning when a capture succeeded with a secure
// window focused; secureCause is the sentence added to SCREENSHOT_FAILED's cause when the
// capture failed with one focused (ADR-0080 §2).
const (
	secureWarning = "the focused window is secure (FLAG_SECURE): Android captures it black"
	secureCause   = "the focused window is secure (FLAG_SECURE), which Android does not capture"
)

// secureCheckBound and secureCheckCap bound one check; `dumpsys window` takes about 35 ms
// (spike §(f)) and prints some 60 KiB, the focus line near its top.
const (
	secureCheckBound = 2 * time.Second
	secureCheckCap   = 4 << 20
)

// secureInterval is P, how often a live view's source runs the check: the larger of 2 s
// and four times the spike's p95 (45 ms), which is 2 s (ADR-0080 §3). A variable so a test
// is quick.
var secureInterval = 2 * time.Second

var errSecureUnreadable = errors.New("the focused window's flags could not be read from dumpsys window")

// secureFocus runs the check: whether the focused window carries FLAG_SECURE. An error is
// a check that failed or whose output could not be parsed; the caller reports nothing then.
func (a *ADB) secureFocus(ctx context.Context, t *target) (bool, error) {
	out, err := a.Run(ctx, t.adb, secureCheckBound, secureCheckCap, "-s", t.serial, "shell", "dumpsys", "window")
	if err != nil {
		return false, err
	}
	if out.ExitCode != 0 || out.Truncated {
		return false, errSecureUnreadable
	}
	return parseSecureFocus(string(out.Stdout))
}

// currentFocusHashRE is the focused window's hash in `mCurrentFocus=Window{<hash> u0 <title>}`.
var currentFocusHashRE = regexp.MustCompile(`mCurrentFocus=Window\{([0-9a-f]+) u[0-9]+ `)

// parseSecureFocus reads only the focused window's flags: the hash on the mCurrentFocus
// line, then the block that starts `Window #N Window{<hash> …}:` and its first `fl=` line,
// whose space-separated flags hold SECURE when the window is secure. Any other window's
// flags are ignored. It errors when the focus line, the block or its flags are missing.
func parseSecureFocus(out string) (bool, error) {
	m := currentFocusHashRE.FindStringSubmatch(out)
	if m == nil {
		return false, errSecureUnreadable
	}
	header := "Window{" + m[1] + " "
	in := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Window #") {
			in = strings.Contains(t, header)
			continue
		}
		if !in || !strings.HasPrefix(t, "fl=") {
			continue
		}
		flags := strings.Fields(strings.TrimPrefix(t, "fl="))
		if len(flags) == 1 {
			// Some builds print the flags as one hex number (a Samsung SM-A256B on Android 16,
			// plan m2t step 10): FLAG_SECURE is 0x2000.
			if v, err := strconv.ParseUint(flags[0], 16, 32); err == nil {
				return v&flagSecure != 0, nil
			}
		}
		for _, f := range flags {
			if f == "SECURE" {
				return true, nil
			}
		}
		return false, nil
	}
	return false, errSecureUnreadable
}

// secureState is a source's last known answer and who hears its changes. The scrcpy
// source's poll sets it and a relayed source's host events do; screen.Source's WatchSecure
// is how the daemon reads it for any source (ADR-0100 §5).
type secureState struct {
	mu       sync.Mutex // held across the listeners' calls, so each hears the changes in order
	known    bool
	secure   bool
	next     int
	watchers map[int]func(secure bool)
}

// WatchSecure implements screen.Source: fn hears the focused window's secure state at once
// when a check has answered already, then on each change. The returned stop removes it and,
// when it returns, no call of fn is running or will start.
func (s *screenSource) WatchSecure(fn func(secure bool)) (stop func()) { return s.secure.watch(fn) }

// setSecure records a check's answer.
func (s *screenSource) setSecure(secure bool) { s.secure.set(secure) }

// watch registers fn; see WatchSecure.
func (st *secureState) watch(fn func(secure bool)) (stop func()) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.watchers == nil {
		st.watchers = map[int]func(bool){}
	}
	id := st.next
	st.next++
	st.watchers[id] = fn
	if st.known {
		fn(st.secure)
	}
	return func() {
		st.mu.Lock()
		delete(st.watchers, id)
		st.mu.Unlock()
	}
}

// set records an answer and tells every watcher when it is the first or a change.
func (st *secureState) set(secure bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.known && st.secure == secure {
		return
	}
	st.known, st.secure = true, secure
	for _, fn := range st.watchers {
		fn(secure)
	}
}

// pollSecure runs the check at the source's start and then every secureInterval until the
// source ends. A failed check changes nothing.
func (s *screenSource) pollSecure() {
	defer s.wg.Done()
	t := &target{adb: s.path, serial: s.serial}
	tick := time.NewTicker(secureInterval)
	defer tick.Stop()
	for {
		if secure, err := s.adb.secureFocus(s.ctx, t); err == nil {
			s.setSecure(secure)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
		}
	}
}
