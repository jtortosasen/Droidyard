package emulator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/process/processtest"
	"github.com/jtortosasen/droidyard/internal/store"
)

// adbServer is a fake adb server on a loopback port the test owns: it answers the smart
// socket's host:version and host:devices-l, or, hanging, reads the request and never
// answers. It is the only adb server these tests reach; none touches 5037.
type adbServer struct {
	port int
	mu   sync.Mutex
	seen []string
}

// startAdbServer listens on a free loopback port. version is what host:version answers
// (a number, in hex on the wire), listing the lines host:devices-l answers; hang makes it
// never answer.
func startAdbServer(t *testing.T, version int, listing string, hang bool) *adbServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &adbServer{port: l.Addr().(*net.TCPAddr).Port}
	t.Cleanup(func() { l.Close() })
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
					if hang {
						io.Copy(io.Discard, c) // read until the client gives up
						return
					}
					var payload string
					switch string(req) {
					case "host:version":
						payload = fmt.Sprintf("%04x", version)
					case "host:devices-l":
						payload = listing
					default:
						fmt.Fprint(c, "FAIL0000")
						return
					}
					fmt.Fprintf(c, "OKAY%04x%s", len(payload), payload)
				}
			}()
		}
	}()
	return s
}

func (s *adbServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// launchStandIn is a live process leading a group of its own, recorded as the launch of
// avd on port, standing in for a qemu a dead daemon left. The record is written in the
// store the test reconciles.
func launchStandIn(t *testing.T, st *store.Store, avd string, port int) *processtest.Proc {
	t.Helper()
	p := processtest.Start(t, processtest.Sleep)
	t.Cleanup(func() { syscall.Kill(-p.Pid, syscall.SIGKILL) })
	return p
}

func recordsOf(t *testing.T, st *store.Store, avds map[string]*processtest.Proc, ports map[string]int) {
	t.Helper()
	var recs []process.Record
	for avd, p := range avds {
		started, err := platform.StartTime(p.Pid)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, process.Record{ID: string(process.EmulatorLaunch), Kind: process.EmulatorLaunch, PID: p.Pid, PGID: p.Pid,
			StartTime: started, Command: []string{"emulator", "-avd", avd}, AVD: avd, AdbPort: ports[avd]})
	}
	if err := st.WriteVersion(process.RecordsFile, process.RecordsVersion, process.Records{Processes: recs}); err != nil {
		t.Fatal(err)
	}
}

// reconcileWith runs process.Reconcile over st with the Emulator's judge, the adapter
// serve builds, and returns the log.
func reconcileWith(t *testing.T, f *fixture, st *store.Store) string {
	t.Helper()
	var log bytes.Buffer
	judge := func(ctx context.Context, r process.Record) (bool, string) {
		return f.em.JudgeLaunch(ctx, r.AVD, r.AdbPort)
	}
	if err := process.Reconcile(st, &log, judge); err != nil {
		t.Fatal(err)
	}
	return log.String()
}

func newReconcileStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func recordsLeft(t *testing.T, st *store.Store) int {
	t.Helper()
	var recs process.Records
	if out, err := st.Read(process.RecordsFile, process.RecordsVersion, &recs); out != store.Loaded || err != nil {
		t.Fatalf("processes.json: %v, %v", out, err)
	}
	return len(recs.Processes)
}

func answers(t *testing.T, f *fixture, serial, avd string) {
	t.Helper()
	devicetest.Write(t, f.adbDir, serial+".avd", []byte(avd+"\r\nOK\r\n"))
}

const (
	oneEmulatorOffline = "emulator-5556\toffline product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"
	oneEmulatorOnline  = "emulator-5554\tdevice product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:2\n"
	protocolVersion    = 41 // 1.0.41: what the fake adb client reports
)

