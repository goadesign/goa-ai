// Package runtime gives suspended cancellation a durable owner. The job follows
// admitted successors, resumes saved work through ordinary published continuations,
// and observes completion without depending on the requesting client returning.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// startSuspendedCancellation derives the worker route from the owner's accepted
// preparation. Client-only runtimes need no local agent definition or queue option.
func (r *Runtime) startSuspendedCancellation(ctx context.Context, request CancelRequest, target session.RunMeta) error {
	owner, err := r.cancellationRoot(ctx, target)
	if err != nil {
		return err
	}
	accepted, err := r.cancellationPreparation(ctx, owner)
	if err != nil {
		return err
	}
	if err := r.Seal(ctx); err != nil {
		return err
	}
	err = r.Engine.StartCancellationWorkflow(ctx, accepted.Request.Workflow+".cancel", accepted.Request.TaskQueue, request)
	var conflict *engine.CancellationConflictError
	if errors.As(err, &conflict) {
		return &CancellationReasonConflictError{RunID: request.RunID, Reason: request.Reason}
	}
	return err
}

// cancellationActivity advances one accepted request and reports whether its
// work has settled. A successful pending observation produces a workflow timer;
// transport or storage failures remain errors for the engine's activity policy.
func (r *Runtime) cancellationActivity(ctx context.Context, request engine.CancellationRequest) (settled bool, activityErr error) {
	ctx, span := r.tracer.Start(ctx, "runtime.cancel_saved_run")
	defer func() {
		activityErr = classifyCancellationActivityError(activityErr)
		span.SetAttributes(attribute.String("cancellation.run_id", request.RunID), attribute.Bool("cancellation.settled", settled))
		if activityErr != nil {
			span.RecordError(activityErr)
			span.SetStatus(codes.Error, "cancellation delivery failed")
		}
		span.End()
	}()
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()
	target, err := r.Store.LoadRun(ctx, request.RunID)
	if err != nil {
		return false, err
	}
	target, err = r.selectedCancellationRun(ctx, target)
	if err != nil {
		return false, err
	}
	if target.Status != session.RunStatusSuspended && session.IsTerminalRunStatus(target.Status) {
		if target.CancellationReason != "" && target.CancellationReason != request.Reason {
			return false, engine.MarkActivityErrorNonRetryable(&engine.CancellationConflictError{RunID: request.RunID, Reason: request.Reason})
		}
		if target.Status == session.RunStatusFailed && target.CancellationReason != "" {
			return false, engine.MarkActivityErrorNonRetryable(fmt.Errorf("run %q failed while settling cancellation", target.RunID))
		}
		return true, nil
	}
	requester, ok := r.Engine.(engine.CancellationRequester)
	if !ok {
		return false, engine.MarkActivityErrorNonRetryable(errors.New("engine does not support cancellation delivery"))
	}
	if target.Status == session.RunStatusRunning {
		selected := request
		selected.RunID = target.RunID
		err := requester.RequestCancellation(ctx, target.RunID, selected)
		if errors.Is(err, engine.ErrWorkflowCompleted) || errors.Is(err, engine.ErrWorkflowNotFound) {
			return false, r.observeClosedCancellationOwner(ctx, target)
		}
		return false, err
	}
	owner, err := r.cancellationRoot(ctx, target)
	if err != nil {
		return false, err
	}
	selected := request
	selected.RunID = target.RunID
	if owner.Status == session.RunStatusRunning {
		err := requester.RequestCancellation(ctx, owner.RunID, selected)
		if errors.Is(err, engine.ErrWorkflowCompleted) || errors.Is(err, engine.ErrWorkflowNotFound) {
			return false, r.observeClosedCancellationOwner(ctx, owner)
		}
		if errors.Is(err, engine.ErrCancellationRunNotOwned) {
			return false, nil
		}
		return false, err
	}
	if owner.Status != session.RunStatusSuspended {
		return false, engine.MarkActivityErrorNonRetryable(fmt.Errorf("suspended run %q has settled owner %q without settling its saved work", target.RunID, owner.RunID))
	}
	return false, r.startCancellationContinuation(ctx, owner, selected)
}

