// Package policy reads the user configuration, which is the file trust is recorded in
// and every policy decision lives in (ADR-0015 §5–6; ADR-0021 §Location;
// ADR-0031; ADR-0032 §1). It is not state: it lives outside the state root, is written
// only by human channels (`droidyard trust`, `policy.update`) or by hand, and is never
// migrated or reset with the state.
//
// Version 1 of the file has five sections: `trust`, `devices`, `shell`, `gradle` and
// `tools` (ADR-0032 §1). Every key is optional; a `null` value is its default. Unknown
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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
// workspace root is (plan m1g D1). A missing file trusts nothing. A file that does not
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
	return trustedBy(snap.roots, snap.prefixes, p.env.Canonical(root)), nil
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
			return nil, UntrustedError(canonical)
		}
		return canonical, nil
	}
}

// ShellAllowed reports whether shell.exec may run with root as its project root:
// `shell.enabled`, or root equal to or under a `shell.roots` entry at a path-component
// boundary (ADR-0032 §2; plan m1k D4). When it may not, the returned failure is
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
	if trustedBy(nil, p.canonical(snap.doc.shell.roots), p.env.Canonical(root)) {
		return true, nil
	}
	return false, p.shellDisabled(root, "")
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
}

// PhysicalAdmitted reports whether a physical device's exact serial is admitted. A
// missing file admits none (the default is off), and an invalid one admits none and
// names itself as the cause.
func (p *Policy) PhysicalAdmitted(serial string) Admission {
	if p.dirErr != nil {
		return Admission{Cause: p.dirErr.Error()}
	}
	snap := p.snapshot()
	switch {
	case snap.err != nil:
		return Admission{Cause: snap.err.Cause}
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
}

// BelowFloorKey is `gradle.allowBelowFloor` as read at daemon start. An invalid file
// sets no key, so Allowed is false and Gradle below the floor is refused (ADR-0032 §4).
func (s Startup) BelowFloorKey() BelowFloor {
	if s.State == StateInvalid {
		return BelowFloor{ConfigPath: s.Path}
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
		Name:     "policy.get",
		Kind:     capability.Query,
		Scope:    capability.Global,
		Audience: capability.AudienceHuman,
		Since:    "0.10.0",
		Description: "Returns the droidyard policy: the configuration file's path and whether it parsed (state missing, ok or invalid, with the parse error when invalid), and the trust, devices and shell sections as written in the file, with their defaults when the file is missing or invalid. " +
			"Policy is the user's to change — trusted roots, the physical-device allowlist and whether shell.exec is enabled — so it is answerable only to a human client: the core refuses this capability from an agent session. " +
			"It never fails on a bad file, so the UI can show the problem.",
		Params:  None{},
		Result:  GetResult{},
		Handler: func(context.Context, *capability.Call) (any, error) { return p.Get(), nil },
	})
	reg.MustRegister(capability.Descriptor{
		Name:       "policy.update",
		Kind:       capability.Command,
		Scope:      capability.Global,
		Audience:   capability.AudienceHuman,
		Since:      "0.10.0",
		Idempotent: true,
		Description: "Applies a JSON Merge Patch (RFC 7386) to the droidyard policy and writes the result, returning policy.get's shape after the write. " +
			"The patch is restricted to the `trust`, `devices` and `shell` sections: each is written as a section object, or as `null` to remove the section and return it to its defaults; inside a section each known key is written as a value, or as `null` to remove the key (`trust.roots`, `trust.prefixes`, `devices.allowPhysical`, `devices.allowlist`, `shell.enabled`, `shell.roots`). " +
			"Arrays are replaced whole, so to add a trusted root send the whole list. Any other key, at the top level or inside a section, is REQUEST_INVALID_PARAMS, not a silent no-op. " +
			"Policy is the user's to change, so it is answerable only to a human client: the core refuses this capability from an agent session. " +
			"It takes the exclusive lock on the configuration's lock file, re-reads the file under it (so a concurrent `droidyard trust` on another key is not lost), canonicalises the path entries the patch wrote, validates the whole result and writes atomically; unknown keys in the file, and every other section, are kept. " +
			"A relative path is REQUEST_INVALID_PARAMS and the file is untouched; a file that does not parse or is of a newer version is POLICY_CONFIG_UNUSABLE and is left untouched. Tool keys are edited by hand, not here. A patch that changes nothing writes nothing.",
		Params: UpdateParams{},
		Result: GetResult{},
		Errors: []capability.ErrorCode{capability.ErrPolicyConfigUnusable, capability.ErrInvalidParams},
		Handler: func(_ context.Context, call *capability.Call) (any, error) {
			patch, _ := call.Params.(UpdateParams)
			res, err := p.Update(patch)
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
		WithNextStep("ask the user to run `droidyard trust '" + strings.ReplaceAll(root, "'", `'\''`) + "'`")
}

// shellDisabled is the fixed failure of shell.exec on a root the policy does not admit
// (ADR-0015 §7; ADR-0032 §2). Its message tells the human what enabling it would mean —
// once on, the agent runs any command as the user, with the user's full reach over
// droidyard's own configuration and trust included (Q1, delegated) — its nextStep names
// the configuration file so a human can enable the root, and its cause names the file
// when the file is the problem (plan m1k D4, D6; ADR-0032 Q1).
func (p *Policy) shellDisabled(root, cause string) *capability.Error {
	e := capability.Errorf(capability.ErrPolicyShellDisabled, "shell.exec is not enabled for the project root %q; enabling it lets an agent run any command as you, with your full rights over this machine, droidyard's own configuration and trust included", root).
		WithNextStep("ask the user to enable shell.exec for this project (`shell.roots` in " + p.path + "), or use a modelled capability")
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
		WithNextStep("fix or remove " + path)
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
		return &snapshot{}
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
	snap := &snapshot{size: fi.Size(), mtime: fi.ModTime()}
	doc, derr := decode(data)
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
	lock, err := lockExclusive(filepath.Join(dir, LockName))
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

// lockExclusive takes a blocking exclusive flock, the serialisation concurrent writers
// need. The kernel releases it when the process dies, so a crashed run never blocks the
// next.
func lockExclusive(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
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
	return doc, nil
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
