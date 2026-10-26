package sdk

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Tests of the plan sdk.install makes before any download (plan m3f step 4; ADR-0129 §2,
// §8; ADR-0130 §1, §2, §4). The fixtures are in installfixture_test.go.

const imageID = "system-images;android-28;default;arm64-v8a"

// stdRepo is a repository with the packages most tests use.
func stdRepo(t *testing.T) *repoBuilder {
	b := newRepoBuilder()
	b.addWith(fxPkg{Path: "platform-tools", Rev: "37.0.1", Display: "Android SDK Platform-Tools"}, pkgZip(t, "platform-tools"))
	b.add("emulator", "37.2.12", pkgZip(t, "emulator"))
	b.add("build-tools;36.1.0", "36.1.0", pkgZip(t, "android-16"))
	b.addImage("28", "default", "arm64-v8a", mkZip(t,
		zipEnt{name: "arm64-v8a/", dos: true},
		zipEnt{name: "arm64-v8a/system.img", data: "image", dos: true}), "emulator:31.1.1")
	return b
}

func idsOf(r InstallResult) []string {
	var out []string
	for _, p := range r.Installed {
		out = append(out, p.ID)
	}
	return out
}

// TestInstallFromNothing is the real sequence on a host with no SDK (ADR-0130 §4; ADR-0131
// §3): sdk.acceptLicense creates the root with licenses/ only, and sdk.install then sets up
// the SDK in it, a root that holds no package being an empty target. Seen failing with step
// 3's plain emptiness test (a root holding licenses/ refused as not an SDK).
func TestInstallFromNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sdk")
	repo := stdRepo(t)
	f := newInst(t, repo.build(), root)
	if present(t, root) {
		t.Fatal("the root exists before anything ran")
	}
	wantDone(t, f.call(capability.ClientUI, "sdk.acceptLicense", `{"id":"android-sdk-license","sha1":"`+fxHash+`"}`))
	if entries, _ := os.ReadDir(root); len(entries) != 1 || entries[0].Name() != "licenses" {
		t.Fatalf("after accepting, the root holds %v, want licenses/ only", entries)
	}

	res := wantInstalled(t, f.installAs("platform-tools"))
	if !res.Created || res.Root != root {
		t.Errorf("created=%v root=%q, want a created root at %q", res.Created, res.Root, root)
	}
	if !reflect.DeepEqual(res.Installed, []InstalledPackage{{ID: "platform-tools", Version: "37.0.1"}}) || len(res.Updated) != 0 {
		t.Errorf("installed=%+v updated=%+v", res.Installed, res.Updated)
	}
	if want := int64(len(repo.files["platform-tools_r37_0_1.zip"])); res.Downloaded != want {
		t.Errorf("downloaded = %d, want the archive's %d", res.Downloaded, want)
	}
	if got := installedVersion(t, filepath.Join(root, "platform-tools")); got != "37.0.1" {
		t.Errorf("package.xml revision = %q", got)
	}
	pk := f.call(capability.ClientMCP, "sdk.packages", `{}`).Result.(PackagesResult)
	if !pk.Found || len(pk.Installed) != 1 || pk.Installed[0].ID != "platform-tools" || pk.Installed[0].Version != "37.0.1" || pk.Installed[0].Update != "" {
		t.Errorf("sdk.packages installed = %+v (found %v), want platform-tools 37.0.1 with no update", pk.Installed, pk.Found)
	}
	if again := wantInstalled(t, f.installAs("platform-tools")); again.Created || len(again.Installed)+len(again.Updated) != 0 {
		t.Errorf("a second install did %+v", again)
	}
}

