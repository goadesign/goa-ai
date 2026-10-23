// Package registry changes native declarations and retirement state atomically
// with catalog registration. Only Registry reads
// these private records; hosts authorize callers before using the catalog API.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	goa "goa.design/goa/v3/pkg"
)

type (
	// catalogTransaction reads a watched catalog and stages one atomic owner write.
	// Catalog lifecycle scripts change the same watched state hash, so a concurrent
	// retirement forces this transaction to read current state again.
	catalogTransaction struct {
		ctx          context.Context
		tx           *redis.Tx
		store        *redisCatalogStore
		writes       map[string]string
		deletes      []string
		registration *catalogRegistration
		indexWrites  map[string]string
	}
	// catalogRegistration adds a native revision together with its exports.
	catalogRegistration struct {
		state      catalogState
		definition *catalogToolset
	}
)

const redisMissingKeyType = "none"

func catalogKey(parts ...string) string {
	raw, err := json.Marshal(parts)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (s *redisCatalogStore) catalogRecords() string { return s.definitions + ":records" }

// updateCatalog runs the complete owner decision against one watched catalog.
// Read-only operations still commit an empty transaction: a concurrent lifecycle
// change therefore retries rather than returning a mixed catalog snapshot.
func (s *Service) updateCatalog(ctx context.Context, operation string, apply func(*catalogTransaction) error) (err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog."+operation)
	defer finishCatalogSpan(ctx, span, &err)
	for {
		err = s.catalogAttempt(ctx, apply)
		if !errors.Is(err, redis.TxFailedErr) {
			return catalogError(err)
		}
		if err := ctx.Err(); err != nil {
			return genregistry.MakeServiceUnavailable(err)
		}
	}
}

// catalogAttempt reads and writes the catalog through one watched transaction.
// updateCatalog repeats the owner decision after a concurrent change; this
// method performs one attempt and returns its result.
func (s *Service) catalogAttempt(ctx context.Context, apply func(*catalogTransaction) error) error {
	store, ok := s.catalog.store.(*redisCatalogStore)
	if !ok {
		return genregistry.MakeServiceUnavailable(errors.New("catalog transaction requires the registry-owned Redis catalog"))
	}
	return store.redis.Watch(ctx, func(tx *redis.Tx) error {
		p := &catalogTransaction{ctx: ctx, tx: tx, store: store, writes: make(map[string]string), indexWrites: make(map[string]string)}
		for _, key := range []string{store.state, store.definitions, store.catalogRecords()} {
			kind, err := tx.Type(ctx, key).Result()
			if err != nil {
				return err
			}
			if kind != redisMissingKeyType && kind != "hash" {
				return errors.New("invalid catalog owner hash type")
			}
		}
		if err := apply(p); err != nil {
			return err
		}
		if p.registration != nil {
			state := p.registration.state
			resource := &genregistry.CatalogResource{Kind: catalogResourceKindAgent, Scope: state.Identity.Scope, Name: state.Identity.Name}
			encoded, err := genregistry.EncodeCatalogResource(resource)
			if err != nil {
				return err
			}
			p.indexWrites[store.scopeIndex(resource.Scope)] = state.Info.Name
			p.indexWrites[store.resourceIndex(resource)] = state.Info.Name
			p.indexWrites[store.ownerResourcesIndex(resource.Scope, resource.Kind)] = string(encoded)
		}
		for index := range p.indexWrites {
			if err := tx.Watch(ctx, index).Err(); err != nil {
				return err
			}
			kind, err := tx.Type(ctx, index).Result()
			if err != nil {
				return err
			}
			if kind != redisMissingKeyType && kind != "zset" {
				return errors.New("invalid catalog resource index type")
			}
		}

		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			if p.registration != nil {
				registration := p.registration
				raw, err := marshalCatalogState(registration.state)
				if err != nil {
					return err
				}
				key := toolsetCatalogKey(registration.state.Info.Name)
				pipe.HSet(ctx, store.state, key, raw)
				pipe.HSet(ctx, store.definitions, key, registration.definition.raw)
			}
			for key, member := range p.indexWrites {
				pipe.ZAdd(ctx, key, redis.Z{Score: 0, Member: member})
			}
			for key, value := range p.writes {
				pipe.HSet(ctx, store.catalogRecords(), key, value)
			}
			if len(p.deletes) > 0 {
				pipe.HDel(ctx, store.catalogRecords(), p.deletes...)
			}
			// Even a read-only snapshot executes a command so concurrent writes
			// invalidate this transaction before the caller receives its result.
			pipe.Exists(ctx, store.state)
			return nil
		})
		if err != nil {
			return err
		}
		// A newly committed native declaration reuses its exact-byte preparation
		// on this process's first read. Repeated admissions have no staged
		// registration, so they cannot replace a warm saved-byte cache entry.
		if p.registration != nil {
			definition := p.registration.definition
			s.catalog.definitionsMu.Lock()
			s.catalog.definitions[definition.info.Name] = definition.catalogDefinition
			s.catalog.definitionsMu.Unlock()
		}
		return nil
	}, store.state, store.definitions, store.retired, store.catalogRecords())
}

func catalogError(err error) error {
	var domainError *goa.ServiceError
	if errors.As(err, &domainError) {
		return domainError
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errToolsetNotFound):
		return genregistry.MakeNotFound(err)
	case errors.Is(err, errAdmissionRetired):
		return genregistry.MakeAdmissionRetired(err)
	case errors.Is(err, errAdmissionConflict):
		return genregistry.MakeAdmissionConflict(err)
	default:
		return genregistry.MakeServiceUnavailable(err)
	}
}

func resourceKey(resource *genregistry.CatalogResource) string {
	return catalogKey(resource.Kind, resource.Scope, resource.Name)
}

func (p *catalogTransaction) value(key string) (string, error) {
	value, err := p.tx.HGet(p.ctx, p.store.catalogRecords(), key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}

func (p *catalogTransaction) activeResource(resource *genregistry.CatalogResource) error {
	retired, err := p.value("retired:" + resourceKey(resource))
	if err != nil {
		return err
	}
	if retired != "" {
		return errAdmissionRetired
	}
	count, err := p.tx.ZCard(p.ctx, p.store.resourceIndex(resource)).Result()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	return errToolsetNotFound
}
