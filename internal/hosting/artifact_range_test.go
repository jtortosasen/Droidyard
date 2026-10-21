package hosting

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

const rangeTestID = "20261021T100000Z-0a1b2c3d"

// rangeLink is a link to a fake host serving handler on the artifact route over real TLS with
// the host's pinned identity; hits counts the requests that reached it.
func rangeLink(t *testing.T, handler http.HandlerFunc) (l *Link, hits *atomic.Int32) {
	t.Helper()
	hits = new(atomic.Int32)
	hostID, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	me, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{hostID.Certificate()}, ClientAuth: tls.RequireAnyClientCert}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	l = newLink(policy.Remote{Name: "qa", Address: srv.Listener.Addr().String(), Fingerprint: hostID.Fingerprint()}, me, "0.0.0", io.Discard)
	l.connected = true
	return l, hits
}

// timelineHandler serves content as the peer route does a finished timeline: by Range, as
// application/x-ndjson.
func timelineHandler(content []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != capability.PeerArtifactsPath+"r."+rangeTestID {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(content))
	}
}

// Decision (ADR-0115 Decision 5, the copy's resume): a ranged fetch of a recording's timeline
// writes exactly the bytes of the range it asked for and reports the total size the host named,
// so the next range is bounded by it. Seen failing with the range's offset left out of the
// request (the second range returned the first bytes of the file again).
func TestArtifactRangeWritesTheRange(t *testing.T) {
	content := []byte("0123456789abcdefghij\n")
	l, _ := rangeLink(t, timelineHandler(content))
	var buf bytes.Buffer
	n, total, err := l.ArtifactRange(t.Context(), rangeTestID, 5, 10, &buf)
	if err != nil || n != 10 || total != int64(len(content)) || buf.String() != "56789abcde" {
		t.Fatalf("ArtifactRange(5, 10) = %d, %d, %v, %q; want 10 bytes %q of %d", n, total, err, buf.String(), "56789abcde", len(content))
	}
	// A range past the end of the file is clipped to it by the host; what arrives is the tail.
	buf.Reset()
	n, total, err = l.ArtifactRange(t.Context(), rangeTestID, 15, 100, &buf)
	if err != nil || n != 6 || total != int64(len(content)) || buf.String() != "fghij\n" {
		t.Fatalf("ArtifactRange(15, 100) = %d, %d, %v, %q; want the tail", n, total, err, buf.String())
	}
}

// Decision (the copy must never take a wrong answer for the range): a host that does not honour
// the Range (200 with the whole file), names another range, serves another type, claims a length
// that is not the body's, or refuses is an error, and whatever was written before a body that
// ends early is a correct prefix of the range, so a copy resumes after it. Seen failing with the
// 200 accepted as the range (the copy appended the whole file at the offset).
func TestArtifactRangeRefusesWhatIsNotTheRange(t *testing.T) {
	content := []byte("0123456789abcdefghij\n")
	cases := []struct {
		name    string
		handler http.HandlerFunc
		check   func(t *testing.T, n int64, err error)
	}{
		{"a host that ignores the Range", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Write(content)
		}, func(t *testing.T, n int64, err error) {
			if !errors.Is(err, ErrArtifactRange) || n != 0 {
				t.Errorf("%d, %v; want ErrArtifactRange and nothing written", n, err)
			}
		}},
		{"another range than asked", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Content-Range", "bytes 2-11/21")
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[2:12])
		}, func(t *testing.T, n int64, err error) {
			if !errors.Is(err, ErrArtifactRange) || n != 0 {
				t.Errorf("%d, %v; want ErrArtifactRange and nothing written", n, err)
			}
		}},
		{"a Content-Range with something after the total", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Content-Range", "bytes 3-12/21 and more")
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[3:13])
		}, func(t *testing.T, n int64, err error) {
			if !errors.Is(err, ErrArtifactRange) || n != 0 {
				t.Errorf("%d, %v; want ErrArtifactRange and nothing written", n, err)
			}
		}},
		{"another media type", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(content))
		}, func(t *testing.T, n int64, err error) {
			if !errors.Is(err, ErrArtifactReply) || n != 0 {
				t.Errorf("%d, %v; want ErrArtifactReply and nothing written", n, err)
			}
		}},
		{"a recording that still runs", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "the artifact is still being written", http.StatusConflict)
		}, func(t *testing.T, n int64, err error) {
			var refused *ErrArtifactRefused
			if !errors.As(err, &refused) || refused.Status != http.StatusConflict {
				t.Errorf("%v; want ErrArtifactRefused 409", err)
			}
		}},
		{"a range the file cannot satisfy", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		}, func(t *testing.T, n int64, err error) {
			var refused *ErrArtifactRefused
			if !errors.As(err, &refused) || refused.Status != http.StatusRequestedRangeNotSatisfiable {
				t.Errorf("%v; want ErrArtifactRefused 416", err)
			}
		}},
		{"a body that ends early", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Content-Range", "bytes 3-12/21")
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[3:7])
		}, func(t *testing.T, n int64, err error) {
			if err == nil || n != 4 {
				t.Errorf("%d, %v; want an error and the 4 bytes that arrived reported as written", n, err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, _ := rangeLink(t, c.handler)
			var buf bytes.Buffer
			n, _, err := l.ArtifactRange(t.Context(), rangeTestID, 3, 10, &buf)
			c.check(t, n, err)
			if int64(buf.Len()) != n {
				t.Errorf("%d bytes written to the writer, %d reported", buf.Len(), n)
			}
		})
	}
}

// Decision (the id becomes a URL path only in the shape of a recording id; a link that is down
// sends nothing): ArtifactRange of a reference that is not a recording id, of an ephemeral
// artifact's id, or with a range that is empty, sends no request; a link that is down is
// ErrNotConnected. Seen failing with the id taken as it came (a path with `..` reached the host).
func TestArtifactRangeSendsNothingForWhatIsNotAnId(t *testing.T) {
	l, hits := rangeLink(t, timelineHandler([]byte("x\n")))
	for _, id := range []string{"", "../x", "r." + rangeTestID, "e.1.2.aaaaaaaaaaaa", rangeTestID + "/..", strings.ToUpper(rangeTestID)} {
		if _, _, err := l.ArtifactRange(t.Context(), id, 0, 1, io.Discard); !errors.Is(err, ErrArtifactID) {
			t.Errorf("ArtifactRange(%q) = %v; want ErrArtifactID", id, err)
		}
	}
	for _, c := range [][2]int64{{-1, 5}, {0, 0}, {0, -3}} {
		if _, _, err := l.ArtifactRange(t.Context(), rangeTestID, c[0], c[1], io.Discard); !errors.Is(err, ErrArtifactRange) {
			t.Errorf("ArtifactRange(off %d, n %d) = %v; want ErrArtifactRange", c[0], c[1], err)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("%d requests reached the host for ids and ranges that are not valid", got)
	}
	l.connected = false
	if _, _, err := l.ArtifactRange(t.Context(), rangeTestID, 0, 1, io.Discard); !errors.Is(err, ErrNotConnected) {
		t.Errorf("a link that is down: %v; want ErrNotConnected", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("%d requests were sent on a link that is down", got)
	}
}