// TestInstallTarget pins Q5's order for where a call writes: the host SDK, else ANDROID_HOME,
// else ANDROID_SDK_ROOT, else the per-OS default; a directory that is neither empty nor an
// SDK is refused before anything is fetched, and so is a relative path or no place at all.
func TestInstallTarget(t *testing.T) {
	run := func(t *testing.T, env []string, root string) (*instFixture, capability.Outcome) {
		f := newInstEnv(t, stdRepo(t).build(), root, env)
		if root != "" {
			acceptLicence(t, root, "android-sdk-license", fxLicenceText)
		}
		return f, f.installAs("platform-tools")
	}
	t.Run("ANDROID_HOME before ANDROID_SDK_ROOT", func(t *testing.T) {
		a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
		_, out := run(t, []string{"ANDROID_SDK_ROOT=" + b, "ANDROID_HOME=" + a}, a)
		wantInstalled(t, out)
		if !present(t, filepath.Join(a, "platform-tools", "package.xml")) || present(t, b) {
			t.Error("the package is not in ANDROID_HOME alone")
		}
	})
	t.Run("ANDROID_SDK_ROOT before the default", func(t *testing.T) {
		home, b := t.TempDir(), filepath.Join(t.TempDir(), "b")
		_, out := run(t, []string{"ANDROID_SDK_ROOT=" + b, "HOME=" + home}, b)
		wantInstalled(t, out)
		if !present(t, filepath.Join(b, "platform-tools", "package.xml")) || present(t, DefaultSDK(home)) {
			t.Error("the package is not in ANDROID_SDK_ROOT alone")
		}
	})
	t.Run("the per-OS default", func(t *testing.T) {
		home := t.TempDir()
		_, out := run(t, []string{"HOME=" + home}, DefaultSDK(home))
		wantInstalled(t, out)
		if !present(t, filepath.Join(DefaultSDK(home), "platform-tools", "package.xml")) {
			t.Error("the package is not under the default")
		}
	})
	t.Run("an existing SDK is the host SDK", func(t *testing.T) {
		a, b := t.TempDir(), filepath.Join(t.TempDir(), "b")
		writePackage(t, filepath.Join(a, "platforms", "android-36", "package.xml"), "platforms;android-36", "2")
		_, out := run(t, []string{"ANDROID_SDK_ROOT=" + b, "ANDROID_HOME=" + a}, a)
		wantInstalled(t, out)
		if !present(t, filepath.Join(a, "platform-tools", "package.xml")) || present(t, b) {
			t.Error("the package did not go to the existing SDK")
		}
	})
	t.Run("a directory that is not an SDK", func(t *testing.T) {
		a := t.TempDir()
		if err := os.WriteFile(filepath.Join(a, "notes.txt"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		f, out := run(t, []string{"ANDROID_HOME=" + a}, "")
		wantCode(t, out, ErrNotWritable)
		if got := f.zipRequests(); len(got) != 0 || len(f.srv.requests()) != 0 {
			t.Errorf("a refused target reached the network: %v", f.srv.requests())
		}
		if entries, _ := os.ReadDir(a); len(entries) != 1 {
			t.Errorf("the directory now holds %d entries", len(entries))
		}
	})
	t.Run("a relative path and no place", func(t *testing.T) {
		for _, env := range [][]string{{"ANDROID_HOME=relative/sdk"}, {}} {
			_, out := run(t, env, "")
			wantCode(t, out, ErrNotWritable)
		}
	})
}

// TestInstallRefusesPendingLicence pins the consent rule (ADR-0129 §2): a licence of the
// closure whose (id, hash) is not in licenses/ refuses the call before a single archive is
// requested, naming the licence and the person's step; a licence file holding another text's
// hash does not count. Seen failing with the dependency closure skipped (TestInstallRefusesDependencyLicence).
func TestInstallRefusesPendingLicence(t *testing.T) {
	for _, c := range []struct {
		name string
		prep func(root string)
	}{
		{"no licence file", func(string) {}},
		{"another text's hash", func(root string) {
			acceptLicence(t, root, "android-sdk-license", fxOtherLicenceText)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sdk")
			f := newInst(t, stdRepo(t).build(), root)
			c.prep(root)
			out := f.installAs("platform-tools")
			wantCode(t, out, ErrLicenseNotAccepted)
			lics := out.Error.Details["licenses"].([]map[string]any)
			if len(lics) != 1 || lics[0]["id"] != "android-sdk-license" || lics[0]["sha1"] != fxHash || !reflect.DeepEqual(lics[0]["packages"], []string{"platform-tools"}) {
				t.Errorf("details.licenses = %v", lics)
			}
			if !strings.Contains(out.Error.NextStep, "droidyard sdk licenses") || !strings.Contains(out.Error.NextStep, "user") {
				t.Errorf("next step = %q, want the person's step", out.Error.NextStep)
			}
			if got := f.zipRequests(); len(got) != 0 {
				t.Errorf("archives were requested before consent: %v", got)
			}
			if present(t, filepath.Join(root, "platform-tools")) || present(t, filepath.Join(root, ".temp")) {
				t.Error("a refused install left files in the root")
			}
		})
	}
}

// TestInstallRefusesDependencyLicence: the licence a dependency is under counts as much as
// the requested package's own. The image's licence is accepted; the emulator it needs is
// under the preview licence, which is not, so the image is refused with nothing fetched.
func TestInstallRefusesDependencyLicence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sdk")
	b := newRepoBuilder()
	b.addWith(fxPkg{Path: "emulator", Rev: "37.2.12", License: "android-sdk-preview-license"}, pkgZip(t, "emulator"))
	b.addImage("28", "default", "arm64-v8a", mkZip(t, zipEnt{name: "arm64-v8a/", dos: true}, zipEnt{name: "arm64-v8a/system.img", data: "i", dos: true}), "emulator:31.1.1")
	f := newInst(t, b.build(), root)
	f.accept()
	out := f.installAs(imageID)
	wantCode(t, out, ErrLicenseNotAccepted)
	lics := out.Error.Details["licenses"].([]map[string]any)
	if len(lics) != 1 || lics[0]["id"] != "android-sdk-preview-license" || !reflect.DeepEqual(lics[0]["packages"], []string{"emulator"}) {
		t.Errorf("details.licenses = %v, want only the preview licence, under emulator", lics)
	}
	if got := f.zipRequests(); len(got) != 0 {
		t.Errorf("archives were requested: %v", got)
	}
}

// TestInstallClosure pins the dependency closure (ADR-0129 §8): an image that depends on
// the emulator installs it first, a chain installs deepest first, and a cycle, a dependency
// the repository lacks and one it only has below the revision needed are all refused as
// SDK_PACKAGE_NOT_FOUND, naming it, before any request for an archive.
func TestInstallClosure(t *testing.T) {
	b := stdRepo(t)
	b.add("libs;a", "1", pkgZip(t, "a"), "libs;b")
	b.add("libs;b", "1", pkgZip(t, "b"), "libs;c")
	b.add("libs;c", "1", pkgZip(t, "c"))
	b.add("libs;p", "1", pkgZip(t, "p"), "libs;q")
	b.add("libs;q", "1", pkgZip(t, "q"), "libs;p")
	b.add("libs;r", "1", pkgZip(t, "r"), "libs;ghost")
	b.add("libs;s", "1", pkgZip(t, "s"), "emulator:99.0.0")
	root := filepath.Join(t.TempDir(), "sdk")
	f := newInst(t, b.build(), root)
	f.accept()

	t.Run("dependencies first", func(t *testing.T) {
		res := wantInstalled(t, f.installAs(imageID))
		if want := []string{"emulator", imageID}; !reflect.DeepEqual(idsOf(res), want) {
			t.Errorf("installed = %v, want %v", idsOf(res), want)
		}
		got := f.zipRequests()
		if len(got) != 2 || !strings.HasPrefix(got[0], "emulator_") || !strings.HasPrefix(got[1], "sys-img/android/") {
			t.Errorf("archive requests = %v, want the emulator's before the image's", got)
		}
		if !present(t, filepath.Join(root, "system-images", "android-28", "default", "arm64-v8a", "package.xml")) {
			t.Error("the image is not at its id-as-path directory")
		}
	})
	t.Run("a chain installs deepest first", func(t *testing.T) {
		res := wantInstalled(t, f.installAs("libs;a"))
		if want := []string{"libs;c", "libs;b", "libs;a"}; !reflect.DeepEqual(idsOf(res), want) {
			t.Errorf("installed = %v, want %v", idsOf(res), want)
		}
	})
	for _, c := range []struct{ name, id, cause string }{
		{"a cycle", "libs;p", "libs;p"},
		{"a dependency the repository lacks", "libs;r", "libs;ghost"},
		{"a dependency only offered below the revision needed", "libs;s", "99.0.0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(f.zipRequests())
			out := f.installAs(c.id)
			wantCode(t, out, ErrPackageNotFound)
			if !strings.Contains(out.Error.Cause, c.cause) {
				t.Errorf("cause = %q, want it to name %q", out.Error.Cause, c.cause)
			}
			if got := len(f.zipRequests()); got != before {
				t.Errorf("a refused closure requested %d archives", got-before)
			}
		})
	}
}

