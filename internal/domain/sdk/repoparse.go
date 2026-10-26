package sdk

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The repository's XML, decoded into structs that hold only the elements the model reads
// (the spike's §(b)), and converted into the model with every id, version and URL
// validated: nothing a repository says reaches a path, a command line or an XML file of
// ours unchecked (ADR-0015 §1, extended by ADR-0129 Q3).

// Patterns an id must match before it can reach a path later. Every component of a
// package id begins with a letter or digit (the first with a lowercase letter), so no
// component is "." or "..", and a licence id is a short lowercase word.
var (
	packageIDPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9._-]*(;[A-Za-z0-9][A-Za-z0-9._-]*)*$`)
	licenseIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	checksumTypeRe   = regexp.MustCompile(`^[a-z0-9-]{1,16}$`)
	checksumValueRe  = regexp.MustCompile(`^[0-9a-fA-F]{1,128}$`)
	xmlNameRe        = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)
	typeLocalRe      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)
)

const (
	maxPackageIDLen  = 255
	maxTypeDetails   = 16 << 10 // the re-encoded children of one type-details
	maxTypeDetailsIn = 64 << 10 // what is buffered of one before it is judged too large
	maxLicenceText   = 128 << 10
	maxLicencesList  = 64
	maxDisplayName   = 200
	maxArchiveSize   = int64(1) << 40
	stableChannel    = "channel-0"
	xsiNamespace     = "http://www.w3.org/2001/XMLSchema-instance"
)

// typeDetailsNamespaces are the namespaces step 1 (c) found in the classic tool's
// package.xml preamble: the only ones a type-details xsi:type may resolve to, since the
// installer writes the element under the preamble's prefix for its namespace.
var typeDetailsNamespaces = map[string]bool{
	"http://schemas.android.com/repository/android/common/01":    true,
	"http://schemas.android.com/repository/android/common/02":    true,
	"http://schemas.android.com/repository/android/generic/01":   true,
	"http://schemas.android.com/repository/android/generic/02":   true,
	"http://schemas.android.com/sdk/android/repo/repository2/01": true,
	"http://schemas.android.com/sdk/android/repo/repository2/02": true,
	"http://schemas.android.com/sdk/android/repo/repository2/03": true,
	"http://schemas.android.com/sdk/android/repo/repository2/04": true,
	"http://schemas.android.com/sdk/android/repo/addon2/01":      true,
	"http://schemas.android.com/sdk/android/repo/addon2/02":      true,
	"http://schemas.android.com/sdk/android/repo/addon2/03":      true,
	"http://schemas.android.com/sdk/android/repo/addon2/04":      true,
	"http://schemas.android.com/sdk/android/repo/sys-img2/01":    true,
	"http://schemas.android.com/sdk/android/repo/sys-img2/02":    true,
	"http://schemas.android.com/sdk/android/repo/sys-img2/03":    true,
	"http://schemas.android.com/sdk/android/repo/sys-img2/04":    true,
	"http://schemas.android.com/sdk/android/repo/sys-img2/05":    true,
}

// TypeDetails is a package's type-details, kept for the package.xml the installer writes:
// the namespace and local name its xsi:type resolved to, and its children as XML text
// re-encoded token by token (unprefixed elements and attributes, text; nothing else).
type TypeDetails struct {
	Namespace string
	Local     string
	Children  string
}

// LicenceRef names the licence a package uses: its id and the sha1 sdklib's hash gives
// the text its own list declares for that id.
type LicenceRef struct {
	ID   string
	SHA1 string
}

// Dependency is another package this one needs, at least at Min when Min is not nil.
type Dependency struct {
	ID  string
	Min *Revision
}

// Archive is the complete archive of a package for this host. URL is absolute, on the
// repository's host and inside its base path. ChecksumType is what the repository says
// the checksum is; only "sha1" is served and the installer refuses any other.
type Archive struct {
	URL          string
	Size         int64
	Checksum     string
	ChecksumType string
}

// RemotePackage is one package the repository offers on the stable channel for this host.
type RemotePackage struct {
	ID           string
	Revision     Revision
	DisplayName  string
	Obsolete     bool
	License      LicenceRef
	Dependencies []Dependency
	Archive      Archive
	TypeDetails  TypeDetails
}

// Licence is a licence text a list declares, with its sdklib hash.
type Licence struct {
	ID   string
	SHA1 string
	Text string
}

// the raw shapes ---------------------------------------------------------------------

type rawLicense struct {
	ID   string `xml:"id,attr"`
	Text string `xml:",chardata"`
}

type rawRevision struct {
	Major   string `xml:"major"`
	Minor   string `xml:"minor"`
	Micro   string `xml:"micro"`
	Preview string `xml:"preview"`
}

type rawArchive struct {
	HostOS   string `xml:"host-os"`
	HostArch string `xml:"host-arch"`
	Complete *struct {
		Size     string `xml:"size"`
		Checksum struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"checksum"`
		URL string `xml:"url"`
	} `xml:"complete"`
}

