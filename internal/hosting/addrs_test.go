package hosting

import (
	"fmt"
	"net/netip"
	"reflect"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func addrOf(ip string, kind Kind) Addr { return Addr{IP: netip.MustParseAddr(ip), Kind: kind} }

// thisMac is the machine of the m2u spike: Tailscale on utun8, a company VPN on utun9, wifi
// on en0, and the addresses that must never be bound.
func thisMac() []Iface {
	return []Iface{
		{Name: "lo0", Up: true, Addrs: []netip.Prefix{pfx("127.0.0.1/8")}},
		{Name: "utun8", Up: true, PointToPoint: true, Addrs: []netip.Prefix{pfx("100.64.0.18/32"), pfx("fd7a:115c:a1e0::1/128")}},
		{Name: "utun9", Up: true, Addrs: []netip.Prefix{pfx("10.221.240.50/24")}},
		{Name: "en0", Up: true, Addrs: []netip.Prefix{pfx("192.168.1.20/24"), pfx("10.0.0.5/8"), pfx("172.16.3.4/16"), pfx("fe80::1/64")}},
		{Name: "en1", Up: false, Addrs: []netip.Prefix{pfx("192.168.9.9/24")}},
	}
}

// TestPrivateAddresses is the rule of ADR-0109 §1 for which addresses of this machine the
// listener binds and how each is tagged: a tunnel interface (point-to-point, or utun*) makes
// an address vpn, whether it is in 100.64.0.0/10 (Tailscale) or RFC 1918 (a company VPN's
// 10.x); any other interface makes an RFC 1918 address lan; 100.64.0.0/10 off a tunnel is
// carrier-grade NAT and never bound; nothing public, link-local, multicast, unspecified,
// IPv6 or on a down interface; loopback only when on names it. A rule that tagged by range
// alone would call the company VPN's 10.221.240.50 a lan address.
func TestPrivateAddresses(t *testing.T) {
	vpn1, vpn2 := addrOf("100.64.0.18", KindVPN), addrOf("10.221.240.50", KindVPN)
	lan1, lan2, lan3 := addrOf("192.168.1.20", KindLAN), addrOf("10.0.0.5", KindLAN), addrOf("172.16.3.4", KindLAN)

	// The bad ones, spread over a tunnel and a plain interface, are all dropped.
	bad := append(thisMac(),
		Iface{Name: "en2", Up: true, Addrs: []netip.Prefix{pfx("100.64.0.99/10"), pfx("172.32.0.1/16"), pfx("8.8.8.8/24"), pfx("169.254.1.1/16"), pfx("0.0.0.0/0"), pfx("224.0.0.1/4")}},
		Iface{Name: "utun3", Up: true, Addrs: []netip.Prefix{pfx("169.254.7.7/16"), pfx("8.8.4.4/32"), pfx("224.0.0.2/4")}})

	nine := Iface{Name: "en5", Up: true}
	for i := range 9 {
		nine.Addrs = append(nine.Addrs, pfx(fmt.Sprintf("192.168.5.%d/24", i+1)))
	}

	rows := []struct {
		name   string
		ifaces []Iface
		on     string
		want   []Addr
	}{
		{"private: every vpn and lan address, tagged", thisMac(), "private", []Addr{vpn1, vpn2, lan1, lan2, lan3}},
		{"vpn: the tunnels' addresses only", thisMac(), "vpn", []Addr{vpn1, vpn2}},
		{"lan: the others only", thisMac(), "lan", []Addr{lan1, lan2, lan3}},
		{"public, link-local, multicast, unspecified and CGNAT off a tunnel are never bound", bad, "private", []Addr{vpn1, vpn2, lan1, lan2, lan3}},
		{"100.64.0.18 on en0 is carrier-grade NAT", []Iface{{Name: "en0", Up: true, Addrs: []netip.Prefix{pfx("100.64.0.18/10")}}}, "private", nil},
		{"one literal binds that address alone", thisMac(), "10.221.240.50", []Addr{vpn2}},
		{"one lan literal", thisMac(), "192.168.1.20", []Addr{lan1}},
		{"loopback is bound only when on names it", thisMac(), "127.0.0.1", []Addr{addrOf("127.0.0.1", KindLAN)}},
		{"a literal that is no interface's binds nothing", thisMac(), "10.9.9.9", nil},
		{"a public literal binds nothing", bad, "8.8.8.8", nil},
		{"an address on a down interface is not bound", thisMac(), "192.168.9.9", nil},
		{"at most 8, the ninth dropped", []Iface{nine}, "private", func() []Addr {
			var out []Addr
			for i := range 8 {
				out = append(out, addrOf(fmt.Sprintf("192.168.5.%d", i+1), KindLAN))
			}
			return out
		}()},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got := Private(row.ifaces, row.on); !reflect.DeepEqual(got, row.want) {
				t.Errorf("Private(on %q) = %v; want %v", row.on, got, row.want)
			}
		})
	}
}

