package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
)

func TestRunOneShotPersistsCanonicalRunlogWithoutSessionState(t *testing.T) {
	t.Parallel()

	bus := hooks.NewBus()
	published := 0
	sub, err := bus.Register(hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
		published++
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = sub.Close()
	})

	sessionStore := sessioninmem.New()
	runlogStore := runloginmem.New()
	rt := newFromOptions(Options{
		Hooks:         bus,
		SessionStore:  sessionStore,
		RunEventStore: runlogStore,
	})
	require.NoError(t, rt.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "example.agent.system",
		AgentID:  "example.agent",
		Role:     prompt.PromptRoleSystem,
		Template: "hello {{ .Name }}",
	}))

	runID := "oneshot-run-1"
	err = rt.RunOneShot(context.Background(), OneShotRunInput{
		AgentID: "example.agent",
		RunID:   runID,
	}, func(runCtx context.Context) error {
		_, renderErr := rt.PromptRegistry.Render(runCtx, "example.agent.system", prompt.Scope{}, map[string]any{
			"Name": "operator",
		})
		return renderErr
	})
	require.NoError(t, err)

	page, err := runlogStore.List(context.Background(), runID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Events, 3)
	require.Equal(t, hooks.RunStarted, page.Events[0].Type)
	require.Equal(t, hooks.PromptRendered, page.Events[1].Type)
	require.Equal(t, hooks.RunCompleted, page.Events[2].Type)
	for _, event := range page.Events {
		require.Equal(t, runID, event.RunID)
		require.Empty(t, event.SessionID)
	}
	_, err = sessionStore.LoadRun(context.Background(), runID)
	require.ErrorIs(t, err, session.ErrRunNotFound)
	require.Equal(t, 3, published)
}

func TestRunOneShotRejectsMissingAgentID(t *testing.T) {
	t.Parallel()

	rt := newFromOptions(Options{})
	err := rt.RunOneShot(context.Background(), OneShotRunInput{}, func(context.Context) error {
		return nil
	})
	require.ErrorIs(t, err, ErrAgentNotFound)
}

func TestRunOneShotRequiresExecutor(t *testing.T) {
	t.Parallel()

	rt := newFromOptions(Options{})
	err := rt.RunOneShot(context.Background(), OneShotRunInput{
		AgentID: "example.agent",
	}, nil)
	require.EqualError(t, err, "one-shot executor is required")
}

func TestRunOneShotClassifiesCanceledExecutionAsCanceled(t *testing.T) {
	t.Parallel()

	runlogStore := runloginmem.New()
	rt := newFromOptions(Options{
		RunEventStore: runlogStore,
	})
	runID := "oneshot-run-canceled"
	err := rt.RunOneShot(context.Background(), OneShotRunInput{
		AgentID: "example.agent",
		RunID:   runID,
	}, func(context.Context) error {
		return context.Canceled
	})
	require.ErrorIs(t, err, context.Canceled)

	page, listErr := runlogStore.List(context.Background(), runID, "", 20)
	require.NoError(t, listErr)
	require.Len(t, page.Events, 2)
	require.Equal(t, hooks.RunCompleted, page.Events[1].Type)

	input := &hooks.ActivityInput{
		Type:      page.Events[1].Type,
		RunID:     page.Events[1].RunID,
		AgentID:   "example.agent",
		SessionID: page.Events[1].SessionID,
		TurnID:    page.Events[1].TurnID,
		Payload:   page.Events[1].Payload,
	}
	event, decodeErr := hooks.DecodeFromHookInput(input)
	require.NoError(t, decodeErr)
	completed, ok := event.(*hooks.RunCompletedEvent)
	require.True(t, ok)
	require.Equal(t, runStatusCanceled, completed.Status)
}
