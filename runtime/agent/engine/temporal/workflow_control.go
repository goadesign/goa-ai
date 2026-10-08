// Package temporal installs workflow control on every engine-owned worker.
// The worker supplies one private state object to native and registered
// workflows. Before an ordinary return, it waits for accepted control deliveries;
// cancellation ends that wait without replacing the workflow's original error.
package temporal

import (
	"errors"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
)

type (
	workflowControlKey struct{}

	workflowControl struct {
		engine        *Engine
		inbound       *workflowControlInbound
		workflow      *temporalWorkflowContext
		obligations   []workflow.Future
		parent        *recoveryBinding
		children      map[string]*recoveryChild
		accept        engine.ProviderRecoveryAccept
		pause         engine.ProviderRecoveryPauseAccept
		pauses        []recoveryPause
		endpoints     map[recoveryEndpointKey]*recoveryEndpoint
		incoming      map[recoveryStreamKey]*recoveryReceiveStream
		incomingOrder []recoveryStreamKey
		outgoing      map[recoveryStreamKey]*recoverySendStream
	}

	workflowControlInterceptor struct {
		interceptor.WorkerInterceptorBase
		engine *Engine
	}

	workflowControlInbound struct {
		interceptor.WorkflowInboundInterceptorBase
		engine       *Engine
		control      *workflowControl
		parentHeader *commonpb.Payload
		early        []recoveryFrame
		failure      error
	}
)

// InterceptWorkflow gives each execution its own control state. The engine
// installs this interceptor regardless of its tracing configuration.
func (i *workflowControlInterceptor) InterceptWorkflow(
	_ workflow.Context, next interceptor.WorkflowInboundInterceptor,
) interceptor.WorkflowInboundInterceptor {
	return &workflowControlInbound{
		WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next},
		engine:                         i.engine,
	}
}

// ExecuteWorkflow wraps the native workflow function so returning from a wrapper
// cannot discard a delivery that its control callbacks already accepted.
func (i *workflowControlInbound) ExecuteWorkflow(
	ctx workflow.Context, input *interceptor.ExecuteWorkflowInput,
) (any, error) {
	i.parentHeader = interceptor.WorkflowHeader(ctx)[recoveryHeaderName]
	parent, err := readRecoveryParent(ctx, i.parentHeader)
	if err != nil {
		return nil, err
	}
	if i.failure != nil {
		return nil, i.failure
	}
	control := &workflowControl{
		engine: i.engine, parent: parent, inbound: i,
		children:  make(map[string]*recoveryChild),
		endpoints: make(map[recoveryEndpointKey]*recoveryEndpoint),
		incoming:  make(map[recoveryStreamKey]*recoveryReceiveStream),
		outgoing:  make(map[recoveryStreamKey]*recoverySendStream),
	}
	i.control = control
	ctx = workflow.WithValue(ctx, workflowControlKey{}, control)
	control.workflow = newTemporalWorkflowContext(i.engine, ctx)
	control.workflow.control = control
	defer i.engine.releaseWorkflowContext(control.workflow.runID)
	workflow.Go(ctx, i.dispatchRecovery)
	// Native workflow code receives the execution's cancelable scope. Its
	// derived contexts retain their own coroutine and cancellation, while an
	// accepted engine cancellation update stops all still-connected work.
	output, err := i.Next.ExecuteWorkflow(control.workflow.ctx, input)
	if temporalerrors.CancellationOnly(err) && i.failure == nil {
		control.workflow.cancelExecution()
		control.dispatchPending(ctx)
		return output, err
	}
	control.dispatchPending(ctx)
	if i.failure != nil {
		return output, i.failure
	}
	if ctx.Err() != nil {
		if err == nil {
			return output, ctx.Err()
		}
		return output, err
	}
	if drainErr := control.drain(control.workflow.ctx); drainErr != nil {
		if i.failure != nil {
			return output, i.failure
		}
		if err != nil {
			return output, err
		}
		return output, drainErr
	}
	return output, err
}

// drain waits for receiver acceptance, including obligations appended by a
// callback while an earlier delivery waits. It stops on cancellation or the
// first failed delivery, so a successful signal send alone cannot complete it.
func (c *workflowControl) drain(ctx workflow.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for index := 0; index < len(c.obligations); index++ {
		if err := c.await(ctx, c.obligations[index].IsReady); err != nil {
			return err
		}
		if err := c.obligations[index].Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// await applies queued control before evaluating an ordinary workflow condition.
// A ready deadline therefore cannot overtake an already received pause report.
func (c *workflowControl) await(ctx workflow.Context, condition func() bool) error {
	for {
		c.dispatchPending(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := workflow.Await(ctx, func() bool {
			return len(c.inbound.early) != 0 || condition()
		}); err != nil {
			return err
		}
		if len(c.inbound.early) == 0 {
			return nil
		}
	}
}

// workflowControlFromContext checks the worker-supplied state before a caller
// constructs an adapter. A different engine cannot take ownership of that state.
func workflowControlFromContext(e *Engine, ctx workflow.Context) (*workflowControl, error) {
	control, ok := ctx.Value(workflowControlKey{}).(*workflowControl)
	if !ok || control == nil {
		return nil, errors.New("temporal engine: workflow control interceptor is missing")
	}
	if control.engine != e {
		return nil, errors.New("temporal engine: workflow control interceptor belongs to another engine")
	}
	return control, nil
}
