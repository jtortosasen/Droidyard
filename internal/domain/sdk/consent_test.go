package sdk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The fixture's licence texts, normalised by hand the way sdklib does (the four steps of
// licenceHash's doc), so the tests do not ask licenceHash what it should answer.
var (
	fxHash        = sha1hex("Synthetic test licence.\n\nTerms that are made up for the tests.")
	fxOtherHash   = sha1hex("A different synthetic text for the same licence id.")
	fxPreviewHash = sha1hex("A synthetic preview licence.")
)

type consentFixture struct {
	t    *testing.T
	srv  *fxServer
	ops  *capability.Operations
	root string
	env  []string
	log  *syncBuf
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newConsent registers the sdk capabilities over the fixture repository, with ANDROID_HOME
// set to root.
func newConsent(t *testing.T, files map[string]string, root string) *consentFixture {
	t.Helper()
	srv := newFxServer(t, files)
	f := &consentFixture{t: t, srv: srv, root: root, env: []string{"ANDROID_HOME=" + root}, log: &syncBuf{}}
	reg := capability.NewRegistry()
	Register(reg, Options{Env: f.env, RepositoryBase: srv.base(), HTTPClient: srv.Client(), GOOS: "darwin", GOARCH: "arm64", Log: f.log})
	f.ops = capability.NewOperations(reg)
	return f
}

// as runs name as a caller of that client kind and waits for the outcome.
func (f *consentFixture) as(client capability.ClientKind, name, params string) capability.Outcome {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, err := f.ops.Start(ctx, capability.Request{Capability: name, Params: []byte(params), Client: client,
		Origin: capability.Origin{ID: string(client) + "-1", Client: client}})
	if err != nil {
		return capability.Outcome{Capability: name, State: capability.StateFailed, Error: asCapabilityError(err)}
	}
	out, err := f.ops.Wait(ctx, id, time.Now().Add(15*time.Second))
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func asCapabilityError(err error) *capability.Error {
	var ce *capability.Error
	if errors.As(err, &ce) {
		return ce
	}
	return &capability.Error{Code: "TEST_UNTYPED", Message: err.Error()}
}

func (f *consentFixture) accept(client capability.ClientKind, id, sum string) capability.Outcome {
	f.t.Helper()
	return f.as(client, "sdk.acceptLicense", fmt.Sprintf(`{"id":%q,"sha1":%q}`, id, sum))
}

func wantCode(t *testing.T, out capability.Outcome, code capability.ErrorCode) {
	t.Helper()
	if out.Error == nil || out.Error.Code != code {
		t.Fatalf("outcome = %s %+v, want error %s", out.State, out.Error, code)
	}
}

func wantDone(t *testing.T, out capability.Outcome) AcceptLicenseResult {
	t.Helper()
	if out.State != capability.StateCompleted || out.Error != nil {
		t.Fatalf("outcome = %s %+v, want completed", out.State, out.Error)
	}
	return out.Result.(AcceptLicenseResult)
}

func readLicence(t *testing.T, root, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "licenses", id))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noLicencesDir(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, "licenses")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("licenses/ exists under %s after a refused call (err %v)", root, err)
	}
}

// TestFixtureHashesAreSdklibs pins the by-hand values above against licenceHash, so a
// slip in the derivation shows here and not as a confusing failure below.
func TestFixtureHashesAreSdklibs(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"current", fxLicenceText, fxHash},
		{"other", fxOtherLicenceText, fxOtherHash},
		{"preview", "A synthetic preview licence.", fxPreviewHash},
	} {
		if got := licenceHash(c.text); got != c.want {
			t.Errorf("%s: licenceHash = %s, want %s", c.name, got, c.want)
		}
	}
}

