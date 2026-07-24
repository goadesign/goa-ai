// Package registry provides the internal tool registry service implementation.
//
// This file owns distributed provider liveness. Catalog membership is the
// authoritative source of which toolsets participate in health tracking, and
// shared health records are scoped to provider instances plus the current
// registration epoch so rollout overlap can keep serving an unchanged schema.
//
// Ping scheduling is deliberately stateless in Redis: every registry node runs
// one local ticker and competes for a short-lived per-toolset lease
// (SET NX PX) before publishing a ping. The lease expires on its own, so after
// any Redis state loss the next tick simply re-acquires it and pings resume.
// This replaced Pulse distributed tickers, which kept replicated ticker state
// that could not be rebuilt after Redis lost it, leaving toolsets permanently
// unhealthy.
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
	"github.com/redis/go-redis/v9"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
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
	// Every node derives ping participation directly from the catalog map: a
	// local ticker enumerates registered toolsets and competes for a per-toolset
	// Redis lease so only one node publishes each ping. Because the lease is an
	// expiring plain Redis key, ping scheduling self-heals after Redis state
	// loss without any node restart.
	HealthTracker interface {
		// Health returns the current health state for a toolset.
		//
		// Contract:
		//   - Health is derived from the last recorded Pong timestamp and the
		//     configured staleness threshold.
		//   - If the toolset has never ponged (or no entry exists), Health reports
		//     Healthy=false with LastPong unset.
		Health(toolset string) (ToolsetHealth, error)

		// RecordPong records a pong response for a provider instance when the pong
		// matches the current catalog registration epoch.
		RecordPong(ctx context.Context, toolset, providerID, pingID string) error

		// RegisterProvider records provider-instance membership for the active
		// catalog registration without marking the provider healthy.
		RegisterProvider(ctx context.Context, toolset, providerID string) error

		// IsHealthy returns whether a toolset has healthy providers.
		// A toolset is healthy if a pong was received within the staleness threshold.
		IsHealthy(toolset string) bool

		// StartPingLoop ensures health tracking participation for a
		// catalog-registered toolset. It is ensure-only and idempotent:
		// participation is derived from the shared catalog, so repeated calls
		// (provider renewals, re-registrations) never restart or duplicate ping
		// scheduling.
		StartPingLoop(ctx context.Context, toolset string) error

		// StopPingLoop stops health tracking for an unregistered toolset and
		// clears its shared health state. Ping scheduling stops on every node as
		// soon as the catalog entry is deleted.
		StopPingLoop(ctx context.Context, toolset string)

		// Close stops the ping scheduler and releases resources.
		Close() error
	}

	// ToolsetHealth reports derived provider health for a toolset.
	ToolsetHealth struct {
		// Healthy reports whether a provider pong was received within the configured threshold.
		Healthy bool
		// ProviderID is the freshest provider instance for the active registration token.
		ProviderID string
		// LastPong is the timestamp of the last recorded pong when available.
		LastPong time.Time
		// RegisteredAt is the timestamp of the freshest active provider record.
		RegisteredAt time.Time
		// Age is the duration since LastPong when available.
		Age time.Duration
		// ProviderCount is the number of provider records for the active registration token.
		ProviderCount int
		// HealthyProviderCount is the number of active-token provider records that are fresh.
		HealthyProviderCount int
		// StalenessThreshold is the configured maximum acceptable pong age.
		StalenessThreshold time.Duration
	}

	// HealthTrackerOption configures optional settings for the health tracker.
	HealthTrackerOption func(*healthTrackerOptions)

	healthTrackerOptions struct {
		pingInterval        time.Duration
		missedPingThreshold int
		leaseScope          string
		logger              telemetry.Logger
	}

	healthTracker struct {
		streamManager       StreamManager
		catalog             *toolsetCatalog
		healthMap           *rmap.Map     // stores provider-instance health records
		catalogMap          *rmap.Map     // stores registered toolsets for cross-node coordination
		redis               *redis.Client // owns the per-toolset ping leases
		nodeID              string        // lease value identifying this node for diagnostics
		leaseScope          string        // prefix isolating leases of distinct registry clusters
		pingInterval        time.Duration
		missedPingThreshold int
		stalenessThreshold  time.Duration
		logger              telemetry.Logger

		stateMu              sync.Mutex
		lastObservedHealthy  map[string]bool
		lastObservedPongNano map[string]int64

		// revFloors remembers, per map hash key, the revision floor this node
		// last established in Redis. Pulse rmap replicas apply an update only
		// when its revision exceeds their local revision, so after Redis state
		// loss resets a map's "=rev" counter every surviving replica silently
		// drops all subsequent updates. The scheduler pins the counter to the
		// wall clock (see ensureMapRevision); a counter observed below the
		// floor means Redis lost the map and triggers a repair.
		revFloors map[string]int64

		closeOnce sync.Once
		closeCh   chan struct{}
		doneCh    chan struct{}
	}

	// healthRecord is the shared liveness state for one provider instance.
	// RegistrationToken ties the pong to the current catalog entry so changed
	// schemas do not inherit stale health from previous providers.
	healthRecord struct {
		ProviderID         string `json:"provider_id"`
		RegistrationToken  string `json:"registration_token"`
		RegisteredUnixNano int64  `json:"registered_unix_nano"`
		LastPongUnixNano   int64  `json:"last_pong_unix_nano"`
	}
)

