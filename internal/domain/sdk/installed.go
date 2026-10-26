package sdk

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Bounds on reading what is installed (plan m1h, D14): at most this many package.xml
// files are read, each at most this large, so a pathological or hostile SDK tree cannot
// make the walk unbounded.
const (
	maxPackages     = 2000
	maxPackageBytes = 1 << 20
)

// Package is one installed SDK package: its id, the package.xml `path` (for example
// "platforms;android-35" or "build-tools;35.0.0"), and its version, the file's
// revision.
type Package struct {
	ID      string `json:"id" jsonschema:"required" jsonschema_description:"The package id: the package.xml path attribute, e.g. \"platforms;android-35\"."`
	Version string `json:"version,omitempty" jsonschema_description:"The package's revision, e.g. \"35.0.0\"."`
}

// Installed is what a host SDK has, read from the package.xml files at its fixed
// locations (sdk.md §Reading what is installed), or from source.properties when a
// package has none. The singletons are absent when the package is not installed; a
// directory with neither file is not listed and is a warning.
type Installed struct {
	Platforms     []Package `json:"platforms" jsonschema:"required" jsonschema_description:"Installed platforms, from platforms/*/package.xml, or source.properties when a package has none."`
	BuildTools    []Package `json:"buildTools" jsonschema:"required" jsonschema_description:"Installed build-tools, from build-tools/*/package.xml, or source.properties when a package has none."`
	PlatformTools *Package  `json:"platformTools,omitempty" jsonschema_description:"platform-tools, from platform-tools/package.xml, or source.properties when it has none; absent when not installed."`
	CmdlineTools  []Package `json:"cmdlineTools" jsonschema:"required" jsonschema_description:"Installed cmdline-tools, from cmdline-tools/*/package.xml, or source.properties when a package has none."`
	Emulator      *Package  `json:"emulator,omitempty" jsonschema_description:"The emulator, from emulator/package.xml, or source.properties when it has none; absent when not installed."`
	SystemImages  []Package `json:"systemImages" jsonschema:"required" jsonschema_description:"Installed system images, from system-images/*/*/*/package.xml, or source.properties when a package has none."`
}

// location is one fixed place a package lives (a directory) and the field of Installed it
// fills.
var locations = []struct {
	pattern string
	kind    string
}{
	{"platforms/*", "platforms"},
	{"build-tools/*", "buildTools"},
	{"platform-tools", "platformTools"},
	{"cmdline-tools/*", "cmdlineTools"},
	{"emulator", "emulator"},
	{"system-images/*/*/*", "systemImages"},
}

// emptyInstalled is a zero Installed whose slices are non-nil, so a result marshals as
// [] rather than null.
func emptyInstalled() Installed {
	return Installed{
		Platforms:    []Package{},
		BuildTools:   []Package{},
		CmdlineTools: []Package{},
		SystemImages: []Package{},
	}
}

// located is an installed package and the directory it was read from.
type located struct {
	kind string
	Package
	dir string
}

// ReadInstalled reads what root has installed, from the package.xml (or, when a package
// has none, the source.properties) at the fixed locations. It reads fresh on every call:
// nothing here is cached. A malformed file, one larger than the bound, a tree with more
// than maxPackages packages, or a directory with neither file (an interrupted install:
// "incomplete package") is skipped and noted, never fatal.
func ReadInstalled(root string) (Installed, []string) {
	inst := emptyInstalled()
	found, warnings := walkInstalled(root)
	for _, m := range found {
		p := m.Package
		switch m.kind {
		case "platforms":
			inst.Platforms = append(inst.Platforms, p)
		case "buildTools":
			inst.BuildTools = append(inst.BuildTools, p)
		case "platformTools":
			inst.PlatformTools = &p
		case "cmdlineTools":
			inst.CmdlineTools = append(inst.CmdlineTools, p)
		case "emulator":
			inst.Emulator = &p
		case "systemImages":
			inst.SystemImages = append(inst.SystemImages, p)
		}
	}
	return inst, warnings
}

// walkInstalled reads every package directory at the fixed locations.
func walkInstalled(root string) ([]located, []string) {
	var warnings []string
	type match struct{ kind, dir string }
	var matches []match
	for _, l := range locations {
		dirs, err := filepath.Glob(filepath.Join(root, l.pattern))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", l.pattern, err))
			continue
		}
		for _, d := range dirs {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				matches = append(matches, match{l.kind, d})
			}
		}
	}
	if len(matches) > maxPackages {
		warnings = append(warnings, fmt.Sprintf("the SDK has more than %d packages; only the first %d were read", maxPackages, maxPackages))
		matches = matches[:maxPackages]
	}
	var out []located
	for _, m := range matches {
		p, found, note := readPackage(m.dir, layoutID(root, m.dir))
		switch {
		case note != "":
			warnings = append(warnings, note)
		case !found:
			rel, _ := filepath.Rel(root, m.dir)
			warnings = append(warnings, fmt.Sprintf("incomplete package at %s: it has neither package.xml nor source.properties", filepath.ToSlash(rel)))
		default:
			out = append(out, located{kind: m.kind, Package: p, dir: m.dir})
		}
	}
	return out, warnings
}

// layoutID is the package id the SDK layout gives a directory: its path under root with
// ";" for "/". A cmdline-tools directory a self-update left beside latest (latest-2) is
// the same package as latest.
func layoutID(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "cmdline-tools/latest-") {
		rel = "cmdline-tools/latest"
	}
	return strings.ReplaceAll(rel, "/", ";")
}

