package notifications

import (
	"context"
	"sync"
	"time"
)

// Sender receives debounced container lifecycle notifications.
type Sender interface {
	Send(ctx context.Context, id, name, action string)
}

type debounceEntry struct {
	name  string
	timer *time.Timer
}

// Debouncer collapses die+start pairs within 1.5s into a single "restart" notification.
type Debouncer struct {
	sender  Sender
	mu      sync.Mutex
	pending map[string]*debounceEntry
}

func NewDebouncer(sender Sender) *Debouncer {
	return &Debouncer{
		sender:  sender,
		pending: make(map[string]*debounceEntry),
	}
}

func (d *Debouncer) OnDie(ctx context.Context, id, name string) {
	d.mu.Lock()
	if p, ok := d.pending[id]; ok {
		p.timer.Stop()
	}
	entry := &debounceEntry{name: name}
	entry.timer = time.AfterFunc(1500*time.Millisecond, func() {
		if ctx.Err() != nil {
			return
		}
		d.mu.Lock()
		if d.pending[id] != entry {
			d.mu.Unlock()
			return
		}
		delete(d.pending, id)
		d.mu.Unlock()
		d.sender.Send(ctx, id, name, "die")
	})
	d.pending[id] = entry
	d.mu.Unlock()
}

func (d *Debouncer) OnStart(ctx context.Context, id, name string) {
	d.mu.Lock()
	if p, ok := d.pending[id]; ok {
		p.timer.Stop()
		delete(d.pending, id)
		d.mu.Unlock()
		d.sender.Send(ctx, id, name, "restart")
		return
	}
	d.mu.Unlock()
	d.sender.Send(ctx, id, name, "start")
}
