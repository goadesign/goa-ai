// These tests send malformed generated tool arguments through the validated
// streaming path. They verify that the planner activity returns only safe
// correction guidance and that the workflow applies its existing recovery,
// budget, and cancellation rules without executing the rejected call.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRunLoopRecoversMalformedStreamedToolCallBeforeExecution(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.stream_kickoff", "catalog")
	lookup := newStrictRecoverySpec()
	var providerCalls, lookupCalls, resumes int
	h := newRecoveryHarness(
		t,
		"stream-model-invocation",
		[]tools.ToolSpec{kickoff, lookup},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == lookup.Name {
				lookupCalls++
				assert.JSONEq(t, `{"query":"accepted"}`, string(call.Payload))
			}
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			assert.False(t, input.SynthesisOnly)
			assertAdvertisedTools(t, input, kickoff.Name, lookup.Name)
			if len(input.ToolOutputs) == 2 {
				return finalPlannerResult("stream replacement completed"), nil
			}
			summary, err := streamPreResponseModel(ctx, input)
			if err != nil {
				var validationErr *model.OutputValidationError
				require.ErrorAs(t, err, &validationErr)
				require.NotEmpty(t, validationErr.RecoveryCorrection())
				require.NotContains(t, err.Error(), "privateSecret")
				require.NotContains(t, err.Error(), "submitted-secret")
				rejected, cloneErr := validationErr.RejectedResponse()
				require.NoError(t, cloneErr)
				require.Nil(t, rejected)
				return nil, err
			}
			require.Len(t, summary.ToolCalls, 1)
			require.Len(t, input.Reminders, 1)
			assert.Equal(t, "model_invocation_recovery", input.Reminders[0].ID)
			assert.Contains(t, input.Reminders[0].Text, `Field "query" must contain a JSON string.`)
			assert.NotContains(t, input.Reminders[0].Text, "privateSecret")
			assert.NotContains(t, input.Reminders[0].Text, "submitted-secret")
			return &planner.PlanResult{ToolCalls: summary.ToolCalls}, nil
		},
	)
	h.runtime.models["test"] = newPreResponseRecoveryStreamModel(&providerCalls, false)

	out, err := h.run(streamRecoveryKickoff(kickoff), policy.CapsState{
		MaxToolCalls:           3,
		RemainingToolCalls:     3,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "stream replacement completed", agentMessageText(out.Final))
	assert.Equal(t, 2, providerCalls)
	assert.Equal(t, 1, lookupCalls)
	assert.Equal(t, 3, resumes)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 30, out.Usage.TotalTokens)
}

func TestRunLoopMalformedStreamedToolCallUsesSharedRecoveryCap(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.stream_cap_kickoff", "catalog")
	lookup := newStrictRecoverySpec()
	var providerCalls, lookupCalls, recoveryAttempts int
	h := newRecoveryHarness(
		t,
		"stream-model-invocation-cap",
		[]tools.ToolSpec{kickoff, lookup},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == lookup.Name {
				lookupCalls++
			}
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			if input.Finalize != nil {
				require.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
				return finalPlannerResult("stopped after streamed invocation recovery cap"), nil
			}
			recoveryAttempts++
			_, err := streamPreResponseModel(ctx, input)
			return nil, err
		},
	)
	h.runtime.models["test"] = newPreResponseRecoveryStreamModel(&providerCalls, true)

	out, err := h.run(streamRecoveryKickoff(kickoff), policy.CapsState{
		MaxToolCalls:           2,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "stopped after streamed invocation recovery cap", agentMessageText(out.Final))
	assert.Equal(t, 2, recoveryAttempts)
	assert.Equal(t, 2, providerCalls)
	assert.Zero(t, lookupCalls)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 30, out.Usage.TotalTokens)
}

func TestRunLoopCancellationPreventsStreamedToolCallReplacement(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.stream_cancel_kickoff", "catalog")
	lookup := newStrictRecoverySpec()
	var providerCalls, plannerCalls int
	h := newRecoveryHarness(
		t,
		"stream-model-invocation-cancel",
		[]tools.ToolSpec{kickoff, lookup},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			_, err := streamPreResponseModel(ctx, input)
			return nil, err
		},
	)
	h.runtime.models["test"] = newPreResponseRecoveryStreamModel(&providerCalls, true)
	workflowCtx, cancel := context.WithCancel(context.Background())
	h.workflow.ctx = workflowCtx
	resume := h.workflow.plannerRoutes["resume"]
	h.workflow.plannerRoutes["resume"] = func(
		ctx context.Context,
		input *PlanActivityInput,
	) (*PlanActivityOutput, error) {
		plannerCalls++
		output, err := resume(ctx, input)
		if output != nil && output.ModelInvocationRecovery != nil {
			cancel()
		}
		return output, err
	}

	out, err := h.run(streamRecoveryKickoff(kickoff), policy.CapsState{
		MaxToolCalls:           2,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       2,
		RemainingRecoveryTurns: 2,
	})

	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, plannerCalls)
	assert.Equal(t, 1, providerCalls)
}

// streamPreResponseModel makes the planner's selected streaming call with the
// currently advertised tools and returns either safe validation failure or the
// accepted replacement calls.
func streamPreResponseModel(
	ctx context.Context,
	input *planner.PlanResumeInput,
) (planner.StreamSummary, error) {
	client, ok := input.Agent.PlannerModelClient("test")
	if !ok {
		return planner.StreamSummary{}, errors.New("test model is not registered")
	}
	return client.Stream(ctx, &model.Request{
		Model: "test",
		Tools: input.Agent.AdvertisedToolDefinitions(),
	})
}

// newPreResponseRecoveryStreamModel returns a usage chunk followed by a
// malformed generated tool call. Later calls either repeat that rejection or
// provide the one accepted replacement used by these tests.
func newPreResponseRecoveryStreamModel(providerCalls *int, alwaysInvalid bool) model.Client {
	return mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			(*providerCalls)++
			payload := rawjson.Message(`{"query":42,"privateSecret":"submitted-secret"}`)
			if !alwaysInvalid && *providerCalls > 1 {
				payload = rawjson.Message(`{"query":"accepted"}`)
			}
			usage := model.TokenUsage{
				InputTokens:  *providerCalls * 4,
				OutputTokens: *providerCalls * 6,
				TotalTokens:  *providerCalls * 10,
			}
			call := model.ToolCall{
				ID:      "stream-call",
				Name:    "catalog.lookup",
				Payload: payload,
			}
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.UsageChunk{Usage: usage},
					model.ToolCallChunk{ToolCall: call},
					model.StopChunk{Reason: "tool_use"},
				},
				response: &model.Response{
					Content: []model.Message{{
						Role: model.ConversationRoleAssistant,
						Parts: []model.Part{model.ToolUsePart{
							ID:    call.ID,
							Name:  call.Name.String(),
							Input: payload,
						}},
					}},
					StopReason: "tool_use",
					Usage:      usage,
				},
			}, nil
		},
	})
}

// streamRecoveryKickoff returns one accepted call that enters the normal
// resume loop before the streamed model rejection occurs.
func streamRecoveryKickoff(kickoff tools.ToolSpec) *PlanResult {
	return &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "kickoff-call",
		Name:       kickoff.Name,
		Payload:    rawjson.Message(`{}`),
	}}}
}
