package device

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// device.systemPanel, device.forceStop and device.rotate (plan m3g step 3; ADR-0133 §4, §5):
// system verbs an agent reaches over MCP with no view open. Each is a fixed command the
// daemon builds: the only caller text that reaches the device shell is a panel or a
// rotation from a closed list and an applicationId that matched applicationIDRE. All run
// through the device policy's admission as device.tapElement does, hold the device for an
// MCP session, and are lent to a peer (plain).

// ErrActionFailed is a system verb the device refused: a non-zero exit, or the command's own
// error line. The exit status is the cause and the command's output is in details, so the
// call is marked untrusted. DEVICE_ADB_FAILED means adb itself could not run (ADR-0133 §Alternatives considered).
const ErrActionFailed capability.ErrorCode = "DEVICE_ACTION_FAILED"

const systemVerbsSince = "0.40.0"

// The bounds of one system verb: each device command gets systemVerbTimeout, the whole call
// systemVerbRunningBound. Variables so a test injects a scale.
var (
	systemVerbTimeout      = 8 * time.Second
	systemVerbRunningBound = 30 * time.Second
)

// systemVerbOutputCap is how much of a command's output is read; systemVerbDetailBytes how
// much of it goes into an error's details.
const (
	systemVerbOutputCap   = 8 << 10
	systemVerbDetailBytes = 300
)

// systemVerbErrors are the codes a system verb can return besides its own.
var systemVerbErrors = append(append([]capability.ErrorCode{}, ActingErrors...),
	ErrActionFailed, capability.ErrInvalidParams, capability.ErrOperationQueueTimeout)

// The panels device.systemPanel opens, by name, and the `cmd statusbar` word that does it.
var panelCommands = map[string]string{
	"notifications": "expand-notifications",
	"quickSettings": "expand-settings",
	"none":          "collapse",
}

// SystemPanelParams is device.systemPanel's parameters.
type SystemPanelParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	Panel  string `json:"panel" jsonschema:"required,enum=notifications,enum=quickSettings,enum=none" jsonschema_description:"notifications opens the shade, quickSettings opens quick settings, none closes the open panel."`
}

// SystemPanelResult is device.systemPanel's result. It claims nothing about the panel:
// `cmd statusbar` exits 0 with no output even when it did nothing (a disabled status bar).
type SystemPanelResult struct {
	Panel string `json:"panel" jsonschema:"required" jsonschema_description:"The panel asked for, as sent. Not proof that it opened: read the screen."`
}

func (p SystemPanelParams) validate() error {
	if _, ok := panelCommands[p.Panel]; !ok {
		return capability.Errorf(capability.ErrInvalidParams, "panel %q is not one of notifications, quickSettings, none", p.Panel)
	}
	return nil
}

// ForceStopParams is device.forceStop's parameters.
type ForceStopParams struct {
	Device        string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	ApplicationID string `json:"applicationId" jsonschema:"required,pattern=^[A-Za-z][A-Za-z0-9_]*(\\.[A-Za-z][A-Za-z0-9_]*)+$" jsonschema_description:"The applicationId to stop: a package name, unmatched ids are rejected before the device is touched."`
}

// ForceStopResult is device.forceStop's result.
type ForceStopResult struct {
	ApplicationID string `json:"applicationId" jsonschema:"required"`
	WasRunning    bool   `json:"wasRunning" jsonschema:"required" jsonschema_description:"True when a process of the app was running just before the stop."`
}

func (p ForceStopParams) validate() error {
	if !applicationIDRE.MatchString(p.ApplicationID) {
		return capability.Errorf(capability.ErrInvalidParams, "applicationId %q is not a package name: use at least two dot-separated segments of letters, digits and underscores, starting with a letter", p.ApplicationID)
	}
	return nil
}

// The rotations device.rotate takes, by name: auto, or the user rotation's quarter turns.
var rotationTurns = map[string]int{"0": 0, "90": 1, "180": 2, "270": 3}

// RotateParams is device.rotate's parameters.
type RotateParams struct {
	Device   string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	Rotation string `json:"rotation" jsonschema:"required,enum=auto,enum=0,enum=90,enum=180,enum=270" jsonschema_description:"auto lets the sensor decide; 0, 90, 180 or 270 turns auto-rotate off and pins the screen to that angle."`
}

