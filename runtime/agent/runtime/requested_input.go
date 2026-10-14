// Package runtime reads public run input from the first request in the exact
// saved suspension. It derives private request identifiers and validates answers
// without starting work; the execution owner accepts and starts the successor.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	genhostinput "goa.design/goa-ai/registry/gen/hostinput"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genruninput "goa.design/goa-ai/registry/gen/runinput"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	requestedInputState struct {
		pending    *api.PendingInput
		checkpoint *workflowCheckpoint
	}
)

// GetRequestedInput loads the specified suspended run and returns its first
// saved question. Callers authorize that run before calling this method; the
// question never includes private service state or provider transcript IDs.
func (r *Runtime) GetRequestedInput(ctx context.Context, currentRunID string) (result *genruninput.RequestedInput, operationErr error) {
	ctx, span := r.tracer.Start(ctx, "runtime.get_requested_input")
	span.SetAttributes(attribute.String("run.id", currentRunID))
	defer func() {
		if operationErr != nil {
			span.RecordError(operationErr)
			span.SetStatus(codes.Error, "requested input read failed")
		}
		span.End()
	}()
	state, err := r.loadRequestedInput(ctx, currentRunID)
	if err != nil {
		return nil, err
	}
	result, err = projectRequestedInput(state)
	if err != nil {
		return nil, fmt.Errorf("%w: requested input: %w", ErrRunSuspensionCorrupt, err)
	}
	return result, nil
}

// PrepareInputResponse validates an answer to the specified suspended run and
// derives the saved request identifiers. This method has no execution effects;
// the caller must durably accept the successor through its execution owner.
func (r *Runtime) PrepareInputResponse(ctx context.Context, currentRunID string, answer *genruninput.InputResponse) (response *api.PendingInputResponse, operationErr error) {
	ctx, span := r.tracer.Start(ctx, "runtime.prepare_input_response")
	span.SetAttributes(attribute.String("run.id", currentRunID))
	defer func() {
		if operationErr != nil {
			span.RecordError(operationErr)
			span.SetStatus(codes.Error, "input response validation failed")
		}
		span.End()
	}()
	if _, err := genruninput.EncodeInputResponse(answer); err != nil {
		return nil, continuationContractError(err)
	}
	state, err := r.loadRequestedInput(ctx, currentRunID)
	if err != nil {
		return nil, err
	}
	response, err = prepareRequestedInputResponse(state, answer)
	if err != nil {
		if errors.Is(err, ErrRunSuspensionCorrupt) {
			return nil, err
		}
		return nil, continuationContractError(err)
	}
	return response, nil
}

// loadRequestedInput uses the storage owner's run checks and each checkpoint
// owner's decoder before following the first saved child. It never selects a
// later request or consults a provider's newer registration.
func (r *Runtime) loadRequestedInput(ctx context.Context, runID string) (*requestedInputState, error) {
	suspension, err := r.LoadRunSuspension(ctx, runID)
	if err != nil {
		return nil, err
	}
	if suspension.Version == registeredRootCheckpointVersion {
		if _, err := decodeRegisteredRootCheckpoint(suspension); err != nil {
			return nil, fmt.Errorf("%w: requested input: %w", ErrRunSuspensionCorrupt, err)
		}
		return &requestedInputState{pending: suspension.Pending[0]}, nil
	}
	checkpoint, err := decodeProviderCheckpoint(suspension)
	if err != nil {
		return nil, fmt.Errorf("%w: requested input: %w", ErrRunSuspensionCorrupt, err)
	}
	for checkpoint.Pending[0].Child != nil {
		child := checkpoint.Pending[0].Child
		checkpoint, err = decodeProviderCheckpoint(child.Suspension)
		if err != nil {
			return nil, fmt.Errorf("%w: requested input: %w", ErrRunSuspensionCorrupt, err)
		}
	}
	pending, err := publicPendingInputs(checkpoint.Pending[:1])
	if err != nil {
		return nil, fmt.Errorf("%w: requested input: %w", ErrRunSuspensionCorrupt, err)
	}
	return &requestedInputState{pending: pending[0], checkpoint: checkpoint}, nil
}

