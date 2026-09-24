package inmem

// Long histories must grow by fresh records and small references. Counting the
// actual store calls also detects recursive reconstruction of every ancestor.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestSeedChainLinearHistoryAndComposedReasoning(t *testing.T) {
	const turns = 300
	store := New()
	now := time.UnixMilli(1_000_000)
	_, err := store.CreateSession(t.Context(), "session", now)
	require.NoError(t, err)
	var expected []*model.Message
	var previous, lastEnd string
	var savedBytes int
	for i := range turns {
		runID := fmt.Sprintf("run-%04d", i)
		kind := storage.SeedLiteral
		if i > 0 {
			kind = storage.SeedNextTurn
		}
		seed, err := store.BeginRunSeed(t.Context(), storage.SeedDeclaration{
			AgentID: "agent", RunID: runID, SessionID: "session", CommandID: runID, AttemptID: runID, Kind: kind,
			SourceRunID: previous, ExcludeReasoning: i == 1,
		})
		require.NoError(t, err)
		system := &model.Message{Role: model.ConversationRoleSystem, Parts: []model.Part{
			model.TextPart{Text: fmt.Sprintf("system-%d", i)},
		}}
		end := appendSeedChainLiteral(t, store, runID, storage.EmptySeedEndID, "system", []*model.Message{system})
		if seed.Source != nil {
			end, err = store.AppendRunSeed(t.Context(), storage.SeedAppend{
				RunID: runID, AttemptID: runID, Record: storage.SeedRecord{Key: "source", PreviousID: end, Prefix: seed.Source},
			})
			require.NoError(t, err)
		}
		fresh := &model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.TextPart{Text: fmt.Sprintf("input-%d:%s", i, strings.Repeat("data ", 200))},
		}}
		end = appendSeedChainLiteral(t, store, runID, end, "input", []*model.Message{fresh})
		require.NoError(t, store.PublishRunSeed(t.Context(), appendSeedTestCompletion(t, store, seed.Declaration, end)))
		start := session.RunStart{
			AgentID: "agent", RunID: runID, SessionID: "session", SeedEndID: end, StartedAt: now,
		}
		_, err = store.StartRootRun(t.Context(), rootStartCommand(t, start))
		require.NoError(t, err)
		answer := &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Text: fmt.Sprintf("reasoning-%d", i), Signature: "native", Final: true},
			model.TextPart{Text: fmt.Sprintf("answer-%d", i)},
		}}
		payload, err := transcript.EncodeRunLogDelta([]*model.Message{answer})
		require.NoError(t, err)
		_, err = store.AppendRunRecord(t.Context(), &runlog.Event{
			RunID: runID, AgentID: "agent", SessionID: "session", EventKey: "answer",
			Type: transcript.RunLogMessagesAppended, Payload: payload, Timestamp: now,
		})
		require.NoError(t, err)
		result, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID: runID, Status: session.RunStatusCompleted,
			Record: completedRecord(t, "completed", start, "success", nil),
		})
		require.NoError(t, err)
		lastEnd, previous = result.ID, runID
		if len(expected) > 0 {
			expected = expected[1:]
		}
		if i == 1 {
			expected, err = transcript.WithoutCompletedReasoning(expected)
			require.NoError(t, err)
		}
		expected = append([]*model.Message{system}, expected...)
		expected = append(expected, fresh, answer)
		for _, record := range store.seeds[runID].records {
			encoded, err := json.Marshal(record)
			require.NoError(t, err)
			savedBytes += len(encoded)
		}
		savedBytes += len(payload)
	}
	reader := &countingSeedReader{Store: store}
	actual, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), reader, previous, lastEnd)
	require.NoError(t, err)
	wantBytes, err := transcript.EncodeRunLogDelta(expected)
	require.NoError(t, err)
	actualBytes, err := transcript.EncodeRunLogDelta(actual)
	require.NoError(t, err)
	require.Equal(t, wantBytes, actualBytes)
	require.Equal(t, turns, reader.seedPages)
	require.Equal(t, turns, reader.runPages)
	require.Less(t, savedBytes, turns*2_000, "each turn stores fresh bytes and a reference, not accumulated history")
	require.Len(t, actual[2].Parts, 1, "a later turn cannot restore reasoning excluded by turn two")
	require.Len(t, actual[len(actual)-1].Parts, 2, "later reasoning remains when exclusion was not requested")
}

type countingSeedReader struct {
	*Store
	seedPages int
	runPages  int
}

func (r *countingSeedReader) ListRunSeedRecords(ctx context.Context, runID, endID, afterID string, limit int) (storage.SeedPage, error) {
	r.seedPages++
	return r.Store.ListRunSeedRecords(ctx, runID, endID, afterID, limit)
}

func (r *countingSeedReader) ListRunTranscriptRecords(ctx context.Context, runID, endID, afterID string, limit int) (runlog.Page, error) {
	r.runPages++
	return r.Store.ListRunTranscriptRecords(ctx, runID, endID, afterID, limit)
}

func appendSeedChainLiteral(t *testing.T, store *Store, runID, previous, key string, messages []*model.Message) string {
	t.Helper()
	payload, err := transcript.EncodeRunLogDelta(messages)
	require.NoError(t, err)
	end, err := store.AppendRunSeed(t.Context(), storage.SeedAppend{
		RunID: runID, AttemptID: runID, Record: storage.SeedRecord{Key: key, PreviousID: previous, Messages: payload},
	})
	require.NoError(t, err)
	return end
}
