package hosting

import (
	"net"
	"net/netip"
	"sort"
	"strings"
)

// The private addresses of this machine (ADR-0109 §1) and the order a client dials a host's
// in (§4). The interfaces are read through a lister the daemon injects, so a test decides
// what the machine looks like.

// MaxPrivateAddrs is how many addresses Private returns at most.
const MaxPrivateAddrs = MaxCodeAddrs

// Iface is one network interface as the address rules read it.
type Iface struct {
	Name string
	// Up is whether the interface is up: a down one has no usable address.
	Up bool
	// PointToPoint marks a tunnel: a VPN's interface has no broadcast domain of its own.
	PointToPoint bool
	// Addrs are the interface's addresses with their prefix lengths.
	Addrs []netip.Prefix
}

// tunnel reports whether the interface carries a VPN: point-to-point, or a macOS utun.
func (i Iface) tunnel() bool {
	return i.PointToPoint || strings.HasPrefix(i.Name, "utun")
}

// Lister reads this machine's interfaces.
type Lister func() ([]Iface, error)

// SystemInterfaces is the Lister of the running machine.
func SystemInterfaces() ([]Iface, error) {
	nis, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(nis))
	for _, ni := range nis {
		i := Iface{Name: ni.Name, Up: ni.Flags&net.FlagUp != 0, PointToPoint: ni.Flags&net.FlagPointToPoint != 0}
		addrs, err := ni.Addrs()
		if err != nil {
			continue // an interface that went away between the two calls
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(n.IP)
			if !ok {
				continue
			}
			ones, _ := n.Mask.Size()
			i.Addrs = append(i.Addrs, netip.PrefixFrom(ip.Unmap(), ones))
		}
		out = append(out, i)
	}
	return out, nil
}

// Private returns the IPv4 addresses of the up interfaces this host may be reached at,
// each tagged (ADR-0109 §1), at most MaxPrivateAddrs. vpn is an address in 100.64.0.0/10
// or RFC 1918 on a tunnel interface; lan an RFC 1918 address on any other. 100.64.0.0/10
// off a tunnel is carrier-grade NAT, shared with strangers, and left out. Never an
// unspecified, link-local, multicast or public address, nor a loopback one unless on names
// it. on is private (everything above), vpn, lan, or one IP literal (ValidOn).
func Private(ifaces []Iface, on string) []Addr {
	literal, perr := netip.ParseAddr(on)
	byIP := perr == nil
	var out []Addr
	seen := map[netip.Addr]bool{}
	for _, i := range ifaces {
		if !i.Up {
			continue
		}
		for _, p := range i.Addrs {
			ip := p.Addr()
			if !ip.Is4() || seen[ip] {
				continue
			}
			kind, ok := classify(ip, i.tunnel(), byIP && ip == literal)
			if !ok {
				continue
			}
			if byIP && ip != literal || on == "vpn" && kind != KindVPN || on == "lan" && kind != KindLAN {
				continue
			}
			seen[ip] = true
			out = append(out, Addr{IP: ip, Kind: kind})
			if len(out) == MaxPrivateAddrs {
				return out
			}
		}
	}
	return out
}

var (
	linkLocal = netip.MustParsePrefix("169.254.0.0/16")
	cgnat4    = netip.MustParsePrefix("100.64.0.0/10")
)

// classify tags one IPv4 address of an interface, or says it is never bound. named is
// whether on names this very address, which is the only way loopback is bound.
func classify(ip netip.Addr, tunnel, named bool) (Kind, bool) {
	switch {
	case ip.IsUnspecified(), ip.IsMulticast(), linkLocal.Contains(ip):
		return "", false
	case ip.IsLoopback():
		return KindLAN, named
	case ip.IsPrivate():
		if tunnel {
			return KindVPN, true
		}
		return KindLAN, true
	case cgnat4.Contains(ip):
		return KindVPN, tunnel
	}
	return "", false
}

// OwnPrefixes are the IPv4 subnets this machine is on: what a client compares a host's lan
// addresses with. Loopback is one of them: a host on this very machine is the nearest.
func OwnPrefixes(ifaces []Iface) []netip.Prefix {
	var out []netip.Prefix
	for _, i := range ifaces {
		if !i.Up {
			continue
		}
		for _, p := range i.Addrs {
			if p.Addr().Is4() {
				out = append(out, p.Masked())
			}
		}
	}
	return out
}

// Order is the order a client tries a host's addresses in (ADR-0109 §4): a lan address
// inside one of own's prefixes first, then the vpn ones, then the rest, each group in the
// order given. The input is not changed.
func Order(addrs []Addr, own []netip.Prefix) []Addr {
	ips := make([]netip.Addr, len(addrs))
	kinds := make([]Kind, len(addrs))
	for i, a := range addrs {
		ips[i], kinds[i] = a.IP, a.Kind
	}
	out := make([]Addr, 0, len(addrs))
	for _, i := range orderIndex(ips, kinds, own) {
		out = append(out, addrs[i])
	}
	return out
}

// orderIndex is Order's ranking over parallel slices; a kind of "" is untagged and ranks
// with the rest.
func orderIndex(ips []netip.Addr, kinds []Kind, own []netip.Prefix) []int {
	rank := func(i int) int {
		switch {
		case kinds[i] == KindLAN && inAny(own, ips[i]):
			return 0
		case kinds[i] == KindVPN:
			return 1
		}
		return 2
	}
	idx := make([]int, len(ips))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return rank(idx[a]) < rank(idx[b]) })
	return idx
}

func inAny(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
