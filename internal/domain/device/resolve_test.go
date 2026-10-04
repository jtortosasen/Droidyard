package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// touchADB makes an executable file at dir/platform-tools/adb (or dir/adb if bare).
func touch(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// sdk makes a host SDK under root with a platform-tools/adb, and returns the adb.
func sdk(t *testing.T, root string) string {
	t.Helper()
	return touch(t, filepath.Join(root, "platform-tools", "adb"))
}

// TestResolveOrder pins adb-device.md §adb resolution and degradation's order, with
// the host SDK as sdk.md §Host SDK orders it: ADB, then the host SDK's
// platform-tools/adb (ANDROID_HOME, ANDROID_SDK_ROOT, derived from adb on PATH, the
// per-OS default), then adb on PATH. Each row has a winner and everything before it
// absent or broken; the winner then loses to the row that comes before it. Reversing
// the order (PATH before the SDK, ANDROID_SDK_ROOT before ANDROID_HOME) fails a row.
func TestResolveOrder(t *testing.T) {
	tmp := t.TempDir()
	adbVar := touch(t, filepath.Join(tmp, "custom", "adb"))
	homeSDK := sdk(t, filepath.Join(tmp, "home-sdk"))
	rootSDK := sdk(t, filepath.Join(tmp, "root-sdk"))
	pathDir := filepath.Join(tmp, "bin")
	pathAdb := touch(t, filepath.Join(pathDir, "adb"))
	// adb on PATH that is a symlink into an SDK: its host SDK is derived from it.
	derivedSDK := filepath.Join(tmp, "derived-sdk")
	derivedAdb, _ := filepath.EvalSymlinks(sdk(t, derivedSDK)) // /var is /private/var on macOS
	linkDir := filepath.Join(tmp, "linkbin")
	os.MkdirAll(linkDir, 0o755)
	os.Symlink(filepath.Join(derivedSDK, "platform-tools", "adb"), filepath.Join(linkDir, "adb"))
	fakeHome := filepath.Join(tmp, "user")
	defAdb := sdk(t, defaultSDK(fakeHome))

	rows := []struct {
		name   string
		env    []string
		path   string
		source string
	}{
		{"ADB beats every other", []string{"ADB=" + adbVar, "ANDROID_HOME=" + filepath.Dir(filepath.Dir(homeSDK)), "PATH=" + pathDir}, adbVar, SourceADBEnv},
		{"ANDROID_HOME beats ANDROID_SDK_ROOT, PATH and the default", []string{"ANDROID_HOME=" + filepath.Dir(filepath.Dir(homeSDK)), "ANDROID_SDK_ROOT=" + filepath.Dir(filepath.Dir(rootSDK)), "PATH=" + pathDir, "HOME=" + fakeHome}, homeSDK, SourceAndroidHome},
		{"ANDROID_SDK_ROOT beats PATH and the default", []string{"ANDROID_SDK_ROOT=" + filepath.Dir(filepath.Dir(rootSDK)), "PATH=" + pathDir, "HOME=" + fakeHome}, rootSDK, SourceSDKRoot},
		{"the SDK derived from a symlinked adb on PATH beats the default", []string{"PATH=" + linkDir, "HOME=" + fakeHome}, derivedAdb, SourceSDKFromPath},
		{"the per-OS default SDK beats a bare adb on PATH", []string{"PATH=" + pathDir, "HOME=" + fakeHome}, defAdb, SourceSDKDefault},
		{"PATH is last", []string{"PATH=" + pathDir, "HOME=" + filepath.Join(tmp, "nobody")}, pathAdb, SourcePath},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := Resolve(r.env)
			if !got.Found || got.Path != r.path || got.Source != r.source {
				t.Errorf("Resolve = found %v path %q source %q (rejected %v), want %q from %s", got.Found, got.Path, got.Source, got.Rejected, r.path, r.source)
			}
		})
	}
}

// TestResolveRejectsWithAReason pins that a candidate that does not qualify is
// reported with why, and the order goes on past it: an SDK without platform-tools/adb
// is rejected and PATH is used; a directory that is not an SDK is rejected as such.
func TestResolveRejectsWithAReason(t *testing.T) {
	tmp := t.TempDir()
	noAdb := filepath.Join(tmp, "sdk-without-adb")
	os.MkdirAll(filepath.Join(noAdb, "platform-tools"), 0o755)
	notSDK := filepath.Join(tmp, "plain-dir")
	os.MkdirAll(notSDK, 0o755)
	pathAdb := touch(t, filepath.Join(tmp, "bin", "adb"))
	got := Resolve([]string{"ANDROID_HOME=" + notSDK, "ANDROID_SDK_ROOT=" + noAdb, "PATH=" + filepath.Dir(pathAdb), "HOME=" + filepath.Join(tmp, "nobody")})
	if !got.Found || got.Source != SourcePath {
		t.Fatalf("Resolve = %+v, want PATH after the rejected SDKs", got)
	}
	var sawNoAdb, sawNotSDK bool
	for _, r := range got.Rejected {
		if r.Source == SourceSDKRoot && strings.HasSuffix(r.Candidate, filepath.Join("platform-tools", "adb")) && r.Reason != "" {
			sawNoAdb = true
		}
		if r.Source == SourceAndroidHome && r.Candidate == notSDK && strings.Contains(r.Reason, "not an Android SDK") {
			sawNotSDK = true
		}
	}
	if !sawNoAdb || !sawNotSDK {
		t.Errorf("rejected = %+v, want the adb-less SDK and the non-SDK each with a reason", got.Rejected)
	}
}

