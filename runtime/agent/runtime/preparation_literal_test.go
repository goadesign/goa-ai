package runtime

// Published compiled bytes must remain a distinct suffix even when a host
// returns a syntactically valid fragment alongside a compiled part.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/storage"
)

type mixedPreparationStore struct{ storage.Store }

func (s mixedPreparationStore) ListRunPreparationRecords(ctx context.Context, runID, endID, cursor string, limit int) (storage.SeedPage, error) {
	page, err := s.Store.ListRunPreparationRecords(ctx, runID, endID, cursor, limit)
	if err == nil && len(page.Records) > 0 {
		page.Records[0].LiteralPart = &storage.LiteralPart{Data: []byte(`[]`), Final: true}
	}
	return page, err
}

func TestPreparationRejectsMixedLiteralSuffix(t *testing.T) {
	eng := &stubEngine{}
	client, store := newPreparedRunTestClient(eng, testAgentDefinition("svc.agent", "workflow", "queue", nil, nil))
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	prepared, err := client.Prepare(t.Context(), "session-1", nil, WithRunID("run"), WithPreparation("command", "attempt"))
	require.NoError(t, err)
	client.(*agentClient).r.Store = mixedPreparationStore{Store: store}
	recovered, found, err := client.RecoverPrepared(t.Context(), "session-1", "run", "command")
	require.ErrorContains(t, err, "invalid accepted preparation record")
	require.Nil(t, recovered)
	require.False(t, found)
	_, err = client.StartPrepared(t.Context(), prepared)
	require.ErrorContains(t, err, "invalid accepted preparation record")
	require.Zero(t, eng.startCalls)
}
