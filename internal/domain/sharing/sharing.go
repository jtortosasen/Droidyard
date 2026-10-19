// Package sharing registers the capabilities that share a physical device with another
// Droidyard and join a share (ADR-0107, ADR-0108): hosting.share, hosting.unshare and
// hosting.join. They are the human's, never an agent's: the person who runs the command is
// the consent ADR-0015 §4 asks for, so they write the physical-device policy a share and a
// join need (devices.allowPhysical, the serial or <remote>/<serial>) along with the lending.
//
// The handlers call Host.Reconcile, which is safe because they run as operations and not on a
// peer's WebSocket: a Reconcile that closes a listener waits for that listener's handlers, so
// one called from inside such a handler would wait for itself.
//
// The package holds no state of its own. The share is hosting.lend and the unused codes the
// host keeps (hosting.Pending); a join is a remote in the configuration. Everything the
// configuration says is written through hosting.Host.Edit, one write each, and the peer
// machinery follows it at once through Host.Reconcile.
package sharing

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// The names of the capabilities.
const (
	CapShare   = "hosting.share"
	CapUnshare = "hosting.unshare"
	CapJoin    = "hosting.join"
)

// The codes this package adds (ADR-0107 §6). HOSTING is a new prefix: the domain that owns
// the failure is the peer link, which no other capability returns a code of.
const (
	// ErrJoinFailed: hosting.join did not pair this daemon with the host. details.reason is
	// unreachable (no address answered), notThisHost (every address answered with another
	// certificate) or refused (the host answered and did not admit this daemon: the code is
	// used, void or wrong, or its answer was unusable).
	ErrJoinFailed capability.ErrorCode = "HOSTING_JOIN_FAILED"
	// ErrUnavailable: this daemon cannot host a share or join one now: it has no peer
	// identity, or no private address to listen on, or the listeners could not bind.
	ErrUnavailable capability.ErrorCode = "HOSTING_UNAVAILABLE"
)

const since = "0.31.0"

// WholeDeviceWarning is what sharing a device means (ADR-0099 §4), said each time one is.
const WholeDeviceWarning = "The whole device is lent: its screen and input reach every app, notification and account on it, and nothing is cleaned when a lease ends. Lend a dedicated device, not your own phone."

// linkWait is how long hosting.join waits for the new remote's link to come up.
var linkWait = 5 * time.Second

// Devices is the adb listing hosting.share reads: *device.ADB.
type Devices interface {
	Shareable(ctx context.Context) ([]device.ShareableDevice, error)
}

// JoinFunc is how a join reaches the host a code names: hosting.Join. A variable of Options
// only so a test can stand in for a host whose answer is hostile.
type JoinFunc func(ctx context.Context, id *hosting.Identity, code hosting.Code, name, api string) (hosting.JoinResult, error)

// Options give the capabilities what they act on. A nil Host (a registry built only to
// describe the contract) leaves every handler answering HOSTING_UNAVAILABLE.
type Options struct {
	Host    *hosting.Host
	Devices Devices
	// API is this daemon's API version, sent to the host in a join.
	API string
	// Join is hosting.Join when nil.
	Join JoinFunc
	// Log receives the join lines (daemon.log); nil discards them.
	Log io.Writer
}

type sharing struct {
	Options
}

// Address is one address a share listens on.
type Address struct {
	Address string `json:"address" jsonschema:"required" jsonschema_description:"An IP literal and a port."`
	Kind    string `json:"kind" jsonschema:"required,enum=vpn,enum=lan" jsonschema_description:"vpn for an address on a tunnel interface, lan for one on any other."`
}

// ShareParams are hosting.share's.
type ShareParams struct {
	Device string `json:"device" jsonschema:"required,minLength=1" jsonschema_description:"The serial of the physical device to share, as device.list prints it."`
	Detach bool   `json:"detach" jsonschema:"required" jsonschema_description:"True: the share lasts until hosting.unshare. Only true is accepted in this version."`
	On     string `json:"on,omitempty" jsonschema_description:"private (the default), vpn, lan or one private IP; written to hosting.on. Absent keeps hosting.on."`
}

