package policy_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// The hosting and remotes sections (ADR-0098 §2, §3, §6): each rule that makes the file
// unusable, and the patch that writes them.

const (
	fpA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fpB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// TestHostingConfig pins every validation row of Q2 §6. A value that fails makes the whole
// file unusable (ADR-0032 §4) rather than being skipped: a peer line dropped in silence
// would be a peer admitted or refused by accident. The cause names the key, and the policy
// then lends nothing and pins nothing; a physical device is refused with it, as for any
// unusable file.
func TestHostingConfig(t *testing.T) {
	good := `{"schemaVersion":1,
		"hosting":{"listen":"127.0.0.1:7318","peers":[{"name":"anna","fingerprint":"` + fpA + `"},{"name":"bob-2","fingerprint":"` + fpB + `"}],
			"lend":[{"device":"R58M001","to":["anna","bob-2"]}]},
		"remotes":[{"name":"qa-lab","address":"10.0.0.5:7318","fingerprint":"` + fpA + `"}]}`
	peer := func(name, fp string) string { return `{"name":"` + name + `","fingerprint":"` + fp + `"}` }
	// remote is a remote with the given addresses list, spelled as the file has it.
	remote := func(addresses string) string {
		return `{"remotes":[{"name":"qa","address":"10.0.0.5:7318","addresses":` + addresses + `,"fingerprint":"` + fpA + `"}]}`
	}
	nine := "[" + strings.TrimSuffix(strings.Repeat(`{"address":"10.0.0.6:7318","kind":"lan"},`, 9), ",") + "]"
	eight := "[" + strings.TrimSuffix(strings.Repeat(`{"address":"10.0.0.6:7318","kind":"lan"},`, 8), ",") + "]"
	rows := []struct {
		name, body string
		cause      string // empty: the file is usable
	}{
		{"a full configuration", good, ""},
		{"nothing set", `{"schemaVersion":1,"hosting":{},"remotes":[]}`, ""},
		{"listen on loopback with an ephemeral port", `{"hosting":{"listen":"127.0.0.1:0"}}`, ""},
		{"listen on an IPv6 address", `{"hosting":{"listen":"[::1]:7318"}}`, ""},
		{"null sections are their defaults", `{"hosting":null,"remotes":null}`, ""},

		{"listen on every IPv4 interface", `{"hosting":{"listen":"0.0.0.0:7318"}}`, "hosting.listen"},
		{"listen on every IPv6 interface", `{"hosting":{"listen":"[::]:7318"}}`, "hosting.listen"},
		{"listen on a host name", `{"hosting":{"listen":"qa.local:7318"}}`, "hosting.listen"},
		{"listen with no port", `{"hosting":{"listen":"10.0.0.5"}}`, "hosting.listen"},
		{"listen that is not a string", `{"hosting":{"listen":7318}}`, "hosting.listen"},

		// hosting.on (ADR-0109 §1): a word, or one IP literal on a private, 100.64.0.0/10 or
		// loopback address; never a public, unspecified, link-local, multicast or IPv6 one.
		{"on private", `{"hosting":{"on":"private"}}`, ""},
		{"on vpn", `{"hosting":{"on":"vpn"}}`, ""},
		{"on lan", `{"hosting":{"on":"lan"}}`, ""},
		{"on an RFC 1918 address", `{"hosting":{"on":"10.221.240.50"}}`, ""},
		{"on a 100.64.0.0/10 address", `{"hosting":{"on":"100.64.0.18"}}`, ""},
		{"on a loopback address", `{"hosting":{"on":"127.0.0.1"}}`, ""},
		{"on null", `{"hosting":{"on":null}}`, ""},
		{"on a public address", `{"hosting":{"on":"8.8.8.8"}}`, "hosting.on"},
		{"on 172.32.0.1, just outside RFC 1918", `{"hosting":{"on":"172.32.0.1"}}`, "hosting.on"},
		{"on the unspecified address", `{"hosting":{"on":"0.0.0.0"}}`, "hosting.on"},
		{"on a link-local address", `{"hosting":{"on":"169.254.1.1"}}`, "hosting.on"},
		{"on a multicast address", `{"hosting":{"on":"224.0.0.1"}}`, "hosting.on"},
		{"on an IPv6 address", `{"hosting":{"on":"fd00::1"}}`, "hosting.on"},
		{"on an address with a port", `{"hosting":{"on":"10.0.0.5:7318"}}`, "hosting.on"},
		{"on a host name", `{"hosting":{"on":"qa.local"}}`, "hosting.on"},
		{"on in capitals", `{"hosting":{"on":"Private"}}`, "hosting.on"},
		{"on that is not a string", `{"hosting":{"on":true}}`, "hosting.on"},

		{"port 1", `{"hosting":{"port":1}}`, ""},
		{"port 65535", `{"hosting":{"port":65535}}`, ""},
		{"port null", `{"hosting":{"port":null}}`, ""},
		{"port 0", `{"hosting":{"port":0}}`, "hosting.port"},
		{"port 65536", `{"hosting":{"port":65536}}`, "hosting.port"},
		{"port negative", `{"hosting":{"port":-1}}`, "hosting.port"},
		{"port as text", `{"hosting":{"port":"7318"}}`, "hosting.port"},
		{"port with a fraction", `{"hosting":{"port":7318.5}}`, "hosting.port"},

		// remotes[].addresses (ADR-0109 §4): at most 8, each an address and a port tagged vpn or lan.
		{"a remote with tagged addresses", remote(`[{"address":"192.168.1.20:7318","kind":"lan"},{"address":"100.64.0.18:7318","kind":"vpn"}]`), ""},
		{"a remote with an empty addresses list", remote(`[]`), ""},
		{"a remote with null addresses", remote(`null`), ""},
		{"a remote with 8 addresses", remote(eight), ""},
		{"a remote with 9 addresses", remote(nine), "remotes[0].addresses"},
		{"an address of a remote that is a host name", remote(`[{"address":"qa.local:7318","kind":"lan"}]`), "remotes[0].addresses[0].address"},
		{"an address of a remote on port 0", remote(`[{"address":"10.0.0.6:0","kind":"lan"}]`), "remotes[0].addresses[0].address"},
		{"an address of a remote with another kind", remote(`[{"address":"10.0.0.6:7318","kind":"wifi"}]`), "remotes[0].addresses[0].kind"},
		{"an address of a remote with no kind", remote(`[{"address":"10.0.0.6:7318"}]`), "remotes[0].addresses[0].kind"},
		{"an address of a remote with no address", remote(`[{"kind":"lan"}]`), "remotes[0].addresses[0].address"},
		{"addresses of a remote that are strings", remote(`["10.0.0.6:7318"]`), "remotes[0].addresses[0]"},
		{"addresses of a remote that are an object", remote(`{"lan":"10.0.0.6:7318"}`), "remotes[0].addresses"},

		{"a peer name in capitals", `{"hosting":{"peers":[` + peer("Anna", fpA) + `]}}`, "hosting.peers[0].name"},
		{"a peer name that starts with a digit", `{"hosting":{"peers":[` + peer("1anna", fpA) + `]}}`, "hosting.peers[0].name"},
		{"a peer name with a slash", `{"hosting":{"peers":[` + peer("a/b", fpA) + `]}}`, "hosting.peers[0].name"},
		{"a peer name of 33 characters", `{"hosting":{"peers":[` + peer("a"+strings.Repeat("b", 32), fpA) + `]}}`, "hosting.peers[0].name"},
		{"a peer name of 32 characters", `{"hosting":{"peers":[` + peer("a"+strings.Repeat("b", 31), fpA) + `]}}`, ""},
		{"a fingerprint that is short", `{"hosting":{"peers":[` + peer("anna", fpA[:63]) + `]}}`, "hosting.peers[0].fingerprint"},
		{"a fingerprint in capitals", `{"hosting":{"peers":[` + peer("anna", strings.ToUpper(fpA)) + `]}}`, "hosting.peers[0].fingerprint"},
		{"a fingerprint that is not hex", `{"hosting":{"peers":[` + peer("anna", strings.Repeat("z", 64)) + `]}}`, "hosting.peers[0].fingerprint"},
		{"a duplicate peer name", `{"hosting":{"peers":[` + peer("anna", fpA) + `,` + peer("anna", fpB) + `]}}`, "hosting.peers[1].name"},
		{"a duplicate peer fingerprint", `{"hosting":{"peers":[` + peer("anna", fpA) + `,` + peer("bob", fpA) + `]}}`, "hosting.peers[1].fingerprint"},
		{"a peer with no fingerprint", `{"hosting":{"peers":[{"name":"anna"}]}}`, "hosting.peers[0].fingerprint"},
		{"a peer key in the wrong case", `{"hosting":{"peers":[{"Name":"anna","fingerprint":"` + fpA + `"}]}}`, "hosting.peers[0].name"},
		{"peers that are not a list", `{"hosting":{"peers":{"anna":"` + fpA + `"}}}`, "hosting.peers"},
		{"a peer that is not an object", `{"hosting":{"peers":["anna"]}}`, "hosting.peers[0]"},

		{"a lent device listed twice", `{"hosting":{"lend":[{"device":"R1","to":["a"]},{"device":"R1","to":["b"]}]}}`, "hosting.lend[1].device"},
		{"a lent device with no peer", `{"hosting":{"lend":[{"device":"R1","to":[]}]}}`, "hosting.lend[0].to"},
		{"a lent device with no to", `{"hosting":{"lend":[{"device":"R1"}]}}`, "hosting.lend[0].to"},
		{"a lent device with an empty serial", `{"hosting":{"lend":[{"device":"","to":["a"]}]}}`, "hosting.lend[0].device"},
		{"a lend to a bad name", `{"hosting":{"lend":[{"device":"R1","to":["A B"]}]}}`, "hosting.lend[0].to"},
		{"a lend to a peer twice", `{"hosting":{"lend":[{"device":"R1","to":["a","a"]}]}}`, "hosting.lend[0].to"},

		{"a remote with a host name", `{"remotes":[{"name":"qa","address":"qa.local:7318","fingerprint":"` + fpA + `"}]}`, "remotes[0].address"},
		{"a remote with no port", `{"remotes":[{"name":"qa","address":"10.0.0.5","fingerprint":"` + fpA + `"}]}`, "remotes[0].address"},
		{"a remote on the unspecified address", `{"remotes":[{"name":"qa","address":"0.0.0.0:7318","fingerprint":"` + fpA + `"}]}`, "remotes[0].address"},
		{"a remote on port 0", `{"remotes":[{"name":"qa","address":"10.0.0.5:0","fingerprint":"` + fpA + `"}]}`, "remotes[0].address"},
		{"a remote name in capitals", `{"remotes":[{"name":"QA","address":"10.0.0.5:7318","fingerprint":"` + fpA + `"}]}`, "remotes[0].name"},
		{"a duplicate remote name", `{"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"` + fpA + `"},{"name":"qa","address":"10.0.0.6:7318","fingerprint":"` + fpB + `"}]}`, "remotes[1].name"},
		{"a duplicate remote fingerprint", `{"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"` + fpA + `"},{"name":"qb","address":"10.0.0.6:7318","fingerprint":"` + fpA + `"}]}`, "remotes[1].fingerprint"},
		{"a remote with a short fingerprint", `{"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"abc"}]}`, "remotes[0].fingerprint"},
		{"remotes that are an object", `{"remotes":{"qa":"10.0.0.5:7318"}}`, "remotes"},
		{"the same fingerprint as a peer and as a remote is two lists", `{"hosting":{"peers":[` + peer("anna", fpA) + `]},"remotes":[{"name":"anna","address":"10.0.0.5:7318","fingerprint":"` + fpA + `"}]}`, ""},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, row.body)
			p := policy.New(envAt(dir))
			cfg := p.ConfigStatus()
			h, herr := p.Hosting()
			r, rerr := p.Remotes()
			if row.cause == "" {
				if cfg.State != policy.StateOK || herr != nil || rerr != nil {
					t.Fatalf("state %s (%s), hosting error %v, remotes error %v; want a usable file", cfg.State, cfg.Error, herr, rerr)
				}
				return
			}
			if cfg.State != policy.StateInvalid || !strings.Contains(cfg.Error, row.cause) {
				t.Errorf("state %s, error %q; want invalid and an error naming %s", cfg.State, cfg.Error, row.cause)
			}
			if herr == nil || rerr == nil || len(h.Peers) != 0 || len(h.Lend) != 0 || h.Listen != "" || h.On != "" || h.Port != 0 || len(r) != 0 {
				t.Errorf("an unusable file still gives hosting %+v (%v) and remotes %+v (%v); want nothing pinned, lent or dialled", h, herr, r, rerr)
			}
			if a := p.PhysicalAdmitted("R1"); a.Admitted || !a.Invalid {
				t.Errorf("a physical device is admitted by an unusable file: %+v", a)
			}
		})
	}
}

