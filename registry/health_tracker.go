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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

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
		expectedToolsets    []string
	}

	healthTracker struct {
		streamManager      StreamManager
		catalog            *toolsetCatalog
		catalogMap         catalogMap
		redis              *redis.Client
		nodeID             string
		leaseScope         string
		revisionHashKey    string
		pingInterval       time.Duration
		stalenessThreshold time.Duration
		expectedToolsets   []string

		// revFloors remembers the highest revision this node pinned or
		// observed per replicated-map hash key; a Redis counter below the
		// floor proves state loss.
		revFloors map[string]int64

		schedulerCtx    context.Context
		cancelScheduler context.CancelFunc
		doneCh          chan struct{}
		closeOnce       sync.Once
	}
)

const (
	// DefaultPingInterval is the default interval between health pings.
	DefaultPingInterval = 10 * time.Second
	// DefaultMissedPingThreshold is the tolerated number of missed pings.
	DefaultMissedPingThreshold = 3
	// HealthSweepSpanName is the root span for one periodic registry health check.
	HealthSweepSpanName = "toolregistry.health.sweep"

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

// withExpectedToolsets records the application-owned toolset names whose
// presence is checked after each successful catalog read.
func withExpectedToolsets(toolsets []string) HealthTrackerOption {
	return func(options *healthTrackerOptions) {
		options.expectedToolsets = slices.Clone(toolsets)
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
) (*healthTracker, error) {
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
	}
	for _, option := range opts {
		option(&options)
	}
	schedulerCtx, cancelScheduler := context.WithCancel(context.Background()) //nolint:gosec // Close owns cancellation.
	tracker := &healthTracker{
		streamManager:      streamManager,
		catalog:            catalog,
		catalogMap:         catalog.m,
		redis:              rdb,
		nodeID:             uuid.NewString(),
		leaseScope:         registryMapName,
		revisionHashKey:    "map:" + registryMapName + ":content",
		pingInterval:       options.pingInterval,
		stalenessThreshold: deriveStalenessThreshold(options.pingInterval, options.missedPingThreshold),
		expectedToolsets:   options.expectedToolsets,
		revFloors:          make(map[string]int64),
		schedulerCtx:       schedulerCtx,
		cancelScheduler:    cancelScheduler,
		doneCh:             make(chan struct{}),
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
	return h.healthFromEntry(entry, now), nil
}

// EnsurePingLoop implements HealthTracker.
func (h *healthTracker) EnsurePingLoop(ctx context.Context, toolset string) error {
	select {
	case <-h.schedulerCtx.Done():
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
		h.cancelScheduler()
		<-h.doneCh
	})
	return nil
}

// healthFromEntry derives routing health from the same catalog record and clock
// instant used to select its revision and provider leases.
func (h *healthTracker) healthFromEntry(entry catalogEntry, now time.Time) ToolsetHealth {
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
	return health
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
		case <-h.schedulerCtx.Done():
			return
		case <-ticker.C:
			h.runHealthSweep(h.schedulerCtx)
		}
	}
}

// runHealthSweep records one scheduler attempt, repairs the catalog revision,
// and samples every toolset whose lease this registry node wins. Failures before
// a toolset sample are recorded on this span because no readiness result exists
// for that toolset.
func (h *healthTracker) runHealthSweep(ctx context.Context) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(
		ctx,
		HealthSweepSpanName,
		trace.WithAttributes(attribute.String("toolregistry.registry", h.leaseScope)),
	)
	defer span.End()

	if err := h.ensureMapRevision(ctx, h.revisionHashKey); err != nil {
		if ctx.Err() != nil {
			return
		}
		recordHealthSweepError(ctx, "repair_revision", "", err)
	}
	h.pingRegisteredToolsets(ctx)
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
	trace.SpanFromContext(ctx).AddEvent(
		"repaired catalog revision",
		trace.WithAttributes(
			attribute.String("toolregistry.registry", h.leaseScope),
			attribute.Int64("toolregistry.previous_revision", rev),
			attribute.Int64("toolregistry.restored_revision", final),
		),
	)
	return nil
}

// pingRegisteredToolsets reads the active toolsets from the shared catalog.
// Every registry replica records the active names it sees. The replica that
// wins a toolset's lease also records whether that toolset can accept calls and
// publishes a ping when at least one provider can receive it. Losing the lease
// means another replica or a previous check already sampled the current
// interval.
func (h *healthTracker) pingRegisteredToolsets(ctx context.Context) {
	toolsets, err := h.catalog.ListToolsets(ctx, nil)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		recordHealthSweepError(ctx, "enumerate_toolsets", "", err)
		return
	}
	active := make(map[string]struct{}, len(toolsets))
	for _, registered := range toolsets {
		active[registered.Name] = struct{}{}
	}
	h.recordCatalogExpectations(ctx, active)
	for _, registered := range toolsets {
		toolset := registered.Name
		h.recordCatalogEntry(ctx, toolset)
		acquired, err := h.acquirePingLease(ctx, toolset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			recordHealthSweepError(ctx, "acquire_lease", toolset, err)
			continue
		}
		if !acquired {
			continue
		}
		h.sampleRegisteredToolset(ctx, toolset)
	}
}

