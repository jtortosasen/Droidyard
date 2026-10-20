package peer_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The artifact route of the peer listener (ADR-0099 §2), over real TLS on loopback. The
// listener asks its ArtifactReader and translates the answer; who owns what is the reader's.

const (
	ownedID   = "e.d-0a1b2c3d.1.0123456789ab"
	foreignID = "e.d-0a1b2c3d.2.0123456789ab"
	goneID    = "e.d-0a1b2c3d.3.0123456789ab"
)

// pairServing is pair with the host's listener serving artifacts from read.
func pairServing(t *testing.T, read peer.ArtifactReader) (host, client *side, link *hosting.Link) {
	t.Helper()
	host, client = newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	host.artifacts = read
	addr, _ := host.listen(t, "127.0.0.1:0")
	client.cfg.remotes = []policy.Remote{{Name: "qa-lab", Address: addr, Fingerprint: host.host.Identity().Fingerprint()}}
	client.host.Start()
	t.Cleanup(client.host.Stop)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the link to come up", func() bool { return client.host.Remote("qa-lab").Connected() })
	return host, client, client.host.Remote("qa-lab")
}

// Decision (ADR-0099 §2, plan m2t step 5): the route serves the bytes of the artifact the
// reader gives the paired peer, with its media type and the untrusted mark, and asks the
// reader with the peer's configured name (the certificate's, not anything the request
// says). An id the reader does not give is 404 whatever the reason it has, an expired one
// 410, and the route answers nothing of its own about who owns what.
func TestArtifactRouteServesWhatTheReaderGives(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	png := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1000)
	host, _, link := pairServing(t, func(name, id string) (peer.Artifact, error) {
		mu.Lock()
		asked = append(asked, name+" "+id)
		mu.Unlock()
		switch id {
		case ownedID:
			return peer.Artifact{MediaType: "image/png", Untrusted: true, Data: png}, nil
		case goneID:
			return peer.Artifact{}, capability.NewError(capability.ErrArtifactExpired, "gone")
		case "e.d-0a1b2c3d.4.0123456789ab":
			return peer.Artifact{MediaType: "text/plain", Data: []byte("a log\n")}, nil
		case "e.d-0a1b2c3d.5.0123456789ab":
			return peer.Artifact{MediaType: "application/zip", Data: []byte("PK")}, nil
		}
		return peer.Artifact{}, capability.NewError(capability.ErrArtifactNotFound, "no")
	})
	_ = host

	got, err := link.Artifact(context.Background(), ownedID, 1<<20)
	if err != nil || got.MediaType != "image/png" || !got.Untrusted || !bytes.Equal(got.Data, png) {
		t.Fatalf("the owner's artifact: %q untrusted %v, %d bytes, %v; want image/png untrusted and the %d bytes", got.MediaType, got.Untrusted, len(got.Data), err, len(png))
	}
	got, err = link.Artifact(context.Background(), "e.d-0a1b2c3d.4.0123456789ab", 1<<20)
	if err != nil || got.MediaType != "text/plain" || got.Untrusted || string(got.Data) != "a log\n" {
		t.Errorf("a text artifact: %+v, %v; want text/plain, trusted as the reader said, its bytes", got, err)
	}
	mu.Lock()
	if len(asked) != 2 || asked[0] != "anna "+ownedID {
		t.Errorf("the reader was asked %v; want the peer's name and the id", asked)
	}
	mu.Unlock()

	for _, c := range []struct {
		name, id string
		status   int
	}{
		{"another owner's artifact", foreignID, http.StatusNotFound},
		{"an expired one", goneID, http.StatusGone},
	} {
		_, err := link.Artifact(context.Background(), c.id, 1<<20)
		var refused *hosting.ErrArtifactRefused
		if !errors.As(err, &refused) || refused.Status != c.status {
			t.Errorf("%s: %v; want status %d", c.name, err, c.status)
		}
	}
	// A media type the route does not serve is not served, whatever the reader holds.
	if _, err := link.Artifact(context.Background(), "e.d-0a1b2c3d.5.0123456789ab", 1<<20); err == nil {
		t.Error("an artifact of an unserved media type was served")
	}
}

// Decision (plan m2t step 5, "bytes from the host are bounded before they are buffered"):
// the fetch refuses an artifact over the caller's bound, by its declared length before a
// byte of the body is read, and keeps none of it; a malformed id is never sent.
func TestArtifactFetchIsBounded(t *testing.T) {
	big := bytes.Repeat([]byte{'x'}, 100<<10)
	_, _, link := pairServing(t, func(name, id string) (peer.Artifact, error) {
		return peer.Artifact{MediaType: "text/plain", Data: big}, nil
	})
	if _, err := link.Artifact(context.Background(), ownedID, int64(len(big))-1); !errors.Is(err, hosting.ErrArtifactTooLarge) {
		t.Errorf("an artifact one byte over the bound: %v; want ErrArtifactTooLarge", err)
	}
	if got, err := link.Artifact(context.Background(), ownedID, int64(len(big))); err != nil || len(got.Data) != len(big) {
		t.Errorf("an artifact exactly at the bound: %d bytes, %v", len(got.Data), err)
	}
	for _, bad := range []string{"", "../x", "d.0123456789abcdef.1", "e.d-0a1b2c3d.1.0123456789ab/../../ws", "e.d-0a1b2c3d.1.0123456789a", "e.d-0a1b2c3d.1.0123456789AB", strings.Repeat("e", 80)} {
		if _, err := link.Artifact(context.Background(), bad, 1<<20); !errors.Is(err, hosting.ErrArtifactID) {
			t.Errorf("id %q: %v; want ErrArtifactID, nothing sent", bad, err)
		}
	}
}

