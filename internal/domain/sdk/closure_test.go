package sdk

import (
	"reflect"
	"testing"
)

// Decision (ADR-0129 §8, plan m3f step 4): the closure's answer does not depend on the order
// of the requested ids. A dependency kept as installed on its first visit is judged again
// when a later id requests it or needs a higher revision of it (the close's verifier, B1).
func TestClosureIsOrderIndependent(t *testing.T) {
	rev := func(s string) *Revision {
		r, ok := ParseRevision(s)
		if !ok {
			t.Fatalf("revision %q", s)
		}
		return &r
	}
	pkg := func(id, r string, deps ...Dependency) *RemotePackage {
		return &RemotePackage{ID: id, Revision: *rev(r), Dependencies: deps}
	}
	m := &Model{byID: map[string]*RemotePackage{}}
	for _, p := range []*RemotePackage{
		pkg("emulator", "36.0.0"),
		pkg("system-images;a", "1", Dependency{ID: "emulator"}),
		pkg("system-images;b", "1", Dependency{ID: "emulator", Min: rev("35.0.0")}),
	} {
		m.byID[p.ID] = p
		m.Packages = append(m.Packages, p)
	}
	installed := map[string]located{"emulator": {Package: Package{ID: "emulator", Version: "30.0.0"}}}
	ids := func(steps []step) []string {
		var out []string
		for _, s := range steps {
			out = append(out, s.pkg.ID)
		}
		return out
	}
	for _, tc := range []struct {
		req  []string
		want []string
	}{
		{[]string{"system-images;a", "system-images;b"}, []string{"system-images;a", "emulator", "system-images;b"}},
		{[]string{"system-images;b", "system-images;a"}, []string{"emulator", "system-images;b", "system-images;a"}},
		{[]string{"system-images;a", "emulator"}, []string{"system-images;a", "emulator"}},
		{[]string{"emulator", "system-images;a"}, []string{"emulator", "system-images;a"}},
	} {
		steps, err := closure(m, installed, tc.req)
		if err != nil {
			t.Fatalf("%v: %v", tc.req, err)
		}
		if got := ids(steps); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("closure(%v) = %v, want %v", tc.req, got, tc.want)
		}
	}
}
