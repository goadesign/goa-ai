package runtime

// These tests use real workflow, planner, child and storage callbacks. The
// existing generated ID codecs provide strict payloads and results without
// invoking an image executor or a model provider.

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/api"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

const admissionChildAgentID = "child.agent"

func TestChildContinuationKeepsIndependentCallsAcrossParentRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	rt := New(newTestStore(), WithEngine(engineinmem.New()))
	generated := genpictures.SpecView()
	tool := tools.ToolSpec{
		Name: "delegate.inspect", IsAgentTool: true, AgentID: admissionChildAgentID,
		Payload: generated.Payload, Result: generated.Result,
		ExecutionPayloadSchema: generated.ExecutionPayloadSchema,
		ExecutionPayloadCodec:  generated.ExecutionPayloadCodec,
	}
	childDefinition := testAgentDefinition(admissionChildAgentID, "child.workflow", "child.queue", []tools.ToolSpec{tool}, nil)
	// The parent executes this tool; the child retains its exported contract.
	parentDefinition := NewAgentDefinition(
		AgentRoute{ID: parentAgentID, WorkflowName: "parent.workflow", DefaultTaskQueue: "parent.queue"},
		[]tools.ToolSpec{tool}, nil, nil, []tools.Ident{tool.Name}, []AgentDefinition{childDefinition}, nil,
	)
	childContexts := make(chan run.Context, 5)
	parentOutputs := make(chan []*planner.ToolOutput, 1)
	child := &stubPlanner{
		start: func(_ context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			payload, err := genpictures.UnmarshalViewPayload(in.RunContext.ToolArgs)
			if err != nil {
				return nil, err
			}
			childContexts <- in.RunContext
			return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{
				ID: "question-" + payload.ID, Question: "Confirm " + payload.ID,
			}))}, nil
		},
		resume: func(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
			payload, err := genpictures.UnmarshalViewPayload(in.RunContext.ToolArgs)
			if err != nil {
				return nil, err
			}
			childContexts <- in.RunContext
			if payload.ID == "A" && in.RunContext.ParentRunID == "parent-1" {
				return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{
					ID: "question-A-again", Question: "Confirm the remaining detail for A",
				}))}, nil
			}
			result, err := genpictures.MarshalViewResult(&genpictures.ViewResult{ID: payload.ID})
			if err != nil {
				return nil, err
			}
			return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: result}}, nil
		},
	}
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition: childDefinition, WorkflowHandler: rt.ExecuteWorkflow, Planner: child,
		PlanActivityName: "child.plan", ResumeActivityName: "child.resume", ExecuteToolActivity: "child.execute",
	}))
	registration := NewAgentToolsetRegistration(AgentToolConfig{Definition: childDefinition, Name: "delegate"})
	registration.Specs = []tools.ToolSpec{tool}
	require.NoError(t, rt.RegisterToolset(registration))
	payloadA, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "A"})
	require.NoError(t, err)
	payloadB, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "B"})
	require.NoError(t, err)
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition: parentDefinition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "parent.plan", ResumeActivityName: "parent.resume", ExecuteToolActivity: "parent.execute",
		Planner: &stubPlanner{
			start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tool.Name, Payload: payloadA}, {Name: tool.Name, Payload: payloadB}}}, nil
			},
			resume: func(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
				parentOutputs <- in.ToolOutputs
				return finalPlannerResult("both original calls completed"), nil
			},
		},
	}))
	_, err = createSessionForTest(ctx, rt.Store, "session-1")
	require.NoError(t, err)
	client := rt.MustClient(parentAgentID)
	labels := map[string]string{"actor": "original-user", "facility": "original-facility", "files": "retained-files"}
	first, err := client.Run(ctx, "session-1", []*model.Message{userMsg("Inspect A and B")}, WithRunID("parent-0"), WithTurnID("turn-0"), WithLabels(labels))
	require.NoError(t, err)
	require.Len(t, first.Suspension.Pending, 2)
	checkpoint, err := decodeWorkflowCheckpoint(first.Suspension, parentDefinition)
	require.NoError(t, err)
	callA := checkpoint.Pending[0].Child.ToolCallID
	callB := checkpoint.Pending[1].Child.ToolCallID
	require.NotEqual(t, callA, callB)
	originalB := checkpoint.Pending[1].Child.Suspension
	originalBBytes := append([]byte(nil), originalB.Checkpoint...)
	second, err := client.Continue(ctx, "session-1", "parent-0", "parent-1", "turn-1", &api.PendingInputResponse{
		Clarification: &api.ClarificationAnswer{ID: "question-A", Answer: "A confirmed"},
	}, WorkflowOptions{})
	require.NoError(t, err)
	require.Len(t, second.Suspension.Pending, 2)
	secondCheckpoint, err := decodeWorkflowCheckpoint(second.Suspension, parentDefinition)
	require.NoError(t, err)
	require.Equal(t, callB, secondCheckpoint.Pending[0].Child.ToolCallID)
	require.Equal(t, originalB, secondCheckpoint.Pending[0].Child.Suspension)
	require.Equal(t, callA, secondCheckpoint.Pending[1].Child.ToolCallID)
	third, err := client.Continue(ctx, "session-1", "parent-1", "parent-2", "turn-2", &api.PendingInputResponse{
		Clarification: &api.ClarificationAnswer{ID: "question-B", Answer: "B confirmed"},
	}, WorkflowOptions{})
	require.NoError(t, err)
	require.Len(t, third.Suspension.Pending, 1)
	thirdCheckpoint, err := decodeWorkflowCheckpoint(third.Suspension, parentDefinition)
	require.NoError(t, err)
	require.Equal(t, callA, thirdCheckpoint.Pending[0].Child.ToolCallID)
	last, err := client.Continue(ctx, "session-1", "parent-2", "parent-3", "turn-3", &api.PendingInputResponse{
		Clarification: &api.ClarificationAnswer{ID: "question-A-again", Answer: "A detail confirmed"},
	}, WorkflowOptions{})
	require.NoError(t, err)
	require.Nil(t, last.Suspension)
	require.Equal(t, "both original calls completed", last.Final.Text())
	// Child completion and parent result publication have separate run IDs.
	for _, completed := range []struct {
		parent string
		source *api.RunSuspension
	}{{"parent-3", thirdCheckpoint.Pending[0].Child.Suspension}, {"parent-2", secondCheckpoint.Pending[0].Child.Suspension}} {
		source, err := decodeWorkflowCheckpointState(completed.source)
		require.NoError(t, err)
		previous, err := rt.Store.LoadRun(ctx, source.PreviousRunID)
		require.NoError(t, err)
		require.NotEmpty(t, previous.SuccessorRunID)
		childRun, err := rt.Store.LoadRun(ctx, previous.SuccessorRunID)
		require.NoError(t, err)
		require.Equal(t, completed.parent, childRun.ParentRunID)
		require.Equal(t, session.RunStatusCompleted, childRun.Status)
	}
	outputs := <-parentOutputs
	require.Len(t, outputs, 2)
	// Each original call has one saved result, and the planner must refer to
	// that record. Each small fixture run must fit in one bounded page.
	publishedResults := make(map[string]*hooks.ToolResultReceivedEvent, 2)
	for _, runID := range []string{"parent-0", "parent-1", "parent-2", "parent-3"} {
		page, err := rt.Store.ListRunRecords(ctx, runID, "", 100)
		require.NoError(t, err)
		require.Empty(t, page.NextCursor)
		for _, record := range page.Events {
			if record.Type != hooks.ToolResultReceived {
				continue
			}
			event, err := hooks.DecodeFromRecordInput(&RecordActivityInput{
				Type: record.Type, EventKey: record.EventKey, RunID: record.RunID, AgentID: record.AgentID,
				SessionID: record.SessionID, TurnID: record.TurnID,
				TimestampMS: record.Timestamp.UnixMilli(), Payload: record.Payload,
			})
			require.NoError(t, err)
			result, ok := event.(*hooks.ToolResultReceivedEvent)
			require.True(t, ok)
			require.Equal(t, tool.Name, result.ToolName)
			require.Contains(t, []string{callA, callB}, result.ToolCallID)
			require.Equal(t, "parent-0", result.CallRunID)
			_, duplicate := publishedResults[result.ToolCallID]
			require.False(t, duplicate, "each original call must publish exactly one result")
			publishedResults[result.ToolCallID] = result
		}
	}
	require.Len(t, publishedResults, 2)
	for _, output := range outputs {
		require.Equal(t, "parent-0", output.CallRunID)
		published, ok := publishedResults[output.ToolCallID]
		require.True(t, ok)
		require.Equal(t, published.RunID(), output.ResultRunID)
		require.Equal(t, []byte(published.ResultJSON), []byte(output.Result))
		result, err := genpictures.UnmarshalViewResult(output.Result)
		require.NoError(t, err)
		switch result.ID {
		case "A":
			require.Equal(t, callA, output.ToolCallID)
			require.Equal(t, "parent-3", output.ResultRunID)
		case "B":
			require.Equal(t, callB, output.ToolCallID)
			require.Equal(t, "parent-3", output.ResultRunID)
		default:
			t.Fatalf("unexpected child result %q", result.ID)
		}
	}
	require.Equal(t, originalBBytes, []byte(originalB.Checkpoint))
	for range 5 {
		childContext := <-childContexts
		require.Equal(t, labels, childContext.Labels)
		require.Equal(t, "session-1", childContext.SessionID)
		meta, err := rt.Store.LoadRun(ctx, childContext.RunID)
		require.NoError(t, err)
		require.Equal(t, childContext.ParentRunID, meta.ParentRunID)
		if childContext.ParentRunID != "parent-0" {
			seed, err := rt.Store.LoadRunSeed(ctx, meta.RunID, meta.SeedEndID)
			require.NoError(t, err)
			previous, err := rt.Store.LoadRun(ctx, seed.Declaration.SourceRunID)
			require.NoError(t, err)
			require.Equal(t, meta.RunID, previous.SuccessorRunID)
			start := session.RunStart{AgentID: meta.AgentID, RunID: meta.RunID, SessionID: meta.SessionID, ParentRunID: meta.ParentRunID, PredecessorRunID: seed.Declaration.SourceRunID, SeedEndID: meta.SeedEndID}
			linked := hooks.NewChildRunLinkedEvent(meta.ParentRunID, parentAgentID, meta.SessionID, tool.Name, childContext.ParentToolCallID, meta.RunID, admissionChildAgentID)
			require.NoError(t, rt.validateChildContinuationStart(ctx, start, linked, seed))
		}
	}
	oldB, err := rt.LoadRunSuspension(ctx, NestedRunIDForToolCall("parent-0", tool.Name, callB))
	require.NoError(t, err)
	require.Equal(t, originalB, oldB)
}

