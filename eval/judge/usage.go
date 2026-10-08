// Package judge records call-local, validated model usage for assessment reports.
// Each structural correction is a separate invocation. Missing provider usage
// remains unknown, and a later successful correction retains earlier failures.
package judge

import (
	"context"
	"sync"
	"time"

	"goa.design/goa-ai/eval"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	callRecorder struct {
		mu    sync.Mutex
		stage string
		calls []eval.ModelCall
	}
	observedProvider struct {
		model.Provider
		recorder *callRecorder
	}
	callObservation struct {
		recorder *callRecorder
		started  time.Time
		usage    *model.TokenUsage
		failure  error
	}
)

var _ model.ProviderCallObserver = (*observedProvider)(nil)

// PrepareClientCall creates one recorder before this provider invocation starts.
func (p *observedProvider) PrepareClientCall(ctx context.Context, _ *model.Request) (context.Context, model.ClientCallObserver, error) {
	return ctx, &callObservation{recorder: p.recorder, started: time.Now()}, nil
}

// ObserveClientComplete receives the result after canonical model validation.
func (o *callObservation) ObserveClientComplete(response *model.Response, err error) error {
	o.failure = err
	if response != nil {
		usage := response.Usage
		if usage.Model != "" || usage.TotalTokens > 0 || usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0 {
			o.usage = &usage
		}
	} else if err != nil {
		o.usage = model.UsageFromError(err)
	}
	return nil
}

// ObserveClientStream receives stream preparation errors. This judge always
// uses Complete, so it does not install a stream observer.
func (o *callObservation) ObserveClientStream(err error) (model.StreamObserver, error) {
	o.failure = err
	return nil, nil
}

// Finish retains one terminal provider invocation and its available usage.
func (o *callObservation) Finish(err error) error {
	if o.failure == nil {
		o.failure = err
	}
	call := eval.ModelCall{Stage: o.recorder.stage, Duration: time.Since(o.started), Usage: o.usage}
	if o.failure != nil {
		call.Error = o.failure.Error()
	}
	o.recorder.mu.Lock()
	o.recorder.calls = append(o.recorder.calls, call)
	o.recorder.mu.Unlock()
	return nil
}

// Abort ends preparation before the provider runs. It creates no paid-call
// record; the caller receives the preparation error through the judge.
func (o *callObservation) Abort(_ error) error {
	return nil
}

func (r *callRecorder) snapshot() []eval.ModelCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]eval.ModelCall(nil), r.calls...)
}
