// These tests reject a completed streamed tool-call turn from planner code.
// The planner names the exact provider message through
// planner.StreamSummary.Message and returns
// planner.NewRecoverableModelPlanningError. They verify that the workflow
// schedules one corrective planning turn carrying the planner's guidance and
// the executable tool catalog, and that an exhausted recovery budget ends the
// run through the existing finalization path.
package runtime

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

// batchRuleCorrection is the planner-authored guidance carried into the
// corrective turn. Its exact text must reach the planner as a reminder.
const batchRuleCorrection = "Call catalog.load on its own. Do not combine it with other tool calls."

func TestRunLoopRecoversRejectedStreamedToolCallTurn(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	load := newAnyJSONSpec("catalog.load")
	var providerCalls, resumes, loadCalls int
	h := newRecoveryHarness(
		t,
		"stream-tool-turn",
		[]tools.ToolSpec{search, load},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == load.Name {
				loadCalls++
			}
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			switch resumes {
			case 1:
				// The model answers with a batch that breaks the planner's rule:
				// catalog.load must be the only call in its batch.
				summary, err := streamPreResponseModel(ctx, input)
				require.NoError(t, err)
				require.Len(t, summary.ToolCalls, 2)
				require.Nil(t, summary.FinalResponse())
				require.NotNil(t, summary.Message())
				return nil, planner.NewRecoverableModelPlanningError(
					errors.New("catalog.load must be the only call in its batch"),
					summary.Message(),
					batchRuleCorrection,
				)
			case 2:
				// The corrective turn keeps every executable tool and carries the
				// planner's guidance as a reminder.
				require.False(t, input.SynthesisOnly)
				require.Nil(t, input.Finalize)
				assertAdvertisedTools(t, input, search.Name, load.Name)
				require.Len(t, input.Reminders, 1)
				assert.Contains(t, input.Reminders[0].Text, batchRuleCorrection)
				assert.Contains(t, input.Reminders[0].Text, "Produce replacement planning output")
				summary, err := streamPreResponseModel(ctx, input)
				require.NoError(t, err)
				require.Len(t, summary.ToolCalls, 1)
				require.Equal(t, load.Name, summary.ToolCalls[0].Name)
				return &planner.PlanResult{
					ToolCalls:            summary.ToolCalls,
					SynthesizeAfterTools: true,
				}, nil
			case 3:
				require.True(t, input.SynthesisOnly)
				return finalPlannerResult("corrected batch complete"), nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		},
	)
	h.runtime.models["test"] = newBatchRuleStreamModel(&providerCalls, search.Name, load.Name, false)

	out, err := h.run(streamRecoveryKickoff(search), policy.CapsState{
		MaxToolCalls:           4,
		RemainingToolCalls:     4,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "corrected batch complete", out.Final.Text())
	assert.Equal(t, 2, providerCalls)
	assert.Equal(t, 3, resumes)
	assert.Equal(t, 1, loadCalls)
	assert.Equal(t, 2, out.ToolCount)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 30, out.Usage.TotalTokens)
}

func TestRunLoopFinalizesWhenRejectedToolCallTurnsExhaustRecoveryBudget(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	load := newAnyJSONSpec("catalog.load")
	var providerCalls, resumes, loadCalls int
	h := newRecoveryHarness(
		t,
		"stream-tool-turn-budget",
		[]tools.ToolSpec{search, load},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == load.Name {
				loadCalls++
			}
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			switch resumes {
			case 1, 2:
				// The model repeats the forbidden batch on both the original and
				// the corrective turn, so the planner rejects it twice.
				if resumes == 2 {
					require.Len(t, input.Reminders, 1)
					assert.Contains(t, input.Reminders[0].Text, batchRuleCorrection)
				}
				summary, err := streamPreResponseModel(ctx, input)
				require.NoError(t, err)
				require.Len(t, summary.ToolCalls, 2)
				return nil, planner.NewRecoverableModelPlanningError(
					errors.New("catalog.load must be the only call in its batch"),
					summary.Message(),
					batchRuleCorrection,
				)
			case 3:
				// No recovery turn remains, so the workflow forces finalization.
				require.NotNil(t, input.Finalize)
				require.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
				return finalPlannerResult("recovery budget exhausted"), nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		},
	)
	h.runtime.models["test"] = newBatchRuleStreamModel(&providerCalls, search.Name, load.Name, true)

	out, err := h.run(streamRecoveryKickoff(search), policy.CapsState{
		MaxToolCalls:           4,
		RemainingToolCalls:     4,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "recovery budget exhausted", out.Final.Text())
	assert.Equal(t, 2, providerCalls)
	assert.Equal(t, 3, resumes)
	assert.Zero(t, loadCalls)
	assert.Equal(t, 1, out.ToolCount)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 30, out.Usage.TotalTokens)
}

// newBatchRuleStreamModel streams a tool-call batch that pairs search with
// load. When alwaysViolate is false, calls after the first return load alone,
// which is the replacement the planner accepts. Each call reports a distinct
// usage total so tests can check that rejected turns still count.
func newBatchRuleStreamModel(
	providerCalls *int,
	search, load tools.Ident,
	alwaysViolate bool,
) model.Client {
	return mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			(*providerCalls)++
			usage := model.TokenUsage{
				InputTokens:  *providerCalls * 4,
				OutputTokens: *providerCalls * 6,
				TotalTokens:  *providerCalls * 10,
			}
			calls := []model.ToolCall{{
				ID:      fmt.Sprintf("load-call-%d", *providerCalls),
				Name:    load,
				Payload: rawjson.Message(`{"id":"one"}`),
			}}
			if alwaysViolate || *providerCalls == 1 {
				calls = append(calls, model.ToolCall{
					ID:      fmt.Sprintf("search-call-%d", *providerCalls),
					Name:    search,
					Payload: rawjson.Message(`{"query":"alarms"}`),
				})
			}
			chunks := make([]model.Chunk, 0, len(calls)+2)
			chunks = append(chunks, model.UsageChunk{Usage: usage})
			message := model.Message{Role: model.ConversationRoleAssistant}
			for _, call := range calls {
				chunks = append(chunks, model.ToolCallChunk{ToolCall: call})
				message.Parts = append(message.Parts, model.ToolUsePart{
					ID:    call.ID,
					Name:  call.Name.String(),
					Input: call.Payload,
				})
			}
			chunks = append(chunks, model.StopChunk{Reason: "tool_use"})
			return &chunkStreamer{
				chunks: chunks,
				response: &model.Response{
					Content:    []model.Message{message},
					StopReason: "tool_use",
					Usage:      usage,
				},
			}, nil
		},
	})
}
