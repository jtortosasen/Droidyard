package textfilter

import (
	"errors"
	"fmt"
	"regexp/syntax"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

func invalidParams(t *testing.T, err error) *capability.Error {
	t.Helper()
	var ce *capability.Error
	if !errors.As(err, &ce) || ce.Code != capability.ErrInvalidParams {
		t.Fatalf("error = %v; want a capability error with code %s", err, capability.ErrInvalidParams)
	}
	return ce
}

// Decision (ADR-0135 §1, plan m3i Q1): every bound a caller's patterns can break is
// REQUEST_INVALID_PARAMS naming the field and index, so the caller fixes the right
// pattern, and the instruction budget is one for the whole call, counted over both lists
// together; a budget counted per pattern would let 40 patterns of 3,999 instructions each
// through.
func TestPatternBounds(t *testing.T) {
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("tag%d", i)
		}
		return out
	}
	// Each list alone is well inside the budget; the two together are past it.
	k := MaxCallInsts / 10
	heavy := make([]string, 6)
	for i := range heavy {
		heavy[i] = fmt.Sprintf("a{%d}", k)
	}
	// 25 alternations of two 1,000-fold repeats: the parser accepts it (a 512-byte pattern
	// cannot reach the parser's own "expression too large"), the estimate refuses it
	// before any program is built.
	huge := strings.Repeat("(a{1000}|b{1000})", 25)

	cases := []struct {
		name string
		spec Spec
		want []string // substrings of the message, all required
	}{
		{"21 match patterns", Spec{Match: many(21)}, []string{"match has 21 patterns"}},
		{"21 exclude patterns", Spec{Exclude: many(21)}, []string{"exclude has 21 patterns"}},
		{"513-byte pattern", Spec{Match: []string{"ok", strings.Repeat("a", 513)}}, []string{"match[1] is 513 bytes"}},
		{"512-byte pattern in exclude", Spec{Exclude: []string{"ok", "ok", strings.Repeat("a", 513)}}, []string{"exclude[2] is 513 bytes"}},
		{"empty pattern", Spec{Match: []string{""}}, []string{"match[0] is 0 bytes"}},
		{"both lists past the budget", Spec{Match: heavy, Exclude: heavy}, []string{"exclude[", "compiled instructions"}},
		{"a pattern far past the budget", Spec{Exclude: []string{"ok", huge}}, []string{"exclude[1] compiles to far more"}},
		{"RE2 refuses a repeat", Spec{Match: []string{"ok", "(a{1000}){1000}"}}, []string{"match[1]: error parsing regexp: invalid repeat count"}},
		{"unclosed group", Spec{Match: []string{"("}}, []string{"match[0]: error parsing regexp: missing closing )"}},
		{"a backreference RE2 does not have", Spec{Exclude: []string{`(a)\1`}}, []string{"exclude[0]: error parsing regexp"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := Compile(c.spec)
			if f != nil {
				t.Fatalf("Compile returned a filter for %s", c.name)
			}
			ce := invalidParams(t, err)
			for _, w := range c.want {
				if !strings.Contains(ce.Message, w) {
					t.Fatalf("message = %q; want it to contain %q", ce.Message, w)
				}
			}
		})
	}

	// The two lists really are fine alone: the failure above is the sum.
	for name, spec := range map[string]Spec{"match alone": {Match: heavy}, "exclude alone": {Exclude: heavy}} {
		if _, err := Compile(spec); err != nil {
			t.Errorf("%s: %v; each list alone must pass for the sum case to prove the budget is per call", name, err)
		}
	}
	// And the bounds are inclusive: 20 patterns, and a 512-byte pattern (a class is one
	// instruction however long; a 512-byte literal would be over the budget by itself).
	if _, err := Compile(Spec{Match: many(20)}); err != nil {
		t.Errorf("20 patterns: %v", err)
	}
	if _, err := Compile(Spec{Exclude: []string{"[" + strings.Repeat("ab", 255) + "]"}}); err != nil {
		t.Errorf("a 512-byte pattern: %v", err)
	}
}

