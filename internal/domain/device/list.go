package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// The codes device.list can return (adb-device.md §Failure modes; plan m1e, Q3).
const (
	ErrADBNotFound capability.ErrorCode = "DEVICE_ADB_NOT_FOUND"
	ErrADBFailed   capability.ErrorCode = "DEVICE_ADB_FAILED"
)

// devicesTimeout bounds `adb devices -l`; a variable so a test of the timeout is quick.
var devicesTimeout = 5 * time.Second

// detailsTimeout bounds all the details, in parallel, together; a variable so a test of
// the timeout is quick.
var detailsTimeout = 3 * time.Second

const (
	listOutputCap  = 64 << 10
	propsOutputCap = 128 << 10
	avdOutputCap   = 1 << 10
)

// ListResult is device.list's result.
type ListResult struct {
	Devices  []Device `json:"devices" jsonschema:"required"`
	Warnings []string `json:"warnings,omitempty"`
}

// Register adds the device capabilities to reg: device.list, device.uiTree and
// device.screenshot (which stores its image in store), device.tapElement,
// device.launch, device.logs (which stores its capture in store), device.watchLogs, device.watchScreen, device.install
// (which resolves its build reference through builds), device.uninstall and
// device.inputText, and the live view's input: device.tap, device.swipe and
// device.pressKey. builds may be nil in a descriptor-only registry, where
// device.install reports no build registry rather than panicking.
func Register(reg *capability.Registry, adb *ADB, store *artifact.Store, builds BuildResolver) {
	reg.MustRegister(capability.Descriptor{
		Name:         "device.list",
		Kind:         capability.Query,
		Scope:        capability.Global,
		Since:        "0.4.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Lists the connected emulators and the physical devices the policy admits, with serial (the value of every device argument), state, kind and model details. A physical device appears only when devices.allowPhysical is true and its serial is allowlisted.",
		Lendable:     true,
		Params:       struct{}{},
		Result:       ListResult{},
		Errors:       []capability.ErrorCode{ErrADBNotFound, ErrADBFailed},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			// The remotes are asked while this machine's own listing runs. A host lends its
			// own devices to a peer, never the ones it borrows.
			var collect func() []remoteAnswer
			if call.Origin.Client != capability.ClientPeer {
				collect = adb.startRemotes(ctx)
			}
			devices, warnings, err := adb.listFor(ctx, call.Origin)
			var ce *capability.Error
			if collect != nil && errors.As(err, &ce) && ce.Code == ErrADBNotFound {
				// No local adb is needed for a remote device (ADR-0100 §6): the remotes'
				// devices are listed, and the missing adb is a warning.
				devices, warnings, err = []Device{}, []string{"adb was not found on this machine, so its own devices are not listed: " + ce.Cause}, nil
			}
			if collect != nil {
				answers := collect()
				if err == nil {
					var remote bool
					if devices, warnings, remote = adb.withRemotes(answers, devices, warnings); remote {
						// What another machine wrote is untrusted as a whole (ADR-0100 §4).
						yes := true
						call.Untrusted = &yes
					}
				}
			}
			if err != nil {
				return nil, err
			}
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			return ListResult{Devices: devices, Warnings: warnings}, nil
		},
	})
	registerUITree(reg, adb)
	registerScreenshot(reg, adb, store)
	registerTapElement(reg, adb)
	registerLaunch(reg, adb)
	registerLogs(reg, adb, store)
	registerWatchLogs(reg, adb)
	registerWatchScreen(reg, adb)
	registerInstall(reg, adb, builds)
	registerInstallUpload(reg, adb)
	registerUninstall(reg, adb)
	registerInputText(reg, adb)
	registerScreenInput(reg, adb)
}

// List runs `adb devices -l`, drops what the policy hides (a physical device is listed
// only when its serial is admitted, ADR-0015 §4; plan m1k D7) and fills in the details of
// each online emulator, best effort, in parallel, under one deadline: a device that
// does not answer in time is listed with sparse details. The errors it returns are the
// contract's: *capability.Error with DEVICE_ADB_NOT_FOUND or DEVICE_ADB_FAILED, or
// ctx's own when the caller cancelled.
func (a *ADB) List(ctx context.Context) ([]Device, error) {
	devices, _, err := a.list(ctx)
	return devices, err
}

// list is List plus the adb client/server version-mismatch warnings (adb-device.md
// §Failure modes: reported on the device call as well as by system.health). It drops
// what the policy hides: a physical device is listed only when its serial is admitted
// (ADR-0015 §4; ADR-0032 §2; plan m1k D7), read live at each call.
func (a *ADB) list(ctx context.Context) ([]Device, []string, error) {
	return a.listFor(ctx, capability.Origin{})
}

