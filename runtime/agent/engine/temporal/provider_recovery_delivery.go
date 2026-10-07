// Package temporal orders control deliveries within each native execution.
// Signal delivery is only transport progress: a future resolves after the peer
// records receiver acceptance. Accepted records remain available for duplicates
// and are reconstructed from Temporal history during replay.
package temporal

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	recoveryStreamKey struct {
		Capability string
		PeerRunID  string
		ToParent   bool
	}

	recoverySendStream struct {
		control    *workflowControl
		peer       recoveryAddress
		next       uint64
		queue      []*recoveryDelivery
		deliveries map[uint64]*recoveryDelivery
		running    bool
		err        error
	}

	recoveryDelivery struct {
		frame    recoveryFrame
		endpoint *recoveryEndpoint
		future   workflow.Future
		result   workflow.Settable
		ack      workflow.Future
		accept   workflow.Settable
		ackFrame *recoveryFrame
	}

	recoveryReceiveStream struct {
		next     uint64
		pending  map[uint64]recoveryFrame
		accepted map[uint64]recoveryAcceptance
	}

	recoveryAcceptance struct {
		frame recoveryFrame
		err   error
	}

	recoveryFuture struct {
		future  workflow.Future
		ctx     workflow.Context
		control *workflowControl
	}
)

// HandleSignal decodes and queues reserved signals without workflow operations:
// Temporal invokes this hook outside a workflow coroutine. The dispatcher below
// applies callbacks and errors when ordinary workflow code next yields.
func (i *workflowControlInbound) HandleSignal(
	ctx workflow.Context, input *interceptor.HandleSignalInput,
) error {
	if input.SignalName != recoverySignalName {
		return i.Next.HandleSignal(ctx, input)
	}
	var frame recoveryFrame
	if err := NewAgentDataConverter().FromPayloads(input.Arg, &frame); err != nil {
		if i.failure == nil {
			i.failure = fmt.Errorf("decode provider recovery signal: %w", err)
		}
		return nil
	}
	i.early = append(i.early, frame)
	return nil
}

func (i *workflowControlInbound) dispatchRecovery(ctx workflow.Context) {
	for {
		if err := workflow.Await(ctx, func() bool {
			return len(i.early) != 0 || i.failure != nil
		}); err != nil {
			return
		}
		i.control.dispatchPending(ctx)
		if i.failure != nil {
			return
		}
	}
}

// dispatchPending runs within a workflow coroutine and never waits inside a
// receiver. It also runs before engine wait conditions and ordinary completion.
func (c *workflowControl) dispatchPending(ctx workflow.Context) {
	i := c.inbound
	pending := i.early
	i.early = nil
	for _, frame := range pending {
		if err := c.receiveFrame(ctx, frame); err != nil && i.failure == nil {
			i.failure = err
		}
	}
	if i.failure != nil {
		c.workflow.cancelExecution()
	}
}

// send records the outgoing obligation before starting its coroutine. All
// requests and pauses toward one peer share this execution's ordered stream.
func (c *workflowControl) send(
	w *temporalWorkflowContext, peer recoveryAddress, frame recoveryFrame, endpoint *recoveryEndpoint,
) (*recoveryFuture, error) {
	info := workflow.GetInfo(w.ctx)
	frame.Source = recoveryAddress{
		Namespace: info.Namespace, WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID,
	}
	frame.FirstRunID = info.FirstRunID
	key := recoveryStreamKey{Capability: frame.Capability, PeerRunID: peer.RunID, ToParent: frame.ToParent}
	stream := c.outgoing[key]
	if stream == nil {
		stream = &recoverySendStream{control: c, peer: peer, deliveries: make(map[uint64]*recoveryDelivery)}
		c.outgoing[key] = stream
	}
	frame.Sequence = stream.next + 1
	// Copy through the same bounded converter used for native signals. Caller
	// mutations cannot change the retained delivery or its duplicate comparison.
	payload, err := NewAgentDataConverter().ToPayload(frame)
	if err != nil {
		return nil, err
	}
	if err := NewAgentDataConverter().FromPayload(payload, &frame); err != nil {
		return nil, err
	}
	stream.next = frame.Sequence
	future, set := workflow.NewFuture(w.ctx)
	ack, accept := workflow.NewFuture(w.ctx)
	delivery := &recoveryDelivery{
		frame: frame, endpoint: endpoint, future: future, result: set, ack: ack, accept: accept,
	}
	stream.deliveries[frame.Sequence] = delivery
	stream.queue = append(stream.queue, delivery)
	c.obligations = append(c.obligations, future)
	if !stream.running {
		stream.running = true
		// A caller may cancel its phase while the engine still owes delivery.
		// Detached cleanup sends must remain on this same ordered stream.
		deliveryCtx, _ := workflow.NewDisconnectedContext(w.ctx)
		workflow.Go(deliveryCtx, stream.deliver)
	}
	return &recoveryFuture{future: future, ctx: w.ctx, control: c}, nil
}

