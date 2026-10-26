//go:build integration

// Native admission retains only newly committed exact-byte preparation. These
// tests use the Registry constructor and Redis transactions to verify that
// repeated delivery, failed writes and changed saved bytes keep read checks.
package registry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

type (
	// nativeCommitHook pauses a new registration before Redis executes it, or
	// returns one write error. Tests can then observe the failed transaction.
	nativeCommitHook struct {
		state    string
		entered  chan struct{}
		release  chan struct{}
		attempts atomic.Int32
		fail     bool
	}
)

func TestRedisNativeAdmissionReusesCommittedPreparation(t *testing.T) {
	service := nativeCacheTestService(t, "", nil)
	input := nativeCacheTestDeclaration()
	input.Exports = testAgentPagingToolset().Tools
	reply, err := service.RegisterAgentExports(t.Context(), input)
	require.NoError(t, err)
	prepared := nativeCacheEntry(service.catalog, input.Name)
	require.NotNil(t, prepared)
	assert.True(t, prepared.nativeAgent)
	assert.Same(t, service.catalog.validator, service.validator)
	read, err := service.catalog.ActiveRegistration(t.Context(), input.Name)
	require.NoError(t, err)
	assert.Same(t, prepared, read.Toolset.catalogDefinition)
	assert.Equal(t, catalogDefinitionDigest(read.Toolset.raw), prepared.digest)

	input.Exports[0].ConsumerContract.Title = "Caller mutation"
	reply.Toolset.Tools[0].ConsumerContract.Title = "Response mutation"
	decodedRead, err := read.Toolset.decode(read.RegisteredAt)
	require.NoError(t, err)
	decodedRead.Tools[0].ConsumerContract.Title = "Read mutation"
	again, err := service.catalog.GetToolset(t.Context(), input.Name)
	require.NoError(t, err)
	assert.Equal(t, "Check installation", again.Tools[0].ConsumerContract.Title)

	restarted := newToolsetCatalog(service.catalog.store, service.catalog.clock)
	assert.Nil(t, nativeCacheEntry(restarted, input.Name))
	afterRestart, err := restarted.ActiveRegistration(t.Context(), input.Name)
	require.NoError(t, err)
	assert.NotSame(t, prepared, afterRestart.Toolset.catalogDefinition)
	beforeTools, err := read.Toolset.decode("")
	require.NoError(t, err)
	afterTools, err := afterRestart.Toolset.decode("")
	require.NoError(t, err)
	assert.Equal(t, beforeTools, afterTools)
}

func TestRedisNativeAdmissionRepeatPreservesSavedByteCache(t *testing.T) {
	service := nativeCacheTestService(t, "", nil)
	input := nativeCacheTestDeclaration()
	first, err := service.RegisterAgentExports(t.Context(), input)
	require.NoError(t, err)
	initial := nativeCacheEntry(service.catalog, input.Name)
	repeated, err := service.RegisterAgentExports(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, first, repeated)
	assert.Same(t, initial, nativeCacheEntry(service.catalog, input.Name))
	store := service.catalog.store.(*redisCatalogStore)
	key := toolsetCatalogKey(input.Name)
	_, raw, _, exists, err := store.Snapshot(t.Context(), key)
	require.NoError(t, err)
	require.True(t, exists)
	changed := " \n" + raw
	require.NoError(t, testRedisClient.HSet(t.Context(), store.definitions, key, changed).Err())
	actual, err := service.catalog.ActiveRegistration(t.Context(), input.Name)
	require.NoError(t, err)
	require.NotSame(t, initial, actual.Toolset.catalogDefinition)
	assert.Equal(t, initial.fingerprint, actual.SchemaFingerprint)
	_, err = service.RegisterAgentExports(t.Context(), input)
	require.NoError(t, err)
	assert.Same(t, actual.Toolset.catalogDefinition, nativeCacheEntry(service.catalog, input.Name))
	persisted, err := testRedisClient.HGet(t.Context(), store.definitions, key).Result()
	require.NoError(t, err)
	assert.Equal(t, changed, persisted)

	corrupt := strings.Replace(raw, `"Name":`, `"Unknown":true,"Name":`, 1)
	require.NoError(t, testRedisClient.HSet(t.Context(), store.definitions, key, corrupt).Err())
	_, err = service.RegisterAgentExports(t.Context(), input)
	require.NoError(t, err)
	assert.Same(t, actual.Toolset.catalogDefinition, nativeCacheEntry(service.catalog, input.Name))
	_, err = service.catalog.ActiveRegistration(t.Context(), input.Name)
	assert.ErrorContains(t, err, "unknown field")
}

