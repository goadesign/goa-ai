// Package temporal accepts cancellation jobs that outlive their HTTP callers.
// Each job records observations and timers until the runtime settles saved work.
package temporal

import (
	"context"
	"errors"
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
)

const cancellationReasonMemoKey = "goa_ai_cancellation_reason"

// RegisterCancellationWorkflow installs an ordinary typed activity and a small
// durable workflow on the owning worker queue. Pending work produces a timer;
// failed delivery uses the existing activity retry rules.
func (e *Engine) RegisterCancellationWorkflow(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, engine.CancellationRequest) (bool, error)) error {
	if err := e.requireWorkerMode("register cancellation workflows"); err != nil {
		return err
	}
	if name == "" || opts.Queue == "" || fn == nil || opts.RetryPolicy.InitialInterval <= 0 {
		return errors.New("cancellation workflow requires name, queue, handler and positive observation interval")
	}
	if err := e.beginWorkflowRegistration(name); err != nil {
		return err
	}
	registered := false
	defer func() {
		if !registered {
			e.abortWorkflowRegistration(name)
		}
	}()
	activityName := name + ".observe"
	opts = e.applyActivityClassDefaults(activityKindRecord, opts)
	wrapped := func(ctx context.Context, request engine.CancellationRequest) (bool, error) {
		settled, err := fn(ctx, request)
		e.recordActivityError(ctx, err)
		return settled, temporalerrors.WrapActivity(err)
	}
	if err := e.registerActivityWithCtx(activityName, opts, wrapped); err != nil {
		return err
	}

	e.workerForQueue(opts.Queue).registerWorkflow(name, e.cancellationWorkflowHandler(name, activityName, opts))
	e.finishWorkflowRegistration(name)
	registered = true
	return nil
}

// StartCancellationWorkflow acknowledges Temporal acceptance without waiting
// for cleanup. Duplicate delivery verifies routing and the original reason.
func (e *Engine) StartCancellationWorkflow(ctx context.Context, name, queue string, request engine.CancellationRequest) error {
	id, digest, err := startrecipe.CancellationRecipe(name, queue, request)
	if err != nil {
		return err
	}
	input, err := NewAgentDataConverter().ToPayload(request)
	if err != nil {
		return err
	}
	opts := client.StartWorkflowOptions{
		ID:                                       id,
		TaskQueue:                                queue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
		Memo:                                     map[string]any{workflowStartRecipeMemoKey: digest[:], cancellationReasonMemoKey: request.Reason},
	}
	_, err = e.startWorkflowWithDigest(ctx, opts, name, converter.NewRawValue(input), digest)
	var conflict *engine.WorkflowStartConflictError
	if !errors.As(err, &conflict) {
		return err
	}
	// The shared start check already rejected changed immutable bytes. Decode
	// the original reason to distinguish reason conflicts from routing conflicts.
	description, readErr := e.client.DescribeWorkflowExecution(ctx, id, "")
	if readErr != nil {
		return readErr
	}
	payload := description.GetWorkflowExecutionInfo().GetMemo().GetFields()[cancellationReasonMemoKey]
	if payload == nil {
		return err
	}
	var reason string
	if decodeErr := NewAgentDataConverter().FromPayload(payload, &reason); decodeErr != nil {
		return fmt.Errorf("decode cancellation reason: %w", decodeErr)
	}
	if reason != request.Reason {
		return &engine.CancellationConflictError{RunID: request.RunID, Reason: request.Reason}
	}
	return err
}

// cancellationWorkflowHandler records each observation and the timer after an
// unfinished result. Temporal may request a fresh history; the same request and
// observation delay continue in that execution. Activity failures keep their retry rules.
func (e *Engine) cancellationWorkflowHandler(name, activityName string, opts engine.ActivityOptions) func(workflow.Context, engine.CancellationRequest) error {
	return func(ctx workflow.Context, request engine.CancellationRequest) error {
		activityContext := workflow.WithActivityOptions(ctx, newTemporalWorkflowContext(e, ctx).activityOptionsFor(activityName, engine.ActivityOptions{}))
		for {
			var settled bool
			if err := workflow.ExecuteActivity(activityContext, activityName, request).Get(ctx, &settled); err != nil {
				return err
			}
			if settled {
				return nil
			}
			if workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
				return workflow.NewContinueAsNewErrorWithOptions(ctx, workflow.ContinueAsNewErrorOptions{
					BackoffStartInterval: opts.RetryPolicy.InitialInterval,
				}, name, request)
			}
			if err := workflow.Sleep(ctx, opts.RetryPolicy.InitialInterval); err != nil {
				return err
			}
		}
	}
}
