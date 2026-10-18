package hosting

import (
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
)

// ErrPinned is what a handshake whose peer certificate is not the pinned one fails with.
var ErrPinned = errors.New("the peer's certificate is not the pinned one")

// PinnedConfig is the TLS configuration of a peer link for one pinned fingerprint: TLS 1.3
// only, HTTP/1.1 (a WebSocket needs it), this daemon's certificate presented, and the
// other end's accepted by its fingerprint alone. Chain verification is off because the
// pin replaces it: no certificate authority is involved (ADR-0098 §2). The framing both
// ends then speak is capability's peerwire.go.
func PinnedConfig(id *Identity, pinned func(fingerprint string) bool) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{id.Certificate()},
		NextProtos:         []string{"http/1.1"},
		InsecureSkipVerify: true, // the pin below is the verification
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("%w: it presented none", ErrPinned)
			}
			if !pinned(FingerprintOf(cs.PeerCertificates[0])) {
				return ErrPinned
			}
			return nil
		},
	}
}

// SamePin compares two fingerprints without leaking where they differ.
func SamePin(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
