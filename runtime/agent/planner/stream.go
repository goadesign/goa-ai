// Package planner defines helpers for streaming model responses into planner
// results and events. This file provides StreamSummary and ConsumeStream for
// planners that work with streaming model clients.
package planner

import (
	"context"
	"errors"
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
}

// PlannerModelClient is a planner-scoped model client that owns PlannerEvents
// emission for the current turn.
//
// Contract:
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
// Usage contract:
//   - Usage deltas emitted as chunks are the canonical streaming signal.
//   - Stream metadata usage is a fallback for adapters that only expose final
//     usage via Metadata().
//   - When both are present, metadata is not added again.
func ConsumeStream(ctx context.Context, streamer model.Streamer, req *model.Request, ev PlannerEvents) (StreamSummary, error) {
	var summary StreamSummary
	if streamer == nil {
		return summary, errors.New("nil streamer")
	}
	if ev == nil {
		return summary, errors.New("nil PlannerEvents")
	}
	defer func() {
		_ = streamer.Close()
	}()
	var sawUsageDelta bool

	for {
		chunk, err := streamer.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return summary, err
		}
		switch chunk.Type {
		case model.ChunkTypeText:
			if chunk.Message == nil || len(chunk.Message.Parts) == 0 {
				continue
			}
			// Concatenate text parts in the chunk in order
			var delta string
			for _, p := range chunk.Message.Parts {
				if tp, ok := p.(model.TextPart); ok && tp.Text != "" {
					delta += tp.Text
				}
			}
			if delta == "" {
				continue
			}
			summary.Text += delta
			ev.AssistantChunk(ctx, delta)
		case model.ChunkTypeThinking:
			// Emit structured thinking block when present.
			if chunk.Message != nil {
				for _, p := range chunk.Message.Parts {
					if tp, ok := p.(model.ThinkingPart); ok {
						ev.PlannerThinkingBlock(ctx, tp)
					}
				}
			}
		case model.ChunkTypeToolCall:
			if chunk.ToolCall.Name == "" {
				continue
			}
			summary.ToolCalls = append(summary.ToolCalls, ToolRequest{
				Name:       chunk.ToolCall.Name,
				Payload:    chunk.ToolCall.Payload,
				ToolCallID: chunk.ToolCall.ID,
			})
		case model.ChunkTypeToolCallDelta:
			if chunk.ToolCallDelta == nil || chunk.ToolCallDelta.ID == "" || chunk.ToolCallDelta.Delta == "" {
				continue
			}
			ev.ToolCallArgsDelta(ctx, chunk.ToolCallDelta.ID, chunk.ToolCallDelta.Name, chunk.ToolCallDelta.Delta)
		case model.ChunkTypeUsage:
			if chunk.UsageDelta != nil {
				sawUsageDelta = true
				stampUsageModelIdentity(chunk.UsageDelta, req)
				summary.Usage = addUsage(summary.Usage, *chunk.UsageDelta)
				ev.UsageDelta(ctx, *chunk.UsageDelta)
			}
		case model.ChunkTypeStop:
			summary.StopReason = chunk.StopReason
		}
	}

	if !sawUsageDelta {
		if meta := streamer.Metadata(); meta != nil {
			if usage, ok := meta["usage"].(model.TokenUsage); ok {
				stampUsageModelIdentity(&usage, req)
				summary.Usage = addUsage(summary.Usage, usage)
				ev.UsageDelta(ctx, usage)
			}
		}
	}

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
