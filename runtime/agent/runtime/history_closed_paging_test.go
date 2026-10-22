package runtime

// A saved final page closes an exact source query. These tests use independent
// runtimes and one store to keep closed pages readable through two responses,
// while unfinished cursors still require the current executable contracts.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/schema"
)

func TestClosedCheckpointPagingAcrossToolReleases(t *testing.T) {
	for _, removed := range []bool{false, true} {
		for _, closed := range []bool{false, true} {
			t.Run(map[bool]string{false: "changed result", true: "removed tools"}[removed]+"/"+map[bool]string{false: "unfinished", true: "closed"}[closed], func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				store := newTestStore()
				source, page := continuationTestSpecs()
				oldCalls, oldResumes, newResumes := 0, 0, 0
				newRuntime := func(old bool) *Runtime {
					rt := New(store, WithEngine(engineinmem.New()))
					specs := []tools.ToolSpec{source, page}
					if !old {
						if removed {
							specs = nil
						} else {
							for i := range specs {
								specs[i].Result.Schema = rawjson.Message(`{"type":"object","required":["current_items"]}`)
								var err error
								specs[i].Result.Codec, err = schema.Codec(specs[i].Result.Schema)
								require.NoError(t, err)
							}
						}
					}
					require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "tools", Specs: specs, Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
						require.True(t, old, "retained history must not execute again")
						oldCalls++
						result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"old_items": []string{"accepted"}}, Bounds: &agent.Bounds{Returned: 1}}
						if call.Name == source.Name || !closed {
							result.Bounds.Truncated, result.Bounds.NextCursor = true, new("accepted-next-page")
						}
						if call.Name == page.Name {
							return &ToolExecutionResult{ToolResult: result, Clarification: &ToolClarification{ID: "first", Question: "Which group?"}}, nil
						}
						return &ToolExecutionResult{ToolResult: result}, nil
					}}))
					var requested model.ToolCall
					require.NoError(t, rt.RegisterModel("local", mustTestModelClient(stubModelClient{complete: func(context.Context, *model.Request) (*model.Response, error) {
						return testModelResponse(nil, requested), nil
					}})))
					names := make([]tools.Ident, 0, len(specs))
					for _, spec := range specs {
						names = append(names, spec.Name)
					}
					definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "test"}, specs, nil, nil, names, nil, nil)
					require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{Definition: definition, WorkflowHandler: rt.ExecuteWorkflow, PlanActivityName: "records.plan", ResumeActivityName: "records.resume", ExecuteToolActivity: "records.execute", Planner: &stubPlanner{
						start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
							return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: source.Name, Payload: rawjson.Message(`{"query":"accepted"}`)}}}, nil
						},
						resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
							if old {
								oldResumes++
								actions := historicalActions(input.Agent)
								require.Len(t, actions, 1)
								requested = model.ToolCall{ID: "page", Name: actions[0], Payload: rawjson.Message(`{}`)}
								client, ok := input.Agent.PlannerModelClient("local")
								require.True(t, ok)
								response, err := client.Complete(ctx, &model.Request{Model: "local", Tools: input.Agent.AdvertisedToolDefinitions()})
								if err != nil {
									return nil, err
								}
								call, err := planner.ToolRequestFromModelCall(response.ToolCalls()[0])
								return &planner.PlanResult{ToolCalls: []planner.ToolRequest{call}}, err
							}
							newResumes++
							require.Empty(t, historicalActions(input.Agent))
							require.Len(t, input.ToolOutputs, 2)
							for _, output := range input.ToolOutputs {
								require.Contains(t, string(output.Result), "old_items")
							}
							if newResumes == 1 {
								return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "second", Question: "Which range?"}))}, nil
							}
							return finalPlannerResult("Accepted pages remain available."), nil
						},
					}}))
					return rt
				}
				old := newRuntime(true)
				_, err := createSessionForTest(ctx, store, "paging-session")
				require.NoError(t, err)
				first, err := old.MustClient("records.agent").Run(ctx, "paging-session", nil, WithRunID("old"), WithTurnID("old"))
				require.NoError(t, err)
				require.NotNil(t, first.Suspension)
				checkpoint, err := decodeWorkflowCheckpointState(first.Suspension)
				require.NoError(t, err)
				require.Len(t, checkpoint.State.ToolOutputs, 2)
				require.Equal(t, checkpoint.State.ToolOutputs[0].ToolCallID, checkpoint.State.ToolOutputs[1].ContinuationRootToolCallID)
				before, err := json.Marshal(storedToolResults(t, old, "old"))
				require.NoError(t, err)
				current := newRuntime(false)
				if closed {
					testCheckpointFinalPageMaterialization(t, first.Suspension, current.agents["records.agent"].Definition)
				}
				previous := "old"
				for i, answer := range []string{"first", "second"} {
					runID := []string{"new-one", "new-two"}[i]
					prepared, err := current.MustClient("records.agent").PrepareContinuation(ctx, "paging-session", previous, runID, runID, &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: answer, Answer: "selected"}}, WorkflowOptions{})
					if !closed {
						if removed {
							require.ErrorContains(t, err, "removed")
						} else {
							require.ErrorContains(t, err, "current_items")
						}
						require.Zero(t, newResumes)
						break
					}
					require.NoError(t, err)
					handle, err := current.MustClient("records.agent").StartPrepared(ctx, prepared)
					require.NoError(t, err)
					out, err := handle.Wait(ctx)
					require.NoError(t, err)
					if i == 0 {
						require.NotNil(t, out.Suspension)
						again, err := decodeWorkflowCheckpointState(out.Suspension)
						require.NoError(t, err)
						require.Equal(t, checkpoint.State.ToolOutputs, again.State.ToolOutputs)
					} else {
						require.Nil(t, out.Suspension)
					}
					previous = runID
				}
				after, err := json.Marshal(storedToolResults(t, current, "old"))
				require.NoError(t, err)
				require.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
				require.Equal(t, 2, oldCalls)
				require.Equal(t, 1, oldResumes)
				require.Equal(t, map[bool]int{false: 0, true: 2}[closed], newResumes)
			})
		}
	}
}

