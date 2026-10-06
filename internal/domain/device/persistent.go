package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/reader"
)

// The persistent UI reader (ADR-0030), run by the daemon on the device and reached over
// `adb forward`. A readerClient is the daemon's handle on one device's reader: the
// forward, whether it has been started, and the per-device lock every read shares
// (plan m1f, R2). The one-shot reader of ADR-0029 remains the fallback.

// Plan m1f step 1 fixes the working numbers: the idle exit is 30 minutes, and a warm
// read is tens of milliseconds (median 14.4 ms, p95 460 ms), so 3 s is the warm read's
// margin. The first read pushes, starts, forwards and pings inside 8 s. Variables so a
// test is quick.
var (
	// readerStartBound bounds the start command and the pings after it.
	readerStartBound = 8 * time.Second
	// warmReadTimeout bounds one dump over the forward.
	warmReadTimeout = 3 * time.Second
	// readerForwardTimeout bounds `adb forward tcp:0 ...`.
	readerForwardTimeout = 5 * time.Second
	// psOutputCap bounds `ps -A -o PID,NAME`; forwardListCap bounds `adb forward --list`,
	// which lists the host's forwards for every device.
	psOutputCap    = 1 << 20
	forwardListCap = 1 << 16
	// readerCloseBound bounds ADB.Close, all devices together.
	readerCloseBound = 3 * time.Second
	// readerPingPause is the wait between ping attempts while a reader starts.
	readerPingPause = 100 * time.Millisecond
	// readerPingCheck is the 1 s rule of plan m1f step 7: a reader found alive by
	// `pidof droidyard-reader-<d12>` that does not answer ping within it is killed by
	// its pid and started again once. Step 1 recorded that a stalled client delays a
	// reader's next request up to 5 s, so such a reader is replaced here on purpose.
	readerPingCheck = time.Second
	// readerNameRE is the only name any pidof or kill acts on (plan m1f step 7; E6):
	// a name that does not match a reader's own shape is never sent to a device shell,
	// so a hostile forward name cannot smuggle a command.
	readerNameRE = regexp.MustCompile(`^droidyard-reader-[0-9a-f]{12}$`)
	// pidRE is the only pid any kill acts on.
	pidRE = regexp.MustCompile(`^[0-9]+$`)
)

const (
	// readerIdleSeconds is D2/ADR-0030: the reader ends itself after 30 minutes
	// without a dump.
	readerIdleSeconds = 1800
	// readerNamePrefix names the reader's abstract socket and its process.
	readerNamePrefix = "droidyard-reader-"
	// oneShotWarning starts the warnings[] entry of a read that fell back to the
	// one-shot reader because the persistent reader could not run (plan m1f, D3). The
	// persistent reader's reason follows it.
	oneShotWarning = "The persistent UI reader could not run on this device, so the screen was read with the one-shot reader Droidyard runs for a single read. Persistent reader: "
)

// readerClient is one device's persistent reader, owned by the ADB that created it.
type readerClient struct {
	serial string
	// name is `droidyard-reader-<d12>`: the abstract socket's name and the process's
	// --nice-name (plan m1f, D2).
	name string
	// remote is the dex's path on the device; it is pushed once and left there
	// (ADR-0030: the dex file may stay in /data/local/tmp).
	remote string

	// lock serialises this device's reads (plan m1f, R2). It is context-aware, not a
	// sync.Mutex, so a waiter inside its read's deadline gives up when the deadline
	// passes rather than hanging.
	lock chan struct{}

	mu       sync.Mutex
	port     string // the forward's host port, "" until forwarded
	launched bool   // the serve command ran at least once (or a reader was adopted)
	pushed   bool   // the dex was pushed by this daemon
	// reconciled is true once `forward --list` has been read for this client: the
	// forward list is consulted on the client's first read and after a failed read,
	// never on a warm read (plan m1f step 7).
	reconciled bool
	// killedForeign remembers reader names of another digest this daemon has already
	// killed, so a later reconciliation does not look them up again (plan m1f step 7).
	killedForeign map[string]bool
}

