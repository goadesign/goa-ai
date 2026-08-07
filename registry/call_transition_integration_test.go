//go:build integration

package registry

// These live-Redis tests pin global call identity across admission transitions.
// Current routing may change or disappear, but a retained call keeps its
// original token and terminal stream while provider authority remains a
// separate catalog fact.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestCallIdentityAndSettlementSurviveAdmissionTransitions(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("call-transition-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{Redis: rdb, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	callAdmissions := svc.callAdmissions.(*callAdmissionStore)
	health := newMockHealthTracker()
	svc.healthTracker = health

	toolset := "transition-toolset"
	providerA := validRegisterPayloadForSchemaAdmission(toolset)
	admissionA, err := svc.Register(ctx, providerA)
	require.NoError(t, err)
	callA := transitionCallPayload(toolset, "call-a")
	admittedA, err := svc.CallTool(ctx, callA)
	require.NoError(t, err)
	require.Equal(t, admissionA.RegistrationToken, admittedA.RegistrationToken)
	callAEventID := retainedPublicationEventID(t, ctx, rdb, callAdmissions, admittedA.ToolUseID)
	staleCall := transitionCallPayload(toolset, "call-stale")
	admittedStale, err := svc.CallTool(ctx, staleCall)
	require.NoError(t, err)
	require.Equal(t, admissionA.RegistrationToken, admittedStale.RegistrationToken)
	staleCallEventID := retainedPublicationEventID(t, ctx, rdb, callAdmissions, admittedStale.ToolUseID)
	overloadedCall := transitionCallPayload(toolset, "call-overload-terminal")
	admittedOverloaded, err := svc.CallTool(ctx, overloadedCall)
	require.NoError(t, err)
	overloadedCallEventID := retainedPublicationEventID(t, ctx, rdb, callAdmissions, admittedOverloaded.ToolUseID)
	retry := toolregistry.NewToolResultRetryMessage(
		admissionA.RegistrationToken,
		admittedOverloaded.ToolUseID,
		toolregistry.ToolRetryReasonProviderOverloaded,
		toolregistry.ProviderOverloadRetryAfter,
	)
	retryJSON, err := json.Marshal(retry)
	require.NoError(t, err)
	claimedA, err := svc.ClaimToolCall(ctx, &genregistry.ProviderToolCallClaimPayload{
		Toolset:                   toolset,
		ProviderID:                providerA.ProviderID,
		ProviderIncarnationID:     providerA.ProviderIncarnationID,
		ProviderRegistrationToken: admissionA.RegistrationToken,
		CallRegistrationToken:     admissionA.RegistrationToken,
		ToolUseID:                 admittedA.ToolUseID,
		RequestEventID:            callAEventID,
	})
	require.NoError(t, err)
	require.Equal(t, string(callClaimExecute), claimedA.Disposition)
	_, err = rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: pulseStreamKeyPrefix + toolregistry.ResultStreamID(admittedOverloaded.ToolUseID),
		Values: map[string]any{
			"n": toolregistry.ResultEventKey,
			"p": retryJSON,
		},
	}).Result()
	require.NoError(t, err)

	require.NoError(t, svc.Unregister(ctx, &genregistry.UnregisterPayload{
		Name:                      toolset,
		ExpectedRegistrationToken: admissionA.RegistrationToken,
	}))
	result := toolregistry.NewToolResultMessage(
		admissionA.RegistrationToken,
		admittedA.ToolUseID,
		json.RawMessage(`{"value":"settled-by-a"}`),
	)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, svc.CompleteToolCall(ctx, &genregistry.CompleteToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                providerA.ProviderID,
		ProviderIncarnationID:     providerA.ProviderIncarnationID,
		RegistrationToken:         admissionA.RegistrationToken,
		ToolUseID:                 admittedA.ToolUseID,
		ResultJSON:                resultJSON,
		RequestEventID:            callAEventID,
		ProviderRegistrationToken: admissionA.RegistrationToken,
	}))
	terminalStreamKey := pulseStreamKeyPrefix + toolregistry.ResultStreamID(admittedA.ToolUseID)
	terminalLength, err := rdb.XLen(ctx, terminalStreamKey).Result()
	require.NoError(t, err)
	require.NoError(t, svc.PublishToolOutputDelta(ctx, &genregistry.PublishToolOutputDeltaPayload{
		Toolset:                   toolset,
		ProviderID:                providerA.ProviderID,
		ProviderIncarnationID:     providerA.ProviderIncarnationID,
		ProviderRegistrationToken: admissionA.RegistrationToken,
		CallRegistrationToken:     admissionA.RegistrationToken,
		ToolUseID:                 admittedA.ToolUseID,
		RequestEventID:            callAEventID,
		Stream:                    "stdout",
		Delta:                     "late output",
	}))
	require.NoError(t, svc.ReportToolCallOverload(ctx, &genregistry.ProviderToolCallClaimPayload{
		Toolset:                   toolset,
		ProviderID:                providerA.ProviderID,
		ProviderIncarnationID:     providerA.ProviderIncarnationID,
		ProviderRegistrationToken: admissionA.RegistrationToken,
		CallRegistrationToken:     admissionA.RegistrationToken,
		ToolUseID:                 admittedA.ToolUseID,
		RequestEventID:            callAEventID,
	}))
	afterLateEvents, err := rdb.XLen(ctx, terminalStreamKey).Result()
	require.NoError(t, err)
	assert.Equal(t, terminalLength, afterLateEvents)
	overloadedResult := toolregistry.NewToolResultMessage(
		admissionA.RegistrationToken,
		admittedOverloaded.ToolUseID,
		json.RawMessage(`{"value":"settled-after-overload"}`),
	)
	overloadedResultJSON, err := json.Marshal(overloadedResult)
	require.NoError(t, err)
	claimedOverloaded, err := svc.ClaimToolCall(ctx, &genregistry.ProviderToolCallClaimPayload{
		Toolset:                   toolset,
		ProviderID:                providerA.ProviderID,
		ProviderIncarnationID:     providerA.ProviderIncarnationID,
		ProviderRegistrationToken: admissionA.RegistrationToken,
		CallRegistrationToken:     admissionA.RegistrationToken,
		ToolUseID:                 admittedOverloaded.ToolUseID,
		RequestEventID:            overloadedCallEventID,
	})
	require.NoError(t, err)
	require.Equal(t, string(callClaimExecute), claimedOverloaded.Disposition)
	require.NoError(t, svc.CompleteToolCall(ctx, &genregistry.CompleteToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                providerA.ProviderID,
		ProviderIncarnationID:     providerA.ProviderIncarnationID,
		RegistrationToken:         admissionA.RegistrationToken,
		ToolUseID:                 admittedOverloaded.ToolUseID,
		ResultJSON:                overloadedResultJSON,
		RequestEventID:            overloadedCallEventID,
		ProviderRegistrationToken: admissionA.RegistrationToken,
	}))

	require.NoError(t, svc.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
		Name:                      toolset,
		ProviderID:                providerA.ProviderID,
		ProviderIncarnationID:     providerA.ProviderIncarnationID,
		ExpectedRegistrationToken: admissionA.RegistrationToken,
	}))
	providerB := validRegisterPayloadForSchemaAdmission(toolset)
	providerB.ProviderID = toolset + "/provider-b"
	providerB.ProviderIncarnationID = testIncarnationB
	providerB.AdmissionRevision = testAdmissionRevisionB
	admissionB, err := svc.Register(ctx, providerB)
	require.NoError(t, err)
	require.NotEqual(t, admissionA.RegistrationToken, admissionB.RegistrationToken)

	health.healthy = false
	replayed, err := svc.CallTool(ctx, callA)
	require.NoError(t, err)
	assert.Equal(t, admittedA, replayed)
	requestStreamKey := pulseStreamKeyPrefix + toolregistry.ToolsetStreamID(toolset)
	requestsBeforeReplay, err := rdb.XLen(ctx, requestStreamKey).Result()
	require.NoError(t, err)
	replayedOverloaded, err := svc.RetryTool(ctx, &genregistry.RetryToolPayload{
		Toolset:                   overloadedCall.Toolset,
		Tool:                      overloadedCall.Tool,
		PayloadJSON:               overloadedCall.PayloadJSON,
		Meta:                      overloadedCall.Meta,
		WireProtocolVersion:       toolregistry.WireProtocolVersion,
		ExpectedRegistrationToken: admissionA.RegistrationToken,
	})
	require.NoError(t, err)
	assert.Equal(t, admittedOverloaded, replayedOverloaded)
	requestsAfterReplay, err := rdb.XLen(ctx, requestStreamKey).Result()
	require.NoError(t, err)
	assert.Equal(t, requestsBeforeReplay, requestsAfterReplay)

	health.healthy = true
	callB := transitionCallPayload(toolset, "call-b")
	admittedB, err := svc.CallTool(ctx, callB)
	require.NoError(t, err)
	require.Equal(t, admissionB.RegistrationToken, admittedB.RegistrationToken)
	require.NoError(t, svc.Unregister(ctx, &genregistry.UnregisterPayload{
		Name:                      toolset,
		ExpectedRegistrationToken: admissionB.RegistrationToken,
	}))

	_, err = svc.ClaimToolCall(ctx, &genregistry.ProviderToolCallClaimPayload{
		Toolset:                   toolset,
		ProviderID:                providerB.ProviderID,
		ProviderIncarnationID:     providerB.ProviderIncarnationID,
		ProviderRegistrationToken: strings.Repeat("c", 64),
		CallRegistrationToken:     admissionA.RegistrationToken,
		ToolUseID:                 admittedStale.ToolUseID,
		RequestEventID:            staleCallEventID,
	})
	require.Error(t, err)

	// B's preserved retired lease may atomically settle the A-owned request.
	settled, err := svc.ClaimToolCall(ctx, &genregistry.ProviderToolCallClaimPayload{
		Toolset:                   toolset,
		ProviderID:                providerB.ProviderID,
		ProviderIncarnationID:     providerB.ProviderIncarnationID,
		ProviderRegistrationToken: admissionB.RegistrationToken,
		CallRegistrationToken:     admissionA.RegistrationToken,
		ToolUseID:                 admittedStale.ToolUseID,
		RequestEventID:            staleCallEventID,
	})
	require.NoError(t, err)
	require.Equal(t, string(callClaimTerminal), settled.Disposition)
	entries, err := rdb.XRange(
		ctx,
		pulseStreamKeyPrefix+toolregistry.ResultStreamID(admittedStale.ToolUseID),
		"-",
		"+",
	).Result()
	require.NoError(t, err)
	var terminalEntries []redis.XMessage
	for _, entry := range entries {
		if entry.Values["n"] == toolregistry.ResultEventKey {
			terminalEntries = append(terminalEntries, entry)
		}
	}
	require.Len(t, terminalEntries, 1)
	payload, ok := terminalEntries[0].Values["p"].(string)
	require.True(t, ok)
	var rejected toolregistry.ToolResultMessage
	require.NoError(t, json.Unmarshal([]byte(payload), &rejected))
	require.NoError(t, toolregistry.ValidateToolResultMessage(rejected))
	assert.Equal(t, admissionA.RegistrationToken, rejected.RegistrationToken)
	assert.Equal(t, admittedStale.ToolUseID, rejected.ToolUseID)
	require.NotNil(t, rejected.Error)
	assert.Equal(t, "queued tool call belongs to an older registration", rejected.Error.Failure.Error.Message)
}

