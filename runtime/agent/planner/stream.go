// Package planner defines helpers for streaming model responses into planner
// results and events. This file provides StreamSummary and ConsumeStream for
// planners that work with streaming model clients.
package planner

import (
	"context"
	"errors"
	"io"
	"strings"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// StreamSummary aggregates the outcome of a streaming LLM invocation.
	// Planners can use the collected text/tool calls when constructing their
	// PlanResult.
	StreamSummary struct {
		// Text accumulates assistant text chunks in the order they were received.
		Text string
		// ToolCalls captures tool invocations requested by the model (if any).
		ToolCalls []ToolRequest
		// Usage aggregates the reported token usage across usage chunks/metadata.
		Usage model.TokenUsage
		// StopReason records the provider stop reason when emitted.
		StopReason string

		source *model.Message
	}
)

// FinalResponse selects the canonical provider message for a terminal planner
// result. Streams that requested tools are not terminal.
func (s StreamSummary) FinalResponse() *FinalResponse {
	if s.source == nil || len(s.ToolCalls) > 0 {
		return nil
	}
	return &FinalResponse{Message: s.source}
}

// PlannerModelClient is a planner-scoped model client that owns PlannerEvents
// emission for the current turn.
//
// Contract:
//   - A returned client accepts exactly one Complete or Stream invocation for
//     the selected planner response. Use PlannerContext.ModelClient for probes.
//   - Complete records assistant text, thinking blocks, and usage with the
//     invocation selected by the planner result.
//   - Stream drains the underlying model stream and returns the aggregated
//     StreamSummary. The runtime publishes presentation after response selection.
//   - This interface intentionally does not expose model.Streamer so callers
//     cannot accidentally combine automatic event emission with ConsumeStream.
type PlannerModelClient interface {
	Complete(ctx context.Context, req *model.Request) (*model.Response, error)
	Stream(ctx context.Context, req *model.Request) (StreamSummary, error)
}

// ConsumeStream drains one model-validated stream and returns its aggregate so
// planners can produce a final response or schedule tool calls. The runtime
// journal owns presentation and usage events.
//
// Usage deltas emitted as chunks are the canonical streaming signal. When a
// stream emits none, the terminal canonical response supplies final usage.
func ConsumeStream(ctx context.Context, streamer *model.ValidatedStream) (summary StreamSummary, err error) {
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if streamer == nil {
		return summary, newOutputContractErrorWithOrigin(
			errors.New("model client returned a nil validated stream"),
			OutputContractOriginModel,
		)
	}
	defer func() {
		err = errors.Join(err, streamer.Close())
	}()
	var (
		sawUsageDelta bool
		text          strings.Builder
	)
	defer func() {
		summary.Text = text.String()
	}()

	for {
		chunk, recvErr := streamer.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				break
			}
			if model.IsStreamValidationError(recvErr) {
				var outputErr *OutputContractError
				if !errors.As(recvErr, &outputErr) {
					recvErr = newOutputContractErrorWithOrigin(
						recvErr,
						OutputContractOriginModel,
					)
				}
			}
			return summary, recvErr
		}
		switch actual := chunk.(type) {
		case model.TextChunk:
			var delta strings.Builder
			for _, p := range actual.Message.Parts {
				switch value := p.(type) {
				case model.TextPart:
					delta.WriteString(value.Text)
				case model.CitationsPart:
					delta.WriteString(value.Text)
				}
			}
			if delta.Len() == 0 {
				continue
			}
			value := delta.String()
			text.WriteString(value)
		case model.ThinkingChunk:
		case model.ToolCallChunk:
			// actual.ToolCall.ThoughtSignature (when present) is captured by the
			// runtime's model-client wrapper before this helper ever sees the
			// chunk; ToolRequest intentionally carries no signature field so
			// opaque provider state never transits this user-facing type.
			summary.ToolCalls = append(summary.ToolCalls, ToolRequest{
				Name:       actual.ToolCall.Name,
				Payload:    actual.ToolCall.Payload,
				ToolCallID: actual.ToolCall.ID,
			})
		case model.ToolCallDeltaChunk:
		case model.UsageChunk:
			sawUsageDelta = true
			summary.Usage = addUsage(summary.Usage, actual.Usage)
		case model.StopChunk:
			summary.StopReason = actual.Reason
		case model.CompletionChunk, model.CompletionDeltaChunk:
			return summary, NewOutputContractError(
				errors.New("planner: ConsumeStream received a completion chunk instead of a planner chunk"),
			)
		default:
			return summary, NewOutputContractError(
				errors.New("planner: ConsumeStream received an unsupported model chunk"),
			)
		}
	}

	response := streamer.Response()
	if !sawUsageDelta && response.Usage != (model.TokenUsage{}) {
		usage := response.Usage
		summary.Usage = addUsage(summary.Usage, usage)
	}
	if len(response.Content) == 0 {
		return summary, NewOutputContractError(
			errors.New("planner: complete stream response has no assistant message"),
		)
	}
	summary.source = &response.Content[len(response.Content)-1]

	return summary, nil
}

// addUsage combines usage deltas after the model stream boundary has verified
// that they all describe one model invocation.
func addUsage(current, delta model.TokenUsage) model.TokenUsage {
	if current.Model == "" {
		current.Model = delta.Model
	}
	if current.ModelClass == "" {
		current.ModelClass = delta.ModelClass
	}
	return model.TokenUsage{
		Model:            current.Model,
		ModelClass:       current.ModelClass,
		InputTokens:      current.InputTokens + delta.InputTokens,
		OutputTokens:     current.OutputTokens + delta.OutputTokens,
		TotalTokens:      current.TotalTokens + delta.TotalTokens,
		CacheReadTokens:  current.CacheReadTokens + delta.CacheReadTokens,
		CacheWriteTokens: current.CacheWriteTokens + delta.CacheWriteTokens,
	}
}
