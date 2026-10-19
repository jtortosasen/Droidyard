package sharing

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// fakeDevices is the adb listing hosting.share reads.
type fakeDevices []device.ShareableDevice

func (f fakeDevices) Shareable(context.Context) ([]device.ShareableDevice, error) { return f, nil }

var phones = fakeDevices{
	{Device: "emulator-5554", State: "device", Kind: device.KindEmulator},
	{Device: "R1", State: "device", Kind: device.KindPhysical, Model: "Pixel"},
	{Device: "OFF", State: "offline", Kind: device.KindPhysical},
	{Device: "DUP", State: "device", Kind: device.KindPhysical, Duplicate: true},
	{Device: "DUP", State: "device", Kind: device.KindPhysical, Duplicate: true},
}

type lab struct {
	host *hosting.Host
	pol  *policy.Policy
	reg  *capability.Registry
	ops  *capability.Operations
}

// newLab is a Host with an identity over a real policy file, with the capabilities registered;
// ifaces is the machine it runs on. No listener is attached: Reconcile binds nothing.
func newLab(t *testing.T, config string, ifaces []hosting.Iface, join JoinFunc) *lab {
	t.Helper()
	state := t.TempDir()
	if config != "" {
		dir := filepath.Join(state, "config")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	pol := policy.New(env)
	h := hosting.New(pol, state, "0.0.0", nil)
	t.Cleanup(h.Uploads().Close)
	if err := h.Init(); err != nil {
		t.Fatal(err)
	}
	h.SetInterfaces(func() ([]hosting.Iface, error) { return ifaces, nil })
	reg := capability.NewRegistry()
	Register(reg, Options{Host: h, Devices: phones, API: "0.0.0", Join: join})
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	return &lab{host: h, pol: pol, reg: reg, ops: ops}
}

var loopback = []hosting.Iface{{Name: "lo0", Up: true, Addrs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}}}