// TestInstallUpdates pins the update of an installed package (ADR-0129 §8): with the seam
// answering "free", the directory is replaced, the old revision and its files are gone, the
// seam was asked about exactly the replace set, and `updated` names the move.
func TestInstallUpdates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sdk")
	f := newInst(t, stdRepo(t).build(), root)
	f.accept()
	writePackage(t, filepath.Join(root, "platform-tools", "package.xml"), "platform-tools", "36", "0", "0")
	old := filepath.Join(root, "platform-tools", "old.txt")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := wantInstalled(t, f.installAs("platform-tools"))
	if want := []UpdatedPackage{{ID: "platform-tools", From: "36.0.0", To: "37.0.1"}}; !reflect.DeepEqual(res.Updated, want) || len(res.Installed) != 0 || res.Created {
		t.Errorf("updated=%+v installed=%+v created=%v, want %+v", res.Updated, res.Installed, res.Created, want)
	}
	if present(t, old) {
		t.Error("a file of the old revision is still there")
	}
	if got := installedVersion(t, filepath.Join(root, "platform-tools")); got != "37.0.1" {
		t.Errorf("revision on disk = %q", got)
	}
	if !present(t, filepath.Join(root, "platform-tools", "bin", "tool")) {
		t.Error("the new revision's files are not in place")
	}
	if want := [][]string{{"platform-tools"}}; !reflect.DeepEqual(f.inUseCalls, want) {
		t.Errorf("the in-use seam was asked %v, want %v", f.inUseCalls, want)
	}
	if left := tempLeftovers(t, root); len(left) != 0 {
		t.Errorf("temporary entries left: %v", left)
	}
}

