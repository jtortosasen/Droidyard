package peer_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The upload route of the peer listener (ADR-0099 §3), over real TLS on loopback.

// uploadsDir is where the host keeps what peers uploaded.
func (s *side) uploadsDir() string {
	return filepath.Join(s.state, filepath.FromSlash(hosting.UploadsDir))
}

// uploaded lists the files in the host's upload directory.
func (s *side) uploaded(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(s.uploadsDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// dialRaw is a pinned TLS connection to the host, on which a test writes an upload's request
// line and headers by hand, so it controls exactly how many bytes follow them.
func dialRaw(t *testing.T, host, client *side, addr string) net.Conn {
	t.Helper()
	cfg := hosting.PinnedConfig(client.host.Identity(), func(fp string) bool { return fp == host.host.Identity().Fingerprint() })
	c, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// putHeaders writes an upload request's head; length < 0 leaves out Content-Length and
// declares a chunked body instead.
func putHeaders(t *testing.T, c net.Conn, length int64) {
	t.Helper()
	head := "PUT " + capability.PeerUploadPath + " HTTP/1.1\r\nHost: host\r\nContent-Type: application/vnd.android.package-archive\r\n"
	if length < 0 {
		head += "Transfer-Encoding: chunked\r\n"
	} else {
		head += fmt.Sprintf("Content-Length: %d\r\n", length)
	}
	if _, err := io.WriteString(c, head+"\r\n"); err != nil {
		t.Fatal(err)
	}
}

func statusOf(t *testing.T, c net.Conn) int {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("no response to the upload: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// Decision (ADR-0099 §3): an upload arrives whole and is stored under an id known only to
// the peer that sent it; the receipt's SHA-256 is the digest of the bytes stored, the file
// is 0600 in a 0700 directory, and daemon.log has a line with the peer, the size and a
// prefix of the digest.
func TestUploadRoundTrip(t *testing.T) {
	host, _, link := pair(t)
	body := make([]byte, 3<<20+17)
	rand.Read(body)

	rec, err := link.Upload(context.Background(), int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if rec.SHA256 != sumOf(body) {
		t.Errorf("receipt SHA-256 = %s; want the digest of the bytes sent", rec.SHA256)
	}
	up, ok := host.host.Uploads().Take("anna", rec.ID)
	if !ok {
		t.Fatalf("the host does not have upload %s for anna", rec.ID)
	}
	stored, err := os.ReadFile(up.Path)
	if err != nil || !bytes.Equal(stored, body) {
		t.Errorf("the stored file differs from the bytes sent (%v)", err)
	}
	fi, _ := os.Stat(up.Path)
	di, _ := os.Stat(host.uploadsDir())
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Errorf("modes: file %v, directory %v; want 0600 and 0700", fi.Mode().Perm(), di.Mode().Perm())
	}
	up.Remove()
	want := fmt.Sprintf("peer anna: stored an upload of %d bytes, sha256 %s", len(body), rec.SHA256[:12])
	if !strings.Contains(host.log.String(), want) {
		t.Errorf("daemon.log has no line %q:\n%s", want, host.log.String())
	}
}

// Decision (ADR-0099 §3): the size bound is answered before a byte of the body is read. The
// requests here send the headers and nothing else, so a handler that read first would wait
// for the body and the test would time out; Content-Length is required (411 for a chunked
// body), and nothing is written for any of them.
func TestUploadBoundsAreAnsweredBeforeReading(t *testing.T) {
	host, client, _ := pair(t)
	addr := host.host.Health().Hosting.Listen
	for _, c := range []struct {
		name   string
		length int64
		status int
	}{
		{"one byte over 512 MiB", capability.PeerMaxUpload + 1, http.StatusRequestEntityTooLarge},
		{"513 MiB", 513 << 20, http.StatusRequestEntityTooLarge},
		{"no length", -1, http.StatusLengthRequired},
		{"empty", 0, http.StatusBadRequest},
	} {
		conn := dialRaw(t, host, client, addr)
		putHeaders(t, conn, c.length)
		if got := statusOf(t, conn); got != c.status {
			t.Errorf("%s: status %d; want %d", c.name, got, c.status)
		}
		if files := host.uploaded(t); len(files) != 0 {
			t.Errorf("%s: the host wrote %v", c.name, files)
		}
	}
	if !strings.Contains(host.log.String(), "an upload of 536870913 bytes was refused") {
		t.Errorf("daemon.log does not say the oversized upload was refused:\n%s", host.log.String())
	}
}

// Decision (ADR-0099 §3, plan m2t step 4): an upload that does not arrive leaves nothing on
// the disk, whatever ended it: the sender going away partway, a body that stalls past the
// bound, and the peer being unpaired while it is sent.
func TestUploadThatDoesNotArriveLeavesNothing(t *testing.T) {
	peer.SetUploadBound(700 * time.Millisecond)
	t.Cleanup(func() { peer.SetUploadBound(11 * time.Minute) })

	partial := func(t *testing.T, host, client *side) net.Conn {
		t.Helper()
		conn := dialRaw(t, host, client, host.host.Health().Hosting.Listen)
		putHeaders(t, conn, 1<<20)
		if _, err := conn.Write(make([]byte, 100<<10)); err != nil {
			t.Fatal(err)
		}
		// The handler is storing it: the file exists before the end.
		testtime.Eventually(t, testtime.Bound(5*time.Second), "the partial file", func() bool { return len(host.uploaded(t)) == 1 })
		return conn
	}

	t.Run("the sender disconnects", func(t *testing.T) {
		host, client, _ := pair(t)
		conn := partial(t, host, client)
		conn.Close()
		testtime.Eventually(t, testtime.Bound(5*time.Second), "the partial file to be removed", func() bool { return len(host.uploaded(t)) == 0 })
	})

	t.Run("the body stalls", func(t *testing.T) {
		host, client, _ := pair(t)
		conn := partial(t, host, client)
		// Product claims (the upload bounds), here and below: unscaled, no level-5 run timed them.
		testtime.Eventually(t, 5*time.Second, "the stalled upload to be cut", func() bool { return len(host.uploaded(t)) == 0 })
		if got := statusOf(t, conn); got != http.StatusInternalServerError {
			t.Errorf("a stalled upload was answered with %d; want 500", got)
		}
	})

	t.Run("the peer is unpaired", func(t *testing.T) {
		peer.SetUploadBound(time.Minute)
		host, client, _ := pair(t)
		conn := partial(t, host, client)
		host.cfg.setPeers()
		testtime.Eventually(t, 6*time.Second, "the revoked upload to be removed", func() bool { return len(host.uploaded(t)) == 0 })
		_ = conn
	})
}

// Decision (ADR-0098 §3): the upload route is behind the same checks as the others, so a
// request carrying an Origin is refused, and only PUT is served.
func TestUploadRouteChecks(t *testing.T) {
	host, client, _ := pair(t)
	addr := host.host.Health().Hosting.Listen
	cfg := hosting.PinnedConfig(client.host.Identity(), func(fp string) bool { return fp == host.host.Identity().Fingerprint() })
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}

	req, _ := http.NewRequest(http.MethodPut, "https://"+addr+capability.PeerUploadPath, strings.NewReader("x"))
	req.Header.Set("Origin", "https://evil.example")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a PUT with an Origin: %d; want 403", resp.StatusCode)
	}
	resp, err = hc.Get("https://" + addr + capability.PeerUploadPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("a GET of the upload route: %d; want 405", resp.StatusCode)
	}
	if files := host.uploaded(t); len(files) != 0 {
		t.Errorf("the host wrote %v", files)
	}
}

// slowBody gives its bytes in small reads, a pause between them, as a slow link would take
// them: the upload is under way for long enough to be cancelled.
type slowBody struct{ left int }

func (s *slowBody) Read(p []byte) (int, error) {
	if s.left == 0 {
		return 0, io.EOF
	}
	time.Sleep(10 * time.Millisecond)
	n := min(len(p), 16<<10, s.left)
	s.left -= n
	return n, nil
}

// Decision (plan m2t step 4): the client giving up on an upload (a cancel, or its own 10
// minutes) ends the request, and the host removes the partial file; a link that is down
// sends nothing and says so.
func TestUploadCancelledByTheClientLeavesNothing(t *testing.T) {
	host, _, link := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := link.Upload(ctx, 8<<20, &slowBody{left: 8 << 20})
		done <- err
	}()
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the partial file", func() bool { return len(host.uploaded(t)) == 1 })
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Upload after a cancel = %v; want context.Canceled", err)
		}
	case <-time.After(testtime.Bound(5 * time.Second)):
		t.Fatal("Upload did not return after its context was cancelled")
	}
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the partial file to be removed", func() bool { return len(host.uploaded(t)) == 0 })

	if _, err := (&hosting.Link{}).Upload(context.Background(), 1, strings.NewReader("x")); err != hosting.ErrNotConnected {
		t.Errorf("Upload on a link that is down = %v; want ErrNotConnected", err)
	}
}

