// Streaming adapter. Invariants: chunks flow through a buffered channel
// (32) drained by Recv; Recv returns io.EOF after a clean end; Close is
// idempotent; Response returns the canonical terminal response after clean EOF.

package vertex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"google.golang.org/genai"

	"goa.design/goa-ai/features/model/internal/outputvalidation"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// geminiStreamer adapts a Gemini GenerateContentStream sequence to the
	// model.Streamer interface. A single pump goroutine (run) translates
	// provider responses into chunks; Recv drains them.
	geminiStreamer struct {
		// ctx is the pump context. Close cancels it so run stops emitting even
		// when the caller abandons the stream without draining it.
		ctx context.Context

		// cancel cancels ctx; Close calls it (context cancellation is
		// idempotent, so is Close).
		cancel context.CancelFunc

		// chunks carries translated chunks from the pump goroutine to Recv. It
		// is buffered (32) and closed by run when the provider stream ends,
		// which is Recv's signal to surface the terminal error or io.EOF.
		chunks chan model.Chunk
		done   chan struct{}

		// mu guards err and canonical, the fields that cross the pump/consumer
		// boundary outside the chunks channel.
		mu sync.Mutex

		// err is the terminal pump error surfaced by Recv after chunks closes.
		err error

		// thoughtText accumulates thought text across Thought parts until a
		// signature finalizes the block. Pump-owned, no locking.
		thoughtText strings.Builder
		// thoughtIndex identifies the current reasoning block. It advances only
		// after a signature closes that block.
		thoughtIndex int

		// completionText accumulates structured-output text for the canonical
		// Completion chunk emitted at stream end. Pump-owned, no locking.
		completionText strings.Builder

		// response is the canonical model response assembled by the pump.
		response model.Response
		// canonical is published atomically when the provider stream completes.
		canonical      *model.Response
		retainedBytes  int
		retainedValues int
		rejectedUsage  model.TokenUsage

		// assistant accumulates provider-authored response parts in provider order.
		assistant model.Message

		// contract classifies provider-authored stream data that cannot be
		// represented by the captured request.
		contract     *model.RequestContract
		callIDs      *vertexToolCallIDAllocator
		pendingCalls []pendingVertexCall
	}

	// pendingVertexCall retains one validated provider call until every
	// explicit call ID in the stream has been reserved.
	pendingVertexCall struct {
		name             string
		payload          rawjson.Message
		id               string
		thoughtSignature string
		partIndex        int
	}
)

// Stream starts one raw Gemini provider stream.
func (c *provider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, err
	}
	prep, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	seq := c.models.GenerateContentStream(ctx, prep.modelID, prep.contents, prep.config)
	s := &geminiStreamer{
		ctx:           ctx,
		cancel:        cancel,
		chunks:        make(chan model.Chunk, 32),
		done:          make(chan struct{}),
		contract:      contract,
		callIDs:       prep.toolCallIDs,
		rejectedUsage: translateUsage(nil, prep.modelID, prep.modelClass),
	}
	go s.run(seq, prep)
	return s, nil
}

