package tailscale

import (
	"encoding/json"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Status holds the detected Tailscale state.
type Status struct {
	Available bool   `json:"available"`
	IP        string `json:"ip,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Domain    string `json:"domain,omitempty"` // e.g. "homelab.tail1234.ts.net"
}

// Monitor periodically detects Tailscale status.
type Monitor struct {
	mu     sync.RWMutex
	status Status
	done   chan struct{}
}

// NewMonitor creates a Tailscale monitor that checks status at startup and periodically.
func NewMonitor() *Monitor {
	m := &Monitor{
		done: make(chan struct{}),
	}
	m.refresh()
	return m
}

// Start begins periodic Tailscale detection (every 5 minutes).
func (m *Monitor) Start() {
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.refresh()
			case <-m.done:
				return
			}
		}
	}()
}

// Stop ends the periodic detection.
func (m *Monitor) Stop() {
	close(m.done)
}

// GetStatus returns the current Tailscale status.
func (m *Monitor) GetStatus() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Monitor) refresh() {
	status := detect()
	m.mu.Lock()
	m.status = status
	m.mu.Unlock()

	if status.Available {
		log.Printf("[tailscale] detected: %s (%s)", status.IP, status.Domain)
	}
}

// tailscaleStatusJSON matches the relevant fields from `tailscale status --json`.
type tailscaleStatusJSON struct {
	Self struct {
		TailscaleIPs []string `json:"TailscaleIPs"`
		DNSName      string   `json:"DNSName"`
		HostName     string   `json:"HostName"`
	} `json:"Self"`
}

func detect() Status {
	// Try `tailscale status --json` first
	if s := detectFromCLI(); s.Available {
		return s
	}
	// Fallback: scan network interfaces for Tailscale CGNAT range
	if ip := detectFromInterfaces(); ip != "" {
		return Status{Available: true, IP: ip}
	}
	return Status{Available: false}
}

func detectFromCLI() Status {
	cmd := exec.Command("tailscale", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return Status{Available: false}
	}

	var ts tailscaleStatusJSON
	if err := json.Unmarshal(output, &ts); err != nil {
		return Status{Available: false}
	}

	if len(ts.Self.TailscaleIPs) == 0 {
		return Status{Available: false}
	}

	domain := strings.TrimSuffix(ts.Self.DNSName, ".")

	return Status{
		Available: true,
		IP:        ts.Self.TailscaleIPs[0],
		Hostname:  ts.Self.HostName,
		Domain:    domain,
	}
}

// detectFromInterfaces scans for an IP in the 100.64.0.0/10 range (Tailscale CGNAT).
func detectFromInterfaces() string {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")

	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.To4() != nil && cgnat.Contains(ipnet.IP) {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}