// reader returns the reader client for a device, creating it on first use.
func (a *ADB) reader(serial string) *readerClient {
	a.rdMu.Lock()
	defer a.rdMu.Unlock()
	if a.readers == nil {
		a.readers = map[string]*readerClient{}
	}
	c := a.readers[serial]
	if c == nil {
		c = &readerClient{
			serial: serial,
			name:   readerNamePrefix + reader.SHA256[:12],
			remote: readerRemote,
			lock:   make(chan struct{}, 1),
		}
		a.readers[serial] = c
	}
	return c
}

// acquire takes the device's read lock, waiting until ctx ends. The caller releases it.
func (c *readerClient) acquire(ctx context.Context) error {
	select {
	case c.lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *readerClient) release() { <-c.lock }

func (c *readerClient) portValue() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.port
}

func (c *readerClient) setPushed() {
	c.mu.Lock()
	c.pushed = true
	c.mu.Unlock()
}

func (c *readerClient) isPushed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pushed
}

func (c *readerClient) havePort() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.port != ""
}

// wasStarted reports whether a serve command ran or a reader was adopted: this daemon
// owns a reader to stop.
func (c *readerClient) wasStarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.launched
}

// didReconcile reports whether `forward --list` has been read for this client.
func (c *readerClient) didReconcile() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reconciled
}

// markReconciled records that the forward list was read, so a warm read does not ask
// again.
func (c *readerClient) markReconciled() {
	c.mu.Lock()
	c.reconciled = true
	c.mu.Unlock()
}

// rememberForeign records a reader name of another digest as killed. It reports true
// the first time and false afterwards, so a name is looked up and killed once (plan m1f
// step 7).
func (c *readerClient) rememberForeign(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.killedForeign == nil {
		c.killedForeign = map[string]bool{}
	}
	if c.killedForeign[name] {
		return false
	}
	c.killedForeign[name] = true
	return true
}

// clearPort forgets a forward that is gone, so the caller makes a new one.
func (c *readerClient) clearPort() {
	c.mu.Lock()
	c.port = ""
	c.mu.Unlock()
}

// setPort remembers a forward this daemon did not make under it (an earlier daemon's,
// still listed): the reader at the other end is the same one.
func (c *readerClient) setPort(port string) {
	c.mu.Lock()
	if port != "" {
		c.port = port
	}
	c.mu.Unlock()
}

// adopt takes over an earlier daemon's forward: the reader is this daemon's to use, so
// it counts as started and Close quits it and removes its forward (plan m1f step 7,
// ADR-0030).
func (c *readerClient) adopt(port string) {
	c.setStarted(port)
}

func (c *readerClient) setStarted(port string) {
	c.mu.Lock()
	c.launched = true
	if port != "" {
		c.port = port
	}
	c.mu.Unlock()
}

// persistentRead is the persistent reader's turn in read: ensure the reader is
// reachable, then take one dump over its forward.
func (a *ADB) persistentRead(parent context.Context, t *target, c *readerClient, start, end time.Time) (doc []byte, kind dumpKind, line, reason string) {
	if ok, why := a.ensureReader(parent, t, c, end); !ok {
		return nil, dumpOther, "", why
	}
	if cerr := parent.Err(); cerr != nil {
		return nil, dumpOther, "", cerr.Error()
	}
	port := c.portValue()
	if port == "" {
		return nil, dumpOther, "", "the reader's forward is gone"
	}
	deadline := time.Now().Add(warmReadTimeout)
	if deadline.After(end) {
		deadline = end
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), warmReadTimeout)
	if err != nil {
		// The forward no longer answers: it may have been removed (a lost forward) or
		// the reader may be gone. The chain restarts the reader once, which reconciles
		// the forward (plan m1f step 7).
		return nil, dumpUnreachable, "", "the reader's forward did not answer: " + err.Error()
	}
	defer conn.Close()
	// A read that is blocked on its connection deadline must still stop when the
	// caller's context ends (plan m1f step 5): close the connection, which unblocks
	// readLimited.
	stop := context.AfterFunc(parent, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(deadline)
	if _, err := io.WriteString(conn, "dump\n"); err != nil {
		return nil, dumpOther, "", "the reader's forward did not take the request"
	}
	resp, truncated, readErr := readLimited(conn, dumpOutputCap)
	return classifyReader(resp, truncated, readErr)
}

