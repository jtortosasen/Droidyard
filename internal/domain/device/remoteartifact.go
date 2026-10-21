package device

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// The artifacts of a remote device's screenshot and logs (plan m2t step 5; ADR-0100 §3): the
// host's result names an artifact in its store, which this daemon fetches over the peer port,
// checks, stores in its own store as untrusted and names by its own reference. The host's
// reference is never passed through: it means nothing here, and the bytes are another
// machine's.

// fetchTimeout bounds one artifact's fetch; a variable so a test is quick.
var fetchTimeout = 60 * time.Second

const (
	// remoteMaxEdge and remoteMaxPixels bound the screenshot of a remote device before it is
	// decoded: a small file can declare an image that takes gigabytes to decode. A phone's
	// screen is a few million pixels.
	remoteMaxEdge   = 16384
	remoteMaxPixels = 16 << 20
	// remoteWarningsMax and remoteWarningMax bound the warnings a host sends: how many, and
	// how long each is.
	remoteWarningsMax = 20
	remoteWarningMax  = 1 << 10
)

// hostArtifacts is the link as the fetch uses it: *hosting.Link.
type hostArtifacts interface {
	Artifact(ctx context.Context, id string, max int64) (hosting.Artifact, error)
}

// screenshotFetched completes a relayed device.screenshot: the host's artifact is fetched
// and checked as a PNG, and the result carries this daemon's own reference to it.
func (a *ADB) screenshotFetched(ctx context.Context, rt *remoteTarget, store *artifact.Store, r ScreenshotResult) (any, error) {
	return a.screenshotFrom(ctx, rt.link, rt, store, r)
}

func (a *ADB) screenshotFrom(ctx context.Context, f hostArtifacts, rt *remoteTarget, store *artifact.Store, r ScreenshotResult) (any, error) {
	id, ok := hosting.ArtifactID(r.Ref.Ref)
	if !ok {
		return nil, incompatible(rt, "it named a screenshot this version cannot fetch")
	}
	data, err := fetchArtifact(ctx, f, rt, id, "image/png", screencapCap, ErrScreenshotFailed, "screenshot")
	if err != nil {
		return nil, err
	}
	cfg, derr := png.DecodeConfig(bytes.NewReader(data))
	if derr != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > remoteMaxEdge || cfg.Height > remoteMaxEdge || cfg.Width*cfg.Height > remoteMaxPixels {
		return nil, screenshotRefused(rt, "it is not a PNG of a size this daemon decodes")
	}
	img, derr := checkPNG(data)
	if derr != nil {
		return nil, screenshotRefused(rt, "it is not a whole PNG ("+derr.Error()+")")
	}
	d, mt, iw, ih, ierr := artifact.EncodeInlineDecoded(data, "image/png", img, artifact.InlineMaxEdge)
	if ierr != nil {
		return nil, screenshotRefused(rt, "the PNG cannot be re-encoded")
	}
	ref, err := store.Put(artifact.Item{
		Data: data, MediaType: "image/png", Width: img.Bounds().Dx(), Height: img.Bounds().Dy(),
		Untrusted: true, Producer: "device.screenshot", Inline: &artifact.Inline{Data: d, MediaType: mt, Width: iw, Height: ih},
	})
	if err != nil {
		return nil, err
	}
	return ScreenshotResult{Ref: ref, Warnings: remoteWarnings(rt, r.Warnings)}, nil
}

// screenshotRefused is the screenshot the host served not being one this daemon takes.
func screenshotRefused(rt *remoteTarget, why string) *capability.Error {
	return capability.NewError(ErrScreenshotFailed, "the screenshot from the host "+rt.remote+" is not one this daemon accepts").
		WithCause("the screenshot's bytes were refused: " + why).
		WithNextStep("Retry device.screenshot. If it keeps failing, check the host's droidyard and that it runs the same version.")
}