// readPackage reads the package in dir: package.xml, else source.properties (its
// Pkg.Revision; the id is the layout's, fallbackID). found is false when dir has neither
// file. A file that cannot be read or parsed, or is larger than maxPackageBytes, returns
// a note and no package.
func readPackage(dir, fallbackID string) (p Package, found bool, note string) {
	p, found, note = readPackageXML(filepath.Join(dir, "package.xml"))
	if found || note != "" {
		return p, found, note
	}
	return readSourceProperties(filepath.Join(dir, "source.properties"), fallbackID)
}

// readBounded reads a regular file of at most maxPackageBytes. exists is false when the
// file does not exist.
func readBounded(path string) (raw []byte, exists bool, note string) {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, false, ""
	}
	if err != nil {
		return nil, true, fmt.Sprintf("%s: %v", path, err)
	}
	// A FIFO or a device never reports its size: only a regular file is read, and never
	// past the bound (D14; step 4 review N2).
	if !fi.Mode().IsRegular() {
		return nil, true, fmt.Sprintf("%s: not a regular file, skipped", path)
	}
	if fi.Size() > maxPackageBytes {
		return nil, true, fmt.Sprintf("%s: larger than %d bytes, skipped", path, maxPackageBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, fmt.Sprintf("%s: %v", path, err)
	}
	raw, err = io.ReadAll(io.LimitReader(f, maxPackageBytes+1))
	f.Close()
	if err != nil {
		return nil, true, fmt.Sprintf("%s: %v", path, err)
	}
	if len(raw) > maxPackageBytes {
		return nil, true, fmt.Sprintf("%s: larger than %d bytes, skipped", path, maxPackageBytes)
	}
	return raw, true, ""
}

func readPackageXML(path string) (Package, bool, string) {
	raw, exists, note := readBounded(path)
	if !exists {
		return Package{}, false, ""
	}
	if note != "" {
		return Package{}, true, note
	}
	var repo struct {
		Local []struct {
			Path     string `xml:"path,attr"`
			Revision struct {
				Major   string `xml:"major"`
				Minor   string `xml:"minor"`
				Micro   string `xml:"micro"`
				Preview string `xml:"preview"`
			} `xml:"revision"`
		} `xml:"localPackage"`
	}
	if err := xml.Unmarshal(raw, &repo); err != nil {
		return Package{}, true, fmt.Sprintf("%s: %v", path, err)
	}
	if len(repo.Local) == 0 {
		return Package{}, true, fmt.Sprintf("%s: no <localPackage>", path)
	}
	lp := repo.Local[0]
	r := lp.Revision
	return Package{ID: lp.Path, Version: revision(r.Major, r.Minor, r.Micro, r.Preview)}, true, ""
}

func readSourceProperties(path, id string) (Package, bool, string) {
	raw, exists, note := readBounded(path)
	if !exists {
		return Package{}, false, ""
	}
	if note != "" {
		return Package{}, true, note
	}
	props, err := ParseProperties(strings.NewReader(string(raw)))
	if err != nil {
		return Package{}, true, fmt.Sprintf("%s: %v", path, err)
	}
	version := strings.TrimSpace(props["Pkg.Revision"])
	if r, ok := ParseRevision(version); ok {
		version = r.String()
	}
	return Package{ID: id, Version: version}, true, ""
}

// revision joins the parts of a package.xml <revision> that are present, e.g. "35.0.0",
// "36.6.11", or "3" for a platform whose revision is a single number; a preview adds
// "-rc3".
func revision(major, minor, micro, preview string) string {
	if r, ok := revisionOf(major, minor, micro, preview); ok {
		return r.String()
	}
	var out []string
	for _, p := range []string{major, minor, micro} {
		if p == "" {
			break
		}
		out = append(out, p)
	}
	return strings.Join(out, ".")
}

// packageDirs are the directories an id's package may be installed in: its layout
// directory, and for cmdline-tools;latest also the latest-N a self-update may leave. It
// reports false for a string that is not a package id.
func packageDirs(root, id string) ([]string, bool) {
	if len(id) > maxPackageIDLen || !packageIDPattern.MatchString(id) {
		return nil, false
	}
	if id == "cmdline-tools;latest" {
		dirs := []string{filepath.Join(root, "cmdline-tools", "latest")}
		if more, err := filepath.Glob(filepath.Join(root, "cmdline-tools", "latest-*")); err == nil {
			dirs = append(dirs, more...)
		}
		return dirs, true
	}
	return []string{filepath.Join(root, filepath.FromSlash(strings.ReplaceAll(id, ";", "/")))}, true
}

// installedOf reads, for every id the repository lists, the package at its layout
// directory <root>/<id with ";" as "/"> (an SDK's package path is its id): package.xml,
// else source.properties. It finds what the six fixed locations do not (ndk;27.0.1,
// sources;android-35). cmdline-tools;latest is the highest revision among cmdline-tools'
// latest and latest-N directories, since a self-update may leave latest-2. An id that does
// not match the package-id pattern is skipped. Malformed files are noted.
func installedOf(root string, ids []string) (map[string]located, []string) {
	out := map[string]located{}
	var warnings []string
	for _, id := range ids {
		dirs, ok := packageDirs(root, id)
		if !ok {
			continue
		}
		for _, d := range dirs {
			if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
				continue
			}
			p, found, note := readPackage(d, id)
			if note != "" {
				warnings = append(warnings, note)
				continue
			}
			if !found {
				continue
			}
			p.ID = id
			cand := located{Package: p, dir: d}
			if old, ok := out[id]; ok && !newerVersion(cand.Version, old.Version) {
				continue
			}
			out[id] = cand
		}
	}
	return out, warnings
}

// newerVersion reports whether a is a newer revision than b; two that do not parse are
// not ordered.
func newerVersion(a, b string) bool {
	ra, okA := ParseRevision(a)
	rb, okB := ParseRevision(b)
	switch {
	case okA && okB:
		return ra.Compare(rb) > 0
	case okA:
		return true
	}
	return false
}
