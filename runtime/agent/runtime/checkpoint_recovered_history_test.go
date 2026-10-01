package runtime

// A failed call becomes history once recovery ends. These tests produce both
// recovered and still-pending checkpoints through the real workflow loop.

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCheckpointRecoveredFailureHistory(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending recovery", true: "recovered failure"}[recovered], func(t *testing.T) {
			spec := newAnyJSONSpec("catalog.search")
			resumes := 0
			h := newRecoveryHarness(t, "recovered-history", []tools.ToolSpec{spec},
				func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					if bytes.Contains(call.Payload, []byte(`"obsolete"`)) {
						return invalidCallResult(call), nil
					}
					return successfulToolResult(call), nil
				},
				func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					resumes++
					if recovered && resumes == 1 {
						return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
							Name: spec.Name, Payload: rawjson.Message(`{"query":"corrected"}`),
						}}}, nil
					}
					if recovered {
						require.Empty(t, input.Reminders)
					}
					return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(
						&planner.AwaitClarification{ID: "next", Question: "Which group?"},
					))}, nil
				})
			out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
				ToolCallID: "failed-call", Name: spec.Name,
				Payload: rawjson.Message(`{"query":"bad","obsolete":true}`),
			}}}, initialCaps(RunPolicy{MaxToolCalls: 8}))
			require.NoError(t, err)
			require.NotNil(t, out.Suspension)
			definition := testRuntimeDefinition(h.runtime, h.input.AgentID)
			checkpoint, err := decodeWorkflowCheckpoint(out.Suspension, definition)
			require.NoError(t, err)
			require.Empty(t, checkpoint.Batch.Calls)
			require.Empty(t, checkpoint.Batch.Records)
			require.NotNil(t, checkpoint.State.ToolOutputs[0].Failure)
			if recovered {
				require.Empty(t, checkpoint.State.PendingRecovery)
				require.Len(t, checkpoint.State.ToolOutputs, 2)
				require.Nil(t, checkpoint.State.ToolOutputs[1].Failure)
			} else {
				require.Len(t, checkpoint.State.PendingRecovery, 1)
			}
			original := append(rawjson.Message(nil), out.Suspension.Checkpoint...)
			oldDecoder := spec.ExecutionPayloadCodec.FromJSON
			spec.ExecutionPayloadCodec.FromJSON = func(data []byte) (any, error) {
				if bytes.Contains(data, []byte(`"obsolete"`)) {
					return nil, errors.New("obsolete input cannot execute")
				}
				return oldDecoder(data)
			}
			current := New(h.runtime.Store)
			seedTestToolSpecs(current, spec)
			_, err = decodeWorkflowCheckpoint(out.Suspension, testRuntimeDefinition(current, h.input.AgentID))
			require.NoError(t, err)
			require.Equal(t, original, out.Suspension.Checkpoint)

			// Matching the call ID alone cannot bless a different failure.
			for _, mutate := range []func(*workflowCheckpoint){
				func(c *workflowCheckpoint) { c.State.ToolEvents = c.State.ToolEvents[1:] },
				func(c *workflowCheckpoint) { c.State.ToolEvents[0].Failure = nil },
				func(c *workflowCheckpoint) { c.State.ToolEvents[0].Failure.Recovery.Action = planner.RecoveryFinish },
				func(c *workflowCheckpoint) { c.State.ToolEvents[0].Failure.Error.Message = "different failure" },
			} {
				copy, err := decodeWorkflowCheckpointState(out.Suspension)
				require.NoError(t, err)
				mutate(copy)
				require.ErrorContains(t, validateCheckpointInputs(copy, definition), "does not match recorded result")
			}
		})
	}
}
