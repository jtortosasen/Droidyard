package emulator

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/emulator/emulatortest"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/process/processtest"
)

func TestMain(m *testing.M) {
	devicetest.Main()
	emulatortest.Main()
	processtest.Main()
	// No test may probe this machine's emulator consoles (5554-5584); the probe's own tests
	// give it a console of their own.
	consolePorts = nil
	os.Exit(m.Run())
}

const (
	serial   = "emulator-5556"
	serial2  = "emulator-5554"
	listHead = "List of devices attached\n"

	emuLine   = serial + " device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"
	emuLine2  = serial2 + " device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:2\n"
	emuOff    = serial + " offline product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"
	ipLine    = "192.168.0.2:5555 device product:sdk_gphone16k_arm64 model:remote transport_id:3\n"
	physLine  = "abc12345 device product:oriole model:Pixel_6 usb:1 transport_id:4\n"
	loadedAVD = "droidyard_m1i"
)

func listOf(lines ...string) string { return listHead + strings.Join(lines, "") }

func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return p
}

// execFile writes an executable file, for a launcher or adb candidate.
func execFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// start is the real spawner a launcher run needs in a test (the fake launcher is a real
// process, the re-executed test binary).
func start(cmd *exec.Cmd) error { return cmd.Start() }

// fixture is the subsystem over the fake adb (in process) and the fake launcher
// (re-executed), with a registry and an operation store to call its capabilities.
type fixture struct {
	adbDir string
	ldir   string
	state  string
	env    []string
	adb    *device.ADB
	em     *Emulator
	ops    *capability.Operations
}

func newFixture(t *testing.T, adbFiles map[string]string, envExtra ...string) *fixture {
	t.Helper()
	adbDir := t.TempDir()
	for name, content := range adbFiles {
		devicetest.Write(t, adbDir, name, []byte(content))
	}
	adb := device.NewADBWithExec(devicetest.Env(t, adbDir, closedPort(t)), devicetest.Exec(adbDir))
	ldir := t.TempDir()
	// The launcher's table is the fake adb's scenario directory, so a boot it starts is
	// listed by the fake adb (plan m1i, D20).
	env := append(emulatortest.Env(t, ldir), emulatortest.TableEnv+"="+adbDir)
	env = append(env, envExtra...)
	state := t.TempDir()
	em := New(env, adb, start, state)
	reg := capability.NewRegistry()
	device.Register(reg, adb, nil, nil)
	Register(reg, em)
	f := &fixture{adbDir: adbDir, ldir: ldir, state: state, env: env, adb: adb, em: em, ops: capability.NewOperations(reg)}
	t.Cleanup(f.killLaunchers)
	return f
}

// killLaunchers ends every launcher the fixture started, so a test that cancels a boot
// (which leaves the emulator running on purpose) still cleans up. A fake launcher that
// forked a child in its group (`.launch.child`) left it behind if the test never ended the
// launch: kill it here too, or it sleeps for an hour.
func (f *fixture) killLaunchers() {
	f.em.mu.Lock()
	launches := make([]*launch, 0, len(f.em.launches))
	for _, l := range f.em.launches {
		launches = append(launches, l)
	}
	f.em.mu.Unlock()
	for _, l := range launches {
		if p, err := os.FindProcess(l.pid); err == nil {
			p.Kill()
		}
		if pid := f.readChildPid(l.avd); pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				p.Kill()
			}
		}
	}
}

