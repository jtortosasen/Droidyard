package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/emulator/emulatortest"
	proc "github.com/jtortosasen/droidyard/internal/process"
)

// emulatorSerial is the serial the fake launcher boots an AVD as by default.
const emulatorSerial = "emulator-5556"

// emulatorDaemon is a re-executed daemon given a fake emulator launcher and a fake adb
// that share the table a boot registers in (plan m1i step 5 check, D20).
type emulatorDaemon struct {
	srv   *process
	state string
	adb   string // the fake adb's scenario directory, which holds the launcher's table
}

// startEmulatorDaemon starts a daemon whose launcher knows one AVD, avd, and whose
// serial answers sys.boot_completed 0 for bootPolls polls before 1.
func startEmulatorDaemon(t *testing.T, avd string, bootPolls int) *emulatorDaemon {
	t.Helper()
	dir, adbDir, state := t.TempDir(), t.TempDir(), shortDir(t)
	emulatortest.Write(t, dir, "list-avds.out", []byte(avd+"\n"))
	devicetest.Write(t, adbDir, "devices.out", []byte("List of devices attached\n"))
	if bootPolls > 0 {
		devicetest.Write(t, adbDir, avd+".bootpolls", []byte(strconv.Itoa(bootPolls)+"\n"))
	}
	srv := droidyardEnv(t, state, "", []string{emulatorDirEnv + "=" + dir, emulatorADBEnv + "=" + adbDir}, "serve")
	srv.expect(t, "listening")
	return &emulatorDaemon{srv: srv, state: state, adb: adbDir}
}

// recordedPIDs are the pids processes.json lists, nil when the file is absent.
func recordedPIDs(t *testing.T, state string) []int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, proc.RecordsFile))
	if os.IsNotExist(err) {
		return nil
	}
	var recs proc.Records
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatalf("processes.json: %v", err)
	}
	var pids []int
	for _, r := range recs.Processes {
		pids = append(pids, r.PID)
	}
	return pids
}

// TestDaemonStopLeavesTheEmulatorOutsideIt: whether the boot is still in flight or
// finished, `droidyard stop` ends the daemon and leaves the fake launcher running, in a
// session of its own, with no pid record (plan m1i step 5 check; emulator.md §Detached
// launch; plan m1i D15 — process-lifecycle.md's one exception). A launcher started
// through Supervisor.Spawn and recorded like adb would fail all three: it would die with
// the daemon's group.
func TestDaemonStopLeavesTheEmulatorOutsideIt(t *testing.T) {
	t.Parallel()
	const avd = "droidyard_m1i"
	for _, tc := range []struct {
		name    string
		midBoot bool
	}{
		{"mid-boot", true},
		{"after a boot", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			polls := 0
			if tc.midBoot {
				polls = 1 << 20 // sys.boot_completed stays 0 for this test's lifetime
			}
			d := startEmulatorDaemon(t, avd, polls)

			params := `{"avd":"` + avd + `"}`
			if tc.midBoot {
				// --detach returns while the boot runs inside the daemon, so the stop
				// below lands mid-boot.
				call := droidyard(t, d.state, "call", "--no-autostart", "--detach", "emulator.start", "--params", params)
				if code := call.wait(t); code != 0 {
					t.Fatalf("the detached start exited %d: %s", code, call.stderr.String())
				}
			} else if env, code := callIn(t, d.state, "", "emulator.start", "--params", params); code != 0 {
				t.Fatalf("emulator.start exited %d: %+v", code, env.Error)
			}

			// The launcher is a process of its own; wait for the boot it registered.
			var pid int
			eventually(t, "the boot registers its serial in the table", func() bool {
				pid = devicetest.EmulatorPID(d.adb, emulatorSerial)
				return pid > 0
			})
			daemonSid, err := syscall.Getsid(d.srv.cmd.Process.Pid)
			if err != nil {
				t.Fatalf("the daemon's session: %v", err)
			}
			before := snap(t, pid) // cleaned up at the end; a reused pid is not a survivor

			if code := droidyard(t, d.state, "stop").wait(t); code != 0 {
				t.Fatalf("stop exited %d", code)
			}
			d.srv.wait(t)

			if left := before.survivors(); len(left) != 1 {
				t.Errorf("after stop the launcher (pid %d) is gone; an emulator outlives the daemon (emulator.md §Detached launch)", pid)
			}
			if recs := recordedPIDs(t, d.state); slices.Contains(recs, pid) {
				t.Errorf("processes.json records the launcher's pid: %v; the emulator is the one child that is not the daemon's", recs)
			}
			if sid, err := syscall.Getsid(pid); err != nil || sid == daemonSid {
				t.Errorf("the launcher's session is %d (err %v); want one of its own, not the daemon's %d", sid, err, daemonSid)
			}
		})
	}
}