// TestAcceptLicenseWritesTheHash pins what is written: a newline and the hash sdklib
// computes (the normalised text's sha1), the line AGP and the classic tools compare. The
// text's raw sha1 and a plainly trimmed text's sha1 are lines nobody reads. Seen failing
// with licenceHash returning the raw sha1.
func TestAcceptLicenseWritesTheHash(t *testing.T) {
	f := newConsent(t, fxDefault(), testRoot(t, "36.0.0"))
	res := wantDone(t, f.accept(capability.ClientUI, "android-sdk-license", fxHash))
	if !res.Added || res.Root != f.root || res.ID != "android-sdk-license" || res.SHA1 != fxHash {
		t.Errorf("result = %+v", res)
	}
	got := readLicence(t, f.root, "android-sdk-license")
	if got != "\n"+fxHash {
		t.Errorf("licenses/android-sdk-license = %q, want a newline and %s", got, fxHash)
	}
	if strings.Contains(got, sha1hex(fxLicenceText)) || strings.Contains(got, sha1hex(strings.TrimSpace(fxLicenceText))) {
		t.Error("the file holds the raw or the plainly trimmed text's hash")
	}
	if !licenseAccepted(f.root, "android-sdk-license", fxHash) {
		t.Error("what was written does not read back as accepted")
	}
	if fi, err := os.Stat(filepath.Join(f.root, "licenses", "android-sdk-license")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %v (%v), want 0644", fi.Mode().Perm(), err)
	}
}

// TestAcceptLicenseKeepsOtherLines pins that the owner's other lines (an older text's hash,
// with or without a final newline) survive byte for byte, the file's mode is kept, and no
// temporary file is left beside it.
func TestAcceptLicenseKeepsOtherLines(t *testing.T) {
	for _, c := range []struct{ name, before string }{
		{"no final newline", "\n" + fxOtherHash},
		{"final newline", "\n" + fxOtherHash + "\n"},
		{"two lines", "\nabcdef\n" + fxOtherHash},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := testRoot(t, "36.0.0")
			dir := filepath.Join(root, "licenses")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "android-sdk-license")
			if err := os.WriteFile(path, []byte(c.before), 0o600); err != nil {
				t.Fatal(err)
			}
			f := newConsent(t, fxDefault(), root)
			wantDone(t, f.accept(capability.ClientCLI, "android-sdk-license", fxHash))
			after := readLicence(t, root, "android-sdk-license")
			if !strings.HasPrefix(after, c.before) {
				t.Errorf("file = %q, want it to begin with the old content %q", after, c.before)
			}
			if !strings.HasSuffix(after, "\n"+fxHash) {
				t.Errorf("file = %q, want the new hash on a line of its own at the end", after)
			}
			if !licenseAccepted(root, "android-sdk-license", fxOtherHash) || !licenseAccepted(root, "android-sdk-license", fxHash) {
				t.Errorf("a hash is no longer a line of %q", after)
			}
			if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v, want the file's own 0600 kept", fi.Mode().Perm())
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Errorf("licenses/ holds %d entries, want only the licence file", len(entries))
			}
		})
	}
}

// TestAcceptLicenseIdempotent pins that accepting what is accepted writes nothing: one
// line, the file untouched (its modification time, set to the past, is the same), and
// the answer says nothing was added.
func TestAcceptLicenseIdempotent(t *testing.T) {
	f := newConsent(t, fxDefault(), testRoot(t, "36.0.0"))
	wantDone(t, f.accept(capability.ClientUI, "android-sdk-license", fxHash))
	path := filepath.Join(f.root, "licenses", "android-sdk-license")
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	res := wantDone(t, f.accept(capability.ClientUI, "android-sdk-license", fxHash))
	if res.Added {
		t.Error("the second call says it added the hash")
	}
	if n := strings.Count(readLicence(t, f.root, "android-sdk-license"), fxHash); n != 1 {
		t.Errorf("the hash is on %d lines, want 1", n)
	}
	if fi, _ := os.Stat(path); !fi.ModTime().Equal(past) {
		t.Errorf("modification time = %v, want %v: the file was rewritten", fi.ModTime(), past)
	}
}

