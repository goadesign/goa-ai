// Cancellation after an operation returns changes the run's final status, but
// terminal storage must retain that operation's exact result and content.
package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/content"
)

type (
	// selectedResultPause holds a workflow after its operation returns until the
	// test delivers cancellation through the engine's real callback queue.
	selectedResultPause struct {
		produced atomic.Bool
		once     sync.Once
		ready    chan struct{}
		release  chan struct{}
	}
	// selectedResultWorkflowContext pauses the Agent's finalization after its
	// planner produced the selected result, before cancellation admission closes.
	selectedResultWorkflowContext struct {
		engine.WorkflowContext
		pause *selectedResultPause
	}
	// selectedResultBus pauses the service's detached tool-result write after
	// its provider returned. Cancellation can still run while this write waits.
	selectedResultBus struct {
		hooks.Bus
		pause *selectedResultPause
	}
)

func (w *selectedResultWorkflowContext) Detached() engine.WorkflowContext {
	detached := w.WorkflowContext.Detached()
	if w.pause.produced.Load() {
		w.pause.once.Do(func() {
			close(w.pause.ready)
			if err := detached.Await(func() bool {
				select {
				case <-w.pause.release:
					return true
				default:
					return false
				}
			}); err != nil {
				panic(err)
			}
		})
	}
	return detached
}

func (b *selectedResultBus) Publish(ctx context.Context, event hooks.Event) error {
	if err := b.Bus.Publish(ctx, event); err != nil {
		return err
	}
	if event.Type() == hooks.ToolResultReceived {
		b.pause.once.Do(func() { close(b.pause.ready); <-b.pause.release })
	}
	return nil
}

func TestCancellationRetainsProducedSelectedResult(t *testing.T) {
	for _, kind := range []string{"agent", "service"} {
		for _, failed := range []bool{false, true} {
			name := "success"
			if failed {
				name = "failure"
			}
			t.Run(kind+"/"+name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				pause := &selectedResultPause{ready: make(chan struct{}), release: make(chan struct{})}
				var release sync.Once
				t.Cleanup(func() { release.Do(func() { close(pause.release) }) })
				store := &terminalActivityFaultStore{Store: newTestStore()}
				backend := engineinmem.New()
				result := &api.ToolOutput{Payload: rawjson.Message(`{"value":9007199254740993}`), Blocks: content.Blocks{&content.TextContent{Text: "Completed evidence"}}}
				if failed {
					result.Payload = nil
					result.Failure = &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolError("Selected operation unavailable"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}
				}
				var handle engine.WorkflowHandle
				var err error
				runID := "service-root"
				if kind == "service" {
					resolution, invocation := registeredRootFixture()
					serviceEngine := registeredRootTestEngine{Engine: backend, execute: func(context.Context, *api.ToolInput) (*api.ToolOutput, error) { return result, nil }}
					rt := New(store, WithEngine(serviceEngine), WithToolWorkflowQueue("service-worker"), WithHooks(&selectedResultBus{Bus: hooks.NewBus(), pause: pause}))
					require.NoError(t, rt.RegisterToolWorkflow(ctx))
					handle, err = rt.StartOneShotToolOperation(ctx, "registry", resolution, invocation, nil, nil)
				} else {
					runID = "agent-root"
					resolution := testNativeRegistryResolution("revision/1")
					operation := resolution.Toolset.Tools[0]
					resolved, resolveErr := registrycontract.Resolve(resolution)
					require.NoError(t, resolveErr)
					spec := resolved.Specs[tools.Ident(operation.Name)]
					rt := New(store, WithEngine(backend))
					definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
					require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
						Definition: definition,
						WorkflowHandler: func(wf engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
							return rt.ExecuteWorkflow(&selectedResultWorkflowContext{WorkflowContext: wf, pause: pause}, input)
						},
						PlanActivityName: "selected.plan", ResumeActivityName: "selected.resume", ExecuteToolActivity: "selected.execute",
						Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
							pause.produced.Store(true)
							return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: result.Payload, Blocks: result.Blocks, Failure: result.Failure}}, nil
						}},
					}))
					require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
						return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read the quantity."}}}})}, nil
					}))
					handle, err = rt.StartOneShotOperation(ctx, runID, operation, rawjson.Message(`{"value":1}`), nil, nil)
				}
				require.NoError(t, err)
				select {
				case <-pause.ready:
				case <-ctx.Done():
					t.Fatal("selected result did not reach the cancellation window")
				}
				require.NoError(t, backend.(engine.CancellationRequester).RequestCancellation(ctx, runID, engine.CancellationRequest{RunID: runID, Reason: "stop-after-result"}))
				release.Do(func() { close(pause.release) })
				output, err := handle.Wait(ctx)
				require.Error(t, err)
				assert.Nil(t, output, "cancellation must not return a successful engine output")
				completion, err := backend.QueryRunCompletion(ctx, runID)
				require.NoError(t, err)
				assert.Equal(t, engine.RunStatusCanceled, completion.Status)
				owner, err := store.LoadRun(ctx, runID)
				require.NoError(t, err)
				assert.Equal(t, session.RunStatusCanceled, owner.Status)
				assert.Equal(t, "stop-after-result", owner.CancellationReason)
				require.Len(t, store.terminalResults, 1)
				require.NotNil(t, store.terminalResults[0])
				if failed {
					saved, ok := store.terminalResults[0].Outcome.AsFailure()
					require.True(t, ok)
					assert.Equal(t, "Selected operation unavailable", saved.Message)
					assert.Equal(t, string(planner.FailureUnavailable), saved.Kind)
					require.Len(t, saved.Blocks, 1)
					block, ok := saved.Blocks[0].Content.AsText()
					require.True(t, ok)
					assert.Equal(t, "Completed evidence", block.Text)
				} else {
					saved, ok := store.terminalResults[0].Outcome.AsSuccess()
					require.True(t, ok)
					value, ok := saved.Value.AsJSON()
					require.True(t, ok)
					assert.Equal(t, string(result.Payload), string(value))
					require.Len(t, saved.Blocks, 1)
					block, ok := saved.Blocks[0].Content.AsText()
					require.True(t, ok)
					assert.Equal(t, "Completed evidence", block.Text)
				}
				require.Len(t, store.terminalRecords, 1)
				event, err := hooks.DecodeRunlogEvent(store.terminalRecords[0])
				require.NoError(t, err)
				completed, ok := event.(*hooks.RunCompletedEvent)
				require.True(t, ok)
				assert.Equal(t, "stop-after-result", completed.Cancellation.Reason)
			})
		}
	}
}
