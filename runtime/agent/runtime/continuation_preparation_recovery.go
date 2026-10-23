// Package runtime compares repeated continuation answers with the published
// request. Matching calls recover its exact launch settings; changed answers
// or caller settings return a conflict without starting another workflow.
package runtime

import (
	"context"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/storage"
)

// recoverPreparedContinuation returns a published request only when its answer,
// predecessor and launch settings match this call. Absence permits first
// preparation; a different saved request returns a conflict before a new start.
func (c *agentClient) recoverPreparedContinuation(ctx context.Context, sessionID, predecessorRunID, runID, turnID, commandID string, response *api.PendingInputResponse, options WorkflowOptions) (*PreparedRun, bool, error) {
	operation := storage.PreparationOperation{
		Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(c.definition.route.ID))},
		SessionID: sessionID, RunID: runID, CommandID: commandID,
	}
	accepted, found, err := c.r.Store.FindRunPreparation(ctx, operation)
	if err != nil || !found {
		return nil, found, err
	}
	data, err := readPreparation(ctx, c.r.Store, accepted)
	if err != nil {
		return nil, false, err
	}
	reference, _, err := c.parseAcceptedPreparation(data, accepted)
	if err != nil {
		return nil, false, err
	}
	prepared, err := startrecipe.ParsePreparedRequest(data)
	if err != nil {
		return nil, false, err
	}
	if accepted.Seed.Declaration.PredecessorRunID != predecessorRunID || prepared.Request.Input.Continuation == nil || prepared.TaskQueueOverride != options.TaskQueue {
		return nil, false, storage.ErrSeedConflict
	}
	launch, err := encodeWorkflowLaunch(options)
	if err != nil {
		return nil, false, continuationContractError(err)
	}
	input := *prepared.Request.Input
	input.TurnID = turnID
	prepared.Request.Input = &input
	prepared.Request.Memo = launch.memo
	prepared.Request.SearchAttributes = launch.searchAttributes
	if options.TaskQueue != "" {
		prepared.Request.TaskQueue = options.TaskQueue
	}
	if err := comparePreparedContinuation(prepared, &api.RunContinuationInput{Response: response}); err != nil {
		return nil, false, err
	}
	return reference, true, nil
}
