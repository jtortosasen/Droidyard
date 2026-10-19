package hosting

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// ErrUploadRefused is an upload the host answered with a status other than 200; the status
// is the whole of what is said of it, since the text is another machine's.
type ErrUploadRefused struct{ Status int }

func (e *ErrUploadRefused) Error() string {
	return fmt.Sprintf("the host answered the upload with status %d", e.Status)
}

// ErrUploadReply is a 200 answer that is not an upload's receipt.
var ErrUploadReply = fmt.Errorf("the host's answer to the upload is not a receipt")

var uploadSumRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// UploadReceipt is what a host stored an upload as: the id device.installUpload names it by
// and the SHA-256 of what it stored. Both are checked for shape here, since the caller sends
// the id back.
type UploadReceipt struct {
	ID     string
	SHA256 string
}

// Upload sends size bytes of body to the host as an APK (ADR-0099 §3): a PUT on its own
// pinned connection, with the length declared, which ends when ctx does. A link that is down
// is ErrNotConnected and nothing is sent; a request that fails on the way is the
// transport's error, wrapped by dialError when the pin or the pairing is the cause; a host
// that refuses is ErrUploadRefused.
func (l *Link) Upload(ctx context.Context, size int64, body io.Reader) (UploadReceipt, error) {
	if !l.Connected() {
		return UploadReceipt{}, ErrNotConnected
	}
	client := l.pinnedClient()
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "https://"+l.remote.Address+capability.PeerUploadPath, io.NopCloser(body))
	if err != nil {
		return UploadReceipt{}, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/vnd.android.package-archive")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return UploadReceipt{}, ctx.Err()
		}
		return UploadReceipt{}, dialError(l.remote, err)
	}
	defer resp.Body.Close()
	if ctx.Err() != nil {
		// Cancelled while the host answered the body it saw cut short: the caller's cancel
		// is the answer, whatever status raced it here (CI's race job, after 2t's close).
		return UploadReceipt{}, ctx.Err()
	}
	if resp.StatusCode != http.StatusOK {
		return UploadReceipt{}, &ErrUploadRefused{Status: resp.StatusCode}
	}
	var r capability.PeerUploadReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&r); err != nil || !ValidUploadID(r.Upload) || !uploadSumRE.MatchString(r.SHA256) {
		return UploadReceipt{}, ErrUploadReply
	}
	return UploadReceipt{ID: r.Upload, SHA256: r.SHA256}, nil
}
