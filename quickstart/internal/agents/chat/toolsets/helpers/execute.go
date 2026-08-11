// Package helpers executes the orchestrator.helpers toolset. This file is
// application-owned: it decodes tool payloads with the generated typed
// descriptor, performs the work, and returns typed results. Replace the
// deterministic answer with your real implementation (service call,
// retrieval, computation).
package helpers

import (
	"context"
	"errors"
	"fmt"

	genhelpers "example.com/quickstart/gen/orchestrator/toolsets/helpers"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
)

// Execute runs one helpers tool call. Payload decoding is a boundary: in a
// production system the arguments are model-authored, so decode failures
// return a classified invalid-call failure with structured correction
// guidance instead of an error.
func Execute(_ context.Context, _ *runtime.ToolCallMeta, call *planner.ToolRequest) (*runtime.ToolExecutionResult, error) {
	switch call.Name {
	case genhelpers.Answer:
		args, err := genhelpers.AnswerTool.Payload.FromJSON(call.Payload)
		if err != nil {
			return runtime.Executed(invalidCall(call, err)), nil
		}
		return runtime.Executed(&planner.ToolResult{
			Name:   call.Name,
			Result: &genhelpers.AnswerResult{Text: answerFor(args.Question)},
		}), nil
	}
	return runtime.Executed(&planner.ToolResult{
		Name: call.Name,
		Failure: &planner.ToolFailure{
			Kind:     planner.FailureInvalidCall,
			Error:    planner.NewToolError(fmt.Sprintf("unknown tool %s", call.Name)),
			Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan},
		},
	}), nil
}

// answerFor returns a deterministic demo answer so the quickstart runs
// without a model or external service.
func answerFor(question string) string {
	return fmt.Sprintf("Deterministic demo answer to: %s", question)
}

// invalidCall classifies a payload decode failure as a correctable
// invalid-call tool failure, carrying the generated codec's structured field
// issues plus the canonical example so the planner can repair the call.
func invalidCall(call *planner.ToolRequest, err error) *planner.ToolResult {
	var issuer interface{ Issues() []*tools.FieldIssue }
	var issues []*tools.FieldIssue
	if errors.As(err, &issuer) {
		issues = issuer.Issues()
	}
	return &planner.ToolResult{
		Name: call.Name,
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureInvalidCall,
			Error: planner.ToolErrorFromError(err),
			Recovery: planner.RecoveryDirective{
				Action:      planner.RecoveryCorrectCall,
				Issues:      issues,
				PriorInput:  append(rawjson.Message(nil), call.Payload...),
				ExampleJSON: append(rawjson.Message(nil), genhelpers.SpecAnswer.Payload.ExampleJSON...),
			},
		},
	}
}
