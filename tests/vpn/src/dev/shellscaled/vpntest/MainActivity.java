package dev.shellscaled.vpntest;

import android.app.Activity;
import android.content.Intent;
import android.net.VpnService;
import android.os.Bundle;

/** Emulator-only fixture. A real VpnService makes policy routing observable. */
public final class MainActivity extends Activity {
    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        if (getIntent().getBooleanExtra("stop", false)) {
            stopService(new Intent(this, TestVpn.class));
            finish();
            return;
        }
        Intent consent = VpnService.prepare(this);
        if (consent == null) startTest();
        else startActivityForResult(consent, 1);
    }
    @Override protected void onActivityResult(int request, int result, Intent data) {
        super.onActivityResult(request, result, data);
        if (request == 1 && result == RESULT_OK) startTest();
        else finish();
    }
    private void startTest() {
        Intent service = new Intent(this, TestVpn.class);
        service.putExtra("full", getIntent().getBooleanExtra("full", false));
        startForegroundService(service);
        finish();
    }
}
