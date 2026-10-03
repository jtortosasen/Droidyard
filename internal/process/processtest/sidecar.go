package processtest

import (
	"bufio"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Sidecar is the dummy supervised process of steps 6 and 7 (plan m1c, R17). It prints
// "ready" on stdout, answers each stdin line with "echo <line>", and exits on stdin
// EOF. It appends what it observes, one line each, to FileEnv's file: "ready",
// "SIGTERM" on each SIGTERM, "eof", "helper <pid>", "flooded". SidecarEnv holds its
// options, comma-separated:
//
//	ignore-eof      run on after stdin ends
//	ignore-term     run on after SIGTERM
//	fork-helper     start a helper that ignores SIGTERM, in the sidecar's process group
//	flood=<bytes>   write that many bytes of output lines after the ready line
//	ready-after=<f> print the ready line only once file f exists
const Sidecar = "sidecar"

// SidecarEnv names the sidecar's options.
const SidecarEnv = "DROIDYARD_TEST_SIDECAR"

// SidecarCommand is how a test describes the dummy to the supervisor: its command,
// and the environment that selects the mode, the event file and the options.
func SidecarCommand(events string, opts ...string) (command string, env []string) {
	return self(), append(os.Environ(),
		ModeEnv+"="+Sidecar, FileEnv+"="+events, SidecarEnv+"="+strings.Join(opts, ","))
}

func record(event string) {
	f, err := os.OpenFile(os.Getenv(FileEnv), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err == nil {
		f.WriteString(event + "\n")
		f.Close()
	}
}

func runSidecar() {
	opts := map[string]string{}
	for _, o := range strings.Split(os.Getenv(SidecarEnv), ",") {
		k, v, _ := strings.Cut(o, "=")
		if k != "" {
			opts[k] = v
		}
	}
	_, ignoreEOF := opts["ignore-eof"]
	_, ignoreTerm := opts["ignore-term"]

	terms := make(chan os.Signal, 4)
	signal.Notify(terms, syscall.SIGTERM)
	go func() {
		for range terms {
			record("SIGTERM")
			if !ignoreTerm {
				os.Exit(0)
			}
		}
	}()
	if _, ok := opts["fork-helper"]; ok {
		h := exec.Command(self())
		h.Env = append(os.Environ(), ModeEnv+"="+TermIgnored)
		if err := h.Start(); err != nil {
			os.Stderr.WriteString("processtest: " + err.Error() + "\n")
			os.Exit(2)
		}
		record("helper " + strconv.Itoa(h.Process.Pid))
	}
	if f := opts["ready-after"]; f != "" {
		for {
			if _, err := os.Stat(f); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	out := bufio.NewWriter(os.Stdout)
	out.WriteString("ready\n")
	out.Flush()
	record("ready")
	if n, _ := strconv.Atoi(opts["flood"]); n > 0 {
		line := strings.Repeat("x", 99) + "\n"
		for written := 0; written < n; written += len(line) {
			out.WriteString(line)
		}
		out.Flush()
		record("flooded")
	}

	go func() {
		time.Sleep(lifetime)
		os.Exit(0)
	}()
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		out.WriteString("echo " + in.Text() + "\n")
		out.Flush()
	}
	record("eof")
	if ignoreEOF {
		select {}
	}
	os.Exit(0)
}

// self is the running test binary. os.Args[0] is not: a re-executed daemon names
// itself droidyard.
func self() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return os.Args[0]
}