func TestConcurrentDispatchClaimsExecuteExactlyOnce(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("call-claim-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{Redis: rdb, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	svc.healthTracker = newMockHealthTracker()

	const toolset = "claim-toolset"
	providerPayload := validRegisterPayloadForSchemaAdmission(toolset)
	admission, err := svc.Register(ctx, providerPayload)
	require.NoError(t, err)
	call, err := svc.CallTool(ctx, transitionCallPayload(toolset, "concurrent"))
	require.NoError(t, err)
	eventID := retainedPublicationEventID(
		t,
		ctx,
		rdb,
		svc.callAdmissions.(*callAdmissionStore),
		call.ToolUseID,
	)
	claimPayload := &genregistry.ProviderToolCallClaimPayload{
		Toolset:                   toolset,
		ProviderID:                providerPayload.ProviderID,
		ProviderIncarnationID:     providerPayload.ProviderIncarnationID,
		ProviderRegistrationToken: admission.RegistrationToken,
		CallRegistrationToken:     admission.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		RequestEventID:            eventID,
	}

	const contenders = 16
	dispositions := make(chan string, contenders)
	errs := make(chan error, contenders)
	var workers sync.WaitGroup
	workers.Add(contenders)
	for range contenders {
		go func() {
			defer workers.Done()
			result, claimErr := svc.ClaimToolCall(ctx, claimPayload)
			if claimErr != nil {
				errs <- claimErr
				return
			}
			dispositions <- result.Disposition
		}()
	}
	workers.Wait()
	close(errs)
	close(dispositions)
	for claimErr := range errs {
		require.NoError(t, claimErr)
	}
	counts := make(map[string]int)
	for disposition := range dispositions {
		counts[disposition]++
	}
	assert.Equal(t, 1, counts[string(callClaimExecute)])
	assert.Equal(t, contenders-1, counts[string(callClaimClaimed)])

	result := toolregistry.NewToolResultMessage(
		admission.RegistrationToken,
		call.ToolUseID,
		json.RawMessage(`{"value":"once"}`),
	)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, svc.CompleteToolCall(ctx, &genregistry.CompleteToolCallPayload{
		Toolset:                   toolset,
		ProviderID:                providerPayload.ProviderID,
		ProviderIncarnationID:     providerPayload.ProviderIncarnationID,
		RegistrationToken:         admission.RegistrationToken,
		ToolUseID:                 call.ToolUseID,
		ResultJSON:                resultJSON,
		RequestEventID:            eventID,
		ProviderRegistrationToken: admission.RegistrationToken,
	}))
	replayed, err := svc.ClaimToolCall(ctx, claimPayload)
	require.NoError(t, err)
	assert.Equal(t, string(callClaimTerminal), replayed.Disposition)
	terminalLength, err := rdb.XLen(
		ctx,
		pulseStreamKeyPrefix+toolregistry.ResultStreamID(call.ToolUseID),
	).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(2), terminalLength)
}

func retainedPublicationEventID(
	t *testing.T,
	ctx context.Context,
	rdb *redis.Client,
	store *callAdmissionStore,
	toolUseID string,
) string {
	t.Helper()
	eventID, err := rdb.HGet(ctx, store.callKey(toolUseID), "publication_event_id").Result()
	require.NoError(t, err)
	return eventID
}

// transitionCallPayload builds one exact request whose tool-use identity is
// stable across replay and distinct for each supplied call ID.
func transitionCallPayload(toolset, callID string) *genregistry.CallToolPayload {
	return &genregistry.CallToolPayload{
		Toolset:     toolset,
		Tool:        "lookup",
		PayloadJSON: []byte(`{"query":"status"}`),
		Meta: &genregistry.ToolCallMeta{
			RunID:      "transition-run",
			SessionID:  "transition-session",
			ToolCallID: callID,
		},
		WireProtocolVersion: toolregistry.WireProtocolVersion,
	}
}
