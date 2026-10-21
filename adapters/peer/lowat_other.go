//go:build !darwin && !linux

package peer

import "net"

func setNotsentLowat(*net.TCPConn, int) error { return nil }

func getNotsentLowat(*net.TCPConn) (int, error) { return 0, nil }
