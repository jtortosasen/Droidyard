package main

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/sharing"
)

func timedOut(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// TestSignalBeforeTheConnectionIsSetClosesIt: a Ctrl-C that lands after the dial and before the
// connection is handed to the share closed nothing (there was none), and the signal handler
// has returned, so the share would be made and kept. set acts on a signal it missed: the
// connection is closed at once (ADR-0108 §3). Seen failing with the check in set removed: the
// connection stays open.
func TestSignalBeforeTheConnectionIsSetClosesIt(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	sc := &shareConn{}
	sc.signalled.Store(true)
	sc.close() // the handler's close, with no connection yet
	sc.set(client, bufio.NewReader(client))

	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := server.Read(make([]byte, 1)); err == nil || timedOut(err) {
		t.Errorf("the connection set after a signal is still open (read: %v); want it closed", err)
	}
	// A connection set with no signal is left alone.
	c2, s2 := net.Pipe()
	defer s2.Close()
	defer c2.Close()
	live := &shareConn{}
	live.set(c2, bufio.NewReader(c2))
	s2.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := s2.Read(make([]byte, 1)); err == nil || !timedOut(err) {
		t.Errorf("a connection set with no signal was closed (read: %v)", err)
	}
}

// TestCandidateLineCleansTheDevicesText: the serial and the state a device reports end up on a
// terminal, so control characters are removed from both, as the model's are already.
// Seen failing with printable removed: the escape sequence is printed.
func TestCandidateLineCleansTheDevicesText(t *testing.T) {
	line := candidateLine(sharing.Candidate{Device: "R5\x1b[2J8M", Transport: "usb", Model: "Pixel", Reason: "adb reports the device as bad\x1b]0;x\x07state"})
	if strings.ContainsAny(line, "\x1b\x07") {
		t.Errorf("the candidate line holds control characters: %q", line)
	}
	if !strings.Contains(line, "R5[2J8M") {
		t.Errorf("the serial is not in the line: %q", line)
	}
}
