package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// STUN constants (RFC 5389)
const (
	stunMagicCookie    uint32 = 0x2112A442
	stunBindingRequest uint16 = 0x0001
	stunAttrXorMapped  uint16 = 0x0020
	stunAttrMapped     uint16 = 0x0001
	stunAttrChangedAddr uint16 = 0x0005
	stunAttrOtherAddr  uint16 = 0x802C
	stunHeaderSize            = 20
	stunTimeout               = 3 * time.Second
)

// ANSI escape sequences
const (
	reset     = "\033[0m"
	bold      = "\033[1m"
	dim       = "\033[2m"
	italic    = "\033[3m"
	underline = "\033[4m"
	red       = "\033[31m"
	green     = "\033[32m"
	yellow    = "\033[33m"
	blue      = "\033[34m"
	magenta   = "\033[35m"
	cyan      = "\033[36m"
	white     = "\033[97m"
	gray      = "\033[90m"
	bgRed     = "\033[41m"
	bgGreen   = "\033[42m"
	bgYellow  = "\033[43m"
	bgBlue    = "\033[44m"
	bgCyan    = "\033[46m"
)

// NAT types (RFC 3489 + extended)
const (
	NATOpen              = "Open Internet"
	NATFullCone          = "Full Cone NAT"
	NATRestrictedCone    = "Restricted Cone NAT"
	NATPortRestricted    = "Port Restricted Cone NAT"
	NATSymmetric         = "Symmetric NAT"
	NATSymmetricFirewall = "Symmetric UDP Firewall"
	NATBlocked           = "UDP Blocked"
)

// STUNResult holds the result of a single STUN query
type STUNResult struct {
	Server     string
	PublicAddr string
	PublicIP   string
	PublicPort int
	LocalPort  int
	Latency    time.Duration
	Error      error
}

// PortAllocInfo describes port allocation behavior
type PortAllocInfo struct {
	Pattern    string // "port-preserving", "sequential", "random"
	Delta      int    // average delta for sequential
	Ports      []int  // observed public ports
	LocalPorts []int  // corresponding local ports
	Predictable bool
}

// FilteringInfo describes NAT filtering behavior
type FilteringInfo struct {
	EndpointIndependent bool // same mapping regardless of destination
	AddressDependent    bool // mapping depends on destination IP
	AddressPortDependent bool // mapping depends on destination IP+port
}

// NATReport is the full diagnostic report
type NATReport struct {
	LocalIP        string
	PublicIP       string
	PublicPort     int
	Results        []STUNResult
	NATType        string
	PortConsistent bool
	IPConsistent   bool
	HairpinOK      bool
	BindingLifetime time.Duration
	PortAlloc      PortAllocInfo
	Filtering      FilteringInfo
	Score          int    // 0-100 hole punch score
	Difficulty     string // "Easy", "Medium", "Hard", "Very Hard", "Impossible"
	HolePunchProb  string // "Very High", "High", "Medium", "Low", "Very Low", "None"
}

// Default STUN servers (geographically diverse)
var defaultSTUNServers = []string{
	"stun.cloudflare.com:3478",
	"stun.miwifi.com:3478",
	"stun.chat.bilibili.com:3478",
	"stun.l.google.com:19302",
	"stun1.l.google.com:19302",
	"stun2.l.google.com:19302",
	"stun.syncthing.net:3478",
}

func main() {
	servers := flag.String("servers", "", "comma-separated STUN servers (default: built-in list)")
	verbose := flag.Bool("v", false, "verbose output")
	fast := flag.Bool("fast", false, "skip binding lifetime test (faster)")
	flag.Parse()

	stunServers := defaultSTUNServers
	if *servers != "" {
		stunServers = strings.Split(*servers, ",")
		for i := range stunServers {
			stunServers[i] = strings.TrimSpace(stunServers[i])
		}
	}

	printBanner()
	report := runDiagnostics(stunServers, *verbose, *fast)
	printReport(report)
}

// ════════════════════════════════════════════════════════════════
// Banner & Visual Helpers
// ════════════════════════════════════════════════════════════════

func printBanner() {
	fmt.Println()
	fmt.Printf("  %s%s╔══════════════════════════════════════════════╗%s\n", bold, cyan, reset)
	fmt.Printf("  %s%s║%s  %s%s⚡ STUN Max — NAT Diagnostic Tool%s           %s%s║%s\n", bold, cyan, reset, bold, white, reset, bold, cyan, reset)
	fmt.Printf("  %s%s║%s     Comprehensive NAT analysis & P2P check   %s%s║%s\n", bold, cyan, reset, bold, cyan, reset)
	fmt.Printf("  %s%s╚══════════════════════════════════════════════╝%s\n", bold, cyan, reset)
	fmt.Println()
}

func printSection(num int, title string) {
	fmt.Printf("\n  %s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", dim, cyan, reset)
	fmt.Printf("  %s%s  TEST %d: %s%s\n", bold, cyan, num, title, reset)
	fmt.Printf("  %s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", dim, cyan, reset)
}

func spinner(done chan bool, msg string) {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0
	for {
		select {
		case <-done:
			fmt.Printf("\r  %s\r", strings.Repeat(" ", len(msg)+10))
			return
		default:
			fmt.Printf("\r  %s%s%s %s", cyan, frames[i%len(frames)], reset, msg)
			i++
			time.Sleep(80 * time.Millisecond)
		}
	}
}

func progressBar(value, max int, width int) string {
	if max == 0 {
		return strings.Repeat("░", width)
	}
	filled := int(float64(value) / float64(max) * float64(width))
	if filled > width {
		filled = width
	}
	bar := ""
	for i := 0; i < width; i++ {
		if i < filled {
			if value >= 80 {
				bar += fmt.Sprintf("%s█%s", green, reset)
			} else if value >= 50 {
				bar += fmt.Sprintf("%s█%s", yellow, reset)
			} else if value >= 30 {
				bar += fmt.Sprintf("%s█%s", yellow, reset)
			} else {
				bar += fmt.Sprintf("%s█%s", red, reset)
			}
		} else {
			bar += fmt.Sprintf("%s░%s", gray, reset)
		}
	}
	return bar
}