// readChildPid is the pid a fake launcher with `.launch.child` recorded, 0 when none.
func (f *fixture) readChildPid(avd string) int {
	b, err := os.ReadFile(filepath.Join(f.ldir, avd+".child.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// awaitLaunch waits until the fixture has a launch for avd and returns it, so a test can
// see a boot that has not resolved its serial yet.
func (f *fixture) awaitLaunch(t *testing.T, avd string) *launch {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l := f.em.launchFor(avd); l != nil {
			return l
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no launch for %s", avd)
	return nil
}

// awaitChildPid waits until the fake launcher recorded its forked child's pid.
func (f *fixture) awaitChildPid(t *testing.T, avd string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pid := f.readChildPid(avd); pid > 0 {
			return pid
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no child pid recorded for %s", avd)
	return 0
}

// awaitFile waits until the fake launcher wrote name in its scenario directory, so a test
// acts only once a SIGTERM handler is in effect (a launcher the fake would ignore).
func (f *fixture) awaitFile(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(f.ldir, name)); err == nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the launcher never wrote %s", name)
}

// awaitDead waits until a pid is gone (signal 0 reaches nothing), so an assertion that a
// process ended does not race the OS reaping it. A pid that is still there at the bound
// fails the test.
func awaitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("pid %d is still alive", pid)
}

// startFixture boots the fake's AVD from stopped: the launcher lists it, boots at once
// and stays alive. It is what a boot test starts from.
func (f *fixture) stopped(t *testing.T, list string) {
	t.Helper()
	f.launcher(t, list)
}

// logOf is the AVD's launcher log, "" when there is none.
func (f *fixture) logOf(t *testing.T, avd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.state, "emulator", avd+".log"))
	if err != nil {
		return ""
	}
	return string(b)
}

// boot runs emulator.start through the operation store, waits for it and returns the
// outcome.
func (f *fixture) boot(t *testing.T, params string) capability.Outcome {
	t.Helper()
	return f.invoke(t, "emulator.start", params)
}

// mustStart is a completed emulator.start.
func mustStart(t *testing.T, out capability.Outcome) StartResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("emulator.start %s: %+v", out.State, out.Error)
	}
	r, ok := out.Result.(StartResult)
	if !ok {
		t.Fatalf("result is %T, want StartResult", out.Result)
	}
	return r
}

// mustWipe is a completed emulator.wipe.
func mustWipe(t *testing.T, out capability.Outcome) WipeResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("emulator.wipe %s: %+v", out.State, out.Error)
	}
	r, ok := out.Result.(WipeResult)
	if !ok {
		t.Fatalf("result is %T, want WipeResult", out.Result)
	}
	return r
}

// scaleBoot shrinks the boot bound and poll so a test runs in milliseconds. It also scales
// the serial deadline (to the boot bound, which is the earlier of the two) and the two
// graces `end` waits, so a test that ends a launch does not wait 45 s or 10 s
// (plan m1m, Q1 ¶1, ¶3).
func scaleBoot(t *testing.T, timeout, poll time.Duration) {
	t.Helper()
	bt, bp, st, eg, ekg := bootTimeout, bootPollInterval, serialTimeout, endGrace, endKillGrace
	bootTimeout, bootPollInterval = timeout, poll
	serialTimeout = timeout
	endGrace, endKillGrace = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() {
		bootTimeout, bootPollInterval, serialTimeout = bt, bp, st
		endGrace, endKillGrace = eg, ekg
	})
}

// scaleSerial shrinks the serial deadline alone, so a test can make it fire well before the
// boot bound and assert which one the boot ended on (plan m1m, Q1 ¶3). It must follow
// scaleBoot, whose cleanup restores the boot-scoped value first.
func scaleSerial(t *testing.T, d time.Duration) {
	t.Helper()
	s := serialTimeout
	serialTimeout = d
	t.Cleanup(func() { serialTimeout = s })
}

// alive reports whether a pid is still there (signal 0 reached it).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// launcherPid is the pid a boot of avd reported to the table.
func (f *fixture) launcherPid(t *testing.T, serial string) int {
	t.Helper()
	return devicetest.EmulatorPID(f.adbDir, serial)
}

// launcher makes the fake print stdout for `-list-avds`.
func (f *fixture) launcher(t *testing.T, stdout string) {
	t.Helper()
	emulatortest.Write(t, f.ldir, "list-avds.out", []byte(stdout))
}

func (f *fixture) calls(t *testing.T) []string { return devicetest.Calls(t, f.adbDir) }

func (f *fixture) listing(t *testing.T) []string {
	t.Helper()
	devices, err := f.adb.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range devices {
		out = append(out, d.Device)
	}
	return out
}

func (f *fixture) invoke(t *testing.T, capName, params string) capability.Outcome {
	t.Helper()
	out, err := f.ops.Invoke(context.Background(), capability.Request{Capability: capName, Params: []byte(params)})
	if err != nil {
		t.Fatalf("Invoke %s: %v", capName, err)
	}
	return out
}

func (f *fixture) list(t *testing.T) ListResult {
	t.Helper()
	out := f.invoke(t, "emulator.list", "{}")
	if out.State != capability.StateCompleted {
		t.Fatalf("emulator.list %s: %+v", out.State, out.Error)
	}
	r, ok := out.Result.(ListResult)
	if !ok {
		t.Fatalf("result is %T, want ListResult", out.Result)
	}
	return r
}

