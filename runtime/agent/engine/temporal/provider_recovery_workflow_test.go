package temporal

// The Temporal SDK workflow test environment keeps accepted tool progress while timers
// wait for a provider. Every planner activity remains single-attempt; only a
// completed activity carrying proven retry permission schedules another one.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	providerRecoveryPlanner struct {
		tool    tools.Ident
		resumes atomic.Int32
	}

	providerRecoveryModel struct {
		calls atomic.Int32
	}
)

func (p *providerRecoveryPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: p.tool, Payload: rawjson.Message(`{}`)}}}, nil
}

func (p *providerRecoveryPlanner) PlanResume(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.resumes.Add(1)
	if len(input.ToolOutputs) != 1 || input.ToolOutputs[0].Result == nil {
		return nil, errors.New("accepted tool result was not retained")
	}
	client, ok := input.Agent.PlannerModelClient("synthetic")
	if !ok {
		return nil, errors.New("synthetic model is not registered")
	}
	response, err := client.Complete(ctx, &model.Request{Model: "synthetic", Messages: input.Messages})
	if err != nil {
		return nil, err
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
}

func (p *providerRecoveryModel) Complete(context.Context, *model.Request) (*model.Response, error) {
	if p.calls.Add(1) <= 2 {
		return nil, model.NewProviderError("synthetic", "complete", 429, model.ProviderErrorKindRateLimited, "temporary", "synthetic throttle", "", true, nil)
	}
	return &model.Response{Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "completed"}}}}, StopReason: "end_turn"}, nil
}

func (*providerRecoveryModel) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, errors.New("unexpected stream")
}

func TestProviderRecoveryWorkflowDoesNotRepeatCompletedTool(t *testing.T) {
	const workflowName, queue = "recovery.workflow", "recovery.queue"
	const planName, resumeName, executeName = "recovery.plan", "recovery.resume", "recovery.execute"
	agentID := agent.Ident("recovery.agent")
	tool := tools.Ident("evidence.read")
	spec := anyJSONToolSpec(tool)
	store := storageinmem.New()
	storageEngine := &storageCaptureEngine{Engine: engineinmem.New()}
	rt := agentruntime.New(store, agentruntime.WithEngine(storageEngine), agentruntime.WithLogger(telemetry.NoopLogger{}))
	_, err := store.CreateSession(t.Context(), "session", time.Now())
	require.NoError(t, err)
	seed := publishTemporalTestSeed(t, store, agentID, "run", "session")
	pl := &providerRecoveryPlanner{tool: tool}
	provider := &providerRecoveryModel{}
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	require.NoError(t, rt.RegisterModel("synthetic", client))
	require.NoError(t, rt.RegisterAgent(t.Context(), agentruntime.AgentRegistration{
		Definition: testTemporalAgentDefinition(agentID, workflowName, queue, []tools.ToolSpec{spec}),
		Planner:    pl, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: planName, ResumeActivityName: resumeName, ExecuteToolActivity: executeName,
		Policy: agentruntime.RunPolicy{MaxToolCalls: 3, MaxRecoveryTurns: 2},
	}))
	var toolCalls atomic.Int32
	require.NoError(t, rt.RegisterToolset(agentruntime.ToolsetRegistration{
		Name: "evidence", Specs: []tools.ToolSpec{spec},
		Execute: func(context.Context, *agentruntime.ToolCall) (*agentruntime.ToolExecutionResult, error) {
			toolCalls.Add(1)
			return &agentruntime.ToolExecutionResult{ToolResult: &planner.ToolResult{Result: map[string]any{"reading": 42}}}, nil
		},
	}))
	eng := &Engine{defaultQueue: queue, activityOptions: map[string]engine.ActivityOptions{
		planName:   {StartToCloseTimeout: time.Minute, RetryPolicy: engine.RetryPolicy{MaxAttempts: 1}},
		resumeName: {StartToCloseTimeout: time.Minute, RetryPolicy: engine.RetryPolicy{MaxAttempts: 1}},
	}}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetDataConverter(NewAgentDataConverter())
	env.RegisterActivityWithOptions(storageEngine.store, activity.RegisterOptions{Name: "runtime.store"})
	env.RegisterActivityWithOptions(rt.PlanStartActivity, activity.RegisterOptions{Name: planName})
	env.RegisterActivityWithOptions(rt.PlanResumeActivity, activity.RegisterOptions{Name: resumeName})
	env.RegisterActivityWithOptions(rt.ExecuteToolActivity, activity.RegisterOptions{Name: executeName})
	input := prepareAcceptedTestWorkflow(t, env, workflowName, queue, &api.RunInput{
		AgentID: agentID, RunID: "run", SessionID: "session", TurnID: "turn", SeedEndID: seed,
		Policy: &api.PolicyOverrides{TimeBudget: time.Second, ProviderRetryBudget: time.Hour},
	})
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&workflowControlInterceptor{engine: eng}}})
	env.ExecuteWorkflow(eng.temporalWorkflowHandler(rt.ExecuteWorkflow), input)
	require.NoError(t, env.GetWorkflowError())
	var out api.RunOutput
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "completed", out.Final.Text())
	require.EqualValues(t, 1, toolCalls.Load())
	require.EqualValues(t, 3, pl.resumes.Load())
	require.EqualValues(t, 3, provider.calls.Load())
}

