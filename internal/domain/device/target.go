package device

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// The codes of the device preamble every capability with a `device` argument runs
// (adb-device.md §Targeting, §Failure modes).
const (
	ErrNotFound     capability.ErrorCode = "DEVICE_NOT_FOUND"
	ErrNotReady     capability.ErrorCode = "DEVICE_NOT_READY"
	ErrNotAllowed   capability.ErrorCode = "DEVICE_NOT_ALLOWED"
	ErrDisconnected capability.ErrorCode = "DEVICE_DISCONNECTED"
)

// PreambleErrors lists the codes the preamble can return, for a descriptor's Errors.
var PreambleErrors = []capability.ErrorCode{ErrADBNotFound, ErrADBFailed, ErrNotFound, ErrNotReady, ErrNotAllowed, ErrDisconnected}

// bootTimeout bounds `getprop sys.boot_completed`; a variable so a test is quick.
var bootTimeout = 3 * time.Second

// target is what the authorization hook read and hands to the handler: the adb it
// resolved and the call's one `devices -l`.
type target struct {
	adb    string
	serial string
	dev    *Device // nil when the serial is not listed
	all    []Device
	// warnings are adb's (the client/server version mismatch), carried on the result
	// as device.list carries them (adb-device.md §Failure modes).
	warnings []string
	// started is when the authorization hook began, used as the clock of an
	// act-then-verify call's running bound (plan m1f, D13). The hook's `Check` and
	// `devices -l` alone can take seconds, so a bound counted from the handler's own
	// entry would be longer than the one the plan promises; recorded here instead.
	started time.Time
	// forPeer is set for a paired peer's call, peer being its configured name (ADR-0099
	// §1): its online candidates hold only what is lent to it.
	forPeer bool
	peer    string
}

// Policy is the physical-device policy seam (ADR-0015 §4; ADR-0032 §2; plan m1k D7):
// it decides whether a physical device's exact serial is admitted. *policy.Policy
// implements it, re-reading the configuration by size and mtime at each call. A nil
// policy is the emulators-only default that predates the configuration file, so every
// registry that sets none keeps that behaviour.
type Policy interface {
	PhysicalAdmitted(serial string) policy.Admission
}

// Lending is the host's lending seam (ADR-0099 §1): whether the device with this serial is
// lent to the named peer now. *policy.Policy implements it, re-reading the configuration at
// each call. That the device is physical and that the physical policy admits it are
// checked beside it, never by it.
type Lending interface {
	Lent(peer, serial string) bool
}

// authorize is the authorization hook of a device capability (plan m1e, Q2; m1k D7). It
// runs the call's one `devices -l` and decides DEVICE_NOT_ALLOWED for a physical device
// the policy does not admit (the emulators-only default when no policy is set), then
// hands the listing on. A string that is not a listed serial is DEVICE_NOT_FOUND here,
// before any handler runs, since adb's -s also accepts selectors (m1k step 5 B1).
//
// who is the call's origin. For a paired peer (ADR-0099 §1) a device that is not lent to
// it, that is not physical, that the host's own policy does not admit or that shares its
// serial is DEVICE_NOT_FOUND, as one that is not there, and the candidates hold only what
// is lent to it; one that is lent to it is leased to its link by this call (ADR-0104 §2).
// A serial that two devices report is DEVICE_NOT_ALLOWED for a local caller, for both
// devices (ADR-0103 §1).
func (a *ADB) authorize(ctx context.Context, who capability.Origin, serial, again string) (*target, error) {
	// D13: the act-then-verify clock is this hook's start, not the handler's entry.
	started := time.Now()
	res := a.Resolve()
	if !res.Found {
		return nil, adbNotFound(res, again)
	}
	// The client/server version comparison, once per resolved binary and before any
	// command that could restart the shared server; its warnings go on the result.
	warnings := a.Check(ctx).Warnings
	all, err := a.listAllTracked(ctx, res.Path)
	if err != nil {
		return nil, err
	}
	t := &target{adb: res.Path, serial: serial, all: all, warnings: warnings, started: started}
	if who.Client == capability.ClientPeer {
		t.forPeer, t.peer = true, who.Name
		// adb's version warning names the host's adb path and its tools.adb key: the
		// host's configuration, which a peer is not told, as device.list tells it none.
		t.warnings = nil
	}
	for i := range all {
		if all[i].Device == serial {
			t.dev = &all[i]
			break
		}
	}
	// A string that is not a listed serial is refused here, before any handler runs: adb's
	// -s also accepts selectors (usb:<path>, product:, model:, device:, a wireless host
	// without its port) that would reach a device under another name, past the policy
	// (m1k §Changes, step 5 B1).
	if t.dev == nil {
		// The serial is not listed: its transport is gone (or never was), so its cache
		// entry — boot state, SDK — is stale and dropped (D3).
		a.forgetDevice(serial)
		return nil, a.notFound(t)
	}
	if t.forPeer {
		if !a.lentTo(t.peer, *t.dev, all) {
			return nil, a.notFound(t)
		}
		// A peer's call of any kind leases the device it names (ADR-0104 §2), after the
		// lending check, so a device not lent to it never says who holds it.
		if err := a.leaseFor(who, serial); err != nil {
			return nil, err
		}
		return t, nil
	}
	if t.dev.Kind != KindEmulator {
		if adm := a.admitted(serial); !adm.Admitted {
			return nil, notAllowed(serial, adm.Cause)
		}
	}
	if n := serialCount(all, serial); n > 1 {
		return nil, notAllowed(serial, duplicateSerial(n, serial))
	}
	return t, nil
}

