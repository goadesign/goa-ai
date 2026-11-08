package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

// refusalTestError implements planner.ToolFailureProvider and error.
type refusalTestError struct {
	failure *planner.ToolFailure
}

func (e *refusalTestError) Error() string {
	if e.failure != nil && e.failure.Error != nil {
		return e.failure.Error.Message
	}
	return "refusal"
}

func (e *refusalTestError) ToolFailure(_ tools.Ident) *planner.ToolFailure {
	return e.failure
}

func TestConfirmationPlanPromptRefusal(t *testing.T) {
	t.Parallel()

	expectedFailure := &planner.ToolFailure{
		Kind:  planner.FailureDomainRejection,
		Error: planner.NewToolError("policy denied execution"),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryReplan,
		},
	}

	rt := New(newTestStore())
	rt.toolConfirmation = &ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{
			tools.Ident("tool.refused"): {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "", &refusalTestError{failure: expectedFailure}
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]string{"status": "denied"}, nil
				},
			},
		},
	}

	call := &ToolCall{
		Name:       tools.Ident("tool.refused"),
		ToolCallID: "call-1",
		Payload:    rawjson.Message(`{"param":"val"}`),
	}

	plan, needs, err := rt.confirmationPlan(t.Context(), call)
	require.NoError(t, err)
	require.False(t, needs)
	require.NotNil(t, plan)
	require.NotNil(t, plan.Refusal)
	assert.Equal(t, planner.FailureDomainRejection, plan.Refusal.Kind)
	assert.Equal(t, "policy denied execution", plan.Refusal.Error.Message)
	assert.Equal(t, planner.RecoveryReplan, plan.Refusal.Recovery.Action)
}

func TestSplitConfirmationCallsWithRefusal(t *testing.T) {
	t.Parallel()

	refusalFailure := &planner.ToolFailure{
		Kind:  planner.FailureDomainRejection,
		Error: planner.NewToolError("pre-confirmation refusal"),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryReplan,
		},
	}

	rt := New(newTestStore())
	rt.toolConfirmation = &ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{
			tools.Ident("tool.confirm"): {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "Confirm execution?", nil
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]string{"status": "denied"}, nil
				},
			},
			tools.Ident("tool.refused"): {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "", &refusalTestError{failure: refusalFailure}
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]string{"status": "denied"}, nil
				},
			},
		},
	}

	allowed := []ToolCall{
		{Name: tools.Ident("tool.immediate"), ToolCallID: "call-1", Payload: rawjson.Message(`{}`)},
		{Name: tools.Ident("tool.confirm"), ToolCallID: "call-2", Payload: rawjson.Message(`{}`)},
		{Name: tools.Ident("tool.refused"), ToolCallID: "call-3", Payload: rawjson.Message(`{}`)},
	}
	specs := []tools.ToolSpec{
		{Name: tools.Ident("tool.immediate")},
		{Name: tools.Ident("tool.confirm")},
		{Name: tools.Ident("tool.refused")},
	}

	base := &workflowConversation{
		RunContext: run.Context{
			RunID:  "run-1",
			TurnID: "turn-1",
		},
	}

	toExecute, toConfirm, refusals, err := rt.splitConfirmationCalls(t.Context(), base, allowed, specs)
	require.NoError(t, err)
	require.Len(t, toExecute, 1)
	assert.Equal(t, tools.Ident("tool.immediate"), toExecute[0].Name)

	require.Len(t, toConfirm, 1)
	assert.Equal(t, tools.Ident("tool.confirm"), toConfirm[0].call.Name)

	require.Len(t, refusals, 1)
	assert.Equal(t, tools.Ident("tool.refused"), refusals[0].call.Name)
	require.NotNil(t, refusals[0].failure)
	assert.Equal(t, planner.FailureDomainRejection, refusals[0].failure.Kind)
	assert.Equal(t, "pre-confirmation refusal", refusals[0].failure.Error.Message)
}

