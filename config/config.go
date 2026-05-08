package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Config struct {
	Port         int    `json:"port"`
	BindAddr     string `json:"bind_addr"`
	ExternalHost string `json:"external_host"`
	Name         string `json:"name"`

	AgentID string `json:"agent_id"`

	APIKey    string `json:"api_key"`
	JWTSecret string `json:"jwt_secret"`

	DockerSocket string `json:"docker_socket"`

	LogLevel string `json:"log_level"`

	RelayURL   string `json:"relay_url,omitempty"`
	RelayToken string `json:"relay_token,omitempty"`

	APNsKeyFile string `json:"apns_key_file,omitempty"`
	APNsKeyID   string `json:"apns_key_id,omitempty"`
	APNsTeamID  string `json:"apns_team_id,omitempty"`
	APNsSandbox bool   `json:"apns_sandbox"` // true = sandbox endpoint
}

var (
	instance *Config
	once     sync.Once
)

const configFileName = "dockertab-agent.json"

const DefaultRelayURL = "wss://iosrelay.dockertab.app"

func NewConfig(path string) (*Config, error) {
	cfg := &Config{
		Port:         8377,
		BindAddr:     "0.0.0.0",
		DockerSocket: "/var/run/docker.sock",
		LogLevel:     "info",
	}

	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("invalid config file: %w", err)
		}
	}

	if v := os.Getenv("DOCKERTAB_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil && port > 0 && port <= 65535 {
			cfg.Port = port
		} else {
			fmt.Fprintf(os.Stderr, "warning: invalid DOCKERTAB_PORT %q, using default %d\n", v, cfg.Port)
		}
	}
	if v := os.Getenv("DOCKERTAB_BIND"); v != "" {
		cfg.BindAddr = v
	}
	if v := os.Getenv("DOCKERTAB_HOST"); v != "" {
		cfg.ExternalHost = v
	}
	if v := os.Getenv("DOCKERTAB_NAME"); v != "" {
		cfg.Name = v
	}
	if v := os.Getenv("DOCKERTAB_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("DOCKERTAB_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := os.Getenv("DOCKERTAB_DOCKER_SOCKET"); v != "" {
		cfg.DockerSocket = v
	}
	if v := os.Getenv("DOCKERTAB_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("DOCKERTAB_RELAY_URL"); v != "" {
		cfg.RelayURL = v
	}
	if v := os.Getenv("DOCKERTAB_RELAY_TOKEN"); v != "" {
		cfg.RelayToken = v
	}
	if v := os.Getenv("DOCKERTAB_APNS_KEY_FILE"); v != "" {
		cfg.APNsKeyFile = v
	}
	if v := os.Getenv("DOCKERTAB_APNS_KEY_ID"); v != "" {
		cfg.APNsKeyID = v
	}
	if v := os.Getenv("DOCKERTAB_APNS_TEAM_ID"); v != "" {
		cfg.APNsTeamID = v
	}
	if v := os.Getenv("DOCKERTAB_APNS_SANDBOX"); v == "true" || v == "1" {
		cfg.APNsSandbox = true
	}

	if cfg.AgentID == "" {
		cfg.AgentID = generateSecret(4)
	}
	if cfg.APIKey == "" {
		cfg.APIKey = generateSecret(32)
	}
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = generateSecret(64)
	}
	if cfg.RelayToken == "" {
		cfg.RelayToken = generateSecret(32)
	}

	if cfg.RelayURL == "" {
		cfg.RelayURL = DefaultRelayURL
	}

	return cfg, nil
}

func Load() (*Config, error) {
	var loadErr error
	once.Do(func() {
		instance, loadErr = NewConfig(getConfigPath())
		if loadErr != nil {
			return
		}
		if err := instance.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save config: %v\n", err)
		}
	})
	return instance, loadErr
}

func (c *Config) Save() error {
	configPath := getConfigPath()
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	toSave := *c
	// Don't persist the default relay URL so binary updates pick up a new default automatically.
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

func detectLANIP() string {
	// Inside Docker, resolve the host machine's LAN IP via the magic hostname.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		if addrs, err := net.LookupHost("host.docker.internal"); err == nil && len(addrs) > 0 {
			ip := addrs[0]
			// 192.168.65.x is Docker Desktop's internal VM bridge, not the user's LAN.
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
