package runtime

import (
	"context"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// This file implements a per-turn model.Client decorator that emits runtime
// planner events as model output is consumed. The wrapper:
//   - Streams: forwards assistant text, thinking blocks, and usage deltas
//     to PlannerEvents so the runtime ledger captures them automatically.
//   - Unary: emits assistant text/thinking from the final response and
//     reports usage when available.
//
// Critical invariants:
//   - Final tool calls are NOT emitted here; those are already surfaced to
//     planners via model.ChunkTypeToolCall and handled by the workflow loop.
//   - Tool call argument deltas MAY be emitted here as a best-effort UX signal
//     (model.ChunkTypeToolCallDelta). Consumers may ignore them; the canonical
//     tool payload remains the finalized tool call and the runtime tool_start.
//   - Emission occurs in the planner activity context to keep ledger writes
//     deterministic and scoped to the current turn.

// eventDecoratedClient wraps a model.Client and forwards stream/unary content to
// PlannerEvents so the runtime ledger captures thinking/text/usage automatically.
type eventDecoratedClient struct {
	inner  model.Client
	events planner.PlannerEvents
}

// newEventDecoratedClient returns a client wrapper that emits PlannerEvents for
// assistant text, thinking blocks, and usage. When inner or events is nil, the
// inner client is returned unchanged.
func newEventDecoratedClient(inner model.Client, events planner.PlannerEvents) model.Client {
	if inner == nil || events == nil {
		return inner
	}
	return &eventDecoratedClient{
		inner:  inner,
		events: events,
	}
}

// Complete delegates to the inner client, then emits usage and assistant
// content (text + thinking) for the final response. If the adapter did not
// stamp model identity, the wrapper fills it from the request.
func (c *eventDecoratedClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
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

// Stream delegates to the inner client and returns a Streamer whose Recv()
// emits PlannerEvents for assistant text, thinking blocks, and usage. Model
// identity from the request is captured so usage chunks can be attributed.
func (c *eventDecoratedClient) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	st, err := c.inner.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &eventStream{
		inner:  st,
		events: c.events,
		ctx:    ctx,
		req:    req,
	}, nil
}

// eventStream decorates a model.Streamer to emit PlannerEvents for chunks.
// It carries the original request so usage chunks can be attributed to the
// requested model when the adapter did not stamp identity.
type eventStream struct {
	inner  model.Streamer
	events planner.PlannerEvents
	ctx    context.Context
	req    *model.Request
}

// Recv forwards events for text/thinking/usage chunks.
//
// Contract:
//   - Final tool calls are passed through untouched for the planner/workflow to
//     handle.
//   - Tool call argument deltas are forwarded as best-effort PlannerEvents for
//     streaming UX; consumers may ignore them.
func (s *eventStream) Recv() (model.Chunk, error) {
	ch, err := s.inner.Recv()
	if err != nil {
		return ch, err
	}
	switch ch.Type {
	case model.ChunkTypeToolCallDelta:
		if ch.ToolCallDelta != nil {
			s.events.ToolCallArgsDelta(s.ctx, ch.ToolCallDelta.ID, ch.ToolCallDelta.Name, ch.ToolCallDelta.Delta)
		}
	case model.ChunkTypeText:
		if ch.Message != nil {
			emitMessageContent(s.ctx, s.events, ch.Message)
		}
	case model.ChunkTypeThinking:
		// Prefer structured thinking parts when present; otherwise use delta text.
		if ch.Message != nil {
			emitThinkingParts(s.ctx, s.events, ch.Message)
		} else if ch.Thinking != "" {
			s.events.PlannerThinkingBlock(s.ctx, model.ThinkingPart{Text: ch.Thinking})
		}
	case model.ChunkTypeUsage:
		if ch.UsageDelta != nil {
			stampModelIdentity(ch.UsageDelta, s.req)
			s.events.UsageDelta(s.ctx, *ch.UsageDelta)
		}
	}
	return ch, nil
}

func (s *eventStream) Close() error {
	return s.inner.Close()
}

func (s *eventStream) Metadata() map[string]any {
	return s.inner.Metadata()
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
