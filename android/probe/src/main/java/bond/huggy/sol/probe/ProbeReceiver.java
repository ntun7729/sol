package bond.huggy.sol.probe;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;

public class ProbeReceiver extends BroadcastReceiver {
    private static final String PREFS = "probe";
    private static final String KEY_RESULT = "result";

    @Override
    public void onReceive(Context context, Intent intent) {
        String url = intent == null ? null : intent.getStringExtra("url");
        PendingResult pending = goAsync();
        new Thread(() -> {
            String result;
            try {
                result = runProbe(url);
            } catch (Throwable t) {
                String message = t.getMessage();
                result = "error=" + t.getClass().getSimpleName()
                        + ":" + (message == null ? "" : message);
            }
            SharedPreferences prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
            prefs.edit().putString(KEY_RESULT, result).commit();
            pending.finish();
        }, "sol-vpn-probe").start();
    }

    private static String runProbe(String target) throws Exception {
        if (target == null || target.isEmpty()) {
            throw new IllegalArgumentException("missing url");
        }

        HttpURLConnection connection = (HttpURLConnection) new URL(target).openConnection();
        connection.setConnectTimeout(15000);
        connection.setReadTimeout(15000);
        connection.setInstanceFollowRedirects(false);
        connection.setRequestProperty("Connection", "close");

        int code = connection.getResponseCode();
        StringBuilder body = new StringBuilder();
        try (BufferedReader reader = new BufferedReader(new InputStreamReader(
                code >= 400 ? connection.getErrorStream() : connection.getInputStream(),
                StandardCharsets.UTF_8))) {
            if (reader != null) {
                String line;
                while ((line = reader.readLine()) != null && body.length() < 1024) {
                    body.append(line);
                }
            }
        }
        connection.disconnect();
        return "code=" + code + ",body=" + body;
    }
}
