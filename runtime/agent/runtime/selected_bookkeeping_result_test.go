package runtime

// These workflows complete required bookkeeping before result reporting fails.
// The selected business result survives, but the workflow still ends in error.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	genrunoutput "goa.design/goa-ai/registry/gen/runoutput"
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

type selectedCloseNoteBus struct {
	hooks.Bus
	note atomic.Bool
}

func (b *selectedCloseNoteBus) Publish(ctx context.Context, event hooks.Event) error {
	if err := b.Bus.Publish(ctx, event); err != nil {
		return err
	}
	if event.Type() == hooks.PlannerNote {
		b.note.Store(true)
	}
	return nil
}

func TestSelectedResultSurvivesCompletedBookkeepingReportingFailure(t *testing.T) {
	for _, kind := range []string{"sole", "multiple-groups", "failed", "clarification"} {
		for _, failedSelection := range []bool{false, true} {
			name := "success"
			if failedSelection {
				name = "failure"
			}
			t.Run(kind+"/"+name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				store := &terminalActivityFaultStore{Store: newTestStore()}
				backend := engineinmem.New()
				rt := New(store, WithEngine(backend))
				resolution := testNativeRegistryResolution("revision/1")
				resolved, err := registrycontract.Resolve(resolution)
				require.NoError(t, err)
				operation := resolution.Toolset.Tools[0]
				selected := resolved.Specs[tools.Ident(operation.Name)]
				first := newBookkeepingSpec("records.write_first")
				second := newBookkeepingSpec("records.write_second")
				var calls atomic.Int32
				require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Inline: true, Specs: []tools.ToolSpec{first, second}, Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
					calls.Add(1)
					result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"written": true}}
					if kind == "failed" {
						result.Result = nil
						result.Failure = &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolError("required write failed"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}
					}
					outcome := Executed(result)
					if kind == "clarification" {
						outcome.Clarification = &ToolClarification{ID: "choose-record", Question: "Which record?"}
					}
					return outcome, nil
				}}))
				result := &planner.FinalToolResult{Result: rawjson.Message(`{"value":9007199254740993}`), Blocks: content.Blocks{&content.TextContent{Text: "Selected evidence"}}}
				if failedSelection {
					result.Result = nil
					result.Failure = &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolError("selected business failure"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}
				}
				expected, err := operationResultFromToolEvent(finalToolResultEvent(selected.Name, result))
				require.NoError(t, err)
				fault := errors.New("completed bookkeeping report failed")
				definition := NewAgentDefinition(AgentRoute{ID: "generic.agent", WorkflowName: "generic.workflow", DefaultTaskQueue: "generic.queue"}, []tools.ToolSpec{selected, first, second}, nil, nil, []tools.Ident{first.Name, second.Name}, nil, nil)
				require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{Definition: definition, PlanActivityName: "bookkeeping.plan", ResumeActivityName: "bookkeeping.resume", ExecuteToolActivity: "bookkeeping.execute", WorkflowHandler: func(wf engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
					return rt.ExecuteWorkflow(&selectedLifetimeWorkflow{WorkflowContext: wf, failure: fault, fail: func(event hooks.Event) bool {
						report, ok := event.(*hooks.ToolResultReceivedEvent)
						return ok && (kind != "multiple-groups" || report.ToolName == second.Name)
					}}, input)
				}, Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					requests := []planner.ToolRequest{{Name: first.Name, Payload: rawjson.Message(`{}`)}}
					if kind == "multiple-groups" {
						requests = append(requests, planner.ToolRequest{Name: second.Name, Payload: rawjson.Message(`{}`)})
					}
					return &planner.PlanResult{FinalToolResult: result, ToolCalls: requests}, nil
				}}}))
				require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
					return &AgentToolConfiguration{Policy: &PolicyOverrides{PerToolTimeout: map[tools.Ident]time.Duration{first.Name: time.Second, second.Name: 2 * time.Second}}, History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Finish and record the result."}}}})}, nil
				}))
				handle, err := rt.StartOneShotOperation(ctx, "bookkeeping-result", operation, rawjson.Message(`{"value":1}`), nil, nil)
				require.NoError(t, err)
				out, err := handle.Wait(ctx)
				require.ErrorContains(t, err, fault.Error())
				assert.Nil(t, out)
				meta, err := store.LoadRun(ctx, "bookkeeping-result")
				require.NoError(t, err)
				assert.Equal(t, session.RunStatusFailed, meta.Status)
				require.Len(t, store.terminalResults, 1)
				if kind == "failed" || kind == "clarification" {
					assert.Nil(t, store.terminalResults[0])
					return
				}
				want, err := genrunoutput.EncodeOperationResult(expected)
				require.NoError(t, err)
				saved, err := genrunoutput.EncodeOperationResult(store.terminalResults[0])
				require.NoError(t, err)
				assert.Equal(t, want, saved)
				expectedCalls := int32(1)
				if kind == "multiple-groups" {
					expectedCalls = 2
				}
				assert.Equal(t, expectedCalls, calls.Load())
			})
		}
	}
}

