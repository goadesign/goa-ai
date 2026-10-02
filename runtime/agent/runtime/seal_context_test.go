package runtime

// These tests hold actual registration and engine callbacks while Seal waits.
// Context results must arrive before release; registration still owns its
// complete validation, engine calls, and commit.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	sealContextEngine struct {
		stubEngine
		storage      func() error
		child        func() error
		seal         func(context.Context) error
		storageCalls atomic.Int32
		childCalls   atomic.Int32
		calls        atomic.Int32
	}
)

const storageRegistrationPhase = "storage"

func TestSealContextDuringEngineRegistration(t *testing.T) {
	for _, phase := range []string{storageRegistrationPhase, "child"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				eng := &sealContextEngine{}
				rt := New(newTestStore(), WithEngine(eng))
				entered := make(chan struct{})
				release := make(chan struct{})
				releaseOnce := sync.OnceFunc(func() { close(release) })
				defer releaseOnce()
				hold := func() error {
					acquired := rt.mu.TryLock()
					assert.True(t, acquired, "engine callback holds the runtime state mutex")
					if acquired {
						rt.mu.Unlock()
					}
					close(entered)
					<-release
					return nil
				}
				if phase == storageRegistrationPhase {
					eng.storage = hold
				} else {
					eng.child = hold
				}
				registration := make(chan error, 1)
				go func() {
					registration <- rt.RegisterAgent(context.Background(), sealContextAgent(rt, "first.agent", nil))
				}()
				<-entered
				rt.mu.RLock()
				if phase == storageRegistrationPhase {
					assert.False(t, rt.storageActivityRegistered)
				} else {
					assert.True(t, rt.storageActivityRegistered)
					assert.False(t, rt.agentChildActivityRegistered)
				}
				rt.mu.RUnlock()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- rt.Seal(ctx) }()
				synctest.Wait()
				cancel()
				require.ErrorIs(t, <-result, context.Canceled)
				assert.True(t, rt.registrationClosed.Load())
				assert.EqualValues(t, 0, eng.calls.Load())
				requirePendingSealResult(t, registration)

				// All four later registrations wait for the admitted agent, then
				// reject without changing its completed registry.
				later := make([]<-chan error, 0, 4)
				for _, call := range sealContextRegistrations(rt) {
					out := make(chan error, 1)
					go func() { out <- call() }()
					later = append(later, out)
				}
				synctest.Wait()
				for _, out := range later {
					requirePendingSealResult(t, out)
				}
				releaseOnce()
				require.NoError(t, <-registration)
				for _, out := range later {
					require.ErrorIs(t, <-out, ErrRegistrationClosed)
				}
				rt.mu.RLock()
				assert.Len(t, rt.agents, 1)
				assert.Len(t, rt.toolsets, 1, "only the runtime-owned toolset remains")
				assert.Empty(t, rt.registries)
				assert.Empty(t, rt.agentToolResolvers)
				rt.mu.RUnlock()
				assert.EqualValues(t, 1, eng.storageCalls.Load())
				assert.EqualValues(t, 1, eng.childCalls.Load())
				require.NoError(t, rt.Seal(context.Background()))
				assert.EqualValues(t, 1, eng.calls.Load())
			})
		})
	}
}

func TestSealContextDuringAnotherSealer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan context.Context, 1)
		release := make(chan struct{})
		releaseOnce := sync.OnceFunc(func() { close(release) })
		defer releaseOnce()
		cause := errors.New("original activation error")
		eng := &sealContextEngine{}
		eng.seal = func(ctx context.Context) error {
			if eng.calls.Load() == 1 {
				entered <- ctx
				<-release
				return cause
			}
			return nil
		}
		rt := New(newTestStore(), WithEngine(eng))
		firstCtx, firstCancel := context.WithCancel(context.Background())
		defer firstCancel()
		first := make(chan error, 1)
		go func() { first <- rt.Seal(firstCtx) }()
		assert.Same(t, firstCtx, <-entered)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		second := make(chan error, 1)
		go func() { second <- rt.Seal(ctx) }()
		synctest.Wait()
		cancel()
		require.ErrorIs(t, <-second, context.Canceled)
		requirePendingSealResult(t, first)
		assert.EqualValues(t, 1, eng.calls.Load())
		releaseOnce()
		assert.Same(t, cause, <-first)
		require.NoError(t, rt.Seal(context.Background()))
		assert.EqualValues(t, 2, eng.calls.Load())
		require.NoError(t, rt.Seal(ctx))
		assert.EqualValues(t, 2, eng.calls.Load())
	})
}

