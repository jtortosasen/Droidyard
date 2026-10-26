package sdk

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Synthetic repository XML shaped by the spike's §(b), served by an httptest TLS server.
// No Google licence text is committed: the licences below are written for these tests.

// fxLicenceText is a synthetic licence text with the shapes the hash rule treats: an
// indented line, single newlines, a blank line and runs of spaces.
const fxLicenceText = "\nSynthetic test licence.\n\n  Terms   that  are\nmade up for the tests.\n\n"

// fxOtherLicenceText is a second text for the same licence id.
const fxOtherLicenceText = "\nA different synthetic text for the same licence id.\n"

const (
	fxRepoRoot = `<?xml version='1.0' encoding='utf-8'?>
<sdk:sdk-repository xmlns:sdk="http://schemas.android.com/sdk/android/repo/repository2/03" xmlns:common="http://schemas.android.com/repository/android/common/02" xmlns:generic="http://schemas.android.com/repository/android/generic/02" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`
	fxRepoEnd = `</sdk:sdk-repository>`

	fxImgRoot = `<?xml version='1.0' encoding='utf-8'?>
<sys-img:sdk-sys-img xmlns:sys-img="http://schemas.android.com/sdk/android/repo/sys-img2/03" xmlns:generic="http://schemas.android.com/repository/android/generic/02" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`
	fxImgEnd = `</sys-img:sdk-sys-img>`

	fxGeneric = `<type-details xsi:type="generic:genericDetailsType"/>`
)

// fxChannels is the channel table both kinds of list carry.
const fxChannels = `<channel id="channel-0">stable</channel><channel id="channel-1">beta</channel><channel id="channel-2">dev</channel>`

// fxLicences declares the licences a list uses.
func fxLicences(text string) string {
	return `<license id="android-sdk-license" type="text">` + text + `</license>` +
		`<license id="android-sdk-preview-license" type="text">A synthetic preview licence.</license>`
}

// fxSum is a 40-character hex checksum made from n.
func fxSum(n int) string { return fmt.Sprintf("%040x", n) }

// fxArchive is one archive element; os and arch are the repository's names ("macosx",
// "aarch64") or empty.
func fxArchive(os, arch string, size int, url string) string {
	s := fmt.Sprintf(`<archive><complete><size>%d</size><checksum type="sha1">%s</checksum><url>%s</url></complete>`, size, fxSum(size), url)
	if os != "" {
		s += `<host-os>` + os + `</host-os>`
	}
	if arch != "" {
		s += `<host-arch>` + arch + `</host-arch>`
	}
	return s + `</archive>`
}

// fxRev is a revision element from "37.0.1", "23.0", "3" or "1.0.0-rc3".
func fxRev(rev string) string {
	rev, preview, _ := strings.Cut(rev, "-rc")
	parts := strings.Split(rev, ".")
	var b strings.Builder
	b.WriteString("<revision>")
	for i, tag := range []string{"major", "minor", "micro"} {
		if i < len(parts) {
			b.WriteString("<" + tag + ">" + parts[i] + "</" + tag + ">")
		}
	}
	if preview != "" {
		b.WriteString("<preview>" + preview + "</preview>")
	}
	return b.String() + "</revision>"
}

// fxPkg is one remotePackage.
type fxPkg struct {
	Path     string
	Channel  string // default channel-0
	License  string // default android-sdk-license
	Rev      string // default 1
	Display  string // default "Package <path>"
	Obsolete bool
	Details  string // the whole type-details element; default fxGeneric
	Deps     string // the whole dependencies element
	Archives string // the archive elements; default a macosx aarch64 and a linux x64 one
}

func (p fxPkg) xml() string {
	def := func(s, d string) string {
		if s == "" {
			return d
		}
		return s
	}
	archives := def(p.Archives, fxArchive("macosx", "aarch64", 1000, "pkg-darwin-arm64.zip")+fxArchive("linux", "x64", 900, "pkg-linux.zip"))
	obs := ""
	if p.Obsolete {
		obs = ` obsolete="true"`
	}
	return fmt.Sprintf(`<remotePackage%s path=%q>%s%s<display-name>%s</display-name><uses-license ref=%q/>%s<channelRef ref=%q/><archives>%s</archives></remotePackage>`,
		obs, p.Path, def(p.Details, fxGeneric), fxRev(def(p.Rev, "1")), def(p.Display, "Package "+p.Path),
		def(p.License, "android-sdk-license"), p.Deps, def(p.Channel, "channel-0"), archives)
}

// fxRepo is a repository2-3.xml with the given packages and licence text.
func fxRepo(licenceText string, pkgs ...fxPkg) string {
	var b strings.Builder
	b.WriteString(fxRepoRoot + fxLicences(licenceText) + fxChannels)
	for _, p := range pkgs {
		b.WriteString(p.xml())
	}
	return b.String() + fxRepoEnd
}

// fxImgList is a system-image list.
func fxImgList(licenceText string, pkgs ...fxPkg) string {
	var b strings.Builder
	b.WriteString(fxImgRoot + fxLicences(licenceText) + fxChannels)
	for _, p := range pkgs {
		b.WriteString(p.xml())
	}
	return b.String() + fxImgEnd
}

