package system_test

import (
	"context"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/system"
)

type noOps struct{}

func (noOps) Running() int { return 0 }

type fakeADB struct{ st device.Status }

func (f fakeADB) Check(context.Context) device.Status { return f.st }

type allPresent struct{}

func (allPresent) Missing(capability.Requirement) (string, bool) { return "", false }

func health(t *testing.T, adb system.ADB) system.Health {
	t.Helper()
	reg := capability.NewRegistry()
	system.Register(reg, allPresent{}, adb, noOps{}, nil, nil)
	d, _ := reg.Lookup("system.health")
	out, err := d.Handler(context.Background(), &capability.Call{})
	if err != nil {
		t.Fatal(err)
	}
	return out.(system.Health)
}

// TestHealthReportsAdbAndItsWarnings pins R1/A2: system.health carries what the adb
// subsystem found, warnings included, as a report and not an error; and a daemon with
// no adb subsystem omits the field rather than reporting a made-up "not found".
func TestHealthReportsAdbAndItsWarnings(t *testing.T) {
	st := device.Status{Found: true, Path: "/x/adb", Source: device.SourcePath, Warnings: []string{"mismatch"}, Rejected: []device.Rejected{}}
	h := health(t, fakeADB{st})
	if h.ADB == nil || !h.ADB.Found || h.ADB.Path != "/x/adb" || len(h.ADB.Warnings) != 1 {
		t.Errorf("health.ADB = %+v, want the subsystem's status", h.ADB)
	}
	if health(t, nil).ADB != nil {
		t.Error("health.ADB set with no adb subsystem")
	}
}