// ensureReader makes a device's reader reachable: reconcile the forward on the client's
// first read, push the dex once, start the process, create the forward and wait for ping.
// It is a no-op once the client has a forward. `forward --list` is not read on a warm
// read: the forward is reconciled on the first read, and after a failed read by
// restartReader. Its failures are reasons for the fallbacks, never errors: read decides
// what to do (plan m1f step 7).
func (a *ADB) ensureReader(parent context.Context, t *target, c *readerClient, end time.Time) (bool, string) {
	ctx, cancel := context.WithDeadline(parent, end.Add(-cleanupReserve))
	defer cancel()

	if !c.didReconcile() {
		stale, why := a.reconcileForward(ctx, t, c)
		// A listing failure with a cached port is ignored and the dump is tried (plan
		// m1f step 7); on the first read there is nothing to try.
		if why != "" && !c.havePort() {
			return false, why
		}
		c.markReconciled()
		// The first read is a slow path: stop a reader of another digest found by
		// process name, before this daemon uses or starts its own (plan m1f step 7).
		a.clearForeignReaders(ctx, t, c)
		if !c.havePort() && stale {
			// A forward to our socket that did not answer: kill its process before the
			// new start (plan m1f step 7).
			a.killOwnReader(ctx, t, c)
		}
	}
	if c.havePort() {
		return true, ""
	}

	// No forward but this daemon had started or adopted the reader: the forward was
	// lost (an adb server restart). Recreate it and see whether the reader behind it
	// still answers, rather than start a second one (ADR-0030; plan m1f step 7).
	if c.wasStarted() {
		if ok, why := a.forwardReader(ctx, t, c); !ok {
			return false, why
		}
		if a.pingPort(ctx, c.portValue(), boundBy(time.Now().Add(readerPingCheck), end)) {
			return true, ""
		}
		a.killOwnReader(ctx, t, c)
	}

	// The slow path: a reader of another digest, found by process name, holds the
	// device's one UiAutomation and must be stopped before this daemon starts (plan m1f
	// step 7).
	a.clearForeignReaders(ctx, t, c)
	if !c.isPushed() {
		if ok, why := a.pushReader(ctx, t, c); !ok {
			return false, why
		}
	}
	// The start bound is measured from the start command, not from the read's start:
	// a slow push does not spend a reader start that has not begun (plan m1f step 5).
	// It is still capped by the read's end.
	started := time.Now()
	if ok, why := a.startServe(ctx, t, c); !ok {
		return false, why
	}
	if !c.havePort() {
		if ok, why := a.forwardReader(ctx, t, c); !ok {
			return false, why
		}
	}
	return a.pingReader(ctx, c, boundBy(started.Add(readerStartBound), end))
}

