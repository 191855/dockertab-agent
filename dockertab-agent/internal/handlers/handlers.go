package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dockertab/agent/config"
	"github.com/dockertab/agent/internal/auth"
	"github.com/dockertab/agent/internal/docker"
	"github.com/dockertab/agent/internal/notifications"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// pairRateLimiter enforces a per-IP limit on pairing attempts.
type pairRateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*pairAttempt
	max      int
	window   time.Duration
}

type pairAttempt struct {
	count     int
	firstSeen time.Time
}

func newPairRateLimiter() *pairRateLimiter {
	rl := &pairRateLimiter{
		attempts: make(map[string]*pairAttempt),
		max:      5,
		window:   5 * time.Minute,
	}
	go rl.cleanup()
	return rl
}

func (rl *pairRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	a, ok := rl.attempts[ip]
	if !ok || now.Sub(a.firstSeen) > rl.window {
		rl.attempts[ip] = &pairAttempt{count: 1, firstSeen: now}
		return true
	}
	a.count++
	return a.count <= rl.max
}

func (rl *pairRateLimiter) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for k, a := range rl.attempts {
			if now.Sub(a.firstSeen) > rl.window {
				delete(rl.attempts, k)
			}
		}
		rl.mu.Unlock()
	}
}

// Handler holds shared dependencies for all route handlers.
type Handler struct {
	Docker    docker.DockerClient
	Auth      *auth.Service
	Config    *config.Config
	AgentID   string
	StartedAt time.Time

	// Premium features (nil when inactive)
	RelayConnected func() bool // Returns true if relay is connected

	// Push notifications (nil when APNs not configured)
	TokenStore *notifications.TokenStore

	// RelayRegisterToken forwards an APNs token to the relay when a LAN/Tailscale
	// iOS client registers via HTTP. Set only when relay is configured.
	RelayRegisterToken func(deviceID, token, environment string)

	pairLimiter *pairRateLimiter
}

func NewHandler(dockerClient docker.DockerClient, authService *auth.Service, cfg *config.Config) *Handler {
	return &Handler{
		Docker:      dockerClient,
		Auth:        authService,
		Config:      cfg,
		AgentID:     cfg.AgentID,
		StartedAt:   time.Now(),
		pairLimiter: newPairRateLimiter(),
	}
}

//Health & System

// Healthz is a lightweight liveness probe (no auth required).
func (h *Handler) Healthz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := h.Docker.Ping(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unhealthy",
			"error":  "docker daemon unreachable",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "healthy",
		"agent_id": h.AgentID,
		"version":  "0.1.0",
		"uptime":   time.Since(h.StartedAt).String(),
	})
}

// GetHostInfo returns system-level Docker host information.
func (h *Handler) GetHostInfo(c *gin.Context) {
	info, err := h.Docker.GetHostInfo(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, info)
}

//Authentication / Pairing

type pairRequest struct {
	APIKey     string `json:"api_key" binding:"required"`
	DeviceID   string `json:"device_id" binding:"required"`
	DeviceName string `json:"device_name" binding:"required"`
}

// Pair exchanges a valid API key for a JWT token (the pairing flow).
// The iOS app scans a QR code containing the API key, then calls this endpoint.
func (h *Handler) Pair(c *gin.Context) {
	if !h.pairLimiter.Allow(c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many pairing attempts, try again later"})
		return
	}

	var req pairRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// Validate API key
	if req.APIKey != h.Config.APIKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid API key"})
		return
	}

	// Generate JWT
	token, err := h.Auth.GenerateToken(req.DeviceID, req.DeviceName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token":       token,
		"agent_id":    h.AgentID,
		"relay_token": h.Config.RelayToken,
		"message":     "paired successfully",
	})
}

//Containers

// ListContainers returns all containers on the host.
func (h *Handler) ListContainers(c *gin.Context) {
	containers, err := h.Docker.ListContainers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"containers": containers,
		"count":      len(containers),
	})
}

