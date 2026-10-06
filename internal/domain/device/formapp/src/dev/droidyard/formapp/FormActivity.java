package dev.droidyard.formapp;

import android.app.Activity;
import android.os.Bundle;
import android.view.View;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

/**
 * The 1f fixture app's only activity, building its views in code (D14) so the whole
 * fixture is this one file plus ids.xml. Its views are the anchors of the milestone's
 * level-5 checks:
 *
 * <ul>
 *   <li>{@code name} EditText, hint "Name" — device.inputText's target;</li>
 *   <li>{@code greet} Button setting {@code greeting} to "Hello, {@code <name>}" —
 *       tapElement's result assertion;</li>
 *   <li>{@code like} Button and {@code likes} TextView ("Likes: N") — a tap whose
 *       effect a later read sees;</li>
 *   <li>{@code banner} TextView removed by {@code dismiss} Button — the {@code gone}
 *       expectation;</li>
 *   <li>two Buttons labelled "Twin" — DEVICE_ELEMENT_AMBIGUOUS;</li>
 *   <li>{@code disabled} Button, not enabled — DEVICE_ELEMENT_DISABLED.</li>
 * </ul>
 *
 * The layout is deliberately compact: the reader drops nodes not visible to the user,
 * and the check needs every one of them. For the same reason there is no scrolling.
 */
public class FormActivity extends Activity {
    private int likes = 0;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        final int pad = (int) (12 * getResources().getDisplayMetrics().density);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        // targetSdk 36 draws edge to edge: without this the form starts under the status
        // bar and its top fields cannot be tapped (plan m1f D22). No action bar either.
        root.setFitsSystemWindows(true);
        root.setPadding(pad, pad, pad, pad);

        final EditText name = new EditText(this);
        name.setId(R.id.name);
        name.setHint("Name");
        root.addView(name);

        final TextView greeting = new TextView(this);
        greeting.setId(R.id.greeting);
        root.addView(greeting);

        Button greet = new Button(this);

        greet.setAllCaps(false); // the reader sees the text as written, not the theme's capitals
        greet.setId(R.id.greet);
        greet.setText("Greet");
        greet.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                greeting.setText("Hello, " + name.getText());
            }
        });
        root.addView(greet);

        final TextView likesView = new TextView(this);
        likesView.setId(R.id.likes);
        likesView.setText("Likes: 0");
        root.addView(likesView);

        Button like = new Button(this);

        like.setAllCaps(false); // the reader sees the text as written, not the theme's capitals
        like.setId(R.id.like);
        like.setText("Like");
        like.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                likes++;
                likesView.setText("Likes: " + likes);
            }
        });
        root.addView(like);

        final TextView banner = new TextView(this);
        banner.setId(R.id.banner);
        banner.setText("A banner");
        root.addView(banner);

        Button dismiss = new Button(this);

        dismiss.setAllCaps(false); // the reader sees the text as written, not the theme's capitals
        dismiss.setId(R.id.dismiss);
        dismiss.setText("Dismiss");
        dismiss.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                // Gone, not INVISIBLE: the reader drops nodes not visible to the user,
                // so the next read no longer contains the banner (the `gone` check).
                banner.setVisibility(View.GONE);
            }
        });
        root.addView(dismiss);

        // Two buttons with the same label and no id: an ambiguous text selector.
        for (int i = 0; i < 2; i++) {
            Button twin = new Button(this);
            twin.setAllCaps(false); // the reader sees the text as written, not the theme's capitals
            twin.setText("Twin");
            root.addView(twin);
        }

        Button disabled = new Button(this);

        disabled.setAllCaps(false); // the reader sees the text as written, not the theme's capitals
        disabled.setId(R.id.disabled);
        disabled.setText("Disabled");
        disabled.setEnabled(false);
        root.addView(disabled);

        setContentView(root);
    }
}