// TestAcceptLicenseRefusesAnotherText pins the consent rule (ADR-0129 §2): the hash must
// be that of a text the repository serves for that id now. Another text for the id, the
// raw sha1 of the served text, a hash with no text, an unknown id and a hash of another
// id's text are all SDK_LICENSE_UNKNOWN, pointing at sdk.licenses, and nothing is
// written. Seen failing with the check removed.
func TestAcceptLicenseRefusesAnotherText(t *testing.T) {
	f := newConsent(t, fxDefault(), testRoot(t, "36.0.0"))
	for _, c := range []struct{ name, id, sum string }{
		{"a text the repository does not serve", "android-sdk-license", fxOtherHash},
		{"the raw sha1 of the served text", "android-sdk-license", sha1hex(fxLicenceText)},
		{"a hash of nothing", "android-sdk-license", strings.Repeat("0", 40)},
		{"an id it does not serve", "android-sdk-arm-dbt-license", fxHash},
		{"another id's text", "android-sdk-preview-license", fxHash},
	} {
		out := f.accept(capability.ClientUI, c.id, c.sum)
		wantCode(t, out, ErrLicenseUnknown)
		if !strings.Contains(out.Error.NextStep, "sdk.licenses") {
			t.Errorf("%s: next step = %q, want sdk.licenses", c.name, out.Error.NextStep)
		}
		noLicencesDir(t, f.root)
	}
	if got := f.log.String(); got != "" {
		t.Errorf("refused calls logged %q", got)
	}
}

// TestAcceptLicenseValidatesTheShape pins that an id or a hash that cannot be what the
// repository serves is refused before the repository is asked, and an id never reaches a
// path.
func TestAcceptLicenseValidatesTheShape(t *testing.T) {
	f := newConsent(t, fxDefault(), testRoot(t, "36.0.0"))
	for _, c := range []struct{ id, sum string }{
		{"../licenses/x", fxHash},
		{"Android-SDK", fxHash},
		{"android-sdk-license", "xyz"},
		{"android-sdk-license", strings.Repeat("g", 40)},
	} {
		wantCode(t, f.accept(capability.ClientUI, c.id, c.sum), capability.ErrInvalidParams)
	}
	if got := f.srv.requests(); len(got) != 0 {
		t.Errorf("a malformed call reached the repository: %v", got)
	}
	noLicencesDir(t, f.root)
	// An upper-case hash is the same hash.
	wantDone(t, f.accept(capability.ClientUI, "android-sdk-license", strings.ToUpper(fxHash)))
	if got := readLicence(t, f.root, "android-sdk-license"); got != "\n"+fxHash {
		t.Errorf("file = %q, want the lower-case hash", got)
	}
}

// TestAcceptLicenseRefusedOverMCP pins ADR-0015 §6 for consent: an agent's session is
// refused (REQUEST_NOT_ALLOWED_HERE, before anything runs) and nothing is written, while
// the UI and the CLI write. Seen failing with the audience set to all.
func TestAcceptLicenseRefusedOverMCP(t *testing.T) {
	for _, name := range []string{"sdk.acceptLicense", "sdk.licenses"} {
		f := newConsent(t, fxDefault(), testRoot(t, "36.0.0"))
		out := f.as(capability.ClientMCP, name, fmt.Sprintf(`{"id":"android-sdk-license","sha1":%q}`, fxHash))
		wantCode(t, out, capability.ErrNotAllowedHere)
		noLicencesDir(t, f.root)
		if got := f.srv.requests(); len(got) != 0 {
			t.Errorf("%s: the refused call reached the repository: %v", name, got)
		}
	}
	for _, client := range []capability.ClientKind{capability.ClientUI, capability.ClientCLI} {
		f := newConsent(t, fxDefault(), testRoot(t, "36.0.0"))
		wantDone(t, f.accept(client, "android-sdk-license", fxHash))
		if !licenseAccepted(f.root, "android-sdk-license", fxHash) {
			t.Errorf("%s: nothing was written", client)
		}
	}
}

