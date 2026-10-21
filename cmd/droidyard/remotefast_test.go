package main

import (
	"strings"
	"testing"
)

// Decision (ADR-0118 §4): `droidyard remote fast <name> on|off` sets the remote's fast mark in
// the configuration (the one policy.update every verb is), shown by `remote list`, leaving the
// rest of the entry as it was; on a name that is not a remote, or any other word than on and
// off, it is refused and writes nothing. Seen failing with the verb writing the mark on the
// wrong entry (the first remote): the second remote stayed unmarked.
func TestRemoteFastSetsTheMark(t *testing.T) {
	pol, _ := hostingPolicy(t)
	mustVerb(t, pol, "remote", "add", "one", "100.64.0.3:7421", fpLab)
	mustVerb(t, pol, "remote", "add", "two", "100.64.0.4:7421", fpAnna)

	out := mustVerb(t, pol, "remote", "fast", "two", "on")
	if !strings.Contains(out, "within seconds") {
		t.Errorf("remote fast printed %q; a running daemon tells the remote within seconds", out)
	}
	got := pol.Get().Remotes
	if len(got) != 2 || got[0].Fast || !got[1].Fast || got[1].Address != "100.64.0.4:7421" || got[1].Fingerprint != fpAnna {
		t.Fatalf("remotes = %+v; want only two marked, its address and fingerprint kept", got)
	}
	if list := mustVerb(t, pol, "remote", "list"); !strings.Contains(list, "two  100.64.0.4:7421  "+fpAnna+"  fast") || strings.Contains(list, fpLab+"  fast") {
		t.Errorf("remote list printed %q", list)
	}
	mustVerb(t, pol, "remote", "fast", "two", "off")
	if got := pol.Get().Remotes; got[1].Fast {
		t.Errorf("after off: %+v", got)
	}
	for _, args := range [][]string{{"fast", "nope", "on"}, {"fast", "two", "maybe"}, {"fast", "two"}} {
		if code, _, _ := verb(t, pol, "remote", args...); code == 0 {
			t.Errorf("remote %v exited 0", args)
		}
	}
}
