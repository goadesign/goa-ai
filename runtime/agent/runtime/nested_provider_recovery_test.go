package runtime

// These tests send a parent request through the in-memory workflow engine.
// A completed tool action must stay completed while a child retries one clean
// model-provider failure using the parent's recovery allowance.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	"goa.design/goa-ai/runtime/agent"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNestedProviderRecoveryPreservesCompletedAction(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	rt := New(newTestStore(), WithEngine(engineinmem.New()), WithLogger(telemetry.NoopLogger{}))
	var actions, modelCalls, parentResumes atomic.Int32
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "records", Specs: genrecords.Specs(),
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			actions.Add(1)
			return Executed(&planner.ToolResult{
				Name: call.Name, Result: &genrecords.ReadResult{Count: 1},
			}), nil
		},
	}))
	const childID = agent.Ident("helper.agent")
	generated := genpictures.SpecView()
	childTool := tools.ToolSpec{
		Name: "delegate.inspect", IsAgentTool: true, AgentID: string(childID),
		Payload: generated.Payload, Result: generated.Result,
		ExecutionPayloadSchema: generated.ExecutionPayloadSchema,
		ExecutionPayloadCodec:  generated.ExecutionPayloadCodec,
	}
	childDefinition := testAgentDefinition(childID, "helper.workflow", "helper.queue", []tools.ToolSpec{childTool}, nil)
	rt.models["synthetic"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			if modelCalls.Add(1) == 1 {
				return nil, model.NewProviderError(
					"synthetic", "complete", 429, model.ProviderErrorKindRateLimited,
					"rate_limit", "try later", "request-1", true, nil,
				)
			}
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "inspection complete"}},
				}},
				StopReason: "stop",
			}, nil
		},
	})
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition: childDefinition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "helper.plan", ResumeActivityName: "helper.resume", ExecuteToolActivity: "helper.execute",
		Planner: &stubPlanner{
			start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				// This helper constructs the typed parent result below. The
				// model call is a probe, so its response is not designated as
				// the planner's completed answer.
				client, ok := input.Agent.ModelClient("synthetic")
				if !ok {
					return nil, fmt.Errorf("synthetic model client is not registered")
				}
				if _, err := client.Complete(ctx, &model.Request{Model: "synthetic", Messages: input.Messages}); err != nil {
					return nil, err
				}
				result, err := genpictures.MarshalViewResult(&genpictures.ViewResult{ID: "selected"})
				if err != nil {
					return nil, err
				}
				return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: result}}, nil
			},
		},
	}))
	childRegistration := NewAgentToolsetRegistration(AgentToolConfig{
		Definition: childDefinition, Name: "delegate",
	})
	childRegistration.Specs = []tools.ToolSpec{childTool}
	require.NoError(t, rt.RegisterToolset(childRegistration))
	specs := append(genrecords.Specs(), childTool)
	parentDefinition := NewAgentDefinition(
		AgentRoute{ID: parentAgentID, WorkflowName: "parent.workflow", DefaultTaskQueue: "parent.queue"},
		specs, nil, nil, []tools.Ident{genrecords.Read, childTool.Name}, []AgentDefinition{childDefinition}, nil,
	)
	childPayload, err := genpictures.MarshalViewPayload(&genpictures.ViewPayload{ID: "selected"})
	require.NoError(t, err)
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		Definition: parentDefinition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "parent.plan", ResumeActivityName: "parent.resume", ExecuteToolActivity: "parent.execute",
		Planner: &stubPlanner{
			start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name: genrecords.Read, Payload: rawjson.Message(`{"query":"selected"}`),
				}}}, nil
			},
			resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				resume := parentResumes.Add(1)
				if len(input.ToolOutputs) != int(resume) {
					return nil, fmt.Errorf("parent received %d accumulated tool results on resume %d", len(input.ToolOutputs), resume)
				}
				if input.ToolOutputs[0].Name != genrecords.Read {
					return nil, fmt.Errorf("parent lost the original action result")
				}
				if resume == 1 {
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
						Name: childTool.Name, Payload: childPayload,
					}}}, nil
				}
				if input.ToolOutputs[1].Name != childTool.Name {
					return nil, fmt.Errorf("parent lost the child result")
				}
				return finalPlannerResult("the original action and inspection completed"), nil
			},
		},
	}))
	_, err = createSessionForTest(ctx, rt.Store, "nested-recovery")
	require.NoError(t, err)
	output, err := rt.MustClient(parentAgentID).Run(
		ctx, "nested-recovery", []*model.Message{userMsg("Complete the action and inspect the selection.")},
		WithRunID("parent-recovery"), WithTurnID("parent-turn"),
		WithProviderRetryBudget(time.Hour), WithRunTimeBudget(15*time.Minute),
	)
	assert.EqualValues(t, 1, actions.Load())
	assert.EqualValues(t, 2, modelCalls.Load())
	assert.EqualValues(t, 2, parentResumes.Load())
	require.NoError(t, err)
	require.NotNil(t, output.Final)
	assert.Equal(t, "the original action and inspection completed", output.Final.Text())
}
