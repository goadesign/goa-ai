package runtime

import (
	"context"
	"errors"
	"io"
	"sync"

	"goa.design/goa-ai/runtime/agent/internal/modelcall"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/internal/provenance"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

// This file wraps model clients used during one planner activity. Each response
// is checked and saved before tracing or planner code can read it. The runtime
// publishes only the response selected by an accepted planner result. The other
// wrappers add request caching and tracing.
//
// Rules:
//   - Complete tool calls go to the planner and are handled by the workflow.
//   - Partial tool arguments may be shown while streaming, but the completed
//     tool call remains the value that executes.
//   - User-visible events are created inside the current planner activity.
//   - Concurrent model calls are saved separately and matched to the exact
//     result returned by the planner. Call order never chooses a response.

type (
	// modelInvocationID identifies one saved model response during a planner
	// activity. It is never returned to planner or workflow code.
	modelInvocationID = provenance.Token

	// plannerModelClient wraps a raw model.Client and identifies the selected
	// invocation for one planner turn.
	plannerModelClient struct {
		inner model.Client
		mu    sync.Mutex
		used  bool
	}
)

// newPlannerModelClient returns a planner-scoped client whose provider output is
// published after the planner selects its response.
func newPlannerModelClient(inner model.Client) planner.PlannerModelClient {
	if inner == nil {
		return nil
	}
	return &plannerModelClient{inner: inner}
}

// Complete calls the model. The saved output is published only if the planner
// result selects this response and passes every runtime check.
func (c *plannerModelClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	if err := c.begin(); err != nil {
		return nil, err
	}
	resp, err := c.inner.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Stream delegates to the inner client, drains the resulting stream through the
// planner helper, and returns the aggregated summary.
func (c *plannerModelClient) Stream(ctx context.Context, req *model.Request) (planner.StreamSummary, error) {
	if err := c.begin(); err != nil {
		return planner.StreamSummary{}, err
	}
	st, err := c.inner.Stream(ctx, req)
	if err != nil {
		if st != nil {
			err = st.Finalize(err)
		}
		return planner.StreamSummary{}, err
	}
	return planner.ConsumeStream(ctx, st)
}

// begin reserves this client for the planner's one selected model call.
func (c *plannerModelClient) begin() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used {
		return errors.New("runtime: PlannerModelClient permits exactly one model invocation per planner turn")
	}
	c.used = true
	return nil
}

// cacheConfiguredProvider wraps a model.Provider and applies the agent CachePolicy
// to each request. It sets Request.Cache only when it is currently nil so
// explicit per-request CacheOptions take precedence over the agent defaults.
type cacheConfiguredProvider struct {
	inner model.Provider
	cache CachePolicy
}

func newCacheConfiguredClient(inner model.Client, cache CachePolicy) model.Client {
	if !cache.AfterSystem && !cache.AfterTools {
		return inner
	}
	return mustWrapModelClient(inner, func(provider model.Provider) model.Provider {
		return &cacheConfiguredProvider{
			inner: provider,
			cache: cache,
		}
	})
}

func (c *cacheConfiguredProvider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	applyCachePolicy(req, c.cache)
	response, err := c.inner.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (c *cacheConfiguredProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	applyCachePolicy(req, c.cache)
	return c.inner.Stream(ctx, req)
}

// modelInvocationSink saves separate model responses for one planner activity.
// Token usage includes every model call made during that activity.
//
// It is separate from planner.PlannerEvents so custom planners do not save
// responses themselves, and response storage does not depend on event wiring.
type modelInvocationSink interface {
	beginModelInvocation(model.ModelClass, context.CancelFunc) (modelInvocationID, error)
	designateModelInvocation(invocationID modelInvocationID) error
	stageRejectedModelOutput(
		invocationID modelInvocationID,
		evidence model.ResponseEvidence,
		err error,
	) error
	recordRejectedModelUsageTotal(invocationID modelInvocationID, usage model.TokenUsage) error
	recordRejectedModelUsageDelta(
		ctx context.Context,
		invocationID modelInvocationID,
		usage model.TokenUsage,
	) error
	recordValidatedModelResponse(invocationID modelInvocationID, response *model.Response) error
	recordModelChunk(ctx context.Context, invocationID modelInvocationID, chunk model.Chunk) error
	finalizeModelInvocation(invocationID modelInvocationID, outcome modelcall.Outcome) error
}

// modelInvocationProvider saves each model call after the opaque client has
// applied canonical validation and before planner code can read the response.
type modelInvocationProvider struct {
	inner      model.Provider
	sink       modelInvocationSink
	designated bool
	mu         sync.Mutex
	terminal   error
}

// modelInvocationCall records one validated result for the invocation started
// before provider execution.
type modelInvocationCall struct {
	provider     *modelInvocationProvider
	invocationID modelInvocationID
	ctx          context.Context
	streamer     *modelInvocationStreamer
	usage        []modelcall.Result
	staging      []modelcall.Result
}

// newModelInvocationClient adds response checking and storage. It returns inner
// unchanged when no response store was provided.
func newModelInvocationClient(inner model.Client, sink modelInvocationSink) model.Client {
	if sink == nil {
		return inner
	}
	return mustWrapModelClient(inner, func(provider model.Provider) model.Provider {
		return &modelInvocationProvider{inner: provider, sink: sink}
	})
}

// newDesignatedModelInvocationClient marks this client as the planner's one
// selected model call.
func newDesignatedModelInvocationClient(inner model.Client, sink modelInvocationSink) model.Client {
	if sink == nil {
		return inner
	}
	return mustWrapModelClient(inner, func(provider model.Provider) model.Provider {
		return &modelInvocationProvider{inner: provider, sink: sink, designated: true}
	})
}

// Complete forwards one raw provider result. The opaque client calls the
// matching modelInvocationCall only after canonical validation.
func (c *modelInvocationProvider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	return c.inner.Complete(ctx, req)
}

// Stream forwards one raw provider stream. The opaque client attaches the
// matching modelInvocationCall only after canonical stream setup.
func (c *modelInvocationProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	return c.inner.Stream(ctx, req)
}

// PrepareClientCall creates the durable invocation before provider execution.
func (c *modelInvocationProvider) PrepareClientCall(
	ctx context.Context,
	req *model.Request,
) (context.Context, model.ClientCallObserver, error) {
	if terminal := c.terminalError(); terminal != nil {
		return ctx, nil, terminal
	}
	invocationCtx, cancel := context.WithCancel(ctx)
	invocationID, err := c.sink.beginModelInvocation(req.ModelClass, cancel)
	if err != nil {
		cancel()
		return ctx, nil, err
	}
	if c.designated {
		if err := c.sink.designateModelInvocation(invocationID); err != nil {
			cleanupErr := c.sink.finalizeModelInvocation(invocationID, modelcall.Outcome{
				ProviderCall: modelcall.Result{Called: true, Err: err},
				Context:      modelcall.Result{Called: true, Err: invocationCtx.Err()},
				Framework:    modelcall.Result{Called: true, Err: err},
			})
			return ctx, nil, errors.Join(err, cleanupErr)
		}
	}
	return invocationCtx, &modelInvocationCall{
		provider:     c,
		invocationID: invocationID,
		ctx:          invocationCtx,
	}, nil
}

// ObserveClientComplete saves one validated unary response or its precise
// output rejection.
func (c *modelInvocationCall) ObserveClientComplete(response *model.Response, err error) error {
	if err != nil {
		var validationErr *model.OutputValidationError
		if errors.As(err, &validationErr) {
			return c.observeRejectedModelOutput(validationErr)
		}
		return nil
	}
	if err := c.provider.sink.recordValidatedModelResponse(c.invocationID, response); err != nil {
		return outputcontract.NewWithOrigin(err, planner.OutputContractOriginPlanner)
	}
	return nil
}

// observeRejectedModelOutput journals one lower-boundary rejection and returns
// the additive model-origin classification understood by workflow engines.
func (c *modelInvocationCall) observeRejectedModelOutput(validationErr *model.OutputValidationError) error {
	var usageErr error
	if usage := validationErr.Usage(); usage != nil {
		usageErr = c.provider.sink.recordRejectedModelUsageTotal(c.invocationID, *usage)
		c.usage = append(c.usage, modelcall.Result{Called: true, Err: usageErr})
	}
	var outputErr error = outputcontract.NewWithOrigin(
		validationErr,
		planner.OutputContractOriginModel,
	)
	recordErr := c.provider.sink.stageRejectedModelOutput(
		c.invocationID,
		validationErr.Evidence(),
		outputErr,
	)
	c.staging = append(c.staging, modelcall.Result{Called: true, Err: recordErr})
	c.provider.latchTerminalError(outputErr)
	return errors.Join(outputErr, usageErr, recordErr)
}

// ObserveClientStream returns journaling behavior for one validated stream or
// records its setup rejection. The model client attaches returned behavior to
// the exact stream; the caller receives through cancellation or completion,
// then finalizes it.
func (c *modelInvocationCall) ObserveClientStream(err error) (model.StreamObserver, error) {
	if err != nil {
		var validationErr *model.OutputValidationError
		if errors.As(err, &validationErr) {
			return nil, c.observeRejectedModelOutput(validationErr)
		}
		return nil, nil
	}
	journaled := &modelInvocationStreamer{
		call: c,
	}
	c.streamer = journaled
	return journaled, nil
}

// Finish observes the frozen pre-finisher error. The model client invokes
// FinalizeModelCall only after every prepared Finish callback has returned.
func (c *modelInvocationCall) Finish(err error) error {
	var outputErr *planner.OutputContractError
	if errors.As(err, &outputErr) {
		c.provider.latchTerminalError(err)
	}
	return nil
}

// FinalizeModelCall freezes the complete model-client outcome in the activity
// journal after every prepared Finish callback has run.
func (c *modelInvocationCall) FinalizeModelCall(outcome modelcall.Outcome) error {
	outcome.Usage = append(outcome.Usage, c.usage...)
	outcome.Staging = append(outcome.Staging, c.staging...)
	outcome.Context = modelcall.Result{Called: true, Err: c.ctx.Err()}
	return c.provider.sink.finalizeModelInvocation(c.invocationID, outcome)
}

// Abort releases invocation state when a later observer cannot prepare.
func (*modelInvocationCall) Abort(error) error {
	return nil
}

// terminalError returns the first terminal output failure observed by this
// planner-scoped client.
func (c *modelInvocationProvider) terminalError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminal
}

// latchTerminalError prevents another inference after malformed model output.
func (c *modelInvocationProvider) latchTerminalError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminal == nil {
		c.terminal = err
	}
}

