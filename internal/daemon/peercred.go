package daemon

import "net"

// peerUID reads the connecting process's uid from the kernel (ADR-0019 §6).
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, uerr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) { uid, uerr = peerUIDOf(int(fd)) }); err != nil {
		return -1, err
	}
	return uid, uerr
}
