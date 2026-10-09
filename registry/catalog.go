// Package registry owns the toolset admission catalog used by the gateway.
//
// Compact provider state owns admission identity, leases, and health. Definitions
// and permanent retirement history are separate. Conditional writes update the
// affected records atomically, using Redis time for lease decisions.
package registry

import (
	"context"
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
)

type (
	// catalogState contains only the facts used by discovery, health, and
	// provider lifecycle operations. Tool definitions never enter this JSON.
	catalogState struct {
		Identity            *CatalogIdentity         `json:"identity,omitempty"`
		NativeAgent         bool                     `json:"native_agent,omitempty"`
		State               catalogEntryState        `json:"state"`
		Info                *genregistry.ToolsetInfo `json:"info"`
		SchemaFingerprint   string                   `json:"schema_fingerprint"`
		AdmissionRevision   string                   `json:"admission_revision"`
		WireProtocolVersion int                      `json:"wire_protocol_version"`
		RegistrationToken   string                   `json:"registration_token"`
		RegisteredAt        string                   `json:"registered_at"`
		ProviderLeases      map[string]providerLease `json:"provider_leases"`
		HealthEpoch         uint64                   `json:"health_epoch"`
		LastPongUnixNano    int64                    `json:"last_pong_unix_nano"`
	}

	// catalogEntry pairs validated current state with its exact definition.
	// Only definition-dependent operations construct this value.
	catalogEntry struct {
		catalogState
		Toolset *catalogToolset
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

	// toolsetCatalog owns atomic provider transitions and validated definitions.
	toolsetCatalog struct {
		store           catalogStore
		clock           registryTimeSource
		validator       *schemaValidator
		definitionsMu   sync.RWMutex
		definitionLoads chan struct{}
		definitions     map[string]*catalogDefinition
	}

	// toolNameConflictError reports that a declaration was rejected because
	// another active toolset in the same identity scope already provides Tool.
	// Owner is that toolset's catalog identity name, for example the public
	// toolset name an application shows its users. Nothing was saved.
	toolNameConflictError struct {
		Tool  string
		Owner string
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
	errProviderLeaseLost = errors.New("provider lease is no longer held")
)

// newToolsetCatalog constructs the canonical admission store.
func newToolsetCatalog(store catalogStore, clock registryTimeSource) *toolsetCatalog {
	return &toolsetCatalog{
		store: store, clock: clock, validator: newSchemaValidator(),
		definitions:     make(map[string]*catalogDefinition),
		definitionLoads: make(chan struct{}, 1),
	}
}

// validatePersistedEntries checks all definition/state pairs and permanent
// retired tokens before the registry begins serving. Old combined records are
// rejected by the new strict decoder and require the offline conversion.
// When every record is valid, it then rebuilds the tool-name claims of records
// with identity, so records saved before claims existed, or by an older
// registry, are protected before this registry accepts any write. Two active
// records in one scope that provide the same tool name fail startup.
func (c *toolsetCatalog) validatePersistedEntries(ctx context.Context) error {
	keys, err := c.store.Keys(ctx)
	if err != nil {
		return fmt.Errorf("enumerate persisted catalog: %w", err)
	}
	definitionKeys, err := c.store.DefinitionKeys(ctx)
	if err != nil {
		return fmt.Errorf("enumerate persisted definitions: %w", err)
	}
	tokens, err := c.store.RetiredTokens(ctx)
	if err != nil {
		return fmt.Errorf("read retired tokens: %w", err)
	}
	var invalid []error
	for _, token := range tokens {
		if err := toolregistry.ValidateRegistrationToken(token); err != nil {
			invalid = append(invalid, err)
		}
	}
	all := make(map[string]struct{}, len(keys)+len(definitionKeys))
	c.pruneDefinitions(keys)
	for _, key := range keys {
		all[key] = struct{}{}
	}
	for _, key := range definitionKeys {
		all[key] = struct{}{}
	}
	keys = keys[:0]
	for key := range all {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var scoped []string
	for _, key := range keys {
		if !strings.HasPrefix(key, toolsetCatalogKeyPrefix) {
			invalid = append(invalid, fmt.Errorf("catalog key %q has invalid prefix", key))
			continue
		}
		name := strings.TrimPrefix(key, toolsetCatalogKeyPrefix)
		entry, err := c.snapshot(ctx, name)
		if err != nil {
			invalid = append(invalid, fmt.Errorf("catalog key %q: %w", key, err))
			continue
		}
		if entry.Identity != nil {
			scoped = append(scoped, name)
		}
	}
	if len(invalid) > 0 {
		return errors.Join(invalid...)
	}
	return c.claimSavedToolNames(ctx, scoped)
}

// claimSavedToolNames makes the saved tool-name claims of the named records
// match their current state: each active record claims every tool name of its
// saved declaration, and each retired record releases its names.
//
// A record whose claim conflicts is retried after the other records, because
// the conflicting name may be a stale claim that another record releases when
// its own turn comes, for example a name an older registry dropped in a
// replacement without updating claims. The passes stop when one pass makes no
// progress. The conflicts left then are two active records providing the same
// tool name; they are returned together, and the registry does not choose
// which record keeps the name.
func (c *toolsetCatalog) claimSavedToolNames(ctx context.Context, names []string) error {
	pending := names
	for {
		var conflicts []string
		var conflictErrs []error
		for _, name := range pending {
			err := c.claimSavedRecordToolNames(ctx, name)
			if errors.As(err, new(*toolNameConflictError)) {
				conflicts = append(conflicts, name)
				conflictErrs = append(conflictErrs, fmt.Errorf("toolset %q: %w", name, err))
				continue
			}
			if err != nil {
				return fmt.Errorf("claim tool names of toolset %q: %w", name, err)
			}
		}
		if len(conflicts) == 0 {
			return nil
		}
		if len(conflicts) == len(pending) {
			return errors.Join(conflictErrs...)
		}
		pending = conflicts
	}
}

// claimSavedRecordToolNames rewrites one record's claims from its current
// saved state with a conditional commit, re-reading when the record changes
// concurrently. A record removed since it was listed needs no claims.
func (c *toolsetCatalog) claimSavedRecordToolNames(ctx context.Context, name string) error {
	key := toolsetCatalogKey(name)
	for {
		raw, definitionRaw, tokenRetired, exists, err := c.store.Snapshot(ctx, key)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		current, err := c.decodeSnapshot(ctx, name, raw, definitionRaw, tokenRetired)
		if err != nil {
			return err
		}
		write := catalogWrite{}
		if current.State == catalogEntryActive {
			write.ClaimToolNames = true
			write.ToolNames = current.Toolset.toolNames()
		}
		updated, err := c.commit(ctx, key, raw, current.catalogState, write)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
}

// Register admits an already validated definition and one provider incarnation.
// Existing leases block a different admission; replacement remembers the old
// token permanently in the same write that publishes the new definition.
func (c *toolsetCatalog) Register(ctx context.Context, definition *catalogToolset, admissionRevision, providerID, incarnationID string, leaseDuration time.Duration) (catalogState, error) {
	if err := validateProviderLeaseDuration(leaseDuration); err != nil {
		return catalogState{}, err
	}
	token, err := admissionRegistrationToken(definition.fingerprint, admissionRevision, toolregistry.WireProtocolVersion)
	if err != nil {
		return catalogState{}, err
	}
	retired, err := c.store.Retired(ctx, token)
	if err != nil {
		return catalogState{}, err
	}
	if retired {
		return catalogState{}, errAdmissionRetired
	}
	name := definition.info.Name
	key := toolsetCatalogKey(name)
	leaseKey := providerLeaseKey(providerID, incarnationID)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return catalogState{}, err
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return catalogState{}, err
		}
		if now.UnixMilli() > math.MaxInt64-leaseDuration.Milliseconds() {
			return catalogState{}, fmt.Errorf("provider lease deadline overflows Unix milliseconds")
		}
		lease := providerLease{ExpiresAtUnixMilli: now.Add(leaseDuration).UnixMilli()}
		var existing catalogState
		if exists {
			existing, err = parseCatalogState(name, raw)
			if err != nil {
				return catalogState{}, err
			}
			if err := requireCatalogIdentity(existing.Identity, definition.identity); err != nil {
				return catalogState{}, err
			}
			if existing.NativeAgent {
				return catalogState{}, fmt.Errorf("%w: %q is a native Agent toolset", errAdmissionBlocked, name)
			}
			// Inspect draining before pruning: full registration must never
			// reopen an incarnation whose accepted work is settling.
			if existing.RegistrationToken == token {
				if existing.State == catalogEntryRetired {
					return catalogState{}, errAdmissionRetired
				}
				if previous, ok := existing.ProviderLeases[leaseKey]; ok && previous.Draining {
					return catalogState{}, errProviderLeaseLost
				}
			}
			pruned := pruneExpiredProviderLeases(&existing, now)
			if existing.RegistrationToken == token {
				routableUntil := routableProviderDeadline(existing)
				if previous := existing.ProviderLeases[leaseKey]; previous.ExpiresAtUnixMilli > lease.ExpiresAtUnixMilli {
					lease.ExpiresAtUnixMilli = previous.ExpiresAtUnixMilli
				}
				existing.ProviderLeases[leaseKey] = lease
				if routableUntil == 0 {
					existing.HealthEpoch++
					existing.LastPongUnixNano = 0
				}
				updated, err := c.commit(ctx, key, raw, existing, catalogWrite{
					CandidateToken: token, RoutableUntilUnixMilli: routableUntil,
				})
				if err != nil {
					return catalogState{}, err
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
						return catalogState{}, err
					}
					if !updated {
						continue
					}
				}
				return catalogState{}, fmt.Errorf("%w: toolset %q retains %d provider leases", errAdmissionBlocked, name, len(existing.ProviderLeases))
			}
		}
		candidate := newCatalogState(definition, admissionRevision, token, now)
		candidate.ProviderLeases[leaseKey] = lease
		write := catalogWrite{CandidateToken: token, ClaimToolNames: true, ToolNames: definition.toolNames()}
		if !exists || existing.SchemaFingerprint != definition.fingerprint {
			write.Definition = definition.raw
		} else {
			// Fingerprints ignore tag/tool order. Retaining the definition also
			// retains its discovery summary, with the new registration time.
			candidate.Info = copyToolsetInfo(existing.Info)
			candidate.Info.RegisteredAt = candidate.RegisteredAt
		}
		if exists {
			write.RetireToken = existing.RegistrationToken
		}
		updated, err := c.commit(ctx, key, raw, candidate, write)
		if err != nil {
			return catalogState{}, err
		}
		if updated {
			return candidate, nil
		}
	}
}

// RenewProvider extends only the exact existing lease. Draining status and a
// longer settlement deadline survive an in-flight renewal during shutdown.
func (c *toolsetCatalog) RenewProvider(ctx context.Context, name, providerID, incarnationID, token string, duration time.Duration) error {
	if err := validateProviderLeaseDuration(duration); err != nil {
		return err
	}
	key := toolsetCatalogKey(name)
	leaseKey := providerLeaseKey(providerID, incarnationID)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: catalog state is absent", errProviderLeaseLost)
		}
		entry, err := parseCatalogState(name, raw)
		if err != nil {
			return err
		}
		if entry.State != catalogEntryActive {
			return fmt.Errorf("%w: admission is retired", errProviderLeaseLost)
		}
		if entry.RegistrationToken != token {
			return fmt.Errorf("%w: admission was replaced", errProviderLeaseLost)
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return err
		}
		lease, exists := entry.ProviderLeases[leaseKey]
		if !exists {
			return fmt.Errorf("%w: provider incarnation is absent", errProviderLeaseLost)
		}
		if lease.ExpiresAtUnixMilli <= now.UnixMilli() {
			return fmt.Errorf("%w: provider lease expired", errProviderLeaseLost)
		}
		if now.UnixMilli() > math.MaxInt64-duration.Milliseconds() {
			return fmt.Errorf("provider lease deadline overflows Unix milliseconds")
		}
		lease.ExpiresAtUnixMilli = max(lease.ExpiresAtUnixMilli, now.Add(duration).UnixMilli())
		entry.ProviderLeases[leaseKey] = lease
		updated, err := c.commit(ctx, key, raw, entry, catalogWrite{LiveLease: leaseKey})
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
}

