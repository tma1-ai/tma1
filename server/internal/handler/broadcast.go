package handler

import "sync"

// hookBroadcaster fans out hook events to SSE subscribers.
type hookBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan []byte]struct{}
}

func newHookBroadcaster() *hookBroadcaster {
	return &hookBroadcaster{
		subscribers: make(map[chan []byte]struct{}),
	}
}

// Subscribe returns a buffered channel that will receive broadcast events.
func (b *hookBroadcaster) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *hookBroadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
	close(ch)
}

// Broadcast sends data to all subscribers. Slow consumers are dropped (non-blocking).
func (b *hookBroadcaster) Broadcast(data []byte) {
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
