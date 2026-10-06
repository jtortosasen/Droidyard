// Package gradletest is the fake Gradle sidecar: a helper process that speaks the
// sidecar's JSON-RPC protocol from a scripted configuration, so tests exercise the
// client's handshake, its crash and protocol handling and the project model without a
// JVM or Gradle (plan m1g step 5, check (a)).
//
// A test's TestMain calls Main; Launch returns a gradle.Launch that runs the test
// binary again in this mode, so the sidecar is a real separate process, killed and
// watched exactly as the real one is.
package gradletest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

const (
	// ModeEnv names the configuration file: set in the re-executed test binary, it makes
	// the process the fake sidecar instead of a test run.
	ModeEnv = "DROIDYARD_FAKE_SIDECAR"
)

// Config is the fake's script. Zero values are sensible defaults: protocol 1, Gradle
// 8.13, source wrapper.
type Config struct {
	// ProtocolVersion is hello's protocolVersion (default 1). Set it to another value
	// to exercise the core's version check.
	ProtocolVersion int    `json:"protocolVersion"`
	SidecarVersion  string `json:"sidecarVersion"`
	TAPIVersion     string `json:"tapiVersion"`
	JavaHome        string `json:"javaHome"`
	JavaVersion     string `json:"javaVersion"`

	// GradleVersion, Source and DaemonJavaHome are the connection.open reply.
	GradleVersion  string `json:"gradleVersion"`
	Source         string `json:"source"`
	DaemonJavaHome string `json:"daemonJavaHome"`

	// Model is the project.model reply, verbatim.
	Model json.RawMessage `json:"model"`

	// StrayOn names a method the fake answers with a line that is not JSON before its
	// real reply: the protocol break the client must treat as a crash.
	StrayOn string `json:"strayOn"`
	// BigLineOn names a method the fake answers with one JSON line of BigLineBytes
	// bytes (default 17 MiB): one over the client's bound.
	BigLineOn    string `json:"bigLineOn"`
	BigLineBytes int    `json:"bigLineBytes"`
	// HoldOn names a method the fake receives and never answers: the caller hangs until
	// the process is killed. HoldAfter, when positive, lets that many calls of HoldOn be
	// answered normally first, so a test can kill the fake mid-call on a later one.
	HoldOn    string `json:"holdOn"`
	HoldAfter int    `json:"holdAfter"`
	// DieOn names a method the fake receives and exits on, without answering.
	DieOn string `json:"dieOn"`

	// Events, when set, is a file the fake appends its lifecycle to: "started <pid>",
	// "served <method>", "eof". A test reads it to see that stdin was closed.
	Events string `json:"events"`
}

// Main runs the fake when ModeEnv is set, and returns otherwise. A test's TestMain
// calls it before m.Run.
func Main() {
	path := os.Getenv(ModeEnv)
	if path == "" {
		return
	}
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake sidecar: %v\n", err)
		os.Exit(2)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "fake sidecar: %v\n", err)
		os.Exit(2)
	}
	cfg.apply()
	run(cfg)
	os.Exit(0)
}

// Launch returns a gradle.Launch that runs the fake from the configuration file at
// path: the test binary itself, re-executed in this mode.
func Launch(path string) gradle.Launch {
	return func(gradle.JVM, gradle.Jars) (string, []string, []string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", nil, nil, err
		}
		env := append(os.Environ(), ModeEnv+"="+path)
		return exe, []string{"-test.run=^$"}, env, nil
	}
}

// WriteConfig writes cfg as a fresh configuration file and returns its path.
func WriteConfig(t interface{ TempDir() string }, cfg Config) string {
	dir := t.TempDir()
	path := dir + "/fake-sidecar.json"
	data, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		panic(err)
	}
	return path
}

func (c *Config) apply() {
	if c.ProtocolVersion == 0 {
		c.ProtocolVersion = 1
	}
	if c.SidecarVersion == "" {
		c.SidecarVersion = "fake-1.0.0"
	}
	if c.GradleVersion == "" {
		c.GradleVersion = "8.13"
	}
	if c.Source == "" {
		c.Source = "wrapper"
	}
	if c.BigLineBytes == 0 {
		c.BigLineBytes = 17 << 20
	}
}

