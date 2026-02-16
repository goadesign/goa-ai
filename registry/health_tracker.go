// Package registry provides the internal tool registry service implementation.
package registry

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
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
		// Health returns the current health state for a toolset.
		//
		// Contract:
		//   - Health is derived from the last recorded Pong timestamp and the
		//     configured staleness threshold.
		//   - If the toolset has never ponged (or no entry exists), Health reports
		//     Healthy=false with LastPong unset.
		Health(toolset string) (ToolsetHealth, error)

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

	// ToolsetHealth reports derived provider health for a toolset.
	ToolsetHealth struct {
		// Healthy reports whether a provider pong was received within the configured threshold.
		Healthy bool
		// LastPong is the timestamp of the last recorded pong when available.
		LastPong time.Time
		// Age is the duration since LastPong when available.
		Age time.Duration
		// StalenessThreshold is the configured maximum acceptable pong age.
		StalenessThreshold time.Duration
	}

	// HealthTrackerOption configures optional settings for the health tracker.
	HealthTrackerOption func(*healthTrackerOptions)

	healthTrackerOptions struct {
		pingInterval        time.Duration
		missedPingThreshold int
		logger              telemetry.Logger
	}

	healthTracker struct {
		streamManager       StreamManager
		healthMap           *rmap.Map // stores last pong timestamps
		registryMap         *rmap.Map // tracks registered toolsets for cross-node coordination
		poolNode            *pool.Node
		pingInterval        time.Duration
		missedPingThreshold int
		stalenessThreshold  time.Duration
		logger              telemetry.Logger

		mu      sync.RWMutex
		tickers map[string]*pool.Ticker
		cancels map[string]context.CancelFunc

		stateMu              sync.Mutex
		lastObservedHealthy  map[string]bool
		lastObservedPongNano map[string]int64

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

func WithHealthLogger(l telemetry.Logger) HealthTrackerOption {
	return func(o *healthTrackerOptions) {
		o.logger = l
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
		logger:              telemetry.NewNoopLogger(),
	}
	for _, opt := range opts {
		opt(options)
	}
	logger := options.logger
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}

	// Staleness threshold = (missedPingThreshold + 1) * pingInterval
	// This gives providers enough time to respond before being marked unhealthy.
	stalenessThreshold := time.Duration(options.missedPingThreshold+1) * options.pingInterval

	// Subscribe before spawning goroutine to avoid race: if a registry event
	// arrives before the goroutine calls Subscribe(), the event is missed.
	registryEvents := registryMap.Subscribe()

	h := &healthTracker{
		streamManager:        streamManager,
		healthMap:            healthMap,
		registryMap:          registryMap,
		poolNode:             node,
		pingInterval:         options.pingInterval,
		missedPingThreshold:  options.missedPingThreshold,
		stalenessThreshold:   stalenessThreshold,
		logger:               logger,
		tickers:              make(map[string]*pool.Ticker),
		cancels:              make(map[string]context.CancelFunc),
		lastObservedHealthy:  make(map[string]bool),
		lastObservedPongNano: make(map[string]int64),
		closeCh:              make(chan struct{}),
	}

	// Start watching for registry changes from other nodes.
	go h.watchRegistryChanges(registryEvents)

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

func (h *healthTracker) Health(toolset string) (ToolsetHealth, error) {
	key := healthKey(toolset)
	val, ok := h.healthMap.Get(key)
	if !ok {
		return ToolsetHealth{
			Healthy:            false,
			StalenessThreshold: h.stalenessThreshold,
		}, nil
	}
	ts, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return ToolsetHealth{}, fmt.Errorf("parse last pong timestamp for %q: %w", toolset, err)
	}
	lastPong := time.Unix(0, ts)
	age := time.Since(lastPong)
	healthy := age <= h.stalenessThreshold
	return ToolsetHealth{
		Healthy:            healthy,
		LastPong:           lastPong,
		Age:                age,
		StalenessThreshold: h.stalenessThreshold,
	}, nil
}

func (h *healthTracker) IsHealthy(toolset string) bool {
	hh, err := h.Health(toolset)
	if err != nil {
		return false
	}
	return hh.Healthy
}

func (h *healthTracker) StartPingLoop(ctx context.Context, toolset string) error {
	// Add to registry map - this notifies all nodes.
	key := registryKey(toolset)
	ts := time.Now().UnixNano()
	_, err := h.registryMap.Set(ctx, key, strconv.FormatInt(ts, 10))
	if err != nil {
		return fmt.Errorf("register toolset: %w", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	// (Re)start local ticker.
	//
	// In production we observed that a node can keep a stale *pool.Ticker in-memory
	// even after the shared ticker-map entry has been deleted remotely (e.g., by a
	// different node). In that case, the local ticker stops but the health tracker
	// still thinks it is running and will not recreate it, causing pings to stop.
	//
	// We solve this by explicitly closing the local ticker instance (without
	// deleting the shared entry) and recreating it on every StartPingLoop.
	if cancel, ok := h.cancels[toolset]; ok {
		cancel()
		delete(h.cancels, toolset)
	}
	if ticker, ok := h.tickers[toolset]; ok {
		ticker.Close()
		delete(h.tickers, toolset)
	}
	return h.startLocalTickerLocked(toolset)
}

func (h *healthTracker) StopPingLoop(ctx context.Context, toolset string) {
	// Remove from registry map - this notifies all nodes.
	key := registryKey(toolset)
	if _, err := h.registryMap.Delete(ctx, key); err != nil {
		h.logger.Error(ctx, "unregister toolset failed", "component", "tool-registry-health", "toolset", toolset, "key", key, "err", err)
	}

	// Clean up health state.
	healthK := healthKey(toolset)
	if _, err := h.healthMap.Delete(ctx, healthK); err != nil {
		h.logger.Error(ctx, "delete toolset health failed", "component", "tool-registry-health", "toolset", toolset, "key", healthK, "err", err)
	}

	// Stop local ticker (other nodes will do the same via watchRegistryChanges).
	h.stopLocalTicker(toolset)

	h.stateMu.Lock()
	delete(h.lastObservedHealthy, toolset)
	delete(h.lastObservedPongNano, toolset)
	h.stateMu.Unlock()
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
			// Close stops the ticker locally without deleting the shared
			// ticker-map entry.
			//
			// This is critical for distributed tickers: on shutdown/restart (or
			// rolling updates), a single node must not delete the shared entry
			// since that would stop pings for all nodes and can leave the cluster
			// with no active pinger.
			ticker.Close()
		}
		h.tickers = make(map[string]*pool.Ticker)
		h.cancels = make(map[string]context.CancelFunc)
	})
	return nil
}