// Decision (ADR-0098 §3): the artifact route is behind the same checks as the others: a
// request with an Origin is refused, only GET is served, and a listener given no reader
// answers 404 to every id.
func TestArtifactRouteChecks(t *testing.T) {
	host, client, _ := pairServing(t, func(string, string) (peer.Artifact, error) {
		return peer.Artifact{MediaType: "text/plain", Data: []byte("x")}, nil
	})
	addr := host.host.Health().Hosting.Listen
	cfg := hosting.PinnedConfig(client.host.Identity(), func(fp string) bool { return fp == host.host.Identity().Fingerprint() })
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	url := "https://" + addr + capability.PeerArtifactsPath + ownedID

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a GET with an Origin: %d; want 403", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, url, strings.NewReader("x"))
	resp, err = hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("a POST of the artifact route: %d; want 405", resp.StatusCode)
	}
	resp, err = hc.Get("https://" + addr + capability.PeerArtifactsPath + "a/b")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an id with a slash: %d; want 404", resp.StatusCode)
	}

	nohost, noclient, nolink := pair(t) // a listener with no reader
	_ = nohost
	_ = noclient
	var refused *hosting.ErrArtifactRefused
	if _, err := nolink.Artifact(context.Background(), ownedID, 1<<20); !errors.As(err, &refused) || refused.Status != http.StatusNotFound {
		t.Errorf("a listener with no artifact reader: %v; want 404", err)
	}
}

// Decision (ADR-0098 §3, plan m2t step 5): a peer unpaired while an artifact is being sent to
// it loses the connection within the revocation sweep, whatever the peer does: the reader
// here stops reading the body, so the host's write blocks, and the sweep is the only thing
// that ends it (the write bound is two minutes). Part of the body, and not all of it, ever
// reaches the peer. Seen failing with the artifact request left out of the sweep: the
// connection stayed open past the sweep.
func TestArtifactDownloadEndsWhenThePeerIsUnpaired(t *testing.T) {
	big := make([]byte, 64<<20)
	host, client, _ := pairServing(t, func(string, string) (peer.Artifact, error) {
		return peer.Artifact{MediaType: "text/plain", Data: big}, nil
	})
	addr := host.host.Health().Hosting.Listen
	conn := dialRaw(t, host, client, addr)
	if _, err := io.WriteString(conn, "GET "+capability.PeerArtifactsPath+ownedID+" HTTP/1.1\r\nHost: host\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusOK || resp.ContentLength != int64(len(big)) {
		t.Fatalf("the head of the download: %v, %+v", err, resp)
	}
	// The peer reads no more: the body fills the connection's buffers and the host's write blocks.
	time.Sleep(500 * time.Millisecond)
	host.cfg.setPeers()
	start := time.Now()
	// The host is to close the connection by itself, with the peer reading nothing meanwhile.
	time.Sleep(4 * time.Second)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := io.Copy(io.Discard, resp.Body)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("the connection was still open %v after the peer was unpaired (the sweep is 2 s); the peer read %d bytes", time.Since(start), n)
	}
	if err == nil || n >= int64(len(big)) {
		t.Errorf("the peer read %d of %d bytes (%v); want the body cut short by the host", n, len(big), err)
	}
}

// Decision (ADR-0098 §3): the artifact request is in the revocation sweep from its start, not
// from when the reader answers: a peer unpaired while the reader is still working is cut off,
// and nothing is written to it when the reader finally answers. Seen failing with the request
// joining the sweep only after the reader call: the peer got the whole artifact.
func TestArtifactRequestUnpairedWhileTheReaderWorks(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	host, client, _ := pairServing(t, func(string, string) (peer.Artifact, error) {
		entered <- struct{}{}
		<-gate
		return peer.Artifact{MediaType: "text/plain", Data: []byte("the artifact")}, nil
	})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	conn := dialRaw(t, host, client, host.host.Health().Hosting.Listen)
	if _, err := io.WriteString(conn, "GET "+capability.PeerArtifactsPath+ownedID+" HTTP/1.1\r\nHost: host\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	<-entered
	host.cfg.setPeers()
	time.Sleep(4 * time.Second) // two sweeps
	open()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK || strings.Contains(string(body), "the artifact") {
			t.Errorf("an unpaired peer got status %d and %q", resp.StatusCode, body)
		}
	}
}
