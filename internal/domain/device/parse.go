package device

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Device states as `adb devices` reports them (adb-device.md §Device model).
const (
	StateDevice       = "device"
	StateOffline      = "offline"
	StateUnauthorized = "unauthorized"
	StateRecovery     = "recovery"
	StateSideload     = "sideload"
	StateUnknown      = "unknown"
)

// Kinds. physical is also what a device that cannot be classified is: the
// emulators-only default never admits an unknown device (adb-device.md §Device model).
const (
	KindEmulator = "emulator"
	KindPhysical = "physical"
)

// Device is one entry of `adb devices -l`, classified, with the best-effort details
// getprop and the emulator console add.
type Device struct {
	Device         string      `json:"device" jsonschema:"required" jsonschema_description:"The serial: the device's identity, and the value of every device argument."`
	State          string      `json:"state" jsonschema:"required,enum=device,enum=offline,enum=unauthorized,enum=recovery,enum=sideload,enum=unknown"`
	Kind           string      `json:"kind" jsonschema:"required,enum=emulator,enum=physical"`
	Online         bool        `json:"online" jsonschema:"required" jsonschema_description:"True when the state is device."`
	Product        string      `json:"product,omitempty"`
	Model          string      `json:"model,omitempty"`
	TransportID    string      `json:"transportId,omitempty"`
	AndroidVersion string      `json:"androidVersion,omitempty" jsonschema_description:"Best effort, from getprop; absent when the device did not answer in time."`
	SDK            int         `json:"sdk,omitempty" jsonschema_description:"The API level; best effort, as androidVersion."`
	AvdName        string      `json:"avdName,omitempty" jsonschema_description:"The emulator's AVD name; best effort, as androidVersion."`
	OccupiedBy     *OccupiedBy `json:"occupiedBy,omitempty" jsonschema_description:"Another chat that holds this device: its install, uninstall, launch, tap through an element, text input and emulator stop or wipe from this session are refused unless the configured conflict is allow, and an idle holder (stale) is taken over by an acting call. Absent when none does, when this session or a session of its chat holds it, or when its holder is gone. Reading the screen, the tree or the logs is never refused."`
	Host           string      `json:"host,omitempty" jsonschema_description:"The name of the remote that lends the device, the prefix of its serial (<host>/<serial>); absent on a device of this machine."`

	usb bool // devices -l reported usb:
}

// ParseDevices reads the stdout of `adb devices -l`. adb's own notices ("* daemon not
// running; starting now") go to stderr and are never given to it; a line of that shape
// on stdout is skipped all the same. A listing without the header, or an entry that is
// not "<serial> <state> ...", is an error: the caller reports it as adb failing, not
// as an empty list.
func ParseDevices(out string) ([]Device, error) {
	devices := []Device{}
	header := false
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "* "):
			continue
		case strings.HasPrefix(line, "List of devices attached"):
			header = true
			continue
		case !header:
			return nil, fmt.Errorf("unexpected line before the device list: %q", line)
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return nil, fmt.Errorf("cannot parse device line %q", line)
		}
		d := Device{Device: f[0], State: normalState(f[1])}
		for _, kv := range f[2:] {
			k, v, ok := strings.Cut(kv, ":")
			if !ok {
				continue
			}
			switch k {
			case "product":
				d.Product = v
			case "model":
				d.Model = v
			case "transport_id":
				d.TransportID = v
			case "usb":
				d.usb = true
			}
		}
		d.Online = d.State == StateDevice
		d.Kind = classify(d)
		devices = append(devices, d)
	}
	if !header {
		return nil, fmt.Errorf("no \"List of devices attached\" header in %q", truncate(out, 120))
	}
	return devices, nil
}

func normalState(s string) string {
	switch s {
	case StateDevice, StateOffline, StateUnauthorized, StateRecovery, StateSideload:
		return s
	}
	return StateUnknown
}

var emulatorSerial = regexp.MustCompile(`^emulator-[0-9]+$`)

// IsEmulatorSerial reports whether a serial addresses an emulator's console
// (emulator-<port>), the only serial `adb emu` answers: emulator.stop refuses anything
// else with EMULATOR_NOT_AN_EMULATOR, before any console command (emulator.md §Stop).
func IsEmulatorSerial(serial string) bool { return emulatorSerial.MatchString(serial) }

// emulatorProducts are the products step 1 recorded from real emulators: sdk_gphone…
// for phones, sdk_gtablet… for the tablet.
var emulatorProducts = []string{"sdk_gphone", "sdk_gtablet"}

// loopbackSerial matches the serials a local emulator attaches under when it has no
// emulator-<port> serial: a loopback address and a port.
var loopbackSerial = regexp.MustCompile(`^(127\.0\.0\.1|localhost):[0-9]+$`)

// classify decides kind (adb-device.md §Device model; ADR-0103 §2): physical if devices -l
// reports usb:, whatever the product says; else emulator if the serial is emulator-<port>,
// or the product is a recorded emulator product and the serial is a loopback address, so a
// device on the network that reports an emulator's product is not taken for one; else
// physical, so the emulators-only default never admits what it cannot recognise.
func classify(d Device) string {
	if d.usb {
		return KindPhysical
	}
	if emulatorSerial.MatchString(d.Device) {
		return KindEmulator
	}
	if loopbackSerial.MatchString(d.Device) {
		for _, p := range emulatorProducts {
			if strings.HasPrefix(d.Product, p) {
				return KindEmulator
			}
		}
	}
	return KindPhysical
}

// propLine is one line of `getprop`: "[name]: [value]".
var propLine = regexp.MustCompile(`^\[([^\]]+)\]: \[(.*)\]$`)

// parseProps reads the two properties the details need from a getprop dump.
func parseProps(out string) (androidVersion string, sdk int) {
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		m := propLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		switch m[1] {
		case "ro.build.version.release":
			androidVersion = m[2]
		case "ro.build.version.sdk":
			sdk, _ = strconv.Atoi(m[2])
		}
	}
	return
}

// parseAvdName reads `emu avd name`: the name, then "OK". Anything else is no name.
func parseAvdName(out string) string {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	if len(lines) >= 2 && strings.TrimSpace(lines[1]) == "OK" {
		return strings.TrimSpace(lines[0])
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
