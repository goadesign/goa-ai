//go:build integration

// Resource discovery tests interleave catalog writes after candidate selection
// and verify that current eligibility and bounded continuation reach the caller.
package registry

import (
	"context"
	"fmt"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	goa "goa.design/goa/v3/pkg"
)

type (
	// resourceReadHook changes test-owned catalog data after the candidate read,
	// then counts eligibility commands so tests can verify per-request work.
	resourceReadHook struct {
		index              string
		afterSelection     func() error
		ranges             int
		retirementChecks   int
		registrationChecks int
	}
)

func TestRedisResourcePageConcurrentChanges(t *testing.T) {
	for _, change := range []string{"record", "provider state", "definition", "retired tokens", "other owner", "retirement", "route removal", "insertion"} {
		t.Run(change, func(t *testing.T) {
			service := policyTestService(t)
			store := service.catalog.store.(*redisCatalogStore)
			declaration, operation := policyTestAgent("owner", "a", "route-a")
			registration, err := service.RegisterAgentExports(t.Context(), declaration)
			require.NoError(t, err)
			next, _ := policyTestAgent("owner", "b", "route-b")
			_, err = service.RegisterAgentExports(t.Context(), next)
			require.NoError(t, err)
			hook := &resourceReadHook{index: store.ownerResourcesIndex("owner", "agent")}
			hook.afterSelection = func() error {
				switch change {
				case "record":
					return testRedisClient.HSet(t.Context(), store.catalogRecords(), "unrelated", "changed").Err()
				case "provider state":
					return testRedisClient.HSet(t.Context(), store.state, "unrelated", "changed").Err()
				case "definition":
					return testRedisClient.HSet(t.Context(), store.definitions, "unrelated", "changed").Err()
				case "retired tokens":
					return testRedisClient.SAdd(t.Context(), store.retired, "unrelated").Err()
				case "other owner":
					other, _ := policyTestAgent("other-owner", "c", "route-c")
					_, err := service.RegisterAgentExports(t.Context(), other)
					return err
				case "retirement":
					return service.RetireAgentResource(t.Context(), operationResource(operation))
				case "route removal":
					return service.catalog.Retire(t.Context(), declaration.Name, registration.RegistrationToken)
				case "insertion":
					inserted, _ := policyTestAgent("owner", "c", "route-c")
					_, err := service.RegisterAgentExports(t.Context(), inserted)
					return err
				default:
					panic("unexpected test change")
				}
			}
			reader := resourcePageReader(t, service, hook)
			page, err := reader.ListCatalogResources(t.Context(), &genregistry.CatalogResourcesQuery{Scope: "owner", Kind: "agent", Limit: 1})
			require.NoError(t, err)
			if change == "retirement" || change == "route removal" {
				assert.Empty(t, page.Resources)
			} else {
				require.Len(t, page.Resources, 1)
				assert.Equal(t, "a", page.Resources[0].Name)
			}
			more, ok := page.Continuation.AsMore()
			require.True(t, ok)
			assert.Equal(t, 1, hook.ranges)
			assert.Equal(t, 1, hook.retirementChecks)
			assert.Equal(t, 1, hook.registrationChecks)
			second, err := reader.ListCatalogResources(t.Context(), &genregistry.CatalogResourcesQuery{Scope: "owner", Kind: "agent", After: &more.After, Limit: 1})
			require.NoError(t, err)
			require.Len(t, second.Resources, 1)
			assert.Equal(t, "b", second.Resources[0].Name)
			if change == "insertion" {
				more, ok := second.Continuation.AsMore()
				require.True(t, ok)
				third, err := reader.ListCatalogResources(t.Context(), &genregistry.CatalogResourcesQuery{Scope: "owner", Kind: "agent", After: &more.After, Limit: 1})
				require.NoError(t, err)
				require.Len(t, third.Resources, 1)
				assert.Equal(t, "c", third.Resources[0].Name)
			}
		})
	}
}

func TestRedisResourcePageRejectsCorruptStorage(t *testing.T) {
	for _, corruption := range []string{"owner index", "retirement hash", "empty retirement hash", "resource index", "retired resource index", "malformed member", "foreign owner", "foreign kind"} {
		t.Run(corruption, func(t *testing.T) {
			service := policyTestService(t)
			store := service.catalog.store.(*redisCatalogStore)
			declaration, operation := policyTestAgent("owner", "a", "route-a")
			_, err := service.RegisterAgentExports(t.Context(), declaration)
			require.NoError(t, err)
			index := store.ownerResourcesIndex("owner", "agent")
			switch corruption {
			case "owner index":
				require.NoError(t, testRedisClient.Del(t.Context(), index).Err())
				require.NoError(t, testRedisClient.Set(t.Context(), index, "invalid", 0).Err())
			case "retirement hash", "empty retirement hash":
				require.NoError(t, testRedisClient.Set(t.Context(), store.catalogRecords(), "invalid", 0).Err())
				if corruption == "empty retirement hash" {
					require.NoError(t, testRedisClient.Del(t.Context(), index).Err())
				}
			case "resource index", "retired resource index":
				if corruption == "retired resource index" {
					require.NoError(t, service.RetireAgentResource(t.Context(), operationResource(operation)))
				}
				resourceIndex := store.resourceIndex(operationResource(operation))
				require.NoError(t, testRedisClient.Del(t.Context(), resourceIndex).Err())
				require.NoError(t, testRedisClient.Set(t.Context(), resourceIndex, "invalid", 0).Err())
			case "malformed member", "foreign owner", "foreign kind":
				member := "invalid"
				if corruption != "malformed member" {
					resource := operationResource(operation)
					if corruption == "foreign owner" {
						resource.Scope = "other"
					} else {
						resource.Kind = "service"
					}
					encoded, err := genregistry.EncodeCatalogResource(resource)
					require.NoError(t, err)
					member = string(encoded)
				}
				require.NoError(t, testRedisClient.Del(t.Context(), index).Err())
				require.NoError(t, testRedisClient.ZAdd(t.Context(), index, redis.Z{Member: member}).Err())
			}
			page, err := service.ListCatalogResources(t.Context(), &genregistry.CatalogResourcesQuery{Scope: "owner", Kind: "agent", Limit: 1})
			if corruption == "retired resource index" {
				require.NoError(t, err)
				assert.Empty(t, page.Resources)
			} else {
				var serviceError *goa.ServiceError
				require.ErrorAs(t, err, &serviceError)
				assert.Equal(t, "service_unavailable", serviceError.Name)
				assert.Nil(t, page)
			}
		})
	}
}

