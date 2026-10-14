// Package runtime lets provider resolvers select initial messages or an exact
// completed run. Runtime code derives destination identity and storage captures
// the source position; providers cannot supply preparation handles or positions.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	// AgentHistory is the required history selection returned by a provider
	// resolver. Construct it with LiteralAgentHistory or NextTurnAgentHistory;
	// its zero value is rejected. Runtime copies messages when accepting resolver
	// output, so later provider changes cannot alter the published preparation.
	AgentHistory struct {
		kind             agentHistoryKind
		messages         []*model.Message
		systems          []*model.Message
		sourceRunID      string
		excludeReasoning bool
	}
	agentHistoryKind uint8
)

const (
	agentHistoryLiteral agentHistoryKind = iota + 1
	agentHistoryNextTurn
)

// LiteralAgentHistory selects exactly the supplied initial messages. Passing
// nil explicitly selects an empty transcript; it does not infer a prior run.
func LiteralAgentHistory(messages []*model.Message) AgentHistory {
	return AgentHistory{kind: agentHistoryLiteral, messages: messages}
}

// NextTurnAgentHistory selects one successfully completed run in the destination
// Agent and Session. Storage captures its terminal position. New system messages
// replace old systems, and fresh non-system messages follow the saved history.
func NextTurnAgentHistory(completedRunID string, systemMessages, freshMessages []*model.Message) AgentHistory {
	return AgentHistory{kind: agentHistoryNextTurn, sourceRunID: completedRunID, systems: systemMessages, messages: freshMessages}
}

// WithoutPriorReasoning excludes completed private reasoning from supplied and
// inherited messages. Saved exclusions remain effective in all later turns.
func (h AgentHistory) WithoutPriorReasoning() AgentHistory {
	h.excludeReasoning = true
	return h
}

// cloneAgentHistory takes ownership of resolver messages and rejects a missing
// selection before preparation performs any history writes.
func cloneAgentHistory(history AgentHistory) (AgentHistory, error) {
	if history.kind != agentHistoryLiteral && history.kind != agentHistoryNextTurn {
		return AgentHistory{}, errors.New("agent configuration requires an explicit history selection")
	}
	var err error
	history.messages, err = model.CloneMessages(history.messages)
	if err != nil {
		return AgentHistory{}, err
	}
	history.systems, err = model.CloneMessages(history.systems)
	if err != nil {
		return AgentHistory{}, err
	}
	return history, nil
}

// stageAgentHistory binds provider-selected history to the accepted destination
// and preparation command. The existing writers enforce transcript and source
// ownership before their result can be published.
func stageAgentHistory(ctx context.Context, store storage.Store, declaration storage.SeedDeclaration, history AgentHistory) (*initialHistoryWriter, error) {
	switch history.kind {
	case agentHistoryLiteral:
		messages := history.messages
		if history.excludeReasoning {
			var err error
			messages, err = transcript.WithoutCompletedReasoning(messages)
			if err != nil {
				return nil, fmt.Errorf("prepare initial messages without prior reasoning: %w", err)
			}
		}
		declaration.Kind = storage.SeedLiteral
		return stageLiteralHistory(ctx, store, declaration, messages)
	case agentHistoryNextTurn:
		declaration.Kind = storage.SeedNextTurn
		declaration.SourceRunID = history.sourceRunID
		declaration.ExcludeReasoning = history.excludeReasoning
		return stageNextTurnHistory(ctx, store, declaration, history.systems, history.messages)
	default:
		return nil, errors.New("agent configuration requires an explicit history selection")
	}
}
