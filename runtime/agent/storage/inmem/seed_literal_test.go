package inmem

// Ordered appends and publication share the store lock. Incomplete literals
// remain private, and retries cannot change their original position or closure.

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type literalPageStore struct {
	storage.Store
	pages int
	edit  func(storage.SeedPage) storage.SeedPage
}

func (s *literalPageStore) ListRunSeedRecords(ctx context.Context, runID, endID, afterID string, _ int) (storage.SeedPage, error) {
	page, err := s.Store.ListRunSeedRecords(ctx, runID, endID, afterID, 1)
	s.pages++
	if s.edit != nil && err == nil {
		page = s.edit(page)
	}
	return page, err
}

func TestSeedLiteralClosureAndExactRetry(t *testing.T) {
	store, start := activeRunStore(t)
	d := storage.SeedDeclaration{AgentID: start.AgentID, RunID: start.RunID, SessionID: start.SessionID, CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral}
	_, err := store.BeginRunSeed(t.Context(), d)
	require.NoError(t, err)
	first := storage.SeedAppend{RunID: d.RunID, AttemptID: d.AttemptID, Record: storage.SeedRecord{
		Key: "first", PreviousID: storage.EmptySeedEndID, LiteralPart: &storage.LiteralPart{Data: []byte(`[{"role":"user",`)},
	}}
	end, err := store.AppendRunSeed(t.Context(), first)
	require.NoError(t, err)
	retry, err := store.AppendRunSeed(t.Context(), first)
	require.NoError(t, err)
	require.Equal(t, end, retry)
	changed := first
	changed.Record.LiteralPart = &storage.LiteralPart{Data: bytes.Clone(first.Record.LiteralPart.Data), Final: true}
	_, err = store.AppendRunSeed(t.Context(), changed)
	require.ErrorIs(t, err, storage.ErrSeedConflict)
	changed.Record.LiteralPart.Final = false
	changed.Record.LiteralPart.Data[0] = '{'
	_, err = store.AppendRunSeed(t.Context(), changed)
	require.ErrorIs(t, err, storage.ErrSeedConflict)
	for _, record := range []storage.SeedRecord{
		{Messages: []byte(`[]`)},
		{Prefix: &storage.HistoryPrefix{RunID: "source", EndID: "1"}},
		{Prepared: []byte(`{}`)},
	} {
		record.Key, record.PreviousID = "interleaved", end
		_, err = store.AppendRunSeed(t.Context(), storage.SeedAppend{RunID: d.RunID, AttemptID: d.AttemptID, Record: record})
		require.ErrorIs(t, err, storage.ErrSeedConflict)
	}
	require.ErrorIs(t, store.PublishRunSeed(t.Context(), storage.SeedPublication{
		RunID: d.RunID, AttemptID: d.AttemptID, SeedEndID: end, EndID: end, PreparedBytes: 1,
	}), storage.ErrSeedConflict)
	_, found, err := store.FindRunPreparation(t.Context(), storage.PreparationOperation{
		AgentID: d.AgentID, RunID: d.RunID, SessionID: d.SessionID, CommandID: d.CommandID,
	})
	require.NoError(t, err)
	require.False(t, found)
	last := storage.SeedAppend{RunID: d.RunID, AttemptID: d.AttemptID, Record: storage.SeedRecord{
		Key: "last", PreviousID: end, LiteralPart: &storage.LiteralPart{Data: []byte(`"parts":[{"kind":"text","text":"exact"}]}]`), Final: true},
	}}
	stale := last
	stale.Record.PreviousID = storage.EmptySeedEndID
	_, err = store.AppendRunSeed(t.Context(), stale)
	require.ErrorIs(t, err, storage.ErrSeedConflict)
	final, err := store.AppendRunSeed(t.Context(), last)
	require.NoError(t, err)
	publication := appendSeedTestCompletion(t, store, d, final)
	afterCompiled := last
	afterCompiled.Record.Key, afterCompiled.Record.PreviousID = "late-literal", publication.EndID
	_, err = store.AppendRunSeed(t.Context(), afterCompiled)
	require.ErrorIs(t, err, storage.ErrSeedConflict)
	require.NoError(t, store.PublishRunSeed(t.Context(), publication))
	accepted, found, err := store.SettleRunPreparation(t.Context(), storage.PreparationAttempt{
		Operation: storage.PreparationOperation{AgentID: d.AgentID, RunID: d.RunID, SessionID: d.SessionID, CommandID: d.CommandID},
		AttemptID: d.AttemptID,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, final, accepted.Seed.EndID)
	require.Equal(t, publication.EndID, accepted.EndID)
	for _, original := range []storage.SeedAppend{first, last} {
		position, err := store.AppendRunSeed(t.Context(), original)
		require.NoError(t, err)
		if original.Record.Key == "first" {
			require.Equal(t, end, position)
		} else {
			require.Equal(t, final, position)
		}
	}
	// Neither caller mutation nor mutation of a returned page alters storage.
	first.Record.LiteralPart.Data[0] = '!'
	page, err := store.ListRunSeedRecords(t.Context(), d.RunID, final, "", 1)
	require.NoError(t, err)
	require.Equal(t, byte('['), page.Records[0].LiteralPart.Data[0])
	page.Records[0].LiteralPart.Data[0] = '?'
	page, err = store.ListRunSeedRecords(t.Context(), d.RunID, final, "", 1)
	require.NoError(t, err)
	require.Equal(t, byte('['), page.Records[0].LiteralPart.Data[0])
	start.SeedEndID = final
	result, err := store.StartRootRun(t.Context(), rootStartCommand(t, start))
	require.NoError(t, err)
	messages, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, start.RunID, result.Started.ID)
	require.NoError(t, err)
	require.Equal(t, "exact", messages[0].Text())
}

