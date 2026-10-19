package peer_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// restart is a second Host over the same state root and configuration: the daemon started
// again after the first one went away.
func (s *side) restart(t *testing.T) *hosting.Host {
	t.Helper()
	h := hosting.New(s.cfg, s.state, "0.0.0", s.log)
	t.Cleanup(h.Uploads().Close)
	if err := h.Init(); err != nil {
		t.Fatal(err)
	}
	return h
}

// TestConsumedCodeIsWrittenFirst is ADR-0108 §2 over real TLS, and the step's "rests on": the
// use of a detached share's code is on disk before the join reaches Admit, so a host that dies
// at any point after it cannot have the code usable twice; a use that cannot be written is a
// refused join, Admit never runs, and the code is void, in memory at once and on disk once the
// disk is back (Reconcile retries). Seen failing with the write made after Admit (the
// consumption flushed by a call after Admit returns): the refused-join rows fail, because the
// join is answered and Admit ran.
func TestConsumedCodeIsWrittenFirst(t *testing.T) {
	host, client, addr, adm := joinSetup(t)
	host.cfg.mu.Lock()
	host.cfg.hosting.Lend = []policy.Lend{{Device: "SER1", To: []string{}}}
	host.cfg.mu.Unlock()
	cid := client.host.Identity()
	join := func(secret [16]byte) error {
		_, err := hosting.Join(context.Background(), cid, codeFor(t, host, addr, secret), "anna", "0.0.0")
		return err
	}

	// A join that is answered has its code on disk, used: a restart straight after does not
	// have it.
	answered, err := host.host.Pending().IssueSaved("SER1", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := join(answered); err != nil {
		t.Fatalf("a join with an unused code: %v", err)
	}
	if n := len(adm.all()); n != 1 {
		t.Fatalf("Admit ran %d times; want 1", n)
	}
	if _, ok := host.restart(t).ConsumeCode(answered); ok {
		t.Error("a code whose join was answered is usable after a restart")
	}

	// The disk fails: the join is refused before Admit, and the code is void straight away.
	failing, err := host.host.Pending().IssueSaved("SER1", true)
	if err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(host.state, "hosting", "shares.json.tmp")
	if err := os.MkdirAll(filepath.Join(block, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if jerr := join(failing); jerr == nil || joinErrReason(t, jerr) != hosting.JoinRefused {
		t.Errorf("a join whose consumption could not be written: %v; want refused", jerr)
	}
	if n := len(adm.all()); n != 1 {
		t.Errorf("Admit ran %d times; want it not to run for a use that could not be written", n)
	}
	if !strings.Contains(host.log.String(), "could not be written") {
		t.Errorf("the host did not log why it refused the join:\n%s", host.log.String())
	}
	if jerr := join(failing); jerr == nil {
		t.Error("the code worked on the next try: it must be void")
	}

	// The disk comes back, the retry writes the file, and a restart has no such code.
	if err := os.RemoveAll(block); err != nil {
		t.Fatal(err)
	}
	host.host.Reconcile()
	if _, ok := host.restart(t).ConsumeCode(failing); ok {
		t.Error("a code whose use could not be written is usable after a restart that follows the retry")
	}
}
