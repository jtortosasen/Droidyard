package sdk

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Tests of what sdk.install does once it has a plan (plan m3f step 4; ADR-0130 §1, §2):
// the download, the checks on the archive, the unpack, the move into place. "Nothing
// reaches a final path unverified or half-written" is what they hold.

const ptZip = "platform-tools_r37_0_1.zip"

// newSimple is a fixture whose repository has platform-tools (and the usual others) and
// whose licence is accepted.
func newSimple(t *testing.T, tweak ...func(*installer)) *instFixture {
	t.Helper()
	f := newInst(t, stdRepo(t).build(), filepath.Join(t.TempDir(), "sdk"), tweak...)
	f.accept()
	return f
}

// cleanAfterRefusal asserts a refused install left the SDK root as it found it: no package
// at the final path, no temporary entry, and nothing beside the root.
func cleanAfterRefusal(t *testing.T, f *instFixture, finalRel string) {
	t.Helper()
	if present(t, filepath.Join(f.root, finalRel)) {
		t.Errorf("something is at the final path %s", finalRel)
	}
	if left := tempLeftovers(t, f.root); len(left) != 0 {
		t.Errorf("temporary entries left: %v", left)
	}
	entries, _ := os.ReadDir(f.root)
	for _, e := range entries {
		if e.Name() != "licenses" && e.Name() != ".temp" {
			t.Errorf("%s was written into the root", e.Name())
		}
	}
	if siblings, _ := os.ReadDir(filepath.Dir(f.root)); len(siblings) != 1 {
		t.Errorf("the root's parent holds %d entries, want only the root", len(siblings))
	}
}

// TestInstallChecksum: an archive whose sha1 is not the declared one, one that is longer or
// shorter than declared, one with a checksum type other than sha1 and one the server does
// not have are all SDK_DOWNLOAD_FAILED with nothing at the final path and no temporary.
// Seen failing with the sha1 comparison removed.
func TestInstallChecksum(t *testing.T) {
	t.Run("a different body", func(t *testing.T) {
		f := newSimple(t)
		body := []byte(f.srv.files[ptZip])
		body[len(body)/2] ^= 0xff
		f.srv.set(ptZip, string(body))
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrDownloadFailed)
		if !strings.Contains(out.Error.Cause, "sha1") {
			t.Errorf("cause = %q, want it to say the sha1 differs", out.Error.Cause)
		}
		cleanAfterRefusal(t, f, "platform-tools")
	})
	t.Run("a checksum type that is not sha1", func(t *testing.T) {
		b := stdRepo(t)
		b.pkgs[0].Archives = strings.Replace(b.pkgs[0].Archives, `type="sha1"`, `type="md5"`, 1)
		f := newInst(t, b.build(), filepath.Join(t.TempDir(), "sdk"))
		f.accept()
		wantCode(t, f.installAs("platform-tools"), ErrDownloadFailed)
		if got := f.zipRequests(); len(got) != 0 {
			t.Errorf("archives requested for a checksum nobody checks: %v", got)
		}
	})
	t.Run("not found", func(t *testing.T) {
		f := newSimple(t)
		f.srv.status[ptZip] = http.StatusNotFound
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrDownloadFailed)
		if !strings.Contains(out.Error.Cause, "404") {
			t.Errorf("cause = %q", out.Error.Cause)
		}
		cleanAfterRefusal(t, f, "platform-tools")
	})
}

