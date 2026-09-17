// Package registry owns the toolset admission catalog used by the gateway.
//
// One rmap value atomically owns admission identity, active/retired state,
// provider leases, and discovery metadata. Every transition uses Redis TIME and
// exact CAS so registry replicas cannot split admission ownership.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/rmap"
)

type (
	// catalogMap captures the authoritative and replicated operations required by
	// the catalog. Production uses *rmap.Map; tests use deterministic fakes.
	catalogMap interface {
		Get(key string) (string, bool)
		Keys() []string
		// AuthoritativeKeys enumerates the keys currently stored in Redis.
		// Local replica keys converge eventually; discovery and scheduling
		// must observe an admission the moment Register commits it.
		AuthoritativeKeys(ctx context.Context) ([]string, error)
		Subscribe() <-chan rmap.EventKind
		Unsubscribe(<-chan rmap.EventKind)
		SetIfNotExists(ctx context.Context, key, value string) (bool, error)
		TestAndSetEx(ctx context.Context, key, test, value string) (prev string, existed bool, updated bool, err error)
	}

	// catalogEntry is the single CAS-owned admission record for one toolset.
	catalogEntry struct {
		State               catalogEntryState        `json:"state"`
		Toolset             *catalogToolset          `json:"toolset"`
		SchemaFingerprint   string                   `json:"schema_fingerprint"`
		AdmissionRevision   string                   `json:"admission_revision"`
		WireProtocolVersion int                      `json:"wire_protocol_version"`
		RegistrationToken   string                   `json:"registration_token"`
		RegisteredAt        string                   `json:"registered_at"`
		ProviderLeases      map[string]providerLease `json:"provider_leases"`
		RetiredTokens       map[string]struct{}      `json:"retired_registration_tokens"`
		HealthEpoch         uint64                   `json:"health_epoch"`
		LastPongUnixNano    int64                    `json:"last_pong_unix_nano"`
	}

	// providerLeaseRecord projects one provider lease for health derivation.
	providerLeaseRecord struct {
		ProviderID            string
		IncarnationID         string
		RegistrationToken     string
		LeaseExpiresUnixMilli int64
		Draining              bool
	}

	// providerLease keeps routing and settlement ownership in one catalog value.
	providerLease struct {
		ExpiresAtUnixMilli int64 `json:"expires_at_unix_milli"`
		Draining           bool  `json:"draining"`
	}

	// toolsetCatalog serializes every admission transition through one rmap key.
	toolsetCatalog struct {
		m     catalogMap
		clock registryTimeSource

		definitionsMu sync.Mutex
		definitions   map[string]*catalogToolset
	}

	catalogEntryState string
)

const (
	toolsetCatalogKeyPrefix = "registry:toolset:"

	catalogEntryActive  catalogEntryState = "active"
	catalogEntryRetired catalogEntryState = "retired"
)

var (
	errAdmissionBlocked  = errors.New("toolset admission blocked")
	errAdmissionRetired  = errors.New("toolset admission retired")
	errAdmissionConflict = errors.New("toolset admission conflict")
	errToolsetNotFound   = errors.New("toolset not found")
)

// newToolsetCatalog constructs the canonical admission store.
func newToolsetCatalog(m catalogMap, clock registryTimeSource) *toolsetCatalog {
	return &toolsetCatalog{m: m, clock: clock, definitions: make(map[string]*catalogToolset)}
}

// validatePersistedEntries reads every authoritative catalog value and applies
// the same strict parser used by registration, routing, and health. Construction
// fails with every incompatible key named so cleanup can remove only the
// affected records while the registry remains offline.
func (c *toolsetCatalog) validatePersistedEntries(ctx context.Context) error {
	keys, err := c.authoritativeKeys(ctx)
	if err != nil {
		return fmt.Errorf("enumerate persisted catalog: %w", err)
	}
	sort.Strings(keys)
	var invalid []error
	for _, key := range keys {
		if !strings.HasPrefix(key, toolsetCatalogKeyPrefix) {
			invalid = append(invalid, fmt.Errorf("catalog key %q has invalid prefix", key))
			continue
		}
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			invalid = append(invalid, err)
			continue
		}
		if !exists {
			continue
		}
		name := strings.TrimPrefix(key, toolsetCatalogKeyPrefix)
		if _, err := c.parseCatalogEntry(name, raw); err != nil {
			invalid = append(invalid, fmt.Errorf("catalog key %q: %w", key, err))
		}
	}
	return errors.Join(invalid...)
}

