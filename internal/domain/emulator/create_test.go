package emulator

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/emulator/emulatortest"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
	"github.com/jtortosasen/droidyard/internal/domain/sdk/sdktest"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
)

// The tests of emulator.create and emulator.delete (plan m3f step 6) run the subsystem over
// the fake adb, the fake launcher and the fake avdmanager (sdktest), the last started
// through a real supervisor, so the process kind and the group are the shipped ones. No
// test reads this machine's processes or files: the process listing and lsof are replaced
// unless the test is about them.

const testImage = "system-images;android-35;google_apis;arm64-v8a"

type avdFx struct {
	*fixture
	sdkRoot string // the SDK holding the fake avdmanager
	toolDir string // the fake avdmanager's scenario directory
	home    string // the daemon's HOME
	jdk     string
	tool    *sdk.Tool
}

// supStarter adapts a supervisor to the avdmanager runner's starter, as cmd/droidyard does.
func supStarter(sup *process.Supervisor) sdk.RecordedStarter {
	return func(cmd *exec.Cmd, kind string) (sdk.Recorded, error) {
		r, err := sup.SpawnRecorded(cmd, process.Kind(kind))
		if err != nil {
			return nil, err
		}
		return r, nil
	}
}

// newAVD is the subsystem over an SDK with the fake avdmanager, the image testImage
// installed and seven device profiles, with the processes of the host replaced by none.
func newAVD(t *testing.T, adbFiles map[string]string, envExtra ...string) *avdFx {
	t.Helper()
	if adbFiles == nil {
		adbFiles = map[string]string{"devices.out": listHead}
	}
	sdkRoot, toolDir := t.TempDir(), t.TempDir()
	sdktest.InstallTools(t, sdkRoot, "latest", "12.0")
	sdktest.Write(t, toolDir, "devices.out", string(sdktest.DevicesOut()))
	installImage(t, sdkRoot, testImage)
	extra := append([]string{"ANDROID_HOME=" + sdkRoot, sdktest.DirEnv + "=" + toolDir}, envExtra...)
	f := newFixture(t, adbFiles, extra...)
	emulatortest.Write(t, f.ldir, "list-avds.fromhome", nil)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sup := process.New(process.Options{Store: st, Log: io.Discard})
	if err := sup.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Shutdown)
	jdk := sdktest.JavaHome(t)
	jvm := func() gradle.JVM {
		return gradle.Discover(gradle.Finder{GOOS: "linux", Env: []string{"JAVA_HOME=" + jdk}, StudioHomes: func() []string { return nil }})
	}
	tool := sdk.NewTool(sdk.ToolOptions{Env: f.env, JVM: jvm, Start: supStarter(sup), TermGrace: time.Second})
	f.em.SetTool(tool)
	f.em.procs = func() ([]platform.Proc, error) { return nil, nil }
	f.em.open = func(context.Context, []int, []string) ([]platform.OpenFile, error) { return nil, nil }
	return &avdFx{fixture: f, sdkRoot: sdkRoot, toolDir: toolDir, home: envMap(f.env)["HOME"], jdk: jdk, tool: tool}
}

