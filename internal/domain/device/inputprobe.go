//go:build !droidyard_inputprobe

package device

import "github.com/jtortosasen/droidyard/internal/domain/device/screen"

// The input probe's hooks, empty in the shipped build (plan m2y step 1, K4): the product
// carries no measurement code. The droidyard_inputprobe build tag replaces them with
// inputprobe_tagged.go, which logs one line per control write and per Hub push. They must
// stay empty: TestProbeIsANoOpByDefault fails if this build logs.

// probeWrite is called with a control message after it was written to the control socket.
func probeWrite([]byte) {}

// probePacket is called with the role of a source ("scrcpy" or "relayed") and a packet
// just pushed into its Hub.
func probePacket(string, screen.Packet) {}
