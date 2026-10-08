// Package temporal binds recovery to children the engine actually starts.
// Temporal supplies each execution's parent and run-chain facts. Private headers
// carry the issued binding between trusted namespace workers; they are not
// authentication against principals allowed to read and modify that namespace.
package temporal

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

type (
	recoveryChild struct {
		id        string
		binding   recoveryBinding
		execution workflow.Execution
		bound     bool
		err       error
	}

	recoveryChildHeaderKey struct{}

	recoveryCapability struct {
		Value string
		Error string
	}

	workflowControlOutbound struct {
		interceptor.WorkflowOutboundInterceptorBase
		inbound *workflowControlInbound
	}
)

const (
	recoveryHeaderName = "goa_ai_provider_recovery_binding"
	recoverySignalName = "goa_ai_provider_recovery"
)

// Init installs private header propagation without changing native inputs.
func (i *workflowControlInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&workflowControlOutbound{
		WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next},
		inbound:                         i,
	})
}

// ExecuteChildWorkflow attaches only the header prepared by StartChildWorkflow.
// Native calls without an engine-issued child request do not acquire a binding.
func (o *workflowControlOutbound) ExecuteChildWorkflow(
	ctx workflow.Context, name string, args ...any,
) workflow.ChildWorkflowFuture {
	if payload, ok := ctx.Value(recoveryChildHeaderKey{}).(*commonpb.Payload); ok {
		interceptor.WorkflowHeader(ctx)[recoveryHeaderName] = payload
	}
	return o.Next.ExecuteChildWorkflow(ctx, name, args...)
}

// NewContinueAsNewError retains the issued parent binding in the next run's
// header. Temporal supplies the next run's own ID and original chain ID.
func (o *workflowControlOutbound) NewContinueAsNewError(
	ctx workflow.Context, name any, args ...any,
) error {
	if o.inbound.parentHeader != nil {
		interceptor.WorkflowHeader(ctx)[recoveryHeaderName] = o.inbound.parentHeader
	}
	return o.Next.NewContinueAsNewError(ctx, name, args...)
}

// readRecoveryParent validates an inherited header against Temporal's start
// history. An absent header is unscoped; malformed existing proof is an error.
func readRecoveryParent(ctx workflow.Context, payload *commonpb.Payload) (*recoveryBinding, error) {
	if payload == nil {
		return nil, nil
	}
	var binding recoveryBinding
	if err := NewAgentDataConverter().FromPayload(payload, &binding); err != nil {
		return nil, fmt.Errorf("decode provider recovery binding: %w", err)
	}
	if _, err := uuid.Parse(binding.Capability); err != nil {
		return nil, errors.New("provider recovery binding has an invalid capability")
	}
	info := workflow.GetInfo(ctx)
	parent := info.ParentWorkflowExecution
	if parent == nil || parent.ID == "" || parent.RunID == "" ||
		binding.Parent != (recoveryAddress{
			Namespace: info.ParentWorkflowNamespace, WorkflowID: parent.ID, RunID: parent.RunID,
		}) {
		return nil, errors.New("provider recovery binding does not match the native parent")
	}
	if info.FirstRunID == "" || info.WorkflowExecution.RunID == "" {
		return nil, errors.New("provider recovery execution has no native run-chain identity")
	}
	return &binding, nil
}

// prepareRecoveryChild records one capability before scheduling the child.
// The eventual start result, rather than an incoming message, supplies its anchor.
func (w *temporalWorkflowContext) prepareRecoveryChild(
	ctx workflow.Context, id string,
) (workflow.Context, *recoveryChild, error) {
	if w.control == nil {
		return nil, nil, errors.New("temporal engine: workflow control interceptor is missing")
	}
	var capability recoveryCapability
	if err := workflow.SideEffect(ctx, newRecoveryCapability).Get(&capability); err != nil {
		return nil, nil, fmt.Errorf("read provider recovery capability: %w", err)
	}
	if capability.Error != "" {
		return nil, nil, fmt.Errorf("create provider recovery capability: %s", capability.Error)
	}
	info := workflow.GetInfo(ctx)
	binding := recoveryBinding{
		Capability: capability.Value,
		Parent: recoveryAddress{
			Namespace: info.Namespace, WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID,
		},
		Inherited: w.control.accept != nil || w.control.parent != nil && w.control.parent.Inherited,
	}
	payload, err := NewAgentDataConverter().ToPayload(binding)
	if err != nil {
		return nil, nil, fmt.Errorf("encode provider recovery binding: %w", err)
	}
	child := &recoveryChild{id: id, binding: binding}
	w.control.children[binding.Capability] = child
	return workflow.WithValue(ctx, recoveryChildHeaderKey{}, payload), child, nil
}

func newRecoveryCapability(workflow.Context) any {
	id, err := uuid.NewRandom()
	if err != nil {
		return recoveryCapability{Error: err.Error()}
	}
	return recoveryCapability{Value: id.String()}
}
