//go:build integration

package registry

// Independent registry readers must return the saved definition even after
// replacements reuse its semantic fingerprint, native token, or timestamp.

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestRedisDefinitionSnapshotAfterServiceReplacementCycle(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	clock := newRedisTimeSource(rdb)
	writer := newToolsetCatalog(store, clock)
	reader := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	client, _ := startServiceAndClients(t, reader)
	input := testServiceDeclaration()
	first, err := client.DeclareServiceToolset(ctx, input)
	require.NoError(t, err)
	_, err = client.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: input.Name})
	require.NoError(t, err)
	changed := testServiceDeclaration()
	changed.Tools[0].ConsumerContract.Title = "Replacement instruction"
	middle, err := writer.Register(ctx, testCatalogDefinition(t, &genregistry.Toolset{
		Name: changed.Name, Tags: changed.Tags, Tools: changed.Tools,
	}), uuid.NewString(), "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	require.NoError(t, writer.ReleaseProvider(ctx, input.Name, "provider", testIncarnationA, middle.RegistrationToken))
	slices.Reverse(input.Tools)
	slices.Reverse(input.Tags)
	last, err := writer.Register(ctx, testCatalogDefinition(t, &genregistry.Toolset{
		Name: input.Name, Tags: input.Tags, Tools: input.Tools,
	}), uuid.NewString(), "provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, first.RegistrationToken, last.RegistrationToken)
	expected, err := newToolsetCatalog(store, clock).GetToolset(ctx, input.Name)
	require.NoError(t, err)
	replay, err := client.DeclareServiceToolset(ctx, testServiceDeclaration())
	require.NoError(t, err)
	assert.Equal(t, expected, replay.Toolset)
	assert.Equal(t, last.RegistrationToken, replay.RegistrationToken)
	resolved, err := client.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: input.Name})
	require.NoError(t, err)
	assert.Equal(t, expected, resolved.Toolset)
	got, err := client.GetToolset(ctx, &genregistry.GetToolsetPayload{Name: input.Name})
	require.NoError(t, err)
	assert.Equal(t, expected, got)
	retired, err := store.Retired(ctx, first.RegistrationToken)
	require.NoError(t, err)
	assert.True(t, retired)
}

func TestRedisNativeSnapshotWithReusedTokenAndTime(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(map[bool]string{false: "equivalent replacement", true: "replacement cycle"}[cycle], func(t *testing.T) {
			ctx := t.Context()
			rdb := getRedis(t)
			store := newRedisCatalogStore(rdb, t.Name())
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			writer := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
			reader := &Service{catalog: newToolsetCatalog(store, clock)}
			writeClient, _ := startServiceAndClients(t, writer)
			readClient, _ := startServiceAndClients(t, reader)
			input := testAgentToolset("revision/1")
			input.Tools[0].Tags = []string{"first", "second"}
			first, err := writeClient.RegisterAgentToolset(ctx, input)
			require.NoError(t, err)
			_, err = readClient.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: input.Name})
			require.NoError(t, err)
			before, definitionBefore, _, _, err := store.Snapshot(ctx, toolsetCatalogKey(input.Name))
			require.NoError(t, err)
			expectedToken := first.RegistrationToken
			if cycle {
				middle := testAgentToolset("revision/2")
				replaced, err := writeClient.ReplaceAgentToolset(ctx, &genregistry.ReplaceAgentToolsetPayload{
					Name: middle.Name, Tools: middle.Tools, ExpectedRegistrationToken: expectedToken,
				})
				require.NoError(t, err)
				expectedToken = replaced.RegistrationToken
			}
			slices.Reverse(input.Tools[0].Tags)
			last, err := writeClient.ReplaceAgentToolset(ctx, &genregistry.ReplaceAgentToolsetPayload{
				Name: input.Name, Tools: input.Tools, ExpectedRegistrationToken: expectedToken,
			})
			require.NoError(t, err)
			assert.Equal(t, first.RegistrationToken, last.RegistrationToken)
			assert.Equal(t, first.Toolset.RegisteredAt, last.Toolset.RegisteredAt)
			after, definitionAfter, _, _, err := store.Snapshot(ctx, toolsetCatalogKey(input.Name))
			require.NoError(t, err)
			require.Equal(t, before, after, "even the compact state is identical")
			require.NotEqual(t, definitionBefore, definitionAfter)
			resolved, err := readClient.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: input.Name})
			require.NoError(t, err)
			assert.Equal(t, last, resolved)
			got, err := readClient.GetToolset(ctx, &genregistry.GetToolsetPayload{Name: input.Name})
			require.NoError(t, err)
			assert.Equal(t, last.Toolset, got)
		})
	}
}

