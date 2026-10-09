package runtime

// A real service activity finishes beside a child that suspends twice. Its
// generated typed payload and input-dependent result hint are accepted once;
// new workers must project the saved event without decoding those inputs again.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/api"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestMaterializedHistoryAcrossSuspendedChildAndPreparedStart(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete content", true: "saved omission preview"}[oversized], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			store := newTestStore()
			var executions, currentDecodes atomic.Int32
			resumed := make(chan *planner.PlanResumeInput, 1)
			generated := genpictures.SpecView()
			read := tools.ToolSpec{
				Name: "records.inspect", Payload: generated.Payload, Result: generated.Result,
				ExecutionPayloadSchema: generated.ExecutionPayloadSchema,
				ExecutionPayloadCodec:  generated.ExecutionPayloadCodec,
				ResultReminder:         "Use the accepted observation.",
			}
			delegate := tools.ToolSpec{
				Name: "delegate.inspect", IsAgentTool: true, AgentID: "nested.reader",
				Payload: generated.Payload, Result: generated.Result,
				ExecutionPayloadSchema: generated.ExecutionPayloadSchema,
				ExecutionPayloadCodec:  generated.ExecutionPayloadCodec,
			}
			childDefinition := testAgentDefinition("nested.reader", "child.workflow", "child.queue", []tools.ToolSpec{delegate}, nil)
			payload, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "accepted-selection"})
			require.NoError(t, err)
			childPayload, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "child-selection"})
			require.NoError(t, err)
			resultID := "accepted-result"
			if oversized {
				resultID = strings.Repeat("x", transcript.MaxToolResultContentBytes+1)
			}
			resultJSON, err := genpictures.MarshalViewResult(&genpictures.ViewResult{ID: resultID})
			require.NoError(t, err)

			// Generated toolsets install hints through this compiler and
			// registration field. Args is the actual generated ViewPayload.
			newRuntime := func(spec tools.ToolSpec, hint string) (*Runtime, AgentDefinition) {
				rt := New(store, WithEngine(engineinmem.New()))
				hints, err := rthints.CompileHintTemplates(map[tools.Ident]string{spec.Name: hint}, nil)
				require.NoError(t, err)
				require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
					Name: "records", Specs: []tools.ToolSpec{spec}, ResultHints: hints,
					Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
						executions.Add(1)
						return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID,
							Result: &genpictures.ViewResult{ID: resultID}, Blocks: richToolContentForTest()}, nil
					}),
				}))
				require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
					Definition: childDefinition, WorkflowHandler: rt.ExecuteWorkflow,
					PlanActivityName: "child.plan", ResumeActivityName: "child.resume", ExecuteToolActivity: "child.execute",
					Planner: &stubPlanner{
						start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
							return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(
								&planner.AwaitClarification{ID: "first", Question: "Confirm the first detail?"},
							))}, nil
						},
						resume: func(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
							if in.RunContext.ParentRunID == "parent-1" {
								return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(
									&planner.AwaitClarification{ID: "second", Question: "Confirm the second detail?"},
								))}, nil
							}
							value, err := genpictures.MarshalViewResult(&genpictures.ViewResult{ID: "child-result"})
							return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: value, Blocks: richToolContentForTest()}}, err
						},
					},
				}))
				registration := NewAgentToolsetRegistration(AgentToolConfig{Definition: childDefinition, Name: "delegate"})
				registration.Specs = []tools.ToolSpec{delegate}
				require.NoError(t, rt.RegisterToolset(registration))
				definition := NewAgentDefinition(
					AgentRoute{ID: "parent.reader", WorkflowName: "parent.workflow", DefaultTaskQueue: "parent.queue"},
					[]tools.ToolSpec{spec, delegate}, nil, nil, []tools.Ident{spec.Name, delegate.Name}, []AgentDefinition{childDefinition}, nil,
				)
				require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
					Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
					PlanActivityName: "parent.plan", ResumeActivityName: "parent.resume", ExecuteToolActivity: "parent.execute",
					Planner: &stubPlanner{
						start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
							return &planner.PlanResult{ToolCalls: []planner.ToolRequest{
								{Name: spec.Name, Payload: payload}, {Name: delegate.Name, Payload: childPayload},
							}}, nil
						},
						resume: func(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
							resumed <- in
							return finalPlannerResult("complete"), nil
						},
					},
				}))
				return rt, definition
			}
			old, oldDefinition := newRuntime(read, "Accepted {{ .Args.ID }}")
			_, err = createSessionForTest(ctx, store, "session-1")
			require.NoError(t, err)
			first, err := old.MustClient("parent.reader").Run(ctx, "session-1",
				[]*model.Message{userMsg("Inspect and confirm")}, WithRunID("parent-0"))
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			firstCheckpoint, err := decodeWorkflowCheckpoint(first.Suspension, oldDefinition)
			require.NoError(t, err)
			require.Zero(t, firstCheckpoint.Batch.Recorded)
			accepted := firstCheckpoint.Batch.Records[0]
			require.True(t, accepted.ResultPublished)
			require.NotNil(t, accepted.ResultRecord)
			require.Equal(t, rawjson.Message(payload), accepted.Call.Payload)
			event, err := decodeToolResultRecord(accepted.ResultRecord, accepted.Call, accepted.CallRunID, accepted.ResultRunID)
			require.NoError(t, err)
			require.Equal(t, "Accepted accepted-selection", event.ResultPreview)
			require.Equal(t, rawjson.Message(resultJSON), event.ResultJSON)
			require.Equal(t, richToolContentForTest(), event.Blocks)
			firstBytes := append(rawjson.Message(nil), first.Suspension.Checkpoint...)

			// Prepare while old code still owns the contract, then submit its
			// unchanged bytes through a fresh worker with a rejecting codec.
			prepared, err := old.MustClient("parent.reader").PrepareContinuation(ctx,
				"session-1", "parent-0", "parent-1", "turn-1",
				&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "first", Answer: "yes"}}, WorkflowOptions{})
			require.NoError(t, err)
			preparedBytes, err := prepared.MarshalBinary()
			require.NoError(t, err)
			read.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
				currentDecodes.Add(1)
				return nil, errors.New("retired input cannot execute")
			}
			current, currentDefinition := newRuntime(read, "Current {{ len .Args.ID }}")
			parsed, err := ParsePreparedRun(preparedBytes)
			require.NoError(t, err)
			handle, err := current.MustClient("parent.reader").StartPrepared(ctx, parsed)
			require.NoError(t, err)
			second, err := handle.Wait(ctx)
			require.NoError(t, err)
			require.NotNil(t, second.Suspension)
			require.Equal(t, firstBytes, first.Suspension.Checkpoint)
			secondCheckpoint, err := decodeWorkflowCheckpoint(second.Suspension, currentDefinition)
			require.NoError(t, err)
			require.Zero(t, secondCheckpoint.Batch.Recorded)
			require.Equal(t, accepted, secondCheckpoint.Batch.Records[0])

			last, err := current.MustClient("parent.reader").Continue(ctx,
				"session-1", "parent-1", "parent-2", "turn-2",
				&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "second", Answer: "yes"}}, WorkflowOptions{})
			require.NoError(t, err)
			require.Nil(t, last.Suspension)
			require.Equal(t, "complete", last.Final.Text())
			require.EqualValues(t, 1, executions.Load())
			require.Zero(t, currentDecodes.Load())
			in := <-resumed
			require.Len(t, in.ToolOutputs, 2)
			for _, output := range in.ToolOutputs {
				require.Equal(t, richToolContentForTest(), output.Blocks)
			}
			require.Equal(t, accepted.Call.ToolCallID, in.ToolOutputs[0].ToolCallID)
			require.Equal(t, rawjson.Message(payload), in.ToolOutputs[0].Payload)
			require.Equal(t, rawjson.Message(resultJSON), in.ToolOutputs[0].Result)
			expectedContent, err := transcript.ProjectToolResultContent(resultJSON, nil, event.ResultPreview, "")
			require.NoError(t, err)
			var resultIDs []string
			for _, message := range in.Messages {
				for _, part := range message.Parts {
					if result, ok := part.(model.ToolResultPart); ok {
						resultIDs = append(resultIDs, result.ToolUseID)
						require.Equal(t, richToolContentForTest(), result.Blocks)
						if result.ToolUseID == transcriptToolCallID(accepted.Call) {
							require.Equal(t, expectedContent, result.Content)
						}
					}
				}
			}
			require.Equal(t, []string{
				transcriptToolCallID(accepted.Call),
				transcriptToolCallID(firstCheckpoint.Batch.Records[1].Call),
			}, resultIDs)
			var acceptedEvents int
			for _, id := range []string{"parent-0", "parent-1", "parent-2"} {
				page, err := current.ListRunEvents(ctx, id, "", 100)
				require.NoError(t, err)
				for _, recorded := range page.Events {
					if recorded.Type != hooks.ToolResultReceived {
						continue
					}
					decoded, err := hooks.DecodeFromRecordInput(&RecordActivityInput{
						Type: recorded.Type, RunID: recorded.RunID, AgentID: recorded.AgentID,
						SessionID: recorded.SessionID, Payload: recorded.Payload,
					})
					require.NoError(t, err)
					result := decoded.(*hooks.ToolResultReceivedEvent)
					if result.ToolCallID == accepted.Call.ToolCallID {
						acceptedEvents++
						require.Equal(t, rawjson.Message(resultJSON), result.ResultJSON)
						require.Equal(t, event.ResultPreview, result.ResultPreview)
						require.Equal(t, richToolContentForTest(), result.Blocks)
					}
				}
			}
			require.Equal(t, 1, acceptedEvents)
			require.ErrorContains(t, validateCheckpointToolRequest(accepted.Call, currentDefinition), "retired input cannot execute")
		})
	}
}
