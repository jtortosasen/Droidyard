package emulator

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// JudgeLaunch decides whether a recorded launch of avd, left by a daemon that died before
// its serial resolved, may be signalled by the next daemon's reconcile (plan m1p Q1 ¶2).
// It answers true only on proof: the adb server at port answers over its own socket, its
// version is the client's, and every serial it lists, offline included, answers `emu avd
// name` with another AVD. Anything else is doubt, and doubt returns false with the reason:
// a launch left running is today's limit, a device killed is what ADR-0033 forbids.
//
// It starts no adb server. The listing is read from the server's socket, so nothing
// listening ends the judgment before any adb process runs. The only clients it then runs
// are `adb version` and `adb -s emulator-N emu avd name`, neither of which contacts the
// server (emu talks to the emulator's console directly), so none can auto-start one; do
// not add a command that reaches the server (see ADB.OnPort). A usb: entry in the
// listing is skipped. Before it says yes it also probes every console port that accepts a
// connection, because a freshly started server may not have scanned its emulators yet. ctx carries the one deadline shared by all records.
func (e *Emulator) JudgeLaunch(ctx context.Context, avd string, port int) (bool, string) {
	if avd == "" {
		return false, "the record names no AVD"
	}
	version, listed, err := device.ServerListing(ctx, port)
	if err != nil {
		return false, fmt.Sprintf("no adb server proved at port %d: %v", port, err)
	}
	build := e.adb.ClientBuild(ctx)
	switch {
	case build == "":
		return false, "the adb client's version could not be read"
	case build != version:
		return false, fmt.Sprintf("the adb server at port %d speaks version %s and the client %s", port, version, build)
	}
	onPort := e.adb.OnPort(port)
	skipped := 0
	for _, d := range listed {
		if d.USB() {
			// A usb: entry is a physical device; it cannot be a qemu. A host:port serial
			// can be an emulator reached over TCP, so it is asked like any other.
			skipped++
			continue
		}
		switch name := onPort.AvdName(ctx, d.Device); name {
		case "":
			return false, fmt.Sprintf("the console of %s did not answer", d.Device)
		case avd:
			return false, fmt.Sprintf("%s is running %s: a device, not a launch", d.Device, avd)
		}
	}
	// A server started moments ago may not have scanned its emulators yet, so an empty or
	// short listing is not proof: ask every console that accepts a connection directly.
	if ok, reason := probeConsoles(ctx, onPort, avd); !ok {
		return false, reason
	}
	return true, fmt.Sprintf("adb at port %d lists %d serial(s) (%d usb skipped), none running %s, and no console answered it", port, len(listed), skipped, avd)
}

// consolePorts are the ports an emulator's console can listen on: even, 5554 to 5584. A
// variable so a test can point the probe at a console of its own.
var consolePorts = func() []int {
	var ports []int
	for p := 5554; p <= 5584; p += 2 {
		ports = append(ports, p)
	}
	return ports
}()

// consoleDial bounds one connection attempt to a console port.
const consoleDial = 300 * time.Millisecond

// probeConsoles asks, concurrently, `emu avd name` of every console port that accepts a
// TCP connection on 127.0.0.1. A console that answers avd, or that does not answer, is
// doubt; one that names another AVD is fine. All of it is bound by ctx.
func probeConsoles(ctx context.Context, onPort *device.ADB, avd string) (bool, string) {
	type result struct {
		port int
		name string
		open bool
	}
	ports := consolePorts
	out := make(chan result, len(ports))
	for _, p := range ports {
		go func() {
			d := net.Dialer{Timeout: consoleDial}
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
			if err != nil {
				out <- result{port: p}
				return
			}
			c.Close()
			out <- result{p, onPort.AvdName(ctx, "emulator-"+strconv.Itoa(p)), true}
		}()
	}
	for range ports {
		r := <-out
		switch {
		case !r.open:
		case r.name == "":
			return false, fmt.Sprintf("the console on port %d accepts connections but did not name its AVD", r.port)
		case r.name == avd:
			return false, fmt.Sprintf("the console on port %d is running %s: a device, not a launch", r.port, avd)
		}
	}
	return true, ""
}

// SetConsolePortsForTest replaces the console ports the judge probes and returns a
// function that restores them. Tests only: a re-executed test daemon uses it so its
// reconcile does not depend on emulators running on the machine.
func SetConsolePortsForTest(ports []int) (restore func()) {
	old := consolePorts
	consolePorts = ports
	return func() { consolePorts = old }
}