// Recv implements model.Streamer.
func (s *geminiStreamer) Recv() (model.Chunk, error) {
	select {
	case ch, ok := <-s.chunks:
		if !ok {
			s.mu.Lock()
			err := s.err
			s.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		return ch, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

// Close implements model.Streamer. It cancels the pump goroutine's context
// so run stops emitting even when the caller abandons the stream without
// draining it. Context cancellation is idempotent, so is Close.
func (s *geminiStreamer) Close() error {
	s.cancel()
	<-s.done
	return nil
}

// Response implements model.Streamer.
func (s *geminiStreamer) Response() *model.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canonical
}

// run is the pump goroutine: it drains the provider sequence, dispatches
// candidate parts to the named part handlers, and finishes with the
// canonical completion, usage, and stop chunks before closing chunks.
func (s *geminiStreamer) run(seq func(func(*genai.GenerateContentResponse, error) bool), prep *preparedRequest) {
	defer close(s.done)
	defer close(s.chunks)
	s.assistant = model.Message{Role: model.ConversationRoleAssistant}
	var stopReason string
	var sawCandidate bool
	var usageSeen bool
	var latestUsage model.TokenUsage
	var grounding *genai.GroundingMetadata
	var rejected error
	for resp, err := range seq {
		if err != nil {
			s.setErr(wrapGeminiError("generate_content_stream", err))
			return
		}
		if resp != nil && resp.UsageMetadata != nil {
			// Usage is independent of candidate content and must survive any
			// later translation rejection.
			latestUsage = translateUsage(resp.UsageMetadata, prep.modelID, prep.modelClass)
			s.rejectedUsage = latestUsage
			usageSeen = true
		}
		if resp == nil || len(resp.Candidates) == 0 {
			continue
		}
		if len(resp.Candidates) != 1 {
			s.setErr(outputvalidation.New(
				model.OutputValidationResponseShape,
				fmt.Errorf("vertex: stream response has %d candidates, want exactly one", len(resp.Candidates)),
			))
			return
		}
		sawCandidate = true
		cand := resp.Candidates[0]
		if cand.FinishReason != "" {
			stopReason = string(cand.FinishReason)
		}
		if cand.GroundingMetadata != nil {
			grounding = cand.GroundingMetadata
		}
		if rejected == nil && cand.Content != nil {
		contentParts:
			for _, part := range cand.Content.Parts {
				if part == nil {
					s.setErr(outputvalidation.New(
						model.OutputValidationResponseShape,
						errors.New("vertex: stream contains a nil part"),
					))
					return
				}
				switch {
				case part.FunctionCall != nil:
					if err := s.handleFunctionCallPart(part, prep); err != nil {
						if _, ok := model.UnadvertisedToolName(err); ok {
							rejected = err
							s.discardSemanticOutput()
							break contentParts
						}
						s.setErr(err)
						return
					}
				case part.Thought:
					if err := s.handleThoughtPart(part); err != nil {
						s.setErr(err)
						return
					}
				case part.Text != "":
					if err := s.handleTextPart(part, prep); err != nil {
						s.setErr(err)
						return
					}
				default:
					s.setErr(outputvalidation.New(
						model.OutputValidationResponseShape,
						errors.New("vertex: stream contains an unsupported response part"),
					))
					return
				}
			}
		}
	}
	if !sawCandidate {
		s.setErr(outputvalidation.New(
			model.OutputValidationStreamProtocol,
			errors.New("vertex: stream returned no candidates"),
		))
		return
	}
	if stopReason == "" {
		s.setErr(outputvalidation.New(
			model.OutputValidationStreamProtocol,
			errors.New("vertex: stream ended before candidate finish reason"),
		))
		return
	}
	if rejected != nil {
		s.setErr(rejected)
		return
	}
	if s.thoughtText.Len() > 0 {
		s.setErr(outputvalidation.New(
			model.OutputValidationResponseShape,
			errors.New("vertex: stream ended with unsigned thinking"),
		))
		return
	}
	if err := s.flushFunctionCalls(); err != nil {
		s.setErr(err)
		return
	}
	if prep.structuredOutput != nil {
		accumulated := s.completionText.String()
		if err := s.retain(accumulated); err != nil {
			s.setErr(err)
			return
		}
		payload, perr := finalStructuredCompletionPayload(accumulated)
		if perr != nil {
			s.setErr(outputvalidation.New(
				model.OutputValidationStructuredOutput,
				fmt.Errorf("vertex: structured output %q: %w", prep.structuredOutput.Name, perr),
			))
			return
		}
		s.emit(model.CompletionChunk{Completion: model.Completion{
			Name:    prep.structuredOutput.Name,
			Payload: payload,
		}})
		s.assistant.Parts = append(s.assistant.Parts, model.TextPart{Text: string(payload)})
	}
	if usageSeen {
		s.emit(model.UsageChunk{Usage: latestUsage})
	}
	if stopReason != "" {
		if err := s.retain(stopReason); err != nil {
			s.setErr(err)
			return
		}
		s.emit(model.StopChunk{
			Reason:        stopReason,
			OutputLimited: vertexOutputLimited(stopReason),
		})
	}
	s.response.StopReason = stopReason
	s.response.OutputLimited = vertexOutputLimited(stopReason)
	s.response.Usage = latestUsage
	grounded, err := applyGroundingMetadata(s.assistant.Parts, grounding)
	if err != nil {
		s.setErr(outputvalidation.New(model.OutputValidationResponseShape, err))
		return
	}
	s.assistant.Parts = grounded
	if len(s.assistant.Parts) > 0 {
		s.response.Content = []model.Message{s.assistant}
	}
	s.mu.Lock()
	s.canonical = &s.response
	s.mu.Unlock()
}

// discardSemanticOutput drops accumulated response content after an
// unadvertised tool name. The provider loop continues only to capture later
// usage and verify a normal finish reason.
func (s *geminiStreamer) discardSemanticOutput() {
	s.thoughtText.Reset()
	s.completionText.Reset()
	s.assistant.Parts = nil
	s.pendingCalls = nil
}

// handleFunctionCallPart validates and retains one functionCall part. Emission
// waits until stream end so later provider IDs are reserved before any missing
// ID receives a deterministic local value.
func (s *geminiStreamer) handleFunctionCallPart(part *genai.Part, prep *preparedRequest) error {
	if part.FunctionCall.Name == "" {
		return outputvalidation.New(
			model.OutputValidationToolIdentity,
			errors.New("vertex: streamed function call is missing its name"),
		)
	}
	name, ok := toolIdent(part.FunctionCall.Name, prep.provToCanon)
	if !ok {
		return outputvalidation.New(
			model.OutputValidationToolIdentity,
			fmt.Errorf(
				"vertex: translate streamed function call: %w",
				model.NewUnadvertisedToolNameError(part.FunctionCall.Name),
			),
		)
	}
	callID := part.FunctionCall.ID
	if err := s.retain(callID); err != nil {
		return err
	}
	if callID != "" {
		if err := s.callIDs.reserve(callID); err != nil {
			return outputvalidation.New(model.OutputValidationToolIdentity, err)
		}
	}
	payload, err := marshalArgs(part.FunctionCall.Args)
	if err != nil {
		return outputvalidation.New(model.OutputValidationToolArguments, err)
	}
	if err := s.retainBytes(len(payload)); err != nil {
		return err
	}
	signature, err := s.retainThoughtSignature(part.ThoughtSignature)
	if err != nil {
		return err
	}
	partIndex := len(s.assistant.Parts)
	s.pendingCalls = append(s.pendingCalls, pendingVertexCall{
		name:             string(name),
		payload:          payload,
		id:               callID,
		thoughtSignature: signature,
		partIndex:        partIndex,
	})
	s.assistant.Parts = append(s.assistant.Parts, nil)
	return nil
}

// flushFunctionCalls assigns missing IDs after the complete provider stream is
// known, then emits calls and fills their original response positions.
func (s *geminiStreamer) flushFunctionCalls() error {
	for _, pending := range s.pendingCalls {
		callID := pending.id
		if callID == "" {
			callID = s.callIDs.next()
			if err := s.retain(callID); err != nil {
				return err
			}
		}
		call := model.ToolCall{
			Name:             tools.Ident(pending.name),
			Payload:          pending.payload,
			ID:               callID,
			ThoughtSignature: pending.thoughtSignature,
		}
		s.assistant.Parts[pending.partIndex] = model.ToolUsePart{
			Name:             pending.name,
			Input:            pending.payload,
			ID:               callID,
			ThoughtSignature: pending.thoughtSignature,
		}
		s.emit(model.ToolCallChunk{ToolCall: call})
	}
	return nil
}

// handleThoughtPart accumulates thought text across Thought parts (mirrors
// the anthropic streamer's thinkingBuffer). Draft chunks are display-only
// and only emitted for text-bearing parts, so a signature-only part
// produces no empty-draft noise. When a signature arrives, the final
// ThinkingPart carries the full accumulated text plus the signature required
// by the canonical replay contract.
func (s *geminiStreamer) handleThoughtPart(part *genai.Part) error {
	if part.Text != "" {
		if err := s.retain(part.Text); err != nil {
			return err
		}
		s.thoughtText.WriteString(part.Text)
		draft := model.ThinkingPart{Text: part.Text, Index: s.thoughtIndex}
		s.emit(model.ThinkingChunk{
			Message: model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{draft}},
		})
	}
	if len(part.ThoughtSignature) > 0 {
		if s.thoughtText.Len() == 0 {
			return outputvalidation.New(
				model.OutputValidationResponseShape,
				errors.New("vertex: thinking signature is missing plaintext content"),
			)
		}
		signature, err := s.retainThoughtSignature(part.ThoughtSignature)
		if err != nil {
			return err
		}
		final := model.ThinkingPart{
			Text:      s.thoughtText.String(),
			Signature: signature,
			Index:     s.thoughtIndex,
			Final:     true,
		}
		s.assistant.Parts = append(s.assistant.Parts, final)
		s.emit(model.ThinkingChunk{
			Message: model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{final}},
		})
		s.thoughtText.Reset()
		s.thoughtIndex++
	}
	return nil
}