// Register atomically creates, renews, or replaces one admission and provider
// lease. Different admissions remain blocked until Redis TIME proves all
// current leases expired.
func (c *toolsetCatalog) Register(
	ctx context.Context,
	toolset *genregistry.Toolset,
	admissionRevision, providerID, incarnationID string,
	leaseDuration time.Duration,
) (catalogEntry, error) {
	if leaseDuration < toolregistry.MinProviderLeaseDuration ||
		leaseDuration > toolregistry.MaxProviderLeaseDuration {
		return catalogEntry{}, fmt.Errorf(
			"provider lease duration must be between %s and %s",
			toolregistry.MinProviderLeaseDuration,
			toolregistry.MaxProviderLeaseDuration,
		)
	}
	fingerprint, err := toolsetSchemaFingerprint(toolset)
	if err != nil {
		return catalogEntry{}, err
	}
	token, err := admissionRegistrationToken(
		fingerprint,
		admissionRevision,
		toolregistry.WireProtocolVersion,
	)
	if err != nil {
		return catalogEntry{}, fmt.Errorf("derive toolset %q admission token: %w", toolset.Name, err)
	}
	key := toolsetCatalogKey(toolset.Name)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return catalogEntry{}, err
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return catalogEntry{}, err
		}
		if now.UnixMilli() > math.MaxInt64-leaseDuration.Milliseconds() {
			return catalogEntry{}, fmt.Errorf("provider lease deadline overflows Unix milliseconds")
		}
		lease := providerLease{ExpiresAtUnixMilli: now.Add(leaseDuration).UnixMilli()}
		if !exists {
			candidate, err := newCatalogEntry(
				toolset,
				fingerprint,
				admissionRevision,
				token,
				providerLeaseKey(providerID, incarnationID),
				lease,
				now,
				make(map[string]struct{}),
			)
			if err != nil {
				return catalogEntry{}, err
			}
			candidateRaw, err := marshalCatalogEntry(candidate)
			if err != nil {
				return catalogEntry{}, err
			}
			inserted, err := c.m.SetIfNotExists(ctx, key, candidateRaw)
			if err != nil {
				return catalogEntry{}, fmt.Errorf("insert toolset %q admission: %w", toolset.Name, err)
			}
			if inserted {
				return candidate, nil
			}
			continue
		}

		existing, err := c.parseCatalogEntry(toolset.Name, raw)
		if err != nil {
			return catalogEntry{}, err
		}
		pruned := pruneExpiredProviderLeases(&existing, now)
		if _, retired := existing.RetiredTokens[token]; retired {
			return catalogEntry{}, fmt.Errorf("%w: %q token %s", errAdmissionRetired, toolset.Name, token)
		}
		if existing.RegistrationToken == token {
			hadRoutable := routableProviderCount(existing, now) > 0
			existing.ProviderLeases[providerLeaseKey(providerID, incarnationID)] = lease
			if !hadRoutable {
				existing.HealthEpoch++
				existing.LastPongUnixNano = 0
			}
			updated, err := c.replace(ctx, key, raw, existing)
			if err != nil {
				return catalogEntry{}, err
			}
			if updated {
				return existing, nil
			}
			continue
		}
		if len(existing.ProviderLeases) > 0 {
			if pruned {
				updated, err := c.replace(ctx, key, raw, existing)
				if err != nil {
					return catalogEntry{}, err
				}
				if !updated {
					continue
				}
			}
			return catalogEntry{}, fmt.Errorf(
				"%w: toolset %q admission %s retains %d provider leases",
				errAdmissionBlocked,
				toolset.Name,
				existing.RegistrationToken,
				len(existing.ProviderLeases),
			)
		}

		retiredTokens := cloneTokenSet(existing.RetiredTokens)
		retiredTokens[existing.RegistrationToken] = struct{}{}
		candidate, err := newCatalogEntry(
			toolset,
			fingerprint,
			admissionRevision,
			token,
			providerLeaseKey(providerID, incarnationID),
			lease,
			now,
			retiredTokens,
		)
		if err != nil {
			return catalogEntry{}, err
		}
		updated, err := c.replace(ctx, key, raw, candidate)
		if err != nil {
			return catalogEntry{}, err
		}
		if updated {
			return candidate, nil
		}
	}
}