type rawDependency struct {
	Path        string       `xml:"path,attr"`
	MinRevision *rawRevision `xml:"min-revision"`
}

type rawPackage struct {
	Path        string         `xml:"path,attr"`
	Obsolete    string         `xml:"obsolete,attr"`
	TypeDetails rawTypeDetails `xml:"type-details"`
	Revision    rawRevision    `xml:"revision"`
	DisplayName string         `xml:"display-name"`
	UsesLicense struct {
		Ref string `xml:"ref,attr"`
	} `xml:"uses-license"`
	ChannelRef struct {
		Ref string `xml:"ref,attr"`
	} `xml:"channelRef"`
	Dependencies []rawDependency `xml:"dependencies>dependency"`
	Archives     []rawArchive    `xml:"archives>archive"`
}

// rawTypeDetails buffers a type-details element's attributes and tokens for judging once
// the document's root namespaces are known.
type rawTypeDetails struct {
	present bool
	start   xml.StartElement
	tokens  []xml.Token
	size    int
	over    bool
}

func (t *rawTypeDetails) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*t = rawTypeDetails{present: true, start: xml.CopyToken(start).(xml.StartElement)}
	depth := 1
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
			if depth == 0 {
				return nil
			}
		}
		if t.over {
			continue
		}
		t.size += tokenSize(tok)
		if t.size > maxTypeDetailsIn {
			t.over, t.tokens = true, nil
			continue
		}
		t.tokens = append(t.tokens, xml.CopyToken(tok))
	}
}

func tokenSize(tok xml.Token) int {
	switch x := tok.(type) {
	case xml.StartElement:
		n := len(x.Name.Local) + len(x.Name.Space) + 2
		for _, a := range x.Attr {
			n += len(a.Name.Local) + len(a.Name.Space) + len(a.Value) + 4
		}
		return n
	case xml.EndElement:
		return len(x.Name.Local) + 3
	case xml.CharData:
		return len(x)
	case xml.Comment:
		return len(x) + 7
	case xml.ProcInst:
		return len(x.Target) + len(x.Inst) + 5
	case xml.Directive:
		return len(x) + 3
	}
	return 1
}

// the parsed list --------------------------------------------------------------------

// drops counts what a list's validation refused, by reason; the model reports the totals
// in one warning, never the offending text.
type drops map[string]int

func (d drops) add(reason string) { d[reason]++ }

// parsedList is one list after validation: its stable packages (one per id), the
// licences it declares and what was dropped.
type parsedList struct {
	packages []*RemotePackage
	licences map[string]Licence
	drops    drops
	seen     int // remotePackage elements read, for the 20,000 bound
}

var errDirective = errors.New("the document has a DOCTYPE or other directive, which is not accepted")

