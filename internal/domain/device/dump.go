package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/reader"
)

// ErrUIDumpFailed is the dump's own failure (ui-automation.md §Failure modes; plan
// m1e, R7). Host failures are DEVICE_ADB_FAILED, from the preamble only (Q3).
const ErrUIDumpFailed capability.ErrorCode = "DEVICE_UI_DUMP_FAILED"

// The dump's time budget (plan m1e, step 6): the whole dump, every attempt and the
// cleanup, ends within dumpDeadline of the first command; one attempt runs at most
// dumpAttempt. The last cleanupReserve of the deadline is kept for removing the
// /sdcard file, so that a dump that used its whole budget still cleans up inside it.
// Variables so tests are quick.
//
// dumpAttempt is above uiautomator's own idle wait (about 10 s), so that its "could not
// get idle state" reaches us as that message and is retried, instead of our timeout
// cutting the attempt first. idleRetryPause is the settle time before that retry. The
// reader (ADR-0029) is bounded by readerPush and readerRun, inside the same deadline.
var (
	dumpDeadline   = 22 * time.Second
	dumpAttempt    = 12 * time.Second
	cleanupReserve = 2 * time.Second
	idleRetryPause = 800 * time.Millisecond
	readerPush     = 5 * time.Second
	readerRun      = 8 * time.Second
)

const (
	dumpOutputCap = 8 << 20
	sdcardDump    = "/sdcard/window_dump.xml"
	idleFailure   = "could not get idle state"
	// readerRemote is where the persistent reader's dex is pushed once and left
	// (ADR-0030); the one-shot reader has its own file, removed after its read. The
	// two never share a path, so a fallback never removes the dex the persistent
	// reader starts from. Both names carry the pinned digest, so two builds never
	// share a file, and both stay inside E6's /data/local/tmp/droidyard-*.
	readerRemote        = "/data/local/tmp/droidyard-reader-" + reader.SHA256 + ".dex"
	readerRemoteOneShot = "/data/local/tmp/droidyard-reader-" + reader.SHA256 + "-oneshot.dex"
	// fallbackWarning starts the warnings[] entry of a read that fell back (ADR-0029); the
	// reader's reason follows it.
	fallbackWarning = "The UI reader could not run on this device, so the screen was read with uiautomator dump, which suspended the device's other accessibility services while it ran."
	// readerNoRoot is the reader's exit status when the active window has no root node.
	readerNoRoot = 3
	readerPrefix = "droidyard-reader:"
	// readerNoRootMessage is the fixed sentence for a reader that found no window root,
	// persistent or one-shot. The reader's own line is device text and never a cause.
	readerNoRootMessage = "the UI reader found no active window to read (the screen may be off or still changing)"
)

// readerDigest is the digest the embedded dex has at run time; a variable so a test can
// stand in for a dex that is not the pinned one.
var readerDigest = reader.Digest

