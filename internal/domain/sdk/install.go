package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// sdk.install and sdk.uninstall (plan m3f step 4; ADR-0129 §2, §5, §8; ADR-0130 §1, §2,
// §4, §5). Droidyard installs, updates and removes SDK packages itself, in Go: nothing is
// spawned, no JVM and no cmdline-tools are needed, and a host with no SDK gets the target
// root (Target) created by its first install. install.go is the plan, made before any
// download: what to install, what to update, whether every licence is accepted, whether
// there is room, whether anything to be replaced is in use. fetch.go does the work.

// The codes of the installer (ADR-0130 §Consequences; ADR-0129 §2, §8). A download,
// checksum or unpack failure is SDK_DOWNLOAD_FAILED; SDK_TOOL_FAILED is avdmanager's.
const (
	// ErrPackageNotFound: a requested id is not in the repository for this host, or a
	// package's dependency is not (or only below the revision needed), or the
	// dependencies form a cycle, or an id to uninstall is not installed. details.candidates
	// lists up to five ids close to an unknown request.
	ErrPackageNotFound capability.ErrorCode = "SDK_PACKAGE_NOT_FOUND"
	// ErrLicenseNotAccepted: the licence of a package to install, or of one of its
	// dependencies, is not accepted in the SDK root. Nothing was fetched. details.licenses
	// names each licence by id and hash with the packages under it; the next step is a
	// person's (ADR-0015 §6).
	ErrLicenseNotAccepted capability.ErrorCode = "SDK_LICENSE_NOT_ACCEPTED"
	// ErrNoSpace: the file system holding the SDK root lacks the space: for the archives
	// before any is fetched, or for one archive's unpacked files after it was fetched.
	ErrNoSpace capability.ErrorCode = "SDK_NO_SPACE"
	// ErrDownloadFailed: an archive could not be fetched, did not match its declared size
	// or sha1, was not an archive Droidyard unpacks, or a bound ran out. Nothing reached
	// the package's final path. details.installed lists the packages of the same call that
	// were already in place.
	ErrDownloadFailed capability.ErrorCode = "SDK_DOWNLOAD_FAILED"
	// ErrPackageInUse: a package that would be replaced or removed is in use, or was not
	// checked. details.packages names each and what uses it; the next step asks the user,
	// never the agent, to stop it (ADR-0129 §8).
	ErrPackageInUse capability.ErrorCode = "SDK_PACKAGE_IN_USE"
)

const (
	maxInstallPackages = 20
	maxCandidates      = 5
	// callBound and silentBound are ADR-0129 §6's working defaults for an install: the
	// whole call, and the time without a byte received.
	callBound   = 60 * time.Minute
	silentBound = 10 * time.Minute
	// tempMaxAge is how old a droidyard-* entry of .temp must be before the next call
	// removes it: a killed daemon leaves only these.
	tempMaxAge = time.Hour
	tempDir    = ".temp"
	tempPrefix = "droidyard-"
)

var (
	errCallBound = errors.New("the install ran past its time bound")
	errSilent    = errors.New("no data was received within the bound")
)

// InstallParams are sdk.install's parameters.
type InstallParams struct {
	Packages []string `json:"packages" jsonschema:"required,minItems=1,maxItems=20" jsonschema_description:"Package ids from sdk.packages. Dependencies are installed with them; an older installed one is updated."`
}

// InstalledPackage is a package the call put in place.
type InstalledPackage struct {
	ID      string `json:"id" jsonschema:"required" jsonschema_description:"The package id."`
	Version string `json:"version" jsonschema:"required" jsonschema_description:"The revision installed."`
}

// UpdatedPackage is a package the call replaced with a newer revision.
type UpdatedPackage struct {
	ID   string `json:"id" jsonschema:"required" jsonschema_description:"The package id."`
	From string `json:"from" jsonschema:"required" jsonschema_description:"The revision that was replaced; it is not kept."`
	To   string `json:"to" jsonschema:"required" jsonschema_description:"The revision now installed."`
}

// InstallResult is sdk.install's result.
type InstallResult struct {
	Root       string             `json:"root" jsonschema:"required" jsonschema_description:"The SDK root installed into: the host SDK, else the directory this call set up."`
	Created    bool               `json:"created" jsonschema:"required" jsonschema_description:"True when the root held no package before this call (it was absent, empty, or held only licenses/): the call set up an SDK."`
	Installed  []InstalledPackage `json:"installed" jsonschema:"required" jsonschema_description:"Packages newly installed, dependencies first."`
	Updated    []UpdatedPackage   `json:"updated" jsonschema:"required" jsonschema_description:"Packages replaced by a newer revision."`
	Downloaded int64              `json:"downloaded" jsonschema:"required" jsonschema_description:"Bytes of the archives fetched. Zero when every package was already at the latest revision."`
	Warnings   []string           `json:"warnings" jsonschema:"required" jsonschema_description:"Notes that do not stop the call."`
}

