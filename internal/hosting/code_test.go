package hosting

import (
	"encoding/base64"
	"net/netip"
	"strings"
	"testing"
)

func sampleCode() Code {
	c := Code{
		Addrs: []Addr{
			{IP: netip.MustParseAddr("100.64.0.18"), Kind: KindVPN},
			{IP: netip.MustParseAddr("192.168.0.118"), Kind: KindLAN},
		},
		Port: 47123,
	}
	for i := range c.Fingerprint {
		c.Fingerprint[i] = byte(i + 1)
	}
	for i := range c.Secret {
		c.Secret[i] = byte(0xA0 + i)
	}
	return c
}

// TestCodeRoundTrip is ADR-0107 §1: the code is dy1. and unpadded base64url of one byte
// layout, every field survives it (the kind tag of each address included, since the client
// orders by it), and two IPv4 addresses make the line about 90 characters.
func TestCodeRoundTrip(t *testing.T) {
	want := sampleCode()
	line, err := want.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "dy1.") || strings.ContainsAny(line, "=+/ ") {
		t.Errorf("the code is %q; want dy1. and unpadded base64url", line)
	}
	if len(line) != 90 {
		t.Errorf("the code with two IPv4 addresses is %d characters; want 90", len(line))
	}
	got, err := ParseCode(line)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Addrs) != 2 || got.Addrs[0] != want.Addrs[0] || got.Addrs[1] != want.Addrs[1] ||
		got.Port != want.Port || got.Fingerprint != want.Fingerprint || got.Secret != want.Secret {
		t.Errorf("the code came back as %+v; want %+v", got, want)
	}

	// IPv6 and eight addresses round-trip too, and a pasted line may carry a newline.
	v6 := Code{Port: 1, Fingerprint: want.Fingerprint, Secret: want.Secret}
	for range MaxCodeAddrs - 1 {
		v6.Addrs = append(v6.Addrs, Addr{IP: netip.MustParseAddr("10.1.2.3"), Kind: KindLAN})
	}
	v6.Addrs = append(v6.Addrs, Addr{IP: netip.MustParseAddr("fd00::1"), Kind: KindVPN})
	line, err = v6.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err = ParseCode(line + "\n")
	if err != nil || len(got.Addrs) != 8 || got.Addrs[7] != v6.Addrs[7] {
		t.Errorf("eight addresses, one IPv6: %+v, %v", got, err)
	}
}

// TestParseCodeRefuses is ADR-0107 §1's list of what a pasted line may not be: each refusal
// is its own case, and none of the errors repeats the text (it may be a code).
func TestParseCodeRefuses(t *testing.T) {
	good := sampleCode()
	goodLine, _ := good.Encode()
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(goodLine, "dy1."))
	enc := func(b []byte) string { return "dy1." + base64.RawURLEncoding.EncodeToString(b) }
	mut := func(f func(b []byte) []byte) string { return enc(f(append([]byte(nil), raw...))) }

	// codeWith builds a code of one address by hand, past Encode's own checks.
	codeWith := func(kind, family byte, ip []byte, port uint16) string {
		b := []byte{1, 1, kind, family}
		b = append(b, ip...)
		b = append(b, byte(port>>8), byte(port))
		b = append(b, good.Fingerprint[:]...)
		b = append(b, good.Secret[:]...)
		return enc(b)
	}

	cases := []struct{ name, line string }{
		{"another prefix", "dy2." + strings.TrimPrefix(goodLine, "dy1.")},
		{"no prefix", strings.TrimPrefix(goodLine, "dy1.")},
		{"not base64url", "dy1.!!!!"},
		{"padded", goodLine + "="},
		{"another version", mut(func(b []byte) []byte { b[0] = 2; return b })},
		{"no address", mut(func(b []byte) []byte { b[1] = 0; return b })},
		{"nine addresses", mut(func(b []byte) []byte { b[1] = 9; return b })},
		{"truncated", mut(func(b []byte) []byte { return b[:len(b)-1] })},
		{"trailing bytes", mut(func(b []byte) []byte { return append(b, 0) })},
		{"unknown kind", codeWith(3, 4, []byte{10, 0, 0, 1}, 1)},
		{"unknown family", codeWith(1, 5, []byte{10, 0, 0, 1, 0}, 1)},
		{"unspecified", codeWith(2, 4, []byte{0, 0, 0, 0}, 1)},
		{"multicast", codeWith(2, 4, []byte{224, 0, 0, 1}, 1)},
		{"public", codeWith(2, 4, []byte{8, 8, 8, 8}, 1)},
		{"public IPv6", codeWith(2, 16, netip.MustParseAddr("2001:db8::1").AsSlice(), 1)},
		{"link-local", codeWith(2, 4, []byte{169, 254, 1, 1}, 1)},
		{"IPv4 as IPv6", codeWith(2, 16, netip.MustParseAddr("::ffff:10.0.0.1").AsSlice(), 1)},
		{"port 0", codeWith(2, 4, []byte{10, 0, 0, 1}, 0)},
	}
	for _, c := range cases {
		if got, err := ParseCode(c.line); err == nil {
			t.Errorf("%s: parsed as %+v; want a refusal", c.name, got)
		} else if len(c.line) > 12 && strings.Contains(err.Error(), c.line[len(c.line)-8:]) {
			t.Errorf("%s: the error quotes the text: %v", c.name, err)
		}
	}
	// Loopback is allowed: the tests and a share on the same machine use it.
	if _, err := ParseCode(codeWith(2, 4, []byte{127, 0, 0, 1}, 9)); err != nil {
		t.Errorf("a loopback address was refused: %v", err)
	}
	if _, err := ParseCode(codeWith(2, 4, []byte{172, 16, 3, 4}, 9)); err != nil {
		t.Errorf("an RFC 1918 address was refused: %v", err)
	}
	// Encode refuses what Parse would, so a bad code is never produced.
	bad := sampleCode()
	bad.Addrs = nil
	if _, err := bad.Encode(); err == nil {
		t.Error("a code with no address was encoded")
	}
}
