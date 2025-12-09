// Package registry provides the internal tool registry service implementation.
package registry

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"goa.design/pulse/pool"
	"goa.design/pulse/rmap"
)

type (
	// HealthTracker tracks provider health status for toolsets.
	// It manages ping/pong health checks to detect when providers become unavailable,
	// enabling fast failure instead of timeouts when all providers are unhealthy.
	//
	// The tracker uses two Pulse replicated maps:
	// 1. A registry map that tracks which toolsets are registered (for cross-node coordination)
	// 2. A health map that stores the last pong timestamp for each toolset
	//
	// All nodes subscribe to the registry map. When a toolset is registered/unregistered,
	// all nodes see the change and start/stop their distributed ticker participation.
	// The distributed ticker ensures only one node sends pings at a time, with automatic
	// failover if that node crashes.
	HealthTracker interface {
		// RecordPong records a pong response for a toolset.
		// This updates the last pong timestamp in the shared health map.
		RecordPong(ctx context.Context, toolset string) error

		// IsHealthy returns whether a toolset has healthy providers.
		// A toolset is healthy if a pong was received within the staleness threshold.
		IsHealthy(toolset string) bool

		// StartPingLoop registers a toolset for health tracking across all nodes.
		// All nodes will participate in the distributed ticker for this toolset.
		StartPingLoop(ctx context.Context, toolset string) error

		// StopPingLoop unregisters a toolset from health tracking across all nodes.
		// All nodes will stop their ticker participation for this toolset.
		StopPingLoop(ctx context.Context, toolset string)

		// Close stops all ping loops and releases resources.
		Close() error
	}

	// HealthTrackerOption configures optional settings for the health tracker.
	HealthTrackerOption func(*healthTrackerOptions)

	healthTrackerOptions struct {
		pingInterval        time.Duration
		missedPingThreshold int
	}

	healthTracker struct {
		streamManager       StreamManager
		healthMap           *rmap.Map // stores last pong timestamps
		registryMap         *rmap.Map // tracks registered toolsets for cross-node coordination
		poolNode            *pool.Node
		pingInterval        time.Duration
		missedPingThreshold int
		stalenessThreshold  time.Duration

		mu      sync.RWMutex
		tickers map[string]*pool.Ticker
		cancels map[string]context.CancelFunc

		closeOnce sync.Once
		closeCh   chan struct{}
	}
)

const (
	// DefaultPingInterval is the default interval between health check pings.
	DefaultPingInterval = 10 * time.Second
	// DefaultMissedPingThreshold is the default number of consecutive missed pings
	// before marking a toolset as unhealthy.
	DefaultMissedPingThreshold = 3

	healthKeyPrefix   = "registry:health:"
	registryKeyPrefix = "registry:toolsets:"
)

// WithPingInterval sets the interval between health check pings.
func WithPingInterval(d time.Duration) HealthTrackerOption {
	return func(o *healthTrackerOptions) {
		o.pingInterval = d
	}
}

// WithMissedPingThreshold sets the number of consecutive missed pings
// before marking a toolset as unhealthy.
func WithMissedPingThreshold(n int) HealthTrackerOption {
	return func(o *healthTrackerOptions) {
		o.missedPingThreshold = n
	}
}

// NewHealthTracker creates a new HealthTracker.
// streamManager is used to publish ping messages to toolset streams.
// healthMap is the Pulse replicated map for storing health state (last pong timestamps).
// registryMap is the Pulse replicated map for tracking registered toolsets across nodes.
// node is the Pulse pool node for creating distributed tickers.
func NewHealthTracker(streamManager StreamManager, healthMap, registryMap *rmap.Map, node *pool.Node, opts ...HealthTrackerOption) (HealthTracker, error) {
	if streamManager == nil {
		return nil, fmt.Errorf("stream manager is required")
	}
	if healthMap == nil {
		return nil, fmt.Errorf("health map is required for distributed health tracking")
	}
	if registryMap == nil {
		return nil, fmt.Errorf("registry map is required for cross-node coordination")
	}
	if node == nil {
		return nil, fmt.Errorf("pool node is required for distributed tickers")
	}

	options := &healthTrackerOptions{
		pingInterval:        DefaultPingInterval,
		missedPingThreshold: DefaultMissedPingThreshold,
	}
	for _, opt := range opts {
		opt(options)
	}

	// Staleness threshold = (missedPingThreshold + 1) * pingInterval
	// This gives providers enough time to respond before being marked unhealthy.
	stalenessThreshold := time.Duration(options.missedPingThreshold+1) * options.pingInterval

	h := &healthTracker{
		streamManager:       streamManager,
		healthMap:           healthMap,
		registryMap:         registryMap,
		poolNode:            node,
		pingInterval:        options.pingInterval,
		missedPingThreshold: options.missedPingThreshold,
		stalenessThreshold:  stalenessThreshold,
		tickers:             make(map[string]*pool.Ticker),
		cancels:             make(map[string]context.CancelFunc),
		closeCh:             make(chan struct{}),
	}

	// Start watching for registry changes from other nodes.
	go h.watchRegistryChanges()

	// Sync with existing registered toolsets.
	h.syncExistingToolsets()

	return h, nil
}

func (h *healthTracker) RecordPong(ctx context.Context, toolset string) error {
	key := healthKey(toolset)
	ts := time.Now().UnixNano()
	_, err := h.healthMap.Set(ctx, key, strconv.FormatInt(ts, 10))
	if err != nil {
		return fmt.Errorf("record pong: %w", err)
	}
	return nil
}

