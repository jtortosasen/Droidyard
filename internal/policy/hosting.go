package policy

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// PeerNamePattern is what a peer's or a remote's name must match (ADR-0098 §2): it is
// the session name a peer's operations carry and the prefix of a remote device's id
// (`<remote>/<serial>`), so it holds no character either needs escaped.
const PeerNamePattern = "^[a-z][a-z0-9-]{0,31}$"

// FingerprintPattern is a certificate fingerprint as `droidyard id` prints it: the
// lowercase hex SHA-256 of the certificate's public key info (ADR-0098 §1).
const FingerprintPattern = "^[0-9a-f]{64}$"

var (
	namePattern        = regexp.MustCompile(PeerNamePattern)
	fingerprintPattern = regexp.MustCompile(FingerprintPattern)
)

// HostPeer is one paired peer of this host: the name its operations carry, and the
// fingerprint its certificate must have (ADR-0098 §2).
type HostPeer struct {
	Name        string `json:"name" jsonschema:"required,pattern=^[a-z][a-z0-9-]{0\\,31}$" jsonschema_description:"The peer's name: the session name its operations carry. Lowercase letters, digits and hyphens, starting with a letter, at most 32 characters. Unique among the peers."`
	Fingerprint string `json:"fingerprint" jsonschema:"required,pattern=^[0-9a-f]{64}$" jsonschema_description:"The peer's certificate fingerprint as droidyard id prints it: 64 lowercase hex digits. Unique among the peers."`
}

// Lend lends one device to the named peers (ADR-0099 §1).
type Lend struct {
	Device string   `json:"device" jsonschema:"required,minLength=1" jsonschema_description:"The device serial lent. Only a physical device is lent, and the host's own devices policy must admit it too."`
	To     []string `json:"to" jsonschema:"required,minItems=1" jsonschema_description:"The names of the peers it is lent to."`
}

// HostingPolicy is the hosting section as policy.get returns it: the peer listener's
// address, the peers paired with this host and the devices it lends them (ADR-0098 §6).
type HostingPolicy struct {
	Listen string     `json:"listen,omitempty" jsonschema_description:"The address the peer listener binds, an IP literal and a port (not an unspecified address); read at daemon start. Absent: this daemon lends nothing."`
	Peers  []HostPeer `json:"peers" jsonschema:"required" jsonschema_description:"The peers paired with this host; read at each request."`
	Lend   []Lend     `json:"lend" jsonschema:"required" jsonschema_description:"The devices lent, and to whom; read at each call."`
}

// Remote is one host this daemon pairs with: the name its devices carry as a prefix, the
// address to dial and the fingerprint its certificate must have (ADR-0098 §2).
type Remote struct {
	Name        string `json:"name" jsonschema:"required,pattern=^[a-z][a-z0-9-]{0\\,31}$" jsonschema_description:"The remote's name: the prefix of its devices' ids. Lowercase letters, digits and hyphens, starting with a letter, at most 32 characters. Unique among the remotes."`
	Address     string `json:"address" jsonschema:"required" jsonschema_description:"The host's peer listener, an IP literal and a port."`
	Fingerprint string `json:"fingerprint" jsonschema:"required,pattern=^[0-9a-f]{64}$" jsonschema_description:"The host's certificate fingerprint: 64 lowercase hex digits. Unique among the remotes."`
}

