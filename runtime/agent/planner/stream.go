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

// ConsumeStream drains the provided streamer, emitting planner events for text and
// thinking chunks via the provided PlannerEvents. It returns the aggregated
// StreamSummary so planners can produce a final response or schedule tool calls.
// Callers are responsible for handling ToolCalls in the resulting summary.
func ConsumeStream(ctx context.Context, streamer model.Streamer, ev PlannerEvents) (StreamSummary, error) {
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
			if chunk.Message.Content == "" {
				continue
			}
			summary.Text += chunk.Message.Content
			ev.AssistantChunk(ctx, chunk.Message.Content)
		case model.ChunkTypeThinking:
			if chunk.Thinking == "" {
				continue
			}
			ev.PlannerThought(ctx, chunk.Thinking, map[string]string{"phase": "thinking"})
		case model.ChunkTypeToolCall:
			if chunk.ToolCall.Name == "" {
				continue
			}
			summary.ToolCalls = append(summary.ToolCalls, ToolRequest{
				Name:    chunk.ToolCall.Name,
				Payload: chunk.ToolCall.Payload,
			})
		case model.ChunkTypeUsage:
			if chunk.UsageDelta != nil {
				summary.Usage = addUsage(summary.Usage, *chunk.UsageDelta)
				ev.UsageDelta(ctx, *chunk.UsageDelta)
			}
		case model.ChunkTypeStop:
			summary.StopReason = chunk.StopReason
		}
	}

	if meta := streamer.Metadata(); meta != nil {
		if usage, ok := meta["usage"].(model.TokenUsage); ok {
			summary.Usage = addUsage(summary.Usage, usage)
			ev.UsageDelta(ctx, usage)
		}
	}

	return summary, nil
}

func addUsage(current, delta model.TokenUsage) model.TokenUsage {
	return model.TokenUsage{
		InputTokens:  current.InputTokens + delta.InputTokens,
		OutputTokens: current.OutputTokens + delta.OutputTokens,
		TotalTokens:  current.TotalTokens + delta.TotalTokens,
	}
}
