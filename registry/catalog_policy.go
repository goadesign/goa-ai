// Package registry validates native declarations and retires permanent resources.
// The host authorizes each request before these catalog operations run.
package registry

import (
	"context"
	"errors"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// RetireAgentResource prevents new revisions and removes the resource from
// catalog discovery while keeping its saved declarations.
func (s *Service) RetireAgentResource(ctx context.Context, p *genregistry.CatalogResource) error {
	if p.Kind != catalogResourceKindAgent {
		return genregistry.MakeValidationError(errors.New("native resource retirement requires agent kind"))
	}
	return s.updateCatalog(ctx, "retire", func(tx *catalogTransaction) error {
		saved, err := tx.value("retired:" + resourceKey(p))
		if err != nil {
			return err
		}
		if saved != "" {
			return nil
		}

		tx.writes["retired:"+resourceKey(p)] = "1"
		return nil
	})
}

// ValidateAgentExports validates the generated operation schemas and reports
// whether the permanent resource has already been retired.
func (s *Service) ValidateAgentExports(ctx context.Context, p *genregistry.AgentExportsValidation) error {
	if err := s.validator.ValidateToolSchemas(p.Exports); err != nil {
		return genregistry.MakeValidationError(err)
	}
	if err := validateAgentTools(p.Exports); err != nil {
		return genregistry.MakeValidationError(err)
	}
	return s.updateCatalog(ctx, "validate_exports", func(tx *catalogTransaction) error {
		retired, err := tx.value("retired:" + resourceKey(&genregistry.CatalogResource{Kind: catalogResourceKindAgent, Scope: p.Scope, Name: p.ResourceName}))
		if err != nil {
			return err
		}
		if retired != "" {
			return errAdmissionRetired
		}

		return nil
	})
}
