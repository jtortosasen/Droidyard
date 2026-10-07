package device

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The device log format spike S9 chose (plan m1i step 1, G1; §Changes 2026-10-07):
// `logcat -v threadtime,epoch,usec,uid` prints one line per entry as
//
//	<epoch>.<usec> <uid> <pid> <tid> <lvl> <tag>: <message>
//
// with the uid, pid and tid right-aligned and the tag left-justified, terminated by
// ": ". The uid column is a number for app lines (10146) and a name for a system
// daemon's (root). `logcat -v nonsense` exits 0 and falls back to a default format
// (G6), so a mismatch is detected from the lines, never the exit status: a non-empty
// line that is neither a buffer marker nor a line of the chosen format is a format
// error (D10).

// logcatFormat is the -v value every device.logs read asks for.
const logcatFormat = "threadtime,epoch,usec,uid"

// logcatBuffers is logcat's default buffer set, the values `buffers` may name.
var logcatBuffers = []string{"main", "system", "crash"}

var (
	// logcatLineRE matches a line with the uid column.
	logcatLineRE = regexp.MustCompile(`^ *([0-9]+)\.([0-9]{6}) +(\S+) +([0-9]+) +([0-9]+) +([VDIWEF]) +(.*)$`)
	// logcatNoUIDRE matches a line without it, where the device ignored the `uid`
	// format element (plan m1i D5's fallback).
	logcatNoUIDRE = regexp.MustCompile(`^ *([0-9]+)\.([0-9]{6}) +([0-9]+) +([0-9]+) +([VDIWEF]) +(.*)$`)
	// bufferMarkerRE matches logcat's "--------- beginning of <buffer>" separator.
	bufferMarkerRE = regexp.MustCompile(`^-+ beginning of ([a-z]+)$`)
	// startProcRE matches ActivityManager's "Start proc <pid>:<pkg>/<user> for …",
	// which names the app's process even though the line carries ActivityManager's
	// own uid (plan m1i D11).
	startProcRE = regexp.MustCompile(`^Start proc ([0-9]+):(\S+?)/`)
	// nativeHeaderRE matches the crash_dump header "pid: <pid>, tid: …, name: … >>> <pkg> <<<".
	nativeHeaderRE = regexp.MustCompile(`^pid: ([0-9]+), .*>>> (\S+) <<<`)
	// pmLineRE matches one `pm list packages -U` line, "package:<name> uid:<n>".
	pmLineRE = regexp.MustCompile(`^package:(\S+) uid:([0-9]+)$`)
)

// priorities maps logcat's level letter to the published enum.
var priorities = map[byte]string{
	'V': "verbose", 'D': "debug", 'I': "info", 'W': "warn", 'E': "error", 'F': "fatal",
}

// priorityRank orders the enum for minPriority.
var priorityRank = map[string]int{
	"verbose": 0, "debug": 1, "info": 2, "warn": 3, "error": 4, "fatal": 5,
}

// logLine is one parsed log line before filtering.
type logLine struct {
	buffer   string
	at       time.Time
	uid      int // -1 when the column names a user rather than a numeric uid
	pid      int
	tid      int
	priority string
	tag      string
	message  string
	raw      string
}

// logResult separates a whole-buffer parse from a format error.
type logParse struct {
	lines  []logLine
	format bool // true when a line was not of the chosen format
}

// parseLogLines reads a whole logcat dump. Buffer markers set the buffer a run of lines
// belongs to; a non-empty line that is neither a marker nor a line of the chosen format
// makes the read a format error (plan m1i D10). withUID says which format was asked for
// (D5's probe).
func parseLogLines(out []byte, withUID bool) logParse {
	var p logParse
	buffer := ""
	for _, s := range rawLines(out) {
		if s == "" {
			continue
		}
		if m := bufferMarkerRE.FindStringSubmatch(s); m != nil {
			buffer = m[1]
			continue
		}
		l, ok := parseLogLine(s, buffer, withUID)
		if !ok {
			p.format = true
			return p
		}
		p.lines = append(p.lines, l)
	}
	return p
}

// parseLogLine parses one line of the chosen format, "" buffer meaning none was named.
func parseLogLine(s, buffer string, withUID bool) (logLine, bool) {
	re := logcatNoUIDRE
	// Group 3 is the uid only in the uid format; the pid, tid, priority and rest sit
	// one group later there.
	pidIdx, tidIdx, prioIdx, restIdx := 3, 4, 5, 6
	if withUID {
		re, pidIdx, tidIdx, prioIdx, restIdx = logcatLineRE, 4, 5, 6, 7
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		return logLine{}, false
	}
	// The tag ends at the first ": " (logcat left-justifies it and terminates it so).
	rest := m[restIdx]
	i := strings.Index(rest, ": ")
	if i < 0 {
		return logLine{}, false
	}
	sec, err1 := strconv.ParseInt(m[1], 10, 64)
	usec, err2 := strconv.ParseInt(m[2], 10, 64)
	pid, err3 := strconv.Atoi(m[pidIdx])
	tid, err4 := strconv.Atoi(m[tidIdx])
	prio, ok := priorities[m[prioIdx][0]]
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || !ok {
		return logLine{}, false
	}
	l := logLine{
		buffer: buffer, uid: -1, raw: s,
		at: time.Unix(sec, usec*1000).UTC(), pid: pid, tid: tid, priority: prio,
		tag: strings.TrimSpace(rest[:i]), message: rest[i+2:],
	}
	if withUID {
		l.uid = uidOf(m[3])
	}
	return l, true
}

// uidOf reads the uid column: a number, or -1 for a name (root, system).
func uidOf(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return -1
}

