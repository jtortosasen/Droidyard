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
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.io.PrintStream;
import java.lang.reflect.Constructor;
import java.lang.reflect.Method;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

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
 */
public class DroidyardReader {
    // Exit status when getRootInActiveWindow stays null; the daemon maps it (dump.go, readerNoRoot).
    private static final int NO_ROOT = 3;
    // Marks the reader's own lines, on stderr in one-shot mode and on the socket in serve mode.
    private static final String PREFIX = "droidyard-reader:";

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

    // The one-shot mode of ADR-0029: connect, read once, disconnect, exit.
    private static int read() throws Exception {
        Automation a = connect();
        String xml;
        try {
            xml = document(a.ua);
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
                    // A stalled client must not hold the single-threaded loop: without a
                    // read timeout one connection that never sends a full request (or
                    // never reads a dump's response) blocks accept() forever, so later
                    // clients, quit and the idle exit never run and UiAutomation stays
                    // held. The timeout fails that read into the catch below, and the
                    // loop goes on.
                    conn.setSoTimeout(5000);
                    // A wake() connection, not a request: the idle timer expired.
                    if (done.get()) {
                        break;
                    }
                    handle(conn, a.ua, lastDump, done);
                } catch (Throwable t) {
                    try {
                        write(conn, PREFIX + " " + t);
                    } catch (IOException ignored) {
                        // The connection is gone; nothing to report it to.
                    }
                } finally {
                    try {
                        conn.close();
                    } catch (IOException ignored) {
                        // Already closed.
                    }
                }
            }
        } catch (IOException e) {
            if (!done.get()) {
                System.err.println(PREFIX + " accept: " + e);
            }
        } finally {
            done.set(true);
            try {
                server.close();
            } catch (IOException ignored) {
                // Already closed.
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

    private static void handle(LocalSocket conn, UiAutomation ua, AtomicLong lastDump, AtomicBoolean done) throws Exception {
        String request = readRequest(conn.getInputStream());
        if (request.equals("ping")) {
            write(conn, "ok " + android.os.Process.myPid());
        } else if (request.equals("dump")) {
            lastDump.set(SystemClock.elapsedRealtime());
            String xml = document(ua);
            if (xml == null) {
                write(conn, PREFIX + " no active window root");
            } else {
                write(conn, xml);
            }
        } else if (request.equals("quit")) {
            done.set(true);
        } else {
            write(conn, PREFIX + " unknown request");
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

    // The active window's tree, as the XML the one-shot writes; null when there is no
    // root. waitForIdle's timeout is not fatal: the tree is read anyway.
    private static String document(UiAutomation ua) throws Exception {
        try {
            ua.waitForIdle(100, 500);
        } catch (Exception e) {
            // Not idle in time (a TimeoutException): read the tree anyway.
        }
        AccessibilityNodeInfo root = null;
        for (int i = 0; i < 5 && root == null; i++) {
            root = ua.getRootInActiveWindow();
            if (root == null) Thread.sleep(100);
        }
        if (root == null) {
            return null;
        }
        Point size = new Point();
        int rotation = display(size);
        StringBuilder sb = new StringBuilder(16 << 10);
        sb.append("<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation=\"").append(rotation).append("\">");
        node(root, 0, new Rect(0, 0, size.x, size.y), sb);
        sb.append("</hierarchy>");
        return sb.toString();
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

    private static void node(AccessibilityNodeInfo n, int index, Rect display, StringBuilder sb) {
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
            AccessibilityNodeInfo c = n.getChild(i);
            // As uiautomator does: a child that is not visible to the user is not dumped.
            if (c != null && c.isVisibleToUser()) node(c, i, display, sb);
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