// TestHostingOnPortAndAddresses: the three keys ADR-0109 adds are read as written, written
// by a patch (a merge for hosting.on and hosting.port, a replacement of the whole list for a
// remote's addresses) and removed by null; the entries the file spelled keep their tags.
func TestHostingOnPortAndAddresses(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"schemaVersion":1,"hosting":{"listen":"127.0.0.1:7318"},"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"`+fpA+`"}]}`)
	p := policy.New(envAt(dir))
	ctx := context.Background()

	res, err := p.Update(ctx, patch(t, `{"hosting":{"on":"private","port":7400},"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"`+fpA+`",
		"addresses":[{"address":"192.168.1.20:7400","kind":"lan"},{"address":"100.64.0.18:7400","kind":"vpn"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []policy.RemoteAddress{{Address: "192.168.1.20:7400", Kind: "lan"}, {Address: "100.64.0.18:7400", Kind: "vpn"}}
	if res.Hosting.On != "private" || res.Hosting.Port != 7400 || res.Hosting.Listen != "127.0.0.1:7318" || len(res.Remotes) != 1 || !reflect.DeepEqual(res.Remotes[0].Addresses, want) {
		t.Errorf("policy.get after the write = %+v / %+v; want on, port and the tagged addresses, listen kept", res.Hosting, res.Remotes)
	}
	// What is returned is a copy: a caller cannot change the next check's addresses.
	got, _ := p.Remotes()
	got[0].Addresses[0].Address = "6.6.6.6:1"
	if again, _ := p.Remotes(); !reflect.DeepEqual(again[0].Addresses, want) {
		t.Errorf("a caller changed the policy's snapshot: %+v", again[0].Addresses)
	}

	res, err = p.Update(ctx, patch(t, `{"hosting":{"on":null,"port":null}}`))
	if err != nil || res.Hosting.On != "" || res.Hosting.Port != 0 || res.Hosting.Listen != "127.0.0.1:7318" {
		t.Errorf("after null, hosting = %+v, %v; want on and port removed, listen kept", res.Hosting, err)
	}
	// The remotes list replaces whole: an entry written without addresses has none.
	res, err = p.Update(ctx, patch(t, `{"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"`+fpA+`"}]}`))
	if err != nil || len(res.Remotes) != 1 || len(res.Remotes[0].Addresses) != 0 {
		t.Errorf("after replacing the list, remotes = %+v, %v; want no addresses", res.Remotes, err)
	}

	// A value the file would refuse is refused by the patch, and the file stays as it was.
	for _, body := range []string{
		`{"hosting":{"on":"8.8.8.8"}}`,
		`{"hosting":{"port":0}}`,
		`{"hosting":{"port":65536}}`,
		`{"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"` + fpA + `","addresses":[{"address":"10.0.0.6:7318","kind":"wifi"}]}]}`,
	} {
		_, err := p.Update(ctx, patch(t, body))
		if e := errCode(err); e == nil || (e.Code != capability.ErrInvalidParams) {
			t.Errorf("%s = %v; want REQUEST_INVALID_PARAMS", body, err)
		}
	}
	// Refused when the patch is decoded: a key an entry does not know.
	for _, body := range []string{
		`{"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"` + fpA + `","addresses":[{"address":"10.0.0.6:7318","kind":"lan","extra":1}]}]}`,
		`{"hosting":{"On":"private"}}`,
	} {
		var up policy.UpdateParams
		if err := json.Unmarshal([]byte(body), &up); err == nil {
			t.Errorf("%s decoded; want it refused at the decode", body)
		}
	}
}

// TestHostingConfigIsReadLive: hosting.peers and hosting.lend are read at each check
// (ADR-0098 §6), so a revocation made by editing the file is seen by the next call; and
// what the policy returns is a copy, not the snapshot the next check reads.
func TestHostingConfigIsReadLive(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"hosting":{"peers":[{"name":"anna","fingerprint":"`+fpA+`"}],"lend":[{"device":"R1","to":["anna"]}]}}`)
	p := policy.New(envAt(dir))
	h, err := p.Hosting()
	if err != nil || len(h.Peers) != 1 || h.Peers[0].Name != "anna" || !reflect.DeepEqual(h.Lend, []policy.Lend{{Device: "R1", To: []string{"anna"}}}) {
		t.Fatalf("hosting = %+v, %v", h, err)
	}
	h.Peers[0].Name, h.Lend[0].To[0] = "mallory", "mallory"
	if again, _ := p.Hosting(); again.Peers[0].Name != "anna" || again.Lend[0].To[0] != "anna" {
		t.Errorf("a caller changed the policy's snapshot: %+v", again)
	}

	write(t, dir, `{"hosting":{"peers":[],"lend":[]},"padding":"so the size changes"}`)
	if now, err := p.Hosting(); err != nil || len(now.Peers) != 0 || len(now.Lend) != 0 {
		t.Errorf("after the edit hosting = %+v, %v; want the peer revoked", now, err)
	}
}

// TestUpdatePatchesHostingAndRemotes: policy.update writes the two sections as it writes
// the others (ADR-0098 §6): a section merges key by key, a list is replaced whole, null
// removes; what would make the file unusable is REQUEST_INVALID_PARAMS and leaves it
// untouched, a key spelled in the wrong case included; the sections a patch does not name
// keep their bytes.
func TestUpdatePatchesHostingAndRemotes(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/a"]},"future":{"x":1}}`)
	p := policy.New(envAt(dir))
	ctx := context.Background()

	res, err := p.Update(ctx, patch(t, `{"hosting":{"listen":"127.0.0.1:7318","peers":[{"name":"anna","fingerprint":"`+fpA+`"}]},
		"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"`+fpB+`"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Hosting.Listen != "127.0.0.1:7318" || len(res.Hosting.Peers) != 1 || res.Hosting.Lend == nil || len(res.Remotes) != 1 || res.Remotes[0].Name != "qa" {
		t.Errorf("policy.get after the write = %+v / %+v", res.Hosting, res.Remotes)
	}
	doc := read(t, path)
	if doc["future"] == nil || len(roots(doc)) != 1 {
		t.Errorf("the write dropped what the patch did not name: %v", doc)
	}

	// A section merges key by key; a list is replaced whole.
	res, err = p.Update(ctx, patch(t, `{"hosting":{"lend":[{"device":"R1","to":["anna"]}],"peers":[{"name":"bob","fingerprint":"`+fpB+`"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Hosting.Listen != "127.0.0.1:7318" || len(res.Hosting.Peers) != 1 || res.Hosting.Peers[0].Name != "bob" || len(res.Hosting.Lend) != 1 {
		t.Errorf("after a merge, hosting = %+v; want the listen kept, the peers replaced, one lend", res.Hosting)
	}

	// null removes a key, a section and the list.
	res, err = p.Update(ctx, patch(t, `{"hosting":{"listen":null},"remotes":null}`))
	if err != nil || res.Hosting.Listen != "" || len(res.Remotes) != 0 || len(res.Hosting.Peers) != 1 {
		t.Errorf("after null, hosting = %+v, remotes = %+v, %v", res.Hosting, res.Remotes, err)
	}
	if _, err := p.Update(ctx, patch(t, `{"hosting":null}`)); err != nil {
		t.Fatal(err)
	}
	if h, _ := p.Hosting(); len(h.Peers) != 0 || len(h.Lend) != 0 {
		t.Errorf("a null hosting section left %+v", h)
	}

	// Each of these makes the file unusable, so it is refused and nothing is written.
	before, mtime := raw(t, path)
	for _, body := range []string{
		`{"hosting":{"listen":"0.0.0.0:7318"}}`,
		`{"hosting":{"peers":[{"name":"anna","fingerprint":"` + fpA + `"},{"name":"anna","fingerprint":"` + fpB + `"}]}}`,
		`{"hosting":{"peers":[{"name":"anna","fingerprint":"` + fpA + `"},{"name":"bob","fingerprint":"` + fpA + `"}]}}`,
		`{"hosting":{"peers":[{"Name":"anna","fingerprint":"` + fpA + `"}]}}`,
		`{"remotes":[{"name":"qa","address":"qa.local:7318","fingerprint":"` + fpA + `"}]}`,
	} {
		_, err := p.Update(ctx, patch(t, body))
		if e := errCode(err); e == nil || e.Code != capability.ErrInvalidParams {
			t.Errorf("%s = %v; want REQUEST_INVALID_PARAMS", body, err)
		}
	}
	after, afterMtime := raw(t, path)
	if string(after) != string(before) || afterMtime != mtime {
		t.Errorf("a refused patch touched the file: %s", after)
	}

	// And these are refused when the patch is decoded, before a call exists.
	for _, body := range []string{
		`{"hosting":{"peers":[{"name":"anna","fingerprint":"` + fpA + `","extra":1}]}}`,
		`{"hosting":{"listening":"127.0.0.1:1"}}`,
		`{"Hosting":{}}`,
		`{"hosting":{"Peers":[]}}`,
		`{"remotes":{"qa":1}}`,
		`{"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"` + fpA + `","x":1}]}`,
	} {
		var u policy.UpdateParams
		if err := json.Unmarshal([]byte(body), &u); err == nil {
			t.Errorf("%s decoded; want a strict refusal", body)
		}
	}
}
