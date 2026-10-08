package runtime

// The workflow asks this activity whether selected saved work still has pages
// before spending recovery capacity or choosing a planner deadline. Reads stay
// outside workflow replay; the recorded boolean chooses the existing branch.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

const continuationActivityName = "runtime.continuation_available"

// continuationAvailableActivity derives a single decision from exact owned
// history and current outputs. Invalid ownership or read failures remain errors.
func (r *Runtime) continuationAvailableActivity(ctx context.Context, request *api.ContinuationActivityInput) (available bool, activityErr error) {
	ctx, span := r.tracer.Start(ctx, "runtime.continuation_available")
	defer func() {
		if activityErr != nil {
			span.RecordError(activityErr)
			span.SetStatus(codes.Error, "continuation availability failed")
		} else {
			span.SetAttributes(attribute.Bool("continuation.available", available))
		}
		span.End()
	}()
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()
	if request == nil {
		return false, engine.MarkActivityErrorNonRetryable(errors.New("continuation activity input is required"))
	}
	if err := enforceContinuationActivityBudget(request); err != nil {
		return false, engine.MarkActivityErrorNonRetryable(err)
	}
	if _, registered := r.agentByID(request.AgentID); !registered {
		return false, engine.MarkActivityErrorNonRetryable(ErrAgentNotFound)
	}
	input, err := r.resolvePlanActivityInput(ctx, &PlanActivityInput{
		AgentID: request.AgentID, RunID: request.RunID, HistoryEndID: request.HistoryEndID,
		ToolOutputs: request.ToolOutputs,
		RunContext:  run.Context{RunID: request.RunID, SessionID: request.SessionID, TextOnly: request.TextOnly},
		Policy:      &PolicyOverrides{TextOnly: request.TextOnly, RestrictToTool: request.RestrictToTool, TagClauses: request.TagClauses},
	})
	if err != nil {
		return false, err
	}
	// Current output references carry no ownership grant. Validate each owner
	// before reading its events, including checkpoint predecessor runs.
	owners := make(map[string]struct{})
	seen := make(map[api.ToolOutputRef]struct{}, len(request.ToolOutputs))
	for _, ref := range request.ToolOutputs {
		if ref == nil || ref.CallRunID == "" || ref.ResultRunID == "" || ref.ToolCallID == "" {
			return false, engine.MarkActivityErrorNonRetryable(errors.New("continuation output requires call owner, result owner and execution ID"))
		}
		if _, duplicate := seen[*ref]; duplicate {
			return false, engine.MarkActivityErrorNonRetryable(errors.New("duplicate continuation output reference"))
		}
		seen[*ref] = struct{}{}
		for _, runID := range []string{ref.CallRunID, ref.ResultRunID} {
			if _, checked := owners[runID]; checked {
				continue
			}
			meta, err := r.Store.LoadRun(ctx, runID)
			if err != nil {
				return false, activityHistoryError(err)
			}
			if meta.RunID != runID || meta.AgentID != string(request.AgentID) || meta.SessionID != request.SessionID {
				return false, engine.MarkActivityErrorNonRetryable(errors.New("continuation output owner mismatch"))
			}
			owners[runID] = struct{}{}
		}
	}
	outputs, err := r.loadPlannerToolOutputs(ctx, request.ToolOutputs)
	if err != nil {
		return false, activityHistoryError(err)
	}
	specs := make(map[tools.Ident]tools.ToolSpec)
	for _, spec := range r.ToolSpecsForAgent(request.AgentID) {
		specs[spec.Name] = spec
	}
	actions, err := r.continuationActionsForHistory(ctx, input, specs, outputs)
	if err != nil {
		return false, activityHistoryError(err)
	}
	return len(actions) > 0, nil
}

// ensureContinuationActivityRegistered uses the existing storage-read attempt
// timeout and retry policy. The workflow further bounds the complete read by
// its remaining hard deadline, without changing either run deadline.
func (r *Runtime) ensureContinuationActivityRegistered(ctx context.Context) error {
	r.mu.Lock()
	if r.continuationActivityRegistered {
		r.mu.Unlock()
		return nil
	}
	timeout := defaultStorageActivityTimeout
	if r.storageActivityTimeout > 0 {
		timeout = r.storageActivityTimeout
	}
	opts := engine.ActivityOptions{StartToCloseTimeout: timeout, RetryPolicy: defaultRetriedActivityPolicy()}
	r.mu.Unlock()
	if err := r.Engine.RegisterContinuationActivity(ctx, continuationActivityName, opts, r.continuationAvailableActivity); err != nil {
		return err
	}
	r.mu.Lock()
	r.continuationActivityRegistered = true
	r.mu.Unlock()
	return nil
}

// continuationAvailable schedules a recorded read before the existing recovery
// branch. The read consumes elapsed time but never changes counters or deadlines.
func (l *workflowLoop) continuationAvailable() (bool, error) {
	refs, err := encodePlannerToolOutputs(l.st.ToolOutputs)
	if err != nil {
		return false, err
	}
	request := &api.ContinuationActivityInput{
		AgentID: l.input.AgentID, RunID: l.base.RunContext.RunID,
		SessionID: l.base.RunContext.SessionID, HistoryEndID: l.base.HistoryEndID,
		ToolOutputs: refs, TextOnly: l.base.RunContext.TextOnly,
	}
	if l.input.Policy != nil {
		request.RestrictToTool = l.input.Policy.RestrictToTool
		request.TagClauses = cloneTagPolicyClauses(l.input.Policy.TagClauses)
	}
	if err := enforceContinuationActivityBudget(request); err != nil {
		return false, err
	}
	options := engine.ActivityOptions{}
	if !l.deadlines.Hard.IsZero() {
		remaining := l.deadlines.Hard.Sub(l.wfCtx.Now())
		if remaining <= 0 {
			return false, context.DeadlineExceeded
		}
		options.ScheduleToCloseTimeout = remaining
	}
	return l.wfCtx.ExecuteContinuationActivity(engine.ContinuationActivityCall{
		Name: continuationActivityName, Input: request, Options: options,
	})
}

// enforceContinuationActivityBudget applies the existing planner-command byte
// allowance to this smaller command before engine scheduling or storage reads.
func enforceContinuationActivityBudget(input *api.ContinuationActivityInput) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if len(data) > maxPlanActivityInputBytes {
		return fmt.Errorf("continuation activity input exceeds budget (%d > %d bytes)", len(data), maxPlanActivityInputBytes)
	}
	return nil
}
