// Package registry saves the generated operation declarations for one immutable
// native revision. Admission checks the exact bytes later readers will load. A
// new committed registration retains that preparation; repeats keep the existing
// cache entry. The host authorizes requests and validates dependencies first.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// RegisterAgentExports retains one accepted revision and its operation schemas.
// Identical delivery survives stable resource retirement, while
// changed content and an independently retired exact registration conflict.
func (s *Service) RegisterAgentExports(ctx context.Context, p *genregistry.AgentExportsDeclaration) (*genregistry.ResolvedToolset, error) {
	if _, err := genregistry.EncodeAgentExportsDeclaration(p); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	body, err := json.Marshal(&genregistry.Toolset{Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Exports})
	if err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	raw := string(body)
	compiled, err := s.catalog.prepareDefinition(p.Name, raw, true)
	if err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	compiled.digest = catalogDefinitionDigest(raw)
	definition := &catalogToolset{
		catalogDefinition: compiled,
		identity:          &CatalogIdentity{Scope: p.Scope, Name: p.ResourceName},
		raw:               raw,
	}
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
			result = resolvedToolsetRegistration(definition, state)
			return nil
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
		result = resolvedToolsetRegistration(definition, state)
		return nil
	})
	return result, err
}
