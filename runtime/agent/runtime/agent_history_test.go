// Package runtime verifies provider history through actual preparation and run
// execution. Saved sources retain their exact identity instead of being copied
// into literal messages, and replay cannot select newer provider configuration.
package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	genregistry "goa.design/goa-ai/registry/gen/registry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	historyPublicationReplyStore struct {
		*testStore
		lost atomic.Bool
	}
	unreadParentHistoryStore struct {
		*testStore
		parentID string
		blocked  bool
		reads    int
	}
)

func TestProviderHistoryDirectExecutionPreservesSourceAndPrivacy(t *testing.T) {
	store := &historyPublicationReplyStore{testStore: newTestStore()}
	rt := New(store, WithEngine(engineinmem.New()))
	definition, operation := providerHistoryDefinition(t)
	old := []*model.Message{
		{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "old system"}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "saved question"}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ThinkingPart{Text: "saved private reasoning", Signature: "signed", Final: true}, model.TextPart{Text: "saved answer"}}},
	}
	sourceEnd := publishProviderHistorySource(t, rt, definition.route.ID, "session", "completed", old, true)
	system := []*model.Message{{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "new system"}}}}
	fresh := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "new question"}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ThinkingPart{Text: "fresh private reasoning", Signature: "new-signature", Final: true}, model.TextPart{Text: "fresh answer"}}},
	}
	var prepared, planned atomic.Int32
	require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, revision string, call *ToolCall) (*AgentToolConfiguration, error) {
		prepared.Add(1)
		assert.Equal(t, "revision/1", revision)
		history := NextTurnAgentHistory("completed", system, fresh).WithoutPriorReasoning()
		if call.RunID == "later" {
			history = NextTurnAgentHistory("next", system, nil)
		}
		return &AgentToolConfiguration{History: history, Labels: map[string]string{"prepared": "original"}, Policy: &PolicyOverrides{}, RenderedPrompts: []prompt.RenderEvent{{PromptID: "system", Version: "revision/1"}}}, nil
	}))
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "history.plan", ResumeActivityName: "history.resume", ExecuteToolActivity: "history.execute",
		Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			planned.Add(1)
			data, err := transcript.EncodeRunLogDelta(input.Messages)
			if err != nil {
				return nil, err
			}
			assert.Contains(t, string(data), "new system")
			assert.Contains(t, string(data), "saved question")
			assert.Contains(t, string(data), "saved answer")
			assert.Contains(t, string(data), "new question")
			assert.NotContains(t, string(data), "old system")
			assert.NotContains(t, string(data), "private reasoning")
			assert.Equal(t, "original", input.RunContext.Labels["prepared"])
			return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":2}`)}}, nil
		}},
	}))
	for range 2 {
		handle, err := rt.StartOperation(t.Context(), "session", "next", operation, rawjson.Message(`{"value":1}`), map[string]string{"accepted": "fixed"}, nil)
		require.NoError(t, err)
		output, err := handle.Wait(t.Context())
		require.NoError(t, err)
		require.NotNil(t, output.FinalToolResult)
	}
	assert.True(t, store.lost.Load(), "publication must recover its accepted result after a lost reply")
	assert.EqualValues(t, 1, prepared.Load())
	assert.EqualValues(t, 1, planned.Load())
	meta, err := store.LoadRun(t.Context(), "next")
	require.NoError(t, err)
	seed, err := store.LoadRunSeed(t.Context(), "next", meta.SeedEndID)
	require.NoError(t, err)
	assert.Equal(t, storage.SeedNextTurn, seed.Declaration.Kind)
	require.NotNil(t, seed.Source)
	assert.Equal(t, &storage.HistoryPrefix{RunID: "completed", EndID: sourceEnd, ExcludeSystem: true, ExcludeReasoning: true}, seed.Source)
	assert.Equal(t, []prompt.RenderEvent{{PromptID: "system", Version: "revision/1"}}, seed.Declaration.RenderedPrompts)
	page, err := store.ListRunSeedRecords(t.Context(), "next", meta.SeedEndID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Records, 3)
	assert.Equal(t, seed.Source, page.Records[1].Prefix)
	assert.NotContains(t, string(page.Records[0].Messages)+string(page.Records[2].Messages), "saved question")
	_, err = rt.StartOperation(t.Context(), "session", "next", operation, rawjson.Message(`{"value":9}`), map[string]string{"accepted": "fixed"}, nil)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
	handle, err := rt.StartOperation(t.Context(), "session", "later", operation, rawjson.Message(`{"value":1}`), nil, nil)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 2, prepared.Load())
	assert.EqualValues(t, 2, planned.Load())
}