// TestAcceptLicenseRefusesLinks pins that a licenses directory or a licence file that is a
// symbolic link is never written through or replaced: SDK_NOT_WRITABLE, the cause names the
// entry relative to the root, and what the link points at is untouched.
func TestAcceptLicenseRefusesLinks(t *testing.T) {
	t.Run("licenses is a link", func(t *testing.T) {
		root := testRoot(t, "36.0.0")
		elsewhere := t.TempDir()
		if err := os.Symlink(elsewhere, filepath.Join(root, "licenses")); err != nil {
			t.Fatal(err)
		}
		f := newConsent(t, fxDefault(), root)
		out := f.accept(capability.ClientUI, "android-sdk-license", fxHash)
		wantCode(t, out, ErrNotWritable)
		if !strings.Contains(out.Error.Cause, "licenses") || strings.Contains(out.Error.Cause, root) {
			t.Errorf("cause = %q, want the entry relative to the root", out.Error.Cause)
		}
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
			t.Errorf("the link's target holds %d entries after the call", len(entries))
		}
	})
	t.Run("the licence file is a link", func(t *testing.T) {
		root := testRoot(t, "36.0.0")
		if err := os.MkdirAll(filepath.Join(root, "licenses"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.WriteFile(target, []byte("\nkept"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "licenses", "android-sdk-license")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		f := newConsent(t, fxDefault(), root)
		out := f.accept(capability.ClientUI, "android-sdk-license", fxHash)
		wantCode(t, out, ErrNotWritable)
		if !strings.Contains(out.Error.Cause, filepath.Join("licenses", "android-sdk-license")) {
			t.Errorf("cause = %q, want licenses/android-sdk-license", out.Error.Cause)
		}
		if b, _ := os.ReadFile(target); string(b) != "\nkept" {
			t.Errorf("the link's target = %q, want it untouched", b)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link was replaced (%v, %v)", fi, err)
		}
	})
}

// TestAcceptLicenseDuringInstall pins ADR-0129 §2's scheduler key: an install holds
// sdk:<root> (here a call that holds it until released), and accepting a licence runs at
// once instead of queueing behind it. Seen failing with the key set to the install's.
func TestAcceptLicenseDuringInstall(t *testing.T) {
	f := newConsent(t, fxDefault(), testRoot(t, "36.0.0"))
	reg := capability.NewRegistry()
	srv := newFxServer(t, fxDefault())
	Register(reg, Options{Env: f.env, RepositoryBase: srv.base(), HTTPClient: srv.Client(), GOOS: "darwin", GOARCH: "arm64", Log: f.log})
	held, release := make(chan struct{}), make(chan struct{})
	type none struct{}
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.install", Kind: capability.Command, Scope: capability.Global,
		Description: "Holds the install key.", Params: none{}, Result: none{}, LongRunning: true,
		Resource: func(*capability.Call) string { return "sdk:" + f.root },
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			close(held)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return none{}, nil
		},
	})
	ops := capability.NewOperations(reg)
	defer close(release)
	installID, err := ops.Start(context.Background(), capability.Request{Capability: "fixture.install", Client: capability.ClientUI, Origin: capability.Origin{ID: "ui-1", Client: capability.ClientUI}})
	if err != nil {
		t.Fatal(err)
	}
	<-held
	id, err := ops.Start(context.Background(), capability.Request{Capability: "sdk.acceptLicense", Client: capability.ClientUI, Origin: capability.Origin{ID: "ui-2", Client: capability.ClientUI},
		Params: []byte(fmt.Sprintf(`{"id":"android-sdk-license","sha1":%q}`, fxHash))})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ops.Wait(context.Background(), id, time.Now().Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	wantDone(t, out)
	if !licenseAccepted(f.root, "android-sdk-license", fxHash) {
		t.Error("the licence was not written while the install held its key")
	}
	if st, err := ops.Wait(context.Background(), installID, time.Now().Add(10*time.Millisecond)); err != nil || st.State == capability.StateCompleted {
		t.Errorf("the install finished too (%v, %v): the test did not hold it", st.State, err)
	}
}

