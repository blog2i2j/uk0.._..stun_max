//go:build windows

package core

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.zx2c4.com/wireguard/tun"
)

// wgTunIface wraps wireguard tun.Device to implement tunIface.
type wgTunIface struct {
	dev  tun.Device
	name string
}

func (w *wgTunIface) Read(b []byte) (int, error) {
	// wireguard/tun uses batch reads; we read one packet at a time
	bufs := [][]byte{b}
	sizes := make([]int, 1)
	n, err := w.dev.Read(bufs, sizes, 0)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, fmt.Errorf("no packets read")
	}
	return sizes[0], nil
}

func (w *wgTunIface) Write(b []byte) (int, error) {
	bufs := [][]byte{b}
	n, err := w.dev.Write(bufs, 0)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, fmt.Errorf("no packets written")
	}
	return len(b), nil
}

func (w *wgTunIface) Close() error {
	return w.dev.Close()
}

func (w *wgTunIface) Name() string {
	return w.name
}

func createPlatformTun() (tunIface, error) {
	dev, err := tun.CreateTUN("StunMax", 1500)
	if err != nil {
		return nil, fmt.Errorf("Wintun device creation failed: %w", err)
	}
	name, err := dev.Name()
	if err != nil {
		dev.Close()
		return nil, err
	}
	return &wgTunIface{dev: dev, name: name}, nil
}

func configureTunInterface(ifName, localIP, peerIP string) error {
	if err := runSilentErr("netsh", "interface", "ip", "set", "address", ifName, "static", localIP, "255.255.255.0"); err != nil {
		return err
	}
	runSilent("netsh", "interface", "ip", "add", "route", peerIP+"/32", ifName)
	return nil
}

func removeTunInterface(ifName string) error {
	return nil
}

func addRoute(ifName, subnet, gateway string) error {
	return runSilentErr("netsh", "interface", "ip", "add", "route", subnet, ifName, gateway)
}

func removeRoute(ifName, subnet string) error {
	runSilent("netsh", "interface", "ip", "delete", "route", subnet, ifName)
	return nil
}

func runSilentErr(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func enableIPForwarding() {
	// Immediate effect (no reboot needed) — enable forwarding on ALL interfaces
	runSilent("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Get-NetAdapter | ForEach-Object { Set-NetIPInterface -InterfaceIndex $_.ifIndex -Forwarding Enabled -ErrorAction SilentlyContinue }`)
	// Also set registry for persistence across reboots
	runSilent("reg", "add", `HKLM\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`,
		"/v", "IPEnableRouter", "/t", "REG_DWORD", "/d", "1", "/f")
}

func enableNAT(ifName string) {
	// Win10: use New-NetNat (works without Hyper-V on modern Win10/11)
	// First remove any existing StunMax NAT
	runSilent("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Remove-NetNat -Name StunMaxNAT -Confirm:$false -ErrorAction SilentlyContinue`)
	// Create NAT for the VPN subnet
	runSilent("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`New-NetNat -Name StunMaxNAT -InternalIPInterfaceAddressPrefix "10.7.0.0/24" -ErrorAction SilentlyContinue`)
	// Fallback: try netsh routing (Windows Server)
	runSilent("netsh", "routing", "ip", "nat", "install")
	runSilent("netsh", "routing", "ip", "nat", "add", "interface", ifName, "full")
}

func disableNAT(ifName string) {
	runSilent("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Remove-NetNat -Name StunMaxNAT -Confirm:$false -ErrorAction SilentlyContinue`)
	runSilent("netsh", "routing", "ip", "nat", "delete", "interface", ifName)
	// Disable forwarding
	runSilent("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Get-NetAdapter | ForEach-Object { Set-NetIPInterface -InterfaceIndex $_.ifIndex -Forwarding Disabled -ErrorAction SilentlyContinue }`)
}

func runSilent(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
}
