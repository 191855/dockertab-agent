package notifications

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type DeviceRecord struct {
	Token        string    `json:"token"`
	Environment  string    `json:"environment"` // "development" | "production"
	RegisteredAt time.Time `json:"registered_at"`
	Events       []string  `json:"events,omitempty"` // empty = all events
}

type TokenStore struct {
	mu     sync.RWMutex
	tokens map[string]DeviceRecord
	path   string
}

func NewTokenStore(configDir string) *TokenStore {
	ts := &TokenStore{
		tokens: make(map[string]DeviceRecord),
		path:   filepath.Join(configDir, "device-tokens.json"),
	}
	ts.load()
	return ts
}

func (ts *TokenStore) Register(deviceID, token, environment string, events []string) {
	ts.mu.Lock()
	ts.tokens[deviceID] = DeviceRecord{
		Token:        token,
		Environment:  environment,
		RegisteredAt: time.Now(),
		Events:       events,
	}
	ts.mu.Unlock()
	ts.save()
}

func (ts *TokenStore) Unregister(deviceID string) {
	ts.mu.Lock()
	delete(ts.tokens, deviceID)
	ts.mu.Unlock()
	ts.save()
}

func (ts *TokenStore) UnregisterByToken(token string) {
	ts.mu.Lock()
	for id, r := range ts.tokens {
		if r.Token == token {
			delete(ts.tokens, id)
			break
		}
	}
	ts.mu.Unlock()
	ts.save()
}

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
		return
	}
	if err := json.Unmarshal(data, &ts.tokens); err != nil {
		log.Printf("[tokenstore] corrupt token file, starting fresh: %v", err)
	}
}

func (ts *TokenStore) save() {
	ts.mu.RLock()
	data, err := json.MarshalIndent(ts.tokens, "", "  ")
	ts.mu.RUnlock()
	if err != nil {
		log.Printf("[tokenstore] marshal failed: %v", err)
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
