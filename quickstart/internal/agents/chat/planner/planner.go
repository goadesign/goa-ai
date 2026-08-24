// Package planner contains the example planner for ChatAgent.
// Goa creates this file only when it does not already exist. The application
// owns all later edits.
package planner

import (
	"context"
	"fmt"

	gentool "example.com/quickstart/gen/orchestrator/toolsets/helpers"
	model "goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

// New returns the example planner registered by the generated bootstrap.
// Replace it with the planner that should answer real application requests.
func New() planner.Planner { return &examplePlanner{} }

// examplePlanner makes one sample tool call and turns its result into an
// assistant reply. It lets a new application run without model credentials.
type examplePlanner struct{}

// PlanStart requests the example tool call selected from the design.
func (*examplePlanner) PlanStart(_ context.Context, _ *planner.PlanInput) (*planner.PlanResult, error) {
	args, err := gentool.AnswerPayloadCodec().FromJSON(gentool.SpecAnswer().Payload.ExampleJSON)
	if err != nil {
		return nil, fmt.Errorf("decode generated helpers.answer example: %w", err)
	}
	call, err := planner.NewToolRequest(gentool.AnswerTool(), args)
	if err != nil {
		return nil, fmt.Errorf("build generated helpers.answer call: %w", err)
	}
	return &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			call,
		},
	}, nil
}

// PlanResume turns the example tool result into the assistant's final reply.
func (*examplePlanner) PlanResume(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
	if len(in.ToolOutputs) != 1 {
		return nil, fmt.Errorf("expected one helpers.answer result, got %d", len(in.ToolOutputs))
	}
	output := in.ToolOutputs[0]
	if output.Name != gentool.Answer {
		return nil, fmt.Errorf("unexpected tool result %s (%s)", output.Name, output.ToolCallID)
	}
	if output.Failure != nil {
		if output.Failure.Error.Cause != nil {
			return nil, fmt.Errorf(
				"helpers.answer failed: %s: %s",
				output.Failure.Error.Message,
				output.Failure.Error.Cause.Message,
			)
		}
		return nil, fmt.Errorf("helpers.answer failed: %s", output.Failure.Error.Message)
	}
	answer := fmt.Sprintf("Tool %s returned %s", output.Name, output.Result)
	return &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: answer}},
			},
		},
	}, nil
}