// TestAcceptLicenseCreatesTheRoot pins ADR-0131 §3: on a host with no SDK the call creates
// the root and licenses/ (0755), and the root with nothing but licenses/ in it is still a
// target: a second licence is accepted into it and the resolver names the same root again,
// where a plain emptiness test would refuse both. Seen failing with a plain emptiness
// test.
func TestAcceptLicenseCreatesTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "sdk")
	f := newConsent(t, fxDefault(), root)
	if got, err := Target(f.env); err != nil || got != root {
		t.Fatalf("Target before = %q, %v; want %s", got, err, root)
	}
	res := wantDone(t, f.accept(capability.ClientUI, "android-sdk-license", fxHash))
	if res.Root != root {
		t.Errorf("root = %s, want %s", res.Root, root)
	}
	if fi, err := os.Stat(filepath.Join(root, "licenses")); err != nil || !fi.IsDir() || fi.Mode().Perm()&0o700 != 0o700 {
		t.Fatalf("licenses/ = %v, %v", fi, err)
	}
	if got, err := Target(f.env); err != nil || got != root {
		t.Errorf("Target after the first accept = %q, %v; want the same root, not refused", got, err)
	}
	wantDone(t, f.accept(capability.ClientUI, "android-sdk-preview-license", fxPreviewHash))
	if !licenseAccepted(root, "android-sdk-license", fxHash) || !licenseAccepted(root, "android-sdk-preview-license", fxPreviewHash) {
		t.Error("a licence is missing after two accepts")
	}
	// A leftover of the installer's temporary directory does not make the root foreign.
	if err := os.Mkdir(filepath.Join(root, ".temp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := Target(f.env); err != nil || got != root {
		t.Errorf("Target with .temp/ = %q, %v; want %s", got, err, root)
	}
}

// TestTargetRefusesANonSDKDirectory pins ADR-0129 §5: an existing directory that holds
// somebody's files and is not an SDK is not installed into, even when it also holds
// licenses/; a file in the way is refused; an SDK is used as it is.
func TestTargetRefusesANonSDKDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "licenses"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"ANDROID_HOME=" + root}
	_, err := Target(env)
	var ce *capability.Error
	if !errors.As(err, &ce) || ce.Code != ErrNotWritable {
		t.Fatalf("Target(%s with notes.txt) = %v, want SDK_NOT_WRITABLE", root, err)
	}
	// Refused for accepting a licence too, and nothing is written into it.
	f := newConsent(t, fxDefault(), root)
	wantCode(t, f.accept(capability.ClientUI, "android-sdk-license", fxHash), ErrNotWritable)
	if entries, _ := os.ReadDir(filepath.Join(root, "licenses")); len(entries) != 0 {
		t.Errorf("licenses/ holds %d entries after a refused call", len(entries))
	}

	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Target([]string{"ANDROID_HOME=" + file}); !errors.As(err, &ce) || ce.Code != ErrNotWritable {
		t.Errorf("Target(a file) = %v, want SDK_NOT_WRITABLE", err)
	}
	if _, err := Target([]string{"ANDROID_HOME=relative/sdk"}); !errors.As(err, &ce) || ce.Code != ErrNotWritable {
		t.Errorf("Target(a relative path) = %v, want SDK_NOT_WRITABLE", err)
	}
	if _, err := Target(nil); !errors.As(err, &ce) || ce.Code != ErrNotWritable {
		t.Errorf("Target(no SDK, no HOME) = %v, want SDK_NOT_WRITABLE", err)
	}

	sdkDir := testRoot(t, "36.0.0")
	if err := os.WriteFile(filepath.Join(sdkDir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := Target([]string{"ANDROID_HOME=" + sdkDir}); err != nil || got != sdkDir {
		t.Errorf("Target(an SDK with other files) = %q, %v; want it used", got, err)
	}
}

// TestTargetOrder pins ADR-0129 Q5's order: the host SDK first, then ANDROID_HOME, then
// ANDROID_SDK_ROOT, then the per-OS default under HOME; an empty directory is a target.
func TestTargetOrder(t *testing.T) {
	home := t.TempDir()
	emptyA, emptyB := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	hostSDK := testRoot(t, "36.0.0")
	if err := os.Mkdir(emptyA, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"a host SDK wins over ANDROID_HOME", []string{"ANDROID_SDK_ROOT=" + hostSDK, "ANDROID_HOME=" + emptyA}, hostSDK},
		{"ANDROID_HOME, an empty directory", []string{"ANDROID_HOME=" + emptyA, "ANDROID_SDK_ROOT=" + emptyB, "HOME=" + home}, emptyA},
		{"ANDROID_SDK_ROOT before the default", []string{"ANDROID_SDK_ROOT=" + emptyB, "HOME=" + home}, emptyB},
		{"the default under HOME", []string{"HOME=" + home}, DefaultSDK(home)},
	} {
		if got, err := Target(c.env); err != nil || got != c.want {
			t.Errorf("%s: Target = %q, %v; want %s", c.name, got, err, c.want)
		}
	}
}

