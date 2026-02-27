package notifications

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DeviceRecord holds the APNs token and metadata for a registered device.
type DeviceRecord struct {
	Token        string    `json:"token"`
	Environment  string    `json:"environment"` // "development" | "production"
	RegisteredAt time.Time `json:"registered_at"`
}

// TokenStore is a thread-safe, file-backed registry of device APNs tokens.
// It is keyed by device_id (from the JWT claims).
type TokenStore struct {
	mu     sync.RWMutex
	tokens map[string]DeviceRecord
	path   string
}

// NewTokenStore creates a TokenStore, loading any previously persisted tokens.
func NewTokenStore(configDir string) *TokenStore {
	ts := &TokenStore{
		tokens: make(map[string]DeviceRecord),
		path:   filepath.Join(configDir, "device-tokens.json"),
	}
	ts.load()
	return ts
}

// Register adds or updates an APNs token for a device.
func (ts *TokenStore) Register(deviceID, token, environment string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.tokens[deviceID] = DeviceRecord{
		Token:        token,
		Environment:  environment,
		RegisteredAt: time.Now(),
	}
	ts.save()
}

// Unregister removes the APNs token for a device.
func (ts *TokenStore) Unregister(deviceID string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.tokens, deviceID)
	ts.save()
}

// All returns a snapshot of all registered device records.
func (ts *TokenStore) All() []DeviceRecord {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	records := make([]DeviceRecord, 0, len(ts.tokens))
	for _, r := range ts.tokens {
		records = append(records, r)
	}
	return records
}

func (ts *TokenStore) load() {
	data, err := os.ReadFile(ts.path)
	if err != nil {
		return // no file yet — start fresh
	}
	if err := json.Unmarshal(data, &ts.tokens); err != nil {
		log.Printf("[tokenstore] corrupt token file, starting fresh: %v", err)
	}
}

func (ts *TokenStore) save() {
	data, err := json.MarshalIndent(ts.tokens, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(ts.path), 0700); err != nil {
		log.Printf("[tokenstore] cannot create directory: %v", err)
		return
	}
	if err := os.WriteFile(ts.path, data, 0600); err != nil {
		log.Printf("[tokenstore] save failed: %v", err)
	}
}
