package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Config holds the agent's runtime configuration.
type Config struct {
	// Server settings
	Port         int    `json:"port"`
	BindAddr     string `json:"bind_addr"`
	ExternalHost string `json:"external_host"` // LAN IP or hostname for QR pairing (e.g. "192.168.1.50")
	Name         string `json:"name"`          // Friendly display name (e.g. "Home NAS", "Dev Server")

	// Identity
	AgentID string `json:"agent_id"`

	// Security
	APIKey    string `json:"api_key"`
	JWTSecret string `json:"jwt_secret"`

	// Docker
	DockerSocket string `json:"docker_socket"`

	// Logging
	LogLevel string `json:"log_level"`

	// Premium
	PremiumEnabled   bool   `json:"premium_enabled"`
	PremiumToken     string `json:"premium_token,omitempty"`     // App Store receipt (base64)
	PremiumActivated string `json:"premium_activated,omitempty"` // ISO 8601

	// Relay (premium)
	RelayURL   string `json:"relay_url,omitempty"`   // e.g. "wss://relay.dockertab.com"
	RelayToken string `json:"relay_token,omitempty"` // Auth token for relay server

	// Tailscale (premium)
	TailscaleEnabled bool `json:"tailscale_enabled"`

	// APNs push notifications (optional — disabled when not configured)
	APNsKeyFile string `json:"apns_key_file,omitempty"` // Path to .p8 private key
	APNsKeyID   string `json:"apns_key_id,omitempty"`   // 10-char key ID
	APNsTeamID  string `json:"apns_team_id,omitempty"`  // 10-char team ID
	APNsSandbox bool   `json:"apns_sandbox"`            // true = sandbox (dev), false = production
}

var (
	instance *Config
	once     sync.Once
)

const configFileName = "dockertab-agent.json"

// DefaultRelayURL is the DockerTab hosted relay. Agents connect here automatically.
// Override with DOCKERTAB_RELAY_URL for self-hosted relay deployments.
const DefaultRelayURL = "wss://dockerrelay.siggnet.com"

// Load reads configuration from file, environment, or generates defaults.
func Load() (*Config, error) {
	var loadErr error
	once.Do(func() {
		instance = &Config{
			Port:         8377,
			BindAddr:     "0.0.0.0",
			DockerSocket: "/var/run/docker.sock",
			LogLevel:     "info",
		}

		// Try loading from config file
		configPath := getConfigPath()
		if data, err := os.ReadFile(configPath); err == nil {
			if err := json.Unmarshal(data, instance); err != nil {
				loadErr = fmt.Errorf("invalid config file: %w", err)
				return
			}
		}

		// Environment overrides
		if v := os.Getenv("DOCKERTAB_PORT"); v != "" {
			fmt.Sscanf(v, "%d", &instance.Port)
		}
		if v := os.Getenv("DOCKERTAB_BIND"); v != "" {
			instance.BindAddr = v
		}
		if v := os.Getenv("DOCKERTAB_HOST"); v != "" {
			instance.ExternalHost = v
		}
		if v := os.Getenv("DOCKERTAB_NAME"); v != "" {
			instance.Name = v
		}
		if v := os.Getenv("DOCKERTAB_API_KEY"); v != "" {
			instance.APIKey = v
		}
		if v := os.Getenv("DOCKERTAB_JWT_SECRET"); v != "" {
			instance.JWTSecret = v
		}
		if v := os.Getenv("DOCKERTAB_DOCKER_SOCKET"); v != "" {
			instance.DockerSocket = v
		}
		if v := os.Getenv("DOCKERTAB_LOG_LEVEL"); v != "" {
			instance.LogLevel = v
		}
		if v := os.Getenv("DOCKERTAB_RELAY_URL"); v != "" {
			instance.RelayURL = v
		}
		if v := os.Getenv("DOCKERTAB_RELAY_TOKEN"); v != "" {
			instance.RelayToken = v
		}
		if v := os.Getenv("DOCKERTAB_PREMIUM_ENABLED"); v == "true" || v == "1" {
			instance.PremiumEnabled = true
		}
		if v := os.Getenv("DOCKERTAB_TAILSCALE_ENABLED"); v == "true" || v == "1" {
			instance.TailscaleEnabled = true
		}
		if v := os.Getenv("DOCKERTAB_APNS_KEY_FILE"); v != "" {
			instance.APNsKeyFile = v
		}
		if v := os.Getenv("DOCKERTAB_APNS_KEY_ID"); v != "" {
			instance.APNsKeyID = v
		}
		if v := os.Getenv("DOCKERTAB_APNS_TEAM_ID"); v != "" {
			instance.APNsTeamID = v
		}
		if v := os.Getenv("DOCKERTAB_APNS_SANDBOX"); v == "true" || v == "1" {
			instance.APNsSandbox = true
		}

		// Generate stable agent ID if not set
		if instance.AgentID == "" {
			instance.AgentID = generateSecret(4) // 8-char hex
		}

		// Generate secrets if not set
		if instance.APIKey == "" {
			instance.APIKey = generateSecret(32)
		}
		if instance.JWTSecret == "" {
			instance.JWTSecret = generateSecret(64)
		}
		// relay_token is auto-generated once and stable. It's included in the pair
		// response so the iOS app can provision relay access after subscribing.
		if instance.RelayToken == "" {
			instance.RelayToken = generateSecret(32)
		}

		// Default relay URL — always use the compiled-in default unless the user
		// has explicitly set a custom URL (via env var or a value that differs from
		// any known previous default). This means the saved config never locks in a
		// stale default; future binary updates automatically pick up the new URL.
		knownDefaults := []string{"", "wss://relay.dockertab.app"}
		for _, old := range knownDefaults {
			if instance.RelayURL == old {
				instance.RelayURL = DefaultRelayURL
				break
			}
		}

		// Persist config so secrets are stable across restarts
		if err := instance.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save config: %v\n", err)
		}
	})

	return instance, loadErr
}

