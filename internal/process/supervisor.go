package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/store"
	"golang.org/x/sys/unix"
)

// Scope is who a supervised process serves: Global, or one workspace.
type Scope string

// Global is the scope of a process shared by every workspace.
const Global Scope = "global"

// WorkspaceScope is the scope of a process owned by the workspace id.
func WorkspaceScope(id string) Scope { return Scope("workspace:" + id) }

// workspace is the workspace id of a workspace scope, "" for Global.
func (s Scope) workspace() string {
	id, _ := strings.CutPrefix(string(s), "workspace:")
	if Scope(id) == s {
		return ""
	}
	return id
}

// ShutdownPolicy is how a process is stopped: Signal to its group, then SIGKILL to the
// group once Timeout has passed.
type ShutdownPolicy struct {
	Signal  syscall.Signal
	Timeout time.Duration
}

// Descriptor is everything the supervisor needs to run a process
// (process-lifecycle.md §Model, without health and restart, which come with the first
// shipped sidecar).
type Descriptor struct {
	ID      string
	Kind    Kind
	Command string
	Args    []string
	// Dir is the working directory; "" is the daemon's.
	Dir string
	// Env is the whole environment; nil is the daemon's.
	Env   []string
	Scope Scope
	// Ready is the readiness probe, run on each line of the process's output until
	// one passes: never a sleep (process-lifecycle.md §Startup).
	Ready func(line string) bool
	// IdleTimeout is how long the process may go unused before it is stopped; zero
	// keeps it until an explicit stop.
	IdleTimeout time.Duration
	Shutdown    ShutdownPolicy
	// LogLines bounds the log sink, the last lines of output kept; zero means
	// DefaultLogLines (process-lifecycle.md §Output handling).
	LogLines int

	// Protocol marks stdout as a protocol channel rather than log output: each line is
	// handed to Lines (never to the sink, which then keeps stderr only) up to
	// MaxProtocolLine. A line longer than that is a protocol violation: pid's group is
	// killed and the in-flight call sees it through Lease.Err (gradle.md §Protocol;
	// m1g D6). stderr still goes to the log sink.
	Protocol bool
	// Lines receives each complete protocol line, in order, on the drain goroutine: it
	// must not block, and a line is never delivered before it has been read whole (an
	// unterminated last line at EOF is dropped).
	Lines func(line string)
	// MaxProtocolLine bounds one protocol line, in bytes; zero means
	// DefaultMaxProtocolLine. A line over it is a protocol violation.
	MaxProtocolLine int
	// OnSpawn runs once after the process is spawned and before the first line is read:
	// the send side of a handshake. A returned error kills the group before readiness.
	OnSpawn func(pid int, send func(line string) error) error
}

// DefaultLogLines is the log sink's bound when a descriptor sets none.
const DefaultLogLines = 1000

// maxLine bounds one line of output in the sink; the rest of a longer line is dropped.
const maxLine = 64 << 10

// DefaultMaxProtocolLine bounds one protocol line when a descriptor sets none: a
// project model on a large build can be hundreds of KiB, so the bound is generous, and
// a line past it is a protocol error, not a truncation (m1g D6, as revised in step 1).
const DefaultMaxProtocolLine = 16 << 20

// State is where a supervised process stands (process-lifecycle.md §Lifecycle states).
type State string

const (
	Starting State = "starting"
	Ready    State = "ready"
	Busy     State = "busy"
	Idle     State = "idle"
	Stopping State = "stopping"
	Stopped  State = "stopped"
	Failed   State = "failed"
)

// ErrClosed is Use after Shutdown.
var ErrClosed = errors.New("the supervisor has shut down")

// ErrUnrecordable is Use while processes.json cannot be written: a process it could
// not record could outlive the daemon unseen, so none is spawned (plan m1c, R10).
var ErrUnrecordable = errors.New("processes.json cannot be written, so no process is started")

