package bond.huggy.sol;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.net.VpnService;
import android.os.Build;

public class DebugControlReceiver extends BroadcastReceiver {
    public static final String ACTION_CONNECT = "bond.huggy.sol.DEBUG_CONNECT";
    public static final String ACTION_DISCONNECT = "bond.huggy.sol.DEBUG_DISCONNECT";
    public static final String ACTION_STATUS = "bond.huggy.sol.DEBUG_STATUS";

    @Override
    public void onReceive(Context context, Intent intent) {
        String action = intent == null ? null : intent.getAction();
        if (ACTION_STATUS.equals(action)) {
            setResultData(status(context));
            return;
        }
        if (ACTION_DISCONNECT.equals(action)) {
            Intent stop = new Intent(context, TProxyService.class);
            stop.setAction(TProxyService.ACTION_DISCONNECT);
            context.startService(stop);
            setResultData("disconnect_requested=true");
            return;
        }
        if (!ACTION_CONNECT.equals(action)) {
            setResultData("error=unknown_action");
            return;
        }

        if (VpnService.prepare(context) != null) {
            setResultData("error=vpn_permission_not_granted");
            return;
        }

        String server = intent.getStringExtra("server");
        String token = intent.getStringExtra("token");
        if (server == null || token == null) {
            setResultData("error=missing_server_or_token");
            return;
        }

        TProxyService.clearLastError(context);
        Intent start = new Intent(context, TProxyService.class);
        start.setAction(TProxyService.ACTION_CONNECT);
        start.putExtra(TProxyService.EXTRA_SERVER_URL, server);
        start.putExtra(TProxyService.EXTRA_TOKEN, token);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            context.startForegroundService(start);
        } else {
            context.startService(start);
        }
        setResultData("connect_requested=true");
    }

    private static String status(Context context) {
        String error = TProxyService.getLastError(context);
        return "solAvailable=" + SolCore.isAvailable()
                + ",solRunning=" + SolCore.isRunning()
                + ",tproxyAvailable=" + TProxyService.isTProxyNativeAvailable()
                + ",tproxyRunning=" + TProxyService.isTProxyNativeRunning()
                + ",lastError=" + (error == null ? "" : error.replace(',', ';'));
    }
}
