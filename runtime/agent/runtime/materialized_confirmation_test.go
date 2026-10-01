package runtime

// A completed call can wait for a sibling's confirmation before entering the
// transcript. Only the sibling's executable input still needs today's codec.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestMaterializedHistoryWithPendingConfirmation(t *testing.T) {
	for _, hint := range []string{"Accepted {{ .Args.ID }}", ""} {
		t.Run(map[bool]string{false: "input hint", true: "empty preview"}[hint == ""], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			store := newTestStore()
			generated := genpictures.SpecView()
			read := tools.ToolSpec{
				Name: tools.Ident("records.inspect_" + uuid.NewString()), Payload: generated.Payload, Result: generated.Result,
				ExecutionPayloadSchema: generated.ExecutionPayloadSchema,
				ExecutionPayloadCodec:  generated.ExecutionPayloadCodec,
			}
			change := read
			change.Name = "records.change"
			var reads, changes, currentDecodes atomic.Int32
			resumed := make(chan *planner.PlanResumeInput, 1)
			result := &genpictures.ViewResult{ID: strings.Repeat("x", transcript.MaxToolResultContentBytes+1)}
			newRuntime := func(readSpec, changeSpec tools.ToolSpec, template string) (*Runtime, AgentDefinition) {
				rt := New(store, WithEngine(engineinmem.New()),
					WithToolConfirmation(&ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{
						change.Name: {
							Prompt: func(context.Context, *ToolCall) (string, error) { return "Apply the change?", nil },
							DeniedResult: func(context.Context, *ToolCall) (any, error) {
								return &genpictures.ViewResult{ID: "denied"}, nil
							},
						},
					}}))
				hints, err := rthints.CompileHintTemplates(map[tools.Ident]string{read.Name: template}, nil)
				require.NoError(t, err)
				require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
					Name: "records", Specs: []tools.ToolSpec{readSpec, changeSpec}, ResultHints: hints,
					Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
						value := result
						if call.Name == read.Name {
							reads.Add(1)
						} else {
							changes.Add(1)
							value = &genpictures.ViewResult{ID: "changed"}
						}
						return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: value}, nil
					}),
				}))
				definition := NewAgentDefinition(
					AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"},
					[]tools.ToolSpec{readSpec, changeSpec}, nil, nil,
					[]tools.Ident{readSpec.Name, changeSpec.Name}, nil, nil,
				)
				require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
					Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
					PlanActivityName: "records.plan", ResumeActivityName: "records.resume", ExecuteToolActivity: "records.execute",
					Planner: &stubPlanner{
						start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
							return &planner.PlanResult{ToolCalls: []planner.ToolRequest{
								{Name: read.Name, Payload: rawjson.Message(`{"id":"accepted"}`)},
								{Name: change.Name, Payload: rawjson.Message(`{"id":"pending"}`)},
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
			old, oldDefinition := newRuntime(read, change, hint)
			_, err := createSessionForTest(ctx, store, "session-1")
			require.NoError(t, err)
			first, err := old.MustClient("records.agent").Run(ctx, "session-1",
				[]*model.Message{userMsg("Inspect, then request approval")}, WithRunID("run-0"))
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			checkpoint, err := decodeWorkflowCheckpoint(first.Suspension, oldDefinition)
			require.NoError(t, err)
			require.Zero(t, checkpoint.Batch.Recorded)
			require.Len(t, checkpoint.Batch.Records, 1)
			accepted := checkpoint.Batch.Records[0]
			require.True(t, accepted.ResultPublished)
			require.NotNil(t, accepted.ResultRecord)
			originalCheckpoint := append(rawjson.Message(nil), first.Suspension.Checkpoint...)
			event, err := decodeToolResultRecord(accepted.ResultRecord, accepted.Call, accepted.CallRunID, accepted.ResultRunID)
			require.NoError(t, err)
			expectedPreview := ""
			if hint != "" {
				expectedPreview = "Accepted accepted"
			}
			require.Equal(t, expectedPreview, event.ResultPreview)
			require.EqualValues(t, 1, reads.Load())
			require.Zero(t, changes.Load())
			prepared, err := old.MustClient("records.agent").PrepareContinuation(ctx, "session-1", "run-0", "run-1", "turn-1",
				&api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{
					ID: first.Suspension.Pending[0].Confirmation.ID, Approved: true, RequestedBy: "operator",
				}}, WorkflowOptions{})
			require.NoError(t, err)
			preparedBytes, err := prepared.MarshalBinary()
			require.NoError(t, err)

			read.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
				currentDecodes.Add(1)
				return nil, errors.New("retired completed input")
			}
			invalidChange := change
			invalidChange.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
				return nil, errors.New("pending input no longer valid")
			}
			rejecting, _ := newRuntime(read, invalidChange, "Current {{ len .Args.ID }}")
			parsed, err := ParsePreparedRun(preparedBytes)
			require.NoError(t, err)
			_, err = rejecting.MustClient("records.agent").StartPrepared(ctx, parsed)
			require.ErrorContains(t, err, "pending input no longer valid")
			require.Zero(t, changes.Load())

			current, currentDefinition := newRuntime(read, change, "Current {{ len .Args.ID }}")
			require.NoError(t, validateCheckpointToolValues(checkpoint, currentDefinition))
			testMaterializedCheckpointIntegrity(t, first.Suspension, currentDefinition)
			parsed, err = ParsePreparedRun(preparedBytes)
			require.NoError(t, err)
			handle, err := current.MustClient("records.agent").StartPrepared(ctx, parsed)
			require.NoError(t, err)
			last, err := handle.Wait(ctx)
			require.NoError(t, err)
			require.Nil(t, last.Suspension)
			require.Equal(t, "complete", last.Final.Text())
			require.EqualValues(t, 1, reads.Load())
			require.EqualValues(t, 1, changes.Load())
			require.Zero(t, currentDecodes.Load())
			require.Equal(t, originalCheckpoint, first.Suspension.Checkpoint)
			in := <-resumed
			require.Equal(t, accepted.Call.Payload, in.ToolOutputs[0].Payload)
			expected, err := transcript.ProjectToolResultContent(event.ResultJSON, event.Bounds, expectedPreview, "")
			require.NoError(t, err)
			var results []model.ToolResultPart
			for _, message := range in.Messages {
				for _, part := range message.Parts {
					if result, ok := part.(model.ToolResultPart); ok {
						results = append(results, result)
					}
				}
			}
			require.Len(t, results, 2)
			require.Equal(t, transcriptToolCallID(accepted.Call), results[0].ToolUseID)
			require.Equal(t, expected, results[0].Content)
			require.False(t, results[0].IsError)
			var publications int
			for _, runID := range []string{"run-0", "run-1"} {
				page, err := current.ListRunEvents(ctx, runID, "", 100)
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
					if decoded.(*hooks.ToolResultReceivedEvent).ToolCallID == accepted.Call.ToolCallID {
						publications++
						require.Equal(t, accepted.ResultRecord.Payload, recorded.Payload)
					}
				}
			}
			require.Equal(t, 1, publications)
		})
	}
}