// restartReader runs the serve command again for a reader found gone, wedged or behind a
// lost forward (ADR-0030: started again once), then waits for ping. The failed read is
// reconciled first (the forward may be gone); a reader of our name that answers ping is
// reused, and one that does not is killed by its own pid and waited for before the new
// start. A reader of another digest is stopped first, since it holds the device's one
// UiAutomation (plan m1f step 7).
func (a *ADB) restartReader(parent context.Context, t *target, c *readerClient, end time.Time) (bool, string) {
	ctx, cancel := context.WithDeadline(parent, end.Add(-cleanupReserve))
	defer cancel()

	// The failed read is what drove this restart: reconcile the forward (it may have
	// been removed). A listing failure with a cached port is ignored: the dump is tried.
	if _, why := a.reconcileForward(ctx, t, c); why != "" && !c.havePort() {
		return false, why
	}
	if !c.havePort() {
		if !c.wasStarted() {
			return false, "the reader's forward is gone"
		}
		if ok, why := a.forwardReader(ctx, t, c); !ok {
			return false, why
		}
	}
	// A reader of our name may be alive and merely unreachable. If it answers ping the
	// same forward is usable and no second reader is started (plan m1f step 7).
	if a.pingPort(ctx, c.portValue(), time.Now().Add(readerPingCheck)) {
		return true, ""
	}
	a.killOwnReader(ctx, t, c)
	// Before any start: a foreign reader holds the device's one UiAutomation, and an
	// adopted reader's dex was not pushed by this daemon, so it is put back before its
	// replacement starts (plan m1f step 7).
	a.clearForeignReaders(ctx, t, c)
	if !c.isPushed() {
		if ok, why := a.pushReader(ctx, t, c); !ok {
			return false, why
		}
	}
	// As in ensureReader: the bound is measured from the start command.
	started := time.Now()
	if ok, why := a.startServe(ctx, t, c); !ok {
		return false, why
	}
	return a.pingReader(ctx, c, boundBy(started.Add(readerStartBound), end))
}

// forwardEntry is one line of `adb forward --list`.
type forwardEntry struct {
	serial string
	port   string
	remote string // the local socket's name, without "localabstract:"
}

// reconcileForward brings a device's reader forward in line with the adb server before a
// read (plan m1f step 7, ADR-0030): a forward to our socket that is gone is forgotten (a
// lost forward); an earlier daemon's forward to our socket is adopted when it answers and
// removed when it does not; a reader of another digest is killed, since it holds the
// device's one UiAutomation. Nothing not named droidyard-reader-* is read, removed or
// killed, and another digest's forward is left alone (E6). stale reports an earlier
// forward to our socket that did not answer, so the caller stops its process before
// starting a new one.
func (a *ADB) reconcileForward(ctx context.Context, t *target, c *readerClient) (stale bool, why string) {
	entries, why := a.forwardList(ctx, t)
	if why != "" {
		return false, why
	}
	a.killForeignReaders(ctx, t, c, entries)
	ours, found := forwardByName(entries, t.serial, c.name)
	switch {
	case !found:
		// Gone, as after an adb server restart: forget the port so it is recreated.
		c.clearPort()
		return false, ""
	case c.havePort() && c.portValue() == ours.port:
		return false, ""
	case c.havePort():
		// Listed under another port than the cached one: use the listed one.
		c.setPort(ours.port)
		return false, ""
	}
	// No cached port and a forward to our socket exists: an earlier daemon's. Adopt it
	// when it answers; otherwise remove it so the new forward is not a stale one beside
	// the new (plan m1f step 7).
	if a.pingPort(ctx, ours.port, time.Now().Add(readerPingCheck)) {
		c.adopt(ours.port)
		return false, ""
	}
	a.removeForward(ctx, t, ours.port)
	return true, ""
}

// killForeignReaders stops every process named droidyard-reader-* of another digest that
// this device's forwards name. Only a name of a reader's own shape reaches a shell
// (readerNameRE), only by pidof then kill, and each name once (E6; plan m1f step 7).
func (a *ADB) killForeignReaders(ctx context.Context, t *target, c *readerClient, entries []forwardEntry) {
	for _, e := range entries {
		if e.serial != t.serial || e.remote == c.name {
			continue
		}
		if !readerNameRE.MatchString(e.remote) {
			continue // never pass a name that is not a reader's own shape to a shell
		}
		if !c.rememberForeign(e.remote) {
			continue
		}
		a.killReader(ctx, t, e.remote)
	}
}

