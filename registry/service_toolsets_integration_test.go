//go:build integration

package registry

// These tests exercise declaration and attachment through generated gRPC and
// real Redis. Separate registry instances share only their owned Redis catalog.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	grpcclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	registrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type (
	// competingCatalogStore pauses one write while another real Redis
	// transition completes, making the losing comparison deterministic.
	competingCatalogStore struct {
		catalogStore
		beforeCommit func()
	}
)

func (s *competingCatalogStore) Commit(ctx context.Context, key, previous string, write catalogWrite) (bool, error) {
	if before := s.beforeCommit; before != nil {
		s.beforeCommit = nil
		before()
	}
	return s.catalogStore.Commit(ctx, key, previous, write)
}

func TestRedisServiceDeclarationGeneratedGRPCOfflineAndReplay(t *testing.T) {
	rdb := getRedis(t)
	ctx := t.Context()
	store := newRedisCatalogStore(rdb, t.Name())
	clock := newRedisTimeSource(rdb)
	svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	client, raw := startServiceAndClients(t, svc)
	before, err := rdb.Time(ctx).Result()
	require.NoError(t, err)
	input := testServiceDeclaration()
	first, err := client.DeclareServiceToolset(ctx, input)
	require.NoError(t, err)
	after, err := rdb.Time(ctx).Result()
	require.NoError(t, err)
	registeredAt, err := time.Parse(time.RFC3339Nano, first.Toolset.RegisteredAt)
	require.NoError(t, err)
	assert.False(t, registeredAt.Before(before))
	assert.False(t, registeredAt.After(after))
	keys, err := rdb.Keys(ctx, "pulse:*").Result()
	require.NoError(t, err)
	assert.Empty(t, keys, "the declaration RPC must not open provider streams or publish pings")

	slices.Reverse(input.Tools)
	slices.Reverse(input.Tags)
	same, err := client.DeclareServiceToolset(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, first, same, "the stored winner controls ordering as well as identity")
	restarted := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	require.NoError(t, restarted.catalog.validatePersistedEntries(ctx))
	client2, _ := startServiceAndClients(t, restarted)
	replayed, err := client2.DeclareServiceToolset(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, first, replayed)

	for _, tc := range []struct {
		name   string
		mutate func(*registrypb.DeclareServiceToolsetRequest)
	}{
		{"missing name", func(p *registrypb.DeclareServiceToolsetRequest) { p.Name = nil }},
		{"overlong route", func(p *registrypb.DeclareServiceToolsetRequest) { p.Name = strPtr(strings.Repeat("界", 257)) }},
		{"empty tools", func(p *registrypb.DeclareServiceToolsetRequest) { p.Tools = nil }},
		{"overlong tool", func(p *registrypb.DeclareServiceToolsetRequest) { p.Tools[0].Name = strPtr(strings.Repeat("界", 257)) }},
		{"unknown consumer kind", func(p *registrypb.DeclareServiceToolsetRequest) { p.Tools[0].ConsumerContract.Kind = strPtr("other") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := grpcclient.NewProtoDeclareServiceToolsetRequest(testServiceDeclaration())
			tc.mutate(request)
			_, err := raw.DeclareServiceToolset(ctx, request)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
	long := testServiceDeclaration()
	long.Name = strings.Repeat("界", 256)
	_, err = client.DeclareServiceToolset(ctx, long)
	require.NoError(t, err, "route length counts Unicode code points, not UTF-8 bytes")

	paired := testServiceDeclaration()
	paired.Name = "paired"
	source, continuation := paired.Tools[0], paired.Tools[1]
	source.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
		CursorField: "cursor", NextCursorField: "next_cursor", ContinueTool: &continuation.Name,
	}}
	continuation.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
		CursorField: "cursor", NextCursorField: "next_cursor", ContinueTool: &continuation.Name, SourceTool: &source.Name,
	}}
	_, err = client.DeclareServiceToolset(ctx, paired)
	require.NoError(t, err)
	paired.Name = "broken-pair"
	continuation.ConsumerContract.Bounds.Paging.SourceTool = strPtr("inventory.other")
	_, err = client.DeclareServiceToolset(ctx, paired)
	requireServiceErrorName(t, err, "validation_error")
	invalid := testServiceDeclaration()
	invalid.Name = "invalid-schema"
	invalid.Tools[0].PayloadSchema = []byte(`{"type":"missing"}`)
	_, err = client.DeclareServiceToolset(ctx, invalid)
	requireServiceErrorName(t, err, "validation_error")
	changed := testServiceDeclaration()
	changed.Tools[0].ConsumerContract.Title = "Different instruction"
	_, err = client.DeclareServiceToolset(ctx, changed)
	requireServiceErrorName(t, err, "admission_conflict")
	require.NoError(t, client.Unregister(ctx, &genregistry.UnregisterPayload{
		Name: first.Toolset.Name, ExpectedRegistrationToken: first.RegistrationToken,
	}))
	_, err = client.DeclareServiceToolset(ctx, testServiceDeclaration())
	requireServiceErrorName(t, err, "admission_retired")
}