// DrainProvider marks one exact lease non-routable while preserving settlement
// ownership for calls that incarnation already claimed.
func (c *toolsetCatalog) DrainProvider(
	ctx context.Context,
	name, providerID, incarnationID, expectedToken string,
	leaseDuration time.Duration,
) error {
	key := toolsetCatalogKey(name)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		entry, err := c.parseCatalogEntry(name, raw)
		if err != nil {
			return err
		}
		if entry.RegistrationToken != expectedToken {
			return nil
		}
		leaseKey := providerLeaseKey(providerID, incarnationID)
		lease, exists := entry.ProviderLeases[leaseKey]
		if !exists {
			return nil
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return err
		}
		if lease.ExpiresAtUnixMilli <= now.UnixMilli() {
			pruneExpiredProviderLeases(&entry, now)
			updated, err := c.replace(ctx, key, raw, entry)
			if err != nil {
				return err
			}
			if updated {
				return nil
			}
			continue
		}
		if now.UnixMilli() > math.MaxInt64-leaseDuration.Milliseconds() {
			return fmt.Errorf("provider drain deadline overflows Unix milliseconds")
		}
		hadRoutable := routableProviderCount(entry, now) > 0
		lease.Draining = true
		lease.ExpiresAtUnixMilli = max(lease.ExpiresAtUnixMilli, now.Add(leaseDuration).UnixMilli())
		entry.ProviderLeases[leaseKey] = lease
		if hadRoutable && routableProviderCount(entry, now) == 0 {
			entry.HealthEpoch++
			entry.LastPongUnixNano = 0
		}
		updated, err := c.replace(ctx, key, raw, entry)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
}

// ReleaseProvider removes one exact provider lease. Missing providers, missing
// records, and stale tokens are idempotent successes.
func (c *toolsetCatalog) ReleaseProvider(
	ctx context.Context,
	name, providerID, incarnationID, expectedToken string,
) error {
	key := toolsetCatalogKey(name)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		entry, err := c.parseCatalogEntry(name, raw)
		if err != nil {
			return err
		}
		if entry.RegistrationToken != expectedToken {
			return nil
		}
		leaseKey := providerLeaseKey(providerID, incarnationID)
		now, err := c.clock.Now(ctx)
		if err != nil {
			return err
		}
		pruned := pruneExpiredProviderLeases(&entry, now)
		if _, exists := entry.ProviderLeases[leaseKey]; !exists {
			if !pruned {
				return nil
			}
			updated, err := c.replace(ctx, key, raw, entry)
			if err != nil {
				return err
			}
			if updated {
				return nil
			}
			continue
		}
		hadRoutable := routableProviderCount(entry, now) > 0
		delete(entry.ProviderLeases, leaseKey)
		if hadRoutable && routableProviderCount(entry, now) == 0 {
			entry.HealthEpoch++
			entry.LastPongUnixNano = 0
		}
		updated, err := c.replace(ctx, key, raw, entry)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
}

// Retire atomically marks the exact admission unavailable while preserving
// leases for graceful release or expiry.
func (c *toolsetCatalog) Retire(ctx context.Context, name, expectedToken string) error {
	key := toolsetCatalogKey(name)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		entry, err := c.parseCatalogEntry(name, raw)
		if err != nil {
			return err
		}
		if entry.RegistrationToken != expectedToken {
			return fmt.Errorf(
				"%w: toolset %q token %s differs from expected %s",
				errAdmissionConflict,
				name,
				entry.RegistrationToken,
				expectedToken,
			)
		}
		if entry.State == catalogEntryRetired {
			return nil
		}
		entry.State = catalogEntryRetired
		entry.RetiredTokens[entry.RegistrationToken] = struct{}{}
		updated, err := c.replace(ctx, key, raw, entry)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
}