// Decision (plan m1p Q1 ¶2; ADR-0033 ¶2): a launch an earlier daemon left is NEVER ended
// when a listed serial, offline included, answers as the same AVD: that emulator has
// become a device. The adb server answers over its socket and its version matches, the
// serial lists offline (as a booting one does) and its console names the AVD: the process
// stays alive and its record is dropped. Seen failing with the AVD check removed
// (signalling whenever the server answers) and with offline serials skipped.
func TestReconcileLeavesAResolvedEmulator(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listHead})
	answers(t, f, "emulator-5556", loadedAVD)
	srv := startAdbServer(t, protocolVersion, oneEmulatorOffline, false)
	st := newReconcileStore(t)
	p := launchStandIn(t, st, loadedAVD, srv.port)
	recordsOf(t, st, map[string]*processtest.Proc{loadedAVD: p}, map[string]int{loadedAVD: srv.port})

	log := reconcileWith(t, f, st)
	if syscall.Kill(p.Pid, 0) != nil {
		t.Errorf("the emulator that became a device was signalled; log:\n%s", log)
	}
	if n := recordsLeft(t, st); n != 0 {
		t.Errorf("%d records left; want the launch's dropped", n)
	}
	if !strings.Contains(log, "a device, not a launch") {
		t.Errorf("the log does not say why: %s", log)
	}
}

// Decision (plan m1p Q1 ¶2): the positive answer: the server answers, its version is the
// client's, and every listed serial runs another AVD: the group is ended. The mutation
// this fails: judging that never says yes (a launch left forever).
func TestReconcileEndsALaunchAdbProvesIsNotADevice(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listHead})
	answers(t, f, "emulator-5554", "Some_Other_AVD")
	srv := startAdbServer(t, protocolVersion, oneEmulatorOnline, false)
	st := newReconcileStore(t)
	p := launchStandIn(t, st, loadedAVD, srv.port)
	recordsOf(t, st, map[string]*processtest.Proc{loadedAVD: p}, map[string]int{loadedAVD: srv.port})

	log := reconcileWith(t, f, st)
	select {
	case <-p.Done():
	case <-time.After(3 * time.Second):
		t.Errorf("the launch was not ended; log:\n%s", log)
	}
	if n := recordsLeft(t, st); n != 0 {
		t.Errorf("%d records left", n)
	}
	if reqs := srv.requests(); len(reqs) < 2 || reqs[0] != "host:version" || reqs[1] != "host:devices-l" {
		t.Errorf("the server saw %q; want host:version then host:devices-l", reqs)
	}
}

// Decision (plan m1p Q1 ¶2): nothing listening on the recorded port is doubt, and doubt
// must not start a server: the judgment ends at the socket, before any adb process runs.
// The fake adb logs every invocation; there must be none. Nothing is signalled and the
// record is dropped. Seen failing with the listing read by spawning `adb devices` (the
// fake logs a call, and a real adb would start a server).
func TestReconcileStartsNoServer(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listHead})
	st := newReconcileStore(t)
	port, _ := strconv.Atoi(closedPort(t))
	p := launchStandIn(t, st, loadedAVD, port)
	recordsOf(t, st, map[string]*processtest.Proc{loadedAVD: p}, map[string]int{loadedAVD: port})

	log := reconcileWith(t, f, st)
	if syscall.Kill(p.Pid, 0) != nil {
		t.Errorf("a launch was signalled with no adb server to prove anything; log:\n%s", log)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("adb was run %d times with nothing listening: %q", len(calls), calls)
	}
	if n := recordsLeft(t, st); n != 0 {
		t.Errorf("%d records left", n)
	}
}

