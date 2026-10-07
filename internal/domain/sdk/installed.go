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
// locations (sdk.md §Reading what is installed). The singletons are absent when the
// package is not installed; a directory without a package.xml contributes nothing.
type Installed struct {
	Platforms     []Package `json:"platforms" jsonschema:"required" jsonschema_description:"Installed platforms, from platforms/*/package.xml."`
	BuildTools    []Package `json:"buildTools" jsonschema:"required" jsonschema_description:"Installed build-tools, from build-tools/*/package.xml."`
	PlatformTools *Package  `json:"platformTools,omitempty" jsonschema_description:"platform-tools, from platform-tools/package.xml, when installed."`
	CmdlineTools  []Package `json:"cmdlineTools" jsonschema:"required" jsonschema_description:"Installed cmdline-tools, from cmdline-tools/*/package.xml."`
	Emulator      *Package  `json:"emulator,omitempty" jsonschema_description:"The emulator, from emulator/package.xml, when installed."`
	SystemImages  []Package `json:"systemImages" jsonschema:"required" jsonschema_description:"Installed system images, from system-images/*/*/*/package.xml."`
}

// location is one fixed place a package.xml lives and the field of Installed it fills.
var locations = []struct {
	pattern string
	kind    string
}{
	{"platforms/*/package.xml", "platforms"},
	{"build-tools/*/package.xml", "buildTools"},
	{"platform-tools/package.xml", "platformTools"},
	{"cmdline-tools/*/package.xml", "cmdlineTools"},
	{"emulator/package.xml", "emulator"},
	{"system-images/*/*/*/package.xml", "systemImages"},
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

// ReadInstalled reads what root has installed, from the package.xml files at the fixed
// locations. It reads fresh on every call: nothing here is cached. A malformed
// package.xml, a file larger than the bound, or a tree with more than maxPackages files
// is skipped and noted, never fatal.
func ReadInstalled(root string) (Installed, []string) {
	inst := emptyInstalled()
	var warnings []string

	type match struct{ kind, path string }
	var matches []match
	for _, l := range locations {
		found, err := filepath.Glob(filepath.Join(root, l.pattern))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", l.pattern, err))
			continue
		}
		for _, m := range found {
			matches = append(matches, match{l.kind, m})
		}
	}
	if len(matches) > maxPackages {
		warnings = append(warnings, fmt.Sprintf("the SDK has more than %d packages; only the first %d were read", maxPackages, maxPackages))
		matches = matches[:maxPackages]
	}
	for _, m := range matches {
		p, note := readPackage(m.path)
		if note != "" {
			warnings = append(warnings, note)
			continue
		}
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

// readPackage reads one package.xml. A file that cannot be read or parsed, or is larger
// than maxPackageBytes, returns a note and no package.
func readPackage(path string) (Package, string) {
	fi, err := os.Stat(path)
	if err != nil {
		return Package{}, fmt.Sprintf("%s: %v", path, err)
	}
	// A FIFO or a device never reports its size: only a regular file is read, and never
	// past the bound (D14; step 4 review N2).
	if !fi.Mode().IsRegular() {
		return Package{}, fmt.Sprintf("%s: not a regular file, skipped", path)
	}
	if fi.Size() > maxPackageBytes {
		return Package{}, fmt.Sprintf("%s: larger than %d bytes, skipped", path, maxPackageBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return Package{}, fmt.Sprintf("%s: %v", path, err)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxPackageBytes+1))
	f.Close()
	if err != nil {
		return Package{}, fmt.Sprintf("%s: %v", path, err)
	}
	if len(raw) > maxPackageBytes {
		return Package{}, fmt.Sprintf("%s: larger than %d bytes, skipped", path, maxPackageBytes)
	}
	var repo struct {
		Local []struct {
			Path     string `xml:"path,attr"`
			Revision struct {
				Major string `xml:"major"`
				Minor string `xml:"minor"`
				Micro string `xml:"micro"`
			} `xml:"revision"`
		} `xml:"localPackage"`
	}
	if err := xml.Unmarshal(raw, &repo); err != nil {
		return Package{}, fmt.Sprintf("%s: %v", path, err)
	}
	if len(repo.Local) == 0 {
		return Package{}, fmt.Sprintf("%s: no <localPackage>", path)
	}
	lp := repo.Local[0]
	return Package{ID: lp.Path, Version: revision(lp.Revision.Major, lp.Revision.Minor, lp.Revision.Micro)}, ""
}

// revision joins the parts of a package.xml <revision> that are present, e.g. "35.0.0",
// "36.6.11", or "3" for a platform whose revision is a single number.
func revision(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p == "" {
			break
		}
		out = append(out, p)
	}
	return strings.Join(out, ".")
}
