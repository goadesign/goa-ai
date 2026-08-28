package bedrock

// This file adapts provider-neutral structured completions to the forced tool
// representation accepted by Anthropic Messages on Bedrock. Callers continue
// to receive completion chunks and one canonical JSON response; the private
// tool definition and its object wrapper never cross the provider boundary.

import (
	"errors"
	"fmt"
	"io"

	"goa.design/goa-ai/features/model/internal/modelid"
	"goa.design/goa-ai/runtime/agent/model"
)

// anthropicStructuredOutputStreamer converts the one private tool invocation
// into the completion events and response declared by the caller's request.
type anthropicStructuredOutputStreamer struct {
	stream         model.Streamer
	toolName       string
	contract       *model.RequestContract
	response       *model.Response
	completionSeen bool
}

// newAnthropicStructuredOutputStreamer binds one private tool stream to the
// original structured-output contract used by the validated client.
func newAnthropicStructuredOutputStreamer(
	stream model.Streamer,
	toolName string,
	contract *model.RequestContract,
) model.Streamer {
	return &anthropicStructuredOutputStreamer{
		stream:   stream,
		toolName: toolName,
		contract: contract,
	}
}

// Recv suppresses private tool JSON fragments, returns the finalized payload as
// one completion chunk, and reifies the complete response before clean EOF.
func (s *anthropicStructuredOutputStreamer) Recv() (model.Chunk, error) {
	for {
		chunk, err := s.stream.Recv()
		// Only literal EOF completes a model stream. A wrapped EOF reports the
		// provider failure that added the wrapper.
		//nolint:errorlint // Exact equality is required by the model stream contract.
		if err == io.EOF {
			resp := s.stream.Response()
			if resp == nil {
				return nil, s.contract.RejectProviderOutput(
					nil,
					errors.New("bedrock: structured output stream completed without a response"),
				)
			}
			if err := reifyStructuredOutputTool(resp, s.toolName); err != nil {
				return nil, s.contract.RejectResponse(resp, err)
			}
			s.response = resp
			return nil, io.EOF
		}
		if err != nil {
			return nil, err
		}

		switch value := chunk.(type) {
		case model.ToolCallDeltaChunk:
			if string(value.Delta.Name) != s.toolName {
				return nil, s.rejectUnexpectedTool(string(value.Delta.Name))
			}
			continue
		case model.ToolCallChunk:
			if string(value.ToolCall.Name) != s.toolName {
				return nil, s.rejectUnexpectedTool(string(value.ToolCall.Name))
			}
			if s.completionSeen {
				return nil, s.contract.RejectProviderOutput(
					nil,
					errors.New("bedrock: structured output stream returned multiple tool calls"),
				)
			}
			payload, err := unwrapStructuredOutputValue(value.ToolCall.Payload)
			if err != nil {
				return nil, s.contract.RejectProviderOutput(
					nil,
					fmt.Errorf("bedrock: structured output tool %q: %w", s.toolName, err),
				)
			}
			s.completionSeen = true
			return model.CompletionChunk{
				Completion: model.Completion{
					Name:    s.toolName,
					Payload: payload,
				},
			}, nil
		case model.ThinkingChunk, model.UsageChunk, model.StopChunk:
			return chunk, nil
		default:
			return nil, s.contract.RejectProviderOutput(
				nil,
				fmt.Errorf("bedrock: structured output stream returned unexpected chunk %T", chunk),
			)
		}
	}
}

// Response exposes the reified response only after Recv has returned clean EOF.
func (s *anthropicStructuredOutputStreamer) Response() *model.Response {
	return s.response
}

// Close releases the underlying Anthropic response stream.
func (s *anthropicStructuredOutputStreamer) Close() error {
	return s.stream.Close()
}

// prepareRequest selects the Bedrock representation for one structured-output
// request. Models without a usable native output format receive one forced
// private tool; models with a compatible native format keep the original
// request for the Anthropic adapter.
func (c *anthropicBedrockProvider) prepareRequest(req *model.Request) (*model.Request, string, error) {
	if req.StructuredOutput == nil {
		return req, "", nil
	}
	resolved, err := modelid.Resolve(
		bedrockProviderName,
		req,
		c.defaultModel,
		c.highModel,
		c.smallModel,
	)
	if err != nil {
		return nil, "", err
	}
	if !structuredOutputUsesTool(resolved, req.StructuredOutput) {
		return req, "", nil
	}
	if len(req.Tools) > 0 || req.ToolChoice != nil {
		return nil, "", errors.New(
			"bedrock: structured output cannot be combined with request tool definitions",
		)
	}
	def, err := structuredOutputToolDefinition(req.StructuredOutput)
	if err != nil {
		return nil, "", err
	}
	toolChoice := &model.ToolChoice{
		Mode: model.ToolChoiceModeTool,
		Name: def.Name,
	}
	if _, err := resolveThinking(req.Thinking, toolChoice, resolved); err != nil {
		return nil, "", err
	}
	effective := *req
	effective.StructuredOutput = nil
	effective.Tools = []*model.ToolDefinition{def}
	effective.ToolChoice = toolChoice
	return &effective, def.Name, nil
}

// rejectUnexpectedTool reports a provider response that violated the forced
// private tool choice before a complete response was available.
func (s *anthropicStructuredOutputStreamer) rejectUnexpectedTool(actual string) error {
	return s.contract.RejectProviderOutput(
		nil,
		fmt.Errorf(
			"bedrock: structured output did not return the forced tool call %q; got %q",
			s.toolName,
			actual,
		),
	)
}
