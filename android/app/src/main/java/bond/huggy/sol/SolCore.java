package bond.huggy.sol;

import android.util.Log;

final class SolCore {
    private static final String TAG = "SolCore";
    private static final int ERR_NATIVE_UNAVAILABLE = 100;

    private static final boolean nativeAvailable;
    private static final String nativeLoadError;

    static {
        boolean available = false;
        String error = "";
        try {
            System.loadLibrary("solcore");
            available = true;
        } catch (Throwable t) {
            error = t.toString();
            Log.e(TAG, "Unable to load SOL native core", t);
        }
        nativeAvailable = available;
        nativeLoadError = error;
    }

    private SolCore() {}

    static boolean isAvailable() {
        return nativeAvailable;
    }

    static String getLoadError() {
        return nativeLoadError;
    }

    static int start(String serverUrl, String token) {
        if (!nativeAvailable) {
            return ERR_NATIVE_UNAVAILABLE;
        }
        try {
            return nativeStart(serverUrl, token);
        } catch (Throwable t) {
            Log.e(TAG, "SOL native start failed", t);
            return ERR_NATIVE_UNAVAILABLE;
        }
    }

    static void stop() {
        if (!nativeAvailable) {
            return;
        }
        try {
            nativeStop();
        } catch (Throwable t) {
            Log.e(TAG, "SOL native stop failed", t);
        }
    }

    static boolean isRunning() {
        if (!nativeAvailable) {
            return false;
        }
        try {
            return nativeIsRunning();
        } catch (Throwable t) {
            Log.e(TAG, "SOL native status failed", t);
            return false;
        }
    }

    private static native int nativeStart(String serverUrl, String token);
    private static native void nativeStop();
    private static native boolean nativeIsRunning();
}
