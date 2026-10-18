package hosting

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// uploadTTL is how long an upload nobody installed is kept (ADR-0099 §3). A variable only
// so this package's tests can shorten it.
var uploadTTL = 10 * time.Minute

// UploadsDir is where the APKs peers uploaded are kept, under the state root (ADR-0021
// §Layout, as ADR-0098 adds it).
const UploadsDir = "hosting/uploads"

var (
	// ErrUploadSize is an upload whose declared size is none or over capability.PeerMaxUpload.
	ErrUploadSize = errors.New("the upload's size is none or over the limit")
	// ErrUploadShort is a body that ended before, or ran past, the size it declared.
	ErrUploadShort = errors.New("the upload's body is not the size it declared")
	// ErrUploadsClosed is an upload made while the daemon stops.
	ErrUploadsClosed = errors.New("the daemon is stopping")
	// ErrUploadBusy is an upload made while the same peer's earlier one is still arriving.
	ErrUploadBusy = capability.ErrPeerUploadBusy
)

var uploadIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidUploadID reports whether s has the shape of an id this store issues. An id is only
// ever a key in memory; the shape is checked so that nothing else is looked up.
func ValidUploadID(s string) bool { return uploadIDRE.MatchString(s) }

// Uploads holds the APKs the paired peers uploaded to this host (ADR-0099 §3), for
// device.installUpload to install. An upload belongs to the peer that made it, is known
// only by a random id looked up in memory (a path is never built from anything a peer
// sends), is the peer's only one (pending or arriving: a peer has one slot, so its
// concurrent uploads cannot fill the disk), and does not outlive uploadTTL or the daemon.
// Bytes from another machine reach the disk only here: the size is bounded before the first
// is read, the directory is 0700 and a file 0600, and a failed, cancelled or abandoned
// upload leaves nothing.
type Uploads struct {
	dir string
	log io.Writer

	mu      sync.Mutex
	pending map[string]*Upload // by id
	byPeer  map[string]string  // peer name → its pending upload's id
	arrives map[string]bool    // peer name → an upload of it is arriving
	gone    map[string]goneID  // peer name → the last pending upload it lost, and why
	closed  bool
	active  sync.WaitGroup // the Put calls in flight, which Close waits for
}

// goneID is an upload id that is no longer pending, and why, for the caller who names it.
type goneID struct{ id, why string }

// Upload is a stored APK a peer made: taken by device.installUpload, which removes it.
type Upload struct {
	// Path is the file. It stays inside the daemon: no result or error carries it.
	Path string
	// SHA256 is the hex digest of the stored bytes.
	SHA256 string
	// Size is its length.
	Size int64

	peer  string
	timer *time.Timer
}

// Remove deletes the file.
func (u *Upload) Remove() { os.Remove(u.Path) }

// NewUploads returns the store under the state root. Nothing is touched until Sweep or the
// first Put. log receives the lines about uploads that expire.
func NewUploads(state string, log io.Writer) *Uploads {
	if log == nil {
		log = io.Discard
	}
	return &Uploads{dir: filepath.Join(state, filepath.FromSlash(UploadsDir)), log: log,
		pending: map[string]*Upload{}, byPeer: map[string]string{}, arrives: map[string]bool{}, gone: map[string]goneID{}}
}