// ErrProtocol wraps the reason a Protocol process broke its line protocol (a line
// over the bound): Lease.Err and the readiness error carry it, so the client tells a
// crash from a plain exit (gradle.md §Protocol; plan m1g D6).
var ErrProtocol = errors.New("protocol violation")

// Options configure a Supervisor.
type Options struct {
	// Store holds processes.json.
	Store *store.Store
	// Log receives a line per failed start, unexpected exit and forced kill.
	Log io.Writer
}

// Supervisor starts described processes on first use, stops them when idle or when
// told, and records each one's pid while it runs (ADR-0021 §Pid records).
type Supervisor struct {
	st    *store.Store
	log   io.Writer
	spawn chan spawnRequest
	quit  chan struct{} // closed by Shutdown: the spawner thread ends

	mu     sync.Mutex
	procs  map[string]*proc
	closed bool
	// refuse says why nothing may be spawned: processes.json not loaded yet, of a
	// newer version, or unreadable. Empty once loaded.
	refuse  string
	records []Record
}

type spawnRequest struct {
	cmd  *exec.Cmd
	done chan error
}

// proc is one run of a described process; a restart is a new proc.
type proc struct {
	d Descriptor

	// Set before spawned closes.
	pid   int
	stdin *os.File
	sink  *sink
	// wmu serialises writes to stdin, so leases sharing one process never interleave a
	// protocol line.
	wmu sync.Mutex

	// Guarded by Supervisor.mu.
	state    State
	leases   int
	idle     *time.Timer
	idleN    int   // which arming of the idle timer is current
	protoErr error // a protocol violation that killed it, if any
	exitErr  error // how cmd.Wait saw it exit, if it has

	spawned  chan struct{} // closed once the spawn succeeded or failed
	ready    chan struct{} // closed once ready, or once it can never be
	readyErr error         // set before ready closes, if it failed
	exited   chan struct{} // closed once the leader is reaped
	finished chan struct{} // closed once stopped or failed, its record removed
}

// New returns a supervisor over the state in st. It spawns nothing until Load has
// read processes.json, which the daemon calls once Reconcile has emptied it.
func New(opts Options) *Supervisor {
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	s := &Supervisor{st: opts.Store, log: opts.Log, spawn: make(chan spawnRequest), quit: make(chan struct{}),
		procs: map[string]*proc{}, refuse: "processes.json has not been loaded"}
	go s.spawner()
	return s
}

// Load reads processes.json and lets Use spawn. Records it finds are kept, never
// overwritten unread. One of a newer version, or one that cannot be read, keeps every
// spawn refused (plan m1c, R10): a process the daemon cannot record could outlive it
// unseen.
func (s *Supervisor) Load() error {
	var recs Records
	out, err := s.st.Read(RecordsFile, store.Version, &recs)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case errors.Is(err, store.ErrNewer):
		s.refuse = "it has a newer schemaVersion"
		return err
	case err != nil:
		s.refuse = "it cannot be read: " + err.Error()
		return fmt.Errorf("reading %s: %w", RecordsFile, err)
	case out == store.Loaded:
		s.records = recs.Processes
	}
	s.refuse = ""
	return nil
}

// spawner starts every child from one goroutine locked to its OS thread for the
// supervisor's life: on Linux, Pdeathsig fires when the thread that forked a child
// exits, not the process (process-lifecycle.md §Shutdown, layer 2). Every kind but
// shell-command asks for that SIGTERM; shell.exec's does not (deathSignal).
func (s *Supervisor) spawner() {
	runtime.LockOSThread()
	for {
		select {
		case r := <-s.spawn:
			r.done <- r.cmd.Start()
		case <-s.quit:
			return
		}
	}
}

