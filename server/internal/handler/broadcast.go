package handler

import "sync"

// HookBroadcaster fans out hook events to SSE subscribers.
type HookBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan []byte]struct{}
}

// NewHookBroadcaster creates a broadcaster for real-time hook event streaming.
func NewHookBroadcaster() *HookBroadcaster {
	return &HookBroadcaster{
		subscribers: make(map[chan []byte]struct{}),
	}
}

// Subscribe returns a buffered channel that will receive broadcast events.
func (b *HookBroadcaster) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *HookBroadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
	close(ch)
}

// Broadcast sends data to all subscribers. Slow consumers are dropped (non-blocking).
func (b *HookBroadcaster) Broadcast(data []byte) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- data:
		default:
			// Slow consumer — drop event to avoid blocking hook ingest.
		}
	}
}