func TestRedisResourcePageNameFilterAndCursorBounds(t *testing.T) {
	service := policyTestService(t)
	store := service.catalog.store.(*redisCatalogStore)
	for _, name := range []string{"a", "b", "c"} {
		declaration, _ := policyTestAgent("owner", name, "route-"+name)
		_, err := service.RegisterAgentExports(t.Context(), declaration)
		require.NoError(t, err)
	}
	hook := &resourceReadHook{index: store.ownerResourcesIndex("owner", "agent")}
	reader := resourcePageReader(t, service, hook)
	name := "b"
	query := &genregistry.CatalogResourcesQuery{Scope: "owner", Kind: "agent", Name: &name, Limit: 1}
	first, err := reader.ListCatalogResources(t.Context(), query)
	require.NoError(t, err)
	assert.Empty(t, first.Resources)
	assert.Zero(t, hook.retirementChecks)
	assert.Zero(t, hook.registrationChecks)
	more, ok := first.Continuation.AsMore()
	require.True(t, ok)
	query.After = &more.After
	second, err := reader.ListCatalogResources(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, second.Resources, 1)
	assert.Equal(t, "b", second.Resources[0].Name)
	assert.Equal(t, 2, hook.ranges)
	assert.Equal(t, 1, hook.retirementChecks)
	assert.Equal(t, 1, hook.registrationChecks)
	for _, mismatch := range []string{"owner", "kind", "name", "malformed", "empty position"} {
		t.Run(mismatch, func(t *testing.T) {
			changed := *query
			switch mismatch {
			case "owner":
				changed.Scope = "other"
			case "kind":
				changed.Kind = "service"
			case "name":
				changed.Name = nil
			case "malformed":
				bad := catalogKey(query.Scope, query.Kind, name) + ".!"
				changed.After = &bad
			case "empty position":
				bad := catalogKey(query.Scope, query.Kind, name) + "."
				changed.After = &bad
			}
			page, err := reader.ListCatalogResources(t.Context(), &changed)
			var serviceError *goa.ServiceError
			require.ErrorAs(t, err, &serviceError)
			assert.Equal(t, "validation_error", serviceError.Name)
			assert.Nil(t, page)
		})
	}
}

func TestRedisResourcePageCandidateLimit(t *testing.T) {
	for _, limit := range []int32{0, 1, 2, 3, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			service := policyTestService(t)
			store := service.catalog.store.(*redisCatalogStore)
			for _, name := range []string{"a", "b", "c"} {
				declaration, _ := policyTestAgent("owner", name, "route-"+name)
				_, err := service.RegisterAgentExports(t.Context(), declaration)
				require.NoError(t, err)
			}
			hook := &resourceReadHook{index: store.ownerResourcesIndex("owner", "agent")}
			reader := resourcePageReader(t, service, hook)
			page, err := reader.ListCatalogResources(t.Context(), &genregistry.CatalogResourcesQuery{Scope: "owner", Kind: "agent", Limit: limit})
			if limit == 0 {
				var serviceError *goa.ServiceError
				require.ErrorAs(t, err, &serviceError)
				assert.Equal(t, "validation_error", serviceError.Name)
				assert.Nil(t, page)
				assert.Zero(t, hook.ranges)
				return
			}
			require.NoError(t, err)
			examined := min(int(limit), 3)
			assert.Len(t, page.Resources, examined)
			assert.Equal(t, 1, hook.ranges)
			assert.Equal(t, examined, hook.retirementChecks)
			assert.Equal(t, examined, hook.registrationChecks)
			_, more := page.Continuation.AsMore()
			assert.Equal(t, limit < 3, more)
		})
	}
}

// resourcePageReader gives one test a separate Redis client so its hook observes
// only discovery commands; the original service performs concurrent writes.
func resourcePageReader(t *testing.T, service *Service, hook *resourceReadHook) *Service {
	t.Helper()
	client := redis.NewClient(testRedisClient.Options())
	t.Cleanup(func() { assert.NoError(t, client.Close()) })
	client.AddHook(hook)
	store := *service.catalog.store.(*redisCatalogStore)
	store.redis = client
	return &Service{catalog: newToolsetCatalog(&store, newRedisTimeSource(client))}
}

func (h *resourceReadHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *resourceReadHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if err := next(ctx, cmd); err != nil {
			return err
		}
		if cmd.Name() == "zrange" && cmd.Args()[1] == h.index {
			h.ranges++
			if h.afterSelection != nil {
				change := h.afterSelection
				h.afterSelection = nil
				return change()
			}
		}
		return nil
	}
}

func (h *resourceReadHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		for _, command := range commands {
			switch command.Name() {
			case "hstrlen":
				h.retirementChecks++
			case "type":
				h.registrationChecks++
			}
		}
		return next(ctx, commands)
	}
}