// Spawn starts cmd through the locked spawner thread, in a process group of its own
// (and, on Linux, with Pdeathsig), for a child that is neither supervised nor
// recorded: a short per-operation command whose caller waits for it itself
// (ADR-0021 §Pid records: such commands are not recorded; process-lifecycle.md
// §Shutdown, layer 2). It has no kind of its own and keeps the death signal. It
// overwrites cmd.SysProcAttr. The caller owns the child: it must call cmd.Wait.
func (s *Supervisor) Spawn(cmd *exec.Cmd) error {
	cmd.SysProcAttr = sysProcAttr("")
	done := make(chan error, 1)
	select {
	case s.spawn <- spawnRequest{cmd: cmd, done: done}:
		return <-done
	case <-s.quit:
		return ErrClosed
	}
}

// Recorded is a process the supervisor started and records while it runs, without
// supervising it (SpawnRecorded): a long-lived child that must not outlive the daemon
// unseen. Its record is removed when the caller reaps it (Wait).
type Recorded struct {
	s    *Supervisor
	cmd  *exec.Cmd
	pid  int
	once sync.Once
}

// Pid is the recorded process's pid.
func (r *Recorded) Pid() int { return r.pid }

// SignalGroup sends sig to the recorded process's entire group (never its pid alone):
// the caller that kills a shell.exec command's group uses it, so a background child the
// command left behind is killed too (plan m1k D5). A group with no member left is
// ESRCH, which the caller treats as "already gone".
func (r *Recorded) SignalGroup(sig syscall.Signal) error {
	return unix.Kill(-r.pid, sig)
}

// Wait reaps the process and removes its record. Call it once, however the process
// ended — by itself, by its pid, or by its group (SignalGroup); a record left behind
// outlives the process and is reaped by the next daemon's Reconcile.
func (r *Recorded) Wait() error {
	var err error
	r.once.Do(func() {
		err = r.cmd.Wait()
		r.s.removeRecord(r.pid)
	})
	return err
}

// SpawnRecorded starts cmd through the locked spawner thread, in a process group of its
// own, and writes its pid record while it runs: a long-lived child that is neither
// supervised nor stopped when idle, but must be findable after the daemon dies, so the
// next daemon's Reconcile reaps it (ADR-0021 §Pid records: the durable exception;
// plan m1i D9). A shell.exec command (kind ShellCommand) is spawned without the death
// signal, so its leader survives the daemon and Reconcile can reach its group
// (plan m1k D5); every other kind is spawned with it, as before. It overwrites
// cmd.SysProcAttr. The caller owns the returned handle and must call Wait.
//
// When the record cannot be written the child is killed and no handle is returned. A
// shell-command's whole group is killed — nothing outside it was forked into it, and a
// background child the command left would otherwise outlive the daemon unrecorded. Every
// other kind is killed by its pid, never its group, which a forked adb server shares
// (m1e R8).
func (s *Supervisor) SpawnRecorded(cmd *exec.Cmd, kind Kind) (*Recorded, error) {
	s.mu.Lock()
	closed, refuse := s.closed, s.refuse
	s.mu.Unlock()
	switch {
	case closed:
		return nil, ErrClosed
	case refuse != "":
		return nil, fmt.Errorf("%w: %s", ErrUnrecordable, refuse)
	}
	cmd.SysProcAttr = sysProcAttr(kind)
	done := make(chan error, 1)
	select {
	case s.spawn <- spawnRequest{cmd: cmd, done: done}:
		if err := <-done; err != nil {
			return nil, err
		}
	case <-s.quit:
		return nil, ErrClosed
	}
	pid := cmd.Process.Pid
	started, _ := platform.StartTime(pid)
	rec := Record{ID: string(kind), Kind: kind, PID: pid, PGID: pid,
		StartTime: started, Command: append([]string(nil), cmd.Args...)}
	if beforeRecord != nil {
		beforeRecord(cmd)
	}
	if err := s.addRecord(rec); err != nil {
		if kind == ShellCommand {
			unix.Kill(-pid, unix.SIGKILL)
		} else {
			cmd.Process.Kill()
		}
		cmd.Wait()
		return nil, fmt.Errorf("%w: %v", ErrUnrecordable, err)
	}
	return &Recorded{s: s, cmd: cmd, pid: pid}, nil
}

