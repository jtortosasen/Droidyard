package hosting

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestPendingKeepsOnlyHashes is ADR-0107 §2: what Pending holds of a code is the SHA-256 of
// its secret, so the secret's bytes are nowhere in its state; a state dump (every field, as
// the language prints it) holds the hash and not the secret in any common rendering.
func TestPendingKeepsOnlyHashes(t *testing.T) {
	p := NewPending()
	secret := p.Issue("R58M1234")
	dump := fmt.Sprintf("%#v %v", p.codes, p.codes)
	for _, form := range []string{
		fmt.Sprintf("%v", secret),
		fmt.Sprintf("%#v", secret),
		fmt.Sprintf("%x", secret),
		fmt.Sprintf("%v", secret[:]),
	} {
		if bytes.Contains([]byte(dump), []byte(form)) {
			t.Errorf("Pending's state holds the secret as %s", form)
		}
	}
	for dev, cs := range p.codes {
		for _, c := range cs {
			if bytes.Contains(c.hash[:], secret[:]) {
				t.Errorf("a stored hash for %s contains the secret", dev)
			}
		}
	}
	if dev, ok, _ := p.Consume(secret); !ok || dev != "R58M1234" {
		t.Errorf("the issued secret consumed as %q, %v; want its device", dev, ok)
	}
}

// TestPendingIsSingleUseAndBounded is ADR-0107 §1, §2: a secret is accepted once however
// many present it together, a ninth unused code of a device voids the oldest, Void drops a
// device's codes, and Any says whether any is left.
func TestPendingIsSingleUseAndBounded(t *testing.T) {
	p := NewPending()
	if p.Any() {
		t.Error("Any is true with nothing issued")
	}
	if _, ok, _ := p.Consume([16]byte{1}); ok {
		t.Error("a secret that was never issued was accepted")
	}

	s := p.Issue("a")
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, _ := p.Consume(s); ok {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Errorf("%d concurrent consumers of one secret were accepted; want 1", won.Load())
	}
	if p.Any() {
		t.Error("Any is true after the only code was used")
	}

	var secrets [][16]byte
	for range MaxCodesPerDevice + 1 {
		secrets = append(secrets, p.Issue("a"))
	}
	other := p.Issue("b")
	if _, ok, _ := p.Consume(secrets[0]); ok {
		t.Error("the oldest of nine codes still works; the ninth must void it")
	}
	if dev, ok, _ := p.Consume(secrets[1]); !ok || dev != "a" {
		t.Errorf("the second code: %q, %v; want a", dev, ok)
	}
	p.Void("a")
	if _, ok, _ := p.Consume(secrets[8]); ok {
		t.Error("a code of a voided device was accepted")
	}
	if dev, ok, _ := p.Consume(other); !ok || dev != "b" {
		t.Errorf("another device's code after Void: %q, %v; want b", dev, ok)
	}
	if p.Any() {
		t.Error("Any is true with every code used or void")
	}
}
