package dev.shellscaled.webuitest;

import android.app.Activity;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.os.SystemClock;
import android.util.Log;
import android.widget.TextView;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;

/** Explicit foreground-only clipboard assertion. Never writes, displays or logs clipboard contents. */
public final class ClipboardProbeActivity extends Activity {
    private final Handler handler = new Handler(Looper.getMainLooper());
    private byte[] expected;
    private long started;
    private boolean completed;
    private TextView result;

    @Override public void onCreate(Bundle saved) {
        super.onCreate(saved);
        result = new TextView(this);
        result.setText("Checking clipboard…");
        result.setTextSize(20);
        result.setPadding(32, 48, 32, 32);
        setContentView(result);
        String hex = getIntent().getStringExtra("expectedSha256");
        if (hex == null || !hex.matches("[0-9a-fA-F]{64}")) {
            finishProbe(false);
            return;
        }
        expected = new byte[32];
        for (int i = 0; i < expected.length; i++) expected[i] = (byte) Integer.parseInt(hex.substring(i * 2, i * 2 + 2), 16);
    }

    @Override public void onWindowFocusChanged(boolean focused) {
        super.onWindowFocusChanged(focused);
        if (focused && expected != null && !completed && started == 0) {
            started = SystemClock.elapsedRealtime();
            handler.postDelayed(this::checkClipboard, 100);
        }
    }

    private void checkClipboard() {
        if (completed || isFinishing()) return;
        boolean matches = false;
        if (hasWindowFocus()) {
            try {
                ClipboardManager clipboard = (ClipboardManager) getSystemService(CLIPBOARD_SERVICE);
                ClipData clip = clipboard == null ? null : clipboard.getPrimaryClip();
                // Do not coerce URI/Intent items, which could access external providers.
                CharSequence text = clip != null && clip.getItemCount() > 0 ? clip.getItemAt(0).getText() : null;
                if (text != null) {
                    byte[] actual = MessageDigest.getInstance("SHA-256").digest(text.toString().getBytes(StandardCharsets.UTF_8));
                    matches = MessageDigest.isEqual(expected, actual);
                }
            } catch (SecurityException | NoSuchAlgorithmException ignored) {
                // Retry briefly after foreground acquisition, then report only the anonymous result.
            }
        }
        if (matches || SystemClock.elapsedRealtime() - started >= 1000) finishProbe(matches);
        else handler.postDelayed(this::checkClipboard, 100);
    }

    private void finishProbe(boolean matches) {
        if (completed) return;
        completed = true;
        handler.removeCallbacksAndMessages(null);
        expected = null;
        result.setText(matches ? "Clipboard match passed" : "Clipboard match failed");
        Log.i("ShellscaledUiTest", (matches ? "PASS" : "FAIL") + " clipboard-match");
    }

    @Override public void onDestroy() {
        handler.removeCallbacksAndMessages(null);
        expected = null;
        super.onDestroy();
    }
}