func TestProviderHistoryRegistryChildKeepsOwnSource(t *testing.T) {
	store := &unreadParentHistoryStore{testStore: newTestStore()}
	rt := New(store, WithEngine(&stubEngine{}))
	definition, _ := providerHistoryDefinition(t)
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	messages := []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "previous provider conversation"}}}}
	sourceEnd := publishProviderHistorySource(t, rt, definition.route.ID, "session-1", "completed-child", messages, true)
	call := testNativeRegistryCall(t, "revision/1")
	call.SessionID = "session-1"
	var resolved int
	require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
		resolved++
		return &AgentToolConfiguration{History: NextTurnAgentHistory("completed-child", nil, []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "fresh child question"}}}})}, nil
	}))
	// The accepted call carries its parent's real history position, but the
	// provider prepares its own source without reading that parent transcript.
	parent := run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}
	position := testToolHistory(t, rt, testSubjectAgent(t, call.Subject), parent, []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "parent-only conversation"}}}})
	store.parentID, store.blocked = call.RunID, true
	command := &api.AgentChildActivityInput{Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}, Call: call, ParentRun: parent, HistoryEndID: position}
	first, err := rt.prepareAgentChildActivity(t.Context(), command)
	require.NoError(t, err)
	require.NotNil(t, first.Success)
	second, err := rt.prepareAgentChildActivity(t.Context(), command)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, resolved)
	assert.Zero(t, store.reads)
	childID := agentChildRunContext(&call).RunID
	seed, err := rt.Store.LoadRunSeed(t.Context(), childID, first.Success.SeedEndID)
	require.NoError(t, err)
	assert.Equal(t, &storage.HistoryPrefix{RunID: "completed-child", EndID: sourceEnd, ExcludeSystem: true}, seed.Source)
	input := &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
		RunExecutorBranchAgent(definition.route.ID))}, RunID: childID, SessionID: call.SessionID, SeedEndID: first.Success.SeedEndID, ParentRunID: call.RunID, ParentToolCallID: call.ToolCallID}
	got := testInitialMessages(t, rt.Store, input)
	encoded, err := transcript.EncodeRunLogDelta(got)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "previous provider conversation")
	assert.Contains(t, string(encoded), "fresh child question")
}

func TestProviderHistoryRejectsInvalidSourceOwnership(t *testing.T) {
	for _, variation := range []string{"wrong agent", "wrong session", "running source", "missing source", "one-shot source"} {
		t.Run(variation, func(t *testing.T) {
			rt := New(newTestStore())
			definition, operation := providerHistoryDefinition(t)
			rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
			sourceAgent, sourceSession := definition.route.ID, "session"
			if variation == "wrong agent" {
				sourceAgent = "other.agent"
			}
			if variation == "wrong session" {
				sourceSession = "other-session"
			}
			publishProviderHistorySource(t, rt, sourceAgent, sourceSession, "completed", []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "saved"}}}}, variation != "running source")
			if variation == "wrong session" {
				_, err := createSessionForTest(t.Context(), rt.Store, "session")
				require.NoError(t, err)
			}
			history := NextTurnAgentHistory("completed", nil, nil)
			if variation == "missing source" {
				history = NextTurnAgentHistory("missing", nil, nil)
			}
			require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
				return &AgentToolConfiguration{History: history}, nil
			}))
			input := &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
				RunExecutorBranchAgent(definition.route.ID))}, RunID: "next", SessionID: "session", Tool: tools.Ident(operation.Name), ToolArgs: rawjson.Message(`{"value":1}`), Operation: operation}
			if variation == "one-shot source" {
				input.SessionID = ""
			}
			_, err := rt.prepareAgentOperationActivity(t.Context(), input)
			require.Error(t, err)
			_, err = rt.Store.LoadRunSeed(t.Context(), "next", storage.EmptySeedEndID)
			require.ErrorIs(t, err, storage.ErrSeedNotFound)
		})
	}
}

