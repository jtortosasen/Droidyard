package emulator

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// launcherOutputCap bounds `emulator -list-avds`'s output; a listing is a few short lines.
const launcherOutputCap = 64 << 10

// AVDConfig is an AVD's configuration, as emulator.list reports it (emulator.md
// §Capabilities, plan m1i D13). target comes from the AVD's entry, the rest from its
// config.ini; a field config.ini does not carry is absent, not empty-string.
type AVDConfig struct {
	Target        string `json:"target,omitempty" jsonschema_description:"The API level or codename the AVD targets, from its .ini entry."`
	ABI           string `json:"abi,omitempty" jsonschema_description:"config.ini's abi.type, e.g. arm64-v8a."`
	DeviceProfile string `json:"deviceProfile,omitempty" jsonschema_description:"config.ini's hw.device.name, e.g. pixel_9a."`
	DisplayName   string `json:"displayName,omitempty" jsonschema_description:"config.ini's avd.ini.displayname, the human name."`
	// Settings are the hardware settings config.ini holds in a form emulator.configure and
	// emulator.create write (settings.go); a key it lacks or holds in another form is left out.
	Settings *Settings `json:"settings,omitempty" jsonschema_description:"The AVD's hardware settings as config.ini holds them, the fields emulator.configure takes."`
}

// Running is the device running an AVD: its serial, the value of every device argument.
type Running struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The running emulator's serial."`
}

// AVD is one AVD emulator.list reports. A name the launcher lists but cannot be located
// keeps configUnavailable with the reason; it is never dropped (plan m1i, D13).
type AVD struct {
	Name    string     `json:"name" jsonschema:"required" jsonschema_description:"The AVD name, as the launcher lists it."`
	Running *Running   `json:"running,omitempty" jsonschema_description:"Present when the AVD is booted; the emulator's serial."`
	Config  *AVDConfig `json:"config,omitempty" jsonschema_description:"The AVD's configuration, when its .ini was found."`
	// configUnavailable is why the configuration could not be read. It names the
	// variables and the home default it looked in, never a concrete path.
	ConfigUnavailable string `json:"configUnavailable,omitempty" jsonschema_description:"Why the configuration is unavailable; it names no paths."`
}

// ListResult is emulator.list's result.
type ListResult struct {
	AVDs     []AVD    `json:"avds" jsonschema:"required"`
	Warnings []string `json:"warnings,omitempty"`
}

func registerList(reg *capability.Registry, e *Emulator) {
	reg.MustRegister(capability.Descriptor{
		Name:         "emulator.list",
		Kind:         capability.Query,
		Scope:        capability.Global,
		Since:        "0.8.0",
		Availability: []capability.Requirement{capability.RequiresEmulator, capability.RequiresADB},
		Description:  "Lists every AVD the emulator launcher knows, with its configuration and, when it is running, its device serial. Use it to pick the avd for emulator.start.",
		Params:       struct{}{},
		Result:       ListResult{},
		Errors:       []capability.ErrorCode{ErrLauncherNotFound, ErrLaunchFailed, device.ErrADBNotFound, device.ErrADBFailed},
		Handler: func(ctx context.Context, _ *capability.Call) (any, error) {
			return e.list(ctx)
		},
	})
}

// list resolves the launcher, asks it for the AVD names, and maps each name to its
// configuration and, when a running emulator reports it, its serial. A name is mapped to
// a serial through `emu avd name` on each online emulator; an emulator that does not
// answer leaves its AVD without running, which is the honest answer.
func (e *Emulator) list(ctx context.Context) (ListResult, error) {
	res := e.Resolve()
	if !res.Found {
		return ListResult{}, launcherNotFound(res, "emulator.list")
	}
	out, err := e.launcherRun(ctx, res.Path, listTimeout, launcherOutputCap, "-list-avds")
	if cerr := ctx.Err(); cerr != nil {
		return ListResult{}, cerr
	}
	if err != nil {
		return ListResult{}, launcherFailed(res.Path, "emulator -list-avds", out, err)
	}
	if out.ExitCode != 0 {
		return ListResult{}, launcherFailed(res.Path, "emulator -list-avds", out, nil)
	}
	names := parseListAvds(out.Stdout)

	devices, err := e.adb.Devices(ctx)
	if err != nil {
		return ListResult{}, err
	}
	serials := runningSerials(ctx, e.adb, devices)

	result := ListResult{AVDs: []AVD{}, Warnings: warnings(res.Warnings, e.adb.Check(ctx).Warnings)}
	for _, name := range names {
		cfg, why := readConfig(name, e.env)
		avd := AVD{Name: name}
		if why != "" {
			avd.ConfigUnavailable = why
		} else {
			avd.Config = &cfg
		}
		if serial, ok := serials[name]; ok {
			avd.Running = &Running{Device: serial}
		}
		result.AVDs = append(result.AVDs, avd)
	}
	return result, nil
}

// runningSerials maps an AVD name to the serial running it, for every online emulator
// whose console answers with a name.
func runningSerials(ctx context.Context, adb *device.ADB, devices []device.Device) map[string]string {
	byName := map[string]string{}
	for _, d := range devices {
		if d.Kind != device.KindEmulator || !d.Online {
			continue
		}
		if name := adb.AvdName(ctx, d.Device); name != "" {
			byName[name] = d.Device
		}
	}
	return byName
}

// avdNameLine is what an AVD name may contain; anything else on the launcher's stdout is
// its own INFO chatter, which is filtered, not kept as an AVD.
var avdNameLine = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// parseListAvds reads `emulator -list-avds`'s stdout: one AVD name per line, deduplicated
// in order, with the launcher's chatter ("INFO | …", blank lines, anything that is not a
// bare AVD name) dropped.
func parseListAvds(out []byte) []string {
	var names []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !avdNameLine.MatchString(line) || seen[line] {
			continue
		}
		seen[line] = true
		names = append(names, line)
	}
	return names
}

// launcherNotFound is EMULATOR_LAUNCHER_NOT_FOUND for a resolution that found nothing.
func launcherNotFound(res Resolution, again string) *capability.Error {
	return capability.NewError(ErrLauncherNotFound, "the emulator launcher was not found").
		WithCause(res.NotFound()).
		WithNextStep("Set ANDROID_HOME to an Android SDK with emulator/emulator, or put emulator on PATH; other device capabilities work without it. Then call " + again + " again.")
}

// launcherFailed is EMULATOR_LAUNCH_FAILED for a launcher that ran but did not answer
// usable. The cause names the command, its exit status or timeout and the decisive line.
func launcherFailed(path, command string, out device.Output, runErr error) *capability.Error {
	var what string
	switch {
	case errors.Is(runErr, device.ErrTimeout):
		what = "timed out"
	case runErr != nil:
		what = runErr.Error()
	default:
		what = fmt.Sprintf("exited with status %d", out.ExitCode)
	}
	cause := command + " " + what
	if line := lastLine(out.Stderr, out.Stdout); line != "" {
		cause += ": " + line
	}
	return capability.NewError(ErrLaunchFailed, "the emulator launcher failed").
		WithCause(cause).
		WithNextStep("Run `" + path + " -list-avds` in a terminal to see why.")
}

// warnings concatenates warning groups, dropping duplicates and empty entries.
func warnings(groups ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range groups {
		for _, w := range g {
			if w == "" || seen[w] {
				continue
			}
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// lastLine is the last non-empty line of the first non-empty stream.
func lastLine(streams ...[]byte) string {
	for _, b := range streams {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
			return truncate(l, 200)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
