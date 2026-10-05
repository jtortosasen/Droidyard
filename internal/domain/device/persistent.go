package device

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
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
	// readerCloseBound bounds ADB.Close, all devices together.
	readerCloseBound = 3 * time.Second
	// readerPingPause is the wait between ping attempts while a reader starts.
	readerPingPause = 100 * time.Millisecond
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
	launched bool   // the serve command ran at least once
	pushed   bool   // the dex is on the device
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
		return nil, dumpOther, "", "the reader's forward did not answer: " + err.Error()
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

// ensureReader makes a device's reader reachable: push the dex once, start the process,
// create the forward and wait for ping. It is a no-op once the client has a port. Its
// failures are reasons for the fallbacks, never errors: read decides what to do.
func (a *ADB) ensureReader(parent context.Context, t *target, c *readerClient, end time.Time) (bool, string) {
	if c.havePort() {
		return true, ""
	}
	ctx, cancel := context.WithDeadline(parent, end.Add(-cleanupReserve))
	defer cancel()

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
	if ok, why := a.forwardReader(ctx, t, c); !ok {
		return false, why
	}
	return a.pingReader(ctx, c, boundBy(started.Add(readerStartBound), end))
}

// restartReader runs the serve command again on an existing forward, for a reader found
// gone (ADR-0030: started again once), then waits for ping.
func (a *ADB) restartReader(parent context.Context, t *target, c *readerClient, end time.Time) (bool, string) {
	ctx, cancel := context.WithDeadline(parent, end.Add(-cleanupReserve))
	defer cancel()
	// As in ensureReader: the bound is measured from the start command.
	started := time.Now()
	if ok, why := a.startServe(ctx, t, c); !ok {
		return false, why
	}
	return a.pingReader(ctx, c, boundBy(started.Add(readerStartBound), end))
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
	for {
		if time.Now().After(deadline) {
			return false, "the reader did not answer ping"
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
					return true, ""
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
			return false, "the reader did not answer ping"
		}
	}
}

// classifyReader turns one forward response into the chain's kind. An empty answer is a
// reader that is gone (its connection ended before a document), which the read starts
// again once; a reader line is its own failure; a partial or missing document falls to
// the one-shot reader.
func classifyReader(resp []byte, truncated bool, readErr error) ([]byte, dumpKind, string, string) {
	if truncated {
		return nil, dumpOther, "", "the reader printed more than the output limit"
	}
	if readErr != nil {
		if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
			return nil, dumpOther, "", "the reader did not finish within " + warmReadTimeout.String()
		}
		if len(resp) == 0 {
			return nil, dumpOther, "", "reading the reader's answer failed: " + readErr.Error()
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
	if !quit && adb != "" {
		// Not told to quit: never forwarded, or the forward did not answer. Kill the
		// process by its own name, never a broader pattern.
		if out, err := a.Run(ctx, adb, readerCloseBound, 1<<10, "-s", c.serial, "shell", "pidof", c.name); err == nil && out.ExitCode == 0 {
			for _, pid := range strings.Fields(string(out.Stdout)) {
				a.Run(ctx, adb, readerCloseBound, 1<<10, "-s", c.serial, "shell", "kill", pid)
			}
		}
	}
	if port == "" || adb == "" {
		return
	}
	a.Run(ctx, adb, readerCloseBound, 1<<10, "-s", c.serial, "forward", "--remove", "tcp:"+port)
}