// listFor is list for the caller who. A paired peer sees only the devices lent to it and
// none of the host's warnings (ADR-0099 §1). Anyone else sees this machine's devices,
// except that a serial two devices report is listed for neither, with a warning
// (ADR-0103 §1).
func (a *ADB) listFor(ctx context.Context, who capability.Origin) ([]Device, []string, error) {
	res := a.Resolve()
	if !res.Found {
		return nil, nil, adbNotFound(res, "device.list")
	}
	// Compares the client's version with the running server's before the first device
	// call, once per resolved binary: a mismatch is a warning on system.health, not an
	// error here (ratifications §Operational defaults).
	warnings := a.Check(ctx).Warnings

	listed, err := a.listAll(ctx, res.Path)
	if err != nil {
		return nil, nil, err
	}
	visible := listed[:0:0]
	if who.Client == capability.ClientPeer {
		for _, d := range listed {
			if a.lentTo(who.Name, d, listed) {
				visible = append(visible, d)
			}
		}
		warnings = nil
	} else {
		invalid := ""
		// Clipped, so the appends copy: the version check's own slice is shared.
		warnings = warnings[:len(warnings):len(warnings)]
		warned := map[string]bool{}
		for _, d := range listed {
			if d.Kind != KindEmulator {
				adm := a.admitted(d.Device)
				if !adm.Admitted {
					if adm.Invalid && invalid == "" {
						invalid = adm.Cause
					}
					continue
				}
			}
			if n := serialCount(listed, d.Device); n > 1 {
				// An emulator, or a physical device the policy admits: one that is hidden
				// stays undisclosed.
				if !warned[d.Device] {
					warned[d.Device] = true
					warnings = append(warnings, duplicateSerial(n, d.Device))
				}
				continue
			}
			visible = append(visible, d)
		}
		if invalid != "" {
			warnings = append(warnings, unusableWarning(invalid))
		}
	}
	a.fillDetails(ctx, res.Path, visible)
	if cerr := ctx.Err(); cerr != nil { // the caller gave up during the details phase
		return nil, nil, cerr
	}
	return visible, warnings, nil
}

// unusableWarning says a physical device is connected and hidden, and only because the
// configuration cannot be read: the listing would otherwise look as if nothing were
// plugged in. One warning however many, and no serial: a hidden device is not disclosed.
func unusableWarning(cause string) string {
	return "a physical device is connected but hidden, because the droidyard configuration could not be used (all physical devices stay hidden until it is fixed): " + cause
}

// fillDetails adds androidVersion, sdk and avdName to the online devices, in place.
func (a *ADB) fillDetails(ctx context.Context, adb string, devices []Device) {
	ctx, cancel := context.WithTimeout(ctx, detailsTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i := range devices {
		d := &devices[i]
		if d.State != StateDevice {
			continue
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			out, err := a.Run(ctx, adb, detailsTimeout, propsOutputCap, "-s", d.Device, "shell", "getprop")
			if err == nil && out.ExitCode == 0 {
				d.AndroidVersion, d.SDK = parseProps(string(out.Stdout))
			}
		}()
		go func() {
			defer wg.Done()
			if !emulatorSerial.MatchString(d.Device) {
				return // the emulator console is addressed by emulator-<port>
			}
			out, err := a.Run(ctx, adb, detailsTimeout, avdOutputCap, "-s", d.Device, "emu", "avd", "name")
			if err == nil && out.ExitCode == 0 {
				d.AvdName = parseAvdName(string(out.Stdout))
			}
		}()
	}
	wg.Wait()
}

// serverPort is the port adb's server uses for this environment.
func (a *ADB) serverPort() string {
	if p := envMap(a.env)["ANDROID_ADB_SERVER_PORT"]; p != "" {
		return p
	}
	return defaultServerPort
}

// adbFailed is DEVICE_ADB_FAILED for a command that did not run to a usable answer.
// The cause names the command, its exit status or timeout and the decisive line.
func adbFailed(path, port, command string, out Output, runErr error) *capability.Error {
	var what string
	switch {
	case errors.Is(runErr, ErrTimeout):
		what = "timed out"
	case runErr != nil:
		what = runErr.Error()
	default:
		what = fmt.Sprintf("exited with status %d", out.ExitCode)
	}
	cause := fmt.Sprintf("adb %s %s", command, what)
	if line := decisiveLine(out); line != "" {
		cause += ": " + line
	}
	return capability.NewError(ErrADBFailed, "adb failed on the host").
		WithCause(cause).
		WithNextStep("Run `" + path + " " + command + "` in a terminal to see why; a port " + port + " taken by another program stops the adb server from starting.")
}

// decisiveLine is the last non-empty line adb printed to stderr, else to stdout.
func decisiveLine(out Output) string {
	for _, b := range [][]byte{out.Stderr, out.Stdout} {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
			return truncate(l, 200)
		}
	}
	return ""
}

// adbNotFound is DEVICE_ADB_NOT_FOUND for a resolution that found nothing.
func adbNotFound(res Resolution, again string) *capability.Error {
	return capability.NewError(ErrADBNotFound, "adb was not found").
		WithCause(res.NotFound()).
		WithNextStep("Set `tools.adb` in the droidyard configuration to the adb binary, or ADB in the environment, or ANDROID_HOME to an Android SDK with platform-tools/adb, or put adb on PATH, then call " + again + " again.")
}

// listAll runs `adb devices -l` with the adb at path and parses it: every device adb
// reports, physical ones included. Its errors are the contract's DEVICE_ADB_FAILED, or
// ctx's own when the caller cancelled.
func (a *ADB) listAll(ctx context.Context, path string) ([]Device, error) {
	out, err := a.Run(ctx, path, devicesTimeout, listOutputCap, "devices", "-l")
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	if err != nil {
		return nil, adbFailed(path, a.serverPort(), "devices -l", out, err)
	}
	if out.ExitCode != 0 {
		return nil, adbFailed(path, a.serverPort(), "devices -l", out, nil)
	}
	listed, err := ParseDevices(string(out.Stdout))
	if err != nil {
		return nil, capability.NewError(ErrADBFailed, "adb devices -l answered something this version cannot read").
			WithCause(err.Error()).
			WithNextStep("Run `adb devices -l` yourself; if it works, report the output as a bug.")
	}
	return listed, nil
}
