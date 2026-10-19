// Package sharing registers the capabilities that share a physical device with another
// Droidyard and join a share (ADR-0107, ADR-0108): hosting.share, hosting.unshare,
// hosting.watchShare and hosting.join. They are the human's, never an agent's: the person who runs the command is
// the consent ADR-0015 §4 asks for, so they write the physical-device policy a share and a
// join need (devices.allowPhysical, the serial or <remote>/<serial>) along with the lending.
//
// The handlers call Host.Reconcile, which is safe because they run as operations and not on a
// peer's WebSocket: a Reconcile that closes a listener waits for that listener's handlers, so
// one called from inside such a handler would wait for itself.
//
// The package holds no state of its own. A detached share is hosting.lend and the unused codes
// the host keeps (hosting.Pending); a foreground share is held by the host in memory, owned by
// the native connection that made it and ended with it (ADR-0108 §3); a join is a remote in
// the configuration. Everything the
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
	CapShare      = "hosting.share"
	CapUnshare    = "hosting.unshare"
	CapWatchShare = "hosting.watchShare"
	CapJoin       = "hosting.join"
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
	// Shareable lists the devices adb reports; manufacturers asks for each online device's
	// manufacturer too, which only the listing of the candidates shows.
	Shareable(ctx context.Context, manufacturers bool) ([]device.ShareableDevice, error)
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

// The modes of a share (ShareResult.Mode).
const (
	ModeForeground = "foreground"
	ModeDetached   = "detached"
)

// ShareParams are hosting.share's.
type ShareParams struct {
	Device string `json:"device,omitempty" jsonschema:"minLength=1" jsonschema_description:"The serial to share, as device.list prints it; absent lists the candidates and shares nothing."`
	Detach bool   `json:"detach,omitempty" jsonschema_description:"True: lasts until hosting.unshare. Absent: a foreground share, which ends with the CLI connection that made it."`
	On     string `json:"on,omitempty" jsonschema_description:"private (the default), vpn, lan or one private IP; written to hosting.on. Absent keeps hosting.on."`
}

// Candidate is one physical device hosting.share lists when it is given no device.
type Candidate struct {
	Device       string `json:"device" jsonschema:"required" jsonschema_description:"The serial."`
	Transport    string `json:"transport" jsonschema:"required,enum=usb,enum=tcp" jsonschema_description:"usb for a device adb reports on a usb: transport, tcp for any other."`
	Manufacturer string `json:"manufacturer,omitempty" jsonschema_description:"ro.product.manufacturer; absent for a device that is not online and alone under its serial, and when the device did not answer in time. The device's own text."`
	Model        string `json:"model,omitempty" jsonschema_description:"As adb devices -l prints it, with underscores; the device's own text."`
	State        string `json:"state" jsonschema:"required" jsonschema_description:"As adb reports it: device, unauthorized, offline…"`
	Shareable    bool   `json:"shareable" jsonschema:"required" jsonschema_description:"True for an online device alone under its serial."`
	Reason       string `json:"reason,omitempty" jsonschema_description:"Why a device is not shareable, and what to do."`
}

// ShareResult is hosting.share's: the share of a device or, when no device was named, the
// candidates.
type ShareResult struct {
	Candidates []Candidate `json:"candidates,omitempty" jsonschema_description:"Set only when no device was named (empty then when no physical device is connected): the physical devices adb reports, emulators left out, shareable or not. Nothing was shared and nothing was written."`
	Device     string      `json:"device,omitempty" jsonschema_description:"The device shared; absent when listing."`
	Code       string      `json:"code,omitempty" jsonschema_description:"The join code, dy1. and base64url: whoever pastes it into droidyard join first gets the device. Single use, no time expiry, a bearer credential until used: pass it only to the person meant to have it. Absent when listing."`
	Addresses  []Address   `json:"addresses,omitempty" jsonschema_description:"The addresses the host listens on and the code carries."`
	Port       int         `json:"port,omitempty"`
	Mode       string      `json:"mode,omitempty" jsonschema:"enum=foreground,enum=detached" jsonschema_description:"The share's mode. A device already shared keeps its mode whatever this call asked for, and gets a fresh code."`
	Owned      bool        `json:"owned,omitempty" jsonschema_description:"True when the share is a foreground share owned by the connection that made this call, which may call hosting.watchShare on it."`
	Notes      []string    `json:"notes,omitempty" jsonschema_description:"What the human running the command must know, one sentence each."`
}

