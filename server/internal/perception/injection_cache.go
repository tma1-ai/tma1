package perception

import (
	"sync"
	"time"
)

// InjectionCache tracks the last digest emitted to each session so we can
// skip repeating identical context turn after turn. The biggest dogfood
// noise source: ~70% of a turn's bundle was unchanged from the previous
// turn but still re-injected.
//
// Safe for concurrent use; the hook handler may call IfChanged from many
// goroutines.
type InjectionCache struct {
	mu    sync.Mutex
	items map[string]injectionCacheEntry
	ttl   time.Duration
}

type injectionCacheEntry struct {
	digest  Digest
	expires time.Time
}

// NewInjectionCache returns a cache with the given per-entry TTL. Entries
// silently expire so a long-idle session re-emits the full bundle on its
// next turn (we want to re-orient the agent after a break).
func NewInjectionCache(ttl time.Duration) *InjectionCache {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &InjectionCache{
		items: make(map[string]injectionCacheEntry),
		ttl:   ttl,
	}
}

// IfChanged returns true iff the new digest differs from what was last
// stored for sessionID. The cache is updated to `d` whenever this returns
// true (so the next call sees the new baseline).
//
// First call for a session always returns true (no previous baseline).
// Forced bypass: pass sessionID="" and the call always returns true
// without touching the cache.
func (c *InjectionCache) IfChanged(sessionID string, d Digest) bool {
	if sessionID == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	prev, ok := c.items[sessionID]
	now := time.Now()
	if ok && now.After(prev.expires) {
		delete(c.items, sessionID)
		ok = false
	}
	if ok && prev.digest.Equal(d) {
		// Bump expiry so a stable-but-active session doesn't fall out.
		c.items[sessionID] = injectionCacheEntry{digest: prev.digest, expires: now.Add(c.ttl)}
		return false
	}

	c.items[sessionID] = injectionCacheEntry{digest: d, expires: now.Add(c.ttl)}
	c.opportunisticGC(now)
	return true
}

// Forget clears the cached entry for sessionID — useful when a session is
// known to have ended (so the next session with the same id re-injects).
func (c *InjectionCache) Forget(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, sessionID)
}

// opportunisticGC drops expired entries; called inline under the lock.
// Bounded to avoid pathological cost on large maps.
func (c *InjectionCache) opportunisticGC(now time.Time) {
	if len(c.items) < 64 {
		return
	}
	checked := 0
	for k, v := range c.items {
		if now.After(v.expires) {
			delete(c.items, k)
		}
		checked++
		if checked >= 32 {
			break
		}
	}
}
