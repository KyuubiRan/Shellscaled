package dev.shellscaled.vpntest;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.content.Intent;
import android.net.VpnService;
import android.os.ParcelFileDescriptor;
import java.io.IOException;

/** Deliberately drops its routed traffic; this is not a production VPN client. */
public final class TestVpn extends VpnService {
    private ParcelFileDescriptor tunnel;
    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        NotificationManager manager = getSystemService(NotificationManager.class);
        manager.createNotificationChannel(new NotificationChannel("test", "Emulator network test", NotificationManager.IMPORTANCE_LOW));
        startForeground(1, new Notification.Builder(this, "test")
                .setSmallIcon(android.R.drawable.stat_sys_warning)
                .setContentTitle("Shellscaled emulator VPN fixture")
                .setContentText("Test traffic is intentionally dropped").build());
        closeTunnel();
        Builder builder = new Builder().setSession("Shellscaled VPN fixture").setMtu(1280)
                .addAddress("192.0.2.1", 32).addAddress("2001:db8::1", 128);
        if (intent != null && intent.getBooleanExtra("full", false)) {
            builder.addRoute("0.0.0.0", 0).addRoute("::", 0);
        } else {
            builder.addRoute("198.18.0.0", 15).addRoute("2001:db8:ffff::", 48);
        }
        tunnel = builder.establish();
        if (tunnel == null) stopSelf();
        return START_NOT_STICKY;
    }
    @Override public void onRevoke() { closeTunnel(); stopSelf(); }
    @Override public void onDestroy() { closeTunnel(); super.onDestroy(); }
    private void closeTunnel() {
        if (tunnel != null) {
            try { tunnel.close(); } catch (IOException ignored) { }
            tunnel = null;
        }
    }
}
