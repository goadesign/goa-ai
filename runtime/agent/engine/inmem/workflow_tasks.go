package inmem

// Workflow callbacks enter a private queue. Only the workflow goroutine removes
// them, at engine wait points and before completion, so callbacks and the main
// handler can share ordinary Go state. Activities continue on separate goroutines.

import (
	"context"
	"errors"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	workflowTask struct {
		apply  func() error
		result *future[struct{}]
		ready  func() bool
	}

	workflowTasks struct {
		mu     sync.Mutex
		queue  []workflowTask
		wake   chan struct{}
		closed bool
	}
)

func newWorkflowTasks() *workflowTasks {
	return &workflowTasks{wake: make(chan struct{}, 1)}
}

// enqueue retains work until the workflow goroutine applies it. Closing the
// workflow rejects new work instead of acknowledging an undelivered callback.
func (s *workflowTasks) enqueue(task workflowTask) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		task.result.complete(struct{}{}, engine.ErrWorkflowCompleted)
		return
	}
	s.queue = append(s.queue, task)
	select {
	case s.wake <- struct{}{}:
	default:
	}
	s.mu.Unlock()
}

func (s *workflowTasks) dispatch() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		task := s.queue[0]
		if task.ready != nil && !task.ready() {
			s.mu.Unlock()
			return
		}
		s.queue[0] = workflowTask{}
		s.queue = s.queue[1:]
		s.mu.Unlock()
		task.result.complete(struct{}{}, task.apply())
	}
}

// close rejects queued callbacks after a terminal result. No callback can run
// on a different goroutine after the main handler has left this attempt.
func (s *workflowTasks) close(cause error) {
	s.mu.Lock()
	s.closed = true
	queued := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, task := range queued {
		task.result.complete(struct{}{}, cause)
	}
}

// wait applies all queued callbacks before testing readiness or a deadline.
// The optional tick supports Await predicates whose activity futures complete
// outside this queue. Callers use their own cancellation scope.
func (w *wfCtx) wait(ctx context.Context, ready func() bool, completed <-chan struct{}) error {
	control := w.executionControl()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		control.tasks.dispatch()
		if err := ctx.Err(); err != nil {
			return err
		}
		if ready() {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-control.tasks.wake:
		case <-completed:
		case <-ticker.C:
		}
	}
}

// finish accepts queued callbacks and drains every outgoing obligation,
// including obligations created by a callback during this drain. Cancellation
// ends the wait without replacing the handler's first terminal error.
func (w *wfCtx) finish(handlerErr error) error {
	control := w.executionControl()
	terminal := errors.Is(handlerErr, context.Canceled) || errors.Is(handlerErr, context.DeadlineExceeded)
	for {
		control.tasks.dispatch()
		for len(control.pending) > 0 && w.ctx.Err() == nil && !terminal {
			pending := control.pending[0]
			control.pending = control.pending[1:]
			if _, err := pending.Get(w.ctx); err != nil && handlerErr == nil {
				handlerErr = err
			}
		}
		control.tasks.mu.Lock()
		if len(control.tasks.queue) == 0 || w.ctx.Err() != nil || terminal {
			control.tasks.closed = true
			control.tasks.mu.Unlock()
			break
		}
		control.tasks.mu.Unlock()
	}
	cause := handlerErr
	if cause == nil {
		cause = w.ctx.Err()
	}
	if cause == nil {
		cause = engine.ErrWorkflowCompleted
	}
	control.tasks.close(cause)
	return handlerErr
}

func (f *future[T]) complete(result T, err error) {
	f.once.Do(func() {
		f.result, f.err = result, err
		if err != nil && f.onFailure != nil {
			f.onFailure(err)
		}
		close(f.ready)
	})
}

// runBlockingActivity lets the workflow accept callbacks while an activity is
// running. The activity still owns its result and must honor its context.
func runBlockingActivity[T any](w *wfCtx, execute func() (T, error)) (T, error) {
	f := &future[T]{ready: make(chan struct{}), workflow: w}
	go func() {
		result, err := execute()
		f.complete(result, err)
	}()
	// Wait for the activity's actual outcome, including its timeout
	// classification, while still dispatching workflow callbacks.
	if err := w.wait(context.WithoutCancel(w.ctx), f.IsReady, f.ready); err != nil {
		var zero T
		return zero, err
	}
	return f.result, f.err
}