func TestSelectedResultSurvivesActualChildRecoveryCloseFailure(t *testing.T) {
	for _, closeErr := range []error{errors.New("parent rejected recovery report"), context.Canceled} {
		t.Run(closeErr.Error(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			store := &terminalActivityFaultStore{Store: newTestStore()}
			backend := engineinmem.New()
			bus := &selectedCloseNoteBus{Bus: hooks.NewBus()}
			rt := New(store, WithEngine(backend), WithHooks(bus))
			resolution := testNativeRegistryResolution("revision/1")
			resolved, err := registrycontract.Resolve(resolution)
			require.NoError(t, err)
			operation := resolution.Toolset.Tools[0]
			selected := resolved.Specs[tools.Ident(operation.Name)]
			bookkeeping := newBookkeepingSpec("records.write")
			require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Inline: true, Specs: []tools.ToolSpec{bookkeeping}, Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
				wf, ok := engine.WorkflowContextFromContext(ctx).(*providerRecoveryWorkflowContext)
				if !ok {
					return nil, errors.New("bookkeeping did not receive installed recovery context")
				}
				// The actual child records a measured interval. Its issued parent
				// rejects the report, and the normal workflow close drains that reply.
				if err := wf.actor.recordOwn(wf, wf.Now().Add(-time.Millisecond), wf.Now()); err != nil {
					return nil, err
				}
				return Executed(&planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"written": true}}), nil
			}}))
			definition := NewAgentDefinition(AgentRoute{ID: "generic.agent", WorkflowName: "generic.workflow", DefaultTaskQueue: "generic.queue"}, []tools.ToolSpec{selected, bookkeeping}, nil, nil, []tools.Ident{bookkeeping.Name}, nil, nil)
			require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{Definition: definition, WorkflowHandler: rt.ExecuteWorkflow, PlanActivityName: "close.plan", ResumeActivityName: "close.resume", ExecuteToolActivity: "close.execute", Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":9007199254740993}`), Blocks: content.Blocks{&content.TextContent{Text: "Before cleanup"}}}, ToolCalls: []planner.ToolRequest{{Name: bookkeeping.Name, Payload: rawjson.Message(`{}`)}}, Notes: []planner.PlannerAnnotation{{Text: "Selection completed"}}}, nil
			}}}))
			require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
				return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Complete the operation."}}}})}, nil
			}))
			request, err := prepareOperationRequest("", "selected-child", operation, rawjson.Message(`{"value":1}`), nil, nil)
			require.NoError(t, err)
			var reports atomic.Int32
			require.NoError(t, backend.RegisterWorkflow(ctx, engine.WorkflowDefinition{Name: "parent", Handler: func(wf engine.WorkflowContext, _ *RunInput) (*RunOutput, error) {
				if err := wf.ProviderRecovery().RegisterPauseHandler(func(_ engine.WorkflowContext, childID string, _ engine.ProviderRecoveryPaused) error {
					if childID != request.ID {
						return errors.New("report did not retain issued child identity")
					}
					reports.Add(1)
					return closeErr
				}); err != nil {
					return nil, err
				}
				child, err := wf.StartChildWorkflow(wf.Context(), engine.ChildWorkflowRequest{ID: request.ID, Workflow: request.Workflow, TaskQueue: request.TaskQueue, Input: request.Input})
				if err != nil {
					return nil, err
				}
				return child.Get(wf.Context())
			}}))
			handle, err := backend.StartWorkflow(ctx, engine.WorkflowStartRequest{ID: "parent", Workflow: "parent", TaskQueue: "test", Input: &RunInput{RunID: "parent", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.parent")}}})
			require.NoError(t, err)
			out, err := handle.Wait(ctx)
			require.ErrorContains(t, err, closeErr.Error())
			assert.Nil(t, out)
			assert.Positive(t, reports.Load())
			assert.True(t, bus.note.Load(), "the child reached selected-result materialization before its close failed")
			meta, err := store.LoadRun(ctx, request.ID)
			require.NoError(t, err)
			expectedStatus := session.RunStatusFailed
			if errors.Is(closeErr, context.Canceled) {
				expectedStatus = session.RunStatusCanceled
			}
			assert.Equal(t, expectedStatus, meta.Status)
			require.Len(t, store.terminalResults, 1)
			require.NotNil(t, store.terminalResults[0])
			success, ok := store.terminalResults[0].Outcome.AsSuccess()
			require.True(t, ok)
			value, ok := success.Value.AsJSON()
			require.True(t, ok)
			assert.Equal(t, `{"value":9007199254740993}`, string(value))
			require.Len(t, success.Blocks, 1)
			block, ok := success.Blocks[0].Content.AsText()
			require.True(t, ok)
			assert.Equal(t, "Before cleanup", block.Text)
		})
	}
}