// projectRequestedInput copies only the data required to answer the saved
// request. Structured answers retain the schema from that exact tool invocation.
func projectRequestedInput(state *requestedInputState) (*genruninput.RequestedInput, error) {
	pending := state.pending
	var request genruninput.RunInputRequest
	switch pending.Kind {
	case api.PendingInputKindMCP:
		questions := make(map[string]*genhostinput.HostInputRequest, len(pending.MCP.Requests))
		for id, original := range pending.MCP.Requests {
			question, err := original.PublicInput()
			if err != nil {
				return nil, fmt.Errorf("host question %q: %w", id, err)
			}
			questions[id] = question
		}
		request = genruninput.NewRunInputRequestMcp(questions)
	case api.PendingInputKindConfirmation:
		confirmation := pending.Confirmation
		request = genruninput.NewRunInputRequestConfirmation(&genruninput.InputConfirmation{
			Title: &confirmation.Title, Prompt: confirmation.Prompt,
			ToolName: string(confirmation.ToolName), Payload: slices.Clone(confirmation.Payload),
		})
	case api.PendingInputKindClarification:
		clarification := &genruninput.InputClarification{}
		if pending.Await.Kind == planner.AwaitItemKindToolClarification {
			clarification.Question = pending.Await.ToolClarification.Question
		} else {
			original := pending.Await.Clarification
			clarification.Question = original.Question
			clarification.MissingFields = slices.Clone(original.MissingFields)
			clarification.ExampleJSON = slices.Clone(original.ExampleJSON)
		}
		request = genruninput.NewRunInputRequestClarification(clarification)
	case api.PendingInputKindToolResults:
		if pending.Await.Kind == planner.AwaitItemKindQuestions {
			original := pending.Await.Questions
			contract, err := requestedResultContract(state, original.ToolName)
			if err != nil {
				return nil, err
			}
			questions := &genruninput.InputQuestions{Title: original.Title, ResultContract: contract}
			for i := range original.Questions {
				item := &original.Questions[i]
				question := &genruninput.InputQuestion{ID: item.ID, Prompt: item.Prompt, AllowMultiple: item.AllowMultiple}
				for j := range item.Options {
					option := &genruninput.InputQuestionOption{}
					option.CreateFromAwaitQuestionOption(&item.Options[j])
					question.Options = append(question.Options, option)
				}
				questions.Questions = append(questions.Questions, question)
			}
			request = genruninput.NewRunInputRequestQuestions(questions)
		} else {
			calls := make(map[string]*genruninput.InputToolCall, len(pending.Await.ExternalTools.Items))
			for _, call := range pending.Await.ExternalTools.Items {
				contract, err := requestedResultContract(state, call.Name)
				if err != nil {
					return nil, err
				}
				calls[call.ToolCallID] = &genruninput.InputToolCall{Name: string(call.Name), Payload: slices.Clone(call.Payload), ResultContract: contract}
			}
			request = genruninput.NewRunInputRequestExternalTools(calls)
		}
	default:
		return nil, fmt.Errorf("unsupported saved input kind %q", pending.Kind)
	}
	return &genruninput.RequestedInput{Request: request}, nil
}

// requestedResultContract reads the schema retained with a suspended question,
// including its original pagination declaration. A missing saved contract is an
// error, never permission to use a newer provider definition.
func requestedResultContract(state *requestedInputState, name tools.Ident) (*genruninput.InputResultContract, error) {
	original, ok := state.checkpoint.Pending[0].ResponseContracts[name]
	if !ok {
		return nil, fmt.Errorf("saved input has no result contract for %q", name)
	}
	result := &genruninput.InputResultContract{Schema: slices.Clone(original.ResultSchema)}
	if original.Bounds != nil {
		result.Bounds = &genregistry.ToolBounds{}
		if paging := original.Bounds.Paging; paging != nil {
			continueTool, sourceTool := string(paging.ContinueTool), string(paging.SourceTool)
			retained := make([]string, len(paging.RetainedFields))
			copy(retained, paging.RetainedFields)
			result.Bounds.Paging = &genregistry.ToolPaging{RetainedFields: retained, CursorField: paging.CursorField, NextCursorField: paging.NextCursorField}
			if continueTool != "" {
				result.Bounds.Paging.ContinueTool = &continueTool
			}
			if sourceTool != "" {
				result.Bounds.Paging.SourceTool = &sourceTool
			}
		}
	}
	return result, nil
}

