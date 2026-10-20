package dev.droidyard.formapp;

import android.app.Activity;
import android.app.AlertDialog;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.text.InputFilter;
import android.text.InputType;
import android.text.SpannableString;
import android.text.Spanned;
import android.text.style.URLSpan;
import android.view.View;
import android.view.ViewGroup;
import android.view.accessibility.AccessibilityNodeInfo;
import android.widget.BaseAdapter;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ListView;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.SeekBar;
import android.widget.Switch;
import android.widget.TextView;
import android.widget.Toast;

/**
 * Milestone 2w's widget screen (step 1): what the spike reads to settle the UI dump's
 * format and the event stream. Started explicitly with
 * {@code am start -n dev.droidyard.formapp/.WidgetsActivity}; {@code FormActivity} is
 * unchanged. Views are built in code, as in FormActivity, and each carries the id the
 * level-5 runs select on:
 *
 * <ul>
 *   <li>{@code toast} Button showing the toast "Saved n-tilde check mark" (written with
 *       Unicode escapes: U+00F1 and U+2713);</li>
 *   <li>{@code notify} Button posting a notification "Code 123456" on its own channel
 *       (POST_NOTIFICATIONS is declared; the grant is {@code pm grant});</li>
 *   <li>{@code list} ListView of 50 rows, the third with a custom accessibility action
 *       "Archive";</li>
 *   <li>{@code level} SeekBar 0-10 at 3, {@code wifi} Switch;</li>
 *   <li>{@code title} TextView marked a heading inside a container with a pane title;</li>
 *   <li>{@code help} Button with a tooltip;</li>
 *   <li>{@code email} EditText, email input, at most 12 characters, with an error set;</li>
 *   <li>{@code link} TextView with a URL span (the URL has a space);</li>
 *   <li>{@code hidden} INVISIBLE TextView;</li>
 *   <li>{@code dialog} Button opening an AlertDialog, a second window;</li>
 *   <li>{@code spinner} Button toggling an indeterminate ProgressBar and a text updated
 *       every 20 ms: a content-change storm.</li>
 * </ul>
 */
public class WidgetsActivity extends Activity {
    private static final String CHANNEL = "droidyard_widgets";

    private final Handler handler = new Handler(Looper.getMainLooper());
    private ProgressBar progress;
    private TextView ticker;
    private LinearLayout stormBox;
    private int ticks;
    private final Runnable tick = new Runnable() {
        @Override
        public void run() {
            ticks++;
            ticker.setText("Tick " + ticks);
            handler.postDelayed(this, 20);
        }
    };

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        final int pad = (int) (8 * getResources().getDisplayMetrics().density);
        final int listHeight = (int) (180 * getResources().getDisplayMetrics().density);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        // targetSdk 36 draws edge to edge (plan m1f D22).
        root.setFitsSystemWindows(true);
        root.setPadding(pad, pad, pad, pad);

        // A container with a pane title, and a heading inside it.
        LinearLayout pane = new LinearLayout(this);
        pane.setOrientation(LinearLayout.VERTICAL);
        if (Build.VERSION.SDK_INT >= 28) {
            pane.setAccessibilityPaneTitle("Widgets pane");
        }
        TextView title = new TextView(this);
        title.setId(R.id.title);
        title.setText("Widgets");
        title.setTextSize(20);
        if (Build.VERSION.SDK_INT >= 28) {
            title.setAccessibilityHeading(true);
        }
        pane.addView(title);
        root.addView(pane);

