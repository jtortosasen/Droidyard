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
// device.screenshot, which stores its image in store.
func Register(reg *capability.Registry, adb *ADB, store *artifact.Store) {
	reg.MustRegister(capability.Descriptor{
		Name:         "device.list",
		Kind:         capability.Query,
		Scope:        capability.Global,
		Since:        "0.4.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Lists the connected emulators with their serial (the value of every device argument), state (device, offline, unauthorized, recovery, sideload, unknown), kind and, best effort, model, product, Android version, API level and AVD name. Physical devices are hidden by default. Fails with DEVICE_ADB_NOT_FOUND when adb cannot be found and DEVICE_ADB_FAILED when adb fails on the host.",
		Params:       struct{}{},
		Result:       ListResult{},
		Errors:       []capability.ErrorCode{ErrADBNotFound, ErrADBFailed},
		Handler: func(ctx context.Context, _ *capability.Call) (any, error) {
			devices, warnings, err := adb.list(ctx)
			if err != nil {
				return nil, err
			}
			return ListResult{Devices: devices, Warnings: warnings}, nil
		},
	})
	registerUITree(reg, adb)
	registerScreenshot(reg, adb, store)
}

// List runs `adb devices -l`, drops what the default policy hides (physical devices,
// ADR-0015 §4; no configuration file exists in 1e, plan R5) and fills in the details of
// each online emulator, best effort, in parallel, under one deadline: a device that
// does not answer in time is listed with sparse details. The errors it returns are the
// contract's: *capability.Error with DEVICE_ADB_NOT_FOUND or DEVICE_ADB_FAILED, or
// ctx's own when the caller cancelled.
func (a *ADB) List(ctx context.Context) ([]Device, error) {
	devices, _, err := a.list(ctx)
	return devices, err
}

// list is List plus the adb client/server version-mismatch warnings (adb-device.md
// §Failure modes: reported on the device call as well as by system.health).
func (a *ADB) list(ctx context.Context) ([]Device, []string, error) {
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
	for _, d := range listed {
		if d.Kind == KindEmulator { // R5: emulators only
			visible = append(visible, d)
		}
	}
	a.fillDetails(ctx, res.Path, visible)
	if cerr := ctx.Err(); cerr != nil { // the caller gave up during the details phase
		return nil, nil, cerr
	}
	return visible, warnings, nil
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
		WithNextStep("Set ADB to the adb binary, or ANDROID_HOME to an Android SDK with platform-tools/adb, or put adb on PATH, then call " + again + " again.")
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
