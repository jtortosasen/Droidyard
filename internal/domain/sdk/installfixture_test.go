package sdk

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The installer's fixtures (plan m3f step 4): zips built with archive/zip, a repository
// that lists them with their real sizes and sha1, an httptest TLS server serving both, and
// an SDK root in t.TempDir(). No Google file and no Google licence text is used.

// zipEnt is one entry of a zip the tests build.
type zipEnt struct {
	name string
	data string
	// mode is the Unix mode the entry carries; 0 means 0644, and fs.ModeDir a directory.
	mode fs.FileMode
	// dos makes the entry as a DOS-made archive does (the system-image zips): no Unix
	// mode at all. unix0 is a Unix-made entry whose mode field is 0.
	dos, unix0 bool
}

func mkZip(t testing.TB, ents ...zipEnt) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range ents {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch {
		case e.dos:
		case e.unix0:
			h.CreatorVersion = 3 << 8
		default:
			mode := e.mode
			if mode == 0 {
				mode = 0o644
			}
			if strings.HasSuffix(e.name, "/") {
				mode |= fs.ModeDir
			}
			h.SetMode(mode)
		}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pkgZip is a plausible package archive: one top directory with a source.properties, an
// executable and a library.
func pkgZip(t testing.TB, top string) []byte {
	return mkZip(t,
		zipEnt{name: top + "/", mode: fs.ModeDir | 0o755},
		zipEnt{name: top + "/source.properties", data: "Pkg.Revision=1\n"},
		zipEnt{name: top + "/bin/tool", data: "#!/bin/sh\n", mode: 0o755},
		zipEnt{name: top + "/lib/x.jar", data: "jar"},
	)
}

// realArchive is an archive element whose size and sha1 are those of data.
func realArchive(osName, arch string, data []byte, url string) string {
	sum := sha1.Sum(data)
	s := fmt.Sprintf(`<archive><complete><size>%d</size><checksum type="sha1">%x</checksum><url>%s</url></complete>`, len(data), sum, url)
	if osName != "" {
		s += `<host-os>` + osName + `</host-os>`
	}
	if arch != "" {
		s += `<host-arch>` + arch + `</host-arch>`
	}
	return s + `</archive>`
}

// depsXML is a dependencies element from "id" or "id:min" strings ("emulator:31.1.1").
func depsXML(deps ...string) string {
	if len(deps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<dependencies>")
	for _, d := range deps {
		id, min, has := strings.Cut(d, ":")
		b.WriteString(`<dependency path="` + id + `">`)
		if has {
			b.WriteString("<min-revision>")
			for i, part := range strings.Split(min, ".") {
				b.WriteString("<" + []string{"major", "minor", "micro"}[i] + ">" + part + "</" + []string{"major", "minor", "micro"}[i] + ">")
			}
			b.WriteString("</min-revision>")
		}
		b.WriteString("</dependency>")
	}
	return b.String() + "</dependencies>"
}

// repoBuilder makes a repository whose packages have real archives.
type repoBuilder struct {
	files map[string]string
	pkgs  []fxPkg
	imgs  []fxPkg
}

func newRepoBuilder() *repoBuilder { return &repoBuilder{files: map[string]string{}} }

// add lists a package of the main list with data as its archive and returns the archive's
// file name.
func (b *repoBuilder) add(id, rev string, data []byte, deps ...string) *repoBuilder {
	return b.addWith(fxPkg{Path: id, Rev: rev, Deps: depsXML(deps...), Display: "Synthetic " + id}, data)
}

func (b *repoBuilder) addWith(p fxPkg, data []byte) *repoBuilder {
	name := archiveName(p.Path, p.Rev)
	p.Archives = realArchive("macosx", "aarch64", data, name)
	b.files[name] = string(data)
	b.pkgs = append(b.pkgs, p)
	return b
}

// addImage lists a system image of the host's ABI.
func (b *repoBuilder) addImage(api, tag, abi string, data []byte, deps ...string) *repoBuilder {
	p := fxImage(api, tag, abi, "")
	p.Deps = depsXML(deps...)
	name := archiveName(p.Path, p.Rev)
	p.Archives = realArchive("", "", data, name)
	b.files["sys-img/android/"+name] = string(data)
	b.imgs = append(b.imgs, p)
	return b
}

func archiveName(id, rev string) string {
	return strings.NewReplacer(";", "_", ".", "_").Replace(id) + "_r" + strings.ReplaceAll(rev, ".", "_") + ".zip"
}

func (b *repoBuilder) build() map[string]string {
	b.files["repository2-3.xml"] = fxRepo(fxLicenceText, b.pkgs...)
	b.files["addons_list-5.xml"] = fxSiteList("sys-img/android/sys-img2-3.xml")
	b.files["sys-img/android/sys-img2-3.xml"] = fxImgList(fxLicenceText, b.imgs...)
	return b.files
}

// instFixture is the sdk capabilities over a builder's repository and an SDK root.
type instFixture struct {
	t    *testing.T
	srv  *fxServer
	root string
	env  []string
	reg  *capability.Registry
	ops  *capability.Operations
	in   *installer
	log  *syncBuf

	mu    sync.Mutex
	inUse func(ids []string) map[string][]string
	// inUseCalls records the replace sets the seam was asked about.
	inUseCalls [][]string
}

// newInst builds the fixture over files with ANDROID_HOME at root (which may not exist).
func newInst(t *testing.T, files map[string]string, root string, tweak ...func(*installer)) *instFixture {
	t.Helper()
	return newInstEnv(t, files, root, []string{"ANDROID_HOME=" + root}, tweak...)
}

// newInstEnv is newInst with the daemon's environment given; root is only what the
// fixture's helpers write licences into.
func newInstEnv(t *testing.T, files map[string]string, root string, env []string, tweak ...func(*installer)) *instFixture {
	t.Helper()
	srv := newFxServer(t, files)
	f := &instFixture{t: t, srv: srv, root: root, env: env, log: &syncBuf{}}
	f.inUse = func([]string) map[string][]string { return nil }
	reg := capability.NewRegistry()
	Register(reg, Options{
		Env: f.env, RepositoryBase: srv.base(), HTTPClient: srv.Client(), GOOS: "darwin", GOARCH: "arm64", Log: f.log,
		InUse: func(_ context.Context, _ string, ids []string) map[string][]string {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.inUseCalls = append(f.inUseCalls, append([]string(nil), ids...))
			return f.inUse(ids)
		},
		tweak: func(in *installer) {
			f.in = in
			for _, tw := range tweak {
				tw(in)
			}
		},
	})
	f.reg = reg
	f.ops = capability.NewOperations(reg)
	return f
}

// accept writes the fixture licence as accepted.
func (f *instFixture) accept() {
	f.t.Helper()
	acceptLicence(f.t, f.root, "android-sdk-license", fxLicenceText)
}

// zipRequests are the archive requests the server has seen.
func (f *instFixture) zipRequests() []string {
	var out []string
	for _, r := range f.srv.requests() {
		if strings.HasSuffix(r, ".zip") {
			out = append(out, r)
		}
	}
	return out
}

// call runs a capability through the scheduler as the given client and waits.
func (f *instFixture) call(client capability.ClientKind, name, params string) capability.Outcome {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	id, err := f.ops.Start(ctx, capability.Request{Capability: name, Params: []byte(params), Client: client,
		Origin: capability.Origin{ID: string(client) + "-1", Client: client}})
	if err != nil {
		return capability.Outcome{Capability: name, State: capability.StateFailed, Error: asCapabilityError(err)}
	}
	out, err := f.ops.Wait(ctx, id, time.Now().Add(30*time.Second))
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// installAs runs sdk.install through the scheduler as an agent.
func (f *instFixture) installAs(ids ...string) capability.Outcome {
	f.t.Helper()
	return f.call(capability.ClientMCP, "sdk.install", fmt.Sprintf(`{"packages":%s}`, jsonList(ids)))
}

func jsonList(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = fmt.Sprintf("%q", id)
	}
	return "[" + strings.Join(q, ",") + "]"
}

// direct runs the installer without the scheduler, the way a unit does.
func (f *instFixture) direct(ids ...string) (InstallResult, *capability.Call, error) {
	f.t.Helper()
	return f.directCtx(context.Background(), ids...)
}

func (f *instFixture) directCtx(ctx context.Context, ids ...string) (InstallResult, *capability.Call, error) {
	f.t.Helper()
	call := &capability.Call{Progress: func(int, int, string) {}}
	res, err := f.in.install(ctx, call, ids)
	if err != nil {
		return InstallResult{}, call, err
	}
	return res.(InstallResult), call, nil
}

func wantInstalled(t *testing.T, out capability.Outcome) InstallResult {
	t.Helper()
	if out.State != capability.StateCompleted || out.Error != nil {
		t.Fatalf("outcome = %s %+v, want completed", out.State, out.Error)
	}
	return out.Result.(InstallResult)
}

// wantErr asserts err is a capability error with code and returns it.
func wantErr(t *testing.T, err error, code capability.ErrorCode) *capability.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s", code)
	}
	ce := asCapabilityError(err)
	if ce.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
	return ce
}

// tempLeftovers are the droidyard-* entries in root/.temp.
func tempLeftovers(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".temp"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "droidyard-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func present(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

// modeOf is the permission bits of path.
func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// installedVersion is the revision the package.xml at root/rel holds.
func installedVersion(t *testing.T, dir string) string {
	t.Helper()
	p, found, note := readPackageXML(filepath.Join(dir, "package.xml"))
	if !found || note != "" {
		t.Fatalf("package.xml in %s: found=%v note=%q", dir, found, note)
	}
	return p.Version
}
