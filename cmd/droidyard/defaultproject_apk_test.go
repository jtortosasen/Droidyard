package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// Decision (close fix round, ADR-0137 §4): the CLI makes a relative apk absolute against
// its own cwd, since the daemon refuses a relative path; an absolute one, a build install
// and another capability's `apk` are left as they are.
//
// Seen failing before the change: the apk stayed "app.apk" and the daemon refused it.
func TestCLIMakesARelativeAPKAbsolute(t *testing.T) {
	ct := shippedTree(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, capability, params, want string }{
		{"relative", "device.install", `{"device":"e","apk":"app.apk"}`, filepath.Join(cwd, "app.apk")},
		{"relative in a folder", "device.install", `{"device":"e","apk":"out/app.apk"}`, filepath.Join(cwd, "out", "app.apk")},
		{"absolute", "device.install", `{"device":"e","apk":"/x/app.apk"}`, "/x/app.apk"},
		{"another capability", "device.other", `{"apk":"app.apk"}`, "app.apk"},
	} {
		call := cli.Call{Capability: c.capability, Params: []byte(c.params)}
		ct.defaultProject(&call)
		var got struct{ APK string }
		if err := json.Unmarshal(call.Params, &got); err != nil || got.APK != c.want {
			t.Errorf("%s: apk %q (%v); want %q", c.name, got.APK, err, c.want)
		}
	}
}