// decodeHosting reads the hosting section and validates it whole: a value that fails makes
// the file unusable (ADR-0032 §4), so the listener, the links and the lending stay off.
func decodeHosting(raw json.RawMessage, doc *document) error {
	if absent(raw) {
		return nil
	}
	var h map[string]json.RawMessage
	if err := json.Unmarshal(raw, &h); err != nil {
		return fmt.Errorf("hosting: %w", err)
	}
	for k, v := range h {
		switch k {
		case "listen":
			if isNull(v) {
				continue
			}
			if err := json.Unmarshal(v, &doc.hosting.Listen); err != nil {
				return fmt.Errorf("hosting.listen: %w", err)
			}
			if err := validListen(doc.hosting.Listen); err != nil {
				return fmt.Errorf("hosting.listen: %w", err)
			}
		case "peers":
			peers, err := decodeEntries("hosting.peers", v, func(e map[string]json.RawMessage, at string) (HostPeer, error) {
				name, err := entryString(e, "name", at)
				if err != nil {
					return HostPeer{}, err
				}
				fp, err := entryString(e, "fingerprint", at)
				if err != nil {
					return HostPeer{}, err
				}
				return HostPeer{Name: name, Fingerprint: fp}, nil
			})
			if err != nil {
				return err
			}
			names, fps := map[string]bool{}, map[string]bool{}
			for i, p := range peers {
				at := fmt.Sprintf("hosting.peers[%d]", i)
				if err := validName(at+".name", p.Name); err != nil {
					return err
				}
				if err := validFingerprint(at+".fingerprint", p.Fingerprint); err != nil {
					return err
				}
				if names[p.Name] {
					return fmt.Errorf("%s.name: %q is the name of an earlier peer; names are unique", at, p.Name)
				}
				if fps[p.Fingerprint] {
					return fmt.Errorf("%s.fingerprint: the fingerprint of %q is the fingerprint of an earlier peer; fingerprints are unique", at, p.Name)
				}
				names[p.Name], fps[p.Fingerprint] = true, true
			}
			doc.hosting.Peers = peers
		case "lend":
			lend, err := decodeEntries("hosting.lend", v, func(e map[string]json.RawMessage, at string) (Lend, error) {
				device, err := entryString(e, "device", at)
				if err != nil {
					return Lend{}, err
				}
				var to []string
				raw, ok := e["to"]
				if !ok || isNull(raw) {
					return Lend{}, fmt.Errorf("%s.to: required", at)
				}
				if err := json.Unmarshal(raw, &to); err != nil {
					return Lend{}, fmt.Errorf("%s.to: %w", at, err)
				}
				return Lend{Device: device, To: to}, nil
			})
			if err != nil {
				return err
			}
			devices := map[string]bool{}
			for i, l := range lend {
				at := fmt.Sprintf("hosting.lend[%d]", i)
				if l.Device == "" {
					return fmt.Errorf("%s.device: an empty string is not a serial", at)
				}
				if devices[l.Device] {
					return fmt.Errorf("%s.device: %q is lent by an earlier entry; list each device once, with every peer it is lent to", at, l.Device)
				}
				devices[l.Device] = true
				if len(l.To) == 0 {
					return fmt.Errorf("%s.to: name at least one peer, or remove the entry", at)
				}
				seen := map[string]bool{}
				for _, n := range l.To {
					if err := validName(at+".to", n); err != nil {
						return err
					}
					if seen[n] {
						return fmt.Errorf("%s.to: %q is named twice", at, n)
					}
					seen[n] = true
				}
			}
			doc.hosting.Lend = lend
		}
	}
	return nil
}

// decodeRemotes reads and validates the remotes list (ADR-0098 §2).
func decodeRemotes(raw json.RawMessage, doc *document) error {
	if absent(raw) {
		return nil
	}
	remotes, err := decodeEntries("remotes", raw, func(e map[string]json.RawMessage, at string) (Remote, error) {
		name, err := entryString(e, "name", at)
		if err != nil {
			return Remote{}, err
		}
		address, err := entryString(e, "address", at)
		if err != nil {
			return Remote{}, err
		}
		fp, err := entryString(e, "fingerprint", at)
		if err != nil {
			return Remote{}, err
		}
		return Remote{Name: name, Address: address, Fingerprint: fp}, nil
	})
	if err != nil {
		return err
	}
	names, fps := map[string]bool{}, map[string]bool{}
	for i, r := range remotes {
		at := fmt.Sprintf("remotes[%d]", i)
		if err := validName(at+".name", r.Name); err != nil {
			return err
		}
		if err := validFingerprint(at+".fingerprint", r.Fingerprint); err != nil {
			return err
		}
		if err := ValidRemoteAddress(r.Address); err != nil {
			return fmt.Errorf("%s.address: %w", at, err)
		}
		if names[r.Name] {
			return fmt.Errorf("%s.name: %q is the name of an earlier remote; names are unique", at, r.Name)
		}
		if fps[r.Fingerprint] {
			return fmt.Errorf("%s.fingerprint: the fingerprint of %q is the fingerprint of an earlier remote; fingerprints are unique", at, r.Name)
		}
		names[r.Name], fps[r.Fingerprint] = true, true
	}
	doc.remotes = remotes
	return nil
}

// validListen refuses what the peer listener must never bind: a host name (it would
// resolve at bind time to whatever the resolver says), an address with no host (every
// interface) and a missing port. Port 0 is allowed, an ephemeral port (ADR-0098 §3).
func validListen(s string) error {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return fmt.Errorf("%q is not an IP address and a port (a host name is not accepted): %v", s, err)
	}
	if ap.Addr().IsUnspecified() {
		return fmt.Errorf("%q binds every interface; name the address of the VPN interface, or a loopback address", s)
	}
	return nil
}

