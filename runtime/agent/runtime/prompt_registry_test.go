package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNewFromOptionsInitializesPromptRegistryWithStore(t *testing.T) {
	t.Parallel()

	store := prompt.NewInMemoryStore()
	rt := newFromOptions(newTestStore(), Options{
		PromptStore: store,
	})
	admitRunForTest(t, rt.Store, session.RunMeta{
		Subject: genrunidentity.RunSubject{
			Executor: genrunidentity.
				NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("example.agent"))}, RunID: "run_1", SessionID: "sess_1",
		Status: session.RunStatusRunning,
	})
	require.NotNil(t, rt.PromptRegistry)
	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     prompt.PromptRoleSystem,
		Template: "baseline {{ .Name }}",
	}))
	require.NoError(t, store.Set(context.Background(), "example.agent.system", prompt.Scope{
		SessionID: "sess_1",
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	}, "override {{ .Name }}", nil))

	rendered, err := rt.PromptRegistry.Render(context.Background(), "example.agent.system", prompt.Scope{
		SessionID: "sess_1",
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	}, map[string]any{
		"Name": "operator",
	})
	require.NoError(t, err)
	require.Equal(t, "override operator", rendered.Text)
}

func TestNewFromOptionsInitializesPromptRegistryWithoutStore(t *testing.T) {
	t.Parallel()

	rt := newFromOptions(newTestStore(), Options{})
	admitRunForTest(t, rt.Store, session.RunMeta{
		Subject: genrunidentity.RunSubject{
			Executor: genrunidentity.
				NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("example.agent"))}, RunID: "run_1", SessionID: "sess_1",
		Status: session.RunStatusRunning,
	})
	require.NotNil(t, rt.PromptRegistry)
	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     prompt.PromptRoleSystem,
		Template: "baseline {{ .Name }}",
	}))

	rendered, err := rt.PromptRegistry.Render(context.Background(), "example.agent.system", prompt.Scope{}, map[string]any{
		"Name": "operator",
	})
	require.NoError(t, err)
	require.Equal(t, "baseline operator", rendered.Text)
}

func TestPlannerContextRenderPromptUsesRunScope(t *testing.T) {
	t.Parallel()

	store := prompt.NewInMemoryStore()
	rt := newFromOptions(newTestStore(), Options{
		PromptStore: store,
	})
	admitRunForTest(t, rt.Store, session.RunMeta{
		Subject: genrunidentity.RunSubject{
			Executor: genrunidentity.
				NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("example.agent"))}, RunID: "run_1", SessionID: "sess_1",
		Status: session.RunStatusRunning,
	})

	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     prompt.PromptRoleSystem,
		Template: "baseline {{ .Name }}",
	}))
	require.NoError(t, store.Set(context.Background(), "example.agent.system", prompt.Scope{
		SessionID: "sess_1",
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	}, "override {{ .Name }}", nil))

	agentCtx := newAgentContext(agentContextOptions{
		runtime:   rt,
		agentID:   "example.agent",
		runID:     "run_1",
		sessionID: "sess_1",
		labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
		events: newPlannerEvents("example.agent", "run_1", "sess_1"),
	})
	rendered, err := agentCtx.RenderPrompt(context.Background(), "example.agent.system", map[string]any{
		"Name": "operator",
	})
	require.NoError(t, err)
	require.Equal(t, "override operator", rendered.Text)
}

func TestPlannerContextRenderPromptCollectsAcceptedEvent(t *testing.T) {
	t.Parallel()

	rt := newFromOptions(newTestStore(), Options{})
	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     prompt.PromptRoleSystem,
		Template: "hello",
		Version:  "v2",
	}))
	events := newPlannerEvents("example.agent", "run_1", "sess_1")
	agentCtx := newAgentContext(agentContextOptions{
		runtime:   rt,
		agentID:   "example.agent",
		runID:     "run_1",
		sessionID: "sess_1",
		events:    events,
	})
	_, err := agentCtx.RenderPrompt(context.Background(), "example.agent.system", nil)
	require.NoError(t, err)

	records, err := events.acceptedRecords(nil)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, hooks.PromptRendered, records[0].Type)
}

