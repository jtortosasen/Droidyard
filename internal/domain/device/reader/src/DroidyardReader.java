import android.app.UiAutomation;
import android.graphics.Point;
import android.graphics.Rect;
import android.net.Credentials;
import android.net.LocalServerSocket;
import android.net.LocalSocket;
import android.net.LocalSocketAddress;
import android.os.Build;
import android.os.Bundle;
import android.os.HandlerThread;
import android.os.Looper;
import android.os.SystemClock;
import android.text.Spanned;
import android.text.style.URLSpan;
import android.util.Base64;
import android.view.InputDevice;
import android.view.KeyCharacterMap;
import android.view.KeyEvent;
import android.view.MotionEvent;
import android.view.accessibility.AccessibilityEvent;
import android.view.accessibility.AccessibilityNodeInfo;
import java.io.ByteArrayOutputStream;
import java.io.Closeable;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.io.PrintStream;
import java.lang.reflect.Constructor;
import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.lang.reflect.Modifier;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ThreadFactory;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;

/**
 * Droidyard's UI reader (ADR-0029, ADR-0030). Run with app_process as the shell user.
 *
 * <p>With no arguments it is the one-shot reader of ADR-0029: it connects UiAutomation
 * with FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES, so the device's other accessibility
 * services keep running, writes the active window's tree to stdout in the XML shape
 * `uiautomator dump` produces, disconnects and exits. Failures go to stderr with a
 * non-zero status.
 *
 * <p>{@code serve <abstract-socket-name> <idleSeconds>} is the persistent reader of
 * ADR-0030: it connects UiAutomation once and answers one request per connection over a
 * LocalServerSocket in the abstract namespace ({@code ping} -> {@code ok <pid>};
 * {@code dump} -> the same XML document the one-shot writes; {@code quit} -> it ends).
 * Only a {@code dump} or an {@code events} request resets the idle timer (ADR-0111); after
 * idleSeconds without one it disconnects UiAutomation and exits, so the device is left as it was found. A response is read to
 * EOF, not to a newline: a successful one is the XML document (or {@code ok <pid>}), and
 * one that cannot be answered starts with {@code droidyard-reader:}. Source of
 * reader.dex: see build.sh.
 *
 * <p>Each {@code dump} runs on one of at most {@link #DUMP_WORKERS} worker threads and is
 * bounded on the device at {@link #DUMP_BOUND_MS} (ADR-0034 §1; plan m1n Q2 ¶1): when the
 * bound passes before the document is complete the client is answered
 * {@code droidyard-reader: busy <ms> <package>} and the worker is left to finish in the
 * background, so an app whose main thread does not answer accessibility queries costs the
 * client the bound and never the reader. A dump arriving while one worker is out starts a
 * second; with two out it is answered {@code busy} at once. {@code ping} and {@code quit}
 * are answered on the accept thread while dumps run, so a blocked read never blocks them.
 * {@code <package>} is the package of the last app the reader read, which may have been
 * replaced while the read was blocked: the daemon does not trust it for the failed read's
 * details and reads the window manager's focus instead (empty when the reader has read
 * nothing yet, and then omitted).
 *
 * <p>{@code dump full [invisible]} (ADR-0110) answers the active window's tree as a superset
 * of {@code uiautomator dump}'s document, one element per line: its attributes in its order
 * on every node, then the extras of {@link #fullNode} when they hold a non-default value.
 * {@code invisible} adds the children not visible to the user. The words are each allowed
 * once and nothing else follows (anything else is {@code bad dump arguments}); the request
 * is fenced, bounded and answered as a plain {@code dump} is. The one-shot takes the same
 * words as arguments ({@code DroidyardReader dump full [invisible]}). No getter of the full
 * document queries another node, so the device-side bound and the platform's 5 s wait hold
 * as for a plain dump.
 *
 * <p>{@code events <afterSeq> <max>} (ADR-0111) answers the accessibility events the reader has
 * kept since it started, from a ring of {@link EventRing#CAPACITY} entries filled by one
 * listener on its UiAutomation connection. The listener copies fields and returns: no
 * getSource(), no parcelable data, no node query, so it cannot block the UiAutomation thread.
 * The request is fenced, answered on the accept thread (a blocked dump never delays it) and
 * counts as a read for the idle exit. The answer is a header line {@code events <instance>
 * <oldestSeq> <nextSeq> <nowWallMs> <startedWallMs>} and then one line per entry of the
 * ring after {@code afterSeq}, at most {@code max} (1 to {@link #EVENTS_MAX}), oldest first:
 * nine tab-separated fields {@code seq time type count window changes package class text}, the
 * last three strictly base64 (empty for none). Only a toast carries text; a notification
 * event's text is never read.
 *
 * <p>It also acts (ADR-0035): {@code tap <x> <y>} injects a touch, {@code keys <base64>}
 * types text as the virtual keyboard's key events, and {@code settext <replace|insert>
 * <base64>} sets the focused input node's text with ACTION_SET_TEXT, reading the field back
 * in the same request. {@code dump} takes an optional {@code idle <quietMs> <maxMs>} (D2)
 * for its own idle wait. An acting request runs on one acting thread, never on a dump
 * worker, and one arriving while another is out is answered {@code busy} at once. It is
 * bounded on the device below the host's bound and its answer says whether anything was
 * executed: {@code refused <reason>} before it, {@code done} (with the field's text read
 * back when one was read) after it, or {@code timeout <ms> [<exception>]} when the bound
 * passed with the request taken, the exception's class appended when a pre-execution error
 * ended it. The {@code <reason>} of a refusal is one of {@code settext} (the node would not
 * take the set-text; another method is allowed), {@code malformed}, {@code unterminated},
 * {@code too-long}, {@code no-focus} or {@code unmappable}, so the client can tell a
 * refusal another method answers from one it does not. A {@code tap} and a {@code settext}
 * are bounded at {@link #ACT_BOUND_BASE_MS}; a {@code keys} grows with its text's length so
 * a long one is not cut off before the keys are typed. An acting request is answered
 * {@code denied} unless its peer's uid is 0 or 2000 — adbd, through which the daemon's
 * forward arrives (ADR-0035 §2); so is every other request, {@code dump}, {@code ping} and
 * {@code quit} (ADR-0102), a read or a stop as much as an act. A request line is capped at
 * {@link #REQUEST_CAP} bytes; a longer one is drained a bounded way and answered
 * {@code refused too-long} before it is parsed or executed, never cut and acted on, and a
 * line cut by EOF is answered {@code refused unterminated} rather than parsed.
 */
public class DroidyardReader {
    // Exit status when getRootInActiveWindow stays null; the daemon maps it (dump.go, readerNoRoot).
    private static final int NO_ROOT = 3;
    // Marks the reader's own lines, on stderr in one-shot mode and on the socket in serve mode.
    private static final String PREFIX = "droidyard-reader:";
    // The device-side bound on one dump (ADR-0034 §1; plan m1n Q2 ¶1): a dump with no
    // complete document within it is answered `busy`. Below the platform's 5 s
    // accessibility interaction timeout, so the client hears back before the app does.
    private static final long DUMP_BOUND_MS = 2500L;
    // The most dump workers a reader runs at once. Accessibility queries are per-thread
    // (AccessibilityInteractionClient.getInstanceForThread), so a second read starts a
    // second worker without disturbing the first; a third is answered `busy` at once.
    private static final int DUMP_WORKERS = 2;
    // The idle wait of a dump that asks for none (D2): today's waitForIdle(100, 500). A
    // poll after an action asks for a shorter one through `dump idle <quietMs> <maxMs>`.
    private static final long IDLE_QUIET_MS = 100L;
    private static final long IDLE_MAX_MS = 500L;
    // The most bytes of one request line that are kept (ADR-0035 §1; plan m1n step 4): a
    // 500-character inputText is up to about 2.7 KB of base64. A longer line is drained up
    // to REQUEST_DRAIN_CAP bytes — enough that the client's write completes and it can read
    // the answer — and refused before it is parsed, never cut and acted on. A line longer
    // still is not drained further: it is refused and the connection closed (plan m1n step
    // 4, fix round N4).
    private static final int REQUEST_CAP = 4096;
    private static final int REQUEST_DRAIN_CAP = 1 << 16;
    // The device-side bound on one acting request (ADR-0035 §1): a synchronous injection
    // blocks while the app's main thread does (the spike saw 11.2 s), so past it the client
    // is answered `timeout`, which says the request may have been executed, and the acting
    // thread is left to finish. A `tap` and a `settext` are bounded at ACT_BOUND_BASE_MS; a
    // `keys` is bounded by that base plus ACT_BOUND_PER_KEY_MS for each character of its
    // text (keys injected one by one, synchronously, cost about 65–75 ms each; plan m1n step 4), capped
    // at ACT_BOUND_CAP_MS. Below the host's bound (persistent.go readerActHostBound, the
    // same formula), so the answer reaches the client before it gives up on the connection.
    private static final long ACT_BOUND_BASE_MS = 2500L;
    private static final long ACT_BOUND_PER_KEY_MS = 100L;
    private static final long ACT_BOUND_CAP_MS = 60000L;
    // The uids an acting request's peer may have (ADR-0035 §2): root, and the shell user
    // adbd forwards arrive as. Any other peer is answered `denied`.
    private static final int[] ACTOR_UIDS = { 0, 2000 };
    // How long a settext or a keys request waits for the field to render its new value
    // before answering with the read-back it has (ADR-0035 §1): a Compose field renders a frame
    // later, so an immediate read can be empty (plan m1n step 2 (e)); a Views field
    // answers in tens of milliseconds.
    private static final long READBACK_BOUND_MS = 300L;
    private static final long READBACK_STEP_MS = 20L;
    // The most characters a full document may reach before the traversal stops (ADR-0110
    // Decision 5): the daemon reads 8 MiB at most, so a document past it is sent cut, which
    // the daemon reads as over the cap, and the reader holds no more than that in memory.
    // Characters are at most as many as UTF-8 bytes, so one past the cap is past it in bytes.
    private static final int FULL_DOC_CAP_CHARS = (8 << 20) + 1;
    // The most entries one `events` answer carries (ADR-0111 Decision 4).
    private static final int EVENTS_MAX = 200;
    // The acting verbs of ADR-0035 §1: the requests the uid fence applies to.
    private static final String[] ACTING_VERBS = { "tap", "keys", "settext" };