// TestInstallDependencyBelowMinRevision: a dependency installed below the min-revision a
// package asks for is in the replace set the seam is given, and is replaced; one that
// meets it is neither asked about nor touched.
func TestInstallDependencyBelowMinRevision(t *testing.T) {
	t.Run("below", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "sdk")
		f := newInst(t, stdRepo(t).build(), root)
		f.accept()
		writePackage(t, filepath.Join(root, "emulator", "package.xml"), "emulator", "30", "0", "0")
		stale := filepath.Join(root, "emulator", "stale.txt")
		os.WriteFile(stale, []byte("x"), 0o644)

		res := wantInstalled(t, f.installAs(imageID))
		if want := []UpdatedPackage{{ID: "emulator", From: "30.0.0", To: "37.2.12"}}; !reflect.DeepEqual(res.Updated, want) {
			t.Errorf("updated = %+v, want %+v", res.Updated, want)
		}
		if want := []string{imageID}; !reflect.DeepEqual(idsOf(res), want) {
			t.Errorf("installed = %v, want %v", idsOf(res), want)
		}
		if want := [][]string{{"emulator"}}; !reflect.DeepEqual(f.inUseCalls, want) {
			t.Errorf("the seam was asked %v, want the dependency in the replace set", f.inUseCalls)
		}
		if present(t, stale) {
			t.Error("the old emulator was not replaced")
		}
	})
	t.Run("enough", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "sdk")
		f := newInst(t, stdRepo(t).build(), root)
		f.accept()
		writePackage(t, filepath.Join(root, "emulator", "package.xml"), "emulator", "32", "0", "0")
		keep := filepath.Join(root, "emulator", "keep.txt")
		os.WriteFile(keep, []byte("x"), 0o644)

		res := wantInstalled(t, f.installAs(imageID))
		if len(res.Updated) != 0 || !reflect.DeepEqual(idsOf(res), []string{imageID}) {
			t.Errorf("updated=%+v installed=%v", res.Updated, idsOf(res))
		}
		if len(f.inUseCalls) != 0 || !present(t, keep) {
			t.Errorf("a sufficient dependency was asked about (%v) or touched", f.inUseCalls)
		}
		for _, r := range f.zipRequests() {
			if strings.HasPrefix(r, "emulator_") {
				t.Error("the emulator's archive was fetched")
			}
		}
	})
}