func TestRunLoopPreConfirmationRefusalImmediateToolOutput(t *testing.T) {
	t.Parallel()

	refusalFailure := &planner.ToolFailure{
		Kind:  planner.FailureDomainRejection,
		Error: planner.NewToolError("operator rejected before prompt"),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryReplan,
		},
	}

	lookup := newAnyJSONSpec(tools.Ident("svc.lookup"))
	denied := newAnyJSONSpec(tools.Ident("svc.denied"))

	rt := New(newTestStore())
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result: map[string]any{
					"name": string(call.Name),
				},
			}, nil
		}),
		Specs: []tools.ToolSpec{lookup, denied},
	}))

	rt.toolConfirmation = &ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{
			denied.Name: {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "", &refusalTestError{failure: refusalFailure}
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]string{"status": "denied"}, nil
				},
			},
		},
	}

	input := &RunInput{
		Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agent.Ident("agent-1")))},
		RunID:     "run-refusal-1",
		SessionID: "sess-refusal-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)

	base := &workflowConversation{
		finalization: &workflowFinalizationState{},
		RunContext: run.Context{
			RunID:     "run-refusal-1",
			SessionID: "sess-refusal-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}

	wfCtx := &testWorkflowContext{
		ctx: t.Context(),
		asyncResult: ToolOutput{
			Payload: rawjson.Message(`{"lookup":"ok"}`),
		},
		planResult: &PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "done after replan"}},
				},
			},
		},
		hasPlanResult: true,
		hookRuntime:   rt,
		recoveryCatalog: &RecoveryCatalog{
			Tools: []tools.Ident{lookup.Name, denied.Name},
		},
	}

	initial := &PlanResult{
		ToolCalls: []ToolCall{
			{ToolCallID: "call-lookup", Name: lookup.Name, Payload: rawjson.Message(`{}`)},
			{ToolCallID: "call-denied", Name: denied.Name, Payload: rawjson.Message(`{}`)},
		},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{
			Definition:          testRegistrationDefinition(testSubjectAgent(t, input.Subject), engine.WorkflowDefinition{}, nil),
			ExecuteToolActivity: "execute",
			ResumeActivityName:  "resume",
		},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Nil(t, out.Suspension)

	// Verify that wfCtx received planner resume input with both tool outputs in call order.
	observedToolOutputs := wfCtx.lastPlannerCall.Input.ToolOutputs
	require.Len(t, observedToolOutputs, 2)
	assert.Equal(t, "call-lookup", observedToolOutputs[0].ToolCallID)
	assert.Equal(t, "call-denied", observedToolOutputs[1].ToolCallID)

	// Verify that the stored runlog events contain the tool results in call order,
	// with call-denied carrying the pre-confirmation refusal failure.
	storedResults := storedToolResults(t, rt, input.RunID)
	require.Len(t, storedResults, 2)
	assert.Equal(t, lookup.Name, storedResults[0].ToolName)
	assert.Nil(t, storedResults[0].Failure)

	assert.Equal(t, denied.Name, storedResults[1].ToolName)
	require.NotNil(t, storedResults[1].Failure)
	assert.Equal(t, planner.FailureDomainRejection, storedResults[1].Failure.Kind)
	assert.Equal(t, "operator rejected before prompt", storedResults[1].Failure.Error.Message)
}

