// Package completion provides the typed runtime contract for service-owned
// direct assistant completions. Generated completion packages expose typed specs
// that this package uses to request provider-enforced structured output and
// decode the final assistant response through generated codecs.
package completion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
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
		// Schema constrains the assistant response as JSON Schema.
		Schema rawjson.Message
		// SchemaWithoutRootExample is Schema without its root example annotation.
		SchemaWithoutRootExample rawjson.Message
		// ExampleJSON is the canonical authored result example.
		ExampleJSON rawjson.Message
		// Fields contains generated paths, types, descriptions, and union branch
		// requirements for the result schema.
		Fields []tools.FieldMetadata
		// Codec is the generated typed codec for the completion result.
		Codec tools.JSONCodec[T]
	}

	// Response contains the one model response and its decoded value.
	Response[T any] struct {
		// Value is the decoded typed completion result.
		Value T
		// ModelResponse is the exact provider response decoded into Value.
		ModelResponse *model.Response
	}

	// Streamer exposes preview chunks and the final typed value from one
	// completion stream. Value remains unavailable until Recv has validated the
	// complete provider stream, finalized its lifecycle, and returned its final
	// completion chunk. One goroutine must call Recv, Value, and Close
	// sequentially.
	Streamer[T any] struct {
		inner      *completionStream
		validation *completionValidation[T]
		value      T
		valueSet   bool
	}

	// completionValidation retains the value accepted by the generated codec at
	// the model stream boundary.
	completionValidation[T any] struct {
		value    T
		valueSet bool
	}

	// completionStream withholds the final completion until the model-owned
	// stream reaches a clean, validated EOF.
	//
	// Contract:
	//   - Preview chunks are optional and surfaced as ChunkTypeCompletionDelta.
	//   - Exactly one canonical ChunkTypeCompletion must arrive before EOF.
	//   - Text and tool chunks are invalid on this typed completion surface.
	completionStream struct {
		inner             *model.ValidatedStream
		completionFailure func() error
		validated         bool
		terminalErr       error
	}
)

var errOutputLimited = errors.New("typed completion output reached its generation limit")

// Complete runs a unary typed completion using the provided generated spec.
func Complete[T any](ctx context.Context, client model.Client, req *model.Request, spec Spec[T]) (*Response[T], error) {
	if err := model.ValidateClient(client); err != nil {
		return nil, fmt.Errorf("completion: %w", err)
	}
	cloned, err := prepareRequest(req, spec, false)
	if err != nil {
		return nil, err
	}
	validation, err := configureCompletionValidation(cloned, spec)
	if err != nil {
		return nil, err
	}
	response, err := client.Complete(ctx, cloned)
	if err != nil {
		return nil, completionOutputError(err)
	}
	return &Response[T]{
		Value:         validation.value,
		ModelResponse: response,
	}, nil
}

// Stream starts a typed completion stream using the provided generated spec.
//
// Streaming completions reuse the provider-neutral model.ValidatedStream
// contract. The final typed value is decoded from the canonical
// ChunkTypeCompletion payload; completion deltas are preview-only and may be
// ignored.
func Stream[T any](ctx context.Context, client model.Client, req *model.Request, spec Spec[T]) (*Streamer[T], error) {
	if err := model.ValidateClient(client); err != nil {
		return nil, fmt.Errorf("completion: %w", err)
	}
	cloned, err := prepareRequest(req, spec, true)
	if err != nil {
		return nil, err
	}
	validation, err := configureCompletionValidation(cloned, spec)
	if err != nil {
		return nil, err
	}
	streamer, err := client.Stream(ctx, cloned)
	if err != nil {
		if streamer != nil {
			err = streamer.Finalize(err)
		}
		return nil, completionOutputError(err)
	}
	if streamer == nil {
		return nil, fmt.Errorf("completion %q stream is nil", spec.Name)
	}
	return newCompletionStream(streamer, validation), nil
}

// Recv returns the next preview or final completion chunk. The final chunk is
// withheld until the complete provider stream passes validation.
func (s *Streamer[T]) Recv() (model.Chunk, error) {
	chunk, err := s.inner.Recv()
	if err != nil {
		return chunk, completionOutputError(err)
	}
	_, ok := chunk.(model.CompletionChunk)
	if !ok {
		return chunk, nil
	}
	s.value = s.validation.value
	s.valueSet = true
	return chunk, nil
}