// TestInstallLatestDoesNothing: a package at the latest revision, or newer than the
// repository's, is left alone: no archive request, and nothing is written (not even .temp).
func TestInstallLatestDoesNothing(t *testing.T) {
	for _, version := range []string{"37.0.1", "38.0.0"} {
		root := filepath.Join(t.TempDir(), "sdk")
		f := newInst(t, stdRepo(t).build(), root)
		f.accept()
		parts := strings.Split(version, ".")
		writePackage(t, filepath.Join(root, "platform-tools", "package.xml"), "platform-tools", parts...)
		res := wantInstalled(t, f.installAs("platform-tools"))
		if len(res.Installed)+len(res.Updated) != 0 || res.Downloaded != 0 {
			t.Errorf("%s: the call did %+v", version, res)
		}
		if got := f.zipRequests(); len(got) != 0 {
			t.Errorf("%s: archives requested: %v", version, got)
		}
		if present(t, filepath.Join(root, ".temp")) {
			t.Errorf("%s: .temp was made for nothing", version)
		}
	}
}

// TestInstallUpdateRefusedInUse: a package that is in use, or that nobody checked, is never
// replaced: SDK_PACKAGE_IN_USE naming it and what uses it, the next step asking the user,
// no archive requested, the directory untouched. The shipped seam until step 5 is "not
// checked yet", which refuses an update but lets a fresh install run. Seen failing with the
// seam skipped.
func TestInstallUpdateRefusedInUse(t *testing.T) {
	prep := func(t *testing.T, tw ...func(*installer)) (*instFixture, string) {
		root := filepath.Join(t.TempDir(), "sdk")
		f := newInst(t, stdRepo(t).build(), root, tw...)
		f.accept()
		writePackage(t, filepath.Join(root, "platform-tools", "package.xml"), "platform-tools", "36", "0", "0")
		return f, root
	}
	check := func(t *testing.T, f *instFixture, root string, out capability.Outcome, by string) {
		wantCode(t, out, ErrPackageInUse)
		pkgs := out.Error.Details["packages"].([]map[string]any)
		if len(pkgs) != 1 || pkgs[0]["id"] != "platform-tools" || !strings.Contains(strings.Join(pkgs[0]["usedBy"].([]string), ","), by) {
			t.Errorf("details.packages = %v, want platform-tools used by %q", pkgs, by)
		}
		if !strings.Contains(out.Error.NextStep, "user") || !out.Untrusted {
			t.Errorf("next step = %q, untrusted = %v: want the user's step and the host text marked", out.Error.NextStep, out.Untrusted)
		}
		if got := f.zipRequests(); len(got) != 0 {
			t.Errorf("archives requested: %v", got)
		}
		if got := installedVersion(t, filepath.Join(root, "platform-tools")); got != "36.0.0" {
			t.Errorf("the package is now at %q", got)
		}
	}
	t.Run("in use", func(t *testing.T) {
		f, root := prep(t)
		f.inUse = func(ids []string) map[string][]string {
			return map[string][]string{"platform-tools": {"adb server (pid 4242)"}}
		}
		check(t, f, root, f.installAs("platform-tools"), "adb server")
	})
	t.Run("not checked yet", func(t *testing.T) {
		f, root := prep(t, func(in *installer) { in.inUse = notChecked })
		check(t, f, root, f.installAs("platform-tools"), "not checked yet")
		// A fresh install replaces nothing, so it runs.
		wantInstalled(t, f.installAs("emulator"))
	})
}