    public static void main(String[] args) {
        int status = 1;
        try {
            if (Build.VERSION.SDK_INT < 24) {
                throw new IllegalStateException("API " + Build.VERSION.SDK_INT + " lacks FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES (API 24)");
            }
            if (args.length == 0) {
                status = read(DumpArgs.PLAIN);
            } else if (args[0].equals("dump")) {
                // The one-shot's full dump (ADR-0110): the same words as the serve mode's
                // request. Only the full form exists here; a plain read has no arguments.
                DumpArgs d = DumpArgs.parse(joinWords(args));
                if (d == null || !d.full) {
                    System.err.println(PREFIX + " bad dump arguments");
                    status = 2;
                } else {
                    status = read(d);
                }
            } else if (args.length == 3 && args[0].equals("serve")) {
                status = serve(args[1], idleSeconds(args[2]));
            } else {
                System.err.println(PREFIX + " usage: DroidyardReader [dump full [invisible] | serve <abstract-socket-name> <idleSeconds>]");
                status = 2;
            }
        } catch (Throwable t) {
            Throwable c = t instanceof java.lang.reflect.InvocationTargetException && t.getCause() != null ? t.getCause() : t;
            System.err.println(PREFIX + " " + c);
        }
        System.out.flush();
        System.exit(status);
    }

    private static String joinWords(String[] words) {
        StringBuilder sb = new StringBuilder();
        for (int i = 0; i < words.length; i++) {
            if (i > 0) sb.append(' ');
            sb.append(words[i]);
        }
        return sb.toString();
    }

    private static int idleSeconds(String s) {
        int n = Integer.parseInt(s);
        if (n < 1) throw new IllegalArgumentException("idleSeconds must be positive: " + s);
        return n;
    }

    // The one-shot mode of ADR-0029: connect, read once, disconnect, exit. No device-side
    // bound: the one-shot has one job and the daemon bounds it (readerRun).
    private static int read(DumpArgs args) throws Exception {
        Automation a = connect();
        String xml;
        try {
            xml = document(a.ua, Long.MAX_VALUE, null, args).xml;
        } finally {
            a.close();
        }
        if (xml == null) {
            // No window to read (the screen off, or a window that never settles): a status
            // of its own, so the daemon reports it and does not fall back to a dump that
            // would meet the same and suspend the other services.
            System.err.println(PREFIX + " no active window root");
            return NO_ROOT;
        }
        PrintStream out = new PrintStream(System.out, false, "UTF-8");
        out.print(xml);
        out.flush();
        return 0;
    }

    // The persistent mode of ADR-0030. The listener is bound in the abstract namespace, so
    // adb forward can reach it; the read itself is the one-shot's document(). Only a dump or
    // an events request resets the idle timer.
    private static int serve(String socketName, int idleSeconds) throws Exception {
        // Bind before connecting UiAutomation: a second start on the same name fails here,
        // at the name, without touching accessibility; and if the bind fails there is no
        // connection to release.
        final LocalServerSocket server = new LocalServerSocket(socketName);
        // The one accessibility listener of the reader (ADR-0111 Decision 1), set once the
        // connection is up. A reader whose listener could not be set does not start: an
        // empty ring would read as a quiet screen.
        final EventRing ring = new EventRing();
        Automation connected = null;
        try {
            connected = connect();
            connected.ua.setOnAccessibilityEventListener(ring);
        } catch (Throwable t) {
            try {
                server.close();
            } catch (IOException ignored) {
                // Already closed.
            }
            if (connected != null) {
                try {
                    connected.close();
                } catch (Throwable ignored) {
                    // The start fails with the first error.
                }
            }
            throw t;
        }
        final Automation a = connected;
        final AtomicBoolean done = new AtomicBoolean(false);
        // elapsedRealtime, not currentTimeMillis: a wall-clock change must not delay the
        // idle exit.
        final AtomicLong lastDump = new AtomicLong(SystemClock.elapsedRealtime());
        final long idleMs = idleSeconds * 1000L;
        // The dump workers and the timer that answers `busy` when one passes its bound.
        // Both use daemon threads, so the process can exit without waiting for a worker
        // still blocked on an app.
        final ExecutorService workers = Executors.newFixedThreadPool(DUMP_WORKERS, daemonThreads("droidyard-reader-dump"));
        // The one acting thread (ADR-0035 §1): injection and ACTION_SET_TEXT run here,
        // never on a dump worker, so a blocked injection cannot spend a dump's capacity.
        final ExecutorService acting = Executors.newSingleThreadExecutor(daemonThreads("droidyard-reader-act"));
        final ScheduledExecutorService timer = Executors.newSingleThreadScheduledExecutor(daemonThreads("droidyard-reader-timer"));
        // How many dumps are out on a worker now: a dump that finds both out is answered
        // `busy` at once.
        final AtomicInteger out = new AtomicInteger(0);
        // Whether an acting request is out: one that finds it out is answered `busy` at
        // once, before it executes anything (ADR-0035 §1).
        final AtomicInteger actingOut = new AtomicInteger(0);
        // The package of the last app the reader read, named in a busy answer. It is not
        // necessarily the app that did not answer: the foreground may have changed while
        // the read was blocked, so the daemon reads the window manager's focus instead. A
        // worker updates it when it reads a root.
        final AtomicReference<String> lastPackage = new AtomicReference<String>(null);

        Thread idle = new Thread(new Runnable() {
            @Override
            public void run() {
                while (!done.get()) {
                    long remaining = idleMs - (SystemClock.elapsedRealtime() - lastDump.get());
                    if (remaining <= 0) {
                        done.set(true);
                        wake(server, socketName);
                        return;
                    }
                    try {
                        Thread.sleep(Math.min(remaining, 1000));
                    } catch (InterruptedException e) {
                        return;
                    }
                }
            }
        }, "droidyard-reader-idle");
        idle.setDaemon(true);
        idle.start();

        try {
            while (!done.get()) {
                LocalSocket conn = server.accept();
                try {
                    // A stalled client must not hold the accept thread: without a read
                    // timeout one connection that never sends a full request blocks
                    // accept() forever, so later clients, quit and the idle exit never
                    // run and UiAutomation stays held. The timeout fails that read into
                    // the catch below, and the loop goes on.
                    conn.setSoTimeout(5000);
                    // A wake() connection, not a request: the idle timer expired.
                    if (done.get()) {
                        closeQuietly(conn);
                        break;
                    }
                    Request request = readRequest(conn.getInputStream());
                    if (request.tooLong) {
                        // Over REQUEST_CAP: the line was drained a bounded way, so the
                        // client's write completed, and it is refused before it is parsed
                        // or executed — never cut and acted on (ADR-0035 §1; plan m1n
                        // step 4). Nothing runs, so the uid fence is moot here: a refusal
                        // is safe for any peer, and the fence exists to stop execution.
                        write(conn, PREFIX + " refused too-long");
                        closeQuietly(conn);
                    } else if (!request.terminated) {
                        // EOF before a newline: a cut request is never parsed, for any verb
                        // (plan m1n step 4, fix round B1). Nothing ran.
                        write(conn, PREFIX + " refused unterminated");
                        closeQuietly(conn);
                    } else if (request.line.equals("ping")) {
                        // Answered on the accept thread, so a blocked dump never blocks
                        // it (ADR-0034 §1): a reader that answers ping is never restarted.
                        // The fence first, as for a dump (ADR-0102).
                        if (!denied(conn)) {
                            write(conn, "ok " + android.os.Process.myPid());
                            closeQuietly(conn);
                        }
                    } else if (request.line.equals("quit")) {
                        // Fenced too: any peer that reached the socket could otherwise stop
                        // the reader (ADR-0102).
                        if (!denied(conn)) {
                            done.set(true);
                            closeQuietly(conn);
                        }
                    } else if (request.line.equals("dump") || request.line.startsWith("dump ")) {
                        // The fence first, before anything is parsed or read: a dump is the
                        // device's screen, so it is answered only to adbd's peer (ADR-0102).
                        if (!denied(conn)) {
                            DumpArgs dumpArgs = DumpArgs.parse(request.line);
                            if (dumpArgs == null) {
                                write(conn, PREFIX + " bad dump arguments");
                                closeQuietly(conn);
                            } else {
                                lastDump.set(SystemClock.elapsedRealtime());
                                // The connection is owned by the worker (or the bound's
                                // timer) now: whichever writes first closes it.
                                submitDump(conn, a.ua, workers, timer, out, lastPackage, dumpArgs);
                            }
                        }
                    } else if (request.line.equals("events") || request.line.startsWith("events ")) {
                        // The fence first, as for a dump (ADR-0102 §2): the events are the
                        // device's screen. Answered here, on the accept thread, from the
                        // ring under its own lock: a blocked dump never delays it, and it
                        // makes no UiAutomation call (ADR-0111 Decision 4).
                        if (!denied(conn)) {
                            long[] ask = parseEvents(request.line);
                            if (ask == null) {
                                write(conn, PREFIX + " bad events arguments");
                            } else {
                                // A read in ADR-0030's sense: it keeps the reader alive.
                                lastDump.set(SystemClock.elapsedRealtime());
                                write(conn, ring.answer(ask[0], (int) ask[1]));
                            }
                            closeQuietly(conn);
                        }
                    } else if (actingRequest(request.line)) {
                        // The fence first, before anything is parsed or executed (ADR-0035
                        // §2): an acting request from any peer but adbd is answered
                        // `denied`.
                        if (!denied(conn)) {
                            Acting act = Acting.parse(request.line);
                            if (act == null) {
                                // Malformed: refused on the accept thread, so the acting
                                // thread is never taken for a request that will not run.
                                write(conn, PREFIX + " refused malformed");
                                closeQuietly(conn);
                            } else {
                                submitActing(conn, a.ua, acting, timer, actingOut, act);
                            }
                        }
                    } else {
                        write(conn, PREFIX + " unknown request");
                        closeQuietly(conn);
                    }
                } catch (Throwable t) {
                    try {
                        write(conn, PREFIX + " " + t);
                    } catch (IOException ignored) {
                        // The connection is gone; nothing to report it to.
                    }
                    closeQuietly(conn);
                }
            }
        } catch (IOException e) {
            if (!done.get()) {
                System.err.println(PREFIX + " accept: " + e);
            }
        } finally {
            done.set(true);
            closeQuietly(server);
            // Best effort: a worker blocked in an app does not answer an interrupt, and
            // the daemon threads do not hold the process open in any case.
            workers.shutdownNow();
            acting.shutdownNow();
            timer.shutdownNow();
            try {
                a.ua.setOnAccessibilityEventListener(null);
            } catch (Throwable ignored) {
                // The connection is closed next, which ends the events anyway.
            }
            try {
                a.close();
            } catch (Throwable t) {
                System.err.println(PREFIX + " disconnect: " + t);
            }
            idle.interrupt();
        }
        return 0;
    }