// GetToolset returns one active toolset.
func (c *toolsetCatalog) GetToolset(ctx context.Context, name string) (*genregistry.Toolset, error) {
	entry, err := c.ActiveRegistration(ctx, name)
	if err != nil {
		return nil, err
	}
	return entry.Toolset.decode()
}

// ActiveRegistration returns the exact active admission used for routing.
func (c *toolsetCatalog) ActiveRegistration(ctx context.Context, name string) (catalogEntry, error) {
	raw, exists, err := c.exactRaw(ctx, toolsetCatalogKey(name))
	if err != nil {
		return catalogEntry{}, err
	}
	if !exists {
		return catalogEntry{}, errToolsetNotFound
	}
	entry, err := c.parseCatalogEntry(name, raw)
	if err != nil {
		return catalogEntry{}, err
	}
	if entry.State != catalogEntryActive {
		return catalogEntry{}, errToolsetNotFound
	}
	return entry, nil
}

// RegistrationToken returns the current active admission token.
func (c *toolsetCatalog) RegistrationToken(ctx context.Context, name string) (string, error) {
	entry, err := c.ActiveRegistration(ctx, name)
	if err != nil {
		return "", err
	}
	return entry.RegistrationToken, nil
}

// VerifyActiveToken rechecks routing ownership immediately before publication.
func (c *toolsetCatalog) VerifyActiveToken(ctx context.Context, name, token string) error {
	entry, err := c.ActiveRegistration(ctx, name)
	if err != nil {
		return err
	}
	if entry.RegistrationToken != token {
		return errToolsetNotFound
	}
	return nil
}

// ActiveProviderLease reports whether one provider currently owns an unexpired
// lease in the exact active admission and returns the Redis timestamp used.
func (c *toolsetCatalog) ActiveProviderLease(
	ctx context.Context,
	name, providerID, incarnationID, token string,
) (bool, time.Time, error) {
	entry, err := c.ActiveRegistration(ctx, name)
	if err != nil {
		return false, time.Time{}, err
	}
	if entry.RegistrationToken != token {
		return false, time.Time{}, errToolsetNotFound
	}
	now, err := c.clock.Now(ctx)
	if err != nil {
		return false, time.Time{}, err
	}
	lease, exists := entry.ProviderLeases[providerLeaseKey(providerID, incarnationID)]
	return exists && lease.ExpiresAtUnixMilli > now.UnixMilli(), now, nil
}

// ActiveProviderLeases projects every lease from the exact active admission.
func (c *toolsetCatalog) ActiveProviderLeases(
	ctx context.Context,
	name, token string,
) ([]providerLeaseRecord, error) {
	entry, err := c.ActiveRegistration(ctx, name)
	if err != nil {
		return nil, err
	}
	if entry.RegistrationToken != token {
		return nil, errToolsetNotFound
	}
	leases := make([]providerLeaseRecord, 0, len(entry.ProviderLeases))
	for leaseKey, lease := range entry.ProviderLeases {
		providerID, incarnationID, err := parseProviderLeaseKey(leaseKey)
		if err != nil {
			return nil, err
		}
		leases = append(leases, providerLeaseRecord{
			ProviderID:            providerID,
			IncarnationID:         incarnationID,
			RegistrationToken:     token,
			LeaseExpiresUnixMilli: lease.ExpiresAtUnixMilli,
			Draining:              lease.Draining,
		})
	}
	return leases, nil
}

// HealthIdentity returns the exact token and membership epoch a ping must carry.
func (c *toolsetCatalog) HealthIdentity(ctx context.Context, name string) (string, uint64, error) {
	entry, now, err := c.healthEntry(ctx, name)
	if err != nil {
		return "", 0, err
	}
	if routableProviderCount(entry, now) == 0 {
		return "", 0, errToolsetNotFound
	}
	return entry.RegistrationToken, entry.HealthEpoch, nil
}

