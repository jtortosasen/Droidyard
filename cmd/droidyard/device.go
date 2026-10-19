package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

const deviceUsage = "usage: droidyard device allow <serial> | disallow <serial>"

// deviceCmd implements `droidyard device allow|disallow <serial>` (ADR-0015 §4, ADR-0032
// §5, §6): the physical-device policy without editing the configuration by hand. Like
// `trust`, it writes the user configuration and starts no daemon; the daemon reads
// `devices` at each call.
func deviceCmd(args []string, stdout, stderr io.Writer) int {
	return deviceWith(policy.New(platform.Current()), args, stdout, stderr)
}

func deviceWith(pol *policy.Policy, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, deviceUsage)
		return 2
	}
	verb, rest := args[0], args[1:]
	if verb != "allow" && verb != "disallow" {
		return usageError(stderr, "device", deviceUsage, fmt.Errorf("unknown verb %q", verb))
	}
	if len(rest) != 1 {
		return usageError(stderr, "device "+verb, deviceUsage)
	}
	serial := rest[0]
	if strings.TrimSpace(serial) == "" || strings.ContainsAny(serial, " \t\r\n") {
		return invalid(stderr, "device "+verb, fmt.Errorf("%q is not a device serial", serial))
	}
	if verb == "allow" {
		return apply(pol, "device allow", stdout, stderr, func(cur policy.GetResult) (edit, error) {
			listed := slices.Contains(cur.Devices.Allowlist, serial)
			if cur.Devices.AllowPhysical && listed {
				return edit{said: []string{serial + " is already allowed"}}, nil
			}
			list := slices.Clone(cur.Devices.Allowlist)
			if !listed {
				list = append(list, serial)
			}
			return edit{
				patch: map[string]any{"devices": map[string]any{"allowPhysical": true, "allowlist": list}},
				said:  []string{"allowed " + serial + ": a running daemon lists it at its next device.list, no restart needed"},
			}, nil
		})
	}
	return apply(pol, "device disallow", stdout, stderr, func(cur policy.GetResult) (edit, error) {
		if !slices.Contains(cur.Devices.Allowlist, serial) {
			return edit{said: []string{serial + " is not allowed"}}, nil
		}
		list := slices.DeleteFunc(slices.Clone(cur.Devices.Allowlist), func(s string) bool { return s == serial })
		return edit{
			patch: map[string]any{"devices": map[string]any{"allowlist": list}},
			said:  []string{"no longer allowing " + serial + " (devices.allowPhysical is left as it was)"},
		}, nil
	})
}