func TestRunLoopPreConfirmationRefusalDoesNotAbortSiblingConfirmation(t *testing.T) {
	t.Parallel()

	refusalFailure := &planner.ToolFailure{
		Kind:  planner.FailureDomainRejection,
		Error: planner.NewToolError("pre-confirmation refusal"),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryReplan,
		},
	}

	confirm := newAnyJSONSpec(tools.Ident("svc.confirm"))
	denied := newAnyJSONSpec(tools.Ident("svc.denied"))
	lookup := newAnyJSONSpec(tools.Ident("svc.lookup"))

	rt := New(newTestStore())
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result: map[string]any{
					"name": string(call.Name),
				},
			}, nil
		}),
		Specs: []tools.ToolSpec{confirm, denied, lookup},
	}))

	rt.toolConfirmation = &ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{
			confirm.Name: {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "Confirm svc.confirm?", nil
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]string{"status": "denied"}, nil
				},
			},
			denied.Name: {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "", &refusalTestError{failure: refusalFailure}
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]string{"status": "denied"}, nil
				},
			},
		},
	}

	input := &RunInput{
		Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agent.Ident("agent-1")))},
		RunID:     "run-refusal-sibling",
		SessionID: "sess-refusal-sibling",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)

	base := &workflowConversation{
		finalization: &workflowFinalizationState{},
		RunContext: run.Context{
			RunID:     "run-refusal-sibling",
			SessionID: "sess-refusal-sibling",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}

	wfCtx := &testWorkflowContext{
		ctx: t.Context(),
		asyncResult: ToolOutput{
			Payload: rawjson.Message(`{"lookup":"ok"}`),
		},
		hookRuntime: rt,
	}

	initial := &PlanResult{
		ToolCalls: []ToolCall{
			{ToolCallID: "call-confirm", Name: confirm.Name, Payload: rawjson.Message(`{}`)},
			{ToolCallID: "call-denied", Name: denied.Name, Payload: rawjson.Message(`{}`)},
			{ToolCallID: "call-lookup", Name: lookup.Name, Payload: rawjson.Message(`{}`)},
		},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{
			Definition:          testRegistrationDefinition(testSubjectAgent(t, input.Subject), engine.WorkflowDefinition{}, nil),
			ExecuteToolActivity: "execute",
			ResumeActivityName:  "resume",
		},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	// Sibling svc.confirm pauses the run waiting for confirmation.
	require.NotNil(t, out.Suspension)
	require.NotEmpty(t, out.Suspension.Pending)
	require.NotNil(t, out.Suspension.Pending[0].Confirmation)
	assert.Equal(t, string(confirm.Name), string(out.Suspension.Pending[0].Confirmation.ToolName))
}

func TestRegisteredRootWorkflowPreConfirmationRefusal(t *testing.T) {
	t.Parallel()

	refusalFailure := &planner.ToolFailure{
		Kind:  planner.FailureDomainRejection,
		Error: planner.NewToolError("service tool refused by policy"),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryReplan,
		},
	}

	resolution, invocation := registeredRootFixture()
	resolution.Toolset.Tools[0].ConsumerContract.Confirmation = nil

	store := newTestStore()
	_, err := store.CreateSession(t.Context(), "session-root-refusal", time.Now().UTC())
	require.NoError(t, err)

	rt := New(store, WithEngine(engineinmem.New()), WithToolWorkflowQueue("service-worker"))
	require.NoError(t, rt.RegisterToolWorkflow(t.Context()))

	var observedCall *ToolCall
	rt.toolConfirmation = &ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{
			tools.Ident("records.find"): {
				Prompt: func(ctx context.Context, call *ToolCall) (string, error) {
					c := *call
					observedCall = &c
					return "", &refusalTestError{failure: refusalFailure}
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]string{"status": "denied"}, nil
				},
			},
		},
	}

	labels := map[string]string{"env": "test", "tenant": "123"}
	handle, err := rt.StartToolOperation(t.Context(), "session-root-refusal", "registry", resolution, invocation, labels, nil)
	require.NoError(t, err)

	output, err := handle.Wait(t.Context())
	require.Error(t, err)
	assert.Nil(t, output)

	var selectedErr *selectedOperationError
	require.ErrorAs(t, err, &selectedErr)
	assert.Equal(t, "registered tool failed: service tool refused by policy", selectedErr.Error())
	assert.Equal(t, "service tool refused by policy", selectedErr.cause.Message)

	// Verify metadata was populated on call passed to confirmation
	require.NotNil(t, observedCall)
	assert.Equal(t, "service-root", observedCall.RunID)
	assert.Equal(t, "session-root-refusal", observedCall.SessionID)
	assert.Equal(t, "test", observedCall.Labels["env"])
	assert.Equal(t, "123", observedCall.Labels["tenant"])

	// Verify the tool result recorded in the run log contains the refusal failure
	storedResults := storedToolResults(t, rt, "service-root")
	require.Len(t, storedResults, 1)
	assert.Equal(t, tools.Ident("records.find"), storedResults[0].ToolName)
	require.NotNil(t, storedResults[0].Failure)
	assert.Equal(t, planner.FailureDomainRejection, storedResults[0].Failure.Kind)
	assert.Equal(t, "service tool refused by policy", storedResults[0].Failure.Error.Message)
}
