// These tests separate an accepted operation result from later reporting and
// cleanup failures. Failed reporting must not replace or erase that result.
package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	genrunoutput "goa.design/goa-ai/registry/gen/runoutput"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/content"
)

type (
	// selectedLifetimeWorkflow fails one real storage request after the planner
	// result has crossed activity validation. Terminal storage still executes.
	selectedLifetimeWorkflow struct {
		engine.WorkflowContext
		fail    func(hooks.Event) bool
		failure error
	}
	selectedLifetimeDelivery struct{ failure error }
	selectedLifetimeControl  struct {
		engine.ProviderRecovery
		failure error
	}
)

func (w *selectedLifetimeWorkflow) Context() context.Context {
	return engine.WithWorkflowContext(w.WorkflowContext.Context(), w)
}
func (w *selectedLifetimeWorkflow) Detached() engine.WorkflowContext {
	return &selectedLifetimeWorkflow{WorkflowContext: w.WorkflowContext.Detached(), fail: w.fail, failure: w.failure}
}
func (w *selectedLifetimeWorkflow) WithCancel() (engine.WorkflowContext, func()) {
	next, cancel := w.WorkflowContext.WithCancel()
	return &selectedLifetimeWorkflow{WorkflowContext: next, fail: w.fail, failure: w.failure}, cancel
}
func (w *selectedLifetimeWorkflow) ExecuteStorageActivity(call engine.StorageActivityCall) (*api.StorageActivityResult, error) {
	if call.Command.Append != nil {
		for _, record := range call.Command.Append.Records {
			if record.Type != hooks.PlannerNote && record.Type != hooks.RunPhaseChanged && record.Type != hooks.ToolResultReceived {
				continue
			}
			event, err := hooks.DecodeFromRecordInput(record)
			if err != nil {
				return nil, err
			}
			if w.fail(event) {
				return nil, w.failure
			}
		}
	}
	return w.WorkflowContext.ExecuteStorageActivity(call)
}
func (d selectedLifetimeDelivery) IsReady() bool { return true }
func (d selectedLifetimeDelivery) Get(context.Context) (struct{}, error) {
	return struct{}{}, d.failure
}
func (c selectedLifetimeControl) Send(engine.WorkflowContext, engine.ProviderRecoveryMessage) (engine.Future[struct{}], error) {
	return selectedLifetimeDelivery{failure: c.failure}, nil
}

func TestSelectedResultSurvivesLaterWorkflowReportingFailure(t *testing.T) {
	for _, stage := range []string{"executing-tools", "notes"} {
		for _, failed := range []bool{false, true} {
			name := "success"
			if failed {
				name = "failure"
			}
			t.Run(stage+"/"+name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				store := &terminalActivityFaultStore{Store: newTestStore()}
				adapter := engineinmem.New()
				rt := New(store, WithEngine(adapter))
				resolution := testNativeRegistryResolution("revision/1")
				operation := resolution.Toolset.Tools[0]
				resolved, err := registrycontract.Resolve(resolution)
				require.NoError(t, err)
				spec := resolved.Specs[tools.Ident(operation.Name)]
				definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
				result := &planner.FinalToolResult{Result: rawjson.Message(`{"value":9007199254740993}`), Blocks: content.Blocks{&content.TextContent{Text: "Original selected evidence"}}}
				if failed {
					result.Result = nil
					result.Failure = &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolError("Original selected failure"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}
				}
				expected, err := operationResultFromToolEvent(finalToolResultEvent(spec.Name, result))
				require.NoError(t, err)
				fault := errors.New("reporting failed after selection")
				require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
					Definition: definition, PlanActivityName: "lifetime.plan", ResumeActivityName: "lifetime.resume", ExecuteToolActivity: "lifetime.execute",
					WorkflowHandler: func(wf engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
						return rt.ExecuteWorkflow(&selectedLifetimeWorkflow{WorkflowContext: wf, failure: fault, fail: func(event hooks.Event) bool {
							if stage == "notes" {
								return event.Type() == hooks.PlannerNote
							}
							phase, ok := event.(*hooks.RunPhaseChangedEvent)
							return ok && phase.Phase == run.PhaseExecutingTools
						}}, input)
					},
					Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
						return &planner.PlanResult{FinalToolResult: result, Notes: []planner.PlannerAnnotation{{Text: "Report completion"}}}, nil
					}},
				}))
				require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
					return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read the quantity."}}}})}, nil
				}))
				handle, err := rt.StartOneShotOperation(ctx, "selected-lifetime", operation, rawjson.Message(`{"value":1}`), nil, nil)
				require.NoError(t, err)
				output, err := handle.Wait(ctx)
				require.ErrorContains(t, err, fault.Error())
				assert.Nil(t, output)
				meta, err := store.LoadRun(ctx, "selected-lifetime")
				require.NoError(t, err)
				assert.Equal(t, session.RunStatusFailed, meta.Status)
				require.Len(t, store.terminalResults, 1)
				saved, err := genrunoutput.EncodeOperationResult(store.terminalResults[0])
				require.NoError(t, err)
				want, err := genrunoutput.EncodeOperationResult(expected)
				require.NoError(t, err)
				assert.Equal(t, want, saved)
			})
		}
	}
}

