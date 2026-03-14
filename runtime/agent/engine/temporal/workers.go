// Package temporal keeps worker lifecycle isolated from the engine's public
// API. The contract is simple: queue registration creates the worker if needed,
// starts it exactly once, and Close stops every worker the engine owns.
package temporal

import (
	"context"
	"sync"

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
	defer e.mu.Unlock()

	if bundle, ok := e.workers[queue]; ok {
		return bundle
	}

	bundle := &workerBundle{
		queue:  queue,
		worker: worker.New(e.client, queue, e.workerOpts),
		logger: e.logger,
	}
	e.workers[queue] = bundle
	bundle.start()
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
}

func (b *workerBundle) start() {
	b.startOnce.Do(func() {
		go func() {
			if err := b.worker.Run(worker.InterruptCh()); err != nil {
				b.logger.Error(context.Background(), "temporal worker exited", "queue", b.queue, "err", err)
			}
		}()
	})
}

func (b *workerBundle) stop() {
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
