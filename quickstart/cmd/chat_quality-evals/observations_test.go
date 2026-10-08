package main

import (
	"errors"
	"testing"

	genevalchatquality "example.com/quickstart/gen/evals/chat_quality"
	genhelpers "example.com/quickstart/gen/orchestrator/toolsets/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval/evidence"
	"goa.design/goa-ai/runtime/agent/planner"
	agentrun "goa.design/goa-ai/runtime/agent/run"
)

func TestSavedEvidencePreservesExactPredicatesAndSuccessfulRetry(t *testing.T) {
	const question = "What is the capital of Japan?"
	tool := genhelpers.AnswerTool()
	arguments, err := tool.Payload.ToJSON(&genhelpers.AnswerPayload{Question: question})
	require.NoError(t, err)
	result, err := tool.Result.ToJSON(&genhelpers.AnswerResult{Text: "Tokyo."})
	require.NoError(t, err)
	collected := &evidence.Evidence{
		Answer: "Tokyo.", TerminalPhase: agentrun.PhaseCompleted,
		ToolCalls: []evidence.ToolCall{
			{
				Name: tool.Name, ToolCallID: "failed-attempt", Args: arguments, Completed: true,
				Failure: &planner.ToolFailure{
					Kind: planner.FailureUnavailable, Error: planner.NewToolError("Temporarily unavailable."),
					Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan},
				},
			},
			{Name: tool.Name, ToolCallID: "successful-attempt", Args: arguments, Result: result, Completed: true},
		},
	}
	for _, test := range []struct {
		name       string
		change     func(*genevalchatquality.GreetingObservation)
		diagnostic string
	}{
		{"success after retry", func(*genevalchatquality.GreetingObservation) {}, ""},
		{"incomplete call", func(value *genevalchatquality.GreetingObservation) { value.ToolCalls[1].Completed = false }, "trajectory"},
		{"wrong question", func(value *genevalchatquality.GreetingObservation) { value.Question = "A different request." }, "question"},
		{"run failed", func(value *genevalchatquality.GreetingObservation) {
			value.TerminalPhase = string(agentrun.PhaseFailed)
		}, "terminal"},
		{"no calls", func(value *genevalchatquality.GreetingObservation) { value.ToolCalls = nil }, "trajectory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := captureObservation(question, collected, nil)
			test.change(observed)
			encoded, err := genevalchatquality.EncodeGreetingObservation(observed)
			require.NoError(t, err)
			decoded, err := genevalchatquality.DecodeGreetingObservation(encoded)
			require.NoError(t, err)
			diagnostic := (checks{}).CheckGreetingReplyRunTools(decoded)
			if test.diagnostic == "" {
				assert.Empty(t, diagnostic)
			} else {
				assert.Contains(t, diagnostic, test.diagnostic)
			}
		})
	}
	failed := captureObservation(question, collected, errors.New("product call failed"))
	assert.Contains(t, (checks{}).CheckGreetingReplyRunTools(failed), "product call failed")
}
