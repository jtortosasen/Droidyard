package artifact

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// forge is ref with its random bits replaced: a guess at a live artifact's counter.
func forge(ref string) string { return ref[:strings.LastIndex(ref, ".")+1] + "000000000000" }

// Decision (ADR-0099 §2, plan m2t step 5): a paired peer reads an ephemeral artifact only if
// one of its own calls made it. Another peer's, the host's own (no owner), a durable
// reference, a reader without an owner, a guess at a live artifact's counter with the wrong
// random bits, an expired id, another run's id and an id that never was are all
// ARTIFACT_NOT_FOUND and nothing else: the error never names a producer, so a peer learns
// nothing of what the host holds (the host agent's shell and build output included). Seen
// failing with the lookup before the owner check: a forged id of a live artifact was
// ARTIFACT_EXPIRED, naming its producer.
func TestGetOwnedIsTheOwnersOnly(t *testing.T) {
	s, clk := newStore(t, 1<<20)
	old, _ := s.Put(Item{Data: []byte("old"), MediaType: "text/plain", Owner: PeerOwner("anna"), Producer: "device.logs"})
	clk.Advance(DefaultTTL + 1)
	theirs, _ := s.Put(Item{Data: []byte("theirs"), MediaType: "text/plain", Owner: PeerOwner("bob"), Producer: "device.logs"})
	hosts, _ := s.Put(Item{Data: []byte("the host's"), MediaType: "text/plain", Producer: "shell.run"})
	fresh, _ := s.Put(Item{Data: []byte("fresh"), MediaType: "text/plain", Owner: PeerOwner("anna"), Producer: "device.logs"})

	e, err := s.GetOwned(fresh.Ref, PeerOwner("anna"))
	if err != nil || string(e.Data) != "fresh" {
		t.Fatalf("the owner's read: %q, %v", e.Data, err)
	}
	for name, c := range map[string]struct{ ref, owner string }{
		"another peer's artifact":             {theirs.Ref, PeerOwner("anna")},
		"the host's own artifact":             {hosts.Ref, PeerOwner("anna")},
		"a reader without an owner":           {fresh.Ref, ""},
		"a durable reference":                 {RefPrefix + "d.0123456789abcdef.1", PeerOwner("anna")},
		"a durable reference by no peer":      {RefPrefix + "d.0123456789abcdef.1", ""},
		"a name that is a prefix":             {fresh.Ref, PeerOwner("fres")},
		"forged bits on a live host artifact": {forge(hosts.Ref), PeerOwner("anna")},
		"forged bits on another peer's":       {forge(theirs.Ref), PeerOwner("anna")},
		"forged bits on the owner's own":      {forge(fresh.Ref), PeerOwner("anna")},
		"an expired artifact of the owner":    {old.Ref, PeerOwner("anna")},
		"an id above the highest issued":      {RefPrefix + "e.d-0a1b2c3d.9999.0123456789ab", PeerOwner("anna")},
		"an id of another daemon run":         {strings.Replace(fresh.Ref, "d-0a1b2c3d", "d-ffffffff", 1), PeerOwner("anna")},
		"a reference that is not one":         {RefPrefix + "e.d-0a1b2c3d.x", PeerOwner("anna")},
		"forged bits on an expired id":        {forge(old.Ref), PeerOwner("anna")},
	} {
		_, err := s.GetOwned(c.ref, c.owner)
		var ce *capability.Error
		if !errors.As(err, &ce) || ce.Code != capability.ErrArtifactNotFound {
			t.Errorf("%s: %v; want ARTIFACT_NOT_FOUND", name, err)
			continue
		}
		raw, _ := json.Marshal(ce)
		for _, leak := range []string{"shell.run", "device.logs", "again", "anna", "bob", "expired", "gone"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s: the error says %q: %s", name, leak, raw)
			}
		}
	}
}

// Decision (ADR-0099 §2): only a paired peer's call produces an owned artifact; the host's own
// clients, and a peer whose origin has no name, produce none, so no peer can read them.
func TestOwnerOfIsThePeersNameOnly(t *testing.T) {
	for _, c := range []struct {
		o    capability.Origin
		want string
	}{
		{capability.Origin{Client: capability.ClientPeer, Name: "anna"}, "peer:anna"},
		{capability.Origin{Client: capability.ClientPeer}, ""},
		{capability.Origin{Client: capability.ClientUI, Name: "anna"}, ""},
		{capability.Origin{Client: capability.ClientMCP, Name: "anna"}, ""},
		{capability.Origin{Client: capability.ClientCLI, Name: "anna"}, ""},
	} {
		if got := OwnerOf(c.o); got != c.want {
			t.Errorf("OwnerOf(%+v) = %q; want %q", c.o, got, c.want)
		}
	}
}