// Save writes the current configuration to disk.
// The default relay URL is intentionally not persisted so that future binary
// updates automatically pick up a new DefaultRelayURL.
func (c *Config) Save() error {
	configPath := getConfigPath()
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	toSave := *c
	if toSave.RelayURL == DefaultRelayURL {
		toSave.RelayURL = ""
	}
	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0600)
}

// ListenAddr returns the formatted address string for the HTTP server.
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.BindAddr, c.Port)
}

// PairingHost returns the host:port the iOS app should connect to.
// Uses ExternalHost if set, otherwise auto-detects the LAN IP.
func (c *Config) PairingHost() string {
	if c.ExternalHost != "" {
		return fmt.Sprintf("%s:%d", c.ExternalHost, c.Port)
	}
	if ip := detectLANIP(); ip != "" {
		return fmt.Sprintf("%s:%d", ip, c.Port)
	}
	return c.ListenAddr()
}

// detectLANIP returns the best LAN IPv4 for QR pairing.
// When running inside a Docker container it resolves host.docker.internal
// (Docker Desktop's built-in alias for the host machine). Otherwise it
// iterates network interfaces, skips Docker bridge ranges, and prefers
// 192.168.x.x then 10.x.x.x.
func detectLANIP() string {
	// Detect Docker container: /.dockerenv is created by the Docker runtime.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		if addrs, err := net.LookupHost("host.docker.internal"); err == nil && len(addrs) > 0 {
			ip := addrs[0]
			// 192.168.65.x is Docker Desktop's internal VM gateway — not reachable
			// from the LAN. Skip it so the caller logs a helpful warning instead.
			if !strings.HasPrefix(ip, "192.168.65.") {
				return ip
			}
		}
		// Cannot auto-detect real LAN IP from inside Docker Desktop.
		// Caller will log a warning asking the user to set DOCKERTAB_HOST.
		return ""
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var candidates []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Skip Docker/virtual interfaces by name
		n := iface.Name
		if strings.HasPrefix(n, "docker") || strings.HasPrefix(n, "br-") ||
			strings.HasPrefix(n, "veth") || strings.HasPrefix(n, "virbr") {
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
			ip4 := ip.To4()
			if ip4 == nil || ip.IsLoopback() {
				continue
			}
			// Skip Docker bridge range 172.16.0.0/12
			if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
				continue
			}
			candidates = append(candidates, ip4.String())
		}
	}
	for _, c := range candidates {
		if strings.HasPrefix(c, "192.168.") {
			return c
		}
	}
	for _, c := range candidates {
		if strings.HasPrefix(c, "10.") {
			return c
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

func getConfigPath() string {
	if v := os.Getenv("DOCKERTAB_CONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "dockertab", configFileName)
}

// APNsConfigured returns true when all required APNs fields are set.
func (c *Config) APNsConfigured() bool {
	return c.APNsKeyFile != "" && c.APNsKeyID != "" && c.APNsTeamID != ""
}

func generateSecret(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		panic(fmt.Sprintf("failed to generate secret: %v", err))
	}
	return hex.EncodeToString(bytes)
}