func TestSealEndedContextStillClosesRegistration(t *testing.T) {
	eng := &sealContextEngine{}
	rt := New(newTestStore(), WithEngine(eng))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, rt.Seal(ctx), context.Canceled)
	assert.False(t, rt.activationComplete.Load())
	assert.EqualValues(t, 0, eng.calls.Load())
	for _, call := range sealContextRegistrations(rt) {
		require.ErrorIs(t, call(), ErrRegistrationClosed)
	}
	require.NoError(t, rt.Seal(t.Context()))
	require.NoError(t, rt.Seal(ctx))
	assert.EqualValues(t, 1, eng.calls.Load())
}

func TestRegistrationAllMethodsWaitForAdmittedAgent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		releaseOnce := sync.OnceFunc(func() { close(release) })
		defer releaseOnce()
		eng := &sealContextEngine{storage: func() error {
			close(entered)
			<-release
			return nil
		}}
		rt := New(newTestStore(), WithEngine(eng))
		first := make(chan error, 1)
		go func() { first <- rt.RegisterAgent(context.Background(), sealContextAgent(rt, "first.agent", nil)) }()
		<-entered
		results := make([]<-chan error, 0, 4)
		for _, call := range sealContextRegistrations(rt) {
			out := make(chan error, 1)
			go func() { out <- call() }()
			results = append(results, out)
		}
		synctest.Wait()
		for _, out := range results {
			requirePendingSealResult(t, out)
		}
		releaseOnce()
		require.NoError(t, <-first)
		for _, out := range results {
			require.NoError(t, <-out)
		}
		rt.mu.RLock()
		assert.Len(t, rt.agents, 2)
		assert.Len(t, rt.toolsets, 2, "runtime-owned and newly registered toolsets")
		assert.Len(t, rt.registries, 1)
		assert.Len(t, rt.agentToolResolvers, 1)
		rt.mu.RUnlock()
		assert.EqualValues(t, 1, eng.storageCalls.Load())
		assert.EqualValues(t, 1, eng.childCalls.Load())
	})
}

func TestSealSynchronousPanicReleasesOwnership(t *testing.T) {
	original := errors.New("original sealer panic")
	eng := &sealContextEngine{seal: func(context.Context) error { panic(original) }}
	rt := New(newTestStore(), WithEngine(eng))
	assert.PanicsWithValue(t, original, func() { require.NoError(t, rt.Seal(t.Context())) })
	eng.seal = nil
	require.NoError(t, rt.Seal(t.Context()))
	assert.EqualValues(t, 2, eng.calls.Load())
}

func TestRegistrationSerializesValidationAndCommit(t *testing.T) {
	for _, mode := range []string{"agent_contract", "toolset_contract", "executable_owner"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt := New(newTestStore(), WithEngine(&sealContextEngine{}))
				entered := make(chan struct{})
				release := make(chan struct{})
				releaseOnce := sync.OnceFunc(func() { close(release) })
				defer releaseOnce()
				spec := newAnyJSONSpec("shared.lookup")
				decode := spec.Payload.Codec.FromJSON
				hold := sync.OnceFunc(func() {
					close(entered)
					<-release
				})
				spec.Payload.ExampleJSON = []byte(`{}`)
				spec.Payload.SchemaWithoutRootExample = spec.Payload.Schema
				spec.Payload.Codec.FromJSON = func(data []byte) (any, error) {
					hold()
					return decode(data)
				}
				first := make(chan error, 1)
				go func() {
					if mode == "agent_contract" {
						first <- rt.RegisterAgent(context.Background(), sealContextAgent(rt, "first.agent", []tools.ToolSpec{spec}))
					} else {
						first <- rt.RegisterToolset(sealContextToolset("first", spec))
					}
				}()
				select {
				case <-entered:
				case err := <-first:
					t.Fatalf("registration returned before decoding its example: %v", err)
				}
				secondSpec := newAnyJSONSpec(spec.Name)
				secondSpec.Payload.ExampleJSON = []byte(`{}`)
				secondSpec.Payload.SchemaWithoutRootExample = secondSpec.Payload.Schema
				if mode != "executable_owner" {
					secondSpec.Description = "different contract"
				}
				second := make(chan error, 1)
				go func() {
					if mode == "toolset_contract" {
						second <- rt.RegisterAgent(context.Background(), sealContextAgent(rt, "second.agent", []tools.ToolSpec{secondSpec}))
					} else {
						second <- rt.RegisterToolset(sealContextToolset("second", secondSpec))
					}
				}()
				synctest.Wait()
				requirePendingSealResult(t, second)
				releaseOnce()
				require.NoError(t, <-first)
				err := <-second
				if mode == "executable_owner" {
					require.ErrorContains(t, err, "already executed")
				} else {
					require.ErrorContains(t, err, "different contract")
				}
				stored, ok := rt.toolSpec(spec.Name)
				require.True(t, ok)
				assert.Equal(t, spec.Description, stored.Description)
			})
		})
	}
}