// selectedCancellationRun follows only the successor admitted by storage. Each
// successor must retain the same agent and Session; cycles reject corrupt state.
func (r *Runtime) selectedCancellationRun(ctx context.Context, current session.RunMeta) (session.RunMeta, error) {
	seen := make(map[string]struct{})
	for {
		if _, duplicate := seen[current.RunID]; duplicate {
			return session.RunMeta{}, storage.NewContractError(errors.New("cycle in admitted run successors"))
		}
		seen[current.RunID] = struct{}{}
		if current.SuccessorRunID == "" {
			return current, nil
		}
		next, err := r.Store.LoadRun(ctx, current.SuccessorRunID)
		if err != nil {
			return session.RunMeta{}, err
		}
		if current.Status != session.RunStatusSuspended || next.RunID != current.SuccessorRunID || next.AgentID != current.AgentID || next.SessionID != current.SessionID {
			return session.RunMeta{}, storage.NewContractError(errors.New("admitted successor does not match its predecessor"))
		}
		current = next
	}
}

// cancellationRoot finds the root execution that owns a saved child. It follows
// actual parent IDs and admitted successors, never a latest-event heuristic.
func (r *Runtime) cancellationRoot(ctx context.Context, current session.RunMeta) (session.RunMeta, error) {
	seen := make(map[string]struct{})
	for current.ParentRunID != "" {
		if _, duplicate := seen[current.RunID]; duplicate {
			return session.RunMeta{}, storage.NewContractError(errors.New("cycle in cancellation parent ownership"))
		}
		seen[current.RunID] = struct{}{}
		parent, err := r.Store.LoadRun(ctx, current.ParentRunID)
		if err != nil {
			return session.RunMeta{}, err
		}
		if parent.RunID != current.ParentRunID || parent.SessionID != current.SessionID {
			return session.RunMeta{}, storage.NewContractError(errors.New("cancellation parent does not own the child's Session"))
		}
		current, err = r.selectedCancellationRun(ctx, parent)
		if err != nil {
			return session.RunMeta{}, err
		}
	}
	return r.selectedCancellationRun(ctx, current)
}

// cancellationPreparation reads the exact request published before this root
// started. It supplies routing without another caller-owned configuration API.
func (r *Runtime) cancellationPreparation(ctx context.Context, owner session.RunMeta) (startrecipe.PreparedRequest, error) {
	seed, err := r.Store.LoadRunSeed(ctx, owner.RunID, owner.SeedEndID)
	if err != nil {
		return startrecipe.PreparedRequest{}, err
	}
	declaration := seed.Declaration
	if declaration.RunID != owner.RunID || declaration.AgentID != owner.AgentID || declaration.SessionID != owner.SessionID {
		return startrecipe.PreparedRequest{}, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
	}
	accepted, found, err := r.Store.FindRunPreparation(ctx, storage.PreparationOperation{
		AgentID: owner.AgentID, RunID: owner.RunID, SessionID: owner.SessionID, CommandID: declaration.CommandID,
	})
	if err != nil {
		return startrecipe.PreparedRequest{}, err
	}
	if !found {
		return startrecipe.PreparedRequest{}, storage.NewContractError(errors.New("cancellation owner has no accepted preparation"))
	}
	data, err := readPreparation(ctx, r.Store, accepted)
	if err != nil {
		return startrecipe.PreparedRequest{}, err
	}
	prepared, err := startrecipe.ParsePreparedRequest(data)
	if err != nil {
		return startrecipe.PreparedRequest{}, storage.NewContractError(err)
	}
	input := prepared.Request.Input
	if input.RunID != owner.RunID || input.ParentRunID != "" || string(input.AgentID) != owner.AgentID || input.SessionID != owner.SessionID || input.SeedEndID != owner.SeedEndID {
		return startrecipe.PreparedRequest{}, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
	}
	return prepared, nil
}

