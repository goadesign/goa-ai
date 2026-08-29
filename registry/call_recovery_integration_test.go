//go:build integration

package registry

// These live-Redis tests pin registry-owned recovery independently from Pulse
// retention: lost dispatch authority becomes one outcome_unknown terminal,
// overload state retries from the call record, and noisy output stays bounded.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	streamopts "goa.design/pulse/streaming/options"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestCallToolReturnsReadableResultStream(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("readable-result-stream-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{Redis: rdb, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	svc.healthTracker = newMockHealthTracker()

	const toolset = "readable-result-stream-toolset"
	_, err = svc.Register(ctx, validRegisterPayloadForSchemaAdmission(toolset))
	require.NoError(t, err)
	callPayload := transitionCallPayload(toolset, "readable")
	call, err := svc.CallTool(ctx, callPayload)
	require.NoError(t, err)
	requireReadableResultStream(t, ctx, rdb, call)

	resultStreamKey := pulseStreamKeyPrefix + toolregistry.ResultStreamID(call.ToolUseID)
	require.EqualValues(t, 1, rdb.Del(ctx, resultStreamKey+":lifecycle").Val())
	replayed, err := svc.CallTool(ctx, callPayload)
	require.NoError(t, err)
	assert.Equal(t, call, replayed)
	requireReadableResultStream(t, ctx, rdb, replayed)
}

func TestLostDispatchLeaseCommitsAndRestoresOutcomeUnknown(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("lost-dispatch-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{Redis: rdb, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	svc.healthTracker = newMockHealthTracker()

	const toolset = "lost-dispatch-toolset"
	provider := validRegisterPayloadForSchemaAdmission(toolset)
	admission, err := svc.Register(ctx, provider)
	require.NoError(t, err)
	callPayload := transitionCallPayload(toolset, "lost-dispatch")
	call, err := svc.CallTool(ctx, callPayload)
	require.NoError(t, err)
	store := svc.callAdmissions.(*callAdmissionStore)
	requestEventID := retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
	claim, err := svc.ClaimToolCall(ctx, &genregistry.ClaimToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ProviderRegistrationToken: admission.RegistrationToken,
		CallRegistrationToken:     admission.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		RequestEventID:            requestEventID,
		ClaimOperationID:          uuid.NewString(),
	})
	require.NoError(t, err)
	require.Equal(t, string(callClaimExecute), claim.Disposition)

	require.NoError(t, svc.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
		Name:                      toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ExpectedRegistrationToken: admission.RegistrationToken,
	}))
	callKey := store.callKey(call.ToolUseID)
	terminalJSON, err := rdb.HGet(ctx, callKey, "terminal_payload").Bytes()
	require.NoError(t, err)
	var terminal toolregistry.ToolResultMessage
	require.NoError(t, json.Unmarshal(terminalJSON, &terminal))
	require.NoError(t, toolregistry.ValidateToolResultMessage(terminal))
	require.NotNil(t, terminal.Error)
	assert.Equal(t, toolregistry.ToolErrorCodeOutcomeUnknown, terminal.Error.Code)
	assert.Equal(t, planner.FailureInternal, terminal.Error.Failure.Kind)
	assert.Equal(t, planner.RecoveryFinish, terminal.Error.Failure.Recovery.Action)
	assert.Contains(t, terminal.Error.Failure.Error.Message, "effect may have occurred")

	oldEventID, err := rdb.HGet(ctx, callKey, "terminal_event_id").Result()
	require.NoError(t, err)
	resultStreamKey := pulseStreamKeyPrefix + toolregistry.ResultStreamID(call.ToolUseID)
	require.EqualValues(t, 1, rdb.XDel(ctx, resultStreamKey, oldEventID).Val())
	require.EqualValues(t, 1, rdb.Del(ctx, resultStreamKey+":lifecycle").Val())
	replayed, err := svc.CallTool(ctx, callPayload)
	require.NoError(t, err)
	assert.Equal(t, call, replayed)
	requireReadableResultStream(t, ctx, rdb, replayed)
	newEventID, err := rdb.HGet(ctx, callKey, "terminal_event_id").Result()
	require.NoError(t, err)
	assert.NotEqual(t, oldEventID, newEventID)
	events, err := rdb.XRange(ctx, resultStreamKey, newEventID, newEventID).Result()
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, string(terminalJSON), events[0].Values["p"])

	require.EqualValues(t, 1, rdb.XDel(ctx, resultStreamKey, newEventID).Val())
	require.EqualValues(t, 1, rdb.Del(ctx, resultStreamKey+":lifecycle").Val())
	retried, err := svc.RetryTool(ctx, &genregistry.RetryToolPayload{
		Toolset:                   callPayload.Toolset,
		Tool:                      callPayload.Tool,
		PayloadJSON:               callPayload.PayloadJSON,
		Meta:                      callPayload.Meta,
		WireProtocolVersion:       toolregistry.WireProtocolVersion,
		ExpectedRegistrationToken: admission.RegistrationToken,
	})
	require.NoError(t, err)
	assert.Equal(t, call, retried)
	requireReadableResultStream(t, ctx, rdb, retried)
	retryEventID, err := rdb.HGet(ctx, callKey, "terminal_event_id").Result()
	require.NoError(t, err)
	assert.NotEqual(t, newEventID, retryEventID)
}