type (
	// modelInvocationStreamer records output from the exact model-validated
	// stream and saves its complete response when the stream ends. A rejected
	// output stays staged until the planner activity inspects the frozen result
	// from every provider, observer, finisher, usage, and context phase.
	modelInvocationStreamer struct {
		call        *modelInvocationCall
		response    *model.Response
		finished    bool
		terminalErr error
	}
)

// ObserveStreamRecv records one result from the exact validated stream. It may
// stop the stream but cannot replace a chunk or response.
func (s *modelInvocationStreamer) ObserveStreamRecv(observation model.StreamObservation) error {
	if s.finished {
		return s.terminalErr
	}
	err := observation.Err
	if err != nil {
		if validationErr, ok := exactModelOutputValidation(err); ok {
			outputErr := outputcontract.NewWithOrigin(
				validationErr,
				planner.OutputContractOriginModel,
			)
			s.call.provider.latchTerminalError(outputErr)
			var recordingErr error
			if observation.RejectedUsageDelta != nil {
				err := s.call.provider.sink.recordRejectedModelUsageDelta(
					s.call.ctx,
					s.call.invocationID,
					*observation.RejectedUsageDelta,
				)
				s.call.usage = append(s.call.usage, modelcall.Result{Called: true, Err: err})
				recordingErr = errors.Join(recordingErr, err)
			}
			if observation.RejectedUsageTotal != nil {
				err := s.call.provider.sink.recordRejectedModelUsageTotal(
					s.call.invocationID,
					*observation.RejectedUsageTotal,
				)
				s.call.usage = append(s.call.usage, modelcall.Result{Called: true, Err: err})
				recordingErr = errors.Join(recordingErr, err)
			}
			stageErr := s.call.provider.sink.stageRejectedModelOutput(
				s.call.invocationID,
				observation.ResponseEvidence,
				outputErr,
			)
			s.call.staging = append(s.call.staging, modelcall.Result{Called: true, Err: stageErr})
			err = errors.Join(outputErr, recordingErr, stageErr)
		}
		if modelcall.Exact(observation.Err, io.EOF) {
			if observation.Response == nil {
				var outputErr error = outputcontract.NewWithOrigin(
					errors.New("model stream ended without a complete response"),
					planner.OutputContractOriginModel,
				)
				stageErr := s.call.provider.sink.stageRejectedModelOutput(
					s.call.invocationID,
					model.ResponseEvidence{},
					outputErr,
				)
				s.call.staging = append(s.call.staging, modelcall.Result{Called: true, Err: stageErr})
				return s.finish(errors.Join(outputErr, stageErr))
			}
			if err := s.call.provider.sink.recordValidatedModelResponse(
				s.call.invocationID,
				observation.Response,
			); err != nil {
				return s.finish(outputcontract.NewWithOrigin(
					err,
					planner.OutputContractOriginPlanner,
				))
			}
			s.response = observation.Response
			if err := s.finish(nil); err != nil {
				return err
			}
		} else {
			if _, ok := exactModelOutputValidation(observation.Err); !ok {
				return s.finish(nil)
			}
			return s.finish(err)
		}
		return nil
	}
	if err := s.call.provider.sink.recordModelChunk(
		s.call.ctx,
		s.call.invocationID,
		observation.Chunk,
	); err != nil {
		if _, ok := observation.Chunk.(model.UsageChunk); ok {
			s.call.usage = append(s.call.usage, modelcall.Result{Called: true, Err: err})
		}
		return s.finish(err)
	}
	if _, ok := observation.Chunk.(model.UsageChunk); ok {
		s.call.usage = append(s.call.usage, modelcall.Result{Called: true})
	}
	return nil
}

// ObserveStreamClose checks that Recv reached a terminal result. The model
// client supplies the complete provider and observer result to Finish after
// every close callback has returned.
func (s *modelInvocationStreamer) ObserveStreamClose(error) error {
	if !s.finished {
		return s.finish(outputcontract.NewWithOrigin(
			errors.New("planner closed model stream before EOF"),
			planner.OutputContractOriginPlanner,
		))
	}
	return nil
}

// finish records this runtime observer's result exactly once. The model client
// separately records every other observer and finisher result.
func (s *modelInvocationStreamer) finish(err error) error {
	if s.finished {
		return s.terminalErr
	}
	s.finished = true
	s.terminalErr = err
	return err
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

// mustWrapModelClient installs trusted runtime middleware beneath the only
// canonical validation boundary. Runtime registration has already established
// the opaque Client invariant, so a wrapping failure is a programming error.
func mustWrapModelClient(inner model.Client, wrap func(model.Provider) model.Provider) model.Client {
	client, err := model.WrapClient(inner, wrap)
	if err != nil {
		panic(err)
	}
	return client
}
