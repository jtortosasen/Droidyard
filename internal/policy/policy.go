// Package policy reads the user configuration, which is the file trust is recorded in
// and every policy decision lives in (ADR-0015 §5–6; ADR-0021 §Location;
// ADR-0031; ADR-0032 §1). It is not state: it lives outside the state root, is written
// only by human channels (`droidyard trust`, `policy.update`) or by hand, and is never
// migrated or reset with the state.
//
// Trust is the project's (ADR-0081): a root that is a linked worktree of a trusted
// repository's main tree is trusted too, and matches `shell.roots` by the same rule.
// It never flows the other way, from a worktree to its main tree or to a sibling, and
// the proof is git's own files inside the trusted repository (platform.LinkedWorktree).
// `droidyard trust <path>` still trusts exactly the path it is given.
//
// Version 1 of the file has six sections: `trust`, `devices`, `shell`, `gradle`, `tools`
// (ADR-0032 §1) and `occupation` (ADR-0094 §7). Every key is optional; a `null` value is its default. Unknown
// keys, at the top level and inside a known section, are kept on every rewrite and
// ignored, so a newer droidyard's keys survive an older one's edit. Every path is
// absolute or the whole file is invalid (ADR-0032 §4): a relative entry would
// canonicalise against the process's cwd, trusting or enabling the wrong thing.
//
// The section a check reads — trust, shell, devices — is read from one size-and-mtime
// snapshot, so a hand edit or a `droidyard trust` takes effect without a restart
// (ADR-0031 §4; ADR-0032 §6). The tool keys are read once, at daemon start.
package policy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// FileName is the configuration file, in the configuration directory
// (plan m1g D1).
const FileName = "config.json"

// LockName is the exclusive lock beside it that serialises writers so two concurrent
// runs lose nothing (plan m1g D2).
const LockName = FileName + ".lock"

// schemaVersion is the only configuration schema version this binary reads and writes.
// A file of a newer version is treated like an unparseable one: nothing is trusted, no
// physical device is admitted, shell.exec stays off, no tool key is used, and it is
// never rewritten (ADR-0032 §4).
const schemaVersion = 1

// The configuration file's state, as system.health's config and policy.get report it
// (ADR-0032 §4, §6; plan m1k D1, D2).
const (
	// StateMissing is a configuration that is not there: the defaults apply.
	StateMissing = "missing"
	// StateOK is a configuration that parsed.
	StateOK = "ok"
	// StateInvalid is a configuration that could not be used; every check fails closed.
	StateInvalid = "invalid"
)

// Policy reads one configuration file with a size-and-mtime cache, so a hand edit or a
// `droidyard trust` is picked up without a restart (ADR-0015 §6) at the cost of a stat
// per check. The tool keys are read once, at construction.
type Policy struct {
	env  platform.Env
	path string
	// dirErr is why the configuration directory could not be resolved. It is reported
	// per call, not at construction: a daemon that reads no configuration still starts.
	dirErr error
	// startup is the read of the tool keys and the below-floor hatch, taken once at
	// construction: these keys take effect at the next daemon start (ADR-0032 §3, §6).
	startup Startup

	mu     sync.Mutex
	cached *snapshot
}

// snapshot is the parsed file and the size and mtime it was parsed from. err is set
// when the file exists but could not be used; present is false then.
type snapshot struct {
	present  bool
	size     int64
	mtime    time.Time
	doc      *document
	roots    []string // the trust entries, canonicalised
	prefixes []string // the trust prefixes, canonicalised
	err      *capability.Error
	// revision is the hex SHA-256 of the bytes this snapshot was parsed from, "absent"
	// when there is no file; "" when the bytes could not be read (ADR-0032 §5, Q2).
	revision string
}

// New returns the policy that reads <env's configuration directory>/config.json and
// reads the tool keys once. It does not touch the file when the directory cannot be
// resolved.
func New(env platform.Env) *Policy {
	if env.Getenv == nil {
		env = platform.Current()
	}
	dir, err := env.ConfigDir()
	p := &Policy{env: env, dirErr: err}
	if err == nil {
		// With no configuration directory there is no path to report: a relative
		// "config.json" would name a file in the daemon's working directory.
		p.path = filepath.Join(dir, FileName)
	}
	p.readStartup()
	return p
}

// Trusted reports whether root is trusted: equal to a `trust.roots` entry, or under a
// `trust.prefixes` entry at a path-component boundary, both canonicalised as a
// workspace root is (plan m1g D1), or a linked worktree of a root so trusted
// (ADR-0081 §1). A missing file trusts nothing. A file that does not
// parse trusts nothing and returns the error, whose cause names the file; it is never
// renamed or rewritten (it is the user's configuration, not state).
func (p *Policy) Trusted(root string) (bool, *capability.Error) {
	if p.dirErr != nil {
		return false, configurationError("", p.dirErr)
	}
	snap := p.snapshot()
	if snap.err != nil {
		return false, snap.err
	}
	if !snap.present {
		return false, nil
	}
	canonical := p.env.Canonical(root)
	if trustedBy(snap.roots, snap.prefixes, canonical) {
		return true, nil
	}
	// A linked worktree of a trusted project is trusted (ADR-0081 §1): the project is
	// what is trusted, never the worktree's own path, and only from the main tree to
	// its worktrees. Re-decided on every call; nothing is cached.
	if main, ok := platform.LinkedWorktree(p.env, canonical); ok {
		return trustedBy(snap.roots, snap.prefixes, main), nil
	}
	return false, nil
}

