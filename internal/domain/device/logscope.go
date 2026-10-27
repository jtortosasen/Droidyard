package device

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// logScope says which lines are the call's: everyone's when no application and no process
// is named; otherwise the application's (its crash lines, then its uid, or its known pids)
// joined with the lines of the named processes' pids. Crash lines join the scope only
// through the application (ADR-0135 §4): another app's crash block is in crashes[], not in
// the lines of a call that named processes alone.
type logScope struct {
	lines       []logLine
	crash       []bool
	appFiltered bool
	byUID       bool
	uid         int
	pids        map[int]bool // the application's known pids, for a device without the uid column
	procs       *processScope
}

// has reports whether line i is in scope.
func (s logScope) has(i int) bool {
	if !s.appFiltered && s.procs.empty() {
		return true
	}
	l := s.lines[i]
	if s.appFiltered {
		switch {
		case s.crash[i]:
			return true
		case s.byUID:
			if l.uid == s.uid {
				return true
			}
		case s.pids[l.pid]:
			return true
		}
	}
	return s.procs.has(l.pid)
}

// count is how many lines are in scope, before the line filter: it tells `filtered` from
// `noLines` (plan m1i D11).
func (s logScope) count() int {
	n := 0
	for i := range s.lines {
		if s.has(i) {
			n++
		}
	}
	return n
}

// emptyReason says why no line was returned (plan m1i D11): `filtered` when lines in scope
// existed but the line filter excluded them, `noLines` when a process in scope is known
// but logged nothing, `noKnownProcess` otherwise (no evidence the application ran, or none
// of the named processes was found).
func emptyReason(inScope, matched int, appSeen, appFiltered bool, procs *processScope) string {
	switch {
	case matched > 0:
		return ""
	case inScope > 0:
		return "filtered"
	case !appFiltered && procs.empty():
		return "noLines"
	case appFiltered && appSeen, procs.found():
		return "noLines"
	}
	return "noKnownProcess"
}

// systemUID is the uid ActivityManager's lines carry (plan m3i step 1 (b): the uid column
// printed 1000 on API 31 and 36).
const systemUID = 1000

// startProcTrusted reports whether a line's "Start proc" may name a process: any app can
// log that text, so only ActivityManager's own at the system uid counts (ADR-0135 §4). A
// device that prints no uid column cannot show the uid, so there nothing is trusted and
// ps alone names the processes.
func startProcTrusted(l logLine, columnPresent bool) bool {
	return l.tag == "ActivityManager" && columnPresent && l.uid == systemUID
}

// processScope resolves the process names a call asked for to pids: from one `ps` listing
// and from trusted Start proc lines. The zero value and a scope of no names are empty.
type processScope struct {
	names []string // the requested names, in order, once each
	pids  map[string]map[int]bool
	all   map[int]bool
}

func newProcessScope(names []string) *processScope {
	s := &processScope{pids: map[string]map[int]bool{}, all: map[int]bool{}}
	for _, n := range names {
		if _, dup := s.pids[n]; dup {
			continue
		}
		s.names = append(s.names, n)
		s.pids[n] = map[int]bool{}
	}
	return s
}

// empty reports whether the call named no process.
func (s *processScope) empty() bool { return s == nil || len(s.names) == 0 }

// has reports whether pid belongs to a named process.
func (s *processScope) has(pid int) bool { return s != nil && s.all[pid] }

// found reports whether any named process was resolved to a pid.
func (s *processScope) found() bool { return s != nil && len(s.all) > 0 }

func (s *processScope) add(name string, pid int) {
	if set, ok := s.pids[name]; ok && pid > 0 {
		set[pid] = true
		s.all[pid] = true
	}
}

// addListed takes the rows of a ps listing whose NAME is exactly a requested one.
func (s *processScope) addListed(rows []deviceProc) {
	for _, r := range rows {
		if pid, err := strconv.Atoi(r.pid); err == nil {
			s.add(r.name, pid)
		}
	}
}

// addStartProc takes the process a Start proc message names, when it is a requested one.
func (s *processScope) addStartProc(message string) {
	if pid, name, ok := startProcOf(message); ok {
		s.add(name, pid)
	}
}

// view is what each requested name resolved to, for the result's filter.
func (s *processScope) view() []LogProcess {
	if s.empty() {
		return nil
	}
	out := make([]LogProcess, 0, len(s.names))
	for _, n := range s.names {
		pids := make([]int, 0, len(s.pids[n]))
		for pid := range s.pids[n] {
			pids = append(pids, pid)
		}
		sortInts(pids)
		// The report is bounded like a relayed one; the scope itself keeps every pid.
		out = append(out, LogProcess{Name: n, PIDs: pids[:min(len(pids), logsProcessPIDsMax)]})
	}
	return out
}

// listProcesses reads the device's process list once: `ps -A -o PID,NAME`, a fixed argv
// that holds no caller text. A list that could not be read whole is a warning, never an
// error: the Start proc lines of the read still name processes. Only a call that cannot go
// on at all (cancelled, adb not runnable, the device gone) is an error.
func (a *ADB) listProcesses(ctx context.Context, t *target) ([]deviceProc, string, error) {
	out, err := a.runShell(ctx, t, logsPSTimeout, psOutputCap, "shell", "ps", "-A", "-o", "PID,NAME")
	if cerr := ctx.Err(); cerr != nil {
		return nil, "", cerr
	}
	var ce *capability.Error
	if errors.As(err, &ce) {
		return nil, "", err // adb could not be run, or the device is gone
	}
	why := ""
	switch {
	case errors.Is(err, ErrTimeout):
		why = "it did not answer in " + logsPSTimeout.String()
	case err != nil:
		why = err.Error()
	case out.ExitCode != 0:
		why = "it exited with status " + strconv.Itoa(out.ExitCode)
	case out.Truncated:
		why = "it printed more than " + strconv.Itoa(psOutputCap>>20) + " MiB"
	}
	if why != "" {
		return nil, "the process list (adb shell ps) could not be read: " + why +
			", so the named processes are matched by the read's Start proc lines only", nil
	}
	return parsePS(out.Stdout), "", nil
}

// parsePS reads `ps -A -o PID,NAME`: one row per process, the header and anything else that
// does not start with a pid skipped.
func parsePS(out []byte) []deviceProc {
	var procs []deviceProc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		i := strings.IndexAny(line, " \t")
		if i <= 0 {
			continue
		}
		pid, name := line[:i], strings.TrimSpace(line[i+1:])
		if _, err := strconv.Atoi(pid); err != nil || name == "" {
			continue
		}
		procs = append(procs, deviceProc{pid: pid, name: name})
	}
	return procs
}
