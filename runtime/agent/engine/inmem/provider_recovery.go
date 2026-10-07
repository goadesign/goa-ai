package inmem

// Provider recovery connects actual in-memory executions through their issued
// child handles. This file owns delivery and acceptance only: the runtime
// decides permissions, elapsed coverage, phase settlement, and terminal causes.

import (
	"errors"
	"fmt"
	"reflect"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	// workflowScopeKey survives wrappers that replace the public workflow
	// context value while retaining the engine's execution and cancel scope.
	workflowScopeKey struct{}

	workflowControl struct {
		workflow  *wfCtx
		tasks     *workflowTasks
		parent    *recoveryChild
		inherited bool
		accept    engine.ProviderRecoveryAccept
		pause     engine.ProviderRecoveryPauseAccept
		children  map[string]*recoveryChild
		local     map[string]*recoveryEndpoint
		pending   []*future[struct{}]
		pauses    []receivedPause
		up        *recoveryDeliveryStream
		down      *recoveryDeliveryStream
	}

	// recoveryChild is created only by StartChildWorkflow. The native handle
	// records every issued attempt, so delayed deliveries keep their original
	// execution even after a retry starts.
	recoveryChild struct {
		parent    *workflowControl
		handle    *handle
		id        string
		inherited bool
		accepted  chan struct{}
	}

	recoveryPort struct {
		workflow *wfCtx
	}

	recoveryRequest struct {
		failure engine.ProviderRecoveryFailure
	}

	recoveryEndpoint struct {
		workflow *wfCtx
		request  *recoveryRequest
		peer     *recoveryEndpoint
		stream   *recoveryDeliveryStream
		receive  engine.ProviderRecoveryReceive
		incoming bool
		forward  *recoveryEndpoint
		err      error
	}

	receivedPause struct {
		child string
		pause engine.ProviderRecoveryPaused
	}

	// Each direction has one sequence across every request and pause from the
	// same child. A repeated sequence returns its original acceptance result.
	recoveryDeliveryStream struct {
		source     *workflowControl
		target     *workflowControl
		child      *recoveryChild
		next       uint64
		deliveries map[uint64]recoveryDelivery
	}

	recoveryDelivery struct {
		value  any
		result *future[struct{}]
	}
)

var (
	_ engine.ProviderRecoveryPort = (*recoveryPort)(nil)
	_ engine.ProviderRecovery     = (*recoveryEndpoint)(nil)
)

func (w *wfCtx) ProviderRecovery() engine.ProviderRecoveryPort {
	w.executionControl()
	return &recoveryPort{workflow: w}
}

// executionControl initializes manually constructed test contexts as well as
// real workflow attempts. Derived contexts retain this same pointer.
func (w *wfCtx) executionControl() *workflowControl {
	if w.control == nil {
		w.control = &workflowControl{
			workflow: w,
			tasks:    newWorkflowTasks(),
			children: make(map[string]*recoveryChild),
			local:    make(map[string]*recoveryEndpoint),
		}
	}
	return w.control
}

func (p *recoveryPort) HasParent() bool {
	return p.workflow.control.inherited
}

func (p *recoveryPort) Register(accept engine.ProviderRecoveryAccept) error {
	control := p.workflow.control
	if accept == nil {
		return errors.New("provider recovery receiver is required")
	}
	if control.accept != nil {
		return errors.New("provider recovery receiver is already registered")
	}
	control.accept = accept
	return nil
}

func (p *recoveryPort) RegisterPauseHandler(accept engine.ProviderRecoveryPauseAccept) error {
	control := p.workflow.control
	if accept == nil {
		return errors.New("provider recovery pause receiver is required")
	}
	if control.pause != nil {
		return errors.New("provider recovery pause receiver is already registered")
	}
	control.pause = accept
	for _, report := range control.pauses {
		if err := accept(p.workflow, report.child, report.pause); err != nil {
			return err
		}
	}
	control.pauses = nil
	return nil
}

