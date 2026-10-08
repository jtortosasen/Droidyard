package device

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// getpropOutputCap bounds a readiness probe's output; `sys.boot_completed` is one digit.
const getpropOutputCap = 256

// What the emulator subsystem (internal/domain/emulator) needs from this package,
// exported rather than duplicated (plan m1i step 4): the device preamble's policy hook,
// the raw listing and the emulator console's AVD name. The emulator package owns no
// device logic, so these are the whole seam between the two.

// avdNameTimeout bounds `emu avd name`; a variable so a test of the timeout is quick.
var avdNameTimeout = 3 * time.Second

// Authorized is one device call's authorization hook, projected. It is what a caller
// that runs the hook but not the readiness check that follows it reads: emulator.stop
// must reach an emulator that is still booting or offline, which the readiness check
// refuses (emulator.md §Stop; plan m1i, B3, D12).
type Authorized struct {
	// ADB is the resolved adb binary the call's commands run with.
	ADB string
	// Serial is the serial the caller named.
	Serial string
	// Device is the serial's entry in the listing, nil when it is not listed.
	Device *Device
	// All is the whole `adb devices -l` listing the hook read, for the online
	// candidates an error reports.
	All []Device
	// Warnings are adb's (the client/server version mismatch).
	Warnings []string
}

// Authorize runs a device call's authorization hook over serial — the policy re-check
// and the call's one `devices -l` (ADR-0015 §4) — and returns what it read, without the
// readiness check a device capability runs next. A physical device the configuration does
// not admit is refused DEVICE_NOT_ALLOWED here, and a serial the listing does not hold is
// DEVICE_NOT_FOUND, exactly as a device call refuses them. again names the capability,
// for the error's nextStep.
func (a *ADB) Authorize(ctx context.Context, serial, again string) (*Authorized, error) {
	t, err := a.authorize(ctx, serial, again)
	if err != nil {
		return nil, err
	}
	return &Authorized{ADB: t.adb, Serial: t.serial, Device: t.dev, All: t.all, Warnings: t.warnings}, nil
}

// Devices reads `adb devices -l` raw: every device adb reports, physical ones included,
// with none of the details List adds. The emulator subsystem reads it for the online
// emulator serials and, while a stop waits, to see a serial leave. Its errors are
// DEVICE_ADB_NOT_FOUND and DEVICE_ADB_FAILED, as device.list's.
func (a *ADB) Devices(ctx context.Context) ([]Device, error) {
	res := a.Resolve()
	if !res.Found {
		return nil, adbNotFound(res, "emulator.list")
	}
	return a.listAll(ctx, res.Path)
}

// AvdName reads the AVD name a running emulator's console reports (`adb -s <serial> emu
// avd name`): the one way to map a serial to the AVD it runs (emulator.md
// §Capabilities). "" when adb cannot be found, the serial is not an emulator console, or
// the console does not answer, so a caller treats "" as "unknown", never as an error.
func (a *ADB) AvdName(ctx context.Context, serial string) string {
	if !emulatorSerial.MatchString(serial) {
		return ""
	}
	res := a.Resolve()
	if !res.Found {
		return ""
	}
	out, err := a.Run(ctx, res.Path, avdNameTimeout, avdOutputCap, "-s", serial, "emu", "avd", "name")
	if err != nil || out.ExitCode != 0 {
		return ""
	}
	return parseAvdName(string(out.Stdout))
}

// ADBPath resolves adb for a caller that runs it without the device preamble (the
// emulator subsystem's own stop on emulator.wipe's behalf, which holds no device key and
// runs no hook): the path, or DEVICE_ADB_NOT_FOUND naming what was tried, exactly as
// device.list's resolution answers it.
func (a *ADB) ADBPath(again string) (string, error) {
	res := a.Resolve()
	if !res.Found {
		return "", adbNotFound(res, again)
	}
	return res.Path, nil
}

