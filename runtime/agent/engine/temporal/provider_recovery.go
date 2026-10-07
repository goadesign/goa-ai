// Package temporal connects recovery requests through actual workflow parents.
// The runtime owns phase decisions and elapsed-time accounting. This file owns
// request identity, callback registration, and automatic native-wrapper relays.
package temporal

import (
	"errors"
	"reflect"

	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	recoveryContextKey struct{}

	recoveryPort struct {
		workflow *temporalWorkflowContext
	}

	recoveryEndpointKey struct {
		Capability string
		PeerRunID  string
		Request    recoveryRequestID
		Incoming   bool
	}

	recoveryEndpoint struct {
		control  *workflowControl
		key      recoveryEndpointKey
		peer     recoveryAddress
		failure  recoveryFailure
		receive  engine.ProviderRecoveryReceive
		forward  *recoveryEndpoint
		err      error
		notified bool
	}

	recoveryPause struct {
		child string
		pause engine.ProviderRecoveryPaused
	}
)

var (
	_ engine.ProviderRecoveryPort = (*recoveryPort)(nil)
	_ engine.ProviderRecovery     = (*recoveryEndpoint)(nil)
)

func (w *temporalWorkflowContext) ProviderRecovery() engine.ProviderRecoveryPort {
	return &recoveryPort{workflow: w}
}

func (p *recoveryPort) HasParent() bool {
	parent := p.workflow.control.parent
	return parent != nil && parent.Inherited
}

func (p *recoveryPort) Register(accept engine.ProviderRecoveryAccept) error {
	if accept == nil {
		return errors.New("provider recovery receiver is required")
	}
	control := p.workflow.control
	if control.accept != nil {
		return errors.New("provider recovery receiver is already registered")
	}
	control.accept = accept
	return nil
}

