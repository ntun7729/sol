package bond.huggy.sol;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.content.pm.ServiceInfo;
import android.net.VpnService;
import android.os.Build;
import android.os.ParcelFileDescriptor;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;

public class TProxyService extends VpnService {
    public static final String ACTION_CONNECT = "bond.huggy.sol.CONNECT";
    public static final String ACTION_DISCONNECT = "bond.huggy.sol.DISCONNECT";
    public static final String EXTRA_SERVER_URL = "server_url";
    public static final String EXTRA_TOKEN = "token";

    private static final String CHANNEL_ID = "sol_vpn";
    private static final int NOTIFICATION_ID = 1001;
    private static final String MAPPED_DNS = "198.18.0.2";

    private static native boolean TProxyStartService(String configPath, int fd);
    private static native boolean TProxyStopService();
    private static native boolean TProxyIsRunning();

    static {
        System.loadLibrary("hev-socks5-tunnel");
    }

    private ParcelFileDescriptor tunFd;

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent == null) {
            return START_NOT_STICKY;
        }
        if (ACTION_DISCONNECT.equals(intent.getAction())) {
            stopTunnel();
            return START_NOT_STICKY;
        }
        if (!ACTION_CONNECT.equals(intent.getAction())) {
            return START_NOT_STICKY;
        }

        String serverUrl = intent.getStringExtra(EXTRA_SERVER_URL);
        String token = intent.getStringExtra(EXTRA_TOKEN);
        if (serverUrl == null || token == null) {
            stopSelf();
            return START_NOT_STICKY;
        }

        startForegroundNow();
        if (!startTunnel(serverUrl.trim(), token.trim())) {
            stopTunnel();
            return START_NOT_STICKY;
        }
        return START_STICKY;
    }

    @Override
    public void onRevoke() {
        stopTunnel();
        super.onRevoke();
    }

    @Override
    public void onDestroy() {
        stopTunnel();
        super.onDestroy();
    }

    private boolean startTunnel(String serverUrl, String token) {
        if (tunFd != null && TProxyIsRunning() && SolCore.nativeIsRunning()) {
            return true;
        }

        int solResult = SolCore.nativeStart(serverUrl, token);
        if (solResult != 0) {
            return false;
        }

        Builder builder = new Builder()
                .setSession("SOL VPN")
                .setMtu(1500)
                .addAddress("198.18.0.1", 32)
                .addRoute("0.0.0.0", 0)
                .addDnsServer(MAPPED_DNS)
                .setBlocking(false);

        // The SOL WebSocket and local SOCKS transport are created by this app.
        // Excluding our own package prevents the transport from being captured
        // by its own VPN and avoids a routing loop.
        try {
            builder.addDisallowedApplication(getPackageName());
        } catch (PackageManager.NameNotFoundException ignored) {
            SolCore.nativeStop();
            return false;
        }

        tunFd = builder.establish();
        if (tunFd == null) {
            SolCore.nativeStop();
            return false;
        }

        File config = new File(getCacheDir(), "sol-tun.yml");
        String yaml = "misc:\n"
                + "  task-stack-size: 86016\n"
                + "  connect-timeout: 30000\n"
                + "  log-level: warn\n"
                + "tunnel:\n"
                + "  mtu: 1500\n"
                + "  icmp: 'reply'\n"
                + "socks5:\n"
                + "  port: 1080\n"
                + "  address: '127.0.0.1'\n"
                + "  udp: 'udp'\n"
                + "mapdns:\n"
                + "  address: " + MAPPED_DNS + "\n"
                + "  port: 53\n"
                + "  network: 240.0.0.0\n"
                + "  netmask: 240.0.0.0\n"
                + "  cache-size: 10000\n";

        try (FileOutputStream out = new FileOutputStream(config, false)) {
            out.write(yaml.getBytes(StandardCharsets.UTF_8));
        } catch (IOException e) {
            closeTun();
            SolCore.nativeStop();
            return false;
        }

        if (!TProxyStartService(config.getAbsolutePath(), tunFd.getFd())) {
            closeTun();
            SolCore.nativeStop();
            return false;
        }
        return true;
    }

    private void stopTunnel() {
        if (TProxyIsRunning()) {
            TProxyStopService();
        }
        closeTun();
        SolCore.nativeStop();
        stopForeground(true);
        stopSelf();
    }

    private void closeTun() {
        if (tunFd == null) {
            return;
        }
        try {
            tunFd.close();
        } catch (IOException ignored) {
        }
        tunFd = null;
    }

    private void startForegroundNow() {
        NotificationManager manager = getSystemService(NotificationManager.class);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            NotificationChannel channel = new NotificationChannel(
                    CHANNEL_ID,
                    "SOL VPN",
                    NotificationManager.IMPORTANCE_LOW);
            manager.createNotificationChannel(channel);
        }

        Intent openIntent = new Intent(this, MainActivity.class);
        openIntent.setFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP | Intent.FLAG_ACTIVITY_SINGLE_TOP);
        PendingIntent pendingIntent = PendingIntent.getActivity(
                this,
                0,
                openIntent,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);

        Notification.Builder builder = Build.VERSION.SDK_INT >= Build.VERSION_CODES.O
                ? new Notification.Builder(this, CHANNEL_ID)
                : new Notification.Builder(this);
        Notification notification = builder
                .setContentTitle("SOL VPN")
                .setContentText("Connected through SOL")
                .setSmallIcon(android.R.drawable.stat_sys_upload_done)
                .setContentIntent(pendingIntent)
                .setOngoing(true)
                .build();

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(
                    NOTIFICATION_ID,
                    notification,
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);
        } else {
            startForeground(NOTIFICATION_ID, notification);
        }
    }
}