func TestRedisServiceDeclarationConcurrentCreators(t *testing.T) {
	for _, identical := range []bool{true, false} {
		t.Run(map[bool]string{true: "equal", false: "different"}[identical], func(t *testing.T) {
			rdb := getRedis(t)
			store := newRedisCatalogStore(rdb, t.Name())
			clock := newRedisTimeSource(rdb)
			start := make(chan struct{})
			results := make([]*genregistry.ResolvedToolset, 2)
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i := range 2 {
				svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
				client, _ := startServiceAndClients(t, svc)
				wg.Go(func() {
					input := testServiceDeclaration()
					if i == 1 {
						slices.Reverse(input.Tools)
						if !identical {
							input.Tools[0].ConsumerContract.Title = "Changed"
						}
					}
					<-start
					results[i], errs[i] = client.DeclareServiceToolset(t.Context(), input)
				})
			}
			close(start)
			wg.Wait()
			if identical {
				require.NoError(t, errs[0])
				require.NoError(t, errs[1])
				assert.Equal(t, results[0], results[1])
			} else {
				winner, loser := 0, 1
				if errs[0] != nil {
					winner, loser = 1, 0
				}
				require.NoError(t, errs[winner])
				requireServiceErrorName(t, errs[loser], "admission_conflict")
				saved, err := newToolsetCatalog(store, clock).GetToolset(t.Context(), "inventory")
				require.NoError(t, err)
				assert.Equal(t, results[winner].Toolset, saved)
			}
			require.NoError(t, newToolsetCatalog(store, clock).validatePersistedEntries(t.Context()))
		})
	}
}

func TestRedisAttachmentCompetesWithReplacementRetirementAndRelease(t *testing.T) {
	const (
		replace = "replace declaration"
		retire  = "retire declaration"
		release = "last release"
	)
	for _, transition := range []string{replace, retire, release} {
		t.Run(transition, func(t *testing.T) {
			ctx := t.Context()
			rdb := getRedis(t)
			store := newRedisCatalogStore(rdb, t.Name())
			clock := newRedisTimeSource(rdb)
			other := newToolsetCatalog(store, clock)
			definition := testCatalogDefinition(t, testCatalogToolset("inventory", "inventory", nil))
			first, err := other.DeclareService(ctx, definition)
			require.NoError(t, err)
			if transition == release {
				_, err = other.AttachProvider(ctx, "inventory", first.RegistrationToken, "old", testIncarnationA, time.Minute)
				require.NoError(t, err)
			}
			competing := &competingCatalogStore{catalogStore: store}
			catalog := newToolsetCatalog(competing, clock)
			competing.beforeCommit = func() {
				switch transition {
				case replace:
					_, err = other.Register(ctx, definition, testAdmissionRevisionB, "deployment-provider", testIncarnationA, time.Minute)
				case retire:
					err = other.Retire(ctx, "inventory", first.RegistrationToken)
				case release:
					err = other.ReleaseProvider(ctx, "inventory", "old", testIncarnationA, first.RegistrationToken)
				}
				require.NoError(t, err)
			}
			attached, err := catalog.AttachProvider(ctx, "inventory", first.RegistrationToken, "new", testIncarnationB, time.Minute)
			saved, readErr := other.snapshot(ctx, "inventory")
			require.NoError(t, readErr)
			switch transition {
			case replace:
				require.ErrorIs(t, err, errAdmissionRetired)
				assert.NotEqual(t, first.RegistrationToken, saved.RegistrationToken)
				assert.NotContains(t, saved.ProviderLeases, providerLeaseKey("new", testIncarnationB))
			case retire:
				require.ErrorIs(t, err, errAdmissionRetired)
				assert.Equal(t, catalogEntryRetired, saved.State)
				assert.Empty(t, saved.ProviderLeases)
			case release:
				require.NoError(t, err)
				assert.Equal(t, first.RegisteredAt, attached.RegisteredAt)
				assert.Len(t, saved.ProviderLeases, 1)
				assert.Contains(t, saved.ProviderLeases, providerLeaseKey("new", testIncarnationB))
				assert.Zero(t, saved.LastPongUnixNano)
			}
			require.NoError(t, newToolsetCatalog(store, clock).validatePersistedEntries(ctx))
		})
	}
}