    // The idle exit cannot interrupt a blocked accept() by closing the server: on Linux a
    // close from another thread does not wake a blocked accept. A connection to our own
    // socket does, so that is the primary wake. If even that connect fails, closing the
    // server is the last resort: it does not unblock the blocked accept either, but done
    // is already set, so the reader exits at the next connection that gets through.
    private static void wake(LocalServerSocket server, String socketName) {
        LocalSocket s = new LocalSocket();
        try {
            s.connect(new LocalSocketAddress(socketName, LocalSocketAddress.Namespace.ABSTRACT));
        } catch (IOException e) {
            // Last resort, and it does not unblock accept; see above.
            try {
                server.close();
            } catch (IOException ignored) {
                // Already closed.
            }
        } finally {
            try {
                s.close();
            } catch (IOException ignored) {
                // Never connected.
            }
        }
    }

    // Hands one dump to a worker, or answers `busy` without starting one. With both workers
    // out the reader is out of capacity: the third connection is answered `busy` at once,
    // rather than queued behind a dump that may never finish (ADR-0034 §1).
    private static void submitDump(final LocalSocket conn, final UiAutomation ua,
            ExecutorService workers, ScheduledExecutorService timer,
            final AtomicInteger out, final AtomicReference<String> lastPackage,
            final DumpArgs dumpArgs) {
        final Answer answer = new Answer(conn);
        final long start = SystemClock.elapsedRealtime();
        if (out.get() >= DUMP_WORKERS) {
            answer.write(busyLine(0, lastPackage.get()));
            return;
        }
        out.incrementAndGet();
        // The device-side bound: past it the client is answered `busy` and the worker is
        // left to finish in the background (its document is abandoned).
        timer.schedule(new Runnable() {
            @Override
            public void run() {
                answer.write(busyLine(SystemClock.elapsedRealtime() - start, lastPackage.get()));
            }
        }, DUMP_BOUND_MS, TimeUnit.MILLISECONDS);
        workers.execute(new Runnable() {
            @Override
            public void run() {
                try {
                    Doc d = document(ua, start + DUMP_BOUND_MS, lastPackage, dumpArgs);
                    if (d.bound) {
                        // The bound passed while the document was being built (a worker
                        // can outlive its answer by the platform's own 5 s query wait).
                        String pkg = d.pkg != null ? d.pkg : lastPackage.get();
                        answer.write(busyLine(SystemClock.elapsedRealtime() - start, pkg));
                    } else if (d.xml == null) {
                        answer.write(PREFIX + " no active window root");
                    } else {
                        answer.write(d.xml);
                    }
                } catch (Throwable t) {
                    answer.write(PREFIX + " " + t);
                } finally {
                    out.decrementAndGet();
                }
            }
        });
    }

    // Hands one acting request to the single acting thread, or answers `busy` at once when
    // another is out (ADR-0035 §1; plan m1n step 4). The request is already fenced and
    // parsed, so this answer, and only this one, is a refusal that comes before anything is
    // executed: the busy answer never executes.
    private static void submitActing(final LocalSocket conn, final UiAutomation ua,
            ExecutorService acting, ScheduledExecutorService timer,
            final AtomicInteger actingOut, final Acting act) {
        final Answer answer = new Answer(conn);
        if (actingOut.get() >= 1) {
            answer.write(PREFIX + " busy");
            return;
        }
        actingOut.incrementAndGet();
        final long start = SystemClock.elapsedRealtime();
        // The device-side bound: past it the client is answered `timeout` — it says the
        // request may have been executed, not that it was not — and the acting thread is
        // left to finish in the background (its answer is discarded). It is not a refusal,
        // so it never leads the caller to a second delivery method (ADR-0035 §1, §3).
        timer.schedule(new Runnable() {
            @Override
            public void run() {
                answer.write(timeoutLine(SystemClock.elapsedRealtime() - start, null));
            }
        }, actBoundMs(act), TimeUnit.MILLISECONDS);
        acting.execute(new Runnable() {
            @Override
            public void run() {
                try {
                    // null means the bound answered before the request ran, so nothing was
                    // executed and there is nothing to write (N1).
                    String out = act.run(ua, answer);
                    if (out != null) answer.write(out);
                } catch (Throwable t) {
                    // The request was taken before the error, so it may have been
                    // executed: the answer is a timeout, never a refusal. The exception's
                    // class is appended so its cause is not lost (plan m1n step 4, fix
                    // round N6).
                    answer.write(timeoutLine(SystemClock.elapsedRealtime() - start, t));
                } finally {
                    actingOut.decrementAndGet();
                }
            }
        });
    }

    // The device-side bound of one acting request (ADR-0035 §1; plan m1n step 4, fix
    // round): a `keys` is bounded by its text's length (each character costs about 40 ms on
    // the emulator), everything else by the base. The same formula is computed on the host
    // (persistent.go readerActDeviceBound), which adds its margin above it.
    private static long actBoundMs(Acting act) {
        if (!act.verb.equals("keys")) return ACT_BOUND_BASE_MS;
        long chars = act.text.codePointCount(0, act.text.length());
        return Math.min(ACT_BOUND_BASE_MS + ACT_BOUND_PER_KEY_MS * chars, ACT_BOUND_CAP_MS);
    }

    // The timeout answer of ADR-0035 §1: the request was taken and the bound passed, so it
    // may have applied. The elapsed time is the host's signal of how long it waited, and a
    // pre-execution error's class is appended when there was one (plan m1n step 4, fix
    // round N6).
    private static String timeoutLine(long ms, Throwable t) {
        if (ms < 0) ms = 0;
        String line = PREFIX + " timeout " + ms;
        if (t != null) line += " " + t.getClass().getSimpleName();
        return line;
    }

    // Whether a request is one of the acting verbs (ADR-0035 §1). The uid fence applies to
    // these, and since ADR-0102 to `dump`, `ping` and `quit` too; it is the same check.
    private static boolean actingRequest(String request) {
        for (String verb : ACTING_VERBS) {
            if (request.equals(verb) || request.startsWith(verb + " ")) return true;
        }
        return false;
    }