func mustFail(t *testing.T, out capability.Outcome, code capability.ErrorCode) *capability.Error {
	t.Helper()
	if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != code {
		t.Fatalf("state %s, error %+v; want failed with %s", out.State, out.Error, code)
	}
	return out.Error
}

func mustStop(t *testing.T, out capability.Outcome) StopResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("emulator.stop %s: %+v", out.State, out.Error)
	}
	r, ok := out.Result.(StopResult)
	if !ok {
		t.Fatalf("result is %T, want StopResult", out.Result)
	}
	return r
}

// scaleStop shrinks the stop's bound and poll so a test runs in milliseconds.
func scaleStop(t *testing.T, timeout, poll time.Duration) {
	t.Helper()
	bt, bp := stopTimeout, stopPollInterval
	stopTimeout, stopPollInterval = timeout, poll
	t.Cleanup(func() { stopTimeout, stopPollInterval = bt, bp })
}

func countCall(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

func anyContains(calls []string, sub string) bool {
	for _, c := range calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// ---- Binary resolution -------------------------------------------------------------

// Decision (plan m1i step 4, check): the launcher is the host SDK's emulator/emulator
// before the one derived from adb's location before emulator on PATH, and its absence is
// reported, not fatal. The mutation this fails: PATH consulted before the host SDK.
func TestResolveOrderHostSDKThenADBLocationThenPATH(t *testing.T) {
	t.Run("host SDK wins over adb location and PATH", func(t *testing.T) {
		root := t.TempDir()
		execFile(t, filepath.Join(root, "emulator", "emulator"))
		sdkDir := t.TempDir()
		execFile(t, filepath.Join(sdkDir, "platform-tools", "adb"))
		execFile(t, filepath.Join(sdkDir, "emulator", "emulator"))
		pdir := t.TempDir()
		execFile(t, filepath.Join(pdir, "emulator"))
		env := []string{"ANDROID_HOME=" + root, "ADB=" + filepath.Join(sdkDir, "platform-tools", "adb"),
			"PATH=" + pdir, "HOME=" + t.TempDir()}
		res := Resolve(env)
		if !res.Found || res.Source != SourceHostSDK {
			t.Fatalf("res = %+v; want found from the host SDK", res)
		}
		if res.Path != filepath.Join(root, "emulator", "emulator") {
			t.Errorf("path = %q, want the host SDK's", res.Path)
		}
	})

	t.Run("adb location wins over PATH", func(t *testing.T) {
		sdkDir := t.TempDir()
		execFile(t, filepath.Join(sdkDir, "platform-tools", "adb"))
		execFile(t, filepath.Join(sdkDir, "emulator", "emulator"))
		pdir := t.TempDir()
		execFile(t, filepath.Join(pdir, "emulator"))
		env := []string{"ADB=" + filepath.Join(sdkDir, "platform-tools", "adb"), "PATH=" + pdir, "HOME=" + t.TempDir()}
		res := Resolve(env)
		if !res.Found || res.Source != SourceADBLocation {
			t.Fatalf("res = %+v; want found from the adb location", res)
		}
		if res.Path != filepath.Join(sdkDir, "emulator", "emulator") {
			t.Errorf("path = %q, want the one derived from adb", res.Path)
		}
	})

	t.Run("PATH is the last resort", func(t *testing.T) {
		pdir := t.TempDir()
		execFile(t, filepath.Join(pdir, "emulator"))
		res := Resolve([]string{"PATH=" + pdir, "HOME=" + t.TempDir()})
		if !res.Found || res.Source != SourcePath {
			t.Fatalf("res = %+v; want found on PATH", res)
		}
	})

	t.Run("none is found", func(t *testing.T) {
		home := t.TempDir()
		res := Resolve([]string{"PATH=", "HOME=" + home})
		if res.Found {
			t.Fatalf("res = %+v; want not found", res)
		}
		if len(res.Rejected) == 0 {
			t.Error("nothing rejected: the reason says nothing was tried")
		}
		if !strings.Contains(res.NotFound(), "not found") {
			t.Errorf("NotFound = %q", res.NotFound())
		}
		// Every candidate names its source (plan m1n, A10): the per-OS default SDK and
		// PATH, the two sources this environment has. Seen failing when an entry read only
		// the path.
		for _, want := range []string{"[SDK (per-OS default)]", "[PATH]"} {
			if !strings.Contains(res.NotFound(), want) {
				t.Errorf("NotFound = %q; want the source named as %s (plan m1n, A10)", res.NotFound(), want)
			}
		}
	})
}

// TestResolvePrefersTheConfiguredLauncher pins ADR-0032 §3 and D8: the `tools.emulator`
// key is the first launcher candidate, ahead of the host SDK; a key that is not
// executable is rejected and the host SDK's launcher wins. Seen failing with the host
// SDK before the key.
func TestResolvePrefersTheConfiguredLauncher(t *testing.T) {
	tmp := t.TempDir()
	keyLauncher := filepath.Join(tmp, "configured", "emulator")
	execFile(t, keyLauncher)
	sdkRoot := t.TempDir()
	execFile(t, filepath.Join(sdkRoot, "emulator", "emulator"))
	env := []string{"ANDROID_HOME=" + sdkRoot, "HOME=" + t.TempDir()}
	keys := Keys{Launcher: policy.ToolKey{Path: keyLauncher}}

	res := ResolveWith(env, keys)
	if !res.Found || res.Path != keyLauncher || res.Source != SourceToolKey {
		t.Fatalf("ResolveWith = %+v; want %q from %s", res, keyLauncher, SourceToolKey)
	}

	// A key that names no file is rejected, and the host SDK's launcher still wins.
	missing := filepath.Join(tmp, "nope", "emulator")
	res = ResolveWith(env, Keys{Launcher: policy.ToolKey{Path: missing}})
	if !res.Found || res.Source != SourceHostSDK {
		t.Fatalf("ResolveWith = %+v; want the host SDK's launcher after the rejected key", res)
	}
	var rejected bool
	for _, r := range res.Rejected {
		if r.Candidate == missing && r.Source == SourceToolKey {
			rejected = true
		}
	}
	if !rejected {
		t.Errorf("rejected = %+v; want the unusable key with source %s", res.Rejected, SourceToolKey)
	}
}

// TestResolveDerivesTheLauncherFromTheConfiguredADB pins D8's derived row: with no
// `tools.emulator` and no host SDK, the launcher derived from adb's location uses adb as
// resolved, so the configured `tools.adb` under <sdk>/platform-tools names the launcher
// beside it. Seen failing when the derived candidate ignores the configured adb.
func TestResolveDerivesTheLauncherFromTheConfiguredADB(t *testing.T) {
	tmp := t.TempDir()
	adb := filepath.Join(tmp, "sdk", "platform-tools", "adb")
	execFile(t, adb)
	launcher := filepath.Join(tmp, "sdk", "emulator", "emulator")
	execFile(t, launcher)
	env := []string{"PATH=", "HOME=" + t.TempDir()}

	res := ResolveWith(env, Keys{ADB: policy.ToolKey{Path: adb}})
	if !res.Found || res.Path != launcher || res.Source != SourceADBLocation {
		t.Fatalf("ResolveWith = %+v; want %q from %s", res, launcher, SourceADBLocation)
	}
}

// TestResolveInvalidConfigurationNamesTheFile pins D8's invalid-file entry for the
// launcher: with an unusable configuration no key is used and the file is listed as
// rejected with `tools.emulator` as its source, whatever else wins.
func TestResolveInvalidConfigurationNamesTheFile(t *testing.T) {
	tmp := t.TempDir()
	sdkRoot := t.TempDir()
	execFile(t, filepath.Join(sdkRoot, "emulator", "emulator"))
	configPath := filepath.Join(tmp, "config.json")
	keys := Keys{Launcher: policy.ToolKey{ConfigPath: configPath, Invalid: "tools.emulator: not an absolute path"}}

	res := ResolveWith([]string{"ANDROID_HOME=" + sdkRoot, "HOME=" + t.TempDir()}, keys)
	if !res.Found || res.Source != SourceHostSDK {
		t.Fatalf("ResolveWith = %+v; want the host SDK despite the bad file", res)
	}
	var named bool
	for _, r := range res.Rejected {
		if r.Candidate == configPath && r.Source == SourceToolKey && strings.Contains(r.Reason, "the configuration file is invalid") {
			named = true
		}
	}
	if !named {
		t.Errorf("rejected = %+v; want the invalid file with source %s", res.Rejected, SourceToolKey)
	}

	// With nothing else to fall to, the "Tried:" list names the file the key came from
	// (plan m1n, A10): `tools.emulator in <config path>`.
	none := ResolveWith([]string{"PATH=", "HOME=" + t.TempDir()}, keys)
	if none.Found {
		t.Fatalf("ResolveWith = %+v; want not found", none)
	}
	if want := "[" + SourceToolKey + " in " + configPath + "]"; !strings.Contains(none.NotFound(), want) {
		t.Errorf("NotFound = %q; want the source named as %s (plan m1n, A10)", none.NotFound(), want)
	}
}

// ---- AVD discovery -----------------------------------------------------------------

func TestAVDDirsOrder(t *testing.T) {
	env := []string{
		"ANDROID_AVD_HOME=/a", "ANDROID_EMULATOR_HOME=/b", "ANDROID_USER_HOME=/c",
		"ANDROID_SDK_HOME=/d", "HOME=/e",
	}
	got := []string{}
	for _, d := range avdDirs(env) {
		got = append(got, d.path)
	}
	want := []string{"/a", "/b/avd", "/c/avd", "/d/.android/avd", "/e/.android/avd"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("avdDirs = %v; want %v", got, want)
	}
}

// Decision (plan m1i step 4, check): each discovery directory wins over the ones after it
// (emulator.md §AVD discovery, as step 1 placed ANDROID_EMULATOR_HOME and ANDROID_SDK_HOME).
func TestReadConfigPrefersTheEarlierDiscoveryDirectory(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeAVD(t, first, loadedAVD, "path="+filepath.Join(first, loadedAVD+".avd")+"\ntarget=android-31\n",
		"abi.type=arm64-v8a\nhw.device.name=pixel_5\n")
	writeAVD(t, second, loadedAVD, "path="+filepath.Join(second, loadedAVD+".avd")+"\ntarget=android-36\n",
		"abi.type=x86_64\n")
	env := []string{"ANDROID_AVD_HOME=" + first, "ANDROID_EMULATOR_HOME=" + filepath.Dir(second), "HOME=" + t.TempDir()}
	cfg, why := readConfig(loadedAVD, env)
	if why != "" {
		t.Fatalf("config unavailable: %s", why)
	}
	if cfg.Target != "android-31" || cfg.ABI != "arm64-v8a" || cfg.DeviceProfile != "pixel_5" {
		t.Errorf("config = %+v; want the first directory's", cfg)
	}
}

// Decision: path= in an entry may name a directory elsewhere, and path.rel resolves
// against the parent of the discovery directory the entry was found in.
func TestReadConfigResolvesPathElsewhereAndPathRel(t *testing.T) {
	t.Run("path elsewhere", func(t *testing.T) {
		home := t.TempDir()
		avd := filepath.Join(t.TempDir(), loadedAVD+".avd")
		writeAVDAt(t, home, loadedAVD, "path="+avd+"\ntarget=android-30\n", avd, "abi.type=arm64-v8a\n")
		cfg, why := readConfig(loadedAVD, []string{"ANDROID_AVD_HOME=" + home, "HOME=" + t.TempDir()})
		if why != "" || cfg.Target != "android-30" || cfg.ABI != "arm64-v8a" {
			t.Errorf("cfg = %+v why = %q; want the elsewhere directory read", cfg, why)
		}
	})

	t.Run("path.rel", func(t *testing.T) {
		base := t.TempDir()
		avdHome := filepath.Join(base, "avd")
		avd := filepath.Join(avdHome, loadedAVD+".avd")
		writeAVDAt(t, avdHome, loadedAVD, "path.rel=avd/"+loadedAVD+".avd\ntarget=android-31\n", avd, "abi.type=arm64-v8a\n")
		cfg, why := readConfig(loadedAVD, []string{"ANDROID_AVD_HOME=" + avdHome, "HOME=" + t.TempDir()})
		if why != "" || cfg.Target != "android-31" || cfg.ABI != "arm64-v8a" {
			t.Errorf("cfg = %+v why = %q; want path.rel resolved against the parent", cfg, why)
		}
	})
}

// Decision (plan m1i, D13): a name with no locatable entry keeps configUnavailable with a
// reason that names the variables, never a concrete path; it is not dropped.
func TestReadConfigUnavailableNamesNoPath(t *testing.T) {
	home := t.TempDir()
	cfg, why := readConfig("ghost", []string{"ANDROID_AVD_HOME=" + home, "HOME=" + home})
	if why == "" {
		t.Fatal("no reason for a name with no entry")
	}
	if cfg != (AVDConfig{}) {
		t.Errorf("cfg = %+v; want zero", cfg)
	}
	if strings.Contains(why, home) {
		t.Errorf("reason %q names a concrete path", why)
	}
	if !strings.Contains(why, "ANDROID_AVD_HOME") {
		t.Errorf("reason %q does not name what was tried", why)
	}
}

func writeAVD(t *testing.T, dir, name, ini, config string) {
	t.Helper()
	writeAVDAt(t, dir, name, ini, filepath.Join(dir, name+".avd"), config)
}

func writeAVDAt(t *testing.T, entryDir, name, ini, avdDir, config string) {
	t.Helper()
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	devicetest.Write(t, entryDir, name+".ini", []byte(ini))
	if err := os.MkdirAll(avdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	devicetest.Write(t, avdDir, "config.ini", []byte(config))
}

// ---- emulator.list -----------------------------------------------------------------

// Decision (plan m1i step 4, check): names come from the launcher's -list-avds (chatter
// filtered per step 1's recording), never this machine's AVDs. The mutation this fails: a
// chatter line kept as an AVD.
func TestListReturnsLauncherNamesOnly(t *testing.T) {
	recorded, err := os.ReadFile(filepath.Join("testdata", "list-avds.stdout"))
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, map[string]string{"devices.out": listHead}, "ANDROID_AVD_HOME="+t.TempDir())
	f.launcher(t, "INFO | Storing crashdata in: /tmp/android-crashdata\n"+string(recorded)+"INFO | Duplicate loglines will be removed\n")

	r := f.list(t)
	if len(r.AVDs) != 1 || r.AVDs[0].Name != strings.TrimSpace(string(recorded)) {
		t.Fatalf("avds = %+v; want only the recorded name", r.AVDs)
	}
	// The recording has no entry under the temporary home, so the configuration is
	// unavailable and the AVD is not dropped.
	if r.AVDs[0].ConfigUnavailable == "" || r.AVDs[0].Config != nil {
		t.Errorf("avd = %+v; want configUnavailable and no config", r.AVDs[0])
	}
}

// Decision (plan m1i, D13): running {device} maps a listed AVD to the online emulator
// whose console reports it, and config comes from the AVD files.
func TestListRunningFromTheOnlineEmulator(t *testing.T) {
	avdHome := t.TempDir()
	writeAVD(t, avdHome, loadedAVD, "path="+filepath.Join(avdHome, loadedAVD+".avd")+"\ntarget=android-31\n",
		"abi.type=arm64-v8a\nhw.device.name=pixel_5\navd.ini.displayname=Droidyard M1i\n")
	avdName, _ := os.ReadFile(filepath.Join("testdata", "emu-avd-name.txt"))
	f := newFixture(t, map[string]string{
		"devices.out":   listOf(emuLine),
		serial + ".avd": string(avdName),
	}, "ANDROID_AVD_HOME="+avdHome)
	f.launcher(t, loadedAVD+"\n")

	r := f.list(t)
	if len(r.AVDs) != 1 {
		t.Fatalf("avds = %+v", r.AVDs)
	}
	a := r.AVDs[0]
	if a.Running == nil || a.Running.Device != serial {
		t.Errorf("running = %+v; want %s", a.Running, serial)
	}
	if a.Config == nil || a.Config.Target != "android-31" || a.Config.ABI != "arm64-v8a" ||
		a.Config.DeviceProfile != "pixel_5" || a.Config.DisplayName != "Droidyard M1i" {
		t.Errorf("config = %+v", a.Config)
	}
}

// Decision (plan m1i step 4, check): with no launcher the call fails
// EMULATOR_LAUNCHER_NOT_FOUND, rather than listing nothing.
func TestListFailsWhenNoLauncherIsFound(t *testing.T) {
	// A fixture whose environment carries no PATH launcher.
	adbDir := t.TempDir()
	devicetest.Write(t, adbDir, "devices.out", []byte(listHead))
	adb := device.NewADBWithExec(devicetest.Env(t, adbDir, closedPort(t)), devicetest.Exec(adbDir))
	em := New([]string{"PATH=", "HOME=" + t.TempDir()}, adb, start, "")
	reg := capability.NewRegistry()
	Register(reg, em)
	ops := capability.NewOperations(reg)

	out, err := ops.Invoke(context.Background(), capability.Request{Capability: "emulator.list"})
	if err != nil {
		t.Fatal(err)
	}
	mustFail(t, out, ErrLauncherNotFound)
}

// ---- emulator.stop -----------------------------------------------------------------

// Decision (plan m1i step 4, check; D12): a stop of emulator-5556 runs `emu kill` once and
// returns only once the serial has left the listing. The mutation this fails: returning
// before the serial left.
func TestStopKillsOnceAndWaitsForTheSerialToLeave(t *testing.T) {
	scaleStop(t, time.Second, 5*time.Millisecond)
	killOut, _ := os.ReadFile(filepath.Join("testdata", "emu-kill.txt"))
	avdName, _ := os.ReadFile(filepath.Join("testdata", "emu-avd-name.txt"))
	f := newFixture(t, map[string]string{
		"devices.out":             listOf(emuLine),
		serial + ".avd":           string(avdName),
		serial + ".emukill.out":   string(killOut),
		serial + ".emukill.leave": "1",
	})

	out := f.invoke(t, "emulator.stop", `{"device":"`+serial+`"}`)
	r := mustStop(t, out)
	if r.Device != serial || r.Avd != loadedAVD {
		t.Errorf("result = %+v; want device %s and avd %s", r, serial, loadedAVD)
	}
	if n := countCall(f.calls(t), "-s "+serial+" emu kill"); n != 1 {
		t.Errorf("%d `emu kill` calls, want one", n)
	}
	if listed := f.listing(t); len(listed) != 0 {
		t.Errorf("the listing still has %v after the stop returned", listed)
	}
}

// Decision (plan m1i step 4, check): an emulator that never leaves ends the stop at the
// bound with EMULATOR_STOP_TIMEOUT, and no command runs after that.
func TestStopTimesOutAndIssuesNoFurtherCommand(t *testing.T) {
	scaleStop(t, 120*time.Millisecond, 10*time.Millisecond)
	f := newFixture(t, map[string]string{
		"devices.out":             listOf(emuLine),
		serial + ".emukill.stays": "",
	})

	out := f.invoke(t, "emulator.stop", `{"device":"`+serial+`"}`)
	mustFail(t, out, ErrStopTimeout)
	if n := countCall(f.calls(t), "-s "+serial+" emu kill"); n != 1 {
		t.Errorf("%d `emu kill` calls, want one", n)
	}
	after := len(f.calls(t))
	time.Sleep(80 * time.Millisecond)
	if len(f.calls(t)) != after {
		t.Errorf("a command ran after the timeout: %v", f.calls(t)[after:])
	}
}

// Decision (plan m1i step 4, check; D12): stop runs the policy hook but not the readiness
// check, so an emulator listed offline or still booting (boot_completed 0) is killed
// without a getprop. The mutation this fails: the full preamble refused DEVICE_NOT_READY.
func TestStopKillsAnOfflineOrBootingEmulatorWithoutReadiness(t *testing.T) {
	t.Run("offline", func(t *testing.T) {
		scaleStop(t, time.Second, 5*time.Millisecond)
		f := newFixture(t, map[string]string{"devices.out": listOf(emuOff)})
		r := mustStop(t, f.invoke(t, "emulator.stop", `{"device":"`+serial+`"}`))
		if r.Avd != "" {
			t.Errorf("avd = %q; an offline emulator has no console to ask", r.Avd)
		}
		if n := countCall(f.calls(t), "-s "+serial+" emu kill"); n != 1 {
			t.Errorf("%d `emu kill` calls, want one", n)
		}
		if anyContains(f.calls(t), "getprop") {
			t.Errorf("a readiness probe ran: %v", f.calls(t))
		}
	})

	t.Run("booting", func(t *testing.T) {
		scaleStop(t, time.Second, 5*time.Millisecond)
		f := newFixture(t, map[string]string{
			"devices.out":                 listOf(emuLine),
			serial + ".bootcompleted.out": "0\n",
		})
		mustStop(t, f.invoke(t, "emulator.stop", `{"device":"`+serial+`"}`))
		if n := countCall(f.calls(t), "-s "+serial+" emu kill"); n != 1 {
			t.Errorf("%d `emu kill` calls, want one", n)
		}
		if anyContains(f.calls(t), "getprop") {
			t.Errorf("a readiness probe ran: %v", f.calls(t))
		}
	})
}

// Decision (plan m1i step 4, check): an emulator reached over ip:port is
// EMULATOR_NOT_AN_EMULATOR before any `emu` command.
func TestStopRefusesAnEmulatorOverIP(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listOf(ipLine)})
	out := f.invoke(t, "emulator.stop", `{"device":"192.168.0.2:5555"}`)
	mustFail(t, out, ErrNotAnEmulator)
	if anyContains(f.calls(t), "emu") {
		t.Errorf("an emu command ran: %v", f.calls(t))
	}
}

