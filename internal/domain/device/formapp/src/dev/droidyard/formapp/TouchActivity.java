package dev.droidyard.formapp;

import android.app.Activity;
import android.graphics.Color;
import android.os.Bundle;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewGroup;

/**
 * Milestone 2y's touch screen (step 1): a screen that changes only on touch, so the first
 * video bytes after an input are caused by that input. Started explicitly with
 * {@code am start -n dev.droidyard.formapp/.TouchActivity}. One full-window view, system
 * bars hidden, no text and no focusable field.
 *
 * <ul>
 *   <li>{@code ACTION_DOWN} and {@code ACTION_MOVE}: {@code rgb(round(255*x/w),
 *       round(255*y/h), 255)}, so the colour says where the finger is;
 *   <li>{@code ACTION_UP}: black;
 *   <li>{@code ACTION_CANCEL}: {@code rgb(255, 0, 255)};
 *   <li>a scroll event ({@code ACTION_SCROLL}): {@code rgb(0, 255, 128 + 16*notches)},
 *       clamped, where notches is the running sum of the vertical scroll values since
 *       the activity started (a wheel tick up is positive), so each tick changes the
 *       colour.
 * </ul>
 *
 * <p>Nothing else draws: an idle screen sends the encoder nothing new.
 */
public class TouchActivity extends Activity {
    private float notches = 0;

    private final class Pad extends View {
        Pad(Activity a) {
            super(a);
            setBackgroundColor(Color.BLACK);
            setFocusable(false);
        }

        @Override
        public boolean onTouchEvent(MotionEvent e) {
            switch (e.getActionMasked()) {
                case MotionEvent.ACTION_DOWN:
                case MotionEvent.ACTION_MOVE:
                    int w = Math.max(getWidth(), 1);
                    int h = Math.max(getHeight(), 1);
                    setBackgroundColor(Color.rgb(
                            clamp(Math.round(255f * e.getX() / w)),
                            clamp(Math.round(255f * e.getY() / h)),
                            255));
                    break;
                case MotionEvent.ACTION_UP:
                    setBackgroundColor(Color.BLACK);
                    break;
                case MotionEvent.ACTION_CANCEL:
                    setBackgroundColor(Color.rgb(255, 0, 255));
                    break;
                default:
                    return true;
            }
            return true;
        }

        @Override
        public boolean onGenericMotionEvent(MotionEvent e) {
            if (e.getActionMasked() != MotionEvent.ACTION_SCROLL) {
                return super.onGenericMotionEvent(e);
            }
            notches += e.getAxisValue(MotionEvent.AXIS_VSCROLL);
            setBackgroundColor(Color.rgb(0, 255, clamp(Math.round(128 + 16 * notches))));
            return true;
        }
    }

    private static int clamp(int v) {
        return Math.max(0, Math.min(255, v));
    }

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        Pad pad = new Pad(this);
        pad.setLayoutParams(new ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        setContentView(pad);
    }

    @Override
    public void onWindowFocusChanged(boolean hasFocus) {
        super.onWindowFocusChanged(hasFocus);
        if (hasFocus) {
            // Immersive: the bars hidden, so the view is the whole window and its x/y
            // map onto the colour the same on every device.
            getWindow().getDecorView().setSystemUiVisibility(
                    View.SYSTEM_UI_FLAG_LAYOUT_STABLE
                            | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION
                            | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
                            | View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                            | View.SYSTEM_UI_FLAG_FULLSCREEN
                            | View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY);
        }
    }
}
