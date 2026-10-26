package sdk

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// TestClassicSdkmanagerReadsTheInstall (level 4, opt-in: DROIDYARD_SDK_TESTS=1 and
// DROIDYARD_SDK_CMDLINE_ZIP=<the cmdline-tools 22.0 archive>, network, a JVM 17 or newer):
// ADR-0130's check that what Droidyard writes is read as installed by Google's tools.
// Droidyard installs platform-tools from the live repository into a temporary root, its
// licence accepted through sdk.acceptLicense; the classic sdkmanager (22.0, the last archive
// with the Java tool), run with stdin closed, a scratch HOME and the discovered JVM, lists
// it at the repository's revision, and licenses/ is the same after.
func TestClassicSdkmanagerReadsTheInstall(t *testing.T) {
	if os.Getenv("DROIDYARD_SDK_TESTS") != "1" {
		t.Skip("level 4: set DROIDYARD_SDK_TESTS=1 (and DROIDYARD_SDK_CMDLINE_ZIP) to install from Google's repository and read it with the classic sdkmanager")
	}
	zipPath := os.Getenv("DROIDYARD_SDK_CMDLINE_ZIP")
	if zipPath == "" {
		t.Skip("level 4: DROIDYARD_SDK_CMDLINE_ZIP is not set; it names the classic cmdline-tools 22.0 archive")
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Skipf("level 4: DROIDYARD_SDK_CMDLINE_ZIP: %v", err)
	}
	jvm := gradle.Discover(gradle.Finder{Env: os.Environ()})
	if !jvm.Found {
		t.Skip("level 4: no JVM 17 or newer for the classic sdkmanager: " + jvm.NotFound())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	snap := NewRepository(RepositoryOptions{}).Load(ctx, true)
	if snap.Model == nil {
		t.Fatalf("the live repository did not load: %s", snap.Cause)
	}
	pt, ok := snap.Model.Package("platform-tools")
	if !ok {
		t.Fatal("platform-tools is not available for this host")
	}

	root := filepath.Join(t.TempDir(), "sdk")
	reg := capability.NewRegistry()
	Register(reg, Options{Env: []string{"ANDROID_HOME=" + root}})
	ops := capability.NewOperations(reg)
	call := func(client capability.ClientKind, name, params string) capability.Outcome {
		t.Helper()
		id, err := ops.Start(ctx, capability.Request{Capability: name, Params: []byte(params), Client: client,
			Origin: capability.Origin{ID: string(client) + "-1", Client: client}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := ops.Wait(ctx, id, time.Now().Add(8*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if out.State != capability.StateCompleted {
			t.Fatalf("%s ended %s: %+v", name, out.State, out.Error)
		}
		return out
	}
	call(capability.ClientUI, "sdk.acceptLicense", fmt.Sprintf(`{"id":%q,"sha1":%q}`, pt.License.ID, pt.License.SHA1))
	res := call(capability.ClientMCP, "sdk.install", `{"packages":["platform-tools"]}`).Result.(InstallResult)
	t.Logf("installed %+v, %d bytes downloaded", res.Installed, res.Downloaded)

	before := readTree(t, filepath.Join(root, "licenses"))
	tool := t.TempDir()
	if out, err := exec.CommandContext(ctx, "unzip", "-q", zipPath, "-d", tool).CombinedOutput(); err != nil {
		t.Fatalf("unzip: %v\n%s", err, out)
	}
	home := t.TempDir()
	cmd := exec.CommandContext(ctx, filepath.Join(tool, "cmdline-tools", "bin", "sdkmanager"), "--sdk_root="+root, "--list_installed")
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "JAVA_HOME=" + jvm.Home, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	t.Logf("sdkmanager --list_installed:\n%s", out)
	if err != nil {
		t.Fatalf("sdkmanager: %v", err)
	}
	line := regexp.MustCompile(`(?m)^\s*platform-tools\s*\|\s*` + regexp.QuoteMeta(pt.Revision.String()) + `\s*\|`)
	if !line.Match(out) {
		t.Errorf("the classic sdkmanager does not list platform-tools at %s as installed", pt.Revision)
	}
	if after := readTree(t, filepath.Join(root, "licenses")); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("licenses/ changed: %v -> %v", before, after)
	}
}

// readTree is the files of dir with their content, for comparing before and after.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}
