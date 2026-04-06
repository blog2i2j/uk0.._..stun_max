//go:build !android

package core

// setPendingVPNConfigIfNeeded is a no-op on non-Android platforms.
// On Android, this stores VPN config for the JNI bridge before createPlatformTun().
func setPendingVPNConfigIfNeeded(localIP, peerIP string, routes []string, mtu int) {}