// testCheckpointFinalPageMaterialization removes transcript progress while
// keeping the accepted final event, then proves an unmaterialized result cannot
// close the source cursor or bypass current input and result validation.
func testCheckpointFinalPageMaterialization(t *testing.T, suspension *api.RunSuspension, definition AgentDefinition) {
	t.Helper()
	for _, materialized := range []bool{false, true} {
		checkpoint, err := decodeWorkflowCheckpointState(suspension)
		require.NoError(t, err)
		checkpoint.Batch.Recorded = 0
		checkpoint.State.ToolOutputs = checkpoint.State.ToolOutputs[:1]
		checkpoint.State.ToolEvents = checkpoint.State.ToolEvents[:1]
		if !materialized {
			checkpoint.Batch.Records[0].ResultRecord = nil
			checkpoint.Batch.Records[0].ResultPublished = false
		}
		uses, err := checkpointInputConsumers(checkpoint)
		require.NoError(t, err)
		source := checkpoint.State.ToolOutputs[0].ToolCallID
		page := checkpoint.Batch.Records[0].Call.ToolCallID
		require.Equal(t, !materialized, uses.current[source])
		require.Equal(t, !materialized, uses.current[page])
		if materialized {
			require.NoError(t, validateCheckpointToolValues(checkpoint, definition))
		} else {
			require.Error(t, validateCheckpointToolValues(checkpoint, definition))
		}
	}
}

func TestClosedPagingRetainedOutputsAndLiveCalls(t *testing.T) {
	rt := New(newTestStore())
	source := sourceContinuationOutput("records.find", "source", `{"old_query":"accepted"}`, "next")
	source.CallRunID, source.ResultRunID = "old", "old"
	page := &planner.ToolOutput{Name: "records.page", ToolCallID: "page", ContinuationRootToolCallID: "source", CallRunID: "old", ResultRunID: "old", Bounds: &agent.Bounds{Returned: 1}}
	outputs := []*planner.ToolOutput{source, page}
	input := &resolvedPlanActivityInput{PlanActivityInput: &PlanActivityInput{RunID: "current"}}
	selected, err := rt.selectHistoricalContinuations(input, outputs, outputs, nil)
	require.NoError(t, err)
	require.Empty(t, selected)
	page.ResultRunID = "current"
	_, err = rt.selectHistoricalContinuations(input, outputs, outputs, nil)
	require.ErrorContains(t, err, "current paging output references unregistered tool")
	page.ResultRunID, page.ContinuationRootToolCallID = "old", "wrong-source"
	_, err = rt.selectHistoricalContinuations(input, outputs, outputs, nil)
	require.ErrorContains(t, err, "unknown source tool call")
}