// GetContainer returns details for a single container.
func (h *Handler) GetContainer(c *gin.Context) {
	id := c.Param("id")
	container, err := h.Docker.GetContainer(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, container)
}

func (h *Handler) StartContainer(c *gin.Context) {
	id := c.Param("id")
	if err := h.Docker.StartContainer(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "container started", "id": id})
}

func (h *Handler) StopContainer(c *gin.Context) {
	id := c.Param("id")
	if err := h.Docker.StopContainer(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "container stopped", "id": id})
}

func (h *Handler) RestartContainer(c *gin.Context) {
	id := c.Param("id")
	if err := h.Docker.RestartContainer(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "container restarted", "id": id})
}

// GetContainerStats returns live CPU/RAM usage for a container.
func (h *Handler) GetContainerStats(c *gin.Context) {
	id := c.Param("id")
	stats, err := h.Docker.GetContainerStats(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// GetContainerLogs returns the last N lines of a container's logs.
func (h *Handler) GetContainerLogs(c *gin.Context) {
	id := c.Param("id")
	lines := 100
	if n, err := strconv.Atoi(c.DefaultQuery("lines", "100")); err == nil && n > 0 && n <= 5000 {
		lines = n
	}

	logs, err := h.Docker.GetContainerLogs(c.Request.Context(), id, lines)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs, "lines": lines})
}

//WebSocket: Live Log Streaming

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// StreamContainerLogs upgrades to a WebSocket and streams live logs.
func (h *Handler) StreamContainerLogs(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	reader, err := h.Docker.StreamLogs(ctx, id, 50)
	if err != nil {
		errMsg, _ := json.Marshal(map[string]string{"error": err.Error()})
		conn.WriteMessage(websocket.TextMessage, errMsg)
		return
	}
	defer reader.Close()

	// Listen for client disconnect
	done := make(chan struct{})
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				close(done)
				return
			}
		}
	}()

	// Stream lines from Docker to the WebSocket
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok {
				return // Stream ended
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				return
			}
		}
	}
}

//WebSocket: Live Stats Streaming

// StreamContainerStats upgrades to a WebSocket and streams live resource stats.
func (h *Handler) StreamContainerStats(c *gin.Context) {
	id := c.Param("id")

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Listen for client disconnect
	done := make(chan struct{})
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				close(done)
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			stats, err := h.Docker.GetContainerStats(c.Request.Context(), id)
			if err != nil {
				conn.WriteJSON(gin.H{"error": err.Error()})
				return
			}
			if err := conn.WriteJSON(stats); err != nil {
				return
			}
		}
	}
}

// sensitiveEnvKeywords are substrings that indicate an env var holds a secret.
var sensitiveEnvKeywords = []string{"PASSWORD", "SECRET", "TOKEN", "KEY", "PASS", "CREDENTIAL", "AUTH", "PRIVATE"}

func isSensitiveEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, kw := range sensitiveEnvKeywords {
		if strings.Contains(upper, kw) {
			return true
		}
	}
	return false
}

