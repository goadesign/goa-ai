package runtime

// Saved registered results describe the contract used by their original call.
// These tests keep completed history readable while current tool execution
// continues to require the complete current declaration. Each case runs for a
// call saved with its full declaration ("direct", written by earlier releases)
// and for a call saved with only its compiled registration ("compiled").

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type historicalStaticSources struct{ selected *genregistry.ResolvedToolset }

// historicalSavedForms are the two ways a call to a compiled tool may be saved.
var historicalSavedForms = []string{"direct", "compiled"}

func TestHistoricalStaticRegistryBoundsUseSavedDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bounds *agent.Bounds
		want   string
	}{
		{name: "complete", bounds: &agent.Bounds{Returned: 1}},
		{name: "without bounds"},
		{name: "unfinished", bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("next")}},
		{name: "invalid empty cursor", bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("")}, want: "empty next_cursor"},
		{name: "invalid untruncated cursor", bounds: &agent.Bounds{Returned: 1, NextCursor: pointer("next")}, want: "without truncation"},
	} {
		for _, form := range historicalSavedForms {
			t.Run(tc.name+"/"+form, func(t *testing.T) {
				rt, def, binding := historicalStaticFixture(t, "metadata", form)
				output := sourceContinuationOutput("records.find", "saved-query", `{"value":1}`, "next")
				output.Registry = binding
				output.Bounds = tc.bounds
				subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(def.route.ID))}
				err := rt.validateHistoricalContinuationBounds(output, rt.agentTools(def.route.ID))
				if tc.want != "" {
					require.ErrorContains(t, err, tc.want)
				} else {
					require.NoError(t, err)
				}
				// Current work cannot use the old declaration in either form.
				_, _, err = rt.callSpec(ToolCall{Name: output.Name, Registry: binding, Subject: subject})
				if form == "compiled" {
					assert.ErrorIs(t, err, registrycontract.ErrCompiledContractChanged)
				} else {
					assert.ErrorContains(t, err, "differs from this agent's static contract")
				}
			})
		}
	}
}

func TestHistoricalStaticRegistryActionsRequireLiveCompatiblePair(t *testing.T) {
	for _, tc := range []struct {
		name, change      string
		complete, closing bool
		want              int
	}{
		{name: "completed annotation change", change: "metadata", complete: true},
		{name: "closing page annotation change", change: "metadata", closing: true},
		{name: "unfinished annotation change", change: "metadata"},
		{name: "unfinished payload change", change: "payload"},
		{name: "removed continuation", change: "remove continuation"},
		{name: "unchanged unfinished", want: 1},
	} {
		for _, form := range historicalSavedForms {
			t.Run(tc.name+"/"+form, func(t *testing.T) {
				rt, def, binding := historicalStaticFixture(t, tc.change, form)
				output := sourceContinuationOutput("records.find", "saved-query", `{"value":1}`, "next")
				output.Registry = binding
				if tc.complete {
					output.Bounds = &agent.Bounds{Returned: 1}
				}
				outputs := []*planner.ToolOutput{output}
				if tc.closing {
					next := historicalSavedBinding(t, form, "records.continue")
					outputs = append(outputs, &planner.ToolOutput{Name: "records.continue", ToolCallID: "saved-page", ContinuationRootToolCallID: "saved-query", Payload: rawjson.Message(`{"cursor":"next"}`), Registry: next, Bounds: &agent.Bounds{Returned: 1}})
				}
				actions, err := rt.availableContinuationActions(def.route.ID, outputs, false)
				require.NoError(t, err)
				want := tc.want
				if tc.name == "unfinished annotation change" && form == "direct" {
					want = 1
				}
				assert.Len(t, actions, want)
			})
		}
	}
}

