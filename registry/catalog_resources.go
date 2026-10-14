// Package registry lists stable resource identities through bounded indexes.
// Revisions and current configuration remain with their resource owner. Stale
// index entries consume page work but never grant access or hide continuation.
package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/redis/go-redis/v9"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// CheckCatalogResource checks saved registration and retirement for one owner
// resource. Callers receive not_found or admission_retired when it is unavailable.
func (s *Service) CheckCatalogResource(ctx context.Context, p *genregistry.CatalogResource) error {
	return s.updateCatalog(ctx, "resource_exists", func(tx *catalogTransaction) error {
		return tx.activeResource(p)
	})
}

// ListCatalogResources examines at most the requested number of indexed
// candidates. Retired candidates may produce an empty page, while
// the explicit continuation lets callers advance without guessing a revision.
func (s *Service) ListCatalogResources(ctx context.Context, p *genregistry.CatalogResourcesQuery) (*genregistry.CatalogResourcesPage, error) {
	if _, err := genregistry.EncodeCatalogResourcesQuery(p); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	name := ""
	if p.Name != nil {
		name = *p.Name
	}
	selection := catalogKey(p.Scope, p.Kind, name)
	minimum := "-"
	if p.After != nil {
		binding, position, ok := strings.Cut(*p.After, ".")
		if !ok || binding != selection {
			return nil, genregistry.MakeValidationError(errors.New("resource continuation does not match selection"))
		}
		member, err := base64.RawURLEncoding.DecodeString(position)
		if err != nil {
			return nil, genregistry.MakeValidationError(err)
		}
		if len(member) == 0 {
			return nil, genregistry.MakeValidationError(errors.New("resource continuation has no position"))
		}
		minimum = "(" + string(member)
	}

	var result *genregistry.CatalogResourcesPage
	err := s.readCatalog(ctx, "list_resources", func(tx *catalogTransaction) error {
		index := tx.store.ownerResourcesIndex(p.Scope, p.Kind)
		if err := tx.tx.Watch(ctx, index).Err(); err != nil {
			return err
		}
		// The extra member only identifies whether another page exists. Its resource
		// is not loaded or checked within this page's candidate budget.
		members, err := tx.tx.ZRangeArgs(ctx, redis.ZRangeArgs{Key: index, Start: minimum, Stop: "+", ByLex: true, Count: int64(p.Limit) + 1}).Result()
		if err != nil {
			return err
		}
		result = &genregistry.CatalogResourcesPage{Resources: []*genregistry.CatalogResource{}, Continuation: genregistry.NewResourcePageContinuationDone(&genregistry.ResourcePageDone{})}
		examined := min(len(members), int(p.Limit))
		for _, member := range members[:examined] {
			resource, err := genregistry.DecodeCatalogResource([]byte(member))
			if err != nil {
				return err
			}
			if resource.Kind != p.Kind || resource.Scope != p.Scope {
				return errors.New("resource index contains a different owner or kind")
			}
			if name != "" && name != resource.Name {
				continue
			}
			if err := tx.activeResource(resource); err != nil {
				if errors.Is(err, errToolsetNotFound) || errors.Is(err, errAdmissionRetired) {
					continue
				}
				return err
			}
			result.Resources = append(result.Resources, resource)
		}
		if len(members) > examined {
			position := base64.RawURLEncoding.EncodeToString([]byte(members[examined-1]))
			result.Continuation = genregistry.NewResourcePageContinuationMore(&genregistry.ResourcePageMore{After: selection + "." + position})
		}
		return nil
	})
	return result, err
}

func (s *redisCatalogStore) resourceIndex(resource *genregistry.CatalogResource) string {
	return s.catalogRecords() + ":resource:" + resourceKey(resource)
}

func (s *redisCatalogStore) ownerResourcesIndex(scope, kind string) string {
	return s.catalogRecords() + ":owners:" + catalogKey(scope, kind)
}
