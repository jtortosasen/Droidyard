package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Remote devices end to end (ADR-0099, ADR-0100): two daemons, each in this process with
// its own state root, configuration and fake adb, paired over real TLS on loopback. The
// host's fake lists three phones and an emulator; the client's lists its own emulator.

const (
	hostEmulator = "emulator-5554"
	phone1       = "R58M001"
	phone2       = "R58M002"
	phone3       = "R58M003"
	remoteName   = "qa"
	remotePhone  = remoteName + "/" + phone1
)

// phoneListing is `adb devices -l` for the host: the emulator and the three usb phones.
func phoneListing() string {
	line := func(serial, usb string) string {
		return serial + " device " + usb + "product:raven model:Pixel_6_Pro transport_id:2\n"
	}
	return "List of devices attached\n" +
		hostEmulator + " device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n" +
		line(phone1, "usb:1-1.1 ") + line(phone2, "usb:1-1.2 ") + line(phone3, "usb:1-1.3 ")
}

// remoteLab is the pair. hostCalls and clientCalls read what each fake adb was asked.
type remoteLab struct {
	host, client       *inProcess
	hostDir, clientDir string
	stopHost           func()
	stopClient         func()
	hostConf           map[string]any // the host's configuration as written, for a test that edits it
	proxy              *linkProxy     // between the client and the host, with viaProxy
}

