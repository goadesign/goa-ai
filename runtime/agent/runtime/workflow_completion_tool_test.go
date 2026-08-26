package runtime

// workflow_completion_tool_test.go verifies that run-scoped completion tools
// end workflows only after their side effect succeeds. Planner text and
// finalization turns cannot substitute for the required tool result.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCompletionToolSuccessEndsRunWithoutPlannerResume(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	resumes := 0
	h := newRecoveryHarness(
		t,
		"completion-success",
		[]tools.ToolSpec{completion},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			return finalPlannerResult("must not be used"), nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name: completion.Name, Payload: rawjson.Message(`{"title":"Recap"}`), ToolCallID: "persist-success",
	}}}, initialCaps(RunPolicy{MaxToolCalls: 3}))

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Nil(t, out.Final)
	assert.Len(t, out.ToolEvents, 1)
	assert.Equal(t, completion.Name, out.ToolEvents[0].Name)
	assert.Same(t, out.ToolEvents[0], out.FinalToolResult)
	assert.Zero(t, resumes)
}

func TestCompletionToolFailureCanBeCorrected(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	resumes := 0
	h := newRecoveryHarness(
		t,
		"completion-correction",
		[]tools.ToolSpec{completion},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if string(call.Payload) == `{"title":"corrected"}` {
				return successfulToolResult(call), nil
			}
			return invalidCallResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name: completion.Name, Payload: rawjson.Message(`{"title":"corrected"}`),
			}}}, nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name: completion.Name, Payload: rawjson.Message(`{}`), ToolCallID: "persist-invalid",
	}}}, initialCaps(RunPolicy{MaxToolCalls: 3, MaxRecoveryTurns: 2}))

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Nil(t, out.Final)
	assert.Len(t, out.ToolEvents, 2)
	assert.Same(t, out.ToolEvents[1], out.FinalToolResult)
	assert.Equal(t, 1, resumes)
}

func TestCompletionToolRejectsClarificationBeforeSuspension(t *testing.T) {
	completion := tools.Ident("briefs.persist")

	err := validateCompletionToolRecords([]stepToolRecord{{
		call:          ToolCall{Name: completion, ToolCallID: "persist-1"},
		result:        &planner.ToolResult{Name: completion, ToolCallID: "persist-1"},
		clarification: &ToolClarification{ID: "clarify-1", Question: "Which title?"},
	}}, completion)

	require.EqualError(t, err, `completion tool "briefs.persist" cannot request clarification`)
}

func TestCompletionToolRejectsWholeWorkflowRetries(t *testing.T) {
	policy := &PolicyOverrides{CompletionTool: "briefs.persist"}

	for _, retry := range []api.RetryPolicy{
		{MaxAttempts: 2},
		{InitialInterval: time.Second},
		{BackoffCoefficient: 2},
	} {
		err := validateCompletionToolWorkflowRetry(policy, &WorkflowOptions{RetryPolicy: retry})
		require.EqualError(t, err, "completion tool runs cannot configure whole-workflow retries")
	}
	require.NoError(t, validateCompletionToolWorkflowRetry(policy, nil))
	require.NoError(t, validateCompletionToolWorkflowRetry(policy, &WorkflowOptions{}))
}

func TestCompletionToolRejectsPlannerTerminalResponse(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	h := newRecoveryHarness(
		t,
		"completion-terminal-response",
		[]tools.ToolSpec{completion},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return nil, nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	out, err := h.run(&PlanResult{
		FinalResponse: finalPlannerResult("looks successful").FinalResponse,
	}, initialCaps(RunPolicy{MaxToolCalls: 3}))

	assert.Nil(t, out)
	require.Error(t, err)
	require.ErrorContains(t, err, `completion tool "briefs.persist" did not succeed`)
}

func TestCompletionToolCapExhaustionFailsWithoutFinalization(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	resumes := 0
	h := newRecoveryHarness(
		t,
		"completion-tool-cap",
		[]tools.ToolSpec{completion},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			return finalPlannerResult("must not finalize"), nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	caps := initialCaps(RunPolicy{MaxToolCalls: 1})
	caps.RemainingToolCalls = 0
	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name: completion.Name, Payload: rawjson.Message(`{}`), ToolCallID: "persist-cap",
	}}}, caps)

	assert.Nil(t, out)
	require.Error(t, err)
	require.ErrorContains(t, err, `completion tool "briefs.persist" did not succeed`)
	assert.Zero(t, resumes)
}

