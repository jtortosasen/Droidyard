package policy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// TestOccupationConfig holds ADR-0094 §7: config.json's occupation section gives the
// conflict mode (refuse by default, ask, allow) and the idle threshold in minutes (120 by
// default, 1 to 10,080), every key optional and null its default, re-read by size and
// mtime; a wrong value makes the whole file unusable as any wrong key does (ADR-0032 §4),
// so it trusts nothing and names the file, while Occupation, which has no safer answer than
// the strictest, gives the defaults. The decision it guards: a typo in the section cannot
// quietly relax the rule to allow, nor leave the rest of the file in force.
func TestOccupationConfig(t *testing.T) {
	const refuse, def = capability.OccupationRefuse, 120 * time.Minute
	valid := []struct {
		name, body string
		conflict   string
		after      time.Duration
	}{
		{"no file section", `{"schemaVersion":1}`, refuse, def},
		{"an empty section", `{"schemaVersion":1,"occupation":{}}`, refuse, def},
		{"a null section", `{"schemaVersion":1,"occupation":null}`, refuse, def},
		{"null keys", `{"schemaVersion":1,"occupation":{"conflict":null,"staleAfterMinutes":null}}`, refuse, def},
		{"refuse", `{"schemaVersion":1,"occupation":{"conflict":"refuse"}}`, refuse, def},
		{"ask", `{"schemaVersion":1,"occupation":{"conflict":"ask"}}`, capability.OccupationAsk, def},
		{"allow", `{"schemaVersion":1,"occupation":{"conflict":"allow"}}`, capability.OccupationAllow, def},
		{"the shortest threshold", `{"schemaVersion":1,"occupation":{"staleAfterMinutes":1}}`, refuse, time.Minute},
		{"the longest threshold", `{"schemaVersion":1,"occupation":{"staleAfterMinutes":10080}}`, refuse, 10080 * time.Minute},
		{"both", `{"schemaVersion":1,"occupation":{"conflict":"allow","staleAfterMinutes":45}}`, capability.OccupationAllow, 45 * time.Minute},
	}
	for _, c := range valid {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, c.body)
			p := policy.New(envAt(dir))
			if st := p.ConfigStatus(); st.State != policy.StateOK {
				t.Fatalf("the file is %s (%s); want ok", st.State, st.Error)
			}
			if conflict, after := p.Occupation(); conflict != c.conflict || after != c.after {
				t.Errorf("Occupation() = %q, %v; want %q, %v", conflict, after, c.conflict, c.after)
			}
		})
	}

	t.Run("a missing file is the defaults", func(t *testing.T) {
		p := policy.New(envAt(t.TempDir()))
		if conflict, after := p.Occupation(); conflict != refuse || after != def {
			t.Errorf("Occupation() = %q, %v; want the defaults", conflict, after)
		}
	})

	wrong := []struct{ key, body string }{
		{"conflict: an unknown mode", `{"schemaVersion":1,"occupation":{"conflict":"warn"}}`},
		{"conflict: a case that is not written", `{"schemaVersion":1,"occupation":{"conflict":"Allow"}}`},
		{"conflict: empty", `{"schemaVersion":1,"occupation":{"conflict":""}}`},
		{"conflict: a number", `{"schemaVersion":1,"occupation":{"conflict":1}}`},
		{"staleAfterMinutes: zero", `{"schemaVersion":1,"occupation":{"staleAfterMinutes":0}}`},
		{"staleAfterMinutes: negative", `{"schemaVersion":1,"occupation":{"staleAfterMinutes":-5}}`},
		{"staleAfterMinutes: past a week", `{"schemaVersion":1,"occupation":{"staleAfterMinutes":10081}}`},
		{"staleAfterMinutes: fractional", `{"schemaVersion":1,"occupation":{"staleAfterMinutes":1.5}}`},
		{"staleAfterMinutes: a string", `{"schemaVersion":1,"occupation":{"staleAfterMinutes":"120"}}`},
		{"the section: an array", `{"schemaVersion":1,"occupation":[]}`},
		// A wrong value beside sections that grant: nothing is granted.
		{"beside trust", `{"schemaVersion":1,"trust":{"roots":["/w/root"]},"occupation":{"conflict":"warn"}}`},
	}
	for _, c := range wrong {
		t.Run("unusable: "+c.key, func(t *testing.T) {
			dir := t.TempDir()
			path := write(t, dir, c.body)
			p := policy.New(envAt(dir))
			if st := p.ConfigStatus(); st.State != policy.StateInvalid || !strings.Contains(st.Error, "occupation") {
				t.Errorf("the file is %s (%q); want invalid, naming the occupation key", st.State, st.Error)
			}
			if trusted, err := p.Trusted("/w/root"); trusted || err == nil || !strings.Contains(err.Cause, path) {
				t.Errorf("Trusted = %v, %v; want false and a cause naming %s", trusted, err, path)
			}
			if conflict, after := p.Occupation(); conflict != refuse || after != def {
				t.Errorf("Occupation() = %q, %v from an unusable file; want the strictest defaults", conflict, after)
			}
		})
	}
}

// TestOccupationIsReadLiveAndKeptByARewrite holds ADR-0094 §7: an edit of the section takes
// effect on the next read without a restart (size and mtime, like trust), and `droidyard
// trust`, which rewrites the file, keeps the section.
func TestOccupationIsReadLiveAndKeptByARewrite(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1,"occupation":{"conflict":"ask"}}`)
	p := policy.New(envAt(dir))
	if conflict, _ := p.Occupation(); conflict != capability.OccupationAsk {
		t.Fatalf("Occupation() = %q; want ask", conflict)
	}
	write(t, dir, `{"schemaVersion":1,"occupation":{"conflict":"allow","staleAfterMinutes":30}}`)
	if conflict, after := p.Occupation(); conflict != capability.OccupationAllow || after != 30*time.Minute {
		t.Errorf("after an edit Occupation() = %q, %v; want allow, 30m", conflict, after)
	}
	root := t.TempDir()
	if _, err := p.Trust(root); err != nil {
		t.Fatal(err)
	}
	sect, _ := read(t, path)["occupation"].(map[string]any)
	if sect["conflict"] != "allow" || sect["staleAfterMinutes"] != float64(30) {
		t.Errorf("trust rewrote the occupation section as %v; want it kept", sect)
	}
}