// Decision (ADR-0135 §3): a subject passes when any match pattern matches (or there are
// none) and no exclude pattern does, so exclude always wins; case-insensitivity is RE2's
// (?i), not a parameter.
func TestMatchExclude(t *testing.T) {
	cases := []struct {
		name    string
		spec    Spec
		subject string
		want    bool
	}{
		{"empty spec keeps everything", Spec{}, "anything", true},
		{"one match", Spec{Match: []string{"Wifi"}}, "WifiService: up", true},
		{"match is OR across patterns: second", Spec{Match: []string{"nomatch", "up$"}}, "WifiService: up", true},
		{"match is OR across patterns: none", Spec{Match: []string{"nomatch", "down$"}}, "WifiService: up", false},
		{"exclude alone drops a hit", Spec{Exclude: []string{"Surface"}}, "SurfaceFlinger: noise", false},
		{"exclude alone keeps the rest", Spec{Exclude: []string{"Surface"}}, "WifiService: up", true},
		{"exclude wins over match", Spec{Match: []string{"Flinger"}, Exclude: []string{"noise"}}, "SurfaceFlinger: noise", false},
		{"exclude is OR too", Spec{Exclude: []string{"x1", "noise"}}, "SurfaceFlinger: noise", false},
		{"case matters by default", Spec{Match: []string{"wifi"}}, "WifiService: up", false},
		{"(?i) folds case", Spec{Match: []string{"(?i)wifi"}}, "WifiService: up", true},
		{"(?i) in exclude", Spec{Exclude: []string{"(?i)SURFACEFLINGER"}}, "SurfaceFlinger: noise", false},
		{"a class the owner's case needs", Spec{Match: []string{`Removed [0-9a-f]+ com\.facebook\.orca#`}}, "ActivityManager: Removed 3fa9 com.facebook.orca#1", true},
		{"anchored tag selects a tag", Spec{Match: []string{"^Wifi: "}}, "Wifi: up", true},
		{"anchored tag does not select a message", Spec{Match: []string{"^Wifi: "}}, "Other: Wifi: up", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := Compile(c.spec)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.Keep(c.subject); got != c.want {
				t.Errorf("Keep(%q) = %v; want %v", c.subject, got, c.want)
			}
			if got, want := f.Empty(), len(c.spec.Match)+len(c.spec.Exclude) == 0; got != want {
				t.Errorf("Empty() = %v; want %v", got, want)
			}
		})
	}

	var nilFilter *Filter
	if !nilFilter.Keep("x") || !nilFilter.Empty() {
		t.Error("a nil Filter must keep everything and be empty")
	}
}

// Decision: the tree estimate that refuses a huge pattern before it is built is an
// upper bound on the compiled program and never so loose (more than estimateSlack/2
// times) that it could refuse a pattern the budget would accept.
func TestEstimateBoundsTheProgram(t *testing.T) {
	for _, p := range []string{
		`a`, `abc`, `(?i)abc`, `a|b|c`, `a*`, `a+`, `a?`, `a{3}`, `a{2,5}`, `a{2,}`, `a{0,40}`, `[a-z]{0,40}x`,
		`(a{10}){10}`, `((a{10}){10}){10}`, `(a|bc){3}d`, `\pL{50}`, `^Tag: .*`, `(?:ab|cd)+e?`,
		`Removed [0-9a-f]+ com\.facebook\.orca#`, `(?i)(abcdefgh){20}`, `x{1000}`, `(a{500}|b{500}){2}`,
	} {
		tree, err := syntax.Parse(p, syntax.Perl)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		prog, err := syntax.Compile(tree.Simplify())
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		est, actual := estimate(tree), len(prog.Inst)
		if est < actual || est*2 > actual*estimateSlack {
			t.Errorf("%s: estimate %d against %d compiled instructions; want actual <= estimate <= %d", p, est, actual, actual*estimateSlack/2)
		}
	}
}