// RotationState is the screen rotation setting as it was read.
type RotationState struct {
	Rotation int  `json:"rotation" jsonschema:"required,minimum=0,maximum=270" jsonschema_description:"The pinned angle in degrees: 0, 90, 180 or 270. It applies when auto is false."`
	Auto     bool `json:"auto" jsonschema:"required" jsonschema_description:"True when auto-rotate was on."`
}

// RotateResult is device.rotate's result.
type RotateResult struct {
	Previous RotationState `json:"previous" jsonschema:"required" jsonschema_description:"The setting before this call, to restore it."`
	// Warnings are the daemon's own words, never device text.
	Warnings []string `json:"warnings,omitempty" jsonschema_description:"Set when a write failed after an earlier one had changed the device; it names what was written."`
}

func (p RotateParams) validate() error {
	if p.Rotation == "auto" {
		return nil
	}
	if _, ok := rotationTurns[p.Rotation]; !ok {
		return capability.Errorf(capability.ErrInvalidParams, "rotation %q is not one of auto, 0, 90, 180, 270", p.Rotation)
	}
	return nil
}

func registerSystemVerbs(reg *capability.Registry, adb *ADB) {
	notDestructive := func(reason string) capability.Annotations {
		return capability.Annotations{Destructive: boolPtr(false), Reason: reason}
	}
	resource := func(device func(any) string) func(*capability.Call) string {
		return func(call *capability.Call) string {
			if s := device(call.Params); s != "" {
				return writeKey(s)
			}
			return ""
		}
	}
	// refused marks the call untrusted when the device refused a command: the error's
	// details carry the command's own output.
	refused := func(call *capability.Call, err error) error {
		if e := capability.AsError(err); e != nil && e.Code == ErrActionFailed {
			yes := true
			call.Untrusted = &yes
		}
		return err
	}

	reg.MustRegister(adb.plain(capability.Descriptor{
		Name:         "device.systemPanel",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Since:        systemVerbsSince,
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Opens the notification shade or quick settings, or closes the open panel, with the device's status-bar command. It does not say a panel opened (a device with the status bar disabled ignores it): read the screen with device.uiTree.",
		Params:       SystemPanelParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(SystemPanelParams).Device) },
		Result:       SystemPanelResult{},
		Occupies:     capability.OccupiesAct,
		Idempotent:   true,
		Errors:       systemVerbErrors,
		Resource:     resource(func(p any) string { return p.(SystemPanelParams).Device }),
		Annotations:  notDestructive("it opens or closes a panel and changes no data (mcp.md §Annotations)"),
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(SystemPanelParams)
			// Validate before the hook so a malformed call runs no adb command at all.
			if err := p.validate(); err != nil {
				return nil, err
			}
			return adb.authorizeActing(ctx, call, p.Device, "device.systemPanel")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(SystemPanelParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			res, err := adb.systemPanel(ctx, call.Authorized.(*target), p)
			if err != nil {
				return nil, refused(call, err)
			}
			return *res, nil
		},
	}))

	reg.MustRegister(adb.plain(capability.Descriptor{
		Name:         "device.forceStop",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Since:        systemVerbsSince,
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Force-stops an application, which ends its processes and loses its unsaved in-memory state, and reports whether it was running. Repeating it is harmless.",
		Params:       ForceStopParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(ForceStopParams).Device) },
		Result:       ForceStopResult{},
		Occupies:     capability.OccupiesAct,
		Idempotent:   true,
		Errors:       systemVerbErrors,
		Resource:     resource(func(p any) string { return p.(ForceStopParams).Device }),
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(ForceStopParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			return adb.authorizeActing(ctx, call, p.Device, "device.forceStop")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(ForceStopParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			res, err := adb.forceStop(ctx, call.Authorized.(*target), p)
			if err != nil {
				return nil, refused(call, err)
			}
			return *res, nil
		},
	}))

	reg.MustRegister(adb.plain(capability.Descriptor{
		Name:         "device.rotate",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Since:        systemVerbsSince,
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Pins the screen to an angle with auto-rotate off, or turns auto-rotate back on, and returns the previous setting. An app that fixes its orientation keeps it, so read device.uiTree for the display.",
		Params:       RotateParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(RotateParams).Device) },
		Result:       RotateResult{},
		Occupies:     capability.OccupiesAct,
		Idempotent:   true,
		Errors:       systemVerbErrors,
		Resource:     resource(func(p any) string { return p.(RotateParams).Device }),
		Annotations:  notDestructive("it changes a display setting and no data, and the result returns the previous one (mcp.md §Annotations)"),
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(RotateParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			return adb.authorizeActing(ctx, call, p.Device, "device.rotate")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(RotateParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			res, err := adb.rotate(ctx, call.Authorized.(*target), p)
			if err != nil {
				return nil, refused(call, err)
			}
			return *res, nil
		},
	}))
}