// watchRegistryChanges reacts to registry map changes from other nodes.
// The events channel must be obtained via registryMap.Subscribe() before
// calling this method to avoid missing events that arrive between tracker
// construction and goroutine startup.
func (h *healthTracker) watchRegistryChanges(events <-chan rmap.EventKind) {
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
			if err := h.startLocalTickerLocked(toolset); err != nil {
				h.logger.Error(context.Background(), "start ticker failed", "event", "start_ticker_failed", "toolset", toolset, "err", err)
			}
		}
	}

	// Stop tickers for unregistered toolsets.
	for toolset := range h.tickers {
		if !registered[toolset] {
			h.stopLocalTickerLocked(toolset)
		}
	}
}

func (h *healthTracker) startLocalTickerLocked(toolset string) error {
	if _, ok := h.tickers[toolset]; ok {
		return nil
	}

	// Use a fresh context for the ping loop that's only cancelled when we explicitly stop.
	// This ensures the loop survives even if the caller ctx (e.g., an RPC request context)
	// is canceled as soon as the request completes.
	loopCtx, cancel := context.WithCancel(context.Background())

	// Create a distributed ticker - only one node in the pool will receive ticks.
	tickerName := fmt.Sprintf("registry:ping:%s", toolset)
	ticker, err := h.poolNode.NewTicker(loopCtx, tickerName, h.pingInterval)
	if err != nil {
		cancel()
		return fmt.Errorf("create distributed ticker: %w", err)
	}

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
	if !strings.HasPrefix(key, registryKeyPrefix) {
		return ""
	}
	return strings.TrimPrefix(key, registryKeyPrefix)
}

func (h *healthTracker) runPingLoop(ctx context.Context, toolset string, ticker *pool.Ticker) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.observeHealth(ctx, toolset)
			h.sendPing(ctx, toolset)
		}
	}
}

func (h *healthTracker) sendPing(ctx context.Context, toolset string) {
	pingID := uuid.New().String()
	msg := toolregistry.NewPingMessage(pingID)
	if err := h.streamManager.PublishToolCall(ctx, toolset, msg); err != nil {
		h.logger.Error(
			context.Background(),
			"publish ping failed",
			"event", "publish_ping_failed",
			"component", "tool-registry-health",
			"toolset", toolset,
			"ping_id", pingID,
			"err", err,
		)
	}
}

func (h *healthTracker) observeHealth(ctx context.Context, toolset string) {
	key := healthKey(toolset)
	val, ok := h.healthMap.Get(key)
	if !ok {
		h.noteHealth(ctx, toolset, false, 0, "missing_health_entry")
		return
	}
	ts, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		h.logger.Error(ctx, "parse pong timestamp failed", "component", "tool-registry-health", "toolset", toolset, "value", val, "err", err)
		h.noteHealth(ctx, toolset, false, 0, "invalid_health_timestamp")
		return
	}
	lastPong := time.Unix(0, ts)
	age := time.Since(lastPong)
	h.noteHealth(ctx, toolset, age <= h.stalenessThreshold, ts, "ok")
}

func (h *healthTracker) noteHealth(ctx context.Context, toolset string, healthy bool, lastPongNano int64, reason string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	prevHealthy, hasPrev := h.lastObservedHealthy[toolset]
	prevPong := h.lastObservedPongNano[toolset]

	h.lastObservedHealthy[toolset] = healthy
	if lastPongNano != 0 {
		h.lastObservedPongNano[toolset] = lastPongNano
	}

	if !hasPrev {
		return
	}
	if prevHealthy == healthy && prevPong == lastPongNano {
		return
	}

	now := time.Now()
	var lastPong time.Time
	if lastPongNano != 0 {
		lastPong = time.Unix(0, lastPongNano)
	} else if prevPong != 0 {
		lastPong = time.Unix(0, prevPong)
	}

	if prevHealthy && !healthy {
		h.logger.Warn(
			ctx,
			"toolset became unhealthy",
			"component", "tool-registry-health",
			"toolset", toolset,
			"reason", reason,
			"staleness_threshold", h.stalenessThreshold.String(),
			"ping_interval", h.pingInterval.String(),
			"missed_ping_threshold", h.missedPingThreshold,
			"last_pong", lastPong.UTC().Format(time.RFC3339Nano),
			"age_since_last_pong", now.Sub(lastPong).String(),
		)
		return
	}
}
