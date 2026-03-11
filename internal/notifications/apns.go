package notifications

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const apnsBundleID = "com.dockertab.app"

// TokenExpiredError is returned when APNs responds with 410 (Unregistered),
// indicating the device token is no longer valid and should be removed.
type TokenExpiredError struct {
	DeviceToken string
	Reason      string
}

func (e *TokenExpiredError) Error() string {
	return fmt.Sprintf("APNs token expired: %s", e.Reason)
}

type APNsClient struct {
	keyID  string
	teamID string
	key    *ecdsa.PrivateKey

	// JWT token cache (valid 60 min; refresh at 55)
	mu       sync.Mutex
	token    string
	tokenExp time.Time

	httpClient *http.Client
}

func NewAPNsClient(keyFile, keyID, teamID string) (*APNsClient, error) {
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read APNs key file %q: %w", keyFile, err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("APNs key file is not valid PEM")
	}

	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse APNs private key: %w", err)
	}

	ecKey, ok := raw.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("APNs key must be an EC private key (P-256)")
	}

	return &APNsClient{
		keyID:      keyID,
		teamID:     teamID,
		key:        ecKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// bearerToken returns a cached ES256 JWT, refreshing every 55 minutes (APNs tokens expire after 60).
func (c *APNsClient) bearerToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}

	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": c.teamID,
		"iat": time.Now().Unix(),
	})
	t.Header["kid"] = c.keyID

	signed, err := t.SignedString(c.key)
	if err != nil {
		return "", fmt.Errorf("sign APNs bearer token: %w", err)
	}

	c.token = signed
	c.tokenExp = time.Now().Add(55 * time.Minute)
	return signed, nil
}

type apnsPayload struct {
	APS           apnsAPS `json:"aps"`
	ContainerID   string  `json:"container_id,omitempty"`
	ContainerName string  `json:"container_name,omitempty"`
	AgentID       string  `json:"agent_id,omitempty"`
}

type apnsAPS struct {
	Alert apnsAlert `json:"alert"`
	Sound string    `json:"sound"`
}

type apnsAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (c *APNsClient) Push(ctx context.Context, deviceToken, title, body, containerID, containerName, agentID string, sandbox bool) error {
	bearer, err := c.bearerToken()
	if err != nil {
		return err
	}

	payload := apnsPayload{
		APS: apnsAPS{
			Alert: apnsAlert{Title: title, Body: body},
			Sound: "default",
		},
		ContainerID:   containerID,
		ContainerName: containerName,
		AgentID:       agentID,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal APNs payload: %w", err)
	}

	host := "https://api.push.apple.com"
	if sandbox {
		host = "https://api.sandbox.push.apple.com"
	}
	url := fmt.Sprintf("%s/3/device/%s", host, deviceToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create APNs request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("apns-topic", apnsBundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("APNs HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var apnsErr struct {
			Reason    string `json:"reason"`
			Timestamp int64  `json:"timestamp"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apnsErr)
		log.Printf("[APNs] push failed: status=%d reason=%s token=%.16s...", resp.StatusCode, apnsErr.Reason, deviceToken)
		if resp.StatusCode == http.StatusGone {
			return &TokenExpiredError{DeviceToken: deviceToken, Reason: apnsErr.Reason}
		}
		return fmt.Errorf("APNs %d: %s", resp.StatusCode, apnsErr.Reason)
	}

	return nil
}
