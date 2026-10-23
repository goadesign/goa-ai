package runtime

// These tests execute old and new generated tool contracts against one stored
// conversation, including planner result loading and a second saved clarification.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCompletedHistoryAcrossGeneratedToolReleases(t *testing.T) {
	for _, variant := range []string{"incompatible result", "removed tool", "paging added"} {
		t.Run(variant, func(t *testing.T) {
			removed, pagingAdded := variant == "removed tool", variant == "paging added"
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			store := newTestStore()
			oldSpec := genrecords.SpecRead()
			finish := genrecords.SpecErase()
			finish.Name, finish.Confirmation, finish.TerminalRun, finish.Bookkeeping = "records.finish", nil, true, true
			acceptedPayload := rawjson.Message(`{"query":"accepted-selection"}`)
			acceptedServerData := rawjson.Message(`[{"kind":"record.card","audience":"timeline","data":{"count":4}}]`)
			var oldExecutions, freshExecutions, plannerResumes int
			newRuntime := func(old bool) *Runtime {
				rt := New(store, WithEngine(engineinmem.New()), WithLogger(telemetry.NoopLogger{}))
				specs := []tools.ToolSpec{finish}
				read := genrecords.SpecRead()
				if old || !removed {
					if !old {
						read.Result = genrecords.SpecErase().Result
						read.ServerData, read.CanonicalizeServerData = nil, nil
					}
					if !old && pagingAdded {
						source, page := continuationTestSpecs()
						read.Bounds = source.Bounds
						page.Name = "records.continue_read"
						read.Bounds.Paging.ContinueTool = page.Name
						page.Bounds.Paging.ContinueTool, page.Bounds.Paging.SourceTool = page.Name, read.Name
						specs = append(specs, page)
					}
					specs = append(specs, read)
				}
				hints, err := rthints.CompileHintTemplates(map[tools.Ident]string{oldSpec.Name: "Accepted {{ .Args.Query }}"}, nil)
				require.NoError(t, err)
				if !old {
					hints = nil
				}
				require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
					Name: "records", Specs: specs, ResultHints: hints,
					Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
						result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: &genrecords.EraseResult{Erased: true}}
						if call.Name == finish.Name {
							return &ToolExecutionResult{ToolResult: result}, nil
						}
						value, err := read.ExecutionPayloadCodec.FromJSON(call.Payload)
						require.NoError(t, err)
						require.IsType(t, &genrecords.ReadPayload{}, value)
						if old {
							oldExecutions++
							result.Result = &genrecords.ReadResult{Count: 4}
							result.ServerData, result.Blocks = acceptedServerData, richToolContentForTest()
							return &ToolExecutionResult{ToolResult: result, Clarification: &ToolClarification{ID: "first", Question: "Which group?"}}, nil
						}
						freshExecutions++
						return &ToolExecutionResult{ToolResult: result, Clarification: &ToolClarification{ID: "second", Question: "Which range?"}}, nil
					},
				}))
				names := make([]tools.Ident, 0, len(specs))
				for _, spec := range specs {
					names = append(names, spec.Name)
				}
				definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "test"}, specs, nil, nil, names, nil, nil)
				require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
					Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
					PlanActivityName: "records.plan", ResumeActivityName: "records.resume", ExecuteToolActivity: "records.execute",
					Planner: &stubPlanner{
						start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
							return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: oldSpec.Name, Payload: acceptedPayload}}}, nil
						},
						resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
							plannerResumes++
							require.Equal(t, acceptedPayload, input.ToolOutputs[0].Payload)
							require.Equal(t, rawjson.Message(`{"count":4}`), input.ToolOutputs[0].Result)
							require.Equal(t, acceptedServerData, input.ToolOutputs[0].ServerData)
							require.Equal(t, richToolContentForTest(), input.ToolOutputs[0].Blocks)
							if plannerResumes == 1 {
								if !removed && !pagingAdded {
									return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: oldSpec.Name, Payload: rawjson.Message(`{"query":"fresh-selection"}`)}}}, nil
								}
								return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "second", Question: "Which range?"}))}, nil
							}
							return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: finish.Name, Payload: rawjson.Message(`{}`)}}}, nil
						},
					},
				}))
				return rt
			}
			old := newRuntime(true)
			_, err := createSessionForTest(ctx, store, "release-session")
			require.NoError(t, err)
			initial := &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("records.agent")}, RunID: "old-run", SessionID: "release-session", TurnID: "old-turn"}
			publishTestRunInput(t, old, initial, nil)
			handle, err := old.Engine.StartWorkflow(ctx, engine.WorkflowStartRequest{ID: initial.RunID, Workflow: "records.workflow", TaskQueue: "test", Input: initial})
			require.NoError(t, err)
			first, err := handle.Wait(ctx)
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			checkpoint, err := decodeWorkflowCheckpointState(first.Suspension)
			require.NoError(t, err)
			accepted := checkpoint.State.ToolEvents[0]
			require.Equal(t, acceptedPayload, checkpoint.State.ToolOutputs[0].Payload)
			originalRecord := checkpoint.Batch.Records[0].ResultRecord
			require.NotNil(t, originalRecord)
			originalEvent, err := decodeToolResultRecord(originalRecord, checkpoint.Batch.Records[0].Call, initial.RunID, initial.RunID)
			require.NoError(t, err)
			require.Equal(t, "Accepted accepted-selection", originalEvent.ResultPreview)
			before, err := json.Marshal(storedToolResults(t, old, initial.RunID))
			require.NoError(t, err)
			current := newRuntime(false)
			previous := initial.RunID
			for i, answer := range []string{"first", "second"} {
				runID := []string{"new-run-1", "new-run-2"}[i]
				prepared, err := current.MustClient("records.agent").PrepareContinuation(ctx, "release-session", previous, runID, runID,
					&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: answer, Answer: "selected"}}, WorkflowOptions{})
				require.NoError(t, err)
				handle, err := current.MustClient("records.agent").StartPrepared(ctx, prepared)
				require.NoError(t, err)
				out, err := handle.Wait(ctx)
				require.NoError(t, err)
				if i == 0 {
					require.NotNil(t, out.Suspension)
					again, err := decodeWorkflowCheckpointState(out.Suspension)
					require.NoError(t, err)
					require.Equal(t, accepted, again.State.ToolEvents[0])
				} else {
					require.NotNil(t, out.FinalToolResult)
					require.Equal(t, finish.Name, out.FinalToolResult.Name)
				}
				previous = runID
			}
			require.Equal(t, 1, oldExecutions)
			require.Equal(t, map[bool]int{false: 1, true: 0}[removed || pagingAdded], freshExecutions)
			require.Equal(t, 2, plannerResumes)
			again, err := decodeWorkflowCheckpointState(first.Suspension)
			require.NoError(t, err)
			require.Equal(t, originalRecord, again.Batch.Records[0].ResultRecord)
			after, err := json.Marshal(storedToolResults(t, current, initial.RunID))
			require.NoError(t, err)
			require.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
		})
	}
}