// Authorize returns the Descriptor.Authorize hook the capabilities that run project
// code install: it learns the root the call would operate on from rootOf, checks it
// against the configuration, and fails WORKSPACE_UNTRUSTED before the handler runs
// (R4, D3). The value it returns, the canonical root, reaches the handler as
// Call.Authorized. Trust is decided here, not in the handler, because the check reads
// the file and Start does no I/O (ADR-0020, amended 2026-10-04).
func (p *Policy) Authorize(rootOf RootOf) func(context.Context, *capability.Call) (any, error) {
	return func(_ context.Context, call *capability.Call) (any, error) {
		root, ok := rootOf(call)
		if !ok || root == "" {
			return nil, capability.Errorf(capability.ErrWorkspaceNotOpen, "the call names no project root, so its trust cannot be decided").
				WithNextStep("open a workspace, then retry")
		}
		canonical := p.env.Canonical(root)
		trusted, err := p.Trusted(canonical)
		if err != nil {
			return nil, err
		}
		if !trusted {
			// A worktree is asked to trust its project (ADR-0081 §5), not itself.
			if main, ok := platform.LinkedWorktree(p.env, canonical); ok {
				return nil, worktreeUntrustedError(canonical, main)
			}
			return nil, UntrustedError(canonical)
		}
		return canonical, nil
	}
}

// ShellAllowed reports whether shell.exec may run with root as its project root:
// `shell.enabled`, or root equal to or under a `shell.roots` entry at a path-component
// boundary, or a linked worktree of such a root (ADR-0032 §2; plan m1k D4; ADR-0081 §4).
// When it may not, the returned failure is
// POLICY_SHELL_DISABLED; its cause names the file when the configuration is invalid,
// and it always names the file in its nextStep. A missing file keeps the shell off.
func (p *Policy) ShellAllowed(root string) (bool, *capability.Error) {
	if p.dirErr != nil {
		return false, p.shellDisabled(root, p.dirErr.Error())
	}
	snap := p.snapshot()
	if snap.err != nil {
		return false, p.shellDisabled(root, snap.err.Cause)
	}
	if !snap.present {
		return false, p.shellDisabled(root, "")
	}
	if snap.doc.shell.enabled {
		return true, nil
	}
	canonical := p.env.Canonical(root)
	shellRoots := p.canonical(snap.doc.shell.roots)
	if trustedBy(nil, shellRoots, canonical) {
		return true, nil
	}
	// The shell is the project's choice too (ADR-0081 §4): a linked worktree of a
	// `shell.roots` project may run it.
	if main, ok := platform.LinkedWorktree(p.env, canonical); ok && trustedBy(nil, shellRoots, main) {
		return true, nil
	}
	return false, p.shellDisabled(root, "")
}

// Occupation is `occupation.conflict` and `occupation.staleAfterMinutes` (ADR-0094 §7):
// what a session that would cross another's worktree is told, and how long a session may
// go without a request before it is shown idle. The defaults are refuse and 120 minutes;
// they are also what a missing or unusable file gives, since refuse is the strictest of
// the three. Read live, by size and mtime, like the rest.
func (p *Policy) Occupation() (conflict string, staleAfter time.Duration) {
	conflict, staleAfter = capability.OccupationRefuse, capability.DefaultStaleAfter
	if p.dirErr != nil {
		return conflict, staleAfter
	}
	snap := p.snapshot()
	if snap.err != nil || !snap.present {
		return conflict, staleAfter
	}
	if c := snap.doc.occupation.conflict; c != "" {
		conflict = c
	}
	if n := snap.doc.occupation.staleAfterMinutes; n != 0 {
		staleAfter = time.Duration(n) * time.Minute
	}
	return conflict, staleAfter
}

// Admission is the physical-device policy's answer for one serial (ADR-0032 §2;
// plan m1k D7). It is a value, not an error, because the failure code DEVICE_NOT_ALLOWED
// belongs to the device domain, which shapes it from Cause.
type Admission struct {
	// Admitted is true when the serial is admitted: `devices.allowPhysical` and an
	// exact `devices.allowlist` entry admit it. Emulators never consult this.
	Admitted bool
	// Cause explains a refusal in the terms DEVICE_NOT_ALLOWED uses: `allowPhysical`
	// is false, the serial is not in the allowlist, or the configuration file could not
	// be used, by name (ADR-0032 §4).
	Cause string
	// Invalid is true when the refusal is because the configuration file could not be
	// used (or its directory resolved), as opposed to the file saying no: the user
	// meant something the file cannot say, and a listing tells them so.
	Invalid bool
}

// PhysicalAdmitted reports whether a physical device's exact serial is admitted. A
// missing file admits none (the default is off), and an invalid one admits none and
// names itself as the cause.
func (p *Policy) PhysicalAdmitted(serial string) Admission {
	if p.dirErr != nil {
		return Admission{Cause: p.dirErr.Error(), Invalid: true}
	}
	snap := p.snapshot()
	switch {
	case snap.err != nil:
		return Admission{Cause: snap.err.Cause, Invalid: true}
	case !snap.present:
		return Admission{Cause: "devices.allowPhysical is false"}
	case !snap.doc.devices.allowPhysical:
		return Admission{Cause: "devices.allowPhysical is false"}
	case !contains(snap.doc.devices.allowlist, serial):
		return Admission{Cause: fmt.Sprintf("%q is not in devices.allowlist", serial)}
	}
	return Admission{Admitted: true}
}