// handleTextPart emits assistant text, or a CompletionDelta preview when
// the request declared structured output: structured-output requests
// replace free-form assistant text with the typed completion contract (see
// runtime/agent/completion) — text deltas become CompletionDelta previews
// and the accumulated text is validated and emitted as one canonical
// Completion chunk once the stream ends, mirroring the bedrock adapter.
func (s *geminiStreamer) handleTextPart(part *genai.Part, prep *preparedRequest) error {
	if prep.structuredOutput != nil {
		if err := s.retain(part.Text); err != nil {
			return err
		}
		s.completionText.WriteString(part.Text)
		s.emit(model.CompletionDeltaChunk{Delta: model.CompletionDelta{
			Name:  prep.structuredOutput.Name,
			Delta: part.Text,
		}})
		return nil
	}
	if err := s.retain(part.Text); err != nil {
		return err
	}
	s.assistant.Parts = append(s.assistant.Parts, model.TextPart{Text: part.Text})
	s.emit(model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: part.Text}},
		},
	})
	return nil
}

// emit delivers a chunk to Recv, dropping it when the pump context is
// canceled so an abandoned stream never blocks the goroutine.
func (s *geminiStreamer) emit(ch model.Chunk) {
	select {
	case s.chunks <- ch:
	case <-s.ctx.Done():
	}
}