func TestChildContinuationStartRejectsAnotherParentsPendingCall(t *testing.T) {
	rt := New(newTestStore())
	tool := newAnyJSONSpec("parent.delegate")
	tool.IsAgentTool, tool.AgentID = true, admissionChildAgentID
	childTool := newAnyJSONSpec("child.lookup")
	childDefinition := testAgentDefinition(admissionChildAgentID, "child.workflow", "child.queue", []tools.ToolSpec{tool, childTool}, nil)
	parentDefinition := testAgentDefinitionWithChildren(parentAgentID, "parent.workflow", "parent.queue", []tools.ToolSpec{tool}, nil, []AgentDefinition{childDefinition})
	rt.agents[parentAgentID] = AgentRegistration{Definition: parentDefinition}
	seedTestToolSpecs(rt, tool, childTool)
	parent := session.RunMeta{AgentID: parentAgentID, RunID: "parent-0", SessionID: "session-1", Status: session.RunStatusRunning}
	admitRunForTest(t, rt.Store, parent)
	child := suspensionContractFixtureWithContext(t, childTool.Name, admissionChildAgentID, "child-0", nil, nil)
	parentSuspension := nestedChildSuspensionFixture(t, parentAgentID, parent.RunID, tool, child)
	childB := suspensionContractFixtureWithContext(t, childTool.Name, admissionChildAgentID, "child-B", nil, nil)
	rewriteSuspensionCheckpoint(t, childB, func(checkpoint *workflowCheckpoint) {
		checkpoint.Context.ParentRunID, checkpoint.Context.ParentAgentID = parent.RunID, parentAgentID
		checkpoint.Context.ParentToolCallID, checkpoint.Context.Tool = "call-B", tool.Name
		checkpoint.Context.ToolArgs = []byte(`{}`)
	})
	rewriteSuspensionCheckpointAndPublic(t, parentSuspension, func(checkpoint *workflowCheckpoint) {
		call := cloneToolCall(checkpoint.Batch.Records[0].Call)
		call.ToolCallID, call.ModelToolCallID = "call-B", "model-B"
		checkpoint.Batch.Result.ToolCalls = append(checkpoint.Batch.Result.ToolCalls, call)
		checkpoint.Batch.Calls = append(checkpoint.Batch.Calls, call)
		checkpoint.Batch.Records = append(checkpoint.Batch.Records, checkpointToolRecord{Call: call, ChildSuspension: childB})
		checkpoint.Pending = append(checkpoint.Pending, checkpointPendingInput{Child: &checkpointChildContinuation{ToolCallID: call.ToolCallID, Suspension: childB}})
	})
	admitRunForTest(t, rt.Store, session.RunMeta{AgentID: admissionChildAgentID, RunID: "child-B", SessionID: "session-1", ParentRunID: parent.RunID, Status: session.RunStatusRunning})
	admitRunForTest(t, rt.Store, session.RunMeta{AgentID: admissionChildAgentID, RunID: "child-0", SessionID: "session-1", ParentRunID: parent.RunID, Status: session.RunStatusRunning})
	for runID, suspension := range map[string]*api.RunSuspension{"parent-0": parentSuspension, "child-0": child, "child-B": childB} {
		data, err := json.Marshal(suspension)
		require.NoError(t, err)
		require.NoError(t, storeSuspensionForTest(t.Context(), rt.Store, runID, session.RunSuspension{ID: suspension.ID, Data: data}))
	}
	active, writer, err := rt.buildStoredContinuationRunInput(t.Context(), parentDefinition, "session-1", "parent-0", "active-parent", "turn-active",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "clarification-1"}}, "active-parent", "active-parent")
	require.NoError(t, err)
	compiled, err := json.Marshal(active)
	require.NoError(t, err)
	require.NoError(t, writer.publish(t.Context(), compiled))
	admitContinuedRunForTest(t, rt.Store, session.RunMeta{AgentID: parentAgentID, RunID: active.RunID, SessionID: active.SessionID, SeedEndID: active.SeedEndID, Status: session.RunStatusRunning}, "parent-0")
	admitRunForTest(t, rt.Store, session.RunMeta{AgentID: parentAgentID, RunID: "unrelated-parent", SessionID: "session-1", Status: session.RunStatusRunning})
	admitRunForTest(t, rt.Store, session.RunMeta{AgentID: parentAgentID, RunID: "foreign-parent", SessionID: "session-other", Status: session.RunStatusRunning})
	for _, test := range []struct{ name, parent, predecessor, call, want string }{
		{"unrelated", "unrelated-parent", "child-0", "child-call", "exact continuation seed"},
		{"cross-chat", "foreign-parent", "child-0", "child-call", "execution parent owner mismatch"},
		{"second-pending", "active-parent", "child-B", "call-B", "selected by the parent's pending work"},
		{"wrong-call", "active-parent", "child-0", "other-call", "selected by the parent's pending work"},
		{"swapped-child", "active-parent", "child-B", "child-call", "selected predecessor suspension"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentID := test.parent
			input, writer, err := rt.buildStoredContinuationRunInput(t.Context(), childDefinition, "session-1", test.predecessor, "candidate-"+test.name, "turn-new",
				&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "clarification-1"}}, "candidate-"+test.name, "candidate-"+test.name)
			require.NoError(t, err)
			input.ParentRunID = parentID
			compiled, err := json.Marshal(input)
			require.NoError(t, err)
			require.NoError(t, writer.publish(t.Context(), compiled))
			started, err := hooks.EncodeToRecordInput(hooks.NewRunStartedEvent(input.RunID, input.AgentID, input.SessionID, parentID, test.predecessor, nil), hooks.EncodeOptions{EventKey: "start", TimestampMS: 1})
			require.NoError(t, err)
			linked, err := hooks.EncodeToRecordInput(hooks.NewChildRunLinkedEvent(parentID, parentAgentID, input.SessionID, tool.Name, test.call, input.RunID, input.AgentID), hooks.EncodeOptions{EventKey: "link", TimestampMS: 1})
			require.NoError(t, err)
			before, err := rt.Store.ListRunRecords(t.Context(), parentID, "", 100)
			require.NoError(t, err)
			_, err = rt.storeRunStart(t.Context(), storageCommandChildStart, append([]byte{1}, make([]byte, 31)...), input.SeedEndID, started, linked)
			require.ErrorContains(t, err, test.want)
			_, err = rt.Store.LoadRun(t.Context(), input.RunID)
			require.ErrorIs(t, err, session.ErrRunNotFound)
			after, err := rt.Store.ListRunRecords(t.Context(), parentID, "", 100)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestChildContinuationPublicClientKeepsRunningParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	rt := New(newTestStore(), WithEngine(engineinmem.New()))
	generated := genpictures.SpecView()
	tool := tools.ToolSpec{Name: "delegate.inspect", IsAgentTool: true, AgentID: admissionChildAgentID,
		Payload: generated.Payload, Result: generated.Result,
		ExecutionPayloadSchema: generated.ExecutionPayloadSchema, ExecutionPayloadCodec: generated.ExecutionPayloadCodec}
	lookup := newAnyJSONSpec("child.lookup")
	definition := testAgentDefinition(admissionChildAgentID, "child.workflow", "child.queue", []tools.ToolSpec{tool, lookup}, nil)
	contexts := make(chan run.Context, 1)
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "child.plan", ResumeActivityName: "child.resume", ExecuteToolActivity: "child.execute",
		Planner: &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			contexts <- input.RunContext
			payload, err := genpictures.UnmarshalViewPayload(input.RunContext.ToolArgs)
			if err != nil {
				return nil, err
			}
			result, err := genpictures.MarshalViewResult(&genpictures.ViewResult{ID: payload.ID})
			if err != nil {
				return nil, err
			}
			return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: result}}, nil
		}},
	}))
	admitRunForTest(t, rt.Store, session.RunMeta{AgentID: parentAgentID, RunID: "running-parent", SessionID: "session-1", Status: session.RunStatusRunning})
	payload, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "retained"})
	require.NoError(t, err)
	original := &RunInput{AgentID: admissionChildAgentID, RunID: "child-0", SessionID: "session-1", ParentRunID: "running-parent"}
	publishTestRunInput(t, rt, original, nil)
	start := session.RunStart{AgentID: admissionChildAgentID, RunID: original.RunID, SessionID: original.SessionID, ParentRunID: original.ParentRunID, SeedEndID: original.SeedEndID, StartedAt: time.Now().UTC().Truncate(time.Millisecond)}
	_, err = rt.Store.StartChildRun(ctx, storage.ChildRunStart{RequestDigest: [32]byte{1}, Run: start,
		ParentLinked: testHookRecord(t, hooks.NewChildRunLinkedEvent(start.ParentRunID, parentAgentID, start.SessionID, tool.Name, "original-call", start.RunID, admissionChildAgentID), "link", start.StartedAt),
		Started:      testHookRecord(t, hooks.NewRunStartedEvent(start.RunID, admissionChildAgentID, start.SessionID, start.ParentRunID, "", nil), "start", start.StartedAt),
		Cancellation: testStartCancellationRecord(t, start),
	})
	require.NoError(t, err)
	suspension := suspensionContractFixtureWithContext(t, lookup.Name, admissionChildAgentID, start.RunID, map[string]string{"actor": "original"}, map[string]any{"retained": "original metadata"})
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.Context.ParentRunID, checkpoint.Context.ParentAgentID = start.ParentRunID, parentAgentID
		checkpoint.Context.ParentToolCallID, checkpoint.Context.Tool, checkpoint.Context.ToolArgs = "original-call", tool.Name, payload
	})
	stored, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(ctx, rt.Store, start.RunID, session.RunSuspension{ID: suspension.ID, Data: stored}))
	out, err := rt.MustClient(admissionChildAgentID).Continue(ctx, start.SessionID, start.RunID, "child-1", "turn-2",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "clarification-1", Answer: "Confirmed"}}, WorkflowOptions{})
	require.NoError(t, err)
	result, err := genpictures.UnmarshalViewResult(out.FinalToolResult.Result)
	require.NoError(t, err)
	require.Equal(t, "retained", result.ID)
	continued := <-contexts
	require.Equal(t, start.ParentRunID, continued.ParentRunID)
	require.Equal(t, "original-call", continued.ParentToolCallID)
	require.Equal(t, payload, []byte(continued.ToolArgs))
	require.Equal(t, "original", continued.Labels["actor"])
	require.Equal(t, "original metadata", continued.Metadata["retained"])
	parent, err := rt.Store.LoadRun(ctx, start.ParentRunID)
	require.NoError(t, err)
	require.Equal(t, session.RunStatusRunning, parent.Status)
	childMeta, err := rt.Store.LoadRun(ctx, "child-1")
	require.NoError(t, err)
	require.Equal(t, start.ParentRunID, childMeta.ParentRunID)
	unchanged, err := rt.Store.LoadRunSuspension(ctx, start.RunID)
	require.NoError(t, err)
	require.Equal(t, stored, unchanged.Data)
}

