package device

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/domain/device/reader"
)

// The persistent UI reader (ADR-0030), run by the daemon on the device and reached over
// `adb forward`. A readerClient is the daemon's handle on one device's reader: the
// forward, whether it has been started, and the per-device lock every read shares
// (plan m1f, R2). The one-shot reader of ADR-0029 remains the fallback.

// Plan m1f step 1 fixes the working numbers: the idle exit is 30 minutes, and a warm
// read is tens of milliseconds (median 14.4 ms, p95 460 ms), so 4 s is the warm read's
// margin. It is above the reader's own device-side bound of 2.5 s (ADR-0034 §1; plan m1n
// step 3), so a `busy` answer at that bound reaches the client with margin through the
// forward instead of being cut off as a timeout. The first read pushes, starts, forwards
// and pings inside 8 s. Variables so a test is quick.
var (
	// readerStartBound bounds the start command and the pings after it.
	readerStartBound = 8 * time.Second
	// readerActBoundBase is the device-side bound of a `tap` or a `settext`, and the base of
	// a `keys` bound (ADR-0035 §1; plan m1n step 4). It is DroidyardReader's
	// ACT_BOUND_BASE_MS: the two must agree, and readerActDeviceBound computes the same
	// formula on the host.
	readerActBoundBase = 2500 * time.Millisecond
	// readerActBoundPerChar is the extra device-side bound a `keys` gets for each character
	// of its text: keys injected one by one, synchronously, cost about 65–75 ms each at level 5 (plan m1n
	// step 4, fix round), with margin. It is DroidyardReader's ACT_BOUND_PER_KEY_MS.
	readerActBoundPerChar = 100 * time.Millisecond
	// readerActBoundCap caps the device-side bound of one acting request, so a huge `keys`
	// does not hold the acting thread indefinitely. It is DroidyardReader's
	// ACT_BOUND_CAP_MS.
	readerActBoundCap = 60 * time.Second
	// readerActMargin is how far the host's bound sits above the device's (ADR-0035 §1;
	// plan m1n step 4), so the reader's own `timeout` answer reaches the client instead of
	// the client giving up first and reporting an unanswered request — one that may have
	// applied — for a request the device had already bounded.
	readerActMargin = 1500 * time.Millisecond
	// warmReadTimeout bounds one dump over the forward.
	warmReadTimeout = 4 * time.Second
	// readerForwardTimeout bounds `adb forward` and the other short adb commands of a forward.
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
	// slowWarmRead is the duration above which a warm read is worth a daemon-log line
	// (plan m1n step 2): a warm read is tens of milliseconds (plan m1f step 1), so a
	// second means the reader or the device was slow.
	slowWarmRead = time.Second
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
	// actOutputCap bounds one acting request's answer. It is far above what a `done` line
	// carries (a field's text base64), so a truncated answer means the reader sent
	// something that was not an answer.
	actOutputCap = 1 << 16
	// oneShotWarning starts the warnings[] entry of a read that fell back to the
	// one-shot reader because the persistent reader could not run (plan m1f, D3). The
	// persistent reader's reason follows it.
	oneShotWarning = "The persistent UI reader could not run on this device, so the screen was read with the one-shot reader Droidyard runs for a single read. Persistent reader: "
	// readerPingReason is why a start or restart gave up: the reader it started did not
	// answer `ping` within readerPingCheck. It is the returned reason and, in the daemon
	// log, the reason `ping failed` (plan m1n step 2).
	readerPingReason = "the reader did not answer ping"
)

// readerFailure is why a warm read of the persistent reader failed, at the granularity
// the daemon log records (plan m1n step 2): the three failures that restart a reader,
// plus rfNone for a failure that is not a restart. It is stated by the read itself, not
// parsed back out of its sentence.
type readerFailure int

const (
	rfNone    readerFailure = iota
	rfTimeout               // the warm read did not finish within warmReadTimeout
	rfEOF                   // the connection ended before the reader wrote a document
	rfRefused               // the forward did not answer the dial
)

// readerRestartReason is the short reason a restart or fallback is logged with (plan m1n
// step 2): "timeout <ms>", "eof", "refused" or "ping failed". A failure that is none of
// these keeps its own reason sentence.
func readerRestartReason(rf readerFailure, reason string) string {
	switch rf {
	case rfTimeout:
		return fmt.Sprintf("timeout %d", warmReadTimeout.Milliseconds())
	case rfEOF:
		return "eof"
	case rfRefused:
		return "refused"
	}
	if reason == readerPingReason {
		return "ping failed"
	}
	return reason
}

// logReaderRestart records a reader restart and why (plan m1n step 2): the failure that
// drove it, or a restart whose fresh reader did not answer ping.
func (a *ADB) logReaderRestart(serial string, rf readerFailure, reason string) {
	a.logf("%s: UI reader restart: %s", serial, oneLine(readerRestartReason(rf, reason)))
}

// logReaderFallback records a fallback to another reader with its reason (plan m1n step
// 2), so a slow read that cost a start or a dump is not invisible.
func (a *ADB) logReaderFallback(serial, stage, reason string) {
	a.logf("%s: UI reader fallback to %s: %s", serial, stage, oneLine(reason))
}

// logWarmRead records a warm read that took at least slowWarmRead, with its duration
// (plan m1n step 2): a warm read should be tens of milliseconds, so a slow one is the
// signal the speed work starts from.
func (a *ADB) logWarmRead(serial string, d time.Duration) {
	if d >= slowWarmRead {
		a.logf("%s: warm UI read took %s", serial, d.Round(time.Millisecond))
	}
}