// ShareResult is hosting.share's.
type ShareResult struct {
	Device    string    `json:"device" jsonschema:"required"`
	Code      string    `json:"code" jsonschema:"required" jsonschema_description:"The join code, dy1. and base64url: whoever pastes it into droidyard join first gets the device. Single use, no time expiry, a bearer credential until used: pass it only to the person meant to have it."`
	Addresses []Address `json:"addresses" jsonschema:"required" jsonschema_description:"The addresses the host listens on and the code carries."`
	Port      int       `json:"port" jsonschema:"required"`
	Detached  bool      `json:"detached" jsonschema:"required"`
	Notes     []string  `json:"notes" jsonschema:"required" jsonschema_description:"What the human running the command must know, one sentence each."`
}

// UnshareParams are hosting.unshare's.
type UnshareParams struct {
	Device string `json:"device,omitempty" jsonschema_description:"The serial to stop sharing; absent stops every share."`
}

// UnshareResult is hosting.unshare's.
type UnshareResult struct {
	Unshared []string `json:"unshared" jsonschema:"required" jsonschema_description:"The serials whose lending was removed."`
}

// JoinParams are hosting.join's.
type JoinParams struct {
	Code string `json:"code" jsonschema:"required,minLength=1" jsonschema_description:"The join code the host's hosting.share printed, dy1. and base64url."`
}

// JoinResult is hosting.join's.
type JoinResult struct {
	Remote    string `json:"remote" jsonschema:"required" jsonschema_description:"The name the host has in remotes, made from its host name."`
	Device    string `json:"device" jsonschema:"required" jsonschema_description:"The device's id here, <remote>/<serial>."`
	Connected bool   `json:"connected" jsonschema:"required" jsonschema_description:"Whether the link to the host came up within 5 s. If not it keeps trying, and the device is listed once it does."`
}

