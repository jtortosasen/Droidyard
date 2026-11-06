package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"golang.org/x/sys/unix"
)

// apkBytes is the least file an install takes: it starts as a zip archive does.
var apkBytes = []byte("PK\x03\x04 an apk the caller built somewhere else")

// apkTree is a folder of files for the apk checks: a real APK, a text file, a file named
// .apk that is no zip, a directory named .apk, and links to each of them.
type apkTree struct {
	dir      string // the folder, symlinks in it resolved
	good     string // a real APK
	notAnAPK string // a text file with the zip magic: only its name is wrong
	notAZip  string // named .apk, not a zip
	dirAPK   string // a directory named .apk
	empty    string // a .apk of no bytes
	big      string // a .apk that starts as a zip and is one byte over what a host takes (sparse)
}

func newAPKTree(t *testing.T) apkTree {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tr := apkTree{dir: dir,
		good:     w("app-debug.apk", apkBytes),
		notAnAPK: w("secret.txt", apkBytes),
		notAZip:  w("broken.apk", []byte("not a zip archive at all")),
		dirAPK:   filepath.Join(dir, "folder.apk"),
	}
	if err := os.Mkdir(tr.dirAPK, 0o700); err != nil {
		t.Fatal(err)
	}
	tr.empty = w("empty.apk", nil)
	tr.big = w("big.apk", apkBytes)
	if err := os.Truncate(tr.big, capability.PeerMaxUpload+1); err != nil {
		t.Fatal(err)
	}
	return tr
}