func callsIn(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, c := range devicetest.Calls(t, dir) {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

func (l *remoteLab) hostCalls(t *testing.T) []string   { return callsIn(t, l.hostDir) }
func (l *remoteLab) clientCalls(t *testing.T) []string { return callsIn(t, l.clientDir) }

// jsonObject renders a configuration file's body.
func jsonObject(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// clientConfig is the client's configuration: its devices policy and the one remote.
func clientConfig(t *testing.T, addr, hostFP string, allowPhysical bool, allowlist ...string) string {
	t.Helper()
	return jsonObject(t, map[string]any{
		"schemaVersion": 1,
		"devices":       map[string]any{"allowPhysical": allowPhysical, "allowlist": allowlist},
		"remotes":       []map[string]string{{"name": remoteName, "address": addr, "fingerprint": hostFP}},
	})
}

// fullAllowlist is the client's allowlist naming every id the host could show it, so that
// only the host's lending check keeps one out.
func fullAllowlist() []string {
	var ids []string
	for _, s := range []string{phone1, phone2, phone3, hostEmulator, "R58M999"} {
		ids = append(ids, remoteName+"/"+s)
	}
	return ids
}

// newRemoteLab starts the host and the client and waits for the link. The host lends
// phone1 to the client (as "anna") and, to show the lending check alone decides, phone2
// to another peer, phone3 to anna though the host's allowlist does not name it, and its
// emulator to anna though only physical devices are lent; the client's allowlist names
// every one of them.
func newRemoteLab(t *testing.T) *remoteLab { return newRemoteLabWith(t, labOptions{}) }

// labOptions vary the pair for the tests that need it.
type labOptions struct {
	// noClientADB gives the client no adb at all: nothing to find on this machine.
	noClientADB bool
	// hostBorrows makes the host a client of a remote "far" that never answers, with far/X1
	// in its own allowlist: the host borrows a device it must not lend on.
	hostBorrows bool
	// hostListDelay and clientListDelay are how long each fake's `devices -l` takes.
	hostListDelay, clientListDelay time.Duration
	// hostFiles are written to the host's fake adb directory after its defaults, so they
	// replace them (a live view's forward and window dump, for the stream tests).
	hostFiles map[string]string
	// hostProcesses runs the host's fake adb as real processes, not in this one, as the
	// live view's recorded server child needs (screen_test.go does the same).
	hostProcesses bool
	// extraPeers are peers the host pairs besides the client "anna", each lent phone1.
	extraPeers []labPeer
	// hostWrapStream wraps the recorded starter of the host's Logcat streams, so a test can
	// hold a child's reaping.
	hostWrapStream func(device.StreamStarter) device.StreamStarter
	// holdRelease, when set, holds every live view's wait for its source's teardown (on the host
	// and on the client, which share this process) until the channel is closed, which the lab
	// does at cleanup if the test has not (device.SetScreenReleaseHold, ADR-0123 §3).
	holdRelease chan struct{}
	// phone2ToAnna lends phone2 to the client as well as to bob.
	phone2ToAnna bool
	// viaProxy puts a TCP proxy between the client and the host (lab.proxy), so a test can cut
	// the link, and restore it, without stopping the host.
	viaProxy bool
}

// labPeer is a peer the host pairs: its name and certificate fingerprint.
type labPeer struct{ name, fingerprint string }

func newRemoteLabWith(t *testing.T, o labOptions) *remoteLab {
	t.Helper()
	lab := &remoteLab{hostDir: t.TempDir(), clientDir: t.TempDir()}

	hostFiles := map[string]string{"devices.out": phoneListing()}
	for _, s := range []string{hostEmulator, phone1, phone2, phone3} {
		hostFiles[s+".bootcompleted.out"] = "1\n"
		hostFiles[s+".getprop"] = fixtureFile(t, "pixel9a-getprop.txt")
		hostFiles[s+".dumptty.out"] = fixtureFile(t, "pixel9a-uidump-devtty.xml")
		hostFiles[s+".push.exit"] = "1" // the persistent reader cannot run: the dump path answers
	}
	hostFiles[phone1+".date.out"] = "1696118400.0\n"
	hostFiles[phone1+".monkey.out"] = ""
	hostFiles[phone1+".apppidof.out"] = "4242\n"
	// device.rotate reads the two rotation settings before it writes them.
	hostFiles[phone1+".get_accelerometer_rotation.out"] = "1\n"
	hostFiles[phone1+".get_user_rotation.out"] = "0\n"
	// The host's reader cannot run here, so its coordinate actions go by `input`, sized by this
	// screencap header (plan m3g step 4).
	hostFiles[phone1+".screenheader.out"] = "        1080        2400\n"
	hostFiles[phone1+".pmpath.out"] = "package:/data/app/x/base.apk\n"
	hostFiles[phone1+".uninstall.out"] = "Success\n"
	maps.Copy(hostFiles, o.hostFiles)
	for name, content := range hostFiles {
		devicetest.Write(t, lab.hostDir, name, []byte(content))
	}
	devicetest.Write(t, lab.clientDir, "devices.out", []byte("List of devices attached\nemulator-5556 device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"))
	devicetest.Write(t, lab.clientDir, "emulator-5556.bootcompleted.out", []byte("1\n"))
	if o.hostListDelay > 0 {
		devicetest.Write(t, lab.hostDir, "devices.delay", []byte(fmt.Sprint(o.hostListDelay.Milliseconds())))
	}
	if o.clientListDelay > 0 {
		devicetest.Write(t, lab.clientDir, "devices.delay", []byte(fmt.Sprint(o.clientListDelay.Milliseconds())))
	}

	// The client's identity first: the host pins it.
	var clientADB *adbSeam
	if !o.noClientADB {
		clientADB = &adbSeam{env: devicetest.Env(t, lab.clientDir, unusedPort(t)), exec: devicetest.Exec(lab.clientDir)}
	}
	lab.client = startInProcessADB(t, "127.0.0.1:0", nil, clientADB)
	if err := lab.client.host.Init(); err != nil {
		t.Fatal(err)
	}
	hostAllow := []string{phone1, phone2, hostEmulator}
	hostConf := map[string]any{}
	if o.hostBorrows {
		hostAllow = append(hostAllow, "far/X1")
		hostConf["remotes"] = []map[string]string{{"name": "far", "address": "127.0.0.1:1", "fingerprint": strings.Repeat("a", 64)}}
	}
	hostConf["schemaVersion"] = 1
	hostConf["devices"] = map[string]any{"allowPhysical": true, "allowlist": hostAllow}
	peers := []map[string]string{{"name": "anna", "fingerprint": lab.client.host.Identity().Fingerprint()}}
	phone1To := []string{"anna"}
	phone2To := []string{"bob"}
	if o.phone2ToAnna {
		phone2To = append(phone2To, "anna")
	}
	for _, p := range o.extraPeers {
		peers = append(peers, map[string]string{"name": p.name, "fingerprint": p.fingerprint})
		phone1To = append(phone1To, p.name)
	}
	lab.hostConf = hostConf
	hostConf["hosting"] = map[string]any{
		"listen": "127.0.0.1:0",
		"peers":  peers,
		"lend": []map[string]any{
			{"device": phone1, "to": phone1To},
			{"device": phone2, "to": phone2To},
			{"device": phone3, "to": []string{"anna"}},
			{"device": hostEmulator, "to": []string{"anna"}},
		},
	}
	hostConfig := jsonObject(t, hostConf)
	hostSeam := &adbSeam{env: devicetest.Env(t, lab.hostDir, unusedPort(t)), exec: devicetest.Exec(lab.hostDir)}
	hostSeam.wrapStream = o.hostWrapStream
	if hold := o.holdRelease; hold != nil {
		device.SetScreenReleaseHold(func() { <-hold })
		t.Cleanup(func() { device.SetScreenReleaseHold(nil) })
	}
	if o.hostProcesses {
		hostSeam.exec = nil // the fake adb as real processes, which the screen server's recorded child needs
		// A process this binary re-executes starts later on a loaded host than the host's
		// 3 s limits, which no test of a lab with processes claims.
		hostSeam.floor = adbFloorFor()
	}
	lab.host = startInProcessConfig(t, "127.0.0.1:0", nil, hostSeam, 0, hostConfig)
	stop := startPeers(lab.host.host, lab.host.reg, lab.host.ops, lab.host.bus, lab.host.store, lab.host.adb, lab.host.log)
	var once sync.Once
	lab.stopHost = func() { once.Do(stop) }
	t.Cleanup(lab.stopHost)
	addr := ""
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host's peer listener", func() bool {
		addr = lab.host.host.Health().Hosting.Listen
		return addr != ""
	})

	if o.viaProxy {
		lab.proxy = newLinkProxy(t, addr)
		addr = lab.proxy.addr
	}
	configAt(t, lab.client.state, clientConfig(t, addr, lab.host.host.Identity().Fingerprint(), true,
		fullAllowlist()...))
	stopClient := startPeers(lab.client.host, lab.client.reg, lab.client.ops, lab.client.bus, lab.client.store, lab.client.adb, os.Stderr)
	var onceClient sync.Once
	lab.stopClient = func() { onceClient.Do(stopClient) }
	t.Cleanup(lab.stopClient)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the client's link to come up", func() bool { return lab.client.host.Remote(remoteName).Connected() })
	if hold := o.holdRelease; hold != nil {
		// Registered last, so it runs first: the daemons' stop waits for the views' releases.
		t.Cleanup(func() {
			select {
			case <-hold:
			default:
				close(hold)
			}
		})
	}
	return lab
}

// rewriteClient replaces the client's configuration; the address and fingerprint it dialled
// stay, since remotes are read at start.
func (l *remoteLab) rewriteClient(t *testing.T, allowPhysical bool, allowlist ...string) {
	t.Helper()
	h := l.host.host.Health().Hosting
	configAt(t, l.client.state, clientConfig(t, h.Listen, l.host.host.Identity().Fingerprint(), allowPhysical, allowlist...))
}

func untrusted(env capability.ResponseEnvelope) bool { return env.Meta != nil && env.Meta.Untrusted }

// checkDelivery holds a relayed coordinate action's result to the delivery the host chose.
func checkDelivery(t *testing.T, result json.RawMessage, want string) {
	t.Helper()
	var r device.ScreenInputResult
	if err := json.Unmarshal(result, &r); err != nil || !r.Delivered || r.Delivery != want {
		t.Errorf("input result %s: %v; want delivered by %s", result, err, want)
	}
}

func listedIDs(rows []device.Device) []string {
	ids := make([]string, 0, len(rows))
	for _, d := range rows {
		ids = append(ids, d.Device)
	}
	return ids
}

// list is the client's device.list.
func (l *remoteLab) list(t *testing.T) (rows []device.Device, warnings []string, env capability.ResponseEnvelope) {
	t.Helper()
	env, _ = l.client.call(t, "device.list")
	var r device.ListResult
	if err := json.Unmarshal(env.Result, &r); err != nil {
		t.Fatalf("device.list: %s (%+v)", env.Result, env.Error)
	}
	return r.Devices, r.Warnings, env
}

// on runs capName on the client with params and returns the envelope.
func (l *remoteLab) on(t *testing.T, capName, params string) capability.ResponseEnvelope {
	t.Helper()
	env, _ := l.client.call(t, capName, "--params", params)
	return env
}

func dev(id string) string { return `"device":"` + id + `"` }

// serialsOf is every serial a fake's command lines address with -s.
func serialsOf(calls []string) []string {
	var out []string
	for _, c := range calls {
		if rest, ok := strings.CutPrefix(c, "-s "); ok {
			s, _, _ := strings.Cut(rest, " ")
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestRemoteDevices(t *testing.T) {
	t.Parallel()
	lab := newRemoteLab(t)

	// The listing: the client's own emulator and the one phone the host lends to it,
	// admitted by both policies, as <remote>/<serial> with host. Everything else the host
	// holds is absent: lent to another peer (phone2), lent but not in the host's
	// allowlist (phone3), an emulator named in lend (only physical devices are lent). The
	// client's allowlist names every one of them, so only the host's lending check is what
	// keeps them out; each is DEVICE_NOT_FOUND when named, and none is disclosed in the
	// candidates the error names.
	t.Run("lending decides what is listed and found", func(t *testing.T) {
		rows, warnings, env := lab.list(t)
		if got := listedIDs(rows); !slices.Equal(got, []string{"emulator-5556", remotePhone}) {
			t.Fatalf("device.list = %v (warnings %q); want the client's emulator and %s", got, warnings, remotePhone)
		}
		if rows[0].Host != "" || rows[1].Host != remoteName || rows[1].Kind != device.KindPhysical || !rows[1].Online {
			t.Errorf("rows = %+v; want host only on the remote one, which is a physical device and online", rows)
		}
		if !untrusted(env) {
			t.Errorf("a listing that holds another machine's rows is not marked untrusted: %+v", env.Meta)
		}
		for _, c := range []struct{ id, why string }{
			{remoteName + "/" + phone2, "lent to another peer"},
			{remoteName + "/" + phone3, "lent but not in the host's allowlist"},
			{remoteName + "/" + hostEmulator, "an emulator named in lend"},
			{remoteName + "/R58M999", "not connected"},
		} {
			env := lab.on(t, "device.uiTree", `{`+dev(c.id)+`}`)
			if env.Error == nil || env.Error.Code != device.ErrNotFound {
				t.Errorf("%s (%s): %+v; want DEVICE_NOT_FOUND", c.id, c.why, env.Error)
				continue
			}
			online, _ := env.Error.Details["online"].([]any)
			if len(online) != 1 || online[0] != remotePhone {
				t.Errorf("%s: online = %v; want only %s, as the client names it", c.id, online, remotePhone)
			}
			if !untrusted(env) {
				t.Errorf("%s: a host's error is not marked untrusted", c.id)
			}
		}
	})

	// emulator.* has no remote form: a remote id is a device that is not there (ADR-0100 §3).
	t.Run("emulator.stop on a remote id", func(t *testing.T) {
		hostBefore := len(lab.hostCalls(t))
		env := lab.on(t, "emulator.stop", `{`+dev(remotePhone)+`}`)
		if env.Error == nil || env.Error.Code != device.ErrNotFound {
			t.Errorf("emulator.stop on %s: %+v; want DEVICE_NOT_FOUND", remotePhone, env.Error)
		}
		if after := len(lab.hostCalls(t)); after != hostBefore {
			t.Errorf("emulator.stop reached the host: %d commands", after-hostBefore)
		}
	})

	// The client's own physical policy decides, before any host call (ADR-0100 §2): an id
	// the client's allowlist does not name, or any id while its allowPhysical is false, is
	// absent from the list and DEVICE_NOT_ALLOWED with the cause when named, and the host
	// is asked nothing for the call.
	t.Run("the client's own policy decides first", func(t *testing.T) {
		for _, c := range []struct {
			name          string
			allowPhysical bool
			allowlist     []string
			cause         string
		}{
			{"removed from the allowlist", true, []string{remoteName + "/" + phone2}, "not in devices.allowlist"},
			{"allowPhysical false", false, []string{remotePhone}, "allowPhysical is false"},
		} {
			lab.rewriteClient(t, c.allowPhysical, c.allowlist...)
			rows, _, _ := lab.list(t)
			if got := listedIDs(rows); !slices.Equal(got, []string{"emulator-5556"}) {
				t.Errorf("%s: device.list = %v; want the client's emulator only", c.name, got)
			}
			before := len(lab.hostCalls(t))
			env := lab.on(t, "device.uiTree", `{`+dev(remotePhone)+`}`)
			if env.Error == nil || env.Error.Code != device.ErrNotAllowed || !strings.Contains(env.Error.Cause, c.cause) || !strings.Contains(env.Error.NextStep, remotePhone) {
				t.Errorf("%s: uiTree %+v; want DEVICE_NOT_ALLOWED with cause %q and a nextStep naming %s", c.name, env.Error, c.cause, remotePhone)
			}
			if after := len(lab.hostCalls(t)); after != before {
				t.Errorf("%s: the host ran %d adb commands for a call the client's policy refused", c.name, after-before)
			}
		}
		lab.rewriteClient(t, true, fullAllowlist()...)
	})

	// Each plain capability on the remote id runs on the host, for the serial, and on the
	// client runs no adb command at all (ADR-0100 §6). Every answer, success or error, is
	// marked untrusted, and a host error keeps its code. device.tap, swipe and pressKey are
	// delivered by the host's own reader or `input` (the host's call); device.scroll needs a
	// live view the host has not opened: the host's DEVICE_NO_STREAM comes back.
	t.Run("the plain class is relayed", func(t *testing.T) {
		clientBefore, hostBefore := len(lab.clientCalls(t)), len(lab.hostCalls(t))
		type step struct {
			capName, params string
			errCode         capability.ErrorCode
			check           func(t *testing.T, result json.RawMessage)
		}
		steps := []step{
			{"device.uiTree", `{` + dev(remotePhone) + `}`, "", func(t *testing.T, r json.RawMessage) {
				var s device.Snapshot
				if err := json.Unmarshal(r, &s); err != nil || len(s.Nodes) == 0 {
					t.Errorf("uiTree result %.200s: %v", r, err)
				}
			}},
			{"device.launch", `{` + dev(remotePhone) + `,"applicationId":"dev.droidyard.formapp"}`, "", func(t *testing.T, r json.RawMessage) {
				var l device.LaunchResult
				if err := json.Unmarshal(r, &l); err != nil || l.PID == nil || *l.PID != 4242 {
					t.Errorf("launch result %s: %v", r, err)
				}
			}},
			{"device.uninstall", `{` + dev(remotePhone) + `,"applicationId":"dev.droidyard.formapp"}`, "", func(t *testing.T, r json.RawMessage) {
				var u device.UninstallResult
				if err := json.Unmarshal(r, &u); err != nil || !u.Uninstalled {
					t.Errorf("uninstall result %s: %v", r, err)
				}
			}},
			// The system verbs (plan m3g step 3, ADR-0133 §7) run on the host's adb for its
			// serial, like the rest of the plain class.
			{"device.systemPanel", `{` + dev(remotePhone) + `,"panel":"quickSettings"}`, "", func(t *testing.T, r json.RawMessage) {
				var sp device.SystemPanelResult
				if err := json.Unmarshal(r, &sp); err != nil || sp.Panel != "quickSettings" {
					t.Errorf("systemPanel result %s: %v", r, err)
				}
			}},
			{"device.rotate", `{` + dev(remotePhone) + `,"rotation":"90"}`, "", func(t *testing.T, r json.RawMessage) {
				var rr device.RotateResult
				if err := json.Unmarshal(r, &rr); err != nil || !rr.Previous.Auto || rr.Previous.Rotation != 0 {
					t.Errorf("rotate result %s: %v", r, err)
				}
			}},
			{"device.tapElement", `{` + dev(remotePhone) + `,"target":{"desc":"Navigate up"}}`, "", func(t *testing.T, r json.RawMessage) {
				var tr device.TapResult
				if err := json.Unmarshal(r, &tr); err != nil || tr.Tapped.MatchedBy == "" {
					t.Errorf("tapElement result %.200s: %v", r, err)
				}
			}},
			{"device.tapElement", `{` + dev(remotePhone) + `,"target":{"text":"no such text anywhere"}}`, device.ErrElementNotFound, nil},
			{"device.inputText", `{` + dev(remotePhone) + `,"text":"abc"}`, "", func(t *testing.T, r json.RawMessage) {
				var it device.InputTextResult
				if err := json.Unmarshal(r, &it); err != nil || it.Characters != 3 {
					t.Errorf("inputText result %s: %v", r, err)
				}
			}},
			// The coordinate actions (plan m3g step 4, ADR-0133 §7) are the host's call: with no
			// view open there it delivers by its own reader or `input`, and the client runs
			// nothing. device.scroll still needs the host's view.
			{"device.tap", `{` + dev(remotePhone) + `,"x":0.5,"y":0.5}`, "", func(t *testing.T, r json.RawMessage) { checkDelivery(t, r, device.DeliveryInput) }},
			{"device.swipe", `{` + dev(remotePhone) + `,"path":[{"x":0.5,"y":0.8,"t":0},{"x":0.5,"y":0.2,"t":200}]}`, "", func(t *testing.T, r json.RawMessage) { checkDelivery(t, r, device.DeliveryInput) }},
			{"device.pressKey", `{` + dev(remotePhone) + `,"key":"home"}`, "", func(t *testing.T, r json.RawMessage) { checkDelivery(t, r, device.DeliveryInput) }},
			{"device.scroll", `{` + dev(remotePhone) + `,"x":0.5,"y":0.5,"dx":0,"dy":1}`, device.ErrNoStream, nil},
		}
		for _, s := range steps {
			env := lab.on(t, s.capName, s.params)
			if !untrusted(env) {
				t.Errorf("%s: the answer is not marked untrusted: %+v", s.capName, env.Meta)
			}
			if s.errCode != "" {
				if env.Error == nil || env.Error.Code != s.errCode {
					t.Errorf("%s: %+v; want the host's code %s kept", s.capName, env.Error, s.errCode)
				}
				continue
			}
			if env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
				t.Errorf("%s: %+v / %+v; want it completed", s.capName, env.Operation, env.Error)
				continue
			}
			s.check(t, env.Result)
		}

		host := lab.hostCalls(t)[hostBefore:]
		if got := serialsOf(host); !slices.Equal(got, []string{phone1}) {
			t.Errorf("the host's commands address %v; want %s alone (-s %s)", got, phone1, phone1)
		}
		for _, want := range []string{"-s " + phone1 + " exec-out uiautomator dump /dev/tty", "-s " + phone1 + " shell pm path dev.droidyard.formapp"} {
			if !slices.Contains(host, want) {
				t.Errorf("the host's commands %q lack %q", host, want)
			}
		}
		if !slices.ContainsFunc(host, func(c string) bool { return strings.HasPrefix(c, "-s "+phone1+" shell monkey ") }) {
			t.Errorf("the host's commands %q hold no launch", host)
		}
		for _, want := range []string{"-s " + phone1 + " shell cmd statusbar expand-settings", "-s " + phone1 + " shell settings put system user_rotation 1",
			// device.tap and device.swipe on the remote phone are the host's `input`, sized by the host's screen.
			"-s " + phone1 + " shell input tap 540 1200", "-s " + phone1 + " shell input swipe 540 1920 540 480 200",
			"-s " + phone1 + " shell input keyevent 3"} {
			if !slices.Contains(host, want) {
				t.Errorf("the host's commands %q lack %q", host, want)
			}
		}
		if client := lab.clientCalls(t)[clientBefore:]; len(client) != 0 {
			t.Errorf("the client ran %q for the remote device; it runs no adb command for it", client)
		}
	})

	// An MCP session on the client uses the remote id through device_uiTree, and through
	// device_swipe, which is the host's call (ADR-0133 §7): the host delivers by `input`, sized
	// by its own screen. device.scroll is for humans: from MCP it is refused by the client's
	// core, with no operation, and the host is asked nothing.
	t.Run("an MCP session on the client", func(t *testing.T) {
		s := startShimEnv(t, lab.client.state, "", "agent", nil, []string{"mcp", "--no-autostart", "--wait", "5s"})
		s.initialize("agent")
		hostBefore := len(lab.hostCalls(t))
		c := s.toolWith(1, "device_uiTree", `{`+dev(remotePhone)+`}`)
		if c.isError {
			t.Fatalf("device_uiTree on %s: %s", remotePhone, c.raw)
		}
		if got := serialsOf(lab.hostCalls(t)[hostBefore:]); !slices.Equal(got, []string{phone1}) {
			t.Errorf("the host's commands for the MCP call address %v", got)
		}
		hostBefore = len(lab.hostCalls(t))
		sw := s.toolWith(2, "device_swipe", `{`+dev(remotePhone)+`,"path":[{"x":0.5,"y":0.1,"t":0},{"x":0.5,"y":0.6,"t":300}]}`)
		if sw.isError {
			t.Fatalf("device_swipe on %s: %s", remotePhone, sw.raw)
		}
		if host := lab.hostCalls(t)[hostBefore:]; !slices.Contains(host, "-s "+phone1+" shell input swipe 540 240 540 1440 300") {
			t.Errorf("the host's commands %q lack the swipe: it is the host's call", host)
		}
		before := len(lab.hostCalls(t))
		id, err := lab.client.ops.Start(context.Background(), capability.Request{Capability: "device.scroll", Client: capability.ClientMCP,
			Params: json.RawMessage(`{` + dev(remotePhone) + `,"x":0.5,"y":0.5,"dx":0,"dy":1}`), Origin: capability.Origin{ID: "mcp-1", Client: capability.ClientMCP}})
		var e *capability.Error
		if id != "" || err == nil || !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere {
			t.Errorf("device.scroll as client mcp: (%q, %v); want REQUEST_NOT_ALLOWED_HERE and no operation", id, err)
		}
		if after := len(lab.hostCalls(t)); after != before {
			t.Errorf("the host recorded %d commands for a call the client refused", after-before)
		}
		s.exit()
	})

	// A client cancel stops the host's operation, which ends cancelled; the operation the
	// client records names the id as its target. The host's own cancel of the same work is
	// OPERATION_CANCELLED, every cancelled operation's error, passed through with its cause: it is not
	// an answer this daemon cannot read.
	t.Run("cancel and the host's own cancel", func(t *testing.T) {
		devicetest.Write(t, lab.hostDir, phone1+".monkey.hang", nil)
		defer os.Remove(filepath.Join(lab.hostDir, phone1+".monkey.hang"))

		start := func() (clientOp string, hostOp string) {
			t.Helper()
			id, err := lab.client.ops.Start(context.Background(), capability.Request{Capability: "device.launch", Client: capability.ClientCLI,
				Params: json.RawMessage(`{` + dev(remotePhone) + `,"applicationId":"dev.droidyard.formapp"}`)})
			if err != nil {
				t.Fatal(err)
			}
			testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to run the launch", func() bool {
				for _, op := range lab.host.ops.Active() {
					if op.Capability == "device.launch" {
						hostOp = op.OperationID
					}
				}
				return hostOp != ""
			})
			for _, op := range lab.client.ops.Active() {
				if op.OperationID == id && (op.Target == nil || op.Target.Device != remotePhone) {
					t.Errorf("the client's operation targets %+v; want device %s", op.Target, remotePhone)
				}
			}
			return id, hostOp
		}
		wait := func(ops *capability.Operations, id string) capability.Outcome {
			t.Helper()
			out, err := ops.Wait(context.Background(), id, time.Now().Add(10*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			return out
		}

		clientOp, hostOp := start()
		if err := lab.client.ops.Cancel(clientOp); err != nil {
			t.Fatal(err)
		}
		if out := wait(lab.client.ops, clientOp); out.State != capability.StateCancelled {
			t.Errorf("the client's operation is %s; want cancelled", out.State)
		}
		if out := wait(lab.host.ops, hostOp); out.State != capability.StateCancelled {
			t.Errorf("the host's operation is %s %+v; a cancel on the client must end it cancelled", out.State, out.Error)
		}

		clientOp, hostOp = start()
		if err := lab.host.ops.Cancel(hostOp); err != nil {
			t.Fatal(err)
		}
		out := wait(lab.client.ops, clientOp)
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != capability.ErrOperationCancelled || !strings.Contains(out.Error.Cause, "the host cancelled") {
			t.Errorf("the host cancelled the work: client %s %+v; want OPERATION_CANCELLED saying the host cancelled it", out.State, out.Error)
		}
		if !out.Untrusted {
			t.Error("a relayed error is not marked untrusted")
		}
	})

	// The link cut in the middle of a call is DEVICE_DISCONNECTED; once it is down, a call
	// is DEVICE_NOT_READY (unreachable) and the listing says the remote is not reachable.
	t.Run("the host goes away", func(t *testing.T) {
		devicetest.Write(t, lab.hostDir, phone1+".monkey.hang", nil)
		id, err := lab.client.ops.Start(context.Background(), capability.Request{Capability: "device.launch", Client: capability.ClientCLI,
			Params: json.RawMessage(`{` + dev(remotePhone) + `,"applicationId":"dev.droidyard.formapp"}`)})
		if err != nil {
			t.Fatal(err)
		}
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to run the launch", func() bool {
			for _, op := range lab.host.ops.Active() {
				if op.Capability == "device.launch" {
					return true
				}
			}
			return false
		})
		lab.stopHost()
		out, err := lab.client.ops.Wait(context.Background(), id, time.Now().Add(10*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != device.ErrDisconnected {
			t.Errorf("the link cut mid-call: %s %+v; want DEVICE_DISCONNECTED", out.State, out.Error)
		}

		testtime.Eventually(t, testtime.Bound(10*time.Second), "the client to see the link down", func() bool { return !lab.client.host.Remote(remoteName).Connected() })
		env := lab.on(t, "device.uiTree", `{`+dev(remotePhone)+`}`)
		if env.Error == nil || env.Error.Code != device.ErrNotReady || env.Error.Details["state"] != "unreachable" {
			t.Errorf("a call with the host stopped: %+v; want DEVICE_NOT_READY (unreachable)", env.Error)
		}
		rows, warnings, _ := lab.list(t)
		if got := listedIDs(rows); !slices.Equal(got, []string{"emulator-5556"}) {
			t.Errorf("device.list with the host stopped = %v; want the client's emulator", got)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "qa") || !strings.Contains(warnings[0], "not reachable") {
			t.Errorf("warnings = %q; want one saying the remote qa is not reachable", warnings)
		}
	})

	// Whatever happened, the client's adb was never asked about the remote phone.
	if got := serialsOf(lab.clientCalls(t)); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "R58M") || strings.Contains(s, "/") }) {
		t.Errorf("the client's adb addressed %v; it must never address a remote device", got)
	}
}
