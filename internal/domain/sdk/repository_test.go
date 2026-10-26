package sdk

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func load(t *testing.T, r *Repository, refresh bool) Snapshot {
	t.Helper()
	return r.Load(context.Background(), refresh)
}

func mustModel(t *testing.T, r *Repository) *Model {
	t.Helper()
	snap := load(t, r, false)
	if snap.Model == nil {
		t.Fatalf("the repository did not load: state %s, cause %q", snap.State, snap.Cause)
	}
	return snap.Model
}

func warningsContain(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func modelIDs(m *Model) []string {
	var out []string
	for _, p := range m.Packages {
		out = append(out, p.ID)
	}
	return out
}

// TestRepositoryModel pins what the model is: the stable channel's package per id with
// this host's archive resolved against its own list, the revision, licence (id and sdklib
// hash), dependencies and the type-details kept for package.xml.
func TestRepositoryModel(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	m := mustModel(t, srv.repo())

	if len(m.Warnings) != 0 {
		t.Errorf("warnings = %v, want none for a well-formed repository", m.Warnings)
	}
	pt, ok := m.Package("platform-tools")
	if !ok {
		t.Fatalf("platform-tools is not in the model: %v", modelIDs(m))
	}
	if pt.Revision.String() != "37.0.1" || pt.DisplayName != "Synthetic Platform-Tools" {
		t.Errorf("platform-tools = %+v", pt)
	}
	if pt.Archive.URL != srv.base()+"platform-tools_r37.0.1-darwin.zip" || pt.Archive.Size != 16000 || pt.Archive.ChecksumType != "sha1" || pt.Archive.Checksum != fxSum(16000) {
		t.Errorf("platform-tools archive = %+v, want the macosx one resolved against the base", pt.Archive)
	}
	wantHash := licenceHash(fxLicenceText)
	if pt.License != (LicenceRef{ID: "android-sdk-license", SHA1: wantHash}) {
		t.Errorf("licence = %+v, want android-sdk-license with sdklib's hash %s", pt.License, wantHash)
	}
	if lic, ok := m.Licences[pt.License]; !ok || lic.Text != fxLicenceText {
		t.Errorf("the licence text for %+v is %q, want the served text", pt.License, lic.Text)
	}

	plat, _ := m.Package("platforms;android-36")
	if plat == nil {
		t.Fatal("platforms;android-36 is not in the model")
	}
	wantPlat := TypeDetails{
		Namespace: "http://schemas.android.com/sdk/android/repo/repository2/03",
		Local:     "platformDetailsType",
		Children:  `<api-level>36</api-level><codename></codename><extension-level>17</extension-level><base-extension>true</base-extension><layoutlib api="15"></layoutlib>`,
	}
	if plat.TypeDetails != wantPlat {
		t.Errorf("platform type-details = %+v, want %+v", plat.TypeDetails, wantPlat)
	}

	img, _ := m.Package("system-images;android-28;default;arm64-v8a")
	if img == nil {
		t.Fatalf("the arm64 image is not in the model: %v", modelIDs(m))
	}
	if img.Archive.URL != srv.base()+"sys-img/android/arm64-v8a-28_r02.zip" {
		t.Errorf("image archive = %s, want the file name resolved against its list's directory", img.Archive.URL)
	}
	if len(img.Dependencies) != 1 || img.Dependencies[0].ID != "emulator" || img.Dependencies[0].Min == nil || img.Dependencies[0].Min.String() != "31.1.1" {
		t.Errorf("image dependencies = %+v, want emulator >= 31.1.1", img.Dependencies)
	}
	wantImg := `<api-level>28</api-level><base-extension>true</base-extension><tag><id>default</id><display>Synthetic default</display></tag><abi>arm64-v8a</abi>`
	if img.TypeDetails.Local != "sysImgDetailsType" || img.TypeDetails.Namespace != "http://schemas.android.com/sdk/android/repo/sys-img2/03" || img.TypeDetails.Children != wantImg {
		t.Errorf("image type-details = %+v", img.TypeDetails)
	}
	if old, _ := m.Package("platforms;android-34"); old == nil || !old.Obsolete {
		t.Errorf("platforms;android-34 = %+v, want present and obsolete", old)
	}
	if _, ok := m.Package("system-images;android-28;default;x86_64"); ok {
		t.Error("an x86_64 image is in the model of an arm64 host")
	}
}

// TestRepositoryHostArchive pins the host decision: an archive is this host's when its
// host-os and host-arch, if present, are, the most specific wins, a package with no
// archive for the host is not available, and system images are the host's ABI only.
func TestRepositoryHostArchive(t *testing.T) {
	files := fxDefault()
	files["repository2-3.xml"] = fxRepo(fxLicenceText,
		fxPkg{Path: "platform-tools", Rev: "37.0.1", Archives: fxArchive("linux", "", 9000, "pt-linux.zip") + fxArchive("macosx", "", 16000, "pt-darwin.zip")},
		fxPkg{Path: "cmdline-tools;latest", Rev: "23.0", Archives: fxArchive("linux", "", 18000, "cl-linux.zip") + fxArchive("macosx", "x64", 15500, "cl-x64.zip") + fxArchive("macosx", "aarch64", 15400, "cl-arm64.zip")},
		fxPkg{Path: "windows-only", Archives: fxArchive("windows", "", 100, "w.zip")},
	)
	srv := newFxServer(t, files)
	for _, c := range []struct {
		goos, goarch  string
		wantPT        string
		wantCmdline   string
		wantImage     string
		absentImage   string
		wantNoWindows bool
	}{
		{"darwin", "arm64", "pt-darwin.zip", "cl-arm64.zip", "system-images;android-28;default;arm64-v8a", "system-images;android-28;default;x86_64", true},
		{"darwin", "amd64", "pt-darwin.zip", "cl-x64.zip", "system-images;android-28;default;x86_64", "system-images;android-28;default;arm64-v8a", true},
		{"linux", "amd64", "pt-linux.zip", "cl-linux.zip", "system-images;android-28;default;x86_64", "system-images;android-28;default;arm64-v8a", true},
	} {
		t.Run(c.goos+"/"+c.goarch, func(t *testing.T) {
			m := mustModel(t, srv.repo(func(o *RepositoryOptions) { o.GOOS, o.GOARCH = c.goos, c.goarch }))
			pt, _ := m.Package("platform-tools")
			cl, _ := m.Package("cmdline-tools;latest")
			if pt == nil || !strings.HasSuffix(pt.Archive.URL, c.wantPT) {
				t.Errorf("platform-tools archive = %+v, want %s", pt, c.wantPT)
			}
			if cl == nil || !strings.HasSuffix(cl.Archive.URL, c.wantCmdline) {
				t.Errorf("cmdline-tools archive = %+v, want %s", cl, c.wantCmdline)
			}
			if _, ok := m.Package(c.wantImage); !ok {
				t.Errorf("%s is not in %v", c.wantImage, modelIDs(m))
			}
			if _, ok := m.Package(c.absentImage); ok {
				t.Errorf("%s is in the model of this host", c.absentImage)
			}
			if _, ok := m.Package("windows-only"); ok {
				t.Error("a package with only a windows archive is available on this host")
			}
		})
	}
}

// TestStableEntryPerID pins the one-entry-per-id rule: an id listed on several channels
// keeps its stable (channel-0) entry whichever comes last, an id with no stable entry is
// not available, and two stable entries keep the newer revision.
func TestStableEntryPerID(t *testing.T) {
	for _, order := range []string{"stable first", "stable last"} {
		t.Run(order, func(t *testing.T) {
			stable := fxPkg{Path: "emulator", Rev: "37.2.12"}
			dev := fxPkg{Path: "emulator", Rev: "37.3.5", Channel: "channel-2", License: "android-sdk-preview-license"}
			pkgs := []fxPkg{stable, dev}
			if order == "stable last" {
				pkgs = []fxPkg{dev, stable}
			}
			pkgs = append(pkgs,
				fxPkg{Path: "dev-only", Channel: "channel-2"},
				fxPkg{Path: "twice", Rev: "2.0"},
				fxPkg{Path: "twice", Rev: "3.0"},
				fxPkg{Path: "twice-reversed", Rev: "3.0"},
				fxPkg{Path: "twice-reversed", Rev: "2.0"},
			)
			files := fxDefault()
			files["repository2-3.xml"] = fxRepo(fxLicenceText, pkgs...)
			m := mustModel(t, newFxServer(t, files).repo())
			em, _ := m.Package("emulator")
			if em == nil || em.Revision.String() != "37.2.12" || em.License.ID != "android-sdk-license" {
				t.Errorf("emulator = %+v, want the channel-0 entry 37.2.12", em)
			}
			if _, ok := m.Package("dev-only"); ok {
				t.Error("an id with no stable entry is available")
			}
			for id, want := range map[string]string{"twice": "3.0", "twice-reversed": "3.0"} {
				if p, _ := m.Package(id); p == nil || p.Revision.String() != want {
					t.Errorf("%s = %+v, want revision %s", id, p, want)
				}
			}
		})
	}
}

// TestArchiveURLRelativeToList pins the URL rule: an archive's url resolves against the
// directory of the list that declared it; an absolute URL, a host-relative one, a
// backslash, or a path that climbs out of the base drops that archive, counted in one
// warning that does not echo the URL.
func TestArchiveURLRelativeToList(t *testing.T) {
	files := fxDefault()
	bad := []string{"https://evil.example/x.zip", "../../../x.zip", "/abs.zip", "//evil.example/x.zip", `a\b.zip`, "http://evil.example/x.zip", "sub/../../../../x.zip"}
	var pkgs []fxPkg
	pkgs = append(pkgs, fxPkg{Path: "ok-image", Archives: fxArchive("", "", 10, "x.zip")})
	for i, u := range bad {
		pkgs = append(pkgs, fxPkg{Path: fmt.Sprintf("bad%d", i), Archives: fxArchive("", "", 10, u)})
	}
	files["sys-img/android/sys-img2-3.xml"] = fxImgList(fxLicenceText, pkgs...)
	m := mustModel(t, newFxServer(t, files).repo())

	srvBase := ""
	if ok, found := m.Package("ok-image"); found {
		srvBase = ok.Archive.URL
	} else {
		t.Fatalf("ok-image is not in the model: %v", modelIDs(m))
	}
	if !strings.HasSuffix(srvBase, "/android/repository/sys-img/android/x.zip") {
		t.Errorf("x.zip in a list under sys-img/android/ resolves to %s, want it under sys-img/android/", srvBase)
	}
	for i := range bad {
		if _, ok := m.Package(fmt.Sprintf("bad%d", i)); ok {
			t.Errorf("a package whose only archive is %q is available", bad[i])
		}
	}
	want := fmt.Sprintf("dropped %d repository entries: archive URL outside the repository ×%d", len(bad), len(bad))
	if !warningsContain(m.Warnings, want) {
		t.Errorf("warnings = %v, want %q", m.Warnings, want)
	}
	for _, w := range m.Warnings {
		if strings.Contains(w, "evil") {
			t.Errorf("warning %q echoes the repository's URL", w)
		}
	}
}

// TestTypeDetailsKept pins what is kept of a type-details: the xsi:type resolved to a
// namespace of the package.xml preamble and its children as unprefixed XML. A prefixed
// child, a child carrying xmlns or xmlns:p, a comment, an unknown namespace or an
// unresolvable prefix drops the package, counted.
func TestTypeDetailsKept(t *testing.T) {
	td := func(inner string) string {
		return `<type-details xsi:type="sdk:platformDetailsType">` + inner + `</type-details>`
	}
	pkgs := []fxPkg{
		{Path: "good", Details: td(`<api-level>35</api-level><layoutlib api="15"/>`)},
		{Path: "prefixed", Details: td(`<sdk:api-level>35</sdk:api-level>`)},
		{Path: "defaultns", Details: td(`<api-level xmlns="http://evil.example/ns">35</api-level>`)},
		{Path: "xmlnsattr", Details: td(`<api-level xmlns:p="http://evil.example/ns">35</api-level>`)},
		{Path: "xmlnsonattrs", Details: td(`<api-level p:x="1" xmlns:p="http://evil.example/ns">35</api-level>`)},
		{Path: "comment", Details: td(`<api-level>35</api-level><!-- a comment -->`)},
		{Path: "procinst", Details: td(`<?target data?><api-level>35</api-level>`)},
		{Path: "unknownns", Details: `<type-details xmlns:evil="http://evil.example/ns" xsi:type="evil:platformDetailsType"><api-level>35</api-level></type-details>`},
		{Path: "unresolved", Details: `<type-details xsi:type="nope:platformDetailsType"/>`},
		{Path: "noprefix", Details: `<type-details xsi:type="platformDetailsType"/>`},
		{Path: "extraattr", Details: `<type-details xsi:type="sdk:platformDetailsType" extra="1"/>`},
		{Path: "badlocal", Details: `<type-details xsi:type="sdk:a-b"/>`},
		{Path: "toolarge", Details: td(`<api-level>` + strings.Repeat("a", 17<<10) + `</api-level>`)},
	}
	files := fxDefault()
	files["repository2-3.xml"] = fxRepo(fxLicenceText, pkgs...)
	m := mustModel(t, newFxServer(t, files).repo())

	good, _ := m.Package("good")
	if good == nil || good.TypeDetails.Children != `<api-level>35</api-level><layoutlib api="15"></layoutlib>` || good.TypeDetails.Local != "platformDetailsType" {
		t.Fatalf("good = %+v", good)
	}
	for _, p := range pkgs[1:] {
		if _, ok := m.Package(p.Path); ok {
			t.Errorf("package %s, whose type-details cannot be kept as unprefixed XML, is in the model", p.Path)
		}
	}
	want := fmt.Sprintf("dropped %d repository entries: unusable type-details ×%d", len(pkgs)-1, len(pkgs)-1)
	if !warningsContain(m.Warnings, want) {
		t.Errorf("warnings = %v, want %q", m.Warnings, want)
	}
}

// TestPackageIDsValidated pins that an id reaches a path later only if it matches the
// patterns: a leading "-", a space, a ".." or "." component and an over-long licence id
// are dropped and counted, valid multi-component ids are kept.
func TestPackageIDsValidated(t *testing.T) {
	long := strings.Repeat("a", 65)
	files := fxDefault()
	repo := fxRepo(fxLicenceText,
		fxPkg{Path: "-rf"}, fxPkg{Path: "a b"}, fxPkg{Path: "x;..;y"}, fxPkg{Path: "a;..;..;x"}, fxPkg{Path: "x;."},
		fxPkg{Path: "a;;b"}, fxPkg{Path: "Upper"},
		fxPkg{Path: "ndk;27.0.1"}, fxPkg{Path: "extras;google;m2repository"},
		fxPkg{Path: "uses-long-licence", License: long},
	)
	// The 65-character licence id is also declared, which is dropped too.
	repo = strings.Replace(repo, `<license id="android-sdk-preview-license"`, `<license id="`+long+`" type="text">t</license><license id="android-sdk-preview-license"`, 1)
	files["repository2-3.xml"] = repo
	files["addons_list-5.xml"] = fxSiteList() // no image lists: only this repository's ids
	m := mustModel(t, newFxServer(t, files).repo())

	got := modelIDs(m)
	slices.Sort(got)
	want := []string{"ndk;27.0.1", "extras;google;m2repository"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("model ids = %v, want only the valid %v", got, want)
	}
	// Seven bad package ids; the licence declaration and the package using it.
	wantWarn := "dropped 9 repository entries: invalid licence id ×2, invalid package id ×7"
	if !warningsContain(m.Warnings, wantWarn) {
		t.Errorf("warnings = %v, want %q", m.Warnings, wantWarn)
	}
}

// TestRepositoryBounds pins every bound on what a repository can make us read: a 17 MiB
// body, more than 20000 packages, a DOCTYPE with an entity, list URLs that are not paths
// inside the base, and more lists than the bound.
func TestRepositoryBounds(t *testing.T) {
	t.Run("a body over 16 MiB fails the fetch", func(t *testing.T) {
		files := fxDefault()
		files["repository2-3.xml"] = fxRepoRoot + "<!--" + strings.Repeat("x", 17<<20) + "-->" + fxRepoEnd
		snap := load(t, newFxServer(t, files).repo(), false)
		if snap.State != "failed" || snap.Model != nil || !strings.Contains(snap.Cause, "larger than 16 MiB") {
			t.Errorf("snapshot = %s %q model=%v, want failed with the bound as the cause", snap.State, snap.Cause, snap.Model != nil)
		}
	})
	t.Run("a body over the bound in a list skips the list", func(t *testing.T) {
		files := fxDefault()
		files["sys-img/android/sys-img2-3.xml"] = fxImgRoot + "<!--" + strings.Repeat("x", 17<<20) + "-->" + fxImgEnd
		m := mustModel(t, newFxServer(t, files).repo())
		if !warningsContain(m.Warnings, "1 of 1 system-image lists could not be read") {
			t.Errorf("warnings = %v, want the skipped list said", m.Warnings)
		}
		if _, ok := m.Package("platform-tools"); !ok {
			t.Error("the repository's own packages are missing because a list failed")
		}
	})
	t.Run("more than 20000 packages fails the fetch", func(t *testing.T) {
		build := func(n int) string {
			var b strings.Builder
			b.WriteString(fxRepoRoot + fxLicences(fxLicenceText) + fxChannels)
			for i := 0; i < n; i++ {
				fmt.Fprintf(&b, `<remotePackage path="p;x%d"><channelRef ref="channel-1"/></remotePackage>`, i)
			}
			return b.String() + fxRepoEnd
		}
		files := fxDefault()
		files["addons_list-5.xml"] = fxSiteList() // the bound counts the lists' packages too
		files["repository2-3.xml"] = build(20001)
		snap := load(t, newFxServer(t, files).repo(), false)
		if snap.State != "failed" || !strings.Contains(snap.Cause, "more than 20000 packages") {
			t.Errorf("snapshot = %s %q, want failed: more than 20000 packages", snap.State, snap.Cause)
		}
		files["repository2-3.xml"] = build(20000)
		if snap := load(t, newFxServer(t, files).repo(), false); snap.Model == nil {
			t.Errorf("exactly 20000 packages failed: %q", snap.Cause)
		}
	})
	t.Run("a DOCTYPE with an entity is not expanded", func(t *testing.T) {
		marker := "EXPANDEDMARKER"
		doctype := `<!DOCTYPE x [<!ENTITY e "` + marker + `">]>`
		// In a system-image list: the list is skipped and nothing of the entity reaches the model.
		files := fxDefault()
		files["sys-img/android/sys-img2-3.xml"] = strings.Replace(fxImgList(fxLicenceText, fxPkg{Path: "ent", Display: "&e;"}), "<sys-img:sdk-sys-img", doctype+"<sys-img:sdk-sys-img", 1)
		m := mustModel(t, newFxServer(t, files).repo())
		if !warningsContain(m.Warnings, "1 of 1 system-image lists could not be read") {
			t.Errorf("warnings = %v, want the list with a DOCTYPE skipped", m.Warnings)
		}
		for _, p := range m.Packages {
			if strings.Contains(p.DisplayName, marker) || strings.Contains(p.ID, marker) {
				t.Errorf("package %+v holds the entity's expansion", p)
			}
		}
		// In the repository itself: the fetch fails, and the cause does not carry it.
		files["repository2-3.xml"] = strings.Replace(fxRepo(fxLicenceText, fxPkg{Path: "ent", Display: "&e;"}), "<sdk:sdk-repository", doctype+"<sdk:sdk-repository", 1)
		snap := load(t, newFxServer(t, files).repo(), false)
		if snap.State != "failed" || strings.Contains(snap.Cause, marker) || !strings.Contains(snap.Cause, "DOCTYPE") {
			t.Errorf("snapshot = %s %q, want failed naming the DOCTYPE, never the expansion", snap.State, snap.Cause)
		}
	})
	t.Run("list URLs that are not paths inside the base are not fetched", func(t *testing.T) {
		other := newFxServer(t, map[string]string{})
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("the plain-http server was asked for %s", r.URL)
		}))
		t.Cleanup(plain.Close)
		files := fxDefault()
		files["addons_list-5.xml"] = fxSiteList(
			other.URL+"/android/repository/x.xml", // https, another host
			plain.URL+"/y.xml",                    // http
			"//evil.example/z.xml",                // scheme-relative
			"/abs.xml",                            // host-relative
			"../up.xml",                           // climbs out
			"a/../../up2.xml",                     // climbs out after a cleaned segment
			"has space.xml",                       // not a clean path
			"sys-img/android/sys-img2-3.xml",
		)
		srv := newFxServer(t, files)
		m := mustModel(t, srv.repo())
		if got := other.requests(); len(got) != 0 {
			t.Errorf("another host was asked for %v", got)
		}
		for _, r := range srv.requests() {
			if slices.Contains([]string{"x.xml", "y.xml", "z.xml", "abs.xml", "up.xml", "up2.xml"}, r) || strings.Contains(r, "evil") || strings.Contains(r, " ") {
				t.Errorf("the repository's own server was asked for %q", r)
			}
		}
		if !warningsContain(m.Warnings, "ignored 7 system-image list URLs") {
			t.Errorf("warnings = %v, want 7 URLs ignored", m.Warnings)
		}
		if _, ok := m.Package("system-images;android-28;default;arm64-v8a"); !ok {
			t.Error("the legitimate list was not read")
		}
	})
	t.Run("lists past the bound are not read", func(t *testing.T) {
		var urls []string
		for i := 0; i < maxRepoLists+3; i++ {
			urls = append(urls, fmt.Sprintf("sys-img/l%d/sys-img2-3.xml", i))
		}
		files := fxDefault()
		files["addons_list-5.xml"] = fxSiteList(urls...)
		srv := newFxServer(t, files)
		m := mustModel(t, srv.repo())
		n := 0
		for _, r := range srv.requests() {
			if strings.HasPrefix(r, "sys-img/") {
				n++
			}
		}
		if n != maxRepoLists {
			t.Errorf("%d lists were fetched, want the bound %d", n, maxRepoLists)
		}
		if !warningsContain(m.Warnings, "only the first 32 were read") {
			t.Errorf("warnings = %v, want the bound said", m.Warnings)
		}
	})
}

