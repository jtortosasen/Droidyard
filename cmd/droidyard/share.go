package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unicode"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/sharing"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
)

const (
	shareUsage   = "usage: droidyard share [<serial>] [-d] [--on vpn|lan|<ip>]"
	unshareUsage = "usage: droidyard unshare [<serial>]"
	joinUsage    = "usage: droidyard join <code>"
)

// humanCall is one call of a human-only capability through the daemon, which it starts when
// none answers (ADR-0108 §8): the share and the code it answers live in the daemon. A
// failure is printed with its cause and next step and is the exit status; ok says the result
// holds the answer.
func humanCall(verb, name string, params any, result any, stderr io.Writer) (exit int, ok bool) {
	raw, err := json.Marshal(params)
	if err != nil {
		fmt.Fprintf(stderr, "droidyard %s: %v\n", verb, err)
		return 1, false
	}
	conn, r, socket, err := connectOrStart(daemon.Hello{Client: capability.ClientCLI}, true, nil)
	if err != nil {
		reportDialError(verb, socket, err)
		return 1, false
	}
	defer conn.Close()
	env, err := runCall(conn, r, cli.Call{Capability: name, Params: raw})
	if err != nil {
		fmt.Fprintf(stderr, "droidyard %s: %v\n", verb, err)
		return 1, false
	}
	if e := env.Error; e != nil {
		printCallError(stderr, verb, e)
		return 1, false
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		fmt.Fprintf(stderr, "droidyard %s: the daemon's answer is not readable: %v\n", verb, err)
		return 1, false
	}
	return 0, true
}

// shareCmd implements `droidyard share [<serial>] [-d] [--on vpn|lan|<ip>]` (ADR-0108):
// with no serial it lists the physical devices and shares the only one that can be; with one
// it admits the serial, lends it and listens for the join, all in the daemon, and prints the
// device, the addresses, what the person must know and, last, the line to paste on the other
// Mac. Without -d the share belongs to this command's connection: it prints who joins and
// leaves until Ctrl-C (or a closed terminal, or SIGTERM), which ends the share by closing
// the connection. With -d it returns and the share lasts until `droidyard unshare`.
func shareCmd(args []string, stdout, stderr io.Writer) int {
	var serial, on string
	detach, haveOn := false, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-d" || a == "--detach":
			detach = true
		case a == "--on" || strings.HasPrefix(a, "--on="):
			if haveOn {
				return usageError(stderr, "share", shareUsage, fmt.Errorf("--on is given twice"))
			}
			haveOn = true
			if v, ok := strings.CutPrefix(a, "--on="); ok {
				on = v
				break
			}
			if i+1 >= len(args) {
				return usageError(stderr, "share", shareUsage, fmt.Errorf("--on needs vpn, lan or an IP address"))
			}
			i++
			on = args[i]
		case strings.HasPrefix(a, "-"):
			return usageError(stderr, "share", shareUsage, fmt.Errorf("unknown option %s", a))
		case serial == "":
			serial = a
		default:
			return usageError(stderr, "share", shareUsage, fmt.Errorf("unexpected argument %q", a))
		}
	}
	if serial != "" && (strings.TrimSpace(serial) == "" || strings.ContainsAny(serial, " \t\r\n")) {
		return invalid(stderr, "share", fmt.Errorf("%q is not a device serial", serial))
	}
	if haveOn {
		if on == "" {
			return usageError(stderr, "share", shareUsage, fmt.Errorf("--on needs vpn, lan or an IP address"))
		}
		if err := policy.ValidOn(on); err != nil {
			return invalid(stderr, "share", fmt.Errorf("--on: %w", err))
		}
	}

	// Installed before the daemon is reached, so a Ctrl-C is never lost to the default action:
	// it closes the connection, and closing it is what ends a foreground share.
	sc := &shareConn{}
	interrupt := make(chan struct{})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	defer close(done)
	defer signal.Stop(sigs)
	go func() {
		select {
		case <-sigs:
			sc.signalled.Store(true)
			close(interrupt)
			sc.close()
		case <-done:
		}
	}()

	conn, r, socket, err := connectOrStart(daemon.Hello{Client: capability.ClientCLI}, true, interrupt)
	if errors.Is(err, errInterrupted) {
		fmt.Fprintln(stderr, "droidyard share:", err)
		return 1
	}
	if err != nil {
		reportDialError("share", socket, err)
		return 1
	}
	sc.set(conn, r)
	defer sc.close()

	if serial == "" {
		picked, exit, ok := sc.pickDevice(stdout, stderr)
		if !ok {
			return exit
		}
		serial = picked
	}

	var res sharing.ShareResult
	if exit, ok := sc.result("share", sharing.CapShare, sharing.ShareParams{Device: serial, Detach: detach, On: on}, &res, stderr); !ok {
		return exit
	}
	switch {
	case res.Mode == sharing.ModeDetached && detach:
		fmt.Fprintf(stdout, "sharing %s until `droidyard unshare %s`\n", res.Device, res.Device)
	case res.Mode == sharing.ModeDetached:
		fmt.Fprintf(stdout, "%s is already shared detached, until `droidyard unshare %s`: a fresh code for the same share\n", res.Device, res.Device)
	case res.Owned:
		fmt.Fprintf(stdout, "sharing %s while this command runs: Ctrl-C stops sharing\n", res.Device)
	default:
		fmt.Fprintf(stdout, "%s is already shared in the foreground of another command: a fresh code for the same share\n", res.Device)
	}
	var at []string
	for _, a := range res.Addresses {
		at = append(at, a.Address+" ("+a.Kind+")")
	}
	fmt.Fprintf(stdout, "listening on %s\n", strings.Join(at, ", "))
	for _, n := range res.Notes {
		fmt.Fprintln(stdout, n)
	}
	// On a line of its own: the line the other person pastes.
	fmt.Fprintf(stdout, "droidyard join %s\n", res.Code)
	if !res.Owned {
		return 0
	}
	return sc.watch(res.Device, stdout, stderr)
}

