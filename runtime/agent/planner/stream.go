// Package planner defines helpers for streaming model responses into planner
// results and events. This file provides StreamSummary and ConsumeStream for
// planners that work with streaming model clients.
package planner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"goa.design/goa-ai/runtime/agent/model"
)

// StreamSummary aggregates the outcome of a streaming LLM invocation. Planners
// can use the collected text/tool calls when constructing their PlanResult.
type StreamSummary struct {
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
//   - Complete emits assistant text, thinking blocks, and usage from the final
//     response before returning it.
//   - Stream drains the underlying model stream, emits PlannerEvents, and
//     returns the aggregated StreamSummary.
//   - This interface intentionally does not expose model.Streamer so callers
//     cannot accidentally combine automatic event emission with ConsumeStream.
type PlannerModelClient interface {
	Complete(ctx context.Context, req *model.Request) (*model.Response, error)
	Stream(ctx context.Context, req *model.Request) (StreamSummary, error)
}

// ConsumeStream drains the provided streamer, emitting planner events for text and
// thinking chunks via the provided PlannerEvents. It returns the aggregated
// StreamSummary so planners can produce a final response or schedule tool calls.
// Callers are responsible for handling ToolCalls in the resulting summary.
//
// Usage deltas emitted as chunks are the canonical streaming signal. When a
// stream emits none, the terminal canonical response supplies final usage.
func ConsumeStream(ctx context.Context, streamer model.Streamer, req *model.Request, ev PlannerEvents) (summary StreamSummary, err error) {
	if streamer == nil {
		return summary, errors.New("nil streamer")
	}
	if ev == nil {
		return summary, errors.New("nil PlannerEvents")
	}
	defer func() {
		err = errors.Join(err, streamer.Close())
	}()
	var sawUsageDelta bool
	var stopped bool
	toolCallIDs := make(map[string]struct{})

	for {
		chunk, recvErr := streamer.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				break
			}
			return summary, recvErr
		}
		if err := model.ValidateChunk(chunk); err != nil {
			return summary, fmt.Errorf("planner: invalid model chunk: %w", err)
		}
		if stopped {
			return summary, fmt.Errorf("planner: model stream emitted %q after stop", chunk.Kind())
		}
		switch actual := chunk.(type) {
		case model.TextChunk:
			// Concatenate text parts in the chunk in order
			var delta string
			for _, p := range actual.Message.Parts {
				if tp, ok := p.(model.TextPart); ok && tp.Text != "" {
					delta += tp.Text
				}
			}
			if delta == "" {
				continue
			}
			summary.Text += delta
			ev.AssistantChunk(ctx, delta)
		case model.ThinkingChunk:
			for _, p := range actual.Message.Parts {
				if tp, ok := p.(model.ThinkingPart); ok {
					ev.PlannerThinkingBlock(ctx, tp)
				}
			}
		case model.ToolCallChunk:
			if _, exists := toolCallIDs[actual.ToolCall.ID]; exists {
				return summary, fmt.Errorf("planner: model stream repeated finalized tool call %q", actual.ToolCall.ID)
			}
			toolCallIDs[actual.ToolCall.ID] = struct{}{}
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
			if _, finalized := toolCallIDs[actual.Delta.ID]; finalized {
				return summary, fmt.Errorf(
					"planner: model stream emitted tool call delta after finalized call %q",
					actual.Delta.ID,
				)
			}
			ev.ToolCallArgsDelta(ctx, actual.Delta.ID, actual.Delta.Name, actual.Delta.Delta)
		case model.UsageChunk:
			sawUsageDelta = true
			stampUsageModelIdentity(&actual.Usage, req)
			summary.Usage = addUsage(summary.Usage, actual.Usage)
			ev.UsageDelta(ctx, actual.Usage)
		case model.StopChunk:
			stopped = true
			summary.StopReason = actual.Reason
		case model.CompletionChunk, model.CompletionDeltaChunk:
			return summary, errors.New("planner: ConsumeStream does not accept typed completion chunks; use completion.Stream")
		default:
			return summary, errors.New("planner: ConsumeStream received an unsupported model chunk")
		}
	}

	response := streamer.Response()
	if err := model.ValidateResponse(response); err != nil {
		return summary, fmt.Errorf("planner: invalid canonical response: %w", err)
	}
	if !stopped {
		return summary, errors.New("planner: model stream ended without stop chunk")
	}
	if summary.StopReason != response.StopReason {
		return summary, fmt.Errorf(
			"planner: stream stop reason %q does not match canonical response %q",
			summary.StopReason,
			response.StopReason,
		)
	}
	responseCalls := response.ToolCalls()
	if len(responseCalls) != len(summary.ToolCalls) {
		return summary, fmt.Errorf(
			"planner: stream emitted %d tool calls but canonical response contains %d",
			len(summary.ToolCalls),
			len(responseCalls),
		)
	}
	for index, responseCall := range responseCalls {
		streamCall := summary.ToolCalls[index]
		if responseCall.ID != streamCall.ToolCallID ||
			responseCall.Name != streamCall.Name ||
			!bytes.Equal(responseCall.Payload, streamCall.Payload) {
			return summary, fmt.Errorf("planner: stream tool call %d does not match canonical response", index)
		}
	}
	if !sawUsageDelta && response.Usage != (model.TokenUsage{}) {
		usage := response.Usage
		stampUsageModelIdentity(&usage, req)
		summary.Usage = addUsage(summary.Usage, usage)
		ev.UsageDelta(ctx, usage)
	}
	if sawUsageDelta {
		responseUsage := response.Usage
		stampUsageModelIdentity(&responseUsage, req)
		if summary.Usage != responseUsage {
			return summary, errors.New("planner: stream usage deltas do not match canonical response usage")
		}
	}
	summary.source = &response.Content[len(response.Content)-1]

	return summary, nil
}
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

func stampUsageModelIdentity(usage *model.TokenUsage, req *model.Request) {
	if usage == nil || req == nil {
		return
	}
	if usage.Model == "" && req.Model != "" {
		usage.Model = req.Model
	}
	if usage.ModelClass == "" && req.ModelClass != "" {
		usage.ModelClass = req.ModelClass
	}
}
