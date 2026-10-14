//go:build integration

// Protocol upgrades preserve the exact saved catalog and completed results.
// Historical registrations remain readable but cannot grant current provider
// authority; an owned replacement creates the new executable registration.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	frameworkruntime "goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/provider"
)

func TestCatalogProtocolUpgradePreservesHistoryAndReplacesAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	rdb := getRedis(t)
	const registryName = "protocol-upgrade"
	store := newRedisCatalogStore(rdb, registryName)
	active := seedProtocol14Catalog(t, store, "inventory", false, true)
	retired := seedProtocol14Catalog(t, store, "retired.inventory", true, false)
	toRetire := seedProtocol14Catalog(t, store, "retire.inventory", false, false)
	activeRaw, definitionRaw, _, _, err := store.Snapshot(ctx, toolsetCatalogKey("inventory"))
	require.NoError(t, err)
	retiredRaw, retiredDefinition, _, _, err := store.Snapshot(ctx, toolsetCatalogKey("retired.inventory"))
	require.NoError(t, err)
	oldCall := &genregistry.CallToolPayload{Toolset: "inventory", Tool: "inventory.lookup", PayloadJSON: []byte(`{"value":7}`), WireProtocolVersion: toolregistry.WireProtocolVersion, Meta: &genregistry.ToolCallMeta{RunID: "completed-old-run", ToolCallID: "read", SessionID: new("old-session")}}
	admissions := newCallAdmissionStore(rdb, registryName)
	completedKey := seedProtocol14CompletedCall(t, admissions, oldCall, active.RegistrationToken)
	completedBefore, err := rdb.HGetAll(ctx, completedKey).Result()
	require.NoError(t, err)
	completedBytes, err := json.Marshal(completedBefore)
	require.NoError(t, err)

	reg, err := New(ctx, Config{Redis: rdb, Name: registryName, PingInterval: 50 * time.Millisecond, MissedPingThreshold: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	client, _ := startServiceAndClients(t, reg.Service())
	require.NoError(t, client.Unregister(ctx, &genregistry.UnregisterPayload{Name: "retire.inventory", ExpectedRegistrationToken: toRetire.RegistrationToken}))
	retiredAfter, err := reg.Service().catalog.snapshot(ctx, "retire.inventory")
	require.NoError(t, err)
	assert.Equal(t, catalogEntryRetired, retiredAfter.State)
	assert.Equal(t, 14, retiredAfter.WireProtocolVersion)
	assert.Equal(t, toRetire.RegistrationToken, retiredAfter.RegistrationToken)
	resolved, err := client.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: "inventory"})
	require.NoError(t, err)
	assert.Equal(t, active.RegistrationToken, resolved.RegistrationToken)
	list, err := client.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
	require.NoError(t, err)
	require.Len(t, list.Toolsets, 1)
	ready, err := client.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{Name: "inventory", ExpectedRegistrationToken: active.RegistrationToken})
	require.NoError(t, err)
	assert.False(t, ready.Ready)

	// Old callers are rejected at generated transport before opening a stream.
	oldCall.WireProtocolVersion = 14
	_, err = client.CallTool(ctx, oldCall)
	require.Error(t, err)
	registration := validRegisterPayloadForSchemaAdmission("blocked-old-provider")
	registration.WireProtocolVersion = 14
	_, err = client.Register(ctx, registration)
	require.Error(t, err)
	attach := &genregistry.AttachProviderPayload{Name: "inventory", ExpectedRegistrationToken: active.RegistrationToken, ProviderID: "new-provider", ProviderIncarnationID: testIncarnationB, WireProtocolVersion: 14}
	_, err = client.AttachProvider(ctx, attach)
	require.Error(t, err)
	attach.WireProtocolVersion = toolregistry.WireProtocolVersion
	_, err = client.AttachProvider(ctx, attach)
	requireServiceErrorName(t, err, "admission_conflict")
	_, err = reg.Service().catalog.AttachProvider(ctx, "inventory", active.RegistrationToken, "new-provider", testIncarnationB, time.Minute)
	require.ErrorIs(t, err, errAdmissionConflict)
	_, err = client.RenewProvider(ctx, &genregistry.RenewProviderPayload{Name: "inventory", ExpectedRegistrationToken: active.RegistrationToken, ProviderID: "old-provider", ProviderIncarnationID: testIncarnationA})
	requireServiceErrorName(t, err, "provider_lease_lost")
	fresh := *oldCall
	fresh.WireProtocolVersion = toolregistry.WireProtocolVersion
	fresh.Meta = &genregistry.ToolCallMeta{RunID: "new-run", ToolCallID: "read"}
	_, err = client.CallTool(ctx, &fresh)
	requireServiceErrorName(t, err, "validation_error")
	requests, err := rdb.Keys(ctx, "pulse:stream:*").Result()
	require.NoError(t, err)
	assert.Empty(t, requests)

	// Replaying a completed old invocation reads its exact terminal through the
	// current request codec, saved call decoder and result-stream restoration.
	oldCall.WireProtocolVersion = toolregistry.WireProtocolVersion
	replay, err := client.CallTool(ctx, oldCall)
	require.NoError(t, err)
	assert.Equal(t, active.RegistrationToken, replay.RegistrationToken)
	events, err := rdb.XRange(ctx, pulseStreamKeyPrefix+toolregistry.ResultStreamID(replay.ToolUseID), "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, events, 1)
	body, ok := events[0].Values["p"].(string)
	require.True(t, ok)
	result, err := toolregistry.DecodeToolResultMessage([]byte(body))
	require.NoError(t, err)
	require.NoError(t, toolregistry.ValidateToolResultMessage(result))
	assert.JSONEq(t, `{"value":7}`, string(result.Result))
	assert.Equal(t, active.RegistrationToken, result.RegistrationToken)
	after, err := rdb.HGetAll(ctx, completedKey).Result()
	require.NoError(t, err)
	// Recreating a missing result stream changes only its event pointer.
	delete(after, "terminal_event_id")
	delete(completedBefore, "terminal_event_id")
	completedBytes, err = json.Marshal(completedBefore)
	require.NoError(t, err)
	afterBytes, err := json.Marshal(after)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(completedBytes), sha256.Sum256(afterBytes))
	currentRaw, currentDefinition, _, _, err := store.Snapshot(ctx, toolsetCatalogKey("inventory"))
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256([]byte(activeRaw)), sha256.Sum256([]byte(currentRaw)))
	assert.Equal(t, sha256.Sum256([]byte(definitionRaw)), sha256.Sum256([]byte(currentDefinition)))

	// Provider-side atomic transitions cannot claim or publish using historical
	// authority, even when an old unexpired lease remains in the saved catalog.
	lease := providerLeaseKey("old-provider", testIncarnationA)
	_, err = admissions.Claim(ctx, "inventory", replay.ToolUseID, active.RegistrationToken, active.RegistrationToken, lease, "1-0", uuid.NewString(), toolregistry.ResultStreamID(replay.ToolUseID), []byte(`{}`))
	require.ErrorIs(t, err, errToolsetNotFound)
	err = admissions.PublishLiveEvent(ctx, "inventory", replay.ToolUseID, active.RegistrationToken, active.RegistrationToken, lease, "1-0", toolregistry.ResultStreamID(replay.ToolUseID), toolregistry.OutputDeltaEventKey, []byte(`{}`))
	require.ErrorIs(t, err, errToolsetNotFound)
	err = admissions.ReportOverload(ctx, "inventory", replay.ToolUseID, active.RegistrationToken, active.RegistrationToken, lease, "1-0", toolregistry.ResultStreamID(replay.ToolUseID), []byte(`{}`), []byte(`{}`))
	require.ErrorIs(t, err, errToolsetNotFound)
	_, err = admissions.Complete(ctx, "inventory", replay.ToolUseID, active.RegistrationToken, active.RegistrationToken, lease, "1-0", toolregistry.ResultStreamID(replay.ToolUseID), []byte(body))
	require.ErrorIs(t, err, errToolsetNotFound)
	pending, _, err := admissions.Ensure(ctx, "inventory", toolregistry.DeriveToolUseID("old-pending", "call", 0), active.RegistrationToken, "pending-digest", time.Minute, 2*time.Minute, outcomeUnknownPayload(active.RegistrationToken, toolregistry.DeriveToolUseID("old-pending", "call", 0)), false)
	require.NoError(t, err)
	_, err = publishAdmittedBounded(ctx, rdb, "upgrade-blocked-request", maxQueuedToolCalls, string(toolregistry.MessageTypeCall), []byte(`{}`), pending, "")
	require.ErrorIs(t, err, errRoutingUnavailable)
	assert.Zero(t, rdb.Exists(ctx, pulseStreamKeyPrefix+"upgrade-blocked-request").Val())

	input := testServiceDeclaration()
	replacement := &genregistry.ReplaceServiceToolsetPayload{Name: input.Name, Tags: input.Tags, Tools: input.Tools, ExpectedRegistrationToken: active.RegistrationToken, ReplacementID: uuid.NewString()}
	_, err = client.ReplaceServiceToolset(ctx, replacement)
	requireServiceErrorName(t, err, "admission_blocked")
	require.NoError(t, client.DrainProvider(ctx, &genregistry.DrainProviderPayload{Name: "inventory", ProviderID: "old-provider", ProviderIncarnationID: testIncarnationA, ExpectedRegistrationToken: active.RegistrationToken, SettlementDurationMs: time.Minute.Milliseconds()}))
	require.NoError(t, client.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{Name: "inventory", ProviderID: "old-provider", ProviderIncarnationID: testIncarnationA, ExpectedRegistrationToken: active.RegistrationToken}))
	updated, err := client.ReplaceServiceToolset(ctx, replacement)
	require.NoError(t, err)
	assert.NotEqual(t, active.RegistrationToken, updated.RegistrationToken)
	historical, err := store.Retired(ctx, active.RegistrationToken)
	require.NoError(t, err)
	assert.True(t, historical)
	historical, err = store.Retired(ctx, retired.RegistrationToken)
	require.NoError(t, err)
	assert.True(t, historical)
	savedRetired, savedRetiredDefinition, _, _, err := store.Snapshot(ctx, toolsetCatalogKey("retired.inventory"))
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256([]byte(retiredRaw)), sha256.Sum256([]byte(savedRetired)))
	assert.Equal(t, sha256.Sum256([]byte(retiredDefinition)), sha256.Sum256([]byte(savedRetiredDefinition)))
	current, err := reg.Service().catalog.activeState(ctx, "inventory")
	require.NoError(t, err)
	assert.Equal(t, toolregistry.WireProtocolVersion, current.WireProtocolVersion)
	replayAfter, err := client.CallTool(ctx, oldCall)
	require.NoError(t, err)
	assert.Equal(t, replay, replayAfter)
	completeUpgradedStandaloneCall(t, ctx, rdb, client, updated)
}

