package runtime

// A reusable summary belongs to one unchanged prefix of the saved conversation.
// Preparation binds it to the actual request, then passes the candidate through
// that call's context. Only the invocation selected by the planner or recovery
// can carry it to the next activity. Parallel helper calls cannot overwrite it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
)

type preparedHistoryKey struct{}

// prepareRequestHistory admits reusable context only for the exact source and
// positions seen before. A different valid request still uses its normal policy.
func prepareRequestHistory(ctx context.Context, req *model.Request, counter model.TokenCounter, policy HistoryPolicy, canonical []*model.Message, saved *api.HistoryContext) (context.Context, []*model.Message, error) {
	var prior *HistorySummary
	if saved != nil {
		source, positions, complete, err := historySourceHashes(req.Messages, saved.Summary.SourceMessages)
		if err != nil {
			return ctx, nil, err
		}
		if complete && source == saved.SourceSHA256 && positions == saved.SourcePositionsSHA256 {
			if _, err := historySummaryMessages(req.Messages, &saved.Summary); err == nil {
				owned, err := cloneHistoryContext(saved)
				if err != nil {
					return ctx, nil, err
				}
				prior = &owned.Summary
			}
		}
	}
	result, err := policy(ctx, req, counter, prior)
	if err != nil {
		return ctx, nil, err
	}
	var candidate *api.HistoryContext
	if result.Summary != nil {
		expected, err := historySummaryMessages(req.Messages, result.Summary)
		if err != nil {
			return ctx, nil, fmt.Errorf("invalid history summary: %w", err)
		}
		want, err := json.Marshal(expected)
		if err != nil {
			return ctx, nil, err
		}
		got, err := json.Marshal(result.Messages)
		if err != nil {
			return ctx, nil, err
		}
		if !bytes.Equal(want, got) {
			return ctx, nil, errors.New("history policy messages do not match its declared summary replacement")
		}
		source, positions, _, err := historySourceHashes(req.Messages, result.Summary.SourceMessages)
		if err != nil {
			return ctx, nil, err
		}
		canonicalSource, _, complete, err := historySourceHashes(canonical, result.Summary.SourceMessages)
		if err != nil {
			return ctx, nil, err
		}
		if complete && source == canonicalSource {
			// System-only request changes can alter turn boundaries. Such a
			// summary remains valid for this request but cannot be saved.
			if _, err := historySummaryMessages(canonical, result.Summary); err == nil {
				candidate, err = cloneHistoryContext(&api.HistoryContext{
					Summary: *result.Summary, SourceSHA256: source, SourcePositionsSHA256: positions,
				})
				if err != nil {
					return ctx, nil, err
				}
			}
		}
	}
	return context.WithValue(ctx, preparedHistoryKey{}, candidate), result.Messages, nil
}

// validateHistoryContext checks durable activity data against the full saved
// conversation. Request-only System positions are checked later at each call.
func validateHistoryContext(messages []*model.Message, saved *api.HistoryContext) error {
	if saved == nil {
		return nil
	}
	if _, err := model.NewRequestContract(&model.Request{Messages: []*model.Message{&saved.Summary.Message}}); err != nil {
		return fmt.Errorf("invalid saved history summary message: %w", err)
	}
	if _, err := historySummaryMessages(messages, &saved.Summary); err != nil {
		return fmt.Errorf("invalid saved history summary: %w", err)
	}
	for _, digest := range []string{saved.SourceSHA256, saved.SourcePositionsSHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != digest {
			return errors.New("saved history summary has an invalid source fingerprint")
		}
	}
	source, _, complete, err := historySourceHashes(messages, saved.Summary.SourceMessages)
	if err != nil {
		return err
	}
	if !complete || source != saved.SourceSHA256 {
		return errors.New("saved history summary does not match the original conversation")
	}
	return nil
}

// historySourceHashes binds every field of the original non-System prefix and
// its exact message positions separately. Systems remain current instructions,
// while their insertion can change references inside a previous summary.
func historySourceHashes(messages []*model.Message, count int) (string, string, bool, error) {
	var source []*model.Message
	var positions []int
	for i, message := range messages {
		if len(source) == count {
			break
		}
		if message.Role != model.ConversationRoleSystem {
			source = append(source, message)
			positions = append(positions, i)
		}
	}
	if len(source) != count {
		return "", "", false, nil
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return "", "", false, err
	}
	sourceHash := sha256.Sum256(encoded)
	encoded, err = json.Marshal(positions)
	if err != nil {
		return "", "", false, err
	}
	positionHash := sha256.Sum256(encoded)
	return hex.EncodeToString(sourceHash[:]), hex.EncodeToString(positionHash[:]), true, nil
}

func cloneHistoryContext(saved *api.HistoryContext) (*api.HistoryContext, error) {
	if saved == nil {
		return nil, nil
	}
	messages, err := model.CloneMessages([]*model.Message{&saved.Summary.Message})
	if err != nil {
		return nil, err
	}
	owned := *saved
	owned.Summary.Message = *messages[0]
	return &owned, nil
}

// exportHistoryContext uses the journal's existing response selection. A helper
// response or code-only planner result never chooses a new summary.
func (j *modelInvocationJournal) exportHistoryContext(recovery bool) (*api.HistoryContext, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	id := j.selected
	if recovery {
		id = j.recovery
	}
	if id.IsZero() {
		return nil, nil
	}
	return cloneHistoryContext(j.invocations[id].historyContext)
}