// TestAcceptLicenseLogged pins the audit line: one daemon.log line per accepted licence
// naming the id, the hash, the root and the kind of client that asked, and nothing for a
// refused call.
func TestAcceptLicenseLogged(t *testing.T) {
	f := newConsent(t, fxDefault(), testRoot(t, "36.0.0"))
	wantCode(t, f.accept(capability.ClientUI, "android-sdk-license", fxOtherHash), ErrLicenseUnknown)
	wantDone(t, f.accept(capability.ClientCLI, "android-sdk-license", fxHash))
	want := fmt.Sprintf("sdk: licence android-sdk-license sha1=%s accepted in %s by client=cli\n", fxHash, f.root)
	if got := f.log.String(); got != want {
		t.Errorf("daemon.log got %q, want %q", got, want)
	}
}

// TestAcceptLicenseNeedsTheRepository pins that consent needs the repository's answer now:
// unreadable is SDK_REPOSITORY_UNAVAILABLE with its cause and nothing written, and so is a
// copy kept past its hour because the refetch failed.
func TestAcceptLicenseNeedsTheRepository(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		files := fxDefault()
		f := newConsent(t, files, testRoot(t, "36.0.0"))
		f.srv.status["repository2-3.xml"] = 503
		out := f.accept(capability.ClientUI, "android-sdk-license", fxHash)
		wantCode(t, out, ErrRepositoryUnavailable)
		if out.Error.Cause == "" {
			t.Error("no cause")
		}
		noLicencesDir(t, f.root)
	})
	t.Run("a stale copy is not now", func(t *testing.T) {
		srv := newFxServer(t, fxDefault())
		clock := &fxClock{t: time.Now()}
		c := &consent{env: []string{"ANDROID_HOME=" + testRoot(t, "36.0.0")}, repo: srv.repo(func(o *RepositoryOptions) { o.Now = clock.now }), log: &syncBuf{}}
		call := &capability.Call{Params: AcceptLicenseParams{ID: "android-sdk-license", SHA1: fxHash}, Origin: capability.Origin{Client: capability.ClientUI}}
		if _, err := c.accept(context.Background(), call); err != nil {
			t.Fatal(err)
		}
		clock.advance(2 * time.Hour)
		srv.status["repository2-3.xml"] = 503
		_, err := c.accept(context.Background(), call)
		var ce *capability.Error
		if !errors.As(err, &ce) || ce.Code != ErrRepositoryUnavailable {
			t.Errorf("accept on a stale copy = %v, want SDK_REPOSITORY_UNAVAILABLE", err)
		}
	})
}

