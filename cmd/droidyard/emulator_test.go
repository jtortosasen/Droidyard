package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/emulator/emulatortest"
	proc "github.com/jtortosasen/droidyard/internal/process"
	"golang.org/x/sys/unix"
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

// TestDaemonStopAndTheEmulatorLaunch: `droidyard stop` ends a boot whose serial is not yet
// resolved and leaves one it has resolved running, in a session of its own, with no pid
// record (ADR-0033 ¶1–2; emulator.md §Detached launch; plan m1i D15 — process-lifecycle.md's
// one exception). The boundary is resolution, not readiness: the daemon owns the launch
// until a new serial answers `emu avd name`, so a stop before then ends it, and once it is
// resolved the emulator is a device the daemon must not signal — it outlives the daemon
// whether or not it has finished booting. A launcher started through Supervisor.Spawn and
// recorded like adb would fail the surviving cases: it would die with the daemon's group.
func TestDaemonStopAndTheEmulatorLaunch(t *testing.T) {
	t.Parallel()
	const avd = "droidyard_m1i"
	for _, tc := range []struct {
		name     string
		resolved bool // the boot's serial is resolved before the stop
		ready    bool // sys.boot_completed reaches 1 before the stop
	}{
		{"before the serial is resolved", false, false},
		{"after the serial is resolved, still booting", true, false},
		{"after a boot", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			polls := 0
			if !tc.ready {
				polls = 1 << 20 // sys.boot_completed stays 0 for this test's lifetime
			}
			d := startEmulatorDaemon(t, avd, polls)
			if !tc.resolved {
				// `emu avd name` never answers, so the boot's serial is never resolved and
				// the stop lands inside the window droidyard owns (ADR-0033 ¶1).
				devicetest.Write(t, d.adb, emulatorSerial+".avd.hang", []byte("1"))
			}

			// --detach returns while the boot runs inside the daemon, so the stop below
			// lands while it is still in flight.
			call := droidyard(t, d.state, "call", "--no-autostart", "--detach", "emulator.start", "--params", `{"avd":"`+avd+`"}`)
			if code := call.wait(t); code != 0 {
				t.Fatalf("the detached start exited %d: %s", code, call.stderr.String())
			}

			// The launcher is a process of its own; wait for the boot it registered.
			var pid int
			eventually(t, "the boot registers its serial in the table", func() bool {
				pid = devicetest.EmulatorPID(d.adb, emulatorSerial)
				return pid > 0
			})
			if tc.resolved {
				// The daemon resolves the serial only after a readiness poll, and the fake
				// counts those in <serial>.polls: the file appearing proves resolution, so
				// the stop lands after it (ADR-0033 ¶2).
				eventually(t, "the boot resolves its serial", func() bool {
					_, err := os.Stat(filepath.Join(d.adb, emulatorSerial+".polls"))
					return err == nil
				})
			}
			daemonSid, err := unix.Getsid(d.srv.cmd.Process.Pid)
			if err != nil {
				t.Fatalf("the daemon's session: %v", err)
			}
			before := snap(t, pid) // cleaned up at the end; a reused pid is not a survivor

			if code := droidyard(t, d.state, "stop").wait(t); code != 0 {
				t.Fatalf("stop exited %d", code)
			}
			d.srv.wait(t)

			// The daemon's shutdown ends a launch it still owns; a resolved one is a device
			// and outlives it (ADR-0033 ¶1–2).
			if left := before.survivors(); !tc.resolved {
				if len(left) != 0 {
					t.Errorf("after stop the launcher (pid %d) is alive; a boot whose serial was not resolved is ended by the daemon's shutdown (ADR-0033 ¶1)", pid)
				}
			} else if len(left) != 1 {
				t.Errorf("after stop the launcher (pid %d) is gone; a resolved emulator outlives the daemon (emulator.md §Detached launch)", pid)
			}
			if recs := recordedPIDs(t, d.state); slices.Contains(recs, pid) {
				t.Errorf("processes.json records the launcher's pid: %v; the emulator is the one child that is not the daemon's", recs)
			}
			if tc.resolved {
				if sid, err := unix.Getsid(pid); err != nil || sid == daemonSid {
					t.Errorf("the launcher's session is %d (err %v); want one of its own, not the daemon's %d", sid, err, daemonSid)
				}
			}
		})
	}
}
