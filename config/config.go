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

type Config struct {
	Port         int    `json:"port"`
	BindAddr     string `json:"bind_addr"`
	ExternalHost string `json:"external_host"` // LAN IP or hostname for QR pairing (e.g. "192.168.1.50")
	Name         string `json:"name"`          // Friendly display name (e.g. "Home NAS", "Dev Server")

	AgentID string `json:"agent_id"`

	APIKey    string `json:"api_key"`
	JWTSecret string `json:"jwt_secret"`

	DockerSocket string `json:"docker_socket"`

	LogLevel string `json:"log_level"`

	RelayURL   string `json:"relay_url,omitempty"`   // e.g. "wss://relay.dockertab.com"
	RelayToken string `json:"relay_token,omitempty"` // Auth token for relay server

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

// DefaultRelayURL is the hosted relay. Override with DOCKERTAB_RELAY_URL for self-hosted deployments.
const DefaultRelayURL = "wss://iosrelay.dockertab.app"

func Load() (*Config, error) {
	var loadErr error
	once.Do(func() {
		instance = &Config{
			Port:         8377,
			BindAddr:     "0.0.0.0",
			DockerSocket: "/var/run/docker.sock",
			LogLevel:     "info",
		}

		configPath := getConfigPath()
		if data, err := os.ReadFile(configPath); err == nil {
			if err := json.Unmarshal(data, instance); err != nil {
				loadErr = fmt.Errorf("invalid config file: %w", err)
				return
			}
		}

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

		if instance.AgentID == "" {
			instance.AgentID = generateSecret(4) // 8-char hex
		}
		if instance.APIKey == "" {
			instance.APIKey = generateSecret(32)
		}
		if instance.JWTSecret == "" {
			instance.JWTSecret = generateSecret(64)
		}
		// relay_token is stable across restarts; included in the pair response so the
		// iOS app can provision relay access after subscribing.
		if instance.RelayToken == "" {
			instance.RelayToken = generateSecret(32)
		}

		// Always prefer the compiled-in default so stale saved values don't persist
		// across binary updates. Explicit custom URLs (via env or config) are preserved.
		knownDefaults := []string{"", "wss://relay.dockertab.app"}
		for _, old := range knownDefaults {
			if instance.RelayURL == old {
				instance.RelayURL = DefaultRelayURL
				break
			}
		}

		if err := instance.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save config: %v\n", err)
		}
	})

	return instance, loadErr
}

// Save writes the config to disk. The default relay URL is not persisted so
// future binary updates automatically pick up a new DefaultRelayURL.
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

func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.BindAddr, c.Port)
}

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
// Inside Docker it resolves host.docker.internal; otherwise it scans interfaces,
// skipping Docker bridges, and prefers 192.168.x.x then 10.x.x.x.
func detectLANIP() string {
	// /.dockerenv is created by the Docker runtime.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		if addrs, err := net.LookupHost("host.docker.internal"); err == nil && len(addrs) > 0 {
			ip := addrs[0]
			// 192.168.65.x is Docker Desktop's internal VM gateway — not the LAN IP.
			if !strings.HasPrefix(ip, "192.168.65.") {
				return ip
			}
		}
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