func TestProviderCertificateCrossesBothPlannerActivityAdapters(t *testing.T) {
	for _, adapter := range []string{"inmem", "temporal"} {
		t.Run(adapter, func(t *testing.T) {
			const planName, workflowName, queue = "certificate.plan", "certificate.workflow", "certificate.queue"
			failure := model.NewProviderError("synthetic", "complete", 503, model.ProviderErrorKindUnavailable,
				"capacity", "", "request-1", true, errors.New("réseau indisponible 世界"))
			produce := func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
				return &api.PlanActivityOutput{PublicationBatchID: "publication-1", ProviderFailure: failure}, nil
			}
			handler := func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
				output, err := w.ExecutePlannerActivity(engine.PlannerActivityCall{
					Name: planName, Input: &api.PlanActivityInput{},
					Options: engine.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: engine.RetryPolicy{MaxAttempts: 1}},
				})
				if err != nil {
					return nil, err
				}
				actual := output.ProviderFailure
				require.NotNil(t, actual)
				assert.NotSame(t, failure, actual)
				assert.Equal(t, failure.Provider(), actual.Provider())
				assert.Equal(t, failure.Operation(), actual.Operation())
				assert.Equal(t, failure.HTTPStatus(), actual.HTTPStatus())
				assert.Equal(t, failure.Kind(), actual.Kind())
				assert.Equal(t, failure.Code(), actual.Code())
				assert.Equal(t, failure.Message(), actual.Message())
				assert.Equal(t, failure.RequestID(), actual.RequestID())
				assert.Equal(t, failure.Retryable(), actual.Retryable())
				assert.Equal(t, failure.Error(), actual.Error())
				require.EqualError(t, actual.Unwrap(), failure.Unwrap().Error())
				assert.NotSame(t, failure.Unwrap(), actual.Unwrap())
				return &api.RunOutput{}, nil
			}
			if adapter == "inmem" {
				eng := engineinmem.New()
				require.NoError(t, eng.RegisterPlannerActivity(t.Context(), planName, engine.ActivityOptions{}, produce))
				require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{Name: workflowName, Handler: handler}))
				handle, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
					ID: "certificate-run", Workflow: workflowName, TaskQueue: queue,
					Input: &api.RunInput{RunID: "certificate-run"},
				})
				require.NoError(t, err)
				_, err = handle.Wait(t.Context())
				require.NoError(t, err)
				return
			}
			eng := &Engine{defaultQueue: queue, activityOptions: map[string]engine.ActivityOptions{}}
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.SetDataConverter(NewAgentDataConverter())
			env.RegisterActivityWithOptions(produce, activity.RegisterOptions{Name: planName})
			input := prepareAcceptedTestWorkflow(t, env, workflowName, queue, &api.RunInput{RunID: "certificate-run"})
			env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&workflowControlInterceptor{engine: eng}}})
			env.ExecuteWorkflow(eng.temporalWorkflowHandler(handler), input)
			require.NoError(t, env.GetWorkflowError())
		})
	}
}