func TestSelectedResultSurvivesRecoveryDeliveryCloseFailure(t *testing.T) {
	for _, failure := range []error{errors.New("recovery delivery failed"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			rt := New(newTestStore())
			wf, err := installProviderRecovery(&testWorkflowContext{ctx: t.Context(), runtime: rt}, nil)
			require.NoError(t, err)
			state := &workflowFinalizationState{}
			base := &workflowConversation{finalization: state, RunContext: run.Context{RunID: "selected-close", Tool: "records.read"}}
			input := &RunInput{RunID: "selected-close", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}}
			result := &PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":9007199254740993}`), Blocks: content.Blocks{&content.TextContent{Text: "Before cleanup"}}}}
			output, err := rt.materializeTerminalPlannerResult(wf.Context(), input, base, "", terminalPlannerState{result: result})
			require.NoError(t, err)
			require.NotNil(t, output.FinalToolResult)
			// A real queued delivery fails only when close drains its acceptance future.
			require.NoError(t, wf.actor.send(wf, selectedLifetimeControl{failure: failure}, engine.ProviderRecoveryStopped{Err: failure}))
			closeErr := wf.actor.close(wf, nil)
			require.ErrorIs(t, closeErr, failure)
			command, err := buildRunTerminalCommand(input, "", terminalRunStatusForError(closeErr), closeErr, state.operationResult, recordDispatchMetadata{EventKey: "terminal", TimestampMS: 1700000000000})
			require.NoError(t, err)
			retained, err := genrunoutput.DecodeOperationResult(command.OperationResult)
			require.NoError(t, err)
			success, ok := retained.Outcome.AsSuccess()
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

func TestSelectedResultRejectsUnacceptedPlannerResults(t *testing.T) {
	for _, kind := range []string{"invalid-schema", "required-completion", "invalid-finalization", "bookkeeping-pending"} {
		t.Run(kind, func(t *testing.T) {
			rt := New(newTestStore())
			resolution := testNativeRegistryResolution("revision/1")
			resolved, err := registrycontract.Resolve(resolution)
			require.NoError(t, err)
			selected := resolved.Specs[tools.Ident(resolution.Toolset.Tools[0].Name)]
			bookkeeping := newAnyJSONSpec("records.bookkeeping")
			bookkeeping.Bookkeeping = true
			definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{selected, bookkeeping}, nil)
			rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
			seedTestToolset(rt, "records", selected, bookkeeping)
			result := &PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":1}`)}}
			runContext := run.Context{RunID: "selection-validation", Tool: selected.Name, ToolArgs: rawjson.Message(`{"value":1}`)}
			input := PlanActivityInput{RunID: runContext.RunID, AgentID: definition.route.ID, RunContext: runContext}
			switch kind {
			case "invalid-schema":
				result.FinalToolResult.Result = rawjson.Message(`{"value":"wrong"}`)
			case "required-completion":
				input.Policy = &PolicyOverrides{CompletionTool: bookkeeping.Name}
			case "invalid-finalization", "bookkeeping-pending":
				result.ToolCalls = []ToolCall{{Name: bookkeeping.Name, ToolCallID: "required-bookkeeping", Payload: rawjson.Message(`{}`)}}
				if kind == "invalid-finalization" {
					input.Finalize = &planner.Termination{Reason: planner.TerminationReasonTimeBudget}
				}
			}
			input = *seedTestPlanInput(t, rt, input, nil)
			base := &workflowConversation{finalization: &workflowFinalizationState{}, RunContext: runContext, HistoryEndID: input.HistoryEndID}
			wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, hasPlanResult: true, planResult: result}
			output, err := rt.runPlanActivityOnce(wf, "plan", engine.ActivityOptions{}, input, base, time.Time{})
			if kind == "bookkeeping-pending" {
				require.NoError(t, err)
				require.NotNil(t, output)
			} else {
				require.Error(t, err)
			}
			assert.Nil(t, base.finalization.operationResult)
		})
	}
}

