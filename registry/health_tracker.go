// Package registry coordinates distributed health pings for catalog admissions.
//
// The catalog record atomically owns provider leases, membership epoch, and
// last-pong time. This tracker owns only local participation in Pulse's
// distributed ticker; it never caches or duplicates authoritative health.
package registry

import (
	"context"
	"errors"
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
	// healthTicker abstracts one local distributed ticker participant.
	healthTicker interface {
		Channel() <-chan time.Time
		Close()
		Stop()
	}

	// poolHealthTicker adapts the production Pulse ticker.
	poolHealthTicker struct {
		ticker *pool.Ticker
	}

	healthTickerFactory func(ctx context.Context, name string, interval time.Duration) (healthTicker, error)

	// HealthTracker coordinates pings and derives health from the catalog.
	HealthTracker interface {
		// Health returns authoritative health for one exact admission token.
		Health(ctx context.Context, toolset, registrationToken string) (ToolsetHealth, error)
		// RecordPong authenticates one exact provider incarnation and ping epoch.
		RecordPong(ctx context.Context, toolset, providerID, incarnationID, pingID string) error
		// EnsurePingLoop ensures this node participates in distributed pings.
		EnsurePingLoop(ctx context.Context, toolset string) error
		// Close joins catalog watching and every local ping loop.
		Close() error
	}

	// ToolsetHealth reports health derived from one catalog CAS record.
	ToolsetHealth struct {
		Healthy            bool
		LastPong           time.Time
		Age                time.Duration
		ProviderCount      int
		StalenessThreshold time.Duration
	}

	// HealthTrackerOption configures health tracking.
	HealthTrackerOption func(*healthTrackerOptions)

	healthTrackerOptions struct {
		pingInterval        time.Duration
		missedPingThreshold int
		logger              telemetry.Logger
	}

	healthTracker struct {
		streamManager       StreamManager
		catalog             *toolsetCatalog
		catalogMap          catalogMap
		newTicker           healthTickerFactory
		pingInterval        time.Duration
		missedPingThreshold int
		stalenessThreshold  time.Duration
		logger              telemetry.Logger
		lifecycleCtx        context.Context
		lifecycleCancel     context.CancelFunc

		mu               sync.Mutex
		tickers          map[string]healthTicker
		cancels          map[string]context.CancelFunc
		tickerIdentities map[string]string
		nextRepair       map[string]time.Time
		closed           bool

		stateMu             sync.Mutex
		lastObservedHealthy map[string]bool
		lastObservedPong    map[string]int64

		closeOnce sync.Once
		wg        sync.WaitGroup
	}
)

const (
	// DefaultPingInterval is the default interval between health pings.
	DefaultPingInterval = 10 * time.Second
	// DefaultMissedPingThreshold is the tolerated number of missed pings.
	DefaultMissedPingThreshold = 3
)

// WithPingInterval sets the ping interval.
func WithPingInterval(duration time.Duration) HealthTrackerOption {
	return func(options *healthTrackerOptions) {
		options.pingInterval = duration
	}
}

// WithMissedPingThreshold sets the tolerated missed-ping count.
func WithMissedPingThreshold(count int) HealthTrackerOption {
	return func(options *healthTrackerOptions) {
		options.missedPingThreshold = count
	}
}

// WithHealthLogger sets the health transition logger.
func WithHealthLogger(logger telemetry.Logger) HealthTrackerOption {
	return func(options *healthTrackerOptions) {
		options.logger = logger
	}
}

