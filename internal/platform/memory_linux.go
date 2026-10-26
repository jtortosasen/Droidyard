package platform

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func totalMemory() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0, fmt.Errorf("/proc/meminfo MemTotal is %q", strings.TrimSpace(rest))
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb << 10, nil
	}
	return 0, fmt.Errorf("/proc/meminfo has no MemTotal")
}