const (
	// DefaultPingInterval is the default interval between health check pings.
	DefaultPingInterval = 10 * time.Second
	// DefaultMissedPingThreshold is the default number of consecutive missed pings
	// before marking a toolset as unhealthy.
	DefaultMissedPingThreshold = 3

	healthKeyPrefix = "registry:health:"

	// revFloorSlack guards revision repair against a wall clock that stepped
	// backward between two repairs: the repaired counter is never below the
	// previous floor plus this slack, which covers every write that can
	// realistically commit between the two repairs. Revisions only need to be
	// monotonic, so overshooting is free.
	revFloorSlack = 1 << 20
)

// revisionPinScript atomically raises a replicated map's "=rev" counter to
// the target when the counter is lower, returning {raised, final counter}.
// The compare and the write execute in one Redis script so concurrent repairs
// from several registry replicas converge on the highest target instead of
// summing increments, which would push the counter above the wall clock and
// silently break clock domination at the next Redis state loss.
var revisionPinScript = redis.NewScript(`
local rev = tonumber(redis.call("HGET", KEYS[1], "=rev")) or 0
local target = tonumber(ARGV[1])
if rev < target then
	redis.call("HSET", KEYS[1], "=rev", target)
	return {1, target}
end
return {0, rev}
`)

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

// WithPingLeaseScope sets the Redis key prefix used for per-toolset ping
// leases. Registry clusters sharing one Redis database must use distinct
// scopes (registry.New passes the registry name). Defaults to "registry".
func WithPingLeaseScope(scope string) HealthTrackerOption {
	return func(o *healthTrackerOptions) {
		o.leaseScope = scope
	}
}

// NewHealthTracker creates a new distributed health tracker.
//
// The tracker derives toolset participation from the shared catalog map,
// stores registration-scoped health in the shared health map, and elects a
// single pinging node per toolset through a short-lived Redis lease keyed by
// the lease scope. The Redis client owns the leases and must be the same
// instance backing the replicated maps.
func NewHealthTracker(streamManager StreamManager, healthMap, catalogMap *rmap.Map, rdb *redis.Client, opts ...HealthTrackerOption) (HealthTracker, error) {
	if streamManager == nil {
		return nil, fmt.Errorf("stream manager is required")
	}
	if healthMap == nil {
		return nil, fmt.Errorf("health map is required for distributed health tracking")
	}
	if catalogMap == nil {
		return nil, fmt.Errorf("catalog map is required for cross-node coordination")
	}
	if rdb == nil {
		return nil, fmt.Errorf("redis client is required for ping leases")
	}

	options := &healthTrackerOptions{
		pingInterval:        DefaultPingInterval,
		missedPingThreshold: DefaultMissedPingThreshold,
		leaseScope:          "registry",
		logger:              telemetry.NewNoopLogger(),
	}
	for _, opt := range opts {
		opt(options)
	}
	logger := options.logger
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}

	h := &healthTracker{
		streamManager:        streamManager,
		catalog:              newToolsetCatalog(catalogMap),
		healthMap:            healthMap,
		catalogMap:           catalogMap,
		redis:                rdb,
		nodeID:               uuid.NewString(),
		leaseScope:           options.leaseScope,
		pingInterval:         options.pingInterval,
		missedPingThreshold:  options.missedPingThreshold,
		stalenessThreshold:   deriveStalenessThreshold(options.pingInterval, options.missedPingThreshold),
		logger:               logger,
		lastObservedHealthy:  make(map[string]bool),
		lastObservedPongNano: make(map[string]int64),
		revFloors:            make(map[string]int64),
		closeCh:              make(chan struct{}),
		doneCh:               make(chan struct{}),
	}

	go h.run()

	return h, nil
}