func validateProviderLeaseDuration(duration time.Duration) error {
	if duration < toolregistry.MinProviderLeaseDuration || duration > toolregistry.MaxProviderLeaseDuration {
		return fmt.Errorf("provider lease duration must be between %s and %s", toolregistry.MinProviderLeaseDuration, toolregistry.MaxProviderLeaseDuration)
	}
	return nil
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
		entry, err := parseCatalogState(name, raw)
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
		updated, err := c.commit(ctx, key, raw, entry, catalogWrite{LiveLease: leaseKey})
		if errors.Is(err, errProviderLeaseLost) {
			return nil
		}
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
		entry, err := parseCatalogState(name, raw)
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
		entry, err := parseCatalogState(name, raw)
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
		write := catalogWrite{}
		if !entry.NativeAgent {
			write.RetireToken = entry.RegistrationToken
		}
		updated, err := c.commit(ctx, key, raw, entry, write)
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
	return entry.Toolset.decode(entry.RegisteredAt)
}

// ActiveRegistration returns one coherent definition and active admission.
func (c *toolsetCatalog) ActiveRegistration(ctx context.Context, name string) (catalogEntry, error) {
	entry, err := c.snapshot(ctx, name)
	if err != nil {
		return catalogEntry{}, err
	}
	if entry.State != catalogEntryActive {
		return catalogEntry{}, errToolsetNotFound
	}
	return entry, nil
}

