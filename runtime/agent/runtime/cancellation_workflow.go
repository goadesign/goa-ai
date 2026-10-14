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
	"reflect"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// startSuspendedCancellation places cleanup on the highest parent's worker.
// Active parents obtain routing from engine admission; saved parents use their
// retained preparation or suspension without requiring old engine metadata.
func (r *Runtime) startSuspendedCancellation(ctx context.Context, request CancelRequest, target session.RunMeta) (startErr error) {
	defer func() {
		var conflict *engine.CancellationConflictError
		if errors.As(startErr, &conflict) {
			startErr = &CancellationReasonConflictError{RunID: request.RunID, Reason: request.Reason}
		}
	}()
	for {
		var err error
		target, err = r.selectedCancellationRun(ctx, target)
		if err != nil {
			return err
		}
		// An admitted successor owns the remaining work. Check its permanent
		// closure before reading a checkpoint or requiring a worker route.
		if target.Status == session.RunStatusSuspended {
			settled, err := r.relinquishEmptyRootSuspension(ctx, request, &target)
			if err != nil || settled {
				return err
			}
			if _, continued := target.SuspensionResolution.(session.ContinuedSuspension); continued {
				continue
			}
		}
		if settled, err := settledCancellationTarget(target, request); settled || err != nil {
			return err
		}
		if err := r.Seal(ctx); err != nil {
			return err
		}
		owner, err := r.cancellationRoot(ctx, target)
		if err != nil {
			return err
		}
		switch owner.Status {
		case session.RunStatusRunning:
			err = r.Engine.StartActiveCancellationWorkflow(ctx, owner.RunID, request)
			if errors.Is(err, engine.ErrWorkflowCompleted) || errors.Is(err, engine.ErrWorkflowNotFound) {
				if err := r.observeClosedCancellationOwner(ctx, owner); err != nil {
					return err
				}
				current, err := r.Store.LoadRun(ctx, owner.RunID)
				if err != nil {
					return err
				}
				if current.Status == session.RunStatusRunning && current.SuspensionResolution == nil {
					return storage.NewContractError(errors.New("closed cancellation owner has no stored transition"))
				}
				target, err = r.Store.LoadRun(ctx, request.RunID)
				if err != nil {
					return err
				}
				continue
			}
		case session.RunStatusSuspended:
			if _, service := owner.Subject.Executor.AsServiceTool(); service {
				suspension, loadErr := r.LoadRunSuspension(ctx, owner.RunID)
				if loadErr != nil {
					return loadErr
				}
				checkpoint, decodeErr := decodeRegisteredRootCheckpoint(suspension)
				if decodeErr != nil {
					return decodeErr
				}
				err = r.Engine.StartCancellationWorkflow(ctx, registeredRootWorkflow+".cancel", checkpoint.Queue, request)
				break
			}
			route, _, routeErr := r.cancellationLaunch(ctx, owner)
			if routeErr != nil {
				return routeErr
			}
			err = r.Engine.StartCancellationWorkflow(ctx, route.WorkflowName+".cancel", route.DefaultTaskQueue, request)
		case session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled:
			fallthrough
		default:
			_, err = r.observeSettledCancellationOwner(ctx, owner, request)
		}
		return err
	}
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
	if target.Status == session.RunStatusSuspended {
		settled, err := r.relinquishEmptyRootSuspension(ctx, request, &target)
		if err != nil || settled {
			return settled, err
		}
		if _, continued := target.SuspensionResolution.(session.ContinuedSuspension); continued {
			return false, nil
		}
	}
	if settled, err := settledCancellationTarget(target, request); settled || err != nil {
		return settled, err
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
	owners, err := r.cancellationOwners(ctx, target)
	if err != nil {
		return false, err
	}
	owner := owners[len(owners)-1]
	for _, candidate := range owners {
		if candidate.Status == session.RunStatusRunning {
			owner = candidate
			break
		}
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
		return r.observeSettledCancellationOwner(ctx, owner, request)
	}
	return false, r.startCancellationContinuation(ctx, owner, selected)
}

// observeSettledCancellationOwner reloads the requested child when its parent
// finished after the first child read. The latest admitted successor must have
// settled too; otherwise the parent closed without completing its saved work.
func (r *Runtime) observeSettledCancellationOwner(ctx context.Context, owner session.RunMeta, request engine.CancellationRequest) (bool, error) {
	target, err := r.Store.LoadRun(ctx, request.RunID)
	if err != nil {
		return false, err
	}
	target, err = r.selectedCancellationRun(ctx, target)
	if err != nil {
		return false, err
	}
	if settled, err := settledCancellationTarget(target, request); settled || err != nil {
		return settled, err
	}
	return false, engine.MarkActivityErrorNonRetryable(fmt.Errorf("unfinished run %q has settled owner %q without settling its saved work", target.RunID, owner.RunID))
}

// settledCancellationTarget checks the selected run's stored outcome before job
// admission and during later delivery. Terminal failure and reason conflict must
// remain errors even when another request finished cleanup first.
func settledCancellationTarget(target session.RunMeta, request engine.CancellationRequest) (bool, error) {
	if target.Status != session.RunStatusSuspended && session.IsTerminalRunStatus(target.Status) {
		if target.CancellationReason != "" && target.CancellationReason != request.Reason {
			return false, engine.MarkActivityErrorNonRetryable(&engine.CancellationConflictError{RunID: request.RunID, Reason: request.Reason})
		}
		if target.Status == session.RunStatusFailed && target.CancellationReason != "" {
			return false, engine.MarkActivityErrorNonRetryable(fmt.Errorf("run %q failed while settling cancellation", target.RunID))
		}
		return true, nil
	}
	return false, nil
}

// selectedCancellationRun follows only the successor admitted by storage. Each
// successor must retain the same subject and Session; cycles reject corrupt state.
func (r *Runtime) selectedCancellationRun(ctx context.Context, current session.RunMeta) (session.RunMeta, error) {
	seen := make(map[string]struct{})
	for {
		if _, duplicate := seen[current.RunID]; duplicate {
			return session.RunMeta{}, storage.NewContractError(errors.New("cycle in admitted run successors"))
		}
		seen[current.RunID] = struct{}{}
		resolution, continued := current.SuspensionResolution.(session.ContinuedSuspension)
		if !continued {
			return current, nil
		}
		next, err := r.Store.LoadRun(ctx, resolution.RunID)
		if err != nil {
			return session.RunMeta{}, err
		}
		if current.Status != session.RunStatusSuspended || next.RunID != resolution.RunID || !reflect.DeepEqual(next.Subject, current.Subject) || next.SessionID != current.SessionID {
			return session.RunMeta{}, storage.NewContractError(errors.New("admitted successor does not match its predecessor"))
		}
		current = next
	}
}

// cancellationRoot finds the root execution that owns a saved child. It follows
// actual parent IDs and admitted successors, never a latest-event heuristic.
func (r *Runtime) cancellationRoot(ctx context.Context, current session.RunMeta) (session.RunMeta, error) {
	current, err := r.selectedCancellationRun(ctx, current)
	if err != nil {
		return session.RunMeta{}, err
	}
	owners, err := r.cancellationOwners(ctx, current)
	if err != nil {
		return session.RunMeta{}, err
	}
	return owners[len(owners)-1], nil
}

// cancellationOwners validates every parent and admitted successor. The ordered
// chain supports nearest-running delivery and highest-parent job placement.
func (r *Runtime) cancellationOwners(ctx context.Context, current session.RunMeta) ([]session.RunMeta, error) {
	owners := []session.RunMeta{current}
	seen := make(map[string]struct{})
	for current.ParentRunID != "" {
		if _, duplicate := seen[current.RunID]; duplicate {
			return nil, storage.NewContractError(errors.New("cycle in cancellation parent ownership"))
		}
		seen[current.RunID] = struct{}{}
		parent, err := r.Store.LoadRun(ctx, current.ParentRunID)
		if err != nil {
			return nil, err
		}
		if parent.RunID != current.ParentRunID || parent.SessionID != current.SessionID {
			return nil, storage.NewContractError(errors.New("cancellation parent does not own the child's Session"))
		}
		current, err = r.selectedCancellationRun(ctx, parent)
		if err != nil {
			return nil, err
		}
		owners = append(owners, current)
	}
	return owners, nil
}

// cancellationLaunch derives an initial direct operation's route from its saved
// declaration. Other roots and every published continuation retain the complete
// submitted request, including the caller's memo and search attributes.
func (r *Runtime) cancellationLaunch(ctx context.Context, owner session.RunMeta) (AgentRoute, workflowLaunchSettings, error) {
	executor, ok := owner.Subject.Executor.AsAgent()
	if !ok {
		return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(errors.New("agent cancellation requires an Agent run subject"))
	}
	seed, err := r.Store.LoadRunSeed(ctx, owner.RunID, owner.SeedEndID)
	if err != nil {
		return AgentRoute{}, workflowLaunchSettings{}, err
	}
	declaration := seed.Declaration
	if owner.ParentRunID != "" || seed.EndID != owner.SeedEndID || declaration.RunID != owner.RunID || !reflect.DeepEqual(declaration.Subject, owner.Subject) || declaration.SessionID != owner.SessionID {
		return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
	}
	if (declaration.Kind == storage.SeedLiteral || declaration.Kind == storage.SeedNextTurn) && owner.Status == session.RunStatusSuspended {
		suspension, err := r.LoadRunSuspension(ctx, owner.RunID)
		if err != nil {
			return AgentRoute{}, workflowLaunchSettings{}, err
		}
		checkpoint, err := decodeProviderCheckpoint(suspension)
		if err != nil {
			return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(err)
		}
		if checkpoint.Context.RegisteredInvocation != nil {
			if err := storage.ValidateSeedDeclaration(declaration); err != nil {
				return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(err)
			}
			selected, err := registeredCheckpointDeclaration(checkpoint)
			if err != nil {
				return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(err)
			}
			target := selected.ConsumerContract.Agent
			return AgentRoute{ID: agent.Ident(executor), WorkflowName: target.Workflow, DefaultTaskQueue: target.Queue}, workflowLaunchSettings{}, nil
		}
		if checkpoint.Context.Operation != nil {
			if err := storage.ValidateSeedDeclaration(declaration); err != nil {
				return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(err)
			}
			if declaration.Kind == storage.SeedLiteral {
				if seed.Source != nil {
					return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(storage.ErrSeedConflict)
				}
			} else if seed.Source == nil || seed.Source.RunID != declaration.SourceRunID || seed.Source.EndID == "" ||
				!seed.Source.ExcludeSystem || seed.Source.ExcludeReasoning != declaration.ExcludeReasoning {
				return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(storage.ErrSeedConflict)
			}
			spec, err := compileAgentOperation(checkpoint.Context.Operation, agent.Ident(executor))
			if err != nil {
				return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(err)
			}
			if spec.Name != checkpoint.Context.Tool || checkpoint.Context.ToolRegistry != nil ||
				checkpoint.Context.ParentRunID != "" || checkpoint.Context.ParentAgentID != "" || checkpoint.Context.ParentToolCallID != "" {
				return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
			}
			if _, err := spec.Payload.Codec.FromJSON(checkpoint.Context.ToolArgs); err != nil {
				return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(err)
			}
			target := checkpoint.Context.Operation.ConsumerContract.Agent
			return AgentRoute{ID: agent.Ident(executor), WorkflowName: target.Workflow, DefaultTaskQueue: target.Queue}, workflowLaunchSettings{}, nil
		}
	}
	accepted, found, err := r.Store.FindRunPreparation(ctx, storage.PreparationOperation{
		Subject: owner.Subject, RunID: owner.RunID, SessionID: owner.SessionID, CommandID: declaration.CommandID,
	})
	if err != nil {
		return AgentRoute{}, workflowLaunchSettings{}, err
	}
	if !found {
		return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(errors.New("cancellation owner has no accepted preparation"))
	}
	data, err := readPreparation(ctx, r.Store, accepted)
	if err != nil {
		return AgentRoute{}, workflowLaunchSettings{}, err
	}
	prepared, err := startrecipe.ParsePreparedRequest(data)
	if err != nil {
		return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(err)
	}
	input := prepared.Request.Input
	if input.RunID != owner.RunID || input.ParentRunID != "" || !reflect.DeepEqual(input.Subject, owner.Subject) || input.SessionID != owner.SessionID || input.SeedEndID != owner.SeedEndID {
		return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
	}
	if input.Continuation != nil {
		previous, err := decodeProviderCheckpoint(input.Continuation.Suspension)
		if err != nil {
			return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(err)
		}
		if declaration.Kind != storage.SeedContinuation || seed.Source == nil ||
			declaration.SourceRunID != previous.PreviousRunID || declaration.SourceEndID != previous.HistoryEndID ||
			seed.Source.RunID != previous.PreviousRunID || seed.Source.EndID != previous.HistoryEndID {
			return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(storage.ErrSeedConflict)
		}
	} else if declaration.Kind == storage.SeedContinuation {
		return AgentRoute{}, workflowLaunchSettings{}, storage.NewContractError(storage.ErrSeedConflict)
	}
	return AgentRoute{ID: agent.Ident(executor), WorkflowName: prepared.Request.Workflow, DefaultTaskQueue: prepared.Request.TaskQueue},
		workflowLaunchSettings{taskQueue: prepared.TaskQueueOverride, memo: prepared.Request.Memo, searchAttributes: prepared.Request.SearchAttributes}, nil
}

// startCancellationContinuation publishes an ordinary root continuation with
// the established cancellation operation. A racing answer may win admission;
// the next job observation follows that selected successor instead.
func (r *Runtime) startCancellationContinuation(ctx context.Context, owner session.RunMeta, request engine.CancellationRequest) error {
	if _, service := owner.Subject.Executor.AsServiceTool(); service {
		identity := appendLengthDelimited([]byte("goa-ai-service-cancel-v1"), owner.RunID)
		identity = appendLengthDelimited(identity, request.RunID)
		digest := sha256.Sum256(identity)
		runID := hex.EncodeToString(digest[:])
		prepared, err := r.prepareRegisteredContinuationRequest(ctx, owner.RunID, runID, &api.RunContinuationInput{Cancellation: &request})
		if err != nil {
			return err
		}
		if _, err := r.startWorkflow(ctx, prepared); err != nil {
			return err
		}
		completion, err := r.Engine.QueryRunCompletion(ctx, runID)
		if err != nil {
			return err
		}
		expected, err := startrecipe.SnapshotRequest(prepared)
		if err != nil {
			return err
		}
		if err := validateOperationCompletion(expected, completion); err != nil {
			return err
		}
		if completion.Status == engine.RunStatusFailed || completion.Status == engine.RunStatusTimedOut || completion.Status == engine.RunStatusCanceled {
			current, err := r.Store.LoadRun(ctx, owner.RunID)
			if err != nil {
				return err
			}
			if current.RunID != owner.RunID || current.SessionID != owner.SessionID || !reflect.DeepEqual(current.Subject, owner.Subject) || !reflect.DeepEqual(current.Origin, owner.Origin) {
				return storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
			}
			// A racing answer can own the predecessor before this cancellation
			// starts. Follow that admitted work on the next observation instead
			// of trying to store a second successor's terminal outcome.
			if resolution, continued := current.SuspensionResolution.(session.ContinuedSuspension); continued && resolution.RunID != runID {
				successor, err := r.selectedCancellationRun(ctx, current)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(successor.Origin, owner.Origin) {
					return storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
				}
				return nil
			}
			if current.SuspensionResolution == nil {
				if err := r.completeObservedOperation(ctx, expected, completion); err != nil {
					return err
				}
				// The engine cannot restart this closed attempt. Preserve its
				// outcome and report that saved work has not been cleaned up.
				return engine.MarkActivityErrorNonRetryable(fmt.Errorf("cancellation continuation failed before admission: %w", completion.WorkflowError))
			}
		}
		return r.completeObservedOperation(ctx, expected, completion)
	}
	executor, ok := owner.Subject.Executor.AsAgent()
	if !ok {
		return storage.NewContractError(errors.New("agent cancellation continuation requires an Agent run subject"))
	}
	registration, found := r.agentByID(agent.Ident(executor))
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
		_, launch, err := r.cancellationLaunch(ctx, owner)
		if err != nil {
			return err
		}
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
		if current.SuspensionResolution == nil {
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
	var reasonConflict *CancellationReasonConflictError
	var startConflict *engine.WorkflowStartConflictError
	if errors.As(err, &cancellationConflict) || errors.As(err, &reasonConflict) || errors.As(err, &startConflict) ||
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
	if current.Status != session.RunStatusRunning || current.SuspensionResolution != nil {
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