func TestSelectedCompletionCallRetainedBeforeResultReporting(t *testing.T) {
	for _, kind := range []string{"terminal", "required-completion", "multiple", "clarification"} {
		t.Run(kind, func(t *testing.T) {
			spec := newAnyJSONSpec("records.complete")
			spec.TerminalRun = kind != "required-completion"
			spec.Bookkeeping = spec.TerminalRun
			h := newRecoveryHarness(t, "selected-"+kind, []tools.ToolSpec{spec}, func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
				return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"ok": true}, Blocks: content.Blocks{&content.TextContent{Text: "Completed side effect"}}}, nil
			}, nil)
			if kind == "required-completion" {
				h.input.Policy = &PolicyOverrides{CompletionTool: spec.Name}
			}
			if kind == "clarification" {
				registration := h.runtime.toolsets["catalog"]
				original := registration.Execute
				registration.Execute = func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
					result, err := original(ctx, call)
					if err != nil {
						return nil, err
					}
					result.Clarification = &ToolClarification{ID: "clarification", Question: "Which record?"}
					return result, nil
				}
				h.runtime.toolsets["catalog"] = registration
			}
			calls := []ToolCall{{Name: spec.Name, ToolCallID: "selected-call", ModelToolCallID: "model-selected", Payload: rawjson.Message(`{}`)}}
			if kind == "multiple" {
				calls = append(calls, ToolCall{Name: spec.Name, ToolCallID: "second-call", ModelToolCallID: "model-second", Payload: rawjson.Message(`{}`)})
			}
			fault := errors.New("tool-result reporting failed")
			wf := &selectedLifetimeWorkflow{WorkflowContext: h.workflow, failure: fault, fail: func(event hooks.Event) bool { return event.Type() == hooks.ToolResultReceived }}
			out, err := h.runtime.runLoop(wf, h.registration, h.input, h.base, &PlanResult{ToolCalls: calls}, initialCaps(RunPolicy{MaxToolCalls: 3}), time.Time{}, time.Time{}, h.input.TurnID, nil)
			require.ErrorContains(t, err, fault.Error())
			assert.Nil(t, out)
			if kind == "multiple" || kind == "clarification" {
				assert.Nil(t, h.base.finalization.operationResult)
				return
			}
			require.NotNil(t, h.base.finalization.operationResult)
			result, ok := h.base.finalization.operationResult.Outcome.AsSuccess()
			require.True(t, ok)
			value, ok := result.Value.AsJSON()
			require.True(t, ok)
			assert.JSONEq(t, `{"ok":true}`, string(value))
			require.Len(t, result.Blocks, 1)
			block, ok := result.Blocks[0].Content.AsText()
			require.True(t, ok)
			assert.Equal(t, "Completed side effect", block.Text)
		})
	}
}

func TestSelectedResultRemainsProvisionalWhenBookkeepingDeadlineExpires(t *testing.T) {
	bookkeeping := newBookkeepingSpec("records.bookkeeping")
	selected := newAnyJSONSpec("records.finish")
	calls := 0
	h := newRecoveryHarness(t, "provisional-deadline", []tools.ToolSpec{bookkeeping, selected}, func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
		calls++
		return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"ok": true}}, nil
	}, nil)
	h.base.RunContext.Tool = selected.Name
	h.base.RunContext.ToolArgs = rawjson.Message(`{}`)
	hard := time.Unix(100, 0)
	h.workflow.now = func() time.Time { return hard }
	initial := &PlanResult{
		FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"provisional":true}`)},
		ToolCalls:       []ToolCall{{Name: bookkeeping.Name, ToolCallID: "required-write", ModelToolCallID: "model-write", Payload: rawjson.Message(`{}`)}},
	}
	out, err := h.runtime.runLoop(h.workflow, h.registration, h.input, h.base, initial, initialCaps(RunPolicy{}), time.Time{}, hard, h.input.TurnID, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, out)
	assert.Zero(t, calls)
	assert.Nil(t, h.base.finalization.operationResult)
}