func (p *recoveryPort) Open(ctx engine.WorkflowContext, failure engine.ProviderRecoveryFailure, receive engine.ProviderRecoveryReceive) (engine.ProviderRecovery, error) {
	w, err := recoveryContext(ctx, p.workflow.control)
	if err != nil {
		return nil, err
	}
	if !p.HasParent() {
		return nil, errors.New("provider recovery has no inherited parent")
	}
	if receive == nil {
		return nil, errors.New("provider recovery reply receiver is required")
	}
	if existing := w.control.local[failure.PublicationBatchID]; existing != nil {
		if !reflect.DeepEqual(existing.request.failure, failure) {
			return nil, errors.New("provider recovery publication has conflicting failure evidence")
		}
		return existing, nil
	}
	endpoint := w.openRecovery(&recoveryRequest{failure: failure}, receive)
	w.control.local[failure.PublicationBatchID] = endpoint
	return endpoint, nil
}

func (p *recoveryPort) ReportPause(ctx engine.WorkflowContext, pause engine.ProviderRecoveryPaused) (engine.Future[struct{}], error) {
	w, err := recoveryContext(ctx, p.workflow.control)
	if err != nil {
		return nil, err
	}
	control := w.control
	if control.parent == nil {
		result := &future[struct{}]{ready: make(chan struct{}), workflow: w}
		result.complete(struct{}{}, nil)
		return result, nil
	}
	stream := control.parentStream()
	return stream.send(pause, func() error {
		target := stream.target
		if target.pause == nil {
			// A wrapper records its child's evidence but cannot claim that
			// its own local work and other children were paused.
			target.pauses = append(target.pauses, receivedPause{child: stream.child.id, pause: pause})
			return nil
		}
		return target.pause(target.workflow, stream.child.id, pause)
	}, nil), nil
}

func (e *recoveryEndpoint) Forward(ctx engine.WorkflowContext, receive engine.ProviderRecoveryReceive) (engine.ProviderRecovery, error) {
	w, err := recoveryContext(ctx, e.workflow.control)
	if err != nil {
		return nil, err
	}
	if !e.incoming {
		return nil, errors.New("only an incoming provider recovery request can be forwarded")
	}
	if !w.control.inherited {
		return nil, errors.New("provider recovery has no inherited parent")
	}
	if receive == nil {
		return nil, errors.New("provider recovery forwarded reply receiver is required")
	}
	if e.forward != nil {
		return nil, errors.New("provider recovery request is already forwarded")
	}
	e.forward = w.openRecovery(e.request, receive)
	return e.forward, nil
}

func (e *recoveryEndpoint) Send(ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage) (engine.Future[struct{}], error) {
	_, err := recoveryContext(ctx, e.workflow.control)
	if err != nil {
		return nil, err
	}
	if message == nil {
		return nil, errors.New("provider recovery message is required")
	}
	return e.stream.send(struct {
		request *recoveryRequest
		message engine.ProviderRecoveryMessage
	}{e.request, message}, func() error {
		if e.peer.err != nil {
			return e.peer.err
		}
		if e.peer.receive == nil {
			return errors.New("provider recovery request was not accepted")
		}
		return e.peer.receive(e.peer.workflow, message)
	}, nil), nil
}

