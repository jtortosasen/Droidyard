// Package gradletest is the fake Gradle sidecar: a helper process that speaks the
// sidecar's JSON-RPC protocol from a scripted configuration, so tests exercise the
// client's handshake, its crash and protocol handling, the project model and a whole
// scripted build without a JVM or Gradle (plan m1g step 5 and step 7, check (a)).
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
	"sync"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

const (
	// ModeEnv names the configuration file: set in the re-executed test binary, it makes
	// the process the fake sidecar instead of a test run.
	ModeEnv = "DROIDYARD_FAKE_SIDECAR"
)

// Config is the fake's script. Zero values are sensible defaults: protocol 2, Gradle
// 8.13, source wrapper.
type Config struct {
	// ProtocolVersion is hello's protocolVersion (default 2). Set it to another value
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
	// Capabilities is connection.open's capabilities map (default {problemEvents: true}).
	// A gated call the connected Gradle refuses shows up here as false, which is how the
	// core learns the build is degraded (plan m1g D17).
	Capabilities map[string]bool `json:"capabilities,omitempty"`

	// Model is the project.model reply, verbatim.
	Model json.RawMessage `json:"model"`

	// Probe is the fake's script for a probed project.model (plan m1h D16): the reply's
	// plugins and probe block and, per module path, its android facts. The fake injects
	// them only when the request asked probe: true, as the real sidecar does; a probed
	// request with no Probe script answers the generic model, no probe fields.
	Probe *Probe `json:"probe,omitempty"`

	// Build is the script for build.run: the notifications a build sends, its outcome
	// and its failures. A nil Build answers build.run with an error, so a test can see
	// the client's failure path.
	Build *Build `json:"build,omitempty"`

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

// Probe is the fake's probed project.model script (plan m1h D16): the plugins and probe
// block the real sidecar replies with, and each Android module's facts, keyed by project
// path. Status is ok (the default), partial, failed or skipped, so every mode the probe
// can answer with is reachable from a test.
type Probe struct {
	Status  string             `json:"status,omitempty"`
	Notes   []string           `json:"notes,omitempty"`
	AGP     string             `json:"agp,omitempty"`
	KGP     string             `json:"kgp,omitempty"`
	Android map[string]Android `json:"android,omitempty"`
}

// Android is one module's probed facts (plan m1h D6, D7).
type Android struct {
	CompileTarget string    `json:"compileTarget,omitempty"`
	BuildTools    string    `json:"buildTools,omitempty"`
	SDKDir        string    `json:"sdkDir,omitempty"`
	Variants      []Variant `json:"variants,omitempty"`
}

// Variant is one AGP variant: its name, its build type and its flavours in dimension
// order (plan m1h D6).
type Variant struct {
	Name      string   `json:"name"`
	BuildType string   `json:"buildType,omitempty"`
	Flavors   []Flavor `json:"flavors,omitempty"`
}

// Flavor is one product flavour: the dimension it belongs to and its name.
type Flavor struct {
	Dimension string `json:"dimension,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Step is one notification a scripted build sends, in the order its Steps list them.
type Step struct {
	// Kind is output, taskFinished, progress, problem or gate. A gate sends nothing: the
	// script waits at it until the file named by Path exists, so a test can hold a build
	// at a known point and let it go.
	Kind string `json:"kind"`

	// output
	Stream string   `json:"stream,omitempty"`
	Lines  []string `json:"lines,omitempty"`

	// taskFinished
	Path       string `json:"path,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Reason     string `json:"reason,omitempty"`

	// progress
	Counts     *gradle.TaskSummary `json:"counts,omitempty"`
	ActiveTask string              `json:"activeTask,omitempty"`

	// problem
	Problem *gradle.Problem `json:"problem,omitempty"`
}

// Build is the script for build.run: build.run is answered {accepted} at once and the
// script's notifications follow, one per Step, then the terminal event (plan m1g
// step 7, check (a)).
type Build struct {
	Steps []Step `json:"steps,omitempty"`
	// Outcome is the terminal event's outcome (default success).
	Outcome string `json:"outcome,omitempty"`
	// Failures, FailureProblems and FailureProblemsDropped are the terminal event's
	// failure tree (gradle.md §Error model).
	Failures               []gradle.Failure `json:"failures,omitempty"`
	FailureProblems        []gradle.Problem `json:"failureProblems,omitempty"`
	FailureProblemsDropped int              `json:"failureProblemsDropped,omitempty"`
	// MissingCapabilities is the terminal event's list: a TAPI call the connected Gradle
	// refused, which makes the build degraded (plan m1g D17). It is the
	// UnsupportedMethodException the sidecar caught, reported to the core.
	MissingCapabilities []string `json:"missingCapabilities,omitempty"`
	// WaitForCancel holds the build open after its steps until build.cancel arrives, so
	// a test can cancel a build mid-flight; the fake then finishes it cancelled.
	WaitForCancel bool `json:"waitForCancel,omitempty"`
	// AcceptDelayMs answers build.run {accepted} only after that delay, while further
	// requests are still read: the build.run queued behind a model fetch in the real
	// sidecar, which a cancel can overtake (step 7 review B1).
	AcceptDelayMs int `json:"acceptDelayMs,omitempty"`
	// Silent sends no terminal notification at all: the lost-terminal-event path.
	Silent bool `json:"silent,omitempty"`
	// DieAfterSteps exits the process, without answering anything more, once that many
	// of the script's notifications have been sent: the sidecar that dies mid-build.
	DieAfterSteps int `json:"dieAfterSteps,omitempty"`
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
		c.ProtocolVersion = 2
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
	if c.Capabilities == nil {
		c.Capabilities = map[string]bool{"problemEvents": true}
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

// sidecar serves the protocol from one process: stdin's requests on the reading
// goroutine, and a build's notifications on a goroutine of their own, so a build.cancel
// can be read while a build is still running. Every write to stdout is serialised.
type sidecar struct {
	open map[string]bool // roots opened on this process, guarded by mu
	cfg  Config
	w    *bufio.Writer
	mu   sync.Mutex

	cmu     sync.Mutex
	cancels map[string]chan struct{}
}

func run(cfg Config) {
	// The supervisor closes stdin and signals its group in one step; a real process
	// would take the signal first. Ignoring SIGTERM makes stdin EOF the observable end,
	// so a test can assert that the daemon closed it (the protocol's own kill switch,
	// D6). SIGKILL still ends it, so the crash paths are unaffected.
	signal.Ignore(syscall.SIGTERM)
	event(cfg, "started %d", os.Getpid())
	s := &sidecar{cfg: cfg, w: bufio.NewWriter(os.Stdout), cancels: map[string]chan struct{}{}}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<16), 8<<20)
	held := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		s.serve(line, &held)
	}
	event(cfg, "eof")
}