// UITreeParams is device.uiTree's parameters.
type UITreeParams struct {
	Device string            `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	Within *AncestorSelector `json:"within,omitempty" jsonschema_description:"Return only the subtree of each matching node, the node included; no match gives an empty tree."`
}

// dumpKey is the per-device dump lock key (ui-automation.md §The tree: uiautomator
// does not tolerate concurrent dumps).
func dumpKey(serial string) string { return "device.dump:" + serial }

func registerUITree(reg *capability.Registry, adb *ADB) {
	reg.MustRegister(capability.Descriptor{
		Name:         "device.uiTree",
		Kind:         capability.Query,
		Scope:        capability.Global,
		Since:        "0.4.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Returns the current UI as a compact accessibility tree: snapshot id, foreground package, and a flat list of nodes with ref, class, id, text, desc and bounds. within returns only the subtree of each matching node, the node included, which is how to narrow a tree that comes back truncated.",
		Params:       UITreeParams{},
		Result:       Snapshot{},
		Text:         capability.TextOf(uiTreeText),
		Errors:       append(append([]capability.ErrorCode{}, PreambleErrors...), ErrUIDumpFailed, capability.ErrInvalidParams, capability.ErrOperationQueueTimeout),
		Resource: func(call *capability.Call) string {
			if s := call.Params.(UITreeParams).Device; s != "" {
				return dumpKey(s)
			}
			return ""
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			return adb.authorize(ctx, call.Params.(UITreeParams).Device, "device.uiTree")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			t := call.Authorized.(*target)
			p := call.Params.(UITreeParams)
			if err := p.Within.validate("within"); err != nil {
				return nil, err
			}
			if err := adb.ready(ctx, t); err != nil {
				return nil, err
			}
			s, err := adb.uiTree(ctx, t, p.Within)
			if err != nil {
				if busyApp(err) {
					// A busy read carries the app's package in Details: device text, so
					// the failed call is untrusted and its note says so (ADR-0034 §1).
					// busyApp is false when the focus could not be read, so a busy failure
					// with no package is not marked: it carries nothing from the device
					// (plan m1n step 3, fix round).
					yes := true
					call.Untrusted = &yes
				}
				return nil, err
			}
			return s, nil
		},
	})
}

func (a *ADB) uiTree(ctx context.Context, t *target, within *AncestorSelector) (Snapshot, error) {
	doc, warnings, err := a.read(ctx, t)
	if err != nil {
		return Snapshot{}, err
	}
	s, err := buildSnapshot(doc, NewSnapshotID(), DefaultUITreeBytes, within)
	if err != nil {
		return Snapshot{}, dumpFailed(err.Error())
	}
	s.Warnings = append(slices.Clone(t.warnings), warnings...)
	// A tree that holds the system dialog's buttons names it (ADR-0034 §3; plan m1n step
	// 6): a standalone read looks a minute back in the events buffer for the dialog's
	// package, because the dialog was raised by an earlier call. The clock read and the
	// events read are paid only when a dialog is actually on screen.
	if hasSystemDialog(s) {
		a.systemDialogForRead(ctx, t, &s, time.Time{}, systemDialogWindow)
	}
	a.snapshots(t.serial).keep(s)
	return s, nil
}

// read returns the screen's hierarchy document: from the persistent UI reader
// (ADR-0030) when it is running, else from the one-shot reader (ADR-0029), and only
// when neither can run from `uiautomator dump`, each fallback with a warning that says
// so. One deadline covers the whole chain: the per-device lock's wait, the persistent
// read, a restart, the one-shot and the dump (plan m1f, R2). A serial that adb says is
// gone is DEVICE_DISCONNECTED either way; the caller's own cancellation is returned as
// it is.
func (a *ADB) read(ctx context.Context, t *target) ([]byte, []string, error) {
	return a.readChain(ctx, t, true, nil)
}

// readNoDump is read without the `uiautomator dump` stage: the persistent reader and
// the one-shot reader only (plan m1f, D17). After a tap the dump would suspend the
// device's accessibility services on every poll, so a poll that both readers fail is a
// failed read, not a dump.
func (a *ADB) readNoDump(ctx context.Context, t *target) ([]byte, []string, error) {
	return a.readChain(ctx, t, false, nil)
}

// readChain is read with the dump stage allowed or not. idle, when non-nil, is the
// reader's idle bound for this read (D2): the polls after an action's first read pass
// the shorter pollIdle, so a screen that never goes quiet does not delay each poll by the
// reader's default wait. A nil idle is a plain `dump`, which is the first read's own
// settle and every standalone read.
func (a *ADB) readChain(ctx context.Context, t *target, allowDump bool, idle *readerIdle) ([]byte, []string, error) {
	start := time.Now()
	end := start.Add(dumpDeadline)
	// The read's own end is clamped to the caller's deadline when that is earlier
	// (plan m1f step 6, fix round): the actions pass phase contexts under the 22 s
	// dumpDeadline, and the one-shot reader's cleanup must end by that phase deadline,
	// not by a fresh 22 s. A caller that already ended is handled below as it was.
	if d, ok := ctx.Deadline(); ok && d.Before(end) {
		end = d
	}

	// fallback is the dump stage, or, when the dump is not allowed (D17), the fixed
	// failure the caller counts as a read without an observation.
	fallback := func(reason string) ([]byte, []string, error) {
		if !allowDump {
			return nil, nil, dumpFailed("the UI reader could not run and uiautomator dump is not used after an action, which would suspend the device's other accessibility services: " + reason)
		}
		// The fallback and its reason go to the daemon log (plan m1n step 2): a read
		// that cost a dump is otherwise invisible to the runs that measure speed.
		a.logReaderFallback(t.serial, "uiautomator dump", reason)
		return a.readWithDump(ctx, t, start, reason)
	}

	// ADR-0029: a dex that is not the pinned one is never put on a device, so neither
	// reader can run; the dump still can.
	if got := readerDigest(); got != reader.SHA256 {
		reason := fmt.Sprintf("the embedded reader.dex has SHA-256 %s, not the pinned %s", got, reader.SHA256)
		return fallback(reason)
	}

	c := a.reader(t.serial)
	lctx, cancel := context.WithDeadline(ctx, end)
	lerr := c.acquire(lctx)
	cancel()
	if lerr != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
		return nil, nil, dumpBusy(fmt.Sprintf("the device's UI reader was busy for longer than the %s read deadline", dumpDeadline))
	}
	defer c.release()

	doc, kind, line, reason, _, rf := a.persistentRead(ctx, t, c, start, end, idle)
	if cerr := ctx.Err(); cerr != nil {
		return nil, nil, cerr
	}
	if kind == dumpAppBusy {
		// The app did not answer within the reader's device-side bound (ADR-0034 §1; plan
		// m1n step 3): the reader answered, so it is alive with its UiAutomation held.
		// Restarting it would stop the very reader whose ping works, and a fallback reader
		// or `uiautomator dump` would suspend the device's other accessibility services for
		// a read that answers at once. So this is the failure: a retry in a few seconds
		// re-reads the same reader. The package in Details comes from the window manager's
		// focus line, not the reader's busy line, which names the last app it read and is
		// stale when the foreground changed since (appBusyError).
		return nil, nil, a.appBusyError(ctx, t)
	}
	if kind == dumpVanished || kind == dumpUnreachable {
		// A reader found gone, a wedged one, or one behind a lost forward is restarted
		// once (ADR-0030; plan m1f step 7). The restart and its reason go to the daemon
		// log (plan m1n step 2), so a run can tell why a read cost a start: Q2 ¶1's
		// decision rests on distinguishing a timeout from an eof.
		a.logReaderRestart(t.serial, rf, reason)
		// restartReader reconciles the forward, kills an unreachable reader of our name
		// by its pid, and starts once.
		if ok, why := a.restartReader(ctx, t, c, end); ok {
			doc, kind, line, reason, _, _ = a.persistentRead(ctx, t, c, start, end, idle)
		} else {
			// The restart did not bring a reader up: its reason (a ping that went
			// unanswered, most often) is logged as the restart's own reason.
			a.logReaderRestart(t.serial, rfNone, why)
			kind, reason = dumpOther, why+": "+reason
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
	}
	switch kind {
	case dumpOK:
		return doc, nil, nil
	case dumpGone:
		return nil, nil, disconnected(t.serial, line)
	case dumpNoRoot:
		// uiautomator dump would meet the same missing window, and suspend the services.
		// The reader's line is device text: the cause is the fixed sentence.
		return nil, nil, dumpFailed(readerNoRootMessage)
	case dumpAppBusy:
		// Reached by the read after a restart (ADR-0030): the first read timed out or met a
		// reader reused because it answered ping, and the read that follows the restart came
		// back busy. A busy answer is the failure, not a reason to fall back, so the one-shot
		// reader and `uiautomator dump` are not tried (ADR-0034 §1).
		return nil, nil, a.appBusyError(ctx, t)
	}

	// The one-shot reader of ADR-0029, with the persistent reader's reason (D3).
	a.logReaderFallback(t.serial, "the one-shot reader", reason)
	doc, kind, line, reason2 := a.runReader(ctx, t, end)
	if cerr := ctx.Err(); cerr != nil {
		return nil, nil, cerr
	}
	switch kind {
	case dumpOK:
		return doc, []string{oneShotWarning + reason}, nil
	case dumpGone:
		return nil, nil, disconnected(t.serial, line)
	case dumpNoRoot:
		return nil, nil, dumpFailed(readerNoRootMessage)
	}

	// uiautomator dump of ADR-0029, with its warning, which names both readers' reasons
	// when the one-shot failed too (D3); after an action the dump stage is refused
	// instead (D17, fallback).
	return fallback(readerFallbackReason(reason, reason2))
}

// readWithDump runs the dump stage and labels its result with the reader's reason.
func (a *ADB) readWithDump(ctx context.Context, t *target, start time.Time, reason string) ([]byte, []string, error) {
	doc, err := a.dumpFrom(ctx, t, start)
	if err != nil {
		return nil, nil, err
	}
	return doc, []string{fallbackWarning + " Reader: " + reason}, nil
}

// runReader pushes the embedded reader to the one-shot path, runs it with app_process
// and removes it. It uses readerRemoteOneShot, never the persistent reader's
// readerRemote: the removal must not take away the dex the persistent reader starts
// from (ADR-0030). The file is pushed on every read and removed after it, so the device
// keeps nothing between reads: a copy left by a killed daemon is overwritten, never
// trusted, and no check of its content is needed. The removal is issued whenever the
// push was, and it ends by end: the work deadline is end minus cleanupReserve, and the
// removal's own deadline is end, which readChain has already clamped to the caller's
// phase deadline when that was earlier (plan m1f step 6, fix round). The removal
// survives the caller's cancellation.
//
// The last result is the reason shown in the fallback warning: the reader's own line or
// adb's, never device text (see readerReason).
func (a *ADB) runReader(parent context.Context, t *target, end time.Time) ([]byte, dumpKind, string, string) {
	// The digest is checked before every push (ADR-0029): a dex that is not the pinned
	// one is never put on a device.
	if got := readerDigest(); got != reader.SHA256 {
		msg := fmt.Sprintf("the embedded reader.dex has SHA-256 %s, not the pinned %s", got, reader.SHA256)
		return nil, dumpOther, msg, msg
	}
	ctx, cancel := context.WithDeadline(parent, end.Add(-cleanupReserve))
	defer cancel()
	dir, err := os.MkdirTemp("", "droidyard-reader-")
	if err != nil {
		return nil, dumpOther, "temporary directory: " + err.Error(), "temporary directory unavailable"
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "reader.dex")
	if err := os.WriteFile(local, reader.Dex(), 0o600); err != nil {
		return nil, dumpOther, "temporary file: " + err.Error(), "temporary file unavailable"
	}
	// A work context that is already expired issues neither the push nor the removal: the
	// removal is registered only once the push was issued, so a read whose budget is gone
	// never removes a file it never put on the device (plan m1f step 6, fix round).
	if err := ctx.Err(); err != nil {
		return nil, dumpOther, "the read's work deadline had already elapsed", "the read's work deadline had already elapsed"
	}
	const push = "push reader.dex"
	out, err := a.Run(ctx, t.adb, readerPush, 1<<10, "-s", t.serial, "push", local, readerRemoteOneShot)
	defer func() {
		rctx, cancel := context.WithDeadline(context.WithoutCancel(parent), end)
		defer cancel()
		a.Run(rctx, t.adb, cleanupReserve, 1<<10, "-s", t.serial, "shell", "rm", "-f", readerRemoteOneShot)
	}()
	if _, kind, line := classifyDump(t.serial, push, out, err, false); kind != dumpOK {
		return nil, kind, line, readerReason(out, err)
	}
	const run = "exec-out app_process " + reader.Class
	out, err = a.Run(ctx, t.adb, readerRun, dumpOutputCap, "-s", t.serial, "exec-out", "CLASSPATH="+readerRemoteOneShot, "app_process", "/system/bin", reader.Class)
	doc, kind, line := classifyDump(t.serial, run, out, err, true)
	if kind == dumpIdle {
		kind = dumpOther // the reader reads on a timeout; an idle message is not its retry
	}
	if kind == dumpOther && err == nil && out.ExitCode == readerNoRoot {
		return nil, dumpNoRoot, readerNoRootMessage, ""
	}
	if kind == dumpOK {
		return doc, kind, "", ""
	}
	return doc, kind, line, readerReason(out, err)
}

// readerReason says why a reader step failed, for the fallback warning: the reader's own
// line (prefix droidyard-reader:, found on stdout or stderr because exec-out merges
// them), else adb's last stderr line, else the exit status. The rest of stdout is device
// text and is never quoted; control characters are dropped and the line is cut at 200.
func readerReason(out Output, err error) string {
	if errors.Is(err, ErrTimeout) {
		return "it timed out"
	}
	if err != nil {
		return err.Error()
	}
	clean := func(l string) string {
		l = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, l))
		return truncate(l, 200)
	}
	for _, b := range [][]byte{out.Stderr, out.Stdout} {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, readerPrefix) {
				return clean(l)
			}
		}
	}
	if lines := strings.Split(strings.TrimSpace(string(out.Stderr)), "\n"); lines[len(lines)-1] != "" {
		return clean(lines[len(lines)-1])
	}
	return fmt.Sprintf("it exited with status %d and no hierarchy", out.ExitCode)
}

func dumpFailed(cause string) *capability.Error {
	return capability.NewError(ErrUIDumpFailed, "the device's UI could not be read").
		WithCause(cause).
		WithNextStep("Retry device.uiTree: nothing on the device was changed. If it keeps failing, the screen may be animating without pause or off: wake it, or wait for the animation to end.")
}

// dumpBusy is dumpFailed for a read that waited out its deadline behind another read of
// the same device. It keeps the code but not the dump's next step: nothing is wrong with
// the screen, another read is in progress.
func dumpBusy(cause string) *capability.Error {
	return capability.NewError(ErrUIDumpFailed, "the device's UI could not be read").
		WithCause(cause).
		WithNextStep("Retry device.uiTree: another read of this device is in progress and holds its UI reader; wait for it to finish, then retry.")
}

// appBusyError is the failed read of a busy app (ADR-0034 §1; plan m1n step 3): the
// reader answered `busy`, so the foreground app's main thread did not answer
// accessibility queries within the reader's device-side bound. It keeps
// DEVICE_UI_DUMP_FAILED but not the dump's next step, because the reader is alive and
// keeps its UiAutomation: nothing was restarted, nothing fell back, and a retry in a few
// seconds re-reads the same reader. The cause is fixed and never quotes device output.
//
// The package in Details is the window manager's current focus, read fresh here: the
// reader's busy line names the last app it read, which is stale when the foreground
// changed since (plan m1n step 3, fix round). That package is device text, so it goes to
// Details and marks the failed call untrusted (busyApp); when the focus cannot be read or
// parsed there is no package, and the failed call carries nothing from the device.
func (a *ADB) appBusyError(ctx context.Context, t *target) *capability.Error {
	e := capability.NewError(ErrUIDumpFailed, "the device's UI could not be read").
		WithCause(appBusyCause).
		WithNextStep("Retry device.uiTree in a few seconds: the foreground app is busy and did not answer, and the reader is still running; nothing on the device was changed.")
	if pkg := a.focusedPackage(ctx, t); pkg != "" {
		e = e.WithDetails(map[string]any{"package": pkg})
	}
	return e
}

// appBusyCause is the busy read's fixed cause (ADR-0034 §1); isAppBusy recognises the
// failure by it, with or without a package in Details.
const appBusyCause = "the foreground app did not answer accessibility queries"

// isAppBusy reports whether err is the busy read's failure (ADR-0034 §1).
func isAppBusy(err error) bool {
	e := capability.AsError(err)
	return e != nil && e.Code == ErrUIDumpFailed && e.Cause == appBusyCause
}

// busyFocusBound bounds the focus read of the busy path: one `dumpsys window`, kept short
// so a busy answer reaches the client promptly (plan m1n step 3, fix round).
const busyFocusBound = 2 * time.Second

// busyFocusCap bounds the focus read's output. `dumpsys window` is large, and the focus
// line is near its top, so the cap is far above the line and keeps a runaway read small.
const busyFocusCap = 4 << 20

// focusedPackage reads the window manager's current focus and returns its package, or ""
// when the line cannot be read or parsed (ADR-0034 §1; plan m1n step 3, fix round). It is
// device text only in the returned package: nothing else of the output leaves here, and an
// unparsed read yields no package rather than a guess.
func (a *ADB) focusedPackage(ctx context.Context, t *target) string {
	out, err := a.Run(ctx, t.adb, busyFocusBound, busyFocusCap, "-s", t.serial, "shell", "dumpsys", "window")
	if err != nil || out.ExitCode != 0 {
		return ""
	}
	pkg := focusedPackageFromDumpsys(string(out.Stdout))
	if !validPackageName(pkg) {
		return ""
	}
	return pkg
}

// focusedWindowRE matches `dumpsys window`'s current-focus line, whose window title is
// `<package>/<activity>`, `Application Not Responding: <package>` or `Application Error:
// <package>` (ADR-0034 §1; plan m1n step 3, fix round). The title is captured whole and
// the package taken from it by packageFromWindowTitle.
var focusedWindowRE = regexp.MustCompile(`mCurrentFocus=Window\{[^ ]+ u[0-9]+ (.+?)\}`)

// focusedPackageFromDumpsys extracts the focused package from `dumpsys window` output,
// returning "" when no focus line is present.
func focusedPackageFromDumpsys(out string) string {
	m := focusedWindowRE.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return packageFromWindowTitle(m[1])
}

// packageFromWindowTitle turns a window title into its package: `<package>/<activity>`
// and the `Application Not Responding: <package>` / `Application Error: <package>` forms
// of a dialog over the app (plan m1n step 3, fix round). A title of any other shape yields
// "".
func packageFromWindowTitle(title string) string {
	title = strings.TrimSpace(title)
	for _, prefix := range []string{"Application Not Responding: ", "Application Error: "} {
		if pkg, ok := strings.CutPrefix(title, prefix); ok {
			return pkg
		}
	}
	if pkg, _, ok := strings.Cut(title, "/"); ok {
		return pkg
	}
	return ""
}

// androidPackageRE is Android's package-name shape: at least two dot-separated segments,
// each starting with a letter (plan m1n step 3, fix round). A focused title that is not a
// package (a system window, a bare activity) is dropped rather than put in Details.
var androidPackageRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// validPackageName reports whether s is a well-formed Android package name (and within the
// 255-byte bound), so no other device text reaches Details (plan m1n step 3, fix round).
func validPackageName(s string) bool {
	return len(s) <= 255 && androidPackageRE.MatchString(s)
}

// busyApp reports whether err is appBusyError's failed read with a package in Details, so
// the handler marks the failed call untrusted: that package is device text (ADR-0034 §1;
// capability-api.md §Untrusted outputs). A busy read whose focus could not be read carries
// no package and is not marked: nothing from the device is in it. The check is structural
// because the busy read reuses DEVICE_UI_DUMP_FAILED.
func busyApp(err error) bool {
	e := capability.AsError(err)
	return e != nil && e.Code == ErrUIDumpFailed && e.Details["package"] != nil
}

// readerFallbackReason labels the dump's warning when both readers failed: the one-shot's
// reason, then the persistent reader's, so the warning names both (plan m1f, D3).
func readerFallbackReason(persistent, oneShot string) string {
	switch {
	case persistent == "":
		return oneShot
	case oneShot == "":
		return persistent
	default:
		return oneShot + " (the persistent reader: " + persistent + ")"
	}
}

type dumpKind int

const (
	dumpOK dumpKind = iota
	dumpIdle
	dumpGone
	dumpOther
	dumpNoRoot      // the reader found no window root: DEVICE_UI_DUMP_FAILED, no fallback
	dumpVanished    // the persistent reader's connection ended before a document: restart once
	dumpUnreachable // the persistent reader's cached forward did not answer or timed out: restart once
	dumpAppBusy     // the reader answered `busy`: the app did not answer, so no restart and no fallback (ADR-0034 §1)
)

// dump runs `uiautomator dump` on the device and returns the hierarchy document. It
// tries `exec-out uiautomator dump /dev/tty` first and, when that produces no
// hierarchy, the /sdcard path (dump, read, remove). One retry on "could not get idle
// state" per call, then DEVICE_UI_DUMP_FAILED; a serial that adb says is gone is
// DEVICE_DISCONNECTED, read from the command's own output. All of it within
// dumpDeadline; the caller's own cancellation is returned as it is.
func (a *ADB) dump(ctx context.Context, t *target) ([]byte, error) {
	return a.dumpFrom(ctx, t, time.Now())
}

// dumpFrom is dump with the deadline counted from start, so that a read that tried the
// reader first spends one budget, not two.
func (a *ADB) dumpFrom(ctx context.Context, t *target, start time.Time) ([]byte, error) {
	if dumpDisabled {
		return nil, dumpFailed("this build was compiled with -tags droidyard_nodump: uiautomator dump is disabled because it suspends the device's accessibility services (plan m1f, E3, D4)")
	}
	wctx, cancel := context.WithDeadline(ctx, start.Add(dumpDeadline-cleanupReserve))
	defer cancel()

	last := "no attempt ran"
	retried := false
	for path := 0; path < 2; {
		var doc []byte
		var kind dumpKind
		var line string
		if path == 0 {
			doc, kind, line = a.dumpTTY(wctx, t)
		} else {
			doc, kind, line = a.dumpSDCard(ctx, wctx, t, start.Add(dumpDeadline))
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		switch kind {
		case dumpOK:
			return doc, nil
		case dumpGone:
			return nil, disconnected(t.serial, line)
		case dumpIdle:
			if retried {
				return nil, dumpFailed(line)
			}
			retried = true
			last = line
			// Let the screen settle before the retry (800 ms, as codex-android-mcp does).
			select {
			case <-time.After(idleRetryPause):
			case <-wctx.Done():
			}
		default:
			path++
			last = line
		}
		if wctx.Err() != nil {
			return nil, dumpFailed(fmt.Sprintf("the %s dump deadline passed: %s", dumpDeadline, last))
		}
	}
	return nil, dumpFailed(last)
}

func (a *ADB) dumpTTY(ctx context.Context, t *target) ([]byte, dumpKind, string) {
	const cmd = "exec-out uiautomator dump /dev/tty"
	out, err := a.Run(ctx, t.adb, dumpAttempt, dumpOutputCap, "-s", t.serial, "exec-out", "uiautomator", "dump", "/dev/tty")
	return classifyDump(t.serial, cmd, out, err, true)
}

// dumpSDCard is the fallback: dump to a file on the device, read it, remove it. The
// removal is issued whenever the dump command ran, on a context that outlives the
// work deadline by cleanupReserve (parent carries only the caller's own cancellation).
func (a *ADB) dumpSDCard(parent, ctx context.Context, t *target, end time.Time) ([]byte, dumpKind, string) {
	const cmd = "exec-out uiautomator dump " + sdcardDump
	out, err := a.Run(ctx, t.adb, dumpAttempt, dumpOutputCap, "-s", t.serial, "exec-out", "uiautomator", "dump", sdcardDump)
	defer func() {
		// Not the caller's cancellation: a cancelled call still removes its file.
		rctx, cancel := context.WithDeadline(context.WithoutCancel(parent), end)
		defer cancel()
		a.Run(rctx, t.adb, cleanupReserve, 1<<10, "-s", t.serial, "shell", "rm", "-f", sdcardDump)
	}()
	if _, kind, line := classifyDump(t.serial, cmd, out, err, false); kind != dumpOK {
		return nil, kind, line
	}
	const cat = "exec-out cat " + sdcardDump
	out, err = a.Run(ctx, t.adb, dumpAttempt, dumpOutputCap, "-s", t.serial, "exec-out", "cat", sdcardDump)
	return classifyDump(t.serial, cat, out, err, true)
}

// classifyDump reads one command's result. With needDoc the command must have printed
// a complete hierarchy (a status line may follow it); without, it must only have
// succeeded (the dump to a file prints a status line). A document wins over any text
// in it: node text such as "device offline" is never read as an adb error.
func classifyDump(serial, cmd string, out Output, runErr error, needDoc bool) ([]byte, dumpKind, string) {
	if needDoc && !out.Truncated && bytes.Contains(out.Stdout, []byte("<hierarchy")) && bytes.Contains(out.Stdout, []byte("</hierarchy>")) {
		return out.Stdout, dumpOK, ""
	}
	if errors.Is(runErr, ErrTimeout) {
		return nil, dumpOther, "adb " + cmd + " timed out"
	}
	if runErr != nil {
		return nil, dumpOther, "adb " + cmd + ": " + runErr.Error()
	}
	if needDoc && bytes.Contains(out.Stdout, []byte("<hierarchy")) {
		// An incomplete document is device text: never scanned for adb's lines and
		// never quoted.
		return nil, dumpOther, fmt.Sprintf("adb %s: incomplete hierarchy (%d bytes)", cmd, len(out.Stdout))
	}
	if line := disconnectedLine(serial, out); line != "" {
		return nil, dumpGone, line
	}
	for _, b := range [][]byte{out.Stderr, out.Stdout} {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(strings.ToLower(l), idleFailure) {
				return nil, dumpIdle, "adb " + cmd + ": " + truncate(strings.TrimSpace(l), 200)
			}
		}
	}
	if !needDoc && out.ExitCode == 0 {
		return nil, dumpOK, ""
	}
	what := fmt.Sprintf("exited with status %d and no hierarchy", out.ExitCode)
	if out.Truncated {
		what = "printed more than the output limit"
	}
	if l := decisiveLine(out); l != "" {
		what += ": " + l
	}
	return nil, dumpOther, "adb " + cmd + " " + what
}
