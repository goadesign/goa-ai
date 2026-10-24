// Package registry exposes active registration facts without loading tool
// declarations. Catalog owners associate indexed routes with their own records;
// this read grants no execution authority.
package registry

import (
	"context"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// CatalogRegistration identifies one active admission without its declaration.
// The token and kind come from the same saved state as the resource identity.
type CatalogRegistration struct {
	// Identity is the application owner and original resource name.
	Identity CatalogIdentity
	// RegistrationToken identifies the exact active admission at this read.
	RegistrationToken string
	// NativeAgent distinguishes Agent exports from provider-served tools.
	NativeAgent bool
}

// ReadCatalogRegistration reads one route's compact saved state and returns its
// active owner, token and kind. Absent or retired routes return not_found; routes
// without saved identity return ErrCatalogIdentityMissing. No declaration,
// provider health or execution permission is read or inferred.
func (s *Service) ReadCatalogRegistration(ctx context.Context, name string) (CatalogRegistration, error) {
	raw, exists, err := s.catalog.exactRaw(ctx, toolsetCatalogKey(name))
	if err != nil {
		return CatalogRegistration{}, err
	}
	if !exists {
		return CatalogRegistration{}, genregistry.MakeNotFound(errToolsetNotFound)
	}
	state, err := parseCatalogState(name, raw)
	if err != nil {
		return CatalogRegistration{}, genregistry.MakeValidationError(err)
	}
	if state.State != catalogEntryActive {
		return CatalogRegistration{}, genregistry.MakeNotFound(errToolsetNotFound)
	}
	if state.Identity == nil {
		return CatalogRegistration{}, ErrCatalogIdentityMissing
	}
	return CatalogRegistration{
		Identity:          *state.Identity,
		RegistrationToken: state.RegistrationToken,
		NativeAgent:       state.NativeAgent,
	}, nil
}
