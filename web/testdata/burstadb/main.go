// Command burstadb is a fake adb for measuring the web UI's Logcat panel against a real
// daemon (plan m2a step 5; K3, A7, A8). Point the daemon at it with ADB=<this binary> and
// an empty ANDROID_AVD_HOME; it never touches a device or an adb server.
//
// It answers exactly the invocations the daemon makes for device.list and for
// device.watchLogs on one emulator, and a following logcat that emits lines at the rate
// the configuration sets. Every message carries the host-clock time it was written, in
// milliseconds ("ts=<ms>"), so the browser can compute a line's latency as Date.now() - ts.
//
// Configuration: a JSON file named by BURSTADB_CONFIG, else burst.json beside the
// binary, else the default below (2 s idle, then 5,000 lines/s for 15 s):
//
//	{
//	  "serial": "emulator-5554",          // the one device
//	  "app": "dev.droidyard.burst",       // the installed application (applicationId filter)
//	  "uid": 10146,                       // its uid; every other line is system (uid 1000)
//	  "backlog": 200,                     // lines printed at once for -T <count>, older than now
//	  "trigger": "",                      // if set, the timeline starts when this file exists
//	  "phases": [{"seconds": 2, "rate": 0}, {"seconds": 15, "rate": 5000}]
//	}
//
// After the last phase the rate is 0 until logcat is killed. BURSTADB_LOG, if set, is a
// file every invocation's arguments are appended to.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type phase struct {
	Seconds float64 `json:"seconds"`
	Rate    float64 `json:"rate"` // lines per second
}

type config struct {
	Serial  string  `json:"serial"`
	App     string  `json:"app"`
	UID     int     `json:"uid"`
	Backlog int     `json:"backlog"`
	Trigger string  `json:"trigger"`
	Phases  []phase `json:"phases"`
}

func defaults() config {
	return config{
		Serial:  "emulator-5554",
		App:     "dev.droidyard.burst",
		UID:     10146,
		Backlog: 200,
		Phases:  []phase{{Seconds: 2, Rate: 0}, {Seconds: 15, Rate: 5000}},
	}
}

// The format the daemon asks for (internal/domain/device.logcatFormat).
const format = "threadtime,epoch,usec,uid"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	logCall(args)
	cfg := loadConfig()
	switch {
	case len(args) == 1 && args[0] == "version":
		fmt.Print("Android Debug Bridge version 1.0.41\nVersion 36.0.0-13206524\nInstalled as /fake/burstadb\n")
		return 0
	case len(args) == 2 && args[0] == "devices" && args[1] == "-l":
		fmt.Printf("List of devices attached\n%-22s device product:burst model:burst_phone device:burst transport_id:1\n\n", cfg.Serial)
		return 0
	}
	if len(args) >= 3 && args[0] == "-s" {
		if args[1] != cfg.Serial {
			fmt.Fprintf(os.Stderr, "error: device '%s' not found\n", args[1])
			return 1
		}
		rest := args[2:]
		switch {
		case eq(rest, "shell", "getprop", "sys.boot_completed"):
			fmt.Println("1")
			return 0
		case eq(rest, "shell", "getprop"):
			fmt.Print("[ro.build.version.release]: [14]\n[ro.build.version.sdk]: [34]\n")
			return 0
		case eq(rest, "emu", "avd", "name"):
			fmt.Print("burst_avd\r\nOK\r\n")
			return 0
		case len(rest) == 6 && rest[0] == "shell" && rest[1] == "pm" && rest[2] == "list" && rest[3] == "packages" && rest[4] == "-U":
			// pm matches by substring, as the real one does.
			if strings.Contains(cfg.App, rest[5]) {
				fmt.Printf("package:%s uid:%d\n", cfg.App, cfg.UID)
			}
			return 0
		case len(rest) >= 1 && rest[0] == "logcat":
			return logcat(cfg, rest[1:])
		}
	}
	fmt.Fprintln(os.Stderr, "burstadb: unexpected arguments:", strings.Join(args, " "))
	return 9
}

