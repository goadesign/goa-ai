package runtime

// A completed page can wait beside a confirmation before its result enters
// the saved transcript. This test follows the real suspension and continuation
// paths and checks the exact stored records needed to recover that page.

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestHistoricalCheckpointRangeRetainsCompletedSibling(t *testing.T) {
	search, continuation := continuationTestSpecs()
	confirmed := newAnyJSONSpec("tools.confirm")
	store := storageinmem.New()
	rt := New(store, WithLogger(telemetry.NoopLogger{}), WithToolConfirmation(&ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{confirmed.Name: {
			Prompt:       func(context.Context, *ToolCall) (string, error) { return "Proceed?", nil },
			DeniedResult: func(context.Context, *ToolCall) (any, error) { return map[string]any{"done": false}, nil },
		}},
	}))
	_, err := createSessionForTest(t.Context(), store, "checkpoint-history")
	require.NoError(t, err)
	var rootID string
	searches, confirmations := 0, 0
	specs := []tools.ToolSpec{search, continuation, confirmed}
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tools", Specs: specs,
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			result := &planner.ToolResult{
				Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"done": true},
			}
			if call.Name == search.Name {
				searches++
				rootID = call.ToolCallID
				result.Bounds = &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("next-page")}
			} else {
				assert.Equal(t, confirmed.Name, call.Name)
				confirmations++
			}
			return result, nil
		}),
	}))
	resumes := 0
	definition := NewAgentDefinition(
		AgentRoute{ID: "checkpoint.agent", WorkflowName: "checkpoint.workflow", DefaultTaskQueue: "checkpoint.queue"},
		specs, nil, nil, []tools.Ident{search.Name, continuation.Name, confirmed.Name}, nil, nil,
	)
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "checkpoint.plan", ResumeActivityName: "checkpoint.resume", ExecuteToolActivity: "checkpoint.execute",
		Planner: &stubPlanner{
			start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{
					{Name: search.Name, Payload: rawjson.Message(`{"query":"records"}`)},
					{Name: confirmed.Name, Payload: rawjson.Message(`{}`)},
				}}, nil
			},
			resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				resumes++
				assert.Len(t, input.ToolOutputs, 2)
				return finalPlannerResult("Completed."), nil
			},
		},
	}))
	client := rt.MustClient("checkpoint.agent")
	first, err := client.Run(t.Context(), "checkpoint-history", nil,
		WithRunID("before-confirmation"), WithTurnID("before"))
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	assert.Equal(t, 1, searches)
	assert.Zero(t, confirmations)
	assert.Zero(t, resumes)
	checkpointBytes := sha256.Sum256(first.Suspension.Checkpoint)
	checkpoint, err := decodeWorkflowCheckpointState(first.Suspension)
	require.NoError(t, err)
	require.Len(t, checkpoint.Batch.Records, 1)
	record := checkpoint.Batch.Records[0]
	assert.Equal(t, rootID, record.Call.ToolCallID)
	assert.Empty(t, record.Call.ModelToolCallID)
	assert.Equal(t, "before-confirmation", record.CallRunID)
	assert.Equal(t, "before-confirmation", record.ResultRunID)
	assert.True(t, record.ResultPublished)
	assert.Zero(t, checkpoint.Batch.Recorded)

	// The first run already stored the page result, but the selected transcript
	// ends before that event. Its checkpoint retains the exact result owner.
	page, err := store.ListRunRecords(t.Context(), "before-confirmation", "", 256)
	require.NoError(t, err)
	require.Empty(t, page.NextCursor)
	endIndex, resultIndex := -1, -1
	for index, event := range page.Events {
		if event.ID == checkpoint.HistoryEndID {
			endIndex = index
		}
		if event.Type == hooks.ToolResultReceived {
			result, err := decodeToolResultRunlogEvent(event)
			require.NoError(t, err)
			if result.ToolCallID == rootID {
				resultIndex = index
			}
		}
	}
	require.NotEqual(t, -1, endIndex)
	require.Greater(t, resultIndex, endIndex)
	saved, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, "before-confirmation", checkpoint.HistoryEndID)
	require.NoError(t, err)
	require.Len(t, saved, 1)
	require.ErrorContains(t, transcript.ValidatePlannerTranscript(saved), "must be followed by user tool_result")

	second, err := client.Continue(t.Context(), "checkpoint-history", "before-confirmation",
		"after-confirmation", "after",
		&api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{
			ID: first.Suspension.Pending[0].Confirmation.ID, Approved: true, RequestedBy: "operator",
		}}, WorkflowOptions{})
	require.NoError(t, err)
	assert.Nil(t, second.Suspension)
	assert.Equal(t, 1, searches)
	assert.Equal(t, 1, confirmations)
	assert.Equal(t, 1, resumes)
	meta, err := store.LoadRun(t.Context(), "after-confirmation")
	require.NoError(t, err)
	seed, err := store.LoadRunSeed(t.Context(), meta.RunID, meta.SeedEndID)
	require.NoError(t, err)
	require.NotNil(t, seed.Source)
	assert.Equal(t, checkpoint.HistoryEndID, seed.Source.EndID)
	assert.Equal(t, "before-confirmation", seed.Source.RunID)
	successor, err := store.ListRunRecords(t.Context(), "after-confirmation", "", 256)
	require.NoError(t, err)
	require.Empty(t, successor.NextCursor)
	for _, event := range successor.Events {
		if event.Type != hooks.ToolResultReceived {
			continue
		}
		result, err := decodeToolResultRunlogEvent(event)
		require.NoError(t, err)
		assert.NotEqual(t, rootID, result.ToolCallID, "the saved page result must not be published again")
	}
	messages, err := transcript.BuildMessagesFromRunLog(t.Context(), store, "after-confirmation")
	require.NoError(t, err)
	require.NoError(t, transcript.ValidatePlannerTranscript(messages))
	results := 0
	for _, message := range messages {
		for _, part := range message.Parts {
			if result, ok := part.(model.ToolResultPart); ok && result.ToolUseID == rootID {
				results++
			}
		}
	}
	assert.Equal(t, 1, results)
	outputs, err := rt.loadPlannerToolOutputs(t.Context(), []*api.ToolOutputRef{{
		CallRunID: record.CallRunID, ResultRunID: record.ResultRunID, ToolCallID: record.Call.ToolCallID,
	}})
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	assert.Equal(t, rootID, outputs[0].ToolCallID)
	assert.Equal(t, "next-page", *outputs[0].Bounds.NextCursor)
	assert.Equal(t, checkpointBytes, sha256.Sum256(first.Suspension.Checkpoint))
	t.Logf("selected history ends at record %s; page result index=%d is after end index=%d; checkpoint preserves call/result run %s; successor publishes no duplicate page result",
		checkpoint.HistoryEndID, resultIndex, endIndex, record.CallRunID)
}

