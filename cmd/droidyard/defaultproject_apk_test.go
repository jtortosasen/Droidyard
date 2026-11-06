package main

import (
	"testing"

	"github.com/jtortosasen/droidyard/adapters/cli"
)

// Decision (ADR-0137 §4, §5; plan m3j step 8): the current directory stands in as the
// project of a CLI call of device.install only when it installs a build. An apk install
// names no project, and a directory that is no project must not turn it into a refusal.
// Another project capability keeps the default whatever its parameters say.
func TestDefaultProjectSkipsAnApkInstall(t *testing.T) {
	ct := shippedTree(t)
	for _, c := range []struct {
		name    string
		call    cli.Call
		project bool
	}{
		{"an apk install", cli.Call{Capability: "device.install", Params: []byte(`{"device":"emulator-5554","apk":"/x/app.apk"}`)}, false},
		{"a build install", cli.Call{Capability: "device.install", Params: []byte(`{"device":"emulator-5554","build":{"module":":app","variant":"debug"}}`)}, true},
		{"a build assembly", cli.Call{Capability: "build.assemble", Params: []byte(`{"module":":app","variant":"debug"}`)}, true},
	} {
		ct.defaultProject(&c.call)
		if got := c.call.Project != ""; got != c.project {
			t.Errorf("%s: project %q; want a project: %v", c.name, c.call.Project, c.project)
		}
	}
}