func TestOverloadRetryUsesLiveCallRecord(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("overload-retry-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{Redis: rdb, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	svc.healthTracker = newMockHealthTracker()

	const toolset = "overload-retry-toolset"
	provider := validRegisterPayloadForSchemaAdmission(toolset)
	admission, err := svc.Register(ctx, provider)
	require.NoError(t, err)
	callPayload := transitionCallPayload(toolset, "overload")
	call, err := svc.CallTool(ctx, callPayload)
	require.NoError(t, err)
	store := svc.callAdmissions.(*callAdmissionStore)
	requestEventID := retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
	require.NoError(t, svc.ReportToolCallOverload(ctx, &genregistry.ProviderToolCallClaimPayload{
		Toolset:                   toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ProviderRegistrationToken: admission.RegistrationToken,
		CallRegistrationToken:     admission.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		RequestEventID:            requestEventID,
	}))
	resultStreamKey := pulseStreamKeyPrefix + toolregistry.ResultStreamID(call.ToolUseID)
	overloadEventCount := rdb.XLen(ctx, resultStreamKey).Val()
	require.NoError(t, svc.ReportToolCallOverload(ctx, &genregistry.ProviderToolCallClaimPayload{
		Toolset:                   toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ProviderRegistrationToken: admission.RegistrationToken,
		CallRegistrationToken:     admission.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		RequestEventID:            requestEventID,
	}))
	assert.Equal(t, overloadEventCount, rdb.XLen(ctx, resultStreamKey).Val())
	require.EqualValues(t, 1, rdb.Del(ctx, resultStreamKey+":lifecycle").Val())
	retried, err := svc.RetryTool(ctx, &genregistry.RetryToolPayload{
		Toolset:                   callPayload.Toolset,
		Tool:                      callPayload.Tool,
		PayloadJSON:               callPayload.PayloadJSON,
		Meta:                      callPayload.Meta,
		WireProtocolVersion:       toolregistry.WireProtocolVersion,
		ExpectedRegistrationToken: admission.RegistrationToken,
	})
	require.NoError(t, err)
	assert.Equal(t, call, retried)
	requireReadableResultStream(t, ctx, rdb, retried)
	assert.EqualValues(
		t,
		2,
		rdb.XLen(ctx, pulseStreamKeyPrefix+toolregistry.ToolsetStreamID(toolset)).Val(),
	)
}

func TestOutputDeltasAreCountBoundedAndPostTerminalSuppressed(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("delta-bounds-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{Redis: rdb, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	svc.healthTracker = newMockHealthTracker()

	const toolset = "delta-bounds-toolset"
	provider := validRegisterPayloadForSchemaAdmission(toolset)
	admission, err := svc.Register(ctx, provider)
	require.NoError(t, err)
	call, err := svc.CallTool(ctx, transitionCallPayload(toolset, "deltas"))
	require.NoError(t, err)
	store := svc.callAdmissions.(*callAdmissionStore)
	requestEventID := retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
	claimPayload := &genregistry.ClaimToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ProviderRegistrationToken: admission.RegistrationToken,
		CallRegistrationToken:     admission.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		RequestEventID:            requestEventID,
		ClaimOperationID:          uuid.NewString(),
	}
	claim, err := svc.ClaimToolCall(ctx, claimPayload)
	require.NoError(t, err)
	require.Equal(t, string(callClaimExecute), claim.Disposition)
	deltaPayload := &genregistry.PublishToolOutputDeltaPayload{
		Toolset:                   claimPayload.Toolset,
		ProviderID:                claimPayload.ProviderID,
		ProviderIncarnationID:     claimPayload.ProviderIncarnationID,
		ProviderRegistrationToken: claimPayload.ProviderRegistrationToken,
		CallRegistrationToken:     claimPayload.CallRegistrationToken,
		ToolUseID:                 claimPayload.ToolUseID,
		RequestEventID:            claimPayload.RequestEventID,
		Stream:                    "stdout",
		Delta:                     "x",
	}
	for range toolregistry.MaxToolOutputDeltaCount + 10 {
		require.NoError(t, svc.PublishToolOutputDelta(ctx, deltaPayload))
	}
	count, err := rdb.HGet(ctx, store.callKey(call.ToolUseID), "output_delta_count").Result()
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(toolregistry.MaxToolOutputDeltaCount), count)

	result := toolregistry.NewToolResultMessage(
		admission.RegistrationToken,
		call.ToolUseID,
		json.RawMessage(`{"ok":true}`),
	)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, svc.CompleteToolCall(ctx, &genregistry.CompleteToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		RegistrationToken:         admission.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		ResultJSON:                resultJSON,
		RequestEventID:            requestEventID,
		ProviderRegistrationToken: admission.RegistrationToken,
	}))
	streamKey := pulseStreamKeyPrefix + toolregistry.ResultStreamID(call.ToolUseID)
	before := rdb.XLen(ctx, streamKey).Val()
	require.NoError(t, svc.PublishToolOutputDelta(ctx, deltaPayload))
	assert.Equal(t, before, rdb.XLen(ctx, streamKey).Val())
	assert.LessOrEqual(t, before, int64(toolregistry.ResultStreamMaxLen))
}

