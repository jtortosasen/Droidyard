package hosting

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/store"
)

// The unused codes of a detached share are kept in <state>/hosting/shares.json (ADR-0108 §2,
// §7; ADR-0107 §2; ADR-0021 §Layout, §Format). A "restart" here is a second Host over the same
// state root, started the way the daemon starts one (Init); the first is left as the daemon a
// crash would leave.

func sharesPath(state string) string { return filepath.Join(state, "hosting", "shares.json") }

func readShares(t *testing.T, state string) string {
	t.Helper()
	raw, err := os.ReadFile(sharesPath(state))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// blockSharesWrite makes the next write of shares.json fail the way a real disk problem does:
// the store removes a stale .tmp before it writes, and a non-empty directory in its place
// cannot be removed. The returned function lifts the block.
func blockSharesWrite(t *testing.T, state string) (lift func()) {
	t.Helper()
	block := sharesPath(state) + ".tmp"
	if err := os.MkdirAll(filepath.Join(block, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.RemoveAll(block); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDetachedCodesSurviveARestart is ADR-0108 §2, §7: a detached share's unused codes are
// read at start, so a code issued before the restart joins after it, once; a foreground
// share's code is never written, so it does not. The file holds the hashes and not a secret
// byte (ADR-0107 §2), 0600 in a 0700 directory. Seen failing with the file not read at start
// (initShares not called by Init): the first row fails.
func TestDetachedCodesSurviveARestart(t *testing.T) {
	state := t.TempDir()
	h, _, _ := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"lend":[{"device":"R1","to":[]},{"device":"R2","to":[]}]}}`)
	s1, err := h.Pending().IssueSaved("R1")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := h.Pending().IssueSaved("R1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.Pending().IssueSaved("R2")
	if err != nil {
		t.Fatal(err)
	}
	// A foreground share's code lives in memory only.
	if !h.StartForeground("F1", "origin") {
		t.Fatal("StartForeground refused")
	}
	fg, err := h.Pending().IssueSaved("F1")
	if err != nil {
		t.Fatal(err)
	}

	file := readShares(t, state)
	for _, s := range [][16]byte{s1, s2, other, fg} {
		for _, form := range []string{hex.EncodeToString(s[:]), base64.RawURLEncoding.EncodeToString(s[:]), base64.StdEncoding.EncodeToString(s[:])} {
			if strings.Contains(file, form) {
				t.Errorf("shares.json holds a secret as %s", form)
			}
		}
		if bytes.Contains([]byte(file), s[:]) {
			t.Error("shares.json holds the raw bytes of a secret")
		}
	}
	if strings.Contains(file, `"F1"`) {
		t.Errorf("shares.json holds the foreground share:\n%s", file)
	}
	if !strings.Contains(file, `"R1"`) || !strings.Contains(file, `"R2"`) || !strings.Contains(file, `"schemaVersion": 1`) {
		t.Errorf("shares.json does not hold the detached shares:\n%s", file)
	}
	if fi, err := os.Stat(sharesPath(state)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("shares.json is %v, %v; want 0600", fi, err)
	}
	if fi, err := os.Stat(filepath.Dir(sharesPath(state))); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("hosting/ is %v, %v; want 0700", fi, err)
	}

	h2, _, _ := daemonAt(t, state)
	if dev, ok := h2.ConsumeCode(s2); !ok || dev != "R1" {
		t.Errorf("a code issued before the restart: %q, %v; want R1 (the file was not read at start)", dev, ok)
	}
	if _, ok := h2.ConsumeCode(s2); ok {
		t.Error("a code worked twice")
	}
	if dev, ok := h2.ConsumeCode(other); !ok || dev != "R2" {
		t.Errorf("another share's code: %q, %v; want R2", dev, ok)
	}
	if _, ok := h2.ConsumeCode(fg); ok {
		t.Error("a foreground share's code survived a restart")
	}

	// The use is in the file: a third start no longer has s2 or other, and still has s1.
	h3, _, _ := daemonAt(t, state)
	if _, ok := h3.ConsumeCode(s2); ok {
		t.Error("a code used before the second restart works again after it")
	}
	if dev, ok := h3.ConsumeCode(s1); !ok || dev != "R1" {
		t.Errorf("an unused code after two restarts: %q, %v; want R1", dev, ok)
	}
}

// TestConsumedCodeIsWrittenFirst is ADR-0108 §2 and the step's "rests on": a code of a detached
// share is on disk, used, before ConsumeCode returns, so a host that crashes straight after
// cannot have it usable again; and a use that cannot be written is refused and the code void,
// then and after a restart once the file can be written. Seen failing with the file written
// after the consumption returned (Consume not flushing): the first row fails; with the failure
// ignored (ok returned true): the second row fails.
func TestConsumedCodeIsWrittenFirst(t *testing.T) {
	state := t.TempDir()
	h, _, log := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"lend":[{"device":"R1","to":[]}]}}`)
	used, _ := h.Pending().IssueSaved("R1")
	failed, _ := h.Pending().IssueSaved("R1")

	if _, ok := h.ConsumeCode(used); !ok {
		t.Fatal("a code issued was not accepted")
	}
	// Killed here: a new host over the same files has not got the code.
	crashed, _, _ := daemonAt(t, state)
	if _, ok := crashed.ConsumeCode(used); ok {
		t.Error("a code consumed before a crash works after it")
	}

	lift := blockSharesWrite(t, state)
	if dev, ok := h.ConsumeCode(failed); ok || dev != "" {
		t.Errorf("a use that could not be written was accepted for %q", dev)
	}
	if _, ok := h.ConsumeCode(failed); ok {
		t.Error("a code whose use could not be written works on the next try: it must be void")
	}
	if !strings.Contains(log.String(), "could not be written") {
		t.Errorf("the host did not log why it refused:\n%s", log.String())
	}
	// The file still lists it (the write failed); the retry that Reconcile makes writes the
	// void state once the disk is back, and a restart then has no such code.
	lift()
	h.Reconcile()
	again, _, _ := daemonAt(t, state)
	if _, ok := again.ConsumeCode(failed); ok {
		t.Error("a code whose use could not be written is usable after a restart that follows the retry")
	}
}

// TestSharesFileWriteFailureIssuesNothing: a code whose record cannot be written is not
// issued, and the codes the share held are as they were.
func TestSharesFileWriteFailureIssuesNothing(t *testing.T) {
	state := t.TempDir()
	h, _, _ := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"lend":[{"device":"R1","to":[]}]}}`)
	kept, err := h.Pending().IssueSaved("R1")
	if err != nil {
		t.Fatal(err)
	}
	lift := blockSharesWrite(t, state)
	if _, err := h.Pending().IssueSaved("R1"); err == nil {
		t.Fatal("a code was issued with the file unwritable")
	}
	lift()
	if dev, ok := h.ConsumeCode(kept); !ok || dev != "R1" {
		t.Errorf("the code held before the failed issue: %q, %v; want it intact", dev, ok)
	}
	if h.CodePending() {
		t.Error("a code is pending that was never issued")
	}
}

// TestVoidIsOnDisk: unshare voids the codes of the share, and a restart does not bring them
// back, not even when the share is lent again.
func TestVoidIsOnDisk(t *testing.T) {
	state := t.TempDir()
	h, _, _ := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"lend":[{"device":"R1","to":[]},{"device":"R2","to":[]}]}}`)
	a, _ := h.Pending().IssueSaved("R1")
	b, _ := h.Pending().IssueSaved("R2")
	if err := h.Pending().Void("R1"); err != nil {
		t.Fatal(err)
	}
	h2, _, _ := daemonAt(t, state)
	if _, ok := h2.ConsumeCode(a); ok {
		t.Error("a code voided before a restart works after it")
	}
	if dev, ok := h2.ConsumeCode(b); !ok || dev != "R2" {
		t.Errorf("another share's code after the void: %q, %v; want R2", dev, ok)
	}
	c, _ := h2.Pending().IssueSaved("R2")
	if err := h2.Pending().VoidAll(); err != nil {
		t.Fatal(err)
	}
	h3, _, _ := daemonAt(t, state)
	if _, ok := h3.ConsumeCode(c); ok || h3.CodePending() {
		t.Error("codes voided by VoidAll are back after a restart")
	}
}