// The fixed current-version bytes preserve an earlier run's execution parent
// while a successor supplies its own parent. No deployed data is used.
func TestRetainedChildSuspensionKeepsHistoricalParentBytes(t *testing.T) {
	data, err := os.ReadFile("testdata/retained_child_suspension_v13.json")
	require.NoError(t, err)
	var envelope api.RunSuspension
	require.NoError(t, json.Unmarshal(data, &envelope))
	stored := session.RunSuspension{ID: envelope.ID, Data: data}
	loaded, err := validateStoredRunSuspension(stored, session.RunMeta{
		AgentID: admissionChildAgentID, RunID: "child-0", SessionID: "session-1", ParentRunID: "parent-0", Status: session.RunStatusSuspended,
	})
	require.NoError(t, err)
	generated := genpictures.SpecView()
	tool := tools.ToolSpec{Name: "delegate.inspect", IsAgentTool: true, AgentID: admissionChildAgentID, Payload: generated.Payload, Result: generated.Result,
		ExecutionPayloadSchema: generated.ExecutionPayloadSchema, ExecutionPayloadCodec: generated.ExecutionPayloadCodec}
	definition := testAgentDefinition(admissionChildAgentID, "child.workflow", "child.queue", []tools.ToolSpec{tool}, nil)
	input := &RunInput{AgentID: admissionChildAgentID, RunID: "child-1", SessionID: "session-1", TurnID: "turn-1", ParentRunID: "parent-1",
		Continuation: &api.RunContinuationInput{Suspension: loaded, Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "Confirmed"}}}}
	checkpoint, err := prepareContinuation(input, definition)
	require.NoError(t, err)
	require.NoError(t, restoreContinuationRunInput(input, checkpoint))
	require.Equal(t, "parent-1", input.ParentRunID)
	require.Equal(t, "parent-0", checkpoint.Context.ParentRunID)
	require.Equal(t, "original-call", input.ParentToolCallID)
	require.Equal(t, "original", input.Labels["actor"])
	require.Equal(t, "original metadata", input.Metadata["retained"])
	require.Equal(t, envelope.Checkpoint, input.Continuation.Suspension.Checkpoint)
	require.Equal(t, data, stored.Data)
	missing := &RunInput{AgentID: admissionChildAgentID, RunID: "child-2", SessionID: "session-1", TurnID: "turn-2", Continuation: input.Continuation}
	_, err = prepareContinuation(missing, definition)
	require.ErrorContains(t, err, "explicit execution parent")
}

