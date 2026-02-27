package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"path/filepath"

	"github.com/dockertab/agent/config"
	"github.com/dockertab/agent/internal/auth"
	"github.com/dockertab/agent/internal/docker"
	"github.com/dockertab/agent/internal/handlers"
	"github.com/dockertab/agent/internal/middleware"
	"github.com/dockertab/agent/internal/notifications"
	"github.com/dockertab/agent/internal/relay"
	"github.com/dockertab/agent/internal/tailscale"
	"github.com/gin-gonic/gin"
	qrterminal "github.com/mdp/qrterminal/v3"
)

const banner = `
  ____             _           _____     _
 |  _ \  ___   ___| | _____ _ |_   _|_ _| |__
 | | | |/ _ \ / __| |/ / _ \ '__| |/ _' | '_ \
 | |_| | (_) | (__|   <  __/ |  | | (_| | |_) |
 |____/ \___/ \___|_|\_\___|_|  |_|\__,_|_.__/
                                    Agent v0.1.0
`

func main() {
	fmt.Print(banner)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Config loaded | Port: %d | Socket: %s", cfg.Port, cfg.DockerSocket)

	dockerClient, err := docker.NewClient(cfg.DockerSocket)
	if err != nil {
		log.Fatalf("Failed to connect to Docker: %v", err)
	}
	defer dockerClient.Close()

	log.Println("Connected to Docker daemon")

	authService := auth.NewService(cfg.JWTSecret)
	handler := handlers.NewHandler(dockerClient, authService, cfg)

	// Push notifications — token store is always created; APNs client is optional
	configDir := filepath.Join(func() string { h, _ := os.UserHomeDir(); return h }(), ".config", "dockertab")
	if v := os.Getenv("DOCKERTAB_CONFIG"); v != "" {
		configDir = filepath.Dir(v)
	}
	tokenStore := notifications.NewTokenStore(configDir)
	handler.TokenStore = tokenStore

	var apnsClient *notifications.APNsClient
	if cfg.APNsConfigured() {
		var apnsErr error
		apnsClient, apnsErr = notifications.NewAPNsClient(cfg.APNsKeyFile, cfg.APNsKeyID, cfg.APNsTeamID)
		if apnsErr != nil {
			log.Printf("WARNING: APNs init failed (%v) — push notifications disabled", apnsErr)
			apnsClient = nil
		}
	}

	if cfg.LogLevel != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(middleware.RequestLogger())
	router.Use(gin.Recovery())
	router.Use(middleware.CORS())

	router.GET("/healthz", handler.Healthz)
	router.POST("/api/v1/pair", handler.Pair)

	api := router.Group("/api/v1")
	api.Use(middleware.JWTAuth(authService))
	{
		// System
		api.GET("/host", handler.GetHostInfo)

		// Containers
		api.GET("/containers", handler.ListContainers)
		api.GET("/containers/:id", handler.GetContainer)
		api.POST("/containers/:id/start", handler.StartContainer)
		api.POST("/containers/:id/stop", handler.StopContainer)
		api.POST("/containers/:id/restart", handler.RestartContainer)
		api.GET("/containers/:id/stats", handler.GetContainerStats)
		api.GET("/containers/:id/logs", handler.GetContainerLogs)
		api.GET("/containers/:id/env", handler.GetContainerEnv)

		// Images
		api.GET("/images", handler.ListImages)

		// Volumes (premium)
		api.GET("/volumes", handler.ListVolumes)

		// WebSocket endpoints
		api.GET("/containers/:id/logs/stream", handler.StreamContainerLogs)
		api.GET("/containers/:id/stats/stream", handler.StreamContainerStats)
		api.GET("/containers/:id/exec", handler.StreamContainerExec)

		// Premium
		api.POST("/premium/activate", handler.ActivatePremium)
		api.GET("/premium/status", handler.GetPremiumStatus)

		// Push notifications
		api.POST("/notifications/register", handler.RegisterDeviceToken)
		api.DELETE("/notifications/unregister", handler.UnregisterDeviceToken)
	}

	var tsMonitor *tailscale.Monitor
	if cfg.TailscaleEnabled && cfg.PremiumEnabled {
		tsMonitor = tailscale.NewMonitor()
		tsMonitor.Start()
		tsStatus := tsMonitor.GetStatus()
		if tsStatus.Available {
			log.Printf("  Tailscale      : %s (%s)", tsStatus.IP, tsStatus.Domain)
		} else {
			log.Println("  Tailscale      : enabled but not detected")
		}
	}

	// Relay client always starts — subscription state is enforced server-side.
	// The agent connects in pending state until a subscriber provisions access via the iOS app.
	relayCtx, relayCancel := context.WithCancel(context.Background())
	relayClient := relay.NewClient(cfg, authService, dockerClient, router, handler.AgentID)
	handler.RelayConnected = relayClient.IsConnected
	handler.RelayRegisterToken = relayClient.RegisterDeviceToken

	// Push notifications: relay-based (preferred) or direct APNs (LAN fallback).
	//
	// The relay holds the developer's APNs key and pushes to all user devices.
	// Direct APNs is a fallback for self-hosted setups without a relay.
	go func() {
		msgs, errs := dockerClient.Events(relayCtx)
		for {
			select {
			case <-relayCtx.Done():
				return
			case err := <-errs:
				if relayCtx.Err() == nil {
					log.Printf("[notifications] Docker events error: %v", err)
				}
				return
			case event, ok := <-msgs:
				if !ok {
					return
				}
				switch event.Action {
				case "start", "stop", "die", "kill":
					relayClient.SendNotification(event.ContainerID, event.ContainerName, event.Action)
				}
			}
		}
	}()

	if apnsClient != nil {
		// Direct APNs path — only used in self-hosted setups without a relay.
		watcher := notifications.NewWatcher(dockerClient, tokenStore, apnsClient, cfg.AgentID, cfg.Name, cfg.APNsSandbox)
		go func() {
			if err := watcher.Start(relayCtx); err != nil && relayCtx.Err() == nil {
				log.Printf("Docker events watcher error: %v", err)
			}
		}()
	}

	log.Println("──────────────────────────────────────────────")
	if cfg.Name != "" {
		log.Printf("  Node name      : %s", cfg.Name)
	}
	log.Printf("  Listening on   : %s", cfg.ListenAddr())
	qrHost := cfg.PairingHost()
	if qrHost == cfg.ListenAddr() {
		log.Printf("  QR code host   : NOT SET — run 'make up' or set DOCKERTAB_HOST")
	} else {
		log.Printf("  QR code host   : %s", qrHost)
	}
	log.Printf("  Relay          : connecting to %s", cfg.RelayURL)
	if apnsClient != nil {
		log.Printf("  Notifications  : direct APNs (sandbox=%v, LAN-only fallback)", cfg.APNsSandbox)
	} else {
		log.Println("  Notifications  : via relay")
	}
	log.Println("──────────────────────────────────────────────")
	pairingPayload := map[string]interface{}{
		"host":     cfg.PairingHost(),
		"api_key":  cfg.APIKey,
		"agent_id": handler.AgentID,
		"version":  "0.1.0",
	}
	if cfg.Name != "" {
		pairingPayload["name"] = cfg.Name
	}
	if cfg.RelayURL != "" {
		pairingPayload["relay_url"] = cfg.RelayURL
	}
	if tsMonitor != nil {
		tsStatus := tsMonitor.GetStatus()
		if tsStatus.Available {
			pairingPayload["tailscale_host"] = fmt.Sprintf("%s:%d", tsStatus.IP, cfg.Port)
		}
	}
	pairingData, _ := json.Marshal(pairingPayload)
	log.Println("  Scan this QR code with the DockerTab iOS app:")
	fmt.Fprintln(log.Writer())
	qrterminal.GenerateHalfBlock(string(pairingData), qrterminal.M, log.Writer())
	fmt.Fprintln(log.Writer())
	log.Println("──────────────────────────────────────────────")

	// Start relay after QR code is printed to avoid log lines corrupting the QR output.
	go relayClient.Start(relayCtx)

	go func() {
		if err := router.Run(cfg.ListenAddr()); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	log.Printf("Received signal %s, shutting down gracefully...", sig)
	relayCancel()
	relayClient.Stop()
	if tsMonitor != nil {
		tsMonitor.Stop()
	}
	dockerClient.Close()
	log.Println("DockerTab Agent stopped.")
}