// call runs a capability as the CLI and returns the error it failed with, if any.
func (l *lab) call(t *testing.T, name, params string) (any, *capability.Error) {
	t.Helper()
	d, _ := l.reg.Lookup(name)
	p, err := decode(d, params)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Handler(context.Background(), &capability.Call{Params: p})
	var ce *capability.Error
	if errors.As(err, &ce) {
		return nil, ce
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res, nil
}

func decodeJSON[T any](s string) (any, error) {
	var v T
	err := json.Unmarshal([]byte(s), &v)
	return v, err
}

func decode(d capability.Descriptor, params string) (any, error) {
	switch d.Name {
	case CapShare:
		return decodeJSON[ShareParams](params)
	case CapUnshare:
		return decodeJSON[UnshareParams](params)
	}
	return decodeJSON[JoinParams](params)
}

// TestShareRefusalsWriteNothing: the device is judged and the addresses found before a byte
// of configuration is written (plan m2u step 3): an emulator or an absent serial is
// DEVICE_NOT_FOUND ("only physical devices are shared"), an offline one DEVICE_NOT_READY, a
// serial two devices report DEVICE_NOT_ALLOWED, a machine with no address `--on` allows is
// HOSTING_UNAVAILABLE, and a bad `on` is REQUEST_INVALID_PARAMS; the configuration file does
// not exist afterwards in any of them. Reversing the order (writing first) leaves a file.
func TestShareRefusalsWriteNothing(t *testing.T) {
	for _, row := range []struct {
		name, params string
		ifaces       []hosting.Iface
		code         capability.ErrorCode
	}{
		{"an emulator", `{"device":"emulator-5554","detach":true,"on":"127.0.0.1"}`, loopback, device.ErrNotFound},
		{"an absent serial", `{"device":"nosuch","detach":true,"on":"127.0.0.1"}`, loopback, device.ErrNotFound},
		{"an offline phone", `{"device":"OFF","detach":true,"on":"127.0.0.1"}`, loopback, device.ErrNotReady},
		{"a duplicated serial", `{"device":"DUP","detach":true,"on":"127.0.0.1"}`, loopback, device.ErrNotAllowed},
		{"no address on allows", `{"device":"R1","detach":true,"on":"127.0.0.1"}`, nil, ErrUnavailable},
		{"a public on", `{"device":"R1","detach":true,"on":"8.8.8.8"}`, loopback, capability.ErrInvalidParams},
		{"a foreground share", `{"device":"R1","detach":false,"on":"127.0.0.1"}`, loopback, capability.ErrInvalidParams},
	} {
		t.Run(row.name, func(t *testing.T) {
			l := newLab(t, "", row.ifaces, nil)
			_, ce := l.call(t, CapShare, row.params)
			if ce == nil || ce.Code != row.code {
				t.Fatalf("error %+v; want %s", ce, row.code)
			}
			if got := l.pol.Get(); got.State != policy.StateMissing {
				t.Errorf("the refused share wrote the configuration: %+v", got)
			}
		})
	}
}

// TestShareThatCannotListenIsTakenBack: the lending a share wrote is removed again when no
// address is bound after Reconcile, and the call fails HOSTING_UNAVAILABLE, with each bind
// error in the details when there are some (ADR-0108; plan m2u step 3). A share of a device
// that was already shared keeps its entry: it is not this call's to take back. The lab has no
// listeners, so nothing is ever bound.
func TestShareThatCannotListenIsTakenBack(t *testing.T) {
	l := newLab(t, "", loopback, nil)
	_, ce := l.call(t, CapShare, `{"device":"R1","detach":true,"on":"127.0.0.1"}`)
	if ce == nil || ce.Code != ErrUnavailable {
		t.Fatalf("error %+v; want HOSTING_UNAVAILABLE", ce)
	}
	got := l.pol.Get()
	if len(got.Hosting.Lend) != 0 {
		t.Errorf("the lend entry stayed: %+v", got.Hosting.Lend)
	}
	if !got.Devices.AllowPhysical || got.Hosting.On != "127.0.0.1" {
		t.Errorf("devices %+v hosting %+v; the rest of the share's write stays (only the lending is taken back)", got.Devices, got.Hosting)
	}

	// A share that was already there stays.
	l = newLab(t, `{"hosting":{"lend":[{"device":"R1","to":["anna"]}]}}`, loopback, nil)
	if _, ce := l.call(t, CapShare, `{"device":"R1","detach":true,"on":"127.0.0.1"}`); ce == nil {
		t.Fatal("a share that listens nowhere succeeded")
	}
	if lend := l.pol.Get().Hosting.Lend; len(lend) != 1 || len(lend[0].To) != 1 {
		t.Errorf("an earlier share was taken back: %+v", lend)
	}
}

// TestJoinReadsTheCodeFirst: a code that is not one is REQUEST_INVALID_PARAMS and nothing is
// dialled; an unusable configuration is POLICY_CONFIG_UNUSABLE and nothing is dialled; neither
// error quotes the pasted text, which may be a code.
func TestJoinReadsTheCodeFirst(t *testing.T) {
	dialled := false
	join := func(context.Context, *hosting.Identity, hosting.Code, string, string) (hosting.JoinResult, error) {
		dialled = true
		return hosting.JoinResult{}, errors.New("not expected")
	}
	l := newLab(t, "", loopback, join)
	secretLooking := "dy1.SECRETSECRETSECRET"
	_, ce := l.call(t, CapJoin, `{"code":"`+secretLooking+`"}`)
	if ce == nil || ce.Code != capability.ErrInvalidParams || strings.Contains(ce.Message+ce.Cause+ce.NextStep, "SECRET") {
		t.Errorf("a bad code: %+v; want REQUEST_INVALID_PARAMS that does not quote it", ce)
	}
	code := hosting.Code{Addrs: []hosting.Addr{{IP: netip.MustParseAddr("127.0.0.1"), Kind: hosting.KindLAN}}, Port: 1}
	code.Secret[0] = 1
	line, _ := code.Encode()
	bad := newLab(t, `{"hosting":{"lend":"x"}}`, loopback, join)
	if _, ce := bad.call(t, CapJoin, `{"code":"`+line+`"}`); ce == nil || ce.Code != capability.ErrPolicyConfigUnusable {
		t.Errorf("an unusable configuration: %+v; want POLICY_CONFIG_UNUSABLE", ce)
	}
	if dialled {
		t.Error("a join that could not be recorded dialled the host")
	}
}

// TestShareResultKeepsNoCode: the operation record of a share keeps no copy of the code once
// it was delivered (ADR-0107 §2: the code exists as the result and the parameter, nowhere else).
func TestShareResultKeepsNoCode(t *testing.T) {
	d, _ := newLab(t, "", loopback, nil).reg.Lookup(CapShare)
	slim := d.Slim(ShareResult{Device: "R1", Code: "dy1.abc", Port: 5})
	if r, ok := slim.(ShareResult); !ok || r.Code != "" || r.Device != "R1" {
		t.Errorf("Slim = %+v; want the result without its code", slim)
	}
}

// TestValidSerial pins the rule for a device the host names (plan m2u step 3; ADR-0100 §4).
func TestValidSerial(t *testing.T) {
	for _, s := range []string{"R58M001", "emulator-5554", "192.168.0.5:5555", strings.Repeat("a", 64)} {
		if !validSerial(s) {
			t.Errorf("%q refused; it is a serial", s)
		}
	}
	for _, s := range []string{"", "a/b", "a b", "a\tb", "a\nb", "a\x1bb", "a​b ", strings.Repeat("a", 65), "\xff"} {
		if s == "a​b " {
			s = "a b" // a Unicode space separator
		}
		if validSerial(s) {
			t.Errorf("%q accepted; the host's text is untrusted", s)
		}
	}
}