// RecordPong implements HealthTracker.
func (h *healthTracker) RecordPong(ctx context.Context, toolset, providerID, pingID string) error {
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

	key := healthKey(toolset, providerID)
	registeredUnixNano := time.Now().UnixNano()
	if raw, ok := h.healthMap.Get(key); ok {
		record, err := parseHealthRecord(raw)
		if err != nil {
			return fmt.Errorf("parse provider health record for %q: %w", toolset, err)
		}
		if record.RegistrationToken == registrationToken {
			registeredUnixNano = record.RegisteredUnixNano
		}
	}
	record := healthRecord{
		ProviderID:         providerID,
		RegistrationToken:  registrationToken,
		RegisteredUnixNano: registeredUnixNano,
		LastPongUnixNano:   time.Now().UnixNano(),
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

// RegisterProvider implements HealthTracker.
func (h *healthTracker) RegisterProvider(ctx context.Context, toolset, providerID string) error {
	registrationToken, err := h.registrationToken(ctx, toolset)
	if err != nil {
		if errors.Is(err, errToolsetNotFound) {
			return nil
		}
		return fmt.Errorf("resolve registration token: %w", err)
	}
	key := healthKey(toolset, providerID)
	registeredUnixNano := time.Now().UnixNano()
	lastPongUnixNano := int64(0)
	if raw, ok := h.healthMap.Get(key); ok {
		record, err := parseHealthRecord(raw)
		if err != nil {
			return fmt.Errorf("parse provider health record for %q: %w", toolset, err)
		}
		if record.RegistrationToken == registrationToken {
			registeredUnixNano = record.RegisteredUnixNano
			lastPongUnixNano = record.LastPongUnixNano
		}
	}
	record := healthRecord{
		ProviderID:         providerID,
		RegistrationToken:  registrationToken,
		RegisteredUnixNano: registeredUnixNano,
		LastPongUnixNano:   lastPongUnixNano,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal provider health record: %w", err)
	}
	if _, err := h.healthMap.Set(ctx, key, string(payload)); err != nil {
		return fmt.Errorf("register provider: %w", err)
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

	prefix := healthKeyPrefixForToolset(toolset)
	records := make([]healthRecord, 0, 4)
	for _, key := range h.healthMap.Keys() {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		raw, ok := h.healthMap.Get(key)
		if !ok {
			continue
		}
		record, err := parseHealthRecord(raw)
		if err != nil {
			return ToolsetHealth{}, fmt.Errorf("parse provider health record for %q: %w", toolset, err)
		}
		records = append(records, record)
	}
	return computeToolsetHealth(records, registrationToken, time.Now(), h.stalenessThreshold), nil
}

// IsHealthy implements HealthTracker.
func (h *healthTracker) IsHealthy(toolset string) bool {
	hh, err := h.Health(toolset)
	if err != nil {
		return false
	}
	return hh.Healthy
}

// StartPingLoop implements HealthTracker. Participation is derived from the
// shared catalog: the scheduler already covers every catalog-registered
// toolset, so this is a pure idempotent ensure that re-registration and
// renewal flows can call any number of times without restarting or
// duplicating ping scheduling.
func (h *healthTracker) StartPingLoop(context.Context, string) error {
	return nil
}

// StopPingLoop implements HealthTracker.
func (h *healthTracker) StopPingLoop(ctx context.Context, toolset string) {
	// Clean up health state. Ping scheduling stops on all nodes automatically
	// once the catalog entry is gone.
	if err := h.deleteHealthRecords(ctx, toolset); err != nil {
		h.logger.Error(ctx, "delete toolset health failed", "component", "tool-registry-health", "toolset", toolset, "err", err)
	}

	h.stateMu.Lock()
	delete(h.lastObservedHealthy, toolset)
	delete(h.lastObservedPongNano, toolset)
	h.stateMu.Unlock()
}

// Close implements HealthTracker.
func (h *healthTracker) Close() error {
	h.closeOnce.Do(func() {
		close(h.closeCh)
		<-h.doneCh
	})
	return nil
}

// run is the single ping scheduler goroutine. It samples the catalog at a
// fraction of the ping interval so newly registered toolsets are picked up
// promptly and lease expirations are re-contended with little slack.
func (h *healthTracker) run() {
	defer close(h.doneCh)
	ticker := time.NewTicker(h.schedulerTickPeriod())
	defer ticker.Stop()

	for {
		select {
		case <-h.closeCh:
			return
		case <-ticker.C:
			h.repairMapRevisions(context.Background())
			h.pingRegisteredToolsets()
		}
	}
}

// repairMapRevisions guards the catalog and health replicated maps against
// Redis state loss. Pulse rmap replicas apply an update only when its revision
// exceeds their local revision, and a flushed hash restarts "=rev" at zero, so
// without repair every replica that outlived the loss would silently drop all
// subsequent catalog and health writes. The scheduler keeps each map's Redis
// counter pinned above the wall clock, which strictly dominates every
// replica's local revision; replicated content then converges as periodic
// writers (provider pongs, provider re-registration) rewrite their keys.
func (h *healthTracker) repairMapRevisions(ctx context.Context) {
	for _, m := range []*rmap.Map{h.catalogMap, h.healthMap} {
		if err := h.ensureMapRevision(ctx, m); err != nil {
			h.logger.Error(
				ctx,
				"repair replicated map revision failed",
				"event", "repair_map_revision_failed",
				"component", "tool-registry-health",
				"map", m.Name,
				"err", err,
			)
		}
	}
}

// ensureMapRevision pins one replicated map's Redis revision counter above the
// wall clock in milliseconds so replica-local revisions can never outrank it.
// Revisions advance at most one per committed write while the clock advances
// around a millisecond per write or faster, so a counter seeded from
// time.Now().UnixMilli() strictly dominates every replica's local revision —
// including revisions committed between two scheduler ticks, which no
// sampling scheme can observe. On the first pass the counter is silently
// raised to the current clock (genesis); afterwards a counter below the
// established floor proves Redis lost the map, and the repair re-pins it so
// post-loss writes propagate to all replicas again.
//
// The pin is a single compare-and-set script: it raises the counter to the
// target only when the counter is lower. Concurrent repairs from several
// registry replicas therefore converge on the highest target instead of
// summing increments, which would push the counter above the wall clock and
// break clock domination at the next state loss.
//
// Two contracts pin this to the goa.design/pulse version in go.mod: the hash
// key and "=rev" field name (rmap does not expose its revision counter), and
// the millisecond resolution — rmap's Lua scripts format revisions with Lua's
// %.14g tostring, so counters must stay far below 1e14, which rules out
// micro- or nanosecond clocks.
func (h *healthTracker) ensureMapRevision(ctx context.Context, m *rmap.Map) error {
	hashKey := "map:" + m.Name + ":content"
	rev, err := h.redis.HGet(ctx, hashKey, "=rev").Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("read revision of %q: %w", hashKey, err)
	}
	floor := h.revFloors[hashKey]
	if floor > 0 && rev >= floor {
		return nil
	}
	target := max(time.Now().UnixMilli(), floor+revFloorSlack)
	res, err := revisionPinScript.Run(ctx, h.redis, []string{hashKey}, target).Int64Slice()
	if err != nil {
		return fmt.Errorf("pin revision of %q: %w", hashKey, err)
	}
	if len(res) != 2 {
		return fmt.Errorf("pin revision of %q: unexpected script reply %v", hashKey, res)
	}
	raised, final := res[0] == 1, res[1]
	h.revFloors[hashKey] = final
	if !raised || floor == 0 {
		return nil
	}
	h.logger.Warn(
		ctx,
		"repaired replicated map revision after Redis state loss",
		"event", "repaired_map_revision",
		"component", "tool-registry-health",
		"map", m.Name,
		"redis_revision", rev,
		"restored_revision", final,
	)
	return nil
}

// pingRegisteredToolsets enumerates catalog-registered toolsets and, for each
// lease this node wins, observes health and publishes one ping. Losing the
// lease means another node (or a previous tick) already owns the current ping
// interval.
func (h *healthTracker) pingRegisteredToolsets() {
	ctx := context.Background()
	for _, key := range h.catalogMap.Keys() {
		toolset := toolsetFromCatalogKey(key)
		if toolset == "" {
			continue
		}
		acquired, err := h.acquirePingLease(ctx, toolset)
		if err != nil {
			h.logger.Error(
				ctx,
				"acquire ping lease failed",
				"event", "acquire_ping_lease_failed",
				"component", "tool-registry-health",
				"toolset", toolset,
				"err", err,
			)
			continue
		}
		if !acquired {
			continue
		}
		h.observeHealth(ctx, toolset)
		h.sendPing(ctx, toolset)
	}
}

// acquirePingLease attempts to win the current ping interval for a toolset.
// The lease is a plain Redis key with the ping interval as TTL: exactly one
// node acquires it per interval, and after Redis state loss the next attempt
// recreates it, which is what makes ping scheduling self-healing.
func (h *healthTracker) acquirePingLease(ctx context.Context, toolset string) (bool, error) {
	return h.redis.SetNX(ctx, h.pingLeaseKey(toolset), h.nodeID, h.pingInterval).Result()
}

// pingLeaseKey returns the Redis key electing the pinging node for a toolset.
func (h *healthTracker) pingLeaseKey(toolset string) string {
	return h.leaseScope + ":ping-lease:" + toolset
}

// schedulerTickPeriod returns how often the scheduler samples the catalog and
// contends for expired leases. A quarter of the ping interval keeps the ping
// cadence within [pingInterval, pingInterval*5/4) without meaningful Redis
// load.
func (h *healthTracker) schedulerTickPeriod() time.Duration {
	return max(h.pingInterval/4, time.Millisecond)
}

// pruneStaleProviderRecords removes provider records whose newest timestamp is
// beyond the retention window. Health reads stay pure; ping-lease observation
// owns bounded cleanup as operational maintenance. The delete is conditional
// on the exact stale value read from the local replica: a provider pong or
// re-registration may rewrite the key between the read and the delete, and a
// blind delete would silently discard that fresh liveness record (the first
// pong after Redis state loss would be lost this way).
func (h *healthTracker) pruneStaleProviderRecords(ctx context.Context, toolset string) error {
	retention := 2 * h.stalenessThreshold
	now := time.Now()
	prefix := healthKeyPrefixForToolset(toolset)
	for _, key := range h.healthMap.Keys() {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		raw, ok := h.healthMap.Get(key)
		if !ok {
			continue
		}
		record, err := parseHealthRecord(raw)
		if err != nil {
			return fmt.Errorf("parse provider health record %q: %w", key, err)
		}
		newest := time.Unix(0, record.RegisteredUnixNano)
		if record.LastPongUnixNano != 0 {
			newest = time.Unix(0, record.LastPongUnixNano)
		}
		if now.Sub(newest) <= retention {
			continue
		}
		if _, err := h.healthMap.TestAndDelete(ctx, key, raw); err != nil {
			return fmt.Errorf("delete stale provider health record %q: %w", key, err)
		}
	}
	return nil
}

// deleteHealthRecords removes every provider-instance health record for a
// toolset after the catalog entry is unregistered.
func (h *healthTracker) deleteHealthRecords(ctx context.Context, toolset string) error {
	legacyKey := legacyHealthKey(toolset)
	if _, err := h.healthMap.Delete(ctx, legacyKey); err != nil {
		return fmt.Errorf("delete legacy health record %q: %w", legacyKey, err)
	}
	prefix := healthKeyPrefixForToolset(toolset)
	for _, key := range h.healthMap.Keys() {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if _, err := h.healthMap.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete provider health record %q: %w", key, err)
		}
	}
	return nil
}

// legacyHealthKey returns the pre-provider-instance health key shape.
// TODO(registry-migration): remove this cleanup after deployed registries have
// aged out all records written before provider-instance health.
func legacyHealthKey(toolset string) string {
	return healthKeyPrefix + toolset
}

// healthKey returns the shared health-map key for one provider instance.
func healthKey(toolset, providerID string) string {
	return healthKeyPrefixForToolset(toolset) + providerID
}

// healthKeyPrefixForToolset returns the key prefix for all provider instances
// serving one toolset.
func healthKeyPrefixForToolset(toolset string) string {
	return healthKeyPrefix + toolset + ":"
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
	if err := h.pruneStaleProviderRecords(ctx, toolset); err != nil {
		h.logger.Error(ctx, "prune provider health records failed", "component", "tool-registry-health", "toolset", toolset, "err", err)
	}
	health, err := h.Health(toolset)
	if err != nil {
		h.logger.Error(ctx, "read toolset health failed", "component", "tool-registry-health", "toolset", toolset, "err", err)
		h.noteHealth(ctx, toolset, ToolsetHealth{}, "missing_health_entry")
		return
	}
	if health.LastPong.IsZero() {
		h.noteHealth(ctx, toolset, health, "missing_health_entry")
		return
	}
	h.noteHealth(ctx, toolset, health, "ok")
}

// deriveStalenessThreshold is the contract between ping configuration and the
// staleness rule computeToolsetHealth applies: a provider may miss
// missedPingThreshold pings and still answer the next one, so the freshness
// window is (missedPingThreshold + 1) * pingInterval. Unit tests pin this
// derivation so a config change cannot silently shrink the window providers
// have to respond.
func deriveStalenessThreshold(pingInterval time.Duration, missedPingThreshold int) time.Duration {
	return time.Duration(missedPingThreshold+1) * pingInterval
}

// computeToolsetHealth derives a toolset's health from its provider health
// records. It is the single owner of the staleness rule: a provider is healthy
// when its last pong is no older than stalenessThreshold at now, and a toolset
// is healthy when at least one provider is. Records from other registration
// epochs (token mismatch) are ignored so a re-registered toolset never
// inherits health from a previous provider. ProviderID/LastPong/Age report the
// provider with the freshest pong; when no provider has ponged yet,
// ProviderID falls back to the newest registration. RegisteredAt always
// reports the newest registration. Winners are selected explicitly so the
// result is independent of record order (callers iterate a replicated map).
// Pure function: Health gathers records from the replicated health map and
// delegates here, and unit tests exercise this directly without
// infrastructure.
func computeToolsetHealth(records []healthRecord, registrationToken string, now time.Time, stalenessThreshold time.Duration) ToolsetHealth {
	health := ToolsetHealth{
		StalenessThreshold: stalenessThreshold,
	}
	var freshestPong, newestRegistered *healthRecord
	for i := range records {
		record := &records[i]
		if record.RegistrationToken != registrationToken {
			continue
		}
		health.ProviderCount++
		if newestRegistered == nil || record.RegisteredUnixNano > newestRegistered.RegisteredUnixNano {
			newestRegistered = record
		}
		if record.LastPongUnixNano == 0 {
			continue
		}
		if now.Sub(time.Unix(0, record.LastPongUnixNano)) <= stalenessThreshold {
			health.HealthyProviderCount++
		}
		if freshestPong == nil || record.LastPongUnixNano > freshestPong.LastPongUnixNano {
			freshestPong = record
		}
	}
	if newestRegistered != nil {
		health.ProviderID = newestRegistered.ProviderID
		health.RegisteredAt = time.Unix(0, newestRegistered.RegisteredUnixNano)
	}
	if freshestPong != nil {
		health.ProviderID = freshestPong.ProviderID
		health.LastPong = time.Unix(0, freshestPong.LastPongUnixNano)
		health.Age = now.Sub(health.LastPong)
	}
	health.Healthy = health.HealthyProviderCount > 0
	return health
}

// parseHealthRecord decodes the shared health-map payload.
func parseHealthRecord(raw string) (healthRecord, error) {
	var record healthRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return healthRecord{}, err
	}
	if record.ProviderID == "" {
		return healthRecord{}, fmt.Errorf("missing provider id")
	}
	if record.RegistrationToken == "" {
		return healthRecord{}, fmt.Errorf("missing registration token")
	}
	if record.RegisteredUnixNano == 0 {
		return healthRecord{}, fmt.Errorf("missing provider registration timestamp")
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
func (h *healthTracker) noteHealth(ctx context.Context, toolset string, health ToolsetHealth, reason string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	prevHealthy, hasPrev := h.lastObservedHealthy[toolset]
	prevPong := h.lastObservedPongNano[toolset]
	lastPongNano := int64(0)
	if !health.LastPong.IsZero() {
		lastPongNano = health.LastPong.UnixNano()
	}

	h.lastObservedHealthy[toolset] = health.Healthy
	if lastPongNano != 0 {
		h.lastObservedPongNano[toolset] = lastPongNano
	}

	if !hasPrev {
		return
	}
	if prevHealthy == health.Healthy && prevPong == lastPongNano {
		return
	}

	now := time.Now()
	var lastPong time.Time
	if lastPongNano != 0 {
		lastPong = time.Unix(0, lastPongNano)
	} else if prevPong != 0 {
		lastPong = time.Unix(0, prevPong)
	}

	if prevHealthy && !health.Healthy {
		h.logger.Warn(
			ctx,
			"toolset became unhealthy",
			"component", "tool-registry-health",
			"toolset", toolset,
			"provider_id", health.ProviderID,
			"provider_count", health.ProviderCount,
			"healthy_provider_count", health.HealthyProviderCount,
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
