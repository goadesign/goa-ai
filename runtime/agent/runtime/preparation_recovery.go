// Package runtime recovers only the result that the owning store accepted.
// Mutable prompts, profile policy, and request compilation are not repeated.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/storage"
)

// RecoverPrepared returns the complete request accepted for an original command.
func (c *agentClient) RecoverPrepared(ctx context.Context, sessionID, runID, commandID string) (*PreparedRun, bool, error) {
	accepted, found, err := c.r.Store.FindRunPreparation(ctx, storage.PreparationOperation{AgentID: string(c.definition.route.ID), SessionID: sessionID, RunID: runID, CommandID: commandID})
	if err != nil || !found {
		return nil, found, err
	}
	data, err := readPreparation(ctx, c.r.Store, accepted)
	if err != nil {
		return nil, false, err
	}
	return c.parseAcceptedPreparation(data, accepted)
}

// SettlePrepared preserves a published request or closes the exact uploader.
func (c *agentClient) SettlePrepared(ctx context.Context, sessionID, runID, commandID, attemptID string) (*PreparedRun, bool, error) {
	accepted, found, err := c.r.Store.SettleRunPreparation(ctx, storage.PreparationAttempt{Operation: storage.PreparationOperation{AgentID: string(c.definition.route.ID), SessionID: sessionID, RunID: runID, CommandID: commandID}, AttemptID: attemptID})
	if err != nil || !found {
		return nil, found, err
	}
	data, err := readPreparation(ctx, c.r.Store, accepted)
	if err != nil {
		return nil, false, err
	}
	return c.parseAcceptedPreparation(data, accepted)
}

// parseAcceptedPreparation checks that the stored bytes describe the same
// original run and owner as the accepted manifest before exposing the request.
func (c *agentClient) parseAcceptedPreparation(data []byte, accepted storage.RunPreparation) (*PreparedRun, bool, error) {
	compiled, err := startrecipe.ParsePreparedRequest(data)
	if err != nil {
		return nil, false, err
	}
	input := compiled.Request.Input
	if compiled.AgentID != string(c.definition.route.ID) || input.RunID != accepted.Seed.Declaration.RunID || input.SessionID != accepted.Seed.Declaration.SessionID || input.SeedEndID != accepted.Seed.EndID {
		return nil, false, storage.NewContractError(storage.ErrSeedConflict)
	}
	d := accepted.Seed.Declaration
	operation := storage.PreparationOperation{AgentID: d.AgentID, RunID: d.RunID, SessionID: d.SessionID, CommandID: d.CommandID}
	return preparedReference(operation, accepted.Seed.EndID, accepted.EndID, data), true, nil
}

// readPreparation reconstructs only the compiled result suffix. Every page is
// bounded by the store and must advance without exceeding the accepted length.
func readPreparation(ctx context.Context, store storage.Store, accepted storage.RunPreparation) ([]byte, error) {
	if accepted.PreparedBytes <= 0 || accepted.PreparedBytes > int64(startrecipe.PreparedRequestByteLimit()) {
		return nil, storage.NewContractError(errors.New("accepted preparation exceeds the compiled request byte bound"))
	}
	var data []byte
	cursor := ""
	previous := accepted.Seed.EndID
	for {
		page, err := store.ListRunPreparationRecords(ctx, accepted.Seed.Declaration.RunID, accepted.EndID, cursor, 512)
		if err != nil {
			return nil, err
		}
		for _, record := range page.Records {
			if record.PreviousID != previous || record.ID == previous || len(record.Prepared) == 0 || len(record.Messages) > 0 || record.LiteralPart != nil || record.Prefix != nil {
				return nil, storage.NewContractError(errors.New("invalid accepted preparation record"))
			}
			if int64(len(data))+int64(len(record.Prepared)) > accepted.PreparedBytes {
				return nil, storage.NewContractError(errors.New("preparation exceeds accepted byte length"))
			}
			data = append(data, record.Prepared...)
			previous = record.ID
		}
		if page.NextCursor == "" {
			if previous != accepted.EndID || int64(len(data)) != accepted.PreparedBytes {
				return nil, storage.NewContractError(fmt.Errorf("incomplete accepted preparation: got %d bytes, want %d", len(data), accepted.PreparedBytes))
			}
			return data, nil
		}
		if len(page.Records) == 0 || page.NextCursor != previous || page.NextCursor == cursor {
			return nil, storage.NewContractError(errors.New("preparation page made no progress"))
		}
		cursor = page.NextCursor
	}
}