        Button toast = button("Toast", R.id.toast);
        toast.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                // Escapes, not literals: the file's encoding is not the compiler's business.
                Toast.makeText(WidgetsActivity.this, "Saved ñ ✓", Toast.LENGTH_SHORT).show();
            }
        });
        root.addView(toast);

        Button notify = button("Notify", R.id.notify);
        notify.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                postNotification();
            }
        });
        root.addView(notify);

        Button help = button("Help", R.id.help);
        if (Build.VERSION.SDK_INT >= 26) {
            help.setTooltipText("Help tooltip");
        }
        root.addView(help);

        Button dialog = button("Dialog", R.id.dialog);
        dialog.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                new AlertDialog.Builder(WidgetsActivity.this)
                        .setTitle("Dialog title")
                        .setMessage("A second window")
                        .setPositiveButton("OK", null)
                        .show();
            }
        });
        root.addView(dialog);

        Button spinner = button("Spinner", R.id.spinner);
        spinner.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                if (stormBox.getVisibility() == View.VISIBLE) {
                    stopStorm();
                } else {
                    stormBox.setVisibility(View.VISIBLE);
                    handler.post(tick);
                }
            }
        });
        root.addView(spinner);

        stormBox = new LinearLayout(this);
        stormBox.setOrientation(LinearLayout.HORIZONTAL);
        stormBox.setVisibility(View.GONE);
        progress = new ProgressBar(this);
        progress.setIndeterminate(true);
        stormBox.addView(progress);
        ticker = new TextView(this);
        ticker.setText("Tick 0");
        stormBox.addView(ticker);
        root.addView(stormBox);

        SeekBar level = new SeekBar(this);
        level.setId(R.id.level);
        level.setMax(10);
        level.setProgress(3);
        root.addView(level);

        Switch wifi = new Switch(this);
        wifi.setId(R.id.wifi);
        wifi.setText("Wi-Fi");
        root.addView(wifi);

        final EditText email = new EditText(this);
        email.setId(R.id.email);
        email.setHint("Email");
        email.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_EMAIL_ADDRESS);
        email.setFilters(new InputFilter[] {new InputFilter.LengthFilter(12)});
        email.setError("Not an email");
        root.addView(email);

        TextView link = new TextView(this);
        link.setId(R.id.link);
        SpannableString text = new SpannableString("See the docs page");
        // A space in the URL: the dump's text-links escapes it as %20.
        text.setSpan(new URLSpan("https://example.com/a b"), 8, 17, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        link.setText(text);
        root.addView(link);

        TextView hidden = new TextView(this);
        hidden.setId(R.id.hidden);
        hidden.setText("Hidden text");
        hidden.setVisibility(View.INVISIBLE);
        root.addView(hidden);

        ListView list = new ListView(this);
        list.setId(R.id.list);
        list.setAdapter(new RowAdapter());
        root.addView(list, new LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, listHeight));

        ScrollView scroll = new ScrollView(this);
        scroll.addView(root);
        setContentView(scroll);
    }

    @Override
    protected void onPause() {
        super.onPause();
        stopStorm();
    }

    private void stopStorm() {
        handler.removeCallbacks(tick);
        if (stormBox != null) {
            stormBox.setVisibility(View.GONE);
        }
    }

    private Button button(String label, int id) {
        Button b = new Button(this);
        b.setAllCaps(false); // the reader sees the text as written, not the theme's capitals
        b.setId(id);
        b.setText(label);
        return b;
    }

    private void postNotification() {
        NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
        Notification.Builder builder;
        if (Build.VERSION.SDK_INT >= 26) {
            // IMPORTANCE_HIGH so the notification shows as a heads-up first.
            nm.createNotificationChannel(new NotificationChannel(CHANNEL, "Widgets", NotificationManager.IMPORTANCE_HIGH));
            builder = new Notification.Builder(this, CHANNEL);
        } else {
            builder = new Notification.Builder(this);
        }
        builder.setSmallIcon(android.R.drawable.ic_dialog_info)
                .setContentTitle("Droidyard")
                .setContentText("Code 123456");
        nm.notify(1, builder.build());
    }

    /** Fifty rows; the third carries the custom accessibility action "Archive". */
    private final class RowAdapter extends BaseAdapter {
        @Override
        public int getCount() {
            return 50;
        }

        @Override
        public Object getItem(int position) {
            return "Row " + position;
        }

        @Override
        public long getItemId(int position) {
            return position;
        }

        @Override
        public View getView(int position, View convert, ViewGroup parent) {
            TextView row = convert instanceof TextView ? (TextView) convert : new TextView(WidgetsActivity.this);
            row.setText("Row " + position);
            row.setPadding(16, 16, 16, 16);
            if (position == 2) {
                row.setAccessibilityDelegate(new View.AccessibilityDelegate() {
                    @Override
                    public void onInitializeAccessibilityNodeInfo(View host, AccessibilityNodeInfo info) {
                        super.onInitializeAccessibilityNodeInfo(host, info);
                        info.addAction(new AccessibilityNodeInfo.AccessibilityAction(R.id.list, "Archive"));
                    }
                });
            } else {
                row.setAccessibilityDelegate(null);
            }
            return row;
        }
    }
}
