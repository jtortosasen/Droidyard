package dev.droidyard.flavoured;

import android.app.Activity;
import android.os.Bundle;
import android.widget.TextView;

import dev.droidyard.flavoured.lib.Greeting;

/** Shows the variant's package name, so a level-5 run can read which one is running. */
public class MainActivity extends Activity {
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        TextView view = new TextView(this);
        view.setFitsSystemWindows(true);
        view.setText(getPackageName() + "\n" + Greeting.text());
        setContentView(view);
    }
}