// serialCount is how many entries of the listing report serial.
func serialCount(all []Device, serial string) int {
	n := 0
	for _, d := range all {
		if d.Device == serial {
			n++
		}
	}
	return n
}

// duplicateSerial is the reason a serial that several devices report is refused (ADR-0103
// §1): the allowlist names serials, so one entry would admit all of them.
func duplicateSerial(n int, serial string) string {
	return fmt.Sprintf("%d devices report the serial %s; droidyard cannot tell them apart, so none of them is used until all but one is unplugged or its serial is changed", n, serial)
}

// lentTo reports whether d, as the listing all holds it, is one the host lends to the peer
// now (ADR-0099 §1): physical, alone under its serial, admitted by the host's own physical
// policy, and named for the peer in hosting.lend. Lending never widens the policy.
func (a *ADB) lentTo(peer string, d Device, all []Device) bool {
	if d.Kind != KindPhysical || serialCount(all, d.Device) != 1 {
		return false
	}
	if !a.admitted(d.Device).Admitted {
		return false
	}
	return a.lending != nil && a.lending.Lent(peer, d.Device)
}

// authorizeActing is authorize for a capability that acts on the device (Descriptor.Occupies
// is OccupiesAct): the check of ADR-0094 §2 follows the policy check and the listing, so a
// serial that is not listed or not admitted is refused as before, and nothing has changed on
// the device when DEVICE_OCCUPIED is returned.
func (a *ADB) authorizeActing(ctx context.Context, call *capability.Call, serial, again string) (*target, error) {
	t, err := a.authorize(ctx, call.Origin, serial, again)
	if err != nil {
		return nil, err
	}
	if err := a.OccupyFor(call, serial); err != nil {
		return nil, err
	}
	return t, nil
}

// notFound is DEVICE_NOT_FOUND for a serial the listing does not hold. Its online list
// names emulators and admitted physical devices only, so it never discloses one the
// configuration hides; for a peer it names only the devices lent to it (ADR-0099 §1). A
// serial that two devices report is named by neither.
func (a *ADB) notFound(t *target) error {
	online := []string{}
	for _, d := range t.all {
		if d.State != StateDevice {
			continue
		}
		if t.forPeer {
			if a.lentTo(t.peer, d, t.all) {
				online = append(online, d.Device)
			}
			continue
		}
		if serialCount(t.all, d.Device) > 1 {
			continue
		}
		if d.Kind == KindEmulator || a.admitted(d.Device).Admitted {
			online = append(online, d.Device)
		}
	}
	return capability.NewError(ErrNotFound, "no device "+t.serial+" is connected").
		WithDetails(map[string]any{"online": online}).
		WithNextStep("Call device.list for the serials that are connected; nothing on the device was changed, so this call is safe to repeat.")
}

// admitted consults the policy seam for a physical serial (ADR-0015 §4; ADR-0032 §2;
// plan m1k D7). The policy re-reads the configuration on every check, so a hand edit or
// a `droidyard trust` decides the next call without a restart. With no policy set — every
// test and registry that predates the configuration file — the emulators-only default
// refuses every physical device.
func (a *ADB) admitted(serial string) policy.Admission {
	if a.policy == nil {
		return policy.Admission{Cause: "the default policy admits emulators only; the device is a physical device"}
	}
	return a.policy.PhysicalAdmitted(serial)
}

// notAllowed is DEVICE_NOT_ALLOWED for a physical device the configuration does not
// admit; cause says which condition failed — `allowPhysical` is false, the serial is not
// in `devices.allowlist`, or the configuration file could not be used, by name (plan m1k
// D7). Its nextStep names both keys so a human can admit the serial.
func notAllowed(serial, cause string) *capability.Error {
	return capability.NewError(ErrNotAllowed, "the device "+serial+" is not permitted by policy").
		WithCause(cause).
		WithNextStep("use an emulator serial from device.list, or ask the user to admit " + serial + " in the droidyard configuration (`devices.allowPhysical` and `devices.allowlist`)")
}