func (s *sidecar) serve(line string, held *int) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		s.writeError(nil, -32700, "parse error")
		return
	}
	event(s.cfg, "served %s", req.Method)
	switch req.Method {
	case s.cfg.DieOn:
		os.Exit(1)
	case s.cfg.HoldOn:
		*held++
		if *held > s.cfg.HoldAfter {
			// Never answer: the caller waits until someone kills the process.
			for {
				time.Sleep(time.Hour)
			}
		}
	}
	if req.Method == s.cfg.StrayOn {
		// The stray line is the whole answer: the core must treat it as a protocol
		// break, and no reply follows to race the failure.
		s.mu.Lock()
		s.w.WriteString("this line is not JSON\n")
		s.w.Flush()
		s.mu.Unlock()
		return
	}

	// Like the real sidecar, a root must be opened on this process before it is used
	// (sidecar/src/Session.kt): a client that forgets a restart is refused.
	var rp struct {
		Root string `json:"root"`
	}
	_ = json.Unmarshal(req.Params, &rp)
	if (req.Method == "project.model" || req.Method == "build.run") && !s.isOpen(rp.Root) {
		s.writeError(req.ID, -32000, "no connection.open for root: "+rp.Root)
		return
	}

	switch req.Method {
	case "hello":
		s.writeResult(req.ID, map[string]any{
			"protocolVersion": s.cfg.ProtocolVersion,
			"sidecarVersion":  s.cfg.SidecarVersion,
			"tapiVersion":     s.cfg.TAPIVersion,
			"java":            map[string]any{"home": s.cfg.JavaHome, "version": s.cfg.JavaVersion},
		})
	case "ping":
		s.writeResult(req.ID, map[string]any{})
	case "connection.open":
		// The real sidecar refuses a root it already holds open (Session.kt
		// openConnection); a fake that did not hid a close that never reached it.
		if s.isOpen(rp.Root) {
			s.writeError(req.ID, -32000, "root is already open: "+rp.Root)
			return
		}
		s.setOpen(rp.Root, true)
		s.writeResult(req.ID, map[string]any{
			"gradleVersion":  s.cfg.GradleVersion,
			"source":         s.cfg.Source,
			"daemonJavaHome": s.cfg.DaemonJavaHome,
			"capabilities":   s.cfg.Capabilities,
		})
	case "connection.close":
		s.setOpen(rp.Root, false)
		s.writeResult(req.ID, map[string]any{})
	case "project.model":
		if req.Method == s.cfg.BigLineOn {
			s.writeBigLine(req.ID, s.cfg.BigLineBytes)
			return
		}
		if len(s.cfg.Model) == 0 {
			s.writeError(req.ID, -32000, "project.model failed")
			return
		}
		var pm struct {
			Probe bool `json:"probe"`
		}
		_ = json.Unmarshal(req.Params, &pm)
		event(s.cfg, "probe %v", pm.Probe)
		s.projectModel(req.ID, pm.Probe)
	case "build.run":
		s.build(req.ID, req.Params)
	case "build.cancel":
		var p struct {
			BuildID string `json:"buildId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.writeResult(req.ID, map[string]any{})
		s.cancel(p.BuildID)
	case "shutdown":
		s.writeResult(req.ID, map[string]any{})
		s.mu.Lock()
		s.w.Flush()
		s.mu.Unlock()
		os.Exit(0)
	default:
		s.writeError(req.ID, -32601, "Method not found: "+req.Method)
	}
}

// projectModel answers project.model with the configured Model plus, for a probed request,
// the Probe script's plugins, probe block and per-module android facts (plan m1h D16),
// matching the real sidecar's reply shape.
func (s *sidecar) projectModel(id json.RawMessage, want bool) {
	if !want {
		var reply map[string]any
		if err := json.Unmarshal(s.cfg.Model, &reply); err != nil {
			s.writeRawResult(id, s.cfg.Model)
			return
		}
		reply["probe"] = map[string]any{"status": "skipped", "notes": []string{}}
		s.writeResult(id, reply)
		return
	}
	if s.cfg.Probe == nil {
		s.writeRawResult(id, s.cfg.Model)
		return
	}
	var reply map[string]any
	if err := json.Unmarshal(s.cfg.Model, &reply); err != nil {
		s.writeRawResult(id, s.cfg.Model)
		return
	}
	p := s.cfg.Probe
	agp, kgp := p.AGP, p.KGP
	if agp == "" {
		agp = "unknown"
	}
	if kgp == "" {
		kgp = "unknown"
	}
	status := p.Status
	if status == "" {
		status = "ok"
	}
	notes := p.Notes
	if notes == nil {
		notes = []string{}
	}
	reply["plugins"] = map[string]any{"agp": agp, "kgp": kgp}
	reply["probe"] = map[string]any{"status": status, "notes": notes}
	if projects, ok := reply["projects"].([]any); ok {
		for _, entry := range projects {
			module, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if android, ok := p.Android[module["path"].(string)]; ok {
				module["android"] = android
			}
		}
	}
	s.writeResult(id, reply)
}

// build answers build.run {accepted} and plays its script on a goroutine of its own, so
// the reading goroutine stays free for build.cancel.
func (s *sidecar) build(id json.RawMessage, params json.RawMessage) {
	var p struct {
		BuildID string   `json:"buildId"`
		Tasks   []string `json:"tasks"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		s.writeError(id, -32602, "build.run: the params could not be read")
		return
	}
	if s.cfg.Build == nil {
		s.writeError(id, -32000, "build.run: this fake has no build script")
		return
	}
	cancel := make(chan struct{})
	s.cmu.Lock()
	s.cancels[p.BuildID] = cancel
	s.cmu.Unlock()
	if d := s.cfg.Build.AcceptDelayMs; d > 0 {
		go func() {
			time.Sleep(time.Duration(d) * time.Millisecond)
			s.writeResult(id, map[string]any{"accepted": true})
			s.play(p.BuildID, cancel)
		}()
		return
	}
	s.writeResult(id, map[string]any{"accepted": true})
	go s.play(p.BuildID, cancel)
}

