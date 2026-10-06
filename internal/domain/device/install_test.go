package device

import (
	"context"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// installWS is the workspace the install fixture admits.
const installWS = "ws-0123456789ab"

// stubResolver is device.install's build resolver in the device tests: it answers with a
// canned output or error and records what it was asked, so a test sees that the handler
// resolves before it touches the device.
type stubResolver struct {
	out   build.ResolvedOutput
	err   error
	calls int
	last  [4]string
}

func (s *stubResolver) ResolveBuild(wsid, buildID, module, variant string) (build.ResolvedOutput, error) {
	s.calls++
	s.last = [4]string{wsid, buildID, module, variant}
	return s.out, s.err
}

// admitInstall admits installWS and nothing else, the way the workspace registry admits
// an open one.
type admitInstall struct{}

func (admitInstall) Admit(target string, record func(string)) bool {
	if target != installWS {
		return false
	}
	record(installWS)
	return true
}

type installFixture struct {
	dir string
	ops *capability.Operations
}

func newInstallFixture(t *testing.T, resolver BuildResolver, files map[string]string) *installFixture {
	t.Helper()
	base := map[string]string{
		"devices.out":                   uiList(phoneLine),
		uiSerial + ".bootcompleted.out": "1\n",
	}
	for k, v := range files {
		base[k] = v
	}
	dir := t.TempDir()
	for name, content := range base {
		devicetest.Write(t, dir, name, []byte(content))
	}
	adb := newFake(t, dir, closedPort(t))
	reg := capability.NewRegistry()
	Register(reg, adb, testStore(t), resolver)
	ops := capability.NewOperations(reg)
	ops.UseWorkspaces(admitInstall{})
	ops.RegisterCapabilities()
	return &installFixture{dir: dir, ops: ops}
}

func (f *installFixture) call(t *testing.T, name, params string) capability.Outcome {
	t.Helper()
	// device.install is workspace-scoped and needs the workspace; device.uninstall is
	// global and must not carry one.
	ws := ""
	if name == "device.install" {
		ws = installWS
	}
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: name, Workspace: ws, Params: []byte(params),
	})
	if err != nil {
		t.Fatalf("%s Start: %v", name, err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(10*time.Second))
	if err != nil {
		t.Fatalf("%s Wait: %v", name, err)
	}
	return out
}

func (f *installFixture) calls(t *testing.T) []string { return devicetest.Calls(t, f.dir) }

func mustInstall(t *testing.T, out capability.Outcome) InstallResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(InstallResult)
	if !ok {
		t.Fatalf("result is %T, want InstallResult", out.Result)
	}
	return r
}

// Decision (plan m1g step 8, check): a resolved output reaches `install -r` with its
// path, and the result carries the record's applicationId, versionCode and APK name.
func TestInstallRunsTheResolvedAPK(t *testing.T) {
	res := &stubResolver{out: build.ResolvedOutput{
		BuildID: "build-1", Module: ":app", Variant: "debug",
		ApplicationID: "com.example.app", VersionCode: intPtr(7),
		APK: "app-debug.apk", Path: "/state/ws/builds/app-debug.apk",
	}}
	f := newInstallFixture(t, res, map[string]string{
		uiSerial + ".install.out": "Performing Streamed Install\nSuccess\n",
	})
	out := f.call(t, "device.install", `{"device":"emulator-5554","build":{"module":":app","variant":"debug"}}`)
	r := mustInstall(t, out)
	if r.ApplicationID != "com.example.app" || r.APK != "app-debug.apk" || r.VersionCode == nil || *r.VersionCode != 7 {
		t.Errorf("result = %+v, want applicationId/versionCode/apk from the record", r)
	}
	if n := count(f.calls(t), "-s emulator-5554 install -r /state/ws/builds/app-debug.apk"); n != 1 {
		t.Errorf("install ran %d times, want once with the resolved path: %v", n, f.calls(t))
	}
	if res.last != [4]string{installWS, "", ":app", "debug"} {
		t.Errorf("resolver args = %v, want the workspace and the reference", res.last)
	}
}