func TestSeedLiteralSettlementAndPurge(t *testing.T) {
	for _, operation := range []string{"settle", "purge"} {
		t.Run(operation, func(t *testing.T) {
			store, start := activeRunStore(t)
			d := storage.SeedDeclaration{AgentID: start.AgentID, RunID: start.RunID, SessionID: start.SessionID, CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral}
			_, err := store.BeginRunSeed(t.Context(), d)
			require.NoError(t, err)
			command := storage.SeedAppend{RunID: d.RunID, AttemptID: d.AttemptID, Record: storage.SeedRecord{
				Key: "first", PreviousID: storage.EmptySeedEndID, LiteralPart: &storage.LiteralPart{Data: []byte(`[`)},
			}}
			end, err := store.AppendRunSeed(t.Context(), command)
			require.NoError(t, err)
			appendError, beginError := storage.ErrPreparationAbandoned, storage.ErrPreparationAbandoned
			if operation == "settle" {
				_, found, err := store.SettleRunPreparation(t.Context(), storage.PreparationAttempt{
					Operation: storage.PreparationOperation{AgentID: d.AgentID, RunID: d.RunID, SessionID: d.SessionID, CommandID: d.CommandID},
					AttemptID: d.AttemptID,
				})
				require.NoError(t, err)
				require.False(t, found)
			} else {
				_, err := store.EndSession(t.Context(), d.SessionID, time.Now())
				require.NoError(t, err)
				require.NoError(t, store.PurgeSession(t.Context(), d.SessionID))
				require.Empty(t, store.seeds)
				appendError, beginError = storage.ErrSeedNotFound, session.ErrSessionPurged
			}
			command.Record.Key, command.Record.PreviousID = "last", end
			command.Record.LiteralPart = &storage.LiteralPart{Data: []byte(`{"role":"user","parts":[{"kind":"text","text":"late"}]}]`), Final: true}
			_, err = store.AppendRunSeed(t.Context(), command)
			require.ErrorIs(t, err, appendError)
			_, err = store.BeginRunSeed(t.Context(), d)
			require.ErrorIs(t, err, beginError)
			_, err = store.LoadRunSeed(t.Context(), d.RunID, end)
			require.Error(t, err, "partial history cannot become readable")
		})
	}
}

