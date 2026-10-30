// Package inmem provides read-only query helpers for accepted in-memory
// workflow executions.
package inmem

import (
	"context"
	"errors"
	"time"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

// QueryAcceptedRun returns the immutable identity metadata and current engine
// status of an accepted workflow execution without retaining or decoding full
// workflow inputs.
func (e *eng) QueryAcceptedRun(ctx context.Context, workflowID string) (engine.AcceptedRun, error) {
	if workflowID == "" {
		return engine.AcceptedRun{}, errors.New("workflow id is required")
	}
	e.mu.RLock()
	h, ok := e.handles[workflowID]
	status := e.statuses[workflowID]
	recipe, recipeOK := e.recipes[workflowID]
	e.mu.RUnlock()
	if !ok {
		return engine.AcceptedRun{}, engine.ErrWorkflowNotFound
	}
	if !recipeOK || recipe.digest == ([32]byte{}) {
		return engine.AcceptedRun{}, errors.New("workflow accepted request digest is missing")
	}
	var completedAt time.Time
	if isTerminalRunStatus(status) {
		select {
		case <-ctx.Done():
			return engine.AcceptedRun{}, ctx.Err()
		case <-h.done:
		}
		h.mu.Lock()
		completedAt = h.completedAt
		h.mu.Unlock()
	}
	return engine.AcceptedRun{
		Subject:     cloneRunSubject(recipe.accepted.subject),
		RunID:       recipe.accepted.runID,
		SessionID:   recipe.accepted.sessionID,
		TurnID:      recipe.accepted.turnID,
		SeedEndID:   recipe.accepted.seedEndID,
		Labels:      cloneLabels(recipe.accepted.labels),
		Status:      status,
		CompletedAt: completedAt,
	}, nil
}

// extractAcceptedRunIdentity copies the small immutable identity fields from an
// accepted RunInput so QueryAcceptedRun does not retain full encoded inputs.
func extractAcceptedRunIdentity(input *api.RunInput) acceptedRunIdentity {
	if input == nil {
		return acceptedRunIdentity{}
	}
	return acceptedRunIdentity{
		subject:   cloneRunSubject(input.Subject),
		runID:     input.RunID,
		sessionID: input.SessionID,
		turnID:    input.TurnID,
		seedEndID: input.SeedEndID,
		labels:    cloneLabels(input.Labels),
	}
}

// cloneRunSubject returns an isolated copy of a RunSubject, deep-copying any
// pointer-backed union branch so caller mutations cannot affect retained state.
func cloneRunSubject(in genrunidentity.RunSubject) genrunidentity.RunSubject {
	out := in
	if st, ok := in.Executor.AsServiceTool(); ok && st != nil {
		copied := *st
		out.Executor.SetServiceTool(&copied)
	}
	return out
}

// cloneLabels returns an isolated copy of a string map when non-empty.
func cloneLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
