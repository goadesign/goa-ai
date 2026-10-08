package inmem

// Cancellation commands join the workflow callback queue before the engine
// cancels execution. The first accepted request is retained for exact repeats;
// commands arriving after completion receive the established terminal result.

import (
	"context"
	"errors"
	"sync"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	// cancellationState serializes commands for one workflow execution.
	cancellationState struct {
		mu              sync.Mutex
		workflow        *wfCtx
		handler         engine.CancellationHandler
		cancel          context.CancelFunc
		acceptedRequest engine.CancellationRequest
		commands        []*cancellationCommand
		processing      bool
		closed          bool
	}

	// cancellationCommand returns the result of one queued request.
	cancellationCommand struct {
		request engine.CancellationRequest
		result  chan error
	}
)

// request queues one command and waits for workflow code to finish handling
// it. Once queued, the command still runs if the caller stops waiting.
func (s *cancellationState) request(ctx context.Context, request engine.CancellationRequest) error {
	command := &cancellationCommand{
		request: request,
		result:  make(chan error, 1),
	}
	s.mu.Lock()
	if s.closed {
		err := completedCancellationResult(s.acceptedRequest, request)
		s.mu.Unlock()
		return err
	}
	s.commands = append(s.commands, command)
	start := s.handler != nil && !s.processing
	if start {
		s.processing = true
	}
	workflow := s.workflow
	s.mu.Unlock()
	if start {
		workflow.control.tasks.enqueue(workflowTask{
			result: &future[struct{}]{ready: make(chan struct{})},
			apply: func() error {
				s.process()
				return nil
			},
		})
	}
	select {
	case err := <-command.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// setHandler installs the only cancellation handler for this workflow. It
// handles commands that arrived before registration before returning.
func (s *cancellationState) setHandler(handler engine.CancellationHandler) error {
	if handler == nil {
		return errors.New("cancellation handler is required")
	}
	s.mu.Lock()
	if s.handler != nil {
		s.mu.Unlock()
		return errors.New("cancellation handler is already registered")
	}
	s.handler = handler
	start := len(s.commands) > 0 && !s.processing
	if start {
		s.processing = true
	}
	s.mu.Unlock()
	if start {
		s.process()
	}
	return nil
}

// startAttempt points cancellation commands at the workflow attempt that is
// currently running.
func (s *cancellationState) startAttempt(workflow *wfCtx) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workflow = workflow
}

// endAttempt removes the failed attempt's handler before the retry delay. A
// cancellation received during the delay waits for the next attempt.
func (s *cancellationState) endAttempt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workflow = nil
	s.handler = nil
	s.processing = false
}

// process handles queued commands in arrival order. The first accepted
// request cancels execution; retries reuse that result without calling the
// handler again.
func (s *cancellationState) process() {
	for {
		s.mu.Lock()
		if len(s.commands) == 0 {
			s.processing = false
			s.mu.Unlock()
			return
		}
		command := s.commands[0]
		s.commands = s.commands[1:]
		acceptedRequest := s.acceptedRequest
		handler := s.handler
		closed := s.closed
		s.mu.Unlock()

		var err error
		switch {
		case closed:
			err = completedCancellationResult(acceptedRequest, command.request)
		case acceptedRequest.RunID == "":
			err = handler(s.workflow, command.request)
			if err == nil {
				s.mu.Lock()
				s.acceptedRequest = command.request
				s.mu.Unlock()
				s.cancel()
			}
		case acceptedRequest != command.request:
			err = &engine.CancellationConflictError{
				RunID:  command.request.RunID,
				Reason: command.request.Reason,
			}
		}
		command.result <- err
	}
}

// finish closes cancellation admission before the workflow publishes its final
// engine result. A handler already storing a reason completes first.
func (s *cancellationState) finish() {
	s.mu.Lock()
	s.closed = true
	queued := s.commands
	s.commands = nil
	acceptedRequest := s.acceptedRequest
	s.mu.Unlock()
	for _, command := range queued {
		command.result <- completedCancellationResult(acceptedRequest, command.request)
	}
}

// completedCancellationResult preserves an accepted request after closure and
// otherwise reports that the workflow can no longer accept a command.
func completedCancellationResult(acceptedRequest, request engine.CancellationRequest) error {
	switch {
	case acceptedRequest.RunID == "":
		return engine.ErrWorkflowCompleted
	case acceptedRequest == request:
		return nil
	default:
		return &engine.CancellationConflictError{RunID: request.RunID, Reason: request.Reason}
	}
}