// newHealthTracker creates ticker coordination over the canonical catalog.
func newHealthTracker(
	ctx context.Context,
	streamManager StreamManager,
	catalog *toolsetCatalog,
	node *pool.Node,
	opts ...HealthTrackerOption,
) (HealthTracker, error) {
	if streamManager == nil {
		return nil, fmt.Errorf("stream manager is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("catalog is required")
	}
	if node == nil {
		return nil, fmt.Errorf("pool node is required")
	}
	options := healthTrackerOptions{
		pingInterval:        DefaultPingInterval,
		missedPingThreshold: DefaultMissedPingThreshold,
		logger:              telemetry.NewNoopLogger(),
	}
	for _, option := range opts {
		option(&options)
	}
	detachedCtx := context.WithoutCancel(ctx)
	// #nosec G118 -- Close stores and invokes lifecycleCancel after stopping all tickers.
	lifecycleCtx, lifecycleCancel := context.WithCancel(detachedCtx)
	events := catalog.m.Subscribe()
	tracker := &healthTracker{
		streamManager: streamManager,
		catalog:       catalog,
		catalogMap:    catalog.m,
		newTicker: func(ctx context.Context, name string, interval time.Duration) (healthTicker, error) {
			ticker, err := node.NewTicker(ctx, name, interval)
			if err != nil {
				return nil, err
			}
			return &poolHealthTicker{ticker: ticker}, nil
		},
		pingInterval:        options.pingInterval,
		missedPingThreshold: options.missedPingThreshold,
		stalenessThreshold:  deriveStalenessThreshold(options.pingInterval, options.missedPingThreshold),
		logger:              options.logger,
		lifecycleCtx:        lifecycleCtx,
		lifecycleCancel:     lifecycleCancel,
		tickers:             make(map[string]healthTicker),
		cancels:             make(map[string]context.CancelFunc),
		tickerIdentities:    make(map[string]string),
		nextRepair:          make(map[string]time.Time),
		lastObservedHealthy: make(map[string]bool),
		lastObservedPong:    make(map[string]int64),
	}
	tracker.wg.Add(1)
	go tracker.watchCatalogChanges(events)
	tracker.syncWithCatalog()
	return tracker, nil
}

// RecordPong implements HealthTracker.
func (h *healthTracker) RecordPong(
	ctx context.Context,
	toolset, providerID, incarnationID, pingID string,
) error {
	token, epoch, ok := parsePingID(pingID)
	if !ok {
		return nil
	}
	if err := h.catalog.RecordPong(
		ctx,
		toolset,
		providerID,
		incarnationID,
		token,
		epoch,
	); err != nil {
		return fmt.Errorf("record catalog pong: %w", err)
	}
	return nil
}

// Health implements HealthTracker.
func (h *healthTracker) Health(
	ctx context.Context,
	toolset, registrationToken string,
) (ToolsetHealth, error) {
	entry, now, err := h.catalog.healthEntry(ctx, toolset)
	if err != nil {
		return ToolsetHealth{}, err
	}
	if entry.RegistrationToken != registrationToken {
		return ToolsetHealth{}, errToolsetNotFound
	}
	health := ToolsetHealth{
		ProviderCount:      len(entry.ProviderLeases),
		StalenessThreshold: h.stalenessThreshold,
	}
	if entry.LastPongUnixNano != 0 {
		health.LastPong = time.Unix(0, entry.LastPongUnixNano)
		health.Age = now.Sub(health.LastPong)
	}
	health.Healthy = health.ProviderCount > 0 &&
		!health.LastPong.IsZero() &&
		health.Age <= health.StalenessThreshold
	return health, nil
}

// EnsurePingLoop implements HealthTracker.
func (h *healthTracker) EnsurePingLoop(ctx context.Context, toolset string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ensurePingLoopLocked(ctx, toolset)
}

// ensurePingLoopLocked repairs missing or stale local participation.
func (h *healthTracker) ensurePingLoopLocked(ctx context.Context, toolset string) error {
	if h.closed {
		return fmt.Errorf("health tracker is closed")
	}
	token, epoch, err := h.catalog.HealthIdentity(ctx, toolset)
	if err != nil {
		return err
	}
	identity := pingIdentity(token, epoch)
	if _, exists := h.tickers[toolset]; !exists {
		return h.startLocalTickerLocked(toolset, identity)
	}
	if h.tickerIdentities[toolset] != identity {
		return h.restartLocalTickerLocked(toolset, identity)
	}
	health, err := h.Health(ctx, toolset, token)
	if err != nil {
		return err
	}
	now := time.Now()
	if health.Healthy {
		h.nextRepair[toolset] = now.Add(2 * h.pingInterval)
		return nil
	}
	if now.Before(h.nextRepair[toolset]) {
		return nil
	}
	if err := h.restartLocalTickerLocked(toolset, identity); err != nil {
		h.nextRepair[toolset] = now.Add(h.pingInterval)
		return err
	}
	h.nextRepair[toolset] = now.Add(2 * h.pingInterval)
	return nil
}

// restartLocalTickerLocked replaces one local ticker participant.
func (h *healthTracker) restartLocalTickerLocked(toolset, identity string) error {
	h.closeLocalTickerLocked(toolset, false)
	return h.startLocalTickerLocked(toolset, identity)
}

// startLocalTickerLocked starts one local ping loop.
func (h *healthTracker) startLocalTickerLocked(toolset, identity string) error {
	loopCtx, cancel := context.WithCancel(h.lifecycleCtx)
	ticker, err := h.newTicker(loopCtx, "registry:ping:"+toolset, h.pingInterval)
	if err != nil {
		cancel()
		return fmt.Errorf("create distributed ticker: %w", err)
	}
	h.tickers[toolset] = ticker
	h.cancels[toolset] = cancel
	h.tickerIdentities[toolset] = identity
	h.nextRepair[toolset] = time.Now().Add(2 * h.pingInterval)
	h.wg.Add(1)
	go h.runPingLoop(loopCtx, toolset, ticker)
	return nil
}

// closeLocalTickerLocked stops one local participant. Retirement intentionally
// removes shared ticker state; replacement only closes this node's handle.
func (h *healthTracker) closeLocalTickerLocked(toolset string, retire bool) {
	if cancel, exists := h.cancels[toolset]; exists {
		cancel()
		delete(h.cancels, toolset)
	}
	if ticker, exists := h.tickers[toolset]; exists {
		if retire {
			ticker.Stop()
		} else {
			ticker.Close()
		}
		delete(h.tickers, toolset)
	}
	delete(h.tickerIdentities, toolset)
	delete(h.nextRepair, toolset)
}

// watchCatalogChanges reconciles events and periodically repairs ticker state.
func (h *healthTracker) watchCatalogChanges(events <-chan rmap.EventKind) {
	defer h.wg.Done()
	defer h.catalogMap.Unsubscribe(events)
	repair := time.NewTicker(h.pingInterval)
	defer repair.Stop()
	for {
		select {
		case <-h.lifecycleCtx.Done():
			return
		case _, open := <-events:
			if !open {
				h.catalogMap.Unsubscribe(events)
				timer := time.NewTimer(h.pingInterval)
				select {
				case <-h.lifecycleCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				events = h.catalogMap.Subscribe()
				continue
			}
			h.syncWithCatalog()
		case <-repair.C:
			h.syncWithCatalog()
		}
	}
}

// syncWithCatalog reconciles active admissions without treating transient reads
// as retirement.
func (h *healthTracker) syncWithCatalog() {
	active := make(map[string]struct{})
	authoritative := true
	for _, key := range h.catalogMap.Keys() {
		toolset := toolsetFromCatalogKey(key)
		if toolset == "" {
			continue
		}
		raw, exists, err := h.catalog.exactRaw(h.lifecycleCtx, key)
		if err != nil {
			h.logger.Error(h.lifecycleCtx, "read catalog entry failed", "toolset", toolset, "err", err)
			authoritative = false
			continue
		}
		if !exists {
			continue
		}
		entry, err := parseCatalogEntry(toolset, raw)
		if err != nil {
			h.logger.Error(h.lifecycleCtx, "parse catalog entry failed", "toolset", toolset, "err", err)
			authoritative = false
			continue
		}
		if entry.State == catalogEntryActive {
			active[toolset] = struct{}{}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for toolset := range active {
		if err := h.ensurePingLoopLocked(h.lifecycleCtx, toolset); err != nil {
			h.logger.Error(h.lifecycleCtx, "ensure ticker failed", "toolset", toolset, "err", err)
		}
	}
	if authoritative {
		for toolset := range h.tickers {
			if _, exists := active[toolset]; !exists {
				h.closeLocalTickerLocked(toolset, true)
			}
		}
	}
}

// runPingLoop publishes pings when this pool node wins a distributed tick.
func (h *healthTracker) runPingLoop(ctx context.Context, toolset string, ticker healthTicker) {
	defer h.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Channel():
			h.observeHealth(ctx, toolset)
			h.sendPing(ctx, toolset)
		}
	}
}

// sendPing publishes one token-and-epoch-fenced ping.
func (h *healthTracker) sendPing(ctx context.Context, toolset string) {
	token, epoch, err := h.catalog.HealthIdentity(ctx, toolset)
	if err != nil {
		if !errors.Is(err, errToolsetNotFound) {
			h.logger.Error(ctx, "resolve ping identity failed", "toolset", toolset, "err", err)
		}
		return
	}
	pingID := newPingID(token, epoch)
	if err := h.streamManager.PublishToolCall(
		ctx,
		toolset,
		toolregistry.NewPingMessage(token, pingID),
	); err != nil {
		h.logger.Error(ctx, "publish ping failed", "toolset", toolset, "ping_id", pingID, "err", err)
	}
}

// observeHealth samples and logs meaningful health transitions.
func (h *healthTracker) observeHealth(ctx context.Context, toolset string) {
	token, _, err := h.catalog.HealthIdentity(ctx, toolset)
	if err != nil {
		return
	}
	health, err := h.Health(ctx, toolset, token)
	if err != nil {
		return
	}
	h.noteHealth(ctx, toolset, health)
}

// noteHealth logs healthy-to-unhealthy transitions without tick-level spam.
func (h *healthTracker) noteHealth(ctx context.Context, toolset string, health ToolsetHealth) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	previousHealthy, observed := h.lastObservedHealthy[toolset]
	previousPong := h.lastObservedPong[toolset]
	var pong int64
	if !health.LastPong.IsZero() {
		pong = health.LastPong.UnixNano()
	}
	h.lastObservedHealthy[toolset] = health.Healthy
	h.lastObservedPong[toolset] = pong
	if observed && previousHealthy && !health.Healthy && previousPong != pong {
		h.logger.Warn(
			ctx,
			"toolset became unhealthy",
			"toolset", toolset,
			"provider_count", health.ProviderCount,
			"last_pong", health.LastPong,
			"age", health.Age,
		)
	}
}

// Close implements HealthTracker.
func (h *healthTracker) Close() error {
	h.closeOnce.Do(func() {
		h.lifecycleCancel()
		h.mu.Lock()
		h.closed = true
		for toolset := range h.tickers {
			h.closeLocalTickerLocked(toolset, false)
		}
		h.mu.Unlock()
		h.wg.Wait()
	})
	return nil
}

// deriveStalenessThreshold defines the shared-pong freshness window.
func deriveStalenessThreshold(interval time.Duration, missed int) time.Duration {
	return time.Duration(missed+1) * interval
}

// pingIdentity binds a ticker participant to one token and membership epoch.
func pingIdentity(token string, epoch uint64) string {
	return token + "/" + strconv.FormatUint(epoch, 10)
}

// newPingID returns a ping identifier carrying token and membership epoch.
func newPingID(token string, epoch uint64) string {
	return pingIdentity(token, epoch) + "/" + uuid.NewString()
}

// parsePingID extracts the exact token and membership epoch.
func parsePingID(pingID string) (string, uint64, bool) {
	parts := strings.SplitN(pingID, "/", 3)
	if len(parts) != 3 {
		return "", 0, false
	}
	if err := toolregistry.ValidateRegistrationToken(parts[0]); err != nil {
		return "", 0, false
	}
	epoch, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || epoch == 0 {
		return "", 0, false
	}
	if _, err := uuid.Parse(parts[2]); err != nil {
		return "", 0, false
	}
	return parts[0], epoch, true
}

// toolsetFromCatalogKey extracts a toolset name from a catalog map key.
func toolsetFromCatalogKey(key string) string {
	if !strings.HasPrefix(key, toolsetCatalogKeyPrefix) {
		return ""
	}
	return strings.TrimPrefix(key, toolsetCatalogKeyPrefix)
}

// Channel returns the production ticker channel.
func (ticker *poolHealthTicker) Channel() <-chan time.Time {
	return ticker.ticker.C
}

// Close stops only local participation.
func (ticker *poolHealthTicker) Close() {
	ticker.ticker.Close()
}

// Stop removes shared participation during retirement.
func (ticker *poolHealthTicker) Stop() {
	ticker.ticker.Stop()
}
