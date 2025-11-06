//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

func TestRunPlanActivityUsesOptions(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{
		ctx:           context.Background(),
		hasPlanResult: true,
		planResult: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: planner.AgentMessage{Role: "assistant", Content: "ok"},
			},
		},
	}

	opts := engine.ActivityOptions{
		Queue:       "custom_queue",
		Timeout:     30 * time.Second,
		RetryPolicy: engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Second, BackoffCoefficient: 2},
	}
	_, err := rt.runPlanActivity(wf, "calc.agent.plan", opts, PlanActivityInput{})
	require.NoError(t, err)
	require.Equal(t, opts.Queue, wf.lastRequest.Queue)
	require.Equal(t, opts.Timeout, wf.lastRequest.Timeout)
	require.Equal(t, opts.RetryPolicy, wf.lastRequest.RetryPolicy)
}

func TestPlanStartActivityInvokesPlanner(t *testing.T) {
	called := false
	pl := &stubPlanner{start: func(ctx context.Context, input planner.PlanInput) (*planner.PlanResult, error) {
		called = true
		require.Equal(t, run.Context{RunID: "run-123"}, input.RunContext)
		require.Len(t, input.Messages, 1)
		require.Equal(t, "hello", input.Messages[0].Content)
		require.NotNil(t, input.Agent)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: "ok"}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	input := PlanActivityInput{AgentID: "service.agent", RunID: "run-123", Messages: []planner.AgentMessage{{Role: "user", Content: "hello"}}, RunContext: run.Context{RunID: "run-123"}}
	out, err := rt.PlanStartActivity(context.Background(), input)
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, out.Result.FinalResponse)
}

func TestPlanResumeActivityPassesToolResults(t *testing.T) {
	called := false
	toolResults := []*planner.ToolResult{{Name: "svc.ts.tool"}}
	pl := &stubPlanner{resume: func(ctx context.Context, input planner.PlanResumeInput) (*planner.PlanResult, error) {
		called = true
		require.Equal(t, toolResults, input.ToolResults)
		require.Equal(t, 3, input.RunContext.Attempt)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "svc.other.tool"}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	input := PlanActivityInput{AgentID: "service.agent", RunID: "run-123", RunContext: run.Context{RunID: "run-123", Attempt: 3}, ToolResults: toolResults}
	out, err := rt.PlanResumeActivity(context.Background(), input)
	require.NoError(t, err)
	require.True(t, called)
	require.Len(t, out.Result.ToolCalls, 1)
}