func TestHistoricalContinuationRestoresCallCompletedBySuccessor(t *testing.T) {
	search, continuation := continuationTestSpecs()
	search.Name, continuation.Name = "delegate.search", "pages.next"
	search.Bounds.Paging.ContinueTool = continuation.Name
	continuation.Bounds.Paging.ContinueTool = continuation.Name
	continuation.Bounds.Paging.SourceTool = search.Name
	search.IsAgentTool, search.AgentID = true, "split.child"
	store := storageinmem.New()
	rt := New(store, WithLogger(telemetry.NoopLogger{}))
	_, err := createSessionForTest(t.Context(), store, "split-owners")
	require.NoError(t, err)
	var rootID string
	executions, starts := 0, 0
	child := testAgentDefinition("split.child", "split.child.workflow", "split.child.queue", []tools.ToolSpec{search}, nil)
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: child, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "split.child.plan", ResumeActivityName: "split.child.resume", ExecuteToolActivity: "split.child.execute",
		Planner: &stubPlanner{
			start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(
					&planner.AwaitClarification{ID: "read-records", Question: "Read these records?"},
				))}, nil
			},
			resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
				executions++
				return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{
					Result: rawjson.Message(`{"items":["record"]}`),
					Bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("saved-page")},
				}}, nil
			},
		},
	}))
	delegate := NewAgentToolsetRegistration(AgentToolConfig{Definition: child, Name: "delegate"})
	delegate.Specs = []tools.ToolSpec{search}
	require.NoError(t, rt.RegisterToolset(delegate))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "pages", Specs: []tools.ToolSpec{continuation},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			t.Errorf("unexpected page execution %q", call.Name)
			return successfulToolResult(call), nil
		}),
	}))
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: NewAgentDefinition(
			AgentRoute{ID: "split.agent", WorkflowName: "split.workflow", DefaultTaskQueue: "split.queue"},
			[]tools.ToolSpec{search, continuation}, nil, nil, []tools.Ident{search.Name, continuation.Name}, []AgentDefinition{child}, nil),
		WorkflowHandler:  rt.ExecuteWorkflow,
		PlanActivityName: "split.plan", ResumeActivityName: "split.resume", ExecuteToolActivity: "split.execute",
		Planner: &stubPlanner{
			start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				starts++
				if starts == 1 {
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
						Name: search.Name, Payload: rawjson.Message(`{"query":"records"}`),
					}}}, nil
				}
				assert.Equal(t, []tools.Ident{continuationActionName(continuation.Name, rootID)}, historicalActions(input.Agent))
				return finalPlannerResult("Saved query remains available."), nil
			},
			resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				require.Len(t, input.ToolOutputs, 1)
				rootID = input.ToolOutputs[0].ToolCallID
				assert.Equal(t, "scheduled", input.ToolOutputs[0].CallRunID)
				assert.Equal(t, "completed", input.ToolOutputs[0].ResultRunID)
				return finalPlannerResult("Query completed."), nil
			},
		},
	}))
	client := rt.MustClient("split.agent")
	first, err := client.Run(t.Context(), "split-owners", nil, WithRunID("scheduled"), WithTurnID("scheduled"))
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	assert.Zero(t, executions)
	checkpoint, err := decodeWorkflowCheckpointState(first.Suspension)
	require.NoError(t, err)
	second, err := client.Continue(t.Context(), "split-owners", "scheduled", "completed", "completed",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID: first.Suspension.Pending[0].Await.Clarification.ID, Answer: "yes",
		}}, WorkflowOptions{})
	require.NoError(t, err)
	assert.Nil(t, second.Suspension)
	assert.Equal(t, 1, executions)

	// The selected predecessor transcript ends before the original schedule.
	// The successor's result must select that exact schedule by its call owner.
	records, err := store.ListRunRecords(t.Context(), "scheduled", "", 256)
	require.NoError(t, err)
	endIndex, scheduleIndex := -1, -1
	for index, event := range records.Events {
		if event.ID == checkpoint.HistoryEndID {
			endIndex = index
		}
		if event.Type == hooks.ToolCallScheduled {
			scheduleIndex = index
		}
	}
	require.NotEqual(t, -1, endIndex)
	require.Greater(t, scheduleIndex, endIndex)
	prepared, err := client.PrepareNextTurn(t.Context(), "split-owners", "completed", nil, nil,
		WithRunID("next"), WithTurnID("next"))
	require.NoError(t, err)
	handle, err := client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, starts)
	assert.Equal(t, 1, executions)
}
