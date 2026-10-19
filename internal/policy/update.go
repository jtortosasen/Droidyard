package policy

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// UpdateParams is policy.update's parameter object: a JSON Merge Patch (RFC 7386)
// restricted to the policy sections (ADR-0032 §5; ADR-0098 §6; plan m1k D3). Each section
// is a patch object or JSON null (remove the section and return it to its defaults); each
// known key inside a section is a value or null (remove the key). Every object is
// closed: an unknown key anywhere is REQUEST_INVALID_PARAMS at Start, not a silent
// no-op, so `{"gradle": {}}` and `{"shell": {"enable": true}}` are refused (the keys
// already in the file are still kept by the write).
//
// The exported fields generate the published schema and give the decoder its strict
// shape; UnmarshalJSON additionally keeps the sections as the caller wrote them,
// so the handler can tell a section or key written as null from one not written at all
// (RFC 7386 needs the difference: absent is no change, null removes).
type UpdateParams struct {
	Trust      *TrustPatch   `json:"trust,omitempty" jsonschema_description:"The trust section to merge: an object with roots and/or prefixes, each an array of absolute paths, or null to remove the section and trust nothing. Arrays are replaced whole." jsonschema:"nullable"`
	Devices    *DevicesPatch `json:"devices,omitempty" jsonschema_description:"The devices section to merge: an object with allowPhysical and/or allowlist, or null to remove the section and admit no physical device." jsonschema:"nullable"`
	Shell      *ShellPatch   `json:"shell,omitempty" jsonschema_description:"The shell section to merge: an object with enabled and/or roots, or null to remove the section and keep shell.exec off." jsonschema:"nullable"`
	Hosting    *HostingPatch `json:"hosting,omitempty" jsonschema_description:"The hosting section to merge: an object with listen, on, port, peers and/or lend, or null to remove the section and lend nothing." jsonschema:"nullable"`
	Remotes    *[]Remote     `json:"remotes,omitempty" jsonschema_description:"The remotes list, replacing the whole list, or null to remove it. A change applies within seconds, with no restart." jsonschema:"nullable"`
	IfRevision *string       `json:"ifRevision,omitempty" jsonschema_description:"The revision policy.get returned; the patch applies only if the file still has it, else POLICY_REVISION_CONFLICT."`

	// raw is the patch as the caller wrote it, kept so a section or key written as null
	// is distinguishable from one not written (RFC 7386). Set by UnmarshalJSON.
	raw map[string]json.RawMessage
}

// TrustPatch is the trust section of a policy.update patch.
type TrustPatch struct {
	Roots    *[]string `json:"roots,omitempty" jsonschema_description:"The trusted roots, each an absolute path, canonicalised as droidyard trust does. Replaces the whole list." jsonschema:"nullable"`
	Prefixes *[]string `json:"prefixes,omitempty" jsonschema_description:"The trusted prefixes, each an absolute path. Replaces the whole list." jsonschema:"nullable"`
}

// DevicesPatch is the devices section of a policy.update patch.
type DevicesPatch struct {
	AllowPhysical *bool     `json:"allowPhysical,omitempty" jsonschema_description:"Whether physical devices may be used at all." jsonschema:"nullable"`
	Allowlist     *[]string `json:"allowlist,omitempty" jsonschema_description:"The exact physical serials admitted. Replaces the whole list." jsonschema:"nullable"`
}

// ShellPatch is the shell section of a policy.update patch.
type ShellPatch struct {
	Enabled *bool     `json:"enabled,omitempty" jsonschema_description:"Whether shell.exec is enabled everywhere." jsonschema:"nullable"`
	Roots   *[]string `json:"roots,omitempty" jsonschema_description:"The project roots shell.exec is enabled for, each an absolute path, canonicalised as droidyard trust does. Replaces the whole list." jsonschema:"nullable"`
}

// HostingPatch is the hosting section of a policy.update patch.
type HostingPatch struct {
	Listen *string     `json:"listen,omitempty" jsonschema_description:"The one address the peer listener binds, an IP literal and a port. A change applies within seconds, with no restart." jsonschema:"nullable"`
	On     *string     `json:"on,omitempty" jsonschema_description:"Where the peer listener binds when listen is unset and this host lends a device: private, vpn, lan, or one IP literal that is private, in 100.64.0.0/10 or loopback." jsonschema:"nullable"`
	Port   *int        `json:"port,omitempty" jsonschema_description:"The port the listener takes on the addresses of on, from 1 to 65535." jsonschema:"nullable,minimum=1,maximum=65535"`
	Peers  *[]HostPeer `json:"peers,omitempty" jsonschema_description:"The peers paired with this host. Replaces the whole list." jsonschema:"nullable"`
	Lend   *[]Lend     `json:"lend,omitempty" jsonschema_description:"The devices lent and to whom. Replaces the whole list." jsonschema:"nullable"`
}