// WatchShareParams are hosting.watchShare's.
type WatchShareParams struct {
	Device string `json:"device" jsonschema:"required,minLength=1" jsonschema_description:"The serial of the foreground share to watch."`
}

// WatchShareResult is hosting.watchShare's.
type WatchShareResult struct {
	Ended string `json:"ended" jsonschema:"required,enum=unshared,enum=connection,enum=stopping" jsonschema_description:"Why the share ended: unshared (hosting.unshare), connection (the connection that made it ended) or stopping (the daemon is stopping)."`
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
	Remote    string   `json:"remote" jsonschema:"required" jsonschema_description:"The name the host has in remotes, made from its host name."`
	Device    string   `json:"device" jsonschema:"required" jsonschema_description:"The device's id here, <remote>/<serial>."`
	Connected bool     `json:"connected" jsonschema:"required" jsonschema_description:"Whether the link to the host came up within 5 s. If not it keeps trying, and the device is listed once it does."`
	Notes     []string `json:"notes,omitempty" jsonschema_description:"What the human running the command must know, one sentence each."`
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
		Description:  "Shares one physical device and returns a single-use join code for droidyard join on the other machine; with no device it lists the candidates and shares nothing. A person's action, refused from agents and peers; detach true lasts until hosting.unshare, else the share belongs to the CLI connection that made it and ends with it.",
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
		Name:        CapWatchShare,
		Kind:        capability.Query,
		Scope:       capability.Global,
		Audience:    capability.AudienceHuman,
		LongRunning: true,
		Since:       since,
		Description: "Runs until the foreground share of a device ends and says why; its progress names the share's peers and which are connected. A person's action, allowed only on a foreground share owned by the caller's own connection, which bounds it.",
		Params:      WatchShareParams{},
		Result:      WatchShareResult{},
		Errors:      []capability.ErrorCode{ErrUnavailable, capability.ErrInvalidParams},
		Target:      func(p any) *capability.OpTarget { return deviceTarget(p.(WatchShareParams).Device) },
		Handler:     s.watch,
	})
	reg.MustRegister(capability.Descriptor{
		Name:        CapUnshare,
		Kind:        capability.Command,
		Scope:       capability.Global,
		Audience:    capability.AudienceHuman,
		Idempotent:  true,
		Since:       since,
		Description: "Stops sharing a device, or every device, in either mode: lending removed, unused codes void; peers stay paired and reach nothing. A person's action, refused from agents and peers.",
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
	if s.Host == nil || s.Devices == nil {
		return nil, unavailable("this daemon has no peer subsystem")
	}
	// A foreground share belongs to the native connection that made it and ends with it
	// (ADR-0108 §3); any other kind of client has no such hook yet, so it shares detached.
	if p.Device != "" && !p.Detach && (call.Origin.Client != capability.ClientCLI || call.Origin.ID == "") {
		return nil, capability.NewError(capability.ErrInvalidParams, "a foreground share belongs to the CLI connection that makes it; this client shares detached only").
			WithNextStep("pass detach true; the share then lasts until hosting.unshare")
	}
	// The device first: only a physical device that is online and alone under its serial is shared.
	listed, err := s.Devices.Shareable(ctx, p.Device == "")
	if err != nil {
		return nil, err
	}
	if p.Device == "" {
		return ShareResult{Candidates: candidates(listed)}, nil
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

	// A device already shared keeps its mode, whichever this call asked for (ADR-0108 §5): a
	// lend entry is made only for a detached share of a device with no foreground share, and a
	// foreground share only for a device with no lend entry. Both are decided inside the edit,
	// which the daemon's own edits take in turn, and the foreground share is started there too,
	// so two shares of one serial cannot each decide before the other has acted.
	created, existed, startedForeground := false, false, false
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
		hostingPatch := map[string]any{"on": on}
		lend := cloneLend(cur.Hosting.Lend)
		existed = slices.ContainsFunc(lend, func(l policy.Lend) bool { return l.Device == p.Device })
		_, held := host.ForegroundOwner(p.Device)
		created = p.Detach && !held && !existed
		// A foreground share is in memory only, started before the listeners are matched so
		// they count it. A retried edit that finds a lend entry now takes it back.
		if existed && startedForeground {
			host.EndForeground(p.Device, hosting.ShareUnshared)
			startedForeground = false
		}
		if !p.Detach && !existed && !startedForeground {
			startedForeground = host.StartForeground(p.Device, call.Origin.ID)
		}
		if created {
			hostingPatch["lend"] = append(lend, policy.Lend{Device: p.Device, To: []string{}})
		}
		return map[string]any{
			"devices": map[string]any{"allowPhysical": true, "allowlist": allow},
			"hosting": hostingPatch,
		}, nil
	})
	if cerr != nil {
		if startedForeground {
			host.EndForeground(p.Device, hosting.ShareUnshared)
		}
		return nil, cerr
	}
	// fail takes back what this call made (a share that listens nowhere is none) and returns e
	// saying what is left: the write that admitted the serial stays, and so does a lending that
	// could not be removed.
	fail := func(e *capability.Error) *capability.Error {
		var undoErr *capability.Error
		if startedForeground {
			host.EndForeground(p.Device, hosting.ShareUnshared)
		}
		if created {
			_, undoErr = host.Edit(context.Background(), func(cur policy.GetResult) (map[string]any, *capability.Error) {
				lend := slices.DeleteFunc(cloneLend(cur.Hosting.Lend), func(l policy.Lend) bool { return l.Device == p.Device })
				return map[string]any{"hosting": map[string]any{"lend": lend}}, nil
			})
		}
		if created || startedForeground {
			host.Reconcile()
		}
		left := p.Device + " stays allowed on this Mac (devices.allowlist)"
		if !wasPhysical && len(others) > 0 {
			left += ", and devices.allowPhysical is now on, which admits every serial already in the allowlist: " + strings.Join(others, ", ")
		}
		left += "; `droidyard device disallow " + p.Device + "` takes that back"
		if undoErr != nil {
			e.Cause = joinNotes(e.Cause, "the lending of "+p.Device+" could not be removed ("+string(undoErr.Code)+": "+undoErr.Message+"), so it is still lent to whoever joins")
			left += "; `droidyard unshare " + p.Device + "` removes the lending"
		}
		e.NextStep = joinNotes(e.NextStep, left)
		return e
	}

	// The connection that asked for a foreground share may have ended while this call worked,
	// and a call is never cancelled by that: the share it owns would outlive it. The
	// connection's end is signalled before its hook runs, so a share registered above is
	// either seen here or ended by the hook.
	if startedForeground && capability.ConnectionGone(ctx) {
		return nil, fail(capability.NewError(ErrUnavailable, "the connection that asked for this share ended before it started, so nothing is lent").
			WithNextStep("share again"))
	}
	host.Reconcile()
	hh := host.Health().Hosting
	port, addrs := boundAddrs(hh.Listening, want, kinds(ifaces))
	if len(addrs) == 0 {
		var why []string
		for _, e := range hh.Errors {
			why = append(why, e.Address+": "+e.Error)
		}
		if len(why) == 0 {
			why = []string{"no listener took an address"}
		}
		return nil, fail(capability.NewError(ErrUnavailable, "the host could not listen on any address").
			WithCause(strings.Join(why, "; ")).
			WithDetails(map[string]any{"errors": hh.Errors}).
			WithNextStep("free the port or fix the address (hosting.port, hosting.listen), then share again; nothing is lent"))
	}
	fp, err := hex.DecodeString(id.Fingerprint())
	if err != nil || len(fp) != 32 {
		return nil, fail(unavailable("this daemon's fingerprint is not a SHA-256"))
	}
	code := hosting.Code{Port: uint16(port)}
	copy(code.Fingerprint[:], fp)
	for _, a := range addrs {
		code.Addrs = append(code.Addrs, hosting.Addr{IP: a.ip, Kind: a.kind})
	}
	// An address a code may not carry (hosting.listen on a public one) is found before a secret is made.
	if _, err := code.Encode(); err != nil {
		return nil, fail(capability.NewError(ErrUnavailable, "the addresses of the host cannot be put in a join code").
			WithCause(err.Error()).
			WithNextStep("listen on a private address (hosting.listen, or --on), then share again; nothing is lent"))
	}
	// A detached share's code is on disk before it is printed (ADR-0108 §2): a write that fails
	// issues nothing, and the share stays as it was.
	// A lend entry just made leaves no code behind: those are of an earlier share that
	// `host unlend` ended.
	if created {
		_ = host.Pending().Void(p.Device)
	}
	secret, err := host.Pending().IssueSaved(p.Device, existed || created)
	if err != nil {
		return nil, fail(capability.NewError(ErrUnavailable, "the join code could not be recorded, so none was issued").
			WithCause(err.Error()).
			WithNextStep("see daemon.log; the state directory must be writable (hosting/shares.json), then share again"))
	}
	code.Secret = secret
	line, err := code.Encode()
	if err != nil {
		_ = host.Pending().Void(p.Device)
		return nil, fail(unavailable(err.Error()))
	}

	// A share on a foreground share that ended meanwhile (its connection closed) has nothing to
	// lend: the code is void, not printed.
	if _, held := host.ForegroundOwner(p.Device); !existed && !created && !held {
		_ = host.Pending().Void(p.Device)
		return nil, fail(capability.NewError(ErrUnavailable, "the foreground share of "+p.Device+" ended while this call was making its code, so nothing is lent").
			WithNextStep("share again"))
	}
	res := ShareResult{Device: p.Device, Code: line, Port: port, Mode: ModeDetached, Addresses: []Address{}}
	if owner, ok := host.ForegroundOwner(p.Device); ok {
		res.Mode, res.Owned = ModeForeground, owner == call.Origin.ID && call.Origin.ID != ""
	}
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
	if res.Mode == ModeDetached {
		res.Notes = append(res.Notes, "This share and its unused codes survive a restart of the daemon. After a restart of this Mac the share resumes when the daemon next starts: any droidyard command starts it.")
	}
	return res, nil
}

