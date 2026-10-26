package emulator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/sdk/sdktest"
)

// The tests of AVD settings (plan m3f step 7; ADR-0129 §1, §9): the writer of config.ini,
// the table of formats, the validation, the edit's refusals, creation with settings and
// the listing. No test touches this machine's AVDs: every AVD is in a temporary HOME and
// the fake avdmanager and launcher are the ones step 6 uses.

func ptr[T any](v T) *T { return &v }

// roomy is a host with 8 GB of memory and 4 CPUs: ramMb up to 4096, cpuCores up to 4.
func roomy() hostLimits { return hostLimits{memMB: 8192, cpus: 4} }

// settingsFx is an AVD "dy" in the launcher's directory with the given config.ini.
func settingsFx(t *testing.T, config string) (*avdFx, string) {
	t.Helper()
	f := newAVD(t, nil)
	f.em.host = roomy
	home := f.avdIn(t, filepath.Join(f.home, ".android", "avd"), "dy")
	cfg := filepath.Join(home, "dy.avd", "config.ini")
	if err := os.WriteFile(cfg, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return f, cfg
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *avdFx) configure(t *testing.T, client capability.ClientKind, avd string, settings any) capability.Outcome {
	t.Helper()
	b, err := json.Marshal(map[string]any{"avd": avd, "settings": settings})
	if err != nil {
		t.Fatal(err)
	}
	return f.call(t, client, "emulator.configure", string(b))
}

// TestSettingsWriteKeepsOtherLinesByteForByte pins the writer's promise (step 7): config.ini
// is the user's file, and a write changes the lines whose keys it sets and nothing else.
// The file has a comment that names an allowlisted key, a line without an "=", unknown keys
// in both forms, CRLF endings, a blank line, a duplicate hw.ramSize and no final newline.
// After the write every other line is identical and in place, the first hw.ramSize is
// replaced (keeping its CRLF), the duplicate is gone, a key that was missing is appended,
// and a key the call does not set keeps its line as it was. Seen failing with the file
// rewritten from a map of its keys: the comment, the order and the endings were lost.
func TestSettingsWriteKeepsOtherLinesByteForByte(t *testing.T) {
	dir := t.TempDir()
	orig := "# comment hw.ramSize = 1\n" +
		"hw.device.name=pixel_9a\r\n" +
		"hw.ramSize=1536M\r\n" +
		"\n" +
		"a line without an equals sign\n" +
		"tag.id = google_apis\n" +
		"hw.ramSize = 999M\n" +
		"vm.heapSize=228M"
	if err := os.WriteFile(filepath.Join(dir, "config.ini"), []byte(orig), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := writeSettings(dir, Settings{RAMMB: ptr(2048), DisplayName: ptr("Pixel = test")}, roomy(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := "# comment hw.ramSize = 1\n" +
		"hw.device.name=pixel_9a\r\n" +
		"hw.ramSize=2048M\r\n" +
		"\n" +
		"a line without an equals sign\n" +
		"tag.id = google_apis\n" +
		"vm.heapSize=228M\n" +
		"avd.ini.displayname=Pixel = test\n"
	if have := readFile(t, filepath.Join(dir, "config.ini")); have != want {
		t.Errorf("config.ini =\n%q\nwant\n%q", have, want)
	}
	if got.RAMMB == nil || *got.RAMMB != 2048 || got.DisplayName == nil || *got.DisplayName != "Pixel = test" || got.VMHeapMB == nil || *got.VMHeapMB != 228 {
		t.Errorf("read back = %+v", got)
	}

	// A file written throughout with CRLF gets its new lines with CRLF.
	crlf := "hw.device.name=x\r\ntag.id = y\r\n"
	if err := os.WriteFile(filepath.Join(dir, "config.ini"), []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSettings(dir, Settings{CPUCores: ptr(2)}, roomy(), false); err != nil {
		t.Fatal(err)
	}
	if have := readFile(t, filepath.Join(dir, "config.ini")); have != crlf+"hw.cpu.ncore=2\r\n" {
		t.Errorf("config.ini = %q", have)
	}
}

// TestSettingsFormats pins the table of step 1 (g) (ADR-0129 §9): the key and the format of
// every setting, as the lines avdmanager writes them. RAM and heap in "<n>M", internal
// storage in bytes, boot as the pair of fastboot keys, graphics as an enabled GPU with the
// mode, and sdcardMb 0 as hw.sdCard = no that leaves sdcard.size as it was. Seen failing
// with the storage written in MB.
func TestSettingsFormats(t *testing.T) {
	all := Settings{
		DisplayName: ptr("My phone"), RAMMB: ptr(2048), VMHeapMB: ptr(256), InternalStorageMB: ptr(4096),
		CPUCores: ptr(4), Graphics: ptr("software"), Orientation: ptr("landscape"), DeviceFrame: ptr(false),
		Keyboard: ptr(true), Boot: ptr("cold"),
	}
	lines := map[string]string{}
	for _, l := range all.lines() {
		lines[l.key] = l.value
	}
	want := map[string]string{
		"avd.ini.displayname": "My phone", "hw.ramSize": "2048M", "vm.heapSize": "256M",
		"disk.dataPartition.size": "4294967296", "hw.cpu.ncore": "4", "hw.gpu.enabled": "yes",
		"hw.gpu.mode": "swiftshader_indirect", "hw.initialOrientation": "landscape",
		"showDeviceFrame": "no", "hw.keyboard": "yes", "fastboot.forceColdBoot": "yes", "fastboot.forceFastBoot": "no",
	}
	if len(lines) != len(want) {
		t.Errorf("lines = %v, want exactly %v", lines, want)
	}
	for k, v := range want {
		if lines[k] != v {
			t.Errorf("%s = %q, want %q", k, lines[k], v)
		}
	}
	for g, mode := range map[string]string{"automatic": "auto", "hardware": "host", "software": "swiftshader_indirect"} {
		for _, l := range (Settings{Graphics: ptr(g)}).lines() {
			if l.key == "hw.gpu.mode" && l.value != mode {
				t.Errorf("graphics %s -> hw.gpu.mode = %s, want %s", g, l.value, mode)
			}
		}
	}
	quick := map[string]string{}
	for _, l := range (Settings{Boot: ptr("quick")}).lines() {
		quick[l.key] = l.value
	}
	if quick["fastboot.forceColdBoot"] != "no" || quick["fastboot.forceFastBoot"] != "yes" || len(quick) != 2 {
		t.Errorf("boot quick = %v", quick)
	}

	// Through the writer: sdcardMb 0 sets hw.sdCard = no and touches nothing else of the card.
	dir := t.TempDir()
	orig := "hw.sdCard = yes\nsdcard.size = 512 MB\nfastboot.forceChosenSnapshotBoot = no\n"
	if err := os.WriteFile(filepath.Join(dir, "config.ini"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSettings(dir, Settings{SDCardMB: ptr(0), Boot: ptr("cold")}, roomy(), false); err != nil {
		t.Fatal(err)
	}
	have := readFile(t, filepath.Join(dir, "config.ini"))
	if want := "hw.sdCard = no\nsdcard.size = 512 MB\nfastboot.forceChosenSnapshotBoot = no\nfastboot.forceColdBoot = yes\nfastboot.forceFastBoot = no\n"; have != want {
		t.Errorf("config.ini = %q, want %q", have, want)
	}
	// sdcardMb above 0 is avdmanager's flag: no line.
	if n := len((Settings{SDCardMB: ptr(64)}).lines()); n != 0 {
		t.Errorf("sdcardMb 64 wrote %d lines", n)
	}
}

// TestSettingsValidated pins that no value outside its range is written (step 7): each field
// out of range, a display name with a newline or with U+202E (a format character), one with a
// space at an end, and a heap over half the RAM of the same call are refused as
// REQUEST_INVALID_PARAMS naming the field, and config.ini is as it was. The host has 8 GB
// and 4 CPUs. Seen failing with the control-character check removed: the newline was written.
func TestSettingsValidated(t *testing.T) {
	const orig = "hw.device.name=pixel_9a\nhw.ramSize=1536M\n"
	f, cfg := settingsFx(t, orig)
	for _, c := range []struct {
		field string
		set   map[string]any
	}{
		{"ramMb", map[string]any{"ramMb": 511}},
		{"ramMb", map[string]any{"ramMb": 4097}}, // above half the host's memory
		{"vmHeapMb", map[string]any{"vmHeapMb": 15}},
		{"vmHeapMb", map[string]any{"vmHeapMb": 1025}},
		{"vmHeapMb", map[string]any{"vmHeapMb": 600, "ramMb": 1024}}, // over half of ramMb
		{"internalStorageMb", map[string]any{"internalStorageMb": 2047}},
		{"internalStorageMb", map[string]any{"internalStorageMb": 262145}},
		{"cpuCores", map[string]any{"cpuCores": 0}},
		{"cpuCores", map[string]any{"cpuCores": 5}},
		{"graphics", map[string]any{"graphics": "gpu"}},
		{"orientation", map[string]any{"orientation": "upside_down"}},
		{"boot", map[string]any{"boot": "warm"}},
		{"displayName", map[string]any{"displayName": ""}},
		{"displayName", map[string]any{"displayName": strings.Repeat("x", 65)}},
		{"displayName", map[string]any{"displayName": "two\nlines"}},
		{"displayName", map[string]any{"displayName": "bidi\u202eevil"}},
		{"displayName", map[string]any{"displayName": " padded"}},
		{"vmHeapMb", map[string]any{"vmHeapMb": 800}}, // over half of the file's 1536M
	} {
		out := f.configure(t, capability.ClientUI, "dy", c.set)
		ce := mustFail(t, out, capability.ErrInvalidParams)
		if !strings.Contains(ce.Message, c.field) {
			t.Errorf("%v: message %q does not name %s", c.set, ce.Message, c.field)
		}
		if have := readFile(t, cfg); have != orig {
			t.Errorf("%v: config.ini changed to %q", c.set, have)
		}
	}
	if n := len(f.creates(t)); n != 0 {
		t.Errorf("avdmanager ran create %d times", n)
	}

	// The edges are accepted.
	ok := map[string]any{"ramMb": 512, "vmHeapMb": 16, "internalStorageMb": 2048, "cpuCores": 4, "displayName": strings.Repeat("é", 64)}
	if out := f.configure(t, capability.ClientUI, "dy", ok); out.State != capability.StateCompleted {
		t.Fatalf("the edges: %s %+v", out.State, out.Error)
	}
	// A call that sets nothing is not a change.
	mustFail(t, f.configure(t, capability.ClientUI, "dy", map[string]any{}), capability.ErrInvalidParams)
}

// TestSettingsRefuseLinks pins the writer's refusals (step 7): a config.ini that is a symlink
// and an AVD directory that is a symlink are EMULATOR_CONFIG_UNWRITABLE, and what the links
// point at is as it was. So is a config.ini that is not a regular file or is over 64 KiB.
func TestSettingsRefuseLinks(t *testing.T) {
	t.Run("config.ini a symlink", func(t *testing.T) {
		f, cfg := settingsFx(t, "x=1\n")
		target := filepath.Join(t.TempDir(), "elsewhere.ini")
		if err := os.WriteFile(target, []byte("target=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Remove(cfg)
		if err := os.Symlink(target, cfg); err != nil {
			t.Fatal(err)
		}
		mustFail(t, f.configure(t, capability.ClientUI, "dy", map[string]any{"cpuCores": 2}), ErrConfigUnwritable)
		if have := readFile(t, target); have != "target=1\n" {
			t.Errorf("the link's target changed: %q", have)
		}
		if fi, err := os.Lstat(cfg); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("config.ini is no longer the link: %v %v", fi, err)
		}
	})
	t.Run("the .avd directory a symlink", func(t *testing.T) {
		f := newAVD(t, nil)
		f.em.host = roomy
		home := filepath.Join(f.home, ".android", "avd")
		real := filepath.Join(t.TempDir(), "real.avd")
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(real, "config.ini"), []byte("x=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeAVDAt(t, home, "dy", "path="+filepath.Join(home, "dy.avd")+"\ntarget=android-35\n", t.TempDir(), "hw.device.name=pixel_9a\n")
		if err := os.Symlink(real, filepath.Join(home, "dy.avd")); err != nil {
			t.Fatal(err)
		}
		mustFail(t, f.configure(t, capability.ClientUI, "dy", map[string]any{"cpuCores": 2}), ErrConfigUnwritable)
		if have := readFile(t, filepath.Join(real, "config.ini")); have != "x=1\n" {
			t.Errorf("the link's target changed: %q", have)
		}
		if ents, _ := os.ReadDir(real); len(ents) != 1 {
			t.Errorf("the target directory holds %d entries, want its config.ini only", len(ents))
		}
	})
	t.Run("not a regular file, and too large", func(t *testing.T) {
		f, cfg := settingsFx(t, "x=1\n")
		os.Remove(cfg)
		if err := os.Mkdir(cfg, 0o755); err != nil {
			t.Fatal(err)
		}
		mustFail(t, f.configure(t, capability.ClientUI, "dy", map[string]any{"cpuCores": 2}), ErrConfigUnwritable)
		os.Remove(cfg)
		big := "# " + strings.Repeat("x", configCap) + "\n"
		if err := os.WriteFile(cfg, []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
		mustFail(t, f.configure(t, capability.ClientUI, "dy", map[string]any{"cpuCores": 2}), ErrConfigUnwritable)
		if have := readFile(t, cfg); have != big {
			t.Error("a file over 64 KiB was rewritten")
		}
	})
}

// TestSettingsAtomic pins the replace (step 7): a write stopped between the temporary file
// being written and the rename leaves the old config.ini intact and no temporary file in the
// directory; a write that completes keeps the file's mode and leaves no temporary either.
// Seen failing with the temporary file left behind on a failure.
func TestSettingsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.ini")
	const orig = "hw.ramSize=1536M\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	only := func(t *testing.T) {
		t.Helper()
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(ents) != 1 || ents[0].Name() != "config.ini" {
			var names []string
			for _, e := range ents {
				names = append(names, e.Name())
			}
			t.Errorf("the directory holds %v, want config.ini only", names)
		}
	}
	settingsFault = func() error { return errors.New("stopped for the test") }
	t.Cleanup(func() { settingsFault = nil })
	_, err := writeSettings(dir, Settings{RAMMB: ptr(2048)}, roomy(), false)
	var ce *capability.Error
	if !errors.As(err, &ce) || ce.Code != ErrConfigUnwritable {
		t.Fatalf("a stopped write: %v, want %s", err, ErrConfigUnwritable)
	}
	if have := readFile(t, path); have != orig {
		t.Errorf("config.ini = %q after a stopped write", have)
	}
	only(t)

	settingsFault = nil
	if _, err := writeSettings(dir, Settings{RAMMB: ptr(2048)}, roomy(), false); err != nil {
		t.Fatal(err)
	}
	if have := readFile(t, path); have != "hw.ramSize=2048M\n" {
		t.Errorf("config.ini = %q", have)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v (%v), want the original 0600", fi.Mode().Perm(), err)
	}
	only(t)
}

// TestConfigureRefusesRunning pins ADR-0129 §7 for an edit: a running AVD is never edited. The
// AVD runs by process (an emulator process of the user whose arguments name it, no adb
// listing), as step 6's delete is refused; config.ini is as it was. Seen failing with the
// running check removed from emulator.configure.
func TestConfigureRefusesRunning(t *testing.T) {
	const orig = "hw.ramSize=1536M\n"
	f, cfg := settingsFx(t, orig)
	f.em.procs = realProcs
	helperAt(t, filepath.Join(t.TempDir(), "qemu-system-aarch64"), "sleep", "-avd", "dy", "-no-window")
	ce := mustFail(t, f.configure(t, capability.ClientUI, "dy", map[string]any{"ramMb": 2048}), ErrAVDRunning)
	if !strings.Contains(ce.NextStep, "change it again") {
		t.Errorf("nextStep = %q", ce.NextStep)
	}
	if have := readFile(t, cfg); have != orig {
		t.Errorf("a running AVD's config.ini changed to %q", have)
	}
}

// TestConfigureRefusesAmbiguous pins ADR-0129 §7 for an edit: a name with an ini in two homes
// is not edited, since which copy is meant is a guess; both are as they were.
func TestConfigureRefusesAmbiguous(t *testing.T) {
	avdHome := t.TempDir()
	f := newAVD(t, nil, "ANDROID_AVD_HOME="+avdHome)
	f.em.host = roomy
	a := f.avdIn(t, avdHome, "dy")
	b := f.avdIn(t, filepath.Join(f.home, ".android", "avd"), "dy")
	ce := mustFail(t, f.configure(t, capability.ClientUI, "dy", map[string]any{"cpuCores": 2}), ErrAVDAmbiguous)
	if !strings.Contains(ce.Cause, "to edit") || strings.Contains(ce.Cause, avdHome) {
		t.Errorf("cause = %q", ce.Cause)
	}
	for _, d := range []string{a, b} {
		if have := readFile(t, filepath.Join(d, "dy.avd", "config.ini")); have != "hw.device.name=pixel_9a\n" {
			t.Errorf("config.ini of a copy = %q", have)
		}
	}
}

// TestConfigureNotListed: a name the launcher does not list is EMULATOR_AVD_NOT_FOUND.
func TestConfigureNotListed(t *testing.T) {
	f, _ := settingsFx(t, "x=1\n")
	mustFail(t, f.configure(t, capability.ClientUI, "other", map[string]any{"cpuCores": 2}), ErrAVDNotFound)
	mustFail(t, f.configure(t, capability.ClientUI, "../dy", map[string]any{"cpuCores": 2}), capability.ErrInvalidParams)
}

// TestConfigureRefusesShrinkAndSdcard pins the two refusals of step 7: sdcardMb is not
// accepted by an edit (an SD card is not resized here), even as 0, and internal storage is
// never lowered below what the AVD has. Equal and larger are written. Seen failing with the
// shrink check removed.
func TestConfigureRefusesShrinkAndSdcard(t *testing.T) {
	const orig = "disk.dataPartition.size = 4294967296\nhw.sdCard = yes\nsdcard.size = 512 MB\n"
	f, cfg := settingsFx(t, orig)
	for _, set := range []map[string]any{{"sdcardMb": 64}, {"sdcardMb": 0}, {"cpuCores": 2, "sdcardMb": 512}} {
		ce := mustFail(t, f.configure(t, capability.ClientUI, "dy", set), capability.ErrInvalidParams)
		if !strings.Contains(ce.Cause, "sdcardMb") {
			t.Errorf("%v: cause = %q", set, ce.Cause)
		}
	}
	ce := mustFail(t, f.configure(t, capability.ClientUI, "dy", map[string]any{"internalStorageMb": 2048}), capability.ErrInvalidParams)
	if !strings.Contains(ce.Cause, "4096") {
		t.Errorf("cause = %q, want the current size", ce.Cause)
	}
	if have := readFile(t, cfg); have != orig {
		t.Errorf("a refused edit changed config.ini to %q", have)
	}
	out := f.configure(t, capability.ClientUI, "dy", map[string]any{"internalStorageMb": 4096})
	if out.State != capability.StateCompleted {
		t.Fatalf("storage kept: %s %+v", out.State, out.Error)
	}
	out = f.configure(t, capability.ClientUI, "dy", map[string]any{"internalStorageMb": 8192, "boot": "cold"})
	if out.State != capability.StateCompleted {
		t.Fatalf("storage grown: %s %+v", out.State, out.Error)
	}
	r := out.Result.(ConfigureResult)
	if r.Avd != "dy" || r.Settings.InternalStorageMB == nil || *r.Settings.InternalStorageMB != 8192 || r.Settings.Boot == nil || *r.Settings.Boot != "cold" {
		t.Errorf("result = %+v, want the settings as read back", r)
	}
	if r.Settings.SDCardMB == nil || *r.Settings.SDCardMB != 512 {
		t.Errorf("the SD card = %v, want it read back as the 512 MB it was", r.Settings.SDCardMB)
	}
	if have := readFile(t, cfg); !strings.Contains(have, "disk.dataPartition.size = 8589934592\n") || !strings.Contains(have, "sdcard.size = 512 MB\n") {
		t.Errorf("config.ini = %q", have)
	}
}

// TestConfigureRefusedOverMCP pins ADR-0129 §1: emulator.configure is a person's call; an
// agent's session is refused before anything runs and nothing is written. Seen failing with
// the audience set to all. The descriptor is the contract the schema publishes.
func TestConfigureRefusedOverMCP(t *testing.T) {
	const orig = "hw.ramSize=1536M\n"
	f, cfg := settingsFx(t, orig)
	_, err := f.ops.Start(t.Context(), capability.Request{Capability: "emulator.configure", Params: []byte(`{"avd":"dy","settings":{"ramMb":2048}}`),
		Client: capability.ClientMCP, Origin: capability.Origin{ID: "mcp-1", Client: capability.ClientMCP}})
	if codeOf(err) != capability.ErrNotAllowedHere {
		t.Fatalf("an agent's emulator.configure: %v, want %s", err, capability.ErrNotAllowedHere)
	}
	if have := readFile(t, cfg); have != orig {
		t.Errorf("config.ini = %q after a refused call", have)
	}
	d, ok := f.reg().Lookup("emulator.configure")
	if !ok || d.Audience != capability.AudienceHuman || d.Lendable || !d.LongRunning || d.Scope != capability.Global || d.Kind != capability.Command {
		t.Errorf("descriptor = %+v", d)
	}
	if a := d.Annotations; a.Destructive == nil || !*a.Destructive || a.Reason == "" {
		t.Errorf("annotations = %+v, want destructive with a reason", a)
	}
}

// TestCreateWithSettings pins emulator.create's settings (step 7): sdcardMb is avdmanager's
// --sdcard flag (64 -> "64M", after the other arguments), and the rest is written to the
// config.ini avdmanager made, whose own lines stay.
func TestCreateWithSettings(t *testing.T) {
	f := newAVD(t, nil)
	f.em.host = roomy
	out := f.call(t, capability.ClientMCP, "emulator.create", `{"avd":"dy_new","profile":"pixel_9a","systemImage":"`+testImage+`","settings":{"sdcardMb":64,"ramMb":2048,"displayName":"My phone","boot":"cold","internalStorageMb":4096}}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("emulator.create %s: %+v", out.State, out.Error)
	}
	creates := f.creates(t)
	if len(creates) != 1 {
		t.Fatalf("%d creates", len(creates))
	}
	if got, want := strings.Join(creates[0].Args, "|"), "create|avd|--name|dy_new|--package|"+testImage+"|--device|pixel_9a|--sdcard|64M"; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
	cfg := readFile(t, filepath.Join(f.home, ".android", "avd", "dy_new.avd", "config.ini"))
	for _, want := range []string{"hw.device.name=pixel_9a\n", "hw.ramSize=2048M\n", "avd.ini.displayname=My phone\n",
		"fastboot.forceColdBoot=yes\n", "fastboot.forceFastBoot=no\n", "disk.dataPartition.size=4294967296\n"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.ini lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "hw.sdCard") || strings.Contains(cfg, "sdcard.size") {
		t.Errorf("sdcardMb 64 wrote a line (the flag is the whole of it):\n%s", cfg)
	}
	l := f.list(t)
	if len(l.AVDs) != 1 || l.AVDs[0].Config == nil || l.AVDs[0].Config.Settings == nil || *l.AVDs[0].Config.Settings.RAMMB != 2048 {
		t.Errorf("emulator.list = %+v, want the settings reported", l.AVDs)
	}

	// sdcardMb 0: no flag; the writer sets hw.sdCard = no.
	out = f.call(t, capability.ClientMCP, "emulator.create", `{"avd":"nocard","profile":"pixel_9a","systemImage":"`+testImage+`","settings":{"sdcardMb":0}}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("emulator.create %s: %+v", out.State, out.Error)
	}
	if c := f.creates(t)[1]; strings.Contains(strings.Join(c.Args, "|"), "--sdcard") {
		t.Errorf("sdcardMb 0 passed a flag: %v", c.Args)
	}
	if cfg := readFile(t, filepath.Join(f.home, ".android", "avd", "nocard.avd", "config.ini")); !strings.Contains(cfg, "hw.sdCard=no\n") {
		t.Errorf("config.ini = %q, want hw.sdCard=no", cfg)
	}

	// An invalid setting is refused before avdmanager runs.
	before := len(f.creates(t))
	mustFail(t, f.call(t, capability.ClientMCP, "emulator.create", `{"avd":"bad","profile":"pixel_9a","systemImage":"`+testImage+`","settings":{"ramMb":100}}`), capability.ErrInvalidParams)
	if len(f.creates(t)) != before {
		t.Error("avdmanager ran for a setting out of range")
	}
}

// TestCreateAllOrNothing pins the all-or-nothing of step 7: when the writer fails after
// avdmanager made the AVD, the AVD is deleted again (avdmanager delete avd) and the call
// fails with the writer's error, so no AVD is left. When that delete fails too, the error
// says an AVD was left behind and what to do. Seen failing with the delete removed.
func TestCreateAllOrNothing(t *testing.T) {
	create := func(f *avdFx) capability.Outcome {
		return f.call(t, capability.ClientMCP, "emulator.create", `{"avd":"dy_new","profile":"pixel_9a","systemImage":"`+testImage+`","settings":{"ramMb":2048}}`)
	}
	t.Cleanup(func() { settingsFault = nil })
	t.Run("the writer fails", func(t *testing.T) {
		f := newAVD(t, nil)
		f.em.host = roomy
		settingsFault = func() error { return errors.New("stopped for the test") }
		mustFail(t, create(f), ErrConfigUnwritable)
		home := filepath.Join(f.home, ".android", "avd")
		if exists(filepath.Join(home, "dy_new.ini")) || exists(filepath.Join(home, "dy_new.avd")) {
			t.Error("the AVD is still there")
		}
		if len(f.deletes(t)) != 1 {
			t.Errorf("%d deletes, want 1", len(f.deletes(t)))
		}
		if l := f.list(t); len(l.AVDs) != 0 {
			t.Errorf("emulator.list = %+v", l.AVDs)
		}
	})
	t.Run("and so does the delete", func(t *testing.T) {
		f := newAVD(t, nil)
		f.em.host = roomy
		settingsFault = func() error { return errors.New("stopped for the test") }
		sdktest.Write(t, f.toolDir, "delete.noop", "")
		ce := mustFail(t, create(f), ErrConfigUnwritable)
		if !strings.Contains(ce.Cause, "could not be deleted") || !strings.Contains(ce.NextStep, "emulator.delete") {
			t.Errorf("cause %q, nextStep %q", ce.Cause, ce.NextStep)
		}
	})
}

// TestListReportsSettings pins the reading side of the table (step 1 (g)'s forms): with or
// without spaces around "=", RAM and heap as "<n>M", storage in bytes, sdcard.size as
// "<n> MB" or "<n>M", hw.sdCard = no as an SD card of 0 beside a sdcard.size that stays, the
// avdmanager default (gpu enabled = no, mode = auto) as automatic, and the boot pair. A key
// that is absent or in another form (1.5G, a storage in bytes that is not whole MB, both
// fastboot keys false) is left out.
func TestListReportsSettings(t *testing.T) {
	f := newAVD(t, nil)
	home := filepath.Join(f.home, ".android", "avd")
	put := func(name, config string) {
		f.avdIn(t, home, name)
		if err := os.WriteFile(filepath.Join(home, name+".avd", "config.ini"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("spaced", "hw.ramSize = 1536M\nvm.heapSize = 228M\ndisk.dataPartition.size = 6442450944\nhw.sdCard = yes\nsdcard.size = 512 MB\n"+
		"hw.cpu.ncore = 4\nhw.gpu.enabled = no\nhw.gpu.mode = auto\nhw.initialOrientation = portrait\nshowDeviceFrame = yes\n"+
		"fastboot.forceColdBoot = no\nfastboot.forceFastBoot = yes\nfastboot.forceChosenSnapshotBoot = no\nhw.keyboard = no\navd.ini.displayname = Pixel 9a\n")
	put("tight", "hw.ramSize=2048M\nvm.heapSize=256M\nhw.sdCard=yes\nsdcard.size=64M\nhw.cpu.ncore=2\nhw.gpu.enabled=yes\nhw.gpu.mode=host\n"+
		"fastboot.forceColdBoot=yes\nfastboot.forceFastBoot=no\nhw.keyboard=yes\n")
	put("nocard", "hw.sdCard = no\nsdcard.size = 512 MB\nhw.gpu.enabled = yes\nhw.gpu.mode = swiftshader_indirect\n")
	put("odd", "hw.cpu.ncore = 3\nhw.ramSize = 1.5G\ndisk.dataPartition.size = 1000\nhw.sdCard = yes\nhw.gpu.enabled = no\nhw.gpu.mode = host\n"+
		"fastboot.forceColdBoot = no\nfastboot.forceFastBoot = no\n")
	// avdmanager 23's exact lines: no spaces, a G unit.
	put("avd23", "hw.ramSize=2G\ndisk.dataPartition.size=10G\nsdcard.size=64M\nvm.heapSize=228M\n")
	put("gb", "hw.ramSize = 1 GB\nvm.heapSize = 1GB\nhw.sdCard = yes\nsdcard.size = 2 GB\n")
	put("bare", "hw.device.name=pixel_9a\n")
	f.launcher(t, "spaced\ntight\nnocard\nodd\navd23\ngb\nbare\n")

	by := map[string]*Settings{}
	for _, a := range f.list(t).AVDs {
		if a.Config == nil {
			t.Fatalf("%s has no config", a.Name)
		}
		by[a.Name] = a.Config.Settings
	}
	i := func(p *int) any {
		if p == nil {
			return nil
		}
		return *p
	}
	s := func(p *string) any {
		if p == nil {
			return nil
		}
		return *p
	}
	b := func(p *bool) any {
		if p == nil {
			return nil
		}
		return *p
	}
	sp := by["spaced"]
	if sp == nil || i(sp.RAMMB) != 1536 || i(sp.VMHeapMB) != 228 || i(sp.InternalStorageMB) != 6144 || i(sp.SDCardMB) != 512 || i(sp.CPUCores) != 4 ||
		s(sp.Graphics) != "automatic" || s(sp.Orientation) != "portrait" || b(sp.DeviceFrame) != true || s(sp.Boot) != "quick" || b(sp.Keyboard) != false || s(sp.DisplayName) != "Pixel 9a" {
		t.Errorf("spaced = %+v", sp)
	}
	ti := by["tight"]
	if ti == nil || i(ti.RAMMB) != 2048 || i(ti.VMHeapMB) != 256 || i(ti.SDCardMB) != 64 || i(ti.CPUCores) != 2 || s(ti.Graphics) != "hardware" || s(ti.Boot) != "cold" || b(ti.Keyboard) != true {
		t.Errorf("tight = %+v", ti)
	}
	nc := by["nocard"]
	if nc == nil || i(nc.SDCardMB) != 0 || s(nc.Graphics) != "software" {
		t.Errorf("nocard = %+v, want an SD card of 0 and software graphics", nc)
	}
	od := by["odd"]
	if od == nil || i(od.CPUCores) != 3 || od.RAMMB != nil || od.InternalStorageMB != nil || od.SDCardMB != nil || od.Graphics != nil || od.Boot != nil {
		t.Errorf("odd = %+v, want only the cores, every other key left out", od)
	}
	a23 := by["avd23"]
	if a23 == nil || i(a23.RAMMB) != 2048 || i(a23.InternalStorageMB) != 10240 || i(a23.VMHeapMB) != 228 {
		t.Errorf("avd23 = %+v, want 2G -> 2048 and 10G -> 10240 MB", a23)
	}
	gb := by["gb"]
	if gb == nil || i(gb.RAMMB) != 1024 || i(gb.VMHeapMB) != 1024 || i(gb.SDCardMB) != 2048 {
		t.Errorf("gb = %+v, want GB and \"<n> GB\" read as 1024 MB each", gb)
	}
	if by["bare"] != nil {
		t.Errorf("bare = %+v, want no settings", by["bare"])
	}
}

// TestSettingsWriteKeepsSeparatorStyle pins that the writer follows the file: a replaced line
// keeps its own separator, an appended key takes the style most lines use, and a tie is
// "key=value" (avdmanager 23's). Seen failing when the writer always wrote "key = value":
// every setting changed made the file mixed.
func TestSettingsWriteKeepsSeparatorStyle(t *testing.T) {
	run := func(orig string, s Settings) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.ini"), []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := writeSettings(dir, s, roomy(), false); err != nil {
			t.Fatal(err)
		}
		return readFile(t, filepath.Join(dir, "config.ini"))
	}
	set := Settings{RAMMB: ptr(2048), CPUCores: ptr(2)}
	if got, want := run("a=b\nhw.ramSize=1024M\nc=d\n", set), "a=b\nhw.ramSize=2048M\nc=d\nhw.cpu.ncore=2\n"; got != want {
		t.Errorf("tight file = %q, want %q", got, want)
	}
	if got, want := run("a = b\nhw.ramSize = 1024M\nc = d\n", set), "a = b\nhw.ramSize = 2048M\nc = d\nhw.cpu.ncore = 2\n"; got != want {
		t.Errorf("spaced file = %q, want %q", got, want)
	}
	if got, want := run("a = b\nc = d\nhw.ramSize=1024M\n", set), "a = b\nc = d\nhw.ramSize=2048M\nhw.cpu.ncore = 2\n"; got != want {
		t.Errorf("mixed file = %q, want %q", got, want)
	}
	if got, want := run("a = b\nc=d\n", Settings{CPUCores: ptr(2)}), "a = b\nc=d\nhw.cpu.ncore=2\n"; got != want {
		t.Errorf("tie = %q, want %q", got, want)
	}
}