// Value returns the decoded completion after Recv returns the final completion
// chunk. It returns false before the complete provider stream is valid.
func (s *Streamer[T]) Value() (T, bool) {
	return s.value, s.valueSet
}

// Close returns the cleanup result already completed by Recv, or closes an
// unfinished stream when the caller stops before a final value.
func (s *Streamer[T]) Close() error {
	return s.inner.Close()
}

// Response returns the complete provider response after the stream ends.
func (s *Streamer[T]) Response() *model.Response {
	return s.inner.Response()
}

// completionPayload extracts the typed JSON value from a response already
// accepted by the model boundary.
func completionPayload[T any](resp *model.Response, spec Spec[T]) ([]byte, error) {
	if len(resp.ToolCalls()) > 0 {
		return nil, fmt.Errorf("completion %q returned tool calls", spec.Name)
	}
	payload, err := responseJSON(resp)
	if err != nil {
		return nil, fmt.Errorf("decode completion %q: %w", spec.Name, err)
	}
	return payload, nil
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
	var body strings.Builder
	var contentKind string
	for _, part := range message.Parts {
		switch actual := part.(type) {
		case model.TextPart:
			if contentKind == "citation" {
				return "", errors.New("completion response contained multiple content parts")
			}
			contentKind = "text"
			// Providers can split one answer across adjacent text parts.
			// Joining them preserves the exact returned text.
			body.WriteString(actual.Text)
		case model.CitationsPart:
			if contentKind != "" {
				return "", errors.New("completion response contained multiple content parts")
			}
			contentKind = "citation"
			body.WriteString(actual.Text)
		case model.ThinkingPart:
			continue
		case model.CacheCheckpointPart:
			continue
		default:
			return "", fmt.Errorf("unsupported response part %T in completion response", part)
		}
	}
	if strings.TrimSpace(body.String()) == "" {
		return "", errors.New("completion response did not contain assistant JSON")
	}
	return body.String(), nil
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
		// Typed completions expect one JSON response. Provider thinking can use
		// the output budget and split that response into parts the decoder
		// rejects. Unless the caller chose otherwise, ask the provider to turn
		// thinking off. Models that cannot disable it may ignore this setting.
		cloned.Thinking = &model.ThinkingOptions{Enable: false}
	}
	cloned.Stream = stream
	cloned.StructuredOutput = structuredOutput
	return &cloned, nil
}

