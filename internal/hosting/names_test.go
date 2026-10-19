package hosting

import (
	"regexp"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/policy"
)

// TestNameFrom is ADR-0107 §5: the name made from a host name is lowercase, the part before
// the first dot, other runs one "-", "m-" before a leading non-letter, at most 32, and "-2",
// "-3"… when taken; and whatever the input, it matches the pattern a policy accepts, since
// the input is another machine's text.
func TestNameFrom(t *testing.T) {
	long := strings.Repeat("a", 40)
	cases := []struct {
		host  string
		taken []string
		want  string
	}{
		{"Jorges-MacBook-Pro.local", nil, "jorges-macbook-pro"},
		{"1mac", nil, "m-1mac"},
		{"mac", []string{"mac"}, "mac-2"},
		{"mac", []string{"mac", "mac-2", "mac-3"}, "mac-4"},
		{long, nil, strings.Repeat("a", 32)},
		{long, []string{strings.Repeat("a", 32)}, strings.Repeat("a", 30) + "-2"},
		{"Jorge's  MacBook", nil, "jorge-s-macbook"},
		{"__x__", nil, "x"},
		{"-lead", nil, "lead"},
		{"", nil, "host"},
		{".local", nil, "host"},
		{"ünï", nil, "n"},
	}
	for _, c := range cases {
		if got := NameFrom(c.host, c.taken); got != c.want {
			t.Errorf("NameFrom(%q, %v) = %q; want %q", c.host, c.taken, got, c.want)
		}
	}

	// A seed set of hostile and odd names: every output matches the pattern, even when the
	// first candidates are taken.
	re := regexp.MustCompile(policy.PeerNamePattern)
	seeds := []string{"", "a", "0", "-", "--", "a.b.c", "A_B", "\x00\xff", "名前", "m-", "m--1",
		strings.Repeat("-", 50), strings.Repeat("9", 50), strings.Repeat("a-", 30), "../etc", "a b\tc\nd"}
	for _, h := range seeds {
		taken := []string{}
		for range 3 {
			got := NameFrom(h, taken)
			if !re.MatchString(got) {
				t.Errorf("NameFrom(%q, %v) = %q, which does not match %s", h, taken, got, policy.PeerNamePattern)
			}
			taken = append(taken, got)
		}
	}
}

func FuzzNameFrom(f *testing.F) {
	for _, s := range []string{"", "Jorges-MacBook-Pro.local", "1mac", "名前", strings.Repeat("a", 60), "a--b"} {
		f.Add(s)
	}
	re := regexp.MustCompile(policy.PeerNamePattern)
	f.Fuzz(func(t *testing.T, host string) {
		taken := []string{}
		for range 3 {
			got := NameFrom(host, taken)
			if !re.MatchString(got) {
				t.Fatalf("NameFrom(%q, %v) = %q, which does not match the pattern", host, taken, got)
			}
			taken = append(taken, got)
		}
	})
}