// beforeRecord, when non-nil, runs between a recorded spawn and its record write. A test
// holds it until the command has forked a child, so a failed write's group kill is
// observable. Nil in production.
var beforeRecord func(*exec.Cmd)

func key(id string, scope Scope) string { return id + "\x00" + string(scope) }

func (s *Supervisor) logf(format string, args ...any) {
	fmt.Fprintf(s.log, "droidyard: supervisor: "+format+"\n", args...)
}

// Lease is a use of a running process. While any lease is held the process is never
// stopped for idleness (process-lifecycle.md §Idle shutdown).
type Lease struct {
	s    *Supervisor
	p    *proc
	once sync.Once
}

// Pid is the leased process's pid.
func (l *Lease) Pid() int { return l.p.pid }

// Stdin is the leased process's stdin, the pipe the supervisor holds. Its output is
// read from the log sink (Output).
func (l *Lease) Stdin() io.Writer { return l.p.stdin }

// Send writes one line to the process, adding the newline, with the writes of every
// lease serialised. It fails once the process's stdin is closed (a stopped process).
func (l *Lease) Send(line string) error { return l.p.send(line) }

// Done is closed once the process is reaped, however it ended.
func (l *Lease) Done() <-chan struct{} { return l.p.exited }

// Err is why the process ended: a protocol violation that killed it, else its exit
// error, else nil while it runs.
func (l *Lease) Err() error {
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	return l.p.err()
}

// err is how the process ended, guarded by Supervisor.mu.
func (p *proc) err() error {
	if p.protoErr != nil {
		return p.protoErr
	}
	return p.exitErr
}

// send writes one line to the process's stdin, serialised across leases.
func (p *proc) send(line string) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err := io.WriteString(p.stdin, line+"\n")
	return err
}

// Release ends the use. The last release starts the idle timer. Releasing twice, or
// after the process stopped, does nothing.
func (l *Lease) Release() {
	l.once.Do(func() {
		s, p := l.s, l.p
		s.mu.Lock()
		defer s.mu.Unlock()
		if p.state != Busy {
			return
		}
		p.leases--
		if p.leases == 0 {
			p.state = Idle
			s.armIdle(p)
		}
	})
}

// Use returns a lease on the process d describes, starting it if it is not running
// and waiting for its readiness probe (process-lifecycle.md §Startup: lazy, readiness
// per process). A done ctx fails Use at once and spawns nothing, so an operation
// cancelled by a close cannot start a sidecar again (ADR-0027); ctx ending while the
// process starts abandons the wait, not the start.
func (s *Supervisor) Use(ctx context.Context, d Descriptor) (*Lease, error) {
	for {
		s.mu.Lock()
		// Checked under the lock a lease is granted under, so no lease is granted on a
		// context already done when the lock was taken. A cancellation that lands after
		// this check is unordered with it by nature; a close that cancels the context
		// then drains the operation and stops the workspace's sidecars, so a lease
		// granted in that window does not outlive the close (ADR-0027).
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("%s not started: %w", d.ID, err)
		}
		switch {
		case s.closed:
			s.mu.Unlock()
			return nil, ErrClosed
		case s.refuse != "":
			why := s.refuse
			s.mu.Unlock()
			return nil, fmt.Errorf("%w: %s", ErrUnrecordable, why)
		}
		p := s.procs[key(d.ID, d.Scope)]
		if p == nil || p.state == Stopped || p.state == Failed {
			p = &proc{d: d, state: Starting, spawned: make(chan struct{}), ready: make(chan struct{}), exited: make(chan struct{}), finished: make(chan struct{})}
			s.procs[key(d.ID, d.Scope)] = p
			s.mu.Unlock()
			if err := s.start(p); err != nil {
				return nil, err
			}
			s.mu.Lock()
		}
		switch p.state {
		case Ready, Busy, Idle:
			p.leases++
			p.state = Busy
			s.disarmIdle(p)
			s.mu.Unlock()
			return &Lease{s: s, p: p}, nil
		case Stopping:
			s.mu.Unlock()
			select {
			case <-p.finished:
			case <-ctx.Done():
			}
			continue
		}
		s.mu.Unlock()
		select {
		case <-p.ready:
			if p.readyErr != nil {
				return nil, p.readyErr
			}
		case <-ctx.Done():
		}
	}
}

