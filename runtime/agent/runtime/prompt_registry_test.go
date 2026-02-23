package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestNewFromOptionsInitializesPromptRegistryWithStore(t *testing.T) {
	t.Parallel()

	store := prompt.NewInMemoryStore()
	rt := newFromOptions(Options{
		PromptStore: store,
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

	rendered, err := rt.PromptRegistry.Render(testPromptRenderContext(), "example.agent.system", prompt.Scope{
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

	rt := newFromOptions(Options{})
	require.NotNil(t, rt.PromptRegistry)
	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     prompt.PromptRoleSystem,
		Template: "baseline {{ .Name }}",
	}))

	rendered, err := rt.PromptRegistry.Render(testPromptRenderContext(), "example.agent.system", prompt.Scope{}, map[string]any{
		"Name": "operator",
	})
	require.NoError(t, err)
	require.Equal(t, "baseline operator", rendered.Text)
}

func TestPlannerContextRenderPromptUsesRunScope(t *testing.T) {
	t.Parallel()

	store := prompt.NewInMemoryStore()
	rt := newFromOptions(Options{
		PromptStore: store,
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
	})
	rendered, err := agentCtx.RenderPrompt(context.Background(), "example.agent.system", map[string]any{
		"Name": "operator",
	})
	require.NoError(t, err)
	require.Equal(t, "override operator", rendered.Text)
}

func TestOnPromptRenderedPublishesHookEvent(t *testing.T) {
	t.Parallel()

	rt := newFromOptions(Options{})

	var rendered *hooks.PromptRenderedEvent
	sub, err := rt.Bus.Register(hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
		e, ok := evt.(*hooks.PromptRenderedEvent)
		if !ok {
			return nil
		}
		rendered = e
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = sub.Close()
	})

	ctx := withPromptRenderHookContext(context.Background(), PromptRenderHookContext{
		RunID:     "run_1",
		AgentID:   "example.agent",
		SessionID: "sess_1",
		TurnID:    "turn_1",
	})
	rt.onPromptRendered(ctx, prompt.RenderEvent{
		PromptID: "example.agent.system",
		Version:  "v2",
		Scope: prompt.Scope{
			SessionID: "sess_1",
			Labels: map[string]string{
				"account": "acme",
				"region":  "west",
			},
		},
	})

	require.NotNil(t, rendered)
	require.Equal(t, "run_1", rendered.RunID())
	require.Equal(t, "example.agent", rendered.AgentID())
	require.Equal(t, "sess_1", rendered.SessionID())
	require.Equal(t, "turn_1", rendered.TurnID())
	require.Equal(t, prompt.Ident("example.agent.system"), rendered.PromptID)
	require.Equal(t, "v2", rendered.Version)
	require.Equal(t, "sess_1", rendered.Scope.SessionID)
	require.Equal(t, "acme", rendered.Scope.Labels["account"])
	require.Equal(t, "west", rendered.Scope.Labels["region"])
}

func testPromptRenderContext() context.Context {
	return withPromptRenderHookContext(context.Background(), PromptRenderHookContext{
		RunID:     "run_1",
		AgentID:   "example.agent",
		SessionID: "sess_1",
		TurnID:    "turn_1",
	})
}