// TestRepositoryRedirect pins that a redirect off the repository's host is not followed:
// the other host is never asked, the list is skipped with a warning, and a redirect to
// the same host is followed.
func TestRepositoryRedirect(t *testing.T) {
	other := newFxServer(t, map[string]string{"sys-img/android/sys-img2-3.xml": fxImgList(fxLicenceText, fxPkg{Path: "from-other-host"})})
	srv := newFxServer(t, fxDefault())
	srv.redirect["sys-img/android/sys-img2-3.xml"] = other.URL + "/android/repository/sys-img/android/sys-img2-3.xml"
	m := mustModel(t, srv.repo())
	if got := other.requests(); len(got) != 0 {
		t.Errorf("the redirect target was asked for %v", got)
	}
	if _, ok := m.Package("from-other-host"); ok {
		t.Error("a package from the redirect target is in the model")
	}
	if !warningsContain(m.Warnings, "1 of 1 system-image lists could not be read") {
		t.Errorf("warnings = %v, want the skipped list said", m.Warnings)
	}

	// The repository itself redirected away: failed, with the cause said without the target.
	srv2 := newFxServer(t, fxDefault())
	srv2.redirect["repository2-3.xml"] = other.URL + "/android/repository/repository2-3.xml"
	snap := load(t, srv2.repo(), false)
	if snap.State != "failed" || !strings.Contains(snap.Cause, "redirected to another host") || strings.Contains(snap.Cause, other.URL) {
		t.Errorf("snapshot = %s %q", snap.State, snap.Cause)
	}

	// A redirect inside the host is followed.
	srv3 := newFxServer(t, fxDefault())
	srv3.files["moved.xml"] = srv3.files["repository2-3.xml"]
	srv3.redirect["repository2-3.xml"] = "/android/repository/moved.xml"
	if snap := load(t, srv3.repo(), false); snap.Model == nil {
		t.Errorf("a redirect inside the host failed: %s", snap.Cause)
	}
}

