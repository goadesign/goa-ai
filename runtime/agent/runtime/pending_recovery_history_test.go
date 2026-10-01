package runtime

// Pending recovery explains a failed call without executing its arguments.
// Continuations must preserve that evidence and validate only fresh corrections.

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/api"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestPendingRecoveryHistoryAcrossInputContractChange(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    string
		invalid bool
	}{
		{"Continue valid", "Continue", false},
		{"Continue invalid", "Continue", true},
		{"Prepare valid", "Prepare", false},
		{"Prepare invalid", "Prepare", true},
		{"PreviouslyPrepared valid", "PreviouslyPrepared", false},
		{"PreviouslyPrepared invalid", "PreviouslyPrepared", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode := test.mode
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			store := newTestStore()
			oldSpec := newAnyJSONSpec("catalog.lookup")
			oldPayload := rawjson.Message(`{"id":1}`)
			var oldExecutions, newExecutions, historicalDecodes, invalidCorrections, validCorrections atomic.Int32
			newRuntime := func(spec tools.ToolSpec, p *stubPlanner, execute func(context.Context, *ToolCall) (*planner.ToolResult, error)) (*Runtime, AgentDefinition) {
				rt := New(store, WithEngine(engineinmem.New()))
				require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
					Name: "catalog", Specs: []tools.ToolSpec{spec}, Execute: wrapExecute(execute),
				}))
				definition := NewAgentDefinition(
					AgentRoute{ID: "recovery.agent", WorkflowName: "recovery.workflow", DefaultTaskQueue: "recovery.queue"},
					[]tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, nil, nil,
				)
				require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
					Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
					PlanActivityName: "recovery.plan", ResumeActivityName: "recovery.resume", ExecuteToolActivity: "recovery.execute",
					Planner: p,
				}))
				return rt, definition
			}
			old, oldDefinition := newRuntime(oldSpec, &stubPlanner{
				start: func(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
					client, ok := in.Agent.PlannerModelClient("initial")
					require.True(t, ok)
					response, err := client.Complete(ctx, &model.Request{
						Model: "initial", Messages: in.Messages, Tools: in.Agent.AdvertisedToolDefinitions(),
					})
					require.NoError(t, err)
					calls := response.ToolCalls()
					require.Len(t, calls, 1)
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
						Name: calls[0].Name, Payload: calls[0].Payload, ModelToolCallID: calls[0].ID,
					}}}, nil
				},
				resume: func(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
					require.Len(t, in.Reminders, 1)
					return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(
						&planner.AwaitClarification{ID: "choice", Question: "Which record?"},
					))}, nil
				},
			}, func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
				oldExecutions.Add(1)
				return invalidCallResult(call), nil
			})
			require.NoError(t, old.RegisterModel("initial", mustTestModelClient(stubModelClient{
				complete: func(context.Context, *model.Request) (*model.Response, error) {
					return &model.Response{
						Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{
							model.ToolUsePart{ID: "initial-failed-call", Name: oldSpec.Name.String(), Input: oldPayload},
						}}}, StopReason: "tool_use",
					}, nil
				},
			})))
			_, err := createSessionForTest(ctx, store, "session-1")
			require.NoError(t, err)
			first, err := old.MustClient("recovery.agent").Run(ctx, "session-1",
				[]*model.Message{userMsg("Find the record")}, WithRunID("run-0"))
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			saved, err := decodeWorkflowCheckpoint(first.Suspension, oldDefinition)
			require.NoError(t, err)
			require.Len(t, saved.State.PendingRecovery, 1)
			require.Empty(t, saved.Batch.Calls)
			failed := saved.State.ToolOutputs[0]
			require.Equal(t, oldPayload, failed.Payload)
			require.Equal(t, oldPayload, failed.Failure.Recovery.PriorInput)
			original := append(rawjson.Message(nil), first.Suspension.Checkpoint...)
			answer := &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "choice", Answer: "Use the named record."}}
			var preparedBytes []byte
			if mode == "PreviouslyPrepared" {
				prepared, err := old.MustClient("recovery.agent").PrepareContinuation(ctx,
					"session-1", "run-0", "run-1", "turn-1", answer, WorkflowOptions{})
				require.NoError(t, err)
				preparedBytes, err = prepared.MarshalBinary()
				require.NoError(t, err)
			}

			// The generated current payload requires a string ID. Neither the
			// failed integer nor a fresh boolean can satisfy that codec.
			currentSpec := oldSpec
			generated := genpictures.SpecView()
			currentSpec.Payload = generated.Payload
			currentSpec.ExecutionPayloadSchema = generated.ExecutionPayloadSchema
			currentSpec.ExecutionPayloadCodec = generated.ExecutionPayloadCodec
			decode := currentSpec.ExecutionPayloadCodec.FromJSON
			currentSpec.ExecutionPayloadCodec.FromJSON = func(raw []byte) (any, error) {
				switch {
				case bytes.Equal(raw, oldPayload):
					historicalDecodes.Add(1)
				case bytes.Equal(raw, []byte(`{"id":false}`)):
					invalidCorrections.Add(1)
				case bytes.Equal(raw, []byte(`{"id":"corrected"}`)):
					validCorrections.Add(1)
				}
				return decode(raw)
			}
			resumes := 0
			current, definition := newRuntime(currentSpec, &stubPlanner{
				resume: func(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
					resumes++
					require.Equal(t, failed.Payload, in.ToolOutputs[0].Payload)
					actual := in.ToolOutputs[0].Failure
					assert.Equal(t, failed.Failure.Kind, actual.Kind)
					assert.Equal(t, failed.Failure.Error, actual.Error)
					assert.Equal(t, failed.Failure.Recovery.Action, actual.Recovery.Action)
					assert.Equal(t, failed.Failure.Recovery.PriorInput, actual.Recovery.PriorInput)
					assert.Equal(t, failed.Failure.Recovery.ExampleJSON, actual.Recovery.ExampleJSON)
					assert.Empty(t, actual.Recovery.Issues)
					if resumes == 1 {
						require.Len(t, in.Reminders, 1)
						require.Contains(t, in.Reminders[0].Text, `"id":1`)
						payload := rawjson.Message(`{"id":"corrected"}`)
						if test.invalid {
							payload = rawjson.Message(`{"id":false}`)
						}
						return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
							Name: currentSpec.Name, Payload: payload,
						}}}, nil
					}
					require.Equal(t, 2, resumes)
					return finalPlannerResult("corrected"), nil
				},
			}, func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
				newExecutions.Add(1)
				require.Equal(t, rawjson.Message(`{"id":"corrected"}`), call.Payload)
				return successfulToolResult(call), nil
			})
			testPendingRecoveryIntegrity(t, first.Suspension, definition)
			client := current.MustClient("recovery.agent")
			var last *RunOutput
			if mode == "Continue" {
				last, err = client.Continue(ctx, "session-1", "run-0", "run-1", "turn-1", answer, WorkflowOptions{})
			} else {
				if mode == "Prepare" {
					prepared, prepareErr := client.PrepareContinuation(ctx,
						"session-1", "run-0", "run-1", "turn-1", answer, WorkflowOptions{})
					require.NoError(t, prepareErr)
					preparedBytes, err = prepared.MarshalBinary()
					require.NoError(t, err)
				}
				prepared, parseErr := ParsePreparedRun(preparedBytes)
				require.NoError(t, parseErr)
				handle, startErr := client.StartPrepared(ctx, prepared)
				require.NoError(t, startErr)
				last, err = handle.Wait(ctx)
			}
			require.Zero(t, historicalDecodes.Load())
			require.EqualValues(t, 1, oldExecutions.Load())
			require.Equal(t, original, first.Suspension.Checkpoint)
			if test.invalid {
				require.ErrorContains(t, err, "field \"id\" must be string, got boolean")
				require.Positive(t, invalidCorrections.Load())
				require.Zero(t, newExecutions.Load())
			} else {
				require.NoError(t, err)
				require.Equal(t, "corrected", last.Final.Text())
				require.Positive(t, validCorrections.Load())
				require.EqualValues(t, 1, newExecutions.Load())
			}
		})
	}
}