func TestPlanStartPreservesRegisteredHistoryAfterContractChange(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		bounds       *agent.Bounds
	}{
		{name: "completed metadata change", change: "metadata", bounds: &agent.Bounds{Returned: 1}},
		{name: "unfinished metadata change", change: "metadata", bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("next")}},
		{name: "unfinished payload change", change: "payload", bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("next")}},
	} {
		for _, form := range historicalSavedForms {
			t.Run(tc.name+"/"+form, func(t *testing.T) {
				rt, def, binding := historicalStaticFixture(t, tc.change, form)
				called := false
				registration := rt.agents[def.route.ID]
				registration.Planner = &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
					called = true
					wantActions := 0
					if tc.change == "metadata" && tc.bounds.Truncated {
						wantActions = 1
					}
					assert.Len(t, historicalActions(input.Agent), wantActions)
					assert.Len(t, input.Messages, 3)
					return finalPlannerResult("New answer."), nil
				}}
				rt.agents[def.route.ID] = registration
				subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(def.route.ID))}
				call := ToolCall{Name: "records.find", ToolCallID: "saved-query", Registry: binding, Payload: rawjson.Message(`{"value":1}`)}
				appendHistoricalHookEvent(t, rt.Store, newToolCallScheduledEvent("saved", subject, "session-1", call, "", "", 0), "call", 1)
				appendHistoricalHookEvent(t, rt.Store, hooks.NewToolResultReceivedEvent("saved", subject, "session-1", "saved", call.Name, call.ToolCallID, "", rawjson.Message(`{"value":1}`), nil, nil, "Saved result", tc.bounds, 0, nil, nil), "result", 2)
				messages := []*model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "saved-query", Name: "records.find", Input: call.Payload}}}, {Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{ToolUseID: "saved-query", Content: rawjson.Message(`{"value":1}`)}}}, userMsg("Answer the new question.")}
				input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: def.route.ID, RunID: "next", RunContext: run.Context{RunID: "next", SessionID: "session-1"}}, messages)
				_, err := rt.PlanStartActivity(t.Context(), input)
				require.NoError(t, err)
				assert.True(t, called)
			})
		}
	}
}

// historicalStaticFixture retains one earlier declaration in the requested
// saved form and registers the current synthetic tool pair, whose generated
// declarations differ as change describes.
func historicalStaticFixture(t *testing.T, change, form string) (*Runtime, AgentDefinition, *tools.RegistryBinding) {
	t.Helper()
	binding := historicalSavedBinding(t, form, "records.find")
	current := testRegistryPagingResolution(strings.Repeat("b", 64))
	switch change {
	case "metadata":
		current.Toolset.Tools[0].ConsumerContract.Meta = map[string][]string{"mcp:readOnlyHint": {"true"}}
	case "payload":
		current.Toolset.Tools[0].PayloadSchema = []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)
		current.Toolset.Tools[0].ConsumerContract.Payload.SchemaWithoutRootExample = current.Toolset.Tools[0].PayloadSchema
		current.Toolset.Tools[0].ExecutionPayloadSchema = current.Toolset.Tools[0].PayloadSchema
	}
	resolved, err := registrycontract.Resolve(current)
	require.NoError(t, err)
	specs := []tools.ToolSpec{resolved.Specs["records.find"], resolved.Specs["records.continue"]}
	if change == "remove continuation" {
		specs = specs[:1]
	}
	rt := newTestRuntimeWithPlanner("records.agent", &stubPlanner{})
	def := testAgentDefinition("records.agent", "records.workflow", "records.queue", specs, nil).WithRegistryTools(historicalStaticSources{current}).WithRegistryDeclarations(testDeclarations(current.Toolset.Tools))
	reg := rt.agents[def.route.ID]
	reg.Definition = def
	rt.agents[def.route.ID] = reg
	seedTestToolSpecs(rt, specs...)
	seedTestToolDefinitions(rt, specs...)
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{def.route.ID: specs}
	require.NoError(t, rt.RegisterRegistry("company", &genregistry.Client{}, unusedRegistryPulse{}))
	return rt, def, binding
}

// historicalSavedBinding saves a call to name as the earlier build did: with
// the full declaration ("direct") or with only its compiled registration.
func historicalSavedBinding(t *testing.T, form, name string) *tools.RegistryBinding {
	t.Helper()
	registered := testRegistryPagingResolution(strings.Repeat("a", 64))
	resolve := registrycontract.Resolve
	if form == "compiled" {
		resolve = registrycontract.ResolveCompiled
	}
	saved, err := resolve(registered)
	require.NoError(t, err)
	binding, err := saved.Select("company", tools.Ident(name))
	require.NoError(t, err)
	return binding
}

func (s historicalStaticSources) Resolve(_ context.Context, catalog *RegistryCatalog) error {
	return catalog.BindStaticService("company", s.selected.Toolset, s.selected, readTestStaticResource, "records.find", "records.continue")
}
func (s historicalStaticSources) Allows(registry, toolset, version string) bool {
	return registry == "company" && toolset == "provider.records" && version == "1.0.0"
}
