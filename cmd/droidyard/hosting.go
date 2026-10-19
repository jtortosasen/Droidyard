package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

const (
	idUsage     = "usage: droidyard id"
	hostUsage   = "usage: droidyard host admit <name> <fingerprint> | revoke <name> | lend <serial> --to <name>[,<name>] | unlend <serial> | listen <ip:port>|off | list"
	remoteUsage = "usage: droidyard remote add <name> <ip:port> <fingerprint> | remove <name> | list"
)

// lendWarning is what lending a device means (ADR-0099 §4), said each time one is lent.
const lendWarning = "The whole device is lent: its screen and input reach every app, notification and account on it, and nothing is cleaned when a lease ends. Lend a dedicated device, not your own phone."

// idCmd implements `droidyard id` (ADR-0098 §1): this daemon's certificate fingerprint,
// what a host pins to admit it, read from system.health. The identity is the daemon's, so
// it starts one when none answers, as `call` does.
func idCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, idUsage)
		return 2
	}
	conn, r, socket, err := connectOrStart(daemon.Hello{Client: capability.ClientCLI}, true, nil)
	if err != nil {
		reportDialError("id", socket, err)
		return 1
	}
	defer conn.Close()
	env, err := runCall(conn, r, cli.Call{Capability: "system.health"})
	if err != nil {
		fmt.Fprintln(stderr, "droidyard id:", err)
		return 1
	}
	if env.Error != nil {
		fmt.Fprintf(stderr, "droidyard id: %s: %s\n", env.Error.Code, env.Error.Message)
		return 1
	}
	var h system.Health
	if err := json.Unmarshal(env.Result, &h); err != nil {
		fmt.Fprintln(stderr, "droidyard id: the daemon's health is not readable:", err)
		return 1
	}
	if h.Identity == nil {
		fmt.Fprintf(stderr, "droidyard id: the daemon has no peer identity; see its log, %s, next to its lock\n", logFile)
		return 1
	}
	fmt.Fprintln(stdout, h.Identity.Fingerprint)
	return 0
}

// hostCmd implements `droidyard host …` (ADR-0098 §7), the host's side of pairing and
// lending. Like `trust`, it writes the user configuration and starts no daemon: a running
// daemon follows `peers` and `lend` at each request and `listen` within seconds.
func hostCmd(args []string, stdout, stderr io.Writer) int {
	return hostWith(policy.New(platform.Current()), args, stdout, stderr)
}

// remoteCmd implements `droidyard remote …` (ADR-0098 §7), the client's side of pairing.
func remoteCmd(args []string, stdout, stderr io.Writer) int {
	return remoteWith(policy.New(platform.Current()), args, stdout, stderr)
}

// edit is what a verb decides from the policy it read: the patch to write (nil when the
// policy already says it) and what to tell the human.
type edit struct {
	patch map[string]any
	said  []string
}