// Decision (plan m1p Q1 ¶2): a server whose version is not the client's would be restarted
// by the client's commands (and the console query could not be trusted): doubt. The server
// speaks 40, the client 41; nothing is signalled and no `emu avd name` is run. Seen failing
// with the version comparison removed.
func TestReconcileDoubtsAVersionMismatch(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listHead})
	answers(t, f, "emulator-5554", "Some_Other_AVD")
	srv := startAdbServer(t, protocolVersion-1, oneEmulatorOnline, false)
	st := newReconcileStore(t)
	p := launchStandIn(t, st, loadedAVD, srv.port)
	recordsOf(t, st, map[string]*processtest.Proc{loadedAVD: p}, map[string]int{loadedAVD: srv.port})

	log := reconcileWith(t, f, st)
	if syscall.Kill(p.Pid, 0) != nil {
		t.Errorf("signalled across a version mismatch; log:\n%s", log)
	}
	for _, c := range f.calls(t) {
		if strings.Contains(c, "emu") {
			t.Errorf("a console query ran against a mismatched server: %q", c)
		}
	}
	if !strings.Contains(log, "speaks version 40 and the client 41") {
		t.Errorf("the log does not name the mismatch:\n%s", log)
	}
	if n := recordsLeft(t, st); n != 0 {
		t.Errorf("%d records left", n)
	}
}

// Decision (plan m1p Q1 ¶2): a listed serial whose console does not answer might be this
// very AVD, so it is doubt: nothing is signalled. Here the other serial answers another
// AVD but the offline one's `emu avd name` never answers (it hangs until the deadline).
// Seen failing with an unanswered console counted as "not this AVD".
func TestReconcileDoubtsASilentConsole(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listHead, "emulator-5556.avd.hang": ""})
	answers(t, f, "emulator-5554", "Some_Other_AVD")
	srv := startAdbServer(t, protocolVersion, oneEmulatorOnline+oneEmulatorOffline, false)
	st := newReconcileStore(t)
	p := launchStandIn(t, st, loadedAVD, srv.port)
	recordsOf(t, st, map[string]*processtest.Proc{loadedAVD: p}, map[string]int{loadedAVD: srv.port})

	started := time.Now()
	log := reconcileWith(t, f, st)
	if d := time.Since(started); d > process.JudgeTimeout+time.Second {
		t.Errorf("reconcile took %s; the judgment is bounded by %s", d, process.JudgeTimeout)
	}
	if syscall.Kill(p.Pid, 0) != nil {
		t.Errorf("signalled with a console that did not answer; log:\n%s", log)
	}
	if n := recordsLeft(t, st); n != 0 {
		t.Errorf("%d records left", n)
	}
}

// Decision (plan m1p Q1 ¶2): all the adb work of every record shares ONE deadline of
// JudgeTimeout. The server reads the request and never answers; two records name it. Both
// are dropped, nothing is signalled, and the whole reconcile ends within the one deadline
// (a deadline per record would take twice as long). Seen failing with a per-record
// timeout (elapsed ~4 s) and with the read not bounded (the test hangs).
func TestReconcileDoubtsWhenAdbHangs(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listHead})
	srv := startAdbServer(t, protocolVersion, "", true)
	st := newReconcileStore(t)
	a := launchStandIn(t, st, "A_one", srv.port)
	b := launchStandIn(t, st, "A_two", srv.port)
	recordsOf(t, st, map[string]*processtest.Proc{"A_one": a, "A_two": b}, map[string]int{"A_one": srv.port, "A_two": srv.port})

	started := time.Now()
	log := reconcileWith(t, f, st)
	if d := time.Since(started); d > process.JudgeTimeout+500*time.Millisecond {
		t.Errorf("reconcile took %s; every record shares one %s deadline", d, process.JudgeTimeout)
	}
	for name, p := range map[string]*processtest.Proc{"A_one": a, "A_two": b} {
		if syscall.Kill(p.Pid, 0) != nil {
			t.Errorf("%s was signalled while adb hung; log:\n%s", name, log)
		}
	}
	if n := recordsLeft(t, st); n != 0 {
		t.Errorf("%d records left; want both dropped", n)
	}
}

// withConsole listens on a loopback port as an emulator console would, and points the
// judge's console probe at it for the test. The serial it answers for is emulator-<port>.
func withConsole(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	old := consolePorts
	consolePorts = []int{port}
	t.Cleanup(func() { consolePorts = old })
	return port
}