// shareConn is the one connection a `droidyard share` holds: a foreground share lives exactly
// as long as it does, so every request goes over it and a signal closes it.
type shareConn struct {
	mu        sync.Mutex
	conn      net.Conn
	r         *bufio.Reader
	seq       int
	signalled atomic.Bool
}

func (s *shareConn) set(conn net.Conn, r *bufio.Reader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn, s.r = conn, r
	// A signal that came after the dial and before this closed nothing, there being no
	// connection yet to close: it is acted on now, and the share is never made.
	if s.signalled.Load() {
		conn.Close()
	}
}

func (s *shareConn) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.conn.Close()
	}
}

// call sends one request and reads its response. The connection carries no other request, so
// the next line is the answer.
func (s *shareConn) call(name string, params any, waitMs *int64) (capability.ResponseEnvelope, error) {
	var env capability.ResponseEnvelope
	raw, err := json.Marshal(params)
	if err != nil {
		return env, err
	}
	s.seq++
	id := fmt.Sprintf("share-%d", s.seq)
	line, err := json.Marshal(capability.RequestEnvelope{ID: id, Capability: name, Params: raw, WaitMs: waitMs})
	if err != nil {
		return env, err
	}
	if _, err := s.conn.Write(append(line, '\n')); err != nil {
		return env, fmt.Errorf("sending the request: %w", err)
	}
	for {
		b, err := s.r.ReadBytes('\n')
		if err != nil {
			return env, fmt.Errorf("the daemon closed the connection before answering: %w", err)
		}
		if err := json.Unmarshal(b, &env); err != nil {
			return env, fmt.Errorf("the daemon's answer is not an envelope: %w", err)
		}
		if env.ID == id {
			return env, nil
		}
	}
}

// result is call for a capability that answers at once: the error the daemon answered with is
// printed and is the exit status; ok says result holds the answer.
func (s *shareConn) result(verb, name string, params, result any, stderr io.Writer) (exit int, ok bool) {
	env, err := s.call(name, params, nil)
	if err != nil {
		if s.signalled.Load() {
			fmt.Fprintf(stderr, "droidyard %s: interrupted\n", verb)
			return 1, false
		}
		fmt.Fprintf(stderr, "droidyard %s: %v\n", verb, err)
		return 1, false
	}
	if env.Error != nil {
		printCallError(stderr, verb, env.Error)
		return 1, false
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		fmt.Fprintf(stderr, "droidyard %s: the daemon's answer is not readable: %v\n", verb, err)
		return 1, false
	}
	return 0, true
}

