// Saved results remain conversation evidence when their owner cannot match a
// current page provider. These tests use only synthetic tools and local storage.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
)

type unavailablePageSources struct {
	selected *genregistry.ResolvedToolset
	read     func(context.Context, string, string) (*genregistry.ResolvedToolset, error)
}

func TestNewTurnReadsHistoryWithoutMatchingPageResource(t *testing.T) {
	current := testRegistryPagingResolution(strings.Repeat("b", 64))
	old := testRegistryPagingResolution(strings.Repeat("a", 64))
	resolved, err := registrycontract.Resolve(current)
	require.NoError(t, err)
	specs := []tools.ToolSpec{resolved.Specs["records.find"], resolved.Specs["records.continue"]}
	reads := 0
	source := unavailablePageSources{selected: current, read: func(context.Context, string, string) (*genregistry.ResolvedToolset, error) {
		reads++
		return nil, nil
	}}
	definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, specs, nil, nil, []tools.Ident{"records.find", "records.continue"}, nil, nil).WithRegistryTools(source).WithRegistryDeclarations(testDeclarations(current.Toolset.Tools))
	store := newTestStore()
	rt := New(store, WithEngine(engineinmem.New()))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: specs, Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
		return nil, errors.New("history-only turn must not execute a provider")
	}}))
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	called := false
	var messages []*model.Message
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{Definition: definition, WorkflowHandler: rt.ExecuteWorkflow, PlanActivityName: "records.plan", ResumeActivityName: "records.resume", ExecuteToolActivity: "records.execute", Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		called = true
		want, err := json.Marshal(messages)
		require.NoError(t, err)
		actual, err := json.Marshal(input.Messages)
		require.NoError(t, err)
		assert.JSONEq(t, string(want), string(actual))
		assert.Empty(t, historicalActions(input.Agent))
		names := make([]string, 0, len(input.Agent.AdvertisedToolDefinitions()))
		for _, tool := range input.Agent.AdvertisedToolDefinitions() {
			names = append(names, tool.Name)
		}
		assert.Contains(t, names, "records.find", "ordinary current queries remain available")
		return finalPlannerResult("The saved report covered only its first page."), nil
	}}}))
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(definition.route.ID))}
	admitRunForTest(t, store, session.RunMeta{Subject: subject, RunID: "saved", SessionID: "history", Status: session.RunStatusRunning})
	saved, err := registrycontract.ResolveCompiled(old)
	require.NoError(t, err)
	binding, err := saved.Select(testRegistryName, "records.find")
	require.NoError(t, err)
	call := ToolCall{Name: "records.find", ToolCallID: "saved-query", Registry: binding, Payload: rawjson.Message(`{"value":1}`)}
	appendHistoricalHookEvent(t, store, newToolCallScheduledEvent("saved", subject, "history", call, "", "", 0), "call", 1)
	appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent("saved", subject, "history", "saved", call.Name, call.ToolCallID, "", rawjson.Message(`{"value":1}`), nil, nil, "Saved result", &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("old-cursor")}, 0, nil, nil), "result", 2)
	messages = []*model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: call.ToolCallID, Name: call.Name.String(), Input: call.Payload}}}, {Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{ToolUseID: call.ToolCallID, Content: rawjson.Message(`{"value":1}`)}}}, &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "The report covers the first page."}}}, userMsg("Summarize the report's limitation.")}
	// The old report is committed before the next turn chooses current tools.
	appendTestActivityHistory(t, rt, &PlanActivityInput{AgentID: definition.route.ID, RunID: "saved", RunContext: run.Context{RunID: "saved", SessionID: "history"}}, messages[:3])
	meta, err := store.LoadRun(t.Context(), "saved")
	require.NoError(t, err)
	meta.Status = session.RunStatusCompleted
	admitRunForTest(t, store, meta)
	before, err := store.ListRunRecords(t.Context(), "saved", "", 256)
	require.NoError(t, err)
	encoded, err := json.Marshal(before.Events)
	require.NoError(t, err)
	beforeHash := sha256.Sum256(encoded)
	bindingHash := sha256.Sum256(binding.Resolution)
	client := rt.MustClient(definition.route.ID)
	prepared, err := client.PrepareNextTurn(t.Context(), "history", "saved", nil, messages[3:], WithRunID("next"), WithTurnID("next"))
	require.NoError(t, err)
	handle, err := client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, 1, reads)
	after, err := store.ListRunRecords(t.Context(), "saved", "", 256)
	require.NoError(t, err)
	encoded, err = json.Marshal(after.Events)
	require.NoError(t, err)
	assert.Equal(t, beforeHash, sha256.Sum256(encoded))
	assert.Equal(t, bindingHash, sha256.Sum256(binding.Resolution))
}