func eq(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// logcat answers the uid probe (-d) and the follow.
func logcat(cfg config, args []string) int {
	var v, from string
	dump := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-v":
			i++
			if i < len(args) {
				v = args[i]
			}
		case "-T":
			i++
			if i < len(args) {
				from = args[i]
			}
		case "-d":
			dump = true
		case "-b", "-t":
			i++
		}
	}
	if v != format {
		fmt.Fprintf(os.Stderr, "burstadb: unsupported logcat format %q\n", v)
		return 1
	}
	out := bufio.NewWriterSize(os.Stdout, 256<<10)
	if dump {
		// The uid-column probe: a few lines with the column.
		fmt.Fprintln(out, "--------- beginning of main")
		now := time.Now()
		for i := 0; i < 3; i++ {
			fmt.Fprintln(out, format1(now.Add(time.Duration(i)*time.Microsecond), cfg.UID, 'I', "Probe", fmt.Sprintf("probe %d", i)))
		}
		return flushed(out)
	}
	fmt.Fprintln(out, "--------- beginning of main")
	// -T <count> starts with that many older lines; -T <epoch> (a reconnect's since) with
	// none: this fake keeps no history to resume from.
	if n := atoiOnly(from); n >= 0 {
		if n > cfg.Backlog {
			n = cfg.Backlog
		}
		start := time.Now().Add(-time.Duration(n+1) * time.Millisecond)
		for i := 0; i < n; i++ {
			at := start.Add(time.Duration(i) * time.Millisecond)
			fmt.Fprintln(out, burstLine(cfg, at, -n+i, at))
		}
	}
	if flushed(out) != 0 {
		return 0
	}
	return follow(cfg, out)
}

// follow emits lines at the configured rate until its stdout closes or it is killed.
func follow(cfg config, out *bufio.Writer) int {
	const tick = 10 * time.Millisecond
	var t0 time.Time // the timeline's start; zero until the trigger exists
	if cfg.Trigger == "" {
		t0 = time.Now()
	}
	seq := 0
	acc := 0.0
	last := time.Now()
	for {
		time.Sleep(tick)
		now := time.Now()
		if t0.IsZero() {
			if _, err := os.Stat(cfg.Trigger); err == nil {
				t0 = now
				last = now
			}
			continue
		}
		dt := now.Sub(last).Seconds()
		last = now
		acc += rateAt(cfg.Phases, now.Sub(t0).Seconds()) * dt
		n := int(acc)
		acc -= float64(n)
		for i := 0; i < n; i++ {
			// Spread the tick's lines over its microseconds, so times are distinct.
			at := now.Add(time.Duration(i) * time.Microsecond)
			fmt.Fprintln(out, burstLine(cfg, at, seq, now))
			seq++
		}
		if n > 0 && flushed(out) != 0 {
			return 0 // stdout closed: the daemon is gone
		}
	}
}

func rateAt(phases []phase, elapsed float64) float64 {
	for _, p := range phases {
		if elapsed < p.Seconds {
			return p.Rate
		}
		elapsed -= p.Seconds
	}
	return 0
}

// burstLine is one logcat line. Its message carries the host-clock time it was written.
// Lines alternate between the application's uid and the system's, so the applicationId
// filter has something to drop. Priorities cycle so the client-side filters have all six.
func burstLine(cfg config, at time.Time, seq int, written time.Time) string {
	uid := 1000
	tag := "SystemBurst"
	if seq%2 == 0 {
		uid, tag = cfg.UID, "AppBurst"
	}
	prio := "VDIWEF"[((seq%6)+6)%6]
	msg := fmt.Sprintf("burst seq=%d ts=%d payload=the quick brown fox jumps over the lazy dog", seq, written.UnixMilli())
	return format1(at, uid, prio, tag, msg)
}

// format1 renders threadtime,epoch,usec,uid: "<sec>.<usec> <uid> <pid> <tid> <P> <tag>: <msg>".
func format1(at time.Time, uid int, prio byte, tag, msg string) string {
	return fmt.Sprintf("%10d.%06d %5d %5d %5d %c %-8s: %s", at.Unix(), at.Nanosecond()/1000, uid, 4242, 4243, prio, tag, msg)
}

func atoiOnly(s string) int {
	if s == "" || strings.Contains(s, ".") {
		return -1
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func flushed(w *bufio.Writer) int {
	if err := w.Flush(); err != nil {
		return 1
	}
	return 0
}

func loadConfig() config {
	cfg := defaults()
	path := os.Getenv("BURSTADB_CONFIG")
	if path == "" {
		if exe, err := os.Executable(); err == nil {
			path = filepath.Join(filepath.Dir(exe), "burst.json")
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "burstadb: bad configuration, using the defaults:", err)
		return defaults()
	}
	return cfg
}

func logCall(args []string) {
	p := os.Getenv("BURSTADB_LOG")
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, strings.Join(args, " "))
}