// Earlier suspensions cannot be resumed after the execution-operation upgrade.
func TestStoredSuspensionRejectsPreviousVersion(t *testing.T) {
	data, err := os.ReadFile("testdata/retained_child_suspension_v12.json")
	require.NoError(t, err)
	var envelope api.RunSuspension
	require.NoError(t, json.Unmarshal(data, &envelope))
	_, err = validateStoredRunSuspension(session.RunSuspension{ID: envelope.ID, Data: data}, session.RunMeta{
		AgentID: admissionChildAgentID, RunID: "child-0", SessionID: "session-1", ParentRunID: "parent-0", Status: session.RunStatusSuspended,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "version")
}

// TestEndedSessionContinuationSettlesAllSavedChildren proves that cleanup keeps
// its execution parent active while both independent saved calls are canceled.
func TestEndedSessionContinuationSettlesAllSavedChildren(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	rt := New(newTestStore(), WithEngine(engineinmem.New()))
	generated := genpictures.SpecView()
	tool := tools.ToolSpec{
		Name: "delegate.inspect", IsAgentTool: true, AgentID: admissionChildAgentID,
		Payload: generated.Payload, Result: generated.Result,
		ExecutionPayloadSchema: generated.ExecutionPayloadSchema,
		ExecutionPayloadCodec:  generated.ExecutionPayloadCodec,
	}
	childDefinition := testAgentDefinition(admissionChildAgentID, "child.workflow", "child.queue", []tools.ToolSpec{tool}, nil)
	parentDefinition := NewAgentDefinition(
		AgentRoute{ID: parentAgentID, WorkflowName: "parent.workflow", DefaultTaskQueue: "parent.queue"},
		[]tools.ToolSpec{tool}, nil, nil, []tools.Ident{tool.Name}, []AgentDefinition{childDefinition}, nil,
	)
	var childPlans, childResumes atomic.Int32
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition: childDefinition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "child.plan", ResumeActivityName: "child.resume", ExecuteToolActivity: "child.execute",
		Planner: &stubPlanner{
			start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				childPlans.Add(1)
				return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{
					ID: "question", Question: "Confirm the requested item",
				}))}, nil
			},
			resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
				childResumes.Add(1)
				return finalPlannerResult("an ended session must not answer"), nil
			},
		},
	}))
	registration := NewAgentToolsetRegistration(AgentToolConfig{Definition: childDefinition, Name: "delegate"})
	registration.Specs = []tools.ToolSpec{tool}
	require.NoError(t, rt.RegisterToolset(registration))
	payloadA, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "A"})
	require.NoError(t, err)
	payloadB, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "B"})
	require.NoError(t, err)
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition: parentDefinition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "parent.plan", ResumeActivityName: "parent.resume", ExecuteToolActivity: "parent.execute",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tool.Name, Payload: payloadA}, {Name: tool.Name, Payload: payloadB}}}, nil
		}},
	}))
	_, err = createSessionForTest(ctx, rt.Store, "session-1")
	require.NoError(t, err)
	client := rt.MustClient(parentAgentID)
	first, err := client.Run(ctx, "session-1", nil, WithRunID("parent-source"), WithTurnID("turn-source"))
	require.NoError(t, err)
	require.Len(t, first.Suspension.Pending, 2)
	checkpoint, err := decodeWorkflowCheckpoint(first.Suspension, parentDefinition)
	require.NoError(t, err)
	_, err = rt.Store.(interface {
		EndSession(context.Context, string, time.Time) (session.Session, error)
	}).EndSession(ctx, "session-1", time.Now())
	require.NoError(t, err)
	_, err = client.Continue(ctx, "session-1", "parent-source", "parent-cleanup", "turn-cleanup", &api.PendingInputResponse{
		Clarification: &api.ClarificationAnswer{ID: "question", Answer: "Unused after session ending"},
	}, WorkflowOptions{})
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 2, childPlans.Load())
	require.Zero(t, childResumes.Load())
	for _, item := range checkpoint.Pending {
		saved, err := decodeWorkflowCheckpointState(item.Child.Suspension)
		require.NoError(t, err)
		previous, err := rt.Store.LoadRun(ctx, saved.PreviousRunID)
		require.NoError(t, err)
		require.NotEmpty(t, previous.SuccessorRunID)
		child, err := rt.Store.LoadRun(ctx, previous.SuccessorRunID)
		require.NoError(t, err)
		require.Equal(t, "parent-cleanup", child.ParentRunID)
		require.Equal(t, session.RunStatusCanceled, child.Status)
		require.Equal(t, run.CancellationReasonSessionEnded, child.CancellationReason)
	}
	parent, err := rt.Store.LoadRun(ctx, "parent-cleanup")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCanceled, parent.Status)
	page, err := rt.ListRunEvents(ctx, "parent-cleanup", "", 20)
	require.NoError(t, err)
	require.Equal(t, 2, countRunEventsByType(page, hooks.ChildRunLinked))
	require.Equal(t, 1, countRunEventsByType(page, hooks.RunCompleted))
}