func (p *recoveryPort) RegisterPauseHandler(accept engine.ProviderRecoveryPauseAccept) error {
	if accept == nil {
		return errors.New("provider recovery pause receiver is required")
	}
	control := p.workflow.control
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

func (p *recoveryPort) Open(
	ctx engine.WorkflowContext, failure engine.ProviderRecoveryFailure, receive engine.ProviderRecoveryReceive,
) (engine.ProviderRecovery, error) {
	w, err := temporalRecoveryContext(ctx, p.workflow.control)
	if err != nil {
		return nil, err
	}
	if !p.HasParent() {
		return nil, errors.New("provider recovery has no inherited parent")
	}
	record, err := encodeRecoveryFailure(failure)
	if err != nil {
		return nil, err
	}
	id := recoveryRequestID{WorkflowID: w.workflowID, RunID: w.runID, Publication: failure.PublicationBatchID}
	return w.openRecovery(id, *record, receive)
}

func (p *recoveryPort) ReportPause(
	ctx engine.WorkflowContext, pause engine.ProviderRecoveryPaused,
) (engine.Future[struct{}], error) {
	w, err := temporalRecoveryContext(ctx, p.workflow.control)
	if err != nil {
		return nil, err
	}
	if w.control.parent == nil {
		return immediateFuture[struct{}]{}, nil
	}
	parent := w.control.parent
	return w.control.send(w, parent.Parent, recoveryFrame{
		Capability: parent.Capability, ToParent: true, Pause: &recoveryPauseInterval{
			StartedAt: encodeRecoveryTime(pause.StartedAt), Through: encodeRecoveryTime(pause.Through),
		},
	}, nil)
}

func (e *recoveryEndpoint) Forward(
	ctx engine.WorkflowContext, receive engine.ProviderRecoveryReceive,
) (engine.ProviderRecovery, error) {
	w, err := temporalRecoveryContext(ctx, e.control)
	if err != nil {
		return nil, err
	}
	if !e.key.Incoming {
		return nil, errors.New("only incoming provider recovery can be forwarded")
	}
	if w.control.parent == nil || !w.control.parent.Inherited {
		return nil, errors.New("provider recovery has no inherited parent")
	}
	if e.forward != nil {
		return nil, errors.New("provider recovery request is already forwarded")
	}
	forward, err := w.openRecovery(e.key.Request, e.failure, receive)
	if err != nil {
		return nil, err
	}
	e.forward = forward
	return forward, nil
}

func (e *recoveryEndpoint) Send(
	ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage,
) (engine.Future[struct{}], error) {
	w, err := temporalRecoveryContext(ctx, e.control)
	if err != nil {
		return nil, err
	}
	record, err := encodeRecoveryMessage(message)
	if err != nil {
		return nil, err
	}
	return e.control.send(w, e.peer, recoveryFrame{
		Capability: e.key.Capability, ToParent: !e.key.Incoming,
		Request: e.key.Request, Message: record,
	}, e)
}

// temporalRecoveryContext recovers the engine-owned scope even when runtime code
// wraps WorkflowContext to measure work. Another execution cannot use this port.
func temporalRecoveryContext(
	ctx engine.WorkflowContext, control *workflowControl,
) (*temporalWorkflowContext, error) {
	w, ok := ctx.Context().Value(recoveryContextKey{}).(*temporalWorkflowContext)
	if !ok || w.control != control {
		return nil, errors.New("provider recovery context belongs to another execution")
	}
	return w, nil
}

// openRecovery keeps the original native run and publication on every relay.
// Later attempts of the same logical child cannot take over this request.
func (w *temporalWorkflowContext) openRecovery(
	id recoveryRequestID, failure recoveryFailure, receive engine.ProviderRecoveryReceive,
) (*recoveryEndpoint, error) {
	if receive == nil {
		return nil, errors.New("provider recovery reply receiver is required")
	}
	parent := w.control.parent
	key := recoveryEndpointKey{Capability: parent.Capability, PeerRunID: parent.Parent.RunID, Request: id}
	if existing := w.control.endpoints[key]; existing != nil {
		if !reflect.DeepEqual(existing.failure, failure) {
			return nil, errors.New("provider recovery publication has conflicting failure evidence")
		}
		return existing, nil
	}
	endpoint := &recoveryEndpoint{
		control: w.control, key: key, peer: parent.Parent,
		failure: failure, receive: receive,
	}
	w.control.endpoints[key] = endpoint
	if _, err := w.control.send(w, parent.Parent, recoveryFrame{
		Capability: parent.Capability, ToParent: true, Request: id, Open: &failure,
	}, endpoint); err != nil {
		delete(w.control.endpoints, key)
		return nil, err
	}
	return endpoint, nil
}

// applyRecovery runs only after the stream verifies its issued child binding.
// Receivers record outgoing obligations synchronously before acceptance is sent.
func (c *workflowControl) applyRecovery(
	w *temporalWorkflowContext, child *recoveryChild, frame recoveryFrame,
) error {
	if frame.Pause != nil {
		if !frame.ToParent {
			return errors.New("provider recovery pause must travel to its parent")
		}
		pause := engine.ProviderRecoveryPaused{StartedAt: frame.Pause.StartedAt.time(), Through: frame.Pause.Through.time()}
		if encodeRecoveryTime(pause.StartedAt) != frame.Pause.StartedAt || encodeRecoveryTime(pause.Through) != frame.Pause.Through {
			return errors.New("provider recovery pause has an invalid timestamp")
		}
		if c.pause == nil {
			c.pauses = append(c.pauses, recoveryPause{child: child.id, pause: pause})
			return nil
		}
		return c.pause(w, child.id, pause)
	}
	key := recoveryEndpointKey{
		Capability: frame.Capability, PeerRunID: frame.Source.RunID,
		Request: frame.Request, Incoming: frame.ToParent,
	}
	if frame.Open != nil {
		if !frame.ToParent || !child.binding.Inherited {
			return errors.New("provider recovery request has no inherited child control")
		}
		if frame.Request.WorkflowID == "" || frame.Request.RunID == "" ||
			frame.Request.Publication != frame.Open.PublicationBatchID {
			return errors.New("provider recovery request has invalid original identity")
		}
		if previous := c.endpoints[key]; previous != nil {
			if !reflect.DeepEqual(previous.failure, *frame.Open) {
				return errors.New("provider recovery request has conflicting failure evidence")
			}
			return previous.err
		}
		failure, err := frame.Open.decode()
		if err != nil {
			return err
		}
		incoming := &recoveryEndpoint{
			control: c, key: key, peer: frame.Source, failure: *frame.Open,
		}
		c.endpoints[key] = incoming
		switch {
		case c.accept != nil:
			incoming.receive, err = c.accept(w, child.id, failure, incoming)
		case c.parent != nil && c.parent.Inherited:
			var onward engine.ProviderRecovery
			onward, err = incoming.Forward(w, func(ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
				_, sendErr := incoming.Send(ctx, message)
				return sendErr
			})
			if err == nil {
				incoming.receive = func(ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
					_, sendErr := onward.Send(ctx, message)
					return sendErr
				}
			}
		default:
			err = errors.New("provider recovery parent has no receiver")
		}
		if err == nil && incoming.receive == nil {
			err = errors.New("provider recovery acceptance returned no receiver")
		}
		incoming.err = err
		return err
	}
	endpoint := c.endpoints[key]
	if endpoint == nil {
		return errors.New("provider recovery transition has no accepted request")
	}
	if endpoint.err != nil {
		return endpoint.err
	}
	message, err := frame.Message.decode()
	if err != nil {
		return err
	}
	return endpoint.receive(w, message)
}

// callbackContext uses the currently running Temporal coroutine for callbacks.
// A cancellation already recorded by the execution remains visible to runtime.
func (c *workflowControl) callbackContext(ctx workflow.Context) *temporalWorkflowContext {
	w := *c.workflow
	w.ctx = ctx
	if c.workflow.ctx.Err() != nil && ctx.Err() == nil {
		var cancel workflow.CancelFunc
		w.ctx, cancel = workflow.WithCancel(ctx)
		cancel()
	}
	return &w
}