func difficultyStars(difficulty string) string {
	switch difficulty {
	case "Easy":
		return fmt.Sprintf("%s★☆☆☆☆%s", green, reset)
	case "Medium":
		return fmt.Sprintf("%s★★☆☆☆%s", yellow, reset)
	case "Hard":
		return fmt.Sprintf("%s★★★☆☆%s", yellow, reset)
	case "Very Hard":
		return fmt.Sprintf("%s★★★★☆%s", red, reset)
	case "Impossible":
		return fmt.Sprintf("%s★★★★★%s", red, reset)
	}
	return "☆☆☆☆☆"
}

// ════════════════════════════════════════════════════════════════
// STUN Protocol Implementation
// ════════════════════════════════════════════════════════════════

func buildSTUNRequest() ([]byte, []byte) {
	req := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(req[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(req[2:4], 0) // length
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	txID := make([]byte, 12)
	rand.Read(txID)
	copy(req[8:20], txID)
	return req, txID
}

func parseSTUNResponse(resp []byte, txID []byte) (string, int, error) {
	if len(resp) < stunHeaderSize {
		return "", 0, fmt.Errorf("response too short: %d bytes", len(resp))
	}
	msgType := binary.BigEndian.Uint16(resp[0:2])
	if msgType != 0x0101 {
		return "", 0, fmt.Errorf("unexpected message type: 0x%04x", msgType)
	}
	if !bytes.Equal(resp[8:20], txID) {
		return "", 0, fmt.Errorf("transaction ID mismatch")
	}

	msgLen := int(binary.BigEndian.Uint16(resp[2:4]))
	if stunHeaderSize+msgLen > len(resp) {
		return "", 0, fmt.Errorf("truncated response")
	}
	attrs := resp[stunHeaderSize : stunHeaderSize+msgLen]

	ip, port, err := findAddress(attrs, stunAttrXorMapped, true)
	if err != nil {
		ip, port, err = findAddress(attrs, stunAttrMapped, false)
	}
	return ip, port, err
}

func findAddress(attrs []byte, targetType uint16, xor bool) (string, int, error) {
	offset := 0
	for offset+4 <= len(attrs) {
		attrType := binary.BigEndian.Uint16(attrs[offset : offset+2])
		attrLen := int(binary.BigEndian.Uint16(attrs[offset+2 : offset+4]))
		offset += 4
		if offset+attrLen > len(attrs) {
			break
		}
		if attrType == targetType {
			return decodeAddress(attrs[offset:offset+attrLen], xor)
		}
		offset += attrLen
		if attrLen%4 != 0 {
			offset += 4 - (attrLen % 4)
		}
	}
	return "", 0, fmt.Errorf("address attribute 0x%04x not found", targetType)
}

func decodeAddress(data []byte, xor bool) (string, int, error) {
	if len(data) < 8 {
		return "", 0, fmt.Errorf("address data too short")
	}
	family := data[1]
	if family != 0x01 {
		return "", 0, fmt.Errorf("unsupported family: 0x%02x (IPv6 not supported)", family)
	}

	rawPort := binary.BigEndian.Uint16(data[2:4])
	rawIP := binary.BigEndian.Uint32(data[4:8])

	var port uint16
	var ip uint32
	if xor {
		port = rawPort ^ uint16(stunMagicCookie>>16)
		ip = rawIP ^ stunMagicCookie
	} else {
		port = rawPort
		ip = rawIP
	}

	ipStr := fmt.Sprintf("%d.%d.%d.%d", byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
	return ipStr, int(port), nil
}

// ════════════════════════════════════════════════════════════════
// STUN Query Functions
// ════════════════════════════════════════════════════════════════

func querySTUN(conn *net.UDPConn, server string) STUNResult {
	start := time.Now()
	result := STUNResult{Server: server}

	serverAddr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		result.Error = fmt.Errorf("resolve: %w", err)
		return result
	}

	req, txID := buildSTUNRequest()

	conn.SetWriteDeadline(time.Now().Add(stunTimeout))
	if _, err := conn.WriteToUDP(req, serverAddr); err != nil {
		result.Error = fmt.Errorf("send: %w", err)
		return result
	}

	conn.SetReadDeadline(time.Now().Add(stunTimeout))
	buf := make([]byte, 1024)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		result.Error = fmt.Errorf("recv: %w", err)
		return result
	}
	result.Latency = time.Since(start)

	ip, port, err := parseSTUNResponse(buf[:n], txID)
	if err != nil {
		result.Error = err
		return result
	}

	result.PublicIP = ip
	result.PublicPort = port
	result.PublicAddr = fmt.Sprintf("%s:%d", ip, port)
	result.LocalPort = conn.LocalAddr().(*net.UDPAddr).Port
	return result
}

func querySTUNFresh(server string) STUNResult {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return STUNResult{Server: server, Error: fmt.Errorf("listen: %w", err)}
	}
	defer conn.Close()
	return querySTUN(conn, server)
}

