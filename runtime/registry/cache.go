package registry

import (
	"context"
	"sync"
	"time"
)

// Cache defines the interface for caching toolset schemas.
type Cache interface {
	// Get retrieves a cached toolset schema by key.
	// Returns nil, nil if the key is not found or expired.
	Get(ctx context.Context, key string) (*ToolsetSchema, error)
	// Set stores a toolset schema with the given TTL.
	Set(ctx context.Context, key string, schema *ToolsetSchema, ttl time.Duration) error
	// Delete removes a cached entry.
	Delete(ctx context.Context, key string) error
}

// RefreshFunc is called when a cache entry needs to be refreshed.
// It receives the key and should return the refreshed schema.
type RefreshFunc func(ctx context.Context, key string) (*ToolsetSchema, error)

// MemoryCache is an in-memory cache implementation with TTL support
// and optional background refresh.
type MemoryCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry

	// Background refresh configuration
	refreshFunc     RefreshFunc
	refreshCooldown time.Duration
	refreshCtx      context.Context
	refreshCancel   context.CancelFunc
	refreshWg       sync.WaitGroup
	refreshCh       chan string
}

type cacheEntry struct {
	schema    *ToolsetSchema
	expiresAt time.Time
	ttl       time.Duration // Original TTL for refresh
}

// MemoryCacheOption configures a MemoryCache.
type MemoryCacheOption func(*MemoryCache)

// WithRefreshFunc sets the function used to refresh expired entries.
// When set, the cache will attempt to refresh entries in the background
// before they expire.
func WithRefreshFunc(fn RefreshFunc) MemoryCacheOption {
	return func(c *MemoryCache) {
		c.refreshFunc = fn
	}
}

// WithRefreshCooldown sets the minimum interval between refresh attempts
// for the same key. Defaults to 10 seconds if not set.
func WithRefreshCooldown(d time.Duration) MemoryCacheOption {
	return func(c *MemoryCache) {
		c.refreshCooldown = d
	}
}

// NewMemoryCache creates a new in-memory cache.
func NewMemoryCache(opts ...MemoryCacheOption) *MemoryCache {
	c := &MemoryCache{
		entries:         make(map[string]*cacheEntry),
		refreshCh:       make(chan string, 100),
		refreshCooldown: 10 * time.Second, // Default cooldown
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get retrieves a cached toolset schema by key.
// If the entry is approaching expiration (within 20% of TTL), a background
// refresh is triggered if a refresh function is configured.
func (c *MemoryCache) Get(_ context.Context, key string) (*ToolsetSchema, error) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return nil, nil
	}

	now := time.Now()
	if now.After(entry.expiresAt) {
		// Entry expired, delete it
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return nil, nil
	}

	// Trigger background refresh if approaching expiration (within 20% of TTL)
	if c.refreshFunc != nil && entry.ttl > 0 {
		refreshThreshold := entry.expiresAt.Add(-entry.ttl / 5)
		if now.After(refreshThreshold) {
			c.triggerRefresh(key)
		}
	}

	return entry.schema, nil
}

// triggerRefresh sends a key to the refresh channel for background processing.
func (c *MemoryCache) triggerRefresh(key string) {
	// Only trigger if refresh loop is running
	if c.refreshCtx == nil {
		return
	}

	select {
	case c.refreshCh <- key:
		// Queued for refresh
	case <-c.refreshCtx.Done():
		// Refresh loop stopped
	default:
		// Channel full, skip this refresh
	}
}

// Set stores a toolset schema with the given TTL.
func (c *MemoryCache) Set(_ context.Context, key string, schema *ToolsetSchema, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &cacheEntry{
		schema:    schema,
		expiresAt: time.Now().Add(ttl),
		ttl:       ttl,
	}
	return nil
}

// Delete removes a cached entry.
func (c *MemoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, key)
	return nil
}

// Clear removes all cached entries.
func (c *MemoryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*cacheEntry)
}

// Len returns the number of entries in the cache.
func (c *MemoryCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}

// StartRefresh starts the background refresh loop.
// The loop processes refresh requests and updates cache entries before they expire.
func (c *MemoryCache) StartRefresh(ctx context.Context) {
	if c.refreshFunc == nil {
		return
	}

	c.refreshCtx, c.refreshCancel = context.WithCancel(ctx)
	c.refreshWg.Add(1)
	go c.refreshLoop()
}

// StopRefresh stops the background refresh loop.
func (c *MemoryCache) StopRefresh() {
	if c.refreshCancel != nil {
		c.refreshCancel()
		c.refreshWg.Wait()
		c.refreshCancel = nil
	}
}

// refreshLoop processes refresh requests from the channel.
func (c *MemoryCache) refreshLoop() {
	defer c.refreshWg.Done()

	// Track recently refreshed keys to avoid duplicate refreshes
	refreshed := make(map[string]time.Time)

	for {
		select {
		case <-c.refreshCtx.Done():
			return
		case key := <-c.refreshCh:
			// Skip if recently refreshed
			if lastRefresh, ok := refreshed[key]; ok {
				if time.Since(lastRefresh) < c.refreshCooldown {
					continue
				}
			}

			// Get current entry to check if refresh is still needed
			c.mu.RLock()
			entry, exists := c.entries[key]
			c.mu.RUnlock()

			if !exists {
				continue
			}

			// Refresh the entry
			schema, err := c.refreshFunc(c.refreshCtx, key)
			if err != nil {
				// Keep existing entry on refresh failure
				continue
			}

			// Update the cache with refreshed data
			c.mu.Lock()
			c.entries[key] = &cacheEntry{
				schema:    schema,
				expiresAt: time.Now().Add(entry.ttl),
				ttl:       entry.ttl,
			}
			c.mu.Unlock()

			refreshed[key] = time.Now()

			// Clean up old refresh tracking entries periodically
			if len(refreshed) > 1000 {
				now := time.Now()
				for k, t := range refreshed {
					if now.Sub(t) > time.Minute {
						delete(refreshed, k)
					}
				}
			}
		}
	}
}
