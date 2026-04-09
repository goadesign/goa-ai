// Package temporal keeps worker lifecycle isolated from the engine's public
// API. Worker-mode engines stage queue handlers during registration and start
// polling only after the runtime seals registration.
package temporal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/telemetry"
)

// workerBundle owns one Temporal SDK worker for one task queue. Activation is
// synchronous: start blocks until the SDK worker starts successfully, the
// caller's context ends, or the bundle is closed.
type workerBundle struct {
	queue                   string
	worker                  worker.Worker
	logger                  telemetry.Logger
	activationRetryInterval time.Duration

	startMu  sync.Mutex
	started  bool
	fatalErr error

	closeOnce sync.Once
	closedCh  chan struct{}
}

const defaultActivationRetryInterval = time.Second

var errWorkerClosed = errors.New("worker bundle closed")

// workerForQueue returns the bundle for one task queue, creating it on first use.
func (e *Engine) workerForQueue(queue string) *workerBundle {
	if queue == "" {
		panic("temporal engine: no task queue configured")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	bundle, ok := e.workers[queue]
	if ok {
		return bundle
	}
	bundle = e.newWorkerBundle(queue)
	e.workers[queue] = bundle
	return bundle
}

// stopWorkers snapshots the current worker set under lock, then stops each
// worker outside the critical section so shutdown never holds the registry lock
// while Temporal drains in-flight tasks.
func (e *Engine) stopWorkers() {
	e.mu.Lock()
	bundles := make([]*workerBundle, 0, len(e.workers))
	for _, bundle := range e.workers {
		bundles = append(bundles, bundle)
	}
	e.mu.Unlock()
	for _, bundle := range bundles {
		bundle.stop()
	}
}

// newWorkerBundle constructs one queue-local worker bundle and wires fatal
// callbacks before any workflow or activity registrations happen.
func (e *Engine) newWorkerBundle(queue string) *workerBundle {
	bundle := &workerBundle{
		queue:                   queue,
		logger:                  e.logger,
		activationRetryInterval: e.activationRetryInterval,
		closedCh:                make(chan struct{}),
	}
	bundle.worker = e.workerFactory(e.client, queue, e.workerOptionsForQueue(bundle))
	return bundle
}

// workerOptionsForQueue derives queue-local SDK worker options and chains fatal
// callbacks so the engine can surface process-fatal failures without discarding
// caller-supplied hooks.
func (e *Engine) workerOptionsForQueue(bundle *workerBundle) worker.Options {
	opts := e.workerOpts
	upstream := opts.OnFatalError
	opts.OnFatalError = func(err error) {
		wrapped := bundle.handleFatal(err)
		if upstream != nil {
			upstream(wrapped)
		}
	}
	return opts
}

func (b *workerBundle) registerWorkflow(name string, fn any) {
	b.worker.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: name})
}

// typed registration reuses the same underlying RegisterWorkflowWithOptions
// since Temporal infers payload type from the function signature.
func (b *workerBundle) registerActivity(name string, fn any) {
	b.worker.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
}

// start activates the queue-local SDK worker. Once this returns nil, the worker
// is live and remains owned by the bundle until stop or a fatal worker error.
func (b *workerBundle) start(ctx context.Context) error {
	b.startMu.Lock()
	defer b.startMu.Unlock()

	if b.isClosed() {
		return fmt.Errorf("temporal worker %q is closed", b.queue)
	}
	if b.fatalErr != nil {
		return b.fatalErr
	}
	if b.started {
		return nil
	}
	if err := b.activate(ctx); err != nil {
		return err
	}
	if b.isClosed() {
		b.worker.Stop()
		return fmt.Errorf("temporal worker %q is closed", b.queue)
	}
	if b.fatalErr != nil {
		return b.fatalErr
	}
	b.started = true
	b.logger.Info(context.Background(), "temporal worker activated", "queue", b.queue)
	return nil
}

// stop terminates activation waiters and stops the SDK worker if it had already
// been activated. Fatal workers are not stopped twice.
func (b *workerBundle) stop() {
	b.closeOnce.Do(func() {
		close(b.closedCh)
		b.startMu.Lock()
		defer b.startMu.Unlock()
		if !b.started {
			return
		}
		b.started = false
		b.worker.Stop()
	})
}

// isStarted reports whether the bundle currently owns an activated SDK worker.
func (b *workerBundle) isStarted() bool {
	b.startMu.Lock()
	defer b.startMu.Unlock()
	return b.started
}

// activate retries worker.Start until activation succeeds, the caller's
// context ends, or the bundle is closed.
func (b *workerBundle) activate(ctx context.Context) error {
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return b.activationFailure(err, lastErr)
		}
		if b.isClosed() {
			return b.activationFailure(errWorkerClosed, lastErr)
		}
		if err := b.worker.Start(); err == nil {
			return nil
		} else {
			lastErr = err
			b.logger.Warn(
				context.Background(),
				"temporal worker activation failed; retrying",
				"queue",
				b.queue,
				"err",
				err,
			)
		}
		if err := b.waitForRetry(ctx); err != nil {
			return b.activationFailure(err, lastErr)
		}
	}
}

// activationFailure formats an activation error while preserving the last SDK
// startup failure when one exists.
func (b *workerBundle) activationFailure(cause error, lastErr error) error {
	if lastErr == nil {
		return fmt.Errorf("temporal worker %q activation failed: %w", b.queue, cause)
	}
	return fmt.Errorf("temporal worker %q activation did not complete: %w", b.queue, errors.Join(cause, lastErr))
}

// waitForRetry blocks until the next activation retry is allowed or activation
// must stop because the caller's context ended or the bundle was closed.
func (b *workerBundle) waitForRetry(ctx context.Context) error {
	timer := time.NewTimer(b.activationRetryInterval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.closedCh:
		return errWorkerClosed
	case <-timer.C:
		return nil
	}
}

// handleFatal records a fatal worker error exactly once for the bundle and
// returns the queue-qualified error delivered to callers.
func (b *workerBundle) handleFatal(err error) error {
	wrapped := fmt.Errorf("temporal worker %q reported fatal error: %w", b.queue, err)

	b.startMu.Lock()
	b.started = false
	if b.fatalErr == nil {
		b.fatalErr = wrapped
	}
	b.startMu.Unlock()

	b.logger.Error(context.Background(), "temporal worker fatal", "queue", b.queue, "err", err)
	return wrapped
}

// isClosed reports whether stop has already been requested for this bundle.
func (b *workerBundle) isClosed() bool {
	select {
	case <-b.closedCh:
		return true
	default:
		return false
	}
}
