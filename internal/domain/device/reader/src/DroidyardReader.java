import android.app.UiAutomation;
import android.graphics.Point;
import android.graphics.Rect;
import android.net.LocalServerSocket;
import android.net.LocalSocket;
import android.net.LocalSocketAddress;
import android.os.Build;
import android.os.HandlerThread;
import android.os.Looper;
import android.os.SystemClock;
import android.view.accessibility.AccessibilityNodeInfo;
import java.io.ByteArrayOutputStream;
import java.io.Closeable;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.io.PrintStream;
import java.lang.reflect.Constructor;
import java.lang.reflect.Method;
import java.nio.charset.StandardCharsets;
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
 * Only a {@code dump} resets the idle timer; after idleSeconds without one it disconnects
 * UiAutomation and exits, so the device is left as it was found. A response is read to
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

    public static void main(String[] args) {
        int status = 1;
        try {
            if (Build.VERSION.SDK_INT < 24) {
                throw new IllegalStateException("API " + Build.VERSION.SDK_INT + " lacks FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES (API 24)");
            }
            if (args.length == 0) {
                status = read();
            } else if (args.length == 3 && args[0].equals("serve")) {
                status = serve(args[1], idleSeconds(args[2]));
            } else {
                System.err.println(PREFIX + " usage: DroidyardReader [serve <abstract-socket-name> <idleSeconds>]");
                status = 2;
            }
        } catch (Throwable t) {
            Throwable c = t instanceof java.lang.reflect.InvocationTargetException && t.getCause() != null ? t.getCause() : t;
            System.err.println(PREFIX + " " + c);
        }
        System.out.flush();
        System.exit(status);
    }

    private static int idleSeconds(String s) {
        int n = Integer.parseInt(s);
        if (n < 1) throw new IllegalArgumentException("idleSeconds must be positive: " + s);
        return n;
    }

    // The one-shot mode of ADR-0029: connect, read once, disconnect, exit. No device-side
    // bound: the one-shot has one job and the daemon bounds it (readerRun).
    private static int read() throws Exception {
        Automation a = connect();
        String xml;
        try {
            xml = document(a.ua, Long.MAX_VALUE, null).xml;
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
    // adb forward can reach it; the read itself is the one-shot's document(). Only a dump
    // resets the idle timer.
    private static int serve(String socketName, int idleSeconds) throws Exception {
        // Bind before connecting UiAutomation: a second start on the same name fails here,
        // at the name, without touching accessibility; and if the bind fails there is no
        // connection to release.
        final LocalServerSocket server = new LocalServerSocket(socketName);
        final Automation a;
        try {
            a = connect();
        } catch (Throwable t) {
            try {
                server.close();
            } catch (IOException ignored) {
                // Already closed.
            }
            throw t;
        }
        final AtomicBoolean done = new AtomicBoolean(false);
        // elapsedRealtime, not currentTimeMillis: a wall-clock change must not delay the
        // idle exit.
        final AtomicLong lastDump = new AtomicLong(SystemClock.elapsedRealtime());
        final long idleMs = idleSeconds * 1000L;
        // The dump workers and the timer that answers `busy` when one passes its bound.
        // Both use daemon threads, so the process can exit without waiting for a worker
        // still blocked on an app.
        final ExecutorService workers = Executors.newFixedThreadPool(DUMP_WORKERS, daemonThreads("droidyard-reader-dump"));
        final ScheduledExecutorService timer = Executors.newSingleThreadScheduledExecutor(daemonThreads("droidyard-reader-timer"));
        // How many dumps are out on a worker now: a dump that finds both out is answered
        // `busy` at once.
        final AtomicInteger out = new AtomicInteger(0);
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
                    String request = readRequest(conn.getInputStream());
                    if (request.equals("ping")) {
                        // Answered on the accept thread, so a blocked dump never blocks
                        // it (ADR-0034 §1): a reader that answers ping is never restarted.
                        write(conn, "ok " + android.os.Process.myPid());
                        closeQuietly(conn);
                    } else if (request.equals("quit")) {
                        done.set(true);
                        closeQuietly(conn);
                    } else if (request.equals("dump")) {
                        lastDump.set(SystemClock.elapsedRealtime());
                        // The connection is owned by the worker (or the bound's timer)
                        // now: whichever writes first closes it.
                        submitDump(conn, a.ua, workers, timer, out, lastPackage);
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
            timer.shutdownNow();
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
            final AtomicInteger out, final AtomicReference<String> lastPackage) {
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
                    Doc d = document(ua, start + DUMP_BOUND_MS, lastPackage);
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

    // One request per connection: read up to the first newline (or EOF, for a client that
    // half-closes), capped so a malformed client cannot make us buffer without bound.
    private static String readRequest(InputStream in) throws IOException {
        ByteArrayOutputStream buf = new ByteArrayOutputStream(16);
        int b;
        while ((b = in.read()) != -1 && b != '\n') {
            if (buf.size() >= 256) break;
            buf.write(b);
        }
        return new String(buf.toByteArray(), StandardCharsets.UTF_8).trim();
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
    }

    // The active window's tree, as the XML the one-shot writes; xml null when there is no
    // root, bound true when deadlineMs passed before the document was complete. The bound
    // is checked between node queries, so a traversal is abandoned at it; a single blocked
    // query cannot be interrupted and is bounded by the platform's 5 s interaction timeout
    // instead. waitForIdle's timeout is not fatal: the tree is read anyway. A root read
    // updates lastPackage (null when the caller does not want it).
    private static Doc document(UiAutomation ua, long deadlineMs, AtomicReference<String> lastPackage) throws Exception {
        try {
            ua.waitForIdle(100, 500);
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
        node(root, 0, new Rect(0, 0, size.x, size.y), sb, deadlineMs, bound);
        if (bound.hit) {
            // The document is abandoned: a partial tree is not a document.
            return new Doc(null, true, pkg);
        }
        sb.append("</hierarchy>");
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
