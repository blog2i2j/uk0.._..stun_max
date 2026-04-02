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
	cmd := exec.Command("netsh", "interface", "ip", "set", "address", ifName, "static", localIP, "255.255.255.0")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		return err
	}
	cmd2 := exec.Command("netsh", "interface", "ip", "add", "route", peerIP+"/32", ifName)
	cmd2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd2.Run()
	return nil
}

func removeTunInterface(ifName string) error {
	return nil
}

func addRoute(ifName, subnet, gateway string) error {
	cmd := exec.Command("netsh", "interface", "ip", "add", "route", subnet, ifName, gateway)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func removeRoute(ifName, subnet string) error {
	cmd := exec.Command("netsh", "interface", "ip", "delete", "route", subnet, ifName)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
	return nil
}

func enableIPForwarding() {
	cmd := exec.Command("reg", "add", `HKLM\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`,
		"/v", "IPEnableRouter", "/t", "REG_DWORD", "/d", "1", "/f")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
}

func enableNAT(ifName string) {
	cmd := exec.Command("netsh", "routing", "ip", "nat", "install")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
	cmd2 := exec.Command("netsh", "routing", "ip", "nat", "add", "interface", ifName, "full")
	cmd2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd2.Run()
}

func disableNAT(ifName string) {
	cmd := exec.Command("netsh", "routing", "ip", "nat", "delete", "interface", ifName)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
}