func TestRegisterAgentWithPromptSpecsRegistersInRegistry(t *testing.T) {
	t.Parallel()

	eng := engineinmem.New()
	rt := New(newTestStore(), WithEngine(eng))
	definition := testAgentDefinition("agent.with.prompts", "agent.with.prompts.workflow", "default-queue", nil, nil)

	specs := []prompt.PromptSpec{
		{
			ID:       "agent.with.prompts.system",
			AgentID:  "agent.with.prompts",
			Role:     prompt.PromptRoleSystem,
			Template: "Hello {{ .Name }}",
		},
		{
			ID:       "agent.with.prompts.user",
			AgentID:  "agent.with.prompts",
			Role:     prompt.PromptRoleUser,
			Template: "User prompt",
		},
	}

	err := rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition:          definition,
		WorkflowHandler:     rt.ExecuteWorkflow,
		PlanActivityName:    "agent.with.prompts.plan",
		ResumeActivityName:  "agent.with.prompts.resume",
		ExecuteToolActivity: "agent.with.prompts.execute",
		Planner:             &stubPlanner{},
		PromptSpecs:         specs,
	})
	require.NoError(t, err)

	content, err := rt.PromptRegistry.Render(t.Context(), "agent.with.prompts.system", prompt.Scope{}, map[string]any{"Name": "World"})
	require.NoError(t, err)
	require.Equal(t, "Hello World", content.Text)

	content, err = rt.PromptRegistry.Render(t.Context(), "agent.with.prompts.user", prompt.Scope{}, nil)
	require.NoError(t, err)
	require.Equal(t, "User prompt", content.Text)
}

func TestRegisterAgentWithDuplicatePromptSpecFails(t *testing.T) {
	t.Parallel()

	eng := engineinmem.New()
	rt := New(newTestStore(), WithEngine(eng))

	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "conflict.prompt",
		Role:     prompt.PromptRoleSystem,
		Template: "already here",
	}))

	definition := testAgentDefinition("agent.conflict", "agent.conflict.workflow", "default-queue", nil, nil)
	err := rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition:          definition,
		WorkflowHandler:     rt.ExecuteWorkflow,
		PlanActivityName:    "agent.conflict.plan",
		ResumeActivityName:  "agent.conflict.resume",
		ExecuteToolActivity: "agent.conflict.execute",
		Planner:             &stubPlanner{},
		PromptSpecs: []prompt.PromptSpec{
			{
				ID:       "conflict.prompt",
				Role:     prompt.PromptRoleSystem,
				Template: "duplicate",
			},
		},
	})
	require.ErrorIs(t, err, prompt.ErrDuplicatePromptSpec)

	_, registered := rt.agents["agent.conflict"]
	require.False(t, registered)
}

func TestRegisterAgentWithInvalidPromptSpecFails(t *testing.T) {
	t.Parallel()

	eng := engineinmem.New()
	rt := New(newTestStore(), WithEngine(eng))
	definition := testAgentDefinition("agent.invalid", "agent.invalid.workflow", "default-queue", nil, nil)

	err := rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition:          definition,
		WorkflowHandler:     rt.ExecuteWorkflow,
		PlanActivityName:    "agent.invalid.plan",
		ResumeActivityName:  "agent.invalid.resume",
		ExecuteToolActivity: "agent.invalid.execute",
		Planner:             &stubPlanner{},
		PromptSpecs: []prompt.PromptSpec{
			{
				ID:       "agent.invalid.system",
				Role:     prompt.PromptRoleSystem,
				Template: "Hello {{ .Undeclared }}",
				Fields: []tools.FieldMetadata{
					{
						Path:     []tools.FieldPathSegment{tools.FixedField("Declared")},
						JSONType: "string",
					},
				},
			},
		},
	})
	require.ErrorIs(t, err, prompt.ErrTemplateFieldNotFound)

	_, registered := rt.agents["agent.invalid"]
	require.False(t, registered)
}

func TestRegisterAgentWithPromptSpecsWithoutPromptRegistryFails(t *testing.T) {
	t.Parallel()

	eng := engineinmem.New()
	rt := New(newTestStore(), WithEngine(eng))
	rt.PromptRegistry = nil

	definition := testAgentDefinition("agent.no.reg", "agent.no.reg.workflow", "default-queue", nil, nil)
	err := rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition:          definition,
		WorkflowHandler:     rt.ExecuteWorkflow,
		PlanActivityName:    "agent.no.reg.plan",
		ResumeActivityName:  "agent.no.reg.resume",
		ExecuteToolActivity: "agent.no.reg.execute",
		Planner:             &stubPlanner{},
		PromptSpecs: []prompt.PromptSpec{
			{
				ID:       "agent.no.reg.system",
				Role:     prompt.PromptRoleSystem,
				Template: "Hello",
			},
		},
	})
	require.ErrorIs(t, err, ErrInvalidConfig)
}
