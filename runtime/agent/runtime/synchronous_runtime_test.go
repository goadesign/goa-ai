package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// callbackOnlyStore deliberately exposes no engine or Session methods.
	callbackOnlyStore struct{ storage.SynchronousStore }
)

func TestSynchronousRuntimeRequiresOnlyCallbackStore(t *testing.T) {
	owner := newTestStore()
	store := callbackOnlyStore{SynchronousStore: owner}
	_, supportsEngine := any(store).(storage.Store)
	assert.False(t, supportsEngine)
	registry := prompt.NewRegistry(nil)
	runtime, err := NewSynchronous(store, registry, hooks.NewBus())
	require.NoError(t, err)
	assert.Same(t, registry, runtime.PromptRegistry)
	calls := 0
	require.NoError(t, runtime.RunOneShot(t.Context(), OneShotRunInput{AgentID: "callback", RunID: "callback-only"}, func(context.Context) error { calls++; return nil }))
	assert.Equal(t, 1, calls)
	saved, err := owner.LoadRun(t.Context(), "callback-only")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCompleted, saved.Status)
	assert.Empty(t, saved.SessionID)
}

func TestNewSynchronousRequiresDependencies(t *testing.T) {
	store := newTestStore()
	registry := prompt.NewRegistry(nil)
	bus := hooks.NewBus()
	for _, test := range []struct {
		name     string
		store    storage.SynchronousStore
		registry *prompt.Registry
		bus      hooks.Bus
	}{
		{name: "store", registry: registry, bus: bus},
		{name: "registry", store: store, bus: bus},
		{name: "bus", store: store, registry: registry},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, err := NewSynchronous(test.store, test.registry, test.bus)
			assert.Nil(t, runtime)
			assert.ErrorContains(t, err, "requires store, prompt registry, and hook bus")
		})
	}
}
