//go:build !droidyard_inputprobe

package device

import (
	"bytes"
	"io"
	"log"
	"net"
	"os"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// Decision (plan m2y step 1, K4): the shipped build carries no measurement code. A control
// write through the screen source and a packet through both Hub hooks write nothing to the
// daemon's log. Seen failing with a log call in inputprobe.go's hooks.
func TestProbeIsANoOpByDefault(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go io.Copy(io.Discard, server)
	s := &screenSource{control: client}
	if _, err := s.write([]byte{2, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	probePacket("scrcpy", screen.Packet{PTS: 1})
	probePacket("relayed", screen.Packet{PTS: 2})
	if logged.Len() != 0 {
		t.Errorf("the shipped build logged %q", logged.String())
	}
}