func querySTUNWithRetry(conn *net.UDPConn, server string, retries int) STUNResult {
	for i := 0; i < retries; i++ {
		r := querySTUN(conn, server)
		if r.Error == nil {
			return r
		}
		if i < retries-1 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	return querySTUN(conn, server)
}

// ════════════════════════════════════════════════════════════════
// Diagnostic Tests
// ════════════════════════════════════════════════════════════════

func runDiagnostics(stunServers []string, verbose, fast bool) NATReport {
	report := NATReport{}

	// Detect local IP
	report.LocalIP = getLocalIP()
	fmt.Printf("  %s●%s Local IP:  %s%s%s\n", cyan, reset, bold, report.LocalIP, reset)

	// ─── Test 1: STUN Reachability ────────────────────────
	printSection(1, "STUN Reachability & Endpoint Mapping")
	fmt.Println()

	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		fmt.Printf("  %s✗ Failed to create UDP socket: %v%s\n", red, err, reset)
		report.NATType = NATBlocked
		report.Difficulty = "Impossible"
		report.HolePunchProb = "None"
		report.Score = 0
		return report
	}
	localPort := conn.LocalAddr().(*net.UDPAddr).Port
	fmt.Printf("  %s●%s Local UDP port: %s%d%s\n\n", cyan, reset, bold, localPort, reset)

	// Query all servers from same socket
	var sameSocketResults []STUNResult
	successCount := 0
	for _, server := range stunServers {
		r := querySTUN(conn, server)
		r.LocalPort = localPort
		sameSocketResults = append(sameSocketResults, r)
		if r.Error != nil {
			if verbose {
				fmt.Printf("    %s✗%s %-38s %s%v%s\n", red, reset, server, gray, r.Error, reset)
			}
		} else {
			successCount++
			latColor := green
			if r.Latency > 200*time.Millisecond {
				latColor = yellow
			} else if r.Latency > 500*time.Millisecond {
				latColor = red
			}
			fmt.Printf("    %s✓%s %-38s → %s%-21s%s  %s%s%s\n",
				green, reset, server, bold, r.PublicAddr, reset,
				latColor, r.Latency.Round(time.Millisecond), reset)
		}
	}
	conn.Close()

	fmt.Printf("\n    %sReachable: %d/%d servers%s\n", dim, successCount, len(stunServers), reset)

	// Filter OK results
	var okResults []STUNResult
	for _, r := range sameSocketResults {
		if r.Error == nil {
			okResults = append(okResults, r)
		}
	}

	if len(okResults) == 0 {
		fmt.Printf("\n  %s✗ No STUN server reachable — UDP appears to be blocked%s\n", red, reset)
		report.NATType = NATBlocked
		report.Difficulty = "Impossible"
		report.HolePunchProb = "None"
		report.Score = 0
		report.Results = sameSocketResults
		return report
	}

	report.Results = sameSocketResults
	report.PublicIP = okResults[0].PublicIP
	report.PublicPort = okResults[0].PublicPort

	// Check consistency
	ips := map[string]bool{}
	ports := map[int]bool{}
	for _, r := range okResults {
		ips[r.PublicIP] = true
		ports[r.PublicPort] = true
	}
	report.IPConsistent = len(ips) == 1
	report.PortConsistent = len(ports) == 1

	fmt.Printf("\n  %s●%s Endpoint mapping analysis:\n", cyan, reset)
	if report.IPConsistent {
		fmt.Printf("    %s✓%s IP mapping:   %sEndpoint-Independent%s (same IP for all servers)\n", green, reset, green, reset)
	} else {
		fmt.Printf("    %s✗%s IP mapping:   %sEndpoint-Dependent%s (different IPs per server)\n", red, reset, red, reset)
	}
	if report.PortConsistent {
		fmt.Printf("    %s✓%s Port mapping: %sEndpoint-Independent%s (same port for all servers)\n", green, reset, green, reset)
	} else {
		fmt.Printf("    %s✗%s Port mapping: %sEndpoint-Dependent%s (different port per server)\n", red, reset, red, reset)
		uniquePorts := []int{}
		for p := range ports {
			uniquePorts = append(uniquePorts, p)
		}
		sort.Ints(uniquePorts)
		fmt.Printf("      %sObserved ports: %v%s\n", gray, uniquePorts, reset)
	}

	// ─── Test 2: Port Allocation Pattern ────────────────────
	printSection(2, "Port Allocation Pattern")
	fmt.Println()

	bestServer := okResults[0].Server
	portAlloc := analyzePortAllocation(bestServer, verbose)
	report.PortAlloc = portAlloc

	switch portAlloc.Pattern {
	case "port-preserving":
		fmt.Printf("  %s●%s Pattern: %s%sPort Preserving%s\n", cyan, reset, bold, green, reset)
		fmt.Printf("    %sNAT preserves your local port number — best case for P2P%s\n", gray, reset)
	case "sequential":
		fmt.Printf("  %s●%s Pattern: %s%sSequential%s (delta ≈ %d)\n", cyan, reset, bold, yellow, reset, portAlloc.Delta)
		fmt.Printf("    %sPort allocation is predictable — port prediction attacks work%s\n", gray, reset)
	case "random":
		fmt.Printf("  %s●%s Pattern: %s%sRandom%s\n", cyan, reset, bold, red, reset)
		fmt.Printf("    %sPort allocation is unpredictable — hole punching is harder%s\n", gray, reset)
	default:
		fmt.Printf("  %s●%s Pattern: %s%sUnknown%s (insufficient data)\n", cyan, reset, bold, gray, reset)
	}

	if len(portAlloc.Ports) > 0 && verbose {
		fmt.Printf("    %sSampled ports: %v%s\n", gray, portAlloc.Ports, reset)
	}

	// ─── Test 3: Filtering Behavior ─────────────────────────
	printSection(3, "NAT Filtering Behavior")
	fmt.Println()

	filtering := analyzeFiltering(okResults, stunServers)
	report.Filtering = filtering

	if filtering.EndpointIndependent {
		fmt.Printf("  %s●%s Filtering: %s%sEndpoint-Independent%s\n", cyan, reset, bold, green, reset)
		fmt.Printf("    %sNAT allows packets from any source — Full Cone behavior%s\n", gray, reset)
	} else if filtering.AddressDependent {
		fmt.Printf("  %s●%s Filtering: %s%sAddress-Dependent%s\n", cyan, reset, bold, yellow, reset)
		fmt.Printf("    %sNAT only allows packets from contacted IPs — Restricted Cone%s\n", gray, reset)
	} else if filtering.AddressPortDependent {
		fmt.Printf("  %s●%s Filtering: %s%sAddress+Port-Dependent%s\n", cyan, reset, bold, yellow, reset)
		fmt.Printf("    %sNAT only allows packets from exact contacted IP:port — Port Restricted%s\n", gray, reset)
	} else {
		fmt.Printf("  %s●%s Filtering: %sCould not determine%s\n", cyan, reset, gray, reset)
		fmt.Printf("    %sMulti-address STUN server needed for precise classification%s\n", gray, reset)
	}

	// ─── Test 4: Hairpin NAT ────────────────────────────────
	printSection(4, "Hairpin NAT (Loopback)")
	fmt.Println()

	done := make(chan bool)
	go spinner(done, "Testing hairpin NAT...")
	report.HairpinOK = testHairpin(okResults[0].PublicAddr)
	done <- true

	if report.HairpinOK {
		fmt.Printf("  %s✓%s Hairpin NAT: %sSupported%s\n", green, reset, green, reset)
		fmt.Printf("    %sYou can send packets to your own public address through the NAT%s\n", gray, reset)
	} else {
		fmt.Printf("  %s─%s Hairpin NAT: %sNot supported%s\n", gray, reset, gray, reset)
		fmt.Printf("    %sNormal for most NATs — does not affect P2P connectivity%s\n", gray, reset)
	}

	// ─── Test 5: Binding Lifetime ───────────────────────────
	if !fast {
		printSection(5, "NAT Binding Lifetime")
		fmt.Println()

		done = make(chan bool)
		go spinner(done, "Measuring binding lifetime (10s wait)...")
		report.BindingLifetime = testBindingLifetime(bestServer)
		done <- true

		if report.BindingLifetime > 0 {
			fmt.Printf("  %s✓%s Binding alive after %s%s%s: %sstable%s\n",
				green, reset, bold, report.BindingLifetime, reset, green, reset)
			fmt.Printf("    %sNAT mapping persists for at least %s — good for maintaining tunnels%s\n",
				gray, report.BindingLifetime, reset)
		} else {
			fmt.Printf("  %s!%s Binding lifetime: %scould not determine%s\n", yellow, reset, yellow, reset)
			fmt.Printf("    %sMapping may have changed during the test period%s\n", gray, reset)
		}
	}

	// ─── Test 6: Port Prediction Accuracy ───────────────────
	if !report.PortConsistent && portAlloc.Pattern == "sequential" {
		printSection(6, "Port Prediction Accuracy")
		fmt.Println()

		accuracy := testPortPrediction(bestServer, portAlloc.Delta)
		if accuracy > 0 {
			fmt.Printf("  %s●%s Prediction accuracy: %s%d%%%s\n", cyan, reset, bold, accuracy, reset)
			if accuracy >= 70 {
				fmt.Printf("    %sBirthday attack + port prediction should work well%s\n", gray, reset)
			} else if accuracy >= 40 {
				fmt.Printf("    %sPort prediction partially works — multi-socket burst recommended%s\n", gray, reset)
			} else {
				fmt.Printf("    %sPort prediction unreliable — rely on birthday attack volume%s\n", gray, reset)
			}
		}
	}

	// Classify NAT
	report.NATType = classifyNAT(report)

	// Score and assess
	report.Score, report.Difficulty, report.HolePunchProb = scoreHolePunch(report)

	return report
}

