package runtime

// Availability uses the same accepted native operation and labels as planning.
// Runs without an unfinished query can finalize without consulting providers.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	availabilityCatalogSource struct {
		resolve func(context.Context, *RegistryCatalog) error
	}
)

func TestContinuationAvailabilityPreservesNativeContextAndSkipsUnusedCatalog(t *testing.T) {
	for _, scenario := range []string{"open query", "open query registry unavailable", "closed query registry unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			rt := New(newTestStore())
			old, current := pagingReleaseRegistration("a"), pagingReleaseRegistration("b")
			resolved, err := registrycontract.ResolveCompiled(old)
			require.NoError(t, err)
			binding, err := resolved.Select(testRegistryName, "records.find")
			require.NoError(t, err)
			owned, err := registrycontract.Resolve(current)
			require.NoError(t, err)
			specs := []tools.ToolSpec{owned.Specs["records.find"], owned.Specs["records.continue"]}
			operation := testNativeRegistryResolution("revision/1").Toolset.Tools[0]
			runContext := run.Context{RunID: "availability", SessionID: "native-session", TurnID: "native-turn",
				Tool: tools.Ident(operation.Name), ToolArgs: rawjson.Message(`{"value":1}`), Operation: operation,
				Labels: map[string]string{"resource": "selected"}, Attempt: 7, MaxDuration: "1m"}
			reads := 0
			dependency := errors.New("registry unavailable")
			sources := availabilityCatalogSource{resolve: func(ctx context.Context, catalog *RegistryCatalog) error {
				reads++
				selected, err := catalog.SelectedOperation()
				require.NoError(t, err)
				assert.Equal(t, operation, selected)
				assert.Equal(t, runContext.Labels, catalog.RunLabels())
				assert.Equal(t, runContext, catalog.runContext)
				if scenario != "open query" {
					return dependency
				}
				return catalog.BindStaticService(testRegistryName, current.Toolset, current, verifyTestStaticResource, "records.find", "records.continue")
			}}
			definition := testAgentDefinition("generic.agent", "generic.workflow", "test", specs, nil).
				WithRegistryTools(sources).WithRegistryDeclarations(testDeclarations(current.Toolset.Tools))
			rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
			rt.registryDeclarations = definition.declarations
			seedTestToolSpecs(rt, specs...)
			require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
			subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(definition.route.ID))}
			admitRunForTest(t, rt.Store, session.RunMeta{Subject: subject, RunID: runContext.RunID, SessionID: runContext.SessionID, Status: session.RunStatusRunning})
			call := ToolCall{Name: "records.find", ToolCallID: "source", Registry: binding, Payload: rawjson.Message(`{"value":1}`)}
			appendHistoricalHookEvent(t, rt.Store, newToolCallScheduledEvent(runContext.RunID, subject, runContext.SessionID, call, "", "", 0), "source", 1)
			bounds := &agent.Bounds{Returned: 1, Truncated: true, NextCursor: new("cursor-a")}
			if scenario == "closed query registry unavailable" {
				bounds.Truncated, bounds.NextCursor = false, nil
			}
			result := rawjson.Message(`{"value":1,"returned":1,"truncated":true}`)
			if !bounds.Truncated {
				result = rawjson.Message(`{"value":1,"returned":1,"truncated":false}`)
			}
			appendHistoricalHookEvent(t, rt.Store, hooks.NewToolResultReceivedEvent(runContext.RunID, subject, runContext.SessionID, runContext.RunID,
				call.Name, call.ToolCallID, "", result, nil, nil, "saved", bounds, 0, nil, nil), "result", 2)
			input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: definition.route.ID, RunID: runContext.RunID, RunContext: runContext}, []*model.Message{userMsg("Continue.")})
			available, err := rt.continuationAvailableActivity(t.Context(), &api.ContinuationActivityInput{
				AgentID: definition.route.ID, RunContext: runContext, HistoryEndID: input.HistoryEndID,
			})
			if scenario == "open query registry unavailable" {
				require.ErrorIs(t, err, dependency)
				assert.False(t, available)
				assert.Equal(t, 1, reads)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, scenario == "open query", available)
			assert.Equal(t, map[bool]int{true: 1, false: 0}[available], reads)
		})
	}
}

func (s availabilityCatalogSource) Resolve(ctx context.Context, catalog *RegistryCatalog) error {
	return s.resolve(ctx, catalog)
}

func (availabilityCatalogSource) Allows(registry, _, _ string) bool {
	return registry == testRegistryName
}