// uiDumpFetched completes a relayed device.uiDump (ADR-0115 Decision 1): the host's document
// is fetched, checked to be a hierarchy this daemon reads and stored here as untrusted under
// this daemon's own reference. The counts are recomputed from the bytes fetched, never taken
// from the host's answer.
func (a *ADB) uiDumpFetched(ctx context.Context, rt *remoteTarget, store *artifact.Store, r UIDumpResult) (any, error) {
	return a.uiDumpFrom(ctx, rt.link, rt, store, r)
}

func (a *ADB) uiDumpFrom(ctx context.Context, f hostArtifacts, rt *remoteTarget, store *artifact.Store, r UIDumpResult) (any, error) {
	id, ok := hosting.ArtifactID(r.Dump.Ref)
	if !ok {
		return nil, incompatible(rt, "it named a UI dump this version cannot fetch")
	}
	data, err := fetchArtifact(ctx, f, rt, id, "application/xml", dumpOutputCap, ErrUIDumpFailed, "UI dump")
	if err != nil {
		return nil, err
	}
	// The parser's own message can quote the document, which is the host's: the cause is fixed.
	_, roots, perr := parseDump(data)
	if perr != nil {
		return nil, capability.NewError(ErrUIDumpFailed, "the UI dump from the host "+rt.remote+" is not one this daemon accepts").
			WithCause(uiDumpUnreadable).
			WithNextStep("Retry the call. If it keeps failing, check the host's droidyard and that it runs the same version.")
	}
	res := UIDumpResult{Device: rt.id, Warnings: remoteWarnings(rt, r.Warnings)}
	for _, root := range roots {
		res.Nodes += countNodes(root)
	}
	if len(roots) > 0 {
		res.Foreground = roots[0].pkg
	}
	ref, err := store.Put(artifact.Item{Data: data, MediaType: "application/xml", Untrusted: true, Producer: "device.uiDump"})
	if err != nil {
		return nil, err
	}
	res.Dump = ref
	return res, nil
}

// logsFetched completes a relayed device.logs: the lines and crashes are bounded as the
// local read bounds them, and the capture the host names is fetched and stored here.
func (a *ADB) logsFetched(ctx context.Context, rt *remoteTarget, store *artifact.Store, r LogsResult) (any, error) {
	return a.logsFrom(ctx, rt.link, rt, store, r)
}

func (a *ADB) logsFrom(ctx context.Context, f hostArtifacts, rt *remoteTarget, store *artifact.Store, r LogsResult) (any, error) {
	if len(r.Lines) > logsMaxLinesMax {
		r.Lines, r.Truncated = r.Lines[len(r.Lines)-logsMaxLinesMax:], true
	}
	if len(r.Crashes) > logsCrashesMax {
		r.Crashes = r.Crashes[len(r.Crashes)-logsCrashesMax:]
	}
	for i := range r.Lines {
		l := &r.Lines[i]
		l.Tag, l.Message = truncateUTF8(l.Tag, logsLineCap), truncateUTF8(l.Message, logsLineCap)
	}
	for i := range r.Crashes {
		c := &r.Crashes[i]
		c.Exception, c.Message = truncateUTF8(c.Exception, logsLineCap), truncateUTF8(c.Message, logsLineCap)
		c.Causes, c.Frames = boundedText(c.Causes, logsCrashesMax), boundedText(c.Frames, logsCrashesMax)
	}
	switch r.EmptyReason {
	case "", "noLines", "noKnownProcess", "filtered":
	default:
		r.EmptyReason = "" // not one of the published reasons
	}
	r.Warnings = remoteWarnings(rt, r.Warnings)
	if r.Capture == nil {
		return r, nil
	}
	id, ok := hosting.ArtifactID(r.Capture.Ref)
	if !ok {
		return nil, incompatible(rt, "it named a log capture this version cannot fetch")
	}
	data, err := fetchArtifact(ctx, f, rt, id, "text/plain", logsCaptureCap, ErrLogsFailed, "log capture")
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		data = []byte(strings.ToValidUTF8(string(data), "�"))
		if len(data) > logsCaptureCap { // each bad byte may have grown to three
			data = []byte(truncateUTF8(string(data), logsCaptureCap))
			r.Warnings = append(r.Warnings, "the capture from the remote "+rt.remote+" was not valid text and was cut to "+strconv.Itoa(logsCaptureCap>>20)+" MiB")
		}
	}
	ref, err := store.Put(artifact.Item{Data: data, MediaType: "text/plain", Untrusted: true, Producer: "device.logs"})
	if err != nil {
		return nil, err
	}
	r.Capture = &ref
	return r, nil
}