// pickDevice lists the physical devices and returns the one to share: the only one that can be.
// Several leave the list and exit 2; none exit 1; nothing is written in either case.
func (s *shareConn) pickDevice(stdout, stderr io.Writer) (serial string, exit int, ok bool) {
	var res sharing.ShareResult
	if exit, ok := s.result("share", sharing.CapShare, sharing.ShareParams{}, &res, stderr); !ok {
		return "", exit, false
	}
	var shareable []sharing.Candidate
	for _, c := range res.Candidates {
		fmt.Fprintln(stdout, candidateLine(c))
		if c.Shareable {
			shareable = append(shareable, c)
		}
	}
	switch len(shareable) {
	case 0:
		if len(res.Candidates) == 0 {
			fmt.Fprintln(stderr, "droidyard share: no physical device is connected")
		} else {
			fmt.Fprintln(stderr, "droidyard share: no device can be shared as it is: see the reasons above")
		}
		fmt.Fprintln(stderr, "Plug the phone in and accept the debugging prompt on it, then share again. Emulators are not shared.")
		return "", 1, false
	case 1:
		fmt.Fprintf(stdout, "only one device can be shared: %s\n", shareable[0].Device)
		return shareable[0].Device, 0, true
	}
	fmt.Fprintln(stderr, "droidyard share: more than one device can be shared; name one: droidyard share <serial>")
	return "", 2, false
}

// candidateLine is one device of the listing: `<transport>  <manufacturer model>  <serial>`,
// and the reason when it cannot be shared. The words are the device's own text, cleaned by
// the daemon.
func candidateLine(c sharing.Candidate) string {
	name := strings.TrimSpace(c.Manufacturer + " " + c.Model)
	if name == "" {
		name = "-"
	}
	// The serial and the state in the reason are the device's own text, as the model is.
	line := c.Transport + "  " + name + "  " + printable(c.Device)
	if !c.Shareable && c.Reason != "" {
		line += "  (" + printable(c.Reason) + ")"
	}
	return line
}

// printable is s without control characters, which a terminal would act on.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// printCallError prints an error envelope the way every human command does.
func printCallError(stderr io.Writer, verb string, e *capability.Error) {
	fmt.Fprintf(stderr, "droidyard %s: %s: %s\n", verb, e.Code, e.Message)
	if e.Cause != "" {
		fmt.Fprintf(stderr, "Cause: %s\n", e.Cause)
	}
	if e.NextStep != "" {
		fmt.Fprintf(stderr, "Next: %s\n", e.NextStep)
	}
}

// watchPoll is how long each operation.wait of the foreground watch waits.
const watchPoll = 1000

// watch holds the foreground share open: it starts hosting.watchShare and waits on it with
// operation.wait, printing who joined, connected and left, until the share ends or the
// connection does. A signal closes the connection, which ends the share (no unshare is
// sent: a `host lend` entry made for the serial meanwhile survives).
func (s *shareConn) watch(serial string, stdout, stderr io.Writer) int {
	zero := int64(0)
	env, err := s.call(sharing.CapWatchShare, sharing.WatchShareParams{Device: serial}, &zero)
	if err != nil {
		return s.lost(serial, stdout, stderr, err)
	}
	if env.Error != nil {
		printCallError(stderr, "share", env.Error)
		return 1
	}
	if env.Operation == nil {
		fmt.Fprintln(stderr, "droidyard share: the daemon started no watch of the share")
		return 1
	}
	opID := env.Operation.ID
	peers := map[string]bool{}
	for {
		timeout := int64(watchPoll)
		env, err := s.call(capability.OperationWait, capability.WaitParams{OperationID: opID, TimeoutMs: &timeout}, nil)
		if err != nil {
			return s.lost(serial, stdout, stderr, err)
		}
		if env.Error != nil {
			printCallError(stderr, "share", env.Error)
			return 1
		}
		if env.Operation == nil {
			continue
		}
		if p := env.Operation.Progress; p != nil && p.Message != "" {
			reportPeers(stdout, peers, sharing.ParsePeers(p.Message))
		}
		if !env.Operation.State.Terminal() {
			continue
		}
		var res sharing.WatchShareResult
		if json.Unmarshal(env.Result, &res) != nil {
			fmt.Fprintln(stderr, "droidyard share: the watch of the share ended without saying why")
			return 1
		}
		switch res.Ended {
		case hosting.ShareUnshared:
			fmt.Fprintf(stdout, "%s was unshared from another command: no longer sharing\n", serial)
		case hosting.ShareStopping:
			fmt.Fprintf(stdout, "the daemon is stopping: %s is no longer shared\n", serial)
		default:
			fmt.Fprintf(stdout, "%s is no longer shared\n", serial)
		}
		return 0
	}
}