// ready decides what the listing says about the device: DEVICE_NOT_FOUND with the
// online candidates, DEVICE_NOT_READY naming the state, and DEVICE_NOT_READY for a
// device that reports `device` but has not finished booting (sys.boot_completed not 1).
// Its online candidates are the devices the policy admits: every online emulator, and a
// physical device only when its serial is admitted (plan m1k D7).
func (a *ADB) ready(ctx context.Context, t *target) error {
	if t.dev == nil {
		return a.notFound(t)
	}
	if t.dev.State != StateDevice {
		fix := map[string]string{
			StateUnauthorized: "Accept the debugging prompt on the device, then retry.",
			StateOffline:      "Wait for the device to come back, or restart it, then retry.",
			StateRecovery:     "The device is in recovery mode: boot it normally, then retry.",
			StateSideload:     "The device is in sideload mode: boot it normally, then retry.",
		}[t.dev.State]
		if fix == "" {
			fix = "Call device.list until its state is device, then retry."
		}
		fix += " Nothing on the device was changed, so this call is safe to repeat."
		return capability.NewError(ErrNotReady, "the device "+t.serial+" is "+t.dev.State).
			WithDetails(map[string]any{"state": t.dev.State}).
			WithNextStep(fix)
	}
	// A device already known to have finished booting on this transport skips the
	// getprop (D3/A9): boot completion cannot un-happen while the transport is the same.
	if a.cachedBootCompleted(t.serial, t.dev.TransportID) {
		return nil
	}
	out, err := a.Run(ctx, t.adb, bootTimeout, 256, "-s", t.serial, "shell", "getprop", "sys.boot_completed")
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		what := err.Error()
		if !errors.Is(err, ErrTimeout) {
			// adb could not be started: a host failure, not a device that is not
			// answering (Q3).
			return capability.NewError(ErrADBFailed, "adb could not be run").
				WithCause("adb shell getprop sys.boot_completed: " + what).
				WithNextStep("Check that the adb binary is executable, then retry.")
		}
		what = "did not answer in " + bootTimeout.String()
		return capability.NewError(ErrNotReady, "the device "+t.serial+" is not answering").
			WithCause("adb shell getprop sys.boot_completed " + what).
			WithDetails(map[string]any{"state": "unresponsive"}).
			WithNextStep("Retry in a few seconds; if it persists, restart the device. Nothing on the device was changed, so this call is safe to repeat.")
	}
	if line := disconnectedLine(t.serial, out); line != "" {
		return disconnected(t.serial, line)
	}
	if out.ExitCode == 0 && strings.TrimSpace(string(out.Stdout)) == "1" {
		// Cache the answer, so the next call on this transport issues no getprop (D3).
		a.rememberBootCompleted(t.serial, t.dev.TransportID)
		return nil
	}
	cause := "sys.boot_completed is " + fmt.Sprintf("%q", strings.TrimSpace(string(out.Stdout)))
	if out.ExitCode != 0 {
		cause = fmt.Sprintf("adb shell getprop sys.boot_completed exited with status %d", out.ExitCode)
		if l := decisiveLine(out); l != "" {
			cause += ": " + l
		}
	}
	return capability.NewError(ErrNotReady, "the device "+t.serial+" is still booting").
		WithCause(cause).
		WithDetails(map[string]any{"state": "booting"}).
		WithNextStep("Wait until sys.boot_completed is 1 (a few seconds to a minute after boot), then retry. Nothing on the device was changed, so this call is safe to repeat.")
}

func disconnected(serial, line string) *capability.Error {
	return capability.NewError(ErrDisconnected, "the device "+serial+" disconnected during the call").
		WithCause(line).
		WithNextStep("Call device.list to see whether it is still there, then retry.")
}

// disconnectedLine returns the line of out's own stderr or stdout (exec-out passes the
// device's error text through stdout) that says the serial is gone, "" if none does:
// the call's own error, with no extra `devices -l`. adb says "device 'S' not found",
// "device offline" and "no devices/emulators found" when a transport drops.
func disconnectedLine(serial string, out Output) string {
	re := regexp.MustCompile(`(?i)device '` + regexp.QuoteMeta(serial) + `' (not found|offline)|device offline|no devices/emulators found`)
	for _, b := range [][]byte{out.Stderr, out.Stdout} {
		for _, l := range strings.Split(string(b), "\n") {
			if re.MatchString(l) {
				return truncate(strings.TrimSpace(l), 200)
			}
		}
	}
	return ""
}
