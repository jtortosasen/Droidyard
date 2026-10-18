package hosting

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// TestIdentity pins ADR-0098 §1: the identity is made once and is the same at every later
// start; the directory is private and the key readable by its owner alone; the
// fingerprint is the SHA-256 of the certificate's public key info, which is that of the key;
// and an identity file that cannot be used is moved aside as any state file is
// (ADR-0021 §Format), reported, and replaced: a bad key makes a new identity, a bad
// certificate alone is made again from the key and the fingerprint stays.
func TestIdentity(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, IdentityDir)

	first, problems, err := LoadIdentity(state)
	if err != nil || len(problems) != 0 {
		t.Fatalf("the first start: %v, problems %v", err, problems)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first.Fingerprint()) {
		t.Fatalf("the fingerprint %q is not 64 lowercase hex digits", first.Fingerprint())
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("identity/ is %o; want 700", m)
	}
	if m := mode(t, filepath.Join(dir, KeyFile)); m != 0o600 {
		t.Errorf("key.pem is %o; want 600", m)
	}

	// The fingerprint is the hash of the public key info of the certificate on disk, read
	// independently of the code that wrote it.
	pemBytes, _ := os.ReadFile(filepath.Join(dir, CertFile))
	block, _ := pem.Decode(pemBytes)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	if got := hex.EncodeToString(sum[:]); got != first.Fingerprint() {
		t.Errorf("the fingerprint is %s; the hash of the certificate's public key info is %s", first.Fingerprint(), got)
	}

	// Stable across restarts.
	again, problems, err := LoadIdentity(state)
	if err != nil || len(problems) != 0 || again.Fingerprint() != first.Fingerprint() {
		t.Errorf("a restart gives %v / %v / %v; want the same identity and no problem", again.Fingerprint(), problems, err)
	}

	// A key someone made world-readable is tightened.
	if err := os.Chmod(filepath.Join(dir, KeyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadIdentity(state); err != nil || mode(t, filepath.Join(dir, KeyFile)) != 0o600 {
		t.Errorf("a loosened key is %o after a start (%v); want 600", mode(t, filepath.Join(dir, KeyFile)), err)
	}

	// A missing certificate is made again from the key: the fingerprint is the key's.
	os.Remove(filepath.Join(dir, CertFile))
	regained, problems, err := LoadIdentity(state)
	if err != nil || len(problems) != 0 || regained.Fingerprint() != first.Fingerprint() {
		t.Errorf("without the certificate: %v / %v / %v; want the same fingerprint and no problem", regained.Fingerprint(), problems, err)
	}

	// A certificate that is not parseable, and one that is another key's, are moved aside
	// and made again; the key, so the fingerprint, stays.
	for name, body := range map[string]string{"garbage": "not a certificate", "another key's": ""} {
		if body == "" {
			other := t.TempDir()
			if _, _, err := LoadIdentity(other); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(filepath.Join(other, IdentityDir, CertFile))
			body = string(b)
		}
		os.WriteFile(filepath.Join(dir, CertFile), []byte(body), 0o600)
		id, problems, err := LoadIdentity(state)
		if err != nil || id.Fingerprint() != first.Fingerprint() {
			t.Fatalf("%s certificate: %v / %v; want the same fingerprint", name, id, err)
		}
		if len(problems) != 1 || problems[0].File != "identity/cert.pem" || problems[0].Problem != "corrupt" || !strings.HasPrefix(problems[0].MovedTo, "identity/cert.pem.corrupt-") {
			t.Errorf("%s certificate: problems %+v; want identity/cert.pem corrupt, moved aside", name, problems)
		}
		if moved, err := os.ReadFile(filepath.Join(state, problems[0].MovedTo)); err != nil || string(moved) != body {
			t.Errorf("%s certificate: the moved file holds %q, %v; want its bytes intact", name, moved, err)
		}
	}

	// A key that is not a key makes a new identity: it is reported, moved aside with its
	// bytes, and the certificate that went with it goes too.
	os.WriteFile(filepath.Join(dir, KeyFile), []byte("garbage"), 0o600)
	fresh, problems, err := LoadIdentity(state)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Fingerprint() == first.Fingerprint() {
		t.Errorf("a corrupt key left the fingerprint %s; want a new identity", fresh.Fingerprint())
	}
	files := map[string]string{}
	for _, p := range problems {
		files[p.File] = p.MovedTo
	}
	if files["identity/key.pem"] == "" || files["identity/cert.pem"] == "" {
		t.Fatalf("problems %+v; want the key and its certificate both moved aside", problems)
	}
	if moved, err := os.ReadFile(filepath.Join(state, files["identity/key.pem"])); err != nil || string(moved) != "garbage" {
		t.Errorf("the corrupt key's bytes were not kept: %q, %v", moved, err)
	}
	if mode(t, filepath.Join(dir, KeyFile)) != 0o600 {
		t.Errorf("the new key is %o; want 600", mode(t, filepath.Join(dir, KeyFile)))
	}
	if next, problems, err := LoadIdentity(state); err != nil || len(problems) != 0 || next.Fingerprint() != fresh.Fingerprint() {
		t.Errorf("the replacement does not survive a restart: %v / %v / %v", next.Fingerprint(), problems, err)
	}

	// A temporary file a write left behind is swept.
	os.WriteFile(filepath.Join(dir, KeyFile+".tmp"), []byte("x"), 0o600)
	if _, _, err := LoadIdentity(state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFile+".tmp")); !os.IsNotExist(err) {
		t.Errorf("a leftover key.pem.tmp was kept (%v)", err)
	}
}
