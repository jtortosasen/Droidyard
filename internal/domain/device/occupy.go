package device

import (
	"context"
	"strconv"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// ErrOccupied is DEVICE_OCCUPIED: an acting call by an MCP session on a device another
// live, non-idle session of another chat holds (ADR-0094 §2, §5), under the configured
// conflict refuse or ask (ask is refuse where Droidyard cannot ask, ADR-0094 §6). It is
// returned before any adb command that changes the device. The message names the holder
// ("another chat" with its client's name and since when); nextStep: another device or
// emulator, or the user's release in the devices panel. An MCP session's reads, the UI and
// the CLI are never refused, and emulator.start never is. A paired peer's call of any kind
// on a lent device is refused one held by another live, non-idle session, whatever the
// conflict says (ADR-0104 §2).
const ErrOccupied capability.ErrorCode = "DEVICE_OCCUPIED"

// ActingErrors lists the codes a capability that occupies the device it acts on can
// return: the preamble's and DEVICE_OCCUPIED, for a descriptor's Errors.
var ActingErrors = append(append([]capability.ErrorCode{}, PreambleErrors...), ErrOccupied)

// LentErrors lists the codes a Lendable capability that does not act can return: a paired
// peer's call of any kind holds the lent device it names, reads and the live view included,
// so DEVICE_OCCUPIED is one of them (ADR-0104 §2). Where the same capability runs for a
// local caller it is never returned.
var LentErrors = append(append([]capability.ErrorCode{}, PreambleErrors...), ErrOccupied)

// Occupation is the registry of who holds which device, as the device and emulator
// packages need it (ADR-0094): *capability.Sessions implements it, and the daemon gives it
// at wiring, as it gives the Policy. A nil Occupation holds nothing and refuses nothing.
type Occupation interface {
	// OccupyDevice decides and applies an acting call's hold; see capability.Sessions.
	OccupyDevice(session, serial string) (held capability.Holder, refused bool)
	// LeaseDevice is OccupyDevice for a paired peer's call: a session that is gone is refused.
	LeaseDevice(session, serial string) (held capability.Holder, refused, gone bool)
	// HoldsDevice reports whether the session holds the serial now.
	HoldsDevice(session, serial string) bool
	// TakeDevice makes the session the holder of a serial it has booted, whoever held it.
	TakeDevice(session, serial string)
	// AdoptDevice makes the session the holder of a serial it adopted, only when free.
	AdoptDevice(session, serial string)
	// ReleaseDevice frees a serial.
	ReleaseDevice(serial string)
	// ReleaseDeviceOf frees a serial only while the session still holds it.
	ReleaseDeviceOf(session, serial string)
	// PeerHolds are the devices a paired peer's link holds (ADR-0104).
	PeerHolds() []capability.PeerHold
	// StreamOpened and StreamClosed count a peer link's open streams, which are activity.
	StreamOpened(session string)
	StreamClosed(session string)
	// UseLeaseEnd sets what is told, outside the registry's lock, when a peer's hold ends by
	// the owner's release or ReleaseDeviceOf.
	UseLeaseEnd(f func(session, serial string))
	// DeviceHolder is who holds a serial, as the asking session is told.
	DeviceHolder(serial, asking string) (capability.Holder, bool)
	// DeviceMark and ForgetDevicesAbsent release the held serials a listing no longer has.
	DeviceMark() uint64
	ForgetDevicesAbsent(present []string, mark uint64, keep func(serial string) bool)
}

// SetOccupation gives the ADB the registry of device occupations. It is called once, at
// wiring, before any call. Without one no call holds or is refused a device.
func (a *ADB) SetOccupation(o Occupation) {
	a.occupation = o
	if o == nil {
		return
	}
	// The end of a peer's lease by the owner's release stops the holder's streams (ADR-0104 §4).
	o.UseLeaseEnd(a.endLease)
}

// occupant is the session that holds on the call's behalf: the MCP session itself. A
// call of the UI or the CLI is no one's, and neither is one joined to an MCP session by its
// chat (Origin.Joined, ADR-0095 §1): it is attributed to the session, not made by it.
func occupant(call *capability.Call) string {
	if call == nil || call.Origin.Client != capability.ClientMCP {
		return ""
	}
	return call.Origin.ID
}

// OccupyFor is the check of an acting call on serial, run in the preamble after the
// policy check (ADR-0094 §2): the call's MCP session holds a free serial, stays on its
// own, takes an idle holder's over, and is refused a live, non-idle other's with
// DEVICE_OCCUPIED. A call that is not an MCP session's is neither.
func (a *ADB) OccupyFor(call *capability.Call, serial string) error {
	id := occupant(call)
	if id == "" || a.occupation == nil {
		return nil
	}
	if held, refused := a.occupation.OccupyDevice(id, serial); refused {
		return occupied(serial, held, false)
	}
	return nil
}

// leaseFor is the lease of a paired peer's call on a lent device, run in the preamble after
// the lending check (ADR-0104 §2): the peer's link session holds a free device, stays on its
// own and takes an idle holder's over, and is refused a live, non-idle other's with
// DEVICE_OCCUPIED, whatever occupation.conflict says. Any call holds, reads included.
func (a *ADB) leaseFor(who capability.Origin, serial string) error {
	if a.occupation == nil || who.Client != capability.ClientPeer || who.ID == "" {
		return nil
	}
	held, refused, gone := a.occupation.LeaseDevice(who.ID, serial)
	switch {
	case refused:
		return occupied(serial, held, true)
	case gone:
		// The call waited its turn while its link ended: it never passes the preamble, or it
		// would read a device another peer has leased since (ADR-0104 §2). Nobody holds the
		// device, so there is no holder to name; DEVICE_DISCONNECTED, a code every lent
		// capability declares, says the call's side of the link is gone.
		return capability.NewError(ErrDisconnected, "the link this call came on ended before it reached the device "+serial).
			WithCause("the peer's link closed while the call was waiting; nothing was run on the device").
			WithNextStep("Call again on the new link; nothing on the device was changed, so this call is safe to repeat.")
	}
	return nil
}

// TakeFor makes the call's MCP session the holder of serial, an emulator it has booted
// (ADR-0094 §2).
func (a *ADB) TakeFor(call *capability.Call, serial string) {
	if a.occupation == nil {
		return
	}
	if id := occupant(call); id != "" {
		a.occupation.TakeDevice(id, serial)
		return
	}
	// A boot by the UI or the CLI holds nothing, and a hold left on the serial by an
	// emulator that vanished unseen is no one's: the booted device is new.
	a.occupation.ReleaseDevice(serial)
}

// AdoptFor makes the call's MCP session the holder of serial, an emulator it adopted,
// only when nobody holds it (ADR-0094 §2).
func (a *ADB) AdoptFor(call *capability.Call, serial string) {
	if id := occupant(call); id != "" && a.occupation != nil {
		a.occupation.AdoptDevice(id, serial)
	}
}

// ReleaseDevice frees serial: its emulator was stopped (ADR-0094 §3).
func (a *ADB) ReleaseDevice(serial string) {
	if a.occupation != nil {
		a.occupation.ReleaseDevice(serial)
	}
}

// occupied is DEVICE_OCCUPIED for serial held by holder; the words for the holder are the
// ones a refused write is told (capability.Holder.Who). A paired peer's way out is to wait
// or to ask the host's user (ADR-0104 §3), not an emulator of its own.
func occupied(serial string, holder capability.Holder, forPeer bool) *capability.Error {
	err := capability.Errorf(ErrOccupied, "%s uses the device %s", holder.Who(), serial)
	if forPeer {
		return err.WithNextStep("try again later (a lease unused for " + strconv.Itoa(int(capability.PeerLeaseIdle.Minutes())) + " minutes is taken over), or ask the person who runs the host to release " + serial)
	}
	return err.WithNextStep("use another device or emulator (emulator_start), or ask the user to release " + serial + " in Droidyard's devices panel")
}

// OccupiedBy is the holder of a device, as another session is told: no session id
// (ADR-0048 §2).
type OccupiedBy struct {
	Name  string    `json:"name,omitempty" jsonschema_description:"What the holder's client called itself, bounded to 64 characters; absent until it has said. Every Claude Code chat is named claude-code. A paired peer is named by the host's configuration."`
	Since time.Time `json:"since" jsonschema:"required" jsonschema_description:"When the holder took the device."`
	Stale bool      `json:"stale,omitempty" jsonschema_description:"True while the holder has made no request for the configured time (15 minutes, and no open stream, for a paired peer): an acting call of another session, any call for a peer, takes the device over. Absent otherwise."`
	Peer  bool      `json:"peer,omitempty" jsonschema_description:"True when the holder is a paired peer's link, so a peer named anna is told from a client that calls itself anna. Absent otherwise."`
}

// markOccupied fills OccupiedBy on the listed devices for the session asking ("" for none).
func (a *ADB) markOccupied(devices []Device, asking string) {
	if a.occupation == nil {
		return
	}
	for i := range devices {
		if h, ok := a.occupation.DeviceHolder(devices[i].Device, asking); ok {
			devices[i].OccupiedBy = &OccupiedBy{Name: h.Name, Since: h.Since, Stale: h.Stale, Peer: h.Peer}
		}
	}
}

// listAllTracked is listAll that also tells the occupation which serials the listing held,
// so a device that left it is freed (ADR-0094 §3). The mark is read before the listing, so
// a hold taken after it is not released on its word.
func (a *ADB) listAllTracked(ctx context.Context, path string) ([]Device, error) {
	var mark uint64
	if a.occupation != nil {
		mark = a.occupation.DeviceMark()
	}
	listed, err := a.listAll(ctx, path)
	if err != nil {
		return nil, err
	}
	if a.occupation != nil {
		serials := make([]string, len(listed))
		for i := range listed {
			serials[i] = listed[i].Device
		}
		// A remote device's id is no adb serial: no local listing has it (ADR-0100 §1).
		a.occupation.ForgetDevicesAbsent(serials, mark, a.isRemoteID)
	}
	return listed, nil
}
