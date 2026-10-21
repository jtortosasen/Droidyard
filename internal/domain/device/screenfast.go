package device

import (
	"context"
	"slices"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The fast preset (ADR-0118 §4): the options of a source the host started for a peer whose
// hello said fast, a lighter stream for a slow link. The values are working defaults, to be
// replaced by the largest preset whose Lag on the slow link stays under 1 s (plan m2y step 8).
const (
	screenFastMaxSize = "max_size=1024"
	screenFastBitRate = "video_bit_rate=2000000"
	screenFastMaxFPS  = "max_fps=30"
)

// screenFastOptions is screenServerOptions with the three values the preset lowers.
func screenFastOptions() []string {
	out := slices.Clone(screenServerOptions)
	for i, o := range out {
		switch {
		case strings.HasPrefix(o, "max_size="):
			out[i] = screenFastMaxSize
		case strings.HasPrefix(o, "video_bit_rate="):
			out[i] = screenFastBitRate
		case strings.HasPrefix(o, "max_fps="):
			out[i] = screenFastMaxFPS
		}
	}
	return out
}

// serverOptions is what this source's server is started with, fixed for the source's life.
func (s *screenSource) serverOptions() []string {
	if s.fast {
		return screenFastOptions()
	}
	return screenServerOptions
}

// acquire is acquireWith for a start that is not for a fast peer.
func (ss *screenSources) acquire(ctx context.Context, a *ADB, t *target) (*screenSource, func(), error) {
	return ss.acquireWith(ctx, a, t, false)
}

// SetPeerFast gives the ADB the lookup of whether a paired peer's hello said fast
// (ADR-0118 §4), set once at wiring; nil means no peer is.
func (a *ADB) SetPeerFast(f func(peer string) bool) { a.peerFast = f }

// callFast says whether the call is a paired peer's whose link hello said fast.
func (a *ADB) callFast(call *capability.Call) bool {
	return a.peerFast != nil && call.Origin.Client == capability.ClientPeer && a.peerFast(call.Origin.Name)
}