// Decision (plan m1g step 8, check): the build resolution's errors are the call's, and
// nothing is installed when it fails. Seen failing with the resolution after the install.
func TestInstallBuildResolutionFailures(t *testing.T) {
	for _, code := range []capability.ErrorCode{
		build.ErrBuildOutputNotFound, build.ErrBuildOutputStale, build.ErrBuildOutputUnsupported, capability.ErrBuildNotFound,
	} {
		res := &stubResolver{err: capability.NewError(code, "no")}
		f := newInstallFixture(t, res, nil)
		out := f.call(t, "device.install", `{"device":"emulator-5554","build":{"module":":app","variant":"debug"}}`)
		mustFail(t, out, code)
		for _, c := range f.calls(t) {
			if len(c) >= len("-s emulator-5554 install") && c[:len("-s emulator-5554 install")] == "-s emulator-5554 install" {
				t.Fatalf("%s: an install ran: %v", code, f.calls(t))
			}
		}
	}
}

// Decision (plan m1g step 8, R3): a "Failure [INSTALL_FAILED_…]" is DEVICE_INSTALL_FAILED
// even on exit 0, and the cause is the bracketed token alone. Seen failing with exit 0
// trusted.
func TestInstallFailureTokenOnExitZero(t *testing.T) {
	res := &stubResolver{out: build.ResolvedOutput{ApplicationID: "com.example.app", APK: "app.apk", Path: "/a/app.apk"}}
	f := newInstallFixture(t, res, map[string]string{
		uiSerial + ".install.err": "adb: failed to install app.apk: Failure [INSTALL_FAILED_INSUFFICIENT_STORAGE]\n",
	})
	out := f.call(t, "device.install", `{"device":"emulator-5554","build":{"module":":app","variant":"debug"}}`)
	e := mustFail(t, out, ErrInstallFailed)
	if e.Cause != "INSTALL_FAILED_INSUFFICIENT_STORAGE" {
		t.Errorf("cause = %q, want the token alone", e.Cause)
	}
}

// Decision (plan m1g step 8, check): a physical serial is DEVICE_NOT_ALLOWED from the
// hook, and the build is never resolved or installed.
func TestInstallPhysicalDeviceRefusedBeforeResolving(t *testing.T) {
	res := &stubResolver{out: build.ResolvedOutput{ApplicationID: "com.example.app", APK: "app.apk", Path: "/a/app.apk"}}
	f := newInstallFixture(t, res, map[string]string{
		"devices.out": uiList("R58M12ABCDE device usb:1-1.2 product:raven model:Pixel_6_Pro transport_id:7\n"),
	})
	out := f.call(t, "device.install", `{"device":"R58M12ABCDE","build":{"module":":app","variant":"debug"}}`)
	mustFail(t, out, ErrNotAllowed)
	if res.calls != 0 {
		t.Errorf("the resolver ran %d times for a refused device", res.calls)
	}
}

// Decision (plan m1g R6): a module or variant that is not project-shaped is rejected
// before anything runs, device or build. Seen failing with the validation after the hook.
func TestInstallInvalidReferenceRejected(t *testing.T) {
	res := &stubResolver{}
	for _, params := range []string{
		`{"device":"emulator-5554","build":{"module":"app","variant":"debug"}}`,
		`{"device":"emulator-5554","build":{"module":":app","variant":"Debug"}}`,
	} {
		f := newInstallFixture(t, res, nil)
		out := f.call(t, "device.install", params)
		mustFail(t, out, capability.ErrInvalidParams)
		if res.calls != 0 {
			t.Errorf("the resolver ran for %s", params)
		}
		if n := len(f.calls(t)); n != 0 {
			t.Errorf("adb ran %d times for %s: %v", n, params, f.calls(t))
		}
	}
}

