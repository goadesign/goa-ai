// Package runtime prepares new turns from a completed run's exact stored history.
// Both clients and provider activities replace old system messages, preserve the
// committed source prefix and apply the same reasoning exclusion rules.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// stageNextTurnHistory validates new messages and the store-selected source,
// then writes new systems, the exact source prefix and fresh input in that order.
// Publication later accepts this history together with the prepared run values.
func stageNextTurnHistory(ctx context.Context, store storage.Store, declaration storage.SeedDeclaration, systemMessages, freshMessages []*model.Message) (*initialHistoryWriter, error) {
	if err := validateSeedPromptFacts(declaration.SessionID, declaration.RenderedPrompts); err != nil {
		return nil, err
	}
	for _, message := range systemMessages {
		if message == nil || message.Role != model.ConversationRoleSystem {
			return nil, errors.New("next-turn system messages must have the system role")
		}
	}
	for _, message := range freshMessages {
		if message == nil || message.Role == model.ConversationRoleSystem {
			return nil, errors.New("next-turn fresh input cannot contain system messages")
		}
	}

	// Saving supplied or retained history performs no provider work. The actual
	// engine start checks any stop returned while binding this exact recipe.
	if _, err := bindHistoryOperation(ctx, store, declaration); err != nil {
		return nil, err
	}
	seed, err := store.BeginRunSeed(ctx, declaration)
	if err != nil {
		return nil, err
	}
	prior, err := transcript.BuildMessagesFromRunLogPrefix(ctx, store, seed.Source.RunID, seed.Source.EndID)
	if err != nil {
		return nil, err
	}
	priorWithoutSystem := prior[:0]
	for _, message := range prior {
		if message.Role != model.ConversationRoleSystem {
			priorWithoutSystem = append(priorWithoutSystem, message)
		}
	}
	if len(priorWithoutSystem) == 0 {
		return nil, errors.New("completed source history has no non-system messages")
	}
	if err := transcript.ValidatePlannerTranscript(priorWithoutSystem); err != nil {
		return nil, fmt.Errorf("invalid completed source transcript: %w", err)
	}
	if declaration.ExcludeReasoning {
		priorWithoutSystem, err = transcript.WithoutCompletedReasoning(priorWithoutSystem)
		if err != nil {
			return nil, err
		}
		systemMessages, err = transcript.WithoutCompletedReasoning(systemMessages)
		if err != nil {
			return nil, err
		}
		freshMessages, err = transcript.WithoutCompletedReasoning(freshMessages)
		if err != nil {
			return nil, err
		}
	}
	complete := make([]*model.Message, 0, len(systemMessages)+len(priorWithoutSystem)+len(freshMessages))
	complete = append(complete, systemMessages...)
	complete = append(complete, priorWithoutSystem...)
	complete = append(complete, freshMessages...)
	if err := transcript.ValidatePlannerTranscript(complete); err != nil {
		return nil, fmt.Errorf("runtime: invalid next-turn transcript: %w", err)
	}
	writer := initialHistoryWriter{store: store, runID: declaration.RunID, attemptID: declaration.AttemptID, endID: storage.EmptySeedEndID}
	if err := writer.appendMessages(ctx, systemMessages); err != nil {
		return nil, err
	}
	if err := writer.appendPrefix(ctx, seed.Source); err != nil {
		return nil, err
	}
	if err := writer.appendMessages(ctx, freshMessages); err != nil {
		return nil, err
	}
	return &writer, nil
}