// delayedJoinClock supplies one already-read instant, then actual Redis time.
// Redis still checks its own current time inside the conditional write.
type delayedJoinClock struct {
	registryTimeSource
	first time.Time
}

func (c *delayedJoinClock) Now(ctx context.Context) (time.Time, error) {
	if !c.first.IsZero() {
		first := c.first
		c.first = time.Time{}
		return first, nil
	}
	return c.registryTimeSource.Now(ctx)
}

func TestRedisProviderJoinRejectsExpiredHealthAtCommit(t *testing.T) {
	for _, operation := range []string{attachJoinOperation, "register"} {
		t.Run(operation, func(t *testing.T) {
			ctx := t.Context()
			rdb := getRedis(t)
			// The scheduler remains wired but its tick cannot consume the
			// deliberately delayed clock read during this focused transition.
			reg, err := New(ctx, Config{Redis: rdb, Name: t.Name(), PingInterval: time.Hour})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
			svc := reg.Service()
			store := svc.catalog.store.(*redisCatalogStore)
			definition := testCatalogDefinition(t, testDefinitionToolset())
			first, err := svc.catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
			require.NoError(t, err)
			now, err := rdb.Time(ctx).Result()
			require.NoError(t, err)
			first.ProviderLeases[providerLeaseKey("provider", testIncarnationA)] = providerLease{ExpiresAtUnixMilli: now.Add(-time.Second).UnixMilli()}
			first.LastPongUnixNano = now.Add(-2 * time.Second).UnixNano()
			raw, err := marshalCatalogState(first)
			require.NoError(t, err)
			require.NoError(t, rdb.HSet(ctx, store.state, toolsetCatalogKey("tools"), raw).Err())
			observed := &observedRetryStore{catalogStore: store, afterRetry: func() {
				unchanged, err := rdb.HGet(ctx, store.state, toolsetCatalogKey("tools")).Result()
				if err != nil {
					t.Errorf("read state after rejected write: %v", err)
					return
				}
				assert.Equal(t, raw, unchanged, "time invalidation must write nothing")
			}}
			svc.catalog.store = observed
			svc.catalog.clock = &delayedJoinClock{registryTimeSource: newRedisTimeSource(rdb), first: now.Add(-2 * time.Second)}
			client, _ := startServiceAndClients(t, svc)
			var result *genregistry.RegisterResult
			if operation == attachJoinOperation {
				result, err = client.AttachProvider(ctx, &genregistry.AttachProviderPayload{
					Name: "tools", ExpectedRegistrationToken: first.RegistrationToken, ProviderID: "provider",
					ProviderIncarnationID: testIncarnationB, WireProtocolVersion: toolregistry.WireProtocolVersion,
				})
			} else {
				input := testDefinitionToolset()
				result, err = client.Register(ctx, &genregistry.RegisterPayload{
					Name: input.Name, Description: input.Description, Version: input.Version, Tags: input.Tags, Tools: input.Tools,
					AdmissionRevision: testAdmissionRevisionA, ProviderID: "provider", ProviderIncarnationID: testIncarnationB,
					WireProtocolVersion: toolregistry.WireProtocolVersion, SchemaFingerprint: definition.fingerprint,
				})
			}
			require.NoError(t, err)
			assert.Equal(t, 1, observed.retries)
			assert.Equal(t, first.RegisteredAt, result.RegisteredAt)
			assert.Equal(t, first.RegistrationToken, result.RegistrationToken)
			joined, err := svc.catalog.activeState(ctx, "tools")
			require.NoError(t, err)
			assert.Greater(t, joined.HealthEpoch, first.HealthEpoch)
			assert.Zero(t, joined.LastPongUnixNano)
			check := &genregistry.CheckAdmissionPayload{Name: "tools", ExpectedRegistrationToken: first.RegistrationToken}
			ready, err := client.CheckAdmission(ctx, check)
			require.NoError(t, err)
			assert.False(t, ready.Ready)
			require.NoError(t, client.Pong(ctx, &genregistry.PongPayload{
				Toolset: "tools", ProviderID: "provider", ProviderIncarnationID: testIncarnationB,
				PingID: newPingID(first.RegistrationToken, first.HealthEpoch),
			}))
			ready, err = client.CheckAdmission(ctx, check)
			require.NoError(t, err)
			assert.False(t, ready.Ready)
			require.NoError(t, client.Pong(ctx, &genregistry.PongPayload{
				Toolset: "tools", ProviderID: "provider", ProviderIncarnationID: testIncarnationB,
				PingID: newPingID(first.RegistrationToken, joined.HealthEpoch),
			}))
			ready, err = client.CheckAdmission(ctx, check)
			require.NoError(t, err)
			assert.True(t, ready.Ready)
		})
	}
}
