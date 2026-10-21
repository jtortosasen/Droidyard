//go:build darwin || linux

package peer

import (
	"net"
	"syscall"
)

// setNotsentLowat sets TCP_NOTSENT_LOWAT on c: the kernel holds at most about n bytes not yet
// sent, so a slow link's backlog stays in the Hub, where the age bound sees it (ADR-0118 §3).
func setNotsentLowat(c *net.TCPConn, n int) error {
	return rawSockopt(c, func(fd int) error {
		return syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, tcpNotsentLowat, n)
	})
}

// getNotsentLowat reads the option back, for the test.
func getNotsentLowat(c *net.TCPConn) (int, error) {
	n := 0
	err := rawSockopt(c, func(fd int) (e error) {
		n, e = syscall.GetsockoptInt(fd, syscall.IPPROTO_TCP, tcpNotsentLowat)
		return e
	})
	return n, err
}

func rawSockopt(c *net.TCPConn, f func(fd int) error) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = f(int(fd)) }); err != nil {
		return err
	}
	return ferr
}
