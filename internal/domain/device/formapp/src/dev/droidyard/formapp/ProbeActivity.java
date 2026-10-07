package dev.droidyard.formapp;

import android.app.Activity;
import android.net.LocalSocket;
import android.net.LocalSocketAddress;
import android.os.Bundle;
import android.util.Log;
import android.widget.TextView;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;

/**
 * The fixture's socket probe (plan m1n step 2 (e), step 4's fence check).
 *
 * <p>Started explicitly with
 * {@code am start -n dev.droidyard.formapp/.ProbeActivity -e socket <name> -e request <line>},
 * it connects to the named abstract socket from this app's own uid and domain, sends the
 * request line, and shows the reply in a text view ({@code probe_result}). It exists so a
 * level-5 run can see whether an ordinary app can reach the reader's socket at all
 * (SELinux may refuse the connect) and what the reader answers a peer that is not adbd:
 * the fence of the reader's acting requests is the peer's uid, and this is the app whose
 * uid must get {@code denied}.
 *
 * <p>One request per connection, read to EOF, as DroidyardReader's serve loop answers: a
 * successful reply is the document (or {@code ok <pid>}), an error begins with
 * {@code droidyard-reader:}. A connect or write failure is shown as the reply, so the run
 * sees the refusal rather than an app that vanished.
 */
public class ProbeActivity extends Activity {
    // A reply is bounded: a dump's document is the largest one, and an unbounded read
    // would fill the view with whatever a socket chose to send.
    private static final int REPLY_CAP = 64 << 10;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        TextView view = new TextView(this);
        view.setId(R.id.probe_result);
        String text;
        try {
            text = probe(getIntent().getStringExtra("socket"), getIntent().getStringExtra("request"));
        } catch (Throwable t) {
            text = "probe failed: " + t;
        }
        // Also to logcat (tag droidyard-probe), so a run can read the reply without a
        // second UiAutomation reading the screen while a spike server holds the device's one.
        Log.i("droidyard-probe", text);
        view.setText(text);
        setContentView(view);
    }

    private static String probe(String socket, String request) throws Exception {
        if (socket == null || socket.isEmpty()) {
            return "probe failed: no socket extra";
        }
        LocalSocket s = new LocalSocket();
        try {
            s.connect(new LocalSocketAddress(socket, LocalSocketAddress.Namespace.ABSTRACT));
            OutputStream out = s.getOutputStream();
            out.write(((request == null ? "" : request) + "\n").getBytes(StandardCharsets.UTF_8));
            out.flush();
            ByteArrayOutputStream buf = new ByteArrayOutputStream(256);
            InputStream in = s.getInputStream();
            byte[] chunk = new byte[4096];
            int n;
            while (buf.size() < REPLY_CAP && (n = in.read(chunk)) != -1) {
                buf.write(chunk, 0, n);
            }
            return new String(buf.toByteArray(), StandardCharsets.UTF_8);
        } finally {
            try {
                s.close();
            } catch (Exception ignored) {
                // Already closed.
            }
        }
    }
}
