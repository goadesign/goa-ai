// Package completion provides the typed runtime contract for service-owned
// direct assistant completions. Generated completion packages expose typed specs
// that this package uses to request provider-enforced structured output and
// decode the final assistant response through generated codecs.
package completion

import (
	"bytes"
	"context"
	"encoding/json"
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

	// Response contains every model response plus the decoded typed value.
	Response[T any] struct {
		// Value is the decoded typed completion result.
		Value T
		// Attempts contains provider-agnostic model responses in invocation order.
		// A successful first response produces one entry; a corrected completion
		// produces the rejected response followed by the accepted response.
		Attempts []*model.Response
	}

	// completionStream validates the typed completion streaming contract on top of
	// a provider-neutral model.Streamer.
	//
	// Contract:
	//   - Preview chunks are optional and surfaced as ChunkTypeCompletionDelta.
	//   - Exactly one canonical ChunkTypeCompletion must arrive before EOF.
	//   - Text and tool chunks are invalid on this typed completion surface.
	completionStream struct {
		inner        model.Streamer
		name         Ident
		finalSeen    bool
		stopped      bool
		finalJSON    []byte
		canonicalize func([]byte) ([]byte, error)
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
	response := &Response[T]{Attempts: []*model.Response{raw}}
	payload, err := completionPayload(raw, spec)
	if err != nil {
		return response, err
	}
	value, err := decodePayload(payload, spec)
	if err != nil {
		corrected, correctionErr := correctionRequest(cloned, raw.Content[0], err, spec)
		if correctionErr != nil {
			return response, correctionErr
		}
		raw, correctionErr = client.Complete(ctx, corrected)
		if correctionErr != nil {
			return response, correctionErr
		}
		response.Attempts = append(response.Attempts, raw)
		value, correctionErr = DecodeResponse(raw, spec)
		if correctionErr != nil {
			return response, fmt.Errorf(
				"completion %q remained invalid after 1 correction: %w",
				spec.Name,
				correctionErr,
			)
		}
	}
	response.Value = value
	return response, nil
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
	return newCompletionStream(streamer, spec), nil
}

// DecodeResponse decodes the structured assistant response with the generated
// typed codec from the completion spec.
func DecodeResponse[T any](resp *model.Response, spec Spec[T]) (T, error) {
	var zero T
	payload, err := completionPayload(resp, spec)
	if err != nil {
		return zero, err
	}
	return decodePayload(payload, spec)
}

// completionPayload validates the provider response envelope separately from
// the generated payload codec. Only codec failures are correctable by asking the
// model for another structured value.
func completionPayload[T any](resp *model.Response, spec Spec[T]) ([]byte, error) {
	if resp == nil {
		return nil, errors.New("completion response is nil")
	}
	if err := model.ValidateResponse(resp); err != nil {
		return nil, fmt.Errorf("completion %q response is invalid: %w", spec.Name, err)
	}
	if len(resp.ToolCalls()) > 0 {
		return nil, fmt.Errorf("completion %q returned tool calls", spec.Name)
	}
	payload, err := responseJSON(resp)
	if err != nil {
		return nil, fmt.Errorf("decode completion %q: %w", spec.Name, err)
	}
	return payload, nil
}

// DecodeChunk decodes the canonical final completion chunk from a typed
// completion stream. Non-completion chunks are ignored and return ok=false.
func DecodeChunk[T any](chunk model.Chunk, spec Spec[T]) (T, bool, error) {
	var zero T
	completion, ok := chunk.(model.CompletionChunk)
	if !ok {
		return zero, false, nil
	}
	if completion.Completion.Name != string(spec.Name) {
		return zero, false, fmt.Errorf(
			"decode completion %q: completion chunk name %q does not match spec",
			spec.Name,
			completion.Completion.Name,
		)
	}
	value, err := decodePayload(completion.Completion.Payload, spec)
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
			// Providers may chunk one logical answer into adjacent text
			// parts; concatenation is transport normalization, not content
			// repair.
			body += actual.Text
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
	if cloned.Thinking == nil {
		// Typed completions are single-shot JSON transactions: provider
		// thinking spends the output budget on thoughts and fragments the
		// response into parts the strict decoder rejects. Absent an explicit
		// caller preference, request thinking off; providers whose models
		// cannot disable it simply ignore the hint.
		cloned.Thinking = &model.ThinkingOptions{Enable: false}
	}
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

// correctionRequest appends the rejected assistant response and one
// framework-owned correction turn. Generated validation issues and examples
// remain structured until this model-facing boundary.
func correctionRequest[T any](
	req *model.Request,
	rejected model.Message,
	decodeErr error,
	spec Spec[T],
) (*model.Request, error) {
	text, err := correctionText(decodeErr, spec.Result)
	if err != nil {
		return nil, fmt.Errorf("build completion %q correction: %w", spec.Name, err)
	}
	rejected.Parts = append([]model.Part(nil), rejected.Parts...)
	messages := make([]*model.Message, 0, len(req.Messages)+2)
	messages = append(messages, req.Messages...)
	messages = append(messages,
		&rejected,
		&model.Message{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: text}},
		},
	)
	corrected := *req
	corrected.Messages = messages
	return &corrected, nil
}

