// Package temporal accepts cancellation jobs that outlive their HTTP callers.
// Each job records observations and timers until the runtime settles saved work.
package temporal

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
)

const cancellationReasonMemoKey = "goa_ai_cancellation_reason"

// RegisterCancellationWorkflow installs an ordinary typed activity and a small
// durable workflow on the owning worker queue. Pending work produces a timer;
// failed delivery uses the existing activity retry rules. Unpinned jobs follow
// the current worker build because observations do not execute agent work.
func (e *Engine) RegisterCancellationWorkflow(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, engine.CancellationRequest) (bool, error)) error {
	if err := e.requireWorkerMode("register cancellation workflows"); err != nil {
		return err
	}
	if name == "" || opts.Queue == "" || fn == nil || opts.RetryPolicy.InitialInterval <= 0 {
		return errors.New("cancellation workflow requires name, queue, handler and positive observation interval")
	}
	if err := e.beginWorkflowRegistration(name, opts.Queue, false); err != nil {
		return err
	}
	registered := false
	defer func() {
		if !registered {
			e.abortWorkflowRegistration(name, opts.Queue)
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

	e.workerForQueue(opts.Queue).worker.RegisterWorkflowWithOptions(e.cancellationWorkflowHandler(name, activityName, opts), workflow.RegisterOptions{
		Name: name, VersioningBehavior: workflow.VersioningBehaviorAutoUpgrade,
	})
	e.finishWorkflowRegistration(name, opts.Queue, false)
	registered = true
	return nil
}

// StartActiveCancellationWorkflow reads the accepted workflow type, queue and
// status in one Temporal description. Its observer follows current workers even
// if the owner finishes before the observer starts. Agent work keeps its own
// accepted worker target; the observer only delivers and follows cancellation.
func (e *Engine) StartActiveCancellationWorkflow(ctx context.Context, ownerRunID string, request engine.CancellationRequest) error {
	description, err := e.client.DescribeWorkflowExecution(ctx, ownerRunID, "")
	if err != nil {
		return mapDescribeWorkflowExecutionError(err)
	}
	info := description.GetWorkflowExecutionInfo()
	if info.GetExecution().GetWorkflowId() != ownerRunID || info.GetType().GetName() == "" || info.GetTaskQueue() == "" {
		return errors.New("active cancellation owner description has missing or mismatched execution routing")
	}
	if info.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING && info.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_PAUSED {
		return engine.ErrWorkflowCompleted
	}
	return e.StartCancellationWorkflow(ctx, info.GetType().GetName()+".cancel", info.GetTaskQueue(), request, nil)
}

// StartCancellationWorkflow acknowledges Temporal acceptance without waiting
// for cleanup. Duplicate delivery verifies routing and the original reason.
// An omitted target reuses an existing job's accepted target, if any.
func (e *Engine) StartCancellationWorkflow(ctx context.Context, name, queue string, request engine.CancellationRequest, target *engine.WorkerDeploymentVersion) error {
	id, digest, err := startrecipe.CancellationRecipe(name, queue, request, target)
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
	if target != nil {
		opts.VersioningOverride = &client.PinnedVersioningOverride{Version: worker.WorkerDeploymentVersion{
			DeploymentName: target.DeploymentName, BuildID: target.BuildID,
		}}
	}
	_, err = e.startWorkflowWithDigest(ctx, opts, name, converter.NewRawValue(input), digest)
	var conflict *engine.WorkflowStartConflictError
	if !errors.As(err, &conflict) {
		return err
	}
	// The submitted recipe differs from the accepted job. Preserve reason
	// conflicts before checking whether an omitted target can reuse that job.
	description, readErr := e.client.DescribeWorkflowExecution(ctx, id, "")
	if readErr != nil {
		return readErr
	}
	info := description.GetWorkflowExecutionInfo()
	payload := info.GetMemo().GetFields()[cancellationReasonMemoKey]
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
	if target != nil {
		return err
	}

	// A caller that omits a target may repeat a job accepted with an explicit
	// pin. Rebuild its complete recipe from Temporal's saved pin and require the
	// original digest; matching the reason alone cannot authorize a new route.
	pinned := info.GetVersioningInfo().GetVersioningOverride().GetPinned()
	if pinned == nil || info.GetType().GetName() != name || info.GetTaskQueue() != queue {
		return err
	}
	version := pinned.GetVersion()
	acceptedTarget := &engine.WorkerDeploymentVersion{
		DeploymentName: version.GetDeploymentName(), BuildID: version.GetBuildId(),
	}
	_, acceptedDigest, recipeErr := startrecipe.CancellationRecipe(name, queue, request, acceptedTarget)
	if recipeErr != nil {
		return fmt.Errorf("validate accepted cancellation target: %w", recipeErr)
	}
	payload = info.GetMemo().GetFields()[workflowStartRecipeMemoKey]
	if payload == nil {
		return err
	}
	var savedDigest []byte
	if decodeErr := NewAgentDataConverter().FromPayload(payload, &savedDigest); decodeErr != nil {
		return fmt.Errorf("decode cancellation start recipe: %w", decodeErr)
	}
	if !bytes.Equal(savedDigest, acceptedDigest[:]) {
		return err
	}
	return nil
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
