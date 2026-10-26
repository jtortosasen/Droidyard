package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// testRoot is an SDK root with platform-tools installed at version.
func testRoot(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	parts := strings.Split(version, ".")
	writePackage(t, filepath.Join(root, "platform-tools", "package.xml"), "platform-tools", parts...)
	return root
}

func acceptLicence(t *testing.T, root, id, text string) {
	t.Helper()
	dir := filepath.Join(root, "licenses")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id), []byte("\n"+licenceHash(text)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func answerFor(t *testing.T, srv *fxServer, root string, p PackagesParams) PackagesResult {
	t.Helper()
	var env []string
	if root != "" {
		env = []string{"ANDROID_HOME=" + root}
	}
	res, err := newPackagesService(env, srv.repo()).answer(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func installedIDs(r PackagesResult) []string {
	var out []string
	for _, e := range r.Installed {
		out = append(out, e.ID)
	}
	return out
}

func availableIDs(r PackagesResult) []string {
	var out []string
	for _, e := range r.Available {
		out = append(out, e.ID)
	}
	return out
}

// TestPackagesAnswer pins the shape of the answer: installed with the newer revision, the
// available list ordered by category and then newest first, licence state read from the
// host SDK, obsolete packages left out unless asked for.
func TestPackagesAnswer(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	root := testRoot(t, "36.0.0")
	acceptLicence(t, root, "android-sdk-license", fxLicenceText)

	res := answerFor(t, srv, root, PackagesParams{})
	if !res.Found || res.Root != root || res.Repository.State != "read" || res.Repository.FetchedAt == nil {
		t.Fatalf("found=%v root=%q repository=%+v", res.Found, res.Root, res.Repository)
	}
	if len(res.Installed) != 1 || res.Installed[0].ID != "platform-tools" || res.Installed[0].Version != "36.0.0" || res.Installed[0].Update != "37.0.1" {
		t.Errorf("installed = %+v, want platform-tools 36.0.0 with the update 37.0.1", res.Installed)
	}
	if res.Installed[0].SizeOnDisk == nil || *res.Installed[0].SizeOnDisk <= 0 {
		t.Errorf("sizeOnDisk = %v, want the package.xml's bytes", res.Installed[0].SizeOnDisk)
	}
	wantIDs := []string{
		"platforms;android-36",
		"build-tools;36.1.0", "build-tools;35.0.0",
		"platform-tools",
		"cmdline-tools;latest",
		"emulator",
		"system-images;android-28;default;arm64-v8a",
	}
	if got := availableIDs(res); !slices.Equal(got, wantIDs) {
		t.Errorf("available = %v, want %v (by category, newest first, no obsolete, the host's ABI)", got, wantIDs)
	}
	for _, e := range res.Available {
		if !e.LicenseAccepted || e.License.SHA1 != licenceHash(fxLicenceText) {
			t.Errorf("%s: licence %+v accepted=%v, want the accepted android-sdk-license", e.ID, e.License, e.LicenseAccepted)
		}
	}
	pt := res.Available[3]
	if pt.Size != 16000 || pt.Version != "37.0.1" || pt.Category != "platformTools" || pt.DisplayName != "Synthetic Platform-Tools" || pt.Dependencies == nil {
		t.Errorf("platform-tools = %+v", pt)
	}
	img := res.Available[6]
	if len(img.Dependencies) != 1 || img.Dependencies[0] != (DependencyEntry{ID: "emulator", MinVersion: "31.1.1"}) {
		t.Errorf("image dependencies = %+v", img.Dependencies)
	}
	if len(res.Licenses) != 1 || res.Licenses[0] != (LicenseState{ID: "android-sdk-license", SHA1: licenceHash(fxLicenceText), Accepted: true}) {
		t.Errorf("licenses = %+v, want one accepted pair", res.Licenses)
	}
	if res.Truncated {
		t.Error("truncated with 7 packages")
	}

	withObsolete := answerFor(t, srv, root, PackagesParams{IncludeObsolete: true})
	if !slices.Contains(availableIDs(withObsolete), "platforms;android-34") {
		t.Errorf("includeObsolete: %v lacks the obsolete platform", availableIDs(withObsolete))
	}
	byCategory := answerFor(t, srv, root, PackagesParams{Category: "buildTools"})
	if got := availableIDs(byCategory); !slices.Equal(got, []string{"build-tools;36.1.0", "build-tools;35.0.0"}) || len(byCategory.Installed) != 0 {
		t.Errorf("category buildTools: available %v installed %v", got, installedIDs(byCategory))
	}
	byQuery := answerFor(t, srv, root, PackagesParams{Query: "ANDROID-36"})
	if got := availableIDs(byQuery); !slices.Equal(got, []string{"platforms;android-36"}) {
		t.Errorf("query: %v, want the platform, ignoring case", got)
	}
}

// TestPackagesLicenceNotAccepted pins that the host SDK decides acceptance, by hash: no
// file, an older text's hash, and a file for the licence of another text are pending.
func TestPackagesLicenceNotAccepted(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	root := testRoot(t, "37.0.1")
	res := answerFor(t, srv, root, PackagesParams{})
	if len(res.Licenses) != 1 || res.Licenses[0].Accepted {
		t.Errorf("licenses = %+v, want one pending", res.Licenses)
	}
	acceptLicence(t, root, "android-sdk-license", fxOtherLicenceText) // an older text of the same id
	res = answerFor(t, srv, root, PackagesParams{})
	for _, e := range res.Available {
		if e.LicenseAccepted {
			t.Errorf("%s is accepted by another text's hash", e.ID)
		}
	}
	if res.Installed[0].Update != "" {
		t.Errorf("update = %q for an installed package at the repository's revision", res.Installed[0].Update)
	}
}

// TestPackagesTwoTextsTwoLicences pins that a licence id with two texts across lists is two
// entries in licenses, and accepting one text does not accept the other.
func TestPackagesTwoTextsTwoLicences(t *testing.T) {
	files := fxDefault()
	files["sys-img/android/sys-img2-3.xml"] = fxImgList(fxOtherLicenceText, fxImage("28", "default", "arm64-v8a", "a.zip"))
	srv := newFxServer(t, files)
	root := testRoot(t, "37.0.1")
	acceptLicence(t, root, "android-sdk-license", fxLicenceText)
	res := answerFor(t, srv, root, PackagesParams{})
	if len(res.Licenses) != 2 {
		t.Fatalf("licenses = %+v, want two (id, sha1) pairs", res.Licenses)
	}
	for _, l := range res.Licenses {
		want := l.SHA1 == licenceHash(fxLicenceText)
		if l.Accepted != want {
			t.Errorf("licence %+v: accepted=%v, want %v", l, l.Accepted, want)
		}
	}
	for _, e := range res.Available {
		wantAccepted := !strings.HasPrefix(e.ID, "system-images;")
		if e.LicenseAccepted != wantAccepted {
			t.Errorf("%s: licenseAccepted=%v, want %v", e.ID, e.LicenseAccepted, wantAccepted)
		}
	}
}

// TestPackagesRepositoryFailed pins that the repository never fails the call: a failed
// fetch is repository.state failed with the installed packages still listed.
func TestPackagesRepositoryFailed(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	srv.status["repository2-3.xml"] = 503
	root := testRoot(t, "36.0.0")
	res := answerFor(t, srv, root, PackagesParams{})
	if res.Repository.State != "failed" || res.Repository.Cause == "" || res.Repository.FetchedAt != nil {
		t.Errorf("repository = %+v, want failed with a cause", res.Repository)
	}
	if got := installedIDs(res); !slices.Equal(got, []string{"platform-tools"}) {
		t.Errorf("installed = %v, want platform-tools still listed", got)
	}
	if res.Available == nil || len(res.Available) != 0 || res.Licenses == nil {
		t.Errorf("available = %v licenses = %v, want empty non-nil lists", res.Available, res.Licenses)
	}
	if res.Installed[0].Update != "" {
		t.Errorf("update = %q without a repository", res.Installed[0].Update)
	}
}

// TestPackagesNoHostSDK pins the empty host: found false with the reason as a warning,
// nothing installed, nothing accepted, the repository still listed.
func TestPackagesNoHostSDK(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	res := answerFor(t, srv, "", PackagesParams{})
	if res.Found || res.Root != "" || len(res.Installed) != 0 || res.Installed == nil {
		t.Errorf("found=%v root=%q installed=%v", res.Found, res.Root, res.Installed)
	}
	if !warningsContain(res.Warnings, "no Android SDK was found") {
		t.Errorf("warnings = %v, want the missing SDK said", res.Warnings)
	}
	if len(res.Available) == 0 {
		t.Error("the repository's packages are not listed without an SDK")
	}
	for _, l := range res.Licenses {
		if l.Accepted {
			t.Errorf("licence %+v accepted without an SDK", l)
		}
	}
}

// TestPackagesLicenceAcceptedBeforeTheFirstInstall: on a host with no SDK yet, a licence
// accepted into the root sdk.install would set up (ADR-0131 §3) reads as accepted, as
// sdk.licenses says; otherwise the SDK panel's Set up stayed pending after Accept (found at
// plan m3f's level 5).
func TestPackagesLicenceAcceptedBeforeTheFirstInstall(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	root := filepath.Join(t.TempDir(), "sdk")
	before := answerFor(t, srv, root, PackagesParams{})
	if before.Found || len(before.Licenses) == 0 {
		t.Fatalf("found=%v licences=%v", before.Found, before.Licenses)
	}
	l := before.Licenses[0]
	if l.Accepted {
		t.Fatalf("licence %+v accepted before anything was written", l)
	}
	if err := os.MkdirAll(filepath.Join(root, "licenses"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "licenses", l.ID), []byte("\n"+l.SHA1), 0o644); err != nil {
		t.Fatal(err)
	}
	after := answerFor(t, srv, root, PackagesParams{})
	if after.Found {
		t.Fatal("a root holding only licenses/ is not an SDK")
	}
	for _, s := range after.Licenses {
		if s.ID == l.ID && s.SHA1 == l.SHA1 && !s.Accepted {
			t.Errorf("licence %s is pending after it was accepted into the target root", l.ID)
		}
	}
	for _, a := range after.Available {
		if a.License.ID == l.ID && a.License.SHA1 == l.SHA1 && !a.LicenseAccepted {
			t.Errorf("%s shows its licence pending after it was accepted", a.ID)
		}
	}
}

// TestPackagesIsBounded pins the 300-package bound and truncated.
func TestPackagesIsBounded(t *testing.T) {
	var pkgs []fxPkg
	for i := 0; i < maxAvailable+5; i++ {
		pkgs = append(pkgs, fxPkg{Path: fmt.Sprintf("platforms;android-%d", i)})
	}
	files := fxDefault()
	files["repository2-3.xml"] = fxRepo(fxLicenceText, pkgs...)
	res := answerFor(t, newFxServer(t, files), "", PackagesParams{})
	if len(res.Available) != maxAvailable || !res.Truncated {
		t.Errorf("%d available, truncated=%v, want %d and true", len(res.Available), res.Truncated, maxAvailable)
	}
	if res.Available[0].ID != fmt.Sprintf("platforms;android-%d", maxAvailable+4) {
		t.Errorf("first = %s, want the newest", res.Available[0].ID)
	}
}

// TestPackagesRejectsAnUnknownCategory pins the handler's own check of the enum.
func TestPackagesRejectsAnUnknownCategory(t *testing.T) {
	_, err := newPackagesService(nil, NewRepository(RepositoryOptions{})).answer(context.Background(), PackagesParams{Category: "nope"})
	var ce *capability.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != capability.ErrInvalidParams {
		t.Errorf("err = %v, want REQUEST_INVALID_PARAMS", err)
	}
}

// TestUpdateAvailable pins `update`: an installed revision below the repository's shows it,
// an equal one, a newer one and one that does not parse do not; 23.0 and 23.0.0 are equal.
func TestUpdateAvailable(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	for _, c := range []struct {
		installed []string
		want      string
	}{
		{[]string{"36", "0", "0"}, "37.0.1"},
		{[]string{"37", "0", "0"}, "37.0.1"},
		{[]string{"37", "0", "1"}, ""},
		{[]string{"37", "0", "2"}, ""},
		{[]string{"37", "0", "1", "x"}, ""},
	} {
		root := t.TempDir()
		writePackage(t, filepath.Join(root, "platform-tools", "package.xml"), "platform-tools", c.installed...)
		res := answerFor(t, srv, root, PackagesParams{})
		if len(res.Installed) != 1 || res.Installed[0].Update != c.want {
			t.Errorf("installed %v: %+v, want update %q", c.installed, res.Installed, c.want)
		}
	}
	// A preview of the same numbers is older than the final release.
	root := t.TempDir()
	writePackage(t, filepath.Join(root, "platform-tools", "package.xml"), "platform-tools", "37", "0", "1")
	body, _ := os.ReadFile(filepath.Join(root, "platform-tools", "package.xml"))
	preview := strings.Replace(string(body), "<micro>1</micro>", "<micro>1</micro><preview>2</preview>", 1)
	os.WriteFile(filepath.Join(root, "platform-tools", "package.xml"), []byte(preview), 0o644)
	res := answerFor(t, srv, root, PackagesParams{})
	if res.Installed[0].Version != "37.0.1-rc2" || res.Installed[0].Update != "37.0.1" {
		t.Errorf("a preview installed: %+v, want 37.0.1-rc2 with the final release as the update", res.Installed[0])
	}
}

// TestInstalledFromSourceProperties pins the fallback: a package with no package.xml but a
// source.properties is installed, at its Pkg.Revision, under the id its place gives; and
// a directory with neither is a warning, not silence.
func TestInstalledFromSourceProperties(t *testing.T) {
	root := t.TempDir()
	put := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("platforms/android-35/source.properties", "Pkg.Path=platforms;android-35\nPkg.Revision=2\n")
	put("cmdline-tools/latest/source.properties", "Pkg.Desc=Android SDK Command-line Tools\nPkg.Path=cmdline-tools;12.0\nPkg.Revision=12.0\n")
	put("build-tools/36.0.0/source.properties", "Pkg.Revision=36.0.0 rc1\n")
	if err := os.MkdirAll(filepath.Join(root, "platforms", "android-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst, warnings := ReadInstalled(root)
	if len(inst.Platforms) != 1 || inst.Platforms[0] != (Package{ID: "platforms;android-35", Version: "2"}) {
		t.Errorf("platforms = %+v, want android-35 at 2 from source.properties", inst.Platforms)
	}
	if len(inst.CmdlineTools) != 1 || inst.CmdlineTools[0] != (Package{ID: "cmdline-tools;latest", Version: "12.0"}) {
		t.Errorf("cmdlineTools = %+v, want latest at 12.0 (its place decides the id, not Pkg.Path)", inst.CmdlineTools)
	}
	if len(inst.BuildTools) != 1 || inst.BuildTools[0].Version != "36.0.0-rc1" {
		t.Errorf("buildTools = %+v, want the preview normalised", inst.BuildTools)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "incomplete package at platforms/android-empty") {
		t.Errorf("warnings = %v, want one: incomplete package at platforms/android-empty", warnings)
	}
}

// TestInstalledOutsideTheSixLocations pins that sdk.packages lists a repository package
// wherever the SDK layout puts it, not only at sdk.status' six places.
func TestInstalledOutsideTheSixLocations(t *testing.T) {
	files := fxDefault()
	files["repository2-3.xml"] = fxRepo(fxLicenceText,
		fxPkg{Path: "ndk;27.0.1", Rev: "27.0.12077973"}, fxPkg{Path: "sources;android-35", Rev: "1"},
		fxPkg{Path: "platform-tools", Rev: "37.0.1"})
	srv := newFxServer(t, files)
	root := testRoot(t, "37.0.1")
	writePackage(t, filepath.Join(root, "ndk", "27.0.1", "package.xml"), "ndk;27.0.1", "27", "0", "1")
	if err := os.MkdirAll(filepath.Join(root, "sources", "android-35"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sources", "android-35", "source.properties"), []byte("Pkg.Revision=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := answerFor(t, srv, root, PackagesParams{})
	if got := installedIDs(res); !slices.Equal(got, []string{"platform-tools", "ndk;27.0.1", "sources;android-35"}) && !slices.Equal(got, []string{"platform-tools", "sources;android-35", "ndk;27.0.1"}) {
		t.Errorf("installed = %v, want platform-tools, ndk;27.0.1 and sources;android-35", got)
	}
	if inst, _ := ReadInstalled(root); len(inst.Platforms)+len(inst.BuildTools)+len(inst.SystemImages)+len(inst.CmdlineTools) != 0 {
		t.Errorf("ReadInstalled found %+v outside its six locations", inst)
	}
}

// TestCmdlineToolsHighestRevision pins that cmdline-tools;latest is the highest revision
// among latest and latest-N, which a self-update may leave beside it.
func TestCmdlineToolsHighestRevision(t *testing.T) {
	put := func(root, dir, rev string) {
		p := filepath.Join(root, "cmdline-tools", dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "source.properties"), []byte("Pkg.Revision="+rev+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		repoRev, wantUpdate string
	}{{"19.0", ""}, {"23.0", "23.0"}} {
		files := fxDefault()
		files["repository2-3.xml"] = fxRepo(fxLicenceText, fxPkg{Path: "cmdline-tools;latest", Rev: c.repoRev})
		root := t.TempDir()
		put(root, "latest", "12.0")
		put(root, "latest-2", "19.0")
		res := answerFor(t, newFxServer(t, files), root, PackagesParams{})
		if len(res.Installed) != 1 || res.Installed[0].ID != "cmdline-tools;latest" || res.Installed[0].Version != "19.0" || res.Installed[0].Update != c.wantUpdate {
			t.Errorf("repository %s: installed = %+v, want latest at 19.0 and update %q", c.repoRev, res.Installed, c.wantUpdate)
		}
	}
}

// TestSizeOnDiskBounded pins the bounded walk: exact bytes within the bounds; past the
// entry bound the size is absent for the rest with one warning.
func TestSizeOnDiskBounded(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	root := testRoot(t, "36.0.0")
	pt := filepath.Join(root, "platform-tools")
	if err := os.MkdirAll(filepath.Join(pt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, n := range map[string]int{"adb": 1000, "sub/fastboot": 500} {
		if err := os.WriteFile(filepath.Join(pt, rel), make([]byte, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink to a big file elsewhere counts nothing.
	big := filepath.Join(t.TempDir(), "big")
	os.WriteFile(big, make([]byte, 1<<20), 0o644)
	os.Symlink(big, filepath.Join(pt, "link"))
	fi, _ := os.Stat(filepath.Join(pt, "package.xml"))
	want := fi.Size() + 1500

	res := answerFor(t, srv, root, PackagesParams{})
	if res.Installed[0].SizeOnDisk == nil || *res.Installed[0].SizeOnDisk != want {
		t.Errorf("sizeOnDisk = %v, want %d (files only, symlinks not followed)", res.Installed[0].SizeOnDisk, want)
	}

	s := newPackagesService([]string{"ANDROID_HOME=" + root}, srv.repo())
	s.sizeEntries = 3 // platform-tools, package.xml, adb... and then the bound
	res, err := s.answer(context.Background(), PackagesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Installed[0].SizeOnDisk != nil || !warningsContain(res.Warnings, "size on disk was left out") {
		t.Errorf("past the bound: sizeOnDisk=%v warnings=%v, want it absent with a warning", res.Installed[0].SizeOnDisk, res.Warnings)
	}
}

// TestPackagesWiring pins the registered descriptor: a read-only query that is open-world
// (the network), with the display name, and only it, marked untrusted by the tag.
func TestPackagesWiring(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	reg := capability.NewRegistry()
	Register(reg, Options{RepositoryBase: srv.base(), HTTPClient: srv.Client(), GOOS: "darwin", GOARCH: "arm64"})
	d, ok := reg.Lookup("sdk.packages")
	if !ok {
		t.Fatal("sdk.packages is not registered")
	}
	a := d.ResolveAnnotations()
	if !a.ReadOnly || !a.OpenWorld || a.Destructive || !a.Idempotent || d.Audience != capability.AudienceAll || d.Scope != capability.Global || d.Since != "0.39.0" {
		t.Errorf("descriptor = %+v %+v", a, d)
	}
	if !d.Untrusted() {
		t.Error("the result is not marked untrusted: displayName is the repository's text")
	}
	out, err := d.Handler(context.Background(), &capability.Call{Params: PackagesParams{Query: "platform-tools"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(PackagesResult); len(got.Available) != 1 || got.Available[0].ID != "platform-tools" {
		t.Errorf("through the registry: %+v", got.Available)
	}
}

// TestPackagesText pins the rendering: one line per package, and a display name that tries
// to forge a line stays inside its quotes.
func TestPackagesText(t *testing.T) {
	files := fxDefault()
	files["repository2-3.xml"] = fxRepo(fxLicenceText,
		fxPkg{Path: "platform-tools", Rev: "37.0.1", Display: "Evil &#10;warning &quot;forged&quot;"})
	root := testRoot(t, "36.0.0")
	res := answerFor(t, newFxServer(t, files), root, PackagesParams{})
	text := packagesText(res)
	lines := strings.Split(text, "\n")
	if !strings.HasPrefix(lines[0], "sdk ") || !strings.Contains(lines[0], "repository read") {
		t.Errorf("first line = %q", lines[0])
	}
	var kinds []string
	for _, l := range lines {
		kinds = append(kinds, strings.Fields(l)[0])
	}
	if !slices.Equal(kinds, []string{"sdk", "installed", "available", "available", "license"}) {
		t.Errorf("lines = %v\n%s", kinds, text)
	}
	// The repository's newline is flattened to a space when the model reads it, and the
	// quotes stay inside the quoted string.
	if !strings.Contains(text, `"Evil  warning \"forged\""`) {
		t.Errorf("the display name is not quoted whole:\n%s", text)
	}
	if !strings.Contains(lines[1], "update=37.0.1") {
		t.Errorf("installed line = %q, want the update said", lines[1])
	}
}

// TestPackagesResultJSON pins that an empty answer marshals lists as [] and not null.
func TestPackagesResultJSON(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	srv.status["repository2-3.xml"] = 500
	raw, err := json.Marshal(answerFor(t, srv, "", PackagesParams{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{`"installed":[]`, `"available":[]`, `"licenses":[]`} {
		if !strings.Contains(string(raw), f) {
			t.Errorf("%s lacks %s", raw, f)
		}
	}
}