// Startup is the configuration as read once at daemon start: the tool keys and the
// below-floor hatch, which take effect only at the next start (ADR-0032 §3, §6;
// plan m1k D8).
type Startup struct {
	// ADB, Emulator and SidecarJavaHome are the configured binary and JVM paths,
	// empty when the file does not set them.
	ADB             string
	Emulator        string
	SidecarJavaHome string
	// AllowBelowFloor is `gradle.allowBelowFloor`, the Tier C hatch (ADR-0018 rule 3).
	AllowBelowFloor bool
	// State is how the read went: StateOK when the file parsed, StateMissing when there
	// was none, StateInvalid when it could not be used, in which case no key takes effect.
	State string
	// Error is the failure when State is StateInvalid.
	Error string
	// Path is the configuration file, so a resolution that cannot use a key names the
	// file it did not read (plan m1k D8).
	Path string
}

// ToolKey is one tool key as the resolution that uses it reads it: `tools.adb`,
// `tools.emulator` or `gradle.sidecarJavaHome`, read once at daemon start (ADR-0032 §3,
// §6; plan m1k D8). Path is the configured candidate, empty when the file does not set
// the key. When the file could not be used at all, Path is empty, ConfigPath names the
// file and Invalid says why: no key takes effect, and the resolution lists the file as a
// rejected candidate (ADR-0032 §4; D8).
type ToolKey struct {
	Path       string
	ConfigPath string
	Invalid    string
}

// ADBKey is `tools.adb` as device resolution reads it (plan m1k D8).
func (s Startup) ADBKey() ToolKey { return s.toolKey(s.ADB) }

// EmulatorKey is `tools.emulator` as emulator resolution reads it (plan m1k D8).
func (s Startup) EmulatorKey() ToolKey { return s.toolKey(s.Emulator) }

// JavaHomeKey is `gradle.sidecarJavaHome` as JVM discovery reads it (plan m1k D8).
func (s Startup) JavaHomeKey() ToolKey { return s.toolKey(s.SidecarJavaHome) }

// BelowFloor is `gradle.allowBelowFloor` as the Gradle client reads it: whether the
// Tier C hatch was set at daemon start, and the configuration file its refusal's
// nextStep names (ADR-0018 rule 3; ADR-0032 §3, §6; plan m1k D9). It is a value, not a
// bare bool, so the refusal can point at the file to edit.
type BelowFloor struct {
	// Allowed is whether `gradle.allowBelowFloor` was true at daemon start. It admits
	// Gradle 7.3–7.x; below 7.3 it changes nothing (ADR-0018 rule 3).
	Allowed bool
	// ConfigPath is the configuration file, named by the below-floor refusal so a human
	// knows which file to edit.
	ConfigPath string
	// Invalid is why the file could not be read at start, when it could not: the key was
	// then not read, and the refusal says so rather than ask for a key already set
	// (ADR-0032 §4; m1k close review N3).
	Invalid string
}

// BelowFloorKey is `gradle.allowBelowFloor` as read at daemon start. An invalid file
// sets no key, so Allowed is false and Gradle below the floor is refused (ADR-0032 §4).
func (s Startup) BelowFloorKey() BelowFloor {
	if s.State == StateInvalid {
		return BelowFloor{ConfigPath: s.Path, Invalid: s.Error}
	}
	return BelowFloor{Allowed: s.AllowBelowFloor, ConfigPath: s.Path}
}

// toolKey builds a ToolKey: the configured path when the file parsed, or the file and
// the reason it did not, when it did not (ADR-0032 §4; plan m1k D8).
func (s Startup) toolKey(path string) ToolKey {
	if s.State == StateInvalid {
		return ToolKey{ConfigPath: s.Path, Invalid: s.Error}
	}
	return ToolKey{Path: path, ConfigPath: s.Path}
}

// Startup returns the keys read once at daemon start.
func (p *Policy) Startup() Startup { return p.startup }

// readStartup reads the tool keys and the below-floor hatch once, from the same decode
// a live check uses (ADR-0032 §3, §6). It records, rather than reports, a bad file: the
// daemon still starts, and each capability the file affects says why.
func (p *Policy) readStartup() {
	if p.dirErr != nil {
		p.startup = Startup{State: StateInvalid, Error: p.dirErr.Error(), Path: p.path}
		return
	}
	data, err := readConfigFile(p.path)
	if errors.Is(err, fs.ErrNotExist) {
		p.startup = Startup{State: StateMissing, Path: p.path}
		return
	}
	if err != nil {
		p.startup = Startup{State: StateInvalid, Error: err.Error(), Path: p.path}
		return
	}
	doc, derr := decode(data)
	if derr != nil {
		p.startup = Startup{State: StateInvalid, Error: derr.Error(), Path: p.path}
		return
	}
	p.startup = Startup{
		ADB:             doc.tools.adb,
		Emulator:        doc.tools.emulator,
		SidecarJavaHome: doc.gradle.sidecarJavaHome,
		AllowBelowFloor: doc.gradle.allowBelowFloor,
		State:           StateOK,
		Path:            p.path,
	}
}