// ════════════════════════════════════════════════════════════════
// Port Allocation Analysis
// ════════════════════════════════════════════════════════════════

func analyzePortAllocation(server string, verbose bool) PortAllocInfo {
	info := PortAllocInfo{}

	sampleCount := 5
	fmt.Printf("  %sSampling %d fresh sockets...%s\n\n", gray, sampleCount, reset)

	for i := 0; i < sampleCount; i++ {
		r := querySTUNFresh(server)
		if r.Error == nil {
			info.Ports = append(info.Ports, r.PublicPort)
			info.LocalPorts = append(info.LocalPorts, r.LocalPort)
			fmt.Printf("    Socket %d: local :%s%-5d%s → public :%s%-5d%s",
				i+1, dim, r.LocalPort, reset, bold, r.PublicPort, reset)
			if i > 0 && len(info.Ports) >= 2 {
				delta := info.Ports[len(info.Ports)-1] - info.Ports[len(info.Ports)-2]
				if delta >= 0 {
					fmt.Printf("  %s(Δ +%d)%s", gray, delta, reset)
				} else {
					fmt.Printf("  %s(Δ %d)%s", gray, delta, reset)
				}
			}
			fmt.Println()
		}
		// Small delay between samples to avoid port reuse
		time.Sleep(50 * time.Millisecond)
	}

	if len(info.Ports) < 3 {
		info.Pattern = "unknown"
		return info
	}

	// Analyze deltas
	sort.Ints(info.Ports)
	deltas := make([]int, 0, len(info.Ports)-1)
	for i := 1; i < len(info.Ports); i++ {
		deltas = append(deltas, info.Ports[i]-info.Ports[i-1])
	}

	// Check port-preserving
	allSame := true
	for i := 0; i < len(info.Ports); i++ {
		if info.Ports[i] != info.LocalPorts[i] {
			allSame = false
			break
		}
	}
	if allSame {
		info.Pattern = "port-preserving"
		info.Predictable = true
		return info
	}

	// Check sequential (small, consistent deltas)
	if len(deltas) >= 2 {
		avgDelta := 0
		maxDev := 0
		for _, d := range deltas {
			avgDelta += d
		}
		avgDelta /= len(deltas)

		for _, d := range deltas {
			dev := abs(d - avgDelta)
			if dev > maxDev {
				maxDev = dev
			}
		}

		// Sequential: average delta 1-10, deviation <= 2
		if avgDelta >= 1 && avgDelta <= 10 && maxDev <= 3 {
			info.Pattern = "sequential"
			info.Delta = avgDelta
			info.Predictable = true
			return info
		}

		// Check if all deltas are 0 (port-preserving at NAT level)
		allZero := true
		for _, d := range deltas {
			if d != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			info.Pattern = "port-preserving"
			info.Predictable = true
			return info
		}
	}

	info.Pattern = "random"
	info.Predictable = false
	return info
}

// ════════════════════════════════════════════════════════════════
// Filtering Analysis
// ════════════════════════════════════════════════════════════════