// deviceProc is one process `ps -A -o PID,NAME` listed.
type deviceProc struct {
	pid  string
	name string
}

// clearForeignReaders stops readers of another digest found by `ps -A -o PID,NAME`: a
// foreign reader holds the device's one UiAutomation even when its forward is gone, so it
// is found by process name, not only by forward (plan m1f step 7). It runs only on the
// slow path, never on a warm read, and acts only on names of a reader's own shape and
// pids ps gave (E6).
func (a *ADB) clearForeignReaders(ctx context.Context, t *target, c *readerClient) {
	for _, p := range a.psReaders(ctx, t) {
		if p.name == c.name {
			continue // our own reader is handled by killOwnReader
		}
		// Remembered by name and pid: a reader of that name that comes back is a new
		// process holding UiAutomation again, and is killed again.
		if !c.rememberForeign(p.name + "@" + p.pid) {
			continue
		}
		a.killPid(ctx, t, p.pid)
	}
}

// psReaders lists this device's processes named like a reader with the pid ps gave.
func (a *ADB) psReaders(ctx context.Context, t *target) []deviceProc {
	// An emulator lists hundreds of processes (11 KB observed on Pixel_9a) and a reader,
	// a recent process, comes last; a cut listing is unknown, never acted on.
	out, err := a.Run(ctx, t.adb, readerCloseBound, psOutputCap, "-s", t.serial, "shell", "ps", "-A", "-o", "PID,NAME")
	if err != nil || out.ExitCode != 0 || out.Truncated {
		return nil
	}
	return parsePSReaders(out.Stdout)
}

// parsePSReaders keeps the `ps -A -o PID,NAME` lines whose name is a reader's own shape;
// the header (PID NAME) and any other process do not match.
func parsePSReaders(out []byte) []deviceProc {
	var procs []deviceProc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !readerNameRE.MatchString(f[1]) {
			continue
		}
		procs = append(procs, deviceProc{pid: f[0], name: f[1]})
	}
	return procs
}

// killOwnReader stops a process of our name and waits, bounded, until it is gone, so the
// new reader can bind the abstract socket alone (plan m1f step 7; E6). It is a no-op when
// none is alive.
func (a *ADB) killOwnReader(ctx context.Context, t *target, c *readerClient) {
	pids := a.pidOf(ctx, t, c.name)
	if len(pids) == 0 {
		return
	}
	for _, pid := range pids {
		a.killPid(ctx, t, pid)
	}
	a.waitPidGone(ctx, t, c.name, pids)
}

// waitPidGone polls pidof until none of pids is reported, up to the 1 s check, so a start
// does not race a process that is still dying (plan m1f step 7).
func (a *ADB) waitPidGone(ctx context.Context, t *target, name string, pids []string) {
	if len(pids) == 0 || !readerNameRE.MatchString(name) {
		return
	}
	want := map[string]bool{}
	for _, p := range pids {
		want[p] = true
	}
	deadline := time.Now().Add(readerPingCheck)
	for {
		remaining := false
		for _, p := range a.pidOf(ctx, t, name) {
			if want[p] {
				remaining = true
				break
			}
		}
		if !remaining || time.Now().After(deadline) {
			return
		}
		select {
		case <-time.After(readerPingPause):
		case <-ctx.Done():
			return
		}
	}
}

// forwardList asks adb for the host's forwards and parses them. Its failure is a reason
// for the fallback, never an error.
func (a *ADB) forwardList(ctx context.Context, t *target) ([]forwardEntry, string) {
	out, err := a.Run(ctx, t.adb, readerForwardTimeout, forwardListCap, "-s", t.serial, "forward", "--list")
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return nil, "adb forward --list timed out"
		}
		return nil, "adb forward --list: " + err.Error()
	}
	if out.ExitCode != 0 {
		return nil, fmt.Sprintf("adb forward --list exited with status %d: %s", out.ExitCode, decisiveLine(out))
	}
	if out.Truncated {
		// A listing cut at the output cap is no listing: acting on its prefix could
		// adopt or remove a forward that was not really there (plan m1f step 7).
		return nil, "adb forward --list was too long"
	}
	return parseForwardList(out.Stdout), ""
}

