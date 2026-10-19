package hosting

import (
	"strings"
	"testing"
)

const (
	admitFPa = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	admitFPb = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	admitFPc = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// TestAdmitPairsAndLends is the host's side of a join (ADR-0107 §5; ADR-0108 §2): the
// fingerprint enters hosting.peers and its name the device's lend `to` in one write; a
// fingerprint already known keeps its name whatever it proposes; another that proposes the
// same name gets the next one; the same fingerprint again changes nothing. Reversing the
// decision (a new name for a known fingerprint, a taken name reused) fails a row.
func TestAdmitPairsAndLends(t *testing.T) {
	old := hostname
	hostname = func() (string, error) { return "Studio.local", nil }
	t.Cleanup(func() { hostname = old })
	state := t.TempDir()
	h, pol, _ := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"peers":[{"name":"old-name","fingerprint":"`+admitFPc+`"}],"lend":[{"device":"R1","to":[]},{"device":"R2","to":["old-name"]}]}}`)

	peer, host, err := h.Admit(admitFPa, "Anna's Mac", "R1")
	if err != nil || peer != "anna-s-mac" || host != "studio" {
		t.Fatalf("Admit = %q, %q, %v; want anna-s-mac named by the host as studio", peer, host, err)
	}
	// A second machine proposing the same name is the next one.
	if peer, _, err := h.Admit(admitFPb, "Anna's Mac", "R1"); err != nil || peer != "anna-s-mac-2" {
		t.Fatalf("a second machine of the same name: %q, %v; want anna-s-mac-2", peer, err)
	}
	// A fingerprint already paired keeps its name, whatever it proposes now, and is lent the new device.
	if peer, _, err := h.Admit(admitFPc, "Something Else", "R1"); err != nil || peer != "old-name" {
		t.Fatalf("a known fingerprint: %q, %v; want its name old-name", peer, err)
	}
	// The same fingerprint again changes nothing: no second peer, no second `to`.
	rev := pol.Get().Revision
	if peer, _, err := h.Admit(admitFPa, "Anna's Mac", "R1"); err != nil || peer != "anna-s-mac" {
		t.Fatalf("a second join of the same fingerprint: %q, %v", peer, err)
	}
	if pol.Get().Revision != rev {
		t.Error("admitting what is already admitted rewrote the configuration")
	}

	got := pol.Get().Hosting
	if len(got.Peers) != 3 {
		t.Errorf("peers = %+v; want old-name, anna-s-mac, anna-s-mac-2", got.Peers)
	}
	wantTo := map[string]string{"R1": "anna-s-mac,anna-s-mac-2,old-name", "R2": "old-name"}
	for _, l := range got.Lend {
		if strings.Join(l.To, ",") != wantTo[l.Device] {
			t.Errorf("%s is lent to %v; want %s", l.Device, l.To, wantTo[l.Device])
		}
	}
}

// TestAdmitNeverTakesALentName: a name lent to before it was admitted (host lend --to bob)
// is not given to a joiner who proposes it, or a code for R1 would also hand over R2.
func TestAdmitNeverTakesALentName(t *testing.T) {
	state := t.TempDir()
	h, pol, _ := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"lend":[{"device":"R1","to":[]},{"device":"R2","to":["bob"]}]}}`)
	peer, _, err := h.Admit(admitFPa, "bob", "R1")
	if err != nil || peer != "bob-2" {
		t.Fatalf("Admit proposing a lent name = %q, %v; want bob-2", peer, err)
	}
	if pol.Lent(peer, "R2") {
		t.Errorf("%s joined with a code for R1 and is lent R2", peer)
	}
}

// TestAdmitRefusalsLeakNoName: Admit's errors reach daemon.log (the peer adapter logs
// them), and the proposed name is another machine's text. A device no longer lent refuses,
// an unusable configuration refuses, each says the code is spent, and neither writes nor
// quotes the name (step 1 verifier). Reversing it (the name in the error) fails the
// Contains rows.
func TestAdmitRefusalsLeakNoName(t *testing.T) {
	const hostile = "../Évil\x1b[31mHost"
	for _, c := range []struct {
		name, config string
	}{
		{"a device no longer lent", `{"hosting":{"lend":[{"device":"OTHER","to":[]}]}}`},
		{"no lending at all", `{"schemaVersion":1}`},
		{"an unusable configuration", `{"hosting":{"lend":"R1"}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := t.TempDir()
			h, pol, _ := daemonAt(t, state)
			writeConfig(t, state, c.config)
			before := pol.Get().Revision
			peer, host, err := h.Admit(admitFPa, hostile, "R1")
			if err == nil || peer != "" || host != "" {
				t.Fatalf("Admit = %q, %q, %v; want a refusal", peer, host, err)
			}
			for _, bad := range []string{"vil", "Host", "\x1b", "../"} {
				if strings.Contains(err.Error(), bad) {
					t.Errorf("the error carries the proposed name (%q): %q", bad, err)
				}
			}
			if !strings.Contains(err.Error(), "spent") {
				t.Errorf("the error does not say the code is spent: %q", err)
			}
			if pol.Get().Revision != before {
				t.Error("a refused join wrote the configuration")
			}
		})
	}
}