// verbContext is the call's context under the running bound, counted from the start of
// the authorization hook as the other acting calls count theirs.
func verbContext(ctx context.Context, t *target) (context.Context, context.CancelFunc) {
	started := t.started
	if started.IsZero() {
		started = time.Now()
	}
	return context.WithDeadline(ctx, started.Add(systemVerbRunningBound))
}

// systemPanel runs `cmd statusbar <word>`. It claims nothing about the panel (spike
// agent-gestures (b): the command exits 0 with no output even when expansion is disabled).
func (a *ADB) systemPanel(ctx context.Context, t *target, p SystemPanelParams) (*SystemPanelResult, error) {
	actx, cancel := verbContext(ctx, t)
	defer cancel()
	if err := a.ready(actx, t); err != nil {
		return nil, err
	}
	if _, err := a.verbCommand(actx, t, "cmd statusbar", "shell", "cmd", "statusbar", panelCommands[p.Panel]); err != nil {
		return nil, err
	}
	return &SystemPanelResult{Panel: p.Panel}, nil
}

// forceStop asks `pidof` whether the app runs, then runs `am force-stop`. The pidof is only
// a read: a missing process exits non-zero with no output, and is not an error.
func (a *ADB) forceStop(ctx context.Context, t *target, p ForceStopParams) (*ForceStopResult, error) {
	actx, cancel := verbContext(ctx, t)
	defer cancel()
	if err := a.ready(actx, t); err != nil {
		return nil, err
	}
	pout, err := a.runShell(actx, t, systemVerbTimeout, 1<<10, "shell", "pidof", p.ApplicationID)
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return nil, timedOut("pidof")
		}
		return nil, err
	}
	_, running := firstPID(pout.Stdout)
	if _, err := a.verbCommand(actx, t, "am force-stop", "shell", "am", "force-stop", p.ApplicationID); err != nil {
		return nil, err
	}
	return &ForceStopResult{ApplicationID: p.ApplicationID, WasRunning: running}, nil
}

// rotate reads the two settings, then writes them: auto-rotate off before the user
// rotation, since the user rotation is ignored while the sensor decides. `auto` writes
// auto-rotate on and nothing else. A write that fails after an earlier one succeeded is a
// completed result that says what was written (ui-automation.md §Design rules).
func (a *ADB) rotate(ctx context.Context, t *target, p RotateParams) (*RotateResult, error) {
	actx, cancel := verbContext(ctx, t)
	defer cancel()
	if err := a.ready(actx, t); err != nil {
		return nil, err
	}
	autoNow, err := a.readSetting(actx, t, "accelerometer_rotation")
	if err != nil {
		return nil, err
	}
	turnsNow, err := a.readSetting(actx, t, "user_rotation")
	if err != nil {
		return nil, err
	}
	if (autoNow != 0 && autoNow != 1) || turnsNow < 0 || turnsNow > 3 {
		return nil, capability.NewError(ErrActionFailed, "the device's rotation settings have values this version cannot read").
			WithCause("accelerometer_rotation and user_rotation are not 0 or 1 and 0 to 3").
			WithNextStep("Read the display with device.uiTree; the rotation was not changed.")
	}
	res := &RotateResult{Previous: RotationState{Rotation: turnsNow * 90, Auto: autoNow == 1}}

	if p.Rotation == "auto" {
		if err := a.writeSetting(actx, t, "accelerometer_rotation", "1"); err != nil {
			return nil, err
		}
		return res, nil
	}
	if err := a.writeSetting(actx, t, "accelerometer_rotation", "0"); err != nil {
		return nil, err
	}
	if err := a.writeSetting(actx, t, "user_rotation", strconv.Itoa(rotationTurns[p.Rotation])); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		why := capability.AsError(err).Message
		if c := capability.AsError(err).Cause; c != "" {
			why = c
		}
		res.Warnings = []string{"accelerometer_rotation was set to 0 (auto-rotate is off), but user_rotation " + p.Rotation + " was not written: " + why}
	}
	return res, nil
}