func TestSeedLiteralReaderAcrossRealPages(t *testing.T) {
	for _, defect := range []string{"none", "missing final", "whole interleaved", "prefix interleaved", "prepared interleaved", "reordered", "repeated", "invalid UTF-8", "oversized page", "truncated page", "cancel between pages"} {
		t.Run(defect, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store, start := activeRunStore(t)
			d := storage.SeedDeclaration{AgentID: start.AgentID, RunID: start.RunID, SessionID: start.SessionID, CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral}
			_, err := store.BeginRunSeed(ctx, d)
			require.NoError(t, err)
			messages := []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "before 雪 after"}}}}
			data, err := transcript.EncodeRunLogDelta(messages)
			require.NoError(t, err)
			split := bytes.Index(data, []byte("雪")) + 1
			require.Greater(t, split, 1)
			end := storage.EmptySeedEndID
			for index, part := range [][]byte{data[:split], data[split:]} {
				end, err = store.AppendRunSeed(ctx, storage.SeedAppend{RunID: d.RunID, AttemptID: d.AttemptID, Record: storage.SeedRecord{
					Key: fmt.Sprint(index), PreviousID: end, LiteralPart: &storage.LiteralPart{Data: part, Final: index == 1},
				}})
				require.NoError(t, err)
			}
			require.NoError(t, store.PublishRunSeed(ctx, appendSeedTestCompletion(t, store, d, end)))
			start.SeedEndID = end
			started, err := store.StartRootRun(ctx, rootStartCommand(t, start))
			require.NoError(t, err)
			reader := &literalPageStore{Store: store}
			reader.edit = func(page storage.SeedPage) storage.SeedPage {
				if reader.pages == 1 && defect == "cancel between pages" {
					cancel()
				}
				if reader.pages == 1 && defect == "truncated page" {
					page.NextCursor = ""
				}
				if reader.pages != 2 {
					return page
				}
				record := &page.Records[0]
				switch defect {
				case "missing final":
					record.LiteralPart.Final = false
				case "whole interleaved":
					record.LiteralPart, record.Messages = nil, []byte(`[]`)
				case "prefix interleaved":
					record.LiteralPart, record.Prefix = nil, &storage.HistoryPrefix{RunID: "source", EndID: "1"}
				case "prepared interleaved":
					record.LiteralPart, record.Prepared = nil, []byte(`{}`)
				case "reordered":
					record.PreviousID = storage.EmptySeedEndID
				case "repeated":
					record.ID = record.PreviousID
				case "invalid UTF-8":
					record.LiteralPart.Data[0] = 0xff
				case "oversized page":
					record.LiteralPart.Data = make([]byte, storage.MaxSeedPageBytes)
				}
				return page
			}
			actual, err := transcript.BuildMessagesFromRunLogPrefix(ctx, reader, d.RunID, started.Started.ID)
			if defect == "none" {
				require.NoError(t, err)
				require.Equal(t, messages, actual)
				require.Equal(t, 2, reader.pages)
			} else {
				require.Error(t, err)
				require.Nil(t, actual)
				if defect == "cancel between pages" {
					require.ErrorIs(t, err, context.Canceled)
					require.Equal(t, 1, reader.pages)
				} else {
					var contract *storage.ContractError
					require.ErrorAs(t, err, &contract)
					if defect == "oversized page" || defect == "truncated page" {
						require.NotErrorIs(t, err, transcript.ErrInvalidHistory, "an invalid provider page does not certify corrupt saved content")
					}
				}
			}
			meta, err := store.LoadRun(t.Context(), d.RunID)
			require.NoError(t, err)
			require.Equal(t, session.RunStatusRunning, meta.Status)
		})
	}
}