func TestProviderHistoryRequiresExplicitChoiceAndCopiesMessages(t *testing.T) {
	rt := New(newTestStore())
	definition, _ := providerHistoryDefinition(t)
	config := &AgentToolConfiguration{}
	require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
		return config, nil
	}))
	_, err := rt.resolveAgentConfiguration(t.Context(), genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "literal"})}, definition, "revision/1", &ToolCall{})
	require.ErrorContains(t, err, "explicit history selection")
	config.History = LiteralAgentHistory(nil)
	_, err = rt.resolveAgentConfiguration(t.Context(), genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "literal"})}, definition, "revision/1", &ToolCall{})
	require.NoError(t, err)
	original := priorReasoningFixture()
	config.History = LiteralAgentHistory(original).WithoutPriorReasoning()
	owned, err := rt.resolveAgentConfiguration(t.Context(), genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "literal"})}, definition, "revision/1", &ToolCall{})
	require.NoError(t, err)
	original[0].Parts[0] = model.TextPart{Text: "mutated"}
	declaration := storage.SeedDeclaration{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
		RunExecutorBranchAgent(string(definition.route.ID)))}, RunID: "literal", CommandID: "literal", AttemptID: "literal"}
	_, err = bindHistoryOperation(t.Context(), rt.Store, declaration, genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "literal"})})
	require.NoError(t, err)
	writer, err := stageAgentHistory(t.Context(), rt.Store, declaration, owned.History)
	require.NoError(t, err)
	require.NoError(t, writer.publish(t.Context(), []byte("prepared")))
	input := &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
		RunExecutorBranchAgent(definition.route.ID))}, RunID: "literal", SeedEndID: writer.endID}
	// Publication appends prepared bytes after the history; the accepted seed
	// retains the original history end independently of those prepared records.
	accepted, found, err := rt.Store.FindRunPreparation(t.Context(), storage.PreparationOperation{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
		RunExecutorBranchAgent(string(definition.route.ID)))}, RunID: "literal", CommandID: "literal"})
	require.NoError(t, err)
	require.True(t, found)
	input.SeedEndID = accepted.Seed.EndID
	data, err := transcript.EncodeRunLogDelta(testInitialMessages(t, rt.Store, input))
	require.NoError(t, err)
	assert.Contains(t, string(data), "Find the report.")
	assert.NotContains(t, string(data), "mutated")
	assert.NotContains(t, string(data), "private reasoning")
}

func providerHistoryDefinition(t *testing.T) (AgentDefinition, *genregistry.ToolSchema) {
	t.Helper()
	resolution := testNativeRegistryResolution("revision/1")
	operation := resolution.Toolset.Tools[0]
	compiled, err := registrycontract.Resolve(resolution)
	require.NoError(t, err)
	spec := compiled.Specs[tools.Ident(operation.Name)]
	return testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil), operation
}

func publishProviderHistorySource(t *testing.T, rt *Runtime, agentID agent.Ident, sessionID, runID string, messages []*model.Message, complete bool) string {
	t.Helper()
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: agentID, RunID: runID, RunContext: run.Context{RunID: runID, SessionID: sessionID}}, messages)
	if !complete {
		return input.HistoryEndID
	}
	record, err := prepareHookRecordInput(t.Context(), hooks.NewRunCompletedEvent(runID, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agentID))}, sessionID, "success", run.PhaseCompleted, nil, nil, nil), "")
	require.NoError(t, err)
	result, err := rt.executeStorageCommand(t.Context(), &api.StorageActivityCommand{Terminal: &api.RunTerminalCommand{Record: record}})
	require.NoError(t, err)
	return result.Terminal.Record.ID
}

func (s *historyPublicationReplyStore) PublishRunSeed(ctx context.Context, publication storage.SeedPublication) error {
	if err := s.testStore.PublishRunSeed(ctx, publication); err != nil {
		return err
	}
	if publication.RunID == "next" && !s.lost.Swap(true) {
		return errors.New("publication reply lost")
	}
	return nil
}

func (s *unreadParentHistoryStore) ListRunRecords(ctx context.Context, runID, cursor string, limit int) (runlog.Page, error) {
	if s.blocked && runID == s.parentID {
		s.reads++
		return runlog.Page{}, errors.New("provider cannot read its caller's transcript")
	}
	return s.testStore.ListRunRecords(ctx, runID, cursor, limit)
}