// TestResolveWarnsWhenHomeAndRootDiffer pins sdk.md §Host SDK step 3: both set and
// different is one warning, and ANDROID_HOME still wins; equal (even spelled
// differently) or one unset is none.
func TestResolveWarnsWhenHomeAndRootDiffer(t *testing.T) {
	tmp := t.TempDir()
	a := sdk(t, filepath.Join(tmp, "a"))
	b := sdk(t, filepath.Join(tmp, "b"))
	ra, rb := filepath.Dir(filepath.Dir(a)), filepath.Dir(filepath.Dir(b))
	got := Resolve([]string{"ANDROID_HOME=" + ra, "ANDROID_SDK_ROOT=" + rb})
	if len(got.Warnings) != 1 || got.Path != a {
		t.Errorf("differing: warnings %v path %q, want one warning and %q", got.Warnings, got.Path, a)
	}
	for name, env := range map[string][]string{
		"same":     {"ANDROID_HOME=" + ra, "ANDROID_SDK_ROOT=" + ra + "/"},
		"one only": {"ANDROID_HOME=" + ra},
	} {
		if w := Resolve(env).Warnings; len(w) != 0 {
			t.Errorf("%s: warnings %v, want none", name, w)
		}
	}
}

// TestResolveADBThatNamesNothingFailsClosed pins Q6: an ADB that is not an executable
// file is not found, with the bad value listed, and no later candidate is tried even
// though a valid ANDROID_HOME and PATH adb exist. Falling through would fail this.
func TestResolveADBThatNamesNothingFailsClosed(t *testing.T) {
	tmp := t.TempDir()
	good := sdk(t, filepath.Join(tmp, "sdk"))
	pathAdb := touch(t, filepath.Join(tmp, "bin", "adb"))
	dir := filepath.Join(tmp, "a-directory")
	os.MkdirAll(dir, 0o755)
	plain := filepath.Join(tmp, "plain")
	os.WriteFile(plain, []byte("x"), 0o644)
	for name, v := range map[string]string{"missing": filepath.Join(tmp, "nope"), "directory": dir, "not executable": plain} {
		got := Resolve([]string{"ADB=" + v, "ANDROID_HOME=" + filepath.Dir(filepath.Dir(good)), "PATH=" + filepath.Dir(pathAdb)})
		if got.Found || got.Path != "" {
			t.Errorf("%s: found %q, want not found", name, got.Path)
		}
		if len(got.Rejected) != 1 || got.Rejected[0].Candidate != v || got.Rejected[0].Source != SourceADBEnv {
			t.Errorf("%s: rejected %+v, want exactly the bad ADB value", name, got.Rejected)
		}
	}
}

// TestResolveWithNothingListsEveryCandidate pins A2: found false, and every candidate
// tried (ANDROID_HOME, ANDROID_SDK_ROOT, the per-OS default, PATH) is listed.
func TestResolveWithNothingListsEveryCandidate(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "h")
	root := filepath.Join(tmp, "r")
	got := Resolve([]string{"ANDROID_HOME=" + home, "ANDROID_SDK_ROOT=" + root, "HOME=" + filepath.Join(tmp, "user"), "PATH=" + filepath.Join(tmp, "empty")})
	if got.Found {
		t.Fatalf("found %q with nothing installed", got.Path)
	}
	want := map[string]bool{home: false, root: false, defaultSDK(filepath.Join(tmp, "user")): false, "adb on PATH": false}
	for _, r := range got.Rejected {
		if _, ok := want[r.Candidate]; ok {
			want[r.Candidate] = true
		}
		if r.Reason == "" {
			t.Errorf("rejected %q without a reason", r.Candidate)
		}
	}
	for c, seen := range want {
		if !seen {
			t.Errorf("candidate %q not listed in %+v", c, got.Rejected)
		}
	}
}

// TestResolveNeverReadsTheProcessEnvironment pins the explicit-environment rule: a
// real ANDROID_HOME and PATH in the process change nothing, so no test can find this
// machine's adb.
func TestResolveNeverReadsTheProcessEnvironment(t *testing.T) {
	tmp := t.TempDir()
	real := sdk(t, filepath.Join(tmp, "sdk"))
	t.Setenv("ANDROID_HOME", filepath.Dir(filepath.Dir(real)))
	t.Setenv("ADB", real)
	t.Setenv("PATH", filepath.Dir(real))
	t.Setenv("HOME", tmp)
	if got := Resolve(nil); got.Found {
		t.Errorf("Resolve(nil) found %q from the process environment", got.Path)
	}
}
