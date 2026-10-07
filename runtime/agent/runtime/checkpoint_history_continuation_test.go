package runtime

// These tests exercise history across actual suspension/restoration and
// immutable prepared launches after the registered input contract changes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestCheckpointHistoryContinuesWithoutReexecuting(t *testing.T) {
	old := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	spec := newAnyJSONSpec("svc.inspect")
	payload := rawjson.Message(`{"query":"status","obsolete":true}`)
	executions := 0
	require.NoError(t, old.RegisterToolset(ToolsetRegistration{
		Name: "svc", Specs: []tools.ToolSpec{spec},
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			executions++
			require.Equal(t, payload, call.Payload)
			return &ToolExecutionResult{
				ToolResult:    &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"value": "found"}},
				Clarification: &ToolClarification{ID: "first", Question: "Which group?"},
			}, nil
		},
	}))
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1"}
	seedRunMeta(t, old, input)
	historyEnd := seedTestPlanInput(t, old, PlanActivityInput{
		AgentID: input.AgentID, RunID: input.RunID,
		RunContext: run.Context{RunID: input.RunID, SessionID: input.SessionID},
	}, nil).HistoryEndID
	reg := AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"}
	first, err := old.runLoop(&testWorkflowContext{ctx: t.Context(), runtime: old}, reg, input,
		&workflowConversation{HistoryEndID: historyEnd, RunContext: run.Context{
			RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID, Attempt: 1,
		}},
		&PlanResult{ToolCalls: []ToolCall{{
			Name: spec.Name, ToolCallID: "inspect-1", ModelToolCallID: "provider-1",
			Payload: payload, ModelPayload: payload,
		}}},
		initialCaps(RunPolicy{MaxToolCalls: 8}), time.Time{}, time.Time{}, input.TurnID, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	require.Equal(t, 1, executions)
	checkpoint, err := decodeWorkflowCheckpointState(first.Suspension)
	require.NoError(t, err)
	require.Equal(t, 1, checkpoint.Batch.Recorded)
	before, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), old.Store, input.RunID, checkpoint.HistoryEndID)
	require.NoError(t, err)
	beforeJSON, err := transcript.EncodeRunLogDelta(before)
	require.NoError(t, err)

	decodes := 0
	spec.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
		decodes++
		return nil, errors.New("obsolete field is not accepted")
	}
	current := New(old.Store, WithLogger(telemetry.NoopLogger{}))
	seedTestToolSpecs(current, spec)
	saved := first.Suspension
	for i, answerID := range []string{"first", "second", "third"} {
		next := &RunInput{
			AgentID: input.AgentID, RunID: fmt.Sprintf("run-%d", i+2), SessionID: input.SessionID,
			TurnID: fmt.Sprintf("turn-%d", i+2),
			Continuation: &api.RunContinuationInput{Suspension: saved, Response: &api.PendingInputResponse{
				Clarification: &api.ClarificationAnswer{ID: answerID, Answer: "Group A"},
			}},
		}
		checkpoint, err := prepareContinuation(next, testRuntimeDefinition(current, input.AgentID))
		require.NoError(t, err)
		require.Equal(t, payload, checkpoint.State.ToolOutputs[0].Payload)
		require.NoError(t, restoreContinuationRunInput(next, checkpoint))
		plan := &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}},
		}}}
		if i == 0 {
			plan = &PlanResult{Await: planner.NewAwait(
				planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "second", Question: "Which range?"}),
				planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "third", Question: "Which detail?"}),
			)}
		}
		wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: current, hasPlanResult: true, planResult: plan}
		out, err := current.resumeSuspendedWorkflow(recoveryContinuationWorkflow(t, wf, checkpoint), reg, next, checkpoint, seedTestContinuationHistory(t, current, next, checkpoint))
		require.NoError(t, err)
		require.Empty(t, wf.lastToolCall.Name)
		if i < 2 {
			require.NotNil(t, out.Suspension)
			saved = out.Suspension
		} else {
			require.NotNil(t, out.Final)
			require.Nil(t, out.Suspension)
		}
	}
	require.Zero(t, decodes)
	after, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), current.Store, input.RunID, checkpoint.HistoryEndID)
	require.NoError(t, err)
	afterJSON, err := transcript.EncodeRunLogDelta(after)
	require.NoError(t, err)
	require.Equal(t, beforeJSON, afterJSON)
	outputs, err := current.loadPlannerToolOutputs(t.Context(), []*api.ToolOutputRef{{
		CallRunID: "run-1", ResultRunID: "run-1", ToolCallID: "inspect-1",
	}})
	require.NoError(t, err)
	require.Equal(t, payload, outputs[0].Payload)
	require.Zero(t, decodes)
}

