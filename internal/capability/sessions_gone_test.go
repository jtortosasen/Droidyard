package capability

import "testing"

// TestLeaseDeviceRefusesAGoneLink is ADR-0104 §2 for a call that outlives its link: a peer
// session that is no longer registered is refused (gone), naming the live holder when there
// is one, where OccupyDevice lets an unregistered session through as it always did for MCP;
// HoldsDevice is true only for the live holder. Seen failing with LeaseDevice answering as
// OccupyDevice does (the gone call went on).
func TestLeaseDeviceRefusesAGoneLink(t *testing.T) {
	s, _, _ := leased(OccupationRefuse)
	s.OccupyDevice("bob", leaseSerial)
	s.End("anna")

	held, refused, gone := s.LeaseDevice("anna", leaseSerial)
	if !refused || !gone || !held.Peer || held.Name != "bob" {
		t.Errorf("a gone link on bob's device got (%+v, refused %v, gone %v); want refused, gone, naming peer bob", held, refused, gone)
	}
	if held, refused, gone := s.LeaseDevice("anna", "R58M002"); refused || !gone || held != (Holder{}) {
		t.Errorf("a gone link on a free device got (%+v, refused %v, gone %v); want gone, with no holder to name", held, refused, gone)
	}
	if _, ok := s.DeviceHolder("R58M002", ""); ok {
		t.Error("a gone link took a lease")
	}
	if _, refused := s.OccupyDevice("anna", leaseSerial); refused {
		t.Error("OccupyDevice no longer lets an unregistered session through")
	}
	if !s.HoldsDevice("bob", leaseSerial) || s.HoldsDevice("anna", leaseSerial) || s.HoldsDevice("bob", "R58M002") {
		t.Error("HoldsDevice is not true for the live holder alone")
	}
	s.End("bob")
	if s.HoldsDevice("bob", leaseSerial) {
		t.Error("HoldsDevice is true for a session that ended")
	}
}