func analyzeFiltering(results []STUNResult, servers []string) FilteringInfo {
	info := FilteringInfo{}

	// If we got consistent port from multiple servers with different IPs,
	// it suggests endpoint-independent mapping (prerequisite for EIF)
	if len(results) < 2 {
		return info
	}

	// Group by server IP
	serverIPs := map[string]bool{}
	for _, r := range results {
		if r.Error == nil {
			host, _, _ := net.SplitHostPort(r.Server)
			addrs, err := net.LookupHost(host)
			if err == nil && len(addrs) > 0 {
				serverIPs[addrs[0]] = true
			}
		}
	}

	// Check if mapping is consistent across different server IPs
	ports := map[int]bool{}
	for _, r := range results {
		if r.Error == nil {
			ports[r.PublicPort] = true
		}
	}

	if len(ports) == 1 && len(serverIPs) >= 2 {
		// Same port returned by servers at different IPs → endpoint-independent mapping
		// Without a proper STUN server with CHANGE-REQUEST, we infer:
		// - If port preserving → likely Full Cone (EIF)
		// - If consistent but not preserving → likely Restricted or Port-Restricted
		info.EndpointIndependent = true
	} else if len(ports) > 1 {
		// Different ports per destination → endpoint-dependent (Symmetric)
		info.AddressPortDependent = true
	}

	return info
}

// ════════════════════════════════════════════════════════════════
// Hairpin & Binding Lifetime Tests
// ════════════════════════════════════════════════════════════════

func testHairpin(publicAddr string) bool {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return false
	}
	defer conn.Close()

	addr, err := net.ResolveUDPAddr("udp4", publicAddr)
	if err != nil {
		return false
	}

	token := make([]byte, 8)
	rand.Read(token)
	msg := append([]byte("HAIRPIN:"), token...)

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	conn.WriteToUDP(msg, addr)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return false
	}
	return bytes.Contains(buf[:n], token)
}

func testBindingLifetime(server string) time.Duration {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return 0
	}
	defer conn.Close()

	r1 := querySTUNWithRetry(conn, server, 2)
	if r1.Error != nil {
		return 0
	}

	time.Sleep(10 * time.Second)

	r2 := querySTUNWithRetry(conn, server, 2)
	if r2.Error != nil {
		return 0
	}

	if r1.PublicAddr == r2.PublicAddr {
		return 10 * time.Second
	}
	return 0
}

// ════════════════════════════════════════════════════════════════
// Port Prediction Accuracy Test
// ════════════════════════════════════════════════════════════════

func testPortPrediction(server string, expectedDelta int) int {
	if expectedDelta == 0 {
		return 100
	}

	// Take 3 samples, predict the 4th, check
	trials := 5
	hits := 0

	for t := 0; t < trials; t++ {
		// Get two reference points
		r1 := querySTUNFresh(server)
		r2 := querySTUNFresh(server)
		if r1.Error != nil || r2.Error != nil {
			continue
		}

		actualDelta := r2.PublicPort - r1.PublicPort
		predicted := r2.PublicPort + actualDelta

		r3 := querySTUNFresh(server)
		if r3.Error != nil {
			continue
		}

		// Allow ±2 margin
		if abs(r3.PublicPort-predicted) <= 2 {
			hits++
		}
	}

	if trials == 0 {
		return 0
	}
	return (hits * 100) / trials
}

// ════════════════════════════════════════════════════════════════
// NAT Classification
// ════════════════════════════════════════════════════════════════

func classifyNAT(report NATReport) string {
	// Check if we're on public internet (no NAT)
	if report.LocalIP != "" {
		for _, r := range report.Results {
			if r.Error == nil && r.PublicIP == report.LocalIP {
				if report.PortConsistent {
					return NATOpen
				}
				return NATSymmetricFirewall
			}
		}
	}

	// Port changes per destination → Symmetric NAT
	if !report.PortConsistent {
		return NATSymmetric
	}

	// Port consistent — some type of Cone NAT
	if report.PortAlloc.Pattern == "port-preserving" {
		// Port-preserving + endpoint-independent = Full Cone
		if report.Filtering.EndpointIndependent {
			return NATFullCone
		}
		// Port-preserving but can't confirm filtering → assume Full Cone
		return NATFullCone
	}

	// Port consistent but not port-preserving
	if report.Filtering.EndpointIndependent {
		return NATRestrictedCone
	}

	// Default: can't distinguish restricted from port-restricted without
	// multi-address STUN server, but port-consistent suggests cone NAT
	// If filtering analysis shows address-dependent → Restricted Cone
	if report.Filtering.AddressDependent {
		return NATRestrictedCone
	}

	return NATPortRestricted
}

// ════════════════════════════════════════════════════════════════
// Hole Punch Scoring
// ════════════════════════════════════════════════════════════════

func scoreHolePunch(report NATReport) (int, string, string) {
	score := 0

	switch report.NATType {
	case NATOpen:
		score = 100
	case NATFullCone:
		score = 95
	case NATRestrictedCone:
		score = 85
	case NATPortRestricted:
		score = 65
	case NATSymmetric:
		score = 30
	case NATSymmetricFirewall:
		score = 25
	case NATBlocked:
		score = 0
	}

	// Adjust for port allocation
	if report.NATType == NATSymmetric {
		switch report.PortAlloc.Pattern {
		case "port-preserving":
			score += 25 // unusual but great
		case "sequential":
			score += 20 // predictable, birthday attack works well
		case "random":
			score -= 10 // worst case
		}
	}

	// Adjust for binding lifetime
	if report.BindingLifetime > 0 {
		score += 5
	}

	// Adjust for hairpin
	if report.HairpinOK {
		score += 2
	}

	// Clamp
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}

	// Map to difficulty and probability
	var difficulty, prob string
	switch {
	case score >= 90:
		difficulty = "Easy"
		prob = "Very High"
	case score >= 75:
		difficulty = "Easy"
		prob = "High"
	case score >= 60:
		difficulty = "Medium"
		prob = "Medium"
	case score >= 40:
		difficulty = "Hard"
		prob = "Medium"
	case score >= 20:
		difficulty = "Very Hard"
		prob = "Low"
	case score > 0:
		difficulty = "Very Hard"
		prob = "Very Low"
	default:
		difficulty = "Impossible"
		prob = "None"
	}

	return score, difficulty, prob
}

// ════════════════════════════════════════════════════════════════
// Report Output
// ════════════════════════════════════════════════════════════════