// recordCatalogExpectations reports whether each application-required toolset
// was present in a successful catalog read. It emits no result when the read
// fails, so missing telemetry remains unknown rather than being reported as an
// absent catalog entry.
func (h *healthTracker) recordCatalogExpectations(ctx context.Context, active map[string]struct{}) {
	for _, toolset := range h.expectedToolsets {
		present := 0
		if _, exists := active[toolset]; exists {
			present = 1
		}
		_, span := otel.Tracer("goa.design/goa-ai/registry").Start(
			ctx,
			"toolregistry.catalog.expectation",
			trace.WithAttributes(
				attribute.String("toolregistry.registry", h.leaseScope),
				attribute.String("toolregistry.toolset", toolset),
				attribute.Int("toolregistry.present", present),
			),
		)
		span.SetStatus(codes.Ok, "checked")
		span.End()
	}
}

// recordCatalogEntry reports one active name returned by the shared catalog.
// Every registry replica records this before competing to send the health ping,
// so each replica reports the active catalog it observed even when another
// replica sends the ping.
func (h *healthTracker) recordCatalogEntry(ctx context.Context, toolset string) {
	_, span := otel.Tracer("goa.design/goa-ai/registry").Start(
		ctx,
		"toolregistry.catalog.entry",
		trace.WithAttributes(
			attribute.String("toolregistry.registry", h.leaseScope),
			attribute.String("toolregistry.toolset", toolset),
		),
	)
	span.SetStatus(codes.Ok, "observed")
	span.End()
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

// sendPing publishes one ping only while the catalog still has a live provider
// for the admission. A provider that leaves after the readiness sample is an
// expected race and receives no ping. Catalog and publication failures are
// returned to the health span.
func (h *healthTracker) sendPing(ctx context.Context, toolset string) error {
	token, epoch, err := h.catalog.HealthIdentity(ctx, toolset)
	if err != nil {
		if errors.Is(err, errToolsetNotFound) {
			return nil
		}
		return fmt.Errorf("resolve ping identity: %w", err)
	}
	pingID := newPingID(token, epoch)
	if err := h.streamManager.PublishToolCall(
		ctx,
		toolset,
		toolregistry.NewPingMessage(token, pingID),
	); err != nil {
		return fmt.Errorf("publish ping: %w", err)
	}
	return nil
}

// sampleRegisteredToolset records one periodic health span after this node wins
// the toolset's sampling lease. A successful catalog read records whether the
// toolset can accept a call. A missing catalog entry records no readiness
// because another operation may have removed it during sampling. Other read
// failures are recorded as errors on the span.
func (h *healthTracker) sampleRegisteredToolset(ctx context.Context, toolset string) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(
		ctx,
		"toolregistry.health",
		trace.WithAttributes(
			attribute.String("toolregistry.registry", h.leaseScope),
			attribute.String("toolregistry.toolset", toolset),
		),
	)
	defer span.End()

	entry, now, err := h.catalog.healthEntry(ctx, toolset)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if !errors.Is(err, errToolsetNotFound) {
			span.RecordError(err)
			span.SetStatus(codes.Error, "read toolset health")
		}
		return
	}
	health := h.healthFromEntry(entry, now)
	ready := 0
	if health.Healthy {
		ready = 1
	}
	span.SetAttributes(
		attribute.Int("toolregistry.ready", ready),
		attribute.Int("toolregistry.provider_count", health.ProviderCount),
		attribute.Bool("toolregistry.pong_seen", !health.LastPong.IsZero()),
		attribute.Int64("toolregistry.staleness_threshold_ms", health.StalenessThreshold.Milliseconds()),
	)
	if !health.LastPong.IsZero() {
		span.SetAttributes(attribute.Int64("toolregistry.last_pong_age_ms", health.Age.Milliseconds()))
	}
	if health.ProviderCount > 0 {
		if err := h.sendPing(ctx, toolset); err != nil {
			if ctx.Err() != nil {
				return
			}
			span.RecordError(err)
			span.SetStatus(codes.Error, "send toolset health ping")
			return
		}
	}
	span.SetStatus(codes.Ok, "observed")
}

// recordHealthSweepError records why the scheduler could not produce one or
// more toolset readiness samples. The toolset is included only when the failed
// operation had already selected one.
func recordHealthSweepError(ctx context.Context, step, toolset string, err error) {
	span := trace.SpanFromContext(ctx)
	attrs := []attribute.KeyValue{attribute.String("toolregistry.step", step)}
	if toolset != "" {
		attrs = append(attrs, attribute.String("toolregistry.toolset", toolset))
	}
	span.RecordError(err, trace.WithAttributes(attrs...))
	span.SetStatus(codes.Error, "health sweep failed")
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