// correctionText renders codec-owned validation evidence without parsing the
// JSON Schema or inventing a second validation language.
func correctionText(decodeErr error, result tools.TypeSpec) (string, error) {
	var text strings.Builder
	text.WriteString("The previous structured response did not match the required JSON contract.\n")
	fmt.Fprintf(&text, "Validation error: %s\n", decodeErr)

	var validation *tools.ValidationError
	if errors.As(decodeErr, &validation) {
		issues := validation.Issues()
		issuesJSON, err := json.Marshal(issues)
		if err != nil {
			return "", fmt.Errorf("encode validation issues: %w", err)
		}
		fmt.Fprintf(&text, "Field issues: %s\n", issuesJSON)
		descriptions := tools.FieldDescriptionsForIssues(
			issues,
			validation.Descriptions(),
			result.FieldDescriptions,
		)
		if len(descriptions) > 0 {
			descriptionsJSON, err := json.Marshal(descriptions)
			if err != nil {
				return "", fmt.Errorf("encode field guidance: %w", err)
			}
			fmt.Fprintf(&text, "Field guidance: %s\n", descriptionsJSON)
		}
	}
	if example := bytes.TrimSpace(result.ExampleJSON); len(example) > 0 {
		fmt.Fprintf(
			&text,
			"JSON shape example (replace example values with values that satisfy this request): %s\n",
			example,
		)
	}
	text.WriteString("Return the complete corrected JSON value. Do not discuss the correction.")
	return text.String(), nil
}

// newCompletionStream wraps a provider-neutral streamer with the typed
// completion streaming contract.
func newCompletionStream[T any](inner model.Streamer, spec Spec[T]) model.Streamer {
	return &completionStream{
		inner: inner,
		name:  spec.Name,
		canonicalize: func(payload []byte) ([]byte, error) {
			value, err := spec.Codec.FromJSON(payload)
			if err != nil {
				return nil, err
			}
			return spec.Codec.ToJSON(value)
		},
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
	if spec.Codec.FromJSON == nil || spec.Codec.ToJSON == nil {
		return nil, fmt.Errorf("completion %q requires a bidirectional result codec", spec.Name)
	}
	if len(spec.Result.ExampleJSON) > 0 && len(spec.Result.SchemaWithoutRootExample) == 0 {
		return nil, fmt.Errorf(
			"completion %q result example requires a schema without the root example",
			spec.Name,
		)
	}
	return &model.StructuredOutput{
		Name:                     string(spec.Name),
		Schema:                   append([]byte(nil), spec.Result.Schema...),
		SchemaWithoutRootExample: append([]byte(nil), spec.Result.SchemaWithoutRootExample...),
		ExampleJSON:              append(tools.RawJSON(nil), spec.Result.ExampleJSON...),
		Description:              spec.Description,
	}, nil
}

func (s *completionStream) Recv() (model.Chunk, error) {
	chunk, err := s.inner.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) && !s.finalSeen {
			return nil, fmt.Errorf(
				"completion %q stream ended without canonical completion chunk",
				s.name,
			)
		}
		if errors.Is(err, io.EOF) {
			if !s.stopped {
				return nil, fmt.Errorf("completion %q stream ended without stop chunk", s.name)
			}
			if err := s.validateCanonicalResponse(); err != nil {
				return nil, err
			}
		}
		return chunk, err
	}
	if err := model.ValidateChunk(chunk); err != nil {
		return nil, fmt.Errorf("completion %q stream emitted invalid chunk: %w", s.name, err)
	}
	if s.stopped {
		return nil, fmt.Errorf("completion %q stream emitted %q after stop", s.name, chunk.Kind())
	}
	switch actual := chunk.(type) {
	case model.CompletionDeltaChunk:
		if err := s.validateCompletionDelta(actual.Delta); err != nil {
			return nil, err
		}
	case model.CompletionChunk:
		if err := s.validateCompletion(actual.Completion); err != nil {
			return nil, err
		}
		s.finalSeen = true
	case model.ThinkingChunk, model.UsageChunk:
		return chunk, nil
	case model.StopChunk:
		if !s.finalSeen {
			return nil, fmt.Errorf(
				"completion %q stream stopped before canonical completion chunk",
				s.name,
			)
		}
		s.stopped = true
	case model.TextChunk, model.ToolCallChunk, model.ToolCallDeltaChunk:
		return nil, fmt.Errorf(
			"completion %q stream emitted unexpected %q chunk",
			s.name,
			chunk.Kind(),
		)
	default:
		return nil, fmt.Errorf(
			"completion %q stream emitted unsupported %q chunk",
			s.name,
			chunk.Kind(),
		)
	}
	return chunk, nil
}

func (s *completionStream) Close() error {
	return s.inner.Close()
}

func (s *completionStream) Response() *model.Response {
	return s.inner.Response()
}

// validateCompletionDelta enforces the preview-only chunk contract for a typed
// completion stream.
func (s *completionStream) validateCompletionDelta(delta model.CompletionDelta) error {
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
func (s *completionStream) validateCompletion(completion model.Completion) error {
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
	if !json.Valid(completion.Payload) {
		return fmt.Errorf("completion %q stream emitted invalid canonical completion JSON", s.name)
	}
	canonical, err := s.canonicalize(completion.Payload)
	if err != nil {
		return fmt.Errorf("completion %q stream emitted invalid canonical completion payload: %w", s.name, err)
	}
	s.finalJSON = canonical
	return nil
}

// validateCanonicalResponse ensures the terminal completion chunk and the
// streamer's replayable response describe the same provider output.
func (s *completionStream) validateCanonicalResponse() error {
	response := s.inner.Response()
	if err := model.ValidateResponse(response); err != nil {
		return fmt.Errorf("completion %q stream returned an invalid canonical response: %w", s.name, err)
	}
	payload, err := responseJSON(response)
	if err != nil {
		return fmt.Errorf("completion %q stream returned an invalid canonical response: %w", s.name, err)
	}
	canonical, err := s.canonicalize(payload)
	if err != nil {
		return fmt.Errorf("completion %q stream returned an invalid canonical response: %w", s.name, err)
	}
	if !bytes.Equal(canonical, s.finalJSON) {
		return fmt.Errorf("completion %q stream chunk does not match canonical response", s.name)
	}
	return nil
}