    // Answers `denied <uid>` and closes the connection when its peer is not adbd's (ADR-0035
    // §2; ADR-0102 for the reads). True when it did: nothing is parsed or run for that peer.
    private static boolean denied(LocalSocket conn) throws IOException {
        int uid = peerUid(conn);
        if (actorUid(uid)) return false;
        write(conn, PREFIX + " denied " + uid);
        closeQuietly(conn);
        return true;
    }

    // A connection's peer uid (ADR-0035 §2). -1 when it cannot be read, which admits
    // nothing: the fence fails closed.
    private static int peerUid(LocalSocket conn) {
        try {
            Credentials c = conn.getPeerCredentials();
            return c == null ? -1 : c.getUid();
        } catch (Throwable t) {
            return -1;
        }
    }

    // Whether a peer's uid may act (ADR-0035 §2): root, or the shell user adbd's forward
    // arrives as.
    private static boolean actorUid(int uid) {
        for (int allowed : ACTOR_UIDS) {
            if (uid == allowed) return true;
        }
        return false;
    }

    // One acting request, parsed and validated on the accept thread so a malformed one is
    // answered `refused` without taking the acting thread (ADR-0035 §1). run() performs it
    // on the acting thread and returns the reader's answer line.
    private static final class Acting {
        private final String verb;
        private int x, y;
        private String text;
        private boolean replace;

        private Acting(String verb) {
            this.verb = verb;
        }

        // parse returns null for a malformed request (a bad verb, argument count, number or
        // base64): the caller answers `refused malformed`, having executed nothing. The
        // base64 of an empty text is empty: `settext replace ` clears the field, so it is
        // well formed, but `keys ` and `settext insert ` of nothing are refused (plan m1n
        // step 4, fix round).
        static Acting parse(String request) {
            String[] parts = request.split(" ", 3);
            if (parts.length == 0) return null;
            if (parts[0].equals("tap")) {
                if (parts.length != 3) return null;
                Acting act = new Acting("tap");
                try {
                    act.x = Integer.parseInt(parts[1]);
                    act.y = Integer.parseInt(parts[2]);
                } catch (NumberFormatException e) {
                    return null;
                }
                return act;
            }
            if (parts[0].equals("keys")) {
                if (parts.length != 2) return null;
                String text = decode(parts[1]);
                if (text == null || text.isEmpty()) return null;
                Acting act = new Acting("keys");
                act.text = text;
                return act;
            }
            if (parts[0].equals("settext")) {
                if (parts.length != 3) return null;
                if (!parts[1].equals("replace") && !parts[1].equals("insert")) return null;
                String text = decode(parts[2]);
                if (text == null) return null;
                boolean replace = parts[1].equals("replace");
                if (text.isEmpty() && !replace) return null;
                Acting act = new Acting("settext");
                act.replace = replace;
                act.text = text;
                return act;
            }
            return null;
        }

        String run(UiAutomation ua, Answer answer) throws Exception {
            if (verb.equals("tap")) return tap(ua, x, y, answer);
            if (verb.equals("keys")) return keys(ua, text, answer);
            return setText(ua, replace, text, answer);
        }
    }

    // Injects a tap at (x, y) (ADR-0035 §1): ACTION_DOWN asynchronous and ACTION_UP
    // synchronous, so the answer waits for the up to be delivered (the spike's timing). The
    // pair is executed whether or not the platform reports it, and the answer is `done`
    // either way: a refusal would let the caller tap a second time (ADR-0035 §3).
    private static String tap(UiAutomation ua, int x, int y, Answer answer) {
        if (answer.answered()) {
            // The bound answered before anything ran: running the tap now would inject into
            // whatever has focus (plan m1n step 4, fix round N1).
            return null;
        }
        long t = SystemClock.uptimeMillis();
        MotionEvent down = MotionEvent.obtain(t, t, MotionEvent.ACTION_DOWN, x, y, 0);
        MotionEvent up = MotionEvent.obtain(t, t + 50, MotionEvent.ACTION_UP, x, y, 0);
        try {
            down.setSource(InputDevice.SOURCE_TOUCHSCREEN);
            up.setSource(InputDevice.SOURCE_TOUCHSCREEN);
            ua.injectInputEvent(down, false);
            ua.injectInputEvent(up, true);
        } finally {
            down.recycle();
            up.recycle();
        }
        return PREFIX + " done";
    }

    // Types text as the virtual keyboard's key events (ADR-0035 §1), each injected
    // synchronously and stamped when it is injected: the app's pace sets the typing's, and no
    // event waits in the queue long enough to be dropped as stale (level 5, plan m1n step 4: a
    // 500-character string injected asynchronously with getEvents' timestamps lost all but 172
    // characters). FLAG_KEEP_TOUCH_MODE keeps the injected keys from taking the device out of
    // touch mode. Text the keyboard cannot map is refused before anything is injected.
    private static String keys(UiAutomation ua, String text, Answer answer) throws InterruptedException {
        KeyCharacterMap map = KeyCharacterMap.load(KeyCharacterMap.VIRTUAL_KEYBOARD);
        KeyEvent[] events = map.getEvents(text.toCharArray());
        if (events == null || events.length == 0) {
            return PREFIX + " refused unmappable";
        }
        // The field's text before the keys: the read-back then waits for it to change,
        // rather than returning the value read a frame before the field renders the new one.
        CharSequence before = focusedText(ua, null);
        if (answer.answered()) {
            return null; // N1: no key is injected once the bound has answered.
        }
        long downTime = SystemClock.uptimeMillis();
        for (int i = 0; i < events.length; i++) {
            KeyEvent e = events[i];
            long now = SystemClock.uptimeMillis();
            if (e.getAction() == KeyEvent.ACTION_DOWN) {
                downTime = now;
            }
            KeyEvent tagged = new KeyEvent(downTime, now, e.getAction(),
                    e.getKeyCode(), e.getRepeatCount(), e.getMetaState(), e.getDeviceId(),
                    e.getFlags() | KeyEvent.FLAG_KEEP_TOUCH_MODE, e.getSource());
            ua.injectInputEvent(tagged, true);
        }
        return done(readBack(ua, null, before == null ? "" : before.toString(), true));
    }

    // Sets the focused input node's text (ADR-0035 §1): ACTION_SET_TEXT with the text
    // itself for a replace, and with the text inserted at the selection for an insert. The
    // field is then refreshed and read back, polling briefly for one that renders a frame
    // later. No focused input node, a password node asked to insert, or one that does not
    // accept the action, is refused before anything is typed; the caller may then use
    // another method (ADR-0035 §3; plan m1n step 4, fix round).
    // The focused input node, found by walking the active window from its root: the node
    // findFocus(FOCUS_INPUT) returns on a Compose screen refused ACTION_SET_TEXT at level 5
    // while the same field found from the root took it (plan m1n step 5; step 2 (e) set a
    // Compose field from the root). A focused, editable node is preferred; findFocus is the
    // fallback when the walk finds none.
    private static AccessibilityNodeInfo focusedEditable(UiAutomation ua) {
        AccessibilityNodeInfo root = ua.getRootInActiveWindow();
        AccessibilityNodeInfo found = root == null ? null : focusedEditable(root, 0);
        return found != null ? found : ua.findFocus(AccessibilityNodeInfo.FOCUS_INPUT);
    }

    private static AccessibilityNodeInfo focusedEditable(AccessibilityNodeInfo n, int depth) {
        if (n.isFocused() && n.isEditable()) {
            return n;
        }
        if (depth > 100) {
            return null;
        }
        int c = n.getChildCount();
        for (int i = 0; i < c; i++) {
            AccessibilityNodeInfo ch = n.getChild(i);
            if (ch != null) {
                AccessibilityNodeInfo r = focusedEditable(ch, depth + 1);
                if (r != null) {
                    return r;
                }
            }
        }
        return null;
    }