// RecordPong atomically authenticates one provider incarnation and records a
// monotonic pong in the same record that owns its lease and health epoch.
func (c *toolsetCatalog) RecordPong(
	ctx context.Context,
	name, providerID, incarnationID, token string,
	epoch uint64,
) error {
	key := toolsetCatalogKey(name)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		entry, err := c.parseCatalogEntry(name, raw)
		if err != nil {
			return err
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return err
		}
		if pruneExpiredProviderLeases(&entry, now) {
			updated, err := c.replace(ctx, key, raw, entry)
			if err != nil {
				return err
			}
			if !updated {
				continue
			}
			if len(entry.ProviderLeases) == 0 {
				return nil
			}
			raw, err = marshalCatalogEntry(entry)
			if err != nil {
				return err
			}
		}
		if entry.State != catalogEntryActive ||
			entry.RegistrationToken != token ||
			entry.HealthEpoch != epoch {
			return nil
		}
		lease, admitted := entry.ProviderLeases[providerLeaseKey(providerID, incarnationID)]
		if !admitted || lease.Draining || lease.ExpiresAtUnixMilli <= now.UnixMilli() {
			return nil
		}
		entry.LastPongUnixNano = max(entry.LastPongUnixNano, now.UnixNano())
		updated, err := c.replace(ctx, key, raw, entry)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
}

// healthEntry returns an authoritative active record after atomically pruning
// expired leases and advancing the membership epoch when the last lease ends.
func (c *toolsetCatalog) healthEntry(ctx context.Context, name string) (catalogEntry, time.Time, error) {
	key := toolsetCatalogKey(name)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return catalogEntry{}, time.Time{}, err
		}
		if !exists {
			return catalogEntry{}, time.Time{}, errToolsetNotFound
		}
		entry, err := c.parseCatalogEntry(name, raw)
		if err != nil {
			return catalogEntry{}, time.Time{}, err
		}
		if entry.State != catalogEntryActive {
			return catalogEntry{}, time.Time{}, errToolsetNotFound
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return catalogEntry{}, time.Time{}, err
		}
		if !pruneExpiredProviderLeases(&entry, now) {
			return entry, now, nil
		}
		updated, err := c.replace(ctx, key, raw, entry)
		if err != nil {
			return catalogEntry{}, time.Time{}, err
		}
		if updated {
			return entry, now, nil
		}
	}
}

// ListToolsets returns active catalog entries matching every requested tag.
func (c *toolsetCatalog) ListToolsets(ctx context.Context, tags []string) ([]*genregistry.ToolsetInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys, err := c.authoritativeKeys(ctx)
	if err != nil {
		return nil, err
	}
	toolsets := make([]*genregistry.ToolsetInfo, 0, len(keys))
	for _, key := range keys {
		if !strings.HasPrefix(key, toolsetCatalogKeyPrefix) {
			continue
		}
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		entry, err := c.parseCatalogEntry(strings.TrimPrefix(key, toolsetCatalogKeyPrefix), raw)
		if err != nil {
			return nil, err
		}
		if entry.State == catalogEntryActive && catalogMatchesTags(entry.Toolset.info.Tags, tags) {
			toolsets = append(toolsets, entry.Toolset.info)
		}
	}
	return toolsets, nil
}

// SearchToolsets returns active entries matching name, description, or tags.
func (c *toolsetCatalog) SearchToolsets(ctx context.Context, query string) ([]*genregistry.ToolsetInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lowerQuery := strings.ToLower(query)
	keys, err := c.authoritativeKeys(ctx)
	if err != nil {
		return nil, err
	}
	toolsets := make([]*genregistry.ToolsetInfo, 0, len(keys))
	for _, key := range keys {
		if !strings.HasPrefix(key, toolsetCatalogKeyPrefix) {
			continue
		}
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		entry, err := c.parseCatalogEntry(strings.TrimPrefix(key, toolsetCatalogKeyPrefix), raw)
		if err != nil {
			return nil, err
		}
		if entry.State == catalogEntryActive && catalogMatchesQuery(entry.Toolset.info, lowerQuery) {
			toolsets = append(toolsets, entry.Toolset.info)
		}
	}
	return toolsets, nil
}

