package relay

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dockertab/agent/config"
	"github.com/dockertab/agent/internal/auth"
	"github.com/dockertab/agent/internal/docker"
	"github.com/gorilla/websocket"
)

// validContainerID matches Docker container IDs (hex) and container names.
var validContainerID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.\-]{0,127}$`)

// streamEntry pairs a stream's cancel function with the iOS client that opened it.
type streamEntry struct {
	clientID string
	cancel   context.CancelFunc
}

// Client manages a persistent outbound WebSocket connection to the relay server.
type Client struct {
	cfg         *config.Config
	authService *auth.Service
	docker      docker.DockerClient
	router      http.Handler
	agentID     string
	relayJWT    string // Internal JWT for authenticating relay-forwarded requests

	conn   *websocket.Conn
	connMu sync.Mutex
	send   chan []byte

	streams      map[string]streamEntry // request_id → {clientID, cancel}
	streamsMu    sync.Mutex
	stdinWriters map[string]io.Writer  // request_id → exec stdin
	stdinMu      sync.Mutex
	execIDs      map[string]string     // request_id → docker execID (for resize)
	execIDsMu    sync.Mutex

	backoffAttempt int
	done           chan struct{}
	once           sync.Once
}

func NewClient(cfg *config.Config, authService *auth.Service, dockerClient docker.DockerClient, router http.Handler, agentID string) *Client {
	relayJWT, err := authService.GenerateToken("relay-internal", "Relay")
	if err != nil {
		log.Printf("[relay] warning: failed to generate internal JWT: %v", err)
	}

	return &Client{
		cfg:          cfg,
		authService:  authService,
		docker:       dockerClient,
		router:       router,
		agentID:      agentID,
		relayJWT:     relayJWT,
		send:         make(chan []byte, 256),
		streams:      make(map[string]streamEntry),
		stdinWriters: make(map[string]io.Writer),
		execIDs:      make(map[string]string),
		done:         make(chan struct{}),
	}
}

// Start connects to the relay server and begins processing messages.
// It reconnects automatically on disconnect.
func (c *Client) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		default:
		}

		if err := c.connect(ctx); err != nil {
			log.Printf("[relay] connection failed: %v", err)
		}

		// Reconnect with exponential backoff
		if !c.backoff(ctx) {
			return
		}
	}
}

// Stop shuts down the relay client gracefully.
func (c *Client) Stop() {
	c.once.Do(func() {
		close(c.done)
		c.connMu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		c.connMu.Unlock()
		c.cancelAllStreams()
	})
}

// IsConnected returns true if the relay connection is active.
func (c *Client) IsConnected() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.conn != nil
}

// RegisterDeviceToken forwards an APNs device token to the relay on behalf of a
// LAN or Tailscale iOS client. These clients authenticate directly with the agent
// (no relay WebSocket), so the agent forwards their token so the relay can push
// notifications to them.
func (c *Client) RegisterDeviceToken(deviceID, token, environment string) {
	c.sendEnvelope(Envelope{
		Type: TypeRegisterAPNs,
		Payload: MustMarshal(RegisterAPNsPayload{
			DeviceID:    deviceID,
			DeviceToken: token,
			Environment: environment,
		}),
	})
}

// SendNotification forwards a container event to the relay for APNs dispatch.
// The relay holds the developer's APNs key and pushes to all registered devices.
func (c *Client) SendNotification(containerID, containerName, action string) {
	c.sendEnvelope(Envelope{
		Type: TypeNotification,
		Payload: MustMarshal(NotificationPayload{
			ContainerID:   containerID,
			ContainerName: containerName,
			Action:        action,
			AgentName:     c.cfg.Name,
		}),
	})
}

func (c *Client) backoff(ctx context.Context) bool {
	c.backoffAttempt++
	delay := time.Duration(math.Min(float64(time.Second)*math.Pow(2, float64(c.backoffAttempt)), float64(60*time.Second)))
	// Add jitter: +/- 20%
	jitter := time.Duration(float64(delay) * (0.8 + 0.4*rand.Float64()))

	log.Printf("[relay] reconnecting in %s...", jitter.Round(time.Millisecond))

	select {
	case <-time.After(jitter):
		return true
	case <-ctx.Done():
		return false
	case <-c.done:
		return false
	}
}

func (c *Client) connect(ctx context.Context) error {
	log.Printf("[relay] connecting to %s", c.cfg.RelayURL)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.cfg.RelayURL+"/agent", nil)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	defer func() {
		c.connMu.Lock()
		c.conn = nil
		c.connMu.Unlock()
		conn.Close()
		c.cancelAllStreams()
	}()

	// Authenticate with relay
	if err := c.authenticate(conn); err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}

	log.Println("[relay] connected and authenticated")
	c.backoffAttempt = 0

	// Run read/write loops
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go c.writeLoop(ctx, conn)
	return c.readLoop(ctx, conn)
}

func (c *Client) authenticate(conn *websocket.Conn) error {
	payload := MustMarshal(AuthPayload{
		AgentID: c.agentID,
		Token:   c.cfg.RelayToken,
	})

	env := Envelope{
		Type:    TypeAuth,
		Payload: payload,
	}

	if err := conn.WriteJSON(env); err != nil {
		return fmt.Errorf("failed to send auth: %w", err)
	}

	// Wait for auth_ok
	var resp Envelope
	if err := conn.ReadJSON(&resp); err != nil {
		return fmt.Errorf("failed to read auth response: %w", err)
	}

	if resp.Type == TypeError {
		var errPayload ErrorPayload
		json.Unmarshal(resp.Payload, &errPayload)
		return fmt.Errorf("relay rejected auth: %s", errPayload.Message)
	}

	if resp.Type != TypeAuthOK {
		return fmt.Errorf("unexpected auth response: %s", resp.Type)
	}

	return nil
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return nil
		default:
		}

		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return fmt.Errorf("read failed: %w", err)
		}

		switch env.Type {
		case TypeClientAuth:
			go c.handleClientAuth(env)
		case TypeClientReAuth:
			go c.handleClientReAuth(env)
		case TypeRequest:
			go c.handleRequest(ctx, env)
		case TypeStreamOpen:
			go c.handleStreamOpen(ctx, env)
		case TypeStreamInput:
			go c.handleStreamInput(env)
		case TypeStreamResize:
			go c.handleStreamResize(env)
		case TypeStreamClose:
			c.handleStreamClose(env)
		case TypePing:
			c.sendEnvelope(Envelope{Type: TypePong})
		}
	}
}

func (c *Client) writeLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case msg := <-c.send:
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("[relay] write failed: %v", err)
				return
			}
		}
	}
}

func (c *Client) sendEnvelope(env Envelope) {
	data, err := json.Marshal(env)
	if err != nil {
		log.Printf("[relay] marshal failed: %v", err)
		return
	}

	select {
	case c.send <- data:
	default:
		log.Println("[relay] send buffer full, dropping message")
	}
}

// handleClientAuth validates an iOS client's JWT locally.
func (c *Client) handleClientAuth(env Envelope) {
	var payload ClientAuthPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		c.sendEnvelope(Envelope{
			Type:     TypeClientAuthResult,
			ClientID: env.ClientID,
			Payload:  MustMarshal(ClientAuthResultPayload{Accepted: false}),
		})
		return
	}

	claims, err := c.authService.ValidateToken(payload.JWT)
	result := ClientAuthResultPayload{Accepted: err == nil}
	if err == nil {
		result.DeviceID = claims.DeviceID
		result.DeviceName = claims.DeviceName
	}

	c.sendEnvelope(Envelope{
		Type:     TypeClientAuthResult,
		ClientID: env.ClientID,
		Payload:  MustMarshal(result),
	})
}

// handleClientReAuth handles API key re-pairing for expired JWTs.
func (c *Client) handleClientReAuth(env Envelope) {
	var payload ClientReAuthPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		c.sendEnvelope(Envelope{
			Type:     TypeClientReAuthResult,
			ClientID: env.ClientID,
			Payload:  MustMarshal(ClientReAuthResultPayload{Accepted: false}),
		})
		return
	}

	// Validate API key
	if payload.APIKey != c.cfg.APIKey {
		c.sendEnvelope(Envelope{
			Type:     TypeClientReAuthResult,
			ClientID: env.ClientID,
			Payload:  MustMarshal(ClientReAuthResultPayload{Accepted: false}),
		})
		return
	}

	// Generate fresh JWT
	token, err := c.authService.GenerateToken(payload.DeviceID, payload.DeviceName)
	if err != nil {
		c.sendEnvelope(Envelope{
			Type:     TypeClientReAuthResult,
			ClientID: env.ClientID,
			Payload:  MustMarshal(ClientReAuthResultPayload{Accepted: false}),
		})
		return
	}

	c.sendEnvelope(Envelope{
		Type:     TypeClientReAuthResult,
		ClientID: env.ClientID,
		Payload:  MustMarshal(ClientReAuthResultPayload{Accepted: true, JWT: token}),
	})
}

// handleRequest dispatches an HTTP request through the Gin router.
func (c *Client) handleRequest(ctx context.Context, env Envelope) {
	var payload RequestPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		c.sendEnvelope(Envelope{
			Type:      TypeResponse,
			RequestID: env.RequestID,
			ClientID:  env.ClientID,
			Payload: MustMarshal(ResponsePayload{
				StatusCode: http.StatusBadRequest,
				Body:       `{"error":"invalid request payload"}`,
			}),
		})
		return
	}

	// Build synthetic HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, payload.Method, payload.Path, strings.NewReader(payload.Body))
	if err != nil {
		c.sendEnvelope(Envelope{
			Type:      TypeResponse,
			RequestID: env.RequestID,
			ClientID:  env.ClientID,
			Payload: MustMarshal(ResponsePayload{
				StatusCode: http.StatusInternalServerError,
				Body:       `{"error":"failed to create request"}`,
			}),
		})
		return
	}

	for k, v := range payload.Headers {
		httpReq.Header.Set(k, v)
	}

	// Inject internal relay JWT so the request passes JWT middleware.
	// The client already authenticated during the relay auth handshake.
	if c.relayJWT != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.relayJWT)
	}

	// Dispatch through router (runs all middleware including JWT auth)
	recorder := httptest.NewRecorder()
	c.router.ServeHTTP(recorder, httpReq)

	// Build response
	respHeaders := make(map[string]string)
	for k, v := range recorder.Header() {
		if len(v) > 0 {
			respHeaders[k] = v[0]
		}
	}

	c.sendEnvelope(Envelope{
		Type:      TypeResponse,
		RequestID: env.RequestID,
		ClientID:  env.ClientID,
		Payload: MustMarshal(ResponsePayload{
			StatusCode: recorder.Code,
			Headers:    respHeaders,
			Body:       recorder.Body.String(),
		}),
	})
}

// handleStreamOpen starts a log or stats stream through the relay.
func (c *Client) handleStreamOpen(ctx context.Context, env Envelope) {
	var payload RequestPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return
	}

	streamCtx, cancel := context.WithCancel(ctx)
	c.streamsMu.Lock()
	c.streams[env.RequestID] = streamEntry{clientID: env.ClientID, cancel: cancel}
	c.streamsMu.Unlock()

	defer func() {
		c.streamsMu.Lock()
		delete(c.streams, env.RequestID)
		c.streamsMu.Unlock()
		cancel()
		c.sendEnvelope(Envelope{
			Type:      TypeStreamClose,
			RequestID: env.RequestID,
			ClientID:  env.ClientID,
		})
	}()

	// Extract container ID from path: /api/v1/containers/:id/logs/stream or /stats/stream
	parts := strings.Split(strings.TrimPrefix(payload.Path, "/"), "/")
	if len(parts) < 4 {
		return
	}
	containerID := parts[3] // api/v1/containers/{id}/...
	if !validContainerID.MatchString(containerID) {
		log.Printf("[relay] rejected invalid container ID: %q", containerID)
		return
	}

	if strings.Contains(payload.Path, "/logs/stream") {
		c.streamLogs(streamCtx, env, containerID)
	} else if strings.Contains(payload.Path, "/stats/stream") {
		c.streamStats(streamCtx, env, containerID)
	} else if strings.Contains(payload.Path, "/exec") {
		// Parse initial terminal size from query string in path.
		rows, cols := 24, 80
		if idx := strings.Index(payload.Path, "?"); idx >= 0 {
			for _, kv := range strings.Split(payload.Path[idx+1:], "&") {
				if parts := strings.SplitN(kv, "=", 2); len(parts) == 2 {
					switch parts[0] {
					case "rows":
						if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
							rows = n
						}
					case "cols":
						if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
							cols = n
						}
					}
				}
			}
		}
		c.streamExec(streamCtx, env, containerID, rows, cols)
	}
}

func (c *Client) streamLogs(ctx context.Context, env Envelope, containerID string) {
	reader, err := c.docker.StreamLogs(ctx, containerID, 50)
	if err != nil {
		log.Printf("[relay] stream logs error: %v", err)
		return
	}
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			c.sendEnvelope(Envelope{
				Type:      TypeStreamData,
				RequestID: env.RequestID,
				ClientID:  env.ClientID,
				Payload:   MustMarshal(StreamPayload{Data: scanner.Text()}),
			})
		}
	}
}

func (c *Client) streamStats(ctx context.Context, env Envelope, containerID string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats, err := c.docker.GetContainerStats(ctx, containerID)
			if err != nil {
				log.Printf("[relay] stream stats error: %v", err)
				return
			}
			data, _ := json.Marshal(stats)
			c.sendEnvelope(Envelope{
				Type:      TypeStreamData,
				RequestID: env.RequestID,
				ClientID:  env.ClientID,
				Payload:   MustMarshal(StreamPayload{Data: string(data)}),
			})
		}
	}
}

// streamExec runs an interactive shell inside the container, piping PTY output
// to the relay as base64-encoded stream_data frames.
// /bin/sh is tried first (universally available); bash and ash are fallbacks.
func (c *Client) streamExec(ctx context.Context, env Envelope, containerID string, rows, cols int) {
	var execID string
	var err error
	for _, shell := range []string{"/bin/sh", "/bin/bash", "/bin/ash"} {
		execID, err = c.docker.ExecCreate(ctx, containerID, []string{shell}, rows, cols)
		if err == nil {
			break
		}
	}
	if err != nil {
		log.Printf("[relay] exec create error: %v", err)
		return
	}

	resp, err := c.docker.ExecAttach(ctx, execID)
	if err != nil {
		log.Printf("[relay] exec attach error: %v", err)
		return
	}
	defer resp.Close()

	// Register stdin writer and execID so input/resize handlers can find them.
	c.stdinMu.Lock()
	c.stdinWriters[env.RequestID] = resp.Conn
	c.stdinMu.Unlock()
	c.execIDsMu.Lock()
	c.execIDs[env.RequestID] = execID
	c.execIDsMu.Unlock()
	defer func() {
		c.stdinMu.Lock()
		delete(c.stdinWriters, env.RequestID)
		c.stdinMu.Unlock()
		c.execIDsMu.Lock()
		delete(c.execIDs, env.RequestID)
		c.execIDsMu.Unlock()
	}()

	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := resp.Reader.Read(buf)
		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(buf[:n])
			c.sendEnvelope(Envelope{
				Type:      TypeStreamData,
				RequestID: env.RequestID,
				ClientID:  env.ClientID,
				Payload:   MustMarshal(StreamPayload{Data: encoded}),
			})
		}
		if err != nil {
			return
		}
	}
}

// handleStreamInput decodes a base64 stdin frame from the iOS client and writes
// it to the active exec session's PTY.
func (c *Client) handleStreamInput(env Envelope) {
	var payload StreamPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		return
	}
	c.stdinMu.Lock()
	w, ok := c.stdinWriters[env.RequestID]
	c.stdinMu.Unlock()
	if !ok {
		return
	}
	_, _ = w.Write(decoded)
}

// handleStreamResize resizes the PTY of an active exec session.
func (c *Client) handleStreamResize(env Envelope) {
	var payload ResizePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.Rows <= 0 || payload.Cols <= 0 {
		return
	}
	c.execIDsMu.Lock()
	execID, ok := c.execIDs[env.RequestID]
	c.execIDsMu.Unlock()
	if !ok {
		return
	}
	_ = c.docker.ExecResize(context.Background(), execID, payload.Rows, payload.Cols)
}

func (c *Client) handleStreamClose(env Envelope) {
	c.streamsMu.Lock()
	defer c.streamsMu.Unlock()
	if env.RequestID != "" {
		// Targeted close: cancel the specific stream.
		if entry, ok := c.streams[env.RequestID]; ok {
			entry.cancel()
			delete(c.streams, env.RequestID)
		}
	} else if env.ClientID != "" {
		// Client disconnected: cancel all streams belonging to that client.
		for id, entry := range c.streams {
			if entry.clientID == env.ClientID {
				entry.cancel()
				delete(c.streams, id)
			}
		}
	}
}

func (c *Client) cancelAllStreams() {
	c.streamsMu.Lock()
	defer c.streamsMu.Unlock()
	for id, entry := range c.streams {
		entry.cancel()
		delete(c.streams, id)
	}
}