func TestParentCancellationAfterChildResuspendsSettlesNewChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	bus := &questionCancellationBus{Bus: hooks.NewBus(), runID: "parent-1", kind: hooks.AwaitClarification}
	rt := New(newTestStore(), WithEngine(engineinmem.New()), WithHooks(bus))
	bus.runtime = rt
	generated := genpictures.SpecView()
	tool := tools.ToolSpec{Name: "delegate.inspect", IsAgentTool: true, AgentID: admissionChildAgentID,
		Payload: generated.Payload, Result: generated.Result,
		ExecutionPayloadSchema: generated.ExecutionPayloadSchema, ExecutionPayloadCodec: generated.ExecutionPayloadCodec}
	childDefinition := testAgentDefinition(admissionChildAgentID, "child.workflow", "child.queue", []tools.ToolSpec{tool}, nil)
	parentDefinition := NewAgentDefinition(AgentRoute{ID: parentAgentID, WorkflowName: "parent.workflow", DefaultTaskQueue: "parent.queue"},
		[]tools.ToolSpec{tool}, nil, nil, []tools.Ident{tool.Name}, []AgentDefinition{childDefinition}, nil)
	var childResumes atomic.Int32
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition: childDefinition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "child.plan", ResumeActivityName: "child.resume", ExecuteToolActivity: "child.execute",
		Planner: &stubPlanner{
			start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "first-question", Question: "Choose"}))}, nil
			},
			resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
				childResumes.Add(1)
				return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "second-question", Question: "Choose again"}))}, nil
			},
		},
	}))
	registration := NewAgentToolsetRegistration(AgentToolConfig{Definition: childDefinition, Name: "delegate"})
	registration.Specs = []tools.ToolSpec{tool}
	require.NoError(t, rt.RegisterToolset(registration))
	payload, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "synthetic"})
	require.NoError(t, err)
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition: parentDefinition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "parent.plan", ResumeActivityName: "parent.resume", ExecuteToolActivity: "parent.execute",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: tool.Name, Payload: payload}}}, nil
		}},
	}))
	_, err = createSessionForTest(ctx, rt.Store, "session")
	require.NoError(t, err)
	client := rt.MustClient(parentAgentID)
	first, err := client.Run(ctx, "session", nil, WithRunID("parent-0"), WithTurnID("turn-0"))
	require.NoError(t, err)
	require.Len(t, first.Suspension.Pending, 1)
	parentCheckpoint, err := decodeWorkflowCheckpoint(first.Suspension, parentDefinition)
	require.NoError(t, err)
	checkpoint, err := decodeWorkflowCheckpointState(parentCheckpoint.Pending[0].Child.Suspension)
	require.NoError(t, err)
	_, err = client.Continue(ctx, "session", "parent-0", "parent-1", "turn-1",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "first-question", Answer: "chosen"}}, WorkflowOptions{})
	require.ErrorIs(t, err, context.Canceled)
	assert.EqualValues(t, 1, childResumes.Load())
	original, err := rt.Store.LoadRun(ctx, checkpoint.PreviousRunID)
	require.NoError(t, err)
	require.NotEmpty(t, original.SuccessorRunID)
	answered, err := rt.Store.LoadRun(ctx, original.SuccessorRunID)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusSuspended, answered.Status)
	require.NotEmpty(t, answered.SuccessorRunID)
	settled, err := rt.Store.LoadRun(ctx, answered.SuccessorRunID)
	require.NoError(t, err)
	assert.Equal(t, "parent-1", settled.ParentRunID)
	assert.Equal(t, session.RunStatusCanceled, settled.Status)
	assert.Equal(t, run.CancellationReasonEngineCanceled, settled.CancellationReason)
	parent, err := rt.Store.LoadRun(ctx, "parent-1")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, parent.Status)
	assert.Equal(t, run.CancellationReasonUserRequested, parent.CancellationReason)
}