// lost is the end of the connection under a watch: ours, by a signal, or the daemon's.
func (s *shareConn) lost(serial string, stdout, stderr io.Writer, err error) int {
	if s.signalled.Load() {
		fmt.Fprintf(stdout, "stopped sharing %s\n", serial)
		return 0
	}
	fmt.Fprintf(stderr, "droidyard share: %v; %s is no longer shared\n", err, serial)
	return 1
}

// reportPeers prints what changed between the peers last seen and now: a name that appears
// joined, one whose link comes up connected, one whose link goes down or that is unpaired
// left. seen is updated.
func reportPeers(out io.Writer, seen map[string]bool, now []hosting.PeerHealth) {
	present := map[string]bool{}
	for _, p := range now {
		present[p.Name] = true
		was, known := seen[p.Name]
		switch {
		case !known:
			fmt.Fprintf(out, "%s joined\n", p.Name)
			if p.Connected {
				fmt.Fprintf(out, "%s connected\n", p.Name)
			}
		case p.Connected && !was:
			fmt.Fprintf(out, "%s connected\n", p.Name)
		case !p.Connected && was:
			fmt.Fprintf(out, "%s left\n", p.Name)
		}
		seen[p.Name] = p.Connected
	}
	var gone []string
	for name, was := range seen {
		if !present[name] {
			gone = append(gone, name)
			if was {
				fmt.Fprintf(out, "%s left\n", name)
			}
		}
	}
	for _, name := range gone {
		delete(seen, name)
	}
}

// unshareCmd implements `droidyard unshare [<serial>]` (ADR-0108 §4).
func unshareCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		return usageError(stderr, "unshare", unshareUsage)
	}
	var serial string
	if len(args) == 1 {
		serial = args[0]
		if strings.HasPrefix(serial, "-") || strings.TrimSpace(serial) == "" || strings.ContainsAny(serial, " \t\r\n") {
			return invalid(stderr, "unshare", fmt.Errorf("%q is not a device serial", serial))
		}
	}
	var res sharing.UnshareResult
	if exit, ok := humanCall("unshare", sharing.CapUnshare, sharing.UnshareParams{Device: serial}, &res, stderr); !ok {
		return exit
	}
	switch {
	case len(res.Unshared) > 0:
		fmt.Fprintf(stdout, "no longer sharing %s: its unused codes are void and those who joined reach nothing\n", strings.Join(res.Unshared, ", "))
	case serial != "":
		fmt.Fprintf(stdout, "%s was not shared\n", serial)
	default:
		fmt.Fprintln(stdout, "nothing was shared")
	}
	return 0
}

// joinCmd implements `droidyard join <code>` (ADR-0107): the whole pairing, one paste.
func joinCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return usageError(stderr, "join", joinUsage)
	}
	var res sharing.JoinResult
	if exit, ok := humanCall("join", sharing.CapJoin, sharing.JoinParams{Code: args[0]}, &res, stderr); !ok {
		return exit
	}
	if res.Connected {
		fmt.Fprintf(stdout, "joined %s: %s is listed now\n", res.Remote, res.Device)
	} else {
		fmt.Fprintf(stdout, "joined %s: %s is listed once the link comes up\n", res.Remote, res.Device)
	}
	for _, n := range res.Notes {
		fmt.Fprintln(stdout, n)
	}
	return 0
}
