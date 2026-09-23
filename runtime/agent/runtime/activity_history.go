package runtime

// Activities load the exact saved transcript named by their input. Complete
// messages stay within the activity and never return to its command payload.

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type resolvedPlanActivityInput struct {
	*PlanActivityInput
	Messages []*model.Message
}

// resolvePlanActivityInput checks the run identity and loads its frozen history.
// Finalization adds its existing instruction only to this invocation's messages.
func (r *Runtime) resolvePlanActivityInput(ctx context.Context, input *PlanActivityInput) (*resolvedPlanActivityInput, error) {
	if input.RunID != input.RunContext.RunID {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("planner run id does not match run context"))
	}
	messages, err := r.loadActivityHistory(ctx, input.AgentID, input.RunID, input.RunContext.SessionID, input.HistoryEndID)
	if err != nil {
		return nil, err
	}
	if err := transcript.ValidatePlannerTranscript(messages); err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(fmt.Errorf("invalid stored planner transcript: %w", err))
	}
	if input.Finalize != nil && input.Finalize.Message != "" {
		messages = append(messages, newTextAgentMessage(model.ConversationRoleSystem, input.Finalize.Message))
	}
	return &resolvedPlanActivityInput{PlanActivityInput: input, Messages: messages}, nil
}

// loadActivityHistory rejects a foreign run before reading its transcript.
// Missing owners and invalid positions cannot become valid by retrying.
func (r *Runtime) loadActivityHistory(ctx context.Context, agentID agent.Ident, runID, sessionID, endID string) ([]*model.Message, error) {
	if agentID == "" || runID == "" || endID == "" {
		return nil, engine.MarkActivityErrorNonRetryable(errors.New("history requires agent, run and end record identifiers"))
	}
	meta, err := r.Store.LoadRun(ctx, runID)
	if err == nil && (meta.RunID != runID || meta.AgentID != string(agentID) || meta.SessionID != sessionID) {
		err = storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
	}
	if err != nil {
		return nil, activityHistoryError(err)
	}
	messages, err := transcript.BuildMessagesFromRunLogPrefix(ctx, r.Store, runID, endID)
	if err != nil {
		return nil, activityHistoryError(err)
	}
	return messages, nil
}

// activityHistoryError retains temporary store errors while rejecting requests
// whose immutable owner or position is invalid.
func activityHistoryError(err error) error {
	var contractErr *storage.ContractError
	if errors.As(err, &contractErr) || errors.Is(err, session.ErrRunNotFound) ||
		errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, session.ErrSessionPurged) {
		return engine.MarkActivityErrorNonRetryable(err)
	}
	return err
}