// ValidRemoteAddress is the rule a remote's address must meet (ADR-0098 §2): an IP literal
// and a non-zero port. `droidyard remote add` applies it before it writes.
func ValidRemoteAddress(s string) error {
	ap, err := netip.ParseAddrPort(s)
	switch {
	case err != nil:
		return fmt.Errorf("%q is not an IP address and a port (a host name is not accepted): %v", s, err)
	case ap.Addr().IsUnspecified() || ap.Port() == 0:
		return fmt.Errorf("%q names no host to dial", s)
	}
	return nil
}

// ValidListen is the rule hosting.listen must meet (ADR-0098 §3); `droidyard host listen`
// applies it before it writes.
func ValidListen(s string) error { return validListen(s) }

// ValidName is the rule a peer's or a remote's name must meet (ADR-0098 §2), for the
// command line to apply before it writes; key names the argument in the error.
func ValidName(key, s string) error { return validName(key, s) }

// ValidFingerprint is the rule a fingerprint must meet (ADR-0098 §2); see ValidName.
func ValidFingerprint(key, s string) error { return validFingerprint(key, s) }

func validName(key, s string) error {
	if !namePattern.MatchString(s) {
		return fmt.Errorf("%s: %q does not match %s", key, s, PeerNamePattern)
	}
	return nil
}

func validFingerprint(key, s string) error {
	if !fingerprintPattern.MatchString(s) {
		return fmt.Errorf("%s: %q is not 64 lowercase hex digits, a fingerprint as `droidyard id` prints it", key, s)
	}
	return nil
}

// decodeEntries reads an array of objects, each through one, keyed by the exact names
// the file spells: encoding/json would match "Name" to "name", and a key written that
// way must not pass for the real one (the same rule UpdateParams applies to a patch).
// A key the entry does not know is ignored, and survives only until its list is rewritten:
// `policy.update` and the `droidyard host` and `remote` verbs replace an array whole, from
// the typed entries (RFC 7386), so the unknown key is dropped with it.
func decodeEntries[T any](key string, raw json.RawMessage, one func(e map[string]json.RawMessage, at string) (T, error)) ([]T, error) {
	if isNull(raw) {
		return nil, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	out := make([]T, 0, len(list))
	for i, item := range list {
		at := fmt.Sprintf("%s[%d]", key, i)
		var e map[string]json.RawMessage
		if err := json.Unmarshal(item, &e); err != nil || e == nil {
			return nil, fmt.Errorf("%s: an object is expected", at)
		}
		v, err := one(e, at)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// entryString reads a required string key of an entry.
func entryString(e map[string]json.RawMessage, key, at string) (string, error) {
	raw, ok := e[key]
	if !ok || isNull(raw) {
		return "", fmt.Errorf("%s.%s: required", at, key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s.%s: %w", at, key, err)
	}
	return s, nil
}

// Hosting is the hosting section as the file has it now, read at each call (ADR-0098 §6).
// A configuration that cannot be used returns its error, and the caller lends nothing: a
// missing file lends nothing too, with no error.
func (p *Policy) Hosting() (HostingPolicy, *capability.Error) {
	h := HostingPolicy{Peers: []HostPeer{}, Lend: []Lend{}}
	if p.dirErr != nil {
		return h, configurationError("", p.dirErr)
	}
	snap := p.snapshot()
	if snap.err != nil {
		return h, snap.err
	}
	if !snap.present {
		return h, nil
	}
	// Copies, so a caller cannot change the snapshot the next check reads.
	h.Listen = snap.doc.hosting.Listen
	h.Peers = append(h.Peers, snap.doc.hosting.Peers...)
	for _, l := range snap.doc.hosting.Lend {
		h.Lend = append(h.Lend, Lend{Device: l.Device, To: append([]string(nil), l.To...)})
	}
	return h, nil
}

// Remotes is the remotes list as the file has it now. As for Hosting, an unusable file
// returns its error and a missing one an empty list.
func (p *Policy) Remotes() ([]Remote, *capability.Error) {
	if p.dirErr != nil {
		return []Remote{}, configurationError("", p.dirErr)
	}
	snap := p.snapshot()
	if snap.err != nil {
		return []Remote{}, snap.err
	}
	if !snap.present || snap.doc.remotes == nil {
		return []Remote{}, nil
	}
	return append([]Remote(nil), snap.doc.remotes...), nil
}

// Lent reports whether the host lends serial to the peer named peer now (ADR-0099 §1). It
// is the lending list alone: that the device is physical and that the host's own devices
// policy admits it are the caller's to check, since lending never widens either. A
// configuration that cannot be used lends nothing.
func (p *Policy) Lent(peer, serial string) bool {
	h, err := p.Hosting()
	if err != nil {
		return false
	}
	for _, l := range h.Lend {
		if l.Device == serial {
			return slices.Contains(l.To, peer)
		}
	}
	return false
}
