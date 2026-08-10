package runtime

import (
	"context"
	"errors"
	"io"
	"sync"

	"goa.design/goa-ai/runtime/agent/internal/provenance"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

// This file implements planner-scoped model client wrappers owned by the
// runtime. Model output is recorded per invocation until the planner selects a
// response, while the remaining wrappers apply cache, tracing, and
// model-invocation policy to raw model clients returned from
// PlannerContext.ModelClient.
//
// Critical invariants:
//   - Final tool calls are NOT emitted here; those are already surfaced to
//     planners via model.ChunkTypeToolCall and handled by the workflow loop.
//   - Tool call argument deltas MAY be emitted here as a best-effort UX signal
//     (model.ChunkTypeToolCallDelta). Consumers may ignore them; the canonical
//     tool payload remains the finalized tool call and the runtime tool_start.
//   - Emission occurs in the planner activity context to keep hook writes
//     deterministic and scoped to the current turn.
//   - modelInvocationClient captures every response in an isolated runtime
//     candidate before planner-facing types exist. The runtime later identifies
//     the candidate from exact model-facing tool calls or opaque terminal
//     provenance; call order never determines the durable transcript.

type (
	// modelInvocationID identifies one runtime-owned response candidate within
	// a planner activity. It never crosses the planner or workflow boundary.
	modelInvocationID = provenance.Token

	// plannerModelClient wraps a raw model.Client and identifies the selected
	// invocation for one planner turn.
	plannerModelClient struct {
		inner  model.Client
		events planner.PlannerEvents
		mu     sync.Mutex
		used   bool
	}
)

// newPlannerModelClient returns a planner-scoped client whose provider output is
// published after the planner selects its response.
func newPlannerModelClient(inner model.Client, events planner.PlannerEvents) planner.PlannerModelClient {
	if inner == nil {
		return nil
	}
	if events == nil {
		panic("runtime: planner model client requires PlannerEvents")
	}
	return &plannerModelClient{
		inner:  inner,
		events: events,
	}
}

// Complete delegates to the inner client. The invocation journal publishes
// presentation only after the planner result selects this response.
func (c *plannerModelClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	if err := c.begin(); err != nil {
		return nil, err
	}
	resp, err := c.inner.Complete(ctx, req)
	if err == nil {
		stampModelIdentity(&resp.Usage, req)
	}
	return resp, err
}

// Stream delegates to the inner client, drains the resulting stream through the
// planner helper, and returns the aggregated summary.
func (c *plannerModelClient) Stream(ctx context.Context, req *model.Request) (planner.StreamSummary, error) {
	if err := c.begin(); err != nil {
		return planner.StreamSummary{}, err
	}
	st, err := c.inner.Stream(ctx, req)
	if err != nil {
		return planner.StreamSummary{}, err
	}
	return planner.ConsumeStream(ctx, st, req, c.events)
}

// begin reserves this planner client for its one selected invocation. Planners
// use PlannerContext.ModelClient for probes or retries before this call.
func (c *plannerModelClient) begin() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used {
		return errors.New("runtime: PlannerModelClient permits exactly one model invocation per planner turn")
	}
	c.used = true
	return nil
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

// modelInvocationSink owns isolated provider response candidates for one
// planner activity. Token usage remains activity-wide so rejected corrective
// attempts are still accounted for.
//
// The interface is intentionally unexported and separate from
// planner.PlannerEvents: custom planners never implement provider transcript
// persistence, and capture must not depend on which PlannerEvents value they
// pass to planner.ConsumeStream.
type modelInvocationSink interface {
	beginModelInvocation() modelInvocationID
	designateModelInvocation(invocationID modelInvocationID) error
	recordModelResponse(invocationID modelInvocationID, response *model.Response) error
	recordModelChunk(invocationID modelInvocationID, chunk model.Chunk) error
	finishModelInvocation(invocationID modelInvocationID, err error) error
}

// modelInvocationClient bounds tentative transcript state to individual model
// calls and captures provider-defined tool-call thought signatures before any
// planner-facing type is constructed.
type modelInvocationClient struct {
	inner      model.Client
	sink       modelInvocationSink
	designated bool
}

// newModelInvocationClient wraps inner with invocation tracking. It returns
// inner unchanged when sink is nil.
func newModelInvocationClient(inner model.Client, sink modelInvocationSink) model.Client {
	if inner == nil {
		return nil
	}
	if sink == nil {
		return inner
	}
	return &modelInvocationClient{inner: inner, sink: sink}
}

// newDesignatedModelInvocationClient captures a model call that is contractually
// the planner's selected invocation and may publish live presentation events.
func newDesignatedModelInvocationClient(inner model.Client, sink modelInvocationSink) model.Client {
	if inner == nil {
		return nil
	}
	if sink == nil {
		return inner
	}
	return &modelInvocationClient{inner: inner, sink: sink, designated: true}
}

// Complete starts a new tentative response, validates the provider result, and
// records its canonical transcript before returning to planner code.
func (c *modelInvocationClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	invocationID := c.sink.beginModelInvocation()
	if c.designated {
		if err := c.sink.designateModelInvocation(invocationID); err != nil {
			return nil, err
		}
	}
	resp, err := c.inner.Complete(ctx, req)
	if err != nil {
		return resp, c.sink.finishModelInvocation(invocationID, err)
	}
	stampModelIdentity(&resp.Usage, req)
	if err := c.sink.recordModelResponse(invocationID, resp); err != nil {
		return nil, c.sink.finishModelInvocation(invocationID, err)
	}
	if err := c.sink.finishModelInvocation(invocationID, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

// Stream starts a new tentative response and wraps the returned Streamer so
// every chunk is validated and the canonical response is captured.
func (c *modelInvocationClient) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	invocationID := c.sink.beginModelInvocation()
	if c.designated {
		if err := c.sink.designateModelInvocation(invocationID); err != nil {
			return nil, err
		}
	}
	st, err := c.inner.Stream(ctx, req)
	if err != nil {
		return nil, c.sink.finishModelInvocation(invocationID, err)
	}
	return &modelInvocationStreamer{
		inner:        st,
		sink:         c.sink,
		invocationID: invocationID,
		request:      req,
	}, nil
}

// modelInvocationStreamer validates every presentation chunk before exposing
// it to planner code and captures the canonical response at clean EOF.
type modelInvocationStreamer struct {
	inner        model.Streamer
	sink         modelInvocationSink
	invocationID modelInvocationID
	request      *model.Request
	finished     bool
}

func (s *modelInvocationStreamer) Recv() (model.Chunk, error) {
	ch, err := s.inner.Recv()
	if err != nil {
		s.finished = true
		if errors.Is(err, io.EOF) {
			response := s.inner.Response()
			if response == nil {
				err := errors.New("model stream ended without a canonical response")
				return nil, s.sink.finishModelInvocation(s.invocationID, err)
			}
			stampModelIdentity(&response.Usage, s.request)
			if err := s.sink.recordModelResponse(s.invocationID, response); err != nil {
				return nil, s.sink.finishModelInvocation(s.invocationID, err)
			}
			if err := s.sink.finishModelInvocation(s.invocationID, nil); err != nil {
				return nil, err
			}
		} else {
			return nil, s.sink.finishModelInvocation(s.invocationID, err)
		}
		return ch, err
	}
	if usage, ok := ch.(model.UsageChunk); ok {
		stampModelIdentity(&usage.Usage, s.request)
		ch = usage
	}
	if err := s.sink.recordModelChunk(s.invocationID, ch); err != nil {
		s.finished = true
		return nil, s.sink.finishModelInvocation(s.invocationID, err)
	}
	return ch, nil
}

func (s *modelInvocationStreamer) Close() error {
	var finishErr error
	if !s.finished {
		s.finished = true
		finishErr = s.sink.finishModelInvocation(s.invocationID, errors.New("model stream closed before EOF"))
	}
	return errors.Join(finishErr, s.inner.Close())
}

func (s *modelInvocationStreamer) Response() *model.Response { return s.inner.Response() }

// StreamEvents prevents planner helpers from publishing tentative provider
// output. The invocation journal publishes the selected response after planning.
func (s *modelInvocationStreamer) StreamEvents(planner.PlannerEvents) planner.PlannerEvents {
	return planner.NoopEvents()
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

// stampModelIdentity fills usage attribution from the request before the
// invocation journal accounts for the provider response.
func stampModelIdentity(usage *model.TokenUsage, req *model.Request) {
	if usage.Model == "" && req.Model != "" {
		usage.Model = req.Model
	}
	if usage.ModelClass == "" && req.ModelClass != "" {
		usage.ModelClass = req.ModelClass
	}
}