func TestCompletedCompiledHistoryDoesNotNeedCurrentDeclaration(t *testing.T) {
	rt, catalog, selected := staticServiceCatalog(t)
	require.NoError(t, catalog.BindStaticService(testRegistryName, genrecords.Toolset(), selected, readTestStaticResource, genrecords.Read))
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	saved := recordedInputSuspension(t, genrecords.Read)
	rewriteSuspensionCheckpoint(t, saved, func(checkpoint *workflowCheckpoint) {
		call := checkpoint.Batch.Calls[0]
		call.Registry, call.Payload = binding, rawjson.Message(`{"query":"accepted-selection"}`)
		checkpoint.Batch.Calls[0], checkpoint.Batch.Result.ToolCalls[0] = call, call
		checkpoint.Batch.Result.SynthesizeAfterTools = true
		checkpoint.Batch.Records[0].Call = call
		checkpoint.State.ToolOutputs[0].Registry, checkpoint.State.ToolOutputs[0].Payload = binding, call.Payload
		checkpoint.State.ToolEvents[0].Result = rawjson.Message(`{"count":4}`)
		checkpoint.State.ToolOutputs[0].Result = checkpoint.State.ToolEvents[0].Result
		checkpoint.Batch.Records[0].Result = checkpoint.State.ToolEvents[0]
		checkpoint.Batch.Records[0].ResultJSON = checkpoint.State.ToolEvents[0].Result
		checkpoint.RequiredTools = requiredCheckpointToolNames(checkpoint)
		saved.RequiredTools = checkpoint.RequiredTools
	})
	currentSpec := genrecords.SpecRead()
	currentSpec.Result = genrecords.SpecErase().Result
	definition := testAgentDefinition("svc.agent", "svc.workflow", "test", []tools.ToolSpec{currentSpec}, nil).
		WithRegistryTools(testRegistrySources{whole: true}).
		WithRegistryDeclarations(func(name tools.Ident) (*genregistry.ToolSchema, bool) {
			declaration, exists := genrecords.ToolSchema(name)
			declaration.ResultSchema = currentSpec.Result.Schema
			return declaration, exists
		})
	_, err = registrycontract.Read(binding, definition.registryDeclaration)
	require.ErrorIs(t, err, registrycontract.ErrCompiledContractChanged)
	checkpoint, err := decodeWorkflowCheckpoint(saved, definition)
	require.NoError(t, err)
	state, err := rt.restoreCheckpointState(checkpoint.State)
	require.NoError(t, err)
	encoded, err := encodeToolEvent(state.ToolEvents[0], checkpoint.Batch.Calls[0], definition)
	require.NoError(t, err)
	require.Equal(t, rawjson.Message(`{"count":4}`), encoded.Result)

	// The same saved selection remains incompatible for an unfinished call.
	rewriteSuspensionCheckpoint(t, saved, func(checkpoint *workflowCheckpoint) {
		checkpoint.Batch.Recorded = 0
		checkpoint.Batch.Records[0].ResultRecord = nil
		checkpoint.Batch.Records[0].ResultPublished = false
		checkpoint.State.ToolOutputs, checkpoint.State.ToolEvents = nil, nil
	})
	_, err = decodeWorkflowCheckpoint(saved, definition)
	require.ErrorIs(t, err, registrycontract.ErrCompiledContractChanged)
}

func TestHistoricalToolEventRejectsMalformedEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*api.ToolEvent)
	}{
		{"result JSON", func(event *api.ToolEvent) { event.Result = rawjson.Message(`{`) }},
		{"result identity", func(event *api.ToolEvent) { event.ToolCallID = "another-call" }},
		{"failure and result", func(event *api.ToolEvent) {
			event.Failure = testToolFailure(planner.FailureUnavailable, planner.RecoveryReplan, "unavailable")
		}},
		{"server envelope", func(event *api.ToolEvent) { event.ServerData = rawjson.Message(`{}`) }},
		{"server audience", func(event *api.ToolEvent) {
			event.ServerData = rawjson.Message(`[{"kind":"old.card","audience":"other","data":{}}]`)
		}},
		{"server kind", func(event *api.ToolEvent) {
			event.ServerData = rawjson.Message(`[{"kind":"","audience":"timeline","data":{}}]`)
		}},
		{"truncated bounds", func(event *api.ToolEvent) { event.Bounds = &agent.Bounds{Truncated: true} }},
		{"empty cursor", func(event *api.ToolEvent) { event.Bounds = &agent.Bounds{Truncated: true, NextCursor: new("")} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := &api.ToolEvent{Name: genrecords.Read, ToolCallID: "call-1", Result: rawjson.Message(`{"count":4}`)}
			test.change(event)
			require.Error(t, validateHistoricalToolEvent(event, ToolCall{Name: genrecords.Read, ToolCallID: "call-1"}))
		})
	}
}

func TestUnmaterializedResultsKeepCurrentGeneratedTypes(t *testing.T) {
	spec := genrecords.SpecRead()
	spec.Result = genrecords.SpecErase().Result
	definition := testAgentDefinition("records.agent", "records.workflow", "test", []tools.ToolSpec{spec}, nil)
	call := ToolCall{Name: spec.Name, ToolCallID: "call-1"}
	_, err := decodeCheckpointToolEvent(&api.ToolEvent{Name: spec.Name, ToolCallID: call.ToolCallID, Result: rawjson.Message(`{"count":4}`)}, call, definition)
	require.Error(t, err)
	decoded, err := decodeCheckpointToolEvent(&api.ToolEvent{Name: spec.Name, ToolCallID: call.ToolCallID, Result: rawjson.Message(`{"erased":true}`)}, call, definition)
	require.NoError(t, err)
	require.Equal(t, &genrecords.EraseResult{Erased: true}, decoded.Result)
}
