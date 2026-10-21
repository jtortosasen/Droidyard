//go:build droidyard_inputprobe

package device

import (
	"bytes"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// Decision (plan m2y step 1, K4): the tagged build logs `probe write <action> <ns>` after
// each control write that succeeded, naming the touch action, and `probe packet <role>
// <pts> <ns>` per push. Run with `go test -tags droidyard_inputprobe`.
func TestInputProbeLogsWritesAndPackets(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go io.Copy(io.Discard, server)
	s := &screenSource{control: client}
	for _, action := range []byte{0, 1, 2, 3} {
		if _, err := s.write([]byte{2, action}); err != nil {
			t.Fatal(err)
		}
	}
	probePacket("relayed", screen.Packet{PTS: 42})
	out := logged.String()
	for _, want := range []string{"probe write down ", "probe write up ", "probe write move ", "probe write cancel ", "probe packet relayed 42 "} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}