// UnmarshalJSON decodes a patch strictly — an unknown key at the top level or inside a
// section, a wrong type, or trailing data is an error, so a bad patch is
// REQUEST_INVALID_PARAMS at Start rather than a silent no-op (plan m1k D3) — and keeps
// the raw sections for the handler.
func (p *UpdateParams) UnmarshalJSON(data []byte) error {
	// A distinct type has no methods, so Decode does not recurse into this one; the
	// strict decode then applies to every level.
	type wire UpdateParams
	var w wire
	// A repeated key would resolve last-wins in the maps below, as it would in the file.
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected data after the parameter object")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return errors.New("the patch must be a JSON object")
	}
	// encoding/json matches field names regardless of case, so "Shell" or "Roots" would
	// pass the strict decode and then be merged as written: the keys are checked exactly
	// (plan m1k D3; step 3 B1).
	allowed := map[string][]string{
		"trust":   {"roots", "prefixes"},
		"devices": {"allowPhysical", "allowlist"},
		"shell":   {"enabled", "roots"},
		"hosting": {"listen", "on", "port", "peers", "lend"},
	}
	for k, v := range raw {
		if k == "ifRevision" {
			// Not a section: the optimistic check, typed by the strict decode. An explicit
			// null would silently turn the check off, so it is refused.
			if isNull(v) {
				return errors.New("ifRevision must be a string: omit it to patch without the check")
			}
			continue
		}
		if k == "remotes" {
			// A list, not a section: the typed decode checks its entries.
			continue
		}
		keys, ok := allowed[k]
		if !ok {
			return fmt.Errorf("unknown key %q: a patch names trust, devices, shell, hosting or remotes, exactly", k)
		}
		if isNull(v) {
			continue
		}
		var sec map[string]json.RawMessage
		if err := json.Unmarshal(v, &sec); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		for sk := range sec {
			if !slices.Contains(keys, sk) {
				return fmt.Errorf("unknown key %q in %s: it takes %s, exactly", sk, k, strings.Join(keys, " and "))
			}
		}
	}
	*p = UpdateParams{Trust: w.Trust, Devices: w.Devices, Shell: w.Shell, Hosting: w.Hosting, Remotes: w.Remotes, IfRevision: w.IfRevision, raw: raw}
	return nil
}

// MergePatch applies an RFC 7386 JSON Merge Patch. As the codebase uses it, patch is a
// section object (the layer above has already removed a null section): a null value
// removes the key, a value that is not an object replaces the target, and an object
// merges key by key. It is the ten-line recursion the plan names (R5).
func MergePatch(target, patch json.RawMessage) json.RawMessage {
	var pm map[string]json.RawMessage
	if err := json.Unmarshal(patch, &pm); err != nil || pm == nil {
		return patch
	}
	var tm map[string]json.RawMessage
	if err := json.Unmarshal(target, &tm); err != nil || tm == nil {
		tm = map[string]json.RawMessage{}
	}
	for k, v := range pm {
		if isNull(v) {
			delete(tm, k)
			continue
		}
		tm[k] = MergePatch(tm[k], v)
	}
	out, err := json.Marshal(tm)
	if err != nil {
		return patch
	}
	return out
}

// updateSeam, when non-nil, runs inside policy.update's critical section — after the
// file is read and before it is written, with the exclusive lock held — so a test can
// interleave a concurrent writer deterministically (plan m1k step 3). Production leaves
// it nil; SetUpdateSeam sets and clears it.
var updateSeam atomic.Pointer[func()]

// SetUpdateSeam installs the update seam, or clears it with nil. For tests only.
func SetUpdateSeam(f func()) {
	if f == nil {
		updateSeam.Store(nil)
		return
	}
	updateSeam.Store(&f)
}

// lockSeam, when non-nil, runs in policy.update just before it takes the lock, so a test
// can know the call is about to wait for it. For tests only; SetLockSeam sets and clears.
var lockSeam atomic.Pointer[func()]