// BootCompleted reads `-s <serial> shell getprop sys.boot_completed`: value is the
// trimmed property, "1" once the device finished booting; gone is true when adb says the
// serial is no longer there (the device disconnected mid-wait, which is
// DEVICE_DISCONNECTED); err is set when adb could not be run at all, which a readiness
// poll treats as "not yet", never as an error. It is the readiness signal of a boot and
// of an adoption (emulator.md §Boot sequence 3; plan m1i, D15).
func (a *ADB) BootCompleted(ctx context.Context, serial string) (value string, gone bool, err error) {
	res := a.Resolve()
	if !res.Found {
		return "", false, fmt.Errorf("adb was not found")
	}
	out, err := a.Run(ctx, res.Path, bootTimeout, getpropOutputCap, "-s", serial, "shell", "getprop", "sys.boot_completed")
	if err != nil {
		return "", false, err
	}
	if out.ExitCode != 0 {
		if line := disconnectedLine(serial, out); line != "" {
			// A booting emulator is listed offline for seconds (spike S8 G11), and adb
			// then says "device offline": that is not gone. Gone is absent from the
			// listing (level 4, step 5: a fresh boot failed DEVICE_DISCONNECTED).
			devs, derr := a.Devices(ctx)
			if derr != nil {
				// The listing could not be read: not the answer, so "not yet", not gone.
				return "", false, derr
			}
			for _, d := range devs {
				if d.Device == serial {
					return "", false, nil
				}
			}
			return "", true, nil
		}
	}
	return strings.TrimSpace(string(out.Stdout)), false, nil
}

// ServerListing asks the adb server listening on 127.0.0.1:port for its protocol version
// ("host:version") and its device listing ("host:devices-l") over its own socket. It
// spawns nothing and so starts no server: nothing listening, an answer that is not the
// protocol's, or ctx ending is an error. It is the read crash recovery judges an
// emulator launch by (plan m1p Q1 ¶2), the way serverVersion reads host:version.
func ServerListing(ctx context.Context, port int) (version string, devices []Device, err error) {
	if port < 1 || port > 65535 {
		return "", nil, fmt.Errorf("%d is not a port", port)
	}
	body, err := hostService(ctx, port, "host:version")
	if err != nil {
		return "", nil, err
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(body)), 16, 32)
	if err != nil {
		return "", nil, fmt.Errorf("host:version answered %q", truncate(string(body), 40))
	}
	body, err = hostService(ctx, port, "host:devices-l")
	if err != nil {
		return "", nil, err
	}
	// The service's payload is the listing without `adb devices`' header line.
	devices, err = ParseDevices("List of devices attached\n" + string(body))
	if err != nil {
		return "", nil, err
	}
	return strconv.FormatUint(v, 10), devices, nil
}

// hostService sends one smart-socket request to the server on port and returns the
// payload of its OKAY answer: four hex digits of length, then that many bytes. Its
// deadline is ctx's.
func hostService(ctx context.Context, port int, req string) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if _, err := fmt.Fprintf(conn, "%04x%s", len(req), req); err != nil {
		return nil, err
	}
	head := make([]byte, 8)
	if _, err := readFull(conn, head); err != nil {
		return nil, err
	}
	if string(head[:4]) != "OKAY" {
		return nil, fmt.Errorf("%s was not answered OKAY", req)
	}
	n, err := strconv.ParseUint(string(head[4:]), 16, 16)
	if err != nil {
		return nil, fmt.Errorf("%s answered a length that is not hex", req)
	}
	body := make([]byte, n)
	if _, err := readFull(conn, body); err != nil {
		return nil, err
	}
	return body, nil
}

// USB reports whether `adb devices -l` gave the entry a usb: field: a physical device,
// never an emulator's qemu.
func (d Device) USB() bool { return d.usb }

// ClientBuild is the protocol version the adb client speaks ("41" for 1.0.41), read with
// `adb version`, which starts no server; "" when adb is not found or does not say. A server
// whose host:version differs is one the client would restart.
func (a *ADB) ClientBuild(ctx context.Context) string {
	res := a.Resolve()
	if !res.Found {
		return ""
	}
	out, err := a.Run(ctx, res.Path, versionTimeout, versionOutputCap, "version")
	if err != nil || out.ExitCode != 0 {
		return ""
	}
	_, build := parseClientVersion(string(out.Stdout))
	return build
}

// OnPort is an ADB like a whose clients are pointed at the adb server on port, by setting
// ANDROID_ADB_SERVER_PORT in their environment. Nothing started through it starts a
// server, but only because the two commands crash recovery runs, `adb version` and `adb -s
// emulator-N emu ...`, never contact the server (emu talks to the emulator's console
// directly). Do not run any command that reaches the server (shell, devices, ...) through
// it: a server that died since the caller's socket read would be auto-started. a is unchanged.
func (a *ADB) OnPort(port int) *ADB {
	env := make([]string, 0, len(a.env)+1)
	for _, kv := range a.env {
		if !strings.HasPrefix(kv, "ANDROID_ADB_SERVER_PORT=") {
			env = append(env, kv)
		}
	}
	env = append(env, "ANDROID_ADB_SERVER_PORT="+strconv.Itoa(port))
	return &ADB{env: env, start: a.start, exec: a.exec, toolKey: a.toolKey}
}