// TestLicensesList pins the listing: one entry per (id, hash) the available packages are
// under, the text as served, acceptance read from the SDK root, at most 20 package ids per
// entry, the ids filter, and the repository's failure as a typed error.
func TestLicensesList(t *testing.T) {
	files := fxDefault()
	var many []fxPkg
	for i := 0; i < 25; i++ {
		many = append(many, fxPkg{Path: fmt.Sprintf("build-tools;30.0.%d", i), Rev: "30.0." + itoa(i)})
	}
	files["repository2-3.xml"] = fxRepo(fxLicenceText, many...)
	f := newConsent(t, files, testRoot(t, "36.0.0"))

	out := f.as(capability.ClientUI, "sdk.licenses", `{}`)
	res := out.Result.(LicensesResult)
	if res.Root != f.root || len(res.Licenses) != 1 {
		t.Fatalf("result = %+v, want one licence under %s", res, f.root)
	}
	l := res.Licenses[0]
	if l.ID != "android-sdk-license" || l.SHA1 != fxHash || l.Accepted || l.Text != fxLicenceText {
		t.Errorf("entry = %+v", l)
	}
	if len(l.Packages) != maxPackagesPerEntry {
		t.Errorf("%d package ids, want the first %d", len(l.Packages), maxPackagesPerEntry)
	}
	if !out.Untrusted {
		t.Error("the outcome is not marked untrusted: the licence text is the repository's")
	}

	wantDone(t, f.accept(capability.ClientUI, "android-sdk-license", fxHash))
	res = f.as(capability.ClientUI, "sdk.licenses", `{"ids":["android-sdk-license"]}`).Result.(LicensesResult)
	if len(res.Licenses) != 1 || !res.Licenses[0].Accepted {
		t.Errorf("after accepting: %+v", res.Licenses)
	}
	res = f.as(capability.ClientUI, "sdk.licenses", `{"ids":["android-sdk-preview-license"]}`).Result.(LicensesResult)
	if len(res.Licenses) != 0 || res.Licenses == nil {
		t.Errorf("a filter on an id no package uses = %+v, want an empty list", res.Licenses)
	}
	wantCode(t, f.as(capability.ClientUI, "sdk.licenses", `{"ids":["../x"]}`), capability.ErrInvalidParams)

	fresh := newConsent(t, files, f.root)
	fresh.srv.status["repository2-3.xml"] = 503
	wantCode(t, fresh.as(capability.ClientUI, "sdk.licenses", `{}`), ErrRepositoryUnavailable)
}

// TestLicensesTwoTextsOneID pins that an id carrying two texts across the lists is two
// entries, each accepted by its own hash.
func TestLicensesTwoTextsOneID(t *testing.T) {
	files := fxDefault()
	files["sys-img/android/sys-img2-3.xml"] = fxImgList(fxOtherLicenceText, fxImage("28", "default", "arm64-v8a", "arm64-v8a-28_r02.zip"))
	f := newConsent(t, files, testRoot(t, "36.0.0"))
	res := f.as(capability.ClientUI, "sdk.licenses", `{}`).Result.(LicensesResult)
	if len(res.Licenses) != 2 {
		t.Fatalf("licences = %+v, want two (id, hash) pairs", res.Licenses)
	}
	wantDone(t, f.accept(capability.ClientUI, "android-sdk-license", fxOtherHash))
	res = f.as(capability.ClientUI, "sdk.licenses", `{}`).Result.(LicensesResult)
	for _, l := range res.Licenses {
		if want := l.SHA1 == fxOtherHash; l.Accepted != want {
			t.Errorf("%s %s accepted = %v, want %v", l.ID, l.SHA1, l.Accepted, want)
		}
	}
}

