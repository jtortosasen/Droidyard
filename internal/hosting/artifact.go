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
var artifactTypes = map[string]bool{"image/png": true, "image/jpeg": true, "text/plain": true}

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
	if !l.Connected() {
		return Artifact{}, ErrNotConnected
	}
	client := l.pinnedClient()
	defer client.CloseIdleConnections()
	addr := l.current()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+capability.PeerArtifactsPath+url.PathEscape(id), nil)
	if err != nil {
		return Artifact{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Artifact{}, ctx.Err()
		}
		return Artifact{}, dialError(l.remoteAt(addr), err)
	}
	defer resp.Body.Close()
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