type parseEnv struct {
	base     *url.URL // the repository base, with a trailing slash
	listPath string   // this list's path relative to the base ("sys-img/android/sys-img2-3.xml")
	hostOS   string   // the repository's name for this host's OS ("macosx")
	hostArch string   // ... and architecture ("aarch64")
	abi      string   // this host's system-image ABI, "" when it has none
	maxPkgs  int
}

// parseList decodes a repository list (repository2-3.xml or a system-image list).
func parseList(data []byte, env parseEnv) (*parsedList, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	out := &parsedList{licences: map[string]Licence{}, drops: drops{}}
	scope := map[string]string{} // the root's xmlns:prefix declarations
	best := map[string]*RemotePackage{}
	var order []string
	inRoot := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.Directive:
			return nil, errDirective
		case xml.StartElement:
			if !inRoot {
				inRoot = true
				for _, a := range t.Attr {
					if a.Name.Space == "xmlns" {
						scope[a.Name.Local] = a.Value
					}
				}
				continue
			}
			switch t.Name.Local {
			case "license":
				var l rawLicense
				if err := d.DecodeElement(&l, &t); err != nil {
					return nil, err
				}
				out.addLicence(l)
			case "remotePackage":
				var rp rawPackage
				if err := d.DecodeElement(&rp, &t); err != nil {
					return nil, err
				}
				out.seen++
				if out.seen > env.maxPkgs {
					return nil, fmt.Errorf("more than %d packages", env.maxPkgs)
				}
				pkg := convertPackage(rp, scope, env, out)
				if pkg == nil {
					continue
				}
				if old, ok := best[pkg.ID]; ok {
					// One entry per id: the same id twice on the stable channel keeps the
					// newer revision.
					if pkg.Revision.Compare(old.Revision) > 0 {
						best[pkg.ID] = pkg
					}
					continue
				}
				best[pkg.ID] = pkg
				order = append(order, pkg.ID)
			default:
				if err := d.Skip(); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, id := range order {
		out.packages = append(out.packages, best[id])
	}
	// A package whose licence its own list does not declare cannot be installed lawfully:
	// dropped, counted.
	kept := out.packages[:0]
	for _, p := range out.packages {
		l, ok := out.licences[p.License.ID]
		if !ok {
			out.drops.add("licence not declared by the package's list")
			continue
		}
		p.License.SHA1 = l.SHA1
		kept = append(kept, p)
	}
	out.packages = kept
	return out, nil
}

func (p *parsedList) addLicence(l rawLicense) {
	if !licenseIDPattern.MatchString(l.ID) {
		p.drops.add("invalid licence id")
		return
	}
	if len(p.licences) >= maxLicencesList || len(l.Text) > maxLicenceText {
		p.drops.add("licence over its bound")
		return
	}
	if _, dup := p.licences[l.ID]; dup {
		p.drops.add("licence declared twice in one list")
		return
	}
	p.licences[l.ID] = Licence{ID: l.ID, SHA1: licenceHash(l.Text), Text: l.Text}
}

// convertPackage validates one remotePackage into the model, or returns nil: silently
// for what simply is not for this host (not stable, another OS, architecture or ABI) and
// with a count in drops for what is malformed.
func convertPackage(rp rawPackage, scope map[string]string, env parseEnv, out *parsedList) *RemotePackage {
	if rp.ChannelRef.Ref != stableChannel {
		return nil
	}
	if len(rp.Path) > maxPackageIDLen || !packageIDPattern.MatchString(rp.Path) {
		out.drops.add("invalid package id")
		return nil
	}
	if strings.HasPrefix(rp.Path, "system-images;") {
		comps := strings.Split(rp.Path, ";")
		if env.abi == "" || comps[len(comps)-1] != env.abi {
			return nil
		}
	}
	arc, ok := pickArchive(rp.Archives, env, out)
	if !ok {
		return nil
	}
	if !licenseIDPattern.MatchString(rp.UsesLicense.Ref) {
		out.drops.add("invalid licence id")
		return nil
	}
	rev, ok := revisionOf(rp.Revision.Major, rp.Revision.Minor, rp.Revision.Micro, rp.Revision.Preview)
	if !ok {
		out.drops.add("invalid revision")
		return nil
	}
	var deps []Dependency
	for _, rd := range rp.Dependencies {
		if len(rd.Path) > maxPackageIDLen || !packageIDPattern.MatchString(rd.Path) {
			out.drops.add("invalid dependency")
			return nil
		}
		dep := Dependency{ID: rd.Path}
		if rd.MinRevision != nil {
			min, ok := revisionOf(rd.MinRevision.Major, rd.MinRevision.Minor, rd.MinRevision.Micro, rd.MinRevision.Preview)
			if !ok {
				out.drops.add("invalid dependency")
				return nil
			}
			dep.Min = &min
		}
		deps = append(deps, dep)
	}
	td, ok := keepTypeDetails(rp.TypeDetails, scope)
	if !ok {
		out.drops.add("unusable type-details")
		return nil
	}
	return &RemotePackage{
		ID:           rp.Path,
		Revision:     rev,
		DisplayName:  cleanText(rp.DisplayName, maxDisplayName),
		Obsolete:     strings.TrimSpace(rp.Obsolete) == "true",
		License:      LicenceRef{ID: rp.UsesLicense.Ref},
		Dependencies: deps,
		Archive:      arc,
		TypeDetails:  td,
	}
}

// pickArchive chooses the complete archive for this host: one whose host-os and
// host-arch, when present, are this host's, the most specific first. ok is false when
// there is none. An archive for this host that is unusable (a URL the base does not own,
// a size or checksum that does not parse) is counted in drops and not chosen.
func pickArchive(archives []rawArchive, env parseEnv, out *parsedList) (arc Archive, ok bool) {
	bestScore := -1
	for _, a := range archives {
		os, arch := strings.TrimSpace(a.HostOS), strings.TrimSpace(a.HostArch)
		if (os != "" && os != env.hostOS) || (arch != "" && arch != env.hostArch) {
			continue
		}
		score := 0
		if os != "" {
			score++
		}
		if arch != "" {
			score++
		}
		if score <= bestScore {
			continue
		}
		cand, reason := convertArchive(a, env)
		if reason != "" {
			out.drops.add(reason)
			continue
		}
		arc, ok, bestScore = cand, true, score
	}
	return arc, ok
}

func convertArchive(a rawArchive, env parseEnv) (Archive, string) {
	if a.Complete == nil {
		return Archive{}, "archive without a complete download"
	}
	size, err := strconv.ParseInt(strings.TrimSpace(a.Complete.Size), 10, 64)
	if err != nil || size <= 0 || size > maxArchiveSize {
		return Archive{}, "archive with an invalid size"
	}
	ctype := strings.TrimSpace(a.Complete.Checksum.Type)
	sum := strings.ToLower(strings.TrimSpace(a.Complete.Checksum.Value))
	if !checksumTypeRe.MatchString(ctype) || !checksumValueRe.MatchString(sum) {
		return Archive{}, "archive with an invalid checksum"
	}
	u, ok := archiveURL(env, strings.TrimSpace(a.Complete.URL))
	if !ok {
		return Archive{}, "archive URL outside the repository"
	}
	return Archive{URL: u, Size: size, Checksum: sum, ChecksumType: ctype}, ""
}

// archiveURL resolves an archive's url against the directory of its own list. The url is
// a bare file name or a relative path; an absolute URL, a host, a leading slash, a
// backslash, a query, or a path that climbs out of the base is refused.
func archiveURL(env parseEnv, raw string) (string, bool) {
	rel, ok := relativeToBase(raw)
	if !ok {
		return "", false
	}
	joined := path.Join(path.Dir(env.listPath), rel)
	if joined == ".." || strings.HasPrefix(joined, "../") || joined == "." {
		return "", false
	}
	u := *env.base
	u.Path = path.Join(env.base.Path, joined)
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	if !strings.HasPrefix(u.Path, env.base.Path) || u.Host != env.base.Host {
		return "", false
	}
	return u.String(), true
}

// relativeToBase is raw as a clean relative path, or false when it is anything else.
func relativeToBase(raw string) (string, bool) {
	if raw == "" || len(raw) > 1024 || strings.ContainsAny(raw, "\\\x00") {
		return "", false
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	if strings.HasPrefix(u.Path, "/") || u.Path == "" {
		return "", false
	}
	return u.Path, true
}

// keepTypeDetails judges and re-encodes a type-details element (see the package plan,
// step 2): its single attribute must be xsi:type, resolved against the document root's
// declarations to a namespace of the package.xml preamble and a plain local name; its
// children are re-encoded as unprefixed elements, unprefixed attributes and text, and
// nothing else (no namespace declaration, comment, processing instruction or
// directive), at most maxTypeDetails bytes.
func keepTypeDetails(t rawTypeDetails, scope map[string]string) (TypeDetails, bool) {
	if !t.present || t.over || t.start.Name.Space != "" {
		return TypeDetails{}, false
	}
	if len(t.start.Attr) != 1 || t.start.Attr[0].Name.Space != xsiNamespace || t.start.Attr[0].Name.Local != "type" {
		return TypeDetails{}, false
	}
	prefix, local, found := strings.Cut(strings.TrimSpace(t.start.Attr[0].Value), ":")
	if !found || prefix == "" || strings.Contains(local, ":") || !typeLocalRe.MatchString(local) {
		return TypeDetails{}, false
	}
	ns, known := scope[prefix]
	if !known || !typeDetailsNamespaces[ns] {
		return TypeDetails{}, false
	}
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	for _, tok := range t.tokens {
		switch x := tok.(type) {
		case xml.StartElement:
			if x.Name.Space != "" || !xmlNameRe.MatchString(x.Name.Local) {
				return TypeDetails{}, false
			}
			start := xml.StartElement{Name: xml.Name{Local: x.Name.Local}}
			for _, a := range x.Attr {
				if a.Name.Space != "" || a.Name.Local == "xmlns" || !xmlNameRe.MatchString(a.Name.Local) {
					return TypeDetails{}, false
				}
				start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: a.Name.Local}, Value: a.Value})
			}
			if enc.EncodeToken(start) != nil {
				return TypeDetails{}, false
			}
		case xml.EndElement:
			if enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: x.Name.Local}}) != nil {
				return TypeDetails{}, false
			}
		case xml.CharData:
			if strings.TrimSpace(string(x)) == "" {
				continue
			}
			if enc.EncodeToken(x) != nil {
				return TypeDetails{}, false
			}
		default:
			return TypeDetails{}, false
		}
		if err := enc.Flush(); err != nil || buf.Len() > maxTypeDetails {
			return TypeDetails{}, false
		}
	}
	if enc.Flush() != nil || buf.Len() > maxTypeDetails {
		return TypeDetails{}, false
	}
	return TypeDetails{Namespace: ns, Local: local, Children: buf.String()}, true
}

// cleanText is s without control and format characters, trimmed and cut to max runes.
func cleanText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			r = ' '
		}
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// the site list ------------------------------------------------------------------------

// siteListURLs reads addons_list-5.xml and returns the url of every system-image site (an
// add-on site is not read: add-ons ended at API 24).
func siteListURLs(data []byte) ([]string, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	type site struct {
		Type string `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
		URL  string `xml:"url"`
	}
	var urls []string
	inRoot := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return urls, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.Directive:
			return nil, errDirective
		case xml.StartElement:
			if !inRoot {
				inRoot = true
				continue
			}
			if t.Name.Local != "site" {
				if err := d.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			var s site
			if err := d.DecodeElement(&s, &t); err != nil {
				return nil, err
			}
			_, local, found := strings.Cut(s.Type, ":")
			if !found {
				local = s.Type
			}
			if local == "sysImgSiteType" {
				urls = append(urls, strings.TrimSpace(s.URL))
			}
		}
	}
}
