package hosting

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

// maxNameLen is the longest name policy.PeerNamePattern allows.
const maxNameLen = 32

// NameFrom is the name one side gives the other from a host name (ADR-0107 §5): lowercase,
// the part before the first dot, every run of other characters one "-", "m-" in front when
// it would start with a non-letter, cut to 32 characters, then "-2", "-3"… until it is not
// among taken. Whatever the input, the result matches policy.PeerNamePattern, since the
// input is another machine's text.
func NameFrom(hostname string, taken []string) string {
	s := strings.ToLower(hostname)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	name := b.String()
	switch {
	case name == "":
		name = "host"
	case name[0] < 'a' || name[0] > 'z':
		name = "m-" + name
	}
	base := cutName(name, maxNameLen)
	if !slices.Contains(taken, base) {
		return base
	}
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		if cand := cutName(name, maxNameLen-len(suffix)) + suffix; !slices.Contains(taken, cand) {
			return cand
		}
	}
}

// cutName cuts name to at most n bytes, never ending in "-".
func cutName(name string, n int) string {
	if len(name) > n {
		name = name[:n]
	}
	return strings.TrimRight(name, "-")
}

// hostname is how this machine's name is read; a variable only so a test can say what it is.
var hostname = os.Hostname

// OwnName is the name this machine gives itself (ADR-0107 §5): its host name made into a
// peer name, "-2", "-3"… until it is not among taken. A machine whose name cannot be read is
// "host".
func OwnName(taken []string) string {
	h, _ := hostname()
	return NameFrom(h, taken)
}
