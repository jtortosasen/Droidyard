//go:build darwin || linux

package peer

import (
	"net"
	"testing"
)

// Decision (ADR-0118 §3): every connection the peer listener accepts has TCP_NOTSENT_LOWAT set
// to 64 KiB, so the kernel holds little of a slow link's backlog. The option is read back from
// an accepted connection. (That it helps is UNVERIFIED, step 1 §S7; this pins only that it is
// set.) Seen failing with the setsockopt removed from Accept: the option read 0.
func TestPeerListenerSetsNotSentLowat(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	l := &joinListener{Listener: ln}
	go func() {
		if c, err := net.Dial("tcp", ln.Addr().String()); err == nil {
			defer c.Close()
			buf := make([]byte, 1)
			c.Read(buf)
		}
	}()
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tc, ok := c.(*joinConn).Conn.(*net.TCPConn)
	if !ok {
		t.Fatalf("the accepted connection wraps %T, not a TCP connection", c.(*joinConn).Conn)
	}
	got, err := getNotsentLowat(tc)
	if err != nil {
		t.Fatal(err)
	}
	if got != notsentLowat {
		t.Errorf("TCP_NOTSENT_LOWAT = %d, want %d", got, notsentLowat)
	}
}
