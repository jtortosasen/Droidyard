package sdk

import (
	"regexp"
	"strconv"
	"strings"
)

// Revision is a package revision as the repository and package.xml write it: major, then
// optional minor and micro, then an optional preview number ("rc3"). A part that is
// absent compares as 0 and is not printed, so 23.0 and 23.0.0 are the same revision and
// print as they were written.
type Revision struct {
	Major, Minor, Micro, Preview int
	// Parts is how many of Major, Minor and Micro were written (1 to 3).
	Parts int
}

// String is "23", "23.0" or "37.0.1", with "-rc3" after it for a preview.
func (r Revision) String() string {
	parts := r.Parts
	if parts < 1 {
		parts = 1
	}
	s := strconv.Itoa(r.Major)
	if parts >= 2 {
		s += "." + strconv.Itoa(r.Minor)
	}
	if parts >= 3 {
		s += "." + strconv.Itoa(r.Micro)
	}
	if r.Preview > 0 {
		s += "-rc" + strconv.Itoa(r.Preview)
	}
	return s
}

// Compare orders revisions: -1 when r is older than o, 0 when equal, 1 when newer. A final
// release is newer than any preview of the same numbers (sdklib's order).
func (r Revision) Compare(o Revision) int {
	for _, d := range [][2]int{{r.Major, o.Major}, {r.Minor, o.Minor}, {r.Micro, o.Micro}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case r.Preview == o.Preview:
		return 0
	case r.Preview == 0:
		return 1
	case o.Preview == 0:
		return -1
	case r.Preview < o.Preview:
		return -1
	}
	return 1
}

var revisionPattern = regexp.MustCompile(`^(\d{1,9})(?:\.(\d{1,9}))?(?:\.(\d{1,9}))?(?:[ -]?rc(\d{1,9}))?$`)

// ParseRevision reads a revision as package.xml's versions print it ("35.0.0", "12.0",
// "3", "1.0.0-rc3") and as source.properties writes it ("36.0.0 rc1").
func ParseRevision(s string) (Revision, bool) {
	m := revisionPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Revision{}, false
	}
	var r Revision
	r.Major, _ = strconv.Atoi(m[1])
	r.Parts = 1
	if m[2] != "" {
		r.Minor, _ = strconv.Atoi(m[2])
		r.Parts = 2
	}
	if m[3] != "" {
		r.Micro, _ = strconv.Atoi(m[3])
		r.Parts = 3
	}
	if m[4] != "" {
		r.Preview, _ = strconv.Atoi(m[4])
	}
	return r, true
}

// revisionOf builds a Revision from the text of package.xml's <revision> children; an
// empty minor or micro is absent. ok is false when a present part is not a number or the
// major is missing.
func revisionOf(major, minor, micro, preview string) (Revision, bool) {
	var r Revision
	n := func(s string) (int, bool) {
		v, err := strconv.Atoi(strings.TrimSpace(s))
		return v, err == nil && v >= 0 && v < 1_000_000_000
	}
	var ok bool
	if r.Major, ok = n(major); !ok {
		return Revision{}, false
	}
	r.Parts = 1
	if strings.TrimSpace(minor) != "" {
		if r.Minor, ok = n(minor); !ok {
			return Revision{}, false
		}
		r.Parts = 2
		if strings.TrimSpace(micro) != "" {
			if r.Micro, ok = n(micro); !ok {
				return Revision{}, false
			}
			r.Parts = 3
		}
	} else if strings.TrimSpace(micro) != "" {
		// A micro without a minor: the minor is 0 and written.
		if r.Micro, ok = n(micro); !ok {
			return Revision{}, false
		}
		r.Parts = 3
	}
	if strings.TrimSpace(preview) != "" {
		if r.Preview, ok = n(preview); !ok {
			return Revision{}, false
		}
	}
	return r, true
}

// naturalLess orders ids the way a person reads versions: runs of digits compare as
// numbers, everything else byte by byte ("android-9" before "android-10",
// "build-tools;9.0.0" before "build-tools;36.1.0").
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			na, ra := digitRun(a)
			nb, rb := digitRun(b)
			if c := compareDigits(na, nb); c != 0 {
				return c < 0
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digitRun(s string) (digits, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// compareDigits compares two digit runs as numbers of any length.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}
