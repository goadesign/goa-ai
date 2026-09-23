package transcript_test

// These tests exercise exact prefix replay against the runtime store and
// malformed store responses at the paging boundary.

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
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type prefixPageFunc func(context.Context, string, string, string, int) (runlog.Page, error)

func (f prefixPageFunc) ListRunTranscriptRecords(ctx context.Context, runID, endID, after string, limit int) (runlog.Page, error) {
	return f(ctx, runID, endID, after, limit)
}

func TestRunLogPrefixPreservesLargeHistoryAndExcludesLaterWrites(t *testing.T) {
	store := newTranscriptTestStore(t, t.Context())
	initial, err := store.ListRunRecords(t.Context(), "run-1", "", 1)
	require.NoError(t, err)
	empty, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, "run-1", initial.Events[0].ID)
	require.NoError(t, err)
	require.Empty(t, empty)

	expected := make([]*model.Message, 0, 514)
	var endID string
	for i := range 514 {
		message := &model.Message{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: fmt.Sprintf("%d:%s", i, strings.Repeat("<evidence>&", 220))}},
			Meta:  map[string]any{"original": json.RawMessage(`{"argument":"<exact>"}`)},
		}
		payload, err := transcript.EncodeRunLogDelta([]*model.Message{message})
		require.NoError(t, err)
		result, err := store.AppendRunRecord(t.Context(), &runlog.Event{
			RunID: "run-1", AgentID: "agent-1", SessionID: "session-1",
			EventKey: fmt.Sprintf("prefix-%d", i), Type: transcript.RunLogMessagesAppended,
			Payload: payload, Timestamp: time.UnixMilli(int64(i)),
		})
		require.NoError(t, err)
		endID = result.ID
		expected = append(expected, message)
	}
	appendTranscriptDelta(t, t.Context(), store, "later", []*model.Message{{
		Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "later"}},
	}})
	actual, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, "run-1", endID)
	require.NoError(t, err)
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	actualJSON, err := json.Marshal(actual)
	require.NoError(t, err)
	require.Greater(t, len(expectedJSON), 1_048_576)
	require.Equal(t, expectedJSON, actualJSON) //nolint:testifylint // Exact encoded bytes, including escaping, must survive replay.
	retry, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, "run-1", endID)
	require.NoError(t, err)
	require.Equal(t, actual, retry)
}

func TestRunLogPrefixRejectsInvalidPositions(t *testing.T) {
	store := newTranscriptTestStore(t, t.Context())
	_, err := store.ListRunTranscriptRecords(t.Context(), "run-1", "9999", "", 1)
	require.ErrorIs(t, err, storage.ErrInvalidTranscriptPosition)
	first, err := store.ListRunRecords(t.Context(), "run-1", "", 1)
	require.NoError(t, err)
	_, err = store.ListRunTranscriptRecords(t.Context(), "run-1", first.Events[0].ID, first.Events[0].ID, 1)
	require.ErrorIs(t, err, storage.ErrInvalidTranscriptPosition, "a start record is not a transcript page cursor")
	_, err = store.ListRunTranscriptRecords(t.Context(), "run-1", "", "", 1)
	require.ErrorIs(t, err, storage.ErrInvalidTranscriptPosition)
}

func TestRunLogPrefixRejectsBrokenPagesAndHonorsCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		page runlog.Page
	}{
		{"empty continuation", runlog.Page{NextCursor: "next"}},
		{"foreign run", runlog.Page{Events: []*runlog.Event{{ID: "1", RunID: "other", Type: transcript.RunLogMessagesSeeded, Payload: []byte(`[]`)}}}},
		{"malformed payload", runlog.Page{Events: []*runlog.Event{{ID: "1", RunID: "run", Type: transcript.RunLogMessagesSeeded, Payload: []byte(`{`)}}}},
		{"non transcript", runlog.Page{Events: []*runlog.Event{{ID: "1", RunID: "run", Type: "other", Payload: []byte(`[]`)}}}},
		{"repeated page", runlog.Page{Events: []*runlog.Event{{ID: "1", RunID: "run", Type: transcript.RunLogMessagesSeeded, Payload: []byte(`[]`)}}, NextCursor: "1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			store := prefixPageFunc(func(context.Context, string, string, string, int) (runlog.Page, error) {
				calls++
				require.LessOrEqual(t, calls, 2)
				return test.page, nil
			})
			_, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, "run", "end")
			require.Error(t, err)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store := prefixPageFunc(func(context.Context, string, string, string, int) (runlog.Page, error) {
		t.Fatal("canceled read reached the store")
		return runlog.Page{}, nil
	})
	_, err := transcript.BuildMessagesFromRunLogPrefix(ctx, store, "run", "end")
	require.ErrorIs(t, err, context.Canceled)
	deadlineStore := prefixPageFunc(func(context.Context, string, string, string, int) (runlog.Page, error) {
		return runlog.Page{}, context.DeadlineExceeded
	})
	_, err = transcript.BuildMessagesFromRunLogPrefix(t.Context(), deadlineStore, "run", "end")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