func TestCompletionToolRecoveryCapFailsWithoutFinalization(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	resumes := 0
	h := newRecoveryHarness(
		t,
		"completion-failure-cap",
		[]tools.ToolSpec{completion},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return invalidCallResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			return finalPlannerResult("must not finalize"), nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name: completion.Name, Payload: rawjson.Message(`{}`), ToolCallID: "persist-failure-cap",
	}}}, initialCaps(RunPolicy{MaxToolCalls: 2, MaxRecoveryTurns: 1}))

	assert.Nil(t, out)
	require.ErrorContains(t, err, `completion tool "briefs.persist" did not succeed`)
	assert.Equal(t, 1, resumes)
}

func TestCompletionToolMustBeOnlyActionInPlannerResponse(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	other := newAnyJSONSpec("briefs.lookup", "catalog")
	h := newRecoveryHarness(
		t,
		"completion-mixed-batch",
		[]tools.ToolSpec{completion, other},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return nil, nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{Name: completion.Name, Payload: rawjson.Message(`{}`), ToolCallID: "persist-mixed"},
		{Name: other.Name, Payload: rawjson.Message(`{}`), ToolCallID: "lookup-mixed"},
	}}, initialCaps(RunPolicy{MaxToolCalls: 3}))

	assert.Nil(t, out)
	require.Error(t, err)
	require.ErrorContains(t, err, `completion tool "briefs.persist" must be the only action`)
}

func TestCompletionToolCannotAccompanyPlannerAwait(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	executions := 0
	h := newRecoveryHarness(
		t,
		"completion-await",
		[]tools.ToolSpec{completion},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			return successfulToolResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return nil, nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			Name: completion.Name, Payload: rawjson.Message(`{}`), ToolCallID: "persist-await",
		}},
		Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID: "clarify-brief", Question: "Which details should the Brief include?",
		})),
	}, initialCaps(RunPolicy{MaxToolCalls: 3}))

	assert.Nil(t, out)
	require.ErrorContains(t, err, `completion tool "briefs.persist" must be the only action`)
	assert.Zero(t, executions)
}

func TestCompletionToolCannotRequestPostToolSynthesis(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	lookup := newAnyJSONSpec("briefs.lookup", "catalog")
	executions := 0
	h := newRecoveryHarness(
		t,
		"completion-synthesis",
		[]tools.ToolSpec{completion, lookup},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			return successfulToolResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return nil, nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			Name: lookup.Name, Payload: rawjson.Message(`{}`), ToolCallID: "lookup-synthesis",
		}},
		SynthesizeAfterTools: true,
	}, initialCaps(RunPolicy{MaxToolCalls: 3}))

	assert.Nil(t, out)
	require.ErrorContains(t, err, `completion tool "briefs.persist" did not succeed`)
	require.ErrorContains(t, err, "planner requested post-tool synthesis")
	assert.Zero(t, executions)
}

func TestCompletionToolCannotBeDelegatedToAwaitWork(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	h := newRecoveryHarness(
		t,
		"completion-external-await",
		[]tools.ToolSpec{completion},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return nil, nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	out, err := h.run(&PlanResult{
		Await: planner.NewAwait(planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
			ID: "external-completion",
			Items: []planner.AwaitToolItem{{
				Name:            completion.Name,
				ModelToolCallID: "completion-call",
				Payload:         rawjson.Message(`{}`),
			}},
		})),
	}, initialCaps(RunPolicy{MaxToolCalls: 3}))

	assert.Nil(t, out)
	require.ErrorContains(t, err, `completion tool "briefs.persist" did not succeed`)
	require.ErrorContains(t, err, "delegated its execution to await work")
}