func TestExecutionDeadlineSettlesClaimBeforeRetention(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("execution-deadline-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{
		Redis:            rdb,
		Name:             name,
		ExecutionTimeout: 100 * time.Millisecond,
		ResultStreamTTL:  toolregistry.MinResultStreamTTL,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	svc.healthTracker = newMockHealthTracker()

	const toolset = "execution-deadline-toolset"
	provider := validRegisterPayloadForSchemaAdmission(toolset)
	registration, err := svc.Register(ctx, provider)
	require.NoError(t, err)
	call, err := svc.CallTool(ctx, transitionCallPayload(toolset, "deadline"))
	require.NoError(t, err)
	executionDeadline, err := time.Parse(time.RFC3339Nano, call.ExecutionDeadline)
	require.NoError(t, err)
	retentionDeadline, err := time.Parse(time.RFC3339Nano, call.ResultStreamExpiresAt)
	require.NoError(t, err)
	assert.True(t, executionDeadline.Before(retentionDeadline))

	store := svc.callAdmissions.(*callAdmissionStore)
	requestEventID := retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
	claim, err := svc.ClaimToolCall(ctx, &genregistry.ClaimToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ProviderRegistrationToken: registration.RegistrationToken,
		CallRegistrationToken:     registration.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		RequestEventID:            requestEventID,
		ClaimOperationID:          uuid.NewString(),
	})
	require.NoError(t, err)
	require.Equal(t, string(callClaimExecute), claim.Disposition)

	callKey := store.callKey(call.ToolUseID)
	require.Eventually(t, func() bool {
		return rdb.HGet(ctx, callKey, "terminal").Val() == "1"
	}, 2*time.Second, 20*time.Millisecond)
	assert.Equal(t, "execution_deadline", rdb.HGet(ctx, callKey, "terminal_cause").Val())
	assert.True(t, rdb.Exists(ctx, callKey).Val() == 1, "terminal call record must remain retained")
	terminalJSON, err := rdb.HGet(ctx, callKey, "terminal_payload").Bytes()
	require.NoError(t, err)
	var terminal toolregistry.ToolResultMessage
	require.NoError(t, json.Unmarshal(terminalJSON, &terminal))
	require.NotNil(t, terminal.Error)
	assert.Equal(t, toolregistry.ToolErrorCodeOutcomeUnknown, terminal.Error.Code)

	leaseIndex := store.leaseSettlementKey(
		registration.RegistrationToken,
		providerLeaseKey(provider.ProviderID, provider.ProviderIncarnationID),
	)
	assert.Zero(t, rdb.ZScore(ctx, store.settlementKey, callKey).Val())
	assert.Zero(t, rdb.ZScore(ctx, leaseIndex, callKey).Val())
	assert.False(t, rdb.HExists(ctx, store.membershipKey, callKey).Val())
}

