// Package inmem runs accepted cancellation jobs independently of the requesting
// context. It mirrors job identity, observation, and retry rules without durability.
package inmem

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

type (
	cancellationWorkflow struct {
		opts    engine.ActivityOptions
		handler func(context.Context, engine.CancellationRequest) (bool, error)
	}
	cancellationJob struct {
		request engine.CancellationRequest
		digest  [32]byte
		done    chan struct{}
		err     error
	}
)

// RegisterCancellationWorkflow installs the handler and the interval between
// successful observations that still report unfinished cleanup.
func (e *eng) RegisterCancellationWorkflow(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, engine.CancellationRequest) (bool, error)) error {
	if name == "" || opts.Queue == "" || fn == nil || opts.RetryPolicy.InitialInterval <= 0 {
		return errors.New("cancellation workflow requires name, queue, handler and positive observation interval")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.workflows[name]; exists {
		return fmt.Errorf("workflow %q already registered", name)
	}
	if _, exists := e.cancellationWorkflows[name]; exists {
		return fmt.Errorf("workflow %q already registered", name)
	}
	if e.cancellationWorkflows == nil {
		e.cancellationWorkflows = make(map[string]cancellationWorkflow)
	}
	e.cancellationWorkflows[name] = cancellationWorkflow{opts: opts, handler: fn}
	return nil
}

// StartCancellationWorkflow retains the first exact request before starting an
// independent job. The caller receives acceptance while cleanup continues.
func (e *eng) StartCancellationWorkflow(ctx context.Context, name, queue string, request engine.CancellationRequest) error {
	id, digest, err := startrecipe.CancellationRecipe(name, queue, request)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	definition, found := e.cancellationWorkflows[name]
	if !found || definition.opts.Queue != queue {
		e.mu.Unlock()
		return fmt.Errorf("cancellation workflow %q is not registered on queue %q", name, queue)
	}
	if previous, exists := e.cancellationJobs[id]; exists {
		e.mu.Unlock()
		if previous.request.Reason != request.Reason {
			return &engine.CancellationConflictError{RunID: request.RunID, Reason: request.Reason}
		}
		if previous.digest != digest {
			return &engine.WorkflowStartConflictError{ID: id}
		}
		return nil
	}
	if e.cancellationJobs == nil {
		e.cancellationJobs = make(map[string]*cancellationJob)
	}
	job := &cancellationJob{request: request, digest: digest, done: make(chan struct{})}
	e.cancellationJobs[id] = job
	e.mu.Unlock()
	go runCancellationJob(context.WithoutCancel(ctx), definition, job)
	return nil
}

// runCancellationJob keeps pending observations distinct from failed attempts.
// Its recorded request is a value, so a handler cannot change later attempts.
func runCancellationJob(ctx context.Context, definition cancellationWorkflow, job *cancellationJob) {
	defer close(job.done)
	for {
		settled, err := executeActivityWithRetry(ctx, definition.opts.StartToCloseTimeout, definition.opts.RetryPolicy, func(attemptCtx context.Context) (bool, error) {
			return definition.handler(attemptCtx, job.request)
		})
		if err != nil || settled {
			job.err = err
			return
		}
		timer := time.NewTimer(definition.opts.RetryPolicy.InitialInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			job.err = ctx.Err()
			return
		case <-timer.C:
		}
	}
}