func judgeOne(t *testing.T, f *fixture, avd string, srvPort int) (*processtest.Proc, *store.Store, string) {
	t.Helper()
	st := newReconcileStore(t)
	p := launchStandIn(t, st, avd, srvPort)
	recordsOf(t, st, map[string]*processtest.Proc{avd: p}, map[string]int{avd: srvPort})
	return p, st, reconcileWith(t, f, st)
}

// Decision (plan m1p Q1 ¶2, verifier N3): an empty listing from a server that answers is
// not proof, since a server started moments ago may not have scanned its emulators. The
// judge also asks every console port that accepts a connection: one that names the
// record's AVD is a device, one that does not answer is doubt, one naming another AVD is
// fine. Seen failing with the probe removed (the first two signal the group).
func TestReconcileProbesTheConsolesBeforeSignalling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(t *testing.T, f *fixture, serial string) // how the console's emu avd name behaves
		signal bool
	}{
		{"the console runs the record's AVD", func(t *testing.T, f *fixture, s string) { answers(t, f, s, loadedAVD) }, false},
		{"the console never answers", func(t *testing.T, f *fixture, s string) { devicetest.Write(t, f.adbDir, s+".avd.hang", nil) }, false},
		{"the console runs another AVD", func(t *testing.T, f *fixture, s string) { answers(t, f, s, "Other_AVD") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, map[string]string{"devices.out": listHead})
			tc.answer(t, f, "emulator-"+strconv.Itoa(withConsole(t)))
			srv := startAdbServer(t, protocolVersion, "", false) // the server lists nothing
			p, st, log := judgeOne(t, f, loadedAVD, srv.port)
			if tc.signal {
				select {
				case <-p.Done():
				case <-time.After(3 * time.Second):
					t.Errorf("a console of another AVD stopped the judgment; log:\n%s", log)
				}
			} else if syscall.Kill(p.Pid, 0) != nil {
				t.Errorf("signalled although a console was doubtful; log:\n%s", log)
			}
			if n := recordsLeft(t, st); n != 0 {
				t.Errorf("%d records left", n)
			}
		})
	}
}

// Decision (plan m1p Q1 ¶2, verifier N2): a listed entry with usb: is a physical device and
// cannot be a qemu, so it is skipped rather than counted as a silent console; a listing of
// an emulator of another AVD plus a phone still proves the launch is not a device. Seen
// failing with the skip removed (the phone has no console: doubt, nothing signalled).
func TestReconcileSkipsUsbDevices(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listHead})
	answers(t, f, "emulator-5554", "Some_Other_AVD")
	phone := "abc12345\tdevice usb:1-1 product:oriole model:Pixel_6 transport_id:4\n"
	srv := startAdbServer(t, protocolVersion, oneEmulatorOnline+phone, false)
	p, _, log := judgeOne(t, f, loadedAVD, srv.port)
	select {
	case <-p.Done():
	case <-time.After(3 * time.Second):
		t.Errorf("a usb phone blocked the judgment; log:\n%s", log)
	}
	if !strings.Contains(log, "1 usb skipped") {
		t.Errorf("the log does not say the phone was skipped:\n%s", log)
	}
}

// Decision (plan m1p Q1 ¶2, verifier N5): a record with no AVD proves nothing about which
// listed serial would be its own, so it is doubt, and nothing is read from adb. Seen
// failing with the check removed (the empty listing then says yes).
func TestReconcileDoubtsARecordWithoutAnAVD(t *testing.T) {
	f := newFixture(t, map[string]string{"devices.out": listHead})
	srv := startAdbServer(t, protocolVersion, "", false)
	p, st, log := judgeOne(t, f, "", srv.port)
	if syscall.Kill(p.Pid, 0) != nil {
		t.Errorf("a record with no AVD was signalled; log:\n%s", log)
	}
	if n := recordsLeft(t, st); n != 0 {
		t.Errorf("%d records left", n)
	}
}