// configureCompletionValidation gives model-client boundaries the generated
// codec and exact response envelope required by this completion.
func configureCompletionValidation[T any](
	request *model.Request,
	spec Spec[T],
) (*completionValidation[T], error) {
	validation := &completionValidation[T]{}
	err := model.SetCompletionValidator(request, func(response *model.Response, streamed *model.Completion) error {
		if response == nil {
			if streamed == nil {
				return fmt.Errorf("completion %q validation requires response or stream output", spec.Name)
			}
			_, err := decodePayload(streamed.Payload, spec)
			return err
		}
		if response.OutputLimited {
			return errOutputLimited
		}
		responsePayload, err := completionPayload(response, spec)
		if err != nil {
			return err
		}
		responseValue, err := decodePayload(responsePayload, spec)
		if err != nil {
			return err
		}
		if streamed == nil {
			validation.value = responseValue
			validation.valueSet = true
			return nil
		}
		if _, err := decodePayload(streamed.Payload, spec); err != nil {
			return err
		}
		if !bytes.Equal(streamed.Payload, responsePayload) {
			return fmt.Errorf("completion %q stream does not match its complete response", spec.Name)
		}
		validation.value = responseValue
		validation.valueSet = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return validation, nil
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

// completionOutputError preserves the planner-facing typed completion error
// while leaving transport and provider failures unchanged.
func completionOutputError(err error) error {
	var validationErr *model.OutputValidationError
	if !errors.As(err, &validationErr) {
		return err
	}
	var outputErr *outputcontract.Error
	if errors.As(err, &outputErr) {
		return err
	}
	return outputcontract.NewWithOrigin(err, outputcontract.OriginModel)
}

// newCompletionStream wraps a provider-neutral streamer with the typed
// completion streaming contract.
func newCompletionStream[T any](
	inner *model.ValidatedStream,
	validation *completionValidation[T],
) *Streamer[T] {
	validated := &completionStream{
		inner: inner,
		completionFailure: func() error {
			if validation.valueSet {
				return nil
			}
			return outputcontract.NewWithOrigin(
				errors.New("validated completion stream has no typed value"),
				outputcontract.OriginModel,
			)
		},
	}
	return &Streamer[T]{
		inner:      validated,
		validation: validation,
	}
}

// structuredOutputFor converts a generated completion spec into the low-level
// provider-neutral structured-output request carried by model.Request.
func structuredOutputFor[T any](spec Spec[T]) (*model.StructuredOutput, error) {
	if spec.Name == "" {
		return nil, errors.New("completion spec name is required")
	}
	if len(spec.Schema) == 0 {
		return nil, fmt.Errorf("completion %q requires a result schema", spec.Name)
	}
	if spec.Codec.FromJSON == nil || spec.Codec.ToJSON == nil {
		return nil, fmt.Errorf("completion %q requires a bidirectional result codec", spec.Name)
	}
	if len(spec.ExampleJSON) > 0 && len(spec.SchemaWithoutRootExample) == 0 {
		return nil, fmt.Errorf(
			"completion %q result example requires a schema without the root example",
			spec.Name,
		)
	}
	return &model.StructuredOutput{
		Name:                     string(spec.Name),
		Schema:                   append([]byte(nil), spec.Schema...),
		SchemaWithoutRootExample: append([]byte(nil), spec.SchemaWithoutRootExample...),
		ExampleJSON:              append(rawjson.Message(nil), spec.ExampleJSON...),
		Description:              spec.Description,
	}, nil
}

func (s *completionStream) Recv() (model.Chunk, error) {
	if s.terminalErr != nil {
		return nil, s.terminalErr
	}
	if s.validated {
		return nil, io.EOF
	}
	chunk, err := s.inner.Recv()
	if err != nil {
		// Only literal EOF completes a model stream. A wrapped EOF reports the
		// provider failure that added the wrapper.
		//nolint:errorlint // Exact equality is required by the model stream contract.
		if err == io.EOF {
			if finalizeErr := s.finalize(nil); finalizeErr != nil {
				return nil, finalizeErr
			}
			return nil, io.EOF
		}
		return nil, s.finalize(err)
	}
	if _, ok := chunk.(model.CompletionChunk); !ok {
		return chunk, nil
	}
	if err := s.drainAfterCompletion(); err != nil {
		return nil, err
	}
	return chunk, nil
}

// drainAfterCompletion reads through EOF and completes all stream lifecycle
// work before exposing the final typed value.
func (s *completionStream) drainAfterCompletion() error {
	for {
		_, err := s.inner.Recv()
		if err != nil {
			// Only literal EOF completes a model stream. A wrapped EOF reports
			// the provider failure that added the wrapper.
			//nolint:errorlint // Exact equality is required by the model stream contract.
			if err == io.EOF {
				return s.finalize(s.completionFailure())
			}
			return s.finalize(err)
		}
	}
}

func (s *completionStream) Close() error {
	return s.inner.Close()
}

func (s *completionStream) Response() *model.Response {
	return s.inner.Response()
}

// finalize gives the model-owned stream the exact terminal or typed-completion
// processing result before this wrapper converts validation into its public
// output-contract category.
func (s *completionStream) finalize(primaryErr error) error {
	operationErr := s.inner.Finalize(primaryErr)
	if operationErr != nil {
		return s.fail(completionStreamError(operationErr))
	}
	s.validated = true
	return nil
}

// fail records the first terminal error and returns it for every later Recv.
func (s *completionStream) fail(err error) error {
	if s.terminalErr != nil {
		return s.terminalErr
	}
	s.terminalErr = err
	return err
}

// completionStreamError gives raw model validation failures the planner-owned
// non-retryable error used by runtime-wrapped streams.
func completionStreamError(err error) error {
	if !model.IsStreamValidationError(err) {
		return err
	}
	var outputErr *outputcontract.Error
	if errors.As(err, &outputErr) {
		return err
	}
	return outputcontract.NewWithOrigin(err, outputcontract.OriginModel)
}
