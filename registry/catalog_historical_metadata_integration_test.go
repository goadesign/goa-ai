//go:build integration

// A synthetic earlier consumer contract must not prevent accepted-result
// recovery or normal replacement with a current executable declaration.
package registry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestHistoricalMetadataStartupReplayAndReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	rdb := getRedis(t)
	const name = "historical-metadata"
	store := newRedisCatalogStore(rdb, name)
	declaration := testServiceDeclaration()
	declaration.Tools = declaration.Tools[:1]
	toolset := &genregistry.Toolset{Name: declaration.Name, Tags: declaration.Tags, Tools: declaration.Tools}
	definition := testCatalogDefinition(t, toolset)
	current, err := json.Marshal(toolset.Tools[0].ConsumerContract)
	require.NoError(t, err)
	const oldContract = `{ "Kind":"service", "Bounds":{"Paging":{"ReplayPayload":true}} }`
	definition.raw = strings.Replace(definition.raw, `"ConsumerContract":`+string(current), `"ConsumerContract":`+oldContract, 1)
	definition.fingerprint = savedFixtureFingerprint(toolset, []byte(oldContract))
	token, err := admissionRegistrationToken(definition.fingerprint, testAdmissionRevisionA, 13)
	require.NoError(t, err)
	state := newCatalogState(definition, testAdmissionRevisionA, token, time.Now().UTC())
	state.WireProtocolVersion = 13
	raw, err := marshalCatalogState(state)
	require.NoError(t, err)
	written, err := store.Commit(ctx, toolsetCatalogKey(toolset.Name), "", catalogWrite{State: raw, Definition: definition.raw, CandidateToken: token})
	require.NoError(t, err)
	require.True(t, written)
	call := &genregistry.CallToolPayload{
		Toolset: "inventory", Tool: "inventory.lookup", PayloadJSON: []byte(`{"value":7}`),
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		Meta:                &genregistry.ToolCallMeta{RunID: "completed-history", ToolCallID: "read", SessionID: new("session")},
	}
	admissions := newCallAdmissionStore(rdb, name)
	completedKey := seedProtocol14CompletedCall(t, admissions, call, token)
	before, err := rdb.HGetAll(ctx, completedKey).Result()
	require.NoError(t, err)
	reg, err := New(ctx, Config{Redis: rdb, Name: name, PingInterval: 50 * time.Millisecond, MissedPingThreshold: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	client, _ := startServiceAndClients(t, reg.Service())
	inspected, err := client.GetToolsetSchemas(ctx, &genregistry.GetToolsetPayload{Name: toolset.Name})
	require.NoError(t, err)
	assert.Equal(t, toolset.Name, inspected.Name)
	assert.Equal(t, toolset.Tags, inspected.Tags)
	assert.Equal(t, state.RegisteredAt, inspected.RegisteredAt)
	require.Len(t, inspected.Tools, 1)
	assert.Equal(t, toolset.Tools[0].Name, inspected.Tools[0].Name)
	assert.Equal(t, toolset.Tools[0].PayloadSchema, inspected.Tools[0].PayloadSchema)
	assert.Equal(t, toolset.Tools[0].ResultSchema, inspected.Tools[0].ResultSchema)
	_, err = client.GetToolsetSchemas(ctx, &genregistry.GetToolsetPayload{Name: "missing"})
	requireServiceErrorName(t, err, "not_found")
	replayed, err := client.CallTool(ctx, call)
	require.NoError(t, err)
	assert.Equal(t, token, replayed.RegistrationToken)
	after, err := rdb.HGetAll(ctx, completedKey).Result()
	require.NoError(t, err)
	delete(before, "terminal_event_id")
	delete(after, "terminal_event_id")
	assert.Equal(t, before, after)
	savedState, savedDefinition, _, _, err := store.Snapshot(ctx, toolsetCatalogKey(toolset.Name))
	require.NoError(t, err)
	assert.Equal(t, raw, savedState)
	assert.Equal(t, definition.raw, savedDefinition)
	_, err = client.AttachProvider(ctx, &genregistry.AttachProviderPayload{
		Name: toolset.Name, ExpectedRegistrationToken: token, ProviderID: "new-provider",
		ProviderIncarnationID: testIncarnationB, WireProtocolVersion: toolregistry.WireProtocolVersion,
	})
	requireServiceErrorName(t, err, "service_unavailable")
	_, err = reg.Service().catalog.AttachProvider(ctx, toolset.Name, token, "new-provider", testIncarnationB, time.Minute)
	require.ErrorIs(t, err, errAdmissionConflict)
	// Full historical reads retain their existing strict response contract;
	// startup does not erase the removed field to manufacture a typed response.
	_, err = client.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: toolset.Name})
	require.Error(t, err)
	currentDefinition := testCatalogDefinition(t, toolset)
	_, err = client.Register(ctx, &genregistry.RegisterPayload{
		Name: declaration.Name, Tags: declaration.Tags, Tools: declaration.Tools,
		ProviderID: "registering-provider", ProviderIncarnationID: uuid.NewString(),
		AdmissionRevision: testAdmissionRevisionB, SchemaFingerprint: currentDefinition.fingerprint,
		WireProtocolVersion: toolregistry.WireProtocolVersion,
	})
	require.NoError(t, err)
	updated, err := client.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: toolset.Name})
	require.NoError(t, err)
	retired, err := store.Retired(ctx, token)
	require.NoError(t, err)
	assert.True(t, retired)
	assert.NotEqual(t, token, updated.RegistrationToken)
	completeUpgradedStandaloneCall(t, ctx, rdb, client, updated)
	replayedAfter, err := client.CallTool(ctx, call)
	require.NoError(t, err)
	assert.Equal(t, replayed, replayedAfter)
}
