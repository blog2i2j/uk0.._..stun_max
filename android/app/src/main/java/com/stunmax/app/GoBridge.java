package com.stunmax.app;

import android.util.Log;

/**
 * GoBridge provides the Java-to-Go interface for the Android app.
 *
 * Go functions are exposed via gomobile/cgo and called from Java.
 * Java VpnService methods are called from Go via JNI callbacks registered here.
 *
 * Usage flow:
 * 1. Java Activity starts → calls GoBridge.init()
 * 2. Go core connects to room, peers discovered
 * 3. User starts VPN → Go calls GoBridge.establishVpn() → Java VpnService
 * 4. Java returns TUN fd → Go reads/writes IP packets
 * 5. User stops VPN → Go calls GoBridge.stopVpn()
 */
public class GoBridge {
    private static final String TAG = "GoBridge";

    /**
     * Establish VPN tunnel via Android VpnService.
     * Called from Go via JNI when the user starts a VPN connection.
     *
     * @return TUN file descriptor, or -1 on failure
     */
    public static int establishVpn(String localIP, String peerIP, String routes, int mtu, String dnsServer) {
        // Wait up to 3 seconds for the VpnService instance to become available.
        // After startService(), Android may take some time to call onCreate().
        StunMaxVpnService service = null;
        for (int i = 0; i < 30; i++) {
            service = StunMaxVpnService.getInstance();
            if (service != null) break;
            try { Thread.sleep(100); } catch (InterruptedException e) { break; }
        }
        if (service == null) {
            Log.e(TAG, "VpnService not running after 3s wait — cannot establish VPN");
            return -1;
        }
        return service.establishVpn(localIP, peerIP, routes, mtu, dnsServer);
    }

    /**
     * Protect a socket fd from being routed through the VPN.
     * Called from Go for the signaling WebSocket connection.
     */
    public static boolean protectSocket(int fd) {
        StunMaxVpnService service = StunMaxVpnService.getInstance();
        if (service == null) {
            Log.w(TAG, "VpnService not running — cannot protect socket");
            return false;
        }
        return service.protectSocket(fd);
    }

    /**
     * Stop the VPN tunnel.
     * Called from Go when the user disconnects VPN.
     */
    public static void stopVpn() {
        StunMaxVpnService service = StunMaxVpnService.getInstance();
        if (service != null) {
            service.stopVpn();
        }
    }
}
