// Terminal writes must validate stored JSON before changing run state or history.
// Valid text must retain its exact value through storage and public replay.
package inmem

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
)

func TestRecordRunTerminalValidatesTextBeforeAppend(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		quoted string
		text   string
	}{
		{name: "invalid byte", quoted: "\"\xff\""},
		{name: "truncated sequence", quoted: "\"\xe2\x82\""},
		{name: "overlong sequence", quoted: "\"\xc0\xaf\""},
		{name: "multibyte", quoted: `"éΩ雪🙂"`, text: "éΩ雪🙂"},
		{name: "distinct spellings", quoted: `"é e\u0301"`, text: "é e\u0301"},
		{name: "escaped replacement", quoted: `"\ufffd"`, text: "\ufffd"},
		{name: "escaped controls", quoted: `"\u0000\n\t"`, text: "\x00\n\t"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store := New()
			start := session.RunStart{
				AgentID: "agent", RunID: "run", SessionID: "session",
				StartedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
				Labels:    map[string]string{"text": "é e\u0301"},
			}
			_, err := store.CreateSession(ctx, start.SessionID, start.StartedAt)
			require.NoError(t, err)
			_, err = store.StartRootRun(ctx, rootStartCommand(t, start))
			require.NoError(t, err)
			before, err := store.LoadRun(ctx, start.RunID)
			require.NoError(t, err)
			beforeRecords, err := store.ListRunRecords(ctx, start.RunID, "", 10)
			require.NoError(t, err)
			record := completedRecord(t, "terminal", start, "failed", nil)
			// The fixture sets the message explicitly so the test does not depend
			// on the default summary chosen for an unclassified execution error.
			event, err := hooks.DecodeRunlogEvent(record)
			require.NoError(t, err)
			event.(*hooks.RunCompletedEvent).Failure = &run.Failure{
				Kind: "execution_failed", Message: "record-text", DebugMessage: "details",
			}
			record.Payload, err = hooks.EncodeRecordPayload(event)
			require.NoError(t, err)
			corrected := bytes.Clone(record.Payload)
			record.Payload = bytes.ReplaceAll(record.Payload, []byte(`"record-text"`), []byte(test.quoted))
			command := storage.RunTerminal{RunID: start.RunID, Status: session.RunStatusFailed, Record: record}

			validationErr := lifecycle.ValidateRunTerminal(command, before)
			if test.text == "" {
				require.ErrorContains(t, validationErr, "record payload contains invalid UTF-8")
				_, err := store.RecordRunTerminal(ctx, command)
				require.ErrorContains(t, err, "record payload contains invalid UTF-8")
				var contractErr *storage.ContractError
				require.ErrorAs(t, err, &contractErr)
				after, err := store.LoadRun(ctx, start.RunID)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				afterRecords, err := store.ListRunRecords(ctx, start.RunID, "", 10)
				require.NoError(t, err)
				assert.Equal(t, beforeRecords, afterRecords)
				// A valid retry with the same event key must still be a new append.
				record.Payload = corrected
			} else {
				require.NoError(t, validationErr)
			}

			result, err := store.RecordRunTerminal(ctx, command)
			require.NoError(t, err)
			assert.True(t, result.Inserted)
			after, err := store.LoadRun(ctx, start.RunID)
			require.NoError(t, err)
			assert.Equal(t, session.RunStatusFailed, after.Status)
			assert.Equal(t, start.StartedAt, after.StartedAt)
			records, err := store.ListRunRecords(ctx, start.RunID, "", 10)
			require.NoError(t, err)
			require.Len(t, records.Events, 2)
			assert.Equal(t, record.Payload, records.Events[1].Payload)
			decoded, err := hooks.DecodeRunlogEvent(records.Events[1])
			require.NoError(t, err)
			completed := decoded.(*hooks.RunCompletedEvent)
			wantText := test.text
			if wantText == "" {
				wantText = "record-text"
			}
			assert.Equal(t, &run.Failure{
				Kind: "execution_failed", Message: wantText, DebugMessage: "details",
			}, completed.Failure)
			assert.Equal(t, start.Labels, completed.Labels)
			assert.Equal(t, record.Timestamp.UnixMilli(), completed.Timestamp())
		})
	}
}
