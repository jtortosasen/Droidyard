package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

func deviceVerb(pol *policy.Policy, args ...string) (code int, out, errs string) {
	var so, se bytes.Buffer
	code = deviceWith(pol, args, &so, &se)
	return code, so.String(), se.String()
}

// TestDeviceAllowCLI holds the owner's decision (plan m2u step 6; ADR-0015 §4, ADR-0032
// §5, §6): allow turns the physical-device policy on and lists the serial, disallow takes
// only the serial out, a no-op writes nothing, a bad serial is refused before any write,
// a concurrent edit is retried once, and the daemon sees the result without a restart.
// (Not parallel: the lock seam is global.)
func TestDeviceAllowCLI(t *testing.T) {
	t.Run("each verb's devices section", func(t *testing.T) {
		pol, _ := hostingPolicy(t)
		if _, err := pol.Update(context.Background(), updateOf(t, `{"shell":{"enabled":true}}`)); err != nil {
			t.Fatal(err)
		}
		if code, _, errs := deviceVerb(pol, "allow", "R58M"); code != 0 {
			t.Fatalf("allow exited %d: %s", code, errs)
		}
		if code, _, errs := deviceVerb(pol, "allow", "qa-lab/R99"); code != 0 {
			t.Fatalf("allow exited %d: %s", code, errs)
		}
		if got := pol.Get().Devices; !got.AllowPhysical || !reflect.DeepEqual(got.Allowlist, []string{"R58M", "qa-lab/R99"}) {
			t.Fatalf("devices after two allows = %+v", got)
		}
		if !pol.Get().Shell.Enabled {
			t.Error("allow lost the shell section")
		}
		if code, _, errs := deviceVerb(pol, "disallow", "R58M"); code != 0 {
			t.Fatalf("disallow exited %d: %s", code, errs)
		}
		// disallow leaves allowPhysical as it was.
		if got := pol.Get().Devices; !got.AllowPhysical || !reflect.DeepEqual(got.Allowlist, []string{"qa-lab/R99"}) {
			t.Fatalf("devices after disallow = %+v; want allowPhysical kept and only qa-lab/R99", got)
		}
	})

	t.Run("already allowed writes nothing", func(t *testing.T) {
		pol, _ := hostingPolicy(t)
		deviceVerb(pol, "allow", "R58M")
		rev := pol.Get().Revision
		code, out, _ := deviceVerb(pol, "allow", "R58M")
		if code != 0 || !strings.Contains(out, "already allowed") || pol.Get().Revision != rev {
			t.Errorf("exit %d, out %q, revision changed %v; want 0, 'already allowed', unchanged", code, out, pol.Get().Revision != rev)
		}
		code, out, _ = deviceVerb(pol, "disallow", "nobody")
		if code != 0 || !strings.Contains(out, "not allowed") || pol.Get().Revision != rev {
			t.Errorf("disallow of an unlisted serial: exit %d, out %q, revision changed %v", code, out, pol.Get().Revision != rev)
		}
	})

	t.Run("an empty serial is refused before any write", func(t *testing.T) {
		pol, _ := hostingPolicy(t)
		rev := pol.Get().Revision
		for _, args := range [][]string{{"allow", ""}, {"disallow", ""}, {"allow", "a b"}, {"allow"}, {"frob", "x"}, {}} {
			if code, _, _ := deviceVerb(pol, args...); code != 2 {
				t.Errorf("%q exited %d; want 2", args, code)
			}
		}
		if pol.Get().Revision != rev || pol.Get().Devices.AllowPhysical {
			t.Error("a refused command wrote the configuration")
		}
	})

	t.Run("a concurrent edit is retried once", func(t *testing.T) {
		pol, _ := hostingPolicy(t)
		var attempts atomic.Int32
		var nested atomic.Bool
		policy.SetLockSeam(func() {
			if nested.Swap(true) {
				return
			}
			defer nested.Store(false)
			if attempts.Add(1) == 1 {
				if _, err := pol.Update(context.Background(), updateOf(t, `{"devices":{"allowlist":["SER0"]}}`)); err != nil {
					t.Errorf("the competing write failed: %v", err)
				}
			}
		})
		t.Cleanup(func() { policy.SetLockSeam(nil) })
		code, _, errs := deviceVerb(pol, "allow", "R58M")
		if n := attempts.Load(); code != 0 || n != 2 {
			t.Fatalf("exit %d (%s), %d updates; want 0 and 2", code, errs, n)
		}
		if got := pol.Get().Devices; !got.AllowPhysical || !reflect.DeepEqual(got.Allowlist, []string{"SER0", "R58M"}) {
			t.Errorf("devices = %+v; want both the competing SER0 and R58M", got)
		}
	})

	t.Run("a running daemon lists the device at its next device.list", func(t *testing.T) {
		const phys = "R58M123"
		p := fakeADBDaemon(t, map[string]string{
			"devices.out": "List of devices attached\n" +
				"emulator-5554 device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n" +
				phys + " device usb:1-1.2 product:raven model:Pixel_6_Pro transport_id:2\n",
			"emulator-5554.getprop": fixtureFile(t, "pixel9a-getprop.txt"),
			"emulator-5554.avd":     fixtureFile(t, "pixel9a-emu-avd-name.txt"),
		})
		listed := func() bool {
			env, code := p.call(t, "device.list")
			if code != 0 || env.Operation == nil || env.Operation.State != capability.StateCompleted {
				t.Fatalf("device.list exited %d with %+v (error %+v)", code, env.Operation, env.Error)
			}
			for _, d := range listOf(t, env) {
				if d.Device == phys {
					return true
				}
			}
			return false
		}
		if listed() {
			t.Fatal("the physical device is listed before it is allowed")
		}
		env := platform.Env{GOOS: platform.Current().GOOS, UID: platform.Current().UID, Getenv: func(k string) string {
			if k == "DROIDYARD_STATE_DIR" {
				return p.state
			}
			return ""
		}}
		if code, _, errs := deviceVerb(policy.New(env), "allow", phys); code != 0 {
			t.Fatalf("allow exited %d: %s", code, errs)
		}
		if !listed() {
			t.Error("the physical device is not listed after device allow")
		}
	})
}