// TestRepositoryRefusesHTTP pins that the base must be https.
func TestRepositoryRefusesHTTP(t *testing.T) {
	r := NewRepository(RepositoryOptions{Base: "http://dl.google.com/android/repository/"})
	if snap := load(t, r, false); snap.State != "failed" || !strings.Contains(snap.Cause, "https") {
		t.Errorf("snapshot = %s %q, want failed: not https", snap.State, snap.Cause)
	}
}

// TestRepositoryCache pins the cache: a second call fetches nothing, refresh fetches, a
// failing refresh returns the copy with the cause, and the copy expires after an hour.
func TestRepositoryCache(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	clock := &fxClock{t: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)}
	r := srv.repo(func(o *RepositoryOptions) { o.Now = clock.now })

	first := load(t, r, false)
	if first.State != "read" || first.Model == nil || !first.FetchedAt.Equal(clock.now()) {
		t.Fatalf("first = %+v, want read now", first)
	}
	fetched := len(srv.requests())
	if fetched == 0 {
		t.Fatal("the first call fetched nothing")
	}

	second := load(t, r, false)
	if second.State != "cached" || second.Model != first.Model || second.Cause != "" {
		t.Errorf("second = %s %q, want the cached copy", second.State, second.Cause)
	}
	if n := len(srv.requests()); n != fetched {
		t.Errorf("the second call made %d requests, want none", n-fetched)
	}

	clock.advance(10 * time.Minute)
	refreshed := load(t, r, true)
	if refreshed.State != "read" || refreshed.Model == first.Model || !refreshed.FetchedAt.Equal(clock.now()) {
		t.Errorf("refresh = %s, want a new read", refreshed.State)
	}
	if n := len(srv.requests()); n <= fetched {
		t.Error("refresh fetched nothing")
	}

	// A failing refresh keeps the old copy and says why.
	srv.status["repository2-3.xml"] = http.StatusInternalServerError
	failed := load(t, r, true)
	if failed.State != "cached" || failed.Model != refreshed.Model || failed.Cause == "" || !failed.FetchedAt.Equal(refreshed.FetchedAt) {
		t.Errorf("failing refresh = %s %q, want the old copy with a cause", failed.State, failed.Cause)
	}

	// The copy expires after an hour: the next call fetches, and failing, keeps the copy.
	clock.advance(61 * time.Minute)
	expired := load(t, r, false)
	if expired.State != "cached" || expired.Cause == "" {
		t.Errorf("after an hour = %s %q, want a refetch that failed, with the copy kept", expired.State, expired.Cause)
	}
	delete(srv.status, "repository2-3.xml")
	if again := load(t, r, false); again.State != "read" {
		t.Errorf("after the server recovered = %s, want read", again.State)
	}
}

