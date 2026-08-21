// Package planner implements the chat agent's brain. This file was
// scaffolded by goa example and is application-owned: it demonstrates the
// full plan -> tool -> resume loop deterministically. PlanStart requests the
// helpers.answer tool with the user's question; PlanResume decodes the tool
// result with the generated typed descriptor and finalizes with the answer.
// Replace the deterministic decisions with LLM calls (via
// in.Agent.PlannerModelClient) to make the agent smart.
package planner

import (
	"context"
	"errors"
	"fmt"

	genhelpers "example.com/quickstart/gen/orchestrator/toolsets/helpers"
	model "goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

// chatPlanner is a deterministic planner.Planner: one tool round-trip, then a
// final answer. Production planners make the same two decisions with an LLM.
type chatPlanner struct{}

// New returns the chat agent's planner.
func New() planner.Planner { return &chatPlanner{} }

// PlanStart asks the runtime to execute helpers.answer with the user's
// question. The generated typed descriptor (genhelpers.AnswerTool) encodes
// the payload, so a design change that renames or retypes a field breaks
// this planner at compile time instead of at runtime.
func (p *chatPlanner) PlanStart(_ context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
	question := lastUserText(in.Messages)
	if question == "" {
		return nil, errors.New("no user question in conversation")
	}
	payload, err := genhelpers.AnswerTool.Payload.ToJSON(&genhelpers.AnswerPayload{Question: question})
	if err != nil {
		return nil, fmt.Errorf("encode %s payload: %w", genhelpers.Answer, err)
	}
	return &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{Name: genhelpers.Answer, Payload: payload}},
	}, nil
}

// PlanLimitFinalization asks Goa-AI to load saved messages before PlanResume
// because the final answer summarizes an earlier answer-tool result.
func (p *chatPlanner) PlanLimitFinalization(
	context.Context,
	*planner.LimitFinalizationInput,
) (planner.LimitFinalizationDecision, error) {
	return planner.HistoryRequiredLimitFinalization(), nil
}

// PlanResume decodes the helpers.answer result from the executed tool history
// and finalizes the run with the answer text.
func (p *chatPlanner) PlanResume(_ context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
	for i := len(in.ToolOutputs) - 1; i >= 0; i-- {
		out := in.ToolOutputs[i]
		if out.Name != genhelpers.Answer || len(out.Result) == 0 {
			continue
		}
		answer, err := genhelpers.AnswerTool.Result.FromJSON(out.Result)
		if err != nil {
			return nil, fmt.Errorf("decode %s result: %w", genhelpers.Answer, err)
		}
		return &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: answer.Text}},
				},
			},
		}, nil
	}
	return nil, fmt.Errorf("no successful %s result in tool history", genhelpers.Answer)
}

// lastUserText returns the text of the most recent user message.
func lastUserText(messages []*model.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != model.ConversationRoleUser {
			continue
		}
		for _, part := range messages[i].Parts {
			if text, ok := part.(model.TextPart); ok {
				return text.Text
			}
		}
	}
	return ""
}