func hostWith(pol *policy.Policy, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, hostUsage)
		return 2
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "admit":
		if len(rest) != 2 {
			return usageError(stderr, "host admit", hostUsage)
		}
		name, fp := rest[0], rest[1]
		if err := validPair(name, fp); err != nil {
			return invalid(stderr, "host admit", err)
		}
		return apply(pol, "host admit", stdout, stderr, func(cur policy.GetResult) (edit, error) {
			peers := cur.Hosting.Peers
			for _, p := range peers {
				switch {
				case p.Name == name && p.Fingerprint == fp:
					return edit{said: []string{name + " is already admitted"}}, nil
				case p.Name == name:
					return edit{}, fmt.Errorf("%s is admitted with another fingerprint; revoke it first (droidyard host revoke %s)", name, name)
				case p.Fingerprint == fp:
					return edit{}, fmt.Errorf("that fingerprint is admitted as %s", p.Name)
				}
			}
			next := append(slices.Clone(peers), policy.HostPeer{Name: name, Fingerprint: fp})
			said := []string{"admitted " + name}
			// A name lent to before it was admitted takes those devices with it.
			if lent := lentTo(cur.Hosting.Lend, name); len(lent) > 0 {
				said = append(said, name+" is already lent "+strings.Join(lent, ", ")+" and reaches it now; droidyard host unlend or revoke takes it away")
			} else {
				said = append(said, "It reaches the devices you lend it: droidyard host lend <serial> --to "+name)
			}
			return edit{patch: map[string]any{"hosting": map[string]any{"peers": next}}, said: said}, nil
		})
	case "revoke":
		if len(rest) != 1 {
			return usageError(stderr, "host revoke", hostUsage)
		}
		name := rest[0]
		if err := policy.ValidName("name", name); err != nil {
			return invalid(stderr, "host revoke", err)
		}
		return apply(pol, "host revoke", stdout, stderr, func(cur policy.GetResult) (edit, error) {
			admitted := slices.ContainsFunc(cur.Hosting.Peers, func(p policy.HostPeer) bool { return p.Name == name })
			lent := lentTo(cur.Hosting.Lend, name)
			if !admitted && len(lent) == 0 {
				return edit{}, fmt.Errorf("%s is not an admitted peer and is lent nothing", name)
			}
			hosting := map[string]any{}
			var said []string
			if admitted {
				hosting["peers"] = slices.DeleteFunc(slices.Clone(cur.Hosting.Peers), func(p policy.HostPeer) bool { return p.Name == name })
				said = append(said, "revoked "+name+": its links close within 2 s")
			} else {
				said = append(said, name+" was not admitted")
			}
			// A name left in `lend` would lend to whoever is admitted under it next, so it goes
			// even when the peer was never admitted.
			if len(lent) > 0 {
				lend, _ := withoutPeer(cur.Hosting.Lend, name)
				hosting["lend"] = lend
				said = append(said, "removed "+name+" from what you lend: "+strings.Join(lent, ", "))
			}
			return edit{patch: map[string]any{"hosting": hosting}, said: said}, nil
		})
	case "lend":
		serial, names, err := lendArgs(rest)
		if err != nil {
			return usageError(stderr, "host lend", hostUsage, err)
		}
		for _, n := range names {
			if err := policy.ValidName("--to", n); err != nil {
				return invalid(stderr, "host lend", err)
			}
		}
		return apply(pol, "host lend", stdout, stderr, func(cur policy.GetResult) (edit, error) {
			lend := slices.Clone(cur.Hosting.Lend)
			entry := policy.Lend{Device: serial, To: names}
			if i := slices.IndexFunc(lend, func(l policy.Lend) bool { return l.Device == serial }); i >= 0 {
				lend[i] = entry
			} else {
				lend = append(lend, entry)
			}
			said := []string{fmt.Sprintf("lent %s to %s", serial, strings.Join(names, ", ")), lendWarning}
			for _, n := range names {
				if !slices.ContainsFunc(cur.Hosting.Peers, func(p policy.HostPeer) bool { return p.Name == n }) {
					said = append(said, n+" is not an admitted peer yet: droidyard host admit "+n+" <fingerprint>")
				}
			}
			if strings.HasPrefix(serial, "emulator-") {
				said = append(said, serial+" is an emulator: only physical devices are lent (ADR-0099 §1), so it is not.")
			}
			// Lending never widens the host's own physical policy (ADR-0099 §1).
			if !cur.Devices.AllowPhysical || !slices.Contains(cur.Devices.Allowlist, serial) {
				said = append(said, "Not lent yet: devices.allowPhysical must be true and "+serial+" in devices.allowlist (your own physical-device policy).")
			}
			return edit{patch: map[string]any{"hosting": map[string]any{"lend": lend}}, said: said}, nil
		})
	case "unlend":
		if len(rest) != 1 || rest[0] == "" {
			return usageError(stderr, "host unlend", hostUsage)
		}
		serial := rest[0]
		return apply(pol, "host unlend", stdout, stderr, func(cur policy.GetResult) (edit, error) {
			if !slices.ContainsFunc(cur.Hosting.Lend, func(l policy.Lend) bool { return l.Device == serial }) {
				return edit{}, fmt.Errorf("%s is not lent", serial)
			}
			lend := slices.DeleteFunc(slices.Clone(cur.Hosting.Lend), func(l policy.Lend) bool { return l.Device == serial })
			return edit{
				patch: map[string]any{"hosting": map[string]any{"lend": lend}},
				said:  []string{"no longer lending " + serial + ": its holder's streams stop within 2 s"},
			}, nil
		})
	case "listen":
		if len(rest) != 1 {
			return usageError(stderr, "host listen", hostUsage)
		}
		addr := rest[0]
		if addr != "off" {
			if err := policy.ValidListen(addr); err != nil {
				return invalid(stderr, "host listen", fmt.Errorf("listen: %w", err))
			}
		}
		return apply(pol, "host listen", stdout, stderr, func(cur policy.GetResult) (edit, error) {
			const live = "A running daemon applies it within seconds."
			if addr == "off" {
				if cur.Hosting.Listen == "" {
					return edit{said: []string{"hosting.listen is already off"}}, nil
				}
				said := "hosting.listen is off"
				if cur.Hosting.On != "" {
					said += "; the listener follows hosting.on (" + cur.Hosting.On + ") while a device is lent"
				}
				return edit{patch: map[string]any{"hosting": map[string]any{"listen": nil}}, said: []string{said, live}}, nil
			}
			if cur.Hosting.Listen == addr {
				return edit{said: []string{"the peer listener is already set to " + addr}}, nil
			}
			return edit{patch: map[string]any{"hosting": map[string]any{"listen": addr}}, said: []string{"the peer listener binds " + addr, live}}, nil
		})
	case "list":
		if len(rest) != 0 {
			return usageError(stderr, "host list", hostUsage)
		}
		return show(pol, "host list", stdout, stderr, func(cur policy.GetResult) {
			h := cur.Hosting
			if h.Listen == "" {
				fmt.Fprintln(stdout, "listen: off")
			} else {
				fmt.Fprintln(stdout, "listen:", h.Listen)
			}
			fmt.Fprintf(stdout, "peers: %d\n", len(h.Peers))
			for _, p := range h.Peers {
				fmt.Fprintf(stdout, "  %s  %s\n", p.Name, p.Fingerprint)
			}
			fmt.Fprintf(stdout, "lent: %d\n", len(h.Lend))
			for _, l := range h.Lend {
				fmt.Fprintf(stdout, "  %s to %s\n", l.Device, strings.Join(l.To, ", "))
			}
		})
	default:
		return usageError(stderr, "host", hostUsage, fmt.Errorf("unknown verb %q", verb))
	}
}