// UninstallParams are sdk.uninstall's parameters.
type UninstallParams struct {
	Packages []string `json:"packages" jsonschema:"required,minItems=1,maxItems=20" jsonschema_description:"Installed package ids, from sdk.packages."`
}

// UninstallResult is sdk.uninstall's result.
type UninstallResult struct {
	Removed  []string `json:"removed" jsonschema:"required" jsonschema_description:"The package ids removed."`
	Freed    int64    `json:"freed" jsonschema:"required" jsonschema_description:"Bytes the removed packages' files took, measured before removal with a bounded walk; a package past the bound counts nothing and the warnings say so."`
	Warnings []string `json:"warnings" jsonschema:"required" jsonschema_description:"Notes that do not stop the call."`
}

// installer serves both calls. The fields after env are seams: tests replace them, the
// shipped values are set by newInstaller.
type installer struct {
	env   []string
	repo  *Repository
	inUse InUseFunc
	log   io.Writer

	free   func(path string) (uint64, error)
	rename func(oldpath, newpath string) error
	now    func() time.Time
	// afterAside, when set, runs after an update moved the old directory aside; an error
	// ends the call as a kill would: nothing is put back and the temporary stays.
	afterAside func() error

	callBound, silentBound time.Duration
	maxEntries             int
	maxUncompressed        uint64
}

func newInstaller(o Options, repo *Repository) *installer {
	in := &installer{
		env: o.Env, repo: repo, inUse: o.InUse, log: o.Log,
		free: freeBytes, rename: os.Rename, now: time.Now,
		callBound: callBound, silentBound: silentBound,
		maxEntries: maxZipEntries, maxUncompressed: maxZipUncompressed,
	}
	if in.inUse == nil {
		in.inUse = notChecked
	}
	if in.log == nil {
		in.log = io.Discard
	}
	if o.tweak != nil {
		o.tweak(in)
	}
	return in
}

func registerInstaller(reg *capability.Registry, in *installer) {
	yes := true
	resource := func(*capability.Call) string {
		root, _ := Target(in.env)
		return "sdk:" + root
	}
	reg.MustRegister(capability.Descriptor{
		Name:        "sdk.install",
		Kind:        capability.Command,
		Scope:       capability.Global,
		LongRunning: true,
		Since:       "0.39.0",
		Description: "Installs SDK packages and their dependencies from Google's repository, dependencies first, updates an installed one that has a newer revision, and sets up the SDK root when the host has none. " +
			"Every licence must already be accepted by the user, a package in use is never replaced, and the call can be cancelled.",
		Idempotent: true,
		Params:     InstallParams{},
		Result:     InstallResult{},
		Errors: []capability.ErrorCode{
			capability.ErrInvalidParams, ErrRepositoryUnavailable, ErrNotWritable, ErrPackageNotFound,
			ErrLicenseNotAccepted, ErrNoSpace, ErrDownloadFailed, ErrPackageInUse,
		},
		Annotations: capability.Annotations{
			Destructive: &yes,
			OpenWorld:   &yes,
			Reason:      "an update replaces an installed package's revision and the older one is not kept (never one in use); downloads from Google's SDK repository and writes the SDK root",
		},
		Resource: resource,
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return in.install(ctx, call, call.Params.(InstallParams).Packages)
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name:        "sdk.uninstall",
		Kind:        capability.Command,
		Scope:       capability.Global,
		LongRunning: true,
		Audience:    capability.AudienceHuman,
		Since:       "0.39.0",
		Description: "Removes installed SDK packages from the SDK root and reports the bytes freed; a package in use is refused. A person's step, refused from agents.",
		Params:      UninstallParams{},
		Result:      UninstallResult{},
		Errors: []capability.ErrorCode{
			capability.ErrInvalidParams, ErrNotWritable, ErrPackageNotFound, ErrPackageInUse,
		},
		Annotations: capability.Annotations{
			Destructive: &yes,
			OpenWorld:   new(bool), // it reads no network
			Reason:      "deletes SDK packages that other projects and AVDs may use",
		},
		Resource: resource,
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return in.uninstall(ctx, call, call.Params.(UninstallParams).Packages)
		},
	})
}

