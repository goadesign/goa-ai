// Declared input bindings must be installed before any planner runs. A callback
// that omits work is an application failure, including during finalization.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestConsumerRequiredBindingsRejectOmittedInstallation(t *testing.T) {
	for _, finalize := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			for _, textOnly := range []bool{false, true} {
				name := "ordinary"
				if finalize {
					name = "finalizer"
				}
				if partial {
					name += "/partial"
				} else {
					name += "/noop"
				}
				if textOnly {
					name += "/text-only"
				}
				t.Run(name, func(t *testing.T) {
					rt := New(newTestStore())
					first, err := contract.Compile(consumerResource("owner").Resolved.Toolset.Tools[0])
					require.NoError(t, err)
					first.TerminalRun = true
					first.Bookkeeping = true
					second := cloneToolSpec(first)
					second.Name = "records.other"
					seedTestToolSpecs(rt, first, second)
					seedTestToolDefinitions(rt, first, second)
					required := []tools.Ident{first.Name, second.Name}
					definition := testAgentDefinition("records.agent", "records.workflow", "records.queue", []tools.ToolSpec{first, second}, nil).WithInputBinding(required, func(_ context.Context, catalog *RegistryCatalog) error {
						if partial {
							return catalog.BindInput(first, consumerBoundSpec(t, first, consumerScopeRef{ID: "accepted"}))
						}
						return nil
					})
					required[0] = second.Name
					assert.Equal(t, first.Name, definition.requiredInputBindings[0], "caller cannot change required declarations")
					plannerCalls := 0
					rt.agentToolSpecs[definition.route.ID] = []tools.ToolSpec{first, second}
					rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: &stubPlanner{
						start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
							plannerCalls++
							return nil, errors.New("must not reach planner")
						},
						resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
							plannerCalls++
							return nil, errors.New("must not reach planner")
						},
					}}
					input := PlanActivityInput{AgentID: definition.route.ID, RunID: "required-run", RunContext: run.Context{RunID: "required-run", TextOnly: textOnly}, Policy: &PolicyOverrides{TextOnly: textOnly}}
					if finalize {
						input.Finalize = &planner.Termination{Reason: planner.TerminationReasonToolFailure}
					}
					prepared := seedTestPlanInput(t, rt, input, nil)
					var output *PlanActivityOutput
					if finalize {
						output, err = rt.PlanResumeActivity(t.Context(), prepared)
					} else {
						output, err = rt.PlanStartActivity(t.Context(), prepared)
					}
					require.ErrorContains(t, err, "required input binding")
					expected := first.Name
					if partial {
						expected = second.Name
					}
					assert.Contains(t, err.Error(), string(expected))
					assert.Nil(t, output)
					assert.Zero(t, plannerCalls)
					assert.True(t, engine.IsActivityErrorNonRetryable(err))
					var modelFailure *planner.OutputContractError
					assert.NotErrorAs(t, err, &modelFailure)
				})
			}
		}
	}
}

func TestConsumerRequiredBindingsRejectUnknownOrRepeatedStaticTools(t *testing.T) {
	original, err := contract.Compile(consumerResource("owner").Resolved.Toolset.Tools[0])
	require.NoError(t, err)
	definition := testAgentDefinition("records.agent", "records.workflow", "records.queue", []tools.ToolSpec{original}, nil)
	prepare := func(context.Context, *RegistryCatalog) error { return nil }
	assert.Panics(t, func() { definition.WithInputBinding([]tools.Ident{"foreign.tool"}, prepare) })
	assert.Panics(t, func() { definition.WithInputBinding([]tools.Ident{original.Name, original.Name}, prepare) })
}
