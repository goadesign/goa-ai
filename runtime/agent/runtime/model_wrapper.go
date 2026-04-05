package runtime

import (
	"context"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// This file implements planner-scoped model client wrappers owned by the
// runtime. The planner client wrapper emits runtime planner events as model
// output is consumed, while the remaining wrappers apply cache/tool/tracing
// policy to raw model clients returned from PlannerContext.ModelClient.
//
// Critical invariants:
//   - Final tool calls are NOT emitted here; those are already surfaced to
//     planners via model.ChunkTypeToolCall and handled by the workflow loop.
//   - Tool call argument deltas MAY be emitted here as a best-effort UX signal
//     (model.ChunkTypeToolCallDelta). Consumers may ignore them; the canonical
//     tool payload remains the finalized tool call and the runtime tool_start.
//   - Emission occurs in the planner activity context to keep ledger writes
//     deterministic and scoped to the current turn.

// plannerModelClient wraps a raw model.Client and owns PlannerEvents emission
// for one planner turn.
type plannerModelClient struct {
	inner  model.Client
	events planner.PlannerEvents
}

// newPlannerModelClient returns a planner-scoped client that emits
// PlannerEvents for assistant text, thinking blocks, and usage.
func newPlannerModelClient(inner model.Client, events planner.PlannerEvents) planner.PlannerModelClient {
	if inner == nil {
		return nil
	}
	if events == nil {
		events = planner.NoopEvents()
	}
	return &plannerModelClient{
		inner:  inner,
		events: events,
	}
}

// Complete delegates to the inner client, then emits usage and assistant
// content (text + thinking) for the final response. If the adapter did not
// stamp model identity, the wrapper fills it from the request.
func (c *plannerModelClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	resp, err := c.inner.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	if (resp.Usage != model.TokenUsage{}) {
		stampModelIdentity(&resp.Usage, req)
		c.events.UsageDelta(ctx, resp.Usage)
	}
	for i := range resp.Content {
		msg := resp.Content[i]
		if msg.Role != model.ConversationRoleAssistant {
			continue
		}
		emitMessageContent(ctx, c.events, &msg)
	}
	return resp, nil
}

// Stream delegates to the inner client, drains the resulting stream through the
// planner helper, and returns the aggregated summary.
func (c *plannerModelClient) Stream(ctx context.Context, req *model.Request) (planner.StreamSummary, error) {
	st, err := c.inner.Stream(ctx, req)
	if err != nil {
		return planner.StreamSummary{}, err
	}
	return planner.ConsumeStream(ctx, st, req, c.events)
}

// cacheConfiguredClient wraps a model.Client and applies the agent CachePolicy
// to each request. It sets Request.Cache only when it is currently nil so
// explicit per-request CacheOptions take precedence over the agent defaults.
type cacheConfiguredClient struct {
	inner model.Client
	cache CachePolicy
}

func newCacheConfiguredClient(inner model.Client, cache CachePolicy) model.Client {
	if inner == nil {
		return nil
	}
	if !cache.AfterSystem && !cache.AfterTools {
		return inner
	}
	return &cacheConfiguredClient{
		inner: inner,
		cache: cache,
	}
}

func (c *cacheConfiguredClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	applyCachePolicy(req, c.cache)
	return c.inner.Complete(ctx, req)
}

func (c *cacheConfiguredClient) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	applyCachePolicy(req, c.cache)
	return c.inner.Stream(ctx, req)
}

// toolUnavailableConfiguredClient ensures the runtime-owned tool_unavailable tool
// is always present in tool-aware requests. Some providers require that any tool
// referenced in tool_use history appears in the current request tool list.
type toolUnavailableConfiguredClient struct {
	inner model.Client
}

func newToolUnavailableConfiguredClient(inner model.Client) model.Client {
	if inner == nil {
		return nil
	}
	return &toolUnavailableConfiguredClient{inner: inner}
}

func (c *toolUnavailableConfiguredClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	ensureToolUnavailableDefinition(req)
	return c.inner.Complete(ctx, req)
}

func (c *toolUnavailableConfiguredClient) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	ensureToolUnavailableDefinition(req)
	return c.inner.Stream(ctx, req)
}

// applyCachePolicy populates Request.Cache from the agent CachePolicy when no
// explicit CacheOptions are present on the request.
func applyCachePolicy(req *model.Request, cache CachePolicy) {
	if req == nil || req.Cache != nil {
		return
	}
	if !cache.AfterSystem && !cache.AfterTools {
		return
	}
	req.Cache = &model.CacheOptions{
		AfterSystem: cache.AfterSystem,
		AfterTools:  cache.AfterTools,
	}
}

func ensureToolUnavailableDefinition(req *model.Request) {
	if req == nil {
		return
	}
	if !requestMayReferenceTools(req) {
		return
	}
	name := tools.ToolUnavailable.String()
	for _, def := range req.Tools {
		if def != nil && def.Name == name {
			return
		}
	}
	req.Tools = append(req.Tools, toolUnavailableToolDefinition())
}

func requestMayReferenceTools(req *model.Request) bool {
	if req == nil {
		return false
	}
	if len(req.Tools) > 0 || req.ToolChoice != nil {
		return true
	}
	for _, msg := range req.Messages {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			switch part.(type) {
			case model.ToolUsePart, model.ToolResultPart:
				return true
			}
		}
	}
	return false
}

// emitMessageContent forwards assistant text and thinking parts from a message.
func emitMessageContent(ctx context.Context, ev planner.PlannerEvents, msg *model.Message) {
	if ev == nil || msg == nil || len(msg.Parts) == 0 {
		return
	}
	// Emit thinking parts first to preserve natural ordering semantics.
	emitThinkingParts(ctx, ev, msg)
	for _, p := range msg.Parts {
		if tp, ok := p.(model.TextPart); ok && tp.Text != "" {
			ev.AssistantChunk(ctx, tp.Text)
		}
	}
}

// stampModelIdentity fills Model and ModelClass on usage when the adapter left
// them empty. This ensures attribution is always present by the time usage
// reaches the hook bus, using the request as the fallback source.
func stampModelIdentity(usage *model.TokenUsage, req *model.Request) {
	if usage.Model == "" && req.Model != "" {
		usage.Model = req.Model
	}
	if usage.ModelClass == "" && req.ModelClass != "" {
		usage.ModelClass = req.ModelClass
	}
}

// emitThinkingParts forwards structured thinking blocks from a message.
func emitThinkingParts(ctx context.Context, ev planner.PlannerEvents, msg *model.Message) {
	if ev == nil || msg == nil || len(msg.Parts) == 0 {
		return
	}
	for _, p := range msg.Parts {
		if tp, ok := p.(model.ThinkingPart); ok {
			ev.PlannerThinkingBlock(ctx, tp)
		}
	}
}
