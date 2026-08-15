package bond.huggy.sol;

final class SolCore {
    static {
        System.loadLibrary("solcore");
    }

    private SolCore() {}

    static native int nativeStart(String serverUrl, String token);
    static native void nativeStop();
    static native boolean nativeIsRunning();
}
