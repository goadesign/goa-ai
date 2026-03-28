// Package completion provides the typed runtime contract for service-owned
// direct assistant completions. Generated completion packages expose typed specs
// that this package uses to request provider-enforced structured output and
// decode the final assistant response through generated codecs.
package completion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Ident is the stable identifier for a generated completion contract.
	Ident string

	// Spec describes one generated typed completion contract.
	Spec[T any] struct {
		// Name is the stable completion identifier declared in the DSL.
		Name Ident
		// Description provides human-readable context about the completion.
		Description string
		// Result describes the generated result schema and generic codec.
		Result tools.TypeSpec
		// Codec is the generated typed codec for the completion result.
		Codec tools.JSONCodec[T]
	}

	// Response contains the raw model response plus the decoded typed value.
	Response[T any] struct {
		// Value is the decoded typed completion result.
		Value T
		// Raw is the original provider-agnostic model response.
		Raw *model.Response
	}

	// completionStream validates the typed completion streaming contract on top of
	// a provider-neutral model.Streamer.
	//
	// Contract:
	//   - Preview chunks are optional and surfaced as ChunkTypeCompletionDelta.
	//   - Exactly one canonical ChunkTypeCompletion must arrive before EOF.
	//   - Text and tool chunks are invalid on this typed completion surface.
	completionStream struct {
		inner     model.Streamer
		name      Ident
		finalSeen bool
	}
)

// Complete runs a unary typed completion using the provided generated spec.
func Complete[T any](ctx context.Context, client model.Client, req *model.Request, spec Spec[T]) (*Response[T], error) {
	if client == nil {
		return nil, errors.New("completion client is required")
	}
	cloned, err := prepareRequest(req, spec, false)
	if err != nil {
		return nil, err
	}
	raw, err := client.Complete(ctx, cloned)
	if err != nil {
		return nil, err
	}
	value, err := DecodeResponse(raw, spec)
	if err != nil {
		return nil, err
	}
	return &Response[T]{
		Value: value,
		Raw:   raw,
	}, nil
}

// Stream starts a typed completion stream using the provided generated spec.
//
// Streaming completions reuse the provider-neutral model.Streamer contract. The
// final typed value is decoded from the canonical ChunkTypeCompletion payload;
// completion deltas are preview-only and may be ignored.
func Stream[T any](ctx context.Context, client model.Client, req *model.Request, spec Spec[T]) (model.Streamer, error) {
	if client == nil {
		return nil, errors.New("completion client is required")
	}
	cloned, err := prepareRequest(req, spec, true)
	if err != nil {
		return nil, err
	}
	streamer, err := client.Stream(ctx, cloned)
	if err != nil {
		return nil, err
	}
	if streamer == nil {
		return nil, fmt.Errorf("completion %q stream is nil", spec.Name)
	}
	return newCompletionStream(streamer, spec.Name), nil
}

// DecodeResponse decodes the structured assistant response with the generated
// typed codec from the completion spec.
func DecodeResponse[T any](resp *model.Response, spec Spec[T]) (T, error) {
	var zero T
	if resp == nil {
		return zero, errors.New("completion response is nil")
	}
	if len(resp.ToolCalls) > 0 {
		return zero, fmt.Errorf("completion %q returned tool calls", spec.Name)
	}
	payload, err := responseJSON(resp)
	if err != nil {
		return zero, fmt.Errorf("decode completion %q: %w", spec.Name, err)
	}
	return decodePayload(payload, spec)
}

// DecodeChunk decodes the canonical final completion chunk from a typed
// completion stream. Non-completion chunks are ignored and return ok=false.
func DecodeChunk[T any](chunk model.Chunk, spec Spec[T]) (T, bool, error) {
	var zero T
	if chunk.Type != model.ChunkTypeCompletion {
		return zero, false, nil
	}
	if chunk.Completion == nil {
		return zero, false, fmt.Errorf("decode completion %q: completion chunk missing payload", spec.Name)
	}
	if chunk.Completion.Name != string(spec.Name) {
		return zero, false, fmt.Errorf(
			"decode completion %q: completion chunk name %q does not match spec",
			spec.Name,
			chunk.Completion.Name,
		)
	}
	value, err := decodePayload(chunk.Completion.Payload, spec)
	if err != nil {
		return zero, false, err
	}
	return value, true, nil
}

// responseJSON extracts the structured JSON payload from a typed completion
// response. Typed completions accept exactly one assistant message with exactly
// one content-bearing JSON part.
func responseJSON(resp *model.Response) ([]byte, error) {
	if len(resp.Content) != 1 {
		return nil, fmt.Errorf("expected exactly 1 assistant message, got %d", len(resp.Content))
	}
	message := resp.Content[0]
	if message.Role != model.ConversationRoleAssistant {
		return nil, fmt.Errorf("unexpected %q message in completion response", message.Role)
	}
	text, err := assistantJSONText(message)
	if err != nil {
		return nil, err
	}
	return []byte(text), nil
}

// assistantJSONText validates the assistant message shape accepted by typed
// completions and returns the single JSON payload text.
func assistantJSONText(message model.Message) (string, error) {
	var body string
	for _, part := range message.Parts {
		switch actual := part.(type) {
		case model.TextPart:
			if body != "" {
				return "", errors.New("completion response contained multiple content parts")
			}
			body = actual.Text
		case model.CitationsPart:
			if body != "" {
				return "", errors.New("completion response contained multiple content parts")
			}
			body = actual.Text
		case model.ThinkingPart:
			continue
		case model.CacheCheckpointPart:
			continue
		default:
			return "", fmt.Errorf("unsupported response part %T in completion response", part)
		}
	}
	if strings.TrimSpace(body) == "" {
		return "", errors.New("completion response did not contain assistant JSON")
	}
	return body, nil
}