func testPendingRecoveryIntegrity(t *testing.T, suspension *api.RunSuspension, definition AgentDefinition) {
	t.Helper()
	for _, test := range []struct {
		name   string
		mutate func(*workflowCheckpoint)
	}{
		{"missing output", func(c *workflowCheckpoint) { c.State.ToolOutputs = nil }},
		{"missing failure event", func(c *workflowCheckpoint) { c.State.ToolEvents = nil }},
		{"duplicate recovery", func(c *workflowCheckpoint) {
			c.State.PendingRecovery = append(c.State.PendingRecovery, c.State.PendingRecovery[0])
		}},
		{"different input", func(c *workflowCheckpoint) { c.State.PendingRecovery[0].Payload = rawjson.Message(`{"id":2}`) }},
		{"different model call", func(c *workflowCheckpoint) { c.State.PendingRecovery[0].ModelToolCallID = "different-provider-call" }},
		{"different failure", func(c *workflowCheckpoint) { c.State.PendingRecovery[0].Failure.Error.Message = "different rejection" }},
		{"different example", func(c *workflowCheckpoint) {
			c.State.PendingRecovery[0].Failure.Recovery.ExampleJSON = rawjson.Message(`{"id":"other"}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkpoint, err := decodeWorkflowCheckpointState(suspension)
			require.NoError(t, err)
			test.mutate(checkpoint)
			require.Error(t, validateCheckpointInputs(checkpoint, definition))
		})
	}
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	require.NoError(t, err)
	paged := definition.specs[0]
	paged.Bounds = &tools.BoundsSpec{Paging: &tools.PagingSpec{
		SourceTool: paged.Name, CursorField: "cursor",
	}}
	pagedDefinition := NewAgentDefinition(definition.route, []tools.ToolSpec{paged}, nil, nil, nil, nil, nil)
	require.NoError(t, validateCheckpointToolValues(checkpoint, pagedDefinition))
	states, err := continuationStates(paged, paged, checkpoint.State.ToolOutputs)
	require.NoError(t, err)
	require.Empty(t, states)
}