// ConfigStatus is the health report of the configuration file (ADR-0032 §4, §6;
// plan m1k D1): its path, its live state (a hand edit is seen without a restart), the
// parse error when it is invalid, and the tool keys' state as read at daemon start.
type ConfigStatus struct {
	Path  string `json:"path" jsonschema:"required" jsonschema_description:"The configuration file's path."`
	State string `json:"state" jsonschema:"required,enum=missing,enum=ok,enum=invalid" jsonschema_description:"missing when there is no file, ok when it parsed, invalid when it could not be used."`
	Error string `json:"error,omitempty" jsonschema_description:"The parse error, when state is invalid."`
	// ToolKeys is the state the tool keys were read in at daemon start: they take effect
	// only then, so this can differ from State (ADR-0032 §6).
	ToolKeys string `json:"toolKeys" jsonschema:"required,enum=missing,enum=ok,enum=invalid" jsonschema_description:"The state the tool keys were read in at daemon start."`
}

// ConfigStatus reports the file's live state and the tool keys' state as read at start.
func (p *Policy) ConfigStatus() ConfigStatus {
	cs := ConfigStatus{Path: p.path, ToolKeys: p.startup.State}
	if p.dirErr != nil {
		cs.State, cs.Error = StateInvalid, p.dirErr.Error()
		return cs
	}
	snap := p.snapshot()
	switch {
	case snap.err != nil:
		cs.State, cs.Error = StateInvalid, snap.err.Cause
	case snap.present:
		cs.State = StateOK
	default:
		cs.State = StateMissing
	}
	return cs
}

// TrustPolicy is the trust section as policy.get returns it.
type TrustPolicy struct {
	Roots    []string `json:"roots" jsonschema:"required" jsonschema_description:"The exact roots trusted."`
	Prefixes []string `json:"prefixes" jsonschema:"required" jsonschema_description:"The prefixes whose subtrees are trusted, at a path-component boundary."`
}

// DevicePolicy is the devices section as policy.get returns it.
type DevicePolicy struct {
	AllowPhysical bool     `json:"allowPhysical" jsonschema:"required" jsonschema_description:"Whether physical devices may be used at all."`
	Allowlist     []string `json:"allowlist" jsonschema:"required" jsonschema_description:"The exact physical serials admitted."`
}

// ShellPolicy is the shell section as policy.get returns it.
type ShellPolicy struct {
	Enabled bool     `json:"enabled" jsonschema:"required" jsonschema_description:"Whether shell.exec is enabled everywhere."`
	Roots   []string `json:"roots" jsonschema:"required" jsonschema_description:"The project roots shell.exec is enabled for."`
}

// GetResult is policy.get's result: the policy sections as written in the file, with
// the defaults when it is missing or invalid (plan m1k D2).
type GetResult struct {
	Path    string       `json:"path" jsonschema:"required" jsonschema_description:"The configuration file's path."`
	State   string       `json:"state" jsonschema:"required,enum=missing,enum=ok,enum=invalid" jsonschema_description:"missing when there is no file, ok when it parsed, invalid when it could not be used."`
	Error   string       `json:"error,omitempty" jsonschema_description:"The parse error, when state is invalid."`
	Trust   TrustPolicy  `json:"trust" jsonschema:"required"`
	Devices DevicePolicy `json:"devices" jsonschema:"required"`
	Shell   ShellPolicy  `json:"shell" jsonschema:"required"`
	// Revision is the token policy.update's ifRevision compares against.
	Revision string `json:"revision" jsonschema:"required" jsonschema_description:"The revision of the file this policy was read from: the hex SHA-256 of its bytes, or absent when there is no file (an invalid file has the hash of its bytes; empty only when the bytes could not be read or no configuration directory resolves; an empty revision never matches ifRevision). Pass it as policy.update's ifRevision to fail instead of overwriting a concurrent edit."`
}

// AbsentRevision is the revision of a configuration file that does not exist.
const AbsentRevision = "absent"

// revisionOf is the revision of a configuration file's bytes.
func revisionOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// parseSeam, when non-nil, runs in snapshot() after the bytes are parsed, so a test can
// change the file at the one moment a revision hashed from a fresh read would differ
// from the parsed bytes' (Q2). Production leaves it nil.
var parseSeam atomic.Pointer[func()]

// SetParseSeam installs the parse seam, or clears it with nil. For tests only.
func SetParseSeam(f func()) {
	if f == nil {
		parseSeam.Store(nil)
		return
	}
	parseSeam.Store(&f)
}

// Get returns the policy as policy.get reports it: the sections as written, with
// defaults when the file is missing or invalid, and the parse error when it is invalid.
// It never fails: the UI and the CLI must be able to show a bad file.
func (p *Policy) Get() GetResult {
	res := GetResult{
		Path:    p.path,
		Trust:   TrustPolicy{Roots: []string{}, Prefixes: []string{}},
		Devices: DevicePolicy{Allowlist: []string{}},
		Shell:   ShellPolicy{Roots: []string{}},
	}
	if p.dirErr != nil {
		res.State, res.Error = StateInvalid, p.dirErr.Error()
		return res
	}
	snap := p.snapshot()
	res.Revision = snap.revision
	switch {
	case snap.err != nil:
		res.State, res.Error = StateInvalid, snap.err.Cause
		return res
	case !snap.present:
		res.State = StateMissing
		return res
	}
	res.State = StateOK
	res.Trust = TrustPolicy{Roots: nonNil(snap.doc.trust.roots), Prefixes: nonNil(snap.doc.trust.prefixes)}
	res.Devices = DevicePolicy{AllowPhysical: snap.doc.devices.allowPhysical, Allowlist: nonNil(snap.doc.devices.allowlist)}
	res.Shell = ShellPolicy{Enabled: snap.doc.shell.enabled, Roots: nonNil(snap.doc.shell.roots)}
	return res
}

