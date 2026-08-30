package runtime

// continuation_contract_test.go verifies the externally supplied union shapes
// before a workflow can decode or apply saved state.

import (
	"testing"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidatePendingInputResponseRequiresOneVariant(t *testing.T) {
	tests := []struct {
		name     string
		response *api.PendingInputResponse
		wantErr  bool
	}{
		{name: "clarification", response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{}}},
		{name: "confirmation", response: &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{}}},
		{name: "tool results", response: &api.PendingInputResponse{ToolResults: &api.ToolResultsSet{}}},
		{name: "empty", response: &api.PendingInputResponse{}, wantErr: true},
		{name: "multiple", response: &api.PendingInputResponse{
			Clarification: &api.ClarificationAnswer{}, Confirmation: &api.ConfirmationDecision{},
		}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePendingInputResponse(test.response)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidatePendingInputResponseForRequiresMatchingFirstRequest(t *testing.T) {
	clarification := planner.AwaitClarificationItem(&planner.AwaitClarification{
		ID:       "clarification-1",
		Question: "Which facility?",
	})
	questions := planner.AwaitQuestionsItem(&planner.AwaitQuestions{
		ID:              "questions-1",
		ToolName:        "svc.questions",
		ModelToolCallID: "model-call-1",
		Questions: []planner.AwaitQuestion{{
			ID:     "question-1",
			Prompt: "Choose one",
		}},
	})
	tests := []struct {
		name     string
		pending  *api.PendingInput
		response *api.PendingInputResponse
		wantErr  string
	}{
		{
			name: "clarification",
			pending: &api.PendingInput{
				Kind:  api.PendingInputKindClarification,
				Await: &clarification,
			},
			response: &api.PendingInputResponse{
				Clarification: &api.ClarificationAnswer{ID: "clarification-1"},
			},
		},
		{
			name: "confirmation",
			pending: &api.PendingInput{
				Kind: api.PendingInputKindConfirmation,
				Confirmation: &api.PendingConfirmation{
					ID:         "confirmation-1",
					ToolName:   "svc.update",
					ToolCallID: "tool-call-1",
				},
			},
			response: &api.PendingInputResponse{
				Confirmation: &api.ConfirmationDecision{ID: "confirmation-1"},
			},
		},
		{
			name: "tool results",
			pending: &api.PendingInput{
				Kind:  api.PendingInputKindToolResults,
				Await: &questions,
			},
			response: &api.PendingInputResponse{
				ToolResults: &api.ToolResultsSet{ID: "questions-1"},
			},
		},
		{
			name: "wrong kind",
			pending: &api.PendingInput{
				Kind:  api.PendingInputKindClarification,
				Await: &clarification,
			},
			response: &api.PendingInputResponse{
				Confirmation: &api.ConfirmationDecision{ID: "clarification-1"},
			},
			wantErr: "requires a clarification response",
		},
		{
			name: "wrong id",
			pending: &api.PendingInput{
				Kind:  api.PendingInputKindClarification,
				Await: &clarification,
			},
			response: &api.PendingInputResponse{
				Clarification: &api.ClarificationAnswer{ID: "clarification-2"},
			},
			wantErr: "does not match pending id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePendingInputResponseFor(test.pending, test.response)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateWorkflowRunInputRejectsMixedContinuationState(t *testing.T) {
	response := &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{}}
	require.NoError(t, validateWorkflowRunInput(&RunInput{
		AgentID: "svc.agent", RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
		Continuation: &api.RunContinuationInput{Response: response},
	}))

	err := validateWorkflowRunInput(&RunInput{
		AgentID: "svc.agent", RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
		Messages:     []*model.Message{{Role: model.ConversationRoleUser}},
		Continuation: &api.RunContinuationInput{Response: response},
	})
	require.ErrorContains(t, err, "caller-supplied checkpoint state")
}

func TestValidateWorkflowRunInputRejectsNegativeRecoveryOverride(t *testing.T) {
	err := validateWorkflowRunInput(&RunInput{
		Policy: &PolicyOverrides{MaxRecoveryTurns: -1},
	})

	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestValidateWorkflowOutputEnforcesIdentityAndTerminalShape(t *testing.T) {
	valid := func() *RunOutput {
		await := planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID: "clarification-1", Question: "Which facility?",
		})
		return &RunOutput{
			AgentID: "child.agent",
			RunID:   "child-run-1",
			Suspension: &api.RunSuspension{
				ID:         "suspension-1",
				Version:    "v1",
				Checkpoint: rawjson.Message(`{}`),
				Pending: []*api.PendingInput{{
					Kind: api.PendingInputKindClarification, Await: &await,
				}},
				RequiredTools: []tools.Ident{"child.lookup"},
			},
		}
	}

	require.NoError(t, validateWorkflowOutput(valid(), agent.Ident("child.agent"), "child-run-1"))

	wrongAgent := valid()
	wrongAgent.AgentID = "other.agent"
	require.ErrorContains(t, validateWorkflowOutput(wrongAgent, agent.Ident("child.agent"), "child-run-1"), "agent mismatch")

	wrongRun := valid()
	wrongRun.RunID = "other-run"
	require.ErrorContains(t, validateWorkflowOutput(wrongRun, agent.Ident("child.agent"), "child-run-1"), "run mismatch")

	mixed := valid()
	mixed.Final = &model.Message{Role: model.ConversationRoleAssistant}
	require.ErrorContains(t, validateWorkflowOutput(mixed, agent.Ident("child.agent"), "child-run-1"), "exactly one terminal result")

	empty := &RunOutput{AgentID: "child.agent", RunID: "child-run-1"}
	require.ErrorContains(t, validateWorkflowOutput(empty, agent.Ident("child.agent"), "child-run-1"), "exactly one terminal result")

	invalidPending := valid()
	invalidPending.Suspension.Pending[0].Await = nil
	require.ErrorContains(t, validateWorkflowOutput(invalidPending, agent.Ident("child.agent"), "child-run-1"), "invalid payload")
}

func TestValidateContinuationIdentityRequiresNewRunAndTurn(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	checkpoint, err := decodeWorkflowCheckpoint(suspensionContractFixture(t, spec.Name), testRuntimeDefinition(runtime, "svc.agent"))
	require.NoError(t, err)

	err = validateContinuationIdentity(&RunInput{
		AgentID: "svc.agent", SessionID: "session-1", RunID: "run-1", TurnID: "turn-2",
	}, checkpoint)
	require.ErrorContains(t, err, "new run id")

	err = validateContinuationIdentity(&RunInput{
		AgentID: "svc.agent", SessionID: "session-1", RunID: "run-2", TurnID: "turn-1",
	}, checkpoint)
	require.ErrorContains(t, err, "new turn id")

	require.NoError(t, validateContinuationIdentity(&RunInput{
		AgentID: "svc.agent", SessionID: "session-1", RunID: "run-2", TurnID: "turn-2",
	}, checkpoint))
}
