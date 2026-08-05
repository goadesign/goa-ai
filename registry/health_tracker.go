// Package registry coordinates distributed health pings for catalog admissions.
//
// The catalog record atomically owns provider leases, membership epoch, and
// last-pong time; this tracker never caches or duplicates authoritative
// health. Ping scheduling is deliberately stateless in Redis: every registry
// node runs one local scheduler and competes for a short-lived per-toolset
// lease (SET NX PX), so exactly one node pings per interval and the next tick
// simply re-acquires a lease Redis lost. This replaced Pulse distributed
// tickers, whose replicated state could not be rebuilt after Redis lost it.
// The scheduler also pins the catalog map's replicated revision counter above
// the wall clock so surviving rmap replicas keep applying writes after Redis
// state loss.
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
	"github.com/redis/go-redis/v9"

	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	// HealthTracker coordinates pings and derives health from the catalog.
	HealthTracker interface {
		// Health returns authoritative health for one exact admission token.
		Health(ctx context.Context, toolset, registrationToken string) (ToolsetHealth, error)
		// RecordPong authenticates one exact provider incarnation and ping epoch.
		RecordPong(ctx context.Context, toolset, providerID, incarnationID, pingID string) error
		// EnsurePingLoop validates that catalog-driven scheduling covers the
		// toolset. Participation derives from the catalog, so the call is
		// ensure-only and idempotent: renewals and re-registrations never
		// restart or duplicate ping scheduling.
		EnsurePingLoop(ctx context.Context, toolset string) error
		// Close joins the scheduler goroutine.
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
		redis               *redis.Client
		nodeID              string
		leaseScope          string
		revisionHashKey     string
		pingInterval        time.Duration
		missedPingThreshold int
		stalenessThreshold  time.Duration
		logger              telemetry.Logger

		// revFloors remembers the highest revision this node pinned or
		// observed per replicated-map hash key; a Redis counter below the
		// floor proves state loss.
		revFloors map[string]int64

		stateMu             sync.Mutex
		lastObservedHealthy map[string]bool
		lastObservedPong    map[string]int64

		closeCh   chan struct{}
		doneCh    chan struct{}
		closeOnce sync.Once
	}
)

const (
	// DefaultPingInterval is the default interval between health pings.
	DefaultPingInterval = 10 * time.Second
	// DefaultMissedPingThreshold is the tolerated number of missed pings.
	DefaultMissedPingThreshold = 3

	// revFloorSlack guards revision repair against a wall clock that stepped
	// backwards between two pins: a repair target is never below the last
	// established floor plus this slack.
	revFloorSlack = 1 << 20
)

// revisionPinScript atomically raises a replicated map's "=rev" counter to
// the target when the counter is lower, so concurrent repairs from several
// registry replicas converge on the highest target instead of summing
// increments.
var revisionPinScript = redis.NewScript(`
local rev = tonumber(redis.call("HGET", KEYS[1], "=rev") or "0")
local target = tonumber(ARGV[1])
if rev < target then
    redis.call("HSET", KEYS[1], "=rev", target)
    return {1, target}
end
return {0, rev}
`)

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