// cancel releases a build waiting for its cancellation.
func (s *sidecar) cancel(buildID string) {
	s.cmu.Lock()
	ch := s.cancels[buildID]
	delete(s.cancels, buildID)
	s.cmu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// play sends a build's notifications and its terminal event, in the order the script
// lists them.
func (s *sidecar) play(buildID string, cancel chan struct{}) {
	b := s.cfg.Build
	for i, step := range b.Steps {
		if step.Kind == "gate" {
			waitForFile(step.Path)
			continue
		}
		s.notify(buildID, step)
		if b.DieAfterSteps > 0 && i+1 >= b.DieAfterSteps {
			os.Exit(1)
		}
	}
	if b.WaitForCancel {
		<-cancel
		s.finished(buildID, "cancelled", b)
		return
	}
	if b.Silent {
		return
	}
	outcome := b.Outcome
	if outcome == "" {
		outcome = "success"
	}
	s.finished(buildID, outcome, b)
}

// waitForFile blocks until path exists, or a minute has passed, so a test that never
// opens its gate ends instead of hanging.
func waitForFile(path string) {
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
}

// notify writes one notification: no id, so the client routes it to the sink of the
// build it names (gradle.md §Protocol).
func (s *sidecar) notify(buildID string, step Step) {
	var method string
	var params any
	switch step.Kind {
	case "output":
		method, params = "event.build.output", map[string]any{
			"buildId": buildID, "stream": step.Stream, "lines": step.Lines,
		}
	case "taskFinished":
		method, params = "event.build.taskFinished", map[string]any{
			"buildId": buildID, "path": step.Path, "outcome": step.Outcome,
			"durationMs": step.DurationMs, "reason": step.Reason,
		}
	case "progress":
		method, params = "event.build.progress", map[string]any{
			"buildId": buildID, "counts": step.Counts, "activeTask": step.ActiveTask,
		}
	case "problem":
		method, params = "event.build.problem", map[string]any{
			"buildId": buildID, "problem": step.Problem,
		}
	default:
		return
	}
	s.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *sidecar) finished(buildID, outcome string, b *Build) {
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "event.build.finished",
		"params": map[string]any{
			"buildId":                buildID,
			"outcome":                outcome,
			"failures":               b.Failures,
			"failureProblems":        b.FailureProblems,
			"failureProblemsDropped": b.FailureProblemsDropped,
			"missingCapabilities":    b.MissingCapabilities,
		},
	})
}

func (s *sidecar) write(msg any) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.w.Write(raw)
	s.w.WriteByte('\n')
	s.w.Flush()
	s.mu.Unlock()
}

func (s *sidecar) writeResult(id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		s.writeError(id, -32603, err.Error())
		return
	}
	s.writeRawResult(id, raw)
}

func (s *sidecar) writeRawResult(id json.RawMessage, raw json.RawMessage) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": idOrNull(id), "result": raw})
}

func (s *sidecar) writeError(id json.RawMessage, code int, text string) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": idOrNull(id),
		"error": map[string]any{"code": code, "message": text}})
}

func (s *sidecar) writeBigLine(id json.RawMessage, n int) {
	raw, _ := json.Marshal(strings.Repeat("x", n))
	s.writeRawResult(id, raw)
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

func (s *sidecar) isOpen(root string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open[root]
}

func (s *sidecar) setOpen(root string, open bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == nil {
		s.open = map[string]bool{}
	}
	s.open[root] = open
}