// Decision (plan m1i step 4, check): an unknown serial is DEVICE_NOT_FOUND with the online
// emulators, from the hook's listing.
func TestStopOfAnUnknownSerialIsNotFound(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listOf(emuLine2)})
	out := f.invoke(t, "emulator.stop", `{"device":"`+serial+`"}`)
	e := mustFail(t, out, device.ErrNotFound)
	if online, _ := e.Details["online"].([]string); len(online) != 1 || online[0] != serial2 {
		t.Errorf("online = %v; want [%s]", e.Details["online"], serial2)
	}
}

// Decision (plan m1i step 4, check): a physical serial is DEVICE_NOT_ALLOWED from the
// policy hook, before EMULATOR_NOT_AN_EMULATOR could apply.
func TestStopOfAPhysicalDeviceIsNotAllowed(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listOf(physLine)})
	out := f.invoke(t, "emulator.stop", `{"device":"abc12345"}`)
	mustFail(t, out, device.ErrNotAllowed)
	if anyContains(f.calls(t), "emu") {
		t.Errorf("an emu command ran: %v", f.calls(t))
	}
}

// Decision (plan m1i, D12/R5): a stop holds device.write:<serial>, so a device.launch on
// that serial queues behind it, while a stop of another serial does not.
func TestStopHoldsTheDeviceWriteKey(t *testing.T) {
	scaleStop(t, 3*time.Second, 20*time.Millisecond)
	f := newFixture(t, map[string]string{
		"devices.out":             listOf(emuLine, emuLine2),
		serial + ".emukill.stays": "",
	})

	stopID, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.stop", Params: []byte(`{"device":"` + serial + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer f.ops.Cancel(stopID)

	launchID, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.launch",
		Params:     []byte(`{"device":"` + serial + `","applicationId":"dev.droidyard.formapp"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer f.ops.Cancel(launchID)

	snap, err := f.ops.Wait(context.Background(), launchID, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != capability.StateQueued || snap.BlockedBy == nil || snap.BlockedBy.Capability != "emulator.stop" {
		t.Fatalf("launch = %s blockedBy %+v; want queued behind emulator.stop", snap.State, snap.BlockedBy)
	}

	// Another serial's key is free: that stop is not queued.
	otherID, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "emulator.stop", Params: []byte(`{"device":"` + serial2 + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer f.ops.Cancel(otherID)
	other, err := f.ops.Wait(context.Background(), otherID, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if other.State == capability.StateQueued {
		t.Errorf("a stop of %s queued behind %s's key, which is a different lock", serial2, serial)
	}
}

// Decision (level 4, step 5): a refused or failing `emu kill` whose serial stays listed is
// adb's failure, reported after a short grace instead of the whole stop bound (the adb
// guard's refusal on Pixel_9a took the full 20 s). Seen failing with the exit ignored.
func TestStopFailsFastWhenEmuKillFails(t *testing.T) {
	scaleStop(t, 10*time.Second, 10*time.Millisecond)
	g := killFailedGrace
	killFailedGrace = 100 * time.Millisecond
	t.Cleanup(func() { killFailedGrace = g })
	f := newFixture(t, map[string]string{
		"devices.out":            listOf(emuLine),
		serial + ".emukill.exit": "97\n",
		serial + ".emukill.err":  "m1i adb guard: refused\n",
	})
	start := time.Now()
	out := f.invoke(t, "emulator.stop", `{"device":"`+serial+`"}`)
	mustFail(t, out, device.ErrADBFailed)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("a failed kill took %s to report, want the short grace", d)
	}
}
