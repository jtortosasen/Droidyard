package hosting

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// ErrArtifactRefused is an artifact fetch the host answered with a status other than 200; the
// status is the whole of what is said of it, since the text is another machine's.
type ErrArtifactRefused struct{ Status int }

func (e *ErrArtifactRefused) Error() string {
	return fmt.Sprintf("the host answered the artifact fetch with status %d", e.Status)
}

var (
	// ErrArtifactTooLarge is an artifact over the bound the caller set, or a declared length
	// that cannot be right: nothing of it was kept.
	ErrArtifactTooLarge = errors.New("the host's artifact is over the size this daemon accepts")
	// ErrArtifactReply is a 200 answer that is not an artifact this version reads: an
	// identifier or a media type it does not know.
	ErrArtifactReply = errors.New("the host's answer to the artifact fetch is not an artifact")
	// ErrArtifactID is an identifier that is not the shape of one a daemon issues.
	ErrArtifactID = errors.New("not an artifact identifier")
)

// artifactIDRE is the shape of an ephemeral artifact's id: class, daemon epoch, counter and
// 48 random bits (internal/artifact). The id comes from the host, and becomes a URL path
// here, so only this shape is ever sent.
var artifactIDRE = regexp.MustCompile(`^e\.[A-Za-z0-9-]{1,32}\.[0-9]{1,19}\.[0-9a-f]{12}$`)

// ArtifactID is the id inside a reference a host named ("droidyard://artifact/<id>"), and
// whether the reference has the shape of an ephemeral artifact's.
func ArtifactID(ref string) (string, bool) {
	id, ok := strings.CutPrefix(ref, artifact.RefPrefix)
	return id, ok && artifactIDRE.MatchString(id)
}

// Artifact is what a host served for an artifact of this daemon's calls: the bytes as it
// stored them, the media type it declared and whether it marked them untrusted.
type Artifact struct {
	MediaType string
	Untrusted bool
	Data      []byte
}

// artifactTypes are the media types a host serves an artifact as (the peer route's rule).
var artifactTypes = map[string]bool{"image/png": true, "image/jpeg": true, "text/plain": true, "application/xml": true}

// Artifact fetches the bytes of the artifact id from the host (ADR-0099 §2), on its own
// pinned connection, which ends when ctx does. At most max bytes are kept: a declared length
// over it is refused before the body is read, and a body that outgrows it is cut and
// refused. A link that is down is ErrNotConnected and nothing is sent; a request that fails
// on the way is the transport's error, wrapped by dialError when the pin or the pairing is
// the cause; a host that refuses is ErrArtifactRefused.
func (l *Link) Artifact(ctx context.Context, id string, max int64) (Artifact, error) {
	if !artifactIDRE.MatchString(id) {
		return Artifact{}, ErrArtifactID
	}
	resp, done, err := l.peerGet(ctx, id, "")
	if err != nil {
		return Artifact{}, err
	}
	defer done()
	if resp.StatusCode != http.StatusOK {
		return Artifact{}, &ErrArtifactRefused{Status: resp.StatusCode}
	}
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !artifactTypes[mt] {
		return Artifact{}, ErrArtifactReply
	}
	if resp.ContentLength > max {
		return Artifact{}, ErrArtifactTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		if ctx.Err() != nil {
			return Artifact{}, ctx.Err()
		}
		return Artifact{}, fmt.Errorf("reading the host's artifact: %w", err)
	}
	if int64(len(data)) > max {
		return Artifact{}, ErrArtifactTooLarge
	}
	return Artifact{MediaType: mt, Untrusted: resp.Header.Get("X-Droidyard-Untrusted") == "true", Data: data}, nil
}

// peerGet is a GET of the host's artifact route for id (the path segment, already checked by
// the caller), on a pinned connection of its own, with rng as the Range header when it is not
// empty. done releases the connection and the body. A link that is down is ErrNotConnected; a
// request that fails on the way is the transport's error, wrapped by dialError when the pin or
// the pairing is the cause.
func (l *Link) peerGet(ctx context.Context, id, rng string) (resp *http.Response, done func(), err error) {
	if !l.Connected() {
		return nil, nil, ErrNotConnected
	}
	client := l.pinnedClient()
	addr := l.current()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+capability.PeerArtifactsPath+url.PathEscape(id), nil)
	if err != nil {
		client.CloseIdleConnections()
		return nil, nil, err
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err = client.Do(req)
	if err != nil {
		client.CloseIdleConnections()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, dialError(l.remoteAt(addr), err)
	}
	return resp, func() { resp.Body.Close(); client.CloseIdleConnections() }, nil
}

// contentRangeRE is a 206's Content-Range as ArtifactRange reads it: exactly `bytes a-b/total`,
// with nothing after the total.
var contentRangeRE = regexp.MustCompile(`^bytes ([0-9]{1,18})-([0-9]{1,18})/([0-9]{1,18})$`)

// ErrArtifactRange is a ranged fetch the host answered with something other than the range
// asked for: nothing of it is kept as the range.
var ErrArtifactRange = errors.New("the host's answer is not the range asked for")

// ArtifactRange fetches bytes [off, off+n) of the finished timeline of the recording id, the
// one the host's peer route serves a peer of its own (ADR-0115 Decision 5), and writes them to
// w as they arrive. A host's answer is only a 206 of this timeline's media type whose
// Content-Range starts at off, ends within the range and agrees with the body's length; the
// total size it names is returned, so the caller bounds the next range by it. Whatever was
// written before an error is a correct prefix of the range, so a caller that kept it asks for
// the rest from off+written. A host that refuses is ErrArtifactRefused, whose status says
// whether the recording still runs (409) or is gone (404, 410). id is checked to be a
// recording id before it becomes a URL path.
func (l *Link) ArtifactRange(ctx context.Context, id string, off, n int64, w io.Writer) (written, total int64, err error) {
	if !artifact.ValidTimelineID(id) {
		return 0, 0, ErrArtifactID
	}
	if off < 0 || n <= 0 {
		return 0, 0, ErrArtifactRange
	}
	resp, done, err := l.peerGet(ctx, "r."+id, fmt.Sprintf("bytes=%d-%d", off, off+n-1))
	if err != nil {
		return 0, 0, err
	}
	defer done()
	if resp.StatusCode != http.StatusPartialContent {
		if resp.StatusCode == http.StatusOK {
			return 0, 0, ErrArtifactRange // the host did not honour the Range
		}
		return 0, 0, &ErrArtifactRefused{Status: resp.StatusCode}
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != artifact.TimelineMediaType {
		return 0, 0, ErrArtifactReply
	}
	m := contentRangeRE.FindStringSubmatch(resp.Header.Get("Content-Range"))
	if m == nil {
		return 0, 0, ErrArtifactRange
	}
	first, err1 := strconv.ParseInt(m[1], 10, 64)
	last, err2 := strconv.ParseInt(m[2], 10, 64)
	total, err3 := strconv.ParseInt(m[3], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, ErrArtifactRange
	}
	if first != off || last < first || last >= off+n || last >= total {
		return 0, 0, ErrArtifactRange
	}
	want := last - first + 1
	if resp.ContentLength != want {
		return 0, 0, ErrArtifactRange
	}
	written, err = io.Copy(w, io.LimitReader(resp.Body, want))
	switch {
	case err == nil && written != want:
		err = io.ErrUnexpectedEOF
	case err != nil && ctx.Err() != nil:
		err = ctx.Err()
	}
	if err != nil {
		return written, total, fmt.Errorf("reading the host's timeline: %w", err)
	}
	return written, total, nil
}
