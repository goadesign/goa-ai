// Package registry saves service declarations separately from provider membership. Initial
// declaration and explicit replacement create no leases or health pings. An
// exact registration accepts providers until it is retired or replaced; a
// replacement cannot commit while any old provider still has a live lease.
package registry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

// DeclareServiceToolset saves a complete service declaration independently of
// provider availability. An identical retry returns the saved winner unchanged.
func (s *Service) DeclareServiceToolset(ctx context.Context, p *genregistry.ServiceToolsetDeclaration) (*genregistry.ResolvedToolset, error) {
	return s.declareServiceToolset(ctx, p, nil)
}

// DeclareServiceToolsetWithIdentity saves application ownership with a service
// declaration. Identity is immutable and does not require an online provider.
func (s *Service) DeclareServiceToolsetWithIdentity(ctx context.Context, identity CatalogIdentity, p *genregistry.ServiceToolsetDeclaration) (*genregistry.ResolvedToolset, error) {
	owned, err := identityInput(identity)
	if err != nil {
		return nil, err
	}
	return s.declareServiceToolset(ctx, p, owned)
}

// ReplaceServiceToolset replaces one exact service registration without
// creating a provider. Live old leases reject the update before any change.
func (s *Service) ReplaceServiceToolset(ctx context.Context, p *genregistry.ReplaceServiceToolsetPayload) (*genregistry.ResolvedToolset, error) {
	return s.replaceServiceToolset(ctx, p, nil)
}

// ReplaceServiceToolsetWithIdentity replaces the selected service declaration
// while preserving its saved application scope and public name.
func (s *Service) ReplaceServiceToolsetWithIdentity(ctx context.Context, identity CatalogIdentity, p *genregistry.ReplaceServiceToolsetPayload) (*genregistry.ResolvedToolset, error) {
	owned, err := identityInput(identity)
	if err != nil {
		return nil, err
	}
	return s.replaceServiceToolset(ctx, p, owned)
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

func (s *Service) declareServiceToolset(ctx context.Context, p *genregistry.ServiceToolsetDeclaration, identity *CatalogIdentity) (*genregistry.ResolvedToolset, error) {
	definition, err := s.prepareServiceToolset(p, identity)
	if err != nil {
		return nil, err
	}
	entry, err := s.catalog.DeclareService(ctx, definition)
	if err != nil {
		return nil, serviceDeclarationError(err)
	}
	return resolvedToolsetRegistration(entry.Toolset, entry.catalogState)
}

func (s *Service) replaceServiceToolset(ctx context.Context, p *genregistry.ReplaceServiceToolsetPayload, identity *CatalogIdentity) (*genregistry.ResolvedToolset, error) {
	definition, err := s.prepareServiceToolset(&genregistry.ServiceToolsetDeclaration{
		Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Tools,
	}, identity)
	if err != nil {
		return nil, err
	}
	entry, err := s.catalog.ReplaceService(ctx, definition, p.ExpectedRegistrationToken, p.ReplacementID)
	if err != nil {
		return nil, serviceDeclarationError(err)
	}
	return resolvedToolsetRegistration(entry.Toolset, entry.catalogState)
}

// prepareServiceToolset applies the same complete schema checks to creation
// and replacement. The returned definition includes immutable owner identity.
func (s *Service) prepareServiceToolset(p *genregistry.ServiceToolsetDeclaration, identity *CatalogIdentity) (*catalogToolset, error) {
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
	definition.identity = identity
	return definition, nil
}

// serviceDeclarationError preserves whether an update was blocked by live
// leases, conflicted with another registration, reused a tool name another
// toolset in the same scope provides, or failed in storage.
func serviceDeclarationError(err error) error {
	switch {
	case errors.Is(err, errAdmissionBlocked):
		return genregistry.MakeAdmissionBlocked(err)
	case errors.Is(err, errAdmissionConflict):
		return genregistry.MakeAdmissionConflict(err)
	case errors.Is(err, errAdmissionRetired):
		return genregistry.MakeAdmissionRetired(err)
	case errors.As(err, new(*toolNameConflictError)):
		return genregistry.MakeToolNameConflict(err)
	default:
		return genregistry.MakeServiceUnavailable(err)
	}
}

// DeclareService creates one admission with a registry-issued revision and no
// provider leases. Reading the saved definition on retry preserves its original
// tool and tag order even when an equivalent request used a different order.
func (c *toolsetCatalog) DeclareService(ctx context.Context, definition *catalogToolset) (catalogEntry, error) {
	name := definition.info.Name
	for {
		current, err := c.snapshot(ctx, name)
		if err == nil {
			if err := requireCatalogIdentity(current.Identity, definition.identity); err != nil {
				return catalogEntry{}, err
			}
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
			Definition: definition.raw, CandidateToken: token,
			ClaimToolNames: true, ToolNames: definition.toolNames(),
		})
		if err != nil {
			return catalogEntry{}, err
		}
		if updated {
			return catalogEntry{catalogState: state, Toolset: definition}, nil
		}
	}
}