func TestRedisNativeAdmissionFailedCommitDoesNotRetainPreparation(t *testing.T) {
	hook := &nativeCommitHook{fail: true}
	service := nativeCacheTestService(t, "", hook)
	input := nativeCacheTestDeclaration()
	_, err := service.RegisterAgentExports(t.Context(), input)
	require.ErrorContains(t, err, "injected write failure")
	assert.Nil(t, nativeCacheEntry(service.catalog, input.Name))
	store := service.catalog.store.(*redisCatalogStore)
	exists, err := testRedisClient.HExists(t.Context(), store.state, toolsetCatalogKey(input.Name)).Result()
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestRedisNativeAdmissionWatchLoserDoesNotRetainPreparation(t *testing.T) {
	hook := &nativeCommitHook{entered: make(chan struct{}), release: make(chan struct{})}
	service := nativeCacheTestService(t, "", hook)
	input := nativeCacheTestDeclaration()
	done := make(chan error, 1)
	go func() {
		_, err := service.RegisterAgentExports(t.Context(), input)
		done <- err
	}()
	select {
	case <-hook.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not reach watched commit")
	}
	store := service.catalog.store.(*redisCatalogStore)
	require.NoError(t, testRedisClient.HSet(t.Context(), store.catalogRecords(), "other-owner-change", "changed").Err())
	close(hook.release)
	select {
	case err := <-done:
		require.ErrorContains(t, err, "injected write failure")
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not finish after watched conflict")
	}
	assert.Equal(t, int32(2), hook.attempts.Load())
	assert.Nil(t, nativeCacheEntry(service.catalog, input.Name))
	exists, err := testRedisClient.HExists(t.Context(), store.state, toolsetCatalogKey(input.Name)).Result()
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestRedisNativeAdmissionConcurrentDeliveryAndConflict(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical", true: "conflicting"}[different], func(t *testing.T) {
			name := "native-cache-" + uuid.NewString()
			services := []*Service{nativeCacheTestService(t, name, nil), nativeCacheTestService(t, name, nil)}
			inputs := []*genregistry.AgentExportsDeclaration{nativeCacheTestDeclaration(), nativeCacheTestDeclaration()}
			if different {
				title := "Different declaration"
				inputs[1].Exports[0].Description = &title
			}
			outcomes := make([]error, 2)
			var wait sync.WaitGroup
			for i := range services {
				wait.Go(func() {
					_, outcomes[i] = services[i].RegisterAgentExports(t.Context(), inputs[i])
				})
			}
			wait.Wait()
			successes, cached := 0, 0
			for i, err := range outcomes {
				if err == nil {
					successes++
				}
				if nativeCacheEntry(services[i].catalog, inputs[i].Name) != nil {
					cached++
				}
			}
			if different {
				assert.Equal(t, 1, successes)
			} else {
				assert.Equal(t, 2, successes)
			}
			assert.Equal(t, 1, cached, "only the newly committed registration retains preparation")
			for i := range services {
				_, err := services[i].catalog.ActiveRegistration(t.Context(), inputs[i].Name)
				require.NoError(t, err)
			}
		})
	}
}

func TestRedisNativeAdmissionRetainsFreshStateAndRetirementChecks(t *testing.T) {
	service := nativeCacheTestService(t, "", nil)
	input := nativeCacheTestDeclaration()
	reply, err := service.RegisterAgentExports(t.Context(), input)
	require.NoError(t, err)
	cached := nativeCacheEntry(service.catalog, input.Name)
	resource := &genregistry.CatalogResource{Kind: "agent", Scope: input.Scope, Name: input.ResourceName}
	require.NoError(t, service.RetireAgentResource(t.Context(), resource))
	assert.Error(t, service.CheckCatalogResource(t.Context(), resource))
	_, err = service.RegisterAgentExports(t.Context(), input)
	require.NoError(t, err)
	assert.Same(t, cached, nativeCacheEntry(service.catalog, input.Name))
	require.NoError(t, service.catalog.Retire(t.Context(), input.Name, reply.RegistrationToken))
	_, err = service.RegisterAgentExports(t.Context(), input)
	require.Error(t, err)
	assert.Same(t, cached, nativeCacheEntry(service.catalog, input.Name))

	fresh := nativeCacheTestService(t, "", nil)
	next := nativeCacheTestDeclaration()
	_, err = fresh.RegisterAgentExports(t.Context(), next)
	require.NoError(t, err)
	store := fresh.catalog.store.(*redisCatalogStore)
	key := toolsetCatalogKey(next.Name)
	raw, err := testRedisClient.HGet(t.Context(), store.state, key).Result()
	require.NoError(t, err)
	state, err := parseCatalogState(next.Name, raw)
	require.NoError(t, err)
	altered := "Changed discovery information"
	state.Info.Description = &altered
	body, err := marshalCatalogState(state)
	require.NoError(t, err)
	require.NoError(t, testRedisClient.HSet(t.Context(), store.state, key, body).Err())
	_, err = fresh.catalog.ActiveRegistration(t.Context(), next.Name)
	assert.ErrorContains(t, err, "discovery metadata does not match")
}

