package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// transientTerminalStore fails the first terminal write so the one-shot
	// runtime must retry the same record without running caller work again.
	transientTerminalStore struct {
		storage.Store
		failures    int
		attempts    int
		terminalErr error
	}
)

func (s *transientTerminalStore) RecordRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.AppendResult, error) {
	s.attempts++
	if s.terminalErr != nil {
		return storage.AppendResult{}, s.terminalErr
	}
	if s.failures > 0 {
		s.failures--
		return storage.AppendResult{}, errors.New("database unavailable")
	}
	return s.Store.RecordRunTerminal(ctx, command)
}

func TestRunOneShotStoresCompleteLifecycle(t *testing.T) {
	store := newTestStore()
	runtime := newFromOptions(store, Options{Hooks: hooks.NewBus()})
	err := runtime.RunOneShot(context.Background(), OneShotRunInput{
		AgentID: "svc.agent", RunID: "run",
	}, func(context.Context) error {
		return nil
	})
	require.NoError(t, err)
	meta, err := store.LoadRun(context.Background(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCompleted, meta.Status)
	page, err := store.ListRunRecords(context.Background(), "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, hooks.RunStarted, page.Events[0].Type)
	require.Equal(t, hooks.RunCompleted, page.Events[1].Type)
}

func TestRunOneShotStoresPromptAfterStart(t *testing.T) {
	store := newTestStore()
	runtime := newFromOptions(store, Options{Hooks: hooks.NewBus()})
	require.NoError(t, runtime.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "svc.agent.system",
		AgentID:  "svc.agent",
		Role:     prompt.PromptRoleSystem,
		Template: "hello",
		Version:  "v1",
	}))
	err := runtime.RunOneShot(context.Background(), OneShotRunInput{
		AgentID: "svc.agent", RunID: "run",
	}, func(ctx context.Context) error {
		_, err := runtime.PromptRegistry.Render(ctx, "svc.agent.system", prompt.Scope{}, nil)
		return err
	})
	require.NoError(t, err)
	page, err := store.ListRunRecords(context.Background(), "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 3)
	require.Equal(t, hooks.RunStarted, page.Events[0].Type)
	require.Equal(t, hooks.PromptRendered, page.Events[1].Type)
	require.Equal(t, hooks.RunCompleted, page.Events[2].Type)
}

func TestRunOneShotStoresFailedLifecycle(t *testing.T) {
	store := newTestStore()
	runtime := newFromOptions(store, Options{Hooks: hooks.NewBus()})
	want := errors.New("failed")
	err := runtime.RunOneShot(context.Background(), OneShotRunInput{
		AgentID: "svc.agent", RunID: "run",
	}, func(context.Context) error {
		return want
	})
	require.ErrorIs(t, err, want)
	meta, err := store.LoadRun(context.Background(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusFailed, meta.Status)
}

func TestRunOneShotStoresDeadlineAsFailure(t *testing.T) {
	store := newTestStore()
	runtime := newFromOptions(store, Options{Hooks: hooks.NewBus()})
	err := runtime.RunOneShot(t.Context(), OneShotRunInput{
		AgentID: "svc.agent", RunID: "run",
	}, func(context.Context) error {
		return context.DeadlineExceeded
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	meta, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusFailed, meta.Status)
}

func TestRunOneShotStoresPromptAndCancellationAfterCallbackCancelsContext(t *testing.T) {
	store := newTestStore()
	retryingStore := &transientTerminalStore{Store: store, failures: 1}
	runtime := newFromOptions(retryingStore, Options{Hooks: hooks.NewBus()})
	require.NoError(t, runtime.PromptRegistry.Register(prompt.PromptSpec{
		ID:       "svc.agent.system",
		AgentID:  "svc.agent",
		Role:     prompt.PromptRoleSystem,
		Template: "hello",
		Version:  "v1",
	}))
	ctx, cancel := context.WithCancel(context.Background())
	executions := 0
	err := runtime.RunOneShot(ctx, OneShotRunInput{
		AgentID: "svc.agent", RunID: "run",
	}, func(ctx context.Context) error {
		executions++
		_, err := runtime.PromptRegistry.Render(ctx, "svc.agent.system", prompt.Scope{}, nil)
		require.NoError(t, err)
		cancel()
		return context.Canceled
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, executions)
	require.Equal(t, 2, retryingStore.attempts)
	meta, err := store.LoadRun(context.Background(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCanceled, meta.Status)
	page, err := store.ListRunRecords(context.Background(), "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 3)
	require.Equal(t, hooks.RunStarted, page.Events[0].Type)
	require.Equal(t, hooks.PromptRendered, page.Events[1].Type)
	require.Equal(t, hooks.RunCompleted, page.Events[2].Type)
}

func TestRunOneShotDoesNotRetryPermanentTerminalConflict(t *testing.T) {
	store := newTestStore()
	conflictingStore := &transientTerminalStore{
		Store: store, terminalErr: storage.NewContractError(session.ErrRunTerminalConflict),
	}
	runtime := newFromOptions(conflictingStore, Options{Hooks: hooks.NewBus()})
	executions := 0

	err := runtime.RunOneShot(t.Context(), OneShotRunInput{
		AgentID: "svc.agent", RunID: "run",
	}, func(context.Context) error {
		executions++
		return nil
	})

	require.ErrorIs(t, err, session.ErrRunTerminalConflict)
	require.Equal(t, 1, executions)
	require.Equal(t, 1, conflictingStore.attempts)
}
