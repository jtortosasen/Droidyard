// Package hosting is a Droidyard daemon's side of the peer link (ADR-0097, ADR-0098): its
// identity, the links it dials to the hosts it is paired with, and the state the peer
// listener and the links report to system.health. The listener itself is
// adapters/peer; the policy that pairs and lends is internal/policy.
package hosting

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/jtortosasen/droidyard/internal/store"
)

// The identity's files, under <state>/identity/ (ADR-0098 §1; ADR-0021 §Layout).
const (
	IdentityDir = "identity"
	KeyFile     = "key.pem"
	CertFile    = "cert.pem"
)

// Identity is a daemon's key and its self-signed certificate. Its Fingerprint is what a
// peer pins; the certificate carries no name and no validity anyone checks, since the
// pin is the whole of the trust (ADR-0098 §2).
type Identity struct {
	cert        tls.Certificate
	fingerprint string
}

// Fingerprint is the lowercase hex SHA-256 of the certificate's SubjectPublicKeyInfo, as
// `droidyard id` prints it. It is that of the key, so a certificate made again from the
// same key keeps it.
func (i *Identity) Fingerprint() string { return i.fingerprint }

// Certificate is the identity as a TLS client or server presents it.
func (i *Identity) Certificate() tls.Certificate { return i.cert }

// FingerprintOf is the fingerprint of a certificate presented to a TLS handshake.
func FingerprintOf(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// LoadIdentity reads the identity in <state>/identity/, or makes it when the key is absent
// (ADR-0098 §1). The directory is 0700, the key 0600. A file that does not parse, or a
// certificate that is not the key's, is renamed <name>.corrupt-<UTC timestamp> and
// replaced, as any state file is (ADR-0021 §Format); the returned problems say which, for
// system.health. A key that cannot be used takes the certificate with it, and the identity
// is new; a certificate alone that cannot be used is made again from the key, and the
// fingerprint stays. The caller holds the daemon's lock: the identity has one writer.
func LoadIdentity(state string) (*Identity, []store.Problem, error) {
	dir := filepath.Join(state, IdentityDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, nil, err
	}
	sweepTemp(dir)

	var problems []store.Problem
	quarantine := func(name string) {
		p := store.Problem{File: IdentityDir + "/" + name, Problem: store.ProblemCorrupt}
		suffix := ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+suffix)); err == nil {
			p.MovedTo = IdentityDir + "/" + name + suffix
		}
		problems = append(problems, p)
	}

	key, keyErr := readKey(filepath.Join(dir, KeyFile))
	switch {
	case keyErr == nil:
		// A key someone loosened is tightened, not trusted as it is.
		if err := os.Chmod(filepath.Join(dir, KeyFile), 0o600); err != nil {
			return nil, problems, err
		}
	case errors.Is(keyErr, fs.ErrNotExist):
	default:
		quarantine(KeyFile)
	}
	if key == nil {
		// No usable key: whatever certificate there is belongs to the one that is gone.
		if _, err := os.Stat(filepath.Join(dir, CertFile)); err == nil {
			quarantine(CertFile)
		}
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, problems, err
		}
		key = k
		if err := writeKey(filepath.Join(dir, KeyFile), key); err != nil {
			return nil, problems, err
		}
	}

	cert, certErr := readCert(filepath.Join(dir, CertFile), key)
	switch {
	case certErr == nil:
	case errors.Is(certErr, fs.ErrNotExist):
	default:
		quarantine(CertFile)
	}
	if cert == nil {
		der, err := newCert(key)
		if err != nil {
			return nil, problems, err
		}
		if err := writeFile(filepath.Join(dir, CertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
			return nil, problems, err
		}
		if cert, err = x509.ParseCertificate(der); err != nil {
			return nil, problems, err
		}
	}
	return &Identity{
		cert:        tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key, Leaf: cert},
		fingerprint: FingerprintOf(cert),
	}, problems, nil
}

// sweepTemp removes the temporary files a write that died left (ADR-0091).
func sweepTemp(dir string) {
	for _, name := range []string{KeyFile, CertFile} {
		os.Remove(filepath.Join(dir, name+".tmp"))
	}
}

// readKey reads the Ed25519 private key; a missing file is fs.ErrNotExist, anything else
// that is not a key is another error.
func readKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an Ed25519 key")
	}
	return key, nil
}

// readCert reads the certificate and checks it is key's.
func readCert(path string, key ed25519.PrivateKey) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !pub.Equal(key.Public()) {
		return nil, errors.New("the certificate is not the key's")
	}
	return cert, nil
}

func writeKey(path string, key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writeFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// newCert is a self-signed certificate for key, good for clients and servers. Nothing
// checks its name or its dates: a peer pins the key (ADR-0098 §2), so they are only what
// a TLS stack needs to see.
func newCert(key ed25519.PrivateKey) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "droidyard"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(100, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	return x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
}

// writeFile writes data 0600 atomically: <path>.tmp, sync, rename, sync the directory.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