// activeState supplies only the compact facts needed for provider ownership.
func (c *toolsetCatalog) activeState(ctx context.Context, name string) (catalogState, error) {
	raw, exists, err := c.exactRaw(ctx, toolsetCatalogKey(name))
	if err != nil {
		return catalogState{}, err
	}
	if !exists {
		return catalogState{}, errToolsetNotFound
	}
	entry, err := parseCatalogState(name, raw)
	if err != nil {
		return catalogState{}, err
	}
	if entry.State != catalogEntryActive {
		return catalogState{}, errToolsetNotFound
	}
	return entry, nil
}

// RegistrationToken returns the current active admission token.
func (c *toolsetCatalog) RegistrationToken(ctx context.Context, name string) (string, error) {
	entry, err := c.activeState(ctx, name)
	if err != nil {
		return "", err
	}
	return entry.RegistrationToken, nil
}

// VerifyActiveToken rechecks routing ownership immediately before publication.
func (c *toolsetCatalog) VerifyActiveToken(ctx context.Context, name, token string) error {
	entry, err := c.activeState(ctx, name)
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
	entry, err := c.activeState(ctx, name)
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
	entry, err := c.activeState(ctx, name)
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
		entry, err := parseCatalogState(name, raw)
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
			raw, err = marshalCatalogState(entry)
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
func (c *toolsetCatalog) healthEntry(ctx context.Context, name string) (catalogState, time.Time, error) {
	key := toolsetCatalogKey(name)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return catalogState{}, time.Time{}, err
		}
		if !exists {
			return catalogState{}, time.Time{}, errToolsetNotFound
		}
		entry, err := parseCatalogState(name, raw)
		if err != nil {
			return catalogState{}, time.Time{}, err
		}
		if entry.State != catalogEntryActive || entry.NativeAgent {
			return catalogState{}, time.Time{}, errToolsetNotFound
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return catalogState{}, time.Time{}, err
		}
		if !pruneExpiredProviderLeases(&entry, now) {
			return entry, now, nil
		}
		updated, err := c.replace(ctx, key, raw, entry)
		if err != nil {
			return catalogState{}, time.Time{}, err
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
	keys, err := c.store.Keys(ctx)
	if err != nil {
		return nil, err
	}
	c.pruneDefinitions(keys)
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
		entry, err := parseCatalogState(strings.TrimPrefix(key, toolsetCatalogKeyPrefix), raw)
		if err != nil {
			return nil, err
		}
		if entry.State == catalogEntryActive && catalogMatchesTags(entry.Info.Tags, tags) {
			toolsets = append(toolsets, copyToolsetInfo(entry.Info))
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
	keys, err := c.store.Keys(ctx)
	if err != nil {
		return nil, err
	}
	c.pruneDefinitions(keys)
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
		entry, err := parseCatalogState(strings.TrimPrefix(key, toolsetCatalogKeyPrefix), raw)
		if err != nil {
			return nil, err
		}
		if entry.State == catalogEntryActive && catalogMatchesQuery(entry.Info, lowerQuery) {
			toolsets = append(toolsets, copyToolsetInfo(entry.Info))
		}
	}
	return toolsets, nil
}

// replace updates compact state without retransmitting definitions or history.
func (c *toolsetCatalog) replace(ctx context.Context, key, previous string, state catalogState) (bool, error) {
	return c.commit(ctx, key, previous, state, catalogWrite{})
}

// commit serializes state once and keeps registration's definition/retirement
// changes in the same conditional write as its provider admission.
func (c *toolsetCatalog) commit(ctx context.Context, key, previous string, state catalogState, write catalogWrite) (bool, error) {
	next, err := marshalCatalogState(state)
	if err != nil {
		return false, err
	}
	write.State = next
	if state.Identity != nil {
		write.Scope = state.Identity.Scope
		write.Indexed = state.State == catalogEntryActive
	}
	updated, err := c.store.Commit(ctx, key, previous, write)
	var taken *toolNameTakenError
	if errors.As(err, &taken) {
		return false, c.toolNameConflict(ctx, taken)
	}
	if err != nil {
		return false, fmt.Errorf("replace catalog key %q: %w", key, err)
	}
	return updated, nil
}

// toolNameConflict reads the saved identity of the toolset that holds the
// rejected tool name and returns a *toolNameConflictError naming it. Only
// scoped records hold names, so a missing record or identity means the saved
// catalog is inconsistent and is returned as a storage error.
func (c *toolsetCatalog) toolNameConflict(ctx context.Context, taken *toolNameTakenError) error {
	raw, exists, err := c.exactRaw(ctx, toolsetCatalogKey(taken.OwnerRoute))
	if err != nil {
		return fmt.Errorf("read toolset holding tool %q: %w", taken.Tool, err)
	}
	if !exists {
		return fmt.Errorf("tool %q is held by missing toolset %q", taken.Tool, taken.OwnerRoute)
	}
	owner, err := parseCatalogState(taken.OwnerRoute, raw)
	if err != nil {
		return fmt.Errorf("read toolset holding tool %q: %w", taken.Tool, err)
	}
	if owner.Identity == nil {
		return fmt.Errorf("tool %q is held by toolset %q without a catalog identity", taken.Tool, taken.OwnerRoute)
	}
	return &toolNameConflictError{Tool: taken.Tool, Owner: owner.Identity.Name}
}

func (e *toolNameConflictError) Error() string {
	return fmt.Sprintf("tool %q is already provided by toolset %q in the same catalog scope", e.Tool, e.Owner)
}

// exactRaw reads compact state and identifies the catalog key on storage errors.
func (c *toolsetCatalog) exactRaw(ctx context.Context, key string) (string, bool, error) {
	raw, exists, err := c.store.Read(ctx, key)
	if err != nil {
		return "", false, fmt.Errorf("read catalog key %q: %w", key, err)
	}
	if !exists {
		c.forgetDefinition(strings.TrimPrefix(key, toolsetCatalogKeyPrefix))
	}
	return raw, exists, nil
}

// newCatalogState derives the small discovery summary once per admission.
func newCatalogState(definition *catalogToolset, revision, token string, now time.Time) catalogState {
	registeredAt := now.UTC().Format(time.RFC3339Nano)
	info := copyToolsetInfo(definition.info)
	info.RegisteredAt = registeredAt
	return catalogState{
		Identity:            definition.identity,
		State:               catalogEntryActive,
		Info:                info,
		SchemaFingerprint:   definition.fingerprint,
		AdmissionRevision:   revision,
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		RegistrationToken:   token,
		RegisteredAt:        registeredAt,
		ProviderLeases:      make(map[string]providerLease),
		HealthEpoch:         1,
	}
}

// pruneExpiredProviderLeases removes leases at or before Redis TIME and fences
// pongs from the prior non-empty membership epoch when the last lease expires.
func pruneExpiredProviderLeases(entry *catalogState, now time.Time) bool {
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
func nonDrainingProviderCount(entry catalogState) int {
	count := 0
	for _, lease := range entry.ProviderLeases {
		if !lease.Draining {
			count++
		}
	}
	return count
}

// routableProviderDeadline returns the last expiry among non-draining leases
// after pruning. A join can preserve health only if one survives until commit.
// Compute this before adding or extending the joining provider's lease.
func routableProviderDeadline(entry catalogState) int64 {
	var deadline int64
	for _, lease := range entry.ProviderLeases {
		if !lease.Draining {
			deadline = max(deadline, lease.ExpiresAtUnixMilli)
		}
	}
	return deadline
}

// routableProviderCount returns unexpired non-draining leases at now.
func routableProviderCount(entry catalogState, now time.Time) int {
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