func TestCheckpointHistoryPreparedBeforeContractChangeStartsUnchanged(t *testing.T) {
	spec := newAnyJSONSpec("svc.lookup")
	oldDefinition := testAgentDefinition("svc.agent", "agent.workflow", "agent.queue", []tools.ToolSpec{spec}, nil)
	client, store := newPreparedRunTestClient(&stubEngine{}, oldDefinition)
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	suspension := recordedInputSuspension(t, spec.Name)
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", Status: session.RunStatusRunning,
	})
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), store, "run-1", session.RunSuspension{ID: suspension.ID, Data: data}))
	response := &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "clarification-1", Answer: "Group A"}}
	prepared, err := client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", response, WorkflowOptions{})
	require.NoError(t, err)
	original, err := prepared.MarshalBinary()
	require.NoError(t, err)

	spec.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
		return nil, errors.New("obsolete field is not accepted")
	}
	currentDefinition := testAgentDefinition("svc.agent", "agent.workflow", "agent.queue", []tools.ToolSpec{spec}, nil)
	eng := engineinmem.New()
	received := make(chan *RunInput, 1)
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "agent.workflow",
		Handler: func(_ engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
			// The worker repeats the same contract check before restoration.
			if _, err := prepareContinuation(input, currentDefinition); err != nil {
				return nil, err
			}
			received <- input
			return &RunOutput{RunID: input.RunID}, nil
		},
	}))
	freshClient := New(store, WithEngine(eng)).MustClientFor(currentDefinition)
	for range 2 {
		parsed, err := ParsePreparedRun(original)
		require.NoError(t, err)
		handle, err := freshClient.StartPrepared(t.Context(), parsed)
		require.NoError(t, err)
		_, err = handle.Wait(t.Context())
		require.NoError(t, err)
	}
	started := <-received
	require.Equal(t, suspension, started.Continuation.Suspension)
	again, err := prepared.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, original, again)
	// Preparing a new continuation with current code accepts the same history.
	_, err = freshClient.PrepareContinuation(t.Context(), "session-1", "run-1", "run-3", "turn-3", response, WorkflowOptions{})
	require.NoError(t, err)
}

func TestCheckpointHistoryNestedChildKeepsPendingParentCurrent(t *testing.T) {
	childTool := newAnyJSONSpec("svc.lookup")
	childTool.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
		return nil, errors.New("obsolete child argument")
	}
	parentTool := newAnyJSONSpec("parent.child")
	parentTool.IsAgentTool = true
	parentTool.AgentID = "svc.agent"
	child := recordedInputSuspension(t, childTool.Name)
	parent := nestedChildSuspensionFixture(t, "parent.agent", "parent-run", parentTool, child)
	childDefinition := testAgentDefinition("svc.agent", "child.workflow", "child.queue", []tools.ToolSpec{childTool, parentTool}, nil)
	definition := testAgentDefinitionWithChildren("parent.agent", "parent.workflow", "parent.queue",
		[]tools.ToolSpec{parentTool}, nil, []AgentDefinition{childDefinition})
	original := append(rawjson.Message(nil), parent.Checkpoint...)
	_, err := decodeWorkflowCheckpoint(parent, definition)
	require.NoError(t, err)
	require.Equal(t, original, parent.Checkpoint)

	parentTool.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
		return nil, errors.New("pending parent argument is incompatible")
	}
	changed := testAgentDefinitionWithChildren("parent.agent", "parent.workflow", "parent.queue",
		[]tools.ToolSpec{parentTool}, nil, []AgentDefinition{childDefinition})
	_, err = decodeWorkflowCheckpoint(parent, changed)
	require.ErrorContains(t, err, "pending parent argument is incompatible")

	rewriteSuspensionCheckpoint(t, child, func(c *workflowCheckpoint) {
		c.Batch.Recorded = 0
		c.Batch.Records[0].ResultPublished = false
		c.State.ToolOutputs = nil
		c.State.ToolEvents = nil
	})
	parent = nestedChildSuspensionFixture(t, "parent.agent", "parent-run", parentTool, child)
	_, err = decodeWorkflowCheckpoint(parent, definition)
	require.ErrorContains(t, err, "obsolete child argument")
}
