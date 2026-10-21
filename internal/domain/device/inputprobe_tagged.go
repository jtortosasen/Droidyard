//go:build droidyard_inputprobe

package device

import (
	"fmt"
	"log"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// This build was compiled with -tags droidyard_inputprobe (plan m2y step 1, K4): it logs a
// line per control write and per Hub push for the latency measurements (T1 and Lag). The
// times are the wall clock in Unix nanoseconds, comparable between two daemons on one
// machine. Nothing here changes what the daemon does.

// probeWrite logs `probe write <action> <unix ns>` after a control message was written.
func probeWrite(b []byte) {
	log.Printf("probe write %s %d", probeAction(b), time.Now().UnixNano())
}

// probePacket logs `probe packet <role> <pts> <unix ns>` for a packet just pushed.
func probePacket(role string, p screen.Packet) {
	log.Printf("probe packet %s %d %d", role, p.PTS, time.Now().UnixNano())
}

// probeAction names a scrcpy control message: a touch by its Android action, a key as
// keydown or keyup, the others by their type.
func probeAction(b []byte) string {
	if len(b) == 0 {
		return "empty"
	}
	switch b[0] {
	case 0:
		if len(b) > 1 && b[1] == 1 {
			return "keyup"
		}
		return "keydown"
	case 2:
		if len(b) > 1 {
			switch b[1] {
			case 0:
				return "down"
			case 1:
				return "up"
			case 2:
				return "move"
			case 3:
				return "cancel"
			}
		}
		return "touch"
	case 3:
		return "scroll"
	case 17:
		return "reset"
	}
	return fmt.Sprintf("type%d", b[0])
}
