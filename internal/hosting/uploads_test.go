package hosting

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

func newUploads(t *testing.T) (*Uploads, string) {
	t.Helper()
	state := t.TempDir()
	u := NewUploads(state, io.Discard)
	t.Cleanup(u.Close)
	return u, filepath.Join(state, filepath.FromSlash(UploadsDir))
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func put(t *testing.T, u *Uploads, peer string, body []byte) string {
	t.Helper()
	id, sum, err := u.Put(peer, int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(body)
	if sum != hex.EncodeToString(h[:]) {
		t.Errorf("Put's SHA-256 = %s; want the digest of the bytes", sum)
	}
	return id
}

// Decision (ADR-0099 §3): a peer has one pending upload; a new one removes the earlier one's
// file and its id no longer finds anything. Seen failing with the uploads kept side by side.
func TestSecondUploadReplacesTheFirst(t *testing.T) {
	u, dir := newUploads(t)
	first := put(t, u, "anna", []byte("first"))
	if len(files(t, dir)) != 1 {
		t.Fatalf("after the first upload: %v", files(t, dir))
	}
	second := put(t, u, "anna", []byte("second"))
	if got := files(t, dir); len(got) != 1 || got[0] != second+".apk" {
		t.Errorf("after the second upload the directory holds %v; want only %s.apk", got, second)
	}
	if _, ok := u.Take("anna", first); ok {
		t.Error("the replaced upload can still be taken")
	}
	// Another peer's upload is its own pending one: it replaces nobody's.
	put(t, u, "bob", []byte("bob's"))
	if len(files(t, dir)) != 2 {
		t.Errorf("anna's and bob's uploads: %v; want both", files(t, dir))
	}
}

// Decision (ADR-0099 §3): an upload is its peer's only. Another peer naming its id finds
// nothing and does not consume it, an id shaped as a path is a key that matches nothing and
// opens no file (a file the id points at outside the directory is untouched), and the owner
// takes it once.
func TestUploadIsThePeersOwnAndUsedOnce(t *testing.T) {
	u, dir := newUploads(t)
	id := put(t, u, "anna", []byte("apk"))

	if _, ok := u.Take("bob", id); ok {
		t.Fatal("bob took anna's upload")
	}
	if _, ok := u.Take("", id); ok {
		t.Fatal("a caller with no peer name took an upload")
	}
	outside := filepath.Join(filepath.Dir(dir), "x.apk")
	if err := os.WriteFile(outside, []byte("not an upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../x", "../x.apk", "../../hosting/uploads/" + id, id + "/..", id + ".apk", strings.ToUpper(id)} {
		if _, ok := u.Take("anna", bad); ok {
			t.Errorf("Take(%q) found something", bad)
		}
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "not an upload" {
		t.Errorf("the file outside the directory was touched: %q, %v", b, err)
	}
	if len(files(t, dir)) != 1 {
		t.Fatalf("a refused Take disturbed the directory: %v", files(t, dir))
	}

	up, ok := u.Take("anna", id)
	if !ok {
		t.Fatal("anna cannot take her upload")
	}
	if b, _ := os.ReadFile(up.Path); string(b) != "apk" {
		t.Errorf("the taken file holds %q", b)
	}
	if _, ok := u.Take("anna", id); ok {
		t.Error("an upload was taken twice")
	}
	up.Remove()
	if len(files(t, dir)) != 0 {
		t.Errorf("after Remove: %v", files(t, dir))
	}
}

// Decision (ADR-0099 §3): bytes from another machine are bounded before they are read and a
// failed upload leaves nothing: a size over the limit, none, a body shorter or longer than
// its declared size, and a body that fails midway. A refused size reads no byte.
func TestFailedUploadLeavesNothing(t *testing.T) {
	u, dir := newUploads(t)

	for _, size := range []int64{0, -1, capability.PeerMaxUpload + 1} {
		_, _, err := u.Put("anna", size, failingReader{t})
		if !errors.Is(err, ErrUploadSize) {
			t.Errorf("size %d: %v; want ErrUploadSize without reading", size, err)
		}
	}
	boom := errors.New("connection reset")
	for name, c := range map[string]struct {
		size int64
		body io.Reader
		want error
	}{
		"shorter": {10, strings.NewReader("12345"), ErrUploadShort},
		"longer":  {5, strings.NewReader("1234567890"), ErrUploadShort},
		"fails":   {10, io.MultiReader(strings.NewReader("12345"), errReader{boom}), boom},
	} {
		if _, _, err := u.Put("anna", c.size, c.body); !errors.Is(err, c.want) {
			t.Errorf("%s: %v; want %v", name, err, c.want)
		}
	}
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("failed uploads left %v", got)
	}
	// And they did not become anna's pending upload.
	if _, ok := u.byPeer["anna"]; ok {
		t.Error("a failed upload is pending")
	}
}

type failingReader struct{ t *testing.T }

func (f failingReader) Read([]byte) (int, error) {
	f.t.Error("a refused upload's body was read")
	return 0, io.EOF
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// Decision (ADR-0099 §3): an upload nobody installed is removed after its time, and one that
// was taken is not (its taker removes it). The clock is the package's uploadTTL.
func TestUnusedUploadExpires(t *testing.T) {
	old := uploadTTL
	uploadTTL = 80 * time.Millisecond
	t.Cleanup(func() { uploadTTL = old })
	var log strings.Builder
	state := t.TempDir()
	u := NewUploads(state, &log)
	t.Cleanup(u.Close)
	dir := filepath.Join(state, filepath.FromSlash(UploadsDir))

	left := put(t, u, "anna", []byte("left alone"))
	taken := put(t, u, "bob", []byte("taken"))
	up, ok := u.Take("bob", taken)
	if !ok {
		t.Fatal("bob cannot take his upload")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(files(t, dir)) > 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := files(t, dir); len(got) != 1 || got[0] != filepath.Base(up.Path) {
		t.Fatalf("after the time passed the directory holds %v; want only the taken file", got)
	}
	time.Sleep(200 * time.Millisecond) // a taken upload's timer is stopped: it stays
	if _, err := os.Stat(up.Path); err != nil {
		t.Errorf("the taken upload was removed by the clock: %v", err)
	}
	if _, ok := u.Take("anna", left); ok {
		t.Error("the expired upload can still be taken")
	}
	u.mu.Lock() // the expiry writes the log under it
	logged := log.String()
	u.mu.Unlock()
	if !strings.Contains(logged, "peer anna: its upload of") {
		t.Errorf("the expiry is not in the log: %q", logged)
	}
}

// Decision (ADR-0091, ADR-0099 §3): what an earlier daemon left is swept at start, so a
// file in the directory is gone after Sweep, and the directory is 0700 below a 0700 parent.
func TestLeftoversAreSweptAtStart(t *testing.T) {
	u, dir := newUploads(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"0123456789abcdef0123456789abcdef.apk", "stray", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("left by a daemon that was killed"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.Sweep(); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("after the sweep: %v", got)
	}
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want 0700", d, fi, err)
		}
	}
}

// Decision (plan m2t step 4: nothing left on a daemon stop): Close removes the pending
// uploads and the taken one being installed, ends the Put calls in flight, and refuses
// every later one.
func TestCloseRemovesEverything(t *testing.T) {
	u, dir := newUploads(t)
	put(t, u, "anna", []byte("pending"))
	id := put(t, u, "bob", []byte("installing"))
	if _, ok := u.Take("bob", id); !ok {
		t.Fatal("bob cannot take his upload")
	}

	pr, pw := io.Pipe()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := u.Put("carol", 100, pr)
		done <- err
	}()
	<-started
	pw.Write([]byte("half"))
	for len(files(t, dir)) < 3 { // carol's partial file exists
		time.Sleep(5 * time.Millisecond)
	}
	closed := make(chan struct{})
	go func() { u.Close(); close(closed) }()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-closed:
		t.Fatal("Close returned while a Put was still arriving: it must wait for it, or the file is written after the sweep")
	default:
	}
	pw.CloseWithError(errors.New("the listener stopped")) // the body fails, as a closed connection does
	<-closed
	if err := <-done; err == nil {
		t.Error("the Put in flight succeeded")
	}
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("after Close: %v", got)
	}
	if _, _, err := u.Put("anna", 1, strings.NewReader("x")); !errors.Is(err, ErrUploadsClosed) {
		t.Errorf("Put after Close: %v; want ErrUploadsClosed", err)
	}
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("a Put after Close wrote %v", got)
	}
}

// noConfig is a policy with no hosting and no remotes.
type noConfig struct{}

func (noConfig) Hosting() (policy.HostingPolicy, *capability.Error) {
	return policy.HostingPolicy{}, nil
}
func (noConfig) Remotes() ([]policy.Remote, *capability.Error) { return nil, nil }

// Decision (ADR-0091, ADR-0099 §3): the daemon's start (Host.Init) sweeps the uploads an
// earlier daemon left, before anything else it does with the state root.
func TestInitSweepsTheUploadsOfAnEarlierDaemon(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, filepath.FromSlash(UploadsDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(dir, "0123456789abcdef0123456789abcdef.apk")
	if err := os.WriteFile(left, []byte("left by an earlier daemon"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := New(noConfig{}, state, "0.0.0", io.Discard)
	t.Cleanup(h.Uploads().Close)
	if err := h.Init(); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); len(got) != 0 {
		t.Errorf("after Init the upload directory holds %v", got)
	}
}
