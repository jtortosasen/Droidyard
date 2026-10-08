package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/emulator/emulatortest"
	proc "github.com/jtortosasen/droidyard/internal/process"
)

// fakeAdbServer answers host:version (41) and an empty host:devices-l on a loopback port
// of its own, and remembers what it was asked.
type fakeAdbServer struct {
	port string
	mu   sync.Mutex
	seen []string
}

func startFakeAdbServer(t *testing.T) *fakeAdbServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	s := &fakeAdbServer{port: strconv.Itoa(l.Addr().(*net.TCPAddr).Port)}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for {
					head := make([]byte, 4)
					if _, err := io.ReadFull(c, head); err != nil {
						return
					}
					n, _ := strconv.ParseUint(string(head), 16, 16)
					req := make([]byte, n)
					if _, err := io.ReadFull(c, req); err != nil {
						return
					}
					s.mu.Lock()
					s.seen = append(s.seen, string(req))
					s.mu.Unlock()
					switch string(req) {
					case "host:version":
						fmt.Fprint(c, "OKAY00040029")
					case "host:devices-l":
						fmt.Fprint(c, "OKAY0000")
					default:
						fmt.Fprint(c, "FAIL0000")
						return
					}
				}
			}()
		}
	}()
	return s
}

func (s *fakeAdbServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// launchRecords are processes.json's emulator-launch records.
func launchRecords(t *testing.T, state string) []proc.Record {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, proc.RecordsFile))
	if err != nil {
		return nil
	}
	var recs proc.Records
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatalf("processes.json: %v", err)
	}
	var out []proc.Record
	for _, r := range recs.Processes {
		if r.Kind == proc.EmulatorLaunch {
			out = append(out, r)
		}
	}
	return out
}

// TestReconcileEndsAPreSerialLaunch: a daemon SIGKILLed while its launcher has no serial
// leaves a launcher no daemon would find; the next daemon's reconcile finds its
// emulator-launch record, asks the adb server on the recorded port (a fake, on a loopback
// port of the test's own) and, because it answers, its version is the client's and it lists
// no serial, ends the launcher's group before the socket answers; processes.json is empty
// (plan m1p Q1; ADR-0033 as amended 2026-10-08). The launcher is the emulatortest fake,
// kept from registering a serial by a long launch delay, in a session of its own, so only a
// reconcile can end it. Seen failing with no record (the launcher survives the second
// daemon) and with the judge never saying yes.
func TestReconcileEndsAPreSerialLaunch(t *testing.T) {
	t.Parallel()
	const avd = "droidyard_m1p"
	dir, adbDir, state := t.TempDir(), t.TempDir(), shortDir(t)
	emulatortest.Write(t, dir, "list-avds.out", []byte(avd+"\n"))
	emulatortest.Write(t, dir, avd+".launch.delay", []byte("120000\n"))
	devicetest.Write(t, adbDir, "devices.out", []byte("List of devices attached\n"))
	adb := startFakeAdbServer(t)
	env := []string{emulatorDirEnv + "=" + dir, emulatorADBEnv + "=" + adbDir, adbPortEnv + "=" + adb.port}

	first := droidyardEnv(t, state, "", env, "serve")
	first.expect(t, "listening")
	call := droidyard(t, state, "call", "--no-autostart", "--detach", "emulator.start", "--params", `{"avd":"`+avd+`"}`)
	if code := call.wait(t); code != 0 {
		t.Fatalf("the detached start exited %d: %s", code, call.stderr.String())
	}
	var rec proc.Record
	eventually(t, "the launch is recorded", func() bool {
		recs := launchRecords(t, state)
		if len(recs) == 1 {
			rec = recs[0]
		}
		return len(recs) == 1
	})
	t.Cleanup(func() { syscall.Kill(-rec.PGID, syscall.SIGKILL) })
	if rec.AVD != avd || strconv.Itoa(rec.AdbPort) != adb.port || rec.PID != rec.PGID {
		t.Fatalf("record = %+v; want the AVD, port %s and a group of its own", rec, adb.port)
	}

	first.cmd.Process.Kill()
	first.wait(t)
	if syscall.Kill(-rec.PGID, 0) != nil {
		t.Fatal("the launcher died with its daemon; this test needs the orphan a killed daemon leaves")
	}

	second := droidyardEnv(t, state, "", env, "serve")
	second.expect(t, "holding")
	// Reconcile ran before the socket answered: the group is gone by now.
	eventually(t, "the next daemon ends the orphan launch", func() bool {
		return syscall.Kill(-rec.PGID, 0) == syscall.ESRCH
	})
	if recs := launchRecords(t, state); len(recs) != 0 {
		t.Errorf("processes.json still holds %+v", recs)
	}
	if reqs := adb.requests(); len(reqs) < 2 || reqs[0] != "host:version" || reqs[1] != "host:devices-l" {
		t.Errorf("the adb server saw %q; want host:version then host:devices-l", reqs)
	}
	second.cmd.Process.Signal(syscall.SIGTERM)
	if code := second.wait(t); code != 0 {
		t.Errorf("the second daemon exited %d", code)
	}
}