// oneLine folds a reason onto one line for the daemon log, so a reason that carries a
// newline cannot make a log line look like two.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

// readerClient is one device's persistent reader, owned by the ADB that created it.
type readerClient struct {
	serial string
	// name is `droidyard-reader-<d12>`: the abstract socket's name and the process's
	// --nice-name (plan m1f, D2).
	name string
	// remote is the dex's path on the device; it is left there (ADR-0030: the dex file
	// may stay in /data/local/tmp) and pushed again when a start finds it changed (dexNeedsPush).
	remote string

	// lock serialises this device's reads (plan m1f, R2). It is context-aware, not a
	// sync.Mutex, so a waiter inside its read's deadline gives up when the deadline
	// passes rather than hanging.
	lock chan struct{}

	mu       sync.Mutex
	sock     string // the forward's socket file, "" until forwarded
	launched bool   // the serve command ran at least once (or a reader was adopted)
	pushed   bool   // the dex was pushed by this daemon; not proof it is still there (dexNeedsPush)
	// serves counts the serve commands that ran, so a call can tell that it started the reader
	// (device.uiEvents reports that as a gap) and not another call's start.
	serves int
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

func (c *readerClient) sockValue() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sock
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

func (c *readerClient) haveSock() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sock != ""
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

// clearSock forgets a forward that is gone, so the caller makes a new one.
func (c *readerClient) clearSock() {
	c.mu.Lock()
	c.sock = ""
	c.mu.Unlock()
}

// setSock remembers a forward this daemon did not make under it (an earlier daemon's,
// still listed): the reader at the other end is the same one.
func (c *readerClient) setSock(sock string) {
	c.mu.Lock()
	if sock != "" {
		c.sock = sock
	}
	c.mu.Unlock()
}

// adopt takes over an earlier daemon's forward: the reader is this daemon's to use, so
// it counts as started and Close quits it and removes its forward (plan m1f step 7,
// ADR-0030).
func (c *readerClient) adopt(sock string) {
	c.setStarted(sock)
}

// serveCount is how many serve commands this client has run.
func (c *readerClient) serveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serves
}

func (c *readerClient) setStarted(sock string) {
	c.mu.Lock()
	c.launched = true
	if sock != "" {
		c.sock = sock
	}
	c.mu.Unlock()
}

// readerIdle is the reader's own idle bound for one read (D2): how long the reader waits
// for the screen to go quiet (quiet, the reader's idle quiet time in ms) before giving up
// and returning what it has (max, the reader's idle max in ms). It is the `dump idle
// <quietMs> <maxMs>` option; a nil idle is a plain `dump`, the reader's defaults.
type readerIdle struct {
	quiet int
	max   int
}

// readerDumpRequest is the request line of a read over the forward: a plain `dump` is the
// reader's default idle wait (100/500); the polls after an action's first read ask for the
// shorter pollIdle (D2), `dump idle <quietMs> <maxMs>`. A full dump (ADR-0110) is `dump
// full`, with `invisible` after it when asked; it takes the default idle wait.
func readerDumpRequest(idle *readerIdle, full *fullOpts) string {
	switch {
	case full != nil:
		return "dump " + full.words() + "\n"
	case idle != nil:
		return fmt.Sprintf("dump idle %d %d\n", idle.quiet, idle.max)
	}
	return "dump\n"
}