func event(cfg Config, format string, args ...any) {
	if cfg.Events == "" {
		return
	}
	f, err := os.OpenFile(cfg.Events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}

func run(cfg Config) {
	// The supervisor closes stdin and signals its group in one step; a real process
	// would take the signal first. Ignoring SIGTERM makes stdin EOF the observable end,
	// so a test can assert that the daemon closed it (the protocol's own kill switch,
	// D6). SIGKILL still ends it, so the crash paths are unaffected.
	signal.Ignore(syscall.SIGTERM)
	event(cfg, "started %d", os.Getpid())
	w := bufio.NewWriter(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<16), 8<<20)
	held := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		serve(w, cfg, line, &held)
	}
	event(cfg, "eof")
}

func serve(w *bufio.Writer, cfg Config, line string, held *int) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		writeError(w, nil, -32700, "parse error")
		return
	}
	event(cfg, "served %s", req.Method)
	switch req.Method {
	case cfg.DieOn:
		os.Exit(1)
	case cfg.HoldOn:
		*held++
		if *held > cfg.HoldAfter {
			// Never answer: the caller waits until someone kills the process.
			for {
				time.Sleep(time.Hour)
			}
		}
	}
	if req.Method == cfg.StrayOn {
		// The stray line is the whole answer: the core must treat it as a protocol
		// break, and no reply follows to race the failure.
		w.WriteString("this line is not JSON\n")
		w.Flush()
		return
	}

	switch req.Method {
	case "hello":
		writeResult(w, req.ID, map[string]any{
			"protocolVersion": cfg.ProtocolVersion,
			"sidecarVersion":  cfg.SidecarVersion,
			"tapiVersion":     cfg.TAPIVersion,
			"java":            map[string]any{"home": cfg.JavaHome, "version": cfg.JavaVersion},
		})
	case "ping":
		writeResult(w, req.ID, map[string]any{})
	case "connection.open":
		writeResult(w, req.ID, map[string]any{
			"gradleVersion":  cfg.GradleVersion,
			"source":         cfg.Source,
			"daemonJavaHome": cfg.DaemonJavaHome,
			"capabilities":   map[string]bool{"problemEvents": true},
		})
	case "connection.close", "build.cancel":
		writeResult(w, req.ID, map[string]any{})
	case "project.model":
		if req.Method == cfg.BigLineOn {
			writeBigLine(w, req.ID, cfg.BigLineBytes)
			return
		}
		if len(cfg.Model) == 0 {
			writeError(w, req.ID, -32000, "project.model failed")
			return
		}
		writeRawResult(w, req.ID, cfg.Model)
	case "shutdown":
		writeResult(w, req.ID, map[string]any{})
		w.Flush()
		os.Exit(0)
	default:
		writeError(w, req.ID, -32601, "Method not found: "+req.Method)
	}
}

func writeResult(w *bufio.Writer, id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		writeError(w, id, -32603, err.Error())
		return
	}
	writeRawResult(w, id, raw)
}

func writeRawResult(w *bufio.Writer, id json.RawMessage, raw json.RawMessage) {
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": idOrNull(id), "result": raw})
	w.Write(msg)
	w.WriteByte('\n')
	w.Flush()
}

func writeError(w *bufio.Writer, id json.RawMessage, code int, text string) {
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": idOrNull(id),
		"error": map[string]any{"code": code, "message": text}})
	w.Write(msg)
	w.WriteByte('\n')
	w.Flush()
}

func writeBigLine(w *bufio.Writer, id json.RawMessage, n int) {
	raw, _ := json.Marshal(strings.Repeat("x", n))
	writeRawResult(w, id, raw)
}

func idOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

// MustModel marshals a project model for Config.Model.
func MustModel(m gradle.Model) json.RawMessage {
	raw, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return raw
}
