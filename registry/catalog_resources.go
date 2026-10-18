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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
func (s *Service) ListCatalogResources(ctx context.Context, p *genregistry.CatalogResourcesQuery) (page *genregistry.CatalogResourcesPage, err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.list_resources")
	defer finishCatalogSpan(ctx, span, &err)
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

	store, ok := s.catalog.store.(*redisCatalogStore)
	if !ok {
		return nil, genregistry.MakeServiceUnavailable(errors.New("catalog resource read requires the registry-owned Redis catalog"))
	}
	// Read the candidates once. The extra member only tells the caller that more
	// work remains; it does not receive registration or retirement checks.
	members, err := store.redis.ZRangeArgs(ctx, redis.ZRangeArgs{Key: store.ownerResourcesIndex(p.Scope, p.Kind), Start: minimum, Stop: "+", ByLex: true, Count: int64(p.Limit) + 1}).Result()
	if err != nil {
		return nil, catalogError(err)
	}
	examined := min(len(members), int(p.Limit))
	candidates := make([]*genregistry.CatalogResource, 0, examined)
	for _, member := range members[:examined] {
		resource, err := genregistry.DecodeCatalogResource([]byte(member))
		if err != nil {
			return nil, catalogError(err)
		}
		if resource.Kind != p.Kind || resource.Scope != p.Scope {
			return nil, catalogError(errors.New("resource index contains a different owner or kind"))
		}
		if name == "" || name == resource.Name {
			candidates = append(candidates, resource)
		}
	}

	// Check all selected candidates at one Redis instant. An ordinary transaction
	// lets unrelated writes complete without invalidating this read. A retirement
	// after selection omits the resource while retaining its continuation position.
	retired := make([]*redis.IntCmd, len(candidates))
	registered := make([]*redis.StatusCmd, len(candidates))
	_, err = store.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		// Even an empty page checks the owned retirement hash so corrupted storage
		// returns an error instead of looking like an empty catalog.
		pipe.HLen(ctx, store.catalogRecords())
		for i, resource := range candidates {
			retired[i] = pipe.HStrLen(ctx, store.catalogRecords(), "retired:"+resourceKey(resource))
			registered[i] = pipe.Type(ctx, store.resourceIndex(resource))
		}
		return nil
	})
	if err != nil {
		return nil, catalogError(err)
	}
	result := &genregistry.CatalogResourcesPage{Resources: []*genregistry.CatalogResource{}, Continuation: genregistry.NewResourcePageContinuationDone(&genregistry.ResourcePageDone{})}
	for i, resource := range candidates {
		if retired[i].Val() > 0 {
			continue
		}
		// Redis removes a sorted set when its last route is removed. Its type
		// therefore distinguishes registered resources without loading any routes.
		switch registered[i].Val() {
		case redisMissingKeyType:
			continue
		case "zset":
			result.Resources = append(result.Resources, resource)
		default:
			return nil, catalogError(errors.New("invalid catalog resource index type"))
		}
	}
	if len(members) > examined {
		position := base64.RawURLEncoding.EncodeToString([]byte(members[examined-1]))
		result.Continuation = genregistry.NewResourcePageContinuationMore(&genregistry.ResourcePageMore{After: selection + "." + position})
	}
	span.SetAttributes(
		attribute.Int("toolregistry.catalog.candidates_examined", examined),
		attribute.Int("toolregistry.catalog.resources_returned", len(result.Resources)),
		attribute.Bool("toolregistry.catalog.has_more", len(members) > examined),
	)
	return result, nil
}

func (s *redisCatalogStore) resourceIndex(resource *genregistry.CatalogResource) string {
	return s.catalogRecords() + ":resource:" + resourceKey(resource)
}

func (s *redisCatalogStore) ownerResourcesIndex(scope, kind string) string {
	return s.catalogRecords() + ":owners:" + catalogKey(scope, kind)
}
