package notifications

import (
	"context"
	"fmt"
	"log"

	"github.com/dockertab/agent/internal/docker"
)

// Watcher subscribes to Docker container events and pushes APNs notifications
// for start, stop, and die events.
type Watcher struct {
	docker    docker.DockerClient
	store     *TokenStore
	client    *APNsClient
	agentID   string
	agentName string
	sandbox   bool
}

// NewWatcher creates a Watcher. agentName is the display name shown in notifications.
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

// Start blocks until ctx is cancelled or Docker events stream errors out.
func (w *Watcher) Start(ctx context.Context) error {
	log.Println("[Notifications] Docker events watcher started")
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
			w.dispatch(ctx, msg)
		}
	}
}

func (w *Watcher) dispatch(ctx context.Context, event docker.ContainerEvent) {
	var title, body string
	name := event.ContainerName
	if name == "" {
		if len(event.ContainerID) >= 12 {
			name = event.ContainerID[:12]
		} else {
			name = event.ContainerID
		}
	}

	displayHost := w.agentName
	if displayHost == "" {
		displayHost = "agent"
	}

	switch event.Action {
	case "die", "stop", "kill":
		title = "Container Stopped"
		body = fmt.Sprintf("%s stopped on %s", name, displayHost)
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
		if err := w.client.Push(ctx, record.Token, title, body, event.ContainerID, name, w.agentID, sandbox); err != nil {
			log.Printf("[Notifications] push failed: %v", err)
		}
	}
}
