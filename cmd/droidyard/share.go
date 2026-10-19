package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/sharing"
	"github.com/jtortosasen/droidyard/internal/policy"
)

const (
	shareUsage   = "usage: droidyard share -d <serial> [--on vpn|lan|<ip>]"
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
		fmt.Fprintf(stderr, "droidyard %s: %s: %s\n", verb, e.Code, e.Message)
		if e.Cause != "" {
			fmt.Fprintf(stderr, "Cause: %s\n", e.Cause)
		}
		if e.NextStep != "" {
			fmt.Fprintf(stderr, "Next: %s\n", e.NextStep)
		}
		return 1, false
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		fmt.Fprintf(stderr, "droidyard %s: the daemon's answer is not readable: %v\n", verb, err)
		return 1, false
	}
	return 0, true
}

// shareCmd implements `droidyard share -d <serial> [--on vpn|lan|<ip>]` (ADR-0108): it
// admits the serial, lends it and listens for the join, all in the daemon, and prints the
// device, the addresses, what the person must know and, last, the line to paste on the
// other Mac.
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
	if serial == "" {
		return usageError(stderr, "share", shareUsage, fmt.Errorf("no device serial"))
	}
	if strings.TrimSpace(serial) == "" || strings.ContainsAny(serial, " \t\r\n") {
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
	if !detach {
		return usageError(stderr, "share", shareUsage, fmt.Errorf("this version shares detached only: add -d"))
	}

	var res sharing.ShareResult
	if exit, ok := humanCall("share", sharing.CapShare, sharing.ShareParams{Device: serial, Detach: true, On: on}, &res, stderr); !ok {
		return exit
	}
	fmt.Fprintf(stdout, "sharing %s until `droidyard unshare %s`\n", res.Device, res.Device)
	var at []string
	for _, a := range res.Addresses {
		at = append(at, a.Address+" ("+a.Kind+")")
	}
	fmt.Fprintf(stdout, "listening on %s\n", strings.Join(at, ", "))
	for _, n := range res.Notes {
		fmt.Fprintln(stdout, n)
	}
	// Last, on a line of its own: the line the other person pastes.
	fmt.Fprintf(stdout, "droidyard join %s\n", res.Code)
	return 0
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
	return 0
}
