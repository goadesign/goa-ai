// This file captures the facts used by the example's exact checks and converts
// saved facts back into evidence.Expect inputs. It never calls the product or
// reads live reference data during assessment.
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	genevalchatquality "example.com/quickstart/gen/evals/chat_quality"
	genhelpers "example.com/quickstart/gen/orchestrator/toolsets/helpers"
	"goa.design/goa-ai/eval/evidence"
	"goa.design/goa-ai/runtime/agent/planner"
	agentrun "goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// checks depends only on the generated observation and tool contracts.
	checks struct{}
)

// CheckGreetingReplyRunTools assesses the recorded tool arguments, results, and
// completion phase with the existing typed expectation implementation.
func (checks) CheckGreetingReplyRunTools(observed *genevalchatquality.GreetingObservation) string {
	saved := &evidence.Evidence{Answer: observed.Answer, TerminalPhase: agentrun.Phase(observed.TerminalPhase)}
	if observed.TerminalFailure != "" {
		saved.TerminalFailure = &agentrun.Failure{Message: observed.TerminalFailure}
	}
	for _, call := range observed.ToolCalls {
		recorded := evidence.ToolCall{
			Name: tools.Ident(call.Name), ToolCallID: call.CallID, ParentToolCallID: call.ParentCallID,
			Args: call.Arguments, Result: call.Result, Completed: call.Completed,
		}
		if call.Failure != nil {
			recorded.Failure = &planner.ToolFailure{
				Kind: planner.FailureKind(call.Failure.Kind), Error: planner.NewToolError(call.Failure.Message),
				Recovery: planner.RecoveryDirective{Action: planner.RecoveryAction(call.Failure.RecoveryAction)},
			}
		}
		saved.ToolCalls = append(saved.ToolCalls, recorded)
	}
	expect := evidence.Expect{Tools: []evidence.Tool{
		evidence.ExpectCall(genhelpers.AnswerTool(),
			func(payload *genhelpers.AnswerPayload) error {
				if payload.Question != observed.Question {
					return fmt.Errorf("question: got %q, want %q", payload.Question, observed.Question)
				}
				return nil
			},
			func(result *genhelpers.AnswerResult) error {
				if result.Text == "" {
					return errors.New("answer text must not be empty")
				}
				return nil
			},
		),
	}}
	var diagnostics []string
	if observed.RunError != "" {
		diagnostics = append(diagnostics, "product run: "+observed.RunError)
	}
	for _, check := range expect.Checks(saved) {
		if !check.Passed {
			diagnostics = append(diagnostics, check.Name+": "+check.Diagnostic)
		}
	}
	return strings.Join(diagnostics, "; ")
}

// CheckHelpersContractPayloadSchema fails when the captured contract lacked a
// payload schema. It does not reload the current tool contract.
func (checks) CheckHelpersContractPayloadSchema(observed genevalchatquality.HelpersContractObservation) string {
	if !observed {
		return "helpers.answer contract has no payload schema"
	}
	return ""
}

// captureObservation copies the collector's ordered calls and terminal facts.
// It records failed and incomplete calls without turning them into assertions.
func captureObservation(question string, collected *evidence.Evidence, productErr error) *genevalchatquality.GreetingObservation {
	observed := &genevalchatquality.GreetingObservation{
		Question: question, Answer: collected.Answer, TerminalPhase: string(collected.TerminalPhase),
	}
	if collected.TerminalFailure != nil {
		observed.TerminalFailure = collected.TerminalFailure.Message
	}
	if productErr != nil {
		observed.RunError = productErr.Error()
	}
	for _, call := range collected.ToolCalls {
		recorded := &genevalchatquality.CapturedToolCall{
			Name: string(call.Name), CallID: call.ToolCallID, ParentCallID: call.ParentToolCallID,
			Arguments: slices.Clone(call.Args), Result: slices.Clone(call.Result), Completed: call.Completed,
		}
		if call.Failure != nil {
			recorded.Failure = &genevalchatquality.CapturedToolFailure{
				Kind: string(call.Failure.Kind), Message: call.Failure.Error.Error(),
				RecoveryAction: string(call.Failure.Recovery.Action),
			}
		}
		observed.ToolCalls = append(observed.ToolCalls, recorded)
	}
	return observed
}