// seedProtocol14Catalog writes the unchanged catalog format with a token
// derived from protocol 14, as an older registry would have saved it.
func seedProtocol14Catalog(t *testing.T, store *redisCatalogStore, name string, retired, leased bool) catalogState {
	t.Helper()
	declaration := testServiceDeclaration()
	declaration.Name = name
	definition := testCatalogDefinition(t, &genregistry.Toolset{Name: name, Tags: declaration.Tags, Tools: declaration.Tools})
	token, err := admissionRegistrationToken(definition.fingerprint, testAdmissionRevisionA, 14)
	require.NoError(t, err)
	state := newCatalogState(definition, testAdmissionRevisionA, token, time.Now().UTC())
	state.WireProtocolVersion = 14
	if leased {
		state.ProviderLeases[providerLeaseKey("old-provider", testIncarnationA)] = providerLease{ExpiresAtUnixMilli: time.Now().Add(time.Minute).UnixMilli()}
		state.LastPongUnixNano = time.Now().UnixNano()
	}
	write := catalogWrite{Definition: definition.raw, CandidateToken: token}
	if retired {
		state.State = catalogEntryRetired
		write.RetireToken = token
	}
	write.State, err = marshalCatalogState(state)
	require.NoError(t, err)
	saved, err := store.Commit(t.Context(), toolsetCatalogKey(name), "", write)
	require.NoError(t, err)
	require.True(t, saved)
	return state
}