// joinNotes joins two sentences of an error's cause or next step.
func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// candidates are the physical devices of the listing, as the person is shown them: emulators
// are not listed at all (ADR-0099 §1, ADR-0108 §1), and a device that cannot be shared says why.
func candidates(listed []device.ShareableDevice) []Candidate {
	out := []Candidate{}
	for _, d := range listed {
		if d.Kind != device.KindPhysical {
			continue
		}
		c := Candidate{Device: d.Device, Transport: "tcp", Manufacturer: d.Manufacturer, Model: d.Model, State: d.State, Shareable: true}
		if d.USB {
			c.Transport = "usb"
		}
		switch {
		case d.Duplicate:
			c.Shareable, c.Reason = false, "more than one device reports this serial, so none of them can be told apart: unplug all but one, or change its serial"
		case d.State == device.StateUnauthorized:
			c.Shareable, c.Reason = false, "unauthorized: accept the debugging prompt on the device"
		case d.State == device.StateOffline:
			c.Shareable, c.Reason = false, "offline: unplug the device and plug it in again"
		case d.State != device.StateDevice:
			c.Shareable, c.Reason = false, "adb reports the device as "+d.State
		}
		out = append(out, c)
	}
	return out
}

// PeersMessage is the progress message of hosting.watchShare: the peers that joined the share
// and which are connected, "anna (connected), bob (not connected)", or "no peers yet". A peer's
// name matches policy.PeerNamePattern, so ParsePeers reads it back.
func PeersMessage(peers []hosting.PeerHealth) string {
	if len(peers) == 0 {
		return "no peers yet"
	}
	parts := make([]string, len(peers))
	for i, p := range peers {
		if p.Connected {
			parts[i] = p.Name + " (connected)"
		} else {
			parts[i] = p.Name + " (not connected)"
		}
	}
	return strings.Join(parts, ", ")
}