// start spawns p in a process group of its own, records its pid at once, and drains
// its output. It returns once the process runs; readiness is p.ready.
func (s *Supervisor) start(p *proc) error {
	fail := func(err error) error {
		err = fmt.Errorf("starting %s: %w", p.d.ID, err)
		s.logf("%v", err)
		s.mu.Lock()
		p.state = Failed
		s.mu.Unlock()
		p.readyErr = err
		close(p.spawned)
		close(p.ready)
		close(p.exited)
		close(p.finished)
		return err
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return fail(err)
	}
	// stderr is a pipe of its own in protocol mode, so the protocol channel (stdout) and
	// the log sink stay separate (m1g D6: stdout carries the protocol only).
	var errR, errW *os.File
	if p.d.Protocol {
		errR, errW, err = os.Pipe()
		if err != nil {
			stdinR.Close()
			stdinW.Close()
			outR.Close()
			outW.Close()
			return fail(err)
		}
	}
	cmd := exec.Command(p.d.Command, p.d.Args...)
	cmd.Dir, cmd.Env = p.d.Dir, p.d.Env
	cmd.Stdin, cmd.Stdout = stdinR, outW
	cmd.Stderr = outW
	if errW != nil {
		cmd.Stderr = errW
	}
	cmd.SysProcAttr = sysProcAttr(p.d.Kind)
	done := make(chan error, 1)
	select {
	case s.spawn <- spawnRequest{cmd: cmd, done: done}:
		err = <-done
	case <-s.quit:
		err = ErrClosed
	}
	stdinR.Close()
	outW.Close()
	if errW != nil {
		errW.Close()
	}
	if err != nil {
		stdinW.Close()
		outR.Close()
		if errR != nil {
			errR.Close()
		}
		return fail(err)
	}
	n := p.d.LogLines
	if n <= 0 {
		n = DefaultLogLines
	}
	p.pid, p.stdin, p.sink = cmd.Process.Pid, stdinW, &sink{max: n}

	// The record goes in as soon as the pid is known, before readiness: a daemon that
	// dies while its child starts still leaves the child findable.
	started, _ := platform.StartTime(p.pid)
	rec := Record{ID: p.d.ID, Kind: p.d.Kind, Workspace: p.d.Scope.workspace(), PID: p.pid, PGID: p.pid,
		StartTime: started, Command: append([]string{p.d.Command}, p.d.Args...)}
	if err := s.addRecord(rec); err != nil {
		unix.Kill(-p.pid, unix.SIGKILL)
		cmd.Wait()
		stdinW.Close()
		outR.Close()
		if errR != nil {
			errR.Close()
		}
		p.pid = 0
		return fail(fmt.Errorf("%w: %v", ErrUnrecordable, err))
	}
	close(p.spawned)
	go s.drain(p, outR)
	if errR != nil {
		go s.drainStderr(p, errR)
	}
	go s.wait(p, cmd)
	if p.d.OnSpawn != nil {
		if err := p.d.OnSpawn(p.pid, p.send); err != nil {
			s.logf("%s (pid %d): handshake: %v", p.d.ID, p.pid, err)
			unix.Kill(-p.pid, unix.SIGKILL)
		}
	}
	return nil
}