// parseForwardList reads `adb forward --list`'s lines: `<serial> tcp:<port>
// <remote>`, as step 1 recorded (`emulator-5554 tcp:63552
// localabstract:droidyard-reader-31edb33baa07`). Only local-socket forwards are kept.
func parseForwardList(out []byte) []forwardEntry {
	var entries []forwardEntry
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[1], "tcp:") {
			continue
		}
		name := strings.TrimPrefix(f[2], "localabstract:")
		if name == f[2] {
			continue // not a local socket forward
		}
		entries = append(entries, forwardEntry{serial: f[0], port: strings.TrimPrefix(f[1], "tcp:"), remote: name})
	}
	return entries
}

// forwardByName finds the entry for a device's local socket.
func forwardByName(entries []forwardEntry, serial, name string) (forwardEntry, bool) {
	for _, e := range entries {
		if e.serial == serial && e.remote == name {
			return e, true
		}
	}
	return forwardEntry{}, false
}

// removeForward removes one forward this daemon owns, by its host port. It never removes
// all forwards and never another name's (E6).
func (a *ADB) removeForward(ctx context.Context, t *target, port string) {
	if port == "" {
		return
	}
	a.Run(ctx, t.adb, readerForwardTimeout, 1<<10, "-s", t.serial, "forward", "--remove", "tcp:"+port)
}

// pidOf lists the pids named name on the device; empty on any failure, and for a name
// that is not a reader's own shape, so no other name reaches the device's shell (E6;
// plan m1f step 7).
func (a *ADB) pidOf(ctx context.Context, t *target, name string) []string {
	if !readerNameRE.MatchString(name) {
		return nil
	}
	out, err := a.Run(ctx, t.adb, readerCloseBound, 1<<10, "-s", t.serial, "shell", "pidof", name)
	if err != nil || out.ExitCode != 0 {
		return nil
	}
	return strings.Fields(string(out.Stdout))
}

// killReader stops every process named name, only that name, by pidof then kill (E6).
func (a *ADB) killReader(ctx context.Context, t *target, name string) {
	for _, pid := range a.pidOf(ctx, t, name) {
		a.killPid(ctx, t, pid)
	}
}

// killPid kills one pid on the device, as `adb shell kill <pid>`. A pid that is not all
// digits is never sent (plan m1f step 7).
func (a *ADB) killPid(ctx context.Context, t *target, pid string) {
	if !pidRE.MatchString(pid) {
		return
	}
	a.Run(ctx, t.adb, readerCloseBound, 1<<10, "-s", t.serial, "shell", "kill", pid)
}

// pushReader puts the embedded dex on the device once. It is the same file the one-shot
// reader uses; the persistent reader leaves it (ADR-0030), and only a daemon that
// restarts pushes it again.
func (a *ADB) pushReader(ctx context.Context, t *target, c *readerClient) (bool, string) {
	dir, err := os.MkdirTemp("", "droidyard-reader-")
	if err != nil {
		return false, "temporary directory unavailable"
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "reader.dex")
	if err := os.WriteFile(local, reader.Dex(), 0o600); err != nil {
		return false, "temporary file unavailable"
	}
	out, err := a.Run(ctx, t.adb, readerPush, 1<<10, "-s", t.serial, "push", local, c.remote)
	if _, kind, line := classifyDump(t.serial, "push reader.dex", out, err, false); kind != dumpOK {
		return false, readerReason(out, err) + ": " + line
	}
	c.setPushed()
	return true, ""
}

