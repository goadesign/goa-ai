// Package registry stores native Agent declarations alongside service tools.
// Native declarations share registry discovery and atomic replacement
// with service tools. They carry no provider leases: the consuming runtime
// executes them as child workflows on explicitly configured Agent workers.
package registry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

// RegisterAgentToolset creates a declaration, or returns its identical active
// registration. It never overwrites a different declaration.
func (s *Service) RegisterAgentToolset(ctx context.Context, p *genregistry.AgentToolsetDeclaration) (*genregistry.ResolvedToolset, error) {
	return s.registerAgentToolset(ctx, &genregistry.Toolset{
		Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Tools,
	}, "")
}

// ReplaceAgentToolset changes the declaration only while the supplied token
// still identifies the current registration.
func (s *Service) ReplaceAgentToolset(ctx context.Context, p *genregistry.ReplaceAgentToolsetPayload) (*genregistry.ResolvedToolset, error) {
	return s.registerAgentToolset(ctx, &genregistry.Toolset{
		Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Tools,
	}, p.ExpectedRegistrationToken)
}

// nativeAgentToken identifies the complete native declaration without implying
// that a Pulse provider holds a lease for it.
func nativeAgentToken(fingerprint string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("agent-toolset\x00"+fingerprint)))
}

// validateAgentTools rejects declarations that cannot finish through the native
// child workflow path. Generated validators own required fields and formats.
func validateAgentTools(declarations []*genregistry.ToolSchema) error {
	for _, declaration := range declarations {
		spec, err := toolcontract.Compile(declaration)
		if err != nil {
			return err
		}
		if !spec.IsAgentTool {
			return fmt.Errorf("tool %q is not a native Agent tool", declaration.Name)
		}
	}
	return nil
}

func (s *Service) registerAgentToolset(ctx context.Context, toolset *genregistry.Toolset, expected string) (*genregistry.ResolvedToolset, error) {
	if err := s.validator.ValidateToolSchemas(toolset.Tools); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	if err := validateAgentTools(toolset.Tools); err != nil {
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
	state, err := s.catalog.RegisterAgent(ctx, definition, expected)
	if err != nil {
		if errors.Is(err, errAdmissionConflict) {
			return nil, genregistry.MakeAdmissionConflict(err)
		}
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	registered, err := definition.decode(state.RegisteredAt)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	return &genregistry.ResolvedToolset{Toolset: registered, RegistrationToken: state.RegistrationToken}, nil
}

// RegisterAgent stores a definition and its discovery state in one conditional
// write. A simultaneous replacement has one winner; accepted calls already own
// their selected definitions and do not reread this record.
func (c *toolsetCatalog) RegisterAgent(ctx context.Context, definition *catalogToolset, expected string) (catalogState, error) {
	name := definition.info.Name
	key := toolsetCatalogKey(name)
	token := nativeAgentToken(definition.fingerprint)
	for {
		raw, exists, err := c.exactRaw(ctx, key)
		if err != nil {
			return catalogState{}, err
		}
		if exists {
			current, err := parseCatalogState(name, raw)
			if err != nil {
				return catalogState{}, err
			}
			if !current.NativeAgent {
				return catalogState{}, fmt.Errorf("%w: %q belongs to a service provider", errAdmissionConflict, name)
			}
			if expected == "" {
				if current.State == catalogEntryActive && current.RegistrationToken == token {
					return current, nil
				}
				return catalogState{}, errAdmissionConflict
			}
			if current.RegistrationToken != expected {
				return catalogState{}, errAdmissionConflict
			}
		} else if expected != "" {
			return catalogState{}, errAdmissionConflict
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return catalogState{}, err
		}
		state := catalogState{
			NativeAgent: true, State: catalogEntryActive,
			Info: copyToolsetInfo(definition.info), SchemaFingerprint: definition.fingerprint,
			RegistrationToken: token, RegisteredAt: now.Format(time.RFC3339Nano),
		}
		state.Info.RegisteredAt = state.RegisteredAt
		updated, err := c.commit(ctx, key, raw, state, catalogWrite{Definition: string(definition.raw)})
		if err != nil {
			return catalogState{}, err
		}
		if updated {
			return state, nil
		}
	}
}
