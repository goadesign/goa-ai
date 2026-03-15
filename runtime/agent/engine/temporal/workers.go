// Package temporal keeps worker lifecycle isolated from the engine's public
// API. Worker-mode engines stage queue handlers during registration and start
// polling only after the runtime seals registration.
package temporal

import (
	"context"
	"sync"
	"sync/atomic"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/telemetry"
)

func (e *Engine) workerForQueue(queue string) *workerBundle {
	if queue == "" {
		panic("temporal engine: no task queue configured")
	}

	e.mu.Lock()
	bundle, ok := e.workers[queue]
	if !ok {
		bundle = &workerBundle{
			queue:  queue,
			worker: worker.New(e.client, queue, e.workerOpts),
			logger: e.logger,
		}
		e.workers[queue] = bundle
	}
	shouldStart := e.registrationSealed
	e.mu.Unlock()
	if shouldStart {
		bundle.start()
	}
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

type workerBundle struct {
	queue  string
	worker worker.Worker
	logger telemetry.Logger

	startOnce sync.Once
	started   atomic.Bool
}

func (b *workerBundle) start() {
	b.startOnce.Do(func() {
		b.started.Store(true)
		go func() {
			if err := b.worker.Run(worker.InterruptCh()); err != nil {
				b.logger.Error(context.Background(), "temporal worker exited", "queue", b.queue, "err", err)
			}
		}()
	})
}

func (b *workerBundle) stop() {
	if !b.started.Load() {
		return
	}
	b.worker.Stop()
}

func (b *workerBundle) registerWorkflow(name string, fn any) {
	b.worker.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: name})
}

// typed registration reuses the same underlying RegisterWorkflowWithOptions
// since Temporal infers payload type from the function signature.
func (b *workerBundle) registerActivity(name string, fn any) {
	b.worker.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
}