func TestRedisNativeAdmissionConcurrentSameProcess(t *testing.T) {
	service := nativeCacheTestService(t, "", nil)
	outcomes := make([]error, 8)
	var wait sync.WaitGroup
	for i := range outcomes {
		wait.Go(func() {
			_, outcomes[i] = service.RegisterAgentExports(t.Context(), nativeCacheTestDeclaration())
		})
	}
	wait.Wait()
	for _, err := range outcomes {
		require.NoError(t, err)
	}
	prepared := nativeCacheEntry(service.catalog, "installation")
	require.NotNil(t, prepared)
	read, err := service.catalog.ActiveRegistration(t.Context(), "installation")
	require.NoError(t, err)
	assert.Same(t, prepared, read.Toolset.catalogDefinition)
}

func TestRedisNativeAdmissionRejectsInvalidExportsWithoutRetaining(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*genregistry.AgentExportsDeclaration)
	}{
		{"nil tool", func(p *genregistry.AgentExportsDeclaration) {
			p.Exports[0] = nil
		}},
		{"incomplete paging", func(p *genregistry.AgentExportsDeclaration) {
			p.Exports = testAgentPagingToolset().Tools[:1]
		}},
		{"duplicate names", func(p *genregistry.AgentExportsDeclaration) {
			p.Exports = append(p.Exports, p.Exports[0])
		}},
		{"service tool", func(p *genregistry.AgentExportsDeclaration) {
			p.Exports[0].ConsumerContract.Kind = "service"
			p.Exports[0].ConsumerContract.Agent = nil
		}},
		{"invalid UTF8", func(p *genregistry.AgentExportsDeclaration) {
			p.Exports[0].ConsumerContract.Title = string([]byte{0xff})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := nativeCacheTestService(t, "", nil)
			input := nativeCacheTestDeclaration()
			test.change(input)
			_, err := service.RegisterAgentExports(t.Context(), input)
			requireServiceErrorName(t, err, "validation_error")
			assert.Nil(t, nativeCacheEntry(service.catalog, input.Name))
		})
	}
}

// nativeCacheTestService uses the production constructor, including its shared
// schema validator. Each test owns the Redis namespace and the client it closes.
func nativeCacheTestService(t *testing.T, name string, hook *nativeCommitHook) *Service {
	t.Helper()
	if name == "" {
		name = "native-cache-" + uuid.NewString()
	}
	client := redis.NewClient(testRedisClient.Options())
	t.Cleanup(func() {
		assert.NoError(t, client.Close())
	})
	registry, err := New(t.Context(), Config{Redis: client, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, registry.Close(t.Context()))
	})
	if hook != nil {
		hook.state = registry.service.catalog.store.(*redisCatalogStore).state
		client.AddHook(hook)
	}
	return registry.service
}

func nativeCacheTestDeclaration() *genregistry.AgentExportsDeclaration {
	source := testAgentToolset("v1")
	return &genregistry.AgentExportsDeclaration{Scope: "owner", ResourceName: "native", Name: source.Name, Exports: source.Tools}
}

func nativeCacheEntry(catalog *toolsetCatalog, name string) *catalogDefinition {
	catalog.definitionsMu.RLock()
	defer catalog.definitionsMu.RUnlock()
	return catalog.definitions[name]
}

func (h *nativeCommitHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}
func (h *nativeCommitHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}
func (h *nativeCommitHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		for _, command := range commands {
			if command.Name() == "hset" && command.Args()[1] == h.state {
				attempt := h.attempts.Add(1)
				if h.fail || attempt > 1 {
					return errors.New("injected write failure")
				}
				close(h.entered)
				select {
				case <-h.release:
				case <-ctx.Done():
					return ctx.Err()
				}
				break
			}
		}
		return next(ctx, commands)
	}
}
