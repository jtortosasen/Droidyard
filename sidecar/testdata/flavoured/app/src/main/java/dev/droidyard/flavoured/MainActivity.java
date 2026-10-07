package dev.droidyard.flavoured;

import android.app.Activity;
import android.os.Bundle;
import android.util.Log;
import android.view.View;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

import dev.droidyard.flavoured.lib.Greeting;

/**
 * Shows the variant's package name, so a level-5 run can read which one is running, and
 * a Crash button for the logs milestone (plan m1i D3): it throws, so device.logs has a
 * Java crash of this app to find after the process died.
 */
public class MainActivity extends Activity {
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        Log.i("DroidyardFixture", "created " + getPackageName());
        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.VERTICAL);
        layout.setFitsSystemWindows(true);
        TextView view = new TextView(this);
        view.setText(getPackageName() + "\n" + Greeting.text());
        layout.addView(view);
        Button crash = new Button(this);
        crash.setText("Crash");
        crash.setAllCaps(false);
        crash.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                throw new IllegalStateException("droidyard fixture crash");
            }
        });
        layout.addView(crash);
        setContentView(layout);
    }
}