func printReport(report NATReport) {
	fmt.Printf("\n\n  %s%s╔══════════════════════════════════════════════╗%s\n", bold, cyan, reset)
	fmt.Printf("  %s%s║%s  %s%sNAT Diagnostic Report%s                        %s%s║%s\n", bold, cyan, reset, bold, white, reset, bold, cyan, reset)
	fmt.Printf("  %s%s╚══════════════════════════════════════════════╝%s\n\n", bold, cyan, reset)

	// ─── Network Info ────────────────────────────────────
	fmt.Printf("  %s%s  Network Information%s\n", bold, white, reset)
	fmt.Printf("  %s──────────────────────────────────────────────%s\n", gray, reset)
	fmt.Printf("  %-24s %s\n", "  Local IP:", report.LocalIP)

	seen := map[string]bool{}
	for _, r := range report.Results {
		if r.Error == nil && !seen[r.PublicAddr] {
			seen[r.PublicAddr] = true
			fmt.Printf("  %-24s %s%s%s\n", "  Public Address:", bold, r.PublicAddr, reset)
		}
	}
	fmt.Println()

	// ─── NAT Type ────────────────────────────────────────
	fmt.Printf("  %s%s  NAT Classification%s\n", bold, white, reset)
	fmt.Printf("  %s──────────────────────────────────────────────%s\n", gray, reset)

	natColor := green
	natIcon := "✓"
	natDesc := ""
	switch report.NATType {
	case NATOpen:
		natDesc = "No NAT — direct internet access"
	case NATFullCone:
		natDesc = "Any external host can send through the mapped port"
	case NATRestrictedCone:
		natColor = green
		natDesc = "Only hosts you've contacted can reply (IP filter)"
	case NATPortRestricted:
		natColor = yellow
		natIcon = "~"
		natDesc = "Only exact IP:port you've contacted can reply"
	case NATSymmetric:
		natColor = red
		natIcon = "✗"
		natDesc = "Different external port per destination — hardest to traverse"
	case NATSymmetricFirewall:
		natColor = red
		natIcon = "!"
		natDesc = "Public IP but port varies — unusual configuration"
	case NATBlocked:
		natColor = red
		natIcon = "✗"
		natDesc = "UDP traffic is blocked entirely"
	}
	fmt.Printf("  %s  %s%s %-20s %s%s%s\n", "", natColor, natIcon, "NAT Type:", bold+natColor, report.NATType, reset)
	fmt.Printf("  %s  %s%s%s\n", "", "  ", gray, natDesc+reset)
	fmt.Println()

	// Mapping details
	mark := func(ok bool, yesText, noText string) string {
		if ok {
			return fmt.Sprintf("%s✓ %s%s", green, yesText, reset)
		}
		return fmt.Sprintf("%s✗ %s%s", red, noText, reset)
	}

	fmt.Printf("    %-24s %s\n", "Port Mapping:", mark(report.PortConsistent, "Endpoint-Independent", "Endpoint-Dependent"))
	fmt.Printf("    %-24s %s\n", "IP Mapping:", mark(report.IPConsistent, "Consistent", "Varies per destination"))
	fmt.Printf("    %-24s %s\n", "Port Allocation:", portAllocStr(report.PortAlloc))
	fmt.Printf("    %-24s %s\n", "Hairpin NAT:", mark(report.HairpinOK, "Supported", "Not supported"))
	if report.BindingLifetime > 0 {
		fmt.Printf("    %-24s %s✓ >%s%s\n", "Binding Lifetime:", green, report.BindingLifetime, reset)
	}
	fmt.Println()

	// ─── P2P Assessment ──────────────────────────────────
	fmt.Printf("  %s%s  P2P Hole Punch Assessment%s\n", bold, white, reset)
	fmt.Printf("  %s──────────────────────────────────────────────%s\n", gray, reset)

	// Score bar
	fmt.Printf("    Score: %s %s%s%d/100%s\n",
		progressBar(report.Score, 100, 25),
		bold, scoreColor(report.Score), report.Score, reset)

	// Difficulty
	fmt.Printf("    Difficulty:       %s  %s%s%s%s\n",
		difficultyStars(report.Difficulty),
		bold, difficultyColor(report.Difficulty), report.Difficulty, reset)

	// Success probability
	fmt.Printf("    Success Rate:     %s%s%s%s\n",
		bold, probColor(report.HolePunchProb), report.HolePunchProb, reset)

	fmt.Println()

	// ─── STUN Max Strategy ───────────────────────────────
	fmt.Printf("  %s%s  STUN Max Strategy%s\n", bold, white, reset)
	fmt.Printf("  %s──────────────────────────────────────────────%s\n", gray, reset)
	printStrategy(report)
	fmt.Println()

	// ─── Compatibility Matrix ────────────────────────────
	fmt.Printf("  %s%s  Peer Compatibility Matrix%s\n", bold, white, reset)
	fmt.Printf("  %s──────────────────────────────────────────────%s\n", gray, reset)
	printCompatibilityMatrix(report)

	// ─── Latency ─────────────────────────────────────────
	printLatencySummary(report)

	// ─── Recommendation ──────────────────────────────────
	fmt.Printf("  %s%s  Recommendation%s\n", bold, white, reset)
	fmt.Printf("  %s──────────────────────────────────────────────%s\n", gray, reset)
	printRecommendation(report)

	fmt.Println()
}

func portAllocStr(p PortAllocInfo) string {
	switch p.Pattern {
	case "port-preserving":
		return fmt.Sprintf("%sPort Preserving%s", green, reset)
	case "sequential":
		return fmt.Sprintf("%sSequential (Δ=%d)%s", yellow, p.Delta, reset)
	case "random":
		return fmt.Sprintf("%sRandom%s", red, reset)
	}
	return fmt.Sprintf("%sUnknown%s", gray, reset)
}

func scoreColor(score int) string {
	if score >= 75 {
		return green
	} else if score >= 50 {
		return yellow
	}
	return red
}

func difficultyColor(d string) string {
	switch d {
	case "Easy":
		return green
	case "Medium":
		return yellow
	case "Hard":
		return yellow
	case "Very Hard":
		return red
	case "Impossible":
		return red
	}
	return gray
}

func probColor(p string) string {
	switch p {
	case "Very High", "High":
		return green
	case "Medium":
		return yellow
	case "Low", "Very Low":
		return red
	case "None":
		return red
	}
	return gray
}