func TestFinalPageBindingChecksMembersAfterUnavailableResource(t *testing.T) {
	for _, scenario := range []string{"unavailable", "later malformed binding", "later owner failure", "invalid chain"} {
		t.Run(scenario, func(t *testing.T) {
			old := testRegistryPagingResolution(strings.Repeat("a", 64))
			current := testRegistryPagingResolution(strings.Repeat("b", 64))
			resolved, err := registrycontract.Resolve(current)
			require.NoError(t, err)
			specs := []tools.ToolSpec{resolved.Specs["records.find"], resolved.Specs["records.continue"]}
			def := testAgentDefinition("records.agent", "records.workflow", "records.queue", specs, nil).WithRegistryTools(testRegistrySources{whole: true}).WithRegistryDeclarations(testDeclarations(current.Toolset.Tools))
			rt := New(newTestStore())
			rt.agents[def.route.ID] = AgentRegistration{Definition: def}
			require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
			catalog := rt.newRegistryCatalog(def)
			reads := 0
			readErr := errors.New("matching resource read failed")
			require.NoError(t, catalog.BindStaticService(testRegistryName, current.Toolset, current, func(context.Context, string, string) (*genregistry.ResolvedToolset, error) {
				reads++
				if reads == 1 {
					return nil, nil
				}
				return nil, readErr
			}, "records.find", "records.continue"))
			prior, err := registrycontract.Resolve(old)
			require.NoError(t, err)
			binding, err := prior.Select(testRegistryName, "records.find")
			require.NoError(t, err)
			root := sourceContinuationOutput("records.find", "root", `{"value":1}`, "old-cursor")
			root.Registry = binding
			page := *root
			page.Name = "records.continue"
			page.ToolCallID = "page"
			page.ContinuationRootToolCallID = "root"
			page.Payload = rawjson.Message(`{"cursor":"old-cursor"}`)
			page.Bounds = &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("next-cursor")}
			page.Registry, err = prior.Select(testRegistryName, page.Name)
			require.NoError(t, err)
			actions, err := rt.availableContinuationActions(def.route.ID, []*planner.ToolOutput{root, &page}, false)
			require.NoError(t, err)
			require.Len(t, actions, 1)
			switch scenario {
			case "unavailable":
				actions[0].outputs = actions[0].outputs[:1]
			case "later malformed binding":
				page.Registry.Resolution = rawjson.Message(`{}`)
			case "invalid chain":
				page.ContinuationRootToolCallID = "missing-root"
			}
			selected, err := catalog.bindContinuationActions(t.Context(), actions)
			if scenario == "unavailable" {
				require.NoError(t, err)
				assert.Empty(t, selected)
			} else {
				require.Error(t, err)
			}
			if scenario == "later owner failure" {
				require.ErrorIs(t, err, readErr)
				assert.Equal(t, 2, reads)
			}
		})
	}
}

func (s unavailablePageSources) Resolve(_ context.Context, catalog *RegistryCatalog) error {
	return catalog.BindStaticService(testRegistryName, s.selected.Toolset, s.selected, s.read, "records.find", "records.continue")
}
func (s unavailablePageSources) Allows(registry, toolset, version string) bool {
	return registry == testRegistryName && toolset == s.selected.Toolset.Name
}