// setErr records the terminal pump error surfaced by Recv after chunks
// closes.
func (s *geminiStreamer) setErr(err error) {
	if err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) {
		if _, providerFailure := model.AsProviderError(err); !providerFailure {
			var validationErr *model.OutputValidationError
			if !errors.As(err, &validationErr) {
				usage := s.rejectedUsage
				err = s.contract.RejectProviderOutput(
					outputvalidation.RequiredKind(err),
					&usage,
					err,
				)
			}
		}
	}
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// finalStructuredCompletionPayload validates the fully-accumulated
// structured-output text as canonical JSON. Unlike tool-call payload
// fragments, typed completions use no fallbacks: empty or invalid JSON is a
// hard provider contract violation surfaced to the caller instead of a
// best-effort coercion.
func finalStructuredCompletionPayload(accumulated string) (rawjson.Message, error) {
	data := []byte(accumulated)
	if !json.Valid(data) {
		return nil, errors.New("structured completion payload is not valid JSON")
	}
	return rawjson.Message(data), nil
}

// retain charges provider text before a private stream accumulator grows.
func (s *geminiStreamer) retain(value string) error {
	return s.retainBytes(len(value))
}

// retainBytes charges one provider-controlled value and its retained byte
// length before private stream state grows.
func (s *geminiStreamer) retainBytes(size int) error {
	if s.retainedValues >= 100_000 {
		return outputvalidation.New(
			model.OutputValidationOutputBounds,
			errors.New("vertex: retained stream output exceeds 100000 values"),
		)
	}
	if size > 16<<20-s.retainedBytes {
		return outputvalidation.New(
			model.OutputValidationOutputBounds,
			errors.New("vertex: retained stream output exceeds 16777216 bytes"),
		)
	}
	s.retainedValues++
	s.retainedBytes += size
	return nil
}

// retainThoughtSignature charges base64 expansion before allocating the
// provider signature string retained for replay.
func (s *geminiStreamer) retainThoughtSignature(signature []byte) (string, error) {
	if len(signature) == 0 {
		return "", nil
	}
	if err := s.retainBytes(base64.StdEncoding.EncodedLen(len(signature))); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}