// TestOrderPrefersOwnSubnet is ADR-0109 §4: a client dials a lan address in one of its own
// subnets first (the screen's latency), then the vpn ones, then the rest, and each group
// keeps the host's order. The client on 192.168.1.0/24 puts the host's 192.168.1.20 (lan)
// before its 10.221.240.50 and 100.64.0.18 (vpn), and those before 10.0.0.5 (lan, not on any
// subnet of the client's, so it ranks with the rest). A client on no shared subnet keeps the
// vpn ones first.
func TestOrderPrefersOwnSubnet(t *testing.T) {
	host := []Addr{addrOf("10.221.240.50", KindVPN), addrOf("10.0.0.5", KindLAN), addrOf("192.168.1.20", KindLAN), addrOf("100.64.0.18", KindVPN)}
	in := append([]Addr(nil), host...)

	got := Order(host, []netip.Prefix{pfx("192.168.1.0/24")})
	want := []Addr{addrOf("192.168.1.20", KindLAN), addrOf("10.221.240.50", KindVPN), addrOf("100.64.0.18", KindVPN), addrOf("10.0.0.5", KindLAN)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a client on 192.168.1.0/24: %v; want %v", got, want)
	}
	if !reflect.DeepEqual(host, in) {
		t.Errorf("Order changed its input: %v", host)
	}

	got = Order(host, []netip.Prefix{pfx("172.20.0.0/16")})
	want = []Addr{addrOf("10.221.240.50", KindVPN), addrOf("100.64.0.18", KindVPN), addrOf("10.0.0.5", KindLAN), addrOf("192.168.1.20", KindLAN)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a client on no shared subnet: %v; want the vpn ones first, then the rest in the host's order: %v", got, want)
	}

	// Only the tag decides, not the range: a company VPN's 10.x address, tagged vpn, is not
	// taken for a lan one even when the client's own subnet is 10.0.0.0/8.
	got = Order([]Addr{addrOf("10.0.0.5", KindLAN), addrOf("10.221.240.50", KindVPN)}, []netip.Prefix{pfx("10.0.0.0/8")})
	if got[0].IP != netip.MustParseAddr("10.0.0.5") {
		t.Errorf("a lan address in the client's subnet is %v; want it first", got)
	}
	got = Order([]Addr{addrOf("10.221.240.50", KindVPN), addrOf("10.0.0.5", KindLAN)}, []netip.Prefix{pfx("192.168.1.0/24")})
	if got[0].IP != netip.MustParseAddr("10.221.240.50") {
		t.Errorf("a vpn address tagged vpn is %v; want it before a lan one outside the client's subnets", got)
	}
}

// TestOwnPrefixes: the subnets of a machine are those of its up interfaces, IPv4 only,
// masked; a down interface has none.
func TestOwnPrefixes(t *testing.T) {
	got := OwnPrefixes(thisMac())
	want := []netip.Prefix{pfx("127.0.0.0/8"), pfx("100.64.0.18/32"), pfx("10.221.240.0/24"), pfx("192.168.1.0/24"), pfx("10.0.0.0/8"), pfx("172.16.0.0/16")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OwnPrefixes = %v; want %v", got, want)
	}
}
