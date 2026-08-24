// Package helpers contains the helpers executor for ChatAgent.
// Goa creates this file only when it does not already exist. The application
// owns all later edits.
package helpers

import (
	"context"
	"errors"
	"fmt"

	helpersspecs "example.com/quickstart/gen/orchestrator/toolsets/helpers"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
)

// Execute checks one tool call against its generated argument contract. The
// initial implementation returns the result example from the design;
// applications replace that result with their service call.
func Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
	if call == nil {
		return nil, errors.New("tool request is nil")
	}
	if meta == nil {
		return nil, errors.New("tool call meta is nil")
	}
	switch call.Name {
	case "helpers.answer":
		// Decode the JSON arguments with the generated helpers.answer contract.
		_, err := helpersspecs.AnswerTool().Payload.FromJSON(call.Payload)
		if err != nil {
			var issuer interface {
				Issues() []*tools.FieldIssue
			}
			var issues []*tools.FieldIssue
			if errors.As(err, &issuer) {
				issues = issuer.Issues()
			}
			return runtime.Executed(&planner.ToolResult{
				Name: call.Name,
				Failure: &planner.ToolFailure{
					Kind:  planner.FailureInvalidCall,
					Error: planner.ToolErrorFromError(err),
					Recovery: planner.RecoveryDirective{
						Action:     planner.RecoveryCorrectCall,
						Issues:     issues,
						PriorInput: append(rawjson.Message(nil), call.Payload...),
						ExampleJSON: append(
							rawjson.Message(nil),
							helpersspecs.SpecAnswer().Payload.ExampleJSON...,
						),
					},
				},
			}), nil
		}
		result, err := helpersspecs.AnswerTool().Result.FromJSON(
			rawjson.Message("{\"text\":\"Tokyo is the capital of Japan.\"}"),
		)
		if err != nil {
			return nil, fmt.Errorf("decode helpers.answer example result: %w", err)
		}
		return runtime.Executed(&planner.ToolResult{
			Name:   call.Name,
			Result: result,
		}), nil
	default:
		return runtime.Executed(&planner.ToolResult{
			Name: call.Name,
			Failure: &planner.ToolFailure{
				Kind:     planner.FailureInvalidCall,
				Error:    planner.NewToolError("unknown tool"),
				Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan},
			},
		}), nil
	}
}