func TestRedisFirstRegisterAndDeclarationSerialize(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	clock := newRedisTimeSource(rdb)
	other := newToolsetCatalog(store, clock)
	definition := testCatalogDefinition(t, testCatalogToolset("inventory", "inventory", nil))
	competing := &competingCatalogStore{catalogStore: store}
	var registered catalogState
	competing.beforeCommit = func() {
		var err error
		registered, err = other.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
		require.NoError(t, err)
	}
	declared, err := newToolsetCatalog(competing, clock).DeclareService(ctx, definition)
	require.NoError(t, err)
	assert.Equal(t, registered, declared.catalogState)
	assert.Equal(t, testAdmissionRevisionA, declared.AdmissionRevision)
	_, err = other.AttachProvider(ctx, "inventory", declared.RegistrationToken, "second", testIncarnationB, time.Minute)
	require.NoError(t, err)
	_, err = other.Register(ctx, definition, testAdmissionRevisionB, "deployment-provider", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errAdmissionBlocked, "attachment wins before a different deployment can replace the declaration")
}

func TestRedisAttachmentGeneratedValidationAndSavedCompatibility(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	reg, err := New(ctx, Config{Redis: rdb, Name: t.Name()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	client, raw := startServiceAndClients(t, svc)
	legacy := validRegisterPayloadForSchemaAdmission("legacy")
	old, err := client.Register(ctx, legacy)
	require.NoError(t, err)
	native, err := client.RegisterAgentToolset(ctx, testAgentToolset("revision/1"))
	require.NoError(t, err)
	store := svc.catalog.store.(*redisCatalogStore)
	beforeState, err := rdb.HGetAll(ctx, store.state).Result()
	require.NoError(t, err)
	beforeDefinition, err := rdb.HGetAll(ctx, store.definitions).Result()
	require.NoError(t, err)
	declared, err := client.DeclareServiceToolset(ctx, testServiceDeclaration())
	require.NoError(t, err)
	require.NoError(t, newToolsetCatalog(store, newRedisTimeSource(rdb)).validatePersistedEntries(ctx))
	for _, saved := range []*genregistry.ResolvedToolset{
		{Toolset: &genregistry.Toolset{Name: legacy.Name}, RegistrationToken: old.RegistrationToken}, native,
	} {
		key := toolsetCatalogKey(saved.Toolset.Name)
		state, err := rdb.HGet(ctx, store.state, key).Result()
		require.NoError(t, err)
		assert.Equal(t, beforeState[key], state)
		definition, err := rdb.HGet(ctx, store.definitions, key).Result()
		require.NoError(t, err)
		assert.Equal(t, beforeDefinition[key], definition)
	}
	payload := &genregistry.AttachProviderPayload{
		Name: declared.Toolset.Name, ExpectedRegistrationToken: declared.RegistrationToken,
		ProviderID: "provider", ProviderIncarnationID: testIncarnationA,
		WireProtocolVersion: toolregistry.WireProtocolVersion,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*registrypb.AttachProviderRequest)
	}{
		{"missing wire", func(p *registrypb.AttachProviderRequest) { p.WireProtocolVersion = nil }},
		{"wrong wire", func(p *registrypb.AttachProviderRequest) {
			p.WireProtocolVersion = new(int32(toolregistry.WireProtocolVersion + 1))
		}},
		{"missing token", func(p *registrypb.AttachProviderRequest) { p.ExpectedRegistrationToken = nil }},
		{"bad incarnation", func(p *registrypb.AttachProviderRequest) { p.ProviderIncarnationId = strPtr("bad") }},
		{"bad provider", func(p *registrypb.AttachProviderRequest) { p.ProviderId = strPtr("bad\x00provider") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := grpcclient.NewProtoAttachProviderRequest(payload)
			tc.mutate(request)
			_, err := raw.AttachProvider(ctx, request)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
	for _, selection := range []struct{ name, token string }{
		{"missing", declared.RegistrationToken}, {declared.Toolset.Name, testStaleToken}, {native.Toolset.Name, native.RegistrationToken},
	} {
		wrong := *payload
		wrong.Name, wrong.ExpectedRegistrationToken = selection.name, selection.token
		_, err := client.AttachProvider(ctx, &wrong)
		requireServiceErrorName(t, err, "admission_conflict")
	}
	attached, err := client.AttachProvider(ctx, payload)
	require.NoError(t, err)
	assert.Equal(t, declared.Toolset.RegisteredAt, attached.RegisteredAt)
	assert.Equal(t, declared.RegistrationToken, attached.RegistrationToken)
	require.NoError(t, client.DrainProvider(ctx, &genregistry.DrainProviderPayload{
		Name: payload.Name, ProviderID: payload.ProviderID, ProviderIncarnationID: payload.ProviderIncarnationID,
		ExpectedRegistrationToken: payload.ExpectedRegistrationToken, SettlementDurationMs: time.Minute.Milliseconds(),
	}))
	_, err = client.AttachProvider(ctx, payload)
	requireServiceErrorName(t, err, "provider_lease_lost")
	require.NoError(t, client.Unregister(ctx, &genregistry.UnregisterPayload{Name: payload.Name, ExpectedRegistrationToken: payload.ExpectedRegistrationToken}))
	_, err = client.AttachProvider(ctx, payload)
	requireServiceErrorName(t, err, "admission_retired")
}
