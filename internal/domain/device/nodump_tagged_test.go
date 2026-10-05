//go:build droidyard_nodump

package device

import (
	"strings"
	"testing"
)

// Decision (plan m1f, D4/E3; run with `go test -tags droidyard_nodump`): a build tagged
// droidyard_nodump issues no `uiautomator dump` anywhere and its dump stage fails
// DEVICE_UI_DUMP_FAILED naming the build, so a run against a device with a watching
// accessibility service never suspends it. Seen failing with the constant ignored (the
// fake would log the dump).
func TestTaggedBuildNeverDumps(t *testing.T) {
	f := newUIFixture(t, nil) // default fixture: the readers cannot run, so the chain reaches the dump
	ce := mustFail(t, f.tree(t, uiSerial), ErrUIDumpFailed)
	if !strings.Contains(ce.Cause, "droidyard_nodump") {
		t.Errorf("cause %q does not name the tagged build", ce.Cause)
	}
	if n := count(f.calls(t), ttyCall); n != 0 {
		t.Errorf("%d /dev/tty dumps, want none", n)
	}
	for _, c := range f.calls(t) {
		if strings.Contains(c, "uiautomator") {
			t.Errorf("a tagged build issued %q", c)
		}
	}
}
