import android.app.UiAutomation;
import android.graphics.Point;
import android.graphics.Rect;
import android.os.Build;
import android.os.HandlerThread;
import android.os.Looper;
import android.view.accessibility.AccessibilityNodeInfo;
import java.io.PrintStream;
import java.lang.reflect.Constructor;
import java.lang.reflect.Method;

/**
 * Droidyard's one-shot UI reader (ADR-0029). Run with app_process as the shell user:
 * it connects UiAutomation with FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES, so the
 * device's other accessibility services keep running, writes the active window's tree
 * to stdout in the XML shape `uiautomator dump` produces, disconnects and exits.
 * Failures go to stderr with a non-zero status. Source of reader.dex: see build.sh.
 */
public class DroidyardReader {
    public static void main(String[] args) {
        int status = 1;
        try {
            if (Build.VERSION.SDK_INT < 24) {
                throw new IllegalStateException("API " + Build.VERSION.SDK_INT + " lacks FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES (API 24)");
            }
            status = read();
        } catch (Throwable t) {
            Throwable c = t instanceof java.lang.reflect.InvocationTargetException && t.getCause() != null ? t.getCause() : t;
            System.err.println("droidyard-reader: " + c);
        }
        System.out.flush();
        System.exit(status);
    }

    // UiAutomation's connection constructor and connect(int) are hidden API, reached by
    // reflection as the platform's own uiautomator reaches them.
    private static int read() throws Exception {
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
        String xml;
        try {
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
            Point size = new Point();
            int rotation = display(size);
            StringBuilder sb = new StringBuilder(16 << 10);
            sb.append("<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation=\"").append(rotation).append("\">");
            if (root != null) node(root, 0, new Rect(0, 0, size.x, size.y), sb);
            sb.append("</hierarchy>");
            xml = sb.toString();
        } finally {
            disconnect.invoke(ua);
            thread.quitSafely();
        }
        PrintStream out = new PrintStream(System.out, false, "UTF-8");
        out.print(xml);
        out.flush();
        return 0;
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
                        if (Character.isHighSurrogate(ch) && i + 1 < v.length() && Character.isLowSurrogate(v.charAt(i + 1))) {
                            sb.append(ch).append(v.charAt(++i));
                        } else if (ch >= 0x20 && ch != 0xFFFE && ch != 0xFFFF && !Character.isSurrogate(ch)) {
                            sb.append(ch);
                        } // else: not valid in XML 1.0, dropped as uiautomator drops it
                }
            }
        }
        sb.append('"');
    }
}
