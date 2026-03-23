// Package registry provides the internal tool registry service implementation.
//
// This file owns distributed provider liveness. Catalog membership is the
// authoritative source of which toolsets participate in health tracking, and
// shared health records are scoped to the current registration epoch so a
// same-name re-registration cannot inherit stale health from a prior provider.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// 1. A catalog map that stores registered toolsets (for cross-node coordination)
	// 2. A health map that stores registration-scoped pong records for each toolset
	//
	// All nodes subscribe to the catalog map. When a toolset is registered/unregistered,
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

		// RecordPong records a pong response for a toolset when the pong matches
		// the current catalog registration epoch.
		RecordPong(ctx context.Context, toolset string, pingID string) error

		// IsHealthy returns whether a toolset has healthy providers.
		// A toolset is healthy if a pong was received within the staleness threshold.
		IsHealthy(toolset string) bool

		// StartPingLoop ensures this node participates in health tracking for a
		// catalog-registered toolset. Cross-node participation is derived from the
		// shared catalog, not from a second membership index.
		StartPingLoop(ctx context.Context, toolset string) error

		// StopPingLoop stops local health tracking participation for an
		// unregistered toolset and clears its shared health state. Other nodes stop
		// via catalog change propagation.
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
		catalog             *toolsetCatalog
		healthMap           *rmap.Map // stores registration-scoped health records
		catalogMap          *rmap.Map // stores registered toolsets for cross-node coordination
		poolNode            *pool.Node
		pingInterval        time.Duration
		missedPingThreshold int
		stalenessThreshold  time.Duration
		logger              telemetry.Logger

		mu      sync.RWMutex
		tickers map[string]*pool.Ticker
		cancels map[string]context.CancelFunc
		// tickerStartedAt records when the current local ticker instance was
		// created so stale-health repair can distinguish "this ticker stopped
		// after we had healthy pongs" from "we have never seen a pong yet".
		tickerStartedAt map[string]time.Time

		stateMu              sync.Mutex
		lastObservedHealthy  map[string]bool
		lastObservedPongNano map[string]int64

		closeOnce sync.Once
		closeCh   chan struct{}
	}

	// healthRecord is the shared liveness state for a toolset registration.
	// RegistrationToken ties the pong to the current catalog entry so same-name
	// re-registration does not inherit stale health from a previous provider.
	healthRecord struct {
		RegistrationToken string `json:"registration_token"`
		LastPongUnixNano  int64  `json:"last_pong_unix_nano"`
	}
)