// seedProtocol14CompletedCall saves the exact prepared/terminal fields that an
// old completed call retains. It does not execute under the new registry.
func seedProtocol14CompletedCall(t *testing.T, store *callAdmissionStore, call *genregistry.CallToolPayload, token string) string {
	t.Helper()
	prepared, err := prepareToolCallIdentity(call.Toolset, call.Tool, call.PayloadJSON, call.Meta)
	require.NoError(t, err)
	admission, _, err := store.Ensure(t.Context(), call.Toolset, prepared.toolUseID, token, prepared.admissionDigest, time.Minute, 2*time.Minute, outcomeUnknownPayload(token, prepared.toolUseID), false)
	require.NoError(t, err)
	result := toolregistry.NewToolResultMessage(token, prepared.toolUseID, json.RawMessage(`{"value":7}`))
	body, err := json.Marshal(result)
	require.NoError(t, err)
	digest := sha256.Sum256(body)
	require.NoError(t, store.redis.HSet(t.Context(), admission.key, map[string]any{"published": "1", "publication_event_id": "1-0", "claim:1-0": "1", "terminal": "1", "terminal_event_id": "2-0", "terminal_digest": hex.EncodeToString(digest[:]), "terminal_payload": string(body), "terminal_cause": "provider"}).Err())
	return admission.key
}

