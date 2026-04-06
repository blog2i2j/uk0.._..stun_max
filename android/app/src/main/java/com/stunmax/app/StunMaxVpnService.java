package com.stunmax.app;

import android.content.Intent;
import android.net.VpnService;
import android.os.ParcelFileDescriptor;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.os.Build;
import android.util.Log;

/**
 * StunMaxVpnService manages the Android VPN tunnel.
 *
 * The Go core calls this service to:
 * 1. Establish a TUN device via VpnService.Builder
 * 2. Get the file descriptor to pass to Go's TUN read/write loop
 * 3. Protect the signaling WebSocket from being routed through the VPN
 *
 * Lifecycle:
 *   Go core -> startVpn(localIP, peerIP, routes[], mtu, dnsServers[])
 *   This service -> Builder.establish() -> returns fd to Go
 *   Go core -> reads/writes IP packets on the fd
 *   Go core -> stopVpn() -> this service tears down
 */
public class StunMaxVpnService extends VpnService {

    private static final String TAG = "StunMaxVPN";
    private static final String CHANNEL_ID = "stunmax_vpn";
    private static final int NOTIFICATION_ID = 1;

    private ParcelFileDescriptor tunFd;
    private static StunMaxVpnService instance;

    @Override
    public void onCreate() {
        super.onCreate();
        instance = this;
        createNotificationChannel();
        Log.i(TAG, "VpnService created");
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && "STOP".equals(intent.getAction())) {
            stopVpn();
            stopSelf();
            return START_NOT_STICKY;
        }
        // Use START_NOT_STICKY: don't auto-restart the service if the app is killed.
        // The Go core will explicitly start the service when needed.
        return START_NOT_STICKY;
    }

    @Override
    public void onDestroy() {
        stopVpn();
        instance = null;
        super.onDestroy();
        Log.i(TAG, "VpnService destroyed");
    }

    /**
     * Returns the singleton instance (available after service start).
     */
    public static StunMaxVpnService getInstance() {
        return instance;
    }

    /**
     * Establish the VPN tunnel and return the TUN file descriptor.
     *
     * @param localIP   Local virtual IP (e.g., "10.7.0.2")
     * @param peerIP    Peer virtual IP (e.g., "10.7.0.3")
     * @param routes    Comma-separated CIDR routes (e.g., "10.7.0.0/24,10.88.0.0/16")
     * @param mtu       MTU size (e.g., 1400)
     * @param dnsServer DNS server IP (e.g., "8.8.8.8"), empty to skip
     * @return file descriptor number, or -1 on failure
     */
    public int establishVpn(String localIP, String peerIP, String routes, int mtu, String dnsServer) {
        try {
            Builder builder = new Builder()
                    .setSession("STUN Max VPN")
                    .setMtu(mtu)
                    .addAddress(localIP, 24);

            // Add routes
            if (routes != null && !routes.isEmpty()) {
                for (String route : routes.split(",")) {
                    route = route.trim();
                    if (route.isEmpty()) continue;
                    String[] parts = route.split("/");
                    if (parts.length == 2) {
                        builder.addRoute(parts[0], Integer.parseInt(parts[1]));
                    }
                }
            }

            // Add default VPN subnet route
            builder.addRoute(peerIP, 32);

            // DNS
            if (dnsServer != null && !dnsServer.isEmpty()) {
                builder.addDnsServer(dnsServer);
            }

            // Allow the app itself to bypass VPN (prevent routing loop)
            builder.addDisallowedApplication(getPackageName());

            tunFd = builder.establish();
            if (tunFd == null) {
                Log.e(TAG, "VPN establish() returned null — user may have denied permission");
                return -1;
            }

            // Start foreground notification
            startForeground(NOTIFICATION_ID, buildNotification());

            int fd = tunFd.getFd();
            Log.i(TAG, "VPN established, fd=" + fd + " mtu=" + mtu);
            return fd;

        } catch (Exception e) {
            Log.e(TAG, "Failed to establish VPN: " + e.getMessage(), e);
            return -1;
        }
    }

    /**
     * Protect a socket from being routed through the VPN tunnel.
     * Called by Go core for the signaling WebSocket connection.
     *
     * @param fd raw socket file descriptor
     * @return true if protected successfully
     */
    public boolean protectSocket(int fd) {
        return protect(fd);
    }

    /**
     * Stop the VPN and close the TUN device.
     */
    public void stopVpn() {
        if (tunFd != null) {
            try {
                tunFd.close();
                Log.i(TAG, "VPN stopped, TUN fd closed");
            } catch (Exception e) {
                Log.e(TAG, "Error closing TUN fd: " + e.getMessage());
            }
            tunFd = null;
        }
        stopForeground(true);
    }

    private void createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            NotificationChannel channel = new NotificationChannel(
                    CHANNEL_ID,
                    "STUN Max VPN",
                    NotificationManager.IMPORTANCE_LOW
            );
            channel.setDescription("VPN tunnel status");
            NotificationManager nm = getSystemService(NotificationManager.class);
            if (nm != null) {
                nm.createNotificationChannel(channel);
            }
        }
    }

    private Notification buildNotification() {
        Notification.Builder builder;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            builder = new Notification.Builder(this, CHANNEL_ID);
        } else {
            builder = new Notification.Builder(this);
        }

        return builder
                .setContentTitle("STUN Max VPN")
                .setContentText("VPN tunnel is active")
                .setSmallIcon(android.R.drawable.ic_lock_lock)
                .setOngoing(true)
                .build();
    }
}