// ParsePeers reads a PeersMessage back; a message that is not one yields no peers.
func ParsePeers(msg string) []hosting.PeerHealth {
	out := []hosting.PeerHealth{}
	if msg == "no peers yet" {
		return out
	}
	for _, part := range strings.Split(msg, ", ") {
		if name, ok := strings.CutSuffix(part, " (not connected)"); ok {
			out = append(out, hosting.PeerHealth{Name: name})
		} else if name, ok := strings.CutSuffix(part, " (connected)"); ok {
			out = append(out, hosting.PeerHealth{Name: name, Connected: true})
		}
	}
	return out
}

// watchEvery is how often hosting.watchShare looks at the share's peers.
var watchEvery = 200 * time.Millisecond

func (s *sharing) watch(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(WatchShareParams)
	host, _, cerr := s.ready()
	if cerr != nil {
		return nil, cerr
	}
	// Only the foreground share of the caller's own connection: the watch is bounded by that
	// connection, so nothing long-running is left unattended (ADR-0019 §Sessions, ADR-0108 §6).
	var w *hosting.ShareWatch
	ok := call.Origin.Client == capability.ClientCLI && call.Origin.ID != ""
	if ok {
		w, ok = host.WatchForeground(p.Device, call.Origin.ID)
	}
	if !ok {
		return nil, capability.NewError(capability.ErrInvalidParams, "there is no foreground share of "+p.Device+" owned by this connection to watch").
			WithNextStep("hosting.watchShare is for the connection that made a foreground share: share without detach on this connection first; a detached share is not watched")
	}
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	last := ""
	for {
		peers := w.Peers()
		if msg := PeersMessage(peers); msg != last {
			last = msg
			connected := 0
			for _, q := range peers {
				if q.Connected {
					connected++
				}
			}
			call.Progress(connected, len(peers), msg)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-w.Done():
			return WatchShareResult{Ended: w.Reason()}, nil
		case <-tick.C:
		}
	}
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
	// A foreground share ends too, its watch learning why (ADR-0108 §3, §4).
	if p.Device == "" {
		for _, serial := range host.EndForegroundAll(hosting.ShareUnshared) {
			if !slices.Contains(removed, serial) {
				removed = append(removed, serial)
			}
		}
	} else if host.EndForeground(p.Device, hosting.ShareUnshared) && !slices.Contains(removed, p.Device) {
		removed = append(removed, p.Device)
	}
	// The codes go whether or not an entry did: a share that was edited away by hand is still void.
	var voidErr error
	if p.Device == "" {
		voidErr = host.Pending().VoidAll()
	} else {
		voidErr = host.Pending().Void(p.Device)
	}
	host.Reconcile()
	if voidErr != nil {
		// The codes are void in this daemon, but the file still lists them: if the device is
		// lent again before the file is written (a new share, then a restart), they would come
		// back. Another unshare, or the daemon's next check, writes it.
		return nil, capability.NewError(ErrUnavailable, "the share stopped, but its unused codes could not be removed from hosting/shares.json").
			WithCause(voidErr.Error()).
			WithDetails(map[string]any{"unshared": append([]string{}, removed...)}).
			WithNextStep("see daemon.log; the state directory must be writable, then unshare again")
	}
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
	wasPhysical := false
	var others []string
	_, cerr = host.Edit(ctx, func(cur policy.GetResult) (map[string]any, *capability.Error) {
		wasPhysical = cur.Devices.AllowPhysical
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
		remoteID := name + "/" + serial
		others = nil
		for _, a := range allow {
			if a != remoteID {
				others = append(others, a)
			}
		}
		if !slices.Contains(allow, remoteID) {
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
	out := JoinResult{Remote: name, Device: name + "/" + serial, Connected: connected}
	if !wasPhysical && len(others) > 0 {
		out.Notes = append(out.Notes, "devices.allowPhysical was off; turning it on admits every serial already in devices.allowlist: "+strings.Join(others, ", ")+". `droidyard device disallow <serial>` takes one back.")
	}
	return out, nil
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