// fxImage is a system image package for abi, depending on the emulator.
func fxImage(api, tag, abi, archiveName string) fxPkg {
	return fxPkg{
		Path: fmt.Sprintf("system-images;android-%s;%s;%s", api, tag, abi),
		Rev:  "2",
		Details: `<type-details xsi:type="sys-img:sysImgDetailsType"><api-level>` + api + `</api-level><base-extension>true</base-extension>` +
			`<tag><id>` + tag + `</id><display>Synthetic ` + tag + `</display></tag><abi>` + abi + `</abi></type-details>`,
		Deps:     `<dependencies><dependency path="emulator"><min-revision><major>31</major><minor>1</minor><micro>1</micro></min-revision></dependency></dependencies>`,
		Archives: fxArchive("", "", 5000, archiveName),
	}
}

// fxSiteList is an addons_list-5.xml naming the given system-image list urls and one
// add-on list.
func fxSiteList(urls ...string) string {
	var b strings.Builder
	b.WriteString(`<common:site-list xmlns:sdk="http://schemas.android.com/sdk/android/addons-list/5" xmlns:common="http://schemas.android.com/repository/android/sites-common/1" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`)
	for _, u := range urls {
		b.WriteString(`<site xsi:type="sdk:sysImgSiteType"><displayName>Images</displayName><url>` + u + `</url></site>`)
	}
	b.WriteString(`<site xsi:type="sdk:addonSiteType"><displayName>Add-ons</displayName><url>addon2-3.xml</url></site>`)
	return b.String() + `</common:site-list>`
}

// fxDefault is a small, complete repository: platform-tools, an emulator on two
// channels, cmdline-tools with an archive per Mac architecture, a platform, build-tools,
// an obsolete platform, and a system image per ABI.
func fxDefault() map[string]string {
	repo := fxRepo(fxLicenceText,
		fxPkg{Path: "platform-tools", Rev: "37.0.1", Display: "Synthetic Platform-Tools",
			Archives: fxArchive("linux", "", 9000, "platform-tools_r37.0.1-linux.zip") + fxArchive("macosx", "", 16000, "platform-tools_r37.0.1-darwin.zip") + fxArchive("windows", "", 8000, "platform-tools_r37.0.1-win.zip")},
		fxPkg{Path: "emulator", Rev: "37.2.12", Display: "Synthetic Emulator"},
		fxPkg{Path: "emulator", Rev: "37.3.5", Channel: "channel-2", License: "android-sdk-preview-license"},
		fxPkg{Path: "cmdline-tools;latest", Rev: "23.0", Display: "Synthetic Command-line Tools",
			Archives: fxArchive("linux", "", 18000, "cmdline-linux.zip") + fxArchive("macosx", "x64", 15500, "cmdline-mac-x64.zip") + fxArchive("macosx", "aarch64", 15400, "cmdline-mac-arm64.zip")},
		fxPkg{Path: "platforms;android-36", Rev: "2", Display: "Synthetic Platform 36",
			Details: `<type-details xsi:type="sdk:platformDetailsType"><api-level>36</api-level><codename></codename><extension-level>17</extension-level><base-extension>true</base-extension><layoutlib api="15"/></type-details>`},
		fxPkg{Path: "platforms;android-34", Rev: "3", Obsolete: true},
		fxPkg{Path: "build-tools;36.1.0", Rev: "36.1.0"},
		fxPkg{Path: "build-tools;35.0.0", Rev: "35.0.0"},
	)
	return map[string]string{
		"repository2-3.xml": repo,
		"addons_list-5.xml": fxSiteList("sys-img/android/sys-img2-3.xml"),
		"sys-img/android/sys-img2-3.xml": fxImgList(fxLicenceText,
			fxImage("28", "default", "arm64-v8a", "arm64-v8a-28_r02.zip"),
			fxImage("28", "default", "x86_64", "x86_64-28_r02.zip")),
	}
}

// fxServer serves files by their path under /android/repository/ over TLS and logs every
// request path.
type fxServer struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string]string
	// status, when set for a path, answers that code instead of the file.
	status map[string]int
	// redirect, when set for a path, answers a 302 to that Location.
	redirect map[string]string
	log      []string
}

func newFxServer(t *testing.T, files map[string]string) *fxServer {
	t.Helper()
	s := &fxServer{files: files, status: map[string]int{}, redirect: map[string]string{}}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/android/repository/")
		s.mu.Lock()
		s.log = append(s.log, rel)
		body, ok := s.files[rel]
		code := s.status[rel]
		loc := s.redirect[rel]
		s.mu.Unlock()
		switch {
		case loc != "":
			http.Redirect(w, r, loc, http.StatusFound)
		case code != 0:
			w.WriteHeader(code)
		case !ok:
			http.NotFound(w, r)
		default:
			w.Write([]byte(body))
		}
	}))
	// A client that goes away mid-handshake is not news.
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

func (s *fxServer) base() string { return s.URL + "/android/repository/" }

// requests is the paths asked for so far.
func (s *fxServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func (s *fxServer) set(path, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = body
}

// repo is a Repository reading this server for a Mac on Apple silicon.
func (s *fxServer) repo(opts ...func(*RepositoryOptions)) *Repository {
	o := RepositoryOptions{Base: s.base(), Client: s.Client(), GOOS: "darwin", GOARCH: "arm64"}
	for _, f := range opts {
		f(&o)
	}
	return NewRepository(o)
}

// fxClock is a settable clock.
type fxClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fxClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fxClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func itoa(n int) string { return strconv.Itoa(n) }
