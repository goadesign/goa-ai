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
	}, "", nil)
}

// RegisterAgentToolsetWithIdentity saves application ownership with a native
// Agent declaration, preserving identical-registration behavior.
func (s *Service) RegisterAgentToolsetWithIdentity(ctx context.Context, identity CatalogIdentity, p *genregistry.AgentToolsetDeclaration) (*genregistry.ResolvedToolset, error) {
	owned, err := identityInput(identity)
	if err != nil {
		return nil, err
	}
	return s.registerAgentToolset(ctx, &genregistry.Toolset{
		Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Tools,
	}, "", owned)
}

// LookupAgentToolsetRegistration compares the complete intended declaration
// and explicit identity with saved registration. An identical active entry
// returns its saved token and time with found=true and no error. An absent
// record returns (nil, false, nil). Invalid input, conflicts and failed reads
// return (nil, false, error); a retired, different, or differently owned entry
// is an admission_conflict.
// Lookup does not reserve the name or change the saved declaration.
func (s *Service) LookupAgentToolsetRegistration(ctx context.Context, identity CatalogIdentity, p *genregistry.AgentToolsetDeclaration) (*genregistry.ResolvedToolset, bool, error) {
	owned, err := identityInput(identity)
	if err != nil {
		return nil, false, err
	}
	definition, err := s.prepareAgentToolset(&genregistry.Toolset{
		Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Tools,
	}, owned)
	if err != nil {
		return nil, false, err
	}
	raw, exists, err := s.catalog.exactRaw(ctx, toolsetCatalogKey(p.Name))
	if err != nil {
		return nil, false, genregistry.MakeServiceUnavailable(err)
	}
	if !exists {
		return nil, false, nil
	}
	current, err := parseCatalogState(p.Name, raw)
	if err != nil {
		return nil, false, genregistry.MakeServiceUnavailable(err)
	}
	if err := checkAgentRegistration(current, owned, nativeAgentToken(definition.fingerprint), ""); err != nil {
		return nil, false, genregistry.MakeAdmissionConflict(err)
	}
	saved, err := resolvedAgentRegistration(definition, current)
	if err != nil {
		return nil, false, err
	}
	return saved, true, nil
}

// ReplaceAgentToolset changes the declaration only while the supplied token
// still identifies the current registration.
func (s *Service) ReplaceAgentToolset(ctx context.Context, p *genregistry.ReplaceAgentToolsetPayload) (*genregistry.ResolvedToolset, error) {
	return s.registerAgentToolset(ctx, &genregistry.Toolset{
		Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Tools,
	}, p.ExpectedRegistrationToken, nil)
}

// ReplaceAgentToolsetWithIdentity replaces the exact native registration
// without changing its application identity or any accepted call.
func (s *Service) ReplaceAgentToolsetWithIdentity(ctx context.Context, identity CatalogIdentity, p *genregistry.ReplaceAgentToolsetPayload) (*genregistry.ResolvedToolset, error) {
	owned, err := identityInput(identity)
	if err != nil {
		return nil, err
	}
	return s.registerAgentToolset(ctx, &genregistry.Toolset{
		Name: p.Name, Description: p.Description, Version: p.Version, Tags: p.Tags, Tools: p.Tools,
	}, p.ExpectedRegistrationToken, owned)
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

func (s *Service) registerAgentToolset(ctx context.Context, toolset *genregistry.Toolset, expected string, identity *CatalogIdentity) (*genregistry.ResolvedToolset, error) {
	definition, err := s.prepareAgentToolset(toolset, identity)
	if err != nil {
		return nil, err
	}
	state, err := s.catalog.RegisterAgent(ctx, definition, expected)
	if err != nil {
		if errors.Is(err, errAdmissionConflict) {
			return nil, genregistry.MakeAdmissionConflict(err)
		}
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	return resolvedAgentRegistration(definition, state)
}

// prepareAgentToolset applies registration's schema and native-tool checks to
// the intended declaration. Lookup and registration compare the same token.
func (s *Service) prepareAgentToolset(toolset *genregistry.Toolset, identity *CatalogIdentity) (*catalogToolset, error) {
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
	definition.identity = identity
	return definition, nil
}

// resolvedAgentRegistration returns a separate copy of the matching declaration
// with the saved token and time. It does not rewrite stored definition bytes.
func resolvedAgentRegistration(definition *catalogToolset, state catalogState) (*genregistry.ResolvedToolset, error) {
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
			if err := checkAgentRegistration(current, definition.identity, token, expected); err != nil {
				return catalogState{}, err
			}
			if expected == "" {
				return current, nil
			}
		} else if expected != "" {
			return catalogState{}, errAdmissionConflict
		}
		now, err := c.clock.Now(ctx)
		if err != nil {
			return catalogState{}, err
		}
		state := catalogState{
			Identity:    definition.identity,
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

// checkAgentRegistration owns the comparison used by lookup and registration.
// Repeating creation requires an identical active native entry. Explicit
// replacement requires the saved token and may reactivate a retired entry.
func checkAgentRegistration(current catalogState, identity *CatalogIdentity, token, expected string) error {
	if err := requireCatalogIdentity(current.Identity, identity); err != nil {
		return err
	}
	if !current.NativeAgent {
		return fmt.Errorf("%w: %q belongs to a service provider", errAdmissionConflict, current.Info.Name)
	}
	if expected == "" {
		if current.State != catalogEntryActive || current.RegistrationToken != token {
			return errAdmissionConflict
		}
		return nil
	}
	if current.RegistrationToken != expected {
		return errAdmissionConflict
	}
	return nil
}