// startServe runs the serve command of plan m1f step 1: `CLASSPATH=<dex> nohup
// app_process /system/bin --nice-name=droidyard-reader-<d12> DroidyardReader serve
// droidyard-reader-<d12> <idleSeconds>`, backgrounded so the adb shell returns. The
// process outlives its shell by nohup (ADR-0030).
func (a *ADB) startServe(ctx context.Context, t *target, c *readerClient) (bool, string) {
	script := "CLASSPATH=" + c.remote + " nohup app_process /system/bin --nice-name=" + c.name +
		" " + reader.Class + " serve " + c.name + " " + strconv.Itoa(readerIdleSeconds) + " >/dev/null 2>&1 &"
	out, err := a.Run(ctx, t.adb, readerStartBound, 1<<10, "-s", t.serial, "shell", script)
	if _, kind, line := classifyDump(t.serial, "shell "+reader.Class+" serve", out, err, false); kind != dumpOK {
		return false, readerReason(out, err) + ": " + line
	}
	c.setStarted("")
	return true, ""
}

// forwardReader creates the host-to-device forward and remembers its port. `tcp:0`
// makes the adb server pick a free port, which it prints.
func (a *ADB) forwardReader(ctx context.Context, t *target, c *readerClient) (bool, string) {
	out, err := a.Run(ctx, t.adb, readerForwardTimeout, 1<<10, "-s", t.serial, "forward", "tcp:0", "localabstract:"+c.name)
	if err != nil || out.ExitCode != 0 {
		return false, "adb forward failed: " + decisiveLine(out)
	}
	port := strings.TrimSpace(string(out.Stdout))
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return false, "adb forward printed no port"
	}
	c.setStarted(port)
	return true, ""
}

// pingReader asks the reader to answer until deadline, so a start that is still coming up
// is waited for rather than failed.
func (a *ADB) pingReader(ctx context.Context, c *readerClient, deadline time.Time) (bool, string) {
	port := c.portValue()
	if port == "" {
		return false, "the reader's forward is gone"
	}
	if a.pingPort(ctx, port, deadline) {
		return true, ""
	}
	return false, "the reader did not answer ping"
}

// pingPort asks the reader at a forwarded port to answer until deadline. It is the one
// ping used for the start wait and for step 7's 1 s check (readerPingCheck) before a
// reader is killed and replaced.
func (a *ADB) pingPort(ctx context.Context, port string, deadline time.Time) bool {
	if port == "" {
		return false
	}
	for {
		if time.Now().After(deadline) {
			return false
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), readerPingPause*5)
		if err == nil {
			conn.SetDeadline(deadline)
			// A ping blocked on its deadline still stops when ctx ends.
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			if _, err := io.WriteString(conn, "ping\n"); err == nil {
				resp, _, _ := readLimited(conn, 256)
				stop()
				conn.Close()
				if strings.HasPrefix(string(resp), "ok ") {
					return true
				}
			} else {
				stop()
				conn.Close()
			}
		}
		t := time.NewTimer(readerPingPause)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return false
		}
	}
}

// classifyReader turns one forward response into the chain's kind. A warm read that
// times out is a wedged reader, and an empty answer is a reader that is gone (its
// connection ended before a document): both are restarted once (plan m1f step 7). A
// reader line is its own failure; a partial or missing document falls to the one-shot
// reader.
func classifyReader(resp []byte, truncated bool, readErr error) ([]byte, dumpKind, string, string) {
	if truncated {
		return nil, dumpOther, "", "the reader printed more than the output limit"
	}
	if readErr != nil {
		if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
			return nil, dumpUnreachable, "", "the reader did not finish within " + warmReadTimeout.String()
		}
		if len(resp) == 0 {
			return nil, dumpVanished, "", "reading the reader's answer failed: " + readErr.Error()
		}
	}
	if len(resp) == 0 {
		return nil, dumpVanished, "", "the reader's connection ended before it wrote a document"
	}
	if bytes.Contains(resp, []byte("<hierarchy")) && bytes.Contains(resp, []byte("</hierarchy>")) {
		return resp, dumpOK, "", ""
	}
	line := strings.TrimSpace(string(resp))
	if strings.HasPrefix(line, readerPrefix) {
		if strings.Contains(strings.ToLower(line), "no active window") {
			// The reader's own line is device text; the cause is the fixed sentence,
			// as on the one-shot path (D3, R5).
			return nil, dumpNoRoot, readerNoRootMessage, readerNoRootMessage
		}
		return nil, dumpOther, "", truncate(line, 200)
	}
	if bytes.Contains(resp, []byte("<hierarchy")) {
		return nil, dumpOther, "", fmt.Sprintf("the reader sent an incomplete hierarchy (%d bytes)", len(resp))
	}
	return nil, dumpOther, "", "the reader answered no hierarchy"
}

