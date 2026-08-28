package runtime

// This file verifies that a correction restored after trusted transport
// decoding enters the same bounded workflow path as locally generated model
// validation guidance. The workflow, not the transport caller, schedules each
// replacement planner turn.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRestoredCorrectableOutputUsesBoundedRecoveryAndResetsAfterSuccess(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.kickoff", "catalog")
	lookup := newStrictRecoverySpec()
	var providerCalls, lookupCalls, replacementTurns int
	h := newRecoveryHarness(
		t,
		"restored-correctable-output",
		[]tools.ToolSpec{kickoff, lookup},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == lookup.Name {
				lookupCalls++
			}
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			require.Nil(t, input.Finalize)
			if hasModelInvocationRecoveryReminder(input) {
				replacementTurns++
				if providerCalls == 3 {
					return finalPlannerResult("replacement completed"), nil
				}
			}
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{
				Model: "test",
				Tools: input.Agent.AdvertisedToolDefinitions(),
			})
			if err != nil {
				return nil, err
			}
			calls := response.ToolCalls()
			require.Len(t, calls, 1)
			request, err := planner.ToolRequestFromModelCall(calls[0])
			require.NoError(t, err)
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{request}}, nil
		},
	)
	h.runtime.models["test"] = restoredRecoveryModel(t, &providerCalls, false)

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "kickoff-call",
			Name:       kickoff.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}, policy.CapsState{
		MaxToolCalls:           3,
		RemainingToolCalls:     3,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "replacement completed", agentMessageText(out.Final))
	assert.Equal(t, 3, providerCalls)
	assert.Equal(t, 1, lookupCalls)
	assert.Equal(t, 2, replacementTurns)
}

func TestRestoredCorrectableOutputStopsAtRecoveryCap(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.kickoff", "catalog")
	var providerCalls, replacementTurns int
	h := newRecoveryHarness(
		t,
		"restored-correctable-output-cap",
		[]tools.ToolSpec{kickoff},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			if input.Finalize != nil {
				require.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
				return finalPlannerResult("stopped after recovery cap"), nil
			}
			if hasModelInvocationRecoveryReminder(input) {
				replacementTurns++
			}
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			_, err := client.Complete(ctx, &model.Request{
				Model: "test",
				Tools: input.Agent.AdvertisedToolDefinitions(),
			})
			return nil, err
		},
	)
	h.runtime.models["test"] = restoredRecoveryModel(t, &providerCalls, true)

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "kickoff-call",
			Name:       kickoff.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}, policy.CapsState{
		MaxToolCalls:           2,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "stopped after recovery cap", agentMessageText(out.Final))
	assert.Equal(t, 2, providerCalls)
	assert.Equal(t, 1, replacementTurns)
}

// restoredRecoveryModel returns transported correction guidance on the first
// invocation and every later odd invocation. When alwaysReject is true, every
// invocation returns a restored validation error.
func restoredRecoveryModel(t *testing.T, providerCalls *int, alwaysReject bool) model.Client {
	t.Helper()
	return mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			(*providerCalls)++
			if alwaysReject || *providerCalls%2 == 1 {
				usage := &model.TokenUsage{
					InputTokens:  6,
					OutputTokens: 4,
					TotalTokens:  10,
				}
				terminal, err := model.RestoreOutputValidationError(
					model.OutputValidationToolArguments,
					errors.New("decoded model output failed its generated contract"),
					model.ResponseEvidence{Present: true},
					usage,
				)
				require.NoError(t, err)
				restored, err := model.RestoreCorrectableOutputValidationError(
					terminal,
					`Field "query" must contain a JSON string.`,
				)
				require.NoError(t, err)
				return nil, restored
			}
			return testModelResponseWithUsage(
				nil,
				model.TokenUsage{
					InputTokens:  7,
					OutputTokens: 5,
					TotalTokens:  12,
				},
				model.ToolCall{
					ID:      fmt.Sprintf("lookup-call-%d", *providerCalls),
					Name:    "catalog.lookup",
					Payload: rawjson.Message(`{"query":"accepted"}`),
				},
			), nil
		},
	})
}

// hasModelInvocationRecoveryReminder reports whether this planner turn is the
// immediate replacement for a rejected model invocation.
func hasModelInvocationRecoveryReminder(input *planner.PlanResumeInput) bool {
	for _, reminder := range input.Reminders {
		if reminder.ID == "model_invocation_recovery" {
			return true
		}
	}
	return false
}
