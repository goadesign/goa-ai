// Package registry saves the generated operation declarations for one immutable
// native revision. Admission validates typed declarations before saving them. A
// new committed registration retains that preparation; repeats keep the existing
// cache entry. The host authorizes requests and validates dependencies first.
package registry

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// RegisterAgentExports retains one accepted revision and its operation schemas.
// Identical delivery survives stable resource retirement, while
// changed content and an independently retired exact registration conflict.
func (s *Service) RegisterAgentExports(ctx context.Context, p *genregistry.AgentExportsDeclaration) (*genregistry.ResolvedToolset, error) {
	if err := genregistry.ValidateAgentExportsDeclarationValue(p); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	definition, err := s.prepareAgentToolset(&genregistry.Toolset{
		Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Exports,
	}, &CatalogIdentity{Scope: p.Scope, Name: p.ResourceName})
	if err != nil {
		return nil, err
	}
	definition.digest = catalogDefinitionDigest(definition.raw)
	definition.nativeAgent = true
	var result *genregistry.ResolvedToolset
	err = s.updateCatalog(ctx, "register_exports", func(tx *catalogTransaction) error {
		raw, err := tx.tx.HGet(ctx, tx.store.state, toolsetCatalogKey(p.Name)).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if err == nil {
			state, err := parseCatalogState(p.Name, raw)
			if err != nil {
				return err
			}
			if !state.ImmutableNative || state.State != catalogEntryActive {
				return errAdmissionConflict
			}
			if err := checkAgentRegistration(state, definition.identity, nativeAgentToken(definition.fingerprint), ""); err != nil {
				return err
			}
			result, err = resolvedToolsetRegistration(definition, state)
			return err
		}
		retired, err := tx.value("retired:" + resourceKey(&genregistry.CatalogResource{Kind: "agent", Scope: p.Scope, Name: p.ResourceName}))
		if err != nil {
			return err
		}
		if retired != "" {
			return errAdmissionRetired
		}
		now, err := s.catalog.clock.Now(ctx)
		if err != nil {
			return err
		}
		state := catalogState{Identity: definition.identity, NativeAgent: true, ImmutableNative: true, State: catalogEntryActive, Info: copyToolsetInfo(definition.info), SchemaFingerprint: definition.fingerprint, RegistrationToken: nativeAgentToken(definition.fingerprint), RegisteredAt: now.Format(time.RFC3339Nano)}
		state.Info.RegisteredAt = state.RegisteredAt
		tx.registration = &catalogRegistration{state: state, definition: definition}
		result, err = resolvedToolsetRegistration(definition, state)
		return err
	})
	return result, err
}