func (a apkTree) link(t *testing.T, name, to string) string {
	t.Helper()
	p := filepath.Join(a.dir, name)
	if err := os.Symlink(to, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// fifo makes a named pipe in the tree: opening it for reading blocks until a writer comes.
func (a apkTree) fifo(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(a.dir, name)
	if err := unix.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// enterCounter is a workspace registry that admits installWS and counts the projects it was
// asked to resolve, so a test sees whether a call opened one.
type enterCounter struct {
	mu sync.Mutex
	n  int
}

func (c *enterCounter) Enter(project string, record func(capability.Entered)) error {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return admitInstall{}.Enter(project, record)
}
func (c *enterCounter) Ended(string) {}
func (c *enterCounter) entered() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// runInstall makes a device.install call with params exactly as given (the project, if any,
// in them) and returns its outcome, or the error a call refused before it became an
// operation answers with.
func runInstall(t *testing.T, ops *capability.Operations, params string) (capability.Outcome, *capability.Error) {
	t.Helper()
	out, err := ops.Invoke(context.Background(), capability.Request{Capability: "device.install", Client: capability.ClientCLI, Params: []byte(params)})
	if err != nil {
		ce, ok := err.(*capability.Error)
		if !ok {
			t.Fatalf("device.install: %v", err)
		}
		return out, ce
	}
	if out.State == capability.StateFailed {
		return out, out.Error
	}
	return out, nil
}

// Decision (ADR-0137 §4; plan m3j step 8, N14): device.install takes exactly one of apk and
// build, an apk is an absolute path to a regular .apk file that starts as a zip and that a
// link cannot stand in for (every check is of what a link leads to, so a link cannot send
// another host file to a peer), and project goes with build alone. Every refusal is
// REQUEST_INVALID_PARAMS and touches neither the device nor a build nor, for a lent
// device, the host: reversing any rule installs, uploads or resolves something here.
func TestInstallApkOrBuild(t *testing.T) {
	tr := newAPKTree(t)
	apkOf := func(path string) string { return `{"device":"emulator-5554","apk":"` + path + `"}` }
	const build = `"build":{"module":":app","variant":"debug"}`
	cases := []struct {
		name   string
		params string
		cwd    string // the working directory the call is made from, "" for the test's own
		say    string // what the refusal says, so the rule that refused it is the one meant
	}{
		{"both apk and build, with a project", `{"project":"` + installWS + `","device":"emulator-5554","apk":"` + tr.good + `",` + build + `}`, "", "not both"},
		{"both apk and build", `{"device":"emulator-5554","apk":"` + tr.good + `",` + build + `}`, "", "not both"},
		{"neither", `{"device":"emulator-5554"}`, "", "neither"},
		{"build without project", `{"device":"emulator-5554",` + build + `}`, "", "takes project"},
		{"apk with a project", `{"project":"` + installWS + `","device":"emulator-5554","apk":"` + tr.good + `"}`, "", "takes no project"},
		{"a relative apk", apkOf("app-debug.apk"), "", "absolute"},
		{"a relative apk that is there from the working directory", apkOf("app-debug.apk"), tr.dir, "absolute"},
		{"an apk that is not there", apkOf(filepath.Join(tr.dir, "missing.apk")), "", "cannot be found"},
		{"a directory", apkOf(tr.dirAPK), "", "not a regular file"},
		// A FIFO would block the open until a writer came: it is refused before it.
		{"a FIFO named .apk", apkOf(tr.fifo(t, "pipe.apk")), "", "not a regular file"},
		{"a file that is not named .apk", apkOf(tr.notAnAPK), "", "not an .apk"},
		{"a link to a non-APK", apkOf(tr.link(t, "innocent.apk", tr.notAnAPK)), "", "not an .apk"},
		{"a link to a link to a non-APK", apkOf(tr.link(t, "twice.apk", filepath.Join(tr.dir, "innocent.apk"))), "", "not an .apk"},
		{"a link to a directory", apkOf(tr.link(t, "dirlink.apk", tr.dirAPK)), "", "not a regular file"},
		{"an empty .apk", apkOf(tr.empty), "", "empty or larger"},
		{"a .apk over the size a host takes", apkOf(tr.big), "", "empty or larger"},
		{"a .apk that is not a zip", apkOf(tr.notAZip), "", "zip"},
		{"a link to a .apk that is not a zip", apkOf(tr.link(t, "linkedbroken.apk", tr.notAZip)), "", "zip"},
	}
	for _, c := range cases {
		t.Run("local/"+c.name, func(t *testing.T) {
			if c.cwd != "" {
				t.Chdir(c.cwd)
			}
			res := &stubResolver{}
			f := newInstallFixture(t, res, nil)
			enter := &enterCounter{}
			f.ops.UseWorkspaces(enter)
			_, ce := runInstall(t, f.ops, c.params)
			if ce == nil || ce.Code != capability.ErrInvalidParams {
				t.Fatalf("error %+v; want %s", ce, capability.ErrInvalidParams)
			}
			if !strings.Contains(ce.Message, c.say) {
				t.Errorf("the refusal says %q; want it to say %q", ce.Message, c.say)
			}
			if !strings.Contains(c.params, "secret.txt") && strings.Contains(ce.Message+ce.Cause+ce.NextStep, "secret.txt") {
				t.Errorf("the error names the file a link leads to: %+v", ce)
			}
			if got := f.calls(t); len(got) != 0 {
				t.Errorf("the device was touched: %v", got)
			}
			if res.calls != 0 {
				t.Errorf("a build was resolved %d times", res.calls)
			}
		})
		t.Run("lent/"+c.name, func(t *testing.T) {
			if c.cwd != "" {
				t.Chdir(c.cwd)
			}
			h := &scriptedUploadHost{}
			f := newRemoteInstallFixture(t, h)
			_, ce := runInstall(t, f.ops, strings.ReplaceAll(c.params, `"emulator-5554"`, `"qa/R58M001"`))
			if ce == nil || ce.Code != capability.ErrInvalidParams {
				t.Fatalf("error %+v; want %s", ce, capability.ErrInvalidParams)
			}
			h.mu.Lock()
			got, calls := h.got, h.calls
			h.mu.Unlock()
			if got != "" || len(calls) != 0 {
				t.Errorf("the host received an upload (%q) or a call (%v)", got, calls)
			}
		})
	}
}

// Decision (ADR-0137 §4; plan m3j step 8): an apk installs the file a link leads to, on a
// local device with adb here and on a lent one by the build's own way, the upload to the
// host and device.installUpload; the result is the file's name, with no project opened and
// no build resolved, and it passes the guards a build's install does (the policy's device
// admission). The lent device gets the bytes of the file the link leads to, the digest the
// host reports being checked as for a build.
func TestInstallApkLocalAndLent(t *testing.T) {
	tr := newAPKTree(t)
	link := tr.link(t, "latest.apk", tr.good)
	sum := sha256.Sum256(apkBytes)
	digest := hex.EncodeToString(sum[:])

	t.Run("local", func(t *testing.T) {
		res := &stubResolver{}
		f := newInstallFixture(t, res, map[string]string{uiSerial + ".install.out": "Performing Streamed Install\nSuccess\n"})
		enter := &enterCounter{}
		f.ops.UseWorkspaces(enter)
		out, ce := runInstall(t, f.ops, `{"device":"emulator-5554","apk":"`+link+`"}`)
		if ce != nil {
			t.Fatalf("%+v", ce)
		}
		r := mustInstall(t, out)
		if r.APK != "latest.apk" || r.ApplicationID != "" || r.VersionCode != nil {
			t.Errorf("result = %+v; want the file's name and nothing else", r)
		}
		if n := count(f.calls(t), "-s emulator-5554 install -r "+tr.good); n != 1 {
			t.Errorf("install ran %d times with the file the link leads to; calls: %v", n, f.calls(t))
		}
		if res.calls != 0 || enter.entered() != 0 {
			t.Errorf("an apk install resolved %d builds and opened %d projects; want none", res.calls, enter.entered())
		}
		// A build's call, beside it, does both: the counter sees what it would not.
		build := &stubResolver{}
		fb := newInstallFixture(t, build, map[string]string{uiSerial + ".install.out": "Success\n"})
		eb := &enterCounter{}
		fb.ops.UseWorkspaces(eb)
		if _, ce := runInstall(t, fb.ops, `{"project":"`+installWS+`","device":"emulator-5554","build":{"module":":app","variant":"debug"}}`); ce != nil {
			t.Fatalf("a build install: %+v", ce)
		}
		if eb.entered() != 1 {
			t.Errorf("a build install opened %d projects; want 1", eb.entered())
		}
	})

	// The device guards a build's install passes are the apk's too: a physical device the
	// policy does not admit is refused, with nothing installed.
	t.Run("a physical device is refused as for a build", func(t *testing.T) {
		f := newInstallFixture(t, &stubResolver{}, map[string]string{
			"devices.out": uiList("R58M12ABCDE device usb:1-1.2 product:raven model:Pixel_6_Pro transport_id:7\n"),
		})
		out, ce := runInstall(t, f.ops, `{"device":"R58M12ABCDE","apk":"`+tr.good+`"}`)
		if ce == nil || ce.Code != ErrNotAllowed {
			t.Fatalf("outcome %s, error %+v; want %s", out.State, ce, ErrNotAllowed)
		}
		for _, c := range f.calls(t) {
			if strings.Contains(c, "install") {
				t.Errorf("an install ran for a refused device: %v", f.calls(t))
			}
		}
	})

	t.Run("lent", func(t *testing.T) {
		h := &scriptedUploadHost{}
		f := newRemoteInstallFixture(t, h)
		out, ce := runInstall(t, f.ops, `{"device":"qa/R58M001","apk":"`+link+`"}`)
		if ce != nil {
			t.Fatalf("%+v", ce)
		}
		r := mustInstall(t, out)
		if r.APK != "latest.apk" || r.ApplicationID != "" {
			t.Errorf("result = %+v; want the file's name and no applicationId", r)
		}
		if !out.Untrusted {
			t.Error("a call that reached another machine is not marked untrusted")
		}
		h.mu.Lock()
		got := h.got
		h.mu.Unlock()
		if got != digest {
			t.Errorf("the host received a file of SHA-256 %q; want the file the link leads to, %q", got, digest)
		}
		if calls := h.asked(); len(calls) != 1 || calls[0] != "device.installUpload" {
			t.Errorf("the host was asked %v; want one device.installUpload", calls)
		}
	})
}