// ReplaceService saves a replacement only after old provider authority has
// ended. Its identity binds this request to the expected registration and the
// complete new contract, so a lost reply can return the original saved result.
func (c *toolsetCatalog) ReplaceService(ctx context.Context, definition *catalogToolset, expected, replacementID string) (entry catalogEntry, err error) {
	name := definition.info.Name
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.service.replace",
		trace.WithAttributes(attribute.String("toolregistry.toolset", name)))
	defer finishServiceReplacementSpan(ctx, span, &err)
	key := toolsetCatalogKey(name)
	revision := uuid.NewSHA1(uuid.NameSpaceOID, []byte("service-toolset-replacement\x00"+expected+"\x00"+replacementID)).String()
	token, err := admissionRegistrationToken(definition.fingerprint, revision, toolregistry.WireProtocolVersion)
	if err != nil {
		return catalogEntry{}, err
	}
	for {
		raw, definitionRaw, tokenRetired, exists, err := c.store.Snapshot(ctx, key)
		if err != nil {
			return catalogEntry{}, err
		}
		if !exists {
			return catalogEntry{}, errAdmissionConflict
		}
		current, err := c.decodeSnapshot(ctx, name, raw, definitionRaw, tokenRetired)
		if err != nil {
			return catalogEntry{}, err
		}
		if err := requireCatalogIdentity(current.Identity, definition.identity); err != nil {
			return catalogEntry{}, err
		}
		if current.NativeAgent {
			return catalogEntry{}, errAdmissionConflict
		}
		if current.RegistrationToken == token {
			if current.State == catalogEntryRetired {
				return catalogEntry{}, errAdmissionRetired
			}
			return current, nil
		}
		if current.RegistrationToken != expected {
			return catalogEntry{}, errAdmissionConflict
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return catalogEntry{}, err
		}
		pruneExpiredProviderLeases(&current.catalogState, now)
		if len(current.ProviderLeases) > 0 {
			return catalogEntry{}, errAdmissionBlocked
		}

		// The conditional write races attachment and renewal against the
		// replacement. Only a state with no live old leases can win.
		state := newCatalogState(definition, revision, token, now)
		savedDefinition := definition
		if current.SchemaFingerprint == definition.fingerprint {
			savedDefinition = current.Toolset
			state.Info = copyToolsetInfo(current.Info)
			state.Info.RegisteredAt = state.RegisteredAt
		}
		updated, err := c.commit(ctx, key, raw, state, catalogWrite{
			Definition: savedDefinition.raw, CandidateToken: token,
			RetireToken:    current.RegistrationToken,
			ClaimToolNames: true, ToolNames: savedDefinition.toolNames(),
		})
		if err != nil {
			return catalogEntry{}, err
		}
		if updated {
			return catalogEntry{catalogState: state, Toolset: savedDefinition}, nil
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

// finishServiceReplacementSpan records rejected updates as expected outcomes.
// Storage failures remain failed operations, with their error on the span.
func finishServiceReplacementSpan(ctx context.Context, span trace.Span, err *error) {
	switch {
	case errors.Is(*err, errAdmissionBlocked):
		span.AddEvent("replacement_blocked_by_live_provider")
	case errors.Is(*err, errAdmissionConflict):
		span.AddEvent("replacement_registration_conflict")
	case errors.Is(*err, errAdmissionRetired):
		span.AddEvent("replacement_already_retired")
	default:
		finishCatalogSpan(ctx, span, err)
		return
	}
	span.End()
}
