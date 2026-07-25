// Package registry owns the toolset admission catalog used by the gateway.
//
// One rmap value atomically owns admission identity, active/retired state,
// provider leases, and discovery metadata. Every transition uses Redis TIME and
// exact CAS so registry replicas cannot split admission ownership.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
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
		Subscribe() <-chan rmap.EventKind
		Unsubscribe(<-chan rmap.EventKind)
		SetIfNotExists(ctx context.Context, key, value string) (bool, error)
		TestAndSetEx(ctx context.Context, key, test, value string) (prev string, existed bool, updated bool, err error)
	}

	// catalogEntry is the single CAS-owned admission record for one toolset.
	catalogEntry struct {
		State             catalogEntryState    `json:"state"`
		Toolset           *genregistry.Toolset `json:"toolset"`
		SchemaFingerprint string               `json:"schema_fingerprint"`
		AdmissionRevision string               `json:"admission_revision"`
		RegistrationToken string               `json:"registration_token"`
		RegisteredAt      string               `json:"registered_at"`
		ProviderLeases    map[string]int64     `json:"provider_leases"`
		RetiredTokens     map[string]struct{}  `json:"retired_registration_tokens"`
		HealthEpoch       uint64               `json:"health_epoch"`
		LastPongUnixNano  int64                `json:"last_pong_unix_nano"`
	}

	// providerLeaseRecord projects one provider lease for health derivation.
	providerLeaseRecord struct {
		ProviderID           string
		IncarnationID        string
		RegistrationToken    string
		LeaseExpiresUnixNano int64
	}

	// toolsetCatalog serializes every admission transition through one rmap key.
	toolsetCatalog struct {
		m     catalogMap
		clock registryTimeSource
	}

	catalogEntryState string
)

const (
	toolsetCatalogKeyPrefix = "registry:toolset:"

	catalogEntryActive  catalogEntryState = "active"
	catalogEntryRetired catalogEntryState = "retired"

	// #nosec G101 -- this public protocol domain separator is not a credential.
	registrationTokenDomain = "goa-ai/tool-registry-admission/v1\x00"
)

var (
	errAdmissionBlocked  = errors.New("toolset admission blocked")
	errAdmissionRetired  = errors.New("toolset admission retired")
	errAdmissionConflict = errors.New("toolset admission conflict")
	errToolsetNotFound   = errors.New("toolset not found")
)