// SetLockSeam installs the lock seam, or clears it with nil. For tests only.
func SetLockSeam(f func()) {
	if f == nil {
		lockSeam.Store(nil)
		return
	}
	lockSeam.Store(&f)
}

// Update applies patch to the policy and writes the result, returning the policy in
// policy.get's shape after the write (ADR-0032 §5; plan m1k D3). It takes the exclusive
// flock on <config dir>/config.json.lock, re-reads the file under it, applies RFC 7386
// to the three sections, canonicalises the path entries the patch wrote, validates the
// whole result and writes atomically, so nothing a concurrent `droidyard trust` wrote
// to another key is lost. The file is left untouched on every failure. A patch that
// changes nothing writes nothing. The wait for the lock ends with ctx, as
// OPERATION_CANCELLED.
func (p *Policy) Update(ctx context.Context, patch UpdateParams) (GetResult, *capability.Error) {
	if p.dirErr != nil {
		return GetResult{}, configUnusable("", p.dirErr)
	}
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return GetResult{}, configUnusable(p.path, err)
	}
	if seam := lockSeam.Load(); seam != nil {
		(*seam)()
	}
	lock, err := lockExclusive(ctx, filepath.Join(dir, LockName))
	if err != nil {
		if ctx.Err() != nil {
			// The wait for another writer ended with the call: nothing was read or written.
			return GetResult{}, capability.NewError(capability.ErrOperationCancelled, "the operation was cancelled while it waited for the configuration lock, so the configuration is untouched")
		}
		return GetResult{}, configUnusable(p.path, err)
	}
	defer lock.Close()
	// Whatever the cache holds may predate a write made while this call waited: drop it,
	// so the Get that follows (a conflict's nextStep, a no-op's result) reads fresh bytes.
	p.mu.Lock()
	p.cached = nil
	p.mu.Unlock()

	top, cerr := p.readTop(patch.IfRevision)
	if cerr != nil {
		return GetResult{}, cerr
	}
	if seam := updateSeam.Load(); seam != nil {
		(*seam)()
	}

	merged := make(map[string]json.RawMessage, len(top))
	for k, v := range top {
		merged[k] = v
	}
	for k, v := range patch.raw {
		if k == "ifRevision" {
			continue
		}
		if isNull(v) {
			delete(merged, k) // a null section is removed: its defaults apply
			continue
		}
		merged[k] = MergePatch(merged[k], v)
	}
	if serr := p.canonicalise(patch.raw, merged); serr != nil {
		return GetResult{}, serr
	}
	if semanticEqual(top, merged) {
		// An empty or no-op patch writes nothing.
		return p.Get(), nil
	}

	data, err := json.Marshal(merged)
	if err != nil {
		return GetResult{}, configUnusable(p.path, err)
	}
	doc, derr := decode(data)
	if derr != nil {
		// The file was valid when read, so only the patch can be the cause: a key the
		// schema did not close, or a value the section rules reject (D1).
		return GetResult{}, capability.Errorf(capability.ErrInvalidParams, "the patch would make the configuration invalid, so it is left untouched").
			WithCause(derr.Error())
	}
	// The reader refuses a file over maxConfigBytes; a patch that would write one is
	// refused here, so an update never leaves a file its own reader rejects (step 3 B3).
	if out, err := encode(doc); err != nil {
		return GetResult{}, configUnusable(p.path, err)
	} else if len(out) > maxConfigBytes {
		return GetResult{}, capability.Errorf(capability.ErrInvalidParams, "the patched configuration would be %d bytes, over the %d the reader accepts, so it is left untouched", len(out), maxConfigBytes)
	}
	if err := writeAtomic(p.path, doc); err != nil {
		return GetResult{}, configUnusable(p.path, err)
	}
	p.mu.Lock()
	p.cached = nil
	p.mu.Unlock()
	return p.Get(), nil
}

