package runtime

// Run a real child, failed-result persistence, recovery planning, and final
// answer through the workflow loop. A timeout is not successful evidence and
// never grants alternative work unless the provider declared that safe.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestAgentTimeoutRecoveryKeepsFailureAndOtherWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		marked bool
		cause  error
		kind   planner.FailureKind
		replan bool
	}{
		{"declared lookup", true, temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, nil), planner.FailureTimeout, true},
		{"unknown write outcome", false, temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, nil), planner.FailureTimeout, false},
		{"unrelated invariant", true, errors.New("lookup invariant failed"), planner.FailureInternal, false},
		{"older generic timeout", true, temporal.NewApplicationError("activity StartToClose timeout", "old_generic"), planner.FailureInternal, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookup := newAnyJSONSpec("lookup.find")
			lookup.IsAgentTool, lookup.ReplanOnTimeout = true, test.marked
			lookup.AgentID = "lookup.agent"
			alternative := newAnyJSONSpec("catalog.list")
			var resumes, childCalls, alternativeCalls int
			h := newRecoveryHarness(t, "timeout", []tools.ToolSpec{alternative},
				func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					alternativeCalls++
					return successfulToolResult(call), nil
				},
				func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					resumes++
					if resumes > 1 {
						return finalPlannerResult("Alternative evidence; lookup remains incomplete."), nil
					}
					require.Len(t, input.ToolOutputs, 1)
					failure := input.ToolOutputs[0].Failure
					require.NotNil(t, failure)
					assert.Equal(t, test.kind, failure.Kind)
					assert.NotNil(t, failure.Error)
					assert.Nil(t, input.ToolOutputs[0].Result)
					if test.replan {
						assert.Equal(t, planner.RecoveryReplan, failure.Recovery.Action)
						assert.Nil(t, input.Finalize)
						assertAdvertisedTools(t, input, alternative.Name)
						return &planner.PlanResult{
							ToolCalls:            []planner.ToolRequest{{Name: alternative.Name, Payload: rawjson.Message(`{}`)}},
							SynthesizeAfterTools: true,
						}, nil
					}
					assert.Equal(t, planner.RecoveryFinish, failure.Recovery.Action)
					require.NotNil(t, input.Finalize)
					assertAdvertisedTools(t, input)
					return finalPlannerResult("Required work failed."), nil
				},
			)
			childDefinition := testAgentDefinition("lookup.agent", "lookup.workflow", "lookup.queue", nil, nil)
			require.NoError(t, h.runtime.RegisterAgent(t.Context(), AgentRegistration{
				Definition: childDefinition, WorkflowHandler: h.runtime.ExecuteWorkflow,
				Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					childCalls++
					return nil, temporalerrors.Wrap(test.cause)
				}},
				PlanActivityName: "lookup.plan", ResumeActivityName: "lookup.resume", ExecuteToolActivity: "lookup.execute",
			}))
			registration := NewAgentToolsetRegistration(h.runtime, AgentToolConfig{
				Definition: childDefinition, Name: "lookup",
			})
			registration.Specs = []tools.ToolSpec{lookup}
			require.NoError(t, h.runtime.RegisterToolset(registration))
			specs := []tools.ToolSpec{lookup, alternative}
			h.registration.Definition = testRegistrationDefinition(h.input.AgentID, engine.WorkflowDefinition{}, specs)
			h.runtime.agents[h.input.AgentID] = h.registration
			h.runtime.agentToolSpecs[h.input.AgentID] = specs
			h.workflow.childRuntime = h.runtime
			h.workflow.plannerRoutes["lookup.plan"] = h.runtime.PlanStartActivity
			out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
				Name: lookup.Name, ToolCallID: "lookup-call", Payload: rawjson.Message(`{}`),
			}}}, initialCaps(RunPolicy{MaxToolCalls: 5, MaxRecoveryTurns: 3}))
			require.NoError(t, err)
			require.NotNil(t, out)
			assert.Equal(t, 1, childCalls, "the timed-out operation must not be retried automatically")
			if test.replan {
				assert.Equal(t, 1, alternativeCalls)
				assert.Contains(t, out.Final.Text(), "lookup remains incomplete")
			} else {
				assert.Zero(t, alternativeCalls)
			}
		})
	}
}

func TestTimeoutRecoveryRegistrationKeepsProviderContract(t *testing.T) {
	spec := newAnyJSONSpec("lookup.find")
	spec.ReplanOnTimeout = true
	rt := New(newTestStore())
	err := rt.RegisterToolset(ToolsetRegistration{
		Name: "lookup", Specs: []tools.ToolSpec{spec}, Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		}),
	})
	require.ErrorContains(t, err, "requires an agent-as-tool registration")
	plain := spec
	plain.ReplanOnTimeout = false
	assert.False(t, equivalentToolSpec(spec, plain), "registrations must not disagree about timeout recovery")
}