// startCancellationContinuation publishes an ordinary root continuation with
// the established cancellation operation. A racing answer may win admission;
// the next job observation follows that selected successor instead.
func (r *Runtime) startCancellationContinuation(ctx context.Context, owner session.RunMeta, request engine.CancellationRequest) error {
	registration, found := r.agentByID(agent.Ident(owner.AgentID))
	if !found {
		return engine.MarkActivityErrorNonRetryable(ErrAgentNotFound)
	}
	suspension, err := r.LoadRunSuspension(ctx, owner.RunID)
	if err != nil {
		return err
	}
	identity := appendLengthDelimited([]byte("goa-ai-cancel-continuation-v1"), owner.RunID)
	identity = appendLengthDelimited(identity, suspension.ID)
	identity = appendLengthDelimited(identity, request.RunID)
	digest := sha256.Sum256(identity)
	runID := hex.EncodeToString(digest[:])
	client := &agentClient{r: r, definition: registration.Definition}
	prepared, found, err := client.RecoverPrepared(ctx, owner.SessionID, runID, runID)
	if err != nil {
		return err
	}
	if !found {
		input, writer, err := r.buildStoredContinuationRunInput(ctx, registration.Definition, owner.SessionID, owner.RunID, runID, runID,
			&api.RunContinuationInput{Cancellation: &request}, runID, runID)
		if err != nil {
			return engine.MarkActivityErrorNonRetryable(err)
		}
		accepted, err := r.cancellationPreparation(ctx, owner)
		if err != nil {
			return err
		}
		launch := workflowLaunchSettings{taskQueue: accepted.TaskQueueOverride, memo: accepted.Request.Memo, searchAttributes: accepted.Request.SearchAttributes}
		compiled, err := prepareRunWithDefinition(input, launch, registration.Definition, true)
		if err != nil {
			return engine.MarkActivityErrorNonRetryable(err)
		}
		prepared, err = publishPreparedRun(ctx, writer, registration.Definition.route.ID, compiled, launch.taskQueue, runID)
		if err != nil {
			return err
		}
	}
	_, err = client.StartPrepared(ctx, prepared)
	if err != nil {
		return err
	}
	completion, err := r.Engine.QueryRunCompletion(ctx, runID)
	if err != nil {
		return err
	}
	if completion.Status == engine.RunStatusFailed || completion.Status == engine.RunStatusTimedOut || completion.Status == engine.RunStatusCanceled {
		current, err := r.Store.LoadRun(ctx, owner.RunID)
		if err != nil {
			return err
		}
		if current.SuccessorRunID == "" {
			return engine.MarkActivityErrorNonRetryable(fmt.Errorf("cancellation continuation failed before admission: %w", completion.WorkflowError))
		}
	}
	return nil
}

// classifyCancellationActivityError retains temporary delivery failures and stops
// retries when stored ownership or an accepted request rejects the same input.
func classifyCancellationActivityError(err error) error {
	err = classifyStorageActivityError(err)
	if err == nil || engine.IsActivityErrorNonRetryable(err) {
		return err
	}
	var cancellationConflict *engine.CancellationConflictError
	var startConflict *engine.WorkflowStartConflictError
	if errors.As(err, &cancellationConflict) || errors.As(err, &startConflict) ||
		errors.Is(err, session.ErrRunNotFound) || errors.Is(err, session.ErrSessionPurged) {
		return engine.MarkActivityErrorNonRetryable(err)
	}
	return err
}

// observeClosedCancellationOwner checks the engine's final outcome and reloads
// storage after rejected delivery. A selected successor or saved final record
// permits another observation; a closed workflow with an unfinished row fails.
func (r *Runtime) observeClosedCancellationOwner(ctx context.Context, owner session.RunMeta) error {
	completion, err := r.Engine.QueryRunCompletion(ctx, owner.RunID)
	if errors.Is(err, engine.ErrWorkflowNotFound) {
		return storage.NewContractError(fmt.Errorf("cancellation owner %q has no engine execution: %w", owner.RunID, err))
	}
	if err != nil {
		return err
	}
	current, err := r.Store.LoadRun(ctx, owner.RunID)
	if err != nil {
		return err
	}
	if current.Status != session.RunStatusRunning || current.SuccessorRunID != "" {
		return nil
	}
	switch completion.Status {
	case engine.RunStatusCompleted, engine.RunStatusFailed, engine.RunStatusCanceled, engine.RunStatusTimedOut:
		return storage.NewContractError(errors.Join(fmt.Errorf("cancellation owner %q closed with engine status %q while storage still reports running", owner.RunID, completion.Status), completion.WorkflowError))
	case engine.RunStatusPending, engine.RunStatusRunning, engine.RunStatusPaused:
		return nil
	default:
		return storage.NewContractError(fmt.Errorf("cancellation owner %q has unknown engine status %q", owner.RunID, completion.Status))
	}
}