// deliver waits for each peer acceptance before sending the next sequence.
// Native signal completion never resolves the caller's acceptance future.
func (s *recoverySendStream) deliver(ctx workflow.Context) {
	for len(s.queue) != 0 {
		delivery := s.queue[0]
		s.queue = s.queue[1:]
		err := s.err
		if err == nil {
			// Future-child options can name another namespace. The SDK reads
			// those options for signals, so select the bound peer's namespace
			// to deliver this recovery message to its original recipient.
			targetCtx := workflow.WithWorkflowNamespace(ctx, s.peer.Namespace) //nolint:staticcheck // SA1019: keep the signal on the bound peer's route.
			err = workflow.SignalExternalWorkflow(targetCtx,
				s.peer.WorkflowID, s.peer.RunID, recoverySignalName, delivery.frame).Get(ctx, nil)
			if err == nil {
				err = s.control.await(ctx, delivery.ack.IsReady)
				if err == nil {
					err = delivery.ack.Get(ctx, nil)
				}
			}
		}
		if err != nil {
			err = normalizeTemporalError(err)
			if s.err == nil {
				s.err = err
			}
			endpoint := delivery.endpoint
			if endpoint != nil && !endpoint.notified {
				endpoint.notified = true
				endpoint.err = err
				// An acceptance callback can queue a reply and then reject the
				// request. Only an accepted request has a receive callback.
				if endpoint.receive != nil {
					if receiveErr := endpoint.receive(s.control.callbackContext(ctx),
						engine.ProviderRecoveryStopped{Err: err}); receiveErr != nil {
						err = errors.Join(err, receiveErr)
					}
				}
			}
			delivery.result.SetError(err)
		} else {
			delivery.result.SetValue(struct{}{})
		}
	}
	s.running = false
}

func (f *recoveryFuture) Get(_ context.Context) (struct{}, error) {
	if err := f.control.await(f.ctx, f.future.IsReady); err != nil {
		return struct{}{}, normalizeTemporalError(err)
	}
	return struct{}{}, normalizeTemporalError(f.future.Get(f.ctx, nil))
}

func (f *recoveryFuture) IsReady() bool {
	return f.future.IsReady()
}

// receiveFrame keeps early and out-of-order deliveries until their exact
// predecessor and issued child binding are available. Duplicate acceptance
// never invokes the runtime receiver again.
func (c *workflowControl) receiveFrame(ctx workflow.Context, frame recoveryFrame) error {
	if err := validateRecoveryFrame(frame); err != nil {
		return err
	}
	if frame.Ack {
		return c.receiveAck(frame)
	}
	key := recoveryStreamKey{
		Capability: frame.Capability, PeerRunID: frame.Source.RunID, ToParent: frame.ToParent,
	}
	stream := c.incoming[key]
	if stream == nil {
		stream = &recoveryReceiveStream{
			next: 1, pending: make(map[uint64]recoveryFrame), accepted: make(map[uint64]recoveryAcceptance),
		}
		c.incoming[key] = stream
		c.incomingOrder = append(c.incomingOrder, key)
	}
	if previous, ok := stream.accepted[frame.Sequence]; ok {
		if !reflect.DeepEqual(previous.frame, frame) {
			c.acknowledge(ctx, frame, errors.New("conflicting provider recovery delivery"))
		} else {
			c.acknowledge(ctx, frame, previous.err)
		}
		return nil
	}
	if previous, ok := stream.pending[frame.Sequence]; ok && !reflect.DeepEqual(previous, frame) {
		c.acknowledge(ctx, frame, errors.New("conflicting pending provider recovery delivery"))
		return nil
	}
	stream.pending[frame.Sequence] = frame
	c.processRecovery(ctx)
	return nil
}

// processRecovery uses insertion order, never Go map iteration, to keep replay
// deterministic when more than one native child execution has pending evidence.
func (c *workflowControl) processRecovery(ctx workflow.Context) {
	for _, key := range c.incomingOrder {
		stream := c.incoming[key]
		for {
			frame, exists := stream.pending[stream.next]
			if !exists {
				break
			}
			child, ready, err := c.validateRecoverySource(frame)
			if !ready {
				break
			}
			if err == nil {
				err = c.applyRecovery(c.callbackContext(ctx), child, frame)
			}
			stream.accepted[stream.next] = recoveryAcceptance{frame: frame, err: err}
			delete(stream.pending, stream.next)
			stream.next++
			c.acknowledge(ctx, frame, err)
		}
	}
}