// replace marshals and exact-CAS replaces one catalog entry.
func (c *toolsetCatalog) replace(
	ctx context.Context,
	key, raw string,
	entry catalogEntry,
) (bool, error) {
	next, err := marshalCatalogEntry(entry)
	if err != nil {
		return false, err
	}
	_, _, updated, err := c.m.TestAndSetEx(ctx, key, raw, next)
	if err != nil {
		return false, fmt.Errorf("replace catalog key %q: %w", key, err)
	}
	return updated, nil
}

// authoritativeKeys reads the current Redis keys and drops local definitions
// whose records were removed. A concurrent registration can only lose reuse;
// its next read still validates the exact saved definition.
func (c *toolsetCatalog) authoritativeKeys(ctx context.Context) ([]string, error) {
	keys, err := c.m.AuthoritativeKeys(ctx)
	if err != nil {
		return nil, err
	}
	present := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if name, ok := strings.CutPrefix(key, toolsetCatalogKeyPrefix); ok {
			present[name] = struct{}{}
		}
	}
	c.definitionsMu.Lock()
	for name := range c.definitions {
		if _, exists := present[name]; !exists {
			delete(c.definitions, name)
		}
	}
	c.definitionsMu.Unlock()
	return keys, nil
}

// exactRaw reads authoritative Redis state through a no-op rmap CAS and drops
// the local definition when Redis reports that its record no longer exists.
func (c *toolsetCatalog) exactRaw(ctx context.Context, key string) (string, bool, error) {
	raw, exists, _, err := c.m.TestAndSetEx(ctx, key, "", "")
	if err != nil {
		return "", false, fmt.Errorf("read catalog key %q: %w", key, err)
	}
	if !exists {
		c.definitionsMu.Lock()
		delete(c.definitions, strings.TrimPrefix(key, toolsetCatalogKeyPrefix))
		c.definitionsMu.Unlock()
	}
	return raw, exists, nil
}

// newCatalogEntry builds a fresh active admission from Redis time.
func newCatalogEntry(
	toolset *genregistry.Toolset,
	fingerprint, revision, token, leaseKey string,
	lease providerLease,
	now time.Time,
	retiredTokens map[string]struct{},
) (catalogEntry, error) {
	registeredAt := now.UTC().Format(time.RFC3339Nano)
	registered := *toolset
	registered.RegisteredAt = registeredAt
	definition, err := newCatalogToolset(&registered, fingerprint)
	if err != nil {
		return catalogEntry{}, err
	}
	return catalogEntry{
		State:               catalogEntryActive,
		Toolset:             definition,
		SchemaFingerprint:   fingerprint,
		AdmissionRevision:   revision,
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		RegistrationToken:   token,
		RegisteredAt:        registeredAt,
		ProviderLeases:      map[string]providerLease{leaseKey: lease},
		RetiredTokens:       retiredTokens,
		HealthEpoch:         1,
	}, nil
}

// pruneExpiredProviderLeases removes leases at or before Redis TIME and fences
// pongs from the prior non-empty membership epoch when the last lease expires.
func pruneExpiredProviderLeases(entry *catalogEntry, now time.Time) bool {
	changed := false
	hadRoutable := nonDrainingProviderCount(*entry) > 0
	for leaseKey, lease := range entry.ProviderLeases {
		if lease.ExpiresAtUnixMilli <= now.UnixMilli() {
			delete(entry.ProviderLeases, leaseKey)
			changed = true
		}
	}
	if hadRoutable && routableProviderCount(*entry, now) == 0 {
		entry.HealthEpoch++
		entry.LastPongUnixNano = 0
	}
	return changed
}

// nonDrainingProviderCount returns membership immediately before expiration
// pruning, so removing the final formerly routable lease advances one epoch.
func nonDrainingProviderCount(entry catalogEntry) int {
	count := 0
	for _, lease := range entry.ProviderLeases {
		if !lease.Draining {
			count++
		}
	}
	return count
}