// GetContainerEnv returns the environment variables for a container as a key→value map.
// Values of sensitive keys (containing PASSWORD, SECRET, TOKEN, etc.) are redacted.
func (h *Handler) GetContainerEnv(c *gin.Context) {
	id := c.Param("id")
	env, err := h.Docker.GetContainerEnv(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	pairs := make(map[string]string, len(env))
	for _, e := range env {
		if idx := strings.Index(e, "="); idx >= 0 {
			key := e[:idx]
			val := e[idx+1:]
			if isSensitiveEnvKey(key) {
				val = "[REDACTED]"
			}
			pairs[key] = val
		} else {
			pairs[e] = ""
		}
	}
	c.JSON(http.StatusOK, gin.H{"env": pairs, "count": len(pairs)})
}

// StreamContainerExec upgrades to a WebSocket and attaches an interactive /bin/sh
// session inside the container. Binary frames carry raw PTY bytes; text frames are
// JSON resize commands {"rows":N,"cols":N}.
func (h *Handler) StreamContainerExec(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()

	// Parse initial terminal size from query params (sent by the iOS client).
	cols, rows := 80, 24
	if n, err := strconv.Atoi(c.DefaultQuery("cols", "80")); err == nil && n > 0 {
		cols = n
	}
	if n, err := strconv.Atoi(c.DefaultQuery("rows", "24")); err == nil && n > 0 {
		rows = n
	}

	var err error
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("exec websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Try shells in preference order. /bin/sh is universal; bash and ash cover
	// containers where sh is absent. NOTE: ExecCreate succeeds regardless of
	// whether the binary exists; the fallback only helps if Docker reports an
	// error at create time (e.g. container not running).
	var execID string
	for _, shell := range []string{"/bin/sh", "/bin/bash", "/bin/ash"} {
		execID, err = h.Docker.ExecCreate(ctx, id, []string{shell}, rows, cols)
		if err == nil {
			break
		}
	}
	if err != nil {
		errMsg, _ := json.Marshal(map[string]string{"error": err.Error()})
		conn.WriteMessage(websocket.TextMessage, errMsg)
		return
	}

	resp, err := h.Docker.ExecAttach(ctx, execID)
	if err != nil {
		errMsg, _ := json.Marshal(map[string]string{"error": err.Error()})
		conn.WriteMessage(websocket.TextMessage, errMsg)
		return
	}
	defer resp.Close()

	done := make(chan struct{})

	// Output: PTY → WebSocket (binary frames)
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Reader.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Input: WebSocket → PTY.
	// Binary frames are raw stdin bytes; text frames are resize commands.
	go func() {
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if msgType == websocket.TextMessage {
				var resize struct {
					Rows int `json:"rows"`
					Cols int `json:"cols"`
				}
				if json.Unmarshal(msg, &resize) == nil && resize.Rows > 0 && resize.Cols > 0 {
					h.Docker.ExecResize(ctx, execID, resize.Rows, resize.Cols)
				}
			} else {
				if _, werr := resp.Conn.Write(msg); werr != nil {
					return
				}
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
		io.WriteString(resp.Conn, "exit\n")
	}
}

// ListImages returns all local Docker images.
func (h *Handler) ListImages(c *gin.Context) {
	images, err := h.Docker.ListImages(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"images": images,
		"count":  len(images),
	})
}

// ListVolumes returns all local Docker volumes.
func (h *Handler) ListVolumes(c *gin.Context) {
	volumes, err := h.Docker.ListVolumes(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"volumes": volumes,
		"count":   len(volumes),
	})
}

//Premium Features

type premiumActivateRequest struct {
	Receipt string `json:"receipt" binding:"required"`
}

// ActivatePremium stores the App Store receipt and enables premium features.
func (h *Handler) ActivatePremium(c *gin.Context) {
	var req premiumActivateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	h.Config.PremiumEnabled = true
	h.Config.PremiumToken = req.Receipt
	h.Config.PremiumActivated = time.Now().UTC().Format(time.RFC3339)
	if err := h.Config.Save(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save config"})
		return
	}

	features := []string{}
	if h.Config.RelayURL != "" {
		features = append(features, "relay")
	}
	if h.Config.TailscaleEnabled {
		features = append(features, "tailscale")
	}

	c.JSON(http.StatusOK, gin.H{
		"premium":  true,
		"features": features,
	})
}

// GetPremiumStatus returns the current premium feature state.
func (h *Handler) GetPremiumStatus(c *gin.Context) {
	relayConnected := false
	if h.RelayConnected != nil {
		relayConnected = h.RelayConnected()
	}

	c.JSON(http.StatusOK, gin.H{
		"premium_enabled": h.Config.PremiumEnabled,
		"activated_at":    h.Config.PremiumActivated,
		"relay_url":       h.Config.RelayURL,
		"relay_connected": relayConnected,
	})
}