// TestRepositoryFetchesOnceUnderConcurrency pins that concurrent callers share one fetch.
func TestRepositoryFetchesOnceUnderConcurrency(t *testing.T) {
	srv := newFxServer(t, fxDefault())
	r := srv.repo()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); load(t, r, false) }()
	}
	wg.Wait()
	n := 0
	for _, p := range srv.requests() {
		if p == "repository2-3.xml" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("repository2-3.xml was fetched %d times by 8 concurrent calls, want 1", n)
	}
}

// TestLicenceTwoTexts pins that a licence is (id, sha1): the same id with two texts in two
// lists gives two licences, each package carries the hash of its own list's text.
func TestLicenceTwoTexts(t *testing.T) {
	files := fxDefault()
	files["sys-img/android/sys-img2-3.xml"] = fxImgList(fxOtherLicenceText, fxImage("28", "default", "arm64-v8a", "a.zip"))
	m := mustModel(t, newFxServer(t, files).repo())
	pt, _ := m.Package("platform-tools")
	img, _ := m.Package("system-images;android-28;default;arm64-v8a")
	if pt == nil || img == nil {
		t.Fatalf("model = %v", modelIDs(m))
	}
	if pt.License.ID != img.License.ID || pt.License.SHA1 == img.License.SHA1 {
		t.Fatalf("licences = %+v and %+v, want one id with two hashes", pt.License, img.License)
	}
	if pt.License.SHA1 != licenceHash(fxLicenceText) || img.License.SHA1 != licenceHash(fxOtherLicenceText) {
		t.Errorf("each package must carry the hash of the text its own list declares")
	}
	for _, ref := range []LicenceRef{pt.License, img.License} {
		if _, ok := m.Licences[ref]; !ok {
			t.Errorf("licence %+v is not in the model", ref)
		}
	}
}

