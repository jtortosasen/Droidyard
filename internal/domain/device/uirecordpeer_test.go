package device

import (
	"slices"
	"testing"
	"time"
)

// seedEndedAt saves an ended record that started i hours after a fixed day and ended a minute
// later, bytes large, started by the peer named (the host's UI when peer is empty).
func seedEndedAt(t *testing.T, r *UIRecordings, i int, peer string, bytes int64) string {
	t.Helper()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
	end := at.Add(time.Minute)
	st := RecordingStarter{Client: "ui"}
	if peer != "" {
		st = RecordingStarter{Client: "peer", Session: "link-" + peer, Peer: peer}
	}
	id := recID(i)
	if err := r.Save(UIRecord{ID: id, Device: "R58M001", MaxSeconds: 900, StartedBy: st, State: RecordingEnded,
		StartedAt: at, EndedAt: &end, EndReason: EndStopped, Bytes: bytes}); err != nil {
		t.Fatal(err)
	}
	return id
}

// Decision (ADR-0115 Decision 3): a peer keeps at most MaxPerPeer ended recordings and
// MaxPerPeerBytes on the host, its oldest going first, and only its own: another peer's and the
// host's are untouched for it, however old, while the global bounds still hold afterwards.
// Seen failing with the per-peer bound removed: nothing was swept, the global bounds being far
// off.
func TestSweepBoundsAPeerBeforeTheGlobalBound(t *testing.T) {
	r, _, _ := newRecordings(t)
	r.MaxRecordings, r.MaxBytes = 50, 1<<40
	r.MaxPerPeer, r.MaxPerPeerBytes = 2, 1<<40
	// anna's three are the oldest and the newest of all; bob's and the host's lie between.
	a1 := seedEndedAt(t, r, 1, "anna", 10)
	b1 := seedEndedAt(t, r, 2, "bob", 5)
	h1 := seedEndedAt(t, r, 3, "", 10)
	a2 := seedEndedAt(t, r, 4, "anna", 10)
	b2 := seedEndedAt(t, r, 5, "bob", 5)
	a3 := seedEndedAt(t, r, 6, "anna", 10)

	gone, err := r.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gone, []string{a1}) {
		t.Fatalf("swept %v; want only anna's oldest %s (bob has two, the host's one is not a peer's)", gone, a1)
	}
	left, _ := r.List()
	var got []string
	for _, rec := range left {
		got = append(got, rec.ID)
	}
	for _, id := range []string{b1, h1, a2, b2, a3} {
		if !slices.Contains(got, id) {
			t.Errorf("recording %s was swept for anna's bound", id)
		}
	}

	// By size, the same: anna's two hold 20 bytes over a limit of 15 (bob's two hold 10), so her older goes.
	r.MaxPerPeer, r.MaxPerPeerBytes = 10, 15
	gone, _ = r.Sweep()
	if !slices.Equal(gone, []string{a2}) {
		t.Fatalf("size sweep swept %v; want anna's older of the two, %s", gone, a2)
	}

	// The global bound still applies to what is left, peers' recordings included.
	r.MaxPerPeer, r.MaxPerPeerBytes = 10, 1<<40
	r.MaxRecordings = 3
	gone, _ = r.Sweep()
	if !slices.Equal(gone, []string{b1}) {
		t.Fatalf("global sweep swept %v; want the oldest left, %s", gone, b1)
	}
}
