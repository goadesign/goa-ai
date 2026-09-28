// Package registry lets an application attach immutable resource ownership to
// its catalog admissions. Identity never changes the stored tool declaration,
// registration token, or provider protocol.
package registry

import (
	"errors"
	"fmt"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// CatalogIdentity identifies a resource in an application's catalog. Scope
// membership is metadata; the application still authenticates and authorizes
// every request before using the catalog.
type CatalogIdentity struct {
	// Scope is the application-owned group used for bounded discovery.
	Scope string `json:"scope"`
	// Name is the original resource name, independently of its registry route.
	Name string `json:"name"`
}

func (i CatalogIdentity) validate() error {
	if i.Scope == "" || i.Name == "" {
		return errors.New("catalog identity requires a scope and name")
	}
	return nil
}

// requireCatalogIdentity prevents registration from adding, removing or
// changing ownership on an existing route. Historical assignment is a separate
// explicit migration, never a registration fallback.
func requireCatalogIdentity(saved, incoming *CatalogIdentity) error {
	if saved == nil && incoming == nil {
		return nil
	}
	if saved == nil || incoming == nil || *saved != *incoming {
		return fmt.Errorf("%w: catalog identity cannot change", errAdmissionConflict)
	}
	return nil
}

func identityInput(identity CatalogIdentity) (*CatalogIdentity, error) {
	if err := identity.validate(); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	return &identity, nil
}