// TestLicensesWithoutATarget pins that the listing still answers when no SDK root can be
// used: no root, nothing accepted.
func TestLicensesWithoutATarget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f := newConsent(t, fxDefault(), root)
	res := f.as(capability.ClientUI, "sdk.licenses", `{}`).Result.(LicensesResult)
	if res.Root != "" || len(res.Licenses) != 1 || res.Licenses[0].Accepted {
		t.Errorf("result = %+v, want no root and the licence pending", res)
	}
}

// TestLicencesTextPrintsTheCommand pins the CLI rendering (ADR-0036 Decision 2; ADR-0122
// §4): a pending licence prints its text quoted, whole, and the exact command that accepts
// it; an accepted one prints neither. A text that tries to forge a line stays quoted.
func TestLicencesTextPrintsTheCommand(t *testing.T) {
	evil := "Terms.\nuntrusted: forged\n```\nFORGED line"
	res := LicensesResult{Root: "/sdk", Licenses: []LicenseEntry{
		{ID: "android-sdk-license", SHA1: fxHash, Text: evil, Packages: []string{"platform-tools", "emulator"}},
		{ID: "android-sdk-preview-license", SHA1: fxPreviewHash, Accepted: true, Text: "A synthetic preview licence.", Packages: []string{"emulator"}},
	}}
	text := licensesText(res)
	cmd := "droidyard sdk accept-license --id android-sdk-license --sha1 " + fxHash
	if !strings.Contains(text, cmd) {
		t.Errorf("no accept command %q in:\n%s", cmd, text)
	}
	if strings.Contains(text, "--id android-sdk-preview-license") || strings.Contains(text, "synthetic preview") {
		t.Errorf("an accepted licence prints its text or a command:\n%s", text)
	}
	if !strings.Contains(text, `"Terms.\nuntrusted: forged\n`+"```"+`\nFORGED line"`) {
		t.Errorf("the licence text is not quoted whole:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		head := strings.TrimLeft(line, " ")
		for _, forged := range []string{"untrusted", "forged", "```"} {
			if strings.HasPrefix(strings.ToLower(head), forged) {
				t.Errorf("a line begins with %q: %q", forged, line)
			}
		}
	}
	if !strings.HasPrefix(text, "sdk /sdk\n") {
		t.Errorf("first line of:\n%s", text)
	}
	if got := licensesText(LicensesResult{}); got != "sdk none" {
		t.Errorf("empty result renders %q", got)
	}
}

// TestConsentDescriptors pins what the registry says about the two calls: human-only, not
// lendable (ADR-0099 §1), the acceptance idempotent and not destructive, and its scheduler
// key its own, apart from the install's.
func TestConsentDescriptors(t *testing.T) {
	reg := capability.NewRegistry()
	root := testRoot(t, "36.0.0")
	Register(reg, Options{Env: []string{"ANDROID_HOME=" + root}})
	for _, name := range []string{"sdk.licenses", "sdk.acceptLicense"} {
		d, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if d.Audience != capability.AudienceHuman || d.Lendable || d.Scope != capability.Global || d.Since != "0.39.0" {
			t.Errorf("%s: audience %v lendable %v scope %v since %s", name, d.Audience, d.Lendable, d.Scope, d.Since)
		}
		if a := d.ResolveAnnotations(); a.Destructive || !a.Idempotent || !a.OpenWorld {
			t.Errorf("%s annotations = %+v", name, a)
		}
	}
	d, _ := reg.Lookup("sdk.acceptLicense")
	key := d.Resource(&capability.Call{Params: AcceptLicenseParams{}})
	if key != "sdk.licenses:"+root || strings.HasPrefix(key, "sdk:") {
		t.Errorf("scheduler key = %q, want sdk.licenses:<root>, not the install's sdk:<root>", key)
	}
}