// drain reads the process's output line by line into its sink and runs the
// readiness probe on each line until one passes. It never blocks the process: the
// sink keeps the last lines only. A protocol process reads stdout through
// drainProtocol instead, and its stderr through drainStderr.
func (s *Supervisor) drain(p *proc, r *os.File) {
	defer r.Close()
	if p.d.Protocol {
		s.drainProtocol(p, r)
		return
	}
	br := bufio.NewReaderSize(r, 64<<10)
	probing := true
	for {
		line, err := readLine(br)
		if line != "" || err == nil {
			p.sink.add(line)
			if probing && p.d.Ready != nil && p.d.Ready(line) {
				probing = false
				s.markReady(p)
			}
		}
		if err != nil {
			return
		}
	}
}

// drainProtocol reads complete protocol lines and hands them to Lines, running the
// readiness probe on each. A line over the bound is a protocol violation: the group is
// killed and Lease.Err reports it (m1g D6). An unterminated last line at EOF is dropped
// (m1g step 1 fix round).
func (s *Supervisor) drainProtocol(p *proc, r *os.File) {
	br := bufio.NewReaderSize(r, 64<<10)
	max := p.d.MaxProtocolLine
	if max <= 0 {
		max = DefaultMaxProtocolLine
	}
	probing := true
	for {
		line, over, err := readProtocolLine(br, max)
		if over {
			s.protocolViolation(p, fmt.Errorf("%w: protocol line over %d MiB", ErrProtocol, max>>20))
			return
		}
		if err == nil {
			if p.d.Lines != nil {
				p.d.Lines(line)
			}
			if probing && p.d.Ready != nil && p.d.Ready(line) {
				probing = false
				s.markReady(p)
			}
		}
		if err != nil {
			return
		}
	}
}

// drainStderr reads stderr into the log sink, so a protocol process's diagnostics are
// still visible while its stdout stays the protocol channel.
func (s *Supervisor) drainStderr(p *proc, r *os.File) {
	defer r.Close()
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(br)
		if line != "" || err == nil {
			p.sink.add(line)
		}
		if err != nil {
			return
		}
	}
}

// protocolViolation records why and kills the process's group: the reaper then fails
// the in-flight call through Lease.Err.
func (s *Supervisor) protocolViolation(p *proc, err error) {
	s.mu.Lock()
	p.protoErr = err
	s.mu.Unlock()
	s.logf("%s (pid %d): %v; killing its group", p.d.ID, p.pid, err)
	unix.Kill(-p.pid, unix.SIGKILL)
}

// readLine reads one line, without its newline, keeping at most maxLine bytes of it.
func readLine(br *bufio.Reader) (string, error) {
	var b []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(b) < maxLine {
			b = append(b, chunk[:min(len(chunk), maxLine-len(b))]...)
		}
		if err != nil || !isPrefix {
			return string(b), err
		}
	}
}