// prepareRequestedInputResponse derives single-request IDs and preserves the
// original order of independent external calls before the runtime validates them.
func prepareRequestedInputResponse(state *requestedInputState, answer *genruninput.InputResponse) (*api.PendingInputResponse, error) {
	pending := state.pending
	response := &api.PendingInputResponse{}
	switch pending.Kind {
	case api.PendingInputKindMCP:
		values, ok := answer.Answer.AsMcp()
		if !ok {
			return nil, errors.New("saved request requires host input answers")
		}
		responses := make(map[string]json.RawMessage, len(values))
		for id, value := range values {
			responses[id] = slices.Clone(value)
		}
		response.MCP = &api.MCPInputResponse{ToolCallID: pending.MCP.ToolCallID, Responses: responses}
	case api.PendingInputKindConfirmation:
		approved, ok := answer.Answer.AsConfirmation()
		if !ok {
			return nil, errors.New("saved request requires an approval decision")
		}
		response.Confirmation = &api.ConfirmationDecision{ID: pending.Confirmation.ID, Approved: bool(approved)}
	case api.PendingInputKindClarification:
		text, ok := answer.Answer.AsClarification()
		if !ok {
			return nil, errors.New("saved request requires a free-text answer")
		}
		var id string
		if pending.Await.Kind == planner.AwaitItemKindToolClarification {
			id = pending.Await.ToolClarification.ID
		} else {
			id = pending.Await.Clarification.ID
		}
		response.Clarification = &api.ClarificationAnswer{ID: id, Answer: string(text)}
	case api.PendingInputKindToolResults:
		if pending.Await.Kind == planner.AwaitItemKindQuestions {
			value, ok := answer.Answer.AsQuestions()
			if !ok {
				return nil, errors.New("saved request requires a structured question answer")
			}
			original := pending.Await.Questions
			result, err := prepareInputToolResult(value, original.ToolName, original.ToolCallID)
			if err != nil {
				return nil, err
			}
			response.ToolResults = &api.ToolResultsSet{ID: original.ID, Results: []*api.ProvidedToolResult{result}}
		} else {
			values, ok := answer.Answer.AsExternalTools()
			if !ok {
				return nil, errors.New("saved request requires external tool results")
			}
			original := pending.Await.ExternalTools
			if len(values) != len(original.Items) {
				return nil, errors.New("response must answer every requested external call exactly once")
			}
			response.ToolResults = &api.ToolResultsSet{ID: original.ID}
			for _, call := range original.Items {
				value, ok := values[call.ToolCallID]
				if !ok {
					return nil, fmt.Errorf("missing external call result %q", call.ToolCallID)
				}
				result, err := prepareInputToolResult(value, call.Name, call.ToolCallID)
				if err != nil {
					return nil, err
				}
				response.ToolResults.Results = append(response.ToolResults.Results, result)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported saved input kind %q", pending.Kind)
	}
	if err := validatePendingInputResponse(response); err != nil {
		return nil, err
	}
	if err := validatePendingInputResponseFor(pending, response); err != nil {
		return nil, err
	}
	if state.checkpoint != nil {
		if err := validateProviderContinuationResponse(state.checkpoint, response); err != nil {
			return nil, err
		}
	}
	return response, nil
}

// prepareInputToolResult uses generated leaf converters and the saved call
// identity. The caller validates the resulting value with that call's schema.
func prepareInputToolResult(value *genruninput.InputToolResult, name tools.Ident, callID string) (*api.ProvidedToolResult, error) {
	blocks, err := inputContentBlocks(value.Blocks)
	if err != nil {
		return nil, err
	}
	result := &api.ProvidedToolResult{Name: name, ToolCallID: callID, Blocks: blocks}
	if success, ok := value.Outcome.AsSuccess(); ok {
		result.Success = &api.ProvidedToolSuccess{Result: slices.Clone(success.Result)}
		if success.Bounds != nil {
			result.Success.Bounds = agent.CloneBounds(success.Bounds.ConvertToBounds())
		}
	} else if failure, ok := value.Outcome.AsFailure(); ok {
		result.Failure = &api.ProvidedToolFailure{Kind: planner.FailureKind(failure.Kind), Message: failure.Message, Action: planner.RecoveryAction(failure.Action)}
		for _, issue := range failure.Issues {
			result.Failure.Issues = append(result.Failure.Issues, issue.ConvertToFieldIssue())
		}
	} else {
		return nil, errors.New("external result requires success or failure")
	}
	return result, nil
}