// newHealthTracker creates lease-scheduled ping coordination over the
// canonical catalog. registryMapName scopes the ping leases and identifies
// the replicated map whose revision counter the scheduler keeps repaired.
func newHealthTracker(
	streamManager StreamManager,
	catalog *toolsetCatalog,
	rdb *redis.Client,
	registryMapName string,
	opts ...HealthTrackerOption,
) (HealthTracker, error) {
	if streamManager == nil {
		return nil, fmt.Errorf("stream manager is required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("catalog is required")
	}
	if rdb == nil {
		return nil, fmt.Errorf("redis client is required")
	}
	if registryMapName == "" {
		return nil, fmt.Errorf("registry map name is required")
	}
	options := healthTrackerOptions{
		pingInterval:        DefaultPingInterval,
		missedPingThreshold: DefaultMissedPingThreshold,
		logger:              telemetry.NewNoopLogger(),
	}
	for _, option := range opts {
		option(&options)
	}
	tracker := &healthTracker{
		streamManager:       streamManager,
		catalog:             catalog,
		catalogMap:          catalog.m,
		redis:               rdb,
		nodeID:              uuid.NewString(),
		leaseScope:          registryMapName,
		revisionHashKey:     "map:" + registryMapName + ":content",
		pingInterval:        options.pingInterval,
		missedPingThreshold: options.missedPingThreshold,
		stalenessThreshold:  deriveStalenessThreshold(options.pingInterval, options.missedPingThreshold),
		logger:              options.logger,
		revFloors:           make(map[string]int64),
		lastObservedHealthy: make(map[string]bool),
		lastObservedPong:    make(map[string]int64),
		closeCh:             make(chan struct{}),
		doneCh:              make(chan struct{}),
	}
	go tracker.run()
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
		ProviderCount:      routableProviderCount(entry, now),
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
	select {
	case <-h.closeCh:
		return fmt.Errorf("health tracker is closed")
	default:
	}
	// Scheduling derives from catalog membership; validating the health
	// identity is the entire ensure contract.
	if _, _, err := h.catalog.HealthIdentity(ctx, toolset); err != nil {
		return err
	}
	return nil
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

// repairMapRevisions guards the catalog replicated map against Redis state
// loss. Pulse rmap replicas apply an update only when its revision exceeds
// their local revision, and a flushed hash restarts "=rev" at zero, so
// without repair every replica that outlived the loss would silently drop
// all subsequent catalog writes — registrations, provider leases, membership
// epochs, and pongs all ride this one map. The scheduler keeps the map's
// Redis counter pinned above the wall clock, which strictly dominates every
// replica's local revision; replicated content then converges as periodic
// writers (provider renewals, pongs) rewrite their keys.
func (h *healthTracker) repairMapRevisions(ctx context.Context) {
	if err := h.ensureMapRevision(ctx, h.revisionHashKey); err != nil {
		h.logger.Error(
			ctx,
			"repair replicated map revision failed",
			"event", "repair_map_revision_failed",
			"component", "tool-registry-health",
			"map", h.leaseScope,
			"err", err,
		)
	}
}

// ensureMapRevision pins one replicated map's Redis revision counter above
// the wall clock in milliseconds so replica-local revisions can never
// outrank it. Revisions advance at most one per committed write while the
// clock advances around a millisecond per write or faster, so a counter
// seeded from time.Now().UnixMilli() strictly dominates every replica's
// local revision — including revisions committed between two scheduler
// ticks, which no sampling scheme can observe. On the first pass the counter
// is silently raised to the current clock (genesis); afterwards a counter
// below the established floor proves Redis lost the map, and the repair
// re-pins it so post-loss writes propagate to all replicas again.
//
// Two contracts pin this to the goa.design/pulse version in go.mod: the hash
// key and "=rev" field name (rmap does not expose its revision counter), and
// the millisecond resolution — rmap's Lua scripts format revisions with
// Lua's %.14g tostring, so counters must stay far below 1e14, which rules
// out micro- or nanosecond clocks.
func (h *healthTracker) ensureMapRevision(ctx context.Context, hashKey string) error {
	rev, err := h.redis.HGet(ctx, hashKey, "=rev").Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("read revision of %q: %w", hashKey, err)
	}
	floor := h.revFloors[hashKey]
	if floor > 0 && rev >= floor {
		h.revFloors[hashKey] = rev
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
		"map", h.leaseScope,
		"redis_revision", rev,
		"restored_revision", final,
	)
	return nil
}

// pingRegisteredToolsets enumerates catalog-registered toolsets and, for each
// lease this node wins, observes health and publishes one ping. Losing the
// lease means another node (or a previous tick) already owns the current
// ping interval.
func (h *healthTracker) pingRegisteredToolsets() {
	ctx := context.Background()
	keys, err := h.catalogMap.AuthoritativeKeys(ctx)
	if err != nil {
		h.logger.Error(
			ctx,
			"enumerate catalog toolsets failed",
			"event", "enumerate_catalog_failed",
			"component", "tool-registry-health",
			"err", err,
		)
		return
	}
	for _, key := range keys {
		toolset := toolsetFromCatalogKey(key)
		if toolset == "" {
			continue
		}
		if _, _, err := h.catalog.HealthIdentity(ctx, toolset); err != nil {
			if !errors.Is(err, errToolsetNotFound) {
				h.logger.Error(ctx, "resolve ping identity failed", "toolset", toolset, "err", err)
			}
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

// deriveStalenessThreshold defines the shared-pong freshness window.
func deriveStalenessThreshold(interval time.Duration, missed int) time.Duration {
	return time.Duration(missed+1) * interval
}

// pingIdentity binds a ping to one token and membership epoch.
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