// TestInstallOversize: the body is stopped past the declared size, whether the server
// announces its length or streams without one, and a body shorter than declared is refused
// too.
func TestInstallOversize(t *testing.T) {
	data := pkgZip(t, "platform-tools")
	declare := func(size int) *repoBuilder {
		b := newRepoBuilder()
		b.add("platform-tools", "37.0.1", data)
		sum := sha1.Sum(data[:min(size, len(data))])
		b.pkgs[0].Archives = fmt.Sprintf(`<archive><complete><size>%d</size><checksum type="sha1">%x</checksum><url>%s</url></complete></archive>`, size, sum, ptZip)
		return b
	}
	t.Run("announced", func(t *testing.T) {
		f := newInst(t, declare(len(data)-10).build(), filepath.Join(t.TempDir(), "sdk"))
		f.accept()
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrDownloadFailed)
		if !strings.Contains(out.Error.Cause, "more than") {
			t.Errorf("cause = %q", out.Error.Cause)
		}
		cleanAfterRefusal(t, f, "platform-tools")
	})
	t.Run("streamed", func(t *testing.T) {
		f := newInst(t, declare(len(data)-10).build(), filepath.Join(t.TempDir(), "sdk"))
		f.accept()
		f.srv.custom[ptZip] = func(w http.ResponseWriter, r *http.Request) {
			w.Write(data[:5])
			w.(http.Flusher).Flush() // no Content-Length from here on
			w.Write(data[5:])
		}
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrDownloadFailed)
		if !strings.Contains(out.Error.Cause, "longer") {
			t.Errorf("cause = %q", out.Error.Cause)
		}
		cleanAfterRefusal(t, f, "platform-tools")
	})
	t.Run("shorter", func(t *testing.T) {
		f := newInst(t, declare(len(data)+10).build(), filepath.Join(t.TempDir(), "sdk"))
		f.accept()
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrDownloadFailed)
		cleanAfterRefusal(t, f, "platform-tools")
	})
}

// TestInstallArchiveRedirect: an archive answered with a redirect to another host is not
// followed (the repository's client allows none off its host): SDK_DOWNLOAD_FAILED, and the
// other host saw no request.
func TestInstallArchiveRedirect(t *testing.T) {
	f := newSimple(t)
	other := newFxServer(t, map[string]string{"x.zip": "bytes"})
	f.srv.redirect[ptZip] = other.URL + "/android/repository/x.zip"
	out := f.installAs("platform-tools")
	wantCode(t, out, ErrDownloadFailed)
	if !strings.Contains(out.Error.Cause, "another host") {
		t.Errorf("cause = %q", out.Error.Cause)
	}
	if got := other.requests(); len(got) != 0 {
		t.Errorf("the other host was asked for %v", got)
	}
	cleanAfterRefusal(t, f, "platform-tools")
}

// TestInstallFinalPathInsideRoot: an id that reaches the installer without the parameter
// check (a model built by hand, the seam standing for a repository entry that slipped past
// the pattern) and climbs out of the root is SDK_NOT_WRITABLE, and nothing is written
// outside the root. Seen failing with the IsLocal check removed.
func TestInstallFinalPathInsideRoot(t *testing.T) {
	f := newSimple(t)
	data := pkgZip(t, "x")
	f.srv.set("evil.zip", string(data))
	const evil = "a;..;..;x"
	ref := LicenceRef{ID: "android-sdk-license", SHA1: fxHash}
	sum := sha1.Sum(data)
	rp := &RemotePackage{
		ID: evil, Revision: Revision{Major: 1, Parts: 1}, DisplayName: "evil", License: ref,
		Archive:     Archive{URL: f.srv.base() + "evil.zip", Size: int64(len(data)), Checksum: fmt.Sprintf("%x", sum), ChecksumType: "sha1"},
		TypeDetails: TypeDetails{Namespace: "http://schemas.android.com/repository/android/generic/02", Local: "genericDetailsType"},
	}
	f.in.repo.model = &Model{
		Packages: []*RemotePackage{rp},
		Licences: map[LicenceRef]Licence{ref: {ID: ref.ID, SHA1: ref.SHA1, Text: fxLicenceText}},
		byID:     map[string]*RemotePackage{evil: rp},
	}
	f.in.repo.fetchedAt = time.Now()

	_, err := f.in.run(context.Background(), &capability.Call{Progress: func(int, int, string) {}}, []string{evil})
	wantErr(t, err, ErrNotWritable)
	outside := filepath.Join(filepath.Dir(f.root), "x")
	if present(t, outside) {
		t.Error("a package was written outside the SDK root")
	}
	cleanAfterRefusal(t, f, "a")
	if got := f.zipRequests(); len(got) != 0 {
		t.Errorf("archives requested for an id outside the root: %v", got)
	}
}