// boundedText is at most n entries of a list of device text, each cut as a log line is.
func boundedText(in []string, n int) []string {
	if len(in) > n {
		in = in[:n]
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = truncateUTF8(s, logsLineCap)
	}
	return out
}

// remoteWarnings is a host's warnings as this daemon passes them on: bounded, and with the
// device named by the id the caller used.
func remoteWarnings(rt *remoteTarget, in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(in), remoteWarningsMax))
	for _, w := range in[:min(len(in), remoteWarningsMax)] {
		out = append(out, rt.named(truncateUTF8(w, remoteWarningMax)))
	}
	return out
}

// fetchArtifact fetches the artifact id from the host, at most max bytes of it, and returns
// them if they are of the media type the capability's result promises. A failure is mapped
// into this daemon's contract (ADR-0100 §4), in its own words: nothing the host wrote is
// quoted. failed is the capability's own failure code, what (screenshot, log capture) names
// the artifact. A fetch changes nothing on the host, so every one of these is safe to repeat.
func fetchArtifact(ctx context.Context, f hostArtifacts, rt *remoteTarget, id, mediaType string, max int64, failed capability.ErrorCode, what string) ([]byte, error) {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	a, err := f.Artifact(fctx, id, max)
	if err != nil {
		return nil, fetchError(ctx, fctx, rt, err, failed, what)
	}
	if a.MediaType != mediaType {
		return nil, incompatible(rt, "it served the "+what+" as another type than its result says")
	}
	return a.Data, nil
}

// fetchError maps an artifact fetch that did not complete.
func fetchError(ctx, fctx context.Context, rt *remoteTarget, err error, failed capability.ErrorCode, what string) error {
	var refused *hosting.ErrArtifactRefused
	isRefused := errors.As(err, &refused)
	gone := func(cause string) error {
		return capability.NewError(failed, "the "+what+" from the host "+rt.remote+" could not be fetched").
			WithCause(cause).
			WithNextStep("Repeat the call. If it keeps failing, update droidyard on both machines to the same version.")
	}
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(fctx.Err(), context.DeadlineExceeded):
		return gone("fetching it took longer than " + fetchTimeout.String())
	case errors.Is(err, hosting.ErrNotConnected):
		return linkError(ctx, rt, err)
	case errors.Is(err, hosting.ErrArtifactReply), errors.Is(err, hosting.ErrArtifactID):
		return incompatible(rt, "it did not serve the "+what+" as this version expects")
	case errors.Is(err, hosting.ErrArtifactTooLarge):
		return gone("it is larger than this daemon accepts")
	case isRefused && (refused.Status == http.StatusNotFound || refused.Status == http.StatusGone):
		return gone("the host no longer holds it (status " + strconv.Itoa(refused.Status) + ")")
	case isRefused:
		return capability.NewError(ErrNotReady, "the host "+rt.remote+" did not serve the "+what).
			WithCause("it answered with status " + strconv.Itoa(refused.Status)).
			WithDetails(map[string]any{"state": "unreachable"}).
			WithNextStep("Retry in a moment; if it persists, look at the host's daemon.log.")
	}
	return capability.NewError(ErrNotReady, "the "+what+" from the host "+rt.remote+" could not be fetched").
		WithCause("the connection to the host failed, or its answer was not an HTTP response").
		WithDetails(map[string]any{"state": "unreachable"}).
		WithNextStep("Check that the host's droidyard is running and that the network between the two reaches it, then retry.")
}