// rawLines splits a dump into lines, dropping the trailing newline and any CR.
func rawLines(out []byte) []string {
	s := string(out)
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

// crashSummary is one crash block's summary for the result's crashes[].
type crashSummary struct {
	pid       int
	at        time.Time
	exception string
	message   string
}

// crashBlocks finds the crash blocks among the parsed lines. A block is a maximal run
// of consecutive lines sharing (buffer, pid, tag) (plan m1i D6); a Java block is an
// AndroidRuntime run from "FATAL EXCEPTION:" carrying "Process: <app>, PID: <pid>", a
// native one a DEBUG run carrying ">>> <app> <<<". app "" matches any app (no filter);
// otherwise the block must name app exactly, and a Java block must also carry uid
// (byUID), and so must a native block: its DEBUG lines carry the app's uid, since
// crash_dump runs as the app (spike S9 G5; step 2 review B2). It
// returns the summaries and which lines are crash lines.
func crashBlocks(lines []logLine, app string, uid int, byUID bool) (crashes []crashSummary, crashLine []bool) {
	crashLine = make([]bool, len(lines))
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j].buffer == lines[i].buffer && lines[j].pid == lines[i].pid && lines[j].tag == lines[i].tag {
			j++
		}
		if s, ok := classifyCrash(lines[i:j], app, uid, byUID); ok {
			crashes = append(crashes, s)
			for k := i; k < j; k++ {
				crashLine[k] = true
			}
		}
		i = j
	}
	return crashes, crashLine
}

// classifyCrash decides whether one (buffer, pid, tag) run is a crash block of app.
func classifyCrash(block []logLine, app string, uid int, byUID bool) (crashSummary, bool) {
	if len(block) == 0 {
		return crashSummary{}, false
	}
	switch block[0].tag {
	case "AndroidRuntime":
		return javaCrash(block, app, uid, byUID)
	case "DEBUG":
		return nativeCrash(block, app, uid, byUID)
	}
	return crashSummary{}, false
}

// javaCrash recognises a Java crash block: a "FATAL EXCEPTION:" line and a
// "Process: <app>, PID: <pid>" line in the same AndroidRuntime run.
func javaCrash(block []logLine, app string, uid int, byUID bool) (crashSummary, bool) {
	fatal, process := -1, -1
	for i, l := range block {
		if fatal < 0 && strings.HasPrefix(l.message, "FATAL EXCEPTION:") {
			fatal = i
		}
		if process < 0 && strings.HasPrefix(l.message, "Process: ") {
			process = i
		}
	}
	if fatal < 0 || process < 0 {
		return crashSummary{}, false
	}
	namePID := strings.SplitN(strings.TrimPrefix(block[process].message, "Process: "), ", PID: ", 2)
	if len(namePID) != 2 {
		return crashSummary{}, false
	}
	if app != "" && namePID[0] != app {
		return crashSummary{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(namePID[1]))
	if err != nil {
		return crashSummary{}, false
	}
	// The crashing process writes the block itself, so a Java block must carry the
	// app's uid when the filter is by uid: another app cannot claim a crash by logging
	// a forged header under its own uid (plan m1i D6).
	if byUID && app != "" && block[process].uid != uid {
		return crashSummary{}, false
	}
	s := crashSummary{pid: pid, at: block[fatal].at}
	if process+1 < len(block) {
		s.exception, s.message = splitColon(block[process+1].message)
	}
	return s, true
}

// nativeCrash recognises a native crash block: the tombstone header names the app and,
// under the uid filter, the block's lines carry the app's uid, so another app cannot log
// a ">>> <app> <<<" header and have its lines reported as this app's crash (review B2).
func nativeCrash(block []logLine, app string, uid int, byUID bool) (crashSummary, bool) {
	if app != "" && byUID && block[0].uid != uid {
		return crashSummary{}, false
	}
	header, signal, pid := -1, -1, 0
	for i, l := range block {
		if m := nativeHeaderRE.FindStringSubmatch(l.message); m != nil && (app == "" || m[2] == app) {
			if header < 0 {
				header = i
				pid, _ = strconv.Atoi(m[1])
			}
		}
		if signal < 0 && strings.HasPrefix(l.message, "signal ") {
			signal = i
		}
	}
	if header < 0 || signal < 0 {
		return crashSummary{}, false
	}
	ex, msg := splitComma(block[signal].message)
	return crashSummary{pid: pid, at: block[header].at, exception: ex, message: msg}, true
}

// splitComma cuts a line at its first ", ": the native signal line names the signal
// then its code and fault address.
func splitComma(s string) (string, string) {
	if i := strings.Index(s, ", "); i >= 0 {
		return s[:i], s[i+2:]
	}
	return s, ""
}

// splitColon cuts a line at its first ": ": the Java exception line and the native
// signal line both name a thing then describe it.
func splitColon(s string) (string, string) {
	if i := strings.Index(s, ": "); i >= 0 {
		return s[:i], s[i+2:]
	}
	return s, ""
}

// startProcPID returns the pid of the app's "Start proc <pid>:<app>/…" line, 0 when the
// line names another package.
func startProcPID(message, app string) int {
	m := startProcRE.FindStringSubmatch(message)
	if m == nil || m[2] != app {
		return 0
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return pid
}

// epochForm renders a device-clock instant as logcat -T's epoch form (plan m1i step 1,
// G3): "<epoch>.<usec>".
func epochForm(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10) + "." + pad6(t.Nanosecond()/1000)
}

// pad6 zero-pads n to six digits (microseconds).
func pad6(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}

// rfcMicro renders a device-clock instant as RFC 3339 UTC with microseconds.
func rfcMicro(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}