// readProtocolLine reads one line whole, without its newline. over is true when the
// line was longer than max: the whole line is still consumed, but nothing of it past
// the bound is kept. A last line with no newline before the end of the stream is never
// returned: a process killed mid-write leaves half a message, which is a death, not a
// protocol violation (m1g step 1 fix round; step 5 review B3).
func readProtocolLine(br *bufio.Reader, max int) (line string, over bool, err error) {
	var b []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if !over {
			if len(b)+len(chunk) > max+1 {
				over = true
			} else {
				b = append(b, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if over {
			return "", true, nil
		}
		b = b[:len(b)-1]
		if n := len(b); n > 0 && b[n-1] == '\r' {
			b = b[:n-1]
		}
		return string(b), false, nil
	}
}

func (s *Supervisor) markReady(p *proc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.state != Starting {
		return
	}
	p.state = Ready
	s.armIdle(p)
	close(p.ready)
}

// wait reaps the process. An exit nobody asked for marks it failed: what is left of
// its group is killed and its record removed, and the next Use starts it afresh.
func (s *Supervisor) wait(p *proc, cmd *exec.Cmd) {
	err := cmd.Wait()
	s.mu.Lock()
	stopping := p.state == Stopping
	starting := p.state == Starting
	p.exitErr = err
	if !stopping {
		p.state = Failed
		s.disarmIdle(p)
	}
	s.mu.Unlock()
	close(p.exited)
	if stopping {
		return
	}
	s.logf("%s (pid %d) exited unexpectedly: %v", p.d.ID, p.pid, err)
	p.stdin.Close()
	if groupPresent(p.pid) {
		unix.Kill(-p.pid, unix.SIGKILL)
		waitGone([]Record{{PGID: p.pid}}, KillGrace)
	}
	s.removeRecord(p.pid)
	if starting {
		// A protocol violation carries its own cause: it is what the in-flight call
		// reports (BUILD_SIDECAR_CRASHED, "protocol line over 16 MiB").
		if p.protoErr != nil {
			p.readyErr = p.protoErr
		} else {
			p.readyErr = fmt.Errorf("%s exited before it was ready: %v; last output: %s", p.d.ID, err, strings.Join(p.sink.tail(5), " | "))
		}
		close(p.ready)
	}
	close(p.finished)
}

// stop ends p whatever its leases: an explicit stop (Stop, Shutdown).
func (s *Supervisor) stop(p *proc) {
	<-p.spawned
	s.mu.Lock()
	switch p.state {
	case Stopping, Stopped, Failed:
		s.mu.Unlock()
		<-p.finished
		return
	}
	wasStarting := p.state == Starting
	p.state = Stopping
	s.disarmIdle(p)
	s.mu.Unlock()
	s.terminate(p, wasStarting)
}

// terminate ends p, already marked stopping: stdin closed, the descriptor's signal to
// its group, SIGKILL to the group if any of it outlives the descriptor's timeout, then
// the record removed. It runs on the descriptor's timeout, never a caller's context,
// so a spent shutdown deadline cannot skip the SIGKILL.
func (s *Supervisor) terminate(p *proc, wasStarting bool) {
	p.stdin.Close()
	sig := p.d.Shutdown.Signal
	if sig == 0 {
		sig = unix.SIGTERM
	}
	unix.Kill(-p.pid, sig)
	if left := waitGone([]Record{{PGID: p.pid}}, p.d.Shutdown.Timeout); len(left) > 0 {
		// The group outlived its timeout. It still has members, so its pgid cannot
		// have been reused.
		unix.Kill(-p.pid, unix.SIGKILL)
		s.logf("%s (pid %d): killed; its group outlived %s by %s", p.d.ID, p.pid, sig, p.d.Shutdown.Timeout)
		if left := waitGone(left, KillGrace); len(left) > 0 {
			s.logf("%s: group %d still present %s after SIGKILL", p.d.ID, p.pid, KillGrace)
		}
	}
	select {
	case <-p.exited:
	case <-time.After(KillGrace):
		s.logf("%s (pid %d): not reaped %s after its group was killed", p.d.ID, p.pid, KillGrace)
	}
	s.removeRecord(p.pid)
	s.mu.Lock()
	p.state = Stopped
	s.mu.Unlock()
	if wasStarting {
		p.readyErr = fmt.Errorf("%s was stopped before it was ready", p.d.ID)
		close(p.ready)
	}
	close(p.finished)
}

// armIdle starts the idle timer of an unused process. Called with s.mu held.
func (s *Supervisor) armIdle(p *proc) {
	if p.d.IdleTimeout <= 0 || p.leases > 0 {
		return
	}
	s.disarmIdle(p)
	p.idleN++
	n := p.idleN
	p.idle = time.AfterFunc(p.d.IdleTimeout, func() { s.idleExpired(p, n) })
}

// disarmIdle cancels the idle timer. Called with s.mu held.
func (s *Supervisor) disarmIdle(p *proc) {
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	p.idleN++
}

// idleExpired stops p if it is still unused since timer n was armed: idle shutdown
// never interrupts an in-flight use (process-lifecycle.md §Idle shutdown).
func (s *Supervisor) idleExpired(p *proc, n int) {
	s.mu.Lock()
	if p.idleN != n || p.leases > 0 || (p.state != Ready && p.state != Idle) {
		s.mu.Unlock()
		return
	}
	// Marked under the same lock Use takes a lease under: no use can begin between
	// the check and the stop.
	p.state = Stopping
	s.disarmIdle(p)
	s.mu.Unlock()
	s.terminate(p, false)
}

// Stop stops every process of scope, leases or not, and returns once they are gone.
func (s *Supervisor) Stop(scope Scope) {
	s.stopAll(func(p *proc) bool { return p.d.Scope == scope })
}

// StopProcess stops the one process id runs in scope, if it is running, and returns
// once it is gone: a client refuses a sidecar that failed its handshake instead of
// leaving it running until the idle timeout.
func (s *Supervisor) StopProcess(id string, scope Scope) {
	s.mu.Lock()
	p := s.procs[key(id, scope)]
	s.mu.Unlock()
	if p != nil {
		s.stop(p)
	}
}

// Shutdown stops every process and refuses further Use. It runs on the descriptors'
// own timeouts and takes no context (process-lifecycle.md §Shutdown).
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	first := !s.closed
	s.closed = true
	s.mu.Unlock()
	s.stopAll(func(*proc) bool { return true })
	if first {
		close(s.quit)
	}
}

func (s *Supervisor) stopAll(match func(*proc) bool) {
	s.mu.Lock()
	var ps []*proc
	for _, p := range s.procs {
		if match(p) {
			ps = append(ps, p)
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.stop(p)
		}()
	}
	wg.Wait()
}

// State is the state of the process id runs in scope; "" if it never ran.
func (s *Supervisor) State(id string, scope Scope) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.procs[key(id, scope)]; p != nil {
		return p.state
	}
	return ""
}