// installImage makes id an installed system image of the SDK at root.
func installImage(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(strings.ReplaceAll(id, ";", "/")))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><ns2:repository xmlns:ns2="http://schemas.android.com/repository/android/common/02"><localPackage path="` + id + `"><revision><major>9</major></revision></localPackage></ns2:repository>`
	if err := os.WriteFile(filepath.Join(dir, "package.xml"), []byte(xml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// call runs a capability through the scheduler as the given client.
func (f *avdFx) call(t *testing.T, client capability.ClientKind, name, params string) capability.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	id, err := f.ops.Start(ctx, capability.Request{Capability: name, Params: []byte(params), Client: client,
		Origin: capability.Origin{ID: string(client) + "-1", Client: client}})
	if err != nil {
		t.Fatalf("Start %s: %v", name, err)
	}
	out, err := f.ops.Wait(ctx, id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *avdFx) create(t *testing.T, avd, profile, image string) capability.Outcome {
	t.Helper()
	return f.call(t, capability.ClientMCP, "emulator.create",
		`{"avd":`+quote(avd)+`,"profile":`+quote(profile)+`,"systemImage":`+quote(image)+`}`)
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

// creates are the `create avd` calls the fake avdmanager logged.
func (f *avdFx) creates(t *testing.T) []sdktest.Call {
	t.Helper()
	var out []sdktest.Call
	for _, c := range sdktest.Calls(t, f.toolDir) {
		if len(c.Args) > 1 && c.Args[0] == "create" {
			out = append(out, c)
		}
	}
	return out
}

func codeOf(err error) capability.ErrorCode {
	var ce *capability.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// TestCreateArgvEnvStdin pins what emulator.create runs (ADR-0129 §7, ADR-0130 §5) and its
// outcome: avdmanager gets the name, image and profile as separate arguments, the fixed
// "no" on stdin, and ANDROID_AVD_HOME set to the one directory the launcher lists, so
// emulator.list then finds the AVD. ANDROID_EMULATOR_HOME and ANDROID_USER_HOME name
// directories the launcher never lists (step 1 (d)), and the HOME default is made. Seen
// failing with the home taken from avdDirs' first entry: the AVD went where the fake
// launcher, which keeps the observed rule, does not list it.
func TestCreateArgvEnvStdin(t *testing.T) {
	f := newAVD(t, nil, "ANDROID_EMULATOR_HOME="+t.TempDir(), "ANDROID_USER_HOME="+t.TempDir())
	out := f.create(t, "dy_new", "pixel_9a", testImage)
	if out.State != capability.StateCompleted {
		t.Fatalf("emulator.create %s: %+v", out.State, out.Error)
	}
	if r := out.Result.(CreateResult); r != (CreateResult{Avd: "dy_new", Profile: "pixel_9a", SystemImage: testImage}) {
		t.Errorf("result = %+v", r)
	}
	creates := f.creates(t)
	if len(creates) != 1 {
		t.Fatalf("avdmanager ran create %d times, want 1", len(creates))
	}
	c := creates[0]
	if got, want := strings.Join(c.Args, "|"), "create|avd|--name|dy_new|--package|"+testImage+"|--device|pixel_9a"; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
	home := filepath.Join(f.home, ".android", "avd")
	if c.Env["ANDROID_AVD_HOME"] != home || c.Env["ANDROID_HOME"] != f.sdkRoot || c.Env["JAVA_HOME"] != f.jdk || c.Env["ANDROID_SDK_ROOT"] != "<unset>" {
		t.Errorf("env = %v, want ANDROID_AVD_HOME %s, ANDROID_HOME %s, JAVA_HOME %s, no ANDROID_SDK_ROOT", c.Env, home, f.sdkRoot, f.jdk)
	}
	if c.Stdin != "no" {
		t.Errorf("stdin = %q, want the fixed no", c.Stdin)
	}
	listed := f.list(t)
	if len(listed.AVDs) != 1 || listed.AVDs[0].Name != "dy_new" {
		t.Fatalf("emulator.list = %+v, want the new AVD", listed.AVDs)
	}
	if cfg := listed.AVDs[0].Config; cfg == nil || cfg.DeviceProfile != "pixel_9a" || cfg.ABI != "arm64-v8a" {
		t.Errorf("the created AVD's config = %+v", cfg)
	}
}

// TestCreateRefusesANameInAnyHome pins ADR-0129 §7: a name present in any of the homes AVD
// discovery reads is refused (EMULATOR_AVD_EXISTS) before avdmanager runs, though the
// launcher lists one directory only: a home it ignores today is one it may read tomorrow.
// The cause names the variable, never a path. Seen failing with only the first home
// checked.
func TestCreateRefusesANameInAnyHome(t *testing.T) {
	for _, h := range []struct{ name, where string }{
		{"ANDROID_AVD_HOME", "ANDROID_AVD_HOME"},
		{"ANDROID_EMULATOR_HOME", "ANDROID_EMULATOR_HOME/avd"},
		{"ANDROID_USER_HOME", "ANDROID_USER_HOME/avd"},
		{"ANDROID_SDK_HOME", "ANDROID_SDK_HOME/.android/avd"},
		{"HOME", "~/.android/avd"},
	} {
		t.Run(h.name, func(t *testing.T) {
			dirs := map[string]string{}
			var env []string
			for _, v := range []string{"ANDROID_AVD_HOME", "ANDROID_EMULATOR_HOME", "ANDROID_USER_HOME", "ANDROID_SDK_HOME"} {
				dirs[v] = t.TempDir()
				env = append(env, v+"="+dirs[v])
			}
			f := newAVD(t, nil, env...)
			dirs["HOME"] = f.home
			base := dirs[h.name]
			avdDir := base
			switch h.name {
			case "ANDROID_EMULATOR_HOME", "ANDROID_USER_HOME":
				avdDir = filepath.Join(base, "avd")
			case "ANDROID_SDK_HOME", "HOME":
				avdDir = filepath.Join(base, ".android", "avd")
			}
			writeAVD(t, avdDir, "taken", "target=android-35\n", "hw.device.name=pixel_9a\n")
			out := f.create(t, "taken", "pixel_9a", testImage)
			ce := mustFail(t, out, ErrAVDExists)
			if !strings.Contains(ce.Cause, h.where) || strings.Contains(ce.Cause, base) {
				t.Errorf("cause = %q, want the variable %q and no path", ce.Cause, h.where)
			}
			if n := len(sdktest.Calls(t, f.toolDir)); n != 0 {
				t.Errorf("avdmanager ran %d times for a name that exists", n)
			}
		})
	}

	t.Run("a directory without its ini where the new one would go", func(t *testing.T) {
		f := newAVD(t, nil)
		if err := os.MkdirAll(filepath.Join(f.home, ".android", "avd", "left.avd"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustFail(t, f.create(t, "left", "pixel_9a", testImage), ErrAVDExists)
	})
	t.Run("the launcher lists it", func(t *testing.T) {
		f := newAVD(t, nil)
		f.launcher(t, "elsewhere\n")
		ce := mustFail(t, f.create(t, "elsewhere", "pixel_9a", testImage), ErrAVDExists)
		if !strings.Contains(ce.Cause, "launcher") {
			t.Errorf("cause = %q", ce.Cause)
		}
		if n := len(sdktest.Calls(t, f.toolDir)); n != 0 {
			t.Errorf("avdmanager ran %d times", n)
		}
	})
}

// TestCreateMissingImage pins the refusal before any run: a system image that is not
// installed (or is not a system image id at all) is EMULATOR_IMAGE_NOT_INSTALLED, the
// next step naming sdk.install of that id when it is one, and the installed images
// listed; avdmanager is not asked, so it never downloads or prompts.
func TestCreateMissingImage(t *testing.T) {
	f := newAVD(t, nil)
	other := "system-images;android-34;default;arm64-v8a"
	ce := mustFail(t, f.create(t, "dy", "pixel_9a", other), ErrImageNotInstalled)
	if !strings.Contains(ce.NextStep, "sdk.install") || !strings.Contains(ce.NextStep, other) {
		t.Errorf("nextStep = %q, want sdk.install of %s", ce.NextStep, other)
	}
	if inst, _ := ce.Details["installed"].([]string); !slices.Equal(inst, []string{testImage}) {
		t.Errorf("details = %v, want the installed image listed", ce.Details)
	}
	for _, bad := range []string{"platforms;android-35", "system-images;android-35", "-rf", ""} {
		ce := mustFail(t, f.create(t, "dy", "pixel_9a", bad), ErrImageNotInstalled)
		if strings.Contains(ce.NextStep, "sdk.install {") {
			t.Errorf("%q is no image id and its nextStep offers to install it: %q", bad, ce.NextStep)
		}
	}
	if n := len(sdktest.Calls(t, f.toolDir)); n != 0 {
		t.Errorf("avdmanager ran %d times", n)
	}
}

// TestCreateUnknownProfile pins EMULATOR_PROFILE_NOT_FOUND: a profile avdmanager does not
// list, with up to five candidates (the ids whose id or name is the request ignoring case
// and punctuation, else those that contain it), and no create run.
func TestCreateUnknownProfile(t *testing.T) {
	f := newAVD(t, nil)
	ce := mustFail(t, f.create(t, "dy", "Pixel 9A", testImage), ErrProfileNotFound)
	if c, _ := ce.Details["candidates"].([]string); !slices.Equal(c, []string{"pixel_9a"}) {
		t.Errorf("candidates = %v, want pixel_9a for Pixel 9A", ce.Details["candidates"])
	}
	ce = mustFail(t, f.create(t, "dy", "pixel", testImage), ErrProfileNotFound)
	if c, _ := ce.Details["candidates"].([]string); !slices.Equal(c, []string{"pixel_9a", "pixel_6"}) {
		t.Errorf("candidates = %v, want pixel_9a and pixel_6 for pixel", ce.Details["candidates"])
	}
	ce = mustFail(t, f.create(t, "dy", "zzz", testImage), ErrProfileNotFound)
	if _, has := ce.Details["candidates"]; has || !strings.Contains(ce.NextStep, "sdk.profiles") {
		t.Errorf("an unrelated request got candidates %v or no pointer to sdk.profiles (%q)", ce.Details, ce.NextStep)
	}
	if n := len(f.creates(t)); n != 0 {
		t.Errorf("avdmanager ran create %d times", n)
	}
	if got := profileNotFound(manyProfiles(), "phone"); got == nil {
		t.Fatal("no error")
	} else if c := got.(*capability.Error).Details["candidates"].([]string); len(c) != 5 {
		t.Errorf("%d candidates, want the limit of five", len(c))
	}
}

func manyProfiles() []sdk.Profile {
	var ps []sdk.Profile
	for _, id := range []string{"phone_a", "phone_b", "phone_c", "phone_d", "phone_e", "phone_f", "phone_g"} {
		ps = append(ps, sdk.Profile{ID: id, Name: strings.ToUpper(id)})
	}
	return ps
}

// TestCreateToolErrors: the tool's own errors pass through with their codes: no cmdline-tools
// (naming sdk.install), a failing run (the decisive line), and a run that exits 0 and wrote
// nothing the launcher lists.
func TestCreateToolErrors(t *testing.T) {
	t.Run("no tools", func(t *testing.T) {
		f := newAVD(t, nil)
		os.RemoveAll(filepath.Join(f.sdkRoot, "cmdline-tools"))
		ce := mustFail(t, f.create(t, "dy", "pixel_9a", testImage), sdk.ErrToolsNotFound)
		if !strings.Contains(ce.NextStep, "cmdline-tools;latest") {
			t.Errorf("nextStep = %q", ce.NextStep)
		}
	})
	t.Run("failed", func(t *testing.T) {
		f := newAVD(t, nil)
		sdktest.Write(t, f.toolDir, "create.fail", "Error: Package path is not valid.\n")
		ce := mustFail(t, f.create(t, "dy", "pixel_9a", testImage), sdk.ErrToolFailed)
		if !strings.Contains(ce.Cause, "Package path is not valid") {
			t.Errorf("cause = %q", ce.Cause)
		}
	})
	t.Run("wrote nothing", func(t *testing.T) {
		f := newAVD(t, nil)
		sdktest.Write(t, f.toolDir, "create.noini", "")
		ce := mustFail(t, f.create(t, "dy", "pixel_9a", testImage), sdk.ErrToolFailed)
		if !strings.Contains(ce.Cause, "dy.ini") {
			t.Errorf("cause = %q, want the missing ini named", ce.Cause)
		}
	})
	t.Run("a bad name", func(t *testing.T) {
		f := newAVD(t, nil)
		mustFail(t, f.create(t, "../x", "pixel_9a", testImage), capability.ErrInvalidParams)
		if n := len(sdktest.Calls(t, f.toolDir)); n != 0 {
			t.Errorf("avdmanager ran %d times", n)
		}
	})
}