// Register adds the sharing capabilities to reg.
func Register(reg *capability.Registry, o Options) {
	s := &sharing{Options: o}
	if s.Join == nil {
		s.Join = hosting.Join
	}
	if s.Log == nil {
		s.Log = io.Discard
	}
	reg.MustRegister(capability.Descriptor{
		Name:         CapShare,
		Kind:         capability.Command,
		Scope:        capability.Global,
		Audience:     capability.AudienceHuman,
		Since:        since,
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Shares one physical device: admits its serial in this machine's physical-device policy, lends it, listens on the private addresses and returns a single-use join code to paste into droidyard join on the other machine. A person's action, refused from an agent session and from a peer; with detach true the share lasts until hosting.unshare.",
		Params:       ShareParams{},
		Result:       ShareResult{},
		Errors: []capability.ErrorCode{
			device.ErrADBNotFound, device.ErrADBFailed, device.ErrNotFound, device.ErrNotReady, device.ErrNotAllowed,
			ErrUnavailable, capability.ErrPolicyConfigUnusable, capability.ErrPolicyRevisionConflict, capability.ErrInvalidParams,
		},
		Annotations: capability.Annotations{
			OpenWorld: &openWorld,
			Reason:    "opens a network listener and lends a whole device to whoever holds the code",
		},
		Target:  func(p any) *capability.OpTarget { return deviceTarget(p.(ShareParams).Device) },
		Handler: s.share,
		Slim: func(result any) any {
			// The code is a bearer credential: the operation record keeps no copy of it once it was delivered.
			if r, ok := result.(ShareResult); ok {
				r.Code = ""
				return r
			}
			return result
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name:        CapUnshare,
		Kind:        capability.Command,
		Scope:       capability.Global,
		Audience:    capability.AudienceHuman,
		Idempotent:  true,
		Since:       since,
		Description: "Stops sharing a device, or every device: its lending is removed, its unused codes are void, and the peers that joined stay paired and reach nothing. A person's action, refused from an agent session.",
		Params:      UnshareParams{},
		Result:      UnshareResult{},
		Errors: []capability.ErrorCode{
			ErrUnavailable, capability.ErrPolicyConfigUnusable, capability.ErrPolicyRevisionConflict, capability.ErrInvalidParams,
		},
		Target:  func(p any) *capability.OpTarget { return deviceTarget(p.(UnshareParams).Device) },
		Handler: s.unshare,
	})
	reg.MustRegister(capability.Descriptor{
		Name:        CapJoin,
		Kind:        capability.Command,
		Scope:       capability.Global,
		Audience:    capability.AudienceHuman,
		Since:       since,
		Description: "Joins the share a join code names: pins the host's certificate from the code, presents this daemon's with its secret, and records the host as a remote with <remote>/<serial> admitted, so the device is listed at once. A person's action, refused from an agent session; the first join the host accepts spends the code.",
		Params:      JoinParams{},
		Result:      JoinResult{},
		Errors: []capability.ErrorCode{
			ErrJoinFailed, ErrUnavailable, capability.ErrPolicyConfigUnusable, capability.ErrPolicyRevisionConflict, capability.ErrInvalidParams,
		},
		Annotations: capability.Annotations{
			OpenWorld: &openWorld,
			Reason:    "dials the addresses in the code",
		},
		Handler: s.join,
	})
}

var openWorld = true

// deviceTarget names the device a call acts on, for the operation rows (ADR-0076); nil for
// none.
func deviceTarget(serial string) *capability.OpTarget {
	if serial == "" {
		return nil
	}
	return &capability.OpTarget{Device: serial}
}

// unavailable is HOSTING_UNAVAILABLE for a daemon with no peer subsystem.
func unavailable(why string) *capability.Error {
	return capability.NewError(ErrUnavailable, "sharing is not available on this daemon").
		WithCause(why).
		WithNextStep("see daemon.log next to the daemon's lock for why the peer link did not start")
}

// ready is the peer link this daemon has, or why it has none.
func (s *sharing) ready() (*hosting.Host, *hosting.Identity, *capability.Error) {
	if s.Host == nil {
		return nil, nil, unavailable("this daemon has no peer subsystem")
	}
	id := s.Host.Identity()
	if id == nil {
		return nil, nil, unavailable("this daemon has no peer identity")
	}
	return s.Host, id, nil
}

// kinds tags this machine's private addresses: the kind each would be listened on.
func kinds(ifaces []hosting.Iface) map[netip.Addr]hosting.Kind {
	out := map[netip.Addr]hosting.Kind{}
	for _, a := range hosting.Private(ifaces, "private") {
		out[a.IP] = a.Kind
	}
	return out
}

func (s *sharing) share(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(ShareParams)
	if !p.Detach {
		return nil, capability.NewError(capability.ErrInvalidParams, "this version shares a device detached only").
			WithNextStep("pass detach true; the share then lasts until hosting.unshare")
	}
	if s.Host == nil || s.Devices == nil {
		return nil, unavailable("this daemon has no peer subsystem")
	}
	// The device first: only a physical device that is online and alone under its serial is shared.
	listed, err := s.Devices.Shareable(ctx)
	if err != nil {
		return nil, err
	}
	var entries []device.ShareableDevice
	for _, d := range listed {
		if d.Device == p.Device {
			entries = append(entries, d)
		}
	}
	switch {
	case len(entries) == 0 || entries[0].Kind != device.KindPhysical:
		online := []string{}
		for _, d := range listed {
			if d.Kind == device.KindPhysical && d.State == device.StateDevice && !d.Duplicate {
				online = append(online, d.Device)
			}
		}
		return nil, capability.NewError(device.ErrNotFound, "no physical device "+p.Device+" is connected: only physical devices are shared").
			WithDetails(map[string]any{"online": online}).
			WithNextStep("share one of the serials in details.online, or plug the device in and accept its debugging prompt")
	case len(entries) > 1:
		return nil, capability.NewError(device.ErrNotAllowed, "more than one device reports the serial "+p.Device+", so none of them is shared").
			WithNextStep("unplug all but one of them, or change its serial, then share again (ADR-0103 §1)")
	case entries[0].State != device.StateDevice:
		return nil, capability.NewError(device.ErrNotReady, "the device "+p.Device+" is "+entries[0].State).
			WithDetails(map[string]any{"state": entries[0].State}).
			WithNextStep("accept the debugging prompt on the device, or wait for it to come back, then share again")
	}
	host, id, cerr := s.ready()
	if cerr != nil {
		return nil, cerr
	}
	cur, cerr := host.Config()
	if cerr != nil {
		return nil, cerr
	}
	on := cur.Hosting.On
	switch {
	case p.On != "":
		if err := policy.ValidOn(p.On); err != nil {
			return nil, capability.Errorf(capability.ErrInvalidParams, "on: %v", err).
				WithNextStep("use private, vpn, lan or one private IP address")
		}
		on = p.On
	case on == "":
		on = "private"
	}
	ifaces, ierr := host.Interfaces()
	if ierr != nil {
		return nil, unavailable("this machine's network interfaces could not be read: " + ierr.Error())
	}
	// The addresses `on` allows are found before anything is written.
	want, werr := wantedAddrs(cur.Hosting.Listen, ifaces, on)
	if werr != nil {
		return nil, unavailable(werr.Error())
	}

	created := false
	wasPhysical := false
	var others []string
	_, cerr = host.Edit(ctx, func(cur policy.GetResult) (map[string]any, *capability.Error) {
		wasPhysical = cur.Devices.AllowPhysical
		allow := slices.Clone(cur.Devices.Allowlist)
		if allow == nil {
			allow = []string{}
		}
		others = nil
		for _, a := range allow {
			if a != p.Device {
				others = append(others, a)
			}
		}
		if !slices.Contains(allow, p.Device) {
			allow = append(allow, p.Device)
		}
		lend := cloneLend(cur.Hosting.Lend)
		created = !slices.ContainsFunc(lend, func(l policy.Lend) bool { return l.Device == p.Device })
		if created {
			lend = append(lend, policy.Lend{Device: p.Device, To: []string{}})
		}
		return map[string]any{
			"devices": map[string]any{"allowPhysical": true, "allowlist": allow},
			"hosting": map[string]any{"on": on, "lend": lend},
		}, nil
	})
	if cerr != nil {
		return nil, cerr
	}
	// undo takes a lending this call made back: a share that listens nowhere is none.
	undo := func() {
		if created {
			host.Edit(context.Background(), func(cur policy.GetResult) (map[string]any, *capability.Error) {
				lend := slices.DeleteFunc(cloneLend(cur.Hosting.Lend), func(l policy.Lend) bool { return l.Device == p.Device })
				return map[string]any{"hosting": map[string]any{"lend": lend}}, nil
			})
			host.Reconcile()
		}
	}

	host.Reconcile()
	hh := host.Health().Hosting
	port, addrs := boundAddrs(hh.Listening, want, kinds(ifaces))
	if len(addrs) == 0 {
		undo()
		var why []string
		for _, e := range hh.Errors {
			why = append(why, e.Address+": "+e.Error)
		}
		if len(why) == 0 {
			why = []string{"no listener took an address"}
		}
		return nil, capability.NewError(ErrUnavailable, "the host could not listen on any address").
			WithCause(strings.Join(why, "; ")).
			WithDetails(map[string]any{"errors": hh.Errors}).
			WithNextStep("free the port or fix the address (hosting.port, hosting.listen), then share again; nothing is lent")
	}
	fp, err := hex.DecodeString(id.Fingerprint())
	if err != nil || len(fp) != 32 {
		undo()
		return nil, unavailable("this daemon's fingerprint is not a SHA-256")
	}
	code := hosting.Code{Port: uint16(port)}
	copy(code.Fingerprint[:], fp)
	for _, a := range addrs {
		code.Addrs = append(code.Addrs, hosting.Addr{IP: a.ip, Kind: a.kind})
	}
	// An address a code may not carry (hosting.listen on a public one) is found before a secret is made.
	if _, err := code.Encode(); err != nil {
		undo()
		return nil, capability.NewError(ErrUnavailable, "the addresses of the host cannot be put in a join code").
			WithCause(err.Error()).
			WithNextStep("listen on a private address (hosting.listen, or --on), then share again; nothing is lent")
	}
	code.Secret = host.Pending().Issue(p.Device)
	line, err := code.Encode()
	if err != nil {
		host.Pending().Void(p.Device)
		undo()
		return nil, unavailable(err.Error())
	}

	res := ShareResult{Device: p.Device, Code: line, Port: port, Detached: true, Addresses: []Address{}}
	for _, a := range addrs {
		res.Addresses = append(res.Addresses, Address{Address: netip.AddrPortFrom(a.ip, uint16(port)).String(), Kind: string(a.kind)})
	}
	res.Notes = []string{
		WholeDeviceWarning,
		p.Device + " is now allowed on this Mac too (devices.allowlist): its own agents and the UI can use it, not only the people who join. `droidyard device disallow " + p.Device + "` takes that back.",
	}
	if !wasPhysical && len(others) > 0 {
		res.Notes = append(res.Notes, "devices.allowPhysical was off; turning it on admits every serial already in devices.allowlist: "+strings.Join(others, ", ")+".")
	}
	res.Notes = append(res.Notes, "A code works for one join and does not expire; `droidyard unshare "+p.Device+"` voids the unused ones. A join the host fails to record spends its code too: share again for a new one.")
	return res, nil
}

// wantedAddrs are the addresses a share would listen on: hosting.listen's one when it is set
// (it wins, ADR-0109 §2), else the private addresses `on` allows. None is an error that says
// what was looked for.
func wantedAddrs(listen string, ifaces []hosting.Iface, on string) ([]netip.AddrPort, error) {
	if listen != "" {
		ap, err := netip.ParseAddrPort(listen)
		if err != nil {
			return nil, fmt.Errorf("hosting.listen %q is not an address", listen)
		}
		return []netip.AddrPort{ap}, nil
	}
	private := hosting.Private(ifaces, on)
	if len(private) == 0 {
		return nil, fmt.Errorf("this Mac has no %s network address to listen on (on: %s)", describeOn(on), on)
	}
	out := make([]netip.AddrPort, len(private))
	for i, a := range private {
		out[i] = netip.AddrPortFrom(a.IP, 0)
	}
	return out, nil
}

func describeOn(on string) string {
	switch on {
	case "private":
		return "private"
	case "vpn", "lan":
		return on
	}
	return "such"
}

// boundAddr is an address a listener took, with its kind.
type boundAddr struct {
	ip   netip.Addr
	kind hosting.Kind
}

// boundAddrs are the listening addresses that one of want names, all on the port of the
// first: what a code carries. A want with port 0 names an address on any port.
func boundAddrs(listening []string, want []netip.AddrPort, tags map[netip.Addr]hosting.Kind) (port int, out []boundAddr) {
	for _, l := range listening {
		ap, err := netip.ParseAddrPort(l)
		if err != nil {
			continue
		}
		if !slices.ContainsFunc(want, func(w netip.AddrPort) bool {
			return w.Addr() == ap.Addr() && (w.Port() == 0 || w.Port() == ap.Port())
		}) {
			continue
		}
		if port == 0 {
			port = int(ap.Port())
		}
		if int(ap.Port()) != port {
			continue
		}
		kind, ok := tags[ap.Addr()]
		if !ok {
			kind = hosting.KindLAN
		}
		out = append(out, boundAddr{ip: ap.Addr(), kind: kind})
	}
	return port, out
}

// cloneLend copies the lending list with every `to` a list, never null.
func cloneLend(in []policy.Lend) []policy.Lend {
	out := make([]policy.Lend, len(in))
	for i, l := range in {
		out[i] = policy.Lend{Device: l.Device, To: slices.Clone(l.To)}
		if out[i].To == nil {
			out[i].To = []string{}
		}
	}
	return out
}

func (s *sharing) unshare(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(UnshareParams)
	host, _, cerr := s.ready()
	if cerr != nil {
		return nil, cerr
	}
	removed := []string{}
	_, cerr = host.Edit(ctx, func(cur policy.GetResult) (map[string]any, *capability.Error) {
		removed = removed[:0]
		keep := []policy.Lend{}
		for _, l := range cloneLend(cur.Hosting.Lend) {
			if p.Device == "" || l.Device == p.Device {
				removed = append(removed, l.Device)
				continue
			}
			keep = append(keep, l)
		}
		if len(removed) == 0 {
			return nil, nil
		}
		return map[string]any{"hosting": map[string]any{"lend": keep}}, nil
	})
	if cerr != nil {
		return nil, cerr
	}
	// The codes go whether or not an entry did: a share that was edited away by hand is still void.
	if p.Device == "" {
		host.Pending().VoidAll()
	} else {
		host.Pending().Void(p.Device)
	}
	host.Reconcile()
	return UnshareResult{Unshared: append([]string{}, removed...)}, nil
}

// validSerial reports whether a device the host named is one this daemon will write into its
// configuration (ADR-0100 §4): the host's text is untrusted.
func validSerial(s string) bool {
	if s == "" || len(s) > 64 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '/' || unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func (s *sharing) join(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(JoinParams)
	// The code is read first and nothing is dialled for one that is not a code. Its error
	// never quotes the text: it may be a code.
	code, err := hosting.ParseCode(p.Code)
	if err != nil {
		return nil, capability.NewError(capability.ErrInvalidParams, err.Error()).
			WithNextStep("paste the whole line the host's droidyard share printed after `droidyard join`")
	}
	host, id, cerr := s.ready()
	if cerr != nil {
		return nil, cerr
	}
	if _, cerr := host.Config(); cerr != nil {
		return nil, cerr
	}
	fp := hex.EncodeToString(code.Fingerprint[:])

	res, err := s.Join(ctx, id, code, hosting.OwnName(nil), s.API)
	if err != nil {
		var je *hosting.JoinError
		if !errors.As(err, &je) {
			return nil, err
		}
		return nil, joinFailed(je)
	}
	if !validSerial(res.Reply.Device) {
		return nil, capability.NewError(ErrJoinFailed, "the host's answer named a device that is not a serial, so nothing was written").
			WithDetails(map[string]any{"reason": hosting.JoinRefused}).
			WithCause("the host paired this Mac and spent the code").
			WithNextStep("ask the host's human for a fresh code (droidyard share -d); this Mac stays paired there, under the same names")
	}
	serial := res.Reply.Device

	addrs := make([]policy.RemoteAddress, len(code.Addrs))
	for i, a := range code.Addrs {
		addrs[i] = policy.RemoteAddress{Address: netip.AddrPortFrom(a.IP, code.Port).String(), Kind: string(a.Kind)}
	}
	answered := netip.AddrPortFrom(res.Addr.IP, code.Port).String()
	name := ""
	_, cerr = host.Edit(ctx, func(cur policy.GetResult) (map[string]any, *capability.Error) {
		remotes := slices.Clone(cur.Remotes)
		if remotes == nil {
			remotes = []policy.Remote{}
		}
		at := slices.IndexFunc(remotes, func(r policy.Remote) bool { return hosting.SamePin(r.Fingerprint, fp) })
		if at >= 0 {
			// A host already known keeps its name and gains the addresses.
			name = remotes[at].Name
			remotes[at].Address, remotes[at].Addresses = answered, addrs
		} else {
			taken := make([]string, len(remotes))
			for i, r := range remotes {
				taken[i] = r.Name
			}
			name = hosting.NameFrom(res.Reply.Host, taken)
			remotes = append(remotes, policy.Remote{Name: name, Address: answered, Addresses: addrs, Fingerprint: fp})
		}
		allow := slices.Clone(cur.Devices.Allowlist)
		if allow == nil {
			allow = []string{}
		}
		if remoteID := name + "/" + serial; !slices.Contains(allow, remoteID) {
			allow = append(allow, remoteID)
		}
		return map[string]any{
			"remotes": remotes,
			"devices": map[string]any{"allowPhysical": true, "allowlist": allow},
		}, nil
	})
	if cerr != nil {
		return nil, capability.NewError(cerr.Code, "the host admitted this Mac and the code is spent, but the configuration could not be written, so the join is not recorded here").
			WithCause(cerr.Message + causeSuffix(cerr)).
			WithNextStep("fix the configuration, then ask the host's human for a fresh code (droidyard share -d): this Mac is paired there already, so the names stay the same")
	}
	fmt.Fprintf(s.Log, "droidyard: joined %s at %s (fingerprint %s): device %q\n", name, answered, fp[:12], serial)

	host.Reconcile()
	connected := waitLinked(ctx, host, name)
	return JoinResult{Remote: name, Device: name + "/" + serial, Connected: connected}, nil
}

func causeSuffix(e *capability.Error) string {
	if e.Cause == "" {
		return ""
	}
	return ": " + e.Cause
}

// waitLinked waits up to linkWait for the link to the named remote to be up.
func waitLinked(ctx context.Context, host *hosting.Host, name string) bool {
	deadline := time.NewTimer(linkWait)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if l := host.Remote(name); l != nil && l.Connected() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

// joinFailed is HOSTING_JOIN_FAILED for a join that did not pair: what each reason means to
// the person who pasted the code.
func joinFailed(je *hosting.JoinError) *capability.Error {
	msg, next := "", ""
	switch je.Reason {
	case hosting.JoinNotThisHost:
		msg = "the machine at the code's address is not the one that issued it"
		next = "check that the code is complete and was meant for this network; ask the host's human to run droidyard share again and paste the new line"
	case hosting.JoinRefused:
		msg = "the host did not admit this Mac: the code is used, void or wrong"
		next = "ask the host's human for a fresh code (droidyard share -d <serial>): a code works for one join, and a join the host failed to record spends it too"
	default:
		msg = "no address in the code answered"
		next = "check that both Macs are on the same network or VPN and that the host is still sharing, then paste the line again; if it keeps failing, ask for a fresh code (the host may have used this one if only its answer was lost)"
	}
	return capability.NewError(ErrJoinFailed, msg).
		WithCause(je.Error()).
		WithDetails(map[string]any{"reason": je.Reason}).
		WithNextStep(next)
}