// Pid is the pid of the process id runs in scope, or of its last run; 0 if none.
func (s *Supervisor) Pid(id string, scope Scope) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.procs[key(id, scope)]; p != nil {
		return p.pid
	}
	return 0
}

// Output is the log sink of the process id runs in scope: its last lines of output.
func (s *Supervisor) Output(id string, scope Scope) []string {
	s.mu.Lock()
	p := s.procs[key(id, scope)]
	s.mu.Unlock()
	if p == nil || p.sink == nil {
		return nil
	}
	return p.sink.tail(p.sink.max)
}

func (s *Supervisor) addRecord(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs := append(append([]Record(nil), s.records...), r)
	if err := s.st.Write(RecordsFile, Records{Processes: recs}); err != nil {
		return err
	}
	s.records = recs
	return nil
}

func (s *Supervisor) removeRecord(pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs := []Record{}
	for _, r := range s.records {
		if r.PID != pid {
			recs = append(recs, r)
		}
	}
	if err := s.st.Write(RecordsFile, Records{Processes: recs}); err != nil {
		s.logf("removing the record of pid %d: %v", pid, err)
	}
	s.records = recs
}

// sink is a log sink: the last max lines of a process's output.
type sink struct {
	mu    sync.Mutex
	max   int
	lines []string
	next  int
}

func (k *sink) add(line string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.lines) < k.max {
		k.lines = append(k.lines, line)
		return
	}
	k.lines[k.next] = line
	k.next = (k.next + 1) % k.max
}

// tail is the last n lines, oldest first.
func (k *sink) tail(n int) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	ordered := append(append([]string(nil), k.lines[k.next:]...), k.lines[:k.next]...)
	if len(ordered) > n {
		ordered = ordered[len(ordered)-n:]
	}
	return ordered
}
