package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/dockertab/agent/config"
	"github.com/dockertab/agent/internal/auth"
	"github.com/dockertab/agent/internal/docker"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// mockDocker is a minimal DockerClient stub for unit tests.
type mockDocker struct {
	pingErr    error
	pingDelay  time.Duration
	envVars    []string
	envErr     error
	execShells []string // records ExecCreate cmd[0] calls
	execErr    error
}

func (m *mockDocker) Ping(ctx context.Context) error {
	if m.pingDelay > 0 {
		select {
		case <-time.After(m.pingDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.pingErr
}
func (m *mockDocker) Close() error                                             { return nil }
func (m *mockDocker) ListContainers(ctx context.Context) ([]docker.ContainerSummary, error) {
	return nil, nil
}
func (m *mockDocker) GetContainer(ctx context.Context, id string) (*docker.ContainerSummary, error) {
	return nil, nil
}
func (m *mockDocker) GetContainerStats(ctx context.Context, id string) (*docker.ContainerStats, error) {
	return nil, nil
}
func (m *mockDocker) GetContainerLogs(ctx context.Context, id string, lines int) (string, error) {
	return "", nil
}
func (m *mockDocker) StreamLogs(ctx context.Context, id string, lines int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (m *mockDocker) GetHostInfo(ctx context.Context) (*docker.HostInfo, error) { return nil, nil }
func (m *mockDocker) ListImages(ctx context.Context) ([]docker.ImageSummary, error) {
	return nil, nil
}
func (m *mockDocker) StartContainer(ctx context.Context, id string) error   { return nil }
func (m *mockDocker) StopContainer(ctx context.Context, id string) error    { return nil }
func (m *mockDocker) RestartContainer(ctx context.Context, id string) error { return nil }
func (m *mockDocker) GetContainerEnv(ctx context.Context, id string) ([]string, error) {
	return m.envVars, m.envErr
}
func (m *mockDocker) ExecCreate(ctx context.Context, containerID string, cmd []string, rows, cols int) (string, error) {
	if len(cmd) > 0 {
		m.execShells = append(m.execShells, cmd[0])
	}
	return "exec-id-123", m.execErr
}
func (m *mockDocker) ExecAttach(ctx context.Context, execID string) (types.HijackedResponse, error) {
	return types.HijackedResponse{}, fmt.Errorf("stub")
}
func (m *mockDocker) ExecResize(ctx context.Context, execID string, rows, cols int) error { return nil }
func (m *mockDocker) Events(ctx context.Context) (<-chan docker.ContainerEvent, <-chan error) {
	return make(chan docker.ContainerEvent), make(chan error)
}
func (m *mockDocker) ListVolumes(ctx context.Context) ([]docker.VolumeSummary, error) {
	return nil, nil
}

func newTestHandler(d docker.DockerClient) *Handler {
	cfg := &config.Config{
		APIKey:  "test-api-key-abc123",
		AgentID: "test-agent",
	}
	svc := auth.NewService("test-jwt-secret")
	return NewHandler(d, svc, cfg)
}

// ────────────────────────────────────────────────────────────────────────────
// Health check tests
// ────────────────────────────────────────────────────────────────────────────

func TestHealthz_Healthy(t *testing.T) {
	h := newTestHandler(&mockDocker{})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/healthz", nil)

	h.Healthz(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "healthy" {
		t.Errorf("expected status=healthy, got %v", body["status"])
	}
}

func TestHealthz_DockerUnreachable(t *testing.T) {
	h := newTestHandler(&mockDocker{pingErr: fmt.Errorf("connection refused")})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/healthz", nil)

	h.Healthz(c)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHealthz_Timeout(t *testing.T) {
	// Ping takes 5s; handler enforces 2s timeout — context should cancel.
	h := newTestHandler(&mockDocker{pingDelay: 5 * time.Second})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/healthz", nil)

	start := time.Now()
	h.Healthz(c)
	elapsed := time.Since(start)

	// Should finish in well under 3s (handler's 2s limit)
	if elapsed > 3*time.Second {
		t.Errorf("health check did not respect 2s timeout (took %v)", elapsed)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on timeout, got %d", w.Code)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Rate limiter tests
// ────────────────────────────────────────────────────────────────────────────

func TestPairRateLimiter_AllowsUnderLimit(t *testing.T) {
	rl := newPairRateLimiter()
	for i := 0; i < 5; i++ {
		if !rl.Allow("1.2.3.4") {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
}

func TestPairRateLimiter_BlocksOverLimit(t *testing.T) {
	rl := newPairRateLimiter()
	for i := 0; i < 5; i++ {
		rl.Allow("1.2.3.4")
	}
	if rl.Allow("1.2.3.4") {
		t.Fatal("6th attempt should be blocked")
	}
}

func TestPairRateLimiter_IndependentPerIP(t *testing.T) {
	rl := newPairRateLimiter()
	for i := 0; i < 5; i++ {
		rl.Allow("1.2.3.4")
	}
	// Different IP is unaffected
	if !rl.Allow("9.9.9.9") {
		t.Fatal("different IP should still be allowed")
	}
}

func TestPairRateLimiter_ResetsAfterWindow(t *testing.T) {
	rl := &pairRateLimiter{
		attempts: make(map[string]*pairAttempt),
		max:      2,
		window:   50 * time.Millisecond,
	}
	rl.Allow("1.2.3.4")
	rl.Allow("1.2.3.4")
	if rl.Allow("1.2.3.4") {
		t.Fatal("3rd attempt should be blocked within window")
	}
	time.Sleep(60 * time.Millisecond)
	if !rl.Allow("1.2.3.4") {
		t.Fatal("should be allowed again after window expires")
	}
}

func TestPair_RateLimitReturns429(t *testing.T) {
	h := newTestHandler(&mockDocker{})
	router := gin.New()
	router.POST("/pair", h.Pair)

	body, _ := json.Marshal(map[string]string{
		"api_key":     "test-api-key-abc123",
		"device_id":   "dev-1",
		"device_name": "iPhone",
	})

	// Exhaust the limit (5 successful, 6th is blocked)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/pair", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	req := httptest.NewRequest("POST", "/pair", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d body=%s", w.Code, w.Body.String())
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Env var redaction tests
// ────────────────────────────────────────────────────────────────────────────

func TestIsSensitiveEnvKey(t *testing.T) {
	cases := []struct {
		key       string
		sensitive bool
	}{
		{"DATABASE_PASSWORD", true},
		{"DB_PASS", true},
		{"JWT_SECRET", true},
		{"API_KEY", true},
		{"ACCESS_TOKEN", true},
		{"PRIVATE_KEY", true},
		{"AWS_CREDENTIAL", true},
		{"AUTH_TOKEN", true},
		{"POSTGRES_PASSWORD", true},
		{"MY_KEY", true},
		{"SECRET_SAUCE", true},
		// Not sensitive
		{"PORT", false},
		{"HOST", false},
		{"LOG_LEVEL", false},
		{"NODE_ENV", false},
		{"TZ", false},
		{"HOSTNAME", false},
		{"DATABASE_HOST", false},
		{"DATABASE_NAME", false},
	}
	for _, tc := range cases {
		got := isSensitiveEnvKey(tc.key)
		if got != tc.sensitive {
			t.Errorf("isSensitiveEnvKey(%q) = %v, want %v", tc.key, got, tc.sensitive)
		}
	}
}

func TestGetContainerEnv_RedactsSensitiveValues(t *testing.T) {
	envVars := []string{
		"PORT=8080",
		"DATABASE_PASSWORD=supersecret",
		"API_KEY=my-api-key",
		"LOG_LEVEL=info",
		"JWT_SECRET=my-jwt",
		"HOST=localhost",
	}
	h := newTestHandler(&mockDocker{envVars: envVars})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/containers/abc/env", nil)
	c.Params = gin.Params{{Key: "id", Value: "abc"}}

	h.GetContainerEnv(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body struct {
		Env map[string]string `json:"env"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)

	// Plain vars are passed through unchanged
	if body.Env["PORT"] != "8080" {
		t.Errorf("PORT should be '8080', got %q", body.Env["PORT"])
	}
	if body.Env["LOG_LEVEL"] != "info" {
		t.Errorf("LOG_LEVEL should be 'info', got %q", body.Env["LOG_LEVEL"])
	}
	if body.Env["HOST"] != "localhost" {
		t.Errorf("HOST should be 'localhost', got %q", body.Env["HOST"])
	}

	// Sensitive vars are redacted
	for _, k := range []string{"DATABASE_PASSWORD", "API_KEY", "JWT_SECRET"} {
		if body.Env[k] != "[REDACTED]" {
			t.Errorf("%s should be [REDACTED], got %q", k, body.Env[k])
		}
	}
}

func TestGetContainerEnv_NoEnvVars(t *testing.T) {
	h := newTestHandler(&mockDocker{envVars: []string{}})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/containers/abc/env", nil)
	c.Params = gin.Params{{Key: "id", Value: "abc"}}

	h.GetContainerEnv(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestGetContainerEnv_DockerError(t *testing.T) {
	h := newTestHandler(&mockDocker{envErr: fmt.Errorf("container not found")})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/containers/abc/env", nil)
	c.Params = gin.Params{{Key: "id", Value: "abc"}}

	h.GetContainerEnv(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Shell fallback tests (require a real HTTP server for the WebSocket upgrade)
// ────────────────────────────────────────────────────────────────────────────

// execTestServer spins up an httptest.Server wired to StreamContainerExec
// and returns the server URL and the mock docker used.
func execTestServer(t *testing.T, d docker.DockerClient) (*httptest.Server, string) {
	t.Helper()
	h := newTestHandler(d)
	router := gin.New()
	router.GET("/containers/:id/exec", h.StreamContainerExec)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, "ws" + strings.TrimPrefix(srv.URL, "http") + "/containers/abc/exec"
}

func TestStreamContainerExec_TriesShFirst(t *testing.T) {
	md := &mockDocker{}
	_, wsURL := execTestServer(t, md)

	// Dial as a WebSocket — upgrade succeeds, ExecCreate is reached.
	// ExecAttach will return the stub error and close; we only care which shell
	// was tried first via ExecCreate.
	conn, _, _ := websocket.DefaultDialer.Dial(wsURL+"?rows=24&cols=80", nil)
	if conn != nil {
		conn.Close()
	}
	// Give the handler goroutine a moment to record the call.
	time.Sleep(50 * time.Millisecond)

	if len(md.execShells) == 0 {
		t.Fatal("expected ExecCreate to be called at least once")
	}
	// /bin/sh must be first — it is the universal POSIX shell present on all
	// images that have a shell (Alpine, Debian, Ubuntu, BusyBox, etc.).
	if md.execShells[0] != "/bin/sh" {
		t.Errorf("expected first shell tried to be /bin/sh, got %q", md.execShells[0])
	}
}

func TestStreamContainerExec_FallsBackToBash(t *testing.T) {
	// /bin/sh fails at ExecCreate (e.g. container reports it missing).
	// Handler should fall back to /bin/bash.
	fm := &failFirstMock{inner: &mockDocker{}, failCount: 1}
	_, wsURL := execTestServer(t, fm)

	conn, _, _ := websocket.DefaultDialer.Dial(wsURL+"?rows=24&cols=80", nil)
	if conn != nil {
		conn.Close()
	}
	time.Sleep(50 * time.Millisecond)

	// The second call (fallback) should have succeeded with /bin/bash.
	if len(fm.inner.execShells) == 0 {
		t.Fatal("expected at least one successful ExecCreate (fallback)")
	}
	if fm.inner.execShells[0] != "/bin/bash" {
		t.Errorf("expected fallback shell /bin/bash, got %q", fm.inner.execShells[0])
	}
}

func TestStreamContainerExec_AllShellsFail(t *testing.T) {
	// All shells fail — handler should close cleanly without panic.
	fm := &failFirstMock{inner: &mockDocker{}, failCount: 99}
	_, wsURL := execTestServer(t, fm)

	conn, _, _ := websocket.DefaultDialer.Dial(wsURL+"?rows=24&cols=80", nil)
	if conn != nil {
		// Read the error message the handler writes back
		conn.SetReadDeadline(time.Now().Add(time.Second))
		_, msg, err := conn.ReadMessage()
		conn.Close()
		if err == nil && len(msg) == 0 {
			t.Error("expected error message from handler when all shells fail")
		}
	}
}

// failFirstMock wraps a mockDocker and returns an error on the first ExecCreate call.
type failFirstMock struct {
	inner     *mockDocker
	failCount int
	calls     int
}

func (f *failFirstMock) Ping(ctx context.Context) error { return f.inner.Ping(ctx) }
func (f *failFirstMock) Close() error                   { return nil }
func (f *failFirstMock) ListContainers(ctx context.Context) ([]docker.ContainerSummary, error) {
	return nil, nil
}
func (f *failFirstMock) GetContainer(ctx context.Context, id string) (*docker.ContainerSummary, error) {
	return nil, nil
}
func (f *failFirstMock) GetContainerStats(ctx context.Context, id string) (*docker.ContainerStats, error) {
	return nil, nil
}
func (f *failFirstMock) GetContainerLogs(ctx context.Context, id string, lines int) (string, error) {
	return "", nil
}
func (f *failFirstMock) StreamLogs(ctx context.Context, id string, lines int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *failFirstMock) GetHostInfo(ctx context.Context) (*docker.HostInfo, error) { return nil, nil }
func (f *failFirstMock) ListImages(ctx context.Context) ([]docker.ImageSummary, error) {
	return nil, nil
}
func (f *failFirstMock) StartContainer(ctx context.Context, id string) error   { return nil }
func (f *failFirstMock) StopContainer(ctx context.Context, id string) error    { return nil }
func (f *failFirstMock) RestartContainer(ctx context.Context, id string) error { return nil }
func (f *failFirstMock) GetContainerEnv(ctx context.Context, id string) ([]string, error) {
	return nil, nil
}
func (f *failFirstMock) ExecCreate(ctx context.Context, containerID string, cmd []string, rows, cols int) (string, error) {
	f.calls++
	if f.calls <= f.failCount {
		return "", fmt.Errorf("exec: %q not found", cmd)
	}
	f.inner.execShells = append(f.inner.execShells, cmd[0])
	return "exec-id-456", nil
}
func (f *failFirstMock) ExecAttach(ctx context.Context, execID string) (types.HijackedResponse, error) {
	return types.HijackedResponse{}, fmt.Errorf("stub")
}
func (f *failFirstMock) ExecResize(ctx context.Context, execID string, rows, cols int) error {
	return nil
}
func (f *failFirstMock) Events(ctx context.Context) (<-chan docker.ContainerEvent, <-chan error) {
	return make(chan docker.ContainerEvent), make(chan error)
}
func (f *failFirstMock) ListVolumes(ctx context.Context) ([]docker.VolumeSummary, error) {
	return nil, nil
}
