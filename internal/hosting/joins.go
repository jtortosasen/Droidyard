package hosting

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"sync"
)

// MaxCodesPerDevice is how many unused codes a share keeps; a ninth voids the oldest
// (ADR-0107 §1).
const MaxCodesPerDevice = 8

// Pending is the unused join codes of this daemon's shares (ADR-0107 §2). It keeps the
// SHA-256 of each secret and nothing else of it: the secret leaves Issue once, inside the
// code the human pastes, and cannot be read back from here.
type Pending struct {
	mu     sync.Mutex
	hashes map[string][][sha256.Size]byte // by device, oldest first
}

// NewPending returns a Pending with no code.
func NewPending() *Pending {
	return &Pending{hashes: map[string][][sha256.Size]byte{}}
}

// Issue makes a new secret for the device and keeps its hash. The device already holding
// MaxCodesPerDevice unused codes loses the oldest.
func (p *Pending) Issue(device string) (secret [16]byte) {
	if _, err := rand.Read(secret[:]); err != nil {
		panic("hosting: no randomness: " + err.Error()) // crypto/rand does not fail on a working OS
	}
	h := sha256.Sum256(secret[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	hs := append(p.hashes[device], h)
	if len(hs) > MaxCodesPerDevice {
		hs = append([][sha256.Size]byte(nil), hs[len(hs)-MaxCodesPerDevice:]...)
	}
	p.hashes[device] = hs
	return secret
}

// Consume finds the secret among the unused codes, comparing in constant time, and removes
// it under the same lock: a secret is accepted once, whatever the number of concurrent
// presenters. device is the one the code was issued for.
func (p *Pending) Consume(secret [16]byte) (device string, ok bool) {
	h := sha256.Sum256(secret[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	var found string
	at, hit := -1, 0
	// Every hash is compared, so the time does not say which one matched.
	for dev, hs := range p.hashes {
		for i := range hs {
			if m := subtle.ConstantTimeCompare(h[:], hs[i][:]); m == 1 {
				found, at, hit = dev, i, 1
			}
		}
	}
	if hit == 0 {
		return "", false
	}
	hs := p.hashes[found]
	hs = append(hs[:at:at], hs[at+1:]...)
	if len(hs) == 0 {
		delete(p.hashes, found)
	} else {
		p.hashes[found] = hs
	}
	return found, true
}

// Void drops every unused code of the device: its share stopped.
func (p *Pending) Void(device string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.hashes, device)
}

// VoidAll drops every unused code of every device: all the shares stopped.
func (p *Pending) VoidAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.hashes)
}

// Any reports whether any code is unused.
func (p *Pending) Any() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.hashes) > 0
}
