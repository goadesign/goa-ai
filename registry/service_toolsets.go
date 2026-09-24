// Package registry separates immutable service declarations from provider
// membership. Declaration creates no provider leases, streams, or pings. The
// catalog scheduler may observe it as unavailable. Attachment grants a lease
// only while the selected registration remains current.
package registry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

// DeclareServiceToolset saves a complete service declaration independently of
// provider availability. An identical retry returns the saved winner unchanged.
func (s *Service) DeclareServiceToolset(ctx context.Context, p *genregistry.ServiceToolsetDeclaration) (*genregistry.ResolvedToolset, error) {
	compiled, err := registrycontract.Compile(p.Tools)
	if err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	for _, spec := range compiled {
		if spec.IsAgentTool {
			return nil, genregistry.MakeValidationError(fmt.Errorf("tool %q is not a service tool", spec.Name))
		}
	}
	toolset := &genregistry.Toolset{
		Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Tools,
	}
	if err := s.validator.ValidateToolSchemas(toolset.Tools); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	fingerprint, err := toolcontract.Fingerprint(toolset)
	if err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	definition, err := newCatalogToolset(toolset, fingerprint, s.validator)
	if err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	entry, err := s.catalog.DeclareService(ctx, definition)
	if err != nil {
		switch {
		case errors.Is(err, errAdmissionConflict):
			return nil, genregistry.MakeAdmissionConflict(err)
		case errors.Is(err, errAdmissionRetired):
			return nil, genregistry.MakeAdmissionRetired(err)
		default:
			return nil, genregistry.MakeServiceUnavailable(err)
		}
	}
	saved, err := entry.Toolset.decode(entry.RegisteredAt)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	return &genregistry.ResolvedToolset{Toolset: saved, RegistrationToken: entry.RegistrationToken}, nil
}

// AttachProvider starts membership for one exact service registration. It never
// creates or replaces a declaration, even when every old provider has left.
func (s *Service) AttachProvider(ctx context.Context, p *genregistry.AttachProviderPayload) (*genregistry.RegisterResult, error) {
	if err := toolregistry.ValidateWireProtocolVersion(p.WireProtocolVersion); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	if _, _, err := s.streamManager.GetOrCreateStream(ctx, p.Name); err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	state, err := s.catalog.AttachProvider(ctx, p.Name, p.ExpectedRegistrationToken, p.ProviderID, p.ProviderIncarnationID, s.providerLeaseDuration)
	if err != nil {
		switch {
		case errors.Is(err, errAdmissionConflict):
			return nil, genregistry.MakeAdmissionConflict(err)
		case errors.Is(err, errAdmissionRetired):
			return nil, genregistry.MakeAdmissionRetired(err)
		case errors.Is(err, errProviderLeaseLost):
			return nil, genregistry.MakeProviderLeaseLost(err)
		default:
			return nil, genregistry.MakeServiceUnavailable(err)
		}
	}
	if err := s.healthTracker.EnsurePingLoop(ctx, p.Name); err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	return &genregistry.RegisterResult{
		RegisteredAt: state.RegisteredAt, RegistrationToken: state.RegistrationToken,
		LeaseDurationMs: s.providerLeaseDuration.Milliseconds(),
	}, nil
}

// DeclareService creates one admission with a registry-issued revision and no
// provider leases. Reading the saved definition on retry preserves its original
// tool and tag order even when an equivalent request used a different order.
func (c *toolsetCatalog) DeclareService(ctx context.Context, definition *catalogToolset) (catalogEntry, error) {
	name := definition.info.Name
	for {
		current, err := c.snapshot(ctx, name)
		if err == nil {
			if current.NativeAgent {
				return catalogEntry{}, errAdmissionConflict
			}
			if current.State == catalogEntryRetired {
				return catalogEntry{}, errAdmissionRetired
			}
			if current.SchemaFingerprint != definition.fingerprint {
				return catalogEntry{}, errAdmissionConflict
			}
			return current, nil
		}
		if !errors.Is(err, errToolsetNotFound) {
			return catalogEntry{}, err
		}
		revision := uuid.NewString()
		token, err := admissionRegistrationToken(definition.fingerprint, revision, toolregistry.WireProtocolVersion)
		if err != nil {
			return catalogEntry{}, err
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return catalogEntry{}, err
		}
		state := newCatalogState(definition, revision, token, now)
		updated, err := c.commit(ctx, toolsetCatalogKey(name), "", state, catalogWrite{
			Definition: string(definition.raw), CandidateToken: token,
		})
		if err != nil {
			return catalogEntry{}, err
		}
		if updated {
			return catalogEntry{catalogState: state, Toolset: definition}, nil
		}
	}
}

// AttachProvider changes only leases and health under the selected token. The
// conditional write makes attachment and replacement mutually exclusive.
func (c *toolsetCatalog) AttachProvider(ctx context.Context, name, token, providerID, incarnationID string, duration time.Duration) (catalogState, error) {
	if err := validateProviderLeaseDuration(duration); err != nil {
		return catalogState{}, err
	}
	key := toolsetCatalogKey(name)
	leaseKey := providerLeaseKey(providerID, incarnationID)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return catalogState{}, err
		}
		// Read retirement after current state on every attempt. A replacement
		// that defeated the previous write has permanently retired this token.
		retired, err := c.store.Retired(ctx, token)
		if err != nil {
			return catalogState{}, err
		}
		if retired {
			return catalogState{}, errAdmissionRetired
		}
		if !exists {
			return catalogState{}, errAdmissionConflict
		}
		state, err := parseCatalogState(name, raw)
		if err != nil {
			return catalogState{}, err
		}
		if state.NativeAgent || state.RegistrationToken != token {
			return catalogState{}, errAdmissionConflict
		}
		if state.State == catalogEntryRetired {
			return catalogState{}, errAdmissionRetired
		}
		if previous := state.ProviderLeases[leaseKey]; previous.Draining {
			return catalogState{}, errProviderLeaseLost
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return catalogState{}, err
		}
		if now.UnixMilli() > math.MaxInt64-duration.Milliseconds() {
			return catalogState{}, fmt.Errorf("provider lease deadline overflows Unix milliseconds")
		}
		pruneExpiredProviderLeases(&state, now)
		routableUntil := routableProviderDeadline(state)
		deadline := max(state.ProviderLeases[leaseKey].ExpiresAtUnixMilli, now.Add(duration).UnixMilli())
		state.ProviderLeases[leaseKey] = providerLease{ExpiresAtUnixMilli: deadline}
		if routableUntil == 0 {
			state.HealthEpoch++
			state.LastPongUnixNano = 0
		}
		updated, err := c.commit(ctx, key, raw, state, catalogWrite{
			CandidateToken: token, RoutableUntilUnixMilli: routableUntil,
		})
		if err != nil {
			return catalogState{}, err
		}
		if updated {
			return state, nil
		}
	}
}