// newToolsetCatalog constructs the canonical admission store.
func newToolsetCatalog(m catalogMap, clock registryTimeSource) *toolsetCatalog {
	return &toolsetCatalog{m: m, clock: clock}
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
		return catalogEntry{}, fmt.Errorf("fingerprint toolset %q schema: %w", toolset.Name, err)
	}
	token, err := admissionRegistrationToken(fingerprint, admissionRevision)
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
		if now.UnixNano() > math.MaxInt64-int64(leaseDuration) {
			return catalogEntry{}, fmt.Errorf("provider lease deadline overflows Unix nanoseconds")
		}
		expiresAt := now.Add(leaseDuration).UnixNano()
		if !exists {
			candidate := newCatalogEntry(
				toolset,
				fingerprint,
				admissionRevision,
				token,
				providerLeaseKey(providerID, incarnationID),
				expiresAt,
				now,
				make(map[string]struct{}),
			)
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

		existing, err := parseCatalogEntry(toolset.Name, raw)
		if err != nil {
			return catalogEntry{}, err
		}
		pruned := pruneExpiredProviderLeases(&existing, now)
		if _, retired := existing.RetiredTokens[token]; retired {
			return catalogEntry{}, fmt.Errorf("%w: %q token %s", errAdmissionRetired, toolset.Name, token)
		}
		if existing.RegistrationToken == token {
			existing.ProviderLeases[providerLeaseKey(providerID, incarnationID)] = expiresAt
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
		candidate := newCatalogEntry(
			toolset,
			fingerprint,
			admissionRevision,
			token,
			providerLeaseKey(providerID, incarnationID),
			expiresAt,
			now,
			retiredTokens,
		)
		updated, err := c.replace(ctx, key, raw, candidate)
		if err != nil {
			return catalogEntry{}, err
		}
		if updated {
			return candidate, nil
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
		entry, err := parseCatalogEntry(name, raw)
		if err != nil {
			return err
		}
		if entry.RegistrationToken != expectedToken {
			return nil
		}
		leaseKey := providerLeaseKey(providerID, incarnationID)
		if _, exists := entry.ProviderLeases[leaseKey]; !exists {
			return nil
		}
		delete(entry.ProviderLeases, leaseKey)
		if len(entry.ProviderLeases) == 0 {
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
		entry, err := parseCatalogEntry(name, raw)
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
	return entry.Toolset, nil
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
	entry, err := parseCatalogEntry(name, raw)
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
	expiresAt, exists := entry.ProviderLeases[providerLeaseKey(providerID, incarnationID)]
	return exists && expiresAt > now.UnixNano(), now, nil
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
	for leaseKey, expiresAt := range entry.ProviderLeases {
		providerID, incarnationID, err := parseProviderLeaseKey(leaseKey)
		if err != nil {
			return nil, err
		}
		leases = append(leases, providerLeaseRecord{
			ProviderID:           providerID,
			IncarnationID:        incarnationID,
			RegistrationToken:    token,
			LeaseExpiresUnixNano: expiresAt,
		})
	}
	return leases, nil
}

// HealthIdentity returns the exact token and membership epoch a ping must carry.
func (c *toolsetCatalog) HealthIdentity(ctx context.Context, name string) (string, uint64, error) {
	entry, _, err := c.healthEntry(ctx, name)
	if err != nil {
		return "", 0, err
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
		entry, err := parseCatalogEntry(name, raw)
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
		expiresAt, admitted := entry.ProviderLeases[providerLeaseKey(providerID, incarnationID)]
		if !admitted || expiresAt <= now.UnixNano() {
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
		entry, err := parseCatalogEntry(name, raw)
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
func (c *toolsetCatalog) ListToolsets(ctx context.Context, tags []string) ([]*genregistry.Toolset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	toolsets := make([]*genregistry.Toolset, 0, len(c.m.Keys()))
	for _, key := range c.m.Keys() {
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
		entry, err := parseCatalogEntry(strings.TrimPrefix(key, toolsetCatalogKeyPrefix), raw)
		if err != nil {
			return nil, err
		}
		if entry.State == catalogEntryActive && catalogMatchesTags(entry.Toolset.Tags, tags) {
			toolsets = append(toolsets, entry.Toolset)
		}
	}
	return toolsets, nil
}

// SearchToolsets returns active entries matching name, description, or tags.
func (c *toolsetCatalog) SearchToolsets(ctx context.Context, query string) ([]*genregistry.Toolset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lowerQuery := strings.ToLower(query)
	toolsets := make([]*genregistry.Toolset, 0, len(c.m.Keys()))
	for _, key := range c.m.Keys() {
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
		entry, err := parseCatalogEntry(strings.TrimPrefix(key, toolsetCatalogKeyPrefix), raw)
		if err != nil {
			return nil, err
		}
		if entry.State == catalogEntryActive && catalogMatchesQuery(entry.Toolset, lowerQuery) {
			toolsets = append(toolsets, entry.Toolset)
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

// exactRaw reads authoritative Redis state through a no-op rmap CAS.
func (c *toolsetCatalog) exactRaw(ctx context.Context, key string) (string, bool, error) {
	raw, exists, _, err := c.m.TestAndSetEx(ctx, key, "", "")
	if err != nil {
		return "", false, fmt.Errorf("read catalog key %q: %w", key, err)
	}
	return raw, exists, nil
}

// newCatalogEntry builds a fresh active admission from Redis time.
func newCatalogEntry(
	toolset *genregistry.Toolset,
	fingerprint, revision, token, leaseKey string,
	expiresAt int64,
	now time.Time,
	retiredTokens map[string]struct{},
) catalogEntry {
	registeredAt := now.UTC().Format(time.RFC3339Nano)
	toolset.RegisteredAt = registeredAt
	return catalogEntry{
		State:             catalogEntryActive,
		Toolset:           toolset,
		SchemaFingerprint: fingerprint,
		AdmissionRevision: revision,
		RegistrationToken: token,
		RegisteredAt:      registeredAt,
		ProviderLeases:    map[string]int64{leaseKey: expiresAt},
		RetiredTokens:     retiredTokens,
		HealthEpoch:       1,
	}
}

// pruneExpiredProviderLeases removes leases at or before Redis TIME and fences
// pongs from the prior non-empty membership epoch when the last lease expires.
func pruneExpiredProviderLeases(entry *catalogEntry, now time.Time) bool {
	changed := false
	hadLeases := len(entry.ProviderLeases) > 0
	for leaseKey, expiresAt := range entry.ProviderLeases {
		if expiresAt <= now.UnixNano() {
			delete(entry.ProviderLeases, leaseKey)
			changed = true
		}
	}
	if hadLeases && len(entry.ProviderLeases) == 0 {
		entry.HealthEpoch++
		entry.LastPongUnixNano = 0
	}
	return changed
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

// marshalCatalogEntry encodes the one persisted admission record.
func marshalCatalogEntry(entry catalogEntry) (string, error) {
	body, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal toolset %q admission: %w", entry.Toolset.Name, err)
	}
	return string(body), nil
}

// parseCatalogEntry validates persisted admission identity and lease state.
func parseCatalogEntry(name, body string) (catalogEntry, error) {
	var entry catalogEntry
	if err := json.Unmarshal([]byte(body), &entry); err != nil {
		return catalogEntry{}, fmt.Errorf("unmarshal toolset %q: %w", name, err)
	}
	if entry.Toolset == nil || entry.Toolset.Name != name {
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid toolset payload", name)
	}
	if entry.RegisteredAt == "" || entry.Toolset.RegisteredAt != entry.RegisteredAt {
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid registered_at", name)
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.RegisteredAt); err != nil {
		return catalogEntry{}, fmt.Errorf("toolset %q invalid registered_at: %w", name, err)
	}
	if err := toolregistry.ValidateAdmissionRevision(entry.AdmissionRevision); err != nil {
		return catalogEntry{}, fmt.Errorf("toolset %q invalid admission revision: %w", name, err)
	}
	fingerprint, err := toolsetSchemaFingerprint(entry.Toolset)
	if err != nil {
		return catalogEntry{}, fmt.Errorf("fingerprint toolset %q schema: %w", name, err)
	}
	if entry.SchemaFingerprint != fingerprint {
		return catalogEntry{}, fmt.Errorf("toolset %q schema fingerprint does not match canonical schema", name)
	}
	token, err := admissionRegistrationToken(fingerprint, entry.AdmissionRevision)
	if err != nil {
		return catalogEntry{}, fmt.Errorf("derive toolset %q admission token: %w", name, err)
	}
	if entry.RegistrationToken != token {
		return catalogEntry{}, fmt.Errorf("toolset %q registration token does not match admission identity", name)
	}
	switch entry.State {
	case catalogEntryActive, catalogEntryRetired:
	default:
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid catalog state %q", name, entry.State)
	}
	if entry.ProviderLeases == nil {
		return catalogEntry{}, fmt.Errorf("toolset %q missing provider lease map", name)
	}
	if entry.HealthEpoch == 0 {
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid health epoch", name)
	}
	if entry.LastPongUnixNano < 0 {
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid last pong timestamp", name)
	}
	for leaseKey, expiresAt := range entry.ProviderLeases {
		if _, _, err := parseProviderLeaseKey(leaseKey); err != nil {
			return catalogEntry{}, fmt.Errorf("toolset %q has invalid provider lease key: %w", name, err)
		}
		if expiresAt <= 0 {
			return catalogEntry{}, fmt.Errorf("toolset %q has invalid provider lease deadline", name)
		}
	}
	if entry.RetiredTokens == nil {
		return catalogEntry{}, fmt.Errorf("toolset %q missing retired registration token set", name)
	}
	for retiredToken := range entry.RetiredTokens {
		if err := toolregistry.ValidateRegistrationToken(retiredToken); err != nil {
			return catalogEntry{}, fmt.Errorf("toolset %q invalid retired registration token: %w", name, err)
		}
	}
	if entry.State == catalogEntryActive {
		if _, retired := entry.RetiredTokens[entry.RegistrationToken]; retired {
			return catalogEntry{}, fmt.Errorf("toolset %q active token is retired", name)
		}
	} else {
		if _, retired := entry.RetiredTokens[entry.RegistrationToken]; !retired {
			return catalogEntry{}, fmt.Errorf("toolset %q retired token is missing its tombstone", name)
		}
	}
	return entry, nil
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
	type fingerprintTool struct {
		Name          string   `json:"name"`
		Description   *string  `json:"description,omitempty"`
		Tags          []string `json:"tags,omitempty"`
		PayloadSchema string   `json:"payload_schema"`
		ResultSchema  string   `json:"result_schema"`
		SidecarSchema string   `json:"sidecar_schema,omitempty"`
	}
	tools := make([]fingerprintTool, len(toolset.Tools))
	for i, tool := range toolset.Tools {
		tools[i] = fingerprintTool{
			Name:          tool.Name,
			Description:   tool.Description,
			Tags:          sortedStrings(tool.Tags),
			PayloadSchema: string(tool.PayloadSchema),
			ResultSchema:  string(tool.ResultSchema),
			SidecarSchema: string(tool.SidecarSchema),
		}
	}
	sort.SliceStable(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})
	body, err := json.Marshal(struct {
		Name        string              `json:"name"`
		Description *string             `json:"description,omitempty"`
		Version     *genregistry.SemVer `json:"version,omitempty"`
		Tags        []string            `json:"tags,omitempty"`
		Tools       []fingerprintTool   `json:"tools"`
	}{
		Name:        toolset.Name,
		Description: toolset.Description,
		Version:     toolset.Version,
		Tags:        sortedStrings(toolset.Tags),
		Tools:       tools,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// admissionRegistrationToken derives the wire-visible execution fence.
func admissionRegistrationToken(schemaFingerprint, admissionRevision string) (string, error) {
	schemaDigest, err := hex.DecodeString(schemaFingerprint)
	if err != nil {
		return "", fmt.Errorf("decode schema fingerprint: %w", err)
	}
	if len(schemaDigest) != sha256.Size {
		return "", fmt.Errorf("schema fingerprint must contain %d bytes", sha256.Size)
	}
	if err := toolregistry.ValidateAdmissionRevision(admissionRevision); err != nil {
		return "", err
	}
	var revisionLength [4]byte
	// #nosec G115 -- canonical validation bounds the revision to 256 bytes.
	binary.BigEndian.PutUint32(revisionLength[:], uint32(len(admissionRevision)))
	body := make([]byte, 0, len(registrationTokenDomain)+sha256.Size+len(revisionLength)+len(admissionRevision))
	body = append(body, registrationTokenDomain...)
	body = append(body, schemaDigest...)
	body = append(body, revisionLength[:]...)
	body = append(body, admissionRevision...)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// sortedStrings returns a sorted copy for order-free identity fields.
func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return sorted
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

func catalogMatchesQuery(toolset *genregistry.Toolset, query string) bool {
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