const (
	// DefaultPingInterval is the default interval between health check pings.
	DefaultPingInterval = 10 * time.Second
	// DefaultMissedPingThreshold is the default number of consecutive missed pings
	// before marking a toolset as unhealthy.
	DefaultMissedPingThreshold = 3

	healthKeyPrefix = "registry:health:"
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

// WithHealthLogger sets the logger used for health-transition and ping errors.
func WithHealthLogger(l telemetry.Logger) HealthTrackerOption {
	return func(o *healthTrackerOptions) {
		o.logger = l
	}
}

// NewHealthTracker creates a new distributed health tracker.
//
// The tracker derives toolset participation from the shared catalog map, stores
// registration-scoped health in the shared health map, and uses a Pulse pool
// ticker so only one node in the cluster publishes pings at a time.
func NewHealthTracker(streamManager StreamManager, healthMap, catalogMap *rmap.Map, node *pool.Node, opts ...HealthTrackerOption) (HealthTracker, error) {
	if streamManager == nil {
		return nil, fmt.Errorf("stream manager is required")
	}
	if healthMap == nil {
		return nil, fmt.Errorf("health map is required for distributed health tracking")
	}
	if catalogMap == nil {
		return nil, fmt.Errorf("catalog map is required for cross-node coordination")
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

	// Subscribe before spawning goroutine to avoid race: if a catalog event
	// arrives before the goroutine calls Subscribe(), the event is missed.
	catalogEvents := catalogMap.Subscribe()

	h := &healthTracker{
		streamManager:        streamManager,
		catalog:              newToolsetCatalog(catalogMap),
		healthMap:            healthMap,
		catalogMap:           catalogMap,
		poolNode:             node,
		pingInterval:         options.pingInterval,
		missedPingThreshold:  options.missedPingThreshold,
		stalenessThreshold:   stalenessThreshold,
		logger:               logger,
		tickers:              make(map[string]*pool.Ticker),
		cancels:              make(map[string]context.CancelFunc),
		tickerStartedAt:      make(map[string]time.Time),
		lastObservedHealthy:  make(map[string]bool),
		lastObservedPongNano: make(map[string]int64),
		closeCh:              make(chan struct{}),
	}

	// Start watching for catalog changes from other nodes.
	go h.watchCatalogChanges(catalogEvents)

	// Sync with existing catalog entries.
	h.syncExistingToolsets()

	return h, nil
}

// RecordPong implements HealthTracker.
func (h *healthTracker) RecordPong(ctx context.Context, toolset string, pingID string) error {
	registrationToken, err := h.registrationToken(ctx, toolset)
	if err != nil {
		if errors.Is(err, errToolsetNotFound) {
			return nil
		}
		return fmt.Errorf("resolve registration token: %w", err)
	}
	if !pingBelongsToRegistration(pingID, registrationToken) {
		return nil
	}

	key := healthKey(toolset)
	record := healthRecord{
		RegistrationToken: registrationToken,
		LastPongUnixNano:  time.Now().UnixNano(),
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal health record: %w", err)
	}
	_, err = h.healthMap.Set(ctx, key, string(payload))
	if err != nil {
		return fmt.Errorf("record pong: %w", err)
	}
	return nil
}

// Health implements HealthTracker.
func (h *healthTracker) Health(toolset string) (ToolsetHealth, error) {
	registrationToken, err := h.registrationToken(context.Background(), toolset)
	if err != nil {
		if errors.Is(err, errToolsetNotFound) {
			return ToolsetHealth{
				Healthy:            false,
				StalenessThreshold: h.stalenessThreshold,
			}, nil
		}
		return ToolsetHealth{}, fmt.Errorf("resolve registration token: %w", err)
	}

	key := healthKey(toolset)
	val, ok := h.healthMap.Get(key)
	if !ok {
		return ToolsetHealth{
			Healthy:            false,
			StalenessThreshold: h.stalenessThreshold,
		}, nil
	}
	record, err := parseHealthRecord(val)
	if err != nil {
		return ToolsetHealth{}, fmt.Errorf("parse last pong timestamp for %q: %w", toolset, err)
	}
	if record.RegistrationToken != registrationToken {
		return ToolsetHealth{
			Healthy:            false,
			StalenessThreshold: h.stalenessThreshold,
		}, nil
	}
	lastPong := time.Unix(0, record.LastPongUnixNano)
	age := time.Since(lastPong)
	healthy := age <= h.stalenessThreshold
	return ToolsetHealth{
		Healthy:            healthy,
		LastPong:           lastPong,
		Age:                age,
		StalenessThreshold: h.stalenessThreshold,
	}, nil
}

// IsHealthy implements HealthTracker.
func (h *healthTracker) IsHealthy(toolset string) bool {
	hh, err := h.Health(toolset)
	if err != nil {
		return false
	}
	return hh.Healthy
}

// StartPingLoop implements HealthTracker.
func (h *healthTracker) StartPingLoop(ctx context.Context, toolset string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.restartLocalTickerLocked(toolset)
}

// restartLocalTickerLocked replaces the current local ticker participant for a
// toolset without deleting the shared ticker-map entry. The caller must hold
// h.mu.
func (h *healthTracker) restartLocalTickerLocked(toolset string) error {
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

// StopPingLoop implements HealthTracker.
func (h *healthTracker) StopPingLoop(ctx context.Context, toolset string) {
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

// Close implements HealthTracker.
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
		h.tickerStartedAt = make(map[string]time.Time)
	})
	return nil
}

// watchCatalogChanges reacts to catalog map changes from other nodes and
// periodically repairs stale local tickers. The events channel must be obtained
// via catalogMap.Subscribe() before calling this method to avoid missing events
// that arrive between tracker construction and goroutine startup.
func (h *healthTracker) watchCatalogChanges(events <-chan rmap.EventKind) {
	defer h.catalogMap.Unsubscribe(events)
	repairTicker := time.NewTicker(h.pingInterval)
	defer repairTicker.Stop()

	for {
		select {
		case <-h.closeCh:
			return
		case <-events:
			h.syncWithCatalog()
		case <-repairTicker.C:
			h.syncWithCatalog()
		}
	}
}

// syncExistingToolsets syncs with toolsets that were registered before this node started.
func (h *healthTracker) syncExistingToolsets() {
	h.syncWithCatalog()
}

// syncWithCatalog ensures local tickers match the catalog state.
func (h *healthTracker) syncWithCatalog() {
	// Get all registered toolsets from the catalog map.
	registered := make(map[string]bool)
	for _, key := range h.catalogMap.Keys() {
		toolset := toolsetFromCatalogKey(key)
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
			continue
		}
		health, shouldRestart, startedAt, err := h.shouldRestartStaleTickerLocked(toolset)
		if err != nil {
			h.logger.Error(context.Background(), "read ticker health failed", "event", "read_ticker_health_failed", "toolset", toolset, "err", err)
			continue
		}
		if shouldRestart {
			h.logger.Warn(
				context.Background(),
				"repairing stale local ticker",
				"event", "repair_stale_ticker",
				"component", "tool-registry-health",
				"toolset", toolset,
				"started_at", startedAt.UTC().Format(time.RFC3339Nano),
				"last_pong", health.LastPong.UTC().Format(time.RFC3339Nano),
				"age_since_last_pong", health.Age.String(),
				"staleness_threshold", health.StalenessThreshold.String(),
			)
			if err := h.restartLocalTickerLocked(toolset); err != nil {
				h.logger.Error(context.Background(), "restart ticker failed", "event", "restart_ticker_failed", "toolset", toolset, "err", err)
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

// startLocalTickerLocked creates this node's distributed ticker participant and
// launches the long-lived ping loop for the toolset.
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
	h.tickerStartedAt[toolset] = time.Now()
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
	delete(h.tickerStartedAt, toolset)
}

// healthKey returns the shared health-map key for a toolset.
func healthKey(toolset string) string {
	return healthKeyPrefix + toolset
}

// toolsetFromCatalogKey extracts the toolset name from a catalog key.
func toolsetFromCatalogKey(key string) string {
	if !strings.HasPrefix(key, toolsetCatalogKeyPrefix) {
		return ""
	}
	return strings.TrimPrefix(key, toolsetCatalogKeyPrefix)
}

// registrationToken resolves the current catalog-backed liveness epoch for a
// toolset. The catalog owns this opaque token so re-registration rotates epoch
// identity even when the human-readable registration timestamp collides.
func (h *healthTracker) registrationToken(ctx context.Context, toolset string) (string, error) {
	return h.catalog.RegistrationToken(ctx, toolset)
}

// shouldRestartStaleTickerLocked reports whether the current local ticker
// instance predates the last healthy pong and the toolset has since gone stale.
// That combination means the cluster previously had working heartbeats, but the
// current ticker generation has stopped making forward progress and should be
// recreated. The caller must hold h.mu.
func (h *healthTracker) shouldRestartStaleTickerLocked(toolset string) (ToolsetHealth, bool, time.Time, error) {
	startedAt, ok := h.tickerStartedAt[toolset]
	if !ok {
		return ToolsetHealth{}, false, time.Time{}, nil
	}
	health, err := h.Health(toolset)
	if err != nil {
		return ToolsetHealth{}, false, time.Time{}, err
	}
	if health.Healthy || health.LastPong.IsZero() {
		return health, false, startedAt, nil
	}
	return health, health.LastPong.After(startedAt), startedAt, nil
}

// runPingLoop emits periodic pings for the distributed ticker winner and logs
// state transitions before each ping publish.
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

// sendPing publishes one health ping bound to the current registration epoch.
func (h *healthTracker) sendPing(ctx context.Context, toolset string) {
	registrationToken, err := h.registrationToken(ctx, toolset)
	if err != nil {
		if errors.Is(err, errToolsetNotFound) {
			return
		}
		h.logger.Error(
			context.Background(),
			"resolve ping registration token failed",
			"event", "resolve_ping_registration_token_failed",
			"component", "tool-registry-health",
			"toolset", toolset,
			"err", err,
		)
		return
	}

	pingID := newPingID(registrationToken)
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

// observeHealth samples the current derived health state and forwards it to the
// transition logger.
func (h *healthTracker) observeHealth(ctx context.Context, toolset string) {
	health, err := h.Health(toolset)
	if err != nil {
		h.logger.Error(ctx, "read toolset health failed", "component", "tool-registry-health", "toolset", toolset, "err", err)
		h.noteHealth(ctx, toolset, false, 0, "missing_health_entry")
		return
	}
	if health.LastPong.IsZero() {
		h.noteHealth(ctx, toolset, false, 0, "missing_health_entry")
		return
	}
	h.noteHealth(ctx, toolset, health.Healthy, health.LastPong.UnixNano(), "ok")
}

// parseHealthRecord decodes the shared health-map payload.
func parseHealthRecord(raw string) (healthRecord, error) {
	var record healthRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return healthRecord{}, err
	}
	return record, nil
}

// newPingID returns a ping identifier that carries the active registration
// token so pong handling can reject stale registrations.
func newPingID(registrationToken string) string {
	return registrationToken + "/" + uuid.New().String()
}

// pingBelongsToRegistration reports whether the ponged ping ID belongs to the
// current registration epoch.
func pingBelongsToRegistration(pingID string, registrationToken string) bool {
	return strings.HasPrefix(pingID, registrationToken+"/")
}

// noteHealth logs health transitions while suppressing duplicate observations
// that would otherwise spam the registry logs on every ping tick.
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