// persistentRead is the persistent reader's turn in read: ensure the reader is
// reachable, then take one dump over its forward, request being the line
// readerDumpRequest made. It also reports why a warm read failed (rf), so the chain can
// log a restart with its reason without parsing the sentence back out (plan m1n step 2),
// and the app's package (busyPkg) of a `busy` answer (ADR-0034 §1).
func (a *ADB) persistentRead(parent context.Context, t *target, c *readerClient, start, end time.Time, request string) (doc []byte, kind dumpKind, line, reason, busyPkg string, rf readerFailure) {
	if ok, why := a.ensureReader(parent, t, c, end); !ok {
		return nil, dumpOther, "", why, "", rfNone
	}
	if cerr := parent.Err(); cerr != nil {
		return nil, dumpOther, "", cerr.Error(), "", rfNone
	}
	sock := c.sockValue()
	if sock == "" {
		return nil, dumpOther, "", "the reader's forward is gone", "", rfNone
	}
	deadline := time.Now().Add(warmReadTimeout)
	if deadline.After(end) {
		deadline = end
	}
	warmStart := time.Now()
	conn, err := net.DialTimeout("unix", sock, warmReadTimeout)
	if err != nil {
		// The forward no longer answers: it may have been removed (a lost forward) or
		// the reader may be gone. The chain restarts the reader once, which reconciles
		// the forward (plan m1f step 7).
		return nil, dumpUnreachable, "", "the reader's forward did not answer: " + err.Error(), "", rfRefused
	}
	defer conn.Close()
	// A read that is blocked on its connection deadline must still stop when the
	// caller's context ends (plan m1f step 5): close the connection, which unblocks
	// readLimited.
	stop := context.AfterFunc(parent, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(deadline)
	if _, err := io.WriteString(conn, request); err != nil {
		return nil, dumpOther, "", "the reader's forward did not take the request", "", rfEOF
	}
	resp, truncated, readErr := readLimited(conn, dumpOutputCap)
	a.logWarmRead(t.serial, time.Since(warmStart))
	return classifyReader(resp, truncated, readErr, strings.HasPrefix(request, "dump full"))
}

// readerActKind is what an acting request (ADR-0035 §1) did. The distinction is the
// contract's: whether anything was executed decides whether the caller may act again
// (ADR-0035 §3).
type readerActKind int

const (
	// readerActDone: the reader executed the request. readBack carries the field's text
	// when the request read one back (`done <base64>`), and is empty otherwise.
	readerActDone readerActKind = iota
	// readerActRefused: the reader refused the request before executing anything, and named
	// the reason in refusal (ADR-0035 §1; plan m1n step 4, fix round N3). Whether another
	// delivery method is allowed depends on that reason: only refusalSetText — a set-text
	// the node would not take — is one the caller may answer with `keys` (ADR-0035 §3).
	readerActRefused
	// readerActBusy: the device's single acting thread was out, and the busy answer came
	// before anything was executed (ADR-0035 §1). The call fails DEVICE_NOT_READY.
	readerActBusy
	// readerActUnanswered: the request was taken and no definitive answer came (a timeout,
	// an EOF, the reader's own bound passing after execution began, or an answer this
	// client cannot trust). It may have applied: the call completes with a warning and
	// nothing else is tried (ADR-0035 §3).
	readerActUnanswered
)

// readerRefusal is why the reader refused an acting request before executing anything
// (ADR-0035 §1, §3; plan m1n step 4, fix round N3). Only refusalSetText is a refusal the
// caller may answer with another delivery method (`keys`); the rest — a malformed or
// unterminated request, a line over the cap, no focused field, unmappable keys — are not,
// and a refusal the reader did not name a reason for is refusalOther, treated the same way.
type readerRefusal int

const (
	refusalOther readerRefusal = iota
	refusalSetText
	refusalMalformed
	refusalUnterminated
	refusalTooLong
	refusalNoFocus
	refusalUnmappable
	// refusalNoSize: a gesture's pixels need the screen's size and the reader could not read
	// it, so nothing was injected (plan m3g step 2). A retry may succeed.
	refusalNoSize
)

// readerRefusalToken maps the token the reader writes after `refused` to its kind
// (DroidyardReader's refusal lines: `refused settext`, `refused malformed`,
// `refused unterminated`, `refused too-long`, `refused no-focus`, `refused unmappable`,
// `refused nosize`; plan
// m1n step 4, fix round N3). An unrecognised token is refusalOther, which never enables
// another method.
func readerRefusalToken(token string) readerRefusal {
	switch token {
	case "settext":
		return refusalSetText
	case "malformed":
		return refusalMalformed
	case "unterminated":
		return refusalUnterminated
	case "too-long":
		return refusalTooLong
	case "no-focus":
		return refusalNoFocus
	case "unmappable":
		return refusalUnmappable
	case "nosize":
		return refusalNoSize
	}
	return refusalOther
}

// refusalPhrase names a refusal in a caller's error, so a refusal the reader reported is
// not flattened into "the reader refused" (ADR-0035 §3; plan m1n step 5). The last phrase is
// deliberately cautious: the reader did not say which, so the caller must not assume the
// refusal was one another method answers.
func refusalPhrase(r readerRefusal) string {
	switch r {
	case refusalSetText:
		return "the focused field refused a text set"
	case refusalMalformed:
		return "the request was malformed"
	case refusalUnterminated:
		return "the request was not properly terminated"
	case refusalTooLong:
		return "the request was over the length cap"
	case refusalNoFocus:
		return "no field had focus"
	case refusalUnmappable:
		return "a character has no key event on the virtual keyboard"
	case refusalNoSize:
		return "the reader could not read the screen's size"
	}
	return "it did not say which refusal this was"
}

// readerAct is one acting request's outcome. readBack is the focused field's text as the
// reader read it back, for the kinds that carry one. refusal says why a refusal happened
// (readerActRefused). reason explains an unanswered answer this client understood but did
// not trust (plan m1n step 4, fix round N8). line is the reader's own answer line, which is
// device output: a caller that quotes it marks it untrusted.
type readerAct struct {
	kind     readerActKind
	readBack string
	refusal  readerRefusal
	reason   string
	line     string
}

// readerActDeviceBound is the device-side bound the reader applies to an acting request
// (DroidyardReader.actBoundMs, the one shared formula; ADR-0035 §1; plan m1n step 4): the
// base for a `tap` or a `settext`, the base plus a per-character allowance for `keys`,
// capped. chars is the request's text length in characters.
func readerActDeviceBound(chars int) time.Duration {
	return readerActBound(chars, 0)
}

// readerActBound is the one formula behind every acting bound: the base, a per-character
// allowance for `keys`, and a span for a request that holds the acting thread for a time (a
// `gesture` for its last t; plan m3g step 2), capped. It is DroidyardReader.actBoundMs.
func readerActBound(chars int, span time.Duration) time.Duration {
	b := readerActBoundBase + time.Duration(chars)*readerActBoundPerChar + span
	if b > readerActBoundCap {
		b = readerActBoundCap
	}
	return b
}

// readerActHostBound bounds one acting request over the forward: the device bound plus
// readerActMargin, so a `timeout` the reader answers at its own bound reaches the client
// with margin instead of being cut off as an unanswered request (ADR-0035 §1; plan m1n step
// 4). chars is the request's text length in characters (0 for a `tap` or a `settext`).
func readerActHostBound(chars int) time.Duration {
	return readerActDeviceBound(chars) + readerActMargin
}

// readerGestureHostBound is the host's bound on a `gesture` whose path lasts span: the
// device's bound for that span plus the margin, so a path longer than the base is not cut
// off by the client while the reader is still injecting it (plan m3g step 2).
func readerGestureHostBound(span time.Duration) time.Duration {
	return readerActBound(0, span) + readerActMargin
}

// readerFenceError is the error an acting request answered `denied` returns (ADR-0035 §2):
// the peer's uid is not adbd's (0 or 2000), so the reader's fence refused the request
// before it executed anything. Nothing ran, so another delivery method is allowed
// (ADR-0035 §3).
func readerFenceError(uid string) error {
	if uid == "" {
		return errors.New("the reader denied the acting request: the connecting peer's uid is not adbd's (0 or 2000), so its uid fence refused it and nothing ran")
	}
	return fmt.Errorf("the reader denied the acting request: the connecting peer's uid is %s, not adbd's (0 or 2000), so its uid fence refused it and nothing ran", uid)
}

// readerReady reports whether a device's persistent reader can take an acting request right
// now (ADR-0035 §3; plan m1n step 5): it takes the device's read lock, makes the reader
// reachable (ensureReader pushes, starts and forwards it when it is not up), and pings the
// forward. The explicit ping is the point (plan m1n step 4, fix round N2): a cached forward
// can be stale — the reader may have died — and ensureReader returns true for a cached socket
// without pinging it, so a caller that acted on that alone could send an acting request to
// a reader that is not there and report the failure as may-have-applied. A false answer
// means nothing was sent to the reader, so a caller may use the non-reader path.
func (a *ADB) readerReady(parent context.Context, t *target, c *readerClient, end time.Time) bool {
	if err := c.acquire(parent); err != nil {
		return false
	}
	defer c.release()
	if ok, _ := a.ensureReader(parent, t, c, end); !ok {
		return false
	}
	sock := c.sockValue()
	if sock == "" {
		return false
	}
	return a.pingSock(parent, sock, boundBy(time.Now().Add(readerPingCheck), end))
}

// actOnReader sends one acting request (ADR-0035 §1) to a device's persistent reader over
// its forward and classifies the answer. The caller holds the device's read lock (c), as
// it does for a read: the lock and the reader's own single acting thread are the two
// layers that keep one device's acts from overlapping. bound is the host's bound on the
// request (readerActHostBound), which sits above the reader's own device-side bound.
//
// A non-nil error means nothing was executed, so the caller may use another delivery
// method (ADR-0035 §3): the reader could not be reached, or the fence answered `denied`.
// With a nil error the kind says what happened: readerActDone executed it, readerActRefused
// executed nothing (refusal says whether another method answers it), readerActBusy means
// another act was out (fail DEVICE_NOT_READY without acting), and readerActUnanswered means
// it may have applied (complete the call with a warning and try nothing else).
func (a *ADB) actOnReader(ctx context.Context, t *target, c *readerClient, request string, bound time.Duration) (readerAct, error) {
	end := time.Now().Add(dumpDeadline)
	if d, ok := ctx.Deadline(); ok && d.Before(end) {
		end = d
	}
	if ok, why := a.ensureReader(ctx, t, c, end); !ok {
		return readerAct{}, fmt.Errorf("the persistent UI reader could not run: %s", why)
	}
	if cerr := ctx.Err(); cerr != nil {
		return readerAct{}, cerr
	}
	sock := c.sockValue()
	if sock == "" {
		return readerAct{}, errors.New("the persistent UI reader's forward is gone")
	}
	deadline := time.Now().Add(bound)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn, err := net.DialTimeout("unix", sock, bound)
	if err != nil {
		// Nothing was sent, so this is not the may-have-applied case: the caller may use
		// another delivery method. The reader is not restarted here — an acting request
		// sits inside an action's phase, and the fallback needs no reader; the next read
		// reconciles the forward (plan m1f step 7).
		return readerAct{}, fmt.Errorf("the reader's forward did not answer: %w", err)
	}
	defer conn.Close()
	// As for a read: a call blocked on its connection deadline must still stop when the
	// caller's context ends.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(deadline)
	if _, err := io.WriteString(conn, request+"\n"); err != nil {
		// The request may have reached the reader in part, so whether it ran is unknown:
		// the honest answer is that it may have applied, never that nothing ran.
		return readerAct{kind: readerActUnanswered}, nil
	}
	resp, _, _ := readLimited(conn, actOutputCap)
	return classifyAct(resp)
}

// classifyAct reads the reader's answer to an acting request (ADR-0035 §1): `denied
// <uid>`, `refused <reason>`, `busy`, `done [<base64 text>]` or `timeout <ms>
// [<exception>]`. Anything else — a reader line it does not know, a partial line, or no
// line at all, because the connection timed out or ended — is unanswered: the request was
// taken, so it may have applied. A `done` whose read-back is not base64 is unanswered too:
// the request ran, so it may have applied, and the reason says why the answer was not
// trusted (plan m1n step 4, fix round N8). Nothing but a `denied`, which returns an error,
// ever reports that nothing ran.
func classifyAct(resp []byte) (readerAct, error) {
	line := strings.TrimSpace(string(resp))
	rest, found := strings.CutPrefix(line, readerPrefix)
	if found {
		fields := strings.Fields(rest)
		if len(fields) > 0 {
			switch fields[0] {
			case "denied":
				uid := ""
				if len(fields) > 1 {
					uid = fields[1]
				}
				return readerAct{}, readerFenceError(uid)
			case "refused":
				refusal := refusalOther
				if len(fields) > 1 {
					refusal = readerRefusalToken(fields[1])
				}
				return readerAct{kind: readerActRefused, refusal: refusal, line: line}, nil
			case "busy":
				return readerAct{kind: readerActBusy, line: line}, nil
			case "done":
				if len(fields) == 1 {
					// `done` with no read-back: the request ran and the field read empty.
					return readerAct{kind: readerActDone, line: line}, nil
				}
				text, err := base64.StdEncoding.DecodeString(fields[1])
				if err != nil {
					return readerAct{kind: readerActUnanswered, line: line,
						reason: "the reader's done line carried a read-back that is not base64"}, nil
				}
				return readerAct{kind: readerActDone, readBack: string(text), line: line}, nil
			case "timeout":
				// `timeout <ms>` or `timeout <ms> <exception>`: the request was taken and the
				// bound passed, or a pre-execution error ended it; either way it may have
				// applied, so it is unanswered, never a refusal (ADR-0035 §1, §3; plan m1n
				// step 4, fix round N6).
				return readerAct{kind: readerActUnanswered, line: line}, nil
			case "unknown":
				// A reader line this client does not know: the request was taken, so it may
				// have applied (plan m1n step 4, fix round B2).
				return readerAct{kind: readerActUnanswered, line: line}, nil
			}
		}
	}
	return readerAct{kind: readerActUnanswered}, nil
}

// actTap taps at a display pixel through the reader (ADR-0035 §1): `tap <x> <y>`. Its host
// bound is the base: a tap carries no text.
func (a *ADB) actTap(ctx context.Context, t *target, c *readerClient, x, y int) (readerAct, error) {
	return a.actOnReader(ctx, t, c, fmt.Sprintf("tap %d %d", x, y), readerActHostBound(0))
}

// The wire of a touch path and a key press (plan m3g step 2; ADR-0133). A coordinate travels
// as 0..gestureCoordMax, the fraction of the screen's size the view wire uses, so the daemon
// needs no size: the reader reads the screen once when the path starts.
const (
	gestureCoordMax = 65535
	// gestureMaxPoints and gestureMaxSpan are the reader's limits on one path
	// (GESTURE_MAX_POINTS and GESTURE_MAX_MS); a request past them is refused as malformed.
	gestureMaxPoints = 64
	gestureMaxSpan   = 5 * time.Second
)

// pathPoint is one point of a path sent to the reader: X and Y are fractions of the screen, 0 at the left and
// top edge and 1 at the right and bottom, and At is the time after the touch goes down.
type pathPoint struct {
	X, Y float64
	At   time.Duration
}

// gestureCoord is a fraction as the wire's integer: clamped to 0..1 (a NaN is 0), then scaled
// and rounded, so 0 is 0, 1 is 65535 and 0.5 is 32768.
func gestureCoord(f float64) int {
	if !(f > 0) {
		return 0
	}
	if f > 1 {
		f = 1
	}
	return int(math.Round(f * gestureCoordMax))
}

// gestureRequest is `gesture <n> <x1> <y1> <t1> ... <xn> <yn> <tn>`: n points, the
// coordinates scaled by gestureCoord and the times in whole milliseconds. It validates
// nothing: the reader refuses a path that is not 2 to gestureMaxPoints points, with the
// first t 0 and the rest strictly increasing within gestureMaxSpan, before it injects
// anything.
func gestureRequest(points []pathPoint) string {
	var b strings.Builder
	b.WriteString("gesture ")
	b.WriteString(strconv.Itoa(len(points)))
	for _, p := range points {
		fmt.Fprintf(&b, " %d %d %d", gestureCoord(p.X), gestureCoord(p.Y), p.At.Milliseconds())
	}
	return b.String()
}

// gestureSpan is a path's last time: how long the reader holds its acting thread, and what
// the bounds on the request grow by.
func gestureSpan(points []pathPoint) time.Duration {
	if len(points) == 0 {
		return 0
	}
	return points[len(points)-1].At
}

// pressRequest is `press <keycode>`: the reader injects the key down and up.
func pressRequest(code int) string {
	return "press " + strconv.Itoa(code)
}

// actGesture injects a touch path through the reader (plan m3g step 2): `gesture ...`. Its
// bounds grow with the path, as the reader's do.
func (a *ADB) actGesture(ctx context.Context, t *target, c *readerClient, points []pathPoint) (readerAct, error) {
	return a.actOnReader(ctx, t, c, gestureRequest(points), readerGestureHostBound(gestureSpan(points)))
}

// actPress injects a key press through the reader (plan m3g step 2): `press <keycode>`. Its
// host bound is the base: a press is two injections, not a path.
func (a *ADB) actPress(ctx context.Context, t *target, c *readerClient, code int) (readerAct, error) {
	return a.actOnReader(ctx, t, c, pressRequest(code), readerActHostBound(0))
}

// actKeys types text as key events through the reader (ADR-0035 §1): `keys <base64>`. The
// text is base64 so a space or a newline in it cannot split the request line. Its host bound
// grows with the text's length, as the reader's device-side bound does (plan m1n step 4).
func (a *ADB) actKeys(ctx context.Context, t *target, c *readerClient, text string) (readerAct, error) {
	return a.actOnReader(ctx, t, c, "keys "+base64Text(text), readerActHostBound(utf8.RuneCountInString(text)))
}

// actSetText sets the focused field's text through the reader (ADR-0035 §1): `settext
// <replace|insert> <base64>`. replace is `clear` or an explicit set; insert is the default
// (the text goes in at the selection). Its host bound is the base: a set-text is one
// ACTION_SET_TEXT, not a key per character.
func (a *ADB) actSetText(ctx context.Context, t *target, c *readerClient, replace bool, text string) (readerAct, error) {
	mode := "insert"
	if replace {
		mode = "replace"
	}
	return a.actOnReader(ctx, t, c, "settext "+mode+" "+base64Text(text), readerActHostBound(0))
}

// actSubmit presses enter through the reader (ADR-0035 §1; plan m1n step 4): `keys` of a
// newline, which the virtual keyboard maps to KEYCODE_ENTER.
func (a *ADB) actSubmit(ctx context.Context, t *target, c *readerClient) (readerAct, error) {
	return a.actKeys(ctx, t, c, "\n")
}

// base64Text encodes a request's text argument (ADR-0035 §1): the reader's line is split
// on spaces, so the text travels base64 and a space, a newline or a non-ASCII character in
// it cannot break the request into the wrong number of arguments.
func base64Text(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
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
		// A listing failure with a cached socket is ignored and the dump is tried (plan
		// m1f step 7); on the first read there is nothing to try.
		if why != "" && !c.haveSock() {
			return false, why
		}
		c.markReconciled()
		// The first read is a slow path: stop a reader of another digest found by
		// process name, before this daemon uses or starts its own (plan m1f step 7).
		a.clearForeignReaders(ctx, t, c)
		if !c.haveSock() && stale {
			// A forward to our socket that did not answer: kill its process before the
			// new start (plan m1f step 7).
			a.killOwnReader(ctx, t, c)
		}
	}
	if c.haveSock() {
		return true, ""
	}

	// No forward but this daemon had started or adopted the reader: the forward was
	// lost (an adb server restart). Recreate it and see whether the reader behind it
	// still answers, rather than start a second one (ADR-0030; plan m1f step 7).
	if c.wasStarted() {
		if ok, why := a.forwardReader(ctx, t, c); !ok {
			return false, why
		}
		if a.pingSock(ctx, c.sockValue(), boundBy(time.Now().Add(readerPingCheck), end)) {
			return true, ""
		}
		a.killOwnReader(ctx, t, c)
	}

	// The slow path: a reader of another digest, found by process name, holds the
	// device's one UiAutomation and must be stopped before this daemon starts (plan m1f
	// step 7).
	a.clearForeignReaders(ctx, t, c)
	if a.dexNeedsPush(ctx, t, c) {
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
	if !c.haveSock() {
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
	// been removed). A listing failure with a cached socket is ignored: the dump is tried.
	if _, why := a.reconcileForward(ctx, t, c); why != "" && !c.haveSock() {
		return false, why
	}
	if !c.haveSock() {
		if !c.wasStarted() {
			return false, "the reader's forward is gone"
		}
		if ok, why := a.forwardReader(ctx, t, c); !ok {
			return false, why
		}
	}
	// A reader of our name may be alive and merely unreachable. If it answers ping the
	// same forward is usable and no second reader is started (plan m1f step 7).
	if a.pingSock(ctx, c.sockValue(), time.Now().Add(readerPingCheck)) {
		return true, ""
	}
	a.killOwnReader(ctx, t, c)
	// Before any start: a foreign reader holds the device's one UiAutomation, and an
	// adopted reader's dex was not pushed by this daemon, so it is put back before its
	// replacement starts (plan m1f step 7).
	a.clearForeignReaders(ctx, t, c)
	if a.dexNeedsPush(ctx, t, c) {
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
	sock   string
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
		// Gone, as after an adb server restart: forget the socket so it is recreated, and
		// unlink the file the server left.
		a.unlinkForward(c.sockValue())
		c.clearSock()
		return false, ""
	case c.haveSock() && c.sockValue() == ours.sock:
		return false, ""
	case c.haveSock():
		// Listed under another socket than the cached one: use the listed one. The cached
		// one's forward may still be listed, so its file stays.
		c.setSock(ours.sock)
		return false, ""
	}
	// No cached socket and a forward to our socket exists: an earlier daemon's. Adopt it
	// when it answers; otherwise remove it so the new forward is not a stale one beside
	// the new (plan m1f step 7).
	if a.pingSock(ctx, ours.sock, time.Now().Add(readerPingCheck)) {
		c.adopt(ours.sock)
		return false, ""
	}
	a.removeForward(ctx, t, ours.sock)
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
	// The listing is every client's: only a file of the shape this daemon makes in its own
	// forward directory is ever dialled or unlinked.
	var own []forwardEntry
	for _, e := range parseForwardList(out.Stdout) {
		if a.ownForward(e.sock) {
			own = append(own, e)
		}
	}
	return own, ""
}

// parseForwardList reads `adb forward --list`'s lines: `<serial> localfilesystem:<path>
// <remote>` (`emulator-5554 localfilesystem:/state/run/fwd/reader-0123456789ab
// localabstract:droidyard-reader-31edb33baa07`, as the spike of plan m2t step 8 recorded;
// the path is printed raw, spaces included).
// Only forwards to a socket file are kept: the loopback ports of ADR-0030's first form
// are not this daemon's any more.
func parseForwardList(out []byte) []forwardEntry {
	var entries []forwardEntry
	for _, line := range strings.Split(string(out), "\n") {
		// The serial is the first field and the remote the last; the local spec is what lies
		// between, as adb prints it: a path with a space in it (macOS's default state root is
		// under "Application Support") is one spec, so the line is not split on every space.
		line = strings.TrimSpace(line)
		i, j := strings.IndexByte(line, ' '), strings.LastIndexByte(line, ' ')
		if i < 0 || j <= i {
			continue
		}
		serial, local, remote := line[:i], strings.TrimSpace(line[i+1:j]), line[j+1:]
		name := strings.TrimPrefix(remote, "localabstract:")
		if !strings.HasPrefix(local, forwardLocalPrefix) || name == remote {
			continue // not a forward to a socket file, or not to a local socket
		}
		entries = append(entries, forwardEntry{serial: serial, sock: strings.TrimPrefix(local, forwardLocalPrefix), remote: name})
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

// removeForward removes one forward this daemon owns, by its socket, and unlinks the file
// adb leaves behind (ADR-0102). It never removes all forwards and never another name's (E6).
func (a *ADB) removeForward(ctx context.Context, t *target, sock string) {
	if sock == "" {
		return
	}
	a.Run(ctx, t.adb, readerForwardTimeout, 1<<10, "-s", t.serial, "forward", "--remove", forwardLocalPrefix+sock)
	a.unlinkForward(sock)
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

// dexNeedsPush decides, before a start, whether the dex must be pushed first. It is on the
// slow path only (the callers are about to start a process), never on a warm read. A dex
// this daemon has not pushed is pushed. One it has pushed is trusted only after one
// bounded `sha256sum` of the fixed remote path: the pushed flag outlives a device restart
// under the same serial, and a restart that kills the guest before its page cache reaches
// the disk leaves the dex 0 bytes long (milestone 2c step 2), or gone after a wipe, so a start
// from it fails and the read falls back for good. The dex is trusted only when the first
// field of the output is the embedded digest; another digest, a missing file or tool
// (observed present on API 31 and 36), a non-zero exit, a truncated or unparseable
// output all mean push: a push is idempotent and its own failure carries the reason. A
// missing tool costs a push per start, never a wrong trust.
func (a *ADB) dexNeedsPush(ctx context.Context, t *target, c *readerClient) bool {
	if !c.isPushed() {
		return true
	}
	// c.remote is readerRemote: a fixed shape built from the pinned digest, no untrusted
	// text reaches the shell.
	out, err := a.Run(ctx, t.adb, readerCloseBound, 1<<10, "-s", t.serial, "shell", "sha256sum", c.remote)
	if err != nil || out.ExitCode != 0 || out.Truncated {
		return true
	}
	fields := strings.Fields(string(out.Stdout))
	return len(fields) == 0 || !strings.EqualFold(fields[0], reader.SHA256)
}

// pushReader puts the embedded dex on the device. It is the same file the one-shot
// reader uses; the persistent reader leaves it (ADR-0030), and it is pushed again when this
// daemon has not pushed it or the device's copy is not the embedded one (dexNeedsPush).
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
	c.mu.Lock()
	c.serves++
	c.mu.Unlock()
	return true, ""
}

// forwardReader creates the host-to-device forward, to a socket file of its own in the
// forward directory (ADR-0102), and remembers the file. A forward that failed may still
// have left the file, so it is unlinked.
func (a *ADB) forwardReader(ctx context.Context, t *target, c *readerClient) (bool, string) {
	sock, err := a.forwardSocket(ctx, t.adb, fwdKindReader)
	if err != nil {
		return false, "no socket for the forward: " + err.Error()
	}
	out, err := a.Run(ctx, t.adb, readerForwardTimeout, 1<<10, "-s", t.serial, "forward", forwardLocalPrefix+sock, "localabstract:"+c.name)
	if err != nil || out.ExitCode != 0 {
		a.unlinkForward(sock)
		return false, "adb forward failed: " + decisiveLine(out)
	}
	c.setStarted(sock)
	return true, ""
}

// pingReader asks the reader to answer until deadline, so a start that is still coming up
// is waited for rather than failed.
func (a *ADB) pingReader(ctx context.Context, c *readerClient, deadline time.Time) (bool, string) {
	sock := c.sockValue()
	if sock == "" {
		return false, "the reader's forward is gone"
	}
	if a.pingSock(ctx, sock, deadline) {
		return true, ""
	}
	return false, readerPingReason
}

// pingSock asks the reader at a forward's socket to answer until deadline. It is the one
// ping used for the start wait and for step 7's 1 s check (readerPingCheck) before a
// reader is killed and replaced.
func (a *ADB) pingSock(ctx context.Context, sock string, deadline time.Time) bool {
	if sock == "" {
		return false
	}
	for {
		if time.Now().After(deadline) {
			return false
		}
		conn, err := net.DialTimeout("unix", sock, readerPingPause*5)
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
// connection ended before a document): both are restarted once (plan m1f step 7). The
// reader's `busy` answer is not a failure of the reader (ADR-0034 §1): its kind never
// restarts and never falls back, and busyPkg is the package the reader named, which is the
// last app it read and so may be stale when the foreground changed since; the daemon takes
// the package from the window manager's focus instead (plan m1n step 3, fix round). A
// reader line is its own failure; a partial or missing document falls to the one-shot
// reader. busyPkg is only meaningful with dumpAppBusy; the readerFailure names the two
// restart cases for the log (plan m1n step 2).
//
// full says the request was a full dump (ADR-0110 Decision 5): a document over the read cap
// is then dumpOverCap, which fails the read naming the cap, and not dumpOther. A plain read
// that passes the cap is a reader at fault, as ever, and falls to the one-shot reader.
func classifyReader(resp []byte, truncated bool, readErr error, full bool) (doc []byte, kind dumpKind, line, reason, busyPkg string, rf readerFailure) {
	if truncated {
		if full {
			return nil, dumpOverCap, "", fullOverCapReason, "", rfNone
		}
		return nil, dumpOther, "", "the reader printed more than the output limit", "", rfNone
	}
	if readErr != nil {
		if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
			return nil, dumpUnreachable, "", "the reader did not finish within " + warmReadTimeout.String(), "", rfTimeout
		}
		if len(resp) == 0 {
			return nil, dumpVanished, "", "reading the reader's answer failed: " + readErr.Error(), "", rfEOF
		}
	}
	if len(resp) == 0 {
		return nil, dumpVanished, "", "the reader's connection ended before it wrote a document", "", rfEOF
	}
	if bytes.Contains(resp, []byte("<hierarchy")) && bytes.Contains(resp, []byte("</hierarchy>")) {
		return resp, dumpOK, "", "", "", rfNone
	}
	line = strings.TrimSpace(string(resp))
	if strings.HasPrefix(line, readerPrefix) {
		if strings.Contains(strings.ToLower(line), "no active window") {
			// The reader's own line is device text; the cause is the fixed sentence,
			// as on the one-shot path (D3, R5).
			return nil, dumpNoRoot, readerNoRootMessage, readerNoRootMessage, "", rfNone
		}
		if pkg, ok := readerBusy(line); ok {
			// The foreground app did not answer within the reader's device-side bound:
			// the reader is alive and keeps its UiAutomation, so the daemon neither
			// restarts it nor falls back (ADR-0034 §1; plan m1n step 3). pkg is the last
			// app the reader read, passed along but not trusted for Details: the daemon
			// reads the window manager's focus instead (plan m1n step 3, fix round).
			return nil, dumpAppBusy, "", "", pkg, rfNone
		}
		return nil, dumpOther, "", truncate(line, 200), "", rfNone
	}
	if bytes.Contains(resp, []byte("<hierarchy")) {
		return nil, dumpOther, "", fmt.Sprintf("the reader sent an incomplete hierarchy (%d bytes)", len(resp)), "", rfNone
	}
	return nil, dumpOther, "", "the reader answered no hierarchy", "", rfNone
}

// readerBusy parses the reader's busy answer (ADR-0034 §1): the line the reader writes
// when the device-side bound passed with no document, `droidyard-reader: busy <ms>
// <package>`. ok is false for any other reader line; pkg is "" when the reader named no
// package (it had read nothing yet). pkg is the last app the reader read, not necessarily
// the app that failed to answer, so the daemon does not use it for Details (plan m1n step
// 3, fix round).
func readerBusy(line string) (pkg string, ok bool) {
	rest, found := strings.CutPrefix(line, readerPrefix)
	if !found {
		return "", false
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 || fields[0] != "busy" {
		return "", false
	}
	if len(fields) >= 3 {
		pkg = fields[2]
	}
	return pkg, true
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

// Close ends every device.watchLogs stream and every live-view source, waiting for each
// child to be reaped, then stops
// every UI reader this daemon used and removes every forward it made: `quit`
// to each reader and `adb forward --remove localfilesystem:<sock>` for each forward, per device in
// parallel, all within readerCloseBound (plan m1f step 5). It is called after the
// daemon's operations have drained. It never issues `forward --remove-all`.
func (a *ADB) Close(ctx context.Context) {
	// The UI recordings first (ADR-0113 §3): each reads its last events and writes its end
	// while the readers still run, within recordStopAllWait.
	a.StopRecordings(ctx)
	// The Logcat streams: each child is killed and reaped, and its pid record
	// removed, before the daemon's supervisor stops (ADR-0041 §6).
	a.streams.StopAll(ctx)
	// The UI events streams' pollers return before the readers they ask are stopped below.
	a.uiStreams.StopAll(ctx)
	// The live view's streams drop their references, then the servers likewise (ADR-0078
	// §6): each child is killed and reaped, its record removed and its forward removed
	// before the supervisor stops.
	a.screenViews.StopAll(ctx)
	a.relayedScreens.StopAll(ctx)
	a.screens.StopAll(ctx)
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
// forwarded (its forward failed) cannot be reached over its socket, so it is killed
// by its process name, pidof then kill, and only that name (E6). All of it is best
// effort inside ctx: a device that never answers must not hold the daemon's shutdown
// past its bound.
func (c *readerClient) close(ctx context.Context, a *ADB, adb string) {
	c.mu.Lock()
	sock, launched := c.sock, c.launched
	c.mu.Unlock()
	if !launched {
		return
	}
	quit := false
	if sock != "" {
		if conn, err := net.DialTimeout("unix", sock, readerPingPause*5); err == nil {
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
	if sock == "" || adb == "" {
		return
	}
	a.Run(ctx, adb, readerCloseBound, 1<<10, "-s", c.serial, "forward", "--remove", forwardLocalPrefix+sock)
	a.unlinkForward(sock)
}