// None is policy.get's empty parameter object.
type None struct{}

// Register adds policy.get to the registry (plan m1k D2). Its audience is human, so
// the core refuses it from an MCP session and the MCP adapter does not list it: policy
// is changed by the user, not through an agent-facing surface (ADR-0015 §6;
// ADR-0032 §5).
func Register(reg *capability.Registry, p *Policy) {
	reg.MustRegister(capability.Descriptor{
		Name:        "policy.get",
		Kind:        capability.Query,
		Scope:       capability.Global,
		Audience:    capability.AudienceHuman,
		Since:       "0.10.0",
		Description: "Returns the droidyard policy: the configuration file's path and state, its revision (pass it to policy.update as ifRevision) and the trust, devices and shell sections. Policy is the user's to change, so the core refuses this capability from an agent session.",
		Params:      None{},
		Result:      GetResult{},
		Handler:     func(context.Context, *capability.Call) (any, error) { return p.Get(), nil },
	})
	reg.MustRegister(capability.Descriptor{
		Name:        "policy.update",
		Kind:        capability.Command,
		Scope:       capability.Global,
		Audience:    capability.AudienceHuman,
		Since:       "0.10.0",
		Idempotent:  true,
		Description: "Applies a JSON Merge Patch (RFC 7386) to the trust, devices and shell sections of the policy and returns policy.get's shape after the write; null removes a key or a section and arrays are replaced whole. The core refuses it from an agent session.",
		Params:      UpdateParams{},
		Result:      GetResult{},
		Errors:      []capability.ErrorCode{capability.ErrPolicyConfigUnusable, capability.ErrInvalidParams, capability.ErrPolicyRevisionConflict},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			patch, _ := call.Params.(UpdateParams)
			res, err := p.Update(ctx, patch)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	})
}

// RootOf maps a call to the project root it would run code in; ok is false when the
// call names no root.
type RootOf func(call *capability.Call) (string, bool)

// UntrustedError is the fixed failure of a capability that would run code in an
// untrusted root: WORKSPACE_UNTRUSTED, nextStep naming the exact human action
// (ADR-0015 §5; plan m1g D3). Its text is a decision, asserted by the tests.
func UntrustedError(root string) *capability.Error {
	return capability.Errorf(capability.ErrWorkspaceUntrusted, "the project root %q is not trusted, so its code will not run", root).
		WithNextStep("ask the user to run `droidyard trust '" + strings.ReplaceAll(root, "'", `'\''`) + "'`; nothing ran, so this call is safe to repeat once the root is trusted.")
}

// UntrustedNextStep is the nextStep of a WORKSPACE_UNTRUSTED raised outside Authorize
// (the git domain's filter-driver refusals): trust root, or, when root is a linked
// worktree, its main tree, since the project is what gets trusted (ADR-0081 §5).
func UntrustedNextStep(root string) string {
	if main, ok := platform.LinkedWorktree(platform.Env{}, root); ok {
		return UntrustedError(main).NextStep
	}
	return UntrustedError(root).NextStep
}

// worktreeUntrustedError is UntrustedError for a linked worktree whose project is not
// trusted: the project is what gets trusted, so the message and the nextStep name the
// main tree (ADR-0081 §5).
func worktreeUntrustedError(root, main string) *capability.Error {
	return capability.Errorf(capability.ErrWorkspaceUntrusted, "the project root %q is a worktree of %q, which is not trusted, so its code will not run", root, main).
		WithNextStep(UntrustedError(main).NextStep)
}

// shellDisabled is the fixed failure of shell.exec on a root the policy does not admit
// (ADR-0015 §7; ADR-0032 §2). Its message tells the human what enabling it would mean —
// once on, the agent runs any command as the user, with the user's full reach over
// droidyard's own configuration and trust included (Q1, delegated) — its nextStep names
// the configuration file so a human can enable the root, and its cause names the file
// when the file is the problem (plan m1k D4, D6; ADR-0032 Q1).
func (p *Policy) shellDisabled(root, cause string) *capability.Error {
	e := capability.Errorf(capability.ErrPolicyShellDisabled, "shell.exec is not enabled for the project root %q; enabling it lets an agent run any command as you, with your full rights over this machine, droidyard's own configuration and trust included", root).
		WithNextStep("ask the user to enable shell.exec for this project (`shell.roots` in " + p.path + ", or the human-only policy.update), or use a modelled capability; nothing ran, so this call is safe to repeat once it is enabled")
	if cause != "" {
		e = e.WithCause(cause)
	}
	return e
}

// configurationError is the failure when the configuration file cannot be read or
// parsed. Its cause names the file (plan m1g D1).
func configurationError(path string, err error) *capability.Error {
	if path == "" {
		return capability.Errorf(capability.ErrWorkspaceUntrusted, "the droidyard configuration could not be read, so nothing is trusted").
			WithCause(err.Error())
	}
	return capability.Errorf(capability.ErrWorkspaceUntrusted, "the configuration file %s could not be read, so nothing is trusted", path).
		WithCause(path + ": " + err.Error()).
		WithNextStep("fix or remove " + path + "; nothing ran, so this call is safe to repeat once the file is usable")
}

