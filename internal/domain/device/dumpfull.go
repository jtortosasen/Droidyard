package device

import (
	"context"
	"fmt"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The read chain of the full UI dump (ADR-0110). The document is the reader's `dump full
// [invisible]`, tried over the persistent reader and then the one-shot reader with the same
// words. It is never `uiautomator dump`, which lacks the extras (the document would change
// shape without notice) and suspends the device's other accessibility services (ADR-0029).

// fullOpts are the words of a full dump after `dump full`. The reader reads the active
// window only (ADR-0110 Decision 4); invisible adds the children not visible to the user.
type fullOpts struct {
	invisible bool
}

// words is the request after the verb, and the one-shot's arguments after the class name:
// `full` and, when asked, `invisible`.
func (o fullOpts) words() string {
	if o.invisible {
		return "full invisible"
	}
	return "full"
}

const (
	// fullBusyCause is the cause of a full dump the reader answered `busy` (ADR-0110 Decision
	// 5), its own and not ADR-0034 §1's "the foreground app did not answer": a large tree
	// can pass the bound with an app that answers every query. It names nothing from the
	// device, so the failed call is not untrusted.
	fullBusyCause = "the full dump did not complete within the reader's bound"
	// fullNoDumpCause starts the cause of a full dump neither reader could take; the readers'
	// reasons follow it.
	fullNoDumpCause = "the UI reader could not run, and a full dump never uses uiautomator dump, which lacks the extras and would suspend the device's other accessibility services: "
)

// fullOverCapReason is why a full document the reader printed past the read cap fails
// (ADR-0110 Decision 5): it names the cap, and nothing from the device.
var fullOverCapReason = fmt.Sprintf("the full dump is larger than the %d MiB the daemon reads", dumpOutputCap>>20)

// readSpec is what one pass of the read chain reads: the plain tree (allowDump says whether
// `uiautomator dump` may be the last stage, idle the reader's idle bound for a poll) or, with
// full set, the full document, which has no dump stage.
type readSpec struct {
	allowDump bool
	idle      *readerIdle
	full      *fullOpts
}

// oneShotArgs are the words the one-shot reader is run with: none for a plain read, `dump
// full [invisible]` for a full one.
func (s readSpec) oneShotArgs() []string {
	if s.full == nil {
		return nil
	}
	args := []string{"dump", "full"}
	if s.full.invisible {
		args = append(args, "invisible")
	}
	return args
}

// failed is the read's own failure: DEVICE_UI_DUMP_FAILED, with the next step of the
// capability that asked.
func (s readSpec) failed(cause string) *capability.Error {
	if s.full != nil {
		return fullDumpFailed(cause, "Retry device.uiDump: nothing on the device was changed. If it keeps failing, the screen may be animating without pause or off: wake it, or wait for the animation to end.")
	}
	return dumpFailed(cause)
}

// lockBusy is the failure of a read that waited out its deadline behind another read of the
// same device.
func (s readSpec) lockBusy(cause string) *capability.Error {
	if s.full != nil {
		return fullDumpFailed(cause, "Retry device.uiDump: another read of this device is in progress and holds its UI reader; wait for it to finish, then retry.")
	}
	return dumpBusy(cause)
}

// appBusy is the failure of a read the reader answered `busy` (ADR-0034 §1). A plain read
// names the foreground package from the window manager; a full dump says only that it did not
// complete in time, with no package, so it makes no further call and carries nothing from the
// device.
func (s readSpec) appBusy(ctx context.Context, a *ADB, t *target) *capability.Error {
	if s.full != nil {
		return fullDumpFailed(fullBusyCause, "Retry device.uiDump in a few seconds: the reader is still running and nothing on the device was changed. If it keeps failing the screen may be too large to read in time; device.uiTree reads a compact tree of it.")
	}
	return a.appBusyError(ctx, t)
}

// overCap is the failure of a full document that passed the read cap (ADR-0110 Decision 5):
// the cause names the cap. A retry reads the same document, so the next step does not offer
// one; nothing was stored. Only a full read can pass it (readerDumpRequest), so a plain read
// has no such failure.
func (s readSpec) overCap() *capability.Error {
	return fullDumpFailed(fullOverCapReason, "Retrying reads the same document. If invisible was set, call device.uiDump without it, which leaves out the off-screen children; or read a compact tree of the screen with device.uiTree.")
}

// fullDumpFailed is dumpFailed for the full dump: the same code and message, the next step
// of device.uiDump.
func fullDumpFailed(cause, nextStep string) *capability.Error {
	return capability.NewError(ErrUIDumpFailed, "the device's UI could not be read").
		WithCause(cause).
		WithNextStep(nextStep)
}

// readFull reads the active window's full document (ADR-0110): the persistent reader, then
// the one-shot reader with the same words, then DEVICE_UI_DUMP_FAILED. It never runs
// `uiautomator dump`. The bytes are the reader's, device text; the second result carries the
// one-shot warning when the persistent reader could not run. A document over the read cap
// (dumpOutputCap) fails DEVICE_UI_DUMP_FAILED at once with a cause that names the cap, from
// either reader, with no restart and no further reader (ADR-0110 Decision 5); no bytes are
// returned.
func (a *ADB) readFull(ctx context.Context, t *target, opts fullOpts) ([]byte, []string, error) {
	return a.readWith(ctx, t, readSpec{full: &opts})
}