// updateFixture is a fixture with platform-tools 36.0.0 installed (and old.txt in it).
func updateFixture(t *testing.T, tweak ...func(*installer)) *instFixture {
	f := newSimple(t, tweak...)
	writePackage(t, filepath.Join(f.root, "platform-tools", "package.xml"), "platform-tools", "36", "0", "0")
	if err := os.WriteFile(filepath.Join(f.root, "platform-tools", "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestInstallRenameRollback: when the rename that puts the new package in place fails after
// the old one was moved aside, the old directory is put back at the final path and no
// temporary is left. Seen failing with the rollback removed.
func TestInstallRenameRollback(t *testing.T) {
	f := updateFixture(t, func(in *installer) {
		in.rename = func(oldpath, newpath string) error {
			if strings.Contains(oldpath, "unpacked") {
				return errors.New("the disk said no")
			}
			return os.Rename(oldpath, newpath)
		}
	})
	out := f.installAs("platform-tools")
	wantCode(t, out, ErrNotWritable)
	if !present(t, filepath.Join(f.root, "platform-tools", "old.txt")) {
		t.Error("the old package was not put back")
	}
	if got := installedVersion(t, filepath.Join(f.root, "platform-tools")); got != "36.0.0" {
		t.Errorf("the package is at %q after a failed update", got)
	}
	if left := tempLeftovers(t, f.root); len(left) != 0 {
		t.Errorf("temporary entries left: %v", left)
	}
}

// TestInstallCrossDevice: a rename that crosses file systems (a parent on another volume, a
// mount point inside the root) is SDK_NOT_WRITABLE naming the entry, with nothing moved.
func TestInstallCrossDevice(t *testing.T) {
	exdev := func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	t.Run("a fresh install", func(t *testing.T) {
		f := newSimple(t, func(in *installer) { in.rename = exdev })
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrNotWritable)
		if !strings.Contains(out.Error.Cause, "platform-tools") || !strings.Contains(out.Error.Cause, "another file system") || strings.Contains(out.Error.Cause, f.root) {
			t.Errorf("cause = %q, want the entry relative to the root and the volume said", out.Error.Cause)
		}
		cleanAfterRefusal(t, f, "platform-tools")
	})
	t.Run("an update", func(t *testing.T) {
		f := updateFixture(t, func(in *installer) { in.rename = exdev })
		wantCode(t, f.installAs("platform-tools"), ErrNotWritable)
		if !present(t, filepath.Join(f.root, "platform-tools", "old.txt")) || installedVersion(t, filepath.Join(f.root, "platform-tools")) != "36.0.0" {
			t.Error("the old package was touched")
		}
		if left := tempLeftovers(t, f.root); len(left) != 0 {
			t.Errorf("temporary entries left: %v", left)
		}
	})
}

// patchDeclaredSize rewrites the uncompressed size of the last central-directory header of
// a zip to n: an entry that says it is shorter than it is.
func patchDeclaredSize(t *testing.T, z []byte, n uint32) []byte {
	t.Helper()
	out := append([]byte(nil), z...)
	for i := len(out) - 28; i >= 0; i-- {
		if out[i] == 'P' && out[i+1] == 'K' && out[i+2] == 1 && out[i+3] == 2 {
			binary.LittleEndian.PutUint32(out[i+24:], n)
			return out
		}
	}
	t.Fatal("no central directory header")
	return nil
}

// TestInstallUnzipRefusals: every way an archive can be wrong in its central directory or
// its entries is refused as SDK_DOWNLOAD_FAILED naming the entry, with the call marked
// untrusted (the name is archive text), nothing written outside the temporary directory, and
// nothing at the final path. The entry limits are lowered by a seam. Seen failing with the
// name check removed.
func TestInstallUnzipRefusals(t *testing.T) {
	top := zipEnt{name: "platform-tools/", mode: 0o755}
	file := zipEnt{name: "platform-tools/adb", data: "adb", mode: 0o755}
	cases := []struct {
		name  string
		zip   func(t *testing.T) []byte
		entry string // what the cause must name; "" for an archive-level refusal
		tweak func(*installer)
	}{
		{"a parent-relative name", func(t *testing.T) []byte { return mkZip(t, top, file, zipEnt{name: "../x", data: "x"}) }, "../x", nil},
		{"a name that climbs out under the right top", func(t *testing.T) []byte {
			return mkZip(t, top, file, zipEnt{name: "platform-tools/../../../../x", data: "x"})
		}, "../../../../x", nil},
		{"an absolute name", func(t *testing.T) []byte { return mkZip(t, top, file, zipEnt{name: "/abs/x", data: "x"}) }, "/abs/x", nil},
		{"a backslash name", func(t *testing.T) []byte { return mkZip(t, top, file, zipEnt{name: `platform-tools\evil`, data: "x"}) }, `platform-tools\\evil`, nil},
		{"a symlink entry", func(t *testing.T) []byte {
			return mkZip(t, top, file, zipEnt{name: "platform-tools/link", data: "/etc/passwd", mode: fs.ModeSymlink | 0o777})
		}, "platform-tools/link", nil},
		{"a device entry", func(t *testing.T) []byte {
			return mkZip(t, top, file, zipEnt{name: "platform-tools/dev", mode: fs.ModeDevice | fs.ModeCharDevice | 0o644})
		}, "platform-tools/dev", nil},
		{"a named pipe entry", func(t *testing.T) []byte {
			return mkZip(t, top, file, zipEnt{name: "platform-tools/fifo", mode: fs.ModeNamedPipe | 0o644})
		}, "platform-tools/fifo", nil},
		{"two top directories", func(t *testing.T) []byte {
			return mkZip(t, top, file, zipEnt{name: "other/", mode: 0o755}, zipEnt{name: "other/y", data: "y"})
		}, "other/", nil},
		{"a file at the top", func(t *testing.T) []byte { return mkZip(t, top, file, zipEnt{name: "stray", data: "x"}) }, "stray", nil},
		{"only a file", func(t *testing.T) []byte { return mkZip(t, zipEnt{name: "stray", data: "x"}) }, "stray", nil},
		{"a name written twice", func(t *testing.T) []byte { return mkZip(t, top, file, file) }, "platform-tools/adb", nil},
		{"an entry longer than declared", func(t *testing.T) []byte {
			return patchDeclaredSize(t, mkZip(t, top, zipEnt{name: "platform-tools/adb", data: "0123456789"}), 4)
		}, "platform-tools/adb", nil},
		{"no entries", func(t *testing.T) []byte { return mkZip(t) }, "", nil},
		{"not a zip", func(t *testing.T) []byte { return []byte("this is not a zip archive at all") }, "", nil},
		{"one entry too many", func(t *testing.T) []byte {
			return mkZip(t, top, file, zipEnt{name: "platform-tools/a", data: "a"}, zipEnt{name: "platform-tools/b", data: "b"}, zipEnt{name: "platform-tools/c", data: "c"})
		}, "", func(in *installer) { in.maxEntries = 4 }},
		{"an uncompressed total too large", func(t *testing.T) []byte {
			return mkZip(t, top, zipEnt{name: "platform-tools/big", data: strings.Repeat("z", 500)})
		}, "", func(in *installer) { in.maxUncompressed = 100 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newRepoBuilder().add("platform-tools", "37.0.1", c.zip(t))
			var tw []func(*installer)
			if c.tweak != nil {
				tw = append(tw, c.tweak)
			}
			f := newInst(t, b.build(), filepath.Join(t.TempDir(), "sdk"), tw...)
			f.accept()
			_, call, err := f.direct("platform-tools")
			ce := wantErr(t, err, ErrDownloadFailed)
			if c.entry != "" {
				if !strings.Contains(ce.Cause, c.entry) {
					t.Errorf("cause = %q, want it to name %q", ce.Cause, c.entry)
				}
				if call.Untrusted == nil || !*call.Untrusted {
					t.Error("an error naming archive text is not marked untrusted")
				}
			}
			cleanAfterRefusal(t, f, "platform-tools")
		})
	}
}

// TestInstallLayout: the archive's top directory is not the id path (build-tools;36.1.0
// unpacks as android-16/) and lands at build-tools/36.1.0; modes are 0755 when any execute
// bit is set and 0644 otherwise, directories 0755, and a DOS-made entry or a Unix one whose
// mode field is 0 (the system-image zips) is a plain 0644 file.
func TestInstallLayout(t *testing.T) {
	z := mkZip(t,
		zipEnt{name: "android-16/", mode: 0o700},
		zipEnt{name: "android-16/aapt", data: "bin", mode: 0o755},
		zipEnt{name: "android-16/exec750", data: "bin", mode: 0o750},
		zipEnt{name: "android-16/private", data: "p", mode: 0o600},
		zipEnt{name: "android-16/lib/", mode: 0o700},
		zipEnt{name: "android-16/lib/x.jar", data: "jar", mode: 0o644},
		zipEnt{name: "android-16/dos.txt", data: "d", dos: true},
		zipEnt{name: "android-16/unix0.txt", data: "u", unix0: true},
		zipEnt{name: "android-16/dosdir/", dos: true},
		zipEnt{name: "android-16/dosdir/f", data: "f", dos: true},
	)
	b := newRepoBuilder().add("build-tools;36.1.0", "36.1.0", z)
	f := newInst(t, b.build(), filepath.Join(t.TempDir(), "sdk"))
	f.accept()
	wantInstalled(t, f.installAs("build-tools;36.1.0"))

	dir := filepath.Join(f.root, "build-tools", "36.1.0")
	if present(t, filepath.Join(f.root, "android-16")) || present(t, filepath.Join(f.root, "build-tools", "android-16")) {
		t.Error("the archive's own top directory name was used")
	}
	for path, want := range map[string]fs.FileMode{
		"aapt": 0o755, "exec750": 0o755, "private": 0o644, "lib/x.jar": 0o644, "dos.txt": 0o644, "unix0.txt": 0o644, "dosdir/f": 0o644, "package.xml": 0o644,
		".": 0o755, "lib": 0o755, "dosdir": 0o755, "../../build-tools": 0o755,
	} {
		if got := modeOf(t, filepath.Join(dir, path)); got != want {
			t.Errorf("%s: mode %v, want %v", path, got, want)
		}
	}
	if got := installedVersion(t, dir); got != "36.1.0" {
		t.Errorf("revision = %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "lib", "x.jar")); string(b) != "jar" {
		t.Errorf("file content = %q", b)
	}
}

// slowBody is a handler that sends the first half of data and then waits for the client to
// go away; started is closed once the half is sent.
func slowBody(data []byte, started chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Write(data[:len(data)/2])
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}
}

// TestInstallCancelled: a cancel while an archive downloads ends the operation cancelled,
// removes the temporary, leaves nothing at that package's path and does not list it, while
// the earlier package of the closure stays installed and is logged.
func TestInstallCancelled(t *testing.T) {
	f := newSimple(t)
	started := make(chan struct{})
	f.srv.custom["build-tools_36_1_0_r36_1_0.zip"] = slowBody([]byte(f.srv.files["build-tools_36_1_0_r36_1_0.zip"]), started)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := f.ops.Start(ctx, capability.Request{Capability: "sdk.install", Params: []byte(`{"packages":["platform-tools","build-tools;36.1.0"]}`),
		Client: capability.ClientMCP, Origin: capability.Origin{ID: "mcp-1", Client: capability.ClientMCP}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("the slow download never started")
	}
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(ctx, id, time.Now().Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != capability.StateCancelled {
		t.Fatalf("outcome = %s %+v, want cancelled", out.State, out.Error)
	}
	cleanAfterRefusal2(t, f)
	if !present(t, filepath.Join(f.root, "platform-tools", "package.xml")) {
		t.Error("the closure's earlier package was lost")
	}
	if present(t, filepath.Join(f.root, "build-tools")) {
		t.Error("the cancelled package reached its path")
	}
	pk := f.call(capability.ClientMCP, "sdk.packages", `{}`).Result.(PackagesResult)
	if got := installedIDs(pk); len(got) != 1 || got[0] != "platform-tools" {
		t.Errorf("sdk.packages lists %v as installed, want only platform-tools", got)
	}
	if got := f.log.String(); !strings.Contains(got, "already in place: platform-tools") {
		t.Errorf("daemon.log = %q, want the package already in place named", got)
	}
}

// cleanAfterRefusal2 is cleanAfterRefusal's temporary check alone, for a root that holds a
// package.
func cleanAfterRefusal2(t *testing.T, f *instFixture) {
	t.Helper()
	if left := tempLeftovers(t, f.root); len(left) != 0 {
		t.Errorf("temporary entries left: %v", left)
	}
}

var errKilled = errors.New("the daemon was killed")

// TestInstallKilledBetweenRenames: a kill after the old directory moved aside and before
// the new one is in place leaves the package absent, never half-written (ADR-0130 §1): not
// at its path, not listed; the old copy stays in the temporary directory for the sweep; and
// the next install installs the package.
func TestInstallKilledBetweenRenames(t *testing.T) {
	f := updateFixture(t, func(in *installer) { in.afterAside = func() error { return errKilled } })
	if _, _, err := f.direct("platform-tools"); err == nil {
		t.Fatal("the simulated kill did not stop the call")
	}
	if present(t, filepath.Join(f.root, "platform-tools")) {
		t.Fatal("something is at the final path after a kill between the renames")
	}
	pk := f.call(capability.ClientMCP, "sdk.packages", `{}`).Result.(PackagesResult)
	if got := installedIDs(pk); len(got) != 0 {
		t.Errorf("sdk.packages lists %v after the kill", got)
	}
	left := tempLeftovers(t, f.root)
	if len(left) != 1 || !present(t, filepath.Join(f.root, ".temp", left[0], "replaced", "old.txt")) {
		t.Errorf("temporary entries = %v, want the one holding the old copy", left)
	}

	f.in.afterAside = nil
	res := wantInstalled(t, f.installAs("platform-tools"))
	if got := idsOf(res); len(got) != 1 || got[0] != "platform-tools" {
		t.Errorf("the next install did %+v", res)
	}
	if got := installedVersion(t, filepath.Join(f.root, "platform-tools")); got != "37.0.1" {
		t.Errorf("revision = %q", got)
	}
}

// TestInstallSweepsOldTemporaries: a call removes the droidyard-* entries of .temp over an
// hour old and keeps a newer one and any entry that is not Droidyard's (the classic tool's).
func TestInstallSweepsOldTemporaries(t *testing.T) {
	f := newSimple(t)
	tmp := filepath.Join(f.root, ".temp")
	old := time.Now().Add(-2 * time.Hour)
	for _, c := range []struct {
		name string
		age  time.Time
	}{
		{"droidyard-old", old}, {"droidyard-new", time.Now().Add(-10 * time.Minute)}, {"foreign-old", old},
		{"droidyard-rollback/replaced", old}, {"droidyard-rollback", old},
	} {
		dir := filepath.Join(tmp, c.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
		if err := os.Chtimes(dir, c.age, c.age); err != nil {
			t.Fatal(err)
		}
	}
	wantInstalled(t, f.installAs("platform-tools"))
	if present(t, filepath.Join(tmp, "droidyard-old")) {
		t.Error("an hour-old droidyard-* entry was kept")
	}
	if !present(t, filepath.Join(tmp, "droidyard-new")) || !present(t, filepath.Join(tmp, "foreign-old")) {
		t.Error("a newer droidyard-* entry or a foreign one was removed")
	}
	// The old package of a failed rollback stays where the error said it is.
	if !present(t, filepath.Join(tmp, "droidyard-rollback", "replaced", "f")) {
		t.Error("the old package an update could not put back was swept")
	}
}

// TestInstallTempSymlinkRefused: a .temp that is a symbolic link is never written through:
// SDK_NOT_WRITABLE, the link's target untouched, nothing downloaded.
func TestInstallTempSymlinkRefused(t *testing.T) {
	f := newSimple(t)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(f.root, ".temp")); err != nil {
		t.Fatal(err)
	}
	out := f.installAs("platform-tools")
	wantCode(t, out, ErrNotWritable)
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("the link's target holds %d entries", len(entries))
	}
	if got := f.zipRequests(); len(got) != 0 {
		t.Errorf("archives requested: %v", got)
	}
	if present(t, filepath.Join(f.root, "platform-tools")) {
		t.Error("a package was installed")
	}
}

// TestInstallBounds: a server that stalls past the silent bound, and one that trickles past
// the call's bound, end the install as SDK_DOWNLOAD_FAILED saying which, with no temporary.
// The bounds are lowered by a seam.
func TestInstallBounds(t *testing.T) {
	t.Run("silent", func(t *testing.T) {
		f := newSimple(t, func(in *installer) { in.silentBound = 150 * time.Millisecond })
		f.srv.custom[ptZip] = func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("PK"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrDownloadFailed)
		if !strings.Contains(out.Error.Cause, "no data") {
			t.Errorf("cause = %q", out.Error.Cause)
		}
		cleanAfterRefusal(t, f, "platform-tools")
	})
	t.Run("the whole call", func(t *testing.T) {
		f := newSimple(t, func(in *installer) { in.callBound = 250 * time.Millisecond })
		f.srv.custom[ptZip] = func(w http.ResponseWriter, r *http.Request) {
			for {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
					w.Write([]byte("x"))
					w.(http.Flusher).Flush()
				}
			}
		}
		out := f.installAs("platform-tools")
		wantCode(t, out, ErrDownloadFailed)
		if !strings.Contains(out.Error.Cause, "bound") {
			t.Errorf("cause = %q", out.Error.Cause)
		}
		cleanAfterRefusal(t, f, "platform-tools")
	})
}

// TestInstallProgress: progress is bytes downloaded of the closure's archives and the
// package's place in it, never going back, and ends at the total.
func TestInstallProgress(t *testing.T) {
	f := newSimple(t)
	type rec struct {
		done, total int
		msg         string
	}
	var got []rec
	call := &capability.Call{Progress: func(d, tot int, m string) { got = append(got, rec{d, tot, m}) }}
	if _, err := f.in.install(context.Background(), call, []string{"platform-tools", "emulator"}); err != nil {
		t.Fatal(err)
	}
	want := len(f.srv.files[ptZip]) + len(f.srv.files["emulator_r37_2_12.zip"])
	var first, second bool
	prev := 0
	for _, r := range got {
		if r.total != want || r.done < prev || r.done > want {
			t.Fatalf("progress %+v after %d, want a total of %d that never goes back", r, prev, want)
		}
		prev = r.done
		first = first || strings.Contains(r.msg, "platform-tools (1 of 2)")
		second = second || strings.Contains(r.msg, "emulator (2 of 2)")
	}
	if !first || !second || prev != want {
		t.Errorf("first=%v second=%v last done=%d of %d: %+v", first, second, prev, want, got)
	}
}

// TestInstallsSerialised: two installs on one root run one at a time, through the
// scheduler's key (sdk:<root>); with the key removed they overlap. Seen failing with the
// descriptor's Resource removed.
func TestInstallsSerialised(t *testing.T) {
	f := newSimple(t)
	var inflight, peak atomic.Int32
	var mu sync.Mutex
	slow := func(name string) http.HandlerFunc {
		data := f.srv.files[name]
		return func(w http.ResponseWriter, r *http.Request) {
			n := inflight.Add(1)
			mu.Lock()
			peak.Store(max(peak.Load(), n))
			mu.Unlock()
			time.Sleep(200 * time.Millisecond)
			w.Write([]byte(data))
			inflight.Add(-1)
		}
	}
	f.srv.custom[ptZip] = slow(ptZip)
	f.srv.custom["emulator_r37_2_12.zip"] = slow("emulator_r37_2_12.zip")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var ids []string
	for _, pkg := range []string{"platform-tools", "emulator"} {
		id, err := f.ops.Start(ctx, capability.Request{Capability: "sdk.install", Params: []byte(`{"packages":["` + pkg + `"]}`),
			Client: capability.ClientMCP, Origin: capability.Origin{ID: "mcp-" + pkg, Client: capability.ClientMCP}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		out, err := f.ops.Wait(ctx, id, time.Now().Add(25*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		wantInstalled(t, out)
	}
	if p := peak.Load(); p != 1 {
		t.Errorf("%d archives were in flight at once, want the installs one at a time", p)
	}
}
