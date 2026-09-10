package runtime

// These tests follow local model-request rejections through real tool and
// planner activities. A rejected request must end work rather than become a
// tool failure that asks the planner for another answer.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRequestValidationToolFailureStopsWorkflow(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprintf("inline=%t", inline), func(t *testing.T) {
			rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
			spec := newAnyJSONSpec("catalog.lookup")
			var starts, resumes, executions int
			cause := model.NewRequestValidationError(errors.New("lookup model request rejected locally"))
			require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
				Name: "catalog", Inline: inline, Specs: []tools.ToolSpec{spec},
				Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
					executions++
					return nil, cause
				},
			}))
			require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
				Definition: NewAgentDefinition(
					AgentRoute{ID: "service.agent", WorkflowName: "service.workflow", DefaultTaskQueue: "queue"},
					[]tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, nil,
				),
				WorkflowHandler: func(wfCtx engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
					return rt.ExecuteWorkflow(wfCtx, input)
				},
				Planner: &stubPlanner{
					start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
						starts++
						return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: spec.Name, Payload: rawjson.Message(`{}`)}}}, nil
					},
					resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
						resumes++
						return finalPlannerResult("unexpected answer"), nil
					},
				},
				PlanActivityName: "plan", ResumeActivityName: "resume", ExecuteToolActivity: "execute",
				Policy: RunPolicy{MaxToolCalls: 2, MaxRecoveryTurns: 2},
			}))
			_, err := createSessionForTest(t.Context(), rt.Store, "session-1")
			require.NoError(t, err)
			wfCtx := &routeWorkflowContext{
				ctx: t.Context(), runID: "run-1", hookRuntime: rt,
				plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
					"plan": rt.PlanStartActivity, "resume": rt.PlanResumeActivity,
				},
				toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
					"execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
						out, err := rt.ExecuteToolActivity(ctx, input)
						return out, temporalerrors.Wrap(err)
					},
				},
			}
			out, err := rt.ExecuteWorkflow(wfCtx, &RunInput{
				AgentID: "service.agent", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1",
			})
			require.Error(t, err)
			assert.Nil(t, out)
			assert.True(t, temporalerrors.IsRequestValidation(err), "%v", err)
			assert.Equal(t, 1, starts)
			assert.Equal(t, 1, executions)
			assert.Zero(t, resumes)
			failure := hooks.RunFailureFromError(err)
			assert.Equal(t, hooks.ErrorKindModelRequest, failure.Kind)
			assert.False(t, failure.Retryable)
			assert.Contains(t, failure.DebugMessage, cause.Error())
		})
	}
}

func TestValidateRequestPlanningFailure(t *testing.T) {
	valid := run.Failure{
		Message: hooks.PublicErrorModelRequest, DebugMessage: "original diagnostic", Kind: hooks.ErrorKindModelRequest,
	}
	require.NoError(t, validatePlanningFailure(&valid))
	for _, test := range []struct {
		name   string
		mutate func(*run.Failure)
	}{
		{"retryable", func(f *run.Failure) { f.Retryable = true }},
		{"provider", func(f *run.Failure) { f.Provider = "provider" }},
		{"operation", func(f *run.Failure) { f.Operation = "complete" }},
		{"code", func(f *run.Failure) { f.Code = "bad_request" }},
		{"HTTP status", func(f *run.Failure) { f.HTTPStatus = 400 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := valid
			test.mutate(&failure)
			require.EqualError(t, validatePlanningFailure(&failure), "model request failure must be nonretryable without provider facts")
		})
	}
}
