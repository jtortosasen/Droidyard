package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// uploadFixture is a host that lends R58M001 to anna and R58M002 to bob, with the store of
// the APKs they upload.
func uploadFixture(t *testing.T) (*hostFixture, *hosting.Uploads, string) {
	t.Helper()
	f := newHostFixture(t, uiList(phoneLine, usbLine("R58M001"), usbLine("R58M002")), []string{"R58M001", "R58M002"}, lendingConfig)
	state := t.TempDir()
	up := hosting.NewUploads(state, io.Discard)
	t.Cleanup(up.Close)
	f.adb.SetUploads(up)
	return f, up, filepath.Join(state, filepath.FromSlash(hosting.UploadsDir))
}

func upload(t *testing.T, up *hosting.Uploads, peer, body string) (id, sum string) {
	t.Helper()
	id, sum, err := up.Put(peer, int64(len(body)), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return id, sum
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func installCalls(f *hostFixture, t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range devicetest.Calls(t, f.dir) {
		if strings.Contains(c, " install ") {
			out = append(out, c)
		}
	}
	return out
}

// Decision (ADR-0099 §3): device.installUpload runs the install the build path runs, on the
// file the calling peer uploaded, and the file is gone afterwards: after a success and
// after `Failure [INSTALL_FAILED_…]`, whose token alone is the cause. The result is the
// digest of the file installed, and no error names the host's path.
func TestInstallUploadInstallsTheFileAndRemovesIt(t *testing.T) {
	f, up, dir := uploadFixture(t)
	body := "PK\x03\x04 the bytes of an apk"
	h := sha256.Sum256([]byte(body))
	want := hex.EncodeToString(h[:])
	devicetest.Write(t, f.dir, "R58M001.install.out", []byte("Performing Streamed Install\nSuccess\n"))

	id, sum := upload(t, up, anna, body)
	if sum != want {
		t.Fatalf("the upload's digest = %s; want %s", sum, want)
	}
	out := f.call(t, anna, "device.installUpload", `{"device":"R58M001","upload":"`+id+`"}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("installUpload: %s, %+v", out.State, out.Error)
	}
	if r, ok := out.Result.(InstallUploadResult); !ok || r.SHA256 != want {
		t.Errorf("result = %+v; want the digest %s", out.Result, want)
	}
	installed, err := os.ReadFile(filepath.Join(f.dir, "R58M001.installed"))
	if err != nil || string(installed) != body {
		t.Errorf("adb installed %q (%v); want the uploaded bytes", installed, err)
	}
	calls := installCalls(f, t)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "-s R58M001 install -r "+dir+string(filepath.Separator)) {
		t.Errorf("install calls = %v; want one `-s R58M001 install -r` of the upload", calls)
	}
	if got := dirFiles(t, dir); len(got) != 0 {
		t.Errorf("after a successful install the upload directory holds %v", got)
	}

	// A refusal: the file is gone, the token is the cause, the path is nowhere.
	devicetest.Write(t, f.dir, "R58M001.install.err", []byte("adb: failed to install "+dir+"/x.apk: Failure [INSTALL_FAILED_UPDATE_INCOMPATIBLE: Package signatures do not match]\n"))
	devicetest.Write(t, f.dir, "R58M001.install.exit", []byte("1"))
	id, _ = upload(t, up, anna, body)
	out = f.call(t, anna, "device.installUpload", `{"device":"R58M001","upload":"`+id+`"}`)
	ce := mustFail(t, out, ErrInstallFailed)
	if ce.Cause != "INSTALL_FAILED_UPDATE_INCOMPATIBLE" || strings.Contains(ce.Message+ce.Cause+ce.NextStep, dir) {
		t.Errorf("error = %+v; want the token alone as cause and no path", ce)
	}
	if got := dirFiles(t, dir); len(got) != 0 {
		t.Errorf("after a failed install the upload directory holds %v", got)
	}
}

// Decision (ADR-0099 §3): an upload is its peer's only, and the preamble decides first. An
// upload another peer made, a made-up id, an id shaped as a path, one already used and a
// caller that is no peer (an upload has no other owner) are REQUEST_INVALID_PARAMS with
// nothing installed and nobody else's upload consumed; a device not lent to the caller is
// DEVICE_NOT_FOUND and leaves the caller's upload where it was.
func TestInstallUploadIsThePeersOwn(t *testing.T) {
	f, up, dir := uploadFixture(t)
	devicetest.Write(t, f.dir, "R58M001.install.out", []byte("Success\n"))
	devicetest.Write(t, f.dir, "R58M002.install.out", []byte("Success\n"))
	bobs, _ := upload(t, up, bob, "bob's apk")

	// Each is the caller's device, and none of the ids is the caller's upload. The host's own
	// caller (no peer) has no upload at all, bob's included.
	for name, c := range map[string]struct{ who, upload string }{
		"another peer's":   {anna, bobs},
		"never issued":     {anna, strings.Repeat("0", 32)},
		"shaped as a path": {anna, "../x"},
		"no peer at all":   {"", bobs},
	} {
		mustFail(t, f.call(t, c.who, "device.installUpload", `{"device":"R58M001","upload":"`+c.upload+`"}`), capability.ErrInvalidParams)
		if got := installCalls(f, t); len(got) != 0 {
			t.Fatalf("%s: an install ran: %v", name, got)
		}
		if got := dirFiles(t, dir); len(got) != 1 {
			t.Fatalf("%s: the upload directory holds %v; want bob's upload untouched", name, got)
		}
	}

	// A device that is not lent to the caller is not there; her upload is not consumed.
	annas, _ := upload(t, up, anna, "anna's apk")
	mustFail(t, f.call(t, anna, "device.installUpload", `{"device":"R58M002","upload":"`+annas+`"}`), ErrNotFound)
	if got := installCalls(f, t); len(got) != 0 {
		t.Fatalf("an install ran on a device that is not lent: %v", got)
	}
	out := f.call(t, anna, "device.installUpload", `{"device":"R58M001","upload":"`+annas+`"}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("anna's own upload on her device: %s, %+v", out.State, out.Error)
	}
	// Used once.
	mustFail(t, f.call(t, anna, "device.installUpload", `{"device":"R58M001","upload":"`+annas+`"}`), capability.ErrInvalidParams)
}

// Decision (verifier nit): an upload that is gone by the time the install takes it (it
// expired, or a newer one replaced it, while the call waited for the device's write key) is
// REQUEST_INVALID_PARAMS whose cause says what happened, and whose next step is the
// client's tool, device.install, which uploads again.
func TestInstallUploadSaysWhyItIsGone(t *testing.T) {
	f, up, _ := uploadFixture(t)
	first, _ := upload(t, up, anna, "first")
	upload(t, up, anna, "second") // replaces the first
	ce := mustFail(t, f.call(t, anna, "device.installUpload", `{"device":"R58M001","upload":"`+first+`"}`), capability.ErrInvalidParams)
	if !strings.Contains(ce.Cause, "replaced by a newer upload") || !strings.Contains(ce.NextStep, "device.install") {
		t.Errorf("error = %+v; want a cause that says it was replaced and a next step that repeats device.install", ce)
	}
}

// Decision (ADR-0060 §2, ADR-0099 §3): device.installUpload is for humans and peers; an MCP
// session is refused with REQUEST_NOT_ALLOWED_HERE before anything is recorded or run.
func TestInstallUploadIsNotForMCP(t *testing.T) {
	f, up, dir := uploadFixture(t)
	id, _ := upload(t, up, anna, "apk")
	_, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.installUpload", Client: capability.ClientMCP,
		Params: []byte(`{"device":"R58M001","upload":"` + id + `"}`),
		Origin: capability.Origin{ID: "mcp-1", Client: capability.ClientMCP},
	})
	if ce := capability.AsError(err); ce == nil || ce.Code != capability.ErrNotAllowedHere {
		t.Fatalf("an MCP call: %v; want REQUEST_NOT_ALLOWED_HERE", err)
	}
	if len(installCalls(f, t)) != 0 || len(dirFiles(t, dir)) != 1 {
		t.Errorf("a refused call ran %v or consumed the upload (%v)", installCalls(f, t), dirFiles(t, dir))
	}
}