// TestRevision pins the revision order: absent parts compare as 0, a final release is
// newer than its previews, and source.properties' forms parse.
func TestRevision(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"23.0", "23.0.0", 0},
		{"23.0", "22.9.9", 1},
		{"12.0", "19.0", -1},
		{"36.0.0", "36.0.0-rc1", 1},
		{"36.0.0-rc1", "36.0.0-rc2", -1},
		{"37.0.1", "37.0.0", 1},
		{"3", "10", -1},
		{"36.0.0 rc1", "36.0.0-rc1", 0},
	} {
		a, ok1 := ParseRevision(c.a)
		b, ok2 := ParseRevision(c.b)
		if !ok1 || !ok2 {
			t.Fatalf("%q or %q does not parse", c.a, c.b)
		}
		if got := a.Compare(b); got != c.want {
			t.Errorf("%s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"", "x", "1.2.3.4", "-1", "1.x"} {
		if _, ok := ParseRevision(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if r, _ := ParseRevision("23.0"); r.String() != "23.0" {
		t.Errorf("23.0 prints as %s", r)
	}
}

// TestNaturalOrder pins the id order sdk.packages lists in: numbers compare as numbers.
func TestNaturalOrder(t *testing.T) {
	in := []string{"build-tools;9.0.0", "build-tools;36.1.0", "build-tools;35.0.0", "build-tools;10.0.0"}
	want := []string{"build-tools;36.1.0", "build-tools;35.0.0", "build-tools;10.0.0", "build-tools;9.0.0"}
	sortByCategory(in, func(s string) string { return s })
	if !slices.Equal(in, want) {
		t.Errorf("order = %v, want %v", in, want)
	}
}