// Decision (plan m1g step 8, check; D18): an absent package is a completed call with
// uninstalled false, and no uninstall runs.
func TestUninstallAbsentPackage(t *testing.T) {
	f := newInstallFixture(t, &stubResolver{}, map[string]string{
		uiSerial + ".pmpath.out": "",
	})
	out := f.call(t, "device.uninstall", `{"device":"emulator-5554","applicationId":"com.example.app"}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(UninstallResult)
	if !ok || r.Uninstalled || r.Reason != "notInstalled" {
		t.Errorf("result = %+v, want uninstalled false/notInstalled", out.Result)
	}
	if n := count(f.calls(t), "-s emulator-5554 uninstall com.example.app"); n != 0 {
		t.Errorf("uninstall ran %d times, want none: %v", n, f.calls(t))
	}
}

// Decision (plan m1g step 8, D18): a present package is uninstalled; adb's own
// "Success" is what decides.
func TestUninstallPresentPackage(t *testing.T) {
	f := newInstallFixture(t, &stubResolver{}, map[string]string{
		uiSerial + ".pmpath.out":    "package:/data/app/com.example.app/base.apk\n",
		uiSerial + ".uninstall.out": "Success\n",
	})
	out := f.call(t, "device.uninstall", `{"device":"emulator-5554","applicationId":"com.example.app"}`)
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(UninstallResult)
	if !ok || !r.Uninstalled || r.ApplicationID != "com.example.app" {
		t.Errorf("result = %+v, want uninstalled true", out.Result)
	}
}

// Decision (plan m1g step 8, D18): a "Failure […]" from uninstall is
// DEVICE_UNINSTALL_FAILED with the bracketed token as the cause.
func TestUninstallFailureToken(t *testing.T) {
	f := newInstallFixture(t, &stubResolver{}, map[string]string{
		uiSerial + ".pmpath.out":    "package:/data/app/com.example.app/base.apk\n",
		uiSerial + ".uninstall.err": "Failure [DELETE_FAILED_INTERNAL_ERROR]\n",
	})
	out := f.call(t, "device.uninstall", `{"device":"emulator-5554","applicationId":"com.example.app"}`)
	e := mustFail(t, out, ErrUninstallFailed)
	if e.Cause != "DELETE_FAILED_INTERNAL_ERROR" {
		t.Errorf("cause = %q, want the token alone", e.Cause)
	}
}

// Decision (plan m1g R6, check): an applicationId that is not a package name is
// rejected before the device is touched.
func TestUninstallInvalidApplicationIDRejected(t *testing.T) {
	f := newInstallFixture(t, &stubResolver{}, nil)
	out := f.call(t, "device.uninstall", `{"device":"emulator-5554","applicationId":"a.b;x"}`)
	mustFail(t, out, capability.ErrInvalidParams)
	if n := len(f.calls(t)); n != 0 {
		t.Errorf("adb ran %d times for a rejected id: %v", n, f.calls(t))
	}
}

// Decision (plan m1g R2, mcp-tool-surface.md §The MVP tool surface): the published
// classification and annotations of the two capabilities, and the codes each lists.
func TestInstallUninstallDescriptors(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, NewADB(nil, nil), testStore(t), &stubResolver{})
	byName := map[string]capability.Descriptor{}
	for _, d := range reg.All() {
		byName[d.Name] = d
	}

	install := byName["device.install"]
	if install.Scope != capability.Workspace || install.Kind != capability.Command || !install.LongRunning || install.Idempotent {
		t.Errorf("device.install = scope %s kind %s LR=%v idem=%v", install.Scope, install.Kind, install.LongRunning, install.Idempotent)
	}
	a := install.ResolveAnnotations()
	if a.Destructive || !a.OpenWorld || a.ReadOnly || a.Idempotent {
		t.Errorf("device.install annotations = %+v, want not destructive, open world, not read-only/idempotent", a)
	}
	if !containsAll(install.Errors, build.ErrBuildOutputNotFound, build.ErrBuildOutputStale, build.ErrBuildOutputUnsupported, ErrInstallFailed) {
		t.Errorf("device.install errors = %v", install.Errors)
	}
	if containsCode(install.Errors, capability.ErrOperationQueueTimeout) {
		t.Error("device.install is long-running and should not list OPERATION_QUEUE_TIMEOUT")
	}

	uninstall := byName["device.uninstall"]
	if uninstall.Scope != capability.Global || uninstall.Kind != capability.Command || uninstall.LongRunning || uninstall.Idempotent {
		t.Errorf("device.uninstall = scope %s kind %s LR=%v idem=%v", uninstall.Scope, uninstall.Kind, uninstall.LongRunning, uninstall.Idempotent)
	}
	ua := uninstall.ResolveAnnotations()
	if !ua.Destructive || !ua.OpenWorld {
		t.Errorf("device.uninstall annotations = %+v, want destructive and open world", ua)
	}
	if !containsAll(uninstall.Errors, ErrUninstallFailed, capability.ErrOperationQueueTimeout) {
		t.Errorf("device.uninstall errors = %v", uninstall.Errors)
	}
}

func containsCode(codes []capability.ErrorCode, code capability.ErrorCode) bool {
	for _, c := range codes {
		if c == code {
			return true
		}
	}
	return false
}

func containsAll(codes []capability.ErrorCode, want ...capability.ErrorCode) bool {
	for _, w := range want {
		if !containsCode(codes, w) {
			return false
		}
	}
	return true
}

func intPtr(n int) *int { return &n }