// checkIDs validates a list of package ids: one to twenty, each a package id, repeats
// dropped. The pattern is the first guard of every path built from an id (the second is
// finalPath's).
func checkIDs(ids []string) ([]string, error) {
	if len(ids) == 0 || len(ids) > maxInstallPackages {
		return nil, capability.Errorf(capability.ErrInvalidParams, "packages has %d entries; give between 1 and %d", len(ids), maxInstallPackages)
	}
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		if len(id) > maxPackageIDLen || !packageIDPattern.MatchString(id) {
			return nil, capability.Errorf(capability.ErrInvalidParams, "%q is not a package id", cleanText(id, 80)).
				WithNextStep("take the ids from sdk.packages")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// step is one package of the plan: the remote package to put in place and, for an update,
// the installed one it replaces.
type step struct {
	pkg  *RemotePackage
	from *located
	lic  Licence // the text of pkg's licence, as the repository served it
}

// plan is what a call would do, decided before any download.
type plan struct {
	root     string
	created  bool
	steps    []step
	total    int64 // the archives' bytes
	warnings []string
}

func (p *plan) updates() []string {
	var ids []string
	for _, s := range p.steps {
		if s.from != nil {
			ids = append(ids, s.pkg.ID)
		}
	}
	return ids
}

// finalPath is where package id lives under root: the id with ";" as "/". The id's
// relative path must be local (no ".." and not absolute), whatever reached this function:
// the repository model validates ids by pattern, and this is the second guard (ADR-0129
// Q3). The path is returned relative as well, for messages.
func finalPath(root, id string) (abs, rel string, err error) {
	rel = filepath.FromSlash(strings.ReplaceAll(id, ";", "/"))
	if !filepath.IsLocal(rel) {
		return "", "", capability.NewError(ErrNotWritable, "the package id does not name a place inside the SDK").
			WithCause(fmt.Sprintf("%q would be installed outside %s", cleanText(id, 80), capability.Quote(root)))
	}
	return filepath.Join(root, rel), rel, nil
}

// makePlan decides a call: the target, the repository, the closure, consent, space, what
// is in the way and what is in use. It touches nothing.
func (in *installer) makePlan(ctx context.Context, call *capability.Call, ids []string) (*plan, error) {
	root, err := Target(in.env)
	if err != nil {
		return nil, err
	}
	snap := in.repo.Load(ctx, false)
	if snap.Model == nil {
		return nil, repositoryError(snap)
	}
	m := snap.Model
	p := &plan{root: root, created: heldNoPackage(root)}
	if snap.Cause != "" {
		p.warnings = append(p.warnings, "the repository could not be refreshed ("+snap.Cause+"): the copy read earlier was used")
	}

	for _, id := range ids {
		if _, ok := m.Package(id); !ok {
			return nil, notFound(m, id)
		}
	}
	var allIDs []string
	for _, rp := range m.Packages {
		allIDs = append(allIDs, rp.ID)
	}
	// A package.xml that cannot be read makes a package look not installed: it is then
	// found by the incomplete-directory check below and left alone, never replaced.
	installed, _ := installedSet(root, allIDs)
	if p.steps, err = closure(m, installed, ids); err != nil {
		return nil, err
	}
	for _, s := range p.steps {
		if s.pkg.Archive.ChecksumType != "sha1" {
			return nil, downloadFailed(s.pkg.ID, "the repository declares a "+cleanText(s.pkg.Archive.ChecksumType, 16)+" checksum for it, and sha1 is the only one Droidyard checks")
		}
		p.total += s.pkg.Archive.Size
	}

	// Consent: every licence of the closure, by (id, hash).
	if pending := pendingLicences(root, p.steps); len(pending) > 0 {
		return nil, licenceError(pending)
	}

	if len(p.steps) == 0 {
		return p, nil
	}

	// Space for the archives, the first of the two checks (ADR-0130 §2).
	if free, err := in.free(root); err != nil {
		p.warnings = append(p.warnings, "free space could not be measured: "+describeFS(err))
	} else if free < uint64(p.total) {
		return nil, capability.NewError(ErrNoSpace, "the disk does not have room for the downloads").
			WithCause(fmt.Sprintf("the archives take %d bytes and %d are free on the file system of %s", p.total, free, capability.Quote(root))).
			WithDetails(map[string]any{"needed": p.total, "free": free})
	}

	// A directory where a package that is not installed would go is an interrupted install
	// (or somebody's files): never removed, never installed over.
	for _, s := range p.steps {
		abs, rel, err := finalPath(root, s.pkg.ID)
		if err != nil {
			return nil, err
		}
		if s.from != nil {
			if filepath.Clean(s.from.dir) != abs {
				return nil, capability.NewError(ErrNotWritable, "the installed package is not where its id puts it").
					WithCause(fmt.Sprintf("%s is installed at %s; update it with the tool that put it there", s.pkg.ID, capability.Quote(relTo(root, s.from.dir))))
			}
			continue
		}
		if _, err := os.Lstat(abs); err == nil {
			return nil, capability.NewError(ErrNotWritable, "a directory is in the way of a package").
				WithCause(fmt.Sprintf("%s exists and is not an installed package (an interrupted install?); it was left alone", capability.Quote(filepath.ToSlash(rel)))).
				WithNextStep("the user removes that directory, then the install can run")
		}
	}

	// In use: only what is replaced; a fresh install replaces nothing.
	if ups := p.updates(); len(ups) > 0 {
		if used := in.inUse(ctx, root, ups); len(used) > 0 {
			return nil, inUseError(call, used, ups)
		}
	}
	return p, nil
}

// closure is the dependency closure of ids over the repository, ordered dependencies first.
// A package at the latest revision is dropped; an installed dependency that meets the
// min-revision asked of it is dropped, one below it is an update. A cycle, or a dependency
// the repository does not have for this host (or only below the revision needed), is
// SDK_PACKAGE_NOT_FOUND naming it.
func closure(m *Model, installed map[string]located, ids []string) ([]step, error) {
	// 1 on the way down, 2 settled as a step or at the latest, 3 kept as installed. A kept
	// id is judged again on every visit: a later request, or a higher min-revision, can
	// still make it an update, whatever order the ids came in.
	state := map[string]int{}
	var steps []step
	var visit func(id string, min *Revision, requested bool, via []string) error
	visit = func(id string, min *Revision, requested bool, via []string) error {
		switch state[id] {
		case 2:
			return nil
		case 1:
			return capability.NewError(ErrPackageNotFound, "the packages depend on each other in a cycle").
				WithCause(fmt.Sprintf("%s is needed by %s, which it needs", id, via[len(via)-1]))
		}
		rp, ok := m.Package(id)
		if !ok {
			return capability.NewError(ErrPackageNotFound, "a dependency is not available for this host").
				WithCause(fmt.Sprintf("%s is needed by %s and the repository does not offer it for this host", id, via[len(via)-1])).
				WithNextStep("call sdk.packages to see what the repository offers")
		}
		if min != nil && rp.Revision.Compare(*min) < 0 {
			return capability.NewError(ErrPackageNotFound, "a dependency is not available at the revision needed").
				WithCause(fmt.Sprintf("%s needs %s at least %s and the repository offers %s", via[len(via)-1], id, min, rp.Revision))
		}
		var from *located
		if l, have := installed[id]; have {
			cur, curOK := ParseRevision(l.Version)
			switch {
			case curOK && cur.Compare(rp.Revision) >= 0:
				state[id] = 2 // at the latest
				return nil
			case !requested && curOK && (min == nil || cur.Compare(*min) >= 0):
				state[id] = 3 // a dependency that is enough as it is, so far
				return nil
			}
			from = &l
		}
		state[id] = 1
		for _, d := range rp.Dependencies {
			if err := visit(d.ID, d.Min, false, append(via, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		steps = append(steps, step{pkg: rp, from: from, lic: m.Licences[rp.License]})
		return nil
	}
	for _, id := range ids {
		if err := visit(id, nil, true, nil); err != nil {
			return nil, err
		}
	}
	return steps, nil
}

// notFound is SDK_PACKAGE_NOT_FOUND for a requested id, with up to five candidates: the
// ids that begin with it, else the ids of its family (what precedes its last ";"), else
// the ids that contain it, newest first.
func notFound(m *Model, id string) error {
	var cands []string
	pick := func(match func(string) bool) {
		if len(cands) > 0 {
			return
		}
		for _, rp := range m.Packages {
			if !rp.Obsolete && match(rp.ID) {
				cands = append(cands, rp.ID)
			}
		}
	}
	low := strings.ToLower(id)
	pick(func(s string) bool { return strings.HasPrefix(strings.ToLower(s), low) })
	if i := strings.LastIndexByte(id, ';'); i > 0 {
		family := strings.ToLower(id[:i+1])
		pick(func(s string) bool { return strings.HasPrefix(strings.ToLower(s), family) })
	}
	pick(func(s string) bool { return strings.Contains(strings.ToLower(s), low) })
	sortByCategory(cands, func(s string) string { return s })
	if len(cands) > maxCandidates {
		cands = cands[:maxCandidates]
	}
	e := capability.NewError(ErrPackageNotFound, "the repository has no such package for this host").
		WithCause(fmt.Sprintf("%q is not a package id it offers", cleanText(id, 80))).
		WithNextStep("call sdk.packages to see the ids")
	if len(cands) > 0 {
		e.WithDetails(map[string]any{"candidates": cands})
	}
	return e
}

// pendingLicence is a licence of the closure that is not accepted, with its packages.
type pendingLicence struct {
	ref      LicenceRef
	packages []string
}

func pendingLicences(root string, steps []step) []pendingLicence {
	var out []pendingLicence
	index := map[LicenceRef]int{}
	for _, s := range steps {
		ref := s.pkg.License
		if licenseAccepted(root, ref.ID, ref.SHA1) {
			continue
		}
		i, ok := index[ref]
		if !ok {
			i = len(out)
			index[ref] = i
			out = append(out, pendingLicence{ref: ref})
		}
		out[i].packages = append(out[i].packages, s.pkg.ID)
	}
	return out
}

func licenceError(pending []pendingLicence) error {
	var details []map[string]any
	var names []string
	for _, p := range pending {
		details = append(details, map[string]any{"id": p.ref.ID, "sha1": p.ref.SHA1, "packages": p.packages})
		names = append(names, p.ref.ID)
	}
	return capability.NewError(ErrLicenseNotAccepted, "a licence these packages are under has not been accepted").
		WithCause("not accepted in the SDK root: " + strings.Join(names, ", ")).
		WithNextStep("the user accepts it: `droidyard sdk licenses`, or the SDK panel").
		WithDetails(map[string]any{"licenses": details})
}

// inUseError is SDK_PACKAGE_IN_USE for the ids the detector named. What uses a package is
// host text (a process name or path), so the failed call is marked untrusted.
func inUseError(call *capability.Call, used map[string][]string, order []string) error {
	var pkgs []map[string]any
	var names []string
	// The ids asked about, in order, then any other id the detector named.
	ids := append([]string(nil), order...)
	for _, id := range slices.Sorted(maps.Keys(used)) {
		if !slices.Contains(order, id) {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		by, ok := used[id]
		if !ok {
			continue
		}
		clean := make([]string, 0, len(by))
		for _, b := range by {
			clean = append(clean, cleanText(b, 200))
		}
		pkgs = append(pkgs, map[string]any{"id": id, "usedBy": clean})
		names = append(names, id)
	}
	yes := true
	call.Untrusted = &yes
	return capability.NewError(ErrPackageInUse, "a package is in use and was not touched").
		WithCause(strings.Join(names, ", ") + " in use; the details say by what").
		WithNextStep("ask the user to stop what uses it (an emulator, the adb server, a build), then run the call again").
		WithDetails(map[string]any{"packages": pkgs})
}

func downloadFailed(id, cause string) *capability.Error {
	return capability.NewError(ErrDownloadFailed, "a package could not be downloaded and installed").
		WithCause(id + ": " + cause).
		WithDetails(map[string]any{"package": id})
}

// heldNoPackage reports whether root holds no package: absent, empty, or only licenses/
// and .temp/ (ADR-0131 §3).
func heldNoPackage(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	for _, e := range entries {
		if n := e.Name(); n != "licenses" && n != tempDir {
			return false
		}
	}
	return true
}

// relTo is path relative to root, with "/" separators, for a message.
func relTo(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

// ensureTemp makes <root>/.temp (0755) and refuses one that is a link or not a directory.
func ensureTemp(root string) (string, error) {
	dir := filepath.Join(root, tempDir)
	switch fi, err := os.Lstat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", capability.NewError(ErrNotWritable, "the SDK's temporary directory cannot be made").
				WithCause(tempDir + ": " + describeFS(err))
		}
		_ = os.Chmod(dir, 0o755)
	case err != nil:
		return "", capability.NewError(ErrNotWritable, "the SDK's temporary directory cannot be read").
			WithCause(tempDir + ": " + describeFS(err))
	case fi.Mode()&fs.ModeSymlink != 0:
		return "", capability.NewError(ErrNotWritable, "the SDK's temporary directory is a symbolic link").
			WithCause(tempDir + " is a link; it was left alone")
	case !fi.IsDir():
		return "", capability.NewError(ErrNotWritable, "the SDK's temporary directory is not a directory").
			WithCause(tempDir + " is not a directory")
	}
	return dir, nil
}

// sweep removes the droidyard-* entries of .temp that are over an hour old: what a killed
// daemon left. Any other entry (the classic tool's) stays, and so does one holding
// "replaced": the old package of an update whose rollback failed, which the call's error
// told the user to recover from there.
func (in *installer) sweep(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, e.Name(), "replaced")); err == nil {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(dir, e.Name())); err == nil && in.now().Sub(fi.ModTime()) > tempMaxAge {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}
