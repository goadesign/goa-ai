package runtime

// Failed batch results never render typed success hints or supply paging
// queries. A sibling can still own a separate executable confirmation.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCheckpointFailedBatchInputs(t *testing.T) {
	failed, confirm := newAnyJSONSpec("catalog.failed"), newAnyJSONSpec("catalog.change")
	h := newRecoveryHarness(t, "failed-batch", []tools.ToolSpec{failed, confirm},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			require.Equal(t, failed.Name, call.Name)
			return invalidCallResult(call), nil
		},
		func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			require.FailNow(t, "batch must suspend before planner resumes")
			return nil, nil
		})
	h.runtime.toolConfirmation = &ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{
		confirm.Name: {
			Prompt:       func(context.Context, *ToolCall) (string, error) { return "Apply change?", nil },
			DeniedResult: func(context.Context, *ToolCall) (any, error) { return map[string]any{"denied": true}, nil },
		},
	}}
	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{Name: failed.Name, ToolCallID: "failure", Payload: rawjson.Message(`{"id":1}`)},
		{Name: confirm.Name, ToolCallID: "confirmation", Payload: rawjson.Message(`{"id":"pending"}`)},
	}}, initialCaps(RunPolicy{MaxToolCalls: 8}))
	require.NoError(t, err)
	saved, err := decodeWorkflowCheckpoint(out.Suspension, testRuntimeDefinition(h.runtime, h.input.AgentID))
	require.NoError(t, err)
	require.Zero(t, saved.Batch.Recorded)
	require.Len(t, saved.Batch.Records, 1)
	require.NotNil(t, saved.Batch.Records[0].Result.Failure)
	require.True(t, saved.Batch.Records[0].ResultPublished)
	require.Empty(t, saved.State.PendingRecovery)

	decodes := 0
	failed.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
		decodes++
		return nil, errors.New("failed arguments are not executable")
	}
	// Declaring paging does not make a failed result a query or cursor source.
	failed.Bounds = &tools.BoundsSpec{Paging: &tools.PagingSpec{CursorField: "cursor"}}
	current := New(h.runtime.Store)
	seedTestToolSpecs(current, failed, confirm)
	definition := testRuntimeDefinition(current, h.input.AgentID)
	require.NoError(t, validateCheckpointToolValues(saved, definition))
	require.Zero(t, decodes)

	// The failure producer can materialize a recorded rejection without any
	// typed argument consumer, even if those arguments fail today's codec.
	record := saved.Batch.Records[0]
	record.ResultPublished, record.ResultRecord = false, nil
	saved.Batch.Records[0] = record
	require.NoError(t, validateCheckpointToolValues(saved, definition))
	result, err := decodeCheckpointToolEvent(record.Result, record.Call, definition.spec)
	require.NoError(t, err)
	content, err := current.materializeToolResult(t.Context(), record.Call, result)
	require.NoError(t, err)
	require.Empty(t, content)
	preview, err := formatToolResultPreviewForCall(t.Context(), current, &record.Call, result)
	require.NoError(t, err)
	require.Empty(t, preview)
	require.Zero(t, decodes)

	confirm.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
		return nil, errors.New("pending confirmation must validate")
	}
	seedTestToolSpecs(current, confirm)
	require.ErrorContains(t, validateCheckpointToolValues(saved, testRuntimeDefinition(current, h.input.AgentID)),
		"pending confirmation must validate")
}
