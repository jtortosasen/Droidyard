package platform

// TotalMemory is the host's physical memory in bytes (macOS: sysctl hw.memsize; Linux:
// MemTotal of /proc/meminfo). The error says it cannot be read on this system; the caller
// then applies no limit that depends on it.
func TotalMemory() (uint64, error) { return totalMemory() }
