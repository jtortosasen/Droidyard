package hosting

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"slices"
	"sync"
	"time"
)

// MaxCodesPerDevice is how many unused codes a share keeps; a ninth voids the oldest
// (ADR-0107 §1).
const MaxCodesPerDevice = 8

// pendingCode is one unused code: the SHA-256 of its secret, when it was issued, and whether
// it is recorded on disk (a detached share's, ADR-0108 §2) or lives in memory only (a
// foreground share's).
type pendingCode struct {
	hash    [sha256.Size]byte
	issued  time.Time
	durable bool
}

// Pending is the unused join codes of this daemon's shares (ADR-0107 §2). It keeps the
// SHA-256 of each secret and nothing else of it: the secret leaves Issue once, inside the
// code the human pastes, and cannot be read back from here.
//
// The codes of a detached share are also kept in <state>/hosting/shares.json, through the
// save function the host attaches (ADR-0108 §2). A code consumed is written before Consume
// returns, so a crash between the consumption and the join's answer cannot make it usable
// again. A write that fails leaves the file stale and the memory right: the codes stay void
// here, and Flush (which the host's Reconcile calls) writes the file when it can.
type Pending struct {
	mu    sync.Mutex
	codes map[string][]pendingCode // by device, oldest first
	// durable says whether a code issued now for the device belongs in the file; nil: none does.
	durable func(device string) bool
	// save writes the file with these records; nil: nothing is written.
	save func([]shareRecord) error
	// dirty: the durable codes in memory are not what the file holds.
	dirty bool
}

// NewPending returns a Pending with no code.
func NewPending() *Pending {
	return &Pending{codes: map[string][]pendingCode{}}
}

// attach gives the Pending the file it keeps the durable codes in.
func (p *Pending) attach(durable func(device string) bool, save func([]shareRecord) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.durable, p.save = durable, save
}

// restore puts the codes an earlier run recorded back, as durable ones. stale says the file
// holds more than these (records were left out), so it is rewritten at the next flush.
func (p *Pending) restore(records []shareRecord, stale bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range records {
		for _, c := range r.Codes {
			p.codes[r.Device] = append(p.codes[r.Device], pendingCode{hash: c.hash, issued: c.Issued, durable: true})
		}
	}
	if stale {
		p.dirty = true
	}
}

// Issue makes a new secret for the device and keeps its hash, in memory. The device already
// holding MaxCodesPerDevice unused codes loses the oldest. A detached share's code is not on
// disk until IssueSaved or Flush writes it.
func (p *Pending) Issue(device string) (secret [16]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.issueLocked(device)
}

func (p *Pending) issueLocked(device string) (secret [16]byte) {
	if _, err := rand.Read(secret[:]); err != nil {
		panic("hosting: no randomness: " + err.Error()) // crypto/rand does not fail on a working OS
	}
	c := pendingCode{hash: sha256.Sum256(secret[:]), issued: time.Now().UTC().Truncate(time.Second)}
	c.durable = p.durable != nil && p.durable(device)
	cs := append(p.codes[device], c)
	if len(cs) > MaxCodesPerDevice {
		for _, old := range cs[:len(cs)-MaxCodesPerDevice] {
			p.dirty = p.dirty || old.durable
		}
		cs = append([]pendingCode(nil), cs[len(cs)-MaxCodesPerDevice:]...)
	}
	p.codes[device] = cs
	p.dirty = p.dirty || c.durable
	return secret
}

// IssueSaved is Issue followed by the write of the file. When the write fails the code is not
// issued: the device holds the codes it held, and the error is the write's.
func (p *Pending) IssueSaved(device string) (secret [16]byte, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, had := p.codes[device]
	prev = slices.Clone(prev)
	wasDirty := p.dirty
	secret = p.issueLocked(device)
	if err := p.flushLocked(); err != nil {
		if had {
			p.codes[device] = prev
		} else {
			delete(p.codes, device)
		}
		p.dirty = wasDirty
		return [16]byte{}, err
	}
	return secret, nil
}

// Consume finds the secret among the unused codes, comparing in constant time, and removes
// it under the same lock: a secret is accepted once, whatever the number of concurrent
// presenters. device is the one the code was issued for. The file of a detached share's code
// is written before Consume returns. When that write fails, err is its error and ok is false:
// the code is void in memory all the same, and nobody is admitted with it.
func (p *Pending) Consume(secret [16]byte) (device string, ok bool, err error) {
	h := sha256.Sum256(secret[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	var found string
	at, hit := -1, 0
	// Every hash is compared, so the time does not say which one matched.
	for dev, cs := range p.codes {
		for i := range cs {
			if m := subtle.ConstantTimeCompare(h[:], cs[i].hash[:]); m == 1 {
				found, at, hit = dev, i, 1
			}
		}
	}
	if hit == 0 {
		return "", false, nil
	}
	cs := p.codes[found]
	used := cs[at]
	cs = append(cs[:at:at], cs[at+1:]...)
	if len(cs) == 0 {
		delete(p.codes, found)
	} else {
		p.codes[found] = cs
	}
	if used.durable {
		p.dirty = true
		if err := p.flushLocked(); err != nil {
			return "", false, err
		}
	}
	return found, true, nil
}

// Void drops every unused code of the device: its share stopped. The error is the file that
// could not be written; the codes are void in memory whatever it says.
func (p *Pending) Void(device string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.codes[device] {
		p.dirty = p.dirty || c.durable
	}
	delete(p.codes, device)
	return p.flushLocked()
}

// VoidAll drops every unused code of every device: all the shares stopped. Its error is
// Void's.
func (p *Pending) VoidAll() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, cs := range p.codes {
		for _, c := range cs {
			p.dirty = p.dirty || c.durable
		}
	}
	clear(p.codes)
	return p.flushLocked()
}

// Flush writes the file if it is stale: the retry of a write that failed.
func (p *Pending) Flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flushLocked()
}

// flushLocked writes every durable code when the file is stale.
func (p *Pending) flushLocked() error {
	if !p.dirty || p.save == nil {
		return nil
	}
	devices := make([]string, 0, len(p.codes))
	for dev := range p.codes {
		devices = append(devices, dev)
	}
	slices.Sort(devices)
	records := []shareRecord{}
	for _, dev := range devices {
		r := shareRecord{Device: dev, Codes: []codeRecord{}}
		for _, c := range p.codes[dev] {
			if c.durable {
				r.Codes = append(r.Codes, codeRecord{hash: c.hash, Issued: c.issued})
			}
		}
		if len(r.Codes) > 0 {
			records = append(records, r)
		}
	}
	if err := p.save(records); err != nil {
		return err
	}
	p.dirty = false
	return nil
}

// Any reports whether any code is unused.
func (p *Pending) Any() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.codes) > 0
}