func TestDrainingLeaseRejectsUnclaimedPublishedCall(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("draining-claim-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{Redis: rdb, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	svc.healthTracker = newMockHealthTracker()

	const toolset = "draining-claim-toolset"
	provider := validRegisterPayloadForSchemaAdmission(toolset)
	registration, err := svc.Register(ctx, provider)
	require.NoError(t, err)
	call, err := svc.CallTool(ctx, transitionCallPayload(toolset, "draining"))
	require.NoError(t, err)
	store := svc.callAdmissions.(*callAdmissionStore)
	requestEventID := retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
	require.NoError(t, svc.DrainProvider(ctx, &genregistry.DrainProviderPayload{
		Name:                      toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ExpectedRegistrationToken: registration.RegistrationToken,
		SettlementDurationMs:      toolregistry.MinProviderLeaseDuration.Milliseconds(),
	}))

	_, err = svc.ClaimToolCall(ctx, &genregistry.ClaimToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ProviderRegistrationToken: registration.RegistrationToken,
		CallRegistrationToken:     registration.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		RequestEventID:            requestEventID,
		ClaimOperationID:          uuid.NewString(),
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "claim tool call")
}

func TestDrainingLeaseCompletesCallClaimedBeforeDrain(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("draining-completion-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{Redis: rdb, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	svc.healthTracker = newMockHealthTracker()

	const toolset = "draining-completion-toolset"
	provider := validRegisterPayloadForSchemaAdmission(toolset)
	registration, err := svc.Register(ctx, provider)
	require.NoError(t, err)
	call, err := svc.CallTool(ctx, transitionCallPayload(toolset, "draining-completion"))
	require.NoError(t, err)
	store := svc.callAdmissions.(*callAdmissionStore)
	requestEventID := retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
	claimPayload := &genregistry.ClaimToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ProviderRegistrationToken: registration.RegistrationToken,
		CallRegistrationToken:     registration.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		RequestEventID:            requestEventID,
		ClaimOperationID:          uuid.NewString(),
	}
	claim, err := svc.ClaimToolCall(ctx, claimPayload)
	require.NoError(t, err)
	require.Equal(t, string(callClaimExecute), claim.Disposition)
	require.NoError(t, svc.DrainProvider(ctx, &genregistry.DrainProviderPayload{
		Name:                      toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ExpectedRegistrationToken: registration.RegistrationToken,
		SettlementDurationMs:      toolregistry.MinProviderLeaseDuration.Milliseconds(),
	}))
	replayedClaim, err := svc.ClaimToolCall(ctx, claimPayload)
	require.NoError(t, err)
	require.Equal(t, string(callClaimExecute), replayedClaim.Disposition)

	result := toolregistry.NewToolResultMessage(
		registration.RegistrationToken,
		call.ToolUseID,
		json.RawMessage(`{"ok":true}`),
	)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, svc.CompleteToolCall(ctx, &genregistry.CompleteToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		RegistrationToken:         registration.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		ResultJSON:                resultJSON,
		RequestEventID:            requestEventID,
		ProviderRegistrationToken: registration.RegistrationToken,
	}))
}

func TestMissingCallHashCleansGlobalAndLeaseSettlementIndexes(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("missing-settlement-%d", time.Now().UnixNano())
	store := newCallAdmissionStore(rdb, name)
	callKey := store.callKey("missing-call")
	leaseIndex := store.leaseSettlementKey(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"provider\x00incarnation",
	)
	require.NoError(t, rdb.ZAdd(ctx, store.settlementKey, redis.Z{
		Score:  float64(time.Now().Add(-time.Second).UnixMilli()),
		Member: callKey,
	}).Err())
	require.NoError(t, rdb.ZAdd(ctx, leaseIndex, redis.Z{
		Score:  float64(time.Now().Add(-time.Second).UnixMilli()),
		Member: callKey,
	}).Err())
	require.NoError(t, rdb.HSet(ctx, store.membershipKey, callKey, leaseIndex).Err())

	_, err := store.SettleLostClaims(ctx, 1)
	require.NoError(t, err)
	assert.Zero(t, rdb.ZCard(ctx, store.settlementKey).Val())
	assert.Zero(t, rdb.ZCard(ctx, leaseIndex).Val())
	assert.False(t, rdb.HExists(ctx, store.membershipKey, callKey).Val())
}

func TestSettlementScannerStopsWithRegistryContext(t *testing.T) {
	rdb := getRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	reg, err := New(ctx, Config{
		Redis: rdb,
		Name:  fmt.Sprintf("settlement-cancel-%d", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	cancel()
	select {
	case <-reg.callSettlement.doneCh:
	case <-time.After(time.Second):
		t.Fatal("settlement scanner did not stop after registry context cancellation")
	}
	require.NoError(t, reg.Close(context.Background()))
}

// requireReadableResultStream proves the CallTool postcondition with a fresh
// client that has no in-memory knowledge of the registry's stream handle.
func requireReadableResultStream(
	t *testing.T,
	ctx context.Context,
	rdb *redis.Client,
	call *genregistry.CallToolResult,
) {
	t.Helper()
	expiresAt, err := time.Parse(time.RFC3339Nano, call.ResultStreamExpiresAt)
	require.NoError(t, err)
	pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	require.NoError(t, err)
	stream, err := pulseClient.Stream(
		toolregistry.ResultStreamID(call.ToolUseID),
		streamopts.WithStreamMaxLen(toolregistry.ResultStreamMaxLen),
		streamopts.WithStreamDeadline(expiresAt),
	)
	require.NoError(t, err)
	reader, err := stream.NewReader(ctx, streamopts.WithReaderStartAtOldest())
	require.NoError(t, err)
	reader.Close()
}
