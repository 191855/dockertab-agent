package notifications

import (
	"context"
	"errors"
	"fmt"
	"log"

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

	debouncer := NewDebouncer(w)
	messages, errs := w.docker.Events(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Println("[Notifications] Docker events watcher stopped")
			return nil
		case err := <-errs:
			if ctx.Err() != nil {
				log.Println("[Notifications] Docker events watcher stopped")
				return nil
			}
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
				debouncer.OnDie(ctx, id, name)
			case "start":
				debouncer.OnStart(ctx, id, name)
			}
		}
	}
}

func containsEvent(events []string, action string) bool {
	for _, e := range events {
		if e == action {
			return true
		}
	}
	return false
}

func (w *Watcher) Send(ctx context.Context, id, name, action string) {
	w.push(ctx, id, name, action)
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
		if len(record.Events) > 0 && !containsEvent(record.Events, action) {
			continue
		}
		sandbox := w.sandbox || record.Environment == "development"
		if err := w.client.Push(ctx, record.Token, title, body, containerID, name, w.agentID, sandbox); err != nil {
			var expired *TokenExpiredError
			if errors.As(err, &expired) {
				log.Printf("[Notifications] removing expired token %.16s...", expired.DeviceToken)
				w.store.UnregisterByToken(expired.DeviceToken)
			} else {
				log.Printf("[Notifications] push failed: %v", err)
			}
		}
	}
}