// Decision (verifier B1): a peer's concurrent uploads cannot fill the host's disk. While one
// of the peer's is arriving, a second PUT is answered 409 and writes nothing, however many
// are sent; the first goes on and completes.
func TestConcurrentUploadsOfOnePeerAreRefused(t *testing.T) {
	host, client, link := pair(t)
	addr := host.host.Health().Hosting.Listen
	first := dialRaw(t, host, client, addr)
	putHeaders(t, first, 1<<20)
	if _, err := first.Write(make([]byte, 100<<10)); err != nil {
		t.Fatal(err)
	}
	testtime.Eventually(t, testtime.Bound(5*time.Second), "the first file", func() bool { return len(host.uploaded(t)) == 1 })

	for range 3 {
		second := dialRaw(t, host, client, addr)
		putHeaders(t, second, 1<<10)
		second.Write(make([]byte, 1<<10))
		if got := statusOf(t, second); got != http.StatusConflict {
			t.Errorf("a second upload while one arrives: status %d; want 409", got)
		}
	}
	if files := host.uploaded(t); len(files) != 1 {
		t.Errorf("the host holds %v; want the arriving file alone", files)
	}
	if _, err := first.Write(make([]byte, 1<<20-100<<10)); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, first); got != http.StatusOK {
		t.Errorf("the first upload ended with %d; want 200", got)
	}
	// With the slot free, the same link uploads again.
	if _, err := link.Upload(context.Background(), 3, strings.NewReader("abc")); err != nil {
		t.Errorf("an upload after the first completed: %v", err)
	}
}

// Decision (verifier nit; ADR-0099 §3): a peer that is unpaired loses the upload it left, so
// a name paired again with another certificate cannot take it. The sweep that closes the
// peer's sockets drops it.
func TestUnpairedPeerLosesItsPendingUpload(t *testing.T) {
	host, _, link := pair(t)
	if _, err := link.Upload(context.Background(), 3, strings.NewReader("abc")); err != nil {
		t.Fatal(err)
	}
	if len(host.uploaded(t)) != 1 {
		t.Fatalf("setup: %v", host.uploaded(t))
	}
	host.cfg.setPeers()
	testtime.Eventually(t, 6*time.Second, "the unpaired peer's upload to be removed", func() bool { return len(host.uploaded(t)) == 0 })
}