func remoteWith(pol *policy.Policy, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, remoteUsage)
		return 2
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "add":
		if len(rest) != 3 {
			return usageError(stderr, "remote add", remoteUsage)
		}
		name, addr, fp := rest[0], rest[1], rest[2]
		if err := validPair(name, fp); err != nil {
			return invalid(stderr, "remote add", err)
		}
		if err := policy.ValidRemoteAddress(addr); err != nil {
			return invalid(stderr, "remote add", fmt.Errorf("address: %w", err))
		}
		return apply(pol, "remote add", stdout, stderr, func(cur policy.GetResult) (edit, error) {
			for _, r := range cur.Remotes {
				switch {
				case r.Name == name && r.Address == addr && r.Fingerprint == fp:
					return edit{said: []string{name + " is already a remote"}}, nil
				case r.Name == name:
					return edit{}, fmt.Errorf("%s is a remote already, with another address or fingerprint; remove it first (droidyard remote remove %s)", name, name)
				case r.Fingerprint == fp:
					return edit{}, fmt.Errorf("that fingerprint is the remote %s", r.Name)
				}
			}
			next := append(slices.Clone(cur.Remotes), policy.Remote{Name: name, Address: addr, Fingerprint: fp})
			return edit{
				patch: map[string]any{"remotes": next},
				said: []string{
					"added the remote " + name,
					"A running daemon dials it within seconds.",
					"A phone it lends is used once devices.allowPhysical is true and " + name + "/<serial> is in devices.allowlist.",
				},
			}, nil
		})
	case "remove":
		if len(rest) != 1 {
			return usageError(stderr, "remote remove", remoteUsage)
		}
		name := rest[0]
		if err := policy.ValidName("name", name); err != nil {
			return invalid(stderr, "remote remove", err)
		}
		return apply(pol, "remote remove", stdout, stderr, func(cur policy.GetResult) (edit, error) {
			if !slices.ContainsFunc(cur.Remotes, func(r policy.Remote) bool { return r.Name == name }) {
				return edit{}, fmt.Errorf("%s is not a remote", name)
			}
			next := slices.DeleteFunc(slices.Clone(cur.Remotes), func(r policy.Remote) bool { return r.Name == name })
			return edit{
				patch: map[string]any{"remotes": next},
				said:  []string{"removed the remote " + name, "A running daemon closes its link within seconds."},
			}, nil
		})
	case "list":
		if len(rest) != 0 {
			return usageError(stderr, "remote list", remoteUsage)
		}
		return show(pol, "remote list", stdout, stderr, func(cur policy.GetResult) {
			fmt.Fprintf(stdout, "remotes: %d\n", len(cur.Remotes))
			for _, r := range cur.Remotes {
				fmt.Fprintf(stdout, "  %s  %s  %s\n", r.Name, r.Address, r.Fingerprint)
			}
		})
	default:
		return usageError(stderr, "remote", remoteUsage, fmt.Errorf("unknown verb %q", verb))
	}
}

// validPair checks a name and a fingerprint with the rules the configuration applies to
// them, before anything is read or written.
func validPair(name, fp string) error {
	if err := policy.ValidName("name", name); err != nil {
		return err
	}
	return policy.ValidFingerprint("fingerprint", fp)
}

