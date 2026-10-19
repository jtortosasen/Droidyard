package device

import "testing"

// Decision (plan m2t step 5): a paired peer's session may hold peerStreamsMax Logcat streams
// of a device at once, because every tab of a client shares one link session; the next
// replaces the peer's oldest, which add returns so the caller ends it before starting a child.
// A local session keeps ADR-0041's rule: its second stream replaces its first (replace),
// and add evicts nothing. The streams of another session or device are not counted.
func TestPeerStreamsAreCappedNotReplaced(t *testing.T) {
	var st streamSet
	key := streamKey{session: "peer-link", serial: "R58M001"}
	var held []*logStream
	for i := range peerStreamsMax {
		s := &logStream{key: key, peer: true}
		evicted, err := st.add(s)
		if err != nil || evicted != nil {
			t.Fatalf("stream %d: evicted %v, %v; want room for %d", i+1, evicted != nil, err, peerStreamsMax)
		}
		if old := st.replace(s); old != nil {
			t.Fatalf("stream %d: replace returned a stream; a peer's streams are not replaced by the session rule", i+1)
		}
		held = append(held, s)
	}
	other := &logStream{key: streamKey{session: "peer-link", serial: "R58M002"}, peer: true}
	if evicted, _ := st.add(other); evicted != nil {
		t.Errorf("a stream of another device evicted %v", evicted)
	}
	fifth := &logStream{key: key, peer: true}
	evicted, _ := st.add(fifth)
	if evicted != held[0] {
		t.Fatalf("the fifth stream evicted %p; want the oldest, %p", evicted, held[0])
	}
	st.remove(evicted)
	if sixth, _ := st.add(&logStream{key: key, peer: true}); sixth != held[1] {
		t.Errorf("the sixth stream evicted %p; want the next oldest, %p", sixth, held[1])
	}

	local := streamKey{session: "ws-1", serial: "emulator-5554"}
	a, b := &logStream{key: local}, &logStream{key: local}
	if e, _ := st.add(a); e != nil {
		t.Fatal("a local stream evicted at add")
	}
	if old := st.replace(a); old != nil {
		t.Fatal("the first local stream replaced something")
	}
	if e, _ := st.add(b); e != nil {
		t.Fatal("a local stream evicted at add")
	}
	if old := st.replace(b); old != a {
		t.Errorf("a local session's second stream replaced %p; want its first, %p", old, a)
	}
}