// routableProviderCount returns unexpired non-draining leases at now.
func routableProviderCount(entry catalogEntry, now time.Time) int {
	count := 0
	for _, lease := range entry.ProviderLeases {
		if !lease.Draining && (now.IsZero() || lease.ExpiresAtUnixMilli > now.UnixMilli()) {
			count++
		}
	}
	return count
}

// providerLeaseKey binds a deployment-stable provider label to one runtime
// incarnation without allowing either component to alias another lease.
func providerLeaseKey(providerID, incarnationID string) string {
	return providerID + "\x00" + incarnationID
}

// parseProviderLeaseKey validates and splits the persisted composite lease key.
func parseProviderLeaseKey(key string) (string, string, error) {
	providerID, incarnationID, ok := strings.Cut(key, "\x00")
	if !ok || providerID == "" {
		return "", "", fmt.Errorf("invalid provider lease key")
	}
	if _, err := uuid.Parse(incarnationID); err != nil {
		return "", "", fmt.Errorf("invalid provider incarnation ID: %w", err)
	}
	return providerID, incarnationID, nil
}

// cloneTokenSet returns independent tombstone ownership for one replacement CAS.
func cloneTokenSet(tokens map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(tokens)+1)
	for token := range tokens {
		cloned[token] = struct{}{}
	}
	return cloned
}

// toolsetSchemaFingerprint returns the canonical schema identity.
func toolsetSchemaFingerprint(toolset *genregistry.Toolset) (string, error) {
	tools := make([]internaladmission.ToolSchema, len(toolset.Tools))
	for i, tool := range toolset.Tools {
		var consumerContract []byte
		if tool.ConsumerContract != nil {
			var err error
			consumerContract, err = json.Marshal(tool.ConsumerContract)
			if err != nil {
				return "", fmt.Errorf("encode tool %q consumer contract: %w", tool.Name, err)
			}
		}
		tools[i] = internaladmission.ToolSchema{
			Name:                   tool.Name,
			Description:            tool.Description,
			Tags:                   tool.Tags,
			PayloadSchema:          tool.PayloadSchema,
			ExecutionPayloadSchema: tool.ExecutionPayloadSchema,
			ResultSchema:           tool.ResultSchema,
			SidecarSchema:          tool.SidecarSchema,
			ConsumerContract:       consumerContract,
		}
	}
	var version *string
	if toolset.Version != nil {
		value := string(*toolset.Version)
		version = &value
	}
	return internaladmission.SchemaFingerprint(internaladmission.Schema{
		Name:        toolset.Name,
		Description: toolset.Description,
		Version:     version,
		Tags:        toolset.Tags,
		Tools:       tools,
	}), nil
}

// admissionRegistrationToken derives the wire-visible execution fence from the
// exact schema, deployment revision, and provider message protocol.
func admissionRegistrationToken(
	schemaFingerprint, admissionRevision string,
	wireProtocolVersion int,
) (string, error) {
	if err := toolregistry.ValidateAdmissionRevision(admissionRevision); err != nil {
		return "", err
	}
	return internaladmission.RegistrationToken(
		schemaFingerprint,
		admissionRevision,
		wireProtocolVersion,
	)
}

func toolsetCatalogKey(name string) string {
	return toolsetCatalogKeyPrefix + name
}

func catalogMatchesTags(toolsetTags, filterTags []string) bool {
	if len(filterTags) == 0 {
		return true
	}
	tagSet := make(map[string]struct{}, len(toolsetTags))
	for _, tag := range toolsetTags {
		tagSet[tag] = struct{}{}
	}
	for _, tag := range filterTags {
		if _, ok := tagSet[tag]; !ok {
			return false
		}
	}
	return true
}

func catalogMatchesQuery(toolset *genregistry.ToolsetInfo, query string) bool {
	if strings.Contains(strings.ToLower(toolset.Name), query) {
		return true
	}
	if toolset.Description != nil && strings.Contains(strings.ToLower(*toolset.Description), query) {
		return true
	}
	for _, tag := range toolset.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}