// TestInstallUnknownPackage: an id the repository does not offer for this host is
// SDK_PACKAGE_NOT_FOUND with up to five candidates by prefix, then family, then substring;
// nothing is requested. An id that is not an id at all is a parameter error.
func TestInstallUnknownPackage(t *testing.T) {
	b := stdRepo(t)
	for _, v := range []string{"35.0.0", "34.0.0", "33.0.1", "32.0.0", "31.0.0", "30.0.3"} {
		b.add("build-tools;"+v, v, pkgZip(t, "bt"))
	}
	f := newInst(t, b.build(), filepath.Join(t.TempDir(), "sdk"))
	f.accept()
	cases := []struct {
		id   string
		want []string
	}{
		{"platform-tool", []string{"platform-tools"}},
		{"build-tools;99.0.0", []string{"build-tools;36.1.0", "build-tools;35.0.0", "build-tools;34.0.0", "build-tools;33.0.1", "build-tools;32.0.0"}},
		{"tools", []string{"build-tools;36.1.0", "build-tools;35.0.0", "build-tools;34.0.0", "build-tools;33.0.1", "build-tools;32.0.0"}},
		{"zzz", nil},
	}
	for _, c := range cases {
		out := f.installAs(c.id)
		wantCode(t, out, ErrPackageNotFound)
		got, _ := out.Error.Details["candidates"].([]string)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: candidates = %v, want %v", c.id, got, c.want)
		}
	}
	if got := f.zipRequests(); len(got) != 0 {
		t.Errorf("archives requested: %v", got)
	}
	for _, params := range []string{`{"packages":["../x"]}`, `{"packages":[]}`, `{"packages":["BAD ID"]}`} {
		wantCode(t, f.call(capability.ClientMCP, "sdk.install", params), capability.ErrInvalidParams)
	}
}

// TestInstallNoSpace pins ADR-0130 §2's two checks. Free space below the archives' total
// refuses before any archive is requested; free space for the archives but not for the
// unpacked files (a small archive that unpacks large) refuses after that archive was
// fetched, leaving nothing at the final path and no temporary.
func TestInstallNoSpace(t *testing.T) {
	t.Run("the archives", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "sdk")
		f := newInst(t, stdRepo(t).build(), root, func(in *installer) {
			in.free = func(string) (uint64, error) { return 100, nil }
		})
		f.accept()
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrNoSpace)
		if got := f.zipRequests(); len(got) != 0 {
			t.Errorf("archives requested: %v", got)
		}
		if out.Error.Details["free"] != uint64(100) {
			t.Errorf("details = %v", out.Error.Details)
		}
	})
	t.Run("the unpacked files", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "sdk")
		big := mkZip(t, zipEnt{name: "big/", mode: 0o755},
			zipEnt{name: "big/zeros", data: strings.Repeat("\x00", 4<<20)})
		b := newRepoBuilder().add("platform-tools", "37.0.1", big)
		archive := uint64(len(b.files["platform-tools_r37_0_1.zip"]))
		f := newInst(t, b.build(), root, func(in *installer) {
			in.free = func(string) (uint64, error) { return archive + 1000, nil }
		})
		f.accept()
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrNoSpace)
		if got := f.zipRequests(); len(got) != 1 {
			t.Errorf("archives requested: %v, want the one that is then found too large", got)
		}
		if present(t, filepath.Join(root, "platform-tools")) {
			t.Error("something reached the final path")
		}
		if left := tempLeftovers(t, root); len(left) != 0 {
			t.Errorf("temporary entries left: %v", left)
		}
	})
}