// readLimited reads a connection to EOF, keeping at most limit bytes. truncated says the
// stream had more; err is the read error, net.Error on a deadline.
func readLimited(r io.Reader, limit int) ([]byte, bool, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, int64(limit)+1))
	if int(n) > limit {
		return buf.Bytes()[:limit], true, err
	}
	return buf.Bytes(), false, err
}

// boundBy is the earlier of two times.
func boundBy(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Close stops every UI reader this daemon used and removes every forward it made: `quit`
// to each reader and `adb forward --remove tcp:<port>` for each forward, per device in
// parallel, all within readerCloseBound (plan m1f step 5). It is called after the
// daemon's operations have drained. It never issues `forward --remove-all`.
func (a *ADB) Close(ctx context.Context) {
	a.rdMu.Lock()
	clients := make([]*readerClient, 0, len(a.readers))
	for _, c := range a.readers {
		clients = append(clients, c)
	}
	a.rdMu.Unlock()
	if len(clients) == 0 {
		return
	}
	adb := a.Resolve().Path
	// Close's own bound, not the shutdown context's: operations may have drained the
	// caller's context, and a forward that is left behind outlives the daemon. The
	// removal still happens, bounded by readerCloseBound (plan m1f step 5).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readerCloseBound)
	defer cancel()
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *readerClient) {
			defer wg.Done()
			c.close(ctx, a, adb)
		}(c)
	}
	wg.Wait()
}

// close quits one reader and removes its forward. A reader that was started but never
// forwarded (its forward failed) cannot be reached over the local port, so it is killed
// by its process name, pidof then kill, and only that name (E6). All of it is best
// effort inside ctx: a device that never answers must not hold the daemon's shutdown
// past its bound.
func (c *readerClient) close(ctx context.Context, a *ADB, adb string) {
	c.mu.Lock()
	port, launched := c.port, c.launched
	c.mu.Unlock()
	if !launched {
		return
	}
	quit := false
	if port != "" {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), readerPingPause*5); err == nil {
			conn.SetDeadline(time.Now().Add(readerPingPause * 5))
			if _, err := io.WriteString(conn, "quit\n"); err == nil {
				quit = true
			}
			io.Copy(io.Discard, io.LimitReader(conn, 64))
			conn.Close()
		}
	}
	if !quit && adb != "" && readerNameRE.MatchString(c.name) {
		// Not told to quit: never forwarded, or the forward did not answer. Kill the
		// process by its own name, never a broader pattern, and only an all-digit pid.
		if out, err := a.Run(ctx, adb, readerCloseBound, 1<<10, "-s", c.serial, "shell", "pidof", c.name); err == nil && out.ExitCode == 0 {
			for _, pid := range strings.Fields(string(out.Stdout)) {
				if pidRE.MatchString(pid) {
					a.Run(ctx, adb, readerCloseBound, 1<<10, "-s", c.serial, "shell", "kill", pid)
				}
			}
		}
	}
	if port == "" || adb == "" {
		return
	}
	a.Run(ctx, adb, readerCloseBound, 1<<10, "-s", c.serial, "forward", "--remove", "tcp:"+port)
}