// trustedBy is the trust rule: an exact entry, or a prefix at a path-component
// boundary, so prefix /a/b trusts /a/b/c and not /a/bc (plan m1g D1). It is the rule
// both `trust.prefixes` and `shell.roots` use.
func trustedBy(roots, prefixes []string, root string) bool {
	for _, r := range roots {
		if r == root {
			return true
		}
	}
	for _, pre := range prefixes {
		if pre == "" {
			continue
		}
		if root == pre {
			return true
		}
		if pre == string(filepath.Separator) {
			// The filesystem root trusts everything; below it the boundary rule would
			// see no '/'-terminated second component.
			if strings.HasPrefix(root, pre) {
				return true
			}
			continue
		}
		if len(root) > len(pre) && strings.HasPrefix(root, pre) && root[len(pre)] == filepath.Separator {
			return true
		}
	}
	return false
}

// canonical maps each non-empty entry through the environment's canonicaliser, so a
// root reached through a symlink matches its canonical entry.
func (p *Policy) canonical(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e == "" {
			continue
		}
		out = append(out, p.env.Canonical(e))
	}
	return out
}

// snapshot returns the parsed file, re-reading it when its size or mtime changed.
func (p *Policy) snapshot() *snapshot {
	if p.dirErr != nil {
		// No path to read: never the defaults of a missing file.
		return &snapshot{err: configurationError("", p.dirErr)}
	}
	fi, err := os.Stat(p.path)
	if errors.Is(err, fs.ErrNotExist) {
		p.mu.Lock()
		p.cached = nil
		p.mu.Unlock()
		return &snapshot{revision: AbsentRevision}
	}
	if err != nil {
		return &snapshot{err: configurationError(p.path, err)}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil && p.cached.size == fi.Size() && p.cached.mtime.Equal(fi.ModTime()) {
		return p.cached
	}
	data, err := readConfigFile(p.path)
	if err != nil {
		p.cached = nil
		return &snapshot{err: configurationError(p.path, err)}
	}
	// The revision is of these bytes: whatever the file becomes afterwards, the policy
	// and its revision describe the same bytes (Q2).
	snap := &snapshot{size: fi.Size(), mtime: fi.ModTime(), revision: revisionOf(data)}
	doc, derr := decode(data)
	if seam := parseSeam.Load(); seam != nil {
		(*seam)()
	}
	if derr != nil {
		snap.err = configurationError(p.path, derr)
	} else {
		snap.present = true
		snap.doc = doc
		snap.roots = p.canonical(doc.trust.roots)
		snap.prefixes = p.canonical(doc.trust.prefixes)
	}
	p.cached = snap
	return snap
}

// Trust adds root to the configuration's trusted roots, canonicalised as a workspace
// root is, and returns the canonical root (plan m1g D2). It takes an exclusive flock on
// <config dir>/config.json.lock, re-reads the file under it, and writes atomically, so
// two concurrent runs lose nothing. A file that does not parse is refused, never
// overwritten. No daemon is involved and none is started.
func (p *Policy) Trust(root string) (string, error) {
	if p.dirErr != nil {
		return "", p.dirErr
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%q is not an absolute path; `droidyard trust` takes the project root", root)
	}
	canonical := p.env.Canonical(root)

	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	lock, err := lockExclusive(context.Background(), filepath.Join(dir, LockName))
	if err != nil {
		return "", err
	}
	defer lock.Close()

	doc, err := readDocument(p.path)
	if err != nil {
		return "", fmt.Errorf("%s could not be read, so it is left untouched: %w", p.path, err)
	}
	if !contains(doc.trust.roots, canonical) {
		doc.trust.roots = append(doc.trust.roots, canonical)
		if err := writeAtomic(p.path, doc); err != nil {
			return "", err
		}
	}
	p.mu.Lock()
	p.cached = nil
	p.mu.Unlock()
	return canonical, nil
}

// lockPoll is how often a waiting lockExclusive tries the lock again.
const lockPoll = 10 * time.Millisecond

// lockExclusive takes the exclusive flock, the serialisation concurrent writers need.
// It never blocks in the kernel: it tries LOCK_EX|LOCK_NB and, while another process
// holds the lock, tries again every lockPoll until ctx is done, so a cancelled call
// stops waiting (a blocking flock cannot be interrupted). The kernel releases the lock
// when the process dies, so a crashed run never blocks the next. A wait that ctx ends
// returns ctx's error.
func lockExclusive(ctx context.Context, path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// document is config.json as parsed: the whole top-level object, kept so a rewrite
// preserves every key it does not know, and the typed view of each known section.
type document struct {
	top     map[string]json.RawMessage // every top-level key, as it was
	trust   trustSection
	devices deviceSection
	shell   shellSection
	gradle  gradleSection
	tools   toolsSection

	occupation occupationSection
}

// occupationSection is the occupation section (ADR-0094 §7): conflict is "" and
// staleAfterMinutes 0 for a key that is not set.
type occupationSection struct {
	conflict          string
	staleAfterMinutes int
}

// trustSection is the trust section: the two known lists plus every other key, kept
// because Trust rewrites this section (the others are copied verbatim).
type trustSection struct {
	extra    map[string]json.RawMessage
	roots    []string
	prefixes []string
}

type deviceSection struct {
	allowPhysical bool
	allowlist     []string
}

type shellSection struct {
	enabled bool
	roots   []string
}

type gradleSection struct {
	sidecarJavaHome string
	allowBelowFloor bool
}

type toolsSection struct {
	adb      string
	emulator string
}

// decode parses a configuration file. A file that is not a JSON object, or whose known
// keys have the wrong shape or a relative path, is an error; so is a schemaVersion
// above this binary's (ADR-0032 §1, §4).
func decode(data []byte) (*document, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, err
	}
	if top == nil {
		return nil, errors.New("the configuration is not a JSON object")
	}
	doc := &document{top: top, trust: trustSection{extra: map[string]json.RawMessage{}}}
	if v, ok := top["schemaVersion"]; ok {
		var n int
		if err := json.Unmarshal(v, &n); err != nil {
			return nil, fmt.Errorf("schemaVersion: %w", err)
		}
		if n > schemaVersion {
			return nil, fmt.Errorf("schemaVersion %d is newer than this droidyard reads (%d)", n, schemaVersion)
		}
	}
	if err := decodeTrust(top["trust"], doc); err != nil {
		return nil, err
	}
	if err := decodeDevices(top["devices"], doc); err != nil {
		return nil, err
	}
	if err := decodeShell(top["shell"], doc); err != nil {
		return nil, err
	}
	if err := decodeGradle(top["gradle"], doc); err != nil {
		return nil, err
	}
	if err := decodeTools(top["tools"], doc); err != nil {
		return nil, err
	}
	if err := decodeOccupation(top["occupation"], doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// maxStaleAfterMinutes is the longest idle threshold the file may set: a week
// (ADR-0094 §7).
const maxStaleAfterMinutes = 10080

func decodeOccupation(raw json.RawMessage, doc *document) error {
	if absent(raw) {
		return nil
	}
	var o map[string]json.RawMessage
	if err := json.Unmarshal(raw, &o); err != nil {
		return fmt.Errorf("occupation: %w", err)
	}
	for k, v := range o {
		switch k {
		case "conflict":
			if isNull(v) {
				continue
			}
			var c string
			if err := json.Unmarshal(v, &c); err != nil {
				return fmt.Errorf("occupation.conflict: %w", err)
			}
			switch c {
			case capability.OccupationRefuse, capability.OccupationAsk, capability.OccupationAllow:
				doc.occupation.conflict = c
			default:
				return fmt.Errorf("occupation.conflict: %q is not refuse, ask or allow", c)
			}
		case "staleAfterMinutes":
			if isNull(v) {
				continue
			}
			var n int
			if err := json.Unmarshal(v, &n); err != nil {
				return fmt.Errorf("occupation.staleAfterMinutes: %w", err)
			}
			if n < 1 || n > maxStaleAfterMinutes {
				return fmt.Errorf("occupation.staleAfterMinutes: %d is not between 1 and %d", n, maxStaleAfterMinutes)
			}
			doc.occupation.staleAfterMinutes = n
		}
	}
	return nil
}

// rejectDuplicateKeys fails a document that repeats a key within one object, at any
// depth. encoding/json keeps the last of two equal keys, so a hand-edited file with two
// `shell` sections would silently enable what the first one said off (or the reverse);
// a duplicate makes the file unparseable instead, which is the fail-closed path of
// ADR-0031 §3 and ADR-0032 §4. The error names the key and the path to it. data has
// already parsed as one JSON value, so only the repeat is looked for here.
func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	// Numbers stay text: a valid file may hold one outside float64's range.
	dec.UseNumber()
	return walkValue(dec, "")
}

// walkValue consumes one JSON value from dec, checking every object in it.
func walkValue(dec *json.Decoder, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // a scalar
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := kt.(string)
			at := key
			if path != "" {
				at = path + "." + key
			}
			if seen[key] {
				return fmt.Errorf("%s: the key is written twice, and which one counts is ambiguous, so the file is refused", at)
			}
			seen[key] = true
			if err := walkValue(dec, at); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkValue(dec, path+"[]"); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // the closing delimiter
	return err
}

func decodeTrust(raw json.RawMessage, doc *document) error {
	if absent(raw) {
		return nil
	}
	var t map[string]json.RawMessage
	if err := json.Unmarshal(raw, &t); err != nil {
		return fmt.Errorf("trust: %w", err)
	}
	for k, v := range t {
		switch k {
		case "roots":
			if err := decodePaths("trust.roots", v, &doc.trust.roots); err != nil {
				return err
			}
		case "prefixes":
			if err := decodePaths("trust.prefixes", v, &doc.trust.prefixes); err != nil {
				return err
			}
		default:
			doc.trust.extra[k] = v
		}
	}
	return nil
}

func decodeDevices(raw json.RawMessage, doc *document) error {
	if absent(raw) {
		return nil
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("devices: %w", err)
	}
	for k, v := range d {
		switch k {
		case "allowPhysical":
			if isNull(v) {
				continue
			}
			if err := json.Unmarshal(v, &doc.devices.allowPhysical); err != nil {
				return fmt.Errorf("devices.allowPhysical: %w", err)
			}
		case "allowlist":
			if isNull(v) {
				continue
			}
			var list []string
			if err := json.Unmarshal(v, &list); err != nil {
				return fmt.Errorf("devices.allowlist: %w", err)
			}
			for _, s := range list {
				if s == "" {
					return errors.New("devices.allowlist: an empty string is not a serial")
				}
			}
			doc.devices.allowlist = list
		}
	}
	return nil
}

func decodeShell(raw json.RawMessage, doc *document) error {
	if absent(raw) {
		return nil
	}
	var s map[string]json.RawMessage
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("shell: %w", err)
	}
	for k, v := range s {
		switch k {
		case "enabled":
			if isNull(v) {
				continue
			}
			if err := json.Unmarshal(v, &doc.shell.enabled); err != nil {
				return fmt.Errorf("shell.enabled: %w", err)
			}
		case "roots":
			if err := decodePaths("shell.roots", v, &doc.shell.roots); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeGradle(raw json.RawMessage, doc *document) error {
	if absent(raw) {
		return nil
	}
	var g map[string]json.RawMessage
	if err := json.Unmarshal(raw, &g); err != nil {
		return fmt.Errorf("gradle: %w", err)
	}
	for k, v := range g {
		switch k {
		case "sidecarJavaHome":
			if err := decodePath("gradle.sidecarJavaHome", v, &doc.gradle.sidecarJavaHome); err != nil {
				return err
			}
		case "allowBelowFloor":
			if isNull(v) {
				continue
			}
			if err := json.Unmarshal(v, &doc.gradle.allowBelowFloor); err != nil {
				return fmt.Errorf("gradle.allowBelowFloor: %w", err)
			}
		}
	}
	return nil
}

func decodeTools(raw json.RawMessage, doc *document) error {
	if absent(raw) {
		return nil
	}
	var t map[string]json.RawMessage
	if err := json.Unmarshal(raw, &t); err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	for k, v := range t {
		switch k {
		case "adb":
			if err := decodePath("tools.adb", v, &doc.tools.adb); err != nil {
				return err
			}
		case "emulator":
			if err := decodePath("tools.emulator", v, &doc.tools.emulator); err != nil {
				return err
			}
		}
	}
	return nil
}

// absent reports whether a section key was not written or was written as JSON null,
// either of which means "its default" (ADR-0032 §1).
func absent(raw json.RawMessage) bool { return len(raw) == 0 || isNull(raw) }

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// decodePaths reads an array of absolute path strings. A relative entry makes the whole
// file invalid (ADR-0032 §1, §4).
func decodePaths(key string, raw json.RawMessage, out *[]string) error {
	if isNull(raw) {
		*out = nil
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if err := absolute(key, list); err != nil {
		return err
	}
	*out = list
	return nil
}

// decodePath reads one path key, which may be a string or null. A relative path makes
// the whole file invalid (ADR-0032 §1, §3).
func decodePath(key string, raw json.RawMessage, out *string) error {
	if isNull(raw) {
		*out = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if s != "" && !filepath.IsAbs(s) {
		return fmt.Errorf("%s: %q is not an absolute path", key, s)
	}
	*out = s
	return nil
}

// absolute refuses a relative entry: canonicalising "." or "~/src" would anchor it at
// "/", trusting or enabling the wrong thing without a word, so the whole file fails
// closed and the cause names the entry (ADR-0032 §1).
func absolute(key string, entries []string) error {
	for _, e := range entries {
		if e != "" && !filepath.IsAbs(e) {
			return fmt.Errorf("%s: %q is not an absolute path", key, e)
		}
	}
	return nil
}

// maxConfigBytes bounds the configuration file: far above any real one, small enough that
// a misplaced large file cannot stall every policy check.
const maxConfigBytes = 1 << 20

// readConfigFile reads the configuration file without blocking and within a bound: it is
// opened non-blocking and must be a regular file (a FIFO or a device at the path, or a
// symlink to one, is an error rather than a hang), and a file over maxConfigBytes is an
// error. A missing file keeps fs.ErrNotExist so callers treat it as the defaults.
func readConfigFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxConfigBytes)
	}
	return data, nil
}

// readDocument reads and parses path. A missing file is an empty configuration, so the
// first `droidyard trust` creates it.
func readDocument(path string) (*document, error) {
	data, err := readConfigFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &document{top: map[string]json.RawMessage{}, trust: trustSection{extra: map[string]json.RawMessage{}}}, nil
	}
	if err != nil {
		return nil, err
	}
	return decode(data)
}

// encode renders a document as indented JSON. The trust section is re-encoded from its
// typed view (Trust edits it) with every key it does not know kept; every other
// top-level key, and every other section whole, is kept as JSON-equal content, so an
// unknown key survives a rewrite (ADR-0032 §1). The bytes are not kept: sections are
// compacted and re-indented, keys sorted, and <, > and & escaped.
func encode(doc *document) ([]byte, error) {
	top := map[string]json.RawMessage{}
	for k, v := range doc.top {
		top[k] = v
	}
	top["schemaVersion"] = json.RawMessage("1")

	section := map[string]json.RawMessage{}
	for k, v := range doc.trust.extra {
		section[k] = v
	}
	roots, err := json.Marshal(nonNil(doc.trust.roots))
	if err != nil {
		return nil, err
	}
	prefixes, err := json.Marshal(nonNil(doc.trust.prefixes))
	if err != nil {
		return nil, err
	}
	section["roots"] = roots
	section["prefixes"] = prefixes
	trust, err := json.Marshal(section)
	if err != nil {
		return nil, err
	}
	top["trust"] = trust

	body, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// writeAtomic replaces path with doc: write <path>.tmp (0600), sync, rename, sync the
// directory, so a reader sees the old file or the new one, never a mix.
func writeAtomic(path string, doc *document) error {
	data, err := encode(doc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