func printStrategy(report NATReport) {
	type strategy struct {
		name    string
		enabled bool
		desc    string
	}

	strategies := []strategy{
		{
			name:    "Phase 1: Rapid Burst",
			enabled: report.NATType != NATBlocked,
			desc:    "20 packets in 500ms from main socket",
		},
		{
			name:    "Phase 2: Birthday Attack",
			enabled: report.NATType == NATSymmetric || report.NATType == NATPortRestricted,
			desc:    "8 parallel sockets × 5 packets each",
		},
		{
			name:    "Phase 3: Port Prediction",
			enabled: report.NATType == NATSymmetric && report.PortAlloc.Pattern == "sequential",
			desc:    fmt.Sprintf("Predict ±10 ports around target (Δ=%d)", report.PortAlloc.Delta),
		},
		{
			name:    "Fallback: Server Relay",
			enabled: true,
			desc:    "Automatic fallback if P2P fails after 5 attempts",
		},
	}

	for _, s := range strategies {
		if s.enabled {
			fmt.Printf("    %s●%s %-28s %s%s%s\n", green, reset, s.name, gray, s.desc, reset)
		} else {
			fmt.Printf("    %s○%s %-28s %s%s (not needed)%s\n", gray, reset, s.name, gray, s.desc, reset)
		}
	}
}

func printCompatibilityMatrix(report NATReport) {
	type compat struct {
		peerNAT string
		result  string
		color   string
		note    string
	}

	var matrix []compat
	switch report.NATType {
	case NATOpen, NATFullCone:
		matrix = []compat{
			{"Open / Full Cone", "✓ Direct", green, "Immediate connection"},
			{"Restricted Cone", "✓ Direct", green, "After initial contact"},
			{"Port Restricted Cone", "✓ Direct", green, "After mutual contact"},
			{"Symmetric (Sequential)", "✓ Direct", green, "Port prediction helps"},
			{"Symmetric (Random)", "✓ Direct", green, "Birthday attack works"},
		}
	case NATRestrictedCone:
		matrix = []compat{
			{"Open / Full Cone", "✓ Direct", green, "Immediate"},
			{"Restricted Cone", "✓ Direct", green, "Mutual burst"},
			{"Port Restricted Cone", "✓ Direct", green, "Mutual burst"},
			{"Symmetric (Sequential)", "~ Maybe", yellow, "Birthday + prediction"},
			{"Symmetric (Random)", "✗ Relay", red, "Too many combinations"},
		}
	case NATPortRestricted:
		matrix = []compat{
			{"Open / Full Cone", "✓ Direct", green, "Immediate"},
			{"Restricted Cone", "✓ Direct", green, "Mutual burst"},
			{"Port Restricted Cone", "~ Maybe", yellow, "Tight timing needed"},
			{"Symmetric (Sequential)", "✗ Relay", red, "Port mismatch"},
			{"Symmetric (Random)", "✗ Relay", red, "Impossible to match"},
		}
	case NATSymmetric:
		if report.PortAlloc.Pattern == "sequential" {
			matrix = []compat{
				{"Open / Full Cone", "✓ Direct", green, "Peer accepts any source"},
				{"Restricted Cone", "~ Maybe", yellow, "Port prediction needed"},
				{"Port Restricted Cone", "✗ Relay", red, "Both sides unpredictable"},
				{"Symmetric (Sequential)", "✗ Relay", red, "Double prediction fails"},
				{"Symmetric (Random)", "✗ Relay", red, "No viable strategy"},
			}
		} else {
			matrix = []compat{
				{"Open / Full Cone", "✓ Direct", green, "Peer accepts any source"},
				{"Restricted Cone", "~ Maybe", yellow, "Birthday attack needed"},
				{"Port Restricted Cone", "✗ Relay", red, "Too restrictive"},
				{"Symmetric", "✗ Relay", red, "Both sides unpredictable"},
			}
		}
	case NATSymmetricFirewall:
		matrix = []compat{
			{"Open / Full Cone", "~ Maybe", yellow, "Firewall may interfere"},
			{"Restricted Cone", "✗ Relay", red, "Port mismatch"},
			{"Port Restricted Cone", "✗ Relay", red, "Port mismatch"},
			{"Symmetric", "✗ Relay", red, "Both sides vary"},
		}
	default:
		matrix = []compat{
			{"Any NAT type", "✗ Blocked", red, "UDP not available"},
		}
	}

	// Table header
	fmt.Printf("    %-30s %-12s %s\n",
		fmt.Sprintf("%sPeer NAT Type%s", underline, reset),
		fmt.Sprintf("%sResult%s", underline, reset),
		fmt.Sprintf("%sNote%s", underline, reset))

	for _, m := range matrix {
		fmt.Printf("    %-30s %s%-12s%s %s%s%s\n",
			m.peerNAT, m.color, m.result, reset, gray, m.note, reset)
	}
	fmt.Println()
}