// TestUnlendDropsTheCodes is ADR-0108 §2: a record whose device has no hosting.lend entry at
// start is dropped, so `host unlend` voids its codes, and the file is rewritten without it.
func TestUnlendDropsTheCodes(t *testing.T) {
	state := t.TempDir()
	h, _, _ := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"lend":[{"device":"R1","to":[]},{"device":"R2","to":[]}]}}`)
	gone, _ := h.Pending().IssueSaved("R1")
	kept, _ := h.Pending().IssueSaved("R2")

	writeConfig(t, state, `{"hosting":{"lend":[{"device":"R2","to":[]}]}}`)
	h2, _, _ := daemonAt(t, state)
	if _, ok := h2.ConsumeCode(gone); ok {
		t.Error("a code of a device no longer lent works after a restart")
	}
	if strings.Contains(readShares(t, state), `"R1"`) {
		t.Errorf("shares.json still holds the device that is no longer lent:\n%s", readShares(t, state))
	}
	if dev, ok := h2.ConsumeCode(kept); !ok || dev != "R2" {
		t.Errorf("the code of a device still lent: %q, %v; want R2", dev, ok)
	}
}

// TestCorruptSharesFileIsQuarantined is ADR-0021 §Format: a file that does not parse, or that
// Droidyard could not have written, is moved to .corrupt-<ts>, reported in the host's
// problems (system.health's stateProblems), and its codes are void; the next share writes a
// new file. A file of a newer schemaVersion is left untouched, reported, and not overwritten.
func TestCorruptSharesFileIsQuarantined(t *testing.T) {
	good := "a" + strings.Repeat("0", 63)
	for _, c := range []struct{ name, body string }{
		{"garbage", "not json at all"},
		{"no schemaVersion", `{"shares":[]}`},
		{"a hash that is not 32 bytes", `{"schemaVersion":1,"shares":[{"device":"R1","codes":[{"sha256":"abcd","issued":"2026-10-19T10:00:00Z"}]}]}`},
		{"a device with no code", `{"schemaVersion":1,"shares":[{"device":"R1","codes":[]}]}`},
		{"a device twice", `{"schemaVersion":1,"shares":[{"device":"R1","codes":[{"sha256":"` + good + `","issued":"2026-10-19T10:00:00Z"}]},{"device":"R1","codes":[{"sha256":"` + good + `","issued":"2026-10-19T10:00:00Z"}]}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := t.TempDir()
			writeConfig(t, state, `{"hosting":{"lend":[{"device":"R1","to":[]}]}}`)
			if err := os.MkdirAll(filepath.Dir(sharesPath(state)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sharesPath(state), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			h, _, _ := daemonAt(t, state)
			var moved string
			for _, p := range h.Problems() {
				if p.File == SharesFile && p.Problem == store.ProblemCorrupt && strings.HasPrefix(p.MovedTo, SharesFile+".corrupt-") {
					moved = p.MovedTo
				}
			}
			if moved == "" {
				t.Fatalf("problems = %+v; want %s corrupt and moved aside", h.Problems(), SharesFile)
			}
			if raw, err := os.ReadFile(filepath.Join(state, moved)); err != nil || string(raw) != c.body {
				t.Errorf("the quarantined file is %q, %v; want the original bytes", raw, err)
			}
			if _, err := os.Stat(sharesPath(state)); err == nil {
				t.Error("the corrupt file is still where it was")
			}
			if h.CodePending() {
				t.Error("a corrupt file left codes pending")
			}
			// The share works again with a new file.
			s, err := h.Pending().IssueSaved("R1")
			if err != nil {
				t.Fatal(err)
			}
			h2, _, _ := daemonAt(t, state)
			if dev, ok := h2.ConsumeCode(s); !ok || dev != "R1" {
				t.Errorf("a code issued after the quarantine: %q, %v; want R1", dev, ok)
			}
		})
	}

	t.Run("newer", func(t *testing.T) {
		state := t.TempDir()
		writeConfig(t, state, `{"hosting":{"lend":[{"device":"R1","to":[]}]}}`)
		body := `{"schemaVersion":2,"shares":[]}`
		if err := os.MkdirAll(filepath.Dir(sharesPath(state)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sharesPath(state), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		h, _, _ := daemonAt(t, state)
		found := false
		for _, p := range h.Problems() {
			found = found || (p.File == SharesFile && p.Problem == store.ProblemNewerVersion)
		}
		if !found {
			t.Errorf("problems = %+v; want %s newerVersion", h.Problems(), SharesFile)
		}
		if _, err := h.Pending().IssueSaved("R1"); !errors.Is(err, store.ErrNewer) {
			t.Errorf("a code issued over a newer file: %v; want %v", err, store.ErrNewer)
		}
		if got := readShares(t, state); got != body {
			t.Errorf("the newer file was rewritten: %q", got)
		}
	})
}

// TestForegroundCodesNeverReachTheFile: a foreground share's codes make no file at all, and
// the end of the share, which voids them, writes none.
func TestForegroundCodesNeverReachTheFile(t *testing.T) {
	state := t.TempDir()
	h, _, _ := daemonAt(t, state)
	writeConfig(t, state, `{"hosting":{"lend":[]}}`)
	h.StartForeground("F1", "origin")
	if _, err := h.Pending().IssueSaved("F1"); err != nil {
		t.Fatal(err)
	}
	h.EndForeground("F1", ShareUnshared)
	if _, err := os.Stat(sharesPath(state)); err == nil {
		t.Errorf("a foreground share wrote shares.json:\n%s", readShares(t, state))
	}
}
