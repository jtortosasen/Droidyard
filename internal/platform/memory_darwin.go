package platform

import "golang.org/x/sys/unix"

func totalMemory() (uint64, error) { return unix.SysctlUint64("hw.memsize") }
