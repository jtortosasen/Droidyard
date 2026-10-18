package capability

import (
	"sort"
	"time"
)

// devHold is one device's holder: the session, since when, and the order the hold was taken
// in (Sessions.devSeq).
type devHold struct {
	id    string
	since time.Time
	seq   uint64
}

// devicesOf is the serials session id holds, sorted; nil when none. mu is held.
func (s *Sessions) devicesOf(id string) []string {
	var out []string
	for serial, h := range s.devHolders {
		if h.id == id {
			out = append(out, serial)
		}
	}
	sort.Strings(out)
	return out
}

// holdDeviceLocked makes session id the holder of serial. mu is held.
func (s *Sessions) holdDeviceLocked(id, serial string) {
	s.devSeq++
	s.devHolders[serial] = devHold{id: id, since: s.now().UTC().Truncate(time.Millisecond), seq: s.devSeq}
}

// releaseDevicesLocked drops every device the session holds, without publishing its row:
// the caller does, once the rest of its change is made. mu is held.
func (s *Sessions) releaseDevicesLocked(id string) {
	for serial, h := range s.devHolders {
		if h.id == id {
			delete(s.devHolders, serial)
		}
	}
}

// OccupyDevice is the decision of an acting call by session id on serial (ADR-0094 §2 to
// §6), taken and applied under the registry's lock, so two sessions acting on one free
// device cannot both hold it. A device nobody holds, or whose holder's record is gone, is
// held by the session; one it holds already stays so. One held by another live session
// of the same chat is left alone: both are one chat (ADR-0094 §1), neither is refused,
// and the second does not hold. One held by an idle session is taken over (ADR-0094 §4).
// One held by a live, non-idle session of another chat is refused (refused is true, held
// describes the holder, nothing has changed) unless the configured conflict is allow, when
// the call goes on and the holder stays (ask is refuse, where Droidyard cannot ask:
// ADR-0094 §6). A session that is not registered holds nothing and is never refused.
func (s *Sessions) OccupyDevice(id, serial string) (held Holder, refused bool) {
	conflict, _ := s.occupationNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return Holder{}, false
	}
	cur, ok := s.devHolders[serial]
	var h *sessionRec
	if ok {
		h = s.byID[cur.id]
	}
	c := claimFree
	switch {
	case !ok || h == nil:
	case cur.id == id:
		c = claimSelf
	case r.chat != "" && r.chat == h.chat:
		c = claimShared
	case h.stale:
		c = claimTakeover
	default:
		c = claimBlocked
	}
	if hook := s.occupyHook.Load(); hook != nil {
		(*hook)()
	}
	switch {
	case c == claimBlocked && conflict != OccupationAllow:
		return Holder{Name: h.name, Since: cur.since}, true
	case c == claimFree || c == claimTakeover:
		s.holdDeviceLocked(id, serial)
		s.publish(id, r)
		if c == claimTakeover {
			s.publish(cur.id, h)
		}
	}
	return Holder{}, false
}

// TakeDevice makes session id the holder of serial, the emulator it has just booted,
// whoever held it: a fresh boot is a device nobody could be using, and emulator.start is
// never refused (ADR-0094 §2). A session that is not registered holds nothing.
func (s *Sessions) TakeDevice(id, serial string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return
	}
	cur, ok := s.devHolders[serial]
	if ok && cur.id == id {
		return
	}
	s.holdDeviceLocked(id, serial)
	s.publish(id, r)
	if ok {
		if h := s.byID[cur.id]; h != nil {
			s.publish(cur.id, h)
		}
	}
}

// AdoptDevice makes session id the holder of serial, an emulator already running that
// emulator.start adopted, only when nobody holds it: adopting is not acting, so it takes
// nothing from a holder (ADR-0094 §2). A session that is not registered holds nothing.
func (s *Sessions) AdoptDevice(id, serial string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return
	}
	if cur, ok := s.devHolders[serial]; ok && s.byID[cur.id] != nil {
		return
	}
	s.holdDeviceLocked(id, serial)
	s.publish(id, r)
}

// ReleaseDevice ends whatever occupation serial has; a device nobody holds is already
// free (ADR-0094 §3). It never changes a binding.
func (s *Sessions) ReleaseDevice(serial string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseDeviceLocked(serial)
}

// releaseDeviceLocked drops serial's holder and publishes its row. mu is held.
func (s *Sessions) releaseDeviceLocked(serial string) {
	cur, ok := s.devHolders[serial]
	if !ok {
		return
	}
	delete(s.devHolders, serial)
	if h := s.byID[cur.id]; h != nil {
		s.publish(cur.id, h)
	}
}

// DeviceMark is the order of the last hold taken. A caller reads it before it lists the
// devices and gives it back to ForgetDevicesAbsent, so a hold taken after the listing
// was read is not released on the listing's word.
func (s *Sessions) DeviceMark() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devSeq
}

// ForgetDevicesAbsent releases every held device whose serial is not in present, the
// serials one `adb devices` listing reported: a device that left the listing is free
// (ADR-0094 §3). mark is DeviceMark as it stood before that listing was read; a hold
// taken after it is kept.
func (s *Sessions) ForgetDevicesAbsent(present []string, mark uint64) {
	in := make(map[string]bool, len(present))
	for _, p := range present {
		in[p] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for serial, h := range s.devHolders {
		if !in[serial] && h.seq <= mark {
			s.releaseDeviceLocked(serial)
		}
	}
}

// DeviceHolder reports who holds serial, for the session asking ("" for the UI or the CLI,
// which are told of every holder). A holder that is the asking session, or a session of the
// asking session's chat, is not reported: occupiedBy names another chat (ADR-0094 §1).
func (s *Sessions) DeviceHolder(serial, asking string) (Holder, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.devHolders[serial]
	if !ok {
		return Holder{}, false
	}
	h := s.byID[cur.id]
	if h == nil || cur.id == asking {
		return Holder{}, false
	}
	if a := s.byID[asking]; a != nil && a.chat != "" && a.chat == h.chat {
		return Holder{}, false
	}
	return Holder{Name: h.name, Since: cur.since, Stale: h.stale}, true
}
