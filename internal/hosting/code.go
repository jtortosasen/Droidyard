package hosting

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// CodePrefix starts every code; it names the format so a pasted line that is not one is
// refused at once (ADR-0107 §1).
const CodePrefix = "dy1."

// MaxCodeAddrs is how many addresses a code carries at most.
const MaxCodeAddrs = 8

// codeVersion is the version byte inside a code.
const codeVersion = 1

// Kind says how an address was found, which is how a client orders them (ADR-0109 §4): a
// vpn address belongs to a tunnel interface, a lan one to any other interface.
type Kind string

const (
	KindVPN Kind = "vpn"
	KindLAN Kind = "lan"
)

// Addr is one address a host can be reached at.
type Addr struct {
	IP   netip.Addr
	Kind Kind
}

// Code is what `droidyard join` is given (ADR-0107 §1): where to reach the host, which
// certificate it presents and the single-use secret. The secret is a bearer credential until
// it is used: a Code is never logged or put in an error.
type Code struct {
	Addrs       []Addr
	Port        uint16
	Fingerprint [32]byte
	Secret      [16]byte
}

// Wire bytes of an address's kind and family.
const (
	kindByteVPN = 1
	kindByteLAN = 2
)

// checkAddr refuses the addresses a code may not carry: it is dialled by whoever pastes it,
// so an address that is not on a private network (or loopback, for tests and a daemon on
// the same machine) is refused rather than dialled.
func checkAddr(a Addr) error {
	ip := a.IP
	switch {
	case !ip.IsValid():
		return errors.New("an address is not valid")
	case ip.Zone() != "":
		return errors.New("an address has a zone")
	case ip.Is4In6():
		return fmt.Errorf("address %s is an IPv4 address written as IPv6", ip)
	case ip.IsUnspecified():
		return fmt.Errorf("address %s is unspecified", ip)
	case ip.IsMulticast():
		return fmt.Errorf("address %s is a multicast address", ip)
	}
	switch a.Kind {
	case KindVPN, KindLAN:
	default:
		return fmt.Errorf("address %s has the kind %q; want vpn or lan", ip, a.Kind)
	}
	if ip.IsLoopback() || ip.IsPrivate() || (ip.Is4() && cgnat.Contains(ip)) {
		return nil
	}
	return fmt.Errorf("address %s is not on a private network", ip)
}

// cgnat is 100.64.0.0/10, the range a VPN overlay such as Tailscale uses.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func (c Code) validate() error {
	if len(c.Addrs) == 0 {
		return errors.New("the code has no address")
	}
	if len(c.Addrs) > MaxCodeAddrs {
		return fmt.Errorf("the code has %d addresses; at most %d", len(c.Addrs), MaxCodeAddrs)
	}
	for _, a := range c.Addrs {
		if err := checkAddr(a); err != nil {
			return err
		}
	}
	if c.Port == 0 {
		return errors.New("the code's port is 0")
	}
	return nil
}

// Encode is the code as the line a human pastes: dy1. and unpadded base64url of the version,
// the address count, each address (kind, family, bytes), the port, the fingerprint and the
// secret. A code that ParseCode would refuse is not encoded.
func (c Code) Encode() (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	b := []byte{codeVersion, byte(len(c.Addrs))}
	for _, a := range c.Addrs {
		k := byte(kindByteLAN)
		if a.Kind == KindVPN {
			k = kindByteVPN
		}
		if a.IP.Is4() {
			ip := a.IP.As4()
			b = append(b, k, 4)
			b = append(b, ip[:]...)
		} else {
			ip := a.IP.As16()
			b = append(b, k, 16)
			b = append(b, ip[:]...)
		}
	}
	b = append(b, byte(c.Port>>8), byte(c.Port))
	b = append(b, c.Fingerprint[:]...)
	b = append(b, c.Secret[:]...)
	return CodePrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// ParseCode reads a pasted code. It refuses another prefix or version, text that is not
// canonical base64url, a wrong length, no address or more than eight, an address that is
// unspecified, multicast or not on a private network (loopback is allowed), and port 0. The
// error never quotes the text: it may be a code.
func ParseCode(s string) (Code, error) {
	s = strings.TrimSpace(s)
	rest, ok := strings.CutPrefix(s, CodePrefix)
	if !ok {
		return Code{}, errors.New("this is not a Droidyard join code: it does not start with " + CodePrefix)
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(rest)
	if err != nil {
		return Code{}, errors.New("the join code is not valid base64url")
	}
	errShort := errors.New("the join code is truncated or has trailing bytes")
	if len(b) < 2 {
		return Code{}, errShort
	}
	if b[0] != codeVersion {
		return Code{}, fmt.Errorf("the join code is version %d; this Droidyard reads version %d", b[0], codeVersion)
	}
	n := int(b[1])
	if n == 0 || n > MaxCodeAddrs {
		return Code{}, fmt.Errorf("the join code has %d addresses; want 1 to %d", n, MaxCodeAddrs)
	}
	b = b[2:]
	var c Code
	for range n {
		if len(b) < 2 {
			return Code{}, errShort
		}
		var a Addr
		switch b[0] {
		case kindByteVPN:
			a.Kind = KindVPN
		case kindByteLAN:
			a.Kind = KindLAN
		default:
			return Code{}, fmt.Errorf("the join code has an address of unknown kind %d", b[0])
		}
		size := int(b[1])
		if size != 4 && size != 16 {
			return Code{}, fmt.Errorf("the join code has an address of family %d", size)
		}
		b = b[2:]
		if len(b) < size {
			return Code{}, errShort
		}
		ip, _ := netip.AddrFromSlice(b[:size])
		a.IP = ip
		b = b[size:]
		if err := checkAddr(a); err != nil {
			return Code{}, err
		}
		c.Addrs = append(c.Addrs, a)
	}
	if len(b) != 2+32+16 {
		return Code{}, errShort
	}
	c.Port = uint16(b[0])<<8 | uint16(b[1])
	copy(c.Fingerprint[:], b[2:34])
	copy(c.Secret[:], b[34:])
	if c.Port == 0 {
		return Code{}, errors.New("the join code's port is 0")
	}
	return c, nil
}