// prepare makes the directory, readable by this user alone.
func (u *Uploads) prepare() error {
	if err := os.MkdirAll(u.dir, 0o700); err != nil {
		return err
	}
	for _, d := range []string{filepath.Dir(u.dir), u.dir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Sweep removes what an earlier daemon left (ADR-0091): the daemon holds the lock, so every
// file here is one nothing refers to any more.
func (u *Uploads) Sweep() error {
	if err := os.RemoveAll(u.dir); err != nil {
		return err
	}
	return u.prepare()
}

// Put stores the size bytes of body as an upload of the named peer and returns its id and
// SHA-256, replacing the peer's earlier pending upload. The size must be declared and at
// most capability.PeerMaxUpload; a body that does not match it, or that fails, leaves
// nothing behind.
func (u *Uploads) Put(peer string, size int64, body io.Reader) (id, sum string, err error) {
	if size <= 0 || size > capability.PeerMaxUpload {
		return "", "", ErrUploadSize
	}
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return "", "", ErrUploadsClosed
	}
	if u.arrives[peer] {
		u.mu.Unlock()
		return "", "", ErrUploadBusy
	}
	// The peer's one slot: its earlier pending upload goes as the new one starts, so no peer
	// has more than one file here at a time, whatever it sends.
	u.dropPending(peer, "replaced by a newer upload")
	u.arrives[peer] = true
	u.active.Add(1)
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		delete(u.arrives, peer)
		u.mu.Unlock()
		u.active.Done()
	}()

	if err := u.prepare(); err != nil {
		return "", "", err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	id = hex.EncodeToString(b[:])
	path := filepath.Join(u.dir, id+".apk")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	// One byte more than declared, so a body that runs past its size is seen.
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = fmt.Errorf("%w: %d bytes against %d", ErrUploadShort, n, size)
	}
	if err != nil {
		os.Remove(path)
		return "", "", err
	}

	up := &Upload{Path: path, SHA256: hex.EncodeToString(h.Sum(nil)), Size: size, peer: peer}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		os.Remove(path)
		return "", "", ErrUploadsClosed
	}
	u.pending[id], u.byPeer[peer] = up, id
	up.timer = time.AfterFunc(uploadTTL, func() { u.expire(id, up) })
	return id, up.SHA256, nil
}

// Take hands over the peer's pending upload with this id, once: it is no longer pending,
// and the caller removes the file when it is done. An id that is another peer's, expired,
// replaced or never issued is not found; the id is only ever a key.
func (u *Uploads) Take(peer, id string) (*Upload, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	up := u.pending[id]
	if u.closed || peer == "" || up == nil || up.peer != peer {
		return nil, false
	}
	up.timer.Stop()
	delete(u.pending, id)
	delete(u.byPeer, peer)
	return up, true
}

// dropPending removes the peer's pending upload, if it has one, and remembers why. u.mu is held.
func (u *Uploads) dropPending(peer, why string) {
	id := u.byPeer[peer]
	up := u.pending[id]
	if up == nil {
		return
	}
	up.timer.Stop()
	delete(u.pending, id)
	delete(u.byPeer, peer)
	u.gone[peer] = goneID{id, why}
	up.Remove()
}

// Forget removes the peer's pending upload: the peer was unpaired, and a name paired again
// with another certificate must not find what the old one left.
func (u *Uploads) Forget(peer string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.dropPending(peer, "removed when the peer was unpaired")
}

// Gone says why an id the peer names is not pending any more: it expired, a newer upload
// replaced it, or the peer was unpaired. It is empty for an id this store never issued to
// the peer, and for one that was taken.
func (u *Uploads) Gone(peer, id string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if g := u.gone[peer]; g.id == id {
		return g.why
	}
	return ""
}

// expire removes up when it is still the pending upload it was.
func (u *Uploads) expire(id string, up *Upload) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.pending[id] != up {
		return
	}
	u.dropPending(up.peer, "expired after "+uploadTTL.String())
	fmt.Fprintf(u.log, "droidyard: peer %s: its upload of %s was not installed in %s; removed\n", up.peer, up.SHA256[:12], uploadTTL)
}

// Close ends the store: no upload is made or taken afterwards, the Put calls in flight end
// (the listener stopped, so their bodies fail), and every file is removed, installing or not.
func (u *Uploads) Close() {
	u.mu.Lock()
	u.closed = true
	for id, up := range u.pending {
		up.timer.Stop()
		delete(u.pending, id)
	}
	clear(u.byPeer)
	u.mu.Unlock()
	u.active.Wait()
	os.RemoveAll(u.dir)
}