func printLatencySummary(report NATReport) {
	var latencies []time.Duration
	for _, r := range report.Results {
		if r.Error == nil {
			latencies = append(latencies, r.Latency)
		}
	}
	if len(latencies) == 0 {
		return
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	minL := latencies[0]
	maxL := latencies[len(latencies)-1]
	var sum time.Duration
	for _, l := range latencies {
		sum += l
	}
	avgL := sum / time.Duration(len(latencies))

	// Jitter (standard deviation)
	var variance float64
	avgNs := float64(avgL.Nanoseconds())
	for _, l := range latencies {
		d := float64(l.Nanoseconds()) - avgNs
		variance += d * d
	}
	jitter := time.Duration(math.Sqrt(variance/float64(len(latencies)))) * time.Nanosecond

	fmt.Printf("  %s%s  STUN Latency%s\n", bold, white, reset)
	fmt.Printf("  %s──────────────────────────────────────────────%s\n", gray, reset)
	fmt.Printf("    Min: %s%-8s%s  Avg: %s%-8s%s  Max: %s%-8s%s  Jitter: %s%s%s\n",
		green, minL.Round(time.Millisecond), reset,
		cyan, avgL.Round(time.Millisecond), reset,
		latencyColor(maxL), maxL.Round(time.Millisecond), reset,
		jitterColor(jitter), jitter.Round(time.Millisecond), reset)
	fmt.Println()
}

func latencyColor(d time.Duration) string {
	if d < 100*time.Millisecond {
		return green
	} else if d < 300*time.Millisecond {
		return yellow
	}
	return red
}

func jitterColor(d time.Duration) string {
	if d < 20*time.Millisecond {
		return green
	} else if d < 50*time.Millisecond {
		return yellow
	}
	return red
}

func printRecommendation(report NATReport) {
	switch {
	case report.Score >= 85:
		fmt.Printf("    %s✓ Your network is excellent for P2P connections.%s\n", green, reset)
		fmt.Printf("    %s  Direct tunnel to most peers will succeed on first attempt.%s\n", green, reset)
		if report.NATType == NATOpen {
			fmt.Printf("    %s  No NAT traversal needed — you have a public IP.%s\n", green, reset)
		}
	case report.Score >= 60:
		fmt.Printf("    %s~ Your network supports P2P in most scenarios.%s\n", yellow, reset)
		fmt.Printf("    %s  Direct connections work with Full/Restricted Cone peers.%s\n", yellow, reset)
		fmt.Printf("    %s  Symmetric NAT peers may require server relay.%s\n", yellow, reset)
		fmt.Printf("    %s  STUN Max will auto-fallback to relay if P2P fails.%s\n", gray, reset)
	case report.Score >= 35:
		fmt.Printf("    %s! P2P connections are possible but challenging.%s\n", yellow, reset)
		fmt.Printf("    %s  STUN Max uses Birthday Attack + Port Prediction to maximize success.%s\n", yellow, reset)
		fmt.Printf("    %s  Peers behind Symmetric NAT will use server relay.%s\n", yellow, reset)
		fmt.Printf("    %s  Consider deploying a custom STUN server for better results.%s\n", gray, reset)
	case report.Score > 0:
		fmt.Printf("    %s✗ P2P hole punching is unlikely to succeed from this network.%s\n", red, reset)
		fmt.Printf("    %s  Most connections will use server relay (encrypted, but higher latency).%s\n", red, reset)
		fmt.Printf("\n    %sSuggestions:%s\n", bold, reset)
		fmt.Printf("    %s  • Try a different network (mobile hotspot, different WiFi)%s\n", gray, reset)
		fmt.Printf("    %s  • Deploy STUN Max server closer to your region%s\n", gray, reset)
		fmt.Printf("    %s  • Check if your router supports UPnP or NAT-PMP%s\n", gray, reset)
		fmt.Printf("    %s  • Consider port forwarding on your router%s\n", gray, reset)
	default:
		fmt.Printf("    %s✗ UDP is blocked — P2P connections are not possible.%s\n", red, reset)
		fmt.Printf("    %s  All data will flow through the server relay (TCP WebSocket).%s\n", red, reset)
		fmt.Printf("\n    %sSuggestions:%s\n", bold, reset)
		fmt.Printf("    %s  • Check firewall/security software blocking UDP%s\n", gray, reset)
		fmt.Printf("    %s  • Corporate networks often block UDP — try a different network%s\n", gray, reset)
		fmt.Printf("    %s  • STUN Max relay mode still provides full functionality%s\n", gray, reset)
	}

	fmt.Println()

	// NAT type explanation
	fmt.Printf("  %s%s  NAT Type Explanation%s\n", bold, white, reset)
	fmt.Printf("  %s──────────────────────────────────────────────%s\n", gray, reset)
	printNATExplanation(report.NATType)
}

func printNATExplanation(natType string) {
	type natInfo struct {
		name  string
		color string
		icon  string
		desc  string
		p2p   string
	}

	allTypes := []natInfo{
		{NATOpen, green, "◆", "No NAT — device has a public IP address", "All peers can connect directly"},
		{NATFullCone, green, "◆", "One-to-one NAT, any external host can send to mapped port", "All peers can connect directly"},
		{NATRestrictedCone, green, "◇", "Like Full Cone, but only allows IPs you've contacted", "Most peers OK, some Symmetric may fail"},
		{NATPortRestricted, yellow, "◇", "Like Restricted, but also filters by source port", "Works with Cone NATs, fails with Symmetric"},
		{NATSymmetric, red, "▪", "Different external mapping per destination — hardest NAT", "Only works with Cone NATs (via prediction)"},
		{NATSymmetricFirewall, red, "▪", "Public IP but firewall varies the port per destination", "Difficult — similar to Symmetric NAT"},
		{NATBlocked, red, "✗", "UDP traffic is entirely blocked", "No P2P possible, relay only"},
	}

	for _, t := range allTypes {
		marker := " "
		if t.name == natType {
			marker = fmt.Sprintf("%s►%s", bold+t.color, reset)
		} else {
			marker = " "
		}

		nameStyle := gray
		if t.name == natType {
			nameStyle = bold + t.color
		}

		fmt.Printf("   %s %s%s %s%s\n", marker, nameStyle, t.icon, t.name, reset)
		if t.name == natType {
			fmt.Printf("       %s%s%s\n", t.color, t.desc, reset)
			fmt.Printf("       %sP2P: %s%s\n", dim, t.p2p, reset)
		}
	}
	fmt.Println()
}

// ════════════════════════════════════════════════════════════════
// Helpers
// ════════════════════════════════════════════════════════════════

func getLocalIP() string {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return "unknown"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Parallel STUN queries
func querySTUNParallel(servers []string) []STUNResult {
	var wg sync.WaitGroup
	results := make([]STUNResult, len(servers))
	for i, server := range servers {
		wg.Add(1)
		go func(idx int, srv string) {
			defer wg.Done()
			results[idx] = querySTUNFresh(srv)
		}(i, server)
	}
	wg.Wait()
	return results
}

// Ensure clean exit
func init() {
	// Reset terminal colors on exit
	go func() {
		c := make(chan os.Signal, 1)
		// We don't import signal package to keep it simple,
		// but colors auto-reset via ANSI reset codes in output
		_ = c
	}()
}
