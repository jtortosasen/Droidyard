package sdk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkSDK makes a directory that looks like an SDK (it contains platform-tools/) and
// returns it.
func mkSDK(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "platform-tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// touch makes an executable file at path.
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

// TestHostOrder pins sdk.md §Host SDK's order: ANDROID_HOME, ANDROID_SDK_ROOT
// (deprecated), an SDK derived from adb on PATH (symlinks resolved), then the per-OS
// default. Each row's winner loses to the row before it. Reversing the order
// (ANDROID_SDK_ROOT before ANDROID_HOME, or the default before the PATH-derived SDK)
// fails a row.
func TestHostOrder(t *testing.T) {
	tmp := t.TempDir()
	homeSDK := mkSDK(t, filepath.Join(tmp, "home-sdk"))
	rootSDK := mkSDK(t, filepath.Join(tmp, "root-sdk"))
	pathDir := filepath.Join(tmp, "bin")
	touch(t, filepath.Join(pathDir, "adb"))
	// adb on PATH that is a symlink into an SDK: its host SDK is derived from it.
	derivedSDK := filepath.Join(tmp, "derived-sdk")
	touch(t, filepath.Join(derivedSDK, "platform-tools", "adb"))
	derivedRoot, _ := filepath.EvalSymlinks(derivedSDK) // /var is /private/var on macOS
	linkDir := filepath.Join(tmp, "linkbin")
	os.MkdirAll(linkDir, 0o755)
	os.Symlink(filepath.Join(derivedSDK, "platform-tools", "adb"), filepath.Join(linkDir, "adb"))
	fakeHome := filepath.Join(tmp, "user")
	mkSDK(t, DefaultSDK(fakeHome))

	rows := []struct {
		name   string
		env    []string
		root   string
		source string
	}{
		{"ANDROID_HOME beats ANDROID_SDK_ROOT, PATH and the default", []string{"ANDROID_HOME=" + homeSDK, "ANDROID_SDK_ROOT=" + rootSDK, "PATH=" + pathDir, "HOME=" + fakeHome}, homeSDK, SourceAndroidHome},
		{"ANDROID_SDK_ROOT beats PATH and the default", []string{"ANDROID_SDK_ROOT=" + rootSDK, "PATH=" + pathDir, "HOME=" + fakeHome}, rootSDK, SourceSDKRoot},
		{"the SDK derived from a symlinked adb on PATH beats the default", []string{"PATH=" + linkDir, "HOME=" + fakeHome}, derivedRoot, SourceSDKFromPath},
		{"the per-OS default is last", []string{"PATH=" + pathDir, "HOME=" + fakeHome}, DefaultSDK(fakeHome), SourceSDKDefault},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := Host(r.env)
			if !got.Found || got.Root != r.root || got.Source != r.source {
				t.Errorf("Host = found %v root %q source %q (rejected %v), want %q from %s", got.Found, got.Root, got.Source, got.Rejected, r.root, r.source)
			}
		})
	}
}

// TestHostRejectsWithAReason pins that a candidate that does not look like an SDK is
// reported with why, and the order goes on past it.
func TestHostRejectsWithAReason(t *testing.T) {
	tmp := t.TempDir()
	notSDK := filepath.Join(tmp, "plain-dir")
	os.MkdirAll(notSDK, 0o755)
	good := mkSDK(t, filepath.Join(tmp, "root-sdk"))
	got := Host([]string{"ANDROID_HOME=" + notSDK, "ANDROID_SDK_ROOT=" + good})
	if !got.Found || got.Root != good {
		t.Fatalf("Host = %+v, want %q after the rejected candidate", got, good)
	}
	if len(got.Rejected) != 1 || got.Rejected[0].Candidate != notSDK || !strings.Contains(got.Rejected[0].Reason, "not an Android SDK") {
		t.Errorf("rejected = %+v, want the non-SDK directory with a reason", got.Rejected)
	}
}

// TestHostWarnsWhenHomeAndRootDiffer pins sdk.md §Host SDK step 3: both set and
// different is one warning, and ANDROID_HOME still wins; equal (even spelled
// differently) or one unset is none.
func TestHostWarnsWhenHomeAndRootDiffer(t *testing.T) {
	tmp := t.TempDir()
	a := mkSDK(t, filepath.Join(tmp, "a"))
	b := mkSDK(t, filepath.Join(tmp, "b"))
	got := Host([]string{"ANDROID_HOME=" + a, "ANDROID_SDK_ROOT=" + b})
	if len(got.Warnings) != 1 || got.Root != a {
		t.Errorf("differing: warnings %v root %q, want one warning and %q", got.Warnings, got.Root, a)
	}
	for name, env := range map[string][]string{
		"same":     {"ANDROID_HOME=" + a, "ANDROID_SDK_ROOT=" + a + "/"},
		"one only": {"ANDROID_HOME=" + a},
	} {
		if w := Host(env).Warnings; len(w) != 0 {
			t.Errorf("%s: warnings %v, want none", name, w)
		}
	}
}

// TestHostWithNothingListsEveryCandidate pins that with no SDK, found is false and
// every candidate tried (ANDROID_HOME, ANDROID_SDK_ROOT, the per-OS default) is listed
// with a reason.
func TestHostWithNothingListsEveryCandidate(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "h")
	root := filepath.Join(tmp, "r")
	user := filepath.Join(tmp, "user")
	got := Host([]string{"ANDROID_HOME=" + home, "ANDROID_SDK_ROOT=" + root, "HOME=" + user, "PATH=" + filepath.Join(tmp, "empty")})
	if got.Found {
		t.Fatalf("found %q with nothing installed", got.Root)
	}
	want := map[string]bool{home: false, root: false, DefaultSDK(user): false}
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
	if !strings.Contains(got.NotFound(), "ANDROID_HOME") {
		t.Errorf("NotFound = %q, want it to name ANDROID_HOME", got.NotFound())
	}
}

// TestHostNeverReadsTheProcessEnvironment pins the explicit-environment rule: a real
// ANDROID_HOME and HOME in the process change nothing, so no test can find this
// machine's SDK.
func TestHostNeverReadsTheProcessEnvironment(t *testing.T) {
	tmp := t.TempDir()
	real := mkSDK(t, filepath.Join(tmp, "sdk"))
	t.Setenv("ANDROID_HOME", real)
	t.Setenv("HOME", filepath.Dir(real))
	if got := Host(nil); got.Found {
		t.Errorf("Host(nil) found %q from the process environment", got.Root)
	}
}