// prepareRequest clones a typed completion request and applies the generated
// structured-output contract with the requested streaming mode.
func prepareRequest[T any](req *model.Request, spec Spec[T], stream bool) (*model.Request, error) {
	if req == nil {
		return nil, errors.New("completion request is required")
	}
	if !stream && req.Stream {
		return nil, fmt.Errorf("completion %q does not support streaming; use a unary request", spec.Name)
	}
	if req.StructuredOutput != nil {
		return nil, fmt.Errorf("completion %q cannot override an existing structured output request", spec.Name)
	}
	if len(req.Tools) > 0 {
		return nil, fmt.Errorf("completion %q does not allow tool definitions", spec.Name)
	}
	if req.ToolChoice != nil {
		return nil, fmt.Errorf("completion %q does not allow tool choice", spec.Name)
	}
	structuredOutput, err := structuredOutputFor(spec)
	if err != nil {
		return nil, err
	}
	cloned := *req
	cloned.Stream = stream
	cloned.StructuredOutput = structuredOutput
	return &cloned, nil
}

// decodePayload decodes a canonical structured completion payload with the
// generated codec from the completion spec.
func decodePayload[T any](payload []byte, spec Spec[T]) (T, error) {
	var zero T
	value, err := spec.Codec.FromJSON(payload)
	if err != nil {
		return zero, fmt.Errorf("decode completion %q: %w", spec.Name, err)
	}
	return value, nil
}

// newCompletionStream wraps a provider-neutral streamer with the typed
// completion streaming contract.
func newCompletionStream(inner model.Streamer, name Ident) model.Streamer {
	return &completionStream{
		inner: inner,
		name:  name,
	}
}

// structuredOutputFor converts a generated completion spec into the low-level
// provider-neutral structured-output request carried by model.Request.
func structuredOutputFor[T any](spec Spec[T]) (*model.StructuredOutput, error) {
	if spec.Name == "" {
		return nil, errors.New("completion spec name is required")
	}
	if len(spec.Result.Schema) == 0 {
		return nil, fmt.Errorf("completion %q requires a result schema", spec.Name)
	}
	return &model.StructuredOutput{
		Name:   string(spec.Name),
		Schema: append([]byte(nil), spec.Result.Schema...),
	}, nil
}

func (s *completionStream) Recv() (model.Chunk, error) {
	chunk, err := s.inner.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) && !s.finalSeen {
			return model.Chunk{}, fmt.Errorf(
				"completion %q stream ended without canonical completion chunk",
				s.name,
			)
		}
		return chunk, err
	}
	switch chunk.Type {
	case model.ChunkTypeCompletionDelta:
		if err := s.validateCompletionDelta(chunk.CompletionDelta); err != nil {
			return model.Chunk{}, err
		}
	case model.ChunkTypeCompletion:
		if err := s.validateCompletion(chunk.Completion); err != nil {
			return model.Chunk{}, err
		}
		s.finalSeen = true
	case model.ChunkTypeThinking, model.ChunkTypeUsage:
		return chunk, nil
	case model.ChunkTypeStop:
		if !s.finalSeen {
			return model.Chunk{}, fmt.Errorf(
				"completion %q stream stopped before canonical completion chunk",
				s.name,
			)
		}
	case model.ChunkTypeText, model.ChunkTypeToolCall, model.ChunkTypeToolCallDelta:
		return model.Chunk{}, fmt.Errorf(
			"completion %q stream emitted unexpected %q chunk",
			s.name,
			chunk.Type,
		)
	default:
		return model.Chunk{}, fmt.Errorf(
			"completion %q stream emitted unsupported %q chunk",
			s.name,
			chunk.Type,
		)
	}
	return chunk, nil
}

func (s *completionStream) Close() error {
	return s.inner.Close()
}

func (s *completionStream) Metadata() map[string]any {
	return s.inner.Metadata()
}

// validateCompletionDelta enforces the preview-only chunk contract for a typed
// completion stream.
func (s *completionStream) validateCompletionDelta(delta *model.CompletionDelta) error {
	if delta == nil {
		return fmt.Errorf("completion %q stream emitted completion delta without payload", s.name)
	}
	if s.finalSeen {
		return fmt.Errorf("completion %q stream emitted completion delta after final completion", s.name)
	}
	if delta.Name != string(s.name) {
		return fmt.Errorf(
			"completion %q stream emitted completion delta for %q",
			s.name,
			delta.Name,
		)
	}
	return nil
}

// validateCompletion enforces the canonical final chunk contract for a typed
// completion stream.
func (s *completionStream) validateCompletion(completion *model.Completion) error {
	if completion == nil {
		return fmt.Errorf("completion %q stream emitted completion without payload", s.name)
	}
	if s.finalSeen {
		return fmt.Errorf("completion %q stream emitted multiple canonical completion chunks", s.name)
	}
	if completion.Name != string(s.name) {
		return fmt.Errorf(
			"completion %q stream emitted completion for %q",
			s.name,
			completion.Name,
		)
	}
	if len(completion.Payload) == 0 {
		return fmt.Errorf("completion %q stream emitted empty canonical completion payload", s.name)
	}
	return nil
}
