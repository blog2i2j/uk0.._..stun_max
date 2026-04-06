package com.stunmax.app;

import android.app.Activity;
import android.content.Intent;
import android.net.VpnService;
import android.os.Bundle;
import android.util.Log;

/**
 * Transparent Activity that requests VPN permission from the user.
 *
 * This exists because VpnService.prepare() returns an Intent that must be
 * launched via startActivityForResult from an Activity context. Launching it
 * from an Application context (which is what GioUI's app.AppContext() returns)
 * does not work on many Android ROMs (Huawei EMUI, Xiaomi MIUI, etc.).
 *
 * Flow:
 * 1. Go calls startActivity(VpnPermissionActivity) with FLAG_ACTIVITY_NEW_TASK
 * 2. This Activity calls VpnService.prepare(this) → gets consent Intent
 * 3. Calls startActivityForResult(intent) → system shows VPN consent dialog
 * 4. User approves → onActivityResult sets permissionGranted = true
 * 5. Go polls permissionGranted flag
 * 6. Activity finishes itself (transparent, user never sees it)
 */
public class VpnPermissionActivity extends Activity {
    private static final String TAG = "StunMaxVPN";
    private static final int REQUEST_VPN = 100;

    // Shared state: Go polls this flag
    private static volatile boolean permissionGranted = false;
    private static volatile boolean requestInProgress = false;

    public static boolean isPermissionGranted() {
        return permissionGranted;
    }

    public static void resetPermission() {
        permissionGranted = false;
    }

    public static boolean isRequestInProgress() {
        return requestInProgress;
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        requestInProgress = true;

        Intent intent = VpnService.prepare(this);
        if (intent == null) {
            // Already granted
            Log.i(TAG, "VpnPermissionActivity: permission already granted");
            permissionGranted = true;
            requestInProgress = false;
            finish();
            return;
        }

        Log.i(TAG, "VpnPermissionActivity: launching VPN consent dialog via startActivityForResult");
        try {
            startActivityForResult(intent, REQUEST_VPN);
        } catch (Exception e) {
            Log.e(TAG, "Failed to start VPN consent activity: " + e.getMessage());
            requestInProgress = false;
            finish();
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == REQUEST_VPN) {
            if (resultCode == RESULT_OK) {
                Log.i(TAG, "VpnPermissionActivity: user APPROVED VPN permission");
                permissionGranted = true;
            } else {
                Log.i(TAG, "VpnPermissionActivity: user DENIED VPN permission");
                permissionGranted = false;
            }
        }
        requestInProgress = false;
        finish();
    }

    @Override
    public void onBackPressed() {
        // User pressed back on consent dialog
        requestInProgress = false;
        super.onBackPressed();
    }
}