func (h *healthTracker) IsHealthy(toolset string) bool {
	key := healthKey(toolset)
	val, ok := h.healthMap.Get(key)
	if !ok {
		return false
	}
	ts, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return false
	}
	lastPong := time.Unix(0, ts)
	return time.Since(lastPong) <= h.stalenessThreshold
}

func (h *healthTracker) StartPingLoop(ctx context.Context, toolset string) error {
	// Add to registry map - this notifies all nodes.
	key := registryKey(toolset)
	ts := time.Now().UnixNano()
	_, err := h.registryMap.Set(ctx, key, strconv.FormatInt(ts, 10))
	if err != nil {
		return fmt.Errorf("register toolset: %w", err)
	}

	// Initialize health with current timestamp so toolset starts healthy.
	healthK := healthKey(toolset)
	_, _ = h.healthMap.SetIfNotExists(ctx, healthK, strconv.FormatInt(ts, 10))

	// Start local ticker (other nodes will do the same via watchRegistryChanges).
	return h.startLocalTicker(ctx, toolset)
}

func (h *healthTracker) StopPingLoop(ctx context.Context, toolset string) {
	// Remove from registry map - this notifies all nodes.
	key := registryKey(toolset)
	_, _ = h.registryMap.Delete(ctx, key)

	// Clean up health state.
	healthK := healthKey(toolset)
	_, _ = h.healthMap.Delete(ctx, healthK)

	// Stop local ticker (other nodes will do the same via watchRegistryChanges).
	h.stopLocalTicker(toolset)
}

func (h *healthTracker) Close() error {
	h.closeOnce.Do(func() {
		close(h.closeCh)

		h.mu.Lock()
		defer h.mu.Unlock()

		for _, cancel := range h.cancels {
			cancel()
		}
		for _, ticker := range h.tickers {
			ticker.Stop()
		}
		h.tickers = make(map[string]*pool.Ticker)
		h.cancels = make(map[string]context.CancelFunc)
	})
	return nil
}

// watchRegistryChanges subscribes to the registry map and reacts to changes
// from other nodes.
func (h *healthTracker) watchRegistryChanges() {
	events := h.registryMap.Subscribe()
	defer h.registryMap.Unsubscribe(events)

	for {
		select {
		case <-h.closeCh:
			return
		case <-events:
			h.syncWithRegistry()
		}
	}
}

// syncExistingToolsets syncs with toolsets that were registered before this node started.
func (h *healthTracker) syncExistingToolsets() {
	h.syncWithRegistry()
}

// syncWithRegistry ensures local tickers match the registry map state.
func (h *healthTracker) syncWithRegistry() {
	// Get all registered toolsets from the registry map.
	registered := make(map[string]bool)
	for _, key := range h.registryMap.Keys() {
		toolset := toolsetFromRegistryKey(key)
		if toolset != "" {
			registered[toolset] = true
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Start tickers for newly registered toolsets.
	for toolset := range registered {
		if _, ok := h.tickers[toolset]; !ok {
			// Use background context since this is triggered by map changes.
			_ = h.startLocalTickerLocked(context.Background(), toolset)
		}
	}

	// Stop tickers for unregistered toolsets.
	for toolset := range h.tickers {
		if !registered[toolset] {
			h.stopLocalTickerLocked(toolset)
		}
	}
}

// startLocalTicker starts a distributed ticker for a toolset on this node.
func (h *healthTracker) startLocalTicker(ctx context.Context, toolset string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.startLocalTickerLocked(ctx, toolset)
}

func (h *healthTracker) startLocalTickerLocked(ctx context.Context, toolset string) error {
	if _, ok := h.tickers[toolset]; ok {
		return nil
	}

	// Create a distributed ticker - only one node in the pool will receive ticks.
	tickerName := fmt.Sprintf("registry:ping:%s", toolset)
	ticker, err := h.poolNode.NewTicker(ctx, tickerName, h.pingInterval)
	if err != nil {
		return fmt.Errorf("create distributed ticker: %w", err)
	}

	// Use a fresh context for the ping loop that's only cancelled when we explicitly stop.
	// This ensures the loop survives even if the original ctx is cancelled.
	loopCtx, cancel := context.WithCancel(context.Background())
	h.tickers[toolset] = ticker
	h.cancels[toolset] = cancel
	go h.runPingLoop(loopCtx, toolset, ticker)

	return nil
}

// stopLocalTicker stops the distributed ticker for a toolset on this node.
func (h *healthTracker) stopLocalTicker(toolset string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLocalTickerLocked(toolset)
}

func (h *healthTracker) stopLocalTickerLocked(toolset string) {
	if cancel, ok := h.cancels[toolset]; ok {
		cancel()
		delete(h.cancels, toolset)
	}
	if ticker, ok := h.tickers[toolset]; ok {
		ticker.Stop()
		delete(h.tickers, toolset)
	}
}

func healthKey(toolset string) string {
	return healthKeyPrefix + toolset
}

func registryKey(toolset string) string {
	return registryKeyPrefix + toolset
}

func toolsetFromRegistryKey(key string) string {
	if len(key) > len(registryKeyPrefix) {
		return key[len(registryKeyPrefix):]
	}
	return ""
}

func (h *healthTracker) runPingLoop(ctx context.Context, toolset string, ticker *pool.Ticker) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.sendPing(ctx, toolset)
		}
	}
}

func (h *healthTracker) sendPing(ctx context.Context, toolset string) {
	pingID := uuid.New().String()
	msg := NewPingMessage(pingID)
	_ = h.streamManager.PublishToolCall(ctx, toolset, msg)
}