// validateRecoverySource checks the issued capability and logical child chain.
// Current native run identity comes from trusted engine code reading SDK Info;
// it remains on each frame and is never replaced by the newest observed run.
func (c *workflowControl) validateRecoverySource(frame recoveryFrame) (*recoveryChild, bool, error) {
	upward := frame.ToParent != frame.Ack
	if !upward {
		if c.parent == nil || frame.Capability != c.parent.Capability || frame.Source != c.parent.Parent {
			return nil, true, errors.New("provider recovery reply does not match the issued parent")
		}
		return nil, true, nil
	}
	child := c.children[frame.Capability]
	if child == nil {
		return nil, true, errors.New("provider recovery sender has no issued child binding")
	}
	if !child.bound {
		return child, false, nil
	}
	if child.err != nil {
		return child, true, child.err
	}
	if frame.Source.WorkflowID != child.id || frame.Source.Namespace != child.binding.Parent.Namespace ||
		frame.FirstRunID != child.execution.RunID {
		return child, true, errors.New("provider recovery sender does not match the accepted child chain")
	}
	return child, true, nil
}

func (c *workflowControl) receiveAck(frame recoveryFrame) error {
	_, ready, err := c.validateRecoverySource(frame)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("provider recovery acknowledgment precedes its issued binding")
	}
	stream := c.outgoing[recoveryStreamKey{
		Capability: frame.Capability, PeerRunID: frame.Source.RunID, ToParent: frame.ToParent,
	}]
	if stream == nil {
		return errors.New("provider recovery acknowledgment has no outgoing stream")
	}
	delivery := stream.deliveries[frame.Sequence]
	if delivery == nil {
		return errors.New("provider recovery acknowledgment has no outgoing delivery")
	}
	if delivery.ackFrame != nil {
		if !reflect.DeepEqual(*delivery.ackFrame, frame) {
			return errors.New("conflicting provider recovery acknowledgment")
		}
		return nil
	}
	cause, err := frame.Error.decode()
	if err != nil {
		return err
	}
	delivery.ackFrame = &frame
	delivery.accept.Set(struct{}{}, cause)
	return nil
}

// acknowledge records a native signal command after the receiver returns.
// Its delivery is also drained. When Temporal reports that the exact recipient
// run is gone, the receiver keeps its acceptance and finishes; a later run never
// receives that old acknowledgment. Other send failures still fail completion.
func (c *workflowControl) acknowledge(ctx workflow.Context, received recoveryFrame, err error) {
	info := workflow.GetInfo(ctx)
	frame := recoveryFrame{
		Capability: received.Capability, Sequence: received.Sequence, Ack: true, ToParent: received.ToParent,
		Source: recoveryAddress{
			Namespace: info.Namespace, WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID,
		},
		FirstRunID: info.FirstRunID,
		Error:      encodeRecoveryError(err),
	}
	disconnected, _ := workflow.NewDisconnectedContext(ctx)
	// A received frame names the acknowledgment recipient, even on rejection.
	// The SDK reads future-child options for signals, so select that source's
	// namespace to return acceptance or rejection to the original sender.
	disconnected = workflow.WithWorkflowNamespace(disconnected, received.Source.Namespace) //nolint:staticcheck // SA1019: keep the acknowledgment on the received source's route.
	sent := workflow.SignalExternalWorkflow(disconnected,
		received.Source.WorkflowID, received.Source.RunID, recoverySignalName, frame)
	future, complete := workflow.NewFuture(disconnected)
	c.obligations = append(c.obligations, future)
	workflow.Go(disconnected, func(ctx workflow.Context) {
		deliveryErr := sent.Get(ctx, nil)
		var closed *temporal.UnknownExternalWorkflowExecutionError
		if errors.As(deliveryErr, &closed) {
			deliveryErr = nil
		}
		complete.Set(struct{}{}, deliveryErr)
	})
}

func validateRecoveryFrame(frame recoveryFrame) error {
	if frame.Capability == "" || frame.Sequence == 0 || frame.Source.Namespace == "" ||
		frame.Source.WorkflowID == "" || frame.Source.RunID == "" {
		return errors.New("provider recovery delivery has incomplete private identity")
	}
	var variants int
	if frame.Open != nil {
		variants++
	}
	if frame.Message != nil {
		variants++
	}
	if frame.Pause != nil {
		variants++
	}
	if frame.Ack {
		if variants != 0 || frame.Request != (recoveryRequestID{}) {
			return errors.New("provider recovery acknowledgment contains a request body")
		}
	} else if variants != 1 || frame.Error != nil {
		return errors.New("provider recovery delivery must contain exactly one body")
	}
	if frame.Pause != nil && frame.Request != (recoveryRequestID{}) {
		return errors.New("provider recovery pause must not name a planner request")
	}
	return nil
}
