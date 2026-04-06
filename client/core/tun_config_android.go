//go:build android

package core

import (
	"fmt"
	"log"
	"net"
	"os"
	"sync"
)

// androidTunFD can be set from the Android Java layer (VpnService) before VPN start.
// The Java side calls SetAndroidTunFD(fd) via the exported Go bridge function.
// This is the legacy path — the new JNI bridge path uses androidCreatePlatformTun().
var (
	androidTunFD   int = -1
	androidTunMu   sync.Mutex
	androidTunOnce sync.Once
)

// SetAndroidTunFD is called from the Android VpnService Java layer to provide
// the TUN file descriptor obtained from VpnService.Builder.establish().
// This is the legacy manual path. The preferred path is the automatic JNI bridge.
func SetAndroidTunFD(fd int) {
	androidTunMu.Lock()
	androidTunFD = fd
	androidTunMu.Unlock()
}

// fdTunIface wraps an os.File (TUN file descriptor) as a tunIface.
type fdTunIface struct {
	file *os.File
	name string
}

func (f *fdTunIface) Read(b []byte) (int, error) {
	return f.file.Read(b)
}

func (f *fdTunIface) Write(b []byte) (int, error) {
	return f.file.Write(b)
}

func (f *fdTunIface) Close() error {
	return f.file.Close()
}

func (f *fdTunIface) Name() string {
	return f.name
}

func createPlatformTun() (tunIface, error) {
	// First check if a TUN fd was manually set (legacy path from Java calling SetAndroidTunFD)
	androidTunMu.Lock()
	manualFD := androidTunFD
	androidTunMu.Unlock()

	if manualFD >= 0 {
		log.Printf("[VPN-Android] Using manually-set TUN fd=%d", manualFD)
		file := os.NewFile(uintptr(manualFD), "tun-android")
		if file == nil {
			return nil, fmt.Errorf("invalid TUN file descriptor: %d", manualFD)
		}
		return &fdTunIface{file: file, name: "tun-android"}, nil
	}

	// Automatic JNI bridge path: establish VPN via Go -> JNI -> VpnService
	log.Println("[VPN-Android] No manual TUN fd set, trying JNI bridge...")
	fd, err := androidCreatePlatformTun()
	if err != nil {
		return nil, fmt.Errorf("Android VPN via JNI: %w", err)
	}

	file := os.NewFile(uintptr(fd), "tun-android")
	if file == nil {
		return nil, fmt.Errorf("invalid TUN file descriptor from JNI: %d", fd)
	}

	// Reset manual fd so future calls also go through JNI
	androidTunMu.Lock()
	androidTunFD = fd
	androidTunMu.Unlock()

	return &fdTunIface{file: file, name: "tun-android"}, nil
}

// setPendingVPNConfigIfNeeded stores VPN config for the JNI bridge on Android.
func setPendingVPNConfigIfNeeded(localIP, peerIP string, routes []string, mtu int) {
	SetPendingVPNConfig(localIP, peerIP, routes, mtu)
}

// configureTunInterface is a no-op on Android.
// VpnService.Builder handles IP/route configuration at establish() time.
func configureTunInterface(ifName, localIP, peerIP string) error {
	return nil
}

// removeTunInterface is a no-op on Android.
func removeTunInterface(ifName string) error {
	return nil
}

// addRoute is a no-op on Android.
// Routes must be added via VpnService.Builder.addRoute() before establish().
func addRoute(ifName, subnet, gateway string) error {
	return nil
}

// protectServerRoute is a no-op on Android.
// Use VpnService.protect(socket) on the Java side instead.
func protectServerRoute(serverHost string) {}

// removeServerRoute is a no-op on Android.
func removeServerRoute(serverHost string) {}

// removeRoute is a no-op on Android.
func removeRoute(ifName, subnet string) error {
	return nil
}

// enableIPForwarding is a no-op on Android (not available without root).
func enableIPForwarding() {}

// enableNAT is a no-op on Android (iptables not available without root).
func enableNAT(ifName string) {}

// disableNAT is a no-op on Android.
func disableNAT(ifName string) {}

// detectExitIP finds the local IP address that can reach the given subnet.
func detectExitIP(subnet string) net.IP {
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() == nil {
				continue
			}
			if ipNet.Contains(ip) {
				return ip
			}
		}
	}
	return nil
}

func pickSNATIP(subnet string, exitIP net.IP) net.IP {
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil
	}
	base := ipNet.IP.To4()
	if base == nil {
		return nil
	}
	for i := 254; i >= 200; i-- {
		candidate := net.IPv4(base[0], base[1], base[2], byte(i))
		if exitIP != nil && candidate.Equal(exitIP) {
			continue
		}
		return candidate
	}
	return nil
}

// setupSNATRoute is a no-op on Android.
func setupSNATRoute(ifName string, snatIP net.IP) {}

// cleanupSNATRoute is a no-op on Android.
func cleanupSNATRoute(ifName string, snatIP net.IP) {}

// stopPlatformVPN stops the Android VpnService and resets pending config.
func stopPlatformVPN() {
	androidStopVPN()
	pendingVPNMu.Lock()
	pendingVPNConfig.ready = false
	pendingVPNMu.Unlock()
	// Reset manual TUN fd so next VPN start goes through JNI fresh
	androidTunMu.Lock()
	androidTunFD = -1
	androidTunMu.Unlock()
	log.Println("[VPN-Android] Platform VPN stopped, state reset")
}

// checkForwardingStatus returns "unavailable" on Android.
func checkForwardingStatus() string {
	return "net.ipv4.ip_forward = unavailable (Android)"
}
