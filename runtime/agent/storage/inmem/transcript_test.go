package inmem

// These tests admit valid transcript records through the store, then verify
// that each returned page is bounded without limiting complete replay.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestTranscriptPageIncludesExactPayloadByteLimit(t *testing.T) {
	for _, size := range []int{transcriptPageMaxPayloadBytes - 1, transcriptPageMaxPayloadBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store, ids, _ := transcriptPageFixture(t, size)
			page, err := store.ListRunTranscriptRecords(t.Context(), "run", ids[0], "", 10000)
			require.NoError(t, err)
			require.Len(t, page.Events, 1)
			require.Len(t, page.Events[0].Payload, size)
			require.Empty(t, page.NextCursor)
		})
	}
}

func TestTranscriptPageStopsBeforeCopyingTheNextRecord(t *testing.T) {
	store, ids, expected := transcriptPageFixture(t, transcriptPageMaxPayloadBytes-256, 256, 256)
	first, err := store.ListRunTranscriptRecords(t.Context(), "run", ids[2], "", 10000)
	require.NoError(t, err)
	require.Len(t, first.Events, 2)
	require.Equal(t, transcriptPageMaxPayloadBytes, len(first.Events[0].Payload)+len(first.Events[1].Payload))
	require.Equal(t, ids[1], first.NextCursor)
	second, err := store.ListRunTranscriptRecords(t.Context(), "run", ids[2], first.NextCursor, 10000)
	require.NoError(t, err)
	require.Len(t, second.Events, 1)
	require.Equal(t, ids[2], second.Events[0].ID)
	require.Empty(t, second.NextCursor)

	// Editing a returned copy cannot alter the stored record.
	firstByte := first.Events[0].Payload[0]
	first.Events[0].Payload[0] = '!'
	again, err := store.ListRunTranscriptRecords(t.Context(), "run", ids[2], "", 10000)
	require.NoError(t, err)
	require.Equal(t, firstByte, again.Events[0].Payload[0])
	actual, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, "run", ids[2])
	require.NoError(t, err)
	wantJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(actual)
	require.NoError(t, err)
	require.True(t, bytes.Equal(wantJSON, gotJSON), "exact encoding must survive multiple pages")
}

func TestTranscriptPageCapsAnOversizedRequestedCount(t *testing.T) {
	sizes := make([]int, transcriptPageMaxRecords+1)
	for i := range sizes {
		sizes[i] = 256
	}
	store, ids, _ := transcriptPageFixture(t, sizes...)
	endID := ids[len(ids)-1]
	first, err := store.ListRunTranscriptRecords(t.Context(), "run", endID, "", 10000)
	require.NoError(t, err)
	require.Len(t, first.Events, transcriptPageMaxRecords)
	require.Equal(t, ids[transcriptPageMaxRecords-1], first.NextCursor)
	second, err := store.ListRunTranscriptRecords(t.Context(), "run", endID, first.NextCursor, 10000)
	require.NoError(t, err)
	require.Len(t, second.Events, 1)
	require.Equal(t, endID, second.Events[0].ID)
	require.Empty(t, second.NextCursor)
}

func TestTranscriptPageRejectsAnUnsupportedSingleRecord(t *testing.T) {
	store, ids, _ := transcriptPageFixture(t, transcriptPageMaxPayloadBytes+1)
	page, err := store.ListRunTranscriptRecords(t.Context(), "run", ids[0], "", 10000)
	var contractErr *storage.ContractError
	require.ErrorAs(t, err, &contractErr)
	require.ErrorContains(t, err, "payload size 1048577 exceeds supported page size 1048576 bytes")
	require.Empty(t, page.Events)
	require.Empty(t, page.NextCursor)
}

// transcriptPageFixture saves messages whose encoded deltas have the requested
// exact sizes. The ordinary append contract remains independent of page limits.
func transcriptPageFixture(t *testing.T, sizes ...int) (*Store, []string, []*model.Message) {
	t.Helper()
	store := New()
	start := session.RunStart{AgentID: "agent", RunID: "run", StartedAt: time.Now().UTC().Truncate(time.Millisecond)}
	_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{
		Run: start, Started: startedRecord(t, "started", start),
	})
	require.NoError(t, err)
	ids := make([]string, 0, len(sizes))
	messages := make([]*model.Message, 0, len(sizes))
	for i, size := range sizes {
		message := &model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{}}}
		empty, err := transcript.EncodeRunLogDelta([]*model.Message{message})
		require.NoError(t, err)
		require.GreaterOrEqual(t, size, len(empty))
		message.Parts[0] = model.TextPart{Text: strings.Repeat("x", size-len(empty))}
		payload, err := transcript.EncodeRunLogDelta([]*model.Message{message})
		require.NoError(t, err)
		require.Len(t, payload, size)
		record, err := store.AppendRunRecord(t.Context(), &runlog.Event{
			RunID: start.RunID, AgentID: agent.Ident(start.AgentID),
			EventKey: fmt.Sprintf("message-%d", i), Timestamp: start.StartedAt,
			Type: transcript.RunLogMessagesAppended, Payload: payload,
		})
		require.NoError(t, err)
		ids = append(ids, record.ID)
		messages = append(messages, message)
	}
	return store, ids, messages
}