// TestInstallRefusesAnIncompleteDirectory: a directory where a package that is not
// installed would go (an interrupted install, or somebody's files) is never removed or
// installed over: SDK_NOT_WRITABLE naming it relative to the root, nothing requested.
func TestInstallRefusesAnIncompleteDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sdk")
	f := newInst(t, stdRepo(t).build(), root)
	f.accept()
	junk := filepath.Join(root, "platform-tools", "junk.txt")
	if err := os.MkdirAll(filepath.Dir(junk), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(junk, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := f.installAs("platform-tools")
	wantCode(t, out, ErrNotWritable)
	if !strings.Contains(out.Error.Cause, "platform-tools") || strings.Contains(out.Error.Cause, root) {
		t.Errorf("cause = %q, want the directory relative to the root", out.Error.Cause)
	}
	if !present(t, junk) {
		t.Error("the directory's file was removed")
	}
	if got := f.zipRequests(); len(got) != 0 {
		t.Errorf("archives requested: %v", got)
	}
}

// TestInstallReportsAnNdkInstalled: a package that is not at one of the six fixed locations
// (ndk;27.0.1) is read as installed at its id-as-path directory, so the install does not
// fetch it again and sdk.packages lists it.
func TestInstallReportsAnNdkInstalled(t *testing.T) {
	b := stdRepo(t)
	b.add("ndk;27.0.1", "27.0.1", pkgZip(t, "27.0.1"))
	root := filepath.Join(t.TempDir(), "sdk")
	f := newInst(t, b.build(), root)
	f.accept()
	writePackage(t, filepath.Join(root, "platforms", "android-36", "package.xml"), "platforms;android-36", "2")
	dir := filepath.Join(root, "ndk", "27.0.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.properties"), []byte("Pkg.Revision = 27.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := wantInstalled(t, f.installAs("ndk;27.0.1"))
	if len(res.Installed)+len(res.Updated) != 0 || len(f.zipRequests()) != 0 {
		t.Errorf("the NDK was fetched again: %+v, %v", res, f.zipRequests())
	}
	pk := f.call(capability.ClientMCP, "sdk.packages", `{"query":"ndk"}`).Result.(PackagesResult)
	if len(pk.Installed) != 1 || pk.Installed[0].ID != "ndk;27.0.1" || pk.Installed[0].Version != "27.0.1" {
		t.Errorf("sdk.packages installed = %+v", pk.Installed)
	}
}

// TestInstallDescriptors pins the published decisions of the two descriptors (plan m3f
// step 4): sdk.install is an agent's destructive, idempotent, open-world command that may
// outlive a wait; sdk.uninstall is a person's. Both hold one scheduler key per root, so
// they never run side by side, and acceptance has a key of its own.
func TestInstallDescriptors(t *testing.T) {
	root := t.TempDir()
	f := newInst(t, stdRepo(t).build(), root)
	reg := f.reg
	install, _ := reg.Lookup("sdk.install")
	uninstall, _ := reg.Lookup("sdk.uninstall")
	accept, _ := reg.Lookup("sdk.acceptLicense")
	a := install.ResolveAnnotations()
	if !a.Destructive || !a.Idempotent || !a.OpenWorld || a.ReadOnly || !install.LongRunning || install.Audience != capability.AudienceAll || install.Lendable {
		t.Errorf("sdk.install: %+v long-running=%v audience=%v", a, install.LongRunning, install.Audience)
	}
	if !strings.Contains(a.Reason, "older one is not kept") {
		t.Errorf("reason = %q, want the update's loss said", a.Reason)
	}
	u := uninstall.ResolveAnnotations()
	if !u.Destructive || uninstall.Audience != capability.AudienceHuman || !uninstall.LongRunning || uninstall.Lendable {
		t.Errorf("sdk.uninstall: %+v audience=%v", u, uninstall.Audience)
	}
	call := &capability.Call{}
	if k := install.Resource(call); k != "sdk:"+root || uninstall.Resource(call) != k {
		t.Errorf("keys: install %q, uninstall %q, want one shared sdk:<root>", k, uninstall.Resource(call))
	}
	if k := accept.Resource(call); k == "sdk:"+root {
		t.Errorf("sdk.acceptLicense takes install's key %q and would queue behind it", k)
	}
	if !strings.Contains(install.Description, "sets up the SDK root when the host has none") {
		t.Errorf("sdk.install's description does not say it creates the root: %q", install.Description)
	}
}