// completeUpgradedStandaloneCall starts a real provider for the replaced
// registration and proves the ordinary sessionless workflow receives its result.
func completeUpgradedStandaloneCall(t *testing.T, ctx context.Context, rdb *redis.Client, client *genregistry.Client, selected *genregistry.ResolvedToolset) {
	t.Helper()
	pulse, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	require.NoError(t, err)
	handler := &registeredServiceHandler{received: make(chan toolregistry.ToolCallMessage, 1)}
	serveCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- provider.Serve(serveCtx, pulse, selected.Toolset.Name, handler, attachedTestRegistration(client, selected.RegistrationToken), provider.Options{ProviderID: "upgraded-provider", Pong: func(ctx context.Context, id, incarnation, ping string) error {
			return client.Pong(ctx, &genregistry.PongPayload{Toolset: selected.Toolset.Name, ProviderID: id, ProviderIncarnationID: incarnation, PingID: ping})
		}})
	}()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			assert.ErrorIs(t, err, context.Canceled)
		case <-time.After(provider.DefaultShutdownTimeout + provider.DefaultRegistrationReleaseTimeout + time.Second):
			t.Error("upgraded provider did not stop")
		}
	})
	require.Eventually(t, func() bool {
		ready, err := client.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{Name: selected.Toolset.Name, ExpectedRegistrationToken: selected.RegistrationToken})
		return err == nil && ready.Ready
	}, 10*time.Second, 20*time.Millisecond)
	runtime := frameworkruntime.New(storageinmem.New(), frameworkruntime.WithEngine(engineinmem.New()), frameworkruntime.WithToolWorkflowQueue("upgraded-worker"))
	require.NoError(t, runtime.RegisterRegistry("registry", client, pulse))
	require.NoError(t, runtime.RegisterToolWorkflow(ctx))
	invocation := &genregistry.RegisteredInvocation{Invocation: &genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "upgraded-run"})}, Target: &genregistry.CatalogOperation{Scope: "inventory-provider", Name: "inventory", Target: genregistry.NewCatalogTargetService(&genregistry.ServiceCatalogTarget{Toolset: selected.Toolset.Name, Tool: "inventory.lookup", RegistrationToken: selected.RegistrationToken})}, Arguments: []byte(`{"value":7}`)}
	handle, err := runtime.StartOneShotToolOperation(ctx, "registry", selected, invocation, nil, nil)
	require.NoError(t, err)
	result, err := handle.Wait(ctx)
	require.NoError(t, err)
	require.NotNil(t, result.FinalToolResult)
	assert.Nil(t, result.FinalToolResult.Failure)
	assert.JSONEq(t, `{"value":7}`, string(result.FinalToolResult.Result))
	message := <-handler.received
	assert.Empty(t, message.Meta.SessionID)
	assert.EqualValues(t, 1, handler.calls.Load())
}