// lendArgs reads `<serial> --to <name>[,<name>]`, the flag before or after the serial.
func lendArgs(args []string) (serial string, names []string, err error) {
	var to string
	haveTo := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--to" || strings.HasPrefix(a, "--to="):
			if haveTo {
				return "", nil, fmt.Errorf("--to is given twice; name every peer in one, comma separated")
			}
			haveTo = true
			if a != "--to" {
				to = strings.TrimPrefix(a, "--to=")
				break
			}
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--to needs the peer names")
			}
			i++
			to = args[i]
		case strings.HasPrefix(a, "-"):
			return "", nil, fmt.Errorf("unknown option %s", a)
		case serial == "":
			serial = a
		default:
			return "", nil, fmt.Errorf("unexpected argument %q", a)
		}
	}
	if serial == "" {
		return "", nil, fmt.Errorf("no device serial")
	}
	if !haveTo || to == "" {
		return "", nil, fmt.Errorf("--to names the peers the device is lent to")
	}
	names = strings.Split(to, ",")
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			return "", nil, fmt.Errorf("%q is named twice in --to", n)
		}
		seen[n] = true
	}
	return serial, names, nil
}

// lentTo is the serials lent to name.
func lentTo(lend []policy.Lend, name string) []string {
	var out []string
	for _, l := range lend {
		if slices.Contains(l.To, name) {
			out = append(out, l.Device)
		}
	}
	return out
}

// withoutPeer removes name from every entry's `to`, dropping an entry left with no one;
// changed says whether anything was removed.
func withoutPeer(lend []policy.Lend, name string) (out []policy.Lend, changed bool) {
	out = []policy.Lend{}
	for _, l := range lend {
		if !slices.Contains(l.To, name) {
			out = append(out, l)
			continue
		}
		changed = true
		if to := slices.DeleteFunc(slices.Clone(l.To), func(n string) bool { return n == name }); len(to) > 0 {
			out = append(out, policy.Lend{Device: l.Device, To: to})
		}
	}
	return out, changed
}

// apply reads the policy, lets build decide the patch from it, and writes it with the
// revision it read: one policy.update with ifRevision. A concurrent edit
// (POLICY_REVISION_CONFLICT) is read again and decided again, once; a second conflict is
// the human's to repeat. The write goes through the configuration's lock, as `trust`
// does, so a running daemon picks it up at its next check.
func apply(pol *policy.Policy, verb string, stdout, stderr io.Writer, build func(cur policy.GetResult) (edit, error)) int {
	for attempt := 0; ; attempt++ {
		cur := pol.Get()
		if cur.State == policy.StateInvalid {
			fmt.Fprintf(stderr, "droidyard %s: the configuration %s cannot be used, so nothing was written: %s\n", verb, cur.Path, cur.Error)
			return 1
		}
		e, err := build(cur)
		if err != nil {
			fmt.Fprintf(stderr, "droidyard %s: %v\n", verb, err)
			return 1
		}
		if e.patch != nil {
			e.patch["ifRevision"] = cur.Revision
			var up policy.UpdateParams
			raw, err := json.Marshal(e.patch)
			if err == nil {
				err = json.Unmarshal(raw, &up)
			}
			if err != nil {
				fmt.Fprintf(stderr, "droidyard %s: %v\n", verb, err)
				return 1
			}
			if _, cerr := pol.Update(context.Background(), up); cerr != nil {
				if cerr.Code == capability.ErrPolicyRevisionConflict && attempt == 0 {
					continue
				}
				fmt.Fprintf(stderr, "droidyard %s: %s: %s\n", verb, cerr.Code, cerr.Message)
				if cerr.Code == capability.ErrPolicyRevisionConflict {
					fmt.Fprintln(stderr, "The configuration changed again while this ran; run the command again.")
				}
				if cerr.Cause != "" {
					fmt.Fprintln(stderr, cerr.Cause)
				}
				return 1
			}
		}
		for _, l := range e.said {
			fmt.Fprintln(stdout, l)
		}
		return 0
	}
}

// show reads the policy and prints it with print; a configuration that cannot be used is
// said, not guessed at.
func show(pol *policy.Policy, verb string, stdout, stderr io.Writer, print func(cur policy.GetResult)) int {
	cur := pol.Get()
	if cur.State == policy.StateInvalid {
		fmt.Fprintf(stderr, "droidyard %s: the configuration %s cannot be used: %s\n", verb, cur.Path, cur.Error)
		return 1
	}
	print(cur)
	return 0
}

// usageError prints the usage of a malformed command line; why, if any, says what was wrong.
func usageError(stderr io.Writer, verb, usage string, why ...error) int {
	for _, e := range why {
		fmt.Fprintf(stderr, "droidyard %s: %v\n", verb, e)
	}
	fmt.Fprintln(stderr, usage)
	return 2
}

// invalid reports an argument the configuration would refuse, before anything is read.
func invalid(stderr io.Writer, verb string, err error) int {
	fmt.Fprintf(stderr, "droidyard %s: %v\n", verb, err)
	return 2
}