func TestCompletionToolRejectsAnotherTerminalTool(t *testing.T) {
	completion := newAnyJSONSpec("briefs.persist", "catalog")
	terminal := newAnyJSONSpec("tasks.complete", "catalog")
	terminal.Bookkeeping = true
	terminal.TerminalRun = true
	executions := 0
	h := newRecoveryHarness(
		t,
		"completion-other-terminal",
		[]tools.ToolSpec{completion, terminal},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			return successfulToolResult(call), nil
		},
		func(_ context.Context, _ *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return nil, nil
		},
	)
	h.input.Policy = &PolicyOverrides{CompletionTool: completion.Name}

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name: terminal.Name, Payload: rawjson.Message(`{}`), ToolCallID: "terminal-other",
	}}}, initialCaps(RunPolicy{MaxToolCalls: 3}))

	assert.Nil(t, out)
	require.ErrorContains(t, err, `completion tool "briefs.persist" did not succeed`)
	require.ErrorContains(t, err, `planner selected terminal tool "tasks.complete"`)
	assert.Zero(t, executions)
}

func TestCompletionToolPolicyRejectsUnexecutableTools(t *testing.T) {
	persist := newAnyJSONSpec("briefs.persist", "briefs")
	lookup := newAnyJSONSpec("briefs.lookup", "briefs")
	audit := newAnyJSONSpec("briefs.audit", "briefs")
	audit.Bookkeeping = true
	terminal := newAnyJSONSpec("briefs.publish", "briefs")
	terminal.Bookkeeping = true
	terminal.TerminalRun = true
	confirmed := newAnyJSONSpec("briefs.confirmed", "briefs")
	confirmed.Confirmation = &tools.ConfirmationSpec{}
	foreign := newAnyJSONSpec("foreign.persist", "foreign")

	rt := New()
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "briefs",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		}),
		Specs: []tools.ToolSpec{persist, lookup, audit, terminal, confirmed},
	}))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "foreign",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		}),
		Specs: []tools.ToolSpec{foreign},
	}))
	reg := AgentRegistration{
		ID:    "briefs.writer",
		Specs: []tools.ToolSpec{persist, lookup, audit, terminal, confirmed},
	}

	tests := []struct {
		name   string
		policy *PolicyOverrides
		want   string
	}{
		{
			name:   "unregistered",
			policy: &PolicyOverrides{CompletionTool: "briefs.missing"},
			want:   `completion tool "briefs.missing" is not registered for agent "briefs.writer"`,
		},
		{
			name:   "registered for another agent",
			policy: &PolicyOverrides{CompletionTool: foreign.Name},
			want:   `completion tool "foreign.persist" is not registered for agent "briefs.writer"`,
		},
		{
			name:   "bookkeeping",
			policy: &PolicyOverrides{CompletionTool: audit.Name},
			want:   `completion tool "briefs.audit" must be budgeted`,
		},
		{
			name:   "terminal",
			policy: &PolicyOverrides{CompletionTool: terminal.Name},
			want:   `completion tool "briefs.publish" must not be a terminal tool`,
		},
		{
			name:   "confirmation required",
			policy: &PolicyOverrides{CompletionTool: confirmed.Name},
			want:   `completion tool "briefs.confirmed" cannot require confirmation`,
		},
		{
			name: "excluded by restriction",
			policy: &PolicyOverrides{
				CompletionTool: persist.Name,
				RestrictToTool: lookup.Name,
			},
			want: `completion tool "briefs.persist" is excluded by the run tool policy`,
		},
		{
			name: "conflicting terminal policies",
			policy: &PolicyOverrides{
				CompletionTool:     persist.Name,
				LimitTerminalPlans: &LimitTerminalPlans{},
			},
			want: "completion tool and limit terminal plans cannot be combined",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rt.validateCompletionToolPolicy(reg, tt.policy)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestWithRunCompletionToolSetsSerializedPolicy(t *testing.T) {
	input := &RunInput{}

	WithRunCompletionTool("briefs.persist")(input)
	payload, err := json.Marshal(input.Policy)
	require.NoError(t, err)

	var restored PolicyOverrides
	require.NoError(t, json.Unmarshal(payload, &restored))
	assert.Equal(t, tools.Ident("briefs.persist"), restored.CompletionTool)
}

func TestCompletionToolIsRequiredBySuspendedRun(t *testing.T) {
	checkpoint := &workflowCheckpoint{
		Policy: &PolicyOverrides{CompletionTool: "briefs.persist"},
	}

	assert.Equal(t, []tools.Ident{"briefs.persist"}, requiredCheckpointToolNames(checkpoint))
}
