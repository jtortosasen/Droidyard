package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// seededBuild is a finished build in the client's history whose one output is an APK on its
// disk: the record a build.assemble would have left, written the way the registry reads it.
type seededBuild struct {
	root, wid, id string
	apk           []byte
}

const (
	seedBuildID = "op-00000000000000a1"
	seedAppID   = "dev.droidyard.formapp"
)

func seedBuild(t *testing.T, clientState string) seededBuild {
	t.Helper()
	root := platform.Current().Canonical(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "settings.gradle"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wid, _ := platform.Current().WorkspaceID(root)
	apk := make([]byte, 2<<20+123)
	rand.Read(apk)
	apkPath := filepath.Join(t.TempDir(), "app-debug.apk")
	if err := os.WriteFile(apkPath, apk, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(apkPath)
	rec := map[string]any{
		"schemaVersion": 1,
		"buildId":       seedBuildID,
		"outcome":       "success",
		"finishedAt":    time.Now().UTC(),
		"outputs": []map[string]any{{
			"module": ":app", "variant": "debug", "applicationId": seedAppID, "versionCode": 7,
			"apks": []map[string]any{{"name": "app-debug.apk", "filters": []string{}, "sizeBytes": fi.Size(), "supported": true}},
		}},
		"outputPaths": []map[string]any{{
			"module": ":app", "variant": "debug", "apk": "app-debug.apk", "path": apkPath, "sizeBytes": fi.Size(), "modTime": fi.ModTime().UTC(),
		}},
	}
	dir := filepath.Join(clientState, "ws", wid, "builds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, seedBuildID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return seededBuild{root: root, wid: wid, id: seedBuildID, apk: apk}
}

// openOn opens the build's workspace on the client, for the workspace-scoped device.install.
func (l *remoteLab) openOn(t *testing.T, root string) {
	t.Helper()
	if out, err := l.client.ops.Invoke(context.Background(), capability.Request{Capability: "workspace.open", Workspace: root, Params: json.RawMessage(`{}`), Client: capability.ClientUI}); err != nil || out.State != capability.StateCompleted {
		t.Fatalf("workspace.open: %+v, %v", out, err)
	}
}

func (l *remoteLab) install(t *testing.T, b seededBuild, id string) capability.Outcome {
	t.Helper()
	out, err := l.client.ops.Invoke(context.Background(), capability.Request{Capability: "device.install", Workspace: b.wid, Client: capability.ClientUI,
		Params: json.RawMessage(`{` + dev(id) + `,"build":{"buildId":"` + b.id + `","module":":app","variant":"debug"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// uploadsOnHost lists what the host's upload directory holds.
func (l *remoteLab) uploadsOnHost(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(l.host.state, filepath.FromSlash(hosting.UploadsDir)))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func hasInstall(calls []string) bool {
	for _, c := range calls {
		if strings.Contains(c, " install ") {
			return true
		}
	}
	return false
}

// Decision (verifier N1): two installs from one client to two devices one host lends both
// succeed. A host keeps one upload of a peer at a time, so the second upload would be
// refused (409) or replace the first before the host installs it unless the client takes
// turns; the host is slowed (its device listing takes 300 ms) so the window is wide. Seen
// failing without the client's slot: the second install is refused by the host.
func TestParallelRemoteInstallsToOneHost(t *testing.T) {
	t.Parallel()
	lab := newRemoteLabWith(t, labOptions{phone2ToAnna: true, hostListDelay: 300 * time.Millisecond, hostFiles: map[string]string{
		phone1 + ".install.out": "Success\n", phone2 + ".install.out": "Success\n"}})
	b := seedBuild(t, lab.client.state)
	lab.openOn(t, b.root)
	outs := make([]capability.Outcome, 2)
	var wg sync.WaitGroup
	for i, id := range []string{remotePhone, remoteName + "/" + phone2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i] = lab.install(t, b, id)
		}()
	}
	wg.Wait()
	for i, out := range outs {
		if out.State != capability.StateCompleted {
			t.Errorf("install %d: %s %+v; want completed", i, out.State, out.Error)
		}
	}
	for _, s := range []string{phone1, phone2} {
		got, _ := os.ReadFile(filepath.Join(lab.hostDir, s+".installed"))
		if sha256.Sum256(got) != sha256.Sum256(b.apk) {
			t.Errorf("%s: the host installed %d bytes that are not the client's APK", s, len(got))
		}
	}
	if files := lab.uploadsOnHost(t); len(files) != 0 {
		t.Errorf("the host kept %v", files)
	}
}

// TestRemoteInstall is plan m2t step 4 between two daemons: the client resolves its build,
// uploads the APK to the host over the pinned connection, and the host installs the file it
// received on the phone it lends, with the install the build path runs; the client runs no
// adb, the host keeps no file, and what comes back is the local build's facts.
func TestRemoteInstall(t *testing.T) {
	t.Parallel()
	lab := newRemoteLabWith(t, labOptions{hostFiles: map[string]string{phone1 + ".install.out": "Performing Streamed Install\nSuccess\n"}})
	b := seedBuild(t, lab.client.state)
	lab.openOn(t, b.root) // the workspace the record belongs to is open on the client
	sum := sha256.Sum256(b.apk)
	installedOnHost := func() []byte {
		got, _ := os.ReadFile(filepath.Join(lab.hostDir, phone1+".installed"))
		return got
	}

	// The host installs exactly the bytes the client built (seen failing with a truncated copy
	// of the upload), keeps no file, and the client ran no adb command.
	t.Run("the host installs the client's build", func(t *testing.T) {
		clientBefore := len(lab.clientCalls(t))
		out := lab.install(t, b, remotePhone)
		if out.State != capability.StateCompleted {
			t.Fatalf("device.install on %s: %s %+v", remotePhone, out.State, out.Error)
		}
		r, ok := out.Result.(device.InstallResult)
		if !ok || r.ApplicationID != seedAppID || r.APK != "app-debug.apk" || r.VersionCode == nil || *r.VersionCode != 7 {
			t.Errorf("result = %+v; want the local build's applicationId, versionCode 7 and app-debug.apk", out.Result)
		}
		if !out.Untrusted {
			t.Error("the answer of a relayed install is not marked untrusted")
		}
		got := installedOnHost()
		if len(got) != len(b.apk) || sha256.Sum256(got) != sum {
			t.Errorf("the host installed %d bytes with SHA-256 %x; want the client's APK, %d bytes, %x", len(got), sha256.Sum256(got), len(b.apk), sum)
		}
		if files := lab.uploadsOnHost(t); len(files) != 0 {
			t.Errorf("the host kept %v after the install", files)
		}
		if after := lab.clientCalls(t); len(after) != clientBefore || hasInstall(after) {
			t.Errorf("the client ran adb for a remote install: %q", after[clientBefore:])
		}
		line := "peer anna: stored an upload of " + strconv.Itoa(len(b.apk)) + " bytes, sha256 " + hex.EncodeToString(sum[:])[:12]
		if !strings.Contains(lab.host.log.String(), line) {
			t.Errorf("the host's daemon.log has no line %q", line)
		}
		if !hasInstall(lab.hostCalls(t)) {
			t.Errorf("the host ran no install: %q", lab.hostCalls(t))
		}
	})

	// A refusal on the device reaches the client as the token alone, and the file is gone
	// from the host.
	t.Run("a failed install keeps nothing and the token reaches the client", func(t *testing.T) {
		devicetest.Write(t, lab.hostDir, phone1+".install.err", []byte("adb: failed to install /x/y.apk: Failure [INSTALL_FAILED_INSUFFICIENT_STORAGE]\n"))
		devicetest.Write(t, lab.hostDir, phone1+".install.exit", []byte("1"))
		defer os.Remove(filepath.Join(lab.hostDir, phone1+".install.err"))
		defer os.Remove(filepath.Join(lab.hostDir, phone1+".install.exit"))
		out := lab.install(t, b, remotePhone)
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != device.ErrInstallFailed || out.Error.Cause != "INSTALL_FAILED_INSUFFICIENT_STORAGE" {
			t.Fatalf("install: %s %+v; want DEVICE_INSTALL_FAILED with the token as cause", out.State, out.Error)
		}
		if strings.Contains(out.Error.Message+out.Error.Cause+out.Error.NextStep, "hosting") {
			t.Errorf("the error names the host's directory: %+v", out.Error)
		}
		if files := lab.uploadsOnHost(t); len(files) != 0 {
			t.Errorf("the host kept %v after a failed install", files)
		}
	})

	// The client's own policy decides before anything is uploaded.
	t.Run("the client's policy refuses before an upload", func(t *testing.T) {
		lab.rewriteClient(t, false, remotePhone)
		defer lab.rewriteClient(t, true, fullAllowlist()...)
		before := lab.host.log.String()
		out := lab.install(t, b, remotePhone)
		if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != device.ErrNotAllowed {
			t.Fatalf("install: %s %+v; want DEVICE_NOT_ALLOWED", out.State, out.Error)
		}
		if lab.host.log.String() != before {
			t.Errorf("the host logged something for a refused call:\n%s", strings.TrimPrefix(lab.host.log.String(), before))
		}
	})

	// A client cancel while the host installs stops the host's install and leaves no file.
	t.Run("a cancel leaves nothing on the host", func(t *testing.T) {
		devicetest.Write(t, lab.hostDir, phone1+".install.hang", nil)
		defer os.Remove(filepath.Join(lab.hostDir, phone1+".install.hang"))
		id, err := lab.client.ops.Start(context.Background(), capability.Request{Capability: "device.install", Workspace: b.wid, Client: capability.ClientUI,
			Params: json.RawMessage(`{` + dev(remotePhone) + `,"build":{"buildId":"` + b.id + `","module":":app","variant":"debug"}}`)})
		if err != nil {
			t.Fatal(err)
		}
		hostOp := ""
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to run the install", func() bool {
			for _, op := range lab.host.ops.Active() {
				if op.Capability == "device.installUpload" {
					hostOp = op.OperationID
				}
			}
			return hostOp != ""
		})
		if err := lab.client.ops.Cancel(id); err != nil {
			t.Fatal(err)
		}
		if out, err := lab.client.ops.Wait(context.Background(), id, time.Now().Add(10*time.Second)); err != nil || out.State != capability.StateCancelled {
			t.Fatalf("the client's operation: %+v, %v; want cancelled", out, err)
		}
		if out, err := lab.host.ops.Wait(context.Background(), hostOp, time.Now().Add(10*time.Second)); err != nil || out.State != capability.StateCancelled {
			t.Fatalf("the host's operation: %+v, %v; want cancelled", out, err)
		}
		testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to remove the upload", func() bool { return len(lab.uploadsOnHost(t)) == 0 })
	})

	// installUpload is no tool and no MCP call (ADR-0060 §2).
	t.Run("installUpload is not for an MCP session", func(t *testing.T) {
		_, err := lab.host.ops.Start(context.Background(), capability.Request{Capability: "device.installUpload", Client: capability.ClientMCP,
			Params: json.RawMessage(`{"device":"` + phone1 + `","upload":"` + strings.Repeat("a", 32) + `"}`), Origin: capability.Origin{ID: "mcp-1", Client: capability.ClientMCP}})
		if ce := capability.AsError(err); ce == nil || ce.Code != capability.ErrNotAllowedHere {
			t.Errorf("an MCP call of device.installUpload: %v; want REQUEST_NOT_ALLOWED_HERE", err)
		}
	})
}
