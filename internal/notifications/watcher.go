package notifications

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/dockertab/agent/internal/docker"
)

type Watcher struct {
	docker    docker.DockerClient
	store     *TokenStore
	client    *APNsClient
	agentID   string
	agentName string
	sandbox   bool
}

func NewWatcher(dockerClient docker.DockerClient, store *TokenStore, client *APNsClient, agentID, agentName string, sandbox bool) *Watcher {
	return &Watcher{
		docker:    dockerClient,
		store:     store,
		client:    client,
		agentID:   agentID,
		agentName: agentName,
		sandbox:   sandbox,
	}
}

// Start processes Docker events until ctx is cancelled or the stream errors.
// die+start pairs within 1.5s are collapsed into a single "restart" notification.
func (w *Watcher) Start(ctx context.Context) error {
	log.Println("[Notifications] Docker events watcher started")

	type pendingEntry struct {
		name  string
		timer *time.Timer
	}
	var pendingMu sync.Mutex
	pending := make(map[string]*pendingEntry)

	messages, errs := w.docker.Events(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Println("[Notifications] Docker events watcher stopped")
			return nil
		case err := <-errs:
			return fmt.Errorf("docker events stream: %w", err)
		case msg, ok := <-messages:
			if !ok {
				return nil
			}
			id, name := msg.ContainerID, msg.ContainerName
			if name == "" {
				if len(id) >= 12 {
					name = id[:12]
				} else {
					name = id
				}
			}
			switch msg.Action {
			case "die":
				pendingMu.Lock()
				if p, ok := pending[id]; ok {
					p.timer.Stop()
				}
				entry := &pendingEntry{name: name}
				entry.timer = time.AfterFunc(1500*time.Millisecond, func() {
					if ctx.Err() != nil {
						return
					}
					pendingMu.Lock()
					if pending[id] != entry {
						pendingMu.Unlock()
						return
					}
					delete(pending, id)
					pendingMu.Unlock()
					w.push(ctx, id, name, "die")
				})
				pending[id] = entry
				pendingMu.Unlock()
			case "start":
				pendingMu.Lock()
				if p, ok := pending[id]; ok {
					p.timer.Stop()
					delete(pending, id)
					pendingMu.Unlock()
					w.push(ctx, id, name, "restart")
				} else {
					pendingMu.Unlock()
					w.push(ctx, id, name, "start")
				}
			}
		}
	}
}

func (w *Watcher) push(ctx context.Context, containerID, name, action string) {
	displayHost := w.agentName
	if displayHost == "" {
		displayHost = "agent"
	}

	var title, body string
	switch action {
	case "die":
		title = "Container Stopped"
		body = fmt.Sprintf("%s stopped on %s", name, displayHost)
	case "restart":
		title = "Container Restarted"
		body = fmt.Sprintf("%s restarted on %s", name, displayHost)
	case "start":
		title = "Container Started"
		body = fmt.Sprintf("%s started on %s", name, displayHost)
	default:
		return
	}

	tokens := w.store.All()
	if len(tokens) == 0 {
		return
	}
	for _, record := range tokens {
		sandbox := w.sandbox || record.Environment == "development"
		if err := w.client.Push(ctx, record.Token, title, body, containerID, name, w.agentID, sandbox); err != nil {
			log.Printf("[Notifications] push failed: %v", err)
		}
	}
}