// readTop reads the configuration file as the top-level object map, checking ifRevision
// (when given) and then validating it with D1's rules. A missing file is an empty object; an invalid or newer one is
// POLICY_CONFIG_UNUSABLE, left untouched (ADR-0032 §4; plan m1k D3).
func (p *Policy) readTop(ifRevision *string) (map[string]json.RawMessage, *capability.Error) {
	data, err := readConfigFile(p.path)
	revision := AbsentRevision
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, configUnusable(p.path, err)
	default:
		revision = revisionOf(data)
	}
	// The comparison is under the lock, with the bytes just read, before anything else
	// is judged about them (ADR-0032 §5, Q2).
	if ifRevision != nil && *ifRevision != revision {
		msg := "the given ifRevision is not a revision policy.get returned, so the update was not applied"
		if wellFormedRevision(*ifRevision) {
			msg = fmt.Sprintf("the configuration changed since revision %s was read, so the update was not applied", *ifRevision)
		}
		return nil, capability.Errorf(capability.ErrPolicyRevisionConflict, "%s", msg).
			WithCause("the file's revision under the lock is " + revision).
			WithNextStep("call policy.get and apply the change again")
	}
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if _, derr := decode(data); derr != nil {
		return nil, configUnusable(p.path, derr)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return nil, configUnusable(p.path, err)
	}
	return top, nil
}

// canonicalise maps the path entries the patch wrote through the environment's
// canonicaliser, as `droidyard trust` does (ADR-0032 §5; plan m1k D3). Only the keys
// the patch supplied are touched, so an untouched section keeps its exact bytes. A
// relative entry is REQUEST_INVALID_PARAMS naming the key, and the file is left
// untouched.
func (p *Policy) canonicalise(patch, merged map[string]json.RawMessage) *capability.Error {
	for _, ref := range []struct{ section, key string }{
		{"trust", "roots"},
		{"trust", "prefixes"},
		{"shell", "roots"},
	} {
		ps, ok := patch[ref.section]
		if !ok || isNull(ps) {
			continue
		}
		var pm map[string]json.RawMessage
		if json.Unmarshal(ps, &pm) != nil {
			continue
		}
		if _, ok := pm[ref.key]; !ok {
			continue
		}
		var sm map[string]json.RawMessage
		if err := json.Unmarshal(merged[ref.section], &sm); err != nil || sm == nil {
			continue
		}
		rawEntry, ok := sm[ref.key]
		if !ok || isNull(rawEntry) {
			continue // the patch removed the key
		}
		var arr []string
		if err := json.Unmarshal(rawEntry, &arr); err != nil {
			continue // not an array of strings; the typed decode prevents this
		}
		for i, e := range arr {
			if e == "" {
				continue
			}
			if !filepath.IsAbs(e) {
				return capability.Errorf(capability.ErrInvalidParams, "%s would be a relative path, so the configuration would be invalid", ref.section+"."+ref.key).
					WithCause(fmt.Sprintf("%s: %q is not an absolute path", ref.section+"."+ref.key, e)).
					WithNextStep("send absolute paths")
			}
			arr[i] = p.env.Canonical(e)
		}
		if b, err := json.Marshal(arr); err == nil {
			sm[ref.key] = b
		}
		if b, err := json.Marshal(sm); err == nil {
			merged[ref.section] = b
		}
	}
	return nil
}

// configUnusable is policy.update's failure when the configuration cannot be read,
// parsed or written (ADR-0032 §4, §5; plan m1k D3). Its cause is the parse or OS error,
// and it names the file.
func configUnusable(path string, err error) *capability.Error {
	e := capability.Errorf(capability.ErrPolicyConfigUnusable, "the droidyard configuration could not be used, so it is left untouched")
	if path != "" {
		e = capability.Errorf(capability.ErrPolicyConfigUnusable, "the configuration file %s could not be used, so it is left untouched", path)
	}
	if err != nil {
		e = e.WithCause(path + ": " + err.Error())
		if path == "" {
			e = e.WithCause(err.Error())
		}
	}
	next := "fix or remove the configuration file, then retry"
	if path != "" {
		next = "fix or remove " + path + ", then retry"
	}
	return e.WithNextStep(next)
}

// semanticEqual reports whether two top-level maps are the same JSON: a no-op patch
// (an empty one, or a section object that merges nothing) writes nothing, and byte
// layout — key order, indentation — does not count.
func semanticEqual(a, b map[string]json.RawMessage) bool {
	ca, ea := canonicalJSON(a)
	cb, eb := canonicalJSON(b)
	return ea == nil && eb == nil && bytes.Equal(ca, cb)
}

func canonicalJSON(m map[string]json.RawMessage) ([]byte, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// wellFormedRevision reports whether s has the shape of a revision: 64 lowercase hex
// digits, or "absent".
func wellFormedRevision(s string) bool {
	if s == AbsentRevision {
		return true
	}
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}
