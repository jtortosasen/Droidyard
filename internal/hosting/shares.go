package hosting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jtortosasen/droidyard/internal/store"
)

// The file a detached share's unused codes are kept in, relative to the state root
// (ADR-0108 §2; ADR-0021 §Layout): schemaVersion 1, 0600 in a 0700 directory, written
// atomically, a corrupt file quarantined and reported in system.health's stateProblems.
const (
	SharesDir  = "hosting"
	SharesFile = SharesDir + "/shares.json"
)

// sharesFile is the file's content: the hashes of the unused codes of each detached share
// (ADR-0107 §2: never a secret).
type sharesFile struct {
	Shares []shareRecord `json:"shares"`
}

type shareRecord struct {
	Device string       `json:"device"`
	Codes  []codeRecord `json:"codes"`
}

type codeRecord struct {
	hash   [sha256.Size]byte
	Issued time.Time
}

// codeJSON is a code as the file spells it.
type codeJSON struct {
	SHA256 string    `json:"sha256"`
	Issued time.Time `json:"issued"`
}

func (c codeRecord) MarshalJSON() ([]byte, error) {
	return json.Marshal(codeJSON{SHA256: hex.EncodeToString(c.hash[:]), Issued: c.Issued})
}

func (c *codeRecord) UnmarshalJSON(b []byte) error {
	var j codeJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	raw, err := hex.DecodeString(j.SHA256)
	if err != nil || len(raw) != sha256.Size {
		return errors.New("a code's sha256 is not 32 bytes of hex")
	}
	copy(c.hash[:], raw)
	c.Issued = j.Issued
	return nil
}

// UnmarshalJSON refuses what Pending never writes (an empty device, a device twice, no code or
// more than MaxCodesPerDevice), so a file that is not ours is quarantined as any corrupt one.
func (f *sharesFile) UnmarshalJSON(b []byte) error {
	var raw struct {
		Shares []shareRecord `json:"shares"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range raw.Shares {
		if r.Device == "" || seen[r.Device] || len(r.Codes) == 0 || len(r.Codes) > MaxCodesPerDevice {
			return errors.New("a share record is not one Droidyard writes")
		}
		seen[r.Device] = true
	}
	f.Shares = raw.Shares
	return nil
}

// initShares opens the file of the unused codes, reads what an earlier run left and attaches
// it to the pending codes (ADR-0108 §2, §7). A record whose device has no hosting.lend entry
// is dropped (host unlend voids its codes); a file that is corrupt is quarantined and its
// codes are void; one of a newer schemaVersion is left alone and its codes are void too. The
// caller holds the daemon's lock: the file has one writer.
func (h *Host) initShares() {
	st, err := store.New(h.state)
	if err != nil {
		fmt.Fprintf(h.log, "droidyard: the unused join codes of detached shares are kept in memory only: %v\n", err)
		return
	}
	h.mu.Lock()
	h.sharesStore = st
	h.mu.Unlock()
	h.pending.attach(h.durableCodes, h.saveShares)

	var f sharesFile
	out, err := st.Read(SharesFile, store.Version, &f)
	switch {
	case err != nil:
		// A newer file is left as it is and not overwritten; an unreadable one is only reported.
		fmt.Fprintf(h.log, "droidyard: %s could not be read (%v); the unused join codes of detached shares are void\n", SharesFile, err)
		return
	case out != store.Loaded:
		if out == store.Corrupt {
			fmt.Fprintf(h.log, "droidyard: %s did not parse and was moved aside; the unused join codes of detached shares are void\n", SharesFile)
		}
		return
	}
	lent := map[string]bool{}
	hp, cerr := h.cfg.Hosting()
	if cerr != nil {
		// What cannot be judged is kept: Admit checks hosting.lend again when a code is used.
		fmt.Fprintf(h.log, "droidyard: the configuration cannot be used, so the unused join codes of detached shares are kept as they are: %s\n", cerr.Cause)
	} else {
		for _, l := range hp.Lend {
			lent[l.Device] = true
		}
	}
	keep := f.Shares[:0:0]
	for _, r := range f.Shares {
		if cerr == nil && !lent[r.Device] {
			continue
		}
		keep = append(keep, r)
	}
	h.pending.restore(keep, len(keep) != len(f.Shares))
	if err := h.pending.Flush(); err != nil {
		fmt.Fprintf(h.log, "droidyard: %s could not be rewritten without the shares that stopped: %v; it is tried again\n", SharesFile, err)
	}
}

// durableCodes says whether the codes of the device belong in the file: a foreground share's
// never do (ADR-0108 §3).
func (h *Host) durableCodes(device string) bool {
	_, foreground := h.ForegroundOwner(device)
	return !foreground
}

// saveShares writes the file: the directory 0700, the file 0600, one atomic replacement.
func (h *Host) saveShares(records []shareRecord) error {
	h.mu.Lock()
	st := h.sharesStore
	h.mu.Unlock()
	if st == nil {
		return errors.New("the join codes' file is not open")
	}
	dir := filepath.Join(h.state, SharesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if records == nil {
		records = []shareRecord{}
	}
	return st.Write(SharesFile, sharesFile{Shares: slices.Clone(records)})
}