// readSetting reads `settings get system <key>` as an integer. An unset key prints "null",
// which reads as 0: the default WindowManager's DisplayRotation reads both keys with, so a
// device that never rotated stays rotatable and its previous state is reported as it acts.
// Anything else that is not a number is a refusal before anything is written.
func (a *ADB) readSetting(ctx context.Context, t *target, key string) (int, error) {
	out, err := a.verbCommand(ctx, t, "settings get "+key, "shell", "settings", "get", "system", key)
	if err != nil {
		return 0, err
	}
	v := strings.TrimSpace(string(out.Stdout))
	if v == "null" {
		return 0, nil
	}
	n, perr := strconv.Atoi(v)
	if perr != nil {
		return 0, actionFailed("settings get "+key+" answered something this version cannot read", out).
			WithNextStep("Read the display with device.uiTree; the rotation was not changed.")
	}
	return n, nil
}

// writeSetting runs `settings put system <key> <value>`.
func (a *ADB) writeSetting(ctx context.Context, t *target, key, value string) error {
	_, err := a.verbCommand(ctx, t, "settings put "+key, "shell", "settings", "put", "system", key, value)
	return err
}

// verbCommand runs one device command of a system verb. A command that does not answer, exits
// non-zero or prints an error line is DEVICE_ACTION_FAILED; what adb itself could not do
// keeps runShell's codes.
func (a *ADB) verbCommand(ctx context.Context, t *target, what string, args ...string) (Output, error) {
	out, err := a.runShell(ctx, t, systemVerbTimeout, systemVerbOutputCap, args...)
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return out, timedOut(what)
		}
		return out, err
	}
	if out.ExitCode != 0 || errorLine(out) != "" {
		return out, actionFailed(what, out)
	}
	return out, nil
}

// timedOut is DEVICE_ACTION_FAILED for a command that did not answer in time.
func timedOut(what string) *capability.Error {
	return capability.NewError(ErrActionFailed, "the device did not finish the command").
		WithCause(what + " did not answer in " + systemVerbTimeout.String()).
		WithNextStep("Check the device with device.list, then retry; an earlier step of the call may have changed it.")
}

// actionFailed is DEVICE_ACTION_FAILED for a command the device refused: the exit status
// (or the fact that it printed an error) is the cause, and what it printed is in details,
// as data.
func actionFailed(what string, out Output) *capability.Error {
	cause := what + " reported an error"
	if out.ExitCode != 0 {
		cause = what + " exited with status " + strconv.Itoa(out.ExitCode)
	}
	return capability.NewError(ErrActionFailed, "the device refused the command").
		WithCause(cause).
		WithDetails(map[string]any{"output": truncateUTF8(strings.TrimSpace(string(out.Stderr)+"\n"+string(out.Stdout)), systemVerbDetailBytes)}).
		WithNextStep("Check the device's state and retry; the output in details is the device's own.")
}

// errorLine is the first line of a command's output that reports an error, the way `am`,
// `cmd` and `settings` do: it begins with "Error" or "Exception", or `cmd` found no such
// service or command. "" when there is none. `am` can print one with exit status 0.
func errorLine(out Output) string {
	for _, b := range [][]byte{out.Stdout, out.Stderr} {
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			low := strings.ToLower(l)
			if strings.HasPrefix(low, "error") || strings.HasPrefix(low, "exception") ||
				strings.Contains(low, "can't find service") || strings.Contains(low, "unknown command") {
				return truncate(l, 200)
			}
		}
	}
	return ""
}

// shellSingleQuote quotes s as one word for the device's shell: '…' with each ' written
// '\” (close the quote, an escaped quote, reopen). Inside single quotes the shell
// expands nothing, so the word reaches the command as the bytes it was given.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