func TestRegistrationActivityFailureCanRetryWithoutDuplicatingSuccess(t *testing.T) {
	for _, phase := range []string{storageRegistrationPhase, "child"} {
		t.Run(phase, func(t *testing.T) {
			cause := errors.New("registration failed")
			eng := &sealContextEngine{}
			if phase == storageRegistrationPhase {
				eng.storage = func() error {
					if eng.storageCalls.Load() == 1 {
						return cause
					}
					return nil
				}
			} else {
				eng.child = func() error {
					if eng.childCalls.Load() == 1 {
						return cause
					}
					return nil
				}
			}
			rt := New(newTestStore(), WithEngine(eng))
			assert.Same(t, cause, rt.RegisterAgent(t.Context(), sealContextAgent(rt, "failed.agent", nil)))
			require.NoError(t, rt.RegisterAgent(t.Context(), sealContextAgent(rt, "next.agent", nil)))
			require.NoError(t, rt.RegisterAgent(t.Context(), sealContextAgent(rt, "last.agent", nil)))
			if phase == storageRegistrationPhase {
				assert.EqualValues(t, 2, eng.storageCalls.Load())
				assert.EqualValues(t, 1, eng.childCalls.Load())
			} else {
				assert.EqualValues(t, 1, eng.storageCalls.Load())
				assert.EqualValues(t, 2, eng.childCalls.Load())
			}
		})
	}
}

func TestRegistrationRegistryAndResolverErrorPriority(t *testing.T) {
	rt := New(newTestStore(), WithEngine(&sealContextEngine{}))
	client := &genregistry.Client{}
	pulse := unusedRegistryPulse{}
	resolver := func(context.Context, string, *ToolCall) (*AgentToolConfiguration, error) { return nil, nil }
	require.NoError(t, rt.RegisterRegistry("catalog", client, pulse))
	require.ErrorContains(t, rt.RegisterRegistry("catalog", client, pulse), "already registered")
	require.NoError(t, rt.RegisterAgentToolResolver("executor", resolver))
	require.ErrorContains(t, rt.RegisterAgentToolResolver("executor", resolver), "already has a resolver")
	require.NoError(t, rt.Seal(t.Context()))
	require.ErrorContains(t, rt.RegisterRegistry("", client, pulse), "required")
	require.ErrorContains(t, rt.RegisterAgentToolResolver("", resolver), "required")
	require.ErrorIs(t, rt.RegisterRegistry("catalog", client, pulse), ErrRegistrationClosed)
	require.ErrorIs(t, rt.RegisterAgentToolResolver("executor", resolver), ErrRegistrationClosed)
}

func (e *sealContextEngine) RegisterStorageActivity(ctx context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error)) error {
	e.storageCalls.Add(1)
	if e.storage != nil {
		if err := e.storage(); err != nil {
			return err
		}
	}
	return e.stubEngine.RegisterStorageActivity(ctx, name, opts, fn)
}

func (e *sealContextEngine) RegisterAgentChildActivity(ctx context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error)) error {
	e.childCalls.Add(1)
	if e.child != nil {
		if err := e.child(); err != nil {
			return err
		}
	}
	return e.stubEngine.RegisterAgentChildActivity(ctx, name, opts, fn)
}

func (e *sealContextEngine) SealRegistration(ctx context.Context) error {
	e.calls.Add(1)
	if e.seal != nil {
		return e.seal(ctx)
	}
	return nil
}

// sealContextAgent supplies a complete registration without executable tools,
// so these tests isolate registration ordering from planner execution.
func sealContextAgent(rt *Runtime, id agent.Ident, specs []tools.ToolSpec) AgentRegistration {
	return AgentRegistration{
		Definition:          testRegistrationDefinition(id, engine.WorkflowDefinition{}, specs),
		WorkflowHandler:     rt.ExecuteWorkflow,
		Planner:             &stubPlanner{},
		PlanActivityName:    string(id) + ".plan",
		ResumeActivityName:  string(id) + ".resume",
		ExecuteToolActivity: string(id) + ".execute",
	}
}

func sealContextToolset(name string, spec tools.ToolSpec) ToolsetRegistration {
	return ToolsetRegistration{
		Name:  name,
		Specs: []tools.ToolSpec{spec},
		Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
			return nil, nil
		},
	}
}

// sealContextRegistrations covers every public registration method with valid
// input, so tests can check both normal registration and closed admission.
func sealContextRegistrations(rt *Runtime) []func() error {
	return []func() error{
		func() error { return rt.RegisterAgent(context.Background(), sealContextAgent(rt, "later.agent", nil)) },
		func() error { return rt.RegisterToolset(sealContextToolset("later", newAnyJSONSpec("later.lookup"))) },
		func() error { return rt.RegisterRegistry("later", &genregistry.Client{}, unusedRegistryPulse{}) },
		func() error {
			return rt.RegisterAgentToolResolver("later", func(context.Context, string, *ToolCall) (*AgentToolConfiguration, error) {
				return nil, nil
			})
		},
	}
}

func requirePendingSealResult(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("held operation returned before release: %v", err)
	default:
	}
}