    private static String setText(UiAutomation ua, boolean replace, String text, Answer answer) throws InterruptedException {
        AccessibilityNodeInfo node = focusedEditable(ua);
        if (node == null) {
            return PREFIX + " refused no-focus";
        }
        if (!replace && node.isPassword()) {
            // A password field reads back masked, so an insert that read its own result
            // back would compare against the mask: keys is the method for one, and this
            // refusal (a set-text the node would not take) lets the caller choose it
            // (ADR-0035 §3).
            return PREFIX + " refused settext";
        }
        String value = replace ? text : insert(node, text);
        Bundle args = new Bundle();
        args.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, value);
        if (answer.answered()) {
            return null; // N1: nothing is set once the bound has answered.
        }
        if (!node.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args)) {
            return PREFIX + " refused settext";
        }
        return done(readBack(ua, node, value, false));
    }

    // The answer of a request that was executed and read back (ADR-0035 §1). An empty
    // read-back is no read-back: the line is `done` with nothing after it.
    private static String done(CharSequence readBack) {
        if (readBack == null || readBack.length() == 0) return PREFIX + " done";
        return PREFIX + " done " + base64(readBack.toString());
    }

    // The focused node's text with text inserted at its selection (ADR-0035 §1; plan m1n
    // step 4): a hint rendered as the node's text is treated as empty, and a selection
    // outside the text is clamped to its ends. API 26's isShowingHintText says directly
    // whether the text is the hint; below it the hint is compared (fix round N5).
    private static String insert(AccessibilityNodeInfo node, String text) {
        CharSequence cur = node.getText();
        String pre = cur == null ? "" : cur.toString();
        if (Build.VERSION.SDK_INT >= 26) {
            if (node.isShowingHintText()) pre = "";
        } else {
            CharSequence hint = node.getHintText();
            if (hint != null && pre.contentEquals(hint)) pre = "";
        }
        int sel = node.getTextSelectionStart();
        if (sel < 0) sel = 0;
        if (sel > pre.length()) sel = pre.length();
        return pre.substring(0, sel) + text + pre.substring(sel);
    }

    // The text a field reads after an act, polled briefly (ADR-0035 §1): a Compose field
    // renders its new value a frame later, so an immediate read can be empty (plan m1n step
    // 2 (e)). With changed false it returns as soon as the field reads expected (a settext,
    // which knows what it asked for); with changed true as soon as it reads anything else
    // (keys, which does not). Either way it otherwise returns the last non-empty text it saw
    // before the bound, and null when it saw none.
    private static CharSequence readBack(UiAutomation ua, AccessibilityNodeInfo node, String expected, boolean changed) throws InterruptedException {
        long deadline = SystemClock.elapsedRealtime() + READBACK_BOUND_MS;
        CharSequence last = null;
        while (true) {
            CharSequence t = focusedText(ua, node);
            if (t != null && t.length() > 0) {
                String s = t.toString();
                if (changed ? !s.equals(expected) : s.equals(expected)) return t;
                last = t;
            }
            if (SystemClock.elapsedRealtime() >= deadline) return last;
            Thread.sleep(READBACK_STEP_MS);
        }
    }

    // The text of the field an act holds, refreshed, or of the node that holds input focus
    // now when the held one reads nothing (a node that reappeared).
    private static CharSequence focusedText(UiAutomation ua, AccessibilityNodeInfo node) {
        if (node != null) {
            node.refresh();
            CharSequence t = node.getText();
            if (t != null && t.length() > 0) return t;
        }
        AccessibilityNodeInfo focus = focusedEditable(ua);
        return focus == null ? null : focus.getText();
    }

    // The text of a base64 argument, or null when it is not strictly base64: a request that
    // cannot be decoded is refused before anything is executed (ADR-0035 §1). The Android
    // decoder tolerates non-canonical input, so the decoded bytes are re-encoded with the
    // same canonical flags and compared: a mismatch is refused (fix round B1).
    private static String decode(String s) {
        byte[] raw;
        try {
            raw = Base64.decode(s, Base64.DEFAULT);
        } catch (IllegalArgumentException e) {
            return null;
        }
        if (!Base64.encodeToString(raw, Base64.NO_WRAP).equals(s)) return null;
        return new String(raw, StandardCharsets.UTF_8);
    }

    private static String base64(String s) {
        return Base64.encodeToString(s.getBytes(StandardCharsets.UTF_8), Base64.NO_WRAP);
    }

    // The words of a dump request. `dump` keeps the reader's own default idle wait (D2), and
    // `dump idle <quietMs> <maxMs>` shortens it (a poll after an action asks for 100/200).
    // `dump full [invisible]` (ADR-0110) is the full document: each word at most once, in
    // that order, nothing else after it, and the default idle wait. parse returns null when
    // the request is malformed, and then it is not a dump. An idle bound must be positive:
    // waitForIdle treats a 0 as unbounded, which no caller asks for.
    private static final class DumpArgs {
        static final DumpArgs PLAIN = new DumpArgs(IDLE_QUIET_MS, IDLE_MAX_MS, false, false);
        final long quiet;
        final long max;
        final boolean full;
        final boolean invisible;

        private DumpArgs(long quiet, long max, boolean full, boolean invisible) {
            this.quiet = quiet;
            this.max = max;
            this.full = full;
            this.invisible = invisible;
        }

        static DumpArgs parse(String request) {
            if (request.equals("dump")) return PLAIN;
            // A trailing empty word is kept (split with a negative limit), so `dump full `
            // is not `dump full`.
            String[] words = request.split(" ", -1);
            if (words.length >= 2 && words[0].equals("dump") && words[1].equals("full")) {
                if (words.length == 2) return new DumpArgs(IDLE_QUIET_MS, IDLE_MAX_MS, true, false);
                if (words.length == 3 && words[2].equals("invisible")) return new DumpArgs(IDLE_QUIET_MS, IDLE_MAX_MS, true, true);
                return null;
            }
            String[] parts = request.split(" ");
            if (parts.length != 4 || !parts[0].equals("dump") || !parts[1].equals("idle")) return null;
            try {
                long quiet = Long.parseLong(parts[2]);
                long max = Long.parseLong(parts[3]);
                if (quiet <= 0 || max <= 0) return null;
                return new DumpArgs(quiet, max, false, false);
            } catch (NumberFormatException e) {
                return null;
            }
        }
    }

    // The words of an `events` request (ADR-0111 Decision 4): `events <afterSeq> <max>`, both
    // plain decimal digits, nothing else, max from 1 to EVENTS_MAX. Returns {afterSeq, max}, or
    // null when the request is malformed, and then nothing is read.
    private static long[] parseEvents(String request) {
        String[] words = request.split(" ", -1);
        if (words.length != 3 || !words[0].equals("events")) return null;
        if (!digits(words[1]) || !digits(words[2])) return null;
        long afterSeq = Long.parseLong(words[1]);
        long max = Long.parseLong(words[2]);
        if (max < 1 || max > EVENTS_MAX) return null;
        return new long[] { afterSeq, max };
    }

    // Whether s is one to eighteen decimal digits: a sequence number that cannot overflow.
    private static boolean digits(String s) {
        if (s.length() < 1 || s.length() > 18) return false;
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            if (c < '0' || c > '9') return false;
        }
        return true;
    }

    // The accessibility events the reader keeps (ADR-0111). It is the listener of the reader's
    // UiAutomation connection and the ring the `events` request reads, under one lock. The
    // listener only copies fields: it takes no node, no source and no parcelable data, and
    // makes no call into the system, so it cannot block the UiAutomation thread. Nothing but a
    // toast's text is ever read from an event (Decision 3): a notification event's getText()
    // is not called at all.
    private static final class EventRing implements UiAutomation.OnAccessibilityEventListener {
        // Working defaults of ADR-0111: the ring's size and the fold window.
        static final int CAPACITY = 512;
        static final long FOLD_MS = 250L;
        // The most characters kept of a toast's text, and of a package or class name. A name
        // is the app's own string, so it is capped to keep an answer of 200 entries well under
        // the 1 MiB the daemon reads.
        static final int TEXT_CAP = 512;
        static final int NAME_CAP = 128;

        private final Object lock = new Object();
        private final Entry[] ring = new Entry[CAPACITY];
        // The sequence number the next entry gets, and the oldest one still in the ring.
        private long next = 1;
        private long oldest = 1;
        // Drawn at start (64 random bits): a client tells a restarted reader by it.
        private final String instance;
        private final long startedWallMs;

        EventRing() {
            long id = new SecureRandom().nextLong();
            this.instance = String.format(Locale.ROOT, "%016x", id);
            this.startedWallMs = System.currentTimeMillis();
        }

        // One kept event. Once an `events` answer has returned it, it is sealed and a later
        // event never folds into it (Decision 2), so a cursor past it misses nothing.
        private static final class Entry {
            final long seq;
            long wallMs;
            long eventTime;
            final String type;
            final String pkg;
            final String cls;
            final int window;
            int changes;
            int count = 1;
            final String text;
            boolean sealed;

            Entry(long seq, long wallMs, long eventTime, String type, String pkg, String cls, int window, int changes, String text) {
                this.seq = seq;
                this.wallMs = wallMs;
                this.eventTime = eventTime;
                this.type = type;
                this.pkg = pkg;
                this.cls = cls;
                this.window = window;
                this.changes = changes;
                this.text = text;
            }
        }

        @Override
        public void onAccessibilityEvent(AccessibilityEvent e) {
            try {
                record(e);
            } catch (Throwable ignored) {
                // Never into the UiAutomation thread: an event that cannot be copied is lost.
            }
        }

        private void record(AccessibilityEvent e) {
            String cls = cap(e.getClassName(), NAME_CAP);
            String type = typeName(e.getEventType(), cls);
            if (type == null) return;
            String pkg = cap(e.getPackageName(), NAME_CAP);
            long eventTime = e.getEventTime();
            // The event's age is how long ago uptime says it happened: wall time then.
            long age = SystemClock.uptimeMillis() - eventTime;
            long wallMs = System.currentTimeMillis() - (age > 0 ? age : 0);
            int window = e.getWindowId();
            int changes = e.getContentChangeTypes();
            // A toast's text, and no other event's: a notification's is never read.
            String text = type.equals("toast") ? toastText(e) : null;
            boolean foldable = type.equals("windowContent") || type.equals("scroll") || type.equals("textChanged");
            synchronized (lock) {
                Entry n = next > 1 ? ring[(int) ((next - 1) % CAPACITY)] : null;
                if (foldable && n != null && !n.sealed && n.type.equals(type)
                        && same(n.pkg, pkg) && n.window == window
                        && eventTime - n.eventTime <= FOLD_MS) {
                    if (n.count < Integer.MAX_VALUE) n.count++;
                    // Events are not guaranteed in timestamp order: an older one folds in
                    // without moving the entry's time back.
                    n.wallMs = Math.max(n.wallMs, wallMs);
                    n.eventTime = Math.max(n.eventTime, eventTime);
                    n.changes |= changes;
                    return;
                }
                ring[(int) (next % CAPACITY)] = new Entry(next, wallMs, eventTime, type, pkg, cls, window, changes, text);
                next++;
                oldest = next > CAPACITY ? next - CAPACITY : 1;
            }
        }

        // The wire name of an event type (the plan's Q6), or null for one the ring does not
        // keep. A notification-state event is a toast only by its class, as the spike saw it
        // (android.widget.Toast); every other one is a notification.
        private static String typeName(int type, String cls) {
            switch (type) {
                case AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED: return "windowState";
                case AccessibilityEvent.TYPE_WINDOW_CONTENT_CHANGED: return "windowContent";
                case AccessibilityEvent.TYPE_WINDOWS_CHANGED: return "windows";
                case AccessibilityEvent.TYPE_VIEW_TEXT_CHANGED: return "textChanged";
                case AccessibilityEvent.TYPE_VIEW_FOCUSED:
                case AccessibilityEvent.TYPE_VIEW_ACCESSIBILITY_FOCUSED: return "focus";
                case AccessibilityEvent.TYPE_VIEW_SCROLLED: return "scroll";
                case AccessibilityEvent.TYPE_VIEW_CLICKED:
                case AccessibilityEvent.TYPE_VIEW_LONG_CLICKED: return "click";
                case AccessibilityEvent.TYPE_VIEW_SELECTED: return "selected";
                case AccessibilityEvent.TYPE_ANNOUNCEMENT: return "announcement";
                case AccessibilityEvent.TYPE_NOTIFICATION_STATE_CHANGED:
                    return cls != null && cls.startsWith("android.widget.Toast") ? "toast" : "notification";
                default: return null;
            }
        }

        // A toast's text: the event's text parts joined by a space, capped. Null when empty.
        private static String toastText(AccessibilityEvent e) {
            List<CharSequence> parts = e.getText();
            if (parts == null || parts.isEmpty()) return null;
            StringBuilder sb = new StringBuilder();
            for (CharSequence part : parts) {
                if (part == null) continue;
                if (sb.length() > 0) sb.append(' ');
                sb.append(part);
                if (sb.length() > TEXT_CAP) break;
            }
            return cap(sb, TEXT_CAP);
        }

        // At most limit characters of s as a String, never ending in half a surrogate pair;
        // null for null or empty.
        private static String cap(CharSequence s, int limit) {
            if (s == null || s.length() == 0) return null;
            int end = Math.min(s.length(), limit);
            if (end < s.length() && Character.isHighSurrogate(s.charAt(end - 1))) end--;
            return end == 0 ? null : s.subSequence(0, end).toString();
        }

        private static boolean same(String a, String b) {
            return a == null ? b == null : a.equals(b);
        }

        // The answer to `events <afterSeq> <max>`: the header, then the entries after afterSeq,
        // at most max, oldest first. Every entry returned is sealed under the lock, so no event
        // folds into it afterwards; the lines are built outside it from the sealed (now
        // unchanging) entries.
        String answer(long afterSeq, int max) {
            Entry[] out = new Entry[max];
            int n = 0;
            long oldestSeq;
            long nextSeq;
            synchronized (lock) {
                oldestSeq = oldest;
                nextSeq = next;
                long from = Math.max(afterSeq + 1, oldest);
                for (long seq = from; seq < next && n < max; seq++) {
                    Entry e = ring[(int) (seq % CAPACITY)];
                    e.sealed = true;
                    out[n++] = e;
                }
            }
            StringBuilder sb = new StringBuilder(128 + n * 96);
            sb.append("events ").append(instance).append(' ').append(oldestSeq).append(' ').append(nextSeq)
                    .append(' ').append(System.currentTimeMillis()).append(' ').append(startedWallMs).append('\n');
            for (int i = 0; i < n; i++) {
                Entry e = out[i];
                sb.append(e.seq).append('\t').append(e.wallMs).append('\t').append(e.type).append('\t')
                        .append(e.count).append('\t').append(e.window).append('\t').append(e.changes).append('\t')
                        .append(b64(e.pkg)).append('\t').append(b64(e.cls)).append('\t').append(b64(e.text)).append('\n');
            }
            return sb.toString();
        }

        private static String b64(String s) {
            return s == null ? "" : base64(s);
        }
    }

    // The busy answer of ADR-0034 §1: how long the read waited, and the package of the app
    // that did not answer when one is known (omitted otherwise, so the daemon reports no
    // package rather than an empty one).
    private static String busyLine(long ms, String pkg) {
        if (ms < 0) ms = 0;
        return pkg == null || pkg.length() == 0 ? PREFIX + " busy " + ms : PREFIX + " busy " + ms + " " + pkg;
    }

    // The one answer of a dumped connection: whichever of the worker and the bound's timer
    // writes first wins, and the connection is closed with it. A worker that finishes after
    // the bound passed is discarded.
    private static final class Answer {
        private final LocalSocket conn;
        private final AtomicBoolean answered = new AtomicBoolean(false);

        Answer(LocalSocket conn) {
            this.conn = conn;
        }

        void write(String s) {
            if (!answered.compareAndSet(false, true)) return;
            try {
                DroidyardReader.write(conn, s);
            } catch (IOException ignored) {
                // The client is gone; nothing to report it to.
            } finally {
                closeQuietly(conn);
            }
        }

        // answered reports whether the connection has been answered, so an acting request
        // whose bound passed before it ran can decline to run it (N1).
        boolean answered() {
            return answered.get();
        }
    }

    private static ThreadFactory daemonThreads(final String name) {
        return new ThreadFactory() {
            private final AtomicInteger n = new AtomicInteger(0);

            @Override
            public Thread newThread(Runnable r) {
                Thread t = new Thread(r, name + "-" + n.incrementAndGet());
                t.setDaemon(true);
                return t;
            }
        };
    }

    private static void closeQuietly(Closeable c) {
        if (c == null) return;
        try {
            c.close();
        } catch (IOException ignored) {
            // Already closed.
        }
    }

    // One request line, as readRequest saw it (plan m1n step 4, fix round B1): line is the
    // request text without its newline, terminated says a newline was reached, and tooLong
    // says more than REQUEST_CAP bytes arrived first. An unterminated line is never parsed:
    // a request cut by EOF is refused for every verb, and tooLong is refused likewise.
    private static final class Request {
        final String line;
        final boolean terminated;
        final boolean tooLong;

        Request(String line, boolean terminated, boolean tooLong) {
            this.line = line;
            this.terminated = terminated;
            this.tooLong = tooLong;
        }
    }

    // One request per connection: read to the first newline. At most REQUEST_CAP bytes are
    // kept; a longer line is drained up to REQUEST_DRAIN_CAP bytes (so the client's write
    // completes and it can read the answer) and tooLong is set, so the caller refuses it —
    // a cut line is never acted on (ADR-0035 §1; plan m1n step 4, fix round B2, N4). A line
    // ended by EOF before a newline is reported unterminated rather than parsed, and only a
    // trailing carriage return is stripped: a trailing empty argument (`settext replace `)
    // is part of the request, so no trim is applied.
    private static Request readRequest(InputStream in) throws IOException {
        ByteArrayOutputStream buf = new ByteArrayOutputStream(64);
        boolean tooLong = false;
        boolean terminated = false;
        int drained = 0;
        int b;
        while ((b = in.read()) != -1) {
            if (b == '\n') {
                terminated = true;
                break;
            }
            if (buf.size() < REQUEST_CAP) {
                buf.write(b);
            } else {
                tooLong = true;
                if (++drained >= REQUEST_DRAIN_CAP) break;
            }
        }
        String line = new String(buf.toByteArray(), StandardCharsets.UTF_8);
        if (line.endsWith("\r")) line = line.substring(0, line.length() - 1);
        return new Request(line, terminated, tooLong);
    }

    private static void write(LocalSocket conn, String s) throws IOException {
        OutputStream out = conn.getOutputStream();
        out.write(s.getBytes(StandardCharsets.UTF_8));
        out.flush();
    }

    // UiAutomation's connection constructor and connect(int) are hidden API, reached by
    // reflection as the platform's own uiautomator reaches them.
    private static Automation connect() throws Exception {
        Looper.prepareMainLooper();
        HandlerThread thread = new HandlerThread("droidyard-reader");
        thread.start();
        Object connection = Class.forName("android.app.UiAutomationConnection").getDeclaredConstructor().newInstance();
        Class<?> iconnection = Class.forName("android.app.IUiAutomationConnection");
        UiAutomation ua = null;
        for (Constructor<?> c : UiAutomation.class.getDeclaredConstructors()) {
            Class<?>[] p = c.getParameterTypes();
            if (p.length == 2 && p[0] == Looper.class && p[1] == iconnection) {
                c.setAccessible(true);
                ua = (UiAutomation) c.newInstance(thread.getLooper(), connection);
            }
        }
        if (ua == null) throw new IllegalStateException("no UiAutomation(Looper, IUiAutomationConnection) constructor");
        Method connect = UiAutomation.class.getDeclaredMethod("connect", int.class);
        connect.setAccessible(true);
        Method disconnect = UiAutomation.class.getDeclaredMethod("disconnect");
        disconnect.setAccessible(true);
        connect.invoke(ua, UiAutomation.FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES);
        return new Automation(ua, thread, disconnect);
    }

    // A connected UiAutomation and the machinery to disconnect it: the same one-shot and
    // serve modes both need it, once each.
    private static final class Automation {
        final UiAutomation ua;
        private final HandlerThread thread;
        private final Method disconnect;

        Automation(UiAutomation ua, HandlerThread thread, Method disconnect) {
            this.ua = ua;
            this.thread = thread;
            this.disconnect = disconnect;
        }

        // As the one-shot always did: a disconnect failure propagates, and then
        // quitSafely is not reached. Serve mode catches it instead, since a shutdown
        // must still close its listener.
        void close() throws Exception {
            disconnect.invoke(ua);
            thread.quitSafely();
        }
    }

    // One dump's outcome: the XML (null when there is no root), whether the device-side
    // bound passed before the document was complete, and the package the root named.
    private static final class Doc {
        final String xml;
        final boolean bound;
        final String pkg;

        Doc(String xml, boolean bound, String pkg) {
            this.xml = xml;
            this.bound = bound;
            this.pkg = pkg;
        }
    }

    // Set while a document is built and the device-side bound passes between node queries.
    private static final class Bound {
        boolean hit;
        // Set when a full document passed FULL_DOC_CAP_CHARS: the traversal stops there.
        boolean over;
    }

    // The active window's tree, as the XML the one-shot writes; xml null when there is no
    // root, bound true when deadlineMs passed before the document was complete. The bound
    // is checked between node queries, so a traversal is abandoned at it; a single blocked
    // query cannot be interrupted and is bounded by the platform's 5 s interaction timeout
    // instead. waitForIdle's timeout is not fatal: the tree is read anyway. A root read
    // updates lastPackage (null when the caller does not want it). idleQuiet and idleMax are
    // waitForIdle's bounds (D2): the reader's own idle wait is the settle, and a poll after
    // an action asks for a shorter one.
    private static Doc document(UiAutomation ua, long deadlineMs, AtomicReference<String> lastPackage,
            DumpArgs args) throws Exception {
        try {
            ua.waitForIdle(args.quiet, args.max);
        } catch (Exception e) {
            // Not idle in time (a TimeoutException): read the tree anyway.
        }
        if (SystemClock.elapsedRealtime() > deadlineMs) {
            return new Doc(null, true, null);
        }
        AccessibilityNodeInfo root = null;
        for (int i = 0; i < 5 && root == null; i++) {
            root = ua.getRootInActiveWindow();
            if (root == null) {
                if (SystemClock.elapsedRealtime() > deadlineMs) break;
                Thread.sleep(100);
            }
        }
        if (root == null) {
            return new Doc(null, SystemClock.elapsedRealtime() > deadlineMs, null);
        }
        String pkg = null;
        CharSequence p = root.getPackageName();
        if (p != null) {
            pkg = p.toString();
            if (lastPackage != null) lastPackage.set(pkg);
        }
        Point size = new Point();
        int rotation = display(size);
        StringBuilder sb = new StringBuilder(16 << 10);
        sb.append("<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation=\"").append(rotation).append("\">");
        Bound bound = new Bound();
        if (args.full) {
            sb.append('\n');
            fullNode(root, 0, new Rect(0, 0, size.x, size.y), sb, deadlineMs, bound, args.invisible);
        } else {
            node(root, 0, new Rect(0, 0, size.x, size.y), sb, deadlineMs, bound);
        }
        if (bound.hit) {
            // The document is abandoned: a partial tree is not a document.
            return new Doc(null, true, pkg);
        }
        if (bound.over) {
            // A document past the cap is sent without its end tag: the daemon reads at most
            // its own cap, sees more than that and fails naming it (ADR-0110 Decision 5), and
            // the reader never holds more than the cap in memory.
            return new Doc(sb.toString(), false, pkg);
        }
        sb.append(args.full ? "</hierarchy>\n" : "</hierarchy>");
        return new Doc(sb.toString(), false, pkg);
    }

    // The default display's rotation and real size, from DisplayManagerGlobal as uiautomator's
    // UiDevice reads them; rotation 0 and an unbounded rectangle if the hidden call fails.
    private static int display(Point size) {
        size.set(Integer.MAX_VALUE, Integer.MAX_VALUE);
        try {
            Object global = Class.forName("android.hardware.display.DisplayManagerGlobal").getMethod("getInstance").invoke(null);
            android.view.Display d = (android.view.Display) global.getClass().getMethod("getRealDisplay", int.class).invoke(global, 0);
            d.getRealSize(size);
            return d.getRotation();
        } catch (Throwable t) {
            return 0;
        }
    }

    private static void node(AccessibilityNodeInfo n, int index, Rect display, StringBuilder sb, long deadlineMs, Bound bound) {
        Rect b = new Rect();
        n.getBoundsInScreen(b);
        b.intersect(display);
        sb.append("<node index=\"").append(index).append('"');
        attr(sb, "text", n.getText());
        attr(sb, "resource-id", n.getViewIdResourceName());
        attr(sb, "class", n.getClassName());
        attr(sb, "package", n.getPackageName());
        attr(sb, "content-desc", n.getContentDescription());
        flag(sb, "checkable", n.isCheckable());
        flag(sb, "checked", n.isChecked());
        flag(sb, "clickable", n.isClickable());
        flag(sb, "enabled", n.isEnabled());
        flag(sb, "focusable", n.isFocusable());
        flag(sb, "focused", n.isFocused());
        // editable marks a node ACTION_SET_TEXT can act on. uiautomator's dump has no such
        // attribute; the daemon uses it to recognise a focused input field before a tap, so
        // it can hide the soft keyboard when the field sits under it (plan m1n step 5, fix
        // round L1; ui-automation.md §Typing).
        flag(sb, "editable", n.isEditable());
        flag(sb, "scrollable", n.isScrollable());
        flag(sb, "long-clickable", n.isLongClickable());
        flag(sb, "password", n.isPassword());
        flag(sb, "selected", n.isSelected());
        sb.append(" bounds=\"[").append(b.left).append(',').append(b.top).append("][").append(b.right).append(',').append(b.bottom).append("]\"");
        sb.append(" drawing-order=\"").append(n.getDrawingOrder()).append('"');
        attr(sb, "hint", Build.VERSION.SDK_INT >= 26 ? n.getHintText() : null);
        sb.append('>');
        int count = n.getChildCount();
        for (int i = 0; i < count; i++) {
            if (bound.hit) return;
            // getChild may itself block on a frozen app: check the bound before each query.
            if (SystemClock.elapsedRealtime() > deadlineMs) {
                bound.hit = true;
                return;
            }
            AccessibilityNodeInfo c = n.getChild(i);
            // As uiautomator does: a child that is not visible to the user is not dumped.
            if (c != null && c.isVisibleToUser()) node(c, i, display, sb, deadlineMs, bound);
        }
        sb.append("</node>");
    }

    // One node of the full document (ADR-0110), one element per line: a leaf is
    // `<node ... />` as uiautomator writes it, a node with children is its start tag, one line
    // for each child and `</node>`. The attributes are uiautomator's, in its order, on every
    // node, layout-only ones included (the plain dump's, less `editable`, which is an extra
    // here); then the extras, each written only when it holds a non-default value and each
    // behind its API level. A boolean extra is written for its non-default value (`important`
    // and `visible-to-user` are written as "false", the rest as "true"), a count or index
    // for the info object it belongs to whenever that object is present. Nothing here
    // queries another node (no getLabeledBy, no traversal links), so a blocked app costs the
    // one query already in flight and the bound between queries holds. With invisible, the
    // children that are not visible to the user are kept, each marked visible-to-user="false"
    // by its own getter.
    private static void fullNode(AccessibilityNodeInfo n, int index, Rect display, StringBuilder sb,
            long deadlineMs, Bound bound, boolean invisible) {
        Rect b = new Rect();
        n.getBoundsInScreen(b);
        b.intersect(display);
        sb.append("<node index=\"").append(index).append('"');
        attr(sb, "text", n.getText());
        attr(sb, "resource-id", n.getViewIdResourceName());
        attr(sb, "class", n.getClassName());
        attr(sb, "package", n.getPackageName());
        attr(sb, "content-desc", n.getContentDescription());
        flag(sb, "checkable", n.isCheckable());
        flag(sb, "checked", n.isChecked());
        flag(sb, "clickable", n.isClickable());
        flag(sb, "enabled", n.isEnabled());
        flag(sb, "focusable", n.isFocusable());
        flag(sb, "focused", n.isFocused());
        flag(sb, "scrollable", n.isScrollable());
        flag(sb, "long-clickable", n.isLongClickable());
        flag(sb, "password", n.isPassword());
        flag(sb, "selected", n.isSelected());
        sb.append(" bounds=\"[").append(b.left).append(',').append(b.top).append("][").append(b.right).append(',').append(b.bottom).append("]\"");
        sb.append(" drawing-order=\"").append(n.getDrawingOrder()).append('"');
        attr(sb, "hint", Build.VERSION.SDK_INT >= 26 ? n.getHintText() : null);
        // The extras, in ADR-0110's order.
        extraFlag(sb, "editable", n.isEditable());
        if (!n.isVisibleToUser()) flag(sb, "visible-to-user", false);
        if (Build.VERSION.SDK_INT >= 30) extra(sb, "state-description", n.getStateDescription());
        extra(sb, "error", n.getError());
        if (Build.VERSION.SDK_INT >= 28) {
            extraFlag(sb, "heading", n.isHeading());
            extra(sb, "pane-title", n.getPaneTitle());
            extra(sb, "tooltip", n.getTooltipText());
        }
        if (n.getInputType() != 0) extra(sb, "input-type", String.valueOf(n.getInputType()));
        if (n.getMaxTextLength() >= 0) extra(sb, "max-text-length", String.valueOf(n.getMaxTextLength()));
        StringBuilder standard = new StringBuilder();
        StringBuilder custom = new StringBuilder();
        List<AccessibilityNodeInfo.AccessibilityAction> actions = n.getActionList();
        if (actions != null) {
            for (AccessibilityNodeInfo.AccessibilityAction x : actions) {
                String name = ActionNames.BY_ID.get(x.getId());
                if (name != null) {
                    if (standard.length() > 0) standard.append(',');
                    standard.append(name);
                } else {
                    // A custom action: its id and label. The label's own line breaks become
                    // spaces, since a newline separates the actions.
                    if (custom.length() > 0) custom.append('\n');
                    CharSequence label = x.getLabel();
                    custom.append(x.getId()).append(':');
                    if (label != null) custom.append(label.toString().replace('\n', ' ').replace('\r', ' '));
                }
            }
        }
        extra(sb, "actions", standard);
        extra(sb, "custom-actions", custom);
        AccessibilityNodeInfo.CollectionInfo collection = n.getCollectionInfo();
        if (collection != null) {
            extra(sb, "collection-rows", String.valueOf(collection.getRowCount()));
            extra(sb, "collection-columns", String.valueOf(collection.getColumnCount()));
            extraFlag(sb, "collection-hierarchical", collection.isHierarchical());
            if (collection.getSelectionMode() != 0) extra(sb, "collection-selection-mode", String.valueOf(collection.getSelectionMode()));
        }
        AccessibilityNodeInfo.CollectionItemInfo item = n.getCollectionItemInfo();
        if (item != null) {
            extra(sb, "collection-item-row", String.valueOf(item.getRowIndex()));
            extra(sb, "collection-item-column", String.valueOf(item.getColumnIndex()));
            extra(sb, "collection-item-row-span", String.valueOf(item.getRowSpan()));
            extra(sb, "collection-item-column-span", String.valueOf(item.getColumnSpan()));
            extraFlag(sb, "collection-item-heading", item.isHeading());
            extraFlag(sb, "collection-item-selected", item.isSelected());
        }
        AccessibilityNodeInfo.RangeInfo range = n.getRangeInfo();
        if (range != null) {
            extra(sb, "range-type", String.valueOf(range.getType()));
            extra(sb, "range-min", String.valueOf(range.getMin()));
            extra(sb, "range-max", String.valueOf(range.getMax()));
            extra(sb, "range-current", String.valueOf(range.getCurrent()));
        }
        extra(sb, "text-links", textLinks(n.getText()));
        extraFlag(sb, "dismissable", n.isDismissable());
        extraFlag(sb, "multi-line", n.isMultiLine());
        extraFlag(sb, "content-invalid", n.isContentInvalid());
        extraFlag(sb, "context-clickable", n.isContextClickable());
        if (!n.isImportantForAccessibility()) flag(sb, "important", false);
        boolean open = false;
        int count = n.getChildCount();
        for (int i = 0; i < count; i++) {
            if (bound.hit || bound.over) return;
            // getChild may itself block on a frozen app: check the bound before each query.
            if (SystemClock.elapsedRealtime() > deadlineMs) {
                bound.hit = true;
                return;
            }
            if (sb.length() > FULL_DOC_CAP_CHARS) {
                bound.over = true;
                return;
            }
            AccessibilityNodeInfo c = n.getChild(i);
            if (c == null || (!invisible && !c.isVisibleToUser())) continue;
            if (!open) {
                sb.append(">\n");
                open = true;
            }
            fullNode(c, i, display, sb, deadlineMs, bound, invisible);
        }
        sb.append(open ? "</node>\n" : " />\n");
    }

    // The URL spans of a node's text as `start-end=url`, space-separated, the URL's spaces as
    // %20 (ADR-0110 Decision 3); null when there are none. Any other kind of span is not
    // listed.
    private static String textLinks(CharSequence text) {
        if (!(text instanceof Spanned)) return null;
        Spanned spanned = (Spanned) text;
        URLSpan[] spans = spanned.getSpans(0, spanned.length(), URLSpan.class);
        StringBuilder links = new StringBuilder();
        for (URLSpan span : spans) {
            String url = span.getURL();
            if (url == null) continue;
            if (links.length() > 0) links.append(' ');
            links.append(spanned.getSpanStart(span)).append('-').append(spanned.getSpanEnd(span))
                    .append('=').append(url.replace(" ", "%20"));
        }
        return links.length() == 0 ? null : links.toString();
    }

    // The names of the platform's standard actions by id, from the public constants of
    // AccessibilityAction (ACTION_SCROLL_FORWARD is "scroll_forward"), so a platform newer than
    // the android.jar this was built against names an action it knows and this does not as a
    // custom one. Loaded on first use, by the first full dump.
    private static final class ActionNames {
        static final Map<Integer, String> BY_ID = build();

        private static Map<Integer, String> build() {
            Map<Integer, String> names = new HashMap<Integer, String>();
            for (Field f : AccessibilityNodeInfo.AccessibilityAction.class.getFields()) {
                if (!Modifier.isStatic(f.getModifiers()) || f.getType() != AccessibilityNodeInfo.AccessibilityAction.class) continue;
                if (!f.getName().startsWith("ACTION_")) continue;
                try {
                    AccessibilityNodeInfo.AccessibilityAction a = (AccessibilityNodeInfo.AccessibilityAction) f.get(null);
                    if (a != null && !names.containsKey(a.getId())) {
                        names.put(a.getId(), f.getName().substring("ACTION_".length()).toLowerCase(Locale.ROOT));
                    }
                } catch (IllegalAccessException ignored) {
                    // A constant that cannot be read is not named; its action reads as custom.
                }
            }
            return names;
        }
    }

    // An extra attribute, written only when it has a value.
    private static void extra(StringBuilder sb, String name, CharSequence v) {
        if (v == null || v.length() == 0) return;
        attr(sb, name, v);
    }

    // An extra flag, written only when it is true.
    private static void extraFlag(StringBuilder sb, String name, boolean v) {
        if (v) flag(sb, name, true);
    }

    private static void flag(StringBuilder sb, String name, boolean v) {
        sb.append(' ').append(name).append("=\"").append(v).append('"');
    }

    private static void attr(StringBuilder sb, String name, CharSequence v) {
        sb.append(' ').append(name).append("=\"");
        if (v != null) {
            for (int i = 0; i < v.length(); i++) {
                char ch = v.charAt(i);
                switch (ch) {
                    case '&': sb.append("&amp;"); break;
                    case '<': sb.append("&lt;"); break;
                    case '>': sb.append("&gt;"); break;
                    case '"': sb.append("&quot;"); break;
                    case '\n': sb.append("&#10;"); break;
                    case '\r': sb.append("&#13;"); break;
                    case '\t': sb.append("&#9;"); break;
                    default:
                        if ((ch >= 0x1 && ch <= 0x8) || (ch >= 0xB && ch <= 0xC) || (ch >= 0xE && ch <= 0x1F)
                                || (ch >= 0x7F && ch <= 0x84) || (ch >= 0x86 && ch <= 0x9F) || (ch >= 0xFDD0 && ch <= 0xFDDF)) {
                            // As AccessibilityNodeInfoDumper.stripInvalidXMLChars: these become ".".
                            sb.append('.');
                        } else if (Character.isHighSurrogate(ch) && i + 1 < v.length() && Character.isLowSurrogate(v.charAt(i + 1))) {
                            sb.append(ch).append(v.charAt(++i));
                        } else if (ch >= 0x20 && ch != 0xFFFE && ch != 0xFFFF && !Character.isSurrogate(ch)) {
                            sb.append(ch);
                        } // else (NUL, U+FFFE, U+FFFF, a lone surrogate): AOSP lets them through to its serializer; XML 1.0 forbids them, so they are dropped
                }
            }
        }
        sb.append('"');
    }
}