// openRecovery creates both ends before queuing acceptance. Later sends use the
// same stream, so the parent's receiver is installed before any transition.
func (w *wfCtx) openRecovery(request *recoveryRequest, receive engine.ProviderRecoveryReceive) *recoveryEndpoint {
	stream := w.control.parentStream()
	if w.control.down == nil {
		w.control.down = &recoveryDeliveryStream{
			source: stream.target, target: w.control, child: stream.child,
			deliveries: make(map[uint64]recoveryDelivery),
		}
	}
	out := &recoveryEndpoint{workflow: w, request: request, stream: stream, receive: receive}
	in := &recoveryEndpoint{
		workflow: stream.target.workflow,
		request:  request,
		stream:   w.control.down,
		incoming: true,
		peer:     out,
	}
	out.peer = in
	stream.send(request, func() error {
		var err error
		switch {
		case stream.target.accept != nil:
			in.receive, err = stream.target.accept(in.workflow, stream.child.id, request.failure, in)
		case stream.target.inherited:
			var onward engine.ProviderRecovery
			onward, err = in.Forward(in.workflow, func(ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
				_, sendErr := in.Send(ctx, message)
				return sendErr
			})
			if err == nil {
				in.receive = func(ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
					_, sendErr := onward.Send(ctx, message)
					return sendErr
				}
			}
		default:
			err = errors.New("provider recovery parent has no receiver")
		}
		if err == nil && in.receive == nil {
			err = errors.New("provider recovery acceptance returned no receiver")
		}
		in.err = err
		return err
	}, func(err error) {
		// Queue the failure before completing acceptance, so completion cannot
		// overtake the reply callback that explains why delivery failed.
		w.control.tasks.enqueue(workflowTask{
			result: &future[struct{}]{ready: make(chan struct{})},
			apply: func() error {
				return receive(w, engine.ProviderRecoveryStopped{Err: err})
			},
		})
	})
	return out
}

func recoveryContext(ctx engine.WorkflowContext, control *workflowControl) (*wfCtx, error) {
	w, ok := ctx.Context().Value(workflowScopeKey{}).(*wfCtx)
	if !ok || w.control != control {
		return nil, errors.New("provider recovery context belongs to another execution")
	}
	return w, nil
}

func (c *workflowControl) parentStream() *recoveryDeliveryStream {
	if c.up == nil {
		c.up = &recoveryDeliveryStream{
			source: c, target: c.parent.parent, child: c.parent,
			deliveries: make(map[uint64]recoveryDelivery),
		}
	}
	return c.up
}

func (s *recoveryDeliveryStream) send(value any, apply func() error, onFailure func(error)) *future[struct{}] {
	s.next++
	return s.deliver(s.next, value, apply, onFailure)
}

// deliver uses an engine-private sequence, not message content, as delivery
// identity. Two legitimate equal phase requests remain two different sends.
func (s *recoveryDeliveryStream) deliver(sequence uint64, value any, apply func() error, onFailure func(error)) *future[struct{}] {
	if previous, ok := s.deliveries[sequence]; ok {
		if reflect.DeepEqual(previous.value, value) {
			return previous.result
		}
		result := &future[struct{}]{ready: make(chan struct{}), workflow: s.source.workflow}
		result.complete(struct{}{}, errors.New("conflicting provider recovery delivery"))
		return result
	}
	result := &future[struct{}]{ready: make(chan struct{}), workflow: s.source.workflow, onFailure: onFailure}
	s.deliveries[sequence] = recoveryDelivery{value: value, result: result}
	s.source.pending = append(s.source.pending, result)
	s.target.tasks.enqueue(workflowTask{
		result: result,
		ready: func() bool {
			select {
			case <-s.child.accepted:
				return true
			default:
				return false
			}
		},
		apply: func() error {
			if err := s.validate(); err != nil {
				return err
			}
			return apply()
		},
	})
	return result
}

// validate checks that the delivery's original execution was issued for this
// child handle and parent binding. Earlier attempts remain valid evidence after
// a retry; their requests, streams, and measured intervals are never reassigned.
// Early messages stay queued until StartChildWorkflow binds the issued handle.
func (s *recoveryDeliveryStream) validate() error {
	child := s.child
	select {
	case <-child.accepted:
	default:
		return errors.New("provider recovery child handle was not accepted")
	}
	if s.target == child.parent && child.parent.children[child.id] != child {
		return errors.New("provider recovery child handle does not match the accepted request")
	}
	var execution *workflowControl
	switch {
	case s.source == child.parent:
		execution = s.target
	case s.target == child.parent:
		execution = s.source
	default:
		return fmt.Errorf("provider recovery child %q delivery has a different parent", child.id)
	}
	child.handle.mu.Lock()
	_, issued := child.handle.issuedControls[execution]
	child.handle.mu.Unlock()
	if !issued || child.handle.parent != child || execution.parent != child {
		return fmt.Errorf("provider recovery child %q execution does not match its issued handle", child.id)
	}
	return nil
}
