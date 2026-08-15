package bond.huggy.sol;

import android.Manifest;
import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.net.VpnService;
import android.os.Build;
import android.os.Bundle;
import android.text.InputType;
import android.view.View;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

public class MainActivity extends Activity {
    private static final int VPN_PERMISSION_REQUEST = 100;
    private static final int NOTIFICATION_PERMISSION_REQUEST = 101;
    private static final String PREFS = "sol_vpn";
    private static final String PREF_SERVER = "server";
    private static final String PREF_TOKEN = "token";

    private EditText serverField;
    private EditText tokenField;
    private TextView statusView;
    private Button actionButton;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        buildUi();
        requestNotificationPermissionIfNeeded();
    }

    @Override
    protected void onResume() {
        super.onResume();
        refreshState();
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == VPN_PERMISSION_REQUEST && resultCode == RESULT_OK) {
            startVpn();
        } else if (requestCode == VPN_PERMISSION_REQUEST) {
            statusView.setText("VPN permission was not granted");
        }
    }

    private void buildUi() {
        SharedPreferences prefs = getSharedPreferences(PREFS, Context.MODE_PRIVATE);

        ScrollView scroll = new ScrollView(this);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        int pad = dp(20);
        root.setPadding(pad, pad, pad, pad);
        scroll.addView(root);

        TextView title = new TextView(this);
        title.setText("SOL VPN");
        title.setTextSize(28);
        root.addView(title, matchWrap());

        TextView description = new TextView(this);
        description.setText("Native Android VPN client for SOL. DNS is mapped inside the app and restored to hostnames before traffic reaches the SOL SOCKS tunnel.");
        description.setTextSize(15);
        description.setPadding(0, dp(8), 0, dp(20));
        root.addView(description, matchWrap());

        TextView serverLabel = new TextView(this);
        serverLabel.setText("Server URL");
        root.addView(serverLabel, matchWrap());

        serverField = new EditText(this);
        serverField.setSingleLine(true);
        serverField.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        serverField.setText(prefs.getString(PREF_SERVER, "wss://sing.huggy.bond/ws"));
        serverField.setHint("wss://host/ws");
        root.addView(serverField, matchWrap());

        TextView tokenLabel = new TextView(this);
        tokenLabel.setText("SOL token");
        tokenLabel.setPadding(0, dp(14), 0, 0);
        root.addView(tokenLabel, matchWrap());

        tokenField = new EditText(this);
        tokenField.setSingleLine(true);
        tokenField.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        tokenField.setText(prefs.getString(PREF_TOKEN, ""));
        tokenField.setHint("Server SOL_TOKEN");
        root.addView(tokenField, matchWrap());

        TextView note = new TextView(this);
        note.setText("The token is stored only in this app's private preferences. SOL currently tunnels TCP. QUIC/other UDP traffic may fail and applications should fall back to TCP.");
        note.setTextSize(13);
        note.setPadding(0, dp(12), 0, dp(18));
        root.addView(note, matchWrap());

        actionButton = new Button(this);
        actionButton.setOnClickListener(this::onActionClicked);
        root.addView(actionButton, matchWrap());

        statusView = new TextView(this);
        statusView.setTextSize(16);
        statusView.setPadding(0, dp(18), 0, 0);
        root.addView(statusView, matchWrap());

        setContentView(scroll);
    }

    private void onActionClicked(View ignored) {
        if (SolCore.isRunning()) {
            try {
                Intent stop = new Intent(this, TProxyService.class);
                stop.setAction(TProxyService.ACTION_DISCONNECT);
                startService(stop);
                statusView.setText("Disconnecting…");
                actionButton.postDelayed(this::refreshState, 500);
            } catch (Throwable t) {
                showFailure("Unable to disconnect: " + safeMessage(t));
            }
            return;
        }

        if (!SolCore.isAvailable()) {
            showFailure("SOL native core is unavailable: " + SolCore.getLoadError());
            return;
        }
        if (!TProxyService.isTProxyNativeAvailable()) {
            showFailure("VPN native engine is unavailable");
            return;
        }

        String server = serverField.getText().toString().trim();
        String token = tokenField.getText().toString().trim();
        if (!(server.startsWith("wss://") || server.startsWith("ws://"))) {
            Toast.makeText(this, "Server URL must start with wss:// or ws://", Toast.LENGTH_LONG).show();
            return;
        }
        if (token.length() < 16) {
            Toast.makeText(this, "SOL token must be at least 16 characters", Toast.LENGTH_LONG).show();
            return;
        }

        getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                .edit()
                .putString(PREF_SERVER, server)
                .putString(PREF_TOKEN, token)
                .apply();
        TProxyService.clearLastError(this);

        try {
            Intent prepare = VpnService.prepare(this);
            if (prepare != null) {
                startActivityForResult(prepare, VPN_PERMISSION_REQUEST);
            } else {
                startVpn();
            }
        } catch (Throwable t) {
            showFailure("Unable to request VPN permission: " + safeMessage(t));
        }
    }

    private void startVpn() {
        String server = serverField.getText().toString().trim();
        String token = tokenField.getText().toString().trim();
        Intent intent = new Intent(this, TProxyService.class);
        intent.setAction(TProxyService.ACTION_CONNECT);
        intent.putExtra(TProxyService.EXTRA_SERVER_URL, server);
        intent.putExtra(TProxyService.EXTRA_TOKEN, token);
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                startForegroundService(intent);
            } else {
                startService(intent);
            }
            statusView.setText("Starting…");
            actionButton.postDelayed(this::refreshState, 900);
        } catch (Throwable t) {
            showFailure("Unable to start VPN service: " + safeMessage(t));
        }
    }

    private void refreshState() {
        boolean running = SolCore.isRunning() && TProxyService.isTProxyNativeRunning();
        actionButton.setText(running ? "Disconnect" : "Connect");

        String lastError = TProxyService.getLastError(this);
        if (running) {
            statusView.setText("Connected — Android traffic is routed through SOL");
        } else if (lastError != null && !lastError.isEmpty()) {
            statusView.setText("Connection failed — " + lastError);
        } else {
            statusView.setText("Disconnected");
        }
        serverField.setEnabled(!running);
        tokenField.setEnabled(!running);
    }

    private void showFailure(String message) {
        statusView.setText(message);
        Toast.makeText(this, message, Toast.LENGTH_LONG).show();
    }

    private void requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU
                && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(
                    new String[]{Manifest.permission.POST_NOTIFICATIONS},
                    NOTIFICATION_PERMISSION_REQUEST);
        }
    }

    private LinearLayout.LayoutParams matchWrap() {
        return new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT);
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    private static String safeMessage(Throwable t) {
        String message = t.getMessage();
        return message == null || message.isEmpty() ? t.getClass().getSimpleName() : message;
    }
}
