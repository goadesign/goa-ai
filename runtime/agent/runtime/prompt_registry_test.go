package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestNewFromOptionsInitializesPromptRegistryWithStore(t *testing.T) {
	t.Parallel()

	store := prompt.NewInMemoryStore()
	rt := newFromOptions(newTestStore(), Options{
		PromptStore: store,
	})
	admitRunForTest(t, rt.Store, session.RunMeta{
		AgentID: "example.agent", RunID: "run_1", SessionID: "sess_1",
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
		AgentID: "example.agent", RunID: "run_1", SessionID: "sess_1",
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
		AgentID: "example.agent", RunID: "run_1", SessionID: "sess_1",
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
