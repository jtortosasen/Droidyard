package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// uploadTimeout bounds the upload of a build to a host, apart from the host's own bound on
// the install, which then counts the install alone (plan m2t step 4). A variable so a test
// is quick.
var uploadTimeout = 10 * time.Minute

// installRemote is device.install on a device a host lends (ADR-0100 §3): the build was
// resolved here, as for a local device, and its APK is uploaded to the host and installed
// there by device.installUpload. What comes back is the build's own facts. The host stores
// the bytes it receives and reports their SHA-256, which must be the digest of the bytes
// sent; the installed file's digest, which it reports again, must be the same.
func (a *ADB) installRemote(ctx context.Context, call *capability.Call, rt *remoteTarget, out build.ResolvedOutput) (InstallResult, error) {
	// Whatever comes of this call involves another machine's answers.
	yes := true
	call.Untrusted = &yes
	f, err := os.Open(out.Path)
	if err != nil {
		return InstallResult{}, installFailed("the build's APK " + out.APK + " could not be opened to upload it")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return InstallResult{}, installFailed("the build's APK " + out.APK + " could not be read to upload it")
	}
	size := fi.Size()
	if size == 0 || size > capability.PeerMaxUpload {
		return InstallResult{}, installFailed("the build's APK " + out.APK + " is " + strconv.FormatInt(size, 10) + " bytes; a host takes an APK of at most " + strconv.FormatInt(capability.PeerMaxUpload, 10))
	}
	if !rt.link.Connected() {
		return InstallResult{}, linkError(ctx, rt, hosting.ErrNotConnected)
	}
	// A host keeps one upload of this daemon at a time: a second install to the same host
	// waits for the first to be installed, or its upload would replace the file the host is
	// about to install.
	release, err := a.holdRemote(ctx, rt.remote)
	if err != nil {
		return InstallResult{}, err
	}
	defer release()

	uctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	sum := sha256.New()
	rec, err := rt.link.Upload(uctx, size, io.TeeReader(f, sum))
	if err != nil {
		return InstallResult{}, uploadError(ctx, uctx, rt, err)
	}
	sent := hex.EncodeToString(sum.Sum(nil))
	if rec.SHA256 != sent {
		return InstallResult{}, installFailed("the host stored a file whose SHA-256 is not the build's APK's; nothing was installed")
	}

	params, _ := json.Marshal(InstallUploadParams{Device: rt.serial, Upload: rec.ID})
	res, err := a.relayParams(ctx, call, "device.installUpload", params, InstallUploadResult{}, installErrors, rt)
	if err != nil {
		return InstallResult{}, err
	}
	if res.(InstallUploadResult).SHA256 != sent {
		return InstallResult{}, installFailed("the host installed a file whose SHA-256 is not the build's APK's")
	}
	return InstallResult{ApplicationID: out.ApplicationID, VersionCode: out.VersionCode, APK: out.APK}, nil
}

// holdRemote takes the remote's slot, waiting for it while ctx lives.
func (a *ADB) holdRemote(ctx context.Context, remote string) (release func(), err error) {
	a.upMu.Lock()
	if a.upSlots == nil {
		a.upSlots = map[string]chan struct{}{}
	}
	slot := a.upSlots[remote]
	if slot == nil {
		slot = make(chan struct{}, 1)
		a.upSlots[remote] = slot
	}
	a.upMu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// uploadError maps an upload that did not complete (ADR-0100 §4), in this daemon's words:
// the caller's own context ending is that, and nothing the host wrote is quoted, since a
// transport error can carry the host's bytes. An upload installs nothing, so every one of
// these is safe to repeat. Only an answer this version cannot read (404, 405, a receipt
// that does not parse) is "incompatible", which ADR-0100 §4 reserves for that.
func uploadError(ctx, uctx context.Context, rt *remoteTarget, err error) error {
	var refused *hosting.ErrUploadRefused
	isRefused := errors.As(err, &refused)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(uctx.Err(), context.DeadlineExceeded):
		return installFailed("uploading the APK to the host " + rt.remote + " took longer than " + uploadTimeout.String() + "; nothing was installed")
	case errors.Is(err, hosting.ErrNotConnected):
		return linkError(ctx, rt, err)
	case errors.Is(err, hosting.ErrUploadReply), isRefused && (refused.Status == http.StatusNotFound || refused.Status == http.StatusMethodNotAllowed):
		what := "it did not answer the upload as this version expects"
		if isRefused {
			what = "it answered the upload with status " + strconv.Itoa(refused.Status)
		}
		return capability.NewError(ErrNotReady, "the host "+rt.remote+" did not take the upload").
			WithCause(what).
			WithDetails(map[string]any{"state": "incompatible"}).
			WithNextStep("Update droidyard on both machines to the same version. Nothing was installed.")
	case isRefused && refused.Status == http.StatusRequestEntityTooLarge:
		return installFailed("the host " + rt.remote + " takes no APK this large; nothing was installed")
	case isRefused && refused.Status == http.StatusBadRequest:
		return installFailed("the host " + rt.remote + " refused the upload as malformed; nothing was installed")
	case isRefused:
		// 403, 409, 500 and the rest: the host did not take it now.
		return capability.NewError(ErrNotReady, "the host "+rt.remote+" did not take the upload").
			WithCause("it answered the upload with status " + strconv.Itoa(refused.Status)).
			WithDetails(map[string]any{"state": "unreachable"}).
			WithNextStep("Retry device.install in a moment; if it persists, look at the host's daemon.log. Nothing was installed.")
	}
	return capability.NewError(ErrNotReady, "the upload to the host "+rt.remote+" did not finish").
		WithCause("the connection to the host failed, or the host's answer was not an HTTP response").
		WithDetails(map[string]any{"state": "unreachable"}).
		WithNextStep("Check that the host's droidyard is running and that the network between the two reaches it, then retry device.install. Nothing was installed.")
}
